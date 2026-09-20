import { describe, it, expect, vi } from 'vitest';
import { runRule } from './executor';

function makeEnv() {
  const sentResults: any[] = [];
  return {
    env: {
      signal: undefined,
      getUrl: () => 'https://example.com/',
      getTitle: () => 'T',
      sleep: vi.fn(async () => {}),
      evaluate: vi.fn(async () => undefined),
      findElement: vi.fn(async () => null),
      findElements: vi.fn(async () => []),
      now: () => 0,
      screenshot: vi.fn(async () => null),
      saveSnapshot: vi.fn(async () => null),
      transport: {
        fetchRule: vi.fn(async () => null),
        sendResult: vi.fn(async (p: any, imm?: boolean) => { sentResults.push({ p, imm }); }),
        sendLog: vi.fn(async () => {}),
        sendStatus: vi.fn(async () => {}),
        sendHeartbeat: vi.fn(async () => ({ cancelRequested: false })),
        sendSnapshot: vi.fn(async () => {}),
      },
    } as any,
    sentResults,
  };
}

describe('runRule sendPolicy integration', () => {
  it('wraps env.transport with BufferingTransport when sendPolicy.batchSize is set', async () => {
    const { env, sentResults } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [
        { action: 'sendResult', payload: { a: 1 }, immediate: false },
        { action: 'sendResult', payload: { b: 2 }, immediate: false },
      ],
      sendPolicy: { batchSize: 10 },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    const batch = sentResults.find((r) => Array.isArray(r.p));
    expect(batch).toBeDefined();
    expect(batch.p).toEqual([{ a: 1 }, { b: 2 }]);
    expect(sentResults.find((r) => r.p?.__final)).toBeDefined();
  });

  it('does NOT wrap env.transport when sendPolicy is absent', async () => {
    const { env, sentResults } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'sendResult', payload: { a: 1 }, immediate: false }],
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(sentResults.find((r) => Array.isArray(r.p))).toBeUndefined();
    expect(sentResults.find((r) => r.p?.a === 1)).toBeDefined();
  });

  it('restores env.transport after runRule exits', async () => {
    const { env } = makeEnv();
    const original = env.transport;
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      sendPolicy: { batchSize: 5 },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(env.transport).toBe(original);
  });

  it('restores env.transport even on failure path', async () => {
    const { env } = makeEnv();
    const original = env.transport;
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'nonexistentActionXYZ' }],
      sendPolicy: { batchSize: 5 },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(env.transport).toBe(original);
  });

  it('multi-call env reuse does not nest wrappers', async () => {
    const { env } = makeEnv();
    const original = env.transport;
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      sendPolicy: { batchSize: 5 },
    };
    await runRule({ rule, taskId: 't1', workerId: 'w', env });
    await runRule({ rule, taskId: 't2', workerId: 'w', env });
    expect(env.transport).toBe(original);
  });

  it('drain drops buffer on failure when sendOnFailure=false', async () => {
    const { env, sentResults } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [
        { action: 'sendResult', payload: { a: 1 }, immediate: false },
        { action: 'nonexistentActionXYZ' },
      ],
      sendPolicy: { batchSize: 10, sendOnFailure: false },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(sentResults.find((r) => Array.isArray(r.p))).toBeUndefined();
    expect(sentResults.find((r) => r.p?.__final)).toBeDefined();
  });

  it('turns a successful rule into failure when its final batch cannot drain', async () => {
    const { env } = makeEnv();
    env.transport.sendResult = vi.fn(async (payload: unknown) => {
      if (Array.isArray(payload)) throw new Error('result acknowledgement lost');
    });
    const result = await runRule({
      rule: {
        id: 'r', version: '1',
        steps: [{ action: 'sendResult', payload: { a: 1 }, immediate: false }],
        sendPolicy: { batchSize: 10 },
      } as any,
      taskId: 't', workerId: 'w', env,
    });
    expect(result.status).toBe('failure');
    expect(result.error).toMatchObject({ type: 'TransportError' });
  });
});
