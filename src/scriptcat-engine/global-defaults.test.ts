import { describe, it, expect, vi } from 'vitest';
import { DEFAULT_REPLAY_HUMANIZE, runRule } from './executor';

// Mock environment with controllable timing + action recording.
function makeMockEnv(overrides: Partial<any> = {}) {
  const calls: any[] = [];
  return {
    env: {
      signal: undefined,
      getUrl: () => 'https://example.com/',
      getTitle: () => 'T',
      sleep: vi.fn(async (_ms: number) => {}),
      evaluate: vi.fn(async () => undefined),
      findElement: vi.fn(async () => null),
      findElements: vi.fn(async () => []),
      now: () => 0,
      screenshot: vi.fn(async () => null),
      saveSnapshot: vi.fn(async () => null),
      transport: {
        fetchRule: vi.fn(async () => null),
        sendResult: vi.fn(async (payload: any, _imm?: boolean) => { calls.push({ kind: 'result', payload }); }),
        sendLog: vi.fn(async (level: string, msg: string, _payload?: any) => { calls.push({ kind: 'log', level, msg }); }),
        sendStatus: vi.fn(async (status: string, _msg?: string) => { calls.push({ kind: 'status', status }); }),
        sendHeartbeat: vi.fn(async () => ({ cancelRequested: false })),
        sendSnapshot: vi.fn(async () => {}),
      },
      ...overrides,
    },
    calls,
  };
}

function enabledClickTarget(): HTMLButtonElement {
  const element = document.createElement('button');
  element.getBoundingClientRect = () => ({
    left: 0,
    top: 0,
    width: 10,
    height: 10,
  } as DOMRect);
  return element;
}

describe('runRule ruleDefaults: timeout/retry', () => {
  it('uses rule.retry when step.retry is absent', async () => {
    const { env } = makeMockEnv({
      findElement: vi.fn(async () => { throw new Error('ElementNotFound: x'); }),
    });
    const rule: any = {
      id: 'r', version: '1', steps: [{ action: 'click', target: { selector: 'x' } }],
      retry: 2,
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.findElement as any).mock.calls.length).toBe(3);
  });

  it('clamps step.retry to rule.maxRetries', async () => {
    const { env } = makeMockEnv({
      findElement: vi.fn(async () => { throw new Error('ElementNotFound: x'); }),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' }, retry: 10 }],
      maxRetries: 1,
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.findElement as any).mock.calls.length).toBe(2);
  });

  it('uses rule.timeout as fallback when step.timeout absent', async () => {
    vi.useFakeTimers();
    try {
      const { env } = makeMockEnv({
        findElement: vi.fn(() => new Promise(() => {})),
      });
      const rule: any = {
        id: 'r', version: '1',
        steps: [{ action: 'click', target: { selector: 'x' } }],
        timeout: 1000,
      };
      const runP = runRule({ rule, taskId: 't', workerId: 'w', env });
      // vitest v4: async variant needed so microtasks between timer ticks drain
      const result = await vi.advanceTimersByTimeAsync(5000).then(() => runP);
      expect(result.status).toBe('failure');
      expect(result.error?.type).toBe('TimeoutError');
    } finally {
      vi.useRealTimers();
    }
  });

  it('rule.maxRetries=0 disables retry entirely (even when step.retry=5)', async () => {
    const { env } = makeMockEnv({
      findElement: vi.fn(async () => { throw new Error('ElementNotFound: x'); }),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' }, retry: 5 }],
      maxRetries: 0,
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.findElement as any).mock.calls.length).toBe(1);
  });

  it('keeps retry disabled when the rule has no retry fields', async () => {
    const { env } = makeMockEnv({
      findElement: vi.fn(async () => { throw new Error('ElementNotFound: x'); }),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' } }],
    };
    const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.findElement as any).mock.calls.length).toBe(1);
    expect(result.status).toBe('failure');
  });
});

describe('runRule ruleDefaults: checkpoint serialization', () => {
  it('ruleDefaults is NOT included in CheckpointState', async () => {
    const capturedCheckpoint: any = {};
    const { env } = makeMockEnv({
      findElement: vi.fn(async () => null),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' } }],
      retry: 3,
    };
    await runRule({
      rule, taskId: 't', workerId: 'w', env,
      onCheckpoint: (snap) => { Object.assign(capturedCheckpoint, snap); },
    });
    expect(capturedCheckpoint).not.toHaveProperty('ruleDefaults');
    expect(capturedCheckpoint).toHaveProperty('extracted');
  });
});

describe('runRule ruleDefaults: humanize merge', () => {
  it('uses the bounded replay humanize policy when the rule omits one', async () => {
    const sleepCalls: number[] = [];
    const { env } = makeMockEnv({
      sleep: vi.fn(async (ms: number) => { sleepCalls.push(ms); }),
      findElement: vi.fn(async () => enabledClickTarget()),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' } }],
    };

    await runRule({ rule, taskId: 't', workerId: 'w', env, humanizeReplay: true });

    expect(sleepCalls.some((ms) => ms >= DEFAULT_REPLAY_HUMANIZE.preDelay![0]
      && ms <= DEFAULT_REPLAY_HUMANIZE.preDelay![1])).toBe(true);
    expect(sleepCalls.some((ms) => ms >= DEFAULT_REPLAY_HUMANIZE.postDelay![0]
      && ms <= DEFAULT_REPLAY_HUMANIZE.postDelay![1])).toBe(true);
    expect(sleepCalls.filter((ms) => ms === 16)).toHaveLength(11);
  });

  it('allows an explicit empty rule policy to opt out', async () => {
    const sleepCalls: number[] = [];
    const { env } = makeMockEnv({
      sleep: vi.fn(async (ms: number) => { sleepCalls.push(ms); }),
      findElement: vi.fn(async () => enabledClickTarget()),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' } }],
      humanize: {},
    };

    await runRule({ rule, taskId: 't', workerId: 'w', env, humanizeReplay: true });

    expect(sleepCalls).toEqual([50]);
  });

  it('uses rule.humanize.preDelay when step has no humanize', async () => {
    const sleepCalls: number[] = [];
    const { env } = makeMockEnv({
      sleep: vi.fn(async (ms: number) => { sleepCalls.push(ms); }),
      findElement: vi.fn(async () => enabledClickTarget()),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'x' } }],
      humanize: { preDelay: [150, 150] },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(sleepCalls).toContain(150);
  });

  it('step.humanize.preDelay overrides rule.humanize.preDelay', async () => {
    const sleepCalls: number[] = [];
    const { env } = makeMockEnv({
      sleep: vi.fn(async (ms: number) => { sleepCalls.push(ms); }),
      findElement: vi.fn(async () => enabledClickTarget()),
    });
    const rule: any = {
      id: 'r', version: '1',
      steps: [{
        action: 'click',
        target: { selector: 'x' },
        humanize: { preDelay: [250, 250] },
      }],
      humanize: { preDelay: [150, 150] },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(sleepCalls).toContain(250);
    expect(sleepCalls).not.toContain(150);
  });
});

describe('runRule sendPolicy: flushInterval', () => {
  it('emits a raw batch mid-rule when flushInterval elapses', async () => {
    vi.useFakeTimers();
    try {
      // sleep must actually block on fake timers for runRule to still be
      // in-progress when we advance past flushInterval.
      const { env, calls } = makeMockEnv({
        sleep: vi.fn(async (ms: number) => { await new Promise<void>((r) => setTimeout(r, ms)); }),
      });
      const rule: any = {
        id: 'r', version: '1',
        steps: [
          { action: 'sendResult', payload: { a: 1 }, immediate: false },
          { action: 'waitForUrl', pattern: 'block', matchType: 'equals', timeout: 1000 },
          { action: 'sendResult', payload: { b: 2 }, immediate: false },
        ],
        sendPolicy: { batchSize: 5, flushInterval: 50 },
      };
      const runP = runRule({ rule, taskId: 't', workerId: 'w', env });
      // waitForUrl will sleep-poll every ~step until timeout. Advance 60ms:
      // periodic flushInterval tick fires mid-rule while buffer has {a:1}.
      await vi.advanceTimersByTimeAsync(60);
      const midRuleBatches = calls
        .filter((c) => c.kind === 'result' && Array.isArray(c.payload))
        .flatMap((c) => c.payload);
      // At least the first sendResult's {a:1} must have been flushed by a tick.
      expect(midRuleBatches).toContainEqual({ a: 1 });
      // Drain the rest of the rule lifecycle (waitForUrl timeout fires, drain runs).
      const result = await vi.advanceTimersByTimeAsync(2000).then(() => runP);
      expect(result.status).toBe('failure'); // waitForUrl timed out
      // The mid-rule tick already flushed {a:1}. Buffer was empty at drain.
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval without batchSize does NOT construct BufferingTransport (R8 lock)', async () => {
    vi.useFakeTimers();
    try {
      const spy = vi.spyOn(globalThis, 'setInterval');
      const { env, calls } = makeMockEnv();
      const rule: any = {
        id: 'r', version: '1',
        steps: [{ action: 'extract', name: 'a', target: { selector: 'x' } }],
        sendPolicy: { flushInterval: 100 },
      };
      await runRule({ rule, taskId: 't', workerId: 'w', env });
      expect(spy).not.toHaveBeenCalled();
      // No raw batch payload anywhere — env.transport is the original.
      expect(calls.some((c) => c.kind === 'result' && Array.isArray(c.payload))).toBe(false);
      spy.mockRestore();
    } finally {
      vi.useRealTimers();
    }
  });

  it('non-drain exit (rule throws) clears timer via executor finally dispose()', async () => {
    vi.useFakeTimers();
    try {
      const { env, calls } = makeMockEnv({
        findElement: vi.fn(async () => { throw new Error('ElementNotFound: boom'); }),
      });
      const rule: any = {
        id: 'r', version: '1',
        steps: [{ action: 'click', target: { selector: 'x' } }],
        sendPolicy: { batchSize: 5, flushInterval: 50 },
      };
      const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
      expect(result.status).toBe('failure');
      const batchCountBeforeAdvance = calls.filter((c) => c.kind === 'result' && Array.isArray(c.payload)).length;
      // After runRule settles, advancing fake timers must not produce new ticks.
      await vi.advanceTimersByTimeAsync(300);
      const batchCountAfterAdvance = calls.filter((c) => c.kind === 'result' && Array.isArray(c.payload)).length;
      expect(batchCountAfterAdvance).toBe(batchCountBeforeAdvance);
    } finally {
      vi.useRealTimers();
    }
  });
});
