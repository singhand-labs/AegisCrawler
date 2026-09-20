/// <reference types="vitest/globals" />
import { describe, it, expect, beforeEach } from 'vitest';
import { runRule } from './executor';
import { classifyError, isRecoverable } from './executor-utils';
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

describe('validateData end-to-end (extract → validate → sendResult)', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <h1 id="title">Hello</h1>
      <span id="price">$9.99</span>
      <span id="sku">ABC-123</span>
    `;
  });

  it('object schema passes when all fields match', async () => {
    const res = await run([
      { action: 'extractText', name: 'title', target: { selector: '#title' } },
      { action: 'extractText', name: 'price', target: { selector: '#price' } },
      {
        action: 'validateData',
        from: 'extracted',
        schema: {
          type: 'object',
          required: ['title', 'price'],
          properties: {
            title: { type: 'string', minLength: 1 },
            price: { type: 'string', pattern: '^\\$' },
          },
        },
      },
      { action: 'sendResult', data: 'ok' },
    ]);
    expect(res.status).toBe('success');
  });

  it('object schema fails when required field missing → rule fails with ValidationError', async () => {
    const res = await run([
      { action: 'extractText', name: 'title', target: { selector: '#title' } },
      // Note: no 'price' extraction — required field is absent.
      {
        action: 'validateData',
        from: 'extracted',
        schema: {
          type: 'object',
          required: ['title', 'price'],
          properties: {
            title: { type: 'string' },
            price: { type: 'string' },
          },
        },
      },
      { action: 'sendResult', data: 'ok' },
    ]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ValidationError');
    expect(res.error?.message).toContain('ValidationError');
    expect(isRecoverable(res.error?.type ?? '')).toBe(false);
  });

  it('schema cache: same schema object reused across two validateData steps', async () => {
    // Locks in the WeakMap-caching contract: two steps referencing the SAME
    // schema object should not blow up. (We can't easily assert cache hits
    // without exposing internals; this test guards against regressions where
    // compilation is unstable or throws on second compile.)
    const schema = { type: 'string', minLength: 1 };
    const res = await run([
      { action: 'extractText', name: 'a', target: { selector: '#title' } },
      { action: 'validateData', from: 'extracted.a', schema },
      { action: 'extractText', name: 'b', target: { selector: '#sku' } },
      { action: 'validateData', from: 'extracted.b', schema },
      { action: 'sendResult', data: 'ok' },
    ]);
    expect(res.status).toBe('success');
  });

  it('onInvalid=warn keeps rule running and emits a warn log', async () => {
    const warnings: Array<{ msg: string; ctx?: any }> = [];
    const env = createMockEnv();
    env.transport = {
      ...mockTransport,
      sendLog: async (level: string, msg: string, ctx?: any) => {
        if (level === 'warn') warnings.push({ msg, ctx });
      },
    };
    const res = await run(
      [
        { action: 'extractText', name: 'price', target: { selector: '#price' } },
        {
          action: 'validateData',
          from: 'extracted.price',
          onInvalid: 'warn',
          schema: { type: 'number', minimum: 0 },
        },
        { action: 'sendResult', data: 'ok' },
      ],
      env,
    );
    expect(res.status).toBe('success');
    expect(warnings).toHaveLength(1);
    expect(warnings[0].msg).toContain('validateData failed');
    expect(warnings[0].ctx?.errors).toBeDefined();
  });

  it('classifyError identifies ValidationError end-to-end (defensive)', () => {
    // Belt-and-suspenders: the rule-failure path uses this classification for
    // retry decisions. Lock the contract at the integration boundary too.
    const t = classifyError(new Error('ValidationError: extracted.price → must be number'));
    expect(t).toBe('ValidationError');
    expect(isRecoverable(t)).toBe(false);
  });
});
