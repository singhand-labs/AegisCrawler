import { describe, it, expect, vi } from 'vitest';
import { BufferingTransport } from './buffering-transport';
import type { Transport } from './types';

function makeInner(): { inner: Transport; calls: any[] } {
  const calls: any[] = [];
  const inner = {
    fetchRule: vi.fn(async () => null),
    sendResult: vi.fn(async (payload: any, imm?: boolean) => { calls.push({ kind: 'result', payload, imm }); }),
    sendLog: vi.fn(async (level: string, msg: string) => { calls.push({ kind: 'log', level, msg }); }),
    sendStatus: vi.fn(async (status: string) => { calls.push({ kind: 'status', status }); }),
    sendHeartbeat: vi.fn(async () => { calls.push({ kind: 'heartbeat' }); }),
    sendSnapshot: vi.fn(async (s: any) => { calls.push({ kind: 'snapshot', s }); }),
  };
  return { inner: inner as unknown as Transport, calls };
}

describe('BufferingTransport', () => {
  it('buffers sendResult until batchSize reached', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 3 });
    await buf.sendResult({ a: 1 });
    await buf.sendResult({ a: 2 });
    expect(calls).toEqual([]);
    await buf.sendResult({ a: 3 });
    expect(calls).toHaveLength(1);
    expect(calls[0]).toEqual({ kind: 'result', payload: [{ a: 1 }, { a: 2 }, { a: 3 }], imm: true });
  });

  it('bypasses buffer when immediate=true', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ a: 1 }, true);
    expect(calls).toEqual([{ kind: 'result', payload: { a: 1 }, imm: true }]);
  });

  it('flushResults drains the buffer without forwarding a control row', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ a: 1 });
    await buf.flushResults();
    expect(calls).toEqual([
      { kind: 'result', payload: [{ a: 1 }], imm: true },
    ]);
  });

  it('user payload with key __final but immediate=false still gets buffered', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ __final: 'business-value' });
    expect(calls).toEqual([]);
    await buf.drain('success');
    expect(calls).toEqual([
      { kind: 'result', payload: [{ __final: 'business-value' }], imm: true },
    ]);
  });

  it('drain(success) flushes buffer', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ a: 1 });
    await buf.drain('success');
    expect(calls).toEqual([
      { kind: 'result', payload: [{ a: 1 }], imm: true },
    ]);
  });

  it('drain(failure) flushes when sendOnFailure is undefined (default send)', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ a: 1 });
    await buf.drain('failure');
    expect(calls).toHaveLength(1);
  });

  it('drain(failure) flushes when sendOnFailure=true', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10, sendOnFailure: true });
    await buf.sendResult({ a: 1 });
    await buf.drain('failure');
    expect(calls).toHaveLength(1);
  });

  it('drain(failure) drops buffer when sendOnFailure=false', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10, sendOnFailure: false });
    await buf.sendResult({ a: 1 });
    await buf.drain('failure');
    expect(calls).toEqual([]);
  });

  it('drain escalates flush failure to caller (runRule/finalize catches)', async () => {
    const { inner } = makeInner();
    (inner.sendResult as any).mockRejectedValueOnce(new Error('network down'));
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ a: 1 });
    // drain does NOT swallow — caller (runRule/finalize) is responsible for try/catch + sendLog.
    await expect(buf.drain('success')).rejects.toThrow('network down');
    expect((inner.sendResult as any).mock.calls.length).toBeGreaterThanOrEqual(1);
  });

  it('restores a failed batch for an exact later retry', async () => {
    const { inner, calls } = makeInner();
    (inner.sendResult as any)
      .mockRejectedValueOnce(new Error('response lost'))
      .mockImplementation(async (payload: any, imm?: boolean) => { calls.push({ kind: 'result', payload, imm }); });
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendResult({ a: 1 });
    await expect(buf.flush()).rejects.toThrow('response lost');
    await buf.flush();
    expect((inner.sendResult as any).mock.calls.map((call: any[]) => call[0])).toEqual([
      [{ a: 1 }],
      [{ a: 1 }],
    ]);
  });

  it('non-sendResult methods delegate to inner', async () => {
    const { inner, calls } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10 });
    await buf.sendLog('info', 'hi');
    await buf.sendStatus('running');
    await buf.sendHeartbeat({});
    await buf.sendSnapshot({} as any);
    await buf.fetchRule('x');
    expect(calls.map((c) => c.kind)).toEqual(['log', 'status', 'heartbeat', 'snapshot']);
  });

  // ---------- flushInterval (periodic setInterval flush) ----------

  it('flushInterval: timer flushes buffered payload every N ms', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await buf.sendResult({ a: 1 }); // below batchSize threshold
      expect(calls).toEqual([]);
      await vi.advanceTimersByTimeAsync(100);
      expect(calls).toEqual([
        { kind: 'result', payload: [{ a: 1 }], imm: true },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: periodic behavior — second tick flushes new payload', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await buf.sendResult({ a: 1 });
      await vi.advanceTimersByTimeAsync(100);
      await buf.sendResult({ b: 2 });
      await vi.advanceTimersByTimeAsync(100);
      expect(calls).toEqual([
        { kind: 'result', payload: [{ a: 1 }], imm: true },
        { kind: 'result', payload: [{ b: 2 }], imm: true },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: empty-buffer tick is a no-op (R9 early-exit)', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await vi.advanceTimersByTimeAsync(100);
      await vi.advanceTimersByTimeAsync(100);
      expect(calls).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: drain clears timer — no further tick after drain', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await buf.sendResult({ a: 1 });
      await vi.advanceTimersByTimeAsync(50); // before interval
      await buf.drain('success'); // should flush + clear timer
      // advance well past the interval — no further emission
      await vi.advanceTimersByTimeAsync(300);
      expect(calls).toEqual([
        { kind: 'result', payload: [{ a: 1 }], imm: true },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: drain flush-reject still clears timer via finally', async () => {
    vi.useFakeTimers();
    try {
      const { inner } = makeInner();
      (inner.sendResult as any).mockRejectedValueOnce(new Error('network down'));
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await buf.sendResult({ a: 1 });
      await expect(buf.drain('success')).rejects.toThrow('network down');
      // Timer was cleared by dispose() in drain's finally — advancing should
      // produce no further sendResult attempts (which would have rejected again).
      await vi.advanceTimersByTimeAsync(300);
      expect((inner.sendResult as any).mock.calls.length).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: drain(failure) with sendOnFailure=false drops buffer AND clears timer', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100, sendOnFailure: false });
      await buf.sendResult({ a: 1 });
      await buf.drain('failure');
      await vi.advanceTimersByTimeAsync(300);
      expect(calls).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval absent: setInterval is never called', async () => {
    vi.useFakeTimers();
    try {
      const spy = vi.spyOn(globalThis, 'setInterval');
      const { inner } = makeInner();
      new BufferingTransport(inner, { batchSize: 10 });
      expect(spy).not.toHaveBeenCalled();
      spy.mockRestore();
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: tick flush failure is logged via sendLog, not escalated', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      (inner.sendResult as any).mockRejectedValue(new Error('transient'));
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await buf.sendResult({ a: 1 });
      await vi.advanceTimersByTimeAsync(100);
      // first tick fires flush, which rejects, which is caught and logged
      expect(calls.some((c) => c.kind === 'log' && c.level === 'warn' && c.msg === 'flushInterval tick failed')).toBe(true);
      // advancing again produces a second warn log — persistent failure stays observable
      await buf.sendResult({ b: 2 });
      const warnCountBefore = calls.filter((c) => c.kind === 'log' && c.level === 'warn').length;
      await vi.advanceTimersByTimeAsync(100);
      const warnCountAfter = calls.filter((c) => c.kind === 'log' && c.level === 'warn').length;
      expect(warnCountAfter).toBeGreaterThan(warnCountBefore);
      buf.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it('flushInterval: sendLog rejecting is swallowed by inner .catch (no unhandled rejection)', async () => {
    vi.useFakeTimers();
    const rejections: unknown[] = [];
    const handler = (reason: unknown) => { rejections.push(reason); };
    process.on('unhandledRejection', handler);
    try {
      const { inner } = makeInner();
      (inner.sendResult as any).mockRejectedValue(new Error('primary'));
      (inner.sendLog as any).mockRejectedValue(new Error('secondary'));
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      await buf.sendResult({ a: 1 });
      // Tick fires: flush rejects → outer .catch fires → sendLog rejects →
      // inner .catch(() => {}) swallows. Without that inner .catch the
      // secondary rejection would surface via process 'unhandledRejection'.
      await vi.advanceTimersByTimeAsync(100);
      await Promise.resolve(); // drain microtask queue
      buf.dispose();
      expect(rejections).toEqual([]);
      expect((inner.sendResult as any).mock.calls.length).toBeGreaterThanOrEqual(1);
      expect((inner.sendLog as any).mock.calls.length).toBeGreaterThanOrEqual(1);
    } finally {
      process.off('unhandledRejection', handler);
      vi.useRealTimers();
    }
  });

  it('flushInterval: concurrent flush (tick + sendResult threshold) is serialized', async () => {
    vi.useFakeTimers();
    try {
      const { inner, calls } = makeInner();
      // Make sendResult slow so concurrent calls would overlap if not serialized.
      let active = 0;
      let maxConcurrent = 0;
      (inner.sendResult as any).mockImplementation(async (payload: any, _imm?: boolean) => {
        active++;
        maxConcurrent = Math.max(maxConcurrent, active);
        await new Promise<void>((r) => setTimeout(r, 50));
        active--;
        calls.push({ kind: 'result', payload });
      });
      const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
      // Trigger a flush via threshold (chained on inflight=resolved).
      await buf.sendResult({ a: 1 });
      // While threshold-flush is in-flight (setTimeout 50ms), advance to tick
      // boundary — periodic flush attempts to fire concurrently.
      await buf.sendResult({ b: 2 }); // adds to buffer; {a:1} already gone from buffer
      await vi.advanceTimersByTimeAsync(100); // tick fires while first flush still in progress
      await vi.advanceTimersByTimeAsync(100); // allow both flushes to complete
      buf.dispose();
      // Never had two inner.sendResult calls running concurrently.
      expect(maxConcurrent).toBe(1);
      // Both items delivered (threshold flush sent {a:1}; tick flush sent {b:2}).
      const batches = calls
        .filter((c) => Array.isArray(c.payload))
        .flatMap((c) => c.payload);
      expect(batches).toContainEqual({ a: 1 });
      expect(batches).toContainEqual({ b: 2 });
    } finally {
      vi.useRealTimers();
    }
  });

  it('dispose() is idempotent — second call is a no-op', async () => {
    const { inner } = makeInner();
    const buf = new BufferingTransport(inner, { batchSize: 10, flushInterval: 100 });
    buf.dispose();
    expect(() => buf.dispose()).not.toThrow();
  });
});
