#!/usr/bin/env ts-node

import assert from 'node:assert/strict';
import { chromium, type BrowserContext, type Page } from 'playwright';
import type { HttpTransport } from '../src/worker/HttpTransport';
import { buildWorkerPageBundle } from '../src/worker-host/bundle';
import { createPageRuleExecutor } from '../src/worker-host/page-driver';
import {
  BAIDU_ENTRY_URL,
  BAIDU_TEST_KEYWORD,
  assertBaiduRowsEqual,
  baiduEnvironmentBlockForURL,
  baiduRowsHash,
  baiduSearchQueryBlockForURL,
  collectBaiduPageOracle,
  type BaiduResultRow,
} from './live-workflow/baidu-search';
import {
  loadProviderFreeBaiduArtifact,
  type ProviderFreeBaiduArtifact,
} from './live-workflow/baidu-provider-free-artifact';
import {
  BAIDU_QUALIFICATION_PROFILE,
  prepareDedicatedProfile,
  resolveDedicatedProfile,
} from './live-workflow/dedicated-profile';

const WARMUP_TIMEOUT_MS = 10 * 60_000;
const REPLAY_TIMEOUT_MS = 3 * 60_000;

const OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    title: { type: 'string' },
    summary: { type: 'string' },
  },
  required: ['title', 'summary'],
  additionalProperties: false,
};

async function waitForHumanBaiduVerification(page: Page): Promise<BaiduResultRow[]> {
  await page.goto(BAIDU_ENTRY_URL, { waitUntil: 'load', timeout: 90_000 });
  await page.bringToFront();
  console.log('\n[baidu-replay] HUMAN VERIFICATION REFRESH — NO RECORDING OR PROVIDER IS ACTIVE');
  console.log(`  - Search exactly for: ${BAIDU_TEST_KEYWORD}`);
  console.log('  - Complete any ordinary Baidu verification yourself.');
  console.log('  - Leave the browser on the visible result page; detection is automatic.');

  const deadline = Date.now() + WARMUP_TIMEOUT_MS;
  while (Date.now() < deadline) {
    const url = page.url();
    const environmentBlock = baiduEnvironmentBlockForURL(url);
    if (environmentBlock && !environmentBlock.startsWith('human authentication or verification page')) {
      throw new Error(`Baidu warm-up left the approved boundary: ${environmentBlock}`);
    }
    if (!baiduSearchQueryBlockForURL(url)) {
      try {
        return await collectBaiduPageOracle(page);
      } catch (error) {
        const currentBlock = baiduEnvironmentBlockForURL(page.url());
        if (!currentBlock.startsWith('human authentication or verification page')) throw error;
      }
    }
    await page.waitForTimeout(500);
  }
  throw new Error('Baidu warm-up timed out before the fixed visible result page was ready');
}

function createMemoryTransport(rows: unknown[], logs: string[]): HttpTransport {
  return {
    sendResult: async (payload: unknown) => { rows.push(structuredClone(payload)); },
    sendLog: async (level: string, message: string) => {
      if (logs.length < 32) logs.push(`${level}:${message}`.slice(0, 500));
    },
    sendHeartbeat: async () => ({ cancelRequested: false }),
    sendStatus: async (status: string, message?: string) => {
      if (logs.length < 32) logs.push(`status:${status}:${message ?? ''}`.slice(0, 500));
    },
    sendSnapshot: async () => undefined,
  } as unknown as HttpTransport;
}

async function runProviderFreeReplay(
  context: BrowserContext,
  artifact: ProviderFreeBaiduArtifact,
): Promise<void> {
  const bundle = await buildWorkerPageBundle();
  const rows: unknown[] = [];
  const logs: string[] = [];
  let oracle: BaiduResultRow[] | undefined;
  const executor = createPageRuleExecutor({
    context,
    bundle,
    verbose: false,
    navigationTimeoutMs: 90_000,
    onPageComplete: async (page) => { oracle = await collectBaiduPageOracle(page); },
  });
  const abort = new AbortController();
  const timeout = setTimeout(() => abort.abort(), REPLAY_TIMEOUT_MS);
  try {
    const result = await executor({
      rule: artifact.rule,
      taskId: `provider-free-baidu-${Date.now()}`,
      workerId: 'provider-free-baidu-replay',
      variables: { keyword: BAIDU_TEST_KEYWORD },
      signal: abort.signal,
      transport: createMemoryTransport(rows, logs),
    });
    assert.equal(result.status, 'success',
      `provider-free Baidu replay failed: ${result.message ?? 'no bounded message'}; logs=${logs.join('|')}`);
    assert(oracle, 'provider-free Baidu replay did not capture its contemporaneous page oracle');
    assertBaiduRowsEqual(rows, OUTPUT_SCHEMA, oracle, 'provider-free replay');
    const canonicalRows = rows as BaiduResultRow[];
    console.log(JSON.stringify({
      status: 'passed',
      providerRequests: 0,
      artifactSha256: artifact.artifactSha256,
      provisionalHash: artifact.workflow.provisionalHash,
      ruleId: artifact.rule.id,
      rows: canonicalRows.length,
      rowsHash: baiduRowsHash(canonicalRows),
      oracleRows: oracle.length,
      oracleHash: baiduRowsHash(oracle),
    }, null, 2));
  } finally {
    clearTimeout(timeout);
  }
}

async function main(): Promise<void> {
  const [artifactPath] = process.argv.slice(2);
  assert(artifactPath && process.argv.length === 3,
    'usage: npm run test:e2e:baidu-replay -- <reviewed-dsl-workflow.json>');
  const artifact = loadProviderFreeBaiduArtifact(artifactPath);
  const profile = resolveDedicatedProfile({
    env: { ...process.env, AEGIS_LIVE_REUSE_RECORDING: '1' },
    headed: true,
    humanDemoEnabled: false,
    selection: ['baidu-search'],
  });
  assert(profile?.name === BAIDU_QUALIFICATION_PROFILE, 'dedicated Baidu profile configuration is required');
  const profileDir = prepareDedicatedProfile(profile);
  console.log(`[baidu-replay] verified artifact ${artifact.artifactSha256}; provider configuration will not be loaded`);

  const context = await chromium.launchPersistentContext(profileDir, { headless: false });
  try {
    const warmup = await context.newPage();
    try {
      const rows = await waitForHumanBaiduVerification(warmup);
      console.log(`[baidu-replay] verification refreshed with ${rows.length} visible ordinary result rows`);
    } finally {
      await warmup.close().catch(() => undefined);
    }
    await runProviderFreeReplay(context, artifact);
  } finally {
    await context.close().catch(() => undefined);
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error(`[baidu-replay] failed: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  });
}
