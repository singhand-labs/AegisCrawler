/**
 * Hermetic end-to-end acceptance for the browser worker runtime
 * (src/worker-host + src/worker-page). Builds and starts the real Go server
 * with worker protocol v2, approves DSL rule versions directly through the
 * Admin API, and executes them in REAL headless Chromium via WorkerHost.
 *
 * Covered:
 *   - two concurrent tasks across two worker slots (persistent profiles);
 *   - one valid batch + one final summary per task, retrievable via Admin API;
 *   - legacy setTag remains log-only across navigation + buffered flush;
 *   - idempotent batch retry acknowledged, conflicting retry rejected (409);
 *   - cross-navigation resume (navigate -> re-inject -> resume -> extract);
 *   - generated-rule execution on a page whose CSP rejects inline scripts;
 *   - requestHuman checkpoint approved through the Admin API (bridge path);
 *   - security boundary: page JS cannot reach the worker key, the bridge
 *     rejects unknown methods and malformed payloads, the bundle carries no
 *     key material, and cross-origin navigation is blocked by host policy.
 *
 * Run: npm run test:e2e:worker
 */

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { ChildProcess } from 'child_process';
import { createServer as createHttpServer, IncomingMessage, Server, ServerResponse } from 'http';
import {
  ADMIN_API_KEY,
  WORKER_API_KEY,
  buildServer,
  getFreePort,
  killServer,
  removeIfExists,
  startServer,
  waitForHealth,
} from './e2e-deployment-test';
import { WorkerHost } from '../src/worker-host/host';
import { buildWorkerPageBundle } from '../src/worker-host/bundle';

const RESULT_TEXT = 'semantic action complete';
const DETAIL_TEXT = 'worker host detail page collected';
const HUMAN_RESULT_TEXT = 'operator checkpoint approved';
const CSP_TITLE = 'Strict CSP generated rule';
const PROFILE = 'worker-e2e-profile';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
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

/**
 * The fixture embeds WORKER_API_KEY as the scan NEEDLE deliberately: page
 * JavaScript is the strongest adversary position (same realm as the worker
 * page runtime), and the scan proves no window global contains the worker
 * credential. The needle lives only in this inline script's closure and is
 * never itself exposed as a window global or extracted as data.
 */
function fixtureHTML(port: number): string {
  return `<!doctype html>
<html lang="en">
  <head><title>worker host fixture</title></head>
  <body>
    <p id="semantic-result">semantic action pending</p>
    <p id="checkpoint-result">${HUMAN_RESULT_TEXT}</p>
    <button id="semantic-action" type="button">Run semantic action</button>
    <p id="bridge-scan-result">scan pending</p>
    <button id="run-bridge-scan" type="button">Run bridge scan</button>
    <button id="cross-origin-link" type="button">Leave origin</button>
    <script>
      (function () {
        var NEEDLE = ${JSON.stringify(WORKER_API_KEY)};
        document.getElementById('semantic-action').addEventListener('click', function () {
          document.getElementById('semantic-result').textContent = ${JSON.stringify(RESULT_TEXT)};
        });
        document.getElementById('cross-origin-link').addEventListener('click', function () {
          location.href = 'http://localhost:${port}/cross-target';
        });
        document.getElementById('run-bridge-scan').addEventListener('click', function () {
          var findings = [];
          var names = Object.getOwnPropertyNames(window);
          for (var i = 0; i < names.length; i++) {
            var value;
            try { value = window[names[i]]; } catch (e) { continue; }
            try {
              if (typeof value === 'string' && value.indexOf(NEEDLE) !== -1) {
                findings.push('window.' + names[i]);
              } else if (value && value !== window.document && (typeof value === 'object' || typeof value === 'function')) {
                var inner = Object.getOwnPropertyNames(value);
                for (var j = 0; j < inner.length; j++) {
                  var nested;
                  try { nested = value[inner[j]]; } catch (e) { continue; }
                  if (typeof nested === 'string' && nested.indexOf(NEEDLE) !== -1) {
                    findings.push('window.' + names[i] + '.' + inner[j]);
                  }
                }
              }
            } catch (e) { /* inaccessible global */ }
          }
          var bridgeType = typeof window.__ocWorkerBridge;
          var unknown = Promise.resolve(window.__ocWorkerBridge('stealCredentials', {}))
            .then(function () { return 'NOT-REJECTED'; }, function () { return 'bridge-unknown-method-rejected'; });
          var shape = Promise.resolve(window.__ocWorkerBridge('sendSnapshot', { name: 42 }))
            .then(function () { return 'NOT-REJECTED'; }, function () { return 'bridge-bad-shape-rejected'; });
          Promise.all([unknown, shape]).then(function (results) {
            document.getElementById('bridge-scan-result').textContent = 'scan complete'
              + ' | key=' + (findings.length > 0 ? 'LEAK:' + findings.join(',') : 'no-key-leak')
              + ' | bridge=' + bridgeType
              + ' | unknown=' + results[0]
              + ' | shape=' + results[1];
          });
        });
      })();
    </script>
  </body>
</html>`;
}

function detailHTML(): string {
  return `<!doctype html><html lang="en"><body><p id="detail-result">${DETAIL_TEXT}</p></body></html>`;
}

function crossTargetHTML(): string {
  return '<!doctype html><html lang="en"><body><p id="cross-target">cross origin target reached</p></body></html>';
}

function strictCSPHTML(): string {
  return `<!doctype html><html lang="en"><body><main>
    <h1>${CSP_TITLE}</h1>
    <p>WorkerHost must execute this replay-approved shape without an inline script element.</p>
    <h2>Runtime injection</h2>
  </main></body></html>`;
}

async function startFixtureServer(port: number): Promise<Server> {
  const server = createHttpServer((request: IncomingMessage, response: ServerResponse) => {
    const pathname = new URL(request.url ?? '/', `http://127.0.0.1:${port}`).pathname;
    if (pathname === '/strict-csp') {
      response.writeHead(200, {
        'Content-Type': 'text/html; charset=utf-8',
        'Content-Security-Policy': "default-src 'self'; script-src 'self'; script-src-elem 'self'",
      });
      response.end(strictCSPHTML());
      return;
    }
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    if (pathname === '/detail') {
      response.end(detailHTML());
      return;
    }
    if (pathname === '/cross-target') {
      response.end(crossTargetHTML());
      return;
    }
    response.end(fixtureHTML(port));
  });
  await listen(server, port);
  return server;
}

interface ApprovedRule {
  ruleId: string;
  version: number;
}

async function approveRule(baseUrl: string, rule: Record<string, unknown>): Promise<ApprovedRule> {
  const ruleId = String(rule.id);
  const created = await apiJSON<any>(baseUrl, 'POST', `/admin/rules/${encodeURIComponent(ruleId)}/versions`, ADMIN_API_KEY, { rule });
  const version = created.ruleVersion?.version;
  assert(Number.isInteger(version), `rule ${ruleId} version was not created: ${JSON.stringify(created)}`);
  await apiJSON(baseUrl, 'POST', `/admin/rules/${encodeURIComponent(ruleId)}/versions/${version}/approve`, ADMIN_API_KEY);
  return { ruleId, version };
}

async function createTask(baseUrl: string, approved: ApprovedRule): Promise<string> {
  const created = await apiJSON<{ taskId: string }>(baseUrl, 'POST', '/admin/tasks', ADMIN_API_KEY, {
    ruleId: approved.ruleId,
    ruleVersionNumber: approved.version,
    variables: {},
  });
  assert(created.taskId, `task creation returned no id for ${approved.ruleId}`);
  return created.taskId;
}

async function waitForTaskStatus(
  baseUrl: string,
  taskId: string,
  statuses: string[],
  timeoutMs: number,
): Promise<any> {
  const deadline = Date.now() + timeoutMs;
  let last: any;
  while (Date.now() < deadline) {
    last = await apiJSON<any>(baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(taskId)}`, ADMIN_API_KEY);
    if (statuses.includes(last.status)) return last;
    await sleep(250);
  }
  throw new Error(`task ${taskId} did not reach [${statuses.join(', ')}] in time (last=${JSON.stringify(last)})`);
}

function basicRule(entryUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-basic',
    version: '1.0.0',
    name: 'Worker host basic collection',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    steps: [
      { action: 'click', target: { selector: '#semantic-action' } },
      { action: 'waitForText', target: { selector: '#semantic-result' }, text: RESULT_TEXT, timeout: 5000 },
      { action: 'extractText', name: 'result', target: { selector: '#semantic-result' } },
      { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true },
    ],
    output: {
      type: 'object',
      properties: { result: { type: 'string' } },
      required: ['result'],
      additionalProperties: false,
    },
  };
}

function navigationRule(entryUrl: string, detailUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-navigation',
    version: '1.0.0',
    name: 'Worker host navigation resume',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    steps: [
      { action: 'navigate', url: detailUrl, waitUntil: 'load' },
      { action: 'waitForElementVisible', target: { selector: '#detail-result' }, timeout: 10000 },
      { action: 'extractText', name: 'detail', target: { selector: '#detail-result' } },
      { action: 'sendResult', payload: { detail: '{{extracted.detail}}' }, immediate: true },
    ],
    output: {
      type: 'object',
      properties: { detail: { type: 'string' } },
      required: ['detail'],
      additionalProperties: false,
    },
  };
}

function legacySetTagBufferedRule(entryUrl: string, detailUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-legacy-settag-buffer',
    version: '1.0.0',
    name: 'Worker host legacy setTag output isolation',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    sendPolicy: { batchSize: 2 },
    steps: [
      { action: 'navigate', url: detailUrl, waitUntil: 'load' },
      { action: 'waitForElementVisible', target: { selector: '#detail-result' }, timeout: 10_000 },
      { action: 'extractText', name: 'result', target: { selector: '#detail-result' } },
      { action: 'setTag', tags: { source: 'legacy', run: 'worker-e2e' }, scope: 'task' },
      { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: false },
      { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: false },
      { action: 'flushResults' },
    ],
    output: {
      type: 'object',
      properties: { result: { type: 'string' } },
      required: ['result'],
      additionalProperties: false,
    },
  };
}

function humanRule(entryUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-human',
    version: '1.0.0',
    name: 'Worker host human checkpoint',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    steps: [
      { action: 'extractText', name: 'result', target: { selector: '#checkpoint-result' } },
      {
        action: 'requestHuman',
        id: 'approve-safe-resume',
        type: 'confirmation',
        prompt: 'Confirm the fixture is safe to resume',
        timeout: 30_000,
        then: [{ action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true }],
      },
    ],
    output: {
      type: 'object',
      properties: { result: { type: 'string' } },
      required: ['result'],
      additionalProperties: false,
    },
  };
}

function securityRule(entryUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-security',
    version: '1.0.0',
    name: 'Worker host security boundary scan',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    steps: [
      { action: 'click', target: { selector: '#run-bridge-scan' } },
      { action: 'waitForText', target: { selector: '#bridge-scan-result' }, text: 'scan complete', timeout: 10000 },
      { action: 'extractText', name: 'scan', target: { selector: '#bridge-scan-result' } },
      { action: 'sendResult', payload: { scan: '{{extracted.scan}}' }, immediate: true },
    ],
    output: {
      type: 'object',
      properties: { scan: { type: 'string' } },
      required: ['scan'],
      additionalProperties: false,
    },
  };
}

function crossOriginRule(entryUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-cross-origin',
    version: '1.0.0',
    name: 'Worker host cross-origin navigation block',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    steps: [
      { action: 'click', target: { selector: '#cross-origin-link' } },
      // Keeps the run alive while the cross-origin navigation commits; the
      // host navigation policy must terminalize the run before this elapses.
      { action: 'waitForTimeout', ms: 30_000 },
      { action: 'sendResult', payload: { result: 'unreachable' }, immediate: true },
    ],
    output: {
      type: 'object',
      properties: { result: { type: 'string' } },
      required: ['result'],
      additionalProperties: false,
    },
  };
}

function strictCSPGeneratedRule(entryUrl: string): Record<string, unknown> {
  return {
    id: 'worker-host-strict-csp-generated',
    version: '1.0.0',
    name: 'Replay-approved generated rule under strict CSP',
    domain: '127.0.0.1',
    enabled: true,
    priority: 'normal',
    entry: entryUrl,
    variables: {},
    steps: [
      { action: 'navigate', url: entryUrl, waitUntil: 'domcontentloaded' },
      { action: 'waitForElementVisible', target: { selector: 'main' } },
      { action: 'scrollBy', direction: 'down', distance: 1, unit: 'pages' },
      { action: 'extractText', name: 'title', target: { selector: 'h1', visible: true } },
      { action: 'extractText', name: 'introduction', target: { selector: 'p', visible: true } },
      { action: 'extractText', name: 'first_section', target: { selector: 'h2', visible: true } },
      {
        action: 'sendResult',
        immediate: true,
        payload: {
          title: '{{extracted.title}}',
          introduction: '{{extracted.introduction}}',
          first_section: '{{extracted.first_section}}',
        },
      },
    ],
    output: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        introduction: { type: 'string' },
        first_section: { type: 'string' },
      },
      required: ['title', 'introduction', 'first_section'],
      additionalProperties: false,
    },
  };
}

async function assertValidBatchAndSummary(baseUrl: string, taskId: string, expectedText: string): Promise<any> {
  const results = await apiJSON<any>(
    baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(taskId)}/results?include_invalid=true`, ADMIN_API_KEY,
  );
  assert(results.page?.total === 1 && results.page?.batches?.length === 1,
    `task ${taskId} did not persist exactly one valid batch: ${JSON.stringify(results.page)}`);
  assert((results.page?.invalidBatches?.length ?? 0) === 0,
    `task ${taskId} quarantined an internal completion marker`);
  assert(results.page?.summary, `task ${taskId} omitted its final execution summary`);
  assert(JSON.stringify(results.page.batches[0].payload).includes(expectedText),
    `task ${taskId} returned unexpected data: ${JSON.stringify(results.page.batches[0].payload)}`);
  return results;
}

async function main(): Promise<void> {
  const serverPort = await getFreePort();
  const fixturePort = await getFreePort();
  const baseUrl = `http://127.0.0.1:${serverPort}`;
  const entryUrl = `http://127.0.0.1:${fixturePort}/collect`;
  const detailUrl = `http://127.0.0.1:${fixturePort}/detail`;
  const strictCSPUrl = `http://127.0.0.1:${fixturePort}/strict-csp`;
  const fixtureOrigin = new URL(entryUrl).origin;
  const serverDir = path.resolve(__dirname, '..', 'server');
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  const stamp = `${Date.now()}-${process.pid}`;
  const dbPath = path.join(os.tmpdir(), `aegis-worker-host-${stamp}.db`);
  const profilesDir = path.join(os.tmpdir(), `aegis-worker-host-profiles-${stamp}`);
  let serverProc: ChildProcess | undefined;
  let fixtureServer: Server | undefined;
  let host: WorkerHost | undefined;
  let hostError: unknown;

  console.log(`[worker-e2e] starting browser worker acceptance on ${baseUrl}`);
  await buildServer(serverDir);
  fixtureServer = await startFixtureServer(fixturePort);
  serverProc = startServer(serverDir, dbPath, serverPort, {
    FEATURE_RECORDING_V2: 'true',
    FEATURE_WORKFLOW_V2: 'true',
    FEATURE_WORKER_PROTOCOL_V2: 'true',
    MAX_WORKER_TASKS: '100',
    RATE_LIMIT_PER_SECOND: '2000',
    RATE_LIMIT_BURST: '4000',
    WORKER_RATE_LIMIT_PER_SECOND: '2000',
    WORKER_RATE_LIMIT_BURST: '4000',
    SITE_RATE_LIMIT_PER_SECOND: '2000',
    SITE_RATE_LIMIT_BURST: '4000',
  });

  try {
    await waitForHealth(baseUrl);

    // The page bundle is built from source only; assert it can never carry
    // credential material into the page realm.
    const bundle = await buildWorkerPageBundle();
    assert(bundle.includes('__ocWorkerBoot'), 'worker page bundle omitted the boot entry point');
    assert(!bundle.includes(WORKER_API_KEY), 'worker page bundle contains the worker API key');

    const [basic, navigation, legacySetTag, human, security, crossOrigin, strictCSP] = await Promise.all([
      approveRule(baseUrl, basicRule(entryUrl)),
      approveRule(baseUrl, navigationRule(entryUrl, detailUrl)),
      approveRule(baseUrl, legacySetTagBufferedRule(entryUrl, detailUrl)),
      approveRule(baseUrl, humanRule(entryUrl)),
      approveRule(baseUrl, securityRule(entryUrl)),
      approveRule(baseUrl, crossOriginRule(entryUrl)),
      approveRule(baseUrl, strictCSPGeneratedRule(strictCSPUrl)),
    ]);
    console.log('[worker-e2e] approved seven immutable rule versions');

    let observedSuccessfulPages = 0;
    host = new WorkerHost({
      serverUrl: baseUrl,
      apiKey: WORKER_API_KEY,
      profile: PROFILE,
      profilesDir,
      workers: 2,
      headless: true,
      workerIdPrefix: 'e2e-host',
      pollIntervalMs: 100,
      heartbeatIntervalMs: 1000,
      verbose: true,
      onPageComplete: (page, result) => {
        assert(result.status === 'success', `page observer received non-success result ${result.status}`);
        assert(page.url().startsWith(fixtureOrigin), `page observer received unexpected URL ${page.url()}`);
        observedSuccessfulPages += 1;
      },
    });
    const hostRun = host.start().catch((err) => {
      hostError = err;
    });

    const [basicTask1, basicTask2, navTask, legacySetTagTask, humanTask, securityTask, crossOriginTask, strictCSPTask] = await Promise.all([
      createTask(baseUrl, basic),
      createTask(baseUrl, basic),
      createTask(baseUrl, navigation),
      createTask(baseUrl, legacySetTag),
      createTask(baseUrl, human),
      createTask(baseUrl, security),
      createTask(baseUrl, crossOrigin),
      createTask(baseUrl, strictCSP),
    ]);

    // Basic concurrency: two tasks over two slots, each one batch + summary.
    await Promise.all([
      waitForTaskStatus(baseUrl, basicTask1, ['done'], 60_000),
      waitForTaskStatus(baseUrl, basicTask2, ['done'], 60_000),
    ]);
    assert(!hostError, `worker host exited unexpectedly: ${String(hostError)}`);
    await assertValidBatchAndSummary(baseUrl, basicTask1, RESULT_TEXT);
    await assertValidBatchAndSummary(baseUrl, basicTask2, RESULT_TEXT);
    console.log('[worker-e2e] two concurrent real-Chromium tasks completed with valid results');

    // Idempotency: an exact batch retry is acknowledged; a conflicting retry
    // with the same key is rejected with 409.
    const taskDetail = await apiJSON<any>(baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(basicTask1)}`, ADMIN_API_KEY);
    const attemptId = taskDetail.currentAttemptId;
    assert(attemptId, `task ${basicTask1} has no current attempt`);
    const batchPayload = (await apiJSON<any>(
      baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(basicTask1)}/results`, ADMIN_API_KEY,
    )).page.batches[0].payload;
    const retryEnvelope = {
      taskId: basicTask1,
      workerId: 'e2e-host-retry-probe',
      attemptId,
      idempotencyKey: `${attemptId}:1:batch`,
      sequence: 1,
      kind: 'batch',
      immediate: true,
      payload: batchPayload,
    };
    const retryResponse = await fetch(`${baseUrl}/results`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${WORKER_API_KEY}` },
      body: JSON.stringify(retryEnvelope),
    });
    assert(retryResponse.ok, `exact idempotent retry was not acknowledged: ${retryResponse.status}`);
    const conflictResponse = await fetch(`${baseUrl}/results`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${WORKER_API_KEY}` },
      body: JSON.stringify({ ...retryEnvelope, payload: { result: 'tampered' } }),
    });
    assert(conflictResponse.status === 409,
      `conflicting idempotency reuse returned ${conflictResponse.status}, expected 409`);
    console.log('[worker-e2e] idempotent batch retry acknowledged; conflicting reuse rejected with 409');

    // Navigation resume: the rule navigates to a second page and extracts
    // there, proving checkpoint -> host re-injection -> resume.
    await waitForTaskStatus(baseUrl, navTask, ['done'], 60_000);
    await assertValidBatchAndSummary(baseUrl, navTask, DETAIL_TEXT);
    console.log('[worker-e2e] cross-navigation resume extracted the second-page result');

    // Provider-generated rules commonly begin by navigating to their entry
    // URL. A strict target-page CSP must not block the host-owned runtime on
    // either initial boot or same-entry navigation resume.
    await waitForTaskStatus(baseUrl, strictCSPTask, ['done'], 60_000);
    await assertValidBatchAndSummary(baseUrl, strictCSPTask, CSP_TITLE);
    console.log('[worker-e2e] replay-approved generated rule completed under strict script CSP');

    // Legacy setTag executes after a real navigation resume, then two
    // immediate=false rows hit batchSize=2. The explicit flush must not emit a
    // control row, and log tags must never enter the closed business schema.
    const legacyTask = await waitForTaskStatus(baseUrl, legacySetTagTask, ['done'], 60_000);
    const legacyResults = await apiJSON<any>(
      baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(legacySetTagTask)}/results?include_invalid=true`, ADMIN_API_KEY,
    );
    const expectedLegacyRows = [{ result: DETAIL_TEXT }, { result: DETAIL_TEXT }];
    assert(legacyTask.status === 'done', `legacy setTag task was not done: ${JSON.stringify(legacyTask)}`);
    assert(legacyResults.page?.total === 1 && legacyResults.page?.batches?.length === 1,
      `legacy setTag task did not persist exactly one valid batch: ${JSON.stringify(legacyResults.page)}`);
    assert((legacyResults.page?.invalidBatches?.length ?? 0) === 0,
      `legacy setTag task produced invalid business rows: ${JSON.stringify(legacyResults.page?.invalidBatches)}`);
    assert(JSON.stringify(legacyResults.page.batches[0].payload) === JSON.stringify(expectedLegacyRows),
      `legacy setTag changed raw business rows: ${JSON.stringify(legacyResults.page.batches[0].payload)}`);
    assert(legacyResults.page?.summary?.kind === 'summary',
      `legacy setTag task omitted its sole final summary: ${JSON.stringify(legacyResults.page?.summary)}`);
    // A done transition is committed atomically only with exactly one summary.
    console.log('[worker-e2e] legacy setTag stayed log-only through navigation, batching, and explicit flush');

    // Human checkpoint through the bridge requestHuman path.
    let pending: any;
    const humanDeadline = Date.now() + 30_000;
    while (!pending && Date.now() < humanDeadline) {
      const interventions = await apiJSON<any>(
        baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(humanTask)}/human-interventions`, ADMIN_API_KEY,
      );
      pending = interventions.interventions?.find((item: any) => item.status === 'pending');
      if (!pending) await sleep(200);
    }
    assert(pending, 'worker did not create a pending human checkpoint through the bridge');
    const waitingTask = await apiJSON<any>(baseUrl, 'GET', `/admin/tasks/${encodeURIComponent(humanTask)}`, ADMIN_API_KEY);
    assert(waitingTask.status === 'waiting_for_human' && waitingTask.currentAttemptId === pending.attemptId,
      `task did not pause on the checkpoint-bound attempt: ${JSON.stringify(waitingTask)}`);
    await apiJSON(
      baseUrl, 'POST',
      `/admin/tasks/${encodeURIComponent(humanTask)}/human-interventions/${encodeURIComponent(pending.id)}/decision`,
      ADMIN_API_KEY,
      { decision: 'approved', checkpointId: pending.checkpointId, note: 'automated worker-host acceptance approval' },
    );
    await waitForTaskStatus(baseUrl, humanTask, ['done'], 30_000);
    await assertValidBatchAndSummary(baseUrl, humanTask, HUMAN_RESULT_TEXT);
    console.log('[worker-e2e] bridge requestHuman checkpoint resumed the same attempt after approval');

    // Security boundary, proven by page JavaScript itself during a real run.
    await waitForTaskStatus(baseUrl, securityTask, ['done'], 60_000);
    const scanResults = await apiJSON<any>(
      baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(securityTask)}/results`, ADMIN_API_KEY,
    );
    const scanText = String(scanResults.page?.batches?.[0]?.payload?.scan ?? '');
    assert(scanText.includes('key=no-key-leak'), `page JS found credential material: ${scanText}`);
    assert(scanText.includes('bridge=function'), `bridge was not installed exactly as a function: ${scanText}`);
    assert(scanText.includes('unknown=bridge-unknown-method-rejected'), `bridge admitted an unknown method: ${scanText}`);
    assert(scanText.includes('shape=bridge-bad-shape-rejected'), `bridge admitted a malformed payload: ${scanText}`);
    console.log('[worker-e2e] page-realm scan proved no key leak and strict bridge validation');

    // Cross-origin navigation during execution is blocked by host policy.
    const blockedTask = await waitForTaskStatus(baseUrl, crossOriginTask, ['failed', 'dead_letter'], 60_000);
    const blockedMessage = `${blockedTask.errorMessage ?? ''} ${blockedTask.errorType ?? ''}`.toLowerCase();
    assert(blockedMessage.includes('rule.domain') || blockedMessage.includes('domain'),
      `cross-origin task failed for an unexpected reason: ${JSON.stringify(blockedTask)}`);
    const crossResults = await apiJSON<any>(
      baseUrl, 'GET', `/api/v1/tasks/${encodeURIComponent(crossOriginTask)}/results?include_invalid=true`, ADMIN_API_KEY,
    );
    assert((crossResults.page?.total ?? 0) === 0, 'blocked run submitted result data');
    console.log('[worker-e2e] cross-origin navigation was blocked by worker host policy');

    assert(fs.existsSync(path.join(profilesDir, `${PROFILE}-1`)), 'persistent profile dir for slot 1 missing');
    assert(fs.existsSync(path.join(profilesDir, `${PROFILE}-2`)), 'persistent profile dir for slot 2 missing');
    assert(observedSuccessfulPages >= 6,
      `host-only page observer saw ${observedSuccessfulPages} successful pages, expected at least 6`);
    assert(!hostError, `worker host exited unexpectedly: ${String(hostError)}`);

    await host.stop();
    host = undefined;
    await hostRun;
    console.log('[worker-e2e] browser worker acceptance passed');
  } finally {
    if (host) await host.stop().catch(() => undefined);
    if (serverProc) await killServer(serverProc);
    await closeServer(fixtureServer);
    for (const file of [dbPath, `${dbPath}-wal`, `${dbPath}-shm`, binaryPath]) {
      removeIfExists(file);
    }
    fs.rmSync(profilesDir, { recursive: true, force: true });
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error('[worker-e2e] browser worker acceptance failed:', error);
    process.exit(1);
  });
}
