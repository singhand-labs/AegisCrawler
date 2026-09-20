// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://example.com/start" }
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Rule } from '../../../src/scriptcat-engine/types';
import { EMPTY_PATH, type StepPath } from '../../../src/scriptcat-engine/step-path';

vi.mock('../../../src/scriptcat-engine/executor', () => ({
  runRule: vi.fn(),
}));

import { runRule } from '../../../src/scriptcat-engine/executor';

const sampleRule: Rule = {
  id: 'test-rule',
  version: '1.0.0',
  name: 'Test Rule',
  domain: 'example.com',
  enabled: true,
  steps: [{ action: 'navigate', url: 'https://example.com' }],
} as Rule;

const bootPayload = {
  rule: sampleRule,
  variables: { maxItems: 5 },
  taskId: 'task-1',
  workerId: 'worker-1',
};

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

interface FreshModule {
  boot: (p: any) => void;
  autoResume: (p: any, c?: any) => void;
  sendMessage: ReturnType<typeof vi.fn>;
  addListener: ReturnType<typeof vi.fn>;
}

async function importFresh(): Promise<FreshModule> {
  vi.resetModules();
  const sendMessage = vi.fn().mockResolvedValue(undefined);
  const removeListener = vi.fn();
  const addListener = vi.fn();
  (globalThis as any).chrome = {
    runtime: {
      sendMessage,
      onMessage: { addListener, removeListener },
    },
  };
  // Importing the module registers __ocReplayBoot / __ocReplayAutoResume on
  // globalThis (the entry points are exposed as globals per §5.1, not exports).
  await import('./replay-runner');
  return {
    boot: (globalThis as any).__ocReplayBoot,
    autoResume: (globalThis as any).__ocReplayAutoResume,
    sendMessage,
    addListener,
  };
}

function loadStateFromSession(taskId: string): any {
  const raw = sessionStorage.getItem(`oc_replay_state_${taskId}`);
  return raw ? JSON.parse(raw) : null;
}

function seedRunningState(taskId: string, overrides: Partial<any> = {}): void {
  sessionStorage.setItem(
    `oc_replay_state_${taskId}`,
    JSON.stringify({
      taskId,
      ruleId: 'test-rule',
      lastCompletedStepPath: EMPTY_PATH,
      expectedUrl: 'https://example.com/start',
      status: 'running',
      ...overrides,
    }),
  );
  sessionStorage.setItem('oc_replay_active', taskId);
}

describe('replay-runner — boot', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
    delete (globalThis as any).chrome;
    delete (globalThis as any).__ocReplayActive;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('invokes runRule with payload and posts REPLAY_COMPLETE on success', async () => {
    vi.mocked(runRule).mockResolvedValue({
      status: 'success',
      message: 'done',
      partialData: {},
    } as any);

    const { boot, sendMessage } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    expect(runRule).toHaveBeenCalledWith(
      expect.objectContaining({
        rule: sampleRule,
        taskId: 'task-1',
        workerId: 'worker-1',
        variables: { maxItems: 5 },
        humanizeReplay: true,
        resumeStepPath: EMPTY_PATH,
      }),
    );
    expect(sendMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        action: 'REPLAY_COMPLETE',
        payload: expect.objectContaining({ status: 'success' }),
      }),
    );
  });

  it('defaults variables to {} when omitted', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success', message: 'ok' } as any);

    const { boot } = await importFresh();
    boot({ rule: sampleRule, taskId: 'task-1', workerId: 'w1' });
    await flushPromises();

    expect(runRule).toHaveBeenCalledWith(expect.objectContaining({ variables: {} }));
  });

  it('writes initial ReplayState to sessionStorage with origin+pathname expectedUrl', async () => {
    vi.mocked(runRule).mockReturnValue(new Promise(() => undefined));

    const { boot } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    const state = loadStateFromSession('task-1');
    expect(state).toBeTruthy();
    expect(state.status).toBe('running');
    expect(state.lastCompletedStepPath).toEqual(EMPTY_PATH);
    expect(state.expectedUrl).toBe('https://example.com/start');
    expect(state.expectedUrlPolicy).toBe('expected-path');
    expect(state.taskId).toBe('task-1');
    expect(state.ruleId).toBe('test-rule');
    // §7.3 data minimization: no extracted/evaluated/captured in sessionStorage.
    expect(state.extracted).toBeUndefined();
  });

  it('realm idempotency guard: second boot of same taskId is a no-op', async () => {
    vi.mocked(runRule).mockReturnValue(new Promise(() => undefined));

    const { boot } = await importFresh();
    boot(bootPayload);
    await flushPromises();
    boot(bootPayload);
    await flushPromises();

    expect(runRule).toHaveBeenCalledTimes(1);
  });

  it('posts failure and skips runRule when preflight fails', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    const badRule: Rule = {
      ...sampleRule,
      steps: [{ action: 'evaluate', script: 'return 1' } as any],
    } as Rule;

    const { boot, sendMessage } = await importFresh();
    boot({ rule: badRule, taskId: 'task-1', workerId: 'w1' });
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
    const calls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_COMPLETE');
    expect(calls.length).toBe(1);
    expect(calls[0][0].payload.status).toBe('failure');
    expect(calls[0][0].payload.message).toMatch(/preflight failed/);
  });

  it('posts failure on domain mismatch', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    // Non-navigate rule so preflight doesn't catch it; domain check fires.
    const offsiteRule: Rule = {
      ...sampleRule,
      domain: 'other.example.org',
      steps: [{ action: 'extract', name: 'x', target: { selector: 'h1' }, fields: {} } as any],
    } as Rule;

    const { boot, sendMessage } = await importFresh();
    boot({ rule: offsiteRule, taskId: 'task-1', workerId: 'w1' });
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
    const calls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_COMPLETE');
    expect(calls[0][0].payload.status).toBe('failure');
    expect(calls[0][0].payload.message).toMatch(/Domain mismatch/);
  });

  it('posts failure when runRule throws', async () => {
    vi.mocked(runRule).mockRejectedValue(new Error('executor crash'));

    const { boot, sendMessage } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    const calls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_COMPLETE');
    expect(calls[0][0].payload).toMatchObject({
      status: 'failure',
      message: 'executor crash',
    });
  });
});

describe('replay-runner — autoResume', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
    delete (globalThis as any).chrome;
    delete (globalThis as any).__ocReplayActive;
  });

  it('is a no-op when no prior state exists', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);

    const { autoResume } = await importFresh();
    autoResume(bootPayload, undefined);
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
  });

  it('is a no-op when prior taskId does not match payload', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    seedRunningState('other-task');

    const { autoResume } = await importFresh();
    autoResume(bootPayload, undefined);
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
  });

  it('resumes with resumeStepPath = nextPendingPath(prior.lastCompletedStepPath)', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    // Last completed step was top-level childIdx 3; nextPendingPath → childIdx 4.
    const priorPath: StepPath = [{ kind: 'top', childIdx: 3 }];
    seedRunningState('task-1', { lastCompletedStepPath: priorPath });

    const { autoResume } = await importFresh();
    autoResume(bootPayload, {
      lastCompletedStepPath: priorPath,
      extracted: { a: 1 },
      evaluated: {},
      captured: {},
    });
    await flushPromises();

    expect(runRule).toHaveBeenCalledWith(
      expect.objectContaining({
        resumeStepPath: [{ kind: 'top', childIdx: 4 }],
        initialContext: { extracted: { a: 1 }, evaluated: {}, captured: {} },
      }),
    );
  });

  it('refuses resume when ctxFromBg lags sessionStorage (stale ctx)', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    // sessionStorage says last completed = childIdx 5 (deepest); ctx says 2.
    // comparePath([top,2], [top,5]) < 0 → stale → fail-fast.
    seedRunningState('task-1', { lastCompletedStepPath: [{ kind: 'top', childIdx: 5 }] });

    const { autoResume, sendMessage } = await importFresh();
    autoResume(bootPayload, {
      lastCompletedStepPath: [{ kind: 'top', childIdx: 2 }],
      extracted: {},
      evaluated: {},
      captured: {},
    });
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
    const calls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_COMPLETE');
    expect(calls[0][0].payload.message).toMatch(/ctx stale/);
  });

  it('refuses resume when current URL hostname differs from expectedUrl', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    // Location stays at example.com (env options); expectedUrl references a
    // different host to trigger the mismatch.
    seedRunningState('task-1', {
      lastCompletedStepPath: [{ kind: 'top', childIdx: 0 }],
      expectedUrl: 'https://evil.example.org/start',
      expectedUrlPolicy: 'same-origin',
    });

    const { autoResume, sendMessage } = await importFresh();
    autoResume(bootPayload, {
      lastCompletedStepPath: [{ kind: 'top', childIdx: 0 }],
      extracted: {},
      evaluated: {},
      captured: {},
    });
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
    const calls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_COMPLETE');
    expect(calls[0][0].payload.message).toMatch(/resume URL mismatch/);
  });

  it('allows an event-driven navigation to change path within the same origin', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    const priorPath: StepPath = [{ kind: 'top', childIdx: 0 }];
    seedRunningState('task-1', {
      lastCompletedStepPath: priorPath,
      expectedUrl: 'https://example.com/education-initiative/',
      expectedUrlPolicy: 'same-origin',
    });

    const { autoResume } = await importFresh();
    autoResume(bootPayload, {
      lastCompletedStepPath: priorPath,
      extracted: {},
      evaluated: {},
      captured: {},
    });
    await flushPromises();

    expect(runRule).toHaveBeenCalledWith(expect.objectContaining({
      resumeStepPath: [{ kind: 'top', childIdx: 1 }],
    }));
  });

  it('restores control state from extension storage after an approved origin change', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    const priorPath: StepPath = [{ kind: 'top', childIdx: 0 }];
    const { autoResume } = await importFresh();

    autoResume(bootPayload, {
      lastCompletedStepPath: priorPath,
      extracted: { retained: 'value' },
      evaluated: {},
      captured: {},
      control: {
        taskId: 'task-1',
        ruleId: 'test-rule',
        lastCompletedStepPath: priorPath,
        expectedUrl: 'https://example.com/start',
        expectedUrlPolicy: 'expected-path',
        status: 'running',
      },
    });
    await flushPromises();

    expect(runRule).toHaveBeenCalledWith(expect.objectContaining({
      resumeStepPath: [{ kind: 'top', childIdx: 1 }],
      initialContext: expect.objectContaining({
        extracted: { retained: 'value' },
      }),
    }));
  });

  it('rejects a sibling path when an explicit navigation target was checkpointed', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    const priorPath: StepPath = [{ kind: 'top', childIdx: 0 }];
    seedRunningState('task-1', {
      lastCompletedStepPath: priorPath,
      expectedUrl: 'https://example.com/education-initiative/',
      expectedUrlPolicy: 'expected-path',
    });

    const { autoResume, sendMessage } = await importFresh();
    autoResume(bootPayload, {
      lastCompletedStepPath: priorPath,
      extracted: {},
      evaluated: {},
      captured: {},
    });
    await flushPromises();

    expect(runRule).not.toHaveBeenCalled();
    const calls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_COMPLETE');
    expect(calls[0][0].payload.message).toMatch(/resume URL mismatch/);
  });
});

describe('replay-runner — checkpoint & rollback', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
    delete (globalThis as any).chrome;
    delete (globalThis as any).__ocReplayActive;
  });

  it('onCheckpoint writes control state to sessionStorage and sends REPLAY_CONTEXT', async () => {
    let checkpointFn: any = null;
    vi.mocked(runRule).mockImplementation(async (opts: any) => {
      checkpointFn = opts.onCheckpoint;
      return { status: 'success', message: 'done' } as any;
    });

    const { boot, sendMessage } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    expect(checkpointFn).not.toBeNull();
    const cpPath: StepPath = [{ kind: 'top', childIdx: 1 }];
    checkpointFn({
      lastCompletedStepPath: cpPath,
      extracted: { items: [1, 2] },
      evaluated: { x: 1 },
      captured: {},
      navigation: {
        phase: 'before-navigation',
        expectedUrl: 'https://example.com/ipad-air/?session=redacted',
        urlPolicy: 'expected-path',
      },
    });
    await flushPromises();

    const state = loadStateFromSession('task-1');
    expect(state.lastCompletedStepPath).toEqual(cpPath);
    expect(state.expectedUrl).toBe('https://example.com/ipad-air/');
    expect(state.expectedUrlPolicy).toBe('expected-path');
    expect(state.extracted).toBeUndefined(); // §7.3 data minimization

    const ctxCalls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_CONTEXT');
    expect(ctxCalls.length).toBe(1);
    expect(ctxCalls[0][0].payload).toMatchObject({
      taskId: 'task-1',
      lastCompletedStepPath: cpPath,
      extracted: { items: [1, 2] },
      control: {
        taskId: 'task-1',
        ruleId: 'test-rule',
        lastCompletedStepPath: cpPath,
        expectedUrl: 'https://example.com/ipad-air/',
        expectedUrlPolicy: 'expected-path',
        status: 'running',
      },
    });
  });

  it('onStepFailure rolls lastCompletedStepPath back via prevPendingPath', async () => {
    let failureFn: any = null;
    vi.mocked(runRule).mockImplementation(async (opts: any) => {
      failureFn = opts.onStepFailure;
      return { status: 'success' } as any;
    });

    const { boot } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    // Simulate that checkpoint advanced to top.childIdx 3, then failure at
    // childIdx 4 rolls back to childIdx 3.
    const current = loadStateFromSession('task-1');
    sessionStorage.setItem(
      'oc_replay_state_task-1',
      JSON.stringify({
        ...current,
        lastCompletedStepPath: [{ kind: 'top', childIdx: 3 }],
      }),
    );

    failureFn([{ kind: 'top', childIdx: 4 }]);
    const state = loadStateFromSession('task-1');
    expect(state.lastCompletedStepPath).toEqual([{ kind: 'top', childIdx: 3 }]);
  });
});

describe('replay-runner — abort', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
    delete (globalThis as any).chrome;
    delete (globalThis as any).__ocReplayActive;
  });

  it('ABORT_REPLAY message triggers AbortController.abort', async () => {
    let capturedSignal: AbortSignal | undefined;
    vi.mocked(runRule).mockImplementation(async (opts: any) => {
      capturedSignal = opts.signal;
      return new Promise(() => undefined);
    });

    const { boot, addListener } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    // The boot path also registers an ABORT_REPLAY listener via registerAbortListener.
    const abortListener = addListener.mock.calls[0][0];
    abortListener({ action: 'ABORT_REPLAY' });
    await flushPromises();

    expect(capturedSignal?.aborted).toBe(true);
  });
});

describe('D-2: hooksCompleted forwarding', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
    delete (globalThis as any).chrome;
    delete (globalThis as any).__ocReplayActive;
  });

  it('passes initialHooksCompleted from ctxFromBg into runRule', async () => {
    vi.mocked(runRule).mockResolvedValue({ status: 'success' } as any);
    const priorPath: StepPath = [{ kind: 'top', childIdx: 0 }];
    seedRunningState('task-1', { lastCompletedStepPath: priorPath });

    const { autoResume } = await importFresh();
    autoResume(bootPayload, {
      lastCompletedStepPath: priorPath,
      extracted: {},
      evaluated: {},
      captured: {},
      hooksCompleted: ['afterAll'],
    });
    await flushPromises();

    expect(runRule).toHaveBeenCalledWith(
      expect.objectContaining({
        initialHooksCompleted: ['afterAll'],
      }),
    );
  });

  it('onCheckpoint callback persists hooksCompleted via REPLAY_CONTEXT', async () => {
    let checkpointFn: any = null;
    vi.mocked(runRule).mockImplementation(async (opts: any) => {
      checkpointFn = opts.onCheckpoint;
      return { status: 'success', message: 'done' } as any;
    });

    const { boot, sendMessage } = await importFresh();
    boot(bootPayload);
    await flushPromises();

    expect(checkpointFn).not.toBeNull();
    checkpointFn({
      lastCompletedStepPath: [{ kind: 'top', childIdx: 1 }],
      extracted: {},
      evaluated: {},
      captured: {},
      hooksCompleted: ['afterAll'],
    });
    await flushPromises();

    const ctxCalls = sendMessage.mock.calls.filter((c) => c[0]?.action === 'REPLAY_CONTEXT');
    expect(ctxCalls.length).toBe(1);
    expect(ctxCalls[0][0].payload).toMatchObject({
      hooksCompleted: ['afterAll'],
    });
  });
});
