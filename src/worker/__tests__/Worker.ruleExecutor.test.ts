/// <reference types="vitest/globals" />
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { Worker, type RuleExecutorContext } from '../Worker';
import { type ClaimedTask } from '../HttpTransport';
import { runRule } from '../../scriptcat-engine/executor';
import type { Rule } from '../../scriptcat-engine/types';

vi.mock('../../scriptcat-engine/executor', () => ({
  runRule: vi.fn(),
}));

const rule: Rule = {
  id: 'rule-1',
  version: '1.0.0',
  name: 'Test Rule',
  domain: 'example.com',
  enabled: true,
  steps: [],
};

const versionedTask: ClaimedTask = {
  taskId: 'task-1',
  attemptId: 'attempt-1',
  ruleId: 'rule-1',
  ruleVersion: '1.0.0',
  ruleVersionNumber: 2,
  rule,
  browserProfileId: 'profile-a',
  variables: { query: 'books' },
};

describe('Worker ruleExecutor seam', () => {
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

  function mockClaimOnce(task: ClaimedTask): void {
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        claimCount++;
        return claimCount === 1
          ? new Response(JSON.stringify(task), { status: 200, headers: { 'Content-Type': 'application/json' } })
          : new Response(null, { status: 204 });
      }
      if (route === '/results') {
        return new Response(JSON.stringify({ success: true, valid: true, duplicate: false }), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });
  }

  it('requires envFactory or ruleExecutor', () => {
    expect(() => new Worker({ baseUrl: 'https://api.example.com/', workerId: 'worker-1' } as never))
      .toThrow('envFactory or ruleExecutor');
  });

  it('delegates execution to ruleExecutor and keeps summary/status behavior', async () => {
    mockClaimOnce(versionedTask);
    const executor = vi.fn(async (_ctx: RuleExecutorContext) => ({
      status: 'success' as const,
      message: 'page run complete',
      data: { items: ['a', 'b'] },
    }));

    const worker = new Worker({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      browserProfileId: 'profile-a',
      ruleExecutor: executor,
      pollIntervalMs: 10,
      heartbeatIntervalMs: 20,
    });
    await runWorkerFor(worker, 100);

    expect(runRule).not.toHaveBeenCalled();
    expect(executor).toHaveBeenCalledTimes(1);
    const ctx = executor.mock.calls[0][0];
    expect(ctx.rule).toEqual(rule);
    expect(ctx.taskId).toBe('task-1');
    expect(ctx.workerId).toBe('worker-1');
    expect(ctx.variables).toEqual({ query: 'books' });
    expect(ctx.signal).toBeInstanceOf(AbortSignal);
    expect(typeof ctx.transport.sendFinalSummary).toBe('function');

    const resultCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/results'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(resultCalls).toHaveLength(1);
    expect(resultCalls[0]).toMatchObject({
      taskId: 'task-1',
      workerId: 'worker-1',
      attemptId: 'attempt-1',
      kind: 'summary',
      payload: { status: 'success', message: 'page run complete', data: { items: ['a', 'b'] } },
    });

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'running' }));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'done', message: 'page run complete' }));
  });

  it('maps ruleExecutor failure and cancellation to terminal statuses', async () => {
    // Attempt-scoped (v2) tasks terminalize through the summary + done/failed
    // statuses; executor-level success/failure/cancelled strings are filtered
    // by HttpTransport for attempt state. A legacy task (no attemptId) retains
    // the historical wire behavior, including a terminal 'cancelled' status.
    const legacyCancelledTask: ClaimedTask = {
      taskId: 'task-2',
      ruleId: 'rule-1',
      ruleVersion: '1.0.0',
      rule,
      variables: {},
    };
    const tasks: ClaimedTask[] = [
      versionedTask,
      legacyCancelledTask,
      { ...versionedTask, taskId: 'task-3', attemptId: 'attempt-3' },
    ];
    let claimCount = 0;
    fetchMock.mockImplementation(async (url: string) => {
      const route = url.replace('https://api.example.com', '');
      if (route === '/tasks/claim') {
        const task = tasks[claimCount++];
        return task
          ? new Response(JSON.stringify(task), { status: 200 })
          : new Response(null, { status: 204 });
      }
      if (route === '/results') {
        return new Response(JSON.stringify({ success: true, valid: true, duplicate: false }), { status: 200 });
      }
      return new Response(null, { status: 200 });
    });

    const outcomes = [
      { status: 'failure' as const, message: 'selector not found' },
      { status: 'cancelled' as const, message: 'Task was cancelled' },
      { status: 'success' as const, message: 'ok' },
    ];
    const worker = new Worker({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      browserProfileId: 'profile-a',
      ruleExecutor: async () => outcomes.shift() ?? { status: 'success' as const },
      pollIntervalMs: 10,
      heartbeatIntervalMs: 20,
    });
    await runWorkerFor(worker, 200);

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ taskId: 'task-1', status: 'failed', message: 'selector not found' }));
    expect(statusCalls).toContainEqual(expect.objectContaining({ taskId: 'task-2', status: 'cancelled' }));
    expect(statusCalls).toContainEqual(expect.objectContaining({ taskId: 'task-3', status: 'done' }));
  });

  it('terminalizes as failed when ruleExecutor throws', async () => {
    mockClaimOnce(versionedTask);
    const worker = new Worker({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      browserProfileId: 'profile-a',
      ruleExecutor: async () => {
        throw new Error('host policy violation');
      },
      pollIntervalMs: 10,
      heartbeatIntervalMs: 20,
    });
    await runWorkerFor(worker, 100);

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toContainEqual(expect.objectContaining({ status: 'failed', message: 'host policy violation' }));
  });
});
