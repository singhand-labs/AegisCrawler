import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { spawn, ChildProcess } from 'child_process';
import { createServer } from 'net';
import { JSDOM } from 'jsdom';
import { Worker } from '../src/worker/Worker';
import { BrowserEnvironment } from '../src/worker/BrowserEnvironment';
import type { Rule } from '../src/scriptcat-engine/types';

export const WORKER_API_KEY = 'e2e-worker-key';
export const ADMIN_API_KEY = 'e2e-admin-key';
export const VARIABLE_ENCRYPTION_KEY = 'e2e-variable-encryption-key-32bytes!';
const RULE_ID = 'e2e-test-rule';

interface TaskResponse {
  id: string;
  status: string;
  ruleId: string;
  errorType?: string;
  errorMessage?: string;
}

interface ResultResponse {
  id: string;
  taskId: string;
  payload: unknown;
  immediate: boolean;
  createdAt: string;
}

export function getFreePort(): Promise<number> {
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

export function buildServer(serverDir: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const proc = spawn('go', ['build', '-o', 'e2e-server.exe', './cmd/server'], {
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

export function startServer(
  serverDir: string,
  dbPath: string,
  port: number,
  extraEnv: Record<string, string> = {},
): ChildProcess {
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  // Allow real-LLM E2E runs by passing through LLM_* env vars when present.
  const llmEnv: Record<string, string | undefined> = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (key.startsWith('LLM_') && value !== undefined) {
      llmEnv[key] = value;
    }
  }
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
      LOG_LEVEL: 'warn',
      LLM_ENABLED: 'false',
      ...llmEnv,
      ...extraEnv,
    },
    stdio: 'pipe',
  });

  // The staging acceptance test deliberately creates enough concurrent HTTP
  // traffic to fill an unread child-process pipe. Always drain server output so
  // logging backpressure cannot block request handlers and masquerade as an
  // application timeout. Individual tests surface failures through API checks.
  proc.stdout?.resume();
  proc.stderr?.resume();

  // Prevent the server process from keeping the test alive on its own.
  proc.unref?.();
  return proc;
}

export async function waitForHealth(baseUrl: string, timeoutMs = 10000): Promise<void> {
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

export async function adminRequest(baseUrl: string, path: string, body: unknown): Promise<unknown> {
  const res = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${ADMIN_API_KEY}`,
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`admin ${path} failed: ${res.status} ${await res.text()}`);
  }
  return res.json();
}

export async function workerGet(baseUrl: string, path: string): Promise<unknown> {
  const res = await fetch(`${baseUrl}${path}`, {
    headers: {
      Authorization: `Bearer ${WORKER_API_KEY}`,
    },
  });
  if (!res.ok) {
    throw new Error(`worker ${path} failed: ${res.status} ${await res.text()}`);
  }
  return res.json();
}

export async function waitForTaskDone(baseUrl: string, taskId: string, timeoutMs = 30000): Promise<TaskResponse> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const task = (await workerGet(baseUrl, `/tasks/${encodeURIComponent(taskId)}`)) as TaskResponse;
    if (task.status === 'done') return task;
    if (task.status === 'failed') {
      throw new Error(`task ${taskId} failed: ${task.errorType ?? 'unknown'}: ${task.errorMessage ?? 'no message'}`);
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`task ${taskId} did not complete in time`);
}

export function killServer(proc: ChildProcess): Promise<void> {
  return new Promise((resolve) => {
    if (!proc || proc.killed) {
      resolve();
      return;
    }

    let settled = false;
    const timer = setTimeout(() => {
      if (!settled) {
        settled = true;
        proc.kill('SIGKILL');
        resolve();
      }
    }, 5000);

    proc.on('exit', () => {
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        resolve();
      }
    });

    proc.kill();
  });
}

export function removeIfExists(filePath: string): void {
  try {
    fs.unlinkSync(filePath);
  } catch {
    // ignore
  }
}

async function main(): Promise<void> {
  const port = await getFreePort();
  const baseUrl = `http://127.0.0.1:${port}`;
  const serverDir = path.resolve(__dirname, '..', 'server');
  const dbPath = path.join(os.tmpdir(), `opencrawler-e2e-${Date.now()}.db`);
  const binaryPath = path.join(serverDir, 'e2e-server.exe');

  console.log(`[e2e] using port ${port}, db ${dbPath}`);
  await buildServer(serverDir);

  const serverProc = startServer(serverDir, dbPath, port, {
    FEATURE_WORKFLOW_V2: 'true',
    FEATURE_WORKER_PROTOCOL_V2: 'true',
  });
  const workerId = `e2e-worker-${Date.now()}`;
  let worker: Worker | null = null;

  try {
    await waitForHealth(baseUrl);
    console.log('[e2e] server healthy');

    const testRule: Rule = {
      id: RULE_ID,
      version: '1.0.0',
      name: 'E2E Deployment Smoke Test Rule',
      domain: 'example.com',
      enabled: true,
      entry: 'https://example.com/',
      steps: [
        {
          action: 'extractText',
          name: 'title',
          target: { selector: 'title' },
        },
        {
          action: 'sendResult',
          payload: { title: '{{extracted.title}}' },
          immediate: true,
        },
      ],
      output: {
        type: 'object',
        properties: { title: { type: 'string' } },
        required: ['title'],
        additionalProperties: false,
      },
    };

    await adminRequest(baseUrl, `/admin/rules/${encodeURIComponent(RULE_ID)}/versions`, { rule: testRule });
    await adminRequest(baseUrl, `/admin/rules/${encodeURIComponent(RULE_ID)}/versions/1/approve`, {});
    console.log('[e2e] immutable rule version approved');

    const createTaskResp = (await adminRequest(baseUrl, '/admin/tasks', {
      ruleId: RULE_ID,
      ruleVersionNumber: 1,
      variables: {},
    })) as { taskId: string };
    const taskId = createTaskResp.taskId;
    console.log(`[e2e] task created: ${taskId}`);

    const dom = new JSDOM(
      '<!DOCTYPE html><html><head><title>Example Domain</title></head><body></body></html>',
      { url: 'https://example.com/' },
    );

    // Expose the jsdom window/document/location to the global scope so that
    // scriptcat-engine navigation and page-info actions operate on the same page.
    (globalThis as any).window = dom.window;
    (globalThis as any).document = dom.window.document;
    (globalThis as any).location = dom.window.location;

    worker = new Worker({
      baseUrl,
      workerId,
      apiKey: WORKER_API_KEY,
      pollIntervalMs: 1000,
      heartbeatIntervalMs: 5000,
      envFactory: (transport, _taskId, _ruleId) =>
        new BrowserEnvironment(transport, dom.window as unknown as Window),
    });

    // Start the worker loop in the background; it will claim and execute the task.
    worker.start().catch((err) => {
      console.error('[e2e] worker error:', err);
    });

    const task = await waitForTaskDone(baseUrl, taskId);
    console.log(`[e2e] task status: ${task.status}`);

    const results = (await workerGet(baseUrl, `/tasks/${encodeURIComponent(taskId)}/results`)) as ResultResponse[];
    const hasTitle = results.some((r) => {
      const payload = r.payload;
      return payload && typeof payload === 'object' && JSON.stringify(payload).includes('"title"');
    });

    if (!hasTitle) {
      throw new Error(`result payloads do not contain title: ${JSON.stringify(results.map((r) => r.payload))}`);
    }

    console.log('[e2e] result payload contains title');
    console.log('[e2e] smoke test passed');
  } finally {
    worker?.stop();
    await killServer(serverProc);
    removeIfExists(dbPath);
    removeIfExists(binaryPath);
  }
}

if (require.main === module) {
  main().catch((err) => {
    console.error('[e2e] smoke test failed:', err);
    process.exit(1);
  });
}
