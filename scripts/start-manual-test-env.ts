import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { spawn, ChildProcess } from 'child_process';
import { chromium, BrowserContext } from 'playwright';
import { createServer } from 'net';
import { extensionChromiumArgs } from './extension-chromium-launch';

const WORKER_API_KEY = 'manual-worker-key-for-testing';
const ADMIN_API_KEY = 'manual-admin-key-for-testing';
const VARIABLE_ENCRYPTION_KEY = 'manual-variable-encryption-key-32bytes!';

function getFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      const port = address && typeof address === 'object' ? address.port : 0;
      server.close(() => resolve(port));
    });
    server.on('error', reject);
  });
}

function buildServer(serverDir: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const proc = spawn('go', ['build', '-o', 'manual-test-server.exe', './cmd/server'], {
      cwd: serverDir,
      stdio: 'pipe',
    });
    let stdout = '';
    let stderr = '';
    proc.stdout?.on('data', (data) => { stdout += String(data); });
    proc.stderr?.on('data', (data) => { stderr += String(data); });
    proc.on('error', reject);
    proc.on('close', (code) => {
      if (code !== 0) {
        reject(new Error(`go build failed (code ${code}): ${stderr || stdout}`));
      } else {
        resolve();
      }
    });
  });
}

function startServer(serverDir: string, dbPath: string, port: number): ChildProcess {
  const binaryPath = path.join(serverDir, 'manual-test-server.exe');
  const proc = spawn(binaryPath, [], {
    cwd: serverDir,
    env: {
      ...process.env,
      LISTEN_ADDR: `127.0.0.1:${port}`,
      DATABASE_PATH: dbPath,
      WORKER_API_KEY,
      ADMIN_API_KEY,
      VARIABLE_ENCRYPTION_KEY,
      REQUIRE_SECURITY_KEYS: 'true',
      LOG_LEVEL: 'info',
      LLM_ENABLED: process.env.LLM_ENABLED ?? 'false',
    },
    stdio: 'inherit',
  });
  proc.unref?.();
  return proc;
}

async function waitForHealth(baseUrl: string, timeoutMs = 15000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseUrl}/health`);
      if (res.ok) return;
      lastError = new Error(`health returned ${res.status}`);
    } catch (err) {
      lastError = err;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`server did not become healthy: ${lastError}`);
}

function buildExtension(): Promise<void> {
  return new Promise((resolve, reject) => {
    const rootDir = path.resolve(__dirname, '..');
    const proc = spawn(process.execPath, [path.join(rootDir, 'scripts', 'build-extension.js')], {
      cwd: rootDir,
      stdio: 'inherit',
    });
    proc.on('error', reject);
    proc.on('close', (code) => {
      if (code !== 0) reject(new Error(`build:extension failed with code ${code}`));
      else resolve();
    });
  });
}

async function getExtensionId(context: BrowserContext): Promise<string> {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    const backgroundPages = context.backgroundPages();
    const serviceWorkers = context.serviceWorkers();
    const backgroundPage = backgroundPages[0] ?? serviceWorkers[0];
    if (backgroundPage) {
      return backgroundPage.evaluate(() => (globalThis as any).chrome.runtime.id);
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('extension background page or service worker not found');
}

async function main(): Promise<void> {
  const serverDir = path.resolve(__dirname, '..', 'server');
  const extensionDir = path.resolve(__dirname, '..', 'dist', 'extension');
  const dbPath = path.join(os.tmpdir(), `opencrawler-manual-${Date.now()}.db`);
  const userDataDir = path.join(os.tmpdir(), `opencrawler-manual-profile-${Date.now()}`);

  console.log('[manual-env] building server...');
  await buildServer(serverDir);

  const port = await getFreePort();
  const baseUrl = `http://127.0.0.1:${port}`;
  console.log(`[manual-env] starting server on ${baseUrl}...`);
  const serverProc = startServer(serverDir, dbPath, port);

  let context: BrowserContext | undefined;

  try {
    await waitForHealth(baseUrl);
    console.log('[manual-env] server healthy');

    console.log('[manual-env] building extension...');
    await buildExtension();

    console.log('[manual-env] launching Chromium with extension...');
    context = await chromium.launchPersistentContext(userDataDir, {
      headless: false,
      args: extensionChromiumArgs(extensionDir, false),
    });

    const extensionId = await getExtensionId(context);
    console.log(`[manual-env] extension id: ${extensionId}`);

    // Configure the extension via the popup page.
    const popupPage = await context.newPage();
    await popupPage.goto(`chrome-extension://${extensionId}/popup.html`);
    await popupPage.evaluate(
      (config) => {
        const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement | null;
        const apiKeyInput = document.getElementById('apiKey') as HTMLInputElement | null;
        const adminApiKeyInput = document.getElementById('adminApiKey') as HTMLInputElement | null;
        if (baseUrlInput) baseUrlInput.value = config.baseUrl;
        if (apiKeyInput) apiKeyInput.value = config.apiKey;
        if (adminApiKeyInput) adminApiKeyInput.value = config.adminApiKey;
        const saveBtn = document.getElementById('save-config');
        if (saveBtn) saveBtn.click();
      },
      { baseUrl, apiKey: WORKER_API_KEY, adminApiKey: ADMIN_API_KEY },
    );
    await new Promise((r) => setTimeout(r, 500));
    await popupPage.close();
    console.log('[manual-env] extension configured');

    // Open Baidu homepage for manual testing.
    const testPage = await context.newPage();
    await testPage.goto('https://www.baidu.com/');
    console.log('[manual-env] opened https://www.baidu.com/');

    console.log('\n==============================================');
    console.log('Manual test environment is ready.');
    console.log(`Server:     ${baseUrl}`);
    console.log(`Admin Key:  ${ADMIN_API_KEY}`);
    console.log(`Worker Key: ${WORKER_API_KEY}`);
    console.log(`DB:         ${dbPath}`);
    console.log('Browser:    Chromium with AegisCrawler extension loaded');
    console.log('==============================================');
    console.log('You can now:');
    console.log('1. Click the AegisCrawler extension icon to open the popup.');
    console.log('2. Click "开始录制" and perform a Baidu search.');
    console.log('3. Click "停止录制" and choose an intent to generate DSL.');
    console.log('4. Replay and confirm the DSL, then save it to the server.');
    console.log('Press Ctrl+C here when finished to close the browser and server.');

    // Keep the process alive until the user terminates it.
    await new Promise(() => {
      // never resolves
    });
  } finally {
    console.log('\n[manual-env] cleaning up...');
    context?.close().catch(() => undefined);
    serverProc.kill();
    try { fs.unlinkSync(dbPath); } catch { /* ignore */ }
    try { fs.unlinkSync(path.join(serverDir, 'manual-test-server.exe')); } catch { /* ignore */ }
  }
}

main().catch((err) => {
  console.error('[manual-env] failed:', err);
  process.exit(1);
});
