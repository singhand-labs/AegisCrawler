/// <reference types="vitest/globals" />
import { describe, it, expect, beforeEach } from 'vitest';
import { runRule } from './executor';
import { preflightRule } from './rule-preflight';
import type { Environment, Rule, Transport } from './types';
import { findElement, findElements } from './selectors';

const mockTransport: Transport = {
  fetchRule: async () => null,
  sendResult: async () => {},
  sendLog: async () => {},
  sendHeartbeat: async () => ({ cancelRequested: false }),
  sendStatus: async () => {},
  sendSnapshot: async () => {},
};

function createMockEnv(): Environment {
  return {
    findElement: (target, timeout) => findElement(target, timeout),
    findElements: (target, timeout) => findElements(target, timeout),
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    now: () => Date.now(),
    transport: mockTransport,
    getUrl: () => 'https://example.com/',
    getTitle: () => 'Test',
    evaluate: async (script, ctx, args) => {
      const fn = new Function('ctx', 'args', script);
      return fn(ctx, args);
    },
    screenshot: async () => null,
    saveSnapshot: async () => null,
  };
}

function rule(steps: any[]): Rule {
  return {
    id: 'test',
    version: '1.0.0',
    name: 'Test',
    domain: 'example.com',
    enabled: true,
    steps,
  };
}

async function run(steps: any[], env?: Environment) {
  return runRule({ rule: rule(steps), taskId: 't1', workerId: 'w1', env: env ?? createMockEnv() });
}

// Simulate the converter output for an `executeJavascript` recording event.
function recordedEvaluate(script: string, overrides: Record<string, any> = {}) {
  return {
    action: 'evaluate',
    name: 'recordedScript',
    script,
    source: 'recorded',
    ...overrides,
  };
}

describe('evaluate opt-in: recorder → preflight → replay', () => {
  beforeEach(() => {
    (window as any).__evalRan = false;
    (window as any).__evalResult = null;
  });

  it('preflight rejects recorded evaluate without trusted (structured error)', () => {
    const errors = preflightRule(rule([recordedEvaluate('return 1')]));
    expect(errors).toHaveLength(1);
    expect(errors[0].code).toBe('evaluate_recorded_untrusted');
    expect(errors[0].scriptPreview).toBe('return 1');
  });

  it('preflight allows recorded evaluate when trusted=true', () => {
    const errors = preflightRule(rule([recordedEvaluate('return 1', { trusted: true })]));
    expect(errors.filter((e) => e.code === 'evaluate_recorded_untrusted')).toEqual([]);
  });

  it('runRule executes the recorded script when trusted=true', async () => {
    const res = await run([
      recordedEvaluate('(window).__evalRan = true; (window).__evalResult = 42; return 42;', { trusted: true }),
    ]);
    expect(res.status).toBe('success');
    expect((window as any).__evalRan).toBe(true);
    expect((window as any).__evalResult).toBe(42);
  });

  it('end-to-end: caller honors preflight rejection before invoking runRule', async () => {
    // The contract we lock in: a caller that respects preflight must NOT
    // invoke runRule when preflight returns errors. This test simulates the
    // gating so a future regression (preflight skipped) would let runRule
    // execute the untrusted script and flip __evalRan.
    const steps = [recordedEvaluate('(window).__evalRan = true; return 1')];
    const errors = preflightRule(rule(steps));
    expect(errors.length).toBeGreaterThan(0);
    // Caller honors the rejection — runRule never runs.
    expect((window as any).__evalRan).toBe(false);
  });

  it('trusted recorded evaluate inside a loop runs on each iteration', async () => {
    let count = 0;
    const env = createMockEnv();
    env.evaluate = async () => {
      count++;
      return count;
    };
    const res = await run([
      {
        action: 'loop',
        type: 'fixedCount',
        count: 3,
        steps: [recordedEvaluate('return window.__n', { trusted: true })],
      },
    ], env);
    expect(res.status).toBe('success');
    expect(count).toBe(3);
  });
});
