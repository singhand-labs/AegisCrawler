import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { spawn, ChildProcess } from 'child_process';
import { createServer as createHttpServer } from 'http';
import { chromium, BrowserContext, Page } from 'playwright';
import type { DomNode, PageAgentRecording } from '../src/rule-generator/types';
import {
  WORKER_API_KEY,
  ADMIN_API_KEY,
  VARIABLE_ENCRYPTION_KEY,
  getFreePort,
  buildServer,
  startServer,
  waitForHealth,
  adminRequest,
  workerGet,
  waitForTaskDone,
  killServer,
  removeIfExists,
} from './e2e-deployment-test';

const WORKER_ID = 'e2e-browser-worker';

interface TaskResponse {
  id: string;
  status: string;
  ruleId: string;
}

interface ResultResponse {
  id: string;
  taskId: string;
  payload: unknown;
  immediate: boolean;
  createdAt: string;
}

interface ClaimedTaskResponse {
  taskId: string;
}

async function workerPost(baseUrl: string, route: string, body: unknown): Promise<Response> {
  return fetch(`${baseUrl}${route}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${WORKER_API_KEY}`,
    },
    body: JSON.stringify(body),
  });
}

async function leaseTaskForBrowserWorker(baseUrl: string, targetTaskId: string): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const claimResponse = await workerPost(baseUrl, '/tasks/claim', { workerId: WORKER_ID });
    if (claimResponse.status === 204) {
      throw new Error(`target task ${targetTaskId} was not available to claim`);
    }
    if (!claimResponse.ok) {
      throw new Error(`task claim failed: ${claimResponse.status} ${await claimResponse.text()}`);
    }

    const claimed = (await claimResponse.json()) as ClaimedTaskResponse;
    if (claimed.taskId === targetTaskId) {
      return;
    }

    const cancelResponse = await workerPost(baseUrl, '/status', {
      taskId: claimed.taskId,
      workerId: WORKER_ID,
      status: 'cancelled',
      message: 'unused task created by browser E2E wizard',
    });
    if (!cancelResponse.ok) {
      throw new Error(`failed to cancel unused task ${claimed.taskId}: ${cancelResponse.status} ${await cancelResponse.text()}`);
    }
  }

  throw new Error(`target task ${targetTaskId} was not claimed after draining the browser E2E queue`);
}

function startStaticServer(
  fixturePath: string,
  port: number,
  routes: Record<string, string> = {},
): ChildProcess {
  const server = createHttpServer((req, res) => {
    const pathname = new URL(req.url ?? '/', `http://127.0.0.1:${port}`).pathname;
    if (routes[pathname] !== undefined) {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end(routes[pathname]);
      return;
    }
    fs.readFile(fixturePath, (err, data) => {
      if (err) {
        res.writeHead(404);
        res.end('not found');
        return;
      }
      res.writeHead(200, { 'Content-Type': 'text/html' });
      res.end(data);
    });
  });

  server.listen(port, '127.0.0.1');

  const proc = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], {
    stdio: 'inherit',
  });
  // Tie the server lifecycle to the dummy process so we can kill it cleanly.
  (proc as any).__e2eServer = server;
  return proc;
}

function semanticFixtureRoutes(pagePort: number, crossOriginPort: number): Record<string, string> {
  return {
    '/semantic': `<!doctype html>
      <html lang="en">
        <head><title>Semantic recording fixture</title></head>
        <body>
          <h1>Semantic recording fixture</h1>
          <input id="fixture-password" type="password" value="semantic-password-value">
          <input id="fixture-token" type="hidden" name="api_token" value="semantic-token-value">
          <p id="private-copy">alice.fixture@example.test token=semantic-inline-token</p>
          <p id="semantic-result">semantic action pending</p>
          <button id="semantic-action" type="button">Run semantic action</button>
          <iframe id="same-frame" src="http://127.0.0.1:${pagePort}/semantic/same-child"></iframe>
          <iframe id="cross-frame" src="http://127.0.0.1:${crossOriginPort}/cross-child?token=cross-query-value"></iframe>
          <iframe id="unavailable-frame" src="data:text/html,%3Cp%3Einaccessible-frame-content%3C%2Fp%3E"></iframe>
          <script>
            window.semanticScriptSentinel = 'top-script-value';
            document.getElementById('semantic-action').addEventListener('click', function () {
              document.getElementById('semantic-result').textContent = 'semantic action complete';
            });
          </script>
        </body>
      </html>`,
    '/semantic/same-child': `<!doctype html>
      <html lang="en"><body>
        <p id="same-content">same-origin child content</p>
        <iframe id="nested-frame" src="http://127.0.0.1:${pagePort}/semantic/nested"></iframe>
      </body></html>`,
    '/semantic/nested': `<!doctype html>
      <html lang="en"><body><p id="nested-content">recursive nested frame content</p></body></html>`,
  };
}

function crossOriginFixtureRoutes(): Record<string, string> {
  return {
    '/cross-child': `<!doctype html>
      <html lang="en"><body>
        <p id="cross-content">cross-origin child content</p>
        <input type="password" value="cross-frame-password-value">
        <script>window.crossFrameScriptSentinel = 'cross-script-value';</script>
      </body></html>`,
  };
}

function killStaticServer(proc: ChildProcess): Promise<void> {
  return new Promise((resolve) => {
    const server = (proc as any).__e2eServer as ReturnType<typeof createHttpServer> | undefined;
    if (server) {
      server.close(() => {
        proc.kill();
        resolve();
      });
    } else {
      proc.kill();
      resolve();
    }
  });
}

function buildUserscriptInitScript(userScriptPath: string, workerApiKey: string, serverUrl: string): string {
  const userScriptSource = fs.readFileSync(userScriptPath, 'utf8');
  return `
    (function () {
      const WORKER_API_KEY = ${JSON.stringify(workerApiKey)};

      window.__TRUSTED_SERVER_ORIGINS__ = [${JSON.stringify(serverUrl)}];

      window.GM_xmlhttpRequest = function (opts) {
        const headers = Object.assign({}, opts.headers || {}, {
          Authorization: 'Bearer ' + WORKER_API_KEY,
        });
        fetch(opts.url, {
          method: opts.method || 'GET',
          headers: headers,
          body: opts.data,
        })
          .then(async (res) => {
            const responseText = await res.text();
            if (typeof opts.onload === 'function') {
              opts.onload({ status: res.status, statusText: res.statusText, responseText });
            }
          })
          .catch((err) => {
            if (typeof opts.onerror === 'function') opts.onerror(err);
          });
      };

      window.GM_getValue = function () { return undefined; };
      window.GM_setValue = function () {};
      window.GM_openInTab = function (url) { window.open(url, '_blank'); };
      window.unsafeWindow = window;

      ${userScriptSource}
    })();
  `;
}

async function openExtensionPopup(context: BrowserContext, extensionId: string): Promise<Page> {
  const popupPage = await context.newPage();
  await popupPage.goto(`chrome-extension://${extensionId}/popup.html`);
  return popupPage;
}

async function configureExtensionViaPopup(popupPage: Page, baseUrl: string): Promise<void> {
  await popupPage.evaluate(
    (config) => {
      const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement | null;
      const apiKeyInput = document.getElementById('apiKey') as HTMLInputElement | null;
      const adminApiKeyInput = document.getElementById('adminApiKey') as HTMLInputElement | null;
      if (baseUrlInput) baseUrlInput.value = config.baseUrl;
      if (apiKeyInput) apiKeyInput.value = config.apiKey;
      if (adminApiKeyInput) adminApiKeyInput.value = config.adminApiKey;

      const saveBtn = document.getElementById('save-config');
      if (!saveBtn) throw new Error('save-config button not found');
      saveBtn.click();
    },
    {
      baseUrl,
      apiKey: WORKER_API_KEY,
      adminApiKey: ADMIN_API_KEY,
    },
  );
  // Give the background script a moment to persist the config.
  await new Promise((r) => setTimeout(r, 300));
}

async function clickPopupButton(popupPage: Page, testPage: Page, buttonId: string): Promise<void> {
  // Keep the test page as the active tab so the background service worker
  // sees the correct tab when START/STOP_RECORDING queries the active tab.
  await testPage.bringToFront();
  await popupPage.evaluate((id) => {
    const btn = document.getElementById(id);
    if (!btn) throw new Error(`popup button ${id} not found`);
    btn.click();
  }, buttonId);
  // Give the background script a moment to process the message.
  await new Promise((r) => setTimeout(r, 300));
}

function walkDom(node: DomNode | undefined, visit: (node: DomNode) => void): void {
  if (!node) return;
  visit(node);
  node.children?.forEach((child) => walkDom(child, visit));
}

async function getLastRecording(popupPage: Page): Promise<PageAgentRecording | null> {
  const response = (await popupPage.evaluate(async () => {
    return new Promise((resolve) => {
      (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_LAST_RECORDING' }, resolve);
    });
  })) as { recording?: PageAgentRecording | null };
  return response.recording ?? null;
}

async function sendExtensionAction(
  popupPage: Page,
  testPage: Page,
  action: 'START_RECORDING' | 'STOP_RECORDING',
): Promise<Record<string, unknown>> {
  await testPage.bringToFront();
  return popupPage.evaluate(async (requestedAction) => {
    return (globalThis as any).chrome.runtime.sendMessage({ action: requestedAction });
  }, action) as Promise<Record<string, unknown>>;
}

async function waitForPersistedRecording(popupPage: Page, timeoutMs = 10000): Promise<PageAgentRecording> {
  const deadline = Date.now() + timeoutMs;
  let recording: PageAgentRecording | null = null;
  while (Date.now() < deadline) {
    recording = await getLastRecording(popupPage);
    if (recording?.meta.serverRecordingId) return recording;
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  throw new Error(`semantic recording was not persisted: ${JSON.stringify(recording?.termination ?? null)}`);
}

async function runSemanticRecordingWorkflow(
  popupPage: Page,
  testPage: Page,
  baseUrl: string,
  semanticUrl: string,
): Promise<void> {
  await testPage.goto(semanticUrl);
  await testPage.waitForSelector('#semantic-action');
  await testPage.frameLocator('#same-frame').locator('#same-content').waitFor();
  await testPage.frameLocator('#same-frame').frameLocator('#nested-frame').locator('#nested-content').waitFor();
  await testPage.frameLocator('#cross-frame').locator('#cross-content').waitFor();

  const started = await sendExtensionAction(popupPage, testPage, 'START_RECORDING');
  if (started.success !== true || started.protocolVersion !== '2.0.0') {
    throw new Error(`semantic recording did not start: ${JSON.stringify(started)}`);
  }
  await testPage.click('#semantic-action');
  await testPage.waitForSelector('#semantic-result:text("semantic action complete")');
  const stopped = await sendExtensionAction(popupPage, testPage, 'STOP_RECORDING');
  if (stopped.success !== true) {
    throw new Error(`semantic recording did not stop: ${JSON.stringify(stopped)}`);
  }
  console.log('[e2e-browser] semantic stop response:', JSON.stringify({
    persistenceWarning: stopped.persistenceWarning,
    hasRecording: Boolean(stopped.recording),
    version: (stopped.recording as PageAgentRecording | undefined)?.version,
    termination: (stopped.recording as PageAgentRecording | undefined)?.termination,
    snapshotPhases: (stopped.recording as PageAgentRecording | undefined)?.snapshots?.map((snapshot) => snapshot.phase),
    serverRecordingId: (stopped.recording as PageAgentRecording | undefined)?.meta?.serverRecordingId,
  }));
  const recording = (stopped.recording as PageAgentRecording | undefined)?.meta?.serverRecordingId
    ? stopped.recording as PageAgentRecording
    : await waitForPersistedRecording(popupPage);

  if (recording.version !== '2.0.0') {
    throw new Error(`expected semantic recording v2, received ${recording.version}`);
  }
  const phases = recording.snapshots.map((snapshot) => snapshot.phase);
  if (JSON.stringify(phases) !== JSON.stringify(['initial', 'before-action', 'final'])) {
    throw new Error(`unexpected semantic snapshot phases: ${JSON.stringify(phases)}`);
  }
  if (!recording.termination?.complete || recording.termination.reason !== 'user') {
    throw new Error(`semantic recording did not stop cleanly: ${JSON.stringify(recording.termination)}`);
  }

  const serialized = JSON.stringify(recording);
  for (const forbidden of [
    'semantic-password-value',
    'semantic-token-value',
    'semantic-inline-token',
    'alice.fixture@example.test',
    'cross-query-value',
    'cross-frame-password-value',
    'top-script-value',
    'cross-script-value',
  ]) {
    if (serialized.includes(forbidden)) {
      throw new Error(`semantic recording exposed forbidden fixture value: ${forbidden}`);
    }
  }
  for (const expected of [
    'same-origin child content',
    'recursive nested frame content',
    'cross-origin child content',
  ]) {
    if (!serialized.includes(expected)) {
      throw new Error(`semantic recording omitted iframe content: ${expected}`);
    }
  }

  const initial = recording.snapshots.find((snapshot) => snapshot.phase === 'initial');
  const beforeAction = recording.snapshots.find((snapshot) => snapshot.phase === 'before-action');
  const final = recording.snapshots.find((snapshot) => snapshot.phase === 'final');
  if (!JSON.stringify(beforeAction?.domTree).includes('semantic action pending')) {
    throw new Error('pre-action snapshot was not captured before the page click handler');
  }
  if (!JSON.stringify(final?.domTree).includes('semantic action complete')) {
    throw new Error('final snapshot did not preserve the post-action DOM');
  }

  const frameReports = initial?.capture?.frames ?? [];
  const capturedCrossOrigin = frameReports.some(
    (frame) => frame.url.includes(`127.0.0.1`) && frame.url.includes('/cross-child') && frame.status === 'captured',
  );
  const explicitlyUnavailable = frameReports.some(
    (frame) => frame.status !== 'captured' && Boolean(frame.error),
  );
  if (!capturedCrossOrigin) throw new Error(`cross-origin frame was not captured: ${JSON.stringify(frameReports)}`);
  if (!explicitlyUnavailable) throw new Error(`inaccessible frame was not reported: ${JSON.stringify(frameReports)}`);

  const iframeBoundaries: DomNode[] = [];
  walkDom(initial?.domTree, (node) => {
    if (node.tagName === 'iframe') iframeBoundaries.push(node);
  });
  if (!iframeBoundaries.some((node) => node.frameOrigin === 'same-origin' && node.frameStatus === 'captured')) {
    throw new Error('same-origin iframe boundary was not marked captured');
  }
  if (!iframeBoundaries.some((node) => node.frameOrigin === 'cross-origin' && node.frameStatus === 'captured')) {
    throw new Error('cross-origin iframe boundary was not marked captured');
  }
  if (!iframeBoundaries.some((node) => node.frameStatus === 'unavailable' || node.frameStatus === 'error')) {
    throw new Error('inaccessible iframe boundary did not retain an explicit status marker');
  }

  const persistedResponse = await fetch(
    `${baseUrl}/api/v1/recordings/${encodeURIComponent(recording.meta.serverRecordingId!)}`,
    { headers: { Authorization: `Bearer ${ADMIN_API_KEY}` } },
  );
  if (!persistedResponse.ok) {
    throw new Error(`persisted semantic recording could not be read: ${persistedResponse.status} ${await persistedResponse.text()}`);
  }
  const persisted = await persistedResponse.json() as { recording?: { payload?: PageAgentRecording } };
  if (persisted.recording?.payload?.version !== '2.0.0') {
    throw new Error('server did not reconstruct the persisted semantic recording');
  }
  console.log('[e2e-browser] semantic recording v2 iframe and redaction checks passed');
}

async function enrichRuleWithExtractStep(baseUrl: string, ruleId: string): Promise<void> {
  const rule = (await workerGet(baseUrl, `/rules/${encodeURIComponent(ruleId)}`)) as {
    id: string;
    version: string;
    steps: Array<Record<string, unknown>>;
  };

  rule.steps.push(
    { action: 'waitForTimeout', ms: 500 },
    { action: 'extractText', name: 'result', target: { selector: '#result' } },
    { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true },
    { action: 'updateStatus', status: 'done', message: 'E2E browser test completed' },
  );

  // POST /admin/rules rejects existing IDs (RULE_EXISTS id-overwrite guard);
  // update the seeded rule through its PATCH endpoint instead.
  const patch = await fetch(`${baseUrl}/admin/rules/${encodeURIComponent(rule.id)}`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${ADMIN_API_KEY}`,
    },
    body: JSON.stringify(rule),
  });
  if (!patch.ok) {
    throw new Error(`admin PATCH /admin/rules/${rule.id} failed: ${patch.status} ${await patch.text()}`);
  }
}

async function openIntentPage(context: BrowserContext, extensionId: string): Promise<Page> {
  // In the Playwright E2E environment, chrome.tabs.create from the popup sometimes
  // opens a tab without a fully initialized chrome.runtime API. Open the wizard
  // page directly via chrome-extension:// URL to ensure the extension context is
  // available, and close any auto-created tabs to avoid interference.
  const intentPage = await context.newPage();
  await intentPage.goto(`chrome-extension://${extensionId}/intent/intent-page.html`);
  intentPage.on('console', (msg) => {
    console.log(`[intent-page-console:${msg.type()}] ${msg.text()}`);
  });
  intentPage.on('pageerror', (err) => {
    console.error('[intent-page-error]', err.message);
  });
  return intentPage;
}

async function dumpIntentPageState(intentPage: Page): Promise<void> {
  const body = await intentPage.locator('body').innerHTML().catch((e) => `failed to get body: ${e.message}`);
  const status = await intentPage.locator('#status').textContent().catch(() => 'no status');
  console.log('[e2e-browser-intent] status text:', status);
  console.log('[e2e-browser-intent] body html:', body.slice(0, 2000));
}

function attachPageLogging(page: Page, label: string): void {
  page.on('console', (msg) => {
    console.log(`[${label}-console:${msg.type()}] ${msg.text()}`);
  });
  page.on('pageerror', (err) => {
    console.error(`[${label}-pageerror]`, err.message);
  });
}

async function dumpReplayStage(intentPage: Page): Promise<void> {
  const stage = await intentPage.locator('#replay-stage').innerHTML().catch((e) => `failed to get replay stage: ${e.message}`);
  const status = await intentPage.locator('.replay-status').textContent().catch(() => 'no replay status');
  console.log('[e2e-browser-intent] replay stage status:', status);
  console.log('[e2e-browser-intent] replay stage html:', stage.slice(0, 3000));
}

async function runIntentWorkflow(
  context: BrowserContext,
  popupPage: Page,
  testPage: Page,
  baseUrl: string,
  extensionId: string,
): Promise<string> {
  // Start recording, interact, and stop; the popup will open the intent wizard.
  await clickPopupButton(popupPage, testPage, 'start-recording');
  console.log('[e2e-browser-intent] recording started');

  await testPage.click('#action-button');
  await new Promise((r) => setTimeout(r, 300));
  console.log('[e2e-browser-intent] button clicked');

  await clickPopupButton(popupPage, testPage, 'stop-recording');
  console.log('[e2e-browser-intent] recording stopped');

  // Log console messages from every new page (including replay tabs) to aid
  // debugging hangs in the real-browser replay.
  context.on('page', async (page) => {
    const url = page.url();
    if (url.includes('/intent/intent-page.html')) {
      await page.close().catch(() => undefined);
      return;
    }
    attachPageLogging(page, `replay-tab-${Date.now()}`);
  });

  // Give the background script time to persist the recording, then open the
  // wizard page directly in a properly initialized extension context.
  await new Promise((r) => setTimeout(r, 500));
  const intentPage = await openIntentPage(context, extensionId);
  console.log('[e2e-browser-intent] intent page opened');

  // Wait for the fallback intent predictions to load.
  try {
    await intentPage.waitForSelector('#step-intent:not(.hidden)', { timeout: 10000 });
    await intentPage.waitForSelector('#candidates-list input[name="intent"]', { timeout: 10000 });
  } catch (err) {
    await dumpIntentPageState(intentPage);
    const screenshotPath = path.join(os.tmpdir(), `opencrawler-e2e-intent-failure-${Date.now()}.png`);
    await intentPage.screenshot({ path: screenshotPath }).catch(() => undefined);
    console.log(`[e2e-browser-intent] screenshot saved: ${screenshotPath}`);
    throw err;
  }
  console.log('[e2e-browser-intent] predictions loaded');

  // Select the first candidate and proceed to preview.
  await intentPage.locator('#candidates-list input[name="intent"]').first().check();
  const selectedLabel = await intentPage.locator('#candidates-list input[name="intent"]:checked').first()
    .locator('..').textContent().catch(() => 'unknown');
  console.log('[e2e-browser-intent] selected candidate:', (selectedLabel ?? 'unknown').slice(0, 200));
  await intentPage.locator('#action-primary').click();

  // Wait for the preview step and proceed to replay confirmation.
  await intentPage.waitForSelector('#step-preview:not(.hidden)', { timeout: 10000 });
  await intentPage.waitForSelector('#wizard-actions', { state: 'visible', timeout: 10000 });
  console.log('[e2e-browser-intent] wizard actions bar is visible on preview step');
  const yamlPreview = await intentPage.locator('#yaml-preview').textContent();
  console.log('[e2e-browser-intent] generated YAML preview:\n', yamlPreview?.slice(0, 2000));
  await intentPage.locator('#action-primary').click();

  // Wait for the replay step, then wait for real replay completion before confirming.
  await intentPage.waitForSelector('#step-replay:not(.hidden)', { timeout: 10000 });
  try {
    await intentPage.waitForFunction(
      () => {
        const status = document.querySelector('.replay-status');
        return status && status.textContent?.includes('回放成功');
      },
      undefined,
      { timeout: 60000 },
    );
  } catch (err) {
    await dumpReplayStage(intentPage);
    const screenshotPath = path.join(os.tmpdir(), `opencrawler-e2e-replay-failure-${Date.now()}.png`);
    await intentPage.screenshot({ path: screenshotPath }).catch(() => undefined);
    console.log(`[e2e-browser-intent] screenshot saved: ${screenshotPath}`);
    throw err;
  }
  await intentPage.locator('#action-primary').click();

  // Confirm all steps and save.
  await intentPage.waitForSelector('#step-confirm:not(.hidden)', { timeout: 10000 });
  await intentPage.locator('#confirm-all').check();
  await intentPage.locator('#action-primary').click();

  // Wait for the save step and extract the saved rule id.
  await intentPage.waitForSelector('#step-save:not(.hidden)', { timeout: 10000 });
  const saveResult = await intentPage.locator('#save-result').textContent();
  if (!saveResult) {
    throw new Error('save result text is empty');
  }
  const match = saveResult.match(/ID[：:]\s*(\S+)/);
  if (!match) {
    throw new Error(`could not parse rule id from save result: ${saveResult}`);
  }
  const ruleId = match[1];
  console.log(`[e2e-browser-intent] rule saved: ${ruleId}`);

  // Approve the rule so it can be used to create tasks.
  await adminRequest(baseUrl, `/admin/rules/${encodeURIComponent(ruleId)}/approve`, {});
  console.log('[e2e-browser-intent] rule approved');

  // Create a task that overrides a keyword variable.
  const createTaskResp = (await adminRequest(baseUrl, '/admin/tasks', {
    ruleId,
    ruleVersion: '1.0.0',
    variables: { keyword: 'e2e-test' },
  })) as { taskId: string };
  console.log(`[e2e-browser-intent] task created: ${createTaskResp.taskId}`);

  return ruleId;
}

async function runSearchWorkflow(
  context: BrowserContext,
  popupPage: Page,
  testPage: Page,
  baseUrl: string,
  extensionId: string,
): Promise<string> {
  await clickPopupButton(popupPage, testPage, 'start-recording');
  console.log('[e2e-browser-search] recording started');

  await testPage.fill('#search-input', 'opencrawler');
  await testPage.click('#search-button');
  await testPage.waitForURL((url) => url.searchParams.has('q'), { timeout: 10000 });
  await new Promise((r) => setTimeout(r, 300));
  console.log('[e2e-browser-search] search submitted');

  await clickPopupButton(popupPage, testPage, 'stop-recording');
  const lastRecording = (await popupPage.evaluate(async () => {
    return new Promise((resolve) => {
      (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_LAST_RECORDING' }, resolve);
    });
  })) as { recording?: { events: unknown[]; snapshots: unknown[]; meta: Record<string, unknown> } };
  console.log('[e2e-browser-search] raw recording events:', JSON.stringify(lastRecording.recording?.events));
  console.log('[e2e-browser-search] raw recording snapshots:', JSON.stringify(lastRecording.recording?.snapshots.map((s: any) => ({ timestamp: s.timestamp, url: s.url, selectorCount: Object.keys(s.selectorMap || {}).length }))));
  console.log('[e2e-browser-search] recording stopped');

  context.on('page', async (page) => {
    const url = page.url();
    if (url.includes('/intent/intent-page.html')) {
      await page.close().catch(() => undefined);
    }
  });

  await new Promise((r) => setTimeout(r, 500));
  const intentPage = await openIntentPage(context, extensionId);
  console.log('[e2e-browser-search] intent page opened');

  try {
    await intentPage.waitForSelector('#step-intent:not(.hidden)', { timeout: 10000 });
    await intentPage.waitForSelector('#candidates-list input[name="intent"]', { timeout: 10000 });
  } catch (err) {
    await dumpIntentPageState(intentPage);
    const screenshotPath = path.join(os.tmpdir(), `opencrawler-e2e-search-failure-${Date.now()}.png`);
    await intentPage.screenshot({ path: screenshotPath }).catch(() => undefined);
    console.log(`[e2e-browser-search] screenshot saved: ${screenshotPath}`);
    throw err;
  }
  console.log('[e2e-browser-search] predictions loaded');

  // Prefer the search-pagination candidate if available; otherwise use the custom description.
  const searchLabel = await intentPage.locator('#candidates-list label').filter({ hasText: /搜索|search/i }).first();
  if (await searchLabel.count() > 0) {
    await searchLabel.locator('input[name="intent"]').check();
  } else {
    await intentPage.locator('#custom-description').fill('搜索关键词并采集结果');
  }
  const searchSelectedLabel = await intentPage.locator('#candidates-list input[name="intent"]:checked').first()
    .locator('..').textContent().catch(() => 'unknown');
  console.log('[e2e-browser-search] selected candidate:', (searchSelectedLabel ?? 'unknown').slice(0, 200));
  await intentPage.locator('#action-primary').click();

  await intentPage.waitForSelector('#step-preview:not(.hidden)', { timeout: 10000 });
  const yamlPreview = await intentPage.locator('#yaml-preview').textContent();
  console.log('[e2e-browser-search] generated YAML preview:\n', yamlPreview?.slice(0, 3000));
  if (!yamlPreview?.includes('{{keyword}}')) {
    throw new Error('generated DSL does not include {{keyword}} variable');
  }
  if (!yamlPreview?.includes('searchResults')) {
    throw new Error('generated DSL does not include searchResults extraction');
  }
  console.log('[e2e-browser-search] DSL preview contains search results extraction');

  await intentPage.locator('#action-primary').click();

  await intentPage.waitForSelector('#step-replay:not(.hidden)', { timeout: 10000 });
  await intentPage.waitForFunction(
    () => {
      const status = document.querySelector('.replay-status');
      return status && status.textContent?.includes('回放成功');
    },
    undefined,
    { timeout: 60000 },
  );
  await intentPage.locator('#action-primary').click();

  await intentPage.waitForSelector('#step-confirm:not(.hidden)', { timeout: 10000 });
  await intentPage.locator('#confirm-all').check();
  await intentPage.locator('#action-primary').click();

  await intentPage.waitForSelector('#step-save:not(.hidden)', { timeout: 10000 });
  const saveResult = await intentPage.locator('#save-result').textContent();
  if (!saveResult) {
    throw new Error('save result text is empty');
  }
  const match = saveResult.match(/ID[：:]\s*(\S+)/);
  if (!match) {
    throw new Error(`could not parse rule id from save result: ${saveResult}`);
  }
  const ruleId = match[1];
  console.log(`[e2e-browser-search] rule saved: ${ruleId}`);

  await adminRequest(baseUrl, `/admin/rules/${encodeURIComponent(ruleId)}/approve`, {});
  console.log('[e2e-browser-search] rule approved');

  const createTaskResp = (await adminRequest(baseUrl, '/admin/tasks', {
    ruleId,
    ruleVersion: '1.0.0',
    variables: { keyword: 'e2e-test' },
  })) as { taskId: string };
  console.log(`[e2e-browser-search] task created: ${createTaskResp.taskId}`);

  return ruleId;
}

async function main(): Promise<void> {
  const serverPort = await getFreePort();
  let pagePort = await getFreePort();
  while (pagePort === serverPort) {
    pagePort = await getFreePort();
  }
  let crossOriginPort = await getFreePort();
  while (crossOriginPort === serverPort || crossOriginPort === pagePort) {
    crossOriginPort = await getFreePort();
  }
  const baseUrl = `http://127.0.0.1:${serverPort}`;
  const pageUrl = `http://127.0.0.1:${pagePort}`;
  const serverDir = path.resolve(__dirname, '..', 'server');
  const dbPath = path.join(os.tmpdir(), `opencrawler-e2e-browser-${Date.now()}.db`);
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  const fixturePath = path.resolve(__dirname, 'fixtures', 'test-page.html');
  const distDir = path.resolve(__dirname, '..', 'dist');
  const userScriptFiles = fs.readdirSync(distDir).filter((f) => f.endsWith('.user.js'));
  if (userScriptFiles.length === 0) {
    throw new Error(`userscript not found in ${distDir}; run npm run build:userscript first`);
  }
  const userScriptPath = path.resolve(distDir, userScriptFiles[0]);

  if (!fs.existsSync(fixturePath)) {
    throw new Error(`test fixture not found at ${fixturePath}`);
  }

  console.log(`[e2e-browser] server port ${serverPort}, page port ${pagePort}, db ${dbPath}`);

  await buildServer(serverDir);
  const serverProc = startServer(serverDir, dbPath, serverPort, { FEATURE_RECORDING_V2: 'true' });
  const staticProc = startStaticServer(fixturePath, pagePort, semanticFixtureRoutes(pagePort, crossOriginPort));
  const crossOriginProc = startStaticServer(fixturePath, crossOriginPort, crossOriginFixtureRoutes());

  let context: BrowserContext | undefined;

  try {
    await waitForHealth(baseUrl);
    console.log('[e2e-browser] server healthy');

    const extensionDir = path.resolve(__dirname, '..', 'dist', 'extension');
    const extensionDirArg = extensionDir.replace(/\\/g, '/');
    const userDataDir = path.join(os.tmpdir(), `opencrawler-e2e-browser-profile-${Date.now()}`);

    context = await chromium.launchPersistentContext(userDataDir, {
      headless: false,
      args: [
        `--disable-extensions-except=${extensionDirArg}`,
        `--load-extension=${extensionDirArg}`,
        '--disable-web-security',
        '--allow-file-access-from-files',
      ],
    });

    // Wait for the extension service worker / background page to be available.
    let backgroundPage: Page | undefined;
    const workerDeadline = Date.now() + 10000;
    while (Date.now() < workerDeadline) {
      const backgroundPages = context.backgroundPages();
      const serviceWorkers = context.serviceWorkers();
      backgroundPage = backgroundPages[0] ?? serviceWorkers[0];
      if (backgroundPage) break;
      await new Promise((r) => setTimeout(r, 500));
    }
    if (!backgroundPage) {
      throw new Error('extension background page or service worker not found');
    }
    const extensionId = await backgroundPage.evaluate(() => (globalThis as any).chrome.runtime.id);
    console.log(`[e2e-browser] extension id ${extensionId}`);

    // Open the test page and the extension popup.
    const testPage = await context.newPage();
    testPage.on('console', (msg) => {
      console.log(`[browser-console:${msg.type()}] ${msg.text()}`);
    });
    testPage.on('pageerror', (err) => {
      console.error('[browser-pageerror]', err.message);
    });
    await testPage.goto(pageUrl);
    await testPage.waitForSelector('#action-button');
    console.log('[e2e-browser] test page loaded');

    const popupPage = await openExtensionPopup(context, extensionId);
    await popupPage.waitForSelector('#start-recording');
    console.log('[e2e-browser] popup opened');

    // Configure the extension to talk to our test server.
    await configureExtensionViaPopup(popupPage, baseUrl);
    console.log('[e2e-browser] extension server config saved');

    await runSemanticRecordingWorkflow(popupPage, testPage, baseUrl, `${pageUrl}/semantic`);

    await testPage.goto(pageUrl);
    await testPage.waitForSelector('#action-button');

    // Run the intent-prediction workflow end-to-end.
    const ruleId = await runIntentWorkflow(context, popupPage, testPage, baseUrl, extensionId);
    console.log('[e2e-browser] intent workflow passed');

    // Run a search-style workflow to verify form submission, navigation,
    // and search-results extraction work end-to-end.
    await testPage.goto(pageUrl);
    await testPage.waitForSelector('#search-input');
    const searchRuleId = await runSearchWorkflow(context, popupPage, testPage, baseUrl, extensionId);
    console.log('[e2e-browser] search workflow passed');

    // The saved rule only contains a click step. Enrich it with an extract
    // step and an explicit status update so we can verify end-to-end results.
    await enrichRuleWithExtractStep(baseUrl, ruleId);
    console.log('[e2e-browser] rule enriched with extract step');

    // Create a fresh task for the enriched rule.
    const createTaskResp = (await adminRequest(baseUrl, '/admin/tasks', {
      ruleId,
      ruleVersion: '1.0.0',
      variables: {},
    })) as { taskId: string };
    const taskId = createTaskResp.taskId;
    console.log(`[e2e-browser] task created: ${taskId}`);
    await leaseTaskForBrowserWorker(baseUrl, taskId);
    console.log(`[e2e-browser] task leased to ${WORKER_ID}: ${taskId}`);

    // Inject the userscript and navigate to the test page with a task hash so
    // the executor boots and runs the rule in the real browser.
    const taskHash =
      '#scrape=' +
      Buffer.from(
        JSON.stringify({
          taskId,
          ruleId,
          serverUrl: baseUrl,
          workerId: WORKER_ID,
          variables: {},
        }),
      ).toString('base64');

    const initScript = buildUserscriptInitScript(userScriptPath, WORKER_API_KEY, baseUrl);
    await testPage.addInitScript(initScript);
    // Add a query parameter to force a full document navigation (a hash-only
    // change may not re-run init scripts).
    await testPage.goto(`${pageUrl}?exec=1${taskHash}`);
    console.log('[e2e-browser] userscript injected, waiting for task completion');

    let task: TaskResponse;
    try {
      task = await waitForTaskDone(baseUrl, taskId, 60000);
    } catch (err) {
      const currentTask = (await workerGet(baseUrl, `/tasks/${encodeURIComponent(taskId)}`)) as TaskResponse;
      console.error('[e2e-browser] task did not complete; current status:', currentTask.status);
      throw err;
    }
    console.log(`[e2e-browser] task status: ${task.status}`);

    const results = (await workerGet(baseUrl, `/tasks/${encodeURIComponent(taskId)}/results`)) as ResultResponse[];
    const hasResult = results.some((r) => {
      const payload = r.payload;
      return payload && typeof payload === 'object' && JSON.stringify(payload).includes('Hello from AegisCrawler');
    });

    if (!hasResult) {
      throw new Error(`result payloads do not contain expected text: ${JSON.stringify(results.map((r) => r.payload))}`);
    }

    console.log('[e2e-browser] result payload contains expected text');

    // Verify the search rule end-to-end with a different keyword variable.
    const searchTaskResp = (await adminRequest(baseUrl, '/admin/tasks', {
      ruleId: searchRuleId,
      ruleVersion: '1.0.0',
      variables: { keyword: 'search-test' },
    })) as { taskId: string };
    const searchTaskId = searchTaskResp.taskId;
    console.log(`[e2e-browser] search task created: ${searchTaskId}`);
    await leaseTaskForBrowserWorker(baseUrl, searchTaskId);
    console.log(`[e2e-browser] search task leased to ${WORKER_ID}: ${searchTaskId}`);

    const searchTaskHash =
      '#scrape=' +
      Buffer.from(
        JSON.stringify({
          taskId: searchTaskId,
          ruleId: searchRuleId,
          serverUrl: baseUrl,
          workerId: WORKER_ID,
          variables: { keyword: 'search-test' },
        }),
      ).toString('base64');

    await testPage.addInitScript(initScript);
    await testPage.goto(`${pageUrl}?exec=2${searchTaskHash}`);
    console.log('[e2e-browser] userscript injected for search task, waiting for task completion');

    let searchTask: TaskResponse;
    try {
      searchTask = await waitForTaskDone(baseUrl, searchTaskId, 60000);
    } catch (err) {
      const currentTask = (await workerGet(baseUrl, `/tasks/${encodeURIComponent(searchTaskId)}`)) as TaskResponse;
      console.error('[e2e-browser] search task did not complete; current status:', currentTask.status);
      throw err;
    }
    console.log(`[e2e-browser] search task status: ${searchTask.status}`);

    const searchResults = (await workerGet(baseUrl, `/tasks/${encodeURIComponent(searchTaskId)}/results`)) as ResultResponse[];
    const hasSearchResult = searchResults.some((r) => {
      const payload = r.payload;
      return payload && typeof payload === 'object' && JSON.stringify(payload).includes('search-test');
    });

    if (!hasSearchResult) {
      throw new Error(`search result payloads do not contain expected text: ${JSON.stringify(searchResults.map((r) => r.payload))}`);
    }

    console.log('[e2e-browser] search result payload contains expected text');
    console.log('[e2e-browser] real browser E2E passed');
  } finally {
    await context?.close().catch(() => {});
    await killStaticServer(staticProc);
    await killStaticServer(crossOriginProc);
    await killServer(serverProc);
    removeIfExists(dbPath);
    removeIfExists(binaryPath);
  }
}

if (require.main === module) {
  main().catch((err) => {
    console.error('[e2e-browser] real browser E2E failed:', err);
    process.exit(1);
  });
}
