/**
 * Opt-in Apple workflow canary for the browser worker runtime — NON-CI.
 *
 * This is the Definition-of-Done check for feat/browser-worker-runtime: from
 * a clean server, the supported worker command (WorkerHost) executes the
 * approved Apple iPad Air pricing rule in a real persistent Chromium profile
 * and the Admin API returns the exact 16 visible, color-deduplicated model
 * combinations — with no temporary scripts and no GM bridge.
 *
 * Prerequisites (operator-provided, never committed):
 *   - a running AegisCrawler server with FEATURE_WORKER_PROTOCOL_V2=true;
 *   - the approved immutable Apple rule version (see docs/browser-worker.md);
 *   - a persistent Chromium profile that can reach apple.com;
 *   - the server's WORKER_API_KEY in a 0600 file; the ADMIN_API_KEY value.
 *
 * Environment:
 *   AEGIS_WORKER_CANARY_APPROVAL   must equal the approval string below
 *   AEGIS_CANARY_SERVER_URL        e.g. http://127.0.0.1:8080
 *   AEGIS_CANARY_ADMIN_API_KEY     admin API key (task creation + results)
 *   AEGIS_CANARY_WORKER_API_KEY_FILE  0600 file containing the worker key
 *   AEGIS_CANARY_PROFILE           named profile to serve (e.g. current-chrome-profile)
 *   AEGIS_CANARY_PROFILES_DIR      parent dir for persistent profiles
 *   AEGIS_CANARY_RULE_ID           approved Apple rule id
 *   AEGIS_CANARY_RULE_VERSION      approved numeric rule version
 *   AEGIS_CANARY_HEADLESS          '1' for headless (default: headed)
 *
 * Run: npx ts-node scripts/worker-apple-canary.ts
 */

import { WorkerHost } from '../src/worker-host/host';
import { readApiKeyFile } from '../src/worker-host/cli';
import {
  assertAppleModelRows,
  assertAppleVisibleExtractionRule,
} from './live-workflow/apple-models';

export const WORKER_CANARY_APPROVAL = 'I approve the live Apple worker canary';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function requiredEnv(env: NodeJS.ProcessEnv, name: string): string {
  const value = (env[name] ?? '').trim();
  assert(value, `${name} is required`);
  return value;
}

async function apiJSON<T>(baseUrl: string, method: string, route: string, token: string, body?: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${method} ${route} returned ${response.status}: ${text}`);
  return text ? (JSON.parse(text) as T) : ({} as T);
}

async function main(): Promise<void> {
  const env = process.env;
  assert(env.AEGIS_WORKER_CANARY_APPROVAL === WORKER_CANARY_APPROVAL,
    `live canary approval is required (AEGIS_WORKER_CANARY_APPROVAL="${WORKER_CANARY_APPROVAL}")`);

  const baseUrl = requiredEnv(env, 'AEGIS_CANARY_SERVER_URL').replace(/\/$/, '');
  const adminKey = requiredEnv(env, 'AEGIS_CANARY_ADMIN_API_KEY');
  const workerKeyFile = requiredEnv(env, 'AEGIS_CANARY_WORKER_API_KEY_FILE');
  const profile = requiredEnv(env, 'AEGIS_CANARY_PROFILE');
  const profilesDir = requiredEnv(env, 'AEGIS_CANARY_PROFILES_DIR');
  const ruleId = requiredEnv(env, 'AEGIS_CANARY_RULE_ID');
  const ruleVersion = Number(requiredEnv(env, 'AEGIS_CANARY_RULE_VERSION'));
  assert(Number.isInteger(ruleVersion) && ruleVersion > 0, 'AEGIS_CANARY_RULE_VERSION must be a positive integer');
  const headless = env.AEGIS_CANARY_HEADLESS === '1';
  const workerApiKey = readApiKeyFile(workerKeyFile);

  // Derive the minimal input snapshot from the approved version's execution
  // contract (the Apple rule declares no required inputs; fail loudly if the
  // bound version ever grows one, since a canary must not fabricate values).
  const detail = await apiJSON<any>(
    baseUrl, 'GET',
    `/admin/rules/${encodeURIComponent(ruleId)}/versions/${ruleVersion}`,
    adminKey,
  );
  const inputSchema = detail?.contract?.inputSchema;
  assert(inputSchema, `rule ${ruleId} v${ruleVersion} has no execution contract input schema`);
  assertAppleVisibleExtractionRule(detail?.ruleVersion?.rule);
  const requiredInputs: string[] = Array.isArray(inputSchema.required) ? inputSchema.required : [];
  assert(requiredInputs.length === 0,
    `canary does not synthesize inputs; rule requires ${requiredInputs.join(', ')} — supply the task manually`);

  const created = await apiJSON<{ taskId: string }>(baseUrl, 'POST', '/admin/tasks', adminKey, {
    ruleId,
    ruleVersionNumber: ruleVersion,
    variables: {},
  });
  const taskId = created.taskId;
  assert(taskId, 'task creation returned no id');
  console.log(`[apple-canary] created task ${taskId} for ${ruleId} v${ruleVersion}`);

  const host = new WorkerHost({
    serverUrl: baseUrl,
    apiKey: workerApiKey,
    profile,
    profilesDir,
    workers: 1,
    headless,
    verbose: true,
  });
  let hostError: unknown;
  const hostRun = host.start().catch((err) => {
    hostError = err;
  });

  try {
    const deadline = Date.now() + 5 * 60_000;
    let task: any;
    while (Date.now() < deadline) {
      task = await apiJSON<any>(baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(taskId)}`, adminKey);
      if (task.status === 'done' || task.status === 'failed' || task.status === 'dead_letter') break;
      await sleep(2000);
    }
    assert(task?.status === 'done', `canary task did not complete successfully: ${JSON.stringify(task)}`);
    assert(!hostError, `worker host exited unexpectedly: ${String(hostError)}`);

    const results = await apiJSON<any>(
      baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(taskId)}/results?include_invalid=true`, adminKey,
    );
    assert((results.page?.invalidBatches?.length ?? 0) === 0,
      `canary quarantined invalid batches: ${JSON.stringify(results.page?.invalidBatches)}`);
    assert(results.page?.summary, 'canary task omitted its final execution summary');
    const rows = (results.page?.batches ?? []).flatMap((batch: any) =>
      Array.isArray(batch.payload) ? batch.payload : [batch.payload]);
    assertAppleModelRows(rows, results.outputSchema);
    console.log(`[apple-canary] task ${taskId} done: ${rows.length} exact visible model combinations, `
      + `${results.page.total} valid batch(es), summary present`);
    console.log('[apple-canary] Apple worker canary passed');
  } finally {
    await host.stop().catch(() => undefined);
    await hostRun.catch(() => undefined);
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error('[apple-canary] failed:', error);
    process.exit(1);
  });
}
