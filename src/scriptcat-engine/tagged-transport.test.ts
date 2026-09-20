import { describe, it, expect, vi } from 'vitest';
import { TaggedTransport } from './tagged-transport';
import type { Transport, RuntimeContext } from './types';

function makeInner(): { inner: Transport; calls: any[] } {
  const calls: any[] = [];
  const inner = {
    fetchRule: vi.fn(async () => null),
    sendResult: vi.fn(async (payload: any, imm?: boolean) => { calls.push({ kind: 'result', payload, imm }); }),
    sendLog: vi.fn(async (level: string, msg: string, extra?: any) => { calls.push({ kind: 'log', level, msg, extra }); }),
    sendStatus: vi.fn(async (status: string) => { calls.push({ kind: 'status', status }); }),
    sendHeartbeat: vi.fn(async () => { calls.push({ kind: 'heartbeat' }); }),
    sendSnapshot: vi.fn(async (s: any) => { calls.push({ kind: 'snapshot', s }); }),
    flushResults: vi.fn(async () => { calls.push({ kind: 'flush' }); }),
  };
  return { inner: inner as unknown as Transport, calls };
}

describe('TaggedTransport', () => {
  it('never injects ctx.tags into sendResult business payloads', async () => {
    const { inner, calls } = makeInner();
    const ctx: RuntimeContext = { tags: { env: 'prod' } } as any;
    const tagged = new TaggedTransport(inner, () => ctx.tags);
    const payload = { data: 1 };
    await tagged.sendResult(payload, true);
    expect(calls).toEqual([{ kind: 'result', payload, imm: true }]);
    expect(calls[0].payload).toBe(payload);
  });

  it('merges ctx.tags into sendLog extra (existing extra keys win)', async () => {
    const { inner, calls } = makeInner();
    const ctx: RuntimeContext = { tags: { env: 'prod', shared: 'ctx' } } as any;
    const tagged = new TaggedTransport(inner, () => ctx.tags);
    await tagged.sendLog('warn', 'msg', { shared: 'extra', extraKey: 1 });
    expect(calls[0].extra).toEqual({ env: 'prod', shared: 'extra', extraKey: 1 });
  });

  it('adds ctx.tags as extra when sendLog called without extra', async () => {
    const { inner, calls } = makeInner();
    const ctx: RuntimeContext = { tags: { env: 'prod' } } as any;
    const tagged = new TaggedTransport(inner, () => ctx.tags);
    await tagged.sendLog('warn', 'msg');
    expect(calls[0].extra).toEqual({ env: 'prod' });
  });

  it('reads ctx.tags live for logs without changing later results', async () => {
    const { inner, calls } = makeInner();
    const ctx: RuntimeContext = {} as any;
    const tagged = new TaggedTransport(inner, () => ctx.tags);
    await tagged.sendLog('info', 'before');
    ctx.tags = { env: 'prod' };
    await tagged.sendLog('info', 'after');
    await tagged.sendResult({ a: 2 });
    expect(calls[0].extra).toBeUndefined();
    expect(calls[1].extra).toEqual({ env: 'prod' });
    expect(calls[2].payload).toEqual({ a: 2 });
  });

  it('no-op when ctx.tags is undefined', async () => {
    const { inner, calls } = makeInner();
    const tagged = new TaggedTransport(inner, () => undefined);
    await tagged.sendResult({ a: 1 });
    await tagged.sendLog('warn', 'msg', { k: 1 });
    expect(calls[0].payload).toEqual({ a: 1 });
    expect(calls[1].extra).toEqual({ k: 1 });
  });

  it('pass-through methods are forwarded unchanged', async () => {
    const { inner, calls } = makeInner();
    const ctx: RuntimeContext = { tags: { env: 'prod' } } as any;
    const tagged = new TaggedTransport(inner, () => ctx.tags);
    await tagged.sendStatus('running', 'msg');
    await tagged.sendHeartbeat({ beat: 1 });
    await tagged.sendSnapshot({ name: 's', type: 'html', data: 'x' } as any);
    await tagged.flushResults();
    expect(calls.map((c) => c.kind)).toEqual(['status', 'heartbeat', 'snapshot', 'flush']);
  });
});
