/// <reference types="vitest/globals" />
import { vi } from 'vitest';
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

describe('validateData (via runRule)', () => {
  beforeEach(() => {
    document.body.innerHTML = `<div id="el">42</div>`;
  });

  it('passes through when no schema', async () => {
    const res = await run([
      { action: 'extractText', name: 't', target: { selector: '#el' } },
      { action: 'validateData', from: 'extracted.t' },
      { action: 'sendResult', data: 'ok' },
    ]);
    expect(res.status).toBe('success');
  });

  it('passes valid data (schema match)', async () => {
    const res = await run([
      { action: 'extractText', name: 'x', target: { selector: '#el' } },
      { action: 'validateData', from: 'extracted.x', schema: { type: 'string' } },
      { action: 'sendResult', data: 'ok' },
    ]);
    expect(res.status).toBe('success');
  });

  it('throws ValidationError on invalid data with onInvalid=fail (default)', async () => {
    const res = await run([
      { action: 'extractText', name: 'x', target: { selector: '#el' } },
      { action: 'validateData', from: 'extracted.x', schema: { type: 'number' } },
      { action: 'sendResult', data: 'ok' },
    ]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ValidationError');
    expect(res.error?.message.startsWith('ValidationError:')).toBe(true);
  });

  it('warns and continues when onInvalid=warn', async () => {
    const warnings: any[] = [];
    const env = createMockEnv();
    env.transport = {
      ...mockTransport,
      sendLog: async (level: string, msg: string, ctx?: any) => {
        if (level === 'warn') warnings.push({ msg, ctx });
      },
    };
    const res = await run(
      [
        { action: 'extractText', name: 'x', target: { selector: '#el' } },
        {
          action: 'validateData',
          from: 'extracted.x',
          onInvalid: 'warn',
          schema: { type: 'number' },
        },
        { action: 'sendResult', data: 'ok' },
      ],
      env,
    );
    expect(res.status).toBe('success');
    expect(warnings.length).toBe(1);
    expect(warnings[0].msg).toContain('validateData failed');
  });

  it('classifyError identifies ValidationError', () => {
    expect(classifyError(new Error('ValidationError: x → foo'))).toBe('ValidationError');
    // Must NOT be recoverable (rule/data errors are not retryable).
    expect(isRecoverable('ValidationError')).toBe(false);
  });
});
