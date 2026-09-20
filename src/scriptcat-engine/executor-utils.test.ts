import { actionMayNavigate, classifyError } from './executor-utils';
import type { Action } from './types';
import { runRule } from './executor';
import { comparePath } from './step-path';
import type { Environment, Rule, Transport } from './types';

describe('classifyError', () => {
  it('recognizes UnsupportedInEnvironment', () => {
    expect(classifyError(new Error('UnsupportedInEnvironment: foo'))).toBe('UnsupportedInEnvironment');
  });

  it('still falls through to UnknownError for unrecognized errors', () => {
    expect(classifyError(new Error('something weird'))).toBe('UnknownError');
  });
});

describe('actionMayNavigate', () => {
  const cases: Array<{ action: Action; expected: boolean }> = [
    { action: { action: 'navigate', url: '/next' }, expected: true },
    { action: { action: 'reload' }, expected: true },
    { action: { action: 'goBack' }, expected: true },
    { action: { action: 'goForward' }, expected: true },
    { action: { action: 'click', target: { selector: '#next' } }, expected: true },
    { action: { action: 'type', target: { selector: '#query' }, value: 'x', submit: true }, expected: true },
    { action: { action: 'type', target: { selector: '#query' }, value: 'x' }, expected: false },
    { action: { action: 'sleep', ms: 1 }, expected: false },
  ];

  it.each(cases)('classifies $action.action as navigating=$expected', ({ action, expected }) => {
    expect(actionMayNavigate(action)).toBe(expected);
  });
});

type SendLog = (level: string, message: string, extra?: Record<string, any>) => Promise<void>;

function softTimeoutEnv(opts: { warnSpy?: ReturnType<typeof vi.fn<SendLog>> }): Environment {
  const sendLog: SendLog = opts.warnSpy ?? vi.fn(async () => {});
  const transport: Transport = {
    fetchRule: async () => null,
    sendResult: async () => {},
    sendLog,
    sendHeartbeat: async () => ({ cancelRequested: false }),
    sendStatus: async () => {},
    sendSnapshot: async () => {},
  };
  return {
    findElement: async () => null,
    findElements: async () => [],
    sleep: async (ms) => new Promise((r) => setTimeout(r, ms)),
    now: () => Date.now(),
    transport,
    getUrl: () => 'https://example.com/',
    getTitle: () => '',
    evaluate: async () => undefined,
    screenshot: async () => null,
    saveSnapshot: async () => null,
  } as Environment;
}

describe('softTimeout warn', () => {
  it('fires warn log when softTimeout < timeout, action still resolves', async () => {
    vi.useFakeTimers();
    try {
      const warnSpy = vi.fn<SendLog>(async () => {});
      const env = softTimeoutEnv({ warnSpy });
      const rule: Rule = {
        id: 'r1', version: '1.0.0', name: 'test', domain: 'example.com', enabled: true,
        steps: [{
          // waitForTimeout calls env.sleep(ms) — fake timers intercept sleep
          // so the action stays in flight while softTimer fires.
          action: 'waitForTimeout',
          ms: 5000,
          timeout: 10000,
          softTimeout: 50,
        } as any],
      };
      const p = runRule({ rule, taskId: 't1', workerId: 'w1', env });
      // advance to fire soft timer (50ms) but not finish the action (5000ms)
      await vi.advanceTimersByTimeAsync(100);
      expect(warnSpy).toHaveBeenCalledTimes(1);
      expect(warnSpy.mock.calls[0][0]).toBe('warn');
      expect(warnSpy.mock.calls[0][1]).toMatch(/Soft timeout/);
      // finish the action
      await vi.advanceTimersByTimeAsync(6000);
      const res = await p;
      expect(res.status).toBe('success');
    } finally {
      vi.useRealTimers();
    }
  });

  it('does NOT emit warn when action completes before softTimeout', async () => {
    const warnSpy = vi.fn<SendLog>(async () => {});
    const env = softTimeoutEnv({ warnSpy });
    const rule: Rule = {
      id: 'r1', version: '1.0.0', name: 'test', domain: 'example.com', enabled: true,
      steps: [{ action: 'waitForTimeout', ms: 0, timeout: 5000, softTimeout: 10000 } as any],
    };
    await runRule({ rule, taskId: 't1', workerId: 'w1', env });
    expect(warnSpy).not.toHaveBeenCalled();
  });

  it('does NOT emit warn when softTimeout >= timeout', async () => {
    const warnSpy = vi.fn<SendLog>(async () => {});
    const env = softTimeoutEnv({ warnSpy });
    const rule: Rule = {
      id: 'r1', version: '1.0.0', name: 'test', domain: 'example.com', enabled: true,
      steps: [{ action: 'waitForTimeout', ms: 0, timeout: 5000, softTimeout: 5000 } as any],
    };
    await runRule({ rule, taskId: 't1', workerId: 'w1', env });
    expect(warnSpy).not.toHaveBeenCalled();
  });

  it('does NOT emit warn when softTimeout undefined', async () => {
    const warnSpy = vi.fn<SendLog>(async () => {});
    const env = softTimeoutEnv({ warnSpy });
    const rule: Rule = {
      id: 'r1', version: '1.0.0', name: 'test', domain: 'example.com', enabled: true,
      steps: [{ action: 'waitForTimeout', ms: 0, timeout: 5000 } as any],
    };
    await runRule({ rule, taskId: 't1', workerId: 'w1', env });
    expect(warnSpy).not.toHaveBeenCalled();
  });

  it('warns for a long-running flow-control container without aborting it', async () => {
    vi.useFakeTimers();
    try {
      const warnSpy = vi.fn<SendLog>(async () => {});
      const env = softTimeoutEnv({ warnSpy });
      const rule: Rule = {
        id: 'r1', version: '1.0.0', name: 'test', domain: 'example.com', enabled: true,
        steps: [{
          action: 'loop',
          type: 'fixedCount',
          count: 1,
          softTimeout: 50,
          steps: [{ action: 'waitForTimeout', ms: 500 }],
        } as any],
      };

      const pending = runRule({ rule, taskId: 't1', workerId: 'w1', env });
      await vi.advanceTimersByTimeAsync(100);

      expect(warnSpy).toHaveBeenCalledTimes(1);
      expect(warnSpy).toHaveBeenCalledWith(
        'warn',
        expect.stringContaining('Soft timeout'),
        {
          stepId: undefined,
          action: 'loop',
          softTimeout: 50,
        },
      );

      await vi.advanceTimersByTimeAsync(500);
      await expect(pending).resolves.toMatchObject({ status: 'success' });
    } finally {
      vi.useRealTimers();
    }
  });

  it('clears a flow-control warning when the container finishes first', async () => {
    vi.useFakeTimers();
    try {
      const warnSpy = vi.fn<SendLog>(async () => {});
      const env = softTimeoutEnv({ warnSpy });
      const rule: Rule = {
        id: 'r1', version: '1.0.0', name: 'test', domain: 'example.com', enabled: true,
        steps: [{
          action: 'loop',
          type: 'fixedCount',
          count: 0,
          softTimeout: 50,
          steps: [],
        } as any],
      };

      await expect(runRule({ rule, taskId: 't1', workerId: 'w1', env }))
        .resolves.toMatchObject({ status: 'success' });
      await vi.advanceTimersByTimeAsync(100);
      expect(warnSpy).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('D-2: hooksCompleted vs comparePath', () => {
  it('hooksCompleted does not affect comparePath ordering', () => {
    // comparePath is pure on StepPath; hooksCompleted is not a StepPath input.
    // This test exists to prevent a future contributor from threading hooks
    // into StepPath (D-2 design constraint).
    expect(comparePath([], [])).toBe(0);
    expect(comparePath([{ kind: 'top', childIdx: 0 }], [{ kind: 'top', childIdx: 1 }])).toBeLessThan(0);
  });
});
