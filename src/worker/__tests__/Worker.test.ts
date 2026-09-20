/// <reference types="vitest/globals" />
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { Worker, type WorkerOptions } from '../Worker';
import { type ClaimedTask } from '../HttpTransport';
import { BrowserEnvironment } from '../BrowserEnvironment';
import { runRule } from '../../scriptcat-engine/executor';
import type { Rule } from '../../scriptcat-engine/types';

vi.mock('../../scriptcat-engine/executor', () => ({
  runRule: vi.fn(),
}));

const task: ClaimedTask = {
  taskId: 'task-1',
  ruleId: 'rule-1',
  ruleVersion: '1.0.0',
  variables: { url: 'https://example.com/' },
};

const rule: Rule = {
  id: 'rule-1',
  version: '1.0.0',
  name: 'Test Rule',
  domain: 'example.com',
  enabled: true,
  steps: [],
};

function createEnvFactory(): WorkerOptions['envFactory'] {
  return (transport, _taskId, _ruleId, allowEvaluate, allowEvaluateDOM) =>
    new BrowserEnvironment(transport, window, allowEvaluate, allowEvaluateDOM);
}

function createWorker(overrides?: Partial<WorkerOptions>) {
  return new Worker({
    baseUrl: 'https://api.example.com/',
    workerId: 'worker-1',
    envFactory: createEnvFactory(),
    pollIntervalMs: 10,
    heartbeatIntervalMs: 20,
    ...overrides,
  });
}

describe('Worker', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    fetchMock = vi.fn();
    globalThis.fetch = (async (url: string, init?: RequestInit) => {
      if (init?.signal?.aborted) {
        const err = new Error('The operation was aborted.');
        err.name = 'AbortError';
        throw err;
      }
      return (fetchMock as unknown as (u: string, i?: RequestInit) => Promise<Response>)(url, init);
    }) as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  async function runWorkerFor(worker: Worker, ms: number): Promise<void> {
    const startPromise = worker.start();
    await vi.advanceTimersByTimeAsync(ms);
    worker.stop();
    await vi.runAllTimersAsync();
    await startPromise;
  }

  function mockFetchRoute(path: string, handler: () => Response) {
    fetchMock.mockImplementation(async (url: string, _init?: RequestInit) => {
      const route = typeof url === 'string' ? url.replace('https://api.example.com', '') : '';
      if (route === path) return handler();
      return new Response(null, { status: 404 });
    });
  }

  it('claims a task, fetches the rule, runs it, and sends result + status', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) {
          return new Response(JSON.stringify(task), { status: 200, headers: { 'Content-Type': 'application/json' } });
        }
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') {
        return new Response(JSON.stringify(rule), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockResolvedValue({
      status: 'success',
      message: 'done',
      partialData: { items: [1, 2] },
    });

    const worker = createWorker();
    await runWorkerFor(worker, 100);

    expect(runRule).toHaveBeenCalledWith(
      expect.objectContaining({
        rule,
        taskId: 'task-1',
        workerId: 'worker-1',
        variables: task.variables,
        env: expect.any(BrowserEnvironment),
      }),
    );

    const resultCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/results'));
    expect(resultCalls).toHaveLength(1);
    const [, resultInit] = resultCalls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(resultInit.body as string)).toMatchObject({
      taskId: 'task-1',
      workerId: 'worker-1',
      immediate: true,
      payload: { status: 'success', message: 'done', data: { items: [1, 2] } },
    });

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'running' }));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'done', message: 'done' }));
  });

  it('executes the immutable claimed rule and reports an attempt-scoped summary', async () => {
    const versionedTask: ClaimedTask = {
      ...task,
      attemptId: 'attempt-1',
      ruleVersionNumber: 2,
      rule,
      browserProfileId: 'profile-a',
      variables: { query: 'books', limit: 10 },
    };
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        return claimCount === 1
          ? new Response(JSON.stringify(versionedTask), { status: 200 })
          : new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') {
        throw new Error('versioned task must not fetch mutable catalog rule');
      }
      if (route === '/results') {
        return new Response(JSON.stringify({ success: true, valid: true, duplicate: false }), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });
    vi.mocked(runRule).mockResolvedValue({
      status: 'success', message: 'done', partialData: { rowCount: 2 },
    });

    const worker = createWorker({ browserProfileId: 'profile-a' });
    await runWorkerFor(worker, 100);

    const claimCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/tasks/claim'));
    expect(JSON.parse((claimCall?.[1] as RequestInit).body as string)).toEqual({
      workerId: 'worker-1', browserProfileId: 'profile-a',
    });
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/rules/rule-1'))).toHaveLength(0);
    expect(runRule).toHaveBeenCalledWith(expect.objectContaining({
      rule,
      variables: versionedTask.variables,
    }));
    const results = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/results'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject({
      taskId: 'task-1', workerId: 'worker-1', attemptId: 'attempt-1',
      idempotencyKey: 'attempt-1:1:summary', sequence: 1, kind: 'summary',
      immediate: true,
      payload: { status: 'success', message: 'done', data: { rowCount: 2 } },
    });
  });

  it('does not execute a task claimed for a different browser profile', async () => {
    vi.mocked(runRule).mockClear();
    fetchMock.mockImplementation(async () => new Response(JSON.stringify({
      ...task,
      browserProfileId: 'profile-b',
    }), { status: 200 }));

    const worker = createWorker({ browserProfileId: 'profile-a', claimBackoffMs: 10 });
    await runWorkerFor(worker, 50);

    expect(runRule).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/status'))).toHaveLength(0);
  });

  it('sends heartbeat while task is running', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) return new Response(JSON.stringify(task), { status: 200 });
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') return new Response(JSON.stringify(rule), { status: 200 });
      if (route === '/heartbeat') return new Response(JSON.stringify({ cancelRequested: false }), { status: 200 });
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockImplementation(async () => {
      await new Promise((r) => setTimeout(r, 120));
      return { status: 'success', message: 'done' };
    });

    const worker = createWorker({ heartbeatIntervalMs: 25 });
    await runWorkerFor(worker, 200);

    const heartbeatCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/heartbeat'));
    expect(heartbeatCalls.length).toBeGreaterThanOrEqual(2);
    heartbeatCalls.forEach(([, init]) => {
      const body = JSON.parse((init as RequestInit).body as string);
      expect(body).toMatchObject({ taskId: 'task-1', workerId: 'worker-1' });
      expect(body.payload).toHaveProperty('ts');
    });
  });

  it('aborts running task when heartbeat returns cancelRequested', async () => {
    let heartbeatCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') return new Response(JSON.stringify(task), { status: 200 });
      if (route === '/rules/rule-1') return new Response(JSON.stringify(rule), { status: 200 });
      if (route === '/heartbeat') {
        heartbeatCount++;
        return new Response(JSON.stringify({ cancelRequested: heartbeatCount >= 2 }), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockImplementation(({ signal }: { signal?: AbortSignal }) => {
      return new Promise((_resolve, reject) => {
        const onAbort = () => {
          const err = new Error('cancelled');
          err.name = 'AbortError';
          reject(err);
        };
        if (signal?.aborted) {
          onAbort();
          return;
        }
        signal?.addEventListener('abort', onAbort, { once: true });
      });
    });

    const worker = createWorker({ heartbeatIntervalMs: 25 });
    await runWorkerFor(worker, 200);

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'cancelled', message: 'Task cancelled by operator' }));
  });

  it('handles claim 204 (no task) by polling', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    const worker = createWorker({ pollIntervalMs: 10 });
    await runWorkerFor(worker, 100);

    const claimCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim'));
    expect(claimCalls.length).toBeGreaterThanOrEqual(2);
    claimCalls.forEach(([url, init]) => {
      expect(String(url)).toBe('https://api.example.com/tasks/claim');
      expect(JSON.parse((init as RequestInit).body as string)).toEqual({ workerId: 'worker-1' });
    });
  });

  it('reports failure when rule fetch returns null', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) return new Response(JSON.stringify(task), { status: 200 });
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') return new Response(null, { status: 404 });
      return new Response(null, { status: 200 });
    });

    const worker = createWorker();
    await runWorkerFor(worker, 100);

    const logCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/logs'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(logCalls).toContainEqual(expect.objectContaining({ level: 'error', message: expect.stringContaining('not found') }));

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'failed', message: expect.stringContaining('not found') }));
  });

  it('reports failure when runRule throws', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) return new Response(JSON.stringify(task), { status: 200 });
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') return new Response(JSON.stringify(rule), { status: 200 });
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockRejectedValue(new Error('boom'));

    const worker = createWorker();
    await runWorkerFor(worker, 100);

    const logCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/logs'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(logCalls).toContainEqual(expect.objectContaining({ level: 'error', message: 'boom' }));

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'failed', message: 'boom' }));
  });

  it('keeps polling when claimTask throws', async () => {
    fetchMock.mockRejectedValue(new Error('network error'));
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const worker = createWorker({ pollIntervalMs: 10, claimBackoffMs: 10 });
    await runWorkerFor(worker, 100);

    const claimCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim'));
    expect(claimCalls.length).toBeGreaterThanOrEqual(2);

    expect(consoleSpy).toHaveBeenCalledWith(
      '[Worker] claim error:',
      'network error',
      expect.stringContaining('Error: network error'),
    );
    consoleSpy.mockRestore();
  });

  it('applies exponential backoff on claim errors and resets on success', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount <= 3) {
          throw new Error(`server error ${claimCount}`);
        }
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: 200 });
    });

    const worker = createWorker({ pollIntervalMs: 10, claimBackoffMs: 10, maxClaimBackoffMs: 40 });
    const startPromise = worker.start();

    await vi.advanceTimersByTimeAsync(0);
    expect(claimCount).toBe(1);

    await vi.advanceTimersByTimeAsync(20);
    expect(claimCount).toBe(2);

    await vi.advanceTimersByTimeAsync(40);
    expect(claimCount).toBe(3);

    await vi.advanceTimersByTimeAsync(40);
    expect(claimCount).toBe(4);

    await vi.advanceTimersByTimeAsync(10);
    expect(claimCount).toBe(5);

    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();
  });

  it('does not exponentially backoff on 4xx claim errors', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        return new Response(null, { status: 400 });
      }
      return new Response(null, { status: 200 });
    });

    const worker = createWorker({ pollIntervalMs: 100, claimBackoffMs: 10, maxClaimBackoffMs: 40 });
    const startPromise = worker.start();

    await vi.advanceTimersByTimeAsync(0);
    expect(claimCount).toBe(1);

    await vi.advanceTimersByTimeAsync(10);
    expect(claimCount).toBe(2);

    await vi.advanceTimersByTimeAsync(10);
    expect(claimCount).toBe(3);

    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();
  });

  it('resets claim backoff on 204 No Content', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) return new Response(null, { status: 204 });
        throw new Error('server error');
      }
      return new Response(null, { status: 200 });
    });

    const worker = createWorker({ pollIntervalMs: 10, claimBackoffMs: 10, maxClaimBackoffMs: 40 });
    const startPromise = worker.start();

    await vi.advanceTimersByTimeAsync(0);
    expect(claimCount).toBe(1);

    await vi.advanceTimersByTimeAsync(10);
    expect(claimCount).toBe(2);

    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();
  });

  it('caps claim backoff at maxClaimBackoffMs', async () => {
    fetchMock.mockRejectedValue(new Error('server error'));

    const worker = createWorker({ pollIntervalMs: 10, claimBackoffMs: 10, maxClaimBackoffMs: 20 });
    const startPromise = worker.start();

    await vi.advanceTimersByTimeAsync(0);
    const firstBatch = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim')).length;
    expect(firstBatch).toBe(1);

    await vi.advanceTimersByTimeAsync(20);
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim')).length).toBe(2);

    await vi.advanceTimersByTimeAsync(20);
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim')).length).toBe(3);

    await vi.advanceTimersByTimeAsync(20);
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim')).length).toBe(4);

    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();
  });

  it('start() resolves after stop()', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const worker = createWorker({ pollIntervalMs: 60000 });
    const startPromise = worker.start();
    await Promise.resolve();
    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();
  });

  it('claim uses HttpTransport and carries Authorization and X-Trace-Id headers', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) {
          return new Response(JSON.stringify(task), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') {
        return new Response(JSON.stringify(rule), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockResolvedValue({ status: 'success', message: 'done' });

    const worker = createWorker({ apiKey: 'secret-key', traceId: 'trace-claim-1' });
    await runWorkerFor(worker, 100);

    const claimCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim'));
    expect(claimCalls.length).toBeGreaterThanOrEqual(1);
    const [, init] = claimCalls[0] as unknown as [string, RequestInit];
    expect(init.headers).toMatchObject({
      Authorization: 'Bearer secret-key',
      'X-Trace-Id': 'trace-claim-1',
    });
    expect(JSON.parse(init.body as string)).toEqual({ workerId: 'worker-1' });
  });

  it('propagates traceId from claim response to task requests', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) {
          return new Response(JSON.stringify(task), {
            status: 200,
            headers: { 'Content-Type': 'application/json', 'X-Trace-Id': 'server-trace-1' },
          });
        }
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') {
        return new Response(JSON.stringify(rule), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockResolvedValue({ status: 'success', message: 'done' });

    const worker = createWorker({ apiKey: 'secret-key' });
    await runWorkerFor(worker, 100);

    const statusCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/status'));
    expect(statusCalls.length).toBeGreaterThanOrEqual(1);
    const [, init] = statusCalls[0] as unknown as [string, RequestInit];
    expect(init.headers).toMatchObject({ 'X-Trace-Id': 'server-trace-1' });
  });

  it('uses default timing options when omitted', () => {
    const worker = new Worker({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      envFactory: createEnvFactory(),
    });
    expect((worker as any).pollIntervalMs).toBe(5000);
    expect((worker as any).heartbeatIntervalMs).toBe(30000);
  });

  it('reports cancelled task status', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount === 1) return new Response(JSON.stringify(task), { status: 200 });
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') return new Response(JSON.stringify(rule), { status: 200 });
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockResolvedValue({ status: 'cancelled', message: 'user cancelled' });

    const worker = createWorker();
    await runWorkerFor(worker, 100);

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'cancelled', message: 'user cancelled' }));
  });

  it('handles claim errors with non-Error objects', async () => {
    fetchMock.mockRejectedValue({ message: undefined, stack: undefined });
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const worker = createWorker({ pollIntervalMs: 10, claimBackoffMs: 10 });
    await runWorkerFor(worker, 50);

    expect(consoleSpy).toHaveBeenCalledWith(
      '[Worker] claim error:',
      expect.objectContaining({ message: undefined, stack: undefined }),
      '',
    );
    consoleSpy.mockRestore();
  });

  it.skip('handles task errors with missing message/stack', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') return new Response(JSON.stringify(task), { status: 200 });
      if (route === '/rules/rule-1') return new Response(JSON.stringify(rule), { status: 200 });
      return new Response(null, { status: 200 });
    });

    const err = new Error();
    (err as any).message = undefined;
    (err as any).stack = undefined;
    vi.mocked(runRule).mockRejectedValue(err);

    const worker = createWorker();
    await runWorkerFor(worker, 100);

    const logCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/logs'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(logCalls).toContainEqual(expect.objectContaining({ level: 'error', message: '' }));
  });

  it('aborts in-flight claim when stop() is called', async () => {
    vi.useRealTimers();
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      return new Promise((_resolve, reject) => {
        const signal = init?.signal;
        const abort = () => {
          const err = new Error('The operation was aborted.');
          err.name = 'AbortError';
          reject(err);
        };
        if (signal?.aborted) {
          abort();
          return;
        }
        signal?.addEventListener('abort', abort, { once: true });
      });
    });

    const worker = createWorker();
    const startPromise = worker.start();
    await new Promise((r) => setTimeout(r, 10));
    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();

    const claimCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/tasks/claim'));
    expect(claimCalls).toHaveLength(1);
    const [, init] = claimCalls[0] as unknown as [string, RequestInit];
    expect(init.signal?.aborted).toBe(true);
  });

  it('aborts in-flight task when stop() is called', async () => {
    vi.useRealTimers();
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const route = (url as string).replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        return Promise.resolve(new Response(JSON.stringify(task), { status: 200 }));
      }
      if (route === '/status') {
        return Promise.resolve(new Response(null, { status: 200 }));
      }
      return new Promise((_resolve, reject) => {
        const signal = init?.signal;
        const abort = () => {
          const err = new Error('The operation was aborted.');
          err.name = 'AbortError';
          reject(err);
        };
        if (signal?.aborted) {
          abort();
          return;
        }
        signal?.addEventListener('abort', abort, { once: true });
      });
    });

    const worker = createWorker();
    const startPromise = worker.start();
    await new Promise((r) => setTimeout(r, 20));
    worker.stop();
    await expect(startPromise).resolves.toBeUndefined();

    const ruleFetchCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/rules/rule-1'));
    expect(ruleFetchCalls.length).toBe(1);
    const [, init] = ruleFetchCalls[0] as unknown as [string, RequestInit];
    expect(init.signal?.aborted).toBe(true);
  });

  it('can be restarted after stop()', async () => {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        if (claimCount % 2 === 1) {
          return new Response(JSON.stringify(task), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return new Response(null, { status: 204 });
      }
      if (route === '/rules/rule-1') {
        return new Response(JSON.stringify(rule), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockImplementation(async () => {
      await new Promise((r) => setTimeout(r, 100));
      return { status: 'success', message: 'done', partialData: { count: 1 } };
    });

    const worker = createWorker({ pollIntervalMs: 10, heartbeatIntervalMs: 60 });

    const start1 = worker.start();
    await vi.advanceTimersByTimeAsync(80);
    worker.stop();
    await vi.runAllTimersAsync();
    await start1;

    const start2 = worker.start();
    await vi.advanceTimersByTimeAsync(80);
    worker.stop();
    await vi.runAllTimersAsync();
    await start2;

    const resultCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/results'));
    expect(resultCalls).toHaveLength(2);
  });

  it('reports genuine task errors even when stop() races', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        return new Response(JSON.stringify(task), { status: 200 });
      }
      if (route === '/rules/rule-1') {
        return new Response(JSON.stringify(rule), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });

    vi.mocked(runRule).mockImplementation(async () => {
      await new Promise((r) => setTimeout(r, 30));
      throw new Error('real failure');
    });

    const worker = createWorker({ pollIntervalMs: 10, heartbeatIntervalMs: 60 });
    const startPromise = worker.start();
    await vi.advanceTimersByTimeAsync(15);
    worker.stop();
    await vi.advanceTimersByTimeAsync(40);
    await vi.runAllTimersAsync();
    await startPromise;

    const logCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/logs'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(logCalls).toContainEqual(expect.objectContaining({ level: 'error', message: 'real failure' }));

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'failed', message: 'real failure' }));

    // Terminal error reports must bypass the worker abort signal.
    const errorStatusCalls = fetchMock.mock.calls.filter(
      ([url, init]) => String(url).endsWith('/status') && JSON.parse((init as RequestInit).body as string).status === 'failed',
    );
    expect(errorStatusCalls).toHaveLength(1);
    expect((errorStatusCalls[0][1] as RequestInit).signal).toBeUndefined();

    const errorLogCalls = fetchMock.mock.calls.filter(
      ([url, init]) => String(url).endsWith('/logs') && JSON.parse((init as RequestInit).body as string).level === 'error',
    );
    expect(errorLogCalls).toHaveLength(1);
    expect((errorLogCalls[0][1] as RequestInit).signal).toBeUndefined();
  });
});
