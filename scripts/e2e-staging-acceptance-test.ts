import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { ChildProcess } from 'child_process';
import { createServer as createHttpServer, IncomingMessage, Server, ServerResponse } from 'http';
import { chromium, BrowserContext, Page } from 'playwright';
import { JSDOM } from 'jsdom';
import { Worker } from '../src/worker/Worker';
import { BrowserEnvironment } from '../src/worker/BrowserEnvironment';
import type { PageAgentRecording, Rule } from '../src/rule-generator/types';
import { BoundedRedactor } from './bounded-redaction';
import { loadLocalLLMConfig, type LocalLLMConfig } from './local-live-llm-canary';
import { buildTaskInputs, STAGE10_RESULT_TEXT } from './staging-task-inputs';
import {
  ADMIN_API_KEY,
  WORKER_API_KEY,
  buildServer,
  getFreePort,
  killServer,
  removeIfExists,
  startServer,
  waitForHealth,
  waitForTaskDone,
} from './e2e-deployment-test';

const TASK_COUNT = 20;
const RESULT_TEXT = STAGE10_RESULT_TEXT;
const MANUAL_HUMAN_APPROVAL = (() => {
  const value = process.env.AEGIS_STAGE11B_MANUAL_APPROVAL ?? '';
  if (!['', '0', '1'].includes(value)) {
    throw new Error('AEGIS_STAGE11B_MANUAL_APPROVAL must be 0 or 1');
  }
  return value === '1';
})();
const MANUAL_HUMAN_TIMEOUT_MS = 5 * 60_000;
const LIVE_LLM_WORKFLOW = (() => {
  const value = process.env.AEGIS_STAGE11B_LIVE_LLM ?? '';
  if (!['', '0', '1'].includes(value)) throw new Error('AEGIS_STAGE11B_LIVE_LLM must be 0 or 1');
  return value === '1';
})();
const LLM_WORKFLOW_TIMEOUT_MS = LIVE_LLM_WORKFLOW ? 180_000 : 60_000;
const FORBIDDEN_RECORDING_VALUES = [
  'stage10-password-value',
  'stage10-token-value',
  'stage10-inline-secret',
  'stage10.fixture@example.test',
  'stage10-cross-password',
  'stage10-query-secret',
];

interface OpenAIMessage {
  role: string;
  content: string;
}

interface OpenAIRequest {
  messages?: OpenAIMessage[];
}

interface ApprovedVersion {
  ruleId: string;
  version: number;
}

interface MCPClientState {
  token: string;
  sessionId?: string;
  nextId: number;
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function getDistinctPort(used: Set<number>): Promise<number> {
  let port = await getFreePort();
  while (used.has(port)) port = await getFreePort();
  used.add(port);
  return port;
}

async function readRequestBody(request: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    request.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
    request.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    request.on('error', reject);
  });
}

function listen(server: Server, port: number): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve());
  });
}

function closeServer(server: Server | undefined): Promise<void> {
  return new Promise((resolve) => {
    if (!server?.listening) {
      resolve();
      return;
    }
    server.close(() => resolve());
  });
}

function fixtureHTML(pagePort: number, crossOriginPort: number): string {
  return `<!doctype html>
    <html lang="en">
      <head><title>Stage 10 semantic fixture</title></head>
      <body>
        <h1>Stage 10 semantic fixture</h1>
        <input id="fixture-password" type="password" value="stage10-password-value">
        <input id="fixture-token" type="hidden" name="api_token" value="stage10-token-value">
        <p id="private-copy">stage10.fixture@example.test token=stage10-inline-secret</p>
        <p id="semantic-result">semantic action pending</p>
        <button id="semantic-action" type="button">Run semantic action</button>
        <iframe id="same-frame" src="http://127.0.0.1:${pagePort}/same-child"></iframe>
        <iframe id="cross-frame" src="http://127.0.0.1:${crossOriginPort}/cross-child?token=stage10-query-secret"></iframe>
        <iframe id="unavailable-frame" src="data:text/html,%3Cp%3Einaccessible-stage10-frame%3C%2Fp%3E"></iframe>
        <script>
          document.getElementById('semantic-action').addEventListener('click', function () {
            document.getElementById('semantic-result').textContent = '${RESULT_TEXT}';
          });
        </script>
      </body>
    </html>`;
}

async function startFixtureServers(pagePort: number, crossOriginPort: number): Promise<[Server, Server]> {
  const pageServer = createHttpServer((request, response) => {
    const pathname = new URL(request.url ?? '/', `http://127.0.0.1:${pagePort}`).pathname;
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    if (pathname === '/same-child') {
      response.end(`<!doctype html><p id="same-content">stage10 same-origin frame</p>
        <iframe id="nested-frame" src="http://127.0.0.1:${pagePort}/nested-child"></iframe>`);
      return;
    }
    if (pathname === '/nested-child') {
      response.end('<!doctype html><p id="nested-content">stage10 recursively nested frame</p>');
      return;
    }
    if (pathname === '/replay-entry') {
      response.end(`<!doctype html>
        <input id="replay-keyword" type="text">
        <a id="replay-submit" href="/replay-result">Run replay</a>`);
      return;
    }
    if (pathname === '/replay-result') {
      response.end('<!doctype html><p id="replay-result">navigation replay complete</p>');
      return;
    }
    response.end(fixtureHTML(pagePort, crossOriginPort));
  });
  const crossOriginServer = createHttpServer((_request, response) => {
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    response.end(`<!doctype html><p id="cross-content">stage10 cross-origin frame</p>
      <input type="password" value="stage10-cross-password">`);
  });
  await Promise.all([listen(pageServer, pagePort), listen(crossOriginServer, crossOriginPort)]);
  return [pageServer, crossOriginServer];
}

function requirementCandidates(): Record<string, unknown> {
  const requirement = (title: string) => ({
    title,
    description: 'Collect the visible result produced by the recorded semantic action.',
    requiredInputs: [],
    optionalInputs: [],
    outputFields: [{ name: 'result', type: 'string', description: 'Visible semantic action result' }],
    sampleOutput: { result: RESULT_TEXT },
  });
  return {
    candidates: [
      { id: 'provider-c1', confidence: 0.96, requirement: requirement('Collect semantic action result') },
      { id: 'provider-c2', confidence: 0.84, requirement: requirement('Verify recorded interaction output') },
      { id: 'provider-c3', confidence: 0.73, requirement: requirement('Monitor visible action completion') },
    ],
  };
}

function JSONBetween(text: string, marker: string, endings: string[]): Record<string, unknown> {
  const start = text.indexOf(marker);
  assert(start >= 0, `fake LLM request omitted ${marker.trim()}`);
  const valueStart = start + marker.length;
  const candidates = endings.map((ending) => text.indexOf(ending, valueStart)).filter((index) => index >= 0);
  const end = candidates.length > 0 ? Math.min(...candidates) : text.length;
  return JSON.parse(text.slice(valueStart, end).trim()) as Record<string, unknown>;
}

function generatedRule(userPrompt: string): Record<string, unknown> {
  const baseline = baselineRule(userPrompt);
  const catalog = selectorCandidateCatalog(userPrompt);
  const resultTarget = targetCandidate(catalog, '#semantic-result');
  return {
    ...baseline,
    steps: [
      { action: 'click', target: { family: 'selector', value: '#semantic-action', name: '' } },
      { action: 'extractText', name: 'result', target: { targetCandidateId: resultTarget, visible: true } },
      { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true },
    ],
  };
}

function baselineRule(userPrompt: string): Record<string, unknown> {
  return JSONBetween(userPrompt, 'Baseline provider rule:\n', [
    '\n\nDeterministic page-text-free selector candidate catalog:',
    '\n\nComplete sanitized recording:',
    '\n\nOrdered chunk analyses:',
  ]);
}

interface SelectorCandidateCatalog {
  catalogHash: string;
  candidates: Array<{
    targetCandidateId?: string;
    observedSelector: string;
  }>;
}

function selectorCandidateCatalog(userPrompt: string): SelectorCandidateCatalog {
  return JSONBetween(userPrompt, 'Deterministic page-text-free selector candidate catalog:\n', [
    '\n\nComplete sanitized recording:',
    '\n\nOrdered chunk analyses:',
    '\n\nReplay diagnostics:',
  ]) as unknown as SelectorCandidateCatalog;
}

function targetCandidate(catalog: SelectorCandidateCatalog, observedSelector: string): string {
  const candidate = catalog.candidates.find((value) =>
    value.observedSelector === observedSelector && value.targetCandidateId);
  assert(candidate?.targetCandidateId,
    `fake LLM selector catalog omitted singleton ${observedSelector}`);
  return candidate.targetCandidateId;
}

// interactionOnlyRule is parseable and structurally schema-valid but
// provisional-invalid: no successful path submits the confirmed output fields
// with sendResult, so the Go provisional validator rejects it with the
// retryable (not terminal) INVALID_DSL classification.
function interactionOnlyRule(userPrompt: string): Record<string, unknown> {
  const baseline = baselineRule(userPrompt);
  const catalog = selectorCandidateCatalog(userPrompt);
  return {
    ...baseline,
    steps: [
      {
        action: 'click',
        target: { family: 'selector', value: '#semantic-action', name: '' },
      },
      {
        action: 'extractText',
        name: 'result',
        target: { targetCandidateId: targetCandidate(catalog, '#semantic-result'), visible: true },
      },
    ],
  };
}

// Mirrors server retryFeedbackPrefix: a bounded retry prepends the previous
// attempt's server-recorded validation failure to the generation prompt.
const RETRY_FEEDBACK_MARKER = 'A previous attempt returned an invalid or unsafe structure:';
const INVALID_DSL_FEEDBACK = 'successful execution must submit the confirmed output fields with sendResult';

interface FakeLLMStats {
  generationRequests: number;
  // Feedback prefix observed at the start of each generation user prompt, in
  // arrival order; an empty string means the request carried no feedback.
  generationFeedback: string[];
}

interface FakeLLM {
  server: Server;
  requestCount: () => number;
  leaks: string[];
  dslStats: FakeLLMStats;
  hangNextRequest: () => Promise<void>;
  releaseHungRequest: () => void;
}

async function startFakeLLM(port: number): Promise<FakeLLM> {
  let requests = 0;
  const leaks: string[] = [];
  const dslStats: FakeLLMStats = { generationRequests: 0, generationFeedback: [] };
  let hangArmed = false;
  let hungRequest: Promise<void> | undefined;
  let resolveHungRequest: (() => void) | undefined;
  let releaseHung: (() => void) | undefined;
  const server = createHttpServer(async (request: IncomingMessage, response: ServerResponse) => {
    if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
      response.writeHead(404).end();
      return;
    }
    const rawBody = await readRequestBody(request);
    requests += 1;
    for (const forbidden of FORBIDDEN_RECORDING_VALUES) {
      if (rawBody.includes(forbidden)) leaks.push(forbidden);
    }
    if (leaks.length > 0) {
      response.writeHead(500, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify({ error: { message: `unsanitized values reached fake LLM: ${leaks.length} forbidden value(s)` } }));
      return;
    }
    const body = JSON.parse(rawBody) as OpenAIRequest;
    const system = body.messages?.find((message) => message.role === 'system')?.content ?? '';
    const user = body.messages?.find((message) => message.role === 'user')?.content ?? '';
    if (hangArmed) {
      hangArmed = false;
      resolveHungRequest?.();
      await new Promise<void>((resolve) => {
        releaseHung = resolve;
      });
      releaseHung = undefined;
      if (response.destroyed || response.writableEnded) return;
    }
    let content: Record<string, unknown>;
    if (system.includes('generate AegisCrawler PageAgent DSL rules')) {
      dslStats.generationRequests += 1;
      dslStats.generationFeedback.push(user.startsWith(RETRY_FEEDBACK_MARKER) ? user.split('\n\n')[0] : '');
      const catalog = selectorCandidateCatalog(user);
      if (dslStats.generationRequests === 1) {
        // First attempt: return a rule the provisional validator rejects as
        // retryable INVALID_DSL (interaction-only, no sendResult). The server
        // must retry with the recorded validation failure fed back into the
        // prompt instead of failing the workflow terminally.
        content = { selectorCatalogHash: catalog.catalogHash, rule: interactionOnlyRule(user) };
      } else {
        content = { selectorCatalogHash: catalog.catalogHash, rule: generatedRule(user) };
      }
    } else if (system.includes('repair an AegisCrawler PageAgent DSL rule')) {
      const catalog = selectorCandidateCatalog(user);
      content = {
        selectorCatalogHash: catalog.catalogHash,
        rule: JSONBetween(user, 'Current provider rule:\n', [
          '\n\nDeterministic page-text-free selector candidate catalog:',
          '\n\nReplay diagnostics:',
        ]),
      };
    } else if (system.includes('design safe web collection requirements')) {
      content = requirementCandidates();
    } else if (system.includes('normalize a user')) {
      content = { requirement: (requirementCandidates().candidates as Array<Record<string, any>>)[0].requirement };
    } else if (system.includes('网页数据采集意图分析专家')) {
      content = {
        candidates: [
          {
            id: 'stage10-intent-1',
            label: '采集页面结果',
            description: '采集录制页面中可见的结果文本。',
            confidence: 0.5,
            suggestedVariables: [],
          },
          {
            id: 'stage10-intent-2',
            label: '验证页面状态',
            description: '验证录制操作完成后的页面状态。',
            confidence: 0.3,
            suggestedVariables: [],
          },
          {
            id: 'stage10-intent-3',
            label: '监控结果变化',
            description: '监控录制操作产生的结果变化。',
            confidence: 0.2,
            suggestedVariables: [],
          },
        ],
      };
    } else {
      content = { summary: 'stage10 fixture analyzed', observedInputs: [], observedOutputs: ['result'], safetyFlags: [] };
    }
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({
      choices: [{ message: { content: JSON.stringify(content) } }],
      usage: { prompt_tokens: 100, completion_tokens: 50 },
    }));
  });
  await listen(server, port);
  return {
    server,
    requestCount: () => requests,
    leaks,
    dslStats,
    hangNextRequest: () => {
      assert(!hangArmed && !releaseHung, 'fake provider already has a hanging request');
      hangArmed = true;
      hungRequest = new Promise<void>((resolve) => {
        resolveHungRequest = resolve;
      });
      return hungRequest;
    },
    releaseHungRequest: () => {
      hangArmed = false;
      releaseHung?.();
      resolveHungRequest?.();
      releaseHung = undefined;
      resolveHungRequest = undefined;
      hungRequest = undefined;
    },
  };
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
  return text ? JSON.parse(text) as T : {} as T;
}

async function adminResponse(
  baseUrl: string,
  method: string,
  route: string,
  body?: unknown,
): Promise<{ response: Response; text: string; json: any }> {
  const response = await fetch(`${baseUrl}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${ADMIN_API_KEY}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  let json: any;
  try {
    json = text ? JSON.parse(text) : {};
  } catch {
    json = undefined;
  }
  return { response, text, json };
}

async function scrapeMetrics(baseUrl: string): Promise<string> {
  const response = await fetch(`${baseUrl}/metrics`);
  const text = await response.text();
  assert(response.ok, `metrics scrape returned ${response.status}: ${text}`);
  return text;
}

function metricValue(metrics: string, name: string, labels: Record<string, string> = {}): number {
  for (const line of metrics.split('\n')) {
    if (!line.startsWith(name)) continue;
    if (!Object.entries(labels).every(([key, value]) => line.includes(`${key}="${value}"`))) continue;
    const raw = line.trim().split(/\s+/).at(-1);
    const value = Number(raw);
    if (Number.isFinite(value)) return value;
  }
  throw new Error(`metrics scrape omitted ${name} ${JSON.stringify(labels)}`);
}

function copySQLiteDatabase(source: string, destination: string): void {
  for (const suffix of ['', '-wal', '-shm']) {
    const sourcePath = `${source}${suffix}`;
    if (fs.existsSync(sourcePath)) fs.copyFileSync(sourcePath, `${destination}${suffix}`);
  }
}

async function killServerImmediately(process: ChildProcess): Promise<void> {
  if (process.exitCode !== null || process.signalCode !== null) return;
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('server did not exit after SIGKILL')), 5000);
    process.once('exit', () => {
      clearTimeout(timeout);
      resolve();
    });
    process.kill('SIGKILL');
  });
}

function intentFixture(title: string): Record<string, unknown> {
  return {
    meta: { title, url: 'http://127.0.0.1/budget-fixture' },
    events: [{ type: 'click', target: '#collect' }],
    snapshots: [],
  };
}

async function predictIntent(
  baseUrl: string,
  title: string,
): Promise<{ response: Response; text: string; json: any }> {
  return adminResponse(baseUrl, 'POST', '/admin/rules/predict-intent', {
    recording: intentFixture(title),
  });
}

function clearLegacyLLMEnvironment(): Record<string, string> {
  const cleared: Record<string, string> = {
    LLM_PROVIDER_CONFIGS: '',
    LLM_PROVIDER: '',
    LLM_API_KEY: '',
    LLM_BASE_URL: '',
    LLM_MODEL: '',
    LLM_TEMPERATURE: '',
    LLM_REQUEST_TIMEOUT: '',
    LLM_MAX_INPUT_TOKENS: '',
    LLM_MAX_OUTPUT_TOKENS: '',
    LLM_OPENAI_ENABLE_THINKING: '',
    LLM_OPENAI_STRICT_TOOL_OUTPUT: '',
    LLM_JOB_MAX_ATTEMPTS: '',
    LLM_DSL_SELECTOR_REPAIR_ENABLED: '',
    LLM_DAILY_COST_BUDGET: '',
    LLM_ALLOW_DEGRADED_FALLBACK: 'false',
    LLM_TEMPERATURE_COMPATIBILITY_RETRY: 'false',
    LLM_MAX_RETRIES: '0',
  };
  for (const suffix of [
    'PROVIDER',
    'ADAPTER',
    'MODEL',
    'BASE_URL',
    'API_KEY',
    'REQUEST_TIMEOUT',
    'TEMPERATURE',
    'STRICT_TOOL_OUTPUT',
    'ENABLE_THINKING',
    'INPUT_USD_PER_MILLION',
    'CACHED_INPUT_USD_PER_MILLION',
    'OUTPUT_USD_PER_MILLION',
    'MAX_INPUT_TOKENS',
    'MAX_OUTPUT_TOKENS',
    'OUTPUT_CAP_DIALECT',
    'PRICE_REVISION',
  ]) {
    cleared[`LLM_FALLBACK_${suffix}`] = '';
  }
  return cleared;
}

function enforcedLLMEnvironment(
  fakeLLMPort: number,
  limits: {
    maxInputTokens: string;
    maxOutputTokens: string;
    inputUSDPerMillion: string;
    outputUSDPerMillion: string;
    maxRequestUSD: string;
    dailyBudgetUSD: string;
    cacheTTL: string;
  },
): Record<string, string> {
  return {
    ...clearLegacyLLMEnvironment(),
    LLM_ENABLED: 'true',
    LLM_POLICY_MODE: 'enforced',
    LLM_PRIMARY_PROVIDER: 'stage10-loopback',
    LLM_PRIMARY_ADAPTER: 'openai',
    LLM_PRIMARY_MODEL: 'stage10-fake-model',
    LLM_PRIMARY_BASE_URL: `http://127.0.0.1:${fakeLLMPort}/v1`,
    LLM_PRIMARY_API_KEY: 'stage10-local-fake-key',
    LLM_PRIMARY_REQUEST_TIMEOUT: '10s',
    LLM_PRIMARY_TEMPERATURE: '0',
    LLM_PRIMARY_STRICT_TOOL_OUTPUT: 'false',
    LLM_PRIMARY_ENABLE_THINKING: 'false',
    LLM_PRIMARY_INPUT_USD_PER_MILLION: limits.inputUSDPerMillion,
    LLM_PRIMARY_CACHED_INPUT_USD_PER_MILLION: '',
    LLM_PRIMARY_OUTPUT_USD_PER_MILLION: limits.outputUSDPerMillion,
    LLM_PRIMARY_MAX_INPUT_TOKENS: limits.maxInputTokens,
    LLM_PRIMARY_MAX_OUTPUT_TOKENS: limits.maxOutputTokens,
    LLM_PRIMARY_OUTPUT_CAP_DIALECT: 'max_tokens',
    LLM_PRIMARY_PRICE_REVISION: 'stage10-loopback-v1',
    LLM_GLOBAL_MAX_REQUEST_USD: limits.maxRequestUSD,
    LLM_GLOBAL_DAILY_BUDGET_USD: limits.dailyBudgetUSD,
    LLM_WORKSPACE_BUDGETS_JSON: JSON.stringify({
      default: {
        maxRequestUSD: limits.maxRequestUSD,
        dailyBudgetUSD: limits.dailyBudgetUSD,
      },
    }),
    LLM_REQUIREMENT_MAX_ATTEMPTS: '2',
    LLM_DSL_GENERATION_MAX_ATTEMPTS: '3',
    LLM_DSL_MAX_REPAIRS: '1',
    LLM_SELECTOR_MAX_REPAIRS: '1',
    LLM_ENABLE_REFLECTION: 'false',
    LLM_CACHE_TTL: limits.cacheTTL,
  };
}

async function assertEnforcedControlPlane(baseUrl: string): Promise<any> {
  const ready = await fetch(`${baseUrl}/health?ready=1`);
  const readyText = await ready.text();
  assert(ready.status === 200 && readyText.includes('"status":"ok"'),
    `enforced LLM readiness returned ${ready.status}: ${readyText}`);

  const policy = await adminResponse(baseUrl, 'GET', '/admin/llm/policy');
  assert(policy.response.ok, `LLM policy endpoint returned ${policy.response.status}: ${policy.text}`);
  assert(policy.json?.mode === 'enforced' && policy.json?.enabled === true,
    `LLM policy did not report enforced enabled mode: ${policy.text}`);
  assert(policy.json?.budgetLedgerBound === true && policy.json?.reconciled === true,
    `LLM policy did not report a reconciled ledger: ${policy.text}`);
  assert(policy.json?.primary?.provider === 'stage10-loopback'
    && policy.json?.primary?.priceRevision === 'stage10-loopback-v1',
  `LLM policy did not report the bound loopback route: ${policy.text}`);
  assert(policy.json?.primary?.productionEligible === false && policy.json?.productionEligible === false,
    'a loopback qualification route was incorrectly marked production eligible');
  assert(!policy.text.includes('stage10-local-fake-key'), 'LLM policy endpoint exposed its synthetic credential');
  return policy.json;
}

async function runHardBudgetQualification(
  serverDir: string,
  fakeLLMPort: number,
  fakeLLM: FakeLLM,
  usedPorts: Set<number>,
  stamp: string,
): Promise<void> {
  // The production adapters conservatively use the exact UTF-8 request-body
  // byte length as an input-token upper bound. Keep this fixture above the
  // complete intent envelope rather than the fake provider's reported usage.
  // The daily cap is one trusted 150-token settlement plus one full 4,146-token
  // crash reservation and its 1-nano split-rounding headroom, so recovery
  // reaches the cap exactly.
  const maxRequestUSD = '0.004146001';
  const dailyBudgetUSD = '0.004296001';
  const trustedCostUSD = '0.00015';
  const serverPort = await getDistinctPort(usedPorts);
  const baseUrl = `http://127.0.0.1:${serverPort}`;
  const dbPath = path.join(os.tmpdir(), `aegis-stage10-budget-${stamp}.db`);
  const restoredDBPath = path.join(os.tmpdir(), `aegis-stage10-budget-restored-${stamp}.db`);
  const env = {
    ...serverEnvironmentBase(),
    ...enforcedLLMEnvironment(fakeLLMPort, {
      maxInputTokens: '4096',
      maxOutputTokens: '50',
      inputUSDPerMillion: '1',
      outputUSDPerMillion: '1',
      maxRequestUSD,
      dailyBudgetUSD,
      cacheTTL: '1h',
    }),
  };
  let process: ChildProcess | undefined;
  let hangingRequest: Promise<unknown> | undefined;
  const providerCallsBefore = fakeLLM.requestCount();

  try {
    process = startServer(serverDir, dbPath, serverPort, env);
    await waitForHealth(baseUrl);
    await assertEnforcedControlPlane(baseUrl);

    const emptyBudget = await adminResponse(baseUrl, 'GET', '/admin/llm/budget');
    assert(emptyBudget.response.ok, `empty budget endpoint returned ${emptyBudget.response.status}: ${emptyBudget.text}`);
    assert(emptyBudget.json?.global?.settledUsd === '0'
      && emptyBudget.json?.global?.activeReservedUsd === '0'
      && emptyBudget.json?.global?.remainingUsd === dailyBudgetUSD,
    `unexpected initial budget snapshot: ${emptyBudget.text}`);

    const first = await predictIntent(baseUrl, 'hard-budget-cache-fixture');
    assert(first.response.ok && first.json?.cacheHit === false,
      `first budgeted completion failed: ${first.response.status} ${first.text}`);
    const afterFirst = await adminResponse(baseUrl, 'GET', '/admin/llm/budget');
    assert(afterFirst.json?.global?.settledUsd === trustedCostUSD
      && afterFirst.json?.global?.activeReservedUsd === '0'
      && afterFirst.json?.global?.remainingUsd === maxRequestUSD,
    `trusted usage did not settle exactly: ${afterFirst.text}`);

    const cached = await predictIntent(baseUrl, 'hard-budget-cache-fixture');
    assert(cached.response.ok && cached.json?.cacheHit === true,
      `identical completion was not served from cache: ${cached.response.status} ${cached.text}`);
    const afterCache = await adminResponse(baseUrl, 'GET', '/admin/llm/budget');
    assert(JSON.stringify(afterCache.json) === JSON.stringify(afterFirst.json),
      `cache hit changed the hard-budget snapshot: before=${afterFirst.text} after=${afterCache.text}`);
    assert(fakeLLM.requestCount() === providerCallsBefore + 1,
      'cache hit crossed the fake provider boundary');

    const beforeCrashMetrics = await scrapeMetrics(baseUrl);
    assert(metricValue(beforeCrashMetrics, 'opencrawler_llm_physical_calls_total', {
      route: 'primary',
      outcome: 'success',
    }) >= 1, 'physical-call success metric remained zero');
    assert(metricValue(beforeCrashMetrics, 'opencrawler_llm_cache_hits_total', {
      provider: 'stage10-loopback',
      model: 'stage10-fake-model',
    }) >= 1, 'cache-hit metric remained zero');

    const hung = fakeLLM.hangNextRequest();
    const crashRequest = predictIntent(baseUrl, 'possibly-dispatched-crash-fixture');
    hangingRequest = crashRequest.catch(() => undefined);
    const crashBoundary = await Promise.race([
      hung.then(() => ({ kind: 'provider' as const })),
      crashRequest.then((result) => ({ kind: 'response' as const, result })),
      sleep(5000).then(() => ({ kind: 'timeout' as const })),
    ]);
    if (crashBoundary.kind === 'response') {
      throw new Error(
        `crash-state request returned before the provider boundary: `
        + `${crashBoundary.result.response.status} ${crashBoundary.result.text}`,
      );
    }
    assert(crashBoundary.kind === 'provider', 'fake provider did not receive the crash-state request');
    const activeBudget = await adminResponse(baseUrl, 'GET', '/admin/llm/budget');
    assert(activeBudget.json?.global?.settledUsd === trustedCostUSD
      && activeBudget.json?.global?.activeReservedUsd === maxRequestUSD
      && activeBudget.json?.global?.remainingUsd === '0',
    `possibly-dispatched reservation was not durably counted: ${activeBudget.text}`);

    await killServerImmediately(process);
    process = undefined;
    fakeLLM.releaseHungRequest();
    await hangingRequest;
    hangingRequest = undefined;
    copySQLiteDatabase(dbPath, restoredDBPath);

    const providerCallsAtCrash = fakeLLM.requestCount();
    process = startServer(serverDir, restoredDBPath, serverPort, env);
    await waitForHealth(baseUrl);
    await assertEnforcedControlPlane(baseUrl);
    const recovered = await adminResponse(baseUrl, 'GET', '/admin/llm/budget');
    assert(recovered.json?.global?.settledUsd === dailyBudgetUSD
      && recovered.json?.global?.activeReservedUsd === '0'
      && recovered.json?.global?.remainingUsd === '0'
      && recovered.json?.global?.uncertainCalls === 1,
    `startup recovery did not conservatively settle the crash state: ${recovered.text}`);
    assert(fakeLLM.requestCount() === providerCallsAtCrash,
      'startup recovery redispatched the possibly-dispatched call');

    const restoredCache = await predictIntent(baseUrl, 'hard-budget-cache-fixture');
    assert(restoredCache.response.ok && restoredCache.json?.cacheHit === true,
      `restored cache hit was blocked by the exhausted budget: ${restoredCache.response.status} ${restoredCache.text}`);
    const afterRestoredCache = await adminResponse(baseUrl, 'GET', '/admin/llm/budget');
    assert(JSON.stringify(afterRestoredCache.json) === JSON.stringify(recovered.json),
      'a restored cache hit changed recovered budget state');

    const denied = await predictIntent(baseUrl, 'hard-budget-denial-fixture');
    assert(denied.response.status === 429 && denied.json?.code === 'LLM_BUDGET_EXCEEDED',
      `exhausted hard budget did not fail closed: ${denied.response.status} ${denied.text}`);
    assert(fakeLLM.requestCount() === providerCallsAtCrash,
      'cap-denied request crossed the fake provider boundary');

    const recoveredMetrics = await scrapeMetrics(baseUrl);
    assert(metricValue(recoveredMetrics, 'opencrawler_llm_budget_settled_usd_nanos') === 4296001,
      'restored budget-settled gauge did not publish the ledger total');
    assert(metricValue(recoveredMetrics, 'opencrawler_llm_budget_rejections_total', {
      scope: 'global',
      limit: 'daily',
    }) >= 1, 'daily cap-rejection metric remained zero');
    assert(metricValue(recoveredMetrics, 'opencrawler_llm_cache_hits_total', {
      provider: 'stage10-loopback',
      model: 'stage10-fake-model',
    }) >= 1, 'restored cache-hit metric remained zero');
    console.log('[stage10] enforced hard budget passed cache-zero, crash recovery, cap denial, metrics, and no-redispatch checks');
  } finally {
    fakeLLM.releaseHungRequest();
    if (hangingRequest) await hangingRequest.catch(() => undefined);
    if (process) await killServer(process);
    for (const file of [
      dbPath, `${dbPath}-wal`, `${dbPath}-shm`,
      restoredDBPath, `${restoredDBPath}-wal`, `${restoredDBPath}-shm`,
    ]) {
      removeIfExists(file);
    }
  }
}

async function extensionPopup(context: BrowserContext, extensionId: string, baseUrl: string): Promise<Page> {
  const popup = await context.newPage();
  await popup.goto(`chrome-extension://${extensionId}/popup.html`);
  await popup.waitForSelector('#start-recording');
  await popup.evaluate((config) => {
    (document.getElementById('baseUrl') as HTMLInputElement).value = config.baseUrl;
    (document.getElementById('apiKey') as HTMLInputElement).value = config.workerKey;
    (document.getElementById('adminApiKey') as HTMLInputElement).value = config.adminKey;
    (document.getElementById('save-config') as HTMLButtonElement).click();
  }, { baseUrl, workerKey: WORKER_API_KEY, adminKey: ADMIN_API_KEY });
  await sleep(300);
  return popup;
}

async function extensionMessage(
  popup: Page,
  target: Page,
  action: 'START_RECORDING' | 'STOP_RECORDING',
): Promise<Record<string, any>> {
  await target.bringToFront();
  return popup.evaluate(async (requestedAction) => {
    return (globalThis as any).chrome.runtime.sendMessage({ action: requestedAction });
  }, action) as Promise<Record<string, any>>;
}

async function assertNavigationReplayTabRetention(
  context: BrowserContext,
  popup: Page,
  pagePort: number,
): Promise<void> {
  const entry = `http://127.0.0.1:${pagePort}/replay-entry`;
  const rule = {
    id: 'stage10-replay-retention',
    version: '1.0.0',
    name: 'Stage 10 replay retention',
    domain: '127.0.0.1',
    entry,
    variables: { keyword: '' },
    steps: [
      { action: 'type', target: { selector: '#replay-keyword', visible: true }, value: '{{keyword}}' },
      { action: 'click', target: { selector: '#replay-submit', visible: true } },
      { action: 'waitForElementVisible', target: { selector: '#replay-result', visible: true } },
      { action: 'extractText', name: 'result', target: { selector: '#replay-result', visible: true } },
      { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true },
    ],
  };
  const start = await popup.evaluate(async ({ replayRule }) =>
    (globalThis as any).chrome.runtime.sendMessage({
      action: 'START_REPLAY',
      payload: { rule: replayRule, variables: { keyword: 'stage10-retained' } },
    }), { replayRule: rule }) as { success?: boolean; taskId?: string; error?: string };
  assert(start.success === true && typeof start.taskId === 'string',
    `navigation replay did not start: ${JSON.stringify(start)}`);
  const taskId = start.taskId;
  const completionDeadline = Date.now() + LLM_WORKFLOW_TIMEOUT_MS;
  let retained: { tabId?: number; taskId?: string } | undefined;
  while (Date.now() < completionDeadline) {
    const state = await popup.evaluate(async () => {
      const value = await (globalThis as any).chrome.storage.session.get([
        'oc_replay_retained_tab',
        'oc_replay_session',
      ]);
      return {
        retained: value.oc_replay_retained_tab,
        session: value.oc_replay_session,
      };
    }) as {
      retained?: { tabId?: number; taskId?: string };
      session?: { taskId?: string };
    };
    retained = state.retained;
    if (retained?.taskId === taskId && Number.isInteger(retained.tabId)) break;
    assert(state.session?.taskId === taskId,
      `navigation replay session ended without retaining a successful tab: ${JSON.stringify(state)}`);
    await sleep(100);
  }
  assert(retained?.taskId === taskId && Number.isInteger(retained.tabId),
    `navigation replay did not retain its successful tab within ${LLM_WORKFLOW_TIMEOUT_MS}ms`);
  const target = context.pages().find((page) => {
    try {
      const url = new URL(page.url());
      return url.hostname === '127.0.0.1'
        && url.port === String(pagePort)
        && url.pathname === '/replay-result';
    } catch {
      return false;
    }
  });
  assert(target, 'successful navigation replay tab disappeared before confirmation');
  assert(await target.locator('#replay-result').textContent() === 'navigation replay complete',
    'retained navigation replay page did not preserve its result DOM');

  const closePromise = target.waitForEvent('close', { timeout: 10_000 });
  const aborted = await popup.evaluate(async () =>
    (globalThis as any).chrome.runtime.sendMessage({ action: 'ABORT_REPLAY' })) as { success?: boolean };
  assert(aborted.success === true, 'retained navigation replay could not be closed through ABORT_REPLAY');
  await closePromise;
  console.log('[stage10] navigation replay tab remained observable until explicit cleanup');
}

async function recordFixture(popup: Page, page: Page, fixtureUrl: string): Promise<PageAgentRecording> {
  await page.goto(fixtureUrl);
  await page.locator('#semantic-action').waitFor();
  await page.frameLocator('#same-frame').locator('#same-content').waitFor();
  await page.frameLocator('#same-frame').frameLocator('#nested-frame').locator('#nested-content').waitFor();
  await page.frameLocator('#cross-frame').locator('#cross-content').waitFor();
  const started = await extensionMessage(popup, page, 'START_RECORDING');
  assert(started.success === true && started.protocolVersion === '2.0.0', `recording start failed: ${JSON.stringify(started)}`);
  await page.click('#semantic-action');
  await page.locator('#semantic-result', { hasText: RESULT_TEXT }).waitFor();
  const stopped = await extensionMessage(popup, page, 'STOP_RECORDING');
  assert(stopped.success === true, `recording stop failed: ${JSON.stringify(stopped)}`);
  let recording = stopped.recording as PageAgentRecording | undefined;
  const deadline = Date.now() + 15_000;
  while (!recording?.meta.serverRecordingId && Date.now() < deadline) {
    const result = await popup.evaluate(async () => (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_LAST_RECORDING' })) as { recording?: PageAgentRecording };
    recording = result.recording;
    await sleep(200);
  }
  assert(recording?.meta.serverRecordingId, 'recording was not durably persisted before LLM work');
  assert(JSON.stringify(recording.snapshots.map((snapshot) => snapshot.phase)) === JSON.stringify(['initial', 'before-action', 'final']),
    `recording snapshot phases were incomplete: ${JSON.stringify(recording.snapshots.map((snapshot) => snapshot.phase))}`);
  const serialized = JSON.stringify(recording);
  for (const [index, forbidden] of FORBIDDEN_RECORDING_VALUES.entries()) {
    assert(!serialized.includes(forbidden), `extension recording leaked forbidden fixture value #${index + 1}`);
  }
  for (const expected of ['stage10 same-origin frame', 'stage10 recursively nested frame', 'stage10 cross-origin frame']) {
    assert(serialized.includes(expected), `recording omitted ${expected}`);
  }
  assert(serialized.includes('unavailable') || serialized.includes('error'), 'recording omitted inaccessible iframe marker');
  return recording;
}

// The wizard must render exactly three candidate cards from the completed
// candidates job, each exposing a non-empty title, description, and a
// rendered confidence value before the user selects one.
async function assertThreeCandidateCards(intent: Page): Promise<void> {
  const cards = intent.locator('.candidate-card.requirement-candidate');
  assert(await cards.count() === 3, `intent page did not render exactly three candidate cards (found ${await cards.count()})`);
  for (let index = 0; index < 3; index += 1) {
    const card = cards.nth(index);
    const title = ((await card.locator('strong').first().textContent()) ?? '').trim();
    const description = ((await card.locator('.candidate-description').textContent()) ?? '').trim();
    const confidence = ((await card.locator('.confidence').textContent()) ?? '').trim();
    assert(title.length > 0, `candidate card ${index + 1} rendered an empty title`);
    assert(description.length > 0, `candidate card ${index + 1} rendered an empty description`);
    assert(/^\d{1,3}%$/.test(confidence), `candidate card ${index + 1} rendered an invalid confidence: ${confidence || 'empty'}`);
  }
}

async function approveRuleVersion(context: BrowserContext, extensionId: string): Promise<ApprovedVersion> {
  for (const page of context.pages()) {
    if (page.url().includes('/intent/intent-page.html')) await page.close().catch(() => undefined);
  }
  const intent = await context.newPage();
  await intent.goto(`chrome-extension://${extensionId}/intent/intent-page.html`);
  // Candidate generation is on demand: no job exists yet in this browser
  // session, so the intent-step primary starts it explicitly.
  await intent.locator('#action-primary').click();
  await intent.waitForSelector('input[name="requirement-candidate"]', { timeout: LLM_WORKFLOW_TIMEOUT_MS });
  assert(await intent.locator('input[name="requirement-candidate"]').count() === 3, 'workflow did not present exactly three candidates');
  await assertThreeCandidateCards(intent);
  await intent.locator('input[name="requirement-candidate"]').first().check();
  await intent.locator('#action-primary').click();
  await intent.waitForSelector('#step-requirement:not(.hidden)');

  await intent.locator('#action-primary').click();
  await intent.waitForFunction(
    () => document.querySelector('#action-primary')?.textContent?.includes('确认此采集需求'),
    undefined,
    { timeout: LLM_WORKFLOW_TIMEOUT_MS },
  );
  await intent.locator('#action-primary').click();
  await intent.waitForSelector('#step-requirement-confirmed:not(.hidden)', { timeout: 30_000 });
  await intent.locator('#browser-profile-id').fill('stage10-chromium-profile');
  await intent.locator('#action-primary').click();
  try {
    await intent.waitForSelector('#step-preview:not(.hidden)', { timeout: LLM_WORKFLOW_TIMEOUT_MS });
  } catch {
    const visibleStep = await intent.locator('.step:not(.hidden)').first().getAttribute('id').catch(() => null);
    const visibleText = ((await intent.locator('body').innerText().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 1200);
    throw new Error(
      `provisional DSL preview did not become ready within ${LLM_WORKFLOW_TIMEOUT_MS}ms; `
      + `step=${visibleStep ?? 'unknown'}; page=${visibleText || 'unavailable'}`,
    );
  }
  const preview = (await intent.locator('#yaml-preview').textContent()) ?? '';
  if (LIVE_LLM_WORKFLOW) {
    assert(preview?.trim(), 'live provider returned an empty provisional DSL');
    if (!preview.includes('sendResult')) {
      console.log('[stage11b] provisional live DSL omitted sendResult; replay validation must repair or reject it');
    }
  } else {
    assert(preview?.includes('extractText') && preview.includes('sendResult'), `provisional DSL omitted result collection: ${preview}`);
  }

  await intent.locator('#action-primary').click();
  await intent.waitForSelector('#step-replay:not(.hidden)');
  try {
    await intent.waitForFunction(() => {
      const status = document.querySelector('.replay-status');
      const confirm = document.querySelector<HTMLButtonElement>('#action-primary');
      return status?.textContent?.includes('回放成功') && confirm?.disabled === false;
    }, undefined, { timeout: LLM_WORKFLOW_TIMEOUT_MS });
  } catch {
    const replayStatus = ((await intent.locator('.replay-status').textContent().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 160);
    const replayError = ((await intent.locator('.replay-error').textContent().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 320);
    const replayLogs = ((await intent.locator('.replay-logs').textContent().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 1200);
    const provisional = preview.replace(/\s+/g, ' ').trim().slice(0, 2000);
    throw new Error(
      `replay/repair did not succeed within ${LLM_WORKFLOW_TIMEOUT_MS}ms; `
      + `status=${replayStatus || 'unavailable'}; error=${replayError || 'unavailable'}; `
      + `logs=${replayLogs || 'unavailable'}; provisional=${provisional || 'unavailable'}`,
    );
  }
  await intent.locator('#action-primary').click();
  await intent.waitForSelector('#step-save:not(.hidden)', { timeout: 30_000 });
  const saved = (await intent.locator('#save-result').textContent()) ?? '';
  const match = saved.match(/规则版本已批准：(.+) v(\d+)/);
  assert(match, `could not parse approved immutable rule version: ${saved}`);
  await intent.close();
  return { ruleId: match[1], version: Number(match[2]) };
}

function installJSDOMGlobals(dom: JSDOM): void {
  const global = globalThis as any;
  global.window = dom.window;
  global.document = dom.window.document;
  global.location = dom.window.location;
  // Do not replace Node's Event implementation: Node 20 fetch/AbortSignal
  // depends on it. This rule only needs the DOM-specific MouseEvent class.
  global.MouseEvent = dom.window.MouseEvent;
}

async function loadApprovedTaskInputs(baseUrl: string, approved: ApprovedVersion): Promise<Record<string, unknown>> {
  const detail = await apiJSON<any>(
    baseUrl, 'GET',
    `/admin/rules/${encodeURIComponent(approved.ruleId)}/versions/${approved.version}`,
    ADMIN_API_KEY,
  );
  assert(detail?.contract?.inputSchema,
    `approved rule ${approved.ruleId} v${approved.version} has no execution contract input schema`);
  return buildTaskInputs(detail.contract.inputSchema);
}

async function executeConcurrentTasks(
  baseUrl: string,
  approved: ApprovedVersion,
  fixtureUrl: string,
): Promise<{ taskIds: string[]; maxConcurrentClicks: number }> {
  const variables = await loadApprovedTaskInputs(baseUrl, approved);
  const taskIds = await Promise.all(Array.from({ length: TASK_COUNT }, async (_, index) => {
    const response = await apiJSON<{ taskId: string }>(baseUrl, 'POST', '/admin/tasks', ADMIN_API_KEY, {
      ruleId: approved.ruleId,
      ruleVersionNumber: approved.version,
      variables,
      priority: index % 2 === 0 ? 'normal' : 'high',
    });
    return response.taskId;
  }));
  assert(new Set(taskIds).size === TASK_COUNT, 'concurrent task creation returned duplicate IDs');

  const dom = new JSDOM(`<!doctype html><button id="semantic-action">Run semantic action</button>
    <p id="semantic-result">semantic action pending</p>`, { url: fixtureUrl });
  installJSDOMGlobals(dom);
  // JSDOM has no layout engine and reports zero-sized rectangles for every
  // element. Give the intentionally visible result a layout box so the
  // generated target's visible:true contract is exercised faithfully.
  dom.window.document.getElementById('semantic-result')!.getBoundingClientRect = () =>
    new dom.window.DOMRect(0, 0, 160, 24);
  let activeClicks = 0;
  let maxConcurrentClicks = 0;
  const button = dom.window.document.getElementById('semantic-action')!;
  button.addEventListener('mousedown', () => {
    activeClicks += 1;
    maxConcurrentClicks = Math.max(maxConcurrentClicks, activeClicks);
  });
  button.addEventListener('mouseup', () => { activeClicks -= 1; });
  button.addEventListener('click', () => {
    dom.window.document.getElementById('semantic-result')!.textContent = RESULT_TEXT;
  });

  const workers = Array.from({ length: TASK_COUNT }, (_, index) => new Worker({
    baseUrl,
    workerId: `stage10-worker-${index + 1}`,
    browserProfileId: 'stage10-chromium-profile',
    apiKey: WORKER_API_KEY,
    pollIntervalMs: 100,
    heartbeatIntervalMs: 30_000,
    claimBackoffMs: 50,
    maxClaimBackoffMs: 250,
    serverErrorBackoffMs: 20,
    envFactory: (transport) => new BrowserEnvironment(transport, dom.window as unknown as Window),
  }));
  const workerRuns = workers.map((worker) => worker.start());
  try {
    await Promise.all(taskIds.map((taskId) => waitForTaskDone(baseUrl, taskId, 60_000)));
  } finally {
    workers.forEach((worker) => worker.stop());
    await Promise.all(workerRuns);
  }
  assert(maxConcurrentClicks >= 2, `20-worker acceptance run did not overlap execution (max=${maxConcurrentClicks})`);

  for (const taskId of taskIds) {
    const results = await apiJSON<any>(baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(taskId)}/results?include_invalid=true`, ADMIN_API_KEY);
    assert(results.ruleVersionNumber === approved.version, `task ${taskId} lost immutable version lineage`);
    assert(results.page?.total === 1 && results.page?.batches?.length === 1, `task ${taskId} did not persist one valid batch`);
    assert((results.page?.invalidBatches?.length ?? 0) === 0, `task ${taskId} quarantined an internal completion marker`);
    assert(results.page?.summary, `task ${taskId} omitted its final execution summary`);
    assert(JSON.stringify(results.page.batches[0].payload).includes(RESULT_TEXT), `task ${taskId} returned unexpected data`);
  }
  dom.window.close();
  return { taskIds, maxConcurrentClicks };
}

async function executeHumanCheckpointTask(
  baseUrl: string,
  fixtureUrl: string,
  operatorContext?: BrowserContext,
): Promise<void> {
  const ruleId = 'stage11b-human-checkpoint';
  const version = await apiJSON<any>(baseUrl, 'POST', `/admin/rules/${ruleId}/versions`, ADMIN_API_KEY, {
    rule: {
      id: ruleId,
      version: '1.0.0',
      name: 'Stage 11B resumable human checkpoint',
      domain: '127.0.0.1',
      enabled: true,
      priority: 'normal',
      variables: {},
      steps: [
        { action: 'extractText', name: 'result', target: { selector: '#checkpoint-result' } },
        {
          action: 'requestHuman',
          id: 'approve-safe-resume',
          type: 'confirmation',
          prompt: 'Confirm the fixture is safe to resume',
          timeout: operatorContext ? MANUAL_HUMAN_TIMEOUT_MS : 30_000,
          then: [{ action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true }],
        },
      ],
      output: {
        type: 'object',
        properties: { result: { type: 'string' } },
        required: ['result'],
        additionalProperties: false,
      },
    },
  });
  const versionNumber = version.ruleVersion?.version;
  assert(Number.isInteger(versionNumber), `human checkpoint rule version was not created: ${JSON.stringify(version)}`);
  await apiJSON(baseUrl, 'POST', `/admin/rules/${ruleId}/versions/${versionNumber}/approve`, ADMIN_API_KEY);
  const created = await apiJSON<{ taskId: string }>(baseUrl, 'POST', '/admin/tasks', ADMIN_API_KEY, {
    ruleId,
    ruleVersionNumber: versionNumber,
    variables: {},
  });

  const dom = new JSDOM('<!doctype html><p id="checkpoint-result">operator checkpoint approved</p>', { url: fixtureUrl });
  installJSDOMGlobals(dom);
  const worker = new Worker({
    baseUrl,
    workerId: 'stage11b-human-worker',
    browserProfileId: 'stage11b-human-profile',
    apiKey: WORKER_API_KEY,
    pollIntervalMs: 100,
    heartbeatIntervalMs: 250,
    claimBackoffMs: 50,
    maxClaimBackoffMs: 250,
    serverErrorBackoffMs: 20,
    envFactory: (transport) => new BrowserEnvironment(transport, dom.window as unknown as Window),
  });
  const workerRun = worker.start();
  try {
    let pending: any;
    const deadline = Date.now() + 15_000;
    while (!pending && Date.now() < deadline) {
      const response = await apiJSON<any>(
        baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(created.taskId)}/human-interventions`, ADMIN_API_KEY,
      );
      pending = response.interventions?.find((item: any) => item.status === 'pending');
      if (!pending) await sleep(100);
    }
    assert(pending, 'worker did not create a pending human checkpoint');
    assert(pending.requestedAction === 'confirm_then_resume', `server derived an unexpected action: ${JSON.stringify(pending)}`);
    assert(pending.targetOrigin === new URL(fixtureUrl).origin, `checkpoint retained an unexpected target: ${JSON.stringify(pending)}`);

    const waitingTask = await apiJSON<any>(baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(created.taskId)}`, ADMIN_API_KEY);
    assert(waitingTask.status === 'waiting_for_human' && waitingTask.currentAttemptId === pending.attemptId,
      `task did not pause on the checkpoint-bound attempt: ${JSON.stringify(waitingTask)}`);
    if (operatorContext) {
      const adminPage = await operatorContext.newPage();
      await adminPage.goto(`${baseUrl}/admin/`);
      await adminPage.evaluate((key) => sessionStorage.setItem('adminApiKey', key), ADMIN_API_KEY);
      await adminPage.goto(`${baseUrl}/admin/tasks/${encodeURIComponent(created.taskId)}`);
      console.log('\n[stage11b] Human action required in the opened Admin UI.');
      console.log('[stage11b] Review the pending checkpoint, click "Approve and resume", then confirm the dialog.');
      console.log(`[stage11b] The local checkpoint expires in ${MANUAL_HUMAN_TIMEOUT_MS / 60_000} minutes.`);

      let decided = pending;
      const decisionDeadline = Date.now() + MANUAL_HUMAN_TIMEOUT_MS;
      while (decided.status === 'pending' && Date.now() < decisionDeadline) {
        await sleep(250);
        const response = await apiJSON<any>(
          baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(created.taskId)}/human-interventions`, ADMIN_API_KEY,
        );
        decided = response.interventions?.find((item: any) => item.id === pending.id) ?? decided;
      }
      assert(decided.status === 'approved', `operator did not approve the local checkpoint: ${JSON.stringify(decided)}`);
    } else {
      await apiJSON(
        baseUrl,
        'POST',
        `/admin/tasks/${encodeURIComponent(created.taskId)}/human-interventions/${encodeURIComponent(pending.id)}/decision`,
        ADMIN_API_KEY,
        { decision: 'approved', checkpointId: pending.checkpointId, note: 'automated hermetic acceptance approval' },
      );
    }
    await waitForTaskDone(baseUrl, created.taskId, 30_000);

    const completedTask = await apiJSON<any>(baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(created.taskId)}`, ADMIN_API_KEY);
    assert(completedTask.status === 'done' && completedTask.currentAttemptId === pending.attemptId,
      `approved checkpoint did not finish the same attempt: ${JSON.stringify(completedTask)}`);
    const results = await apiJSON<any>(
      baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(created.taskId)}/results?include_invalid=true`, ADMIN_API_KEY,
    );
    assert(results.page?.total === 1 && (results.page?.invalidBatches?.length ?? 0) === 0,
      `human checkpoint result was not schema-valid: ${JSON.stringify(results)}`);
    assert(JSON.stringify(results.page.batches[0].payload).includes('operator checkpoint approved'),
      `human checkpoint continuation did not run: ${JSON.stringify(results.page.batches)}`);
  } finally {
    worker.stop();
    await workerRun;
    dom.window.close();
  }
}

function parseMCPResponse(text: string): any {
  const trimmed = text.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith('{')) return JSON.parse(trimmed);
  const payloads = trimmed.split(/\r?\n/)
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice(5).trim())
    .filter((line) => line && line !== '[DONE]');
  assert(payloads.length > 0, `MCP response was neither JSON nor SSE: ${trimmed.slice(0, 500)}`);
  return JSON.parse(payloads[payloads.length - 1]);
}

async function mcpPost(baseUrl: string, client: MCPClientState, payload: Record<string, unknown>): Promise<any> {
  const response = await fetch(`${baseUrl}/mcp`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${client.token}`,
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
      ...(client.sessionId ? { 'Mcp-Session-Id': client.sessionId } : {}),
    },
    body: JSON.stringify(payload),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`MCP ${String(payload.method)} returned ${response.status}: ${text}`);
  client.sessionId = response.headers.get('mcp-session-id') ?? client.sessionId;
  return parseMCPResponse(text);
}

async function initializeMCP(baseUrl: string, token: string): Promise<MCPClientState> {
  const client: MCPClientState = { token, nextId: 1 };
  const initialized = await mcpPost(baseUrl, client, {
    jsonrpc: '2.0', id: client.nextId++, method: 'initialize',
    params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'stage10-acceptance', version: '1' } },
  });
  assert(initialized?.result?.protocolVersion, `MCP initialize returned an invalid response: ${JSON.stringify(initialized)}`);
  await mcpPost(baseUrl, client, { jsonrpc: '2.0', method: 'notifications/initialized', params: {} });
  return client;
}

async function callMCP(baseUrl: string, client: MCPClientState, name: string, args: Record<string, unknown>): Promise<any> {
  const response = await mcpPost(baseUrl, client, {
    jsonrpc: '2.0', id: client.nextId++, method: 'tools/call', params: { name, arguments: args },
  });
  assert(!response?.result?.isError, `MCP ${name} failed: ${JSON.stringify(response)}`);
  return response?.result?.structuredContent;
}

async function verifyPersistedInterfaces(
  baseUrl: string,
  token: string,
  approved: ApprovedVersion,
  taskId: string,
  createSchedule: boolean,
): Promise<void> {
  const adminResults = await apiJSON<any>(baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(taskId)}/results`, ADMIN_API_KEY);
  assert(adminResults.page?.total === 1 && adminResults.ruleVersionNumber === approved.version, 'Admin API lost persisted result lineage');
  const client = await initializeMCP(baseUrl, token);
  const mcpResults = await callMCP(baseUrl, client, 'get_task_results', { taskId });
  assert(mcpResults?.total === 1 && mcpResults?.ruleVersion === approved.version, `MCP lost persisted results: ${JSON.stringify(mcpResults)}`);
  if (createSchedule) {
    const schedule = await callMCP(baseUrl, client, 'create_schedule', {
      ruleId: approved.ruleId,
      ruleVersion: approved.version,
      name: 'Stage 10 recovery schedule',
      type: 'cron',
      expression: '0 9 * * *',
      timezone: 'UTC',
      inputs: await loadApprovedTaskInputs(baseUrl, approved),
    });
    assert(schedule?.id, `MCP write scope did not create a schedule: ${JSON.stringify(schedule)}`);
    const schedules = await callMCP(baseUrl, client, 'list_schedules', { ruleId: approved.ruleId });
    assert(schedules?.schedules?.some((item: any) => item.id === schedule.id), 'MCP read scope did not return its persisted schedule');
  }
}

function serverEnvironmentBase(): Record<string, string> {
  return {
    FEATURE_RECORDING_V2: 'true',
    FEATURE_WORKFLOW_V2: 'true',
    FEATURE_WORKER_PROTOCOL_V2: 'true',
    FEATURE_MCP: 'true',
    LLM_JOB_WORKER_INTERVAL: '50ms',
    LLM_RATE_LIMIT_PER_SECOND: '2000',
    LLM_RATE_LIMIT_BURST: '4000',
    MAX_WORKER_TASKS: '100',
    RATE_LIMIT_PER_SECOND: '2000',
    RATE_LIMIT_BURST: '4000',
    WORKER_RATE_LIMIT_PER_SECOND: '2000',
    WORKER_RATE_LIMIT_BURST: '4000',
    SITE_RATE_LIMIT_PER_SECOND: '2000',
    SITE_RATE_LIMIT_BURST: '4000',
    MCP_RATE_LIMIT_PER_SECOND: '2000',
    MCP_RATE_LIMIT_BURST: '4000',
  };
}

function serverEnvironment(fakeLLMPort: number, liveLLM?: LocalLLMConfig): Record<string, string> {
  const provider: Record<string, string> = liveLLM ? {
    LLM_PROVIDER: liveLLM.providerAdapter,
    LLM_API_KEY: liveLLM.apiKey,
    LLM_BASE_URL: liveLLM.providerBaseURL,
    LLM_MODEL: liveLLM.model,
    LLM_MAX_RETRIES: '0',
    LLM_ENABLE_REFLECTION: 'false',
    LLM_CACHE_TTL: '0s',
    LLM_REQUEST_TIMEOUT: '120s',
  } : enforcedLLMEnvironment(fakeLLMPort, {
    maxInputTokens: '1000000',
    maxOutputTokens: '100000',
    inputUSDPerMillion: '0.000001',
    outputUSDPerMillion: '0.000001',
    maxRequestUSD: '0.01',
    dailyBudgetUSD: '1',
    cacheTTL: '0s',
  });
  return {
    ...serverEnvironmentBase(),
    LLM_ENABLED: 'true',
    ...provider,
  };
}

async function main(): Promise<void> {
  const liveLLM = LIVE_LLM_WORKFLOW ? loadLocalLLMConfig() : undefined;
  const usedPorts = new Set<number>();
  const serverPort = await getDistinctPort(usedPorts);
  const pagePort = await getDistinctPort(usedPorts);
  const crossOriginPort = await getDistinctPort(usedPorts);
  const fakeLLMPort = await getDistinctPort(usedPorts);
  const baseUrl = `http://127.0.0.1:${serverPort}`;
  const fixtureUrl = `http://127.0.0.1:${pagePort}/semantic`;
  const serverDir = path.resolve(__dirname, '..', 'server');
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  const stamp = `${Date.now()}-${process.pid}`;
  const dbPath = path.join(os.tmpdir(), `aegis-stage10-${stamp}.db`);
  const recoveredDBPath = path.join(os.tmpdir(), `aegis-stage10-recovered-${stamp}.db`);
  const userDataDir = path.join(os.tmpdir(), `aegis-stage10-chromium-${stamp}`);
  const extensionDir = path.resolve(__dirname, '..', 'dist', 'extension');
  let serverProc: ChildProcess | undefined;
  let context: BrowserContext | undefined;
  let fixtureServers: [Server, Server] | undefined;
  let fakeLLM: Awaited<ReturnType<typeof startFakeLLM>> | undefined;
  const serverDiagnostics = new BoundedRedactor(8000, [
    ...FORBIDDEN_RECORDING_VALUES,
    ...(liveLLM ? [liveLLM.apiKey] : []),
  ]);

  const captureServerDiagnostics = (process: ChildProcess): void => {
    process.stderr?.on('data', (chunk) => {
      serverDiagnostics.append(String(chunk));
    });
  };

  const providerLabel = liveLLM ? `${liveLLM.providerLabel}/${liveLLM.model}` : 'the fake provider';
  console.log(`[stage10] starting ${MANUAL_HUMAN_APPROVAL ? 'human-supervised' : 'unattended'} acceptance with ${providerLabel} on ${baseUrl}`);
  await buildServer(serverDir);
  if (!liveLLM) fakeLLM = await startFakeLLM(fakeLLMPort);
  fixtureServers = await startFixtureServers(pagePort, crossOriginPort);
  const env = serverEnvironment(fakeLLMPort, liveLLM);
  serverProc = startServer(serverDir, dbPath, serverPort, env);
  captureServerDiagnostics(serverProc);

  try {
    await waitForHealth(baseUrl);
    if (fakeLLM) {
      await runHardBudgetQualification(serverDir, fakeLLMPort, fakeLLM, usedPorts, stamp);
      await assertEnforcedControlPlane(baseUrl);
    }
    context = await chromium.launchPersistentContext(userDataDir, {
      headless: false,
      args: [
        `--disable-extensions-except=${extensionDir.replace(/\\/g, '/')}`,
        `--load-extension=${extensionDir.replace(/\\/g, '/')}`,
        '--disable-web-security',
        '--allow-file-access-from-files',
      ],
    });
    const deadline = Date.now() + 15_000;
    let background = context.backgroundPages()[0] ?? context.serviceWorkers()[0];
    while (!background && Date.now() < deadline) {
      await sleep(250);
      background = context.backgroundPages()[0] ?? context.serviceWorkers()[0];
    }
    assert(background, 'Manifest V3 extension service worker did not start');
    const extensionId = await background.evaluate(() => (globalThis as any).chrome.runtime.id) as string;
    const popup = await extensionPopup(context, extensionId, baseUrl);
    await assertNavigationReplayTabRetention(context, popup, pagePort);
    const fixturePage = await context.newPage();
    const recording = await recordFixture(popup, fixturePage, fixtureUrl);
    console.log(`[stage10] recording ${recording.meta.serverRecordingId} captured and sanitized`);

    const approved = await approveRuleVersion(context, extensionId);
    if (fakeLLM) {
      assert(fakeLLM.requestCount() >= 2 && fakeLLM.leaks.length === 0,
        'production LLM orchestration did not use the safe local provider boundary');
      const { generationRequests, generationFeedback } = fakeLLM.dslStats;
      assert(generationRequests >= 2,
        `provisional-invalid fake rule did not trigger a bounded retry: observed ${generationRequests} DSL generation request(s)`);
      assert(generationFeedback[0] === '',
        'the first DSL generation attempt unexpectedly carried retry feedback');
      assert(
        generationFeedback.slice(1).every((feedback) => feedback.includes(INVALID_DSL_FEEDBACK)),
        `DSL generation retry did not carry the previous server-side validation feedback: ${JSON.stringify(generationFeedback)}`,
      );
      console.log(`[stage10] provisional-invalid DSL regenerated after ${generationRequests} attempts with validation feedback in the retry prompt`);
    }
    console.log(`[stage10] approved immutable ${approved.ruleId} v${approved.version}`);
    if (!MANUAL_HUMAN_APPROVAL) {
      await context.close();
      context = undefined;
    }

    const execution = await executeConcurrentTasks(baseUrl, approved, fixtureUrl);
    console.log(`[stage10] ${TASK_COUNT} tasks completed; max overlapping browser actions ${execution.maxConcurrentClicks}`);

    const operatorContext = MANUAL_HUMAN_APPROVAL ? context : undefined;
    if (MANUAL_HUMAN_APPROVAL) assert(operatorContext, 'manual checkpoint approval requires an active browser context');
    await executeHumanCheckpointTask(baseUrl, fixtureUrl, operatorContext);
    console.log('[stage11b] checkpoint-bound operator approval resumed the same attempt');

    if (context) {
      await context.close();
      context = undefined;
    }

    const tokenResponse = await apiJSON<{ token: string }>(baseUrl, 'POST', '/admin/mcp/tokens', ADMIN_API_KEY, {
      name: 'stage10 acceptance token', permissions: ['read', 'write'],
    });
    assert(tokenResponse.token, 'MCP token endpoint did not return the one-time bearer token');

    await killServer(serverProc);
    serverProc = startServer(serverDir, dbPath, serverPort, env);
    captureServerDiagnostics(serverProc);
    await waitForHealth(baseUrl);
    await verifyPersistedInterfaces(baseUrl, tokenResponse.token, approved, execution.taskIds[0], true);
    console.log('[stage10] restart preserved Admin and MCP state');

    await killServer(serverProc);
    serverProc = undefined;
    // Copy with WAL/SHM sidecars: the server does not checkpoint on SIGTERM,
    // so recent commits (e.g. the MCP token) may still be WAL-resident and a
    // main-file-only copy would silently lose them.
    copySQLiteDatabase(dbPath, recoveredDBPath);
    serverProc = startServer(serverDir, recoveredDBPath, serverPort, env);
    captureServerDiagnostics(serverProc);
    await waitForHealth(baseUrl);
    await verifyPersistedInterfaces(baseUrl, tokenResponse.token, approved, execution.taskIds[0], false);
    console.log('[stage10] stopped-database restore preserved immutable lineage and results');
    console.log(`[stage10] ${MANUAL_HUMAN_APPROVAL ? 'human-supervised' : 'unattended'} staging acceptance passed`);
  } catch (error) {
    if (fakeLLM) {
      console.error(`[stage10] fake provider diagnostics: ${JSON.stringify({
        requests: fakeLLM.requestCount(),
        leakCount: fakeLLM.leaks.length,
        dslStats: fakeLLM.dslStats,
      })}`);
    }
    const boundedServerDiagnostics = serverDiagnostics.render().trim();
    if (boundedServerDiagnostics) {
      console.error(`[stage10] bounded server diagnostics:\n${boundedServerDiagnostics}`);
    }
    throw error;
  } finally {
    fakeLLM?.releaseHungRequest();
    if (context) await context.close().catch(() => undefined);
    if (serverProc) await killServer(serverProc);
    await Promise.all([
      closeServer(fixtureServers?.[0]),
      closeServer(fixtureServers?.[1]),
      closeServer(fakeLLM?.server),
    ]);
    for (const file of [dbPath, `${dbPath}-wal`, `${dbPath}-shm`, recoveredDBPath, `${recoveredDBPath}-wal`, `${recoveredDBPath}-shm`, binaryPath]) {
      removeIfExists(file);
    }
    fs.rmSync(userDataDir, { recursive: true, force: true });
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error('[stage10] unattended staging acceptance failed:', error);
    process.exit(1);
  });
}
