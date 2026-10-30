import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { createServer, Server } from 'http';
import { chromium, BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../src/rule-generator/types';
import {
  ADMIN_API_KEY,
  WORKER_API_KEY,
  VARIABLE_ENCRYPTION_KEY,
  buildServer,
  getFreePort,
  killServer,
  startServer,
  waitForHealth,
} from './e2e-deployment-test';

function startFixtureServer(port: number): Promise<Server> {
  const sourceUrl = `http://127.0.0.1:${port}/cross-tab/source`;
  const detailUrl = `http://127.0.0.1:${port}/cross-tab/detail`;
  const pages: Record<string, string> = {
    '/cross-tab/source': `<!doctype html>
      <html lang="en"><head><title>Cross-tab source fixture</title></head>
      <body>
        <h1>Source listing</h1>
        <a id="open-detail" href="${detailUrl}" target="_blank" rel="noopener">Open detail in a new tab</a>
        <button id="continue-source" type="button">Continue on source</button>
        <p id="source-result">source action pending</p>
        <script>
          document.getElementById('continue-source').addEventListener('click', () => {
            document.getElementById('source-result').textContent = 'source action complete';
          });
        </script>
      </body></html>`,
    '/cross-tab/detail': `<!doctype html>
      <html lang="en"><head><title>Cross-tab detail fixture</title></head>
      <body>
        <h1>Detail page</h1>
        <button id="complete-detail" type="button">Complete detail</button>
        <p id="detail-result">detail action pending</p>
        <script>
          document.getElementById('complete-detail').addEventListener('click', () => {
            document.getElementById('detail-result').textContent = 'detail action complete';
          });
        </script>
      </body></html>`,
  };

  const server = createServer((req, res) => {
    const pathname = new URL(req.url ?? '/', `http://127.0.0.1:${port}`).pathname;
    const body = pages[pathname];
    if (!body) {
      res.writeHead(404);
      res.end('not found');
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(body);
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve(server));
  });
}

async function sendRecordingAction(popup: Page, pageToActivate: Page, action: 'START_RECORDING' | 'STOP_RECORDING') {
  await pageToActivate.bringToFront();
  return popup.evaluate(async (requestedAction) =>
    (globalThis as any).chrome.runtime.sendMessage({ action: requestedAction }), action);
}

async function getLastRecording(popup: Page): Promise<PageAgentRecording | null> {
  const response = await popup.evaluate(async () =>
    (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_LAST_RECORDING' })) as {
      recording?: PageAgentRecording | null;
    };
  return response.recording ?? null;
}

async function waitForRecording(popup: Page): Promise<PageAgentRecording> {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    const recording = await getLastRecording(popup);
    if (recording?.meta.serverRecordingId) return recording;
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  throw new Error('cross-tab recording was not persisted by the local server');
}

function assertRecording(recording: PageAgentRecording, sourceUrl: string, detailUrl: string): void {
  if (recording.version !== '2.0.0') {
    throw new Error(`expected recording v2, got ${recording.version}`);
  }
  if (!recording.termination?.complete || recording.termination.reason !== 'user') {
    throw new Error(`recording did not stop cleanly: ${JSON.stringify(recording.termination)}`);
  }

  const events = recording.events;
  const clickEvents = events.filter((event) => event.type === 'click');
  if (clickEvents.length !== 3) {
    throw new Error(`recorder UI actions leaked into the recording: expected 3 page clicks, got ${clickEvents.length}: ${JSON.stringify(clickEvents)}`);
  }
  const newTabClickIndex = events.findIndex((event) =>
    event.type === 'click' && event.opensNewTab === true && event.destinationUrl === detailUrl);
  if (newTabClickIndex < 0) {
    throw new Error(`recording omitted the source link's new-tab destination: ${JSON.stringify(events)}`);
  }

  const destinationTransitionIndex = events.findIndex((event) =>
    event.type === 'navigate' && event.tabTransition === true && event.url === detailUrl);
  if (destinationTransitionIndex <= newTabClickIndex) {
    throw new Error(`destination tab transition was missing or out of order: ${JSON.stringify(events)}`);
  }

  const returnTransitionIndex = events.findIndex((event, index) =>
    index > destinationTransitionIndex && event.type === 'navigate' && event.tabTransition === true && event.url === sourceUrl);
  if (returnTransitionIndex < 0) {
    throw new Error(`return to the source tab was not recorded after the detail tab: ${JSON.stringify(events)}`);
  }

  const clicksAfterDestination = events.slice(destinationTransitionIndex + 1, returnTransitionIndex)
    .filter((event) => event.type === 'click');
  const sourceClicksAfterReturn = events.slice(returnTransitionIndex + 1)
    .filter((event) => event.type === 'click');
  if (clicksAfterDestination.length === 0 || sourceClicksAfterReturn.length === 0) {
    throw new Error(`actions were not recorded on both tabs: ${JSON.stringify(events)}`);
  }

  const snapshotUrls = recording.snapshots.map((snapshot) => snapshot.url);
  if (!snapshotUrls.some((url) => url === sourceUrl) || !snapshotUrls.some((url) => url === detailUrl)) {
    throw new Error(`recording snapshots did not retain both tab URLs: ${JSON.stringify(snapshotUrls)}`);
  }
}

async function main(): Promise<void> {
  const serverPort = await getFreePort();
  const pagePort = await getFreePort();
  const baseUrl = `http://127.0.0.1:${serverPort}`;
  const sourceUrl = `http://127.0.0.1:${pagePort}/cross-tab/source`;
  const detailUrl = `http://127.0.0.1:${pagePort}/cross-tab/detail`;
  const rootDir = path.resolve(__dirname, '..');
  const serverDir = path.join(rootDir, 'server');
  const dbPath = path.join(os.tmpdir(), `aegis-cross-tab-e2e-${Date.now()}.db`);
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  const extensionDir = path.join(rootDir, 'dist', 'extension').replace(/\\/g, '/');
  const profileDir = path.join(os.tmpdir(), `aegis-cross-tab-profile-${Date.now()}`);
  let serverProc: ReturnType<typeof startServer> | undefined;
  let fixtureServer: Server | undefined;
  let context: BrowserContext | undefined;

  try {
    await buildServer(serverDir);
    serverProc = startServer(serverDir, dbPath, serverPort, { FEATURE_RECORDING_V2: 'true' });
    fixtureServer = await startFixtureServer(pagePort);
    await waitForHealth(baseUrl);

    context = await chromium.launchPersistentContext(profileDir, {
      headless: false,
      args: [
        `--disable-extensions-except=${extensionDir}`,
        `--load-extension=${extensionDir}`,
      ],
    });

    let backgroundPage: Page | undefined;
    const deadline = Date.now() + 10000;
    while (Date.now() < deadline) {
      backgroundPage = context.backgroundPages()[0] ?? context.serviceWorkers()[0];
      if (backgroundPage) break;
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    if (!backgroundPage) throw new Error('extension service worker did not start');
    const extensionId = await backgroundPage.evaluate(() => (globalThis as any).chrome.runtime.id);

    const sourcePage = await context.newPage();
    await sourcePage.goto(sourceUrl);
    await sourcePage.locator('#open-detail').waitFor();

    const popupPage = await context.newPage();
    await popupPage.goto(`chrome-extension://${extensionId}/popup.html`);
    await popupPage.locator('#start-recording').waitFor();
    await popupPage.evaluate((config) => {
      (document.getElementById('baseUrl') as HTMLInputElement).value = config.baseUrl;
      (document.getElementById('apiKey') as HTMLInputElement).value = config.workerKey;
      (document.getElementById('adminApiKey') as HTMLInputElement).value = config.adminKey;
      document.getElementById('save-config')?.click();
    }, { baseUrl, workerKey: WORKER_API_KEY, adminKey: ADMIN_API_KEY });
    await new Promise((resolve) => setTimeout(resolve, 300));

    const started = await sendRecordingAction(popupPage, sourcePage, 'START_RECORDING') as Record<string, unknown>;
    if (started.success !== true || started.protocolVersion !== '2.0.0') {
      throw new Error(`cross-tab recording did not start: ${JSON.stringify(started)}`);
    }

    const detailPagePromise = context.waitForEvent('page');
    await sourcePage.locator('#open-detail').click();
    const detailPage = await detailPagePromise;
    await detailPage.waitForURL(detailUrl);
    await detailPage.locator('#complete-detail').click();
    await detailPage.locator('#detail-result:text("detail action complete")').waitFor();

    await sourcePage.bringToFront();
    await sourcePage.locator('#continue-source').click();
    await sourcePage.locator('#source-result:text("source action complete")').waitFor();
    await new Promise((resolve) => setTimeout(resolve, 500));

    await sourcePage.locator('[data-hud="stop"]').click();
    await sourcePage.locator('[data-aegis-recording-hud]').waitFor({ state: 'detached' });
    const recording = await waitForRecording(popupPage);
    assertRecording(recording, sourceUrl, detailUrl);
    console.log(`[e2e-cross-tab] passed: ${recording.events.length} events, ${recording.snapshots.length} snapshots across source and detail tabs`);
  } finally {
    await context?.close().catch(() => undefined);
    if (fixtureServer) await new Promise<void>((resolve) => fixtureServer!.close(() => resolve()));
    if (serverProc) await killServer(serverProc);
    if (fs.existsSync(profileDir)) {
      const resolvedTemp = path.resolve(os.tmpdir());
      const resolvedProfile = path.resolve(profileDir);
      if (!resolvedProfile.startsWith(`${resolvedTemp}${path.sep}`) || !path.basename(resolvedProfile).startsWith('aegis-cross-tab-profile-')) {
        throw new Error(`refusing to remove unexpected browser profile path: ${resolvedProfile}`);
      }
      fs.rmSync(resolvedProfile, { recursive: true, force: true });
    }
    try { fs.unlinkSync(dbPath); } catch { /* local test cleanup */ }
    try { fs.unlinkSync(binaryPath); } catch { /* local test cleanup */ }
  }
}

main().catch((error) => {
  console.error('[e2e-cross-tab] failed:', error);
  process.exit(1);
});