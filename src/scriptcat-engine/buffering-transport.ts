import type { Transport, Snapshot } from './types';

/**
 * Transport proxy that buffers sendResult calls until batchSize is reached,
 * then forwards them as a raw array accepted by the versioned result API.
 * Callers that pass immediate=true bypass the buffer. Explicit flush actions
 * call flushResults(), which drains locally without emitting a control row.
 *
 * When flushInterval > 0, a periodic setInterval wakes every N ms to flush
 * whatever has accumulated. Tick failures are caught and routed through
 * inner.sendLog('warn', ...) — they must NOT escalate or kill the rule
 * (transient timer-tick failures should be observable but non-fatal).
 *
 * drain(status) is called by runRule at end-of-rule; on failure, the buffer
 * is dropped unless policy.sendOnFailure !== false. Flush failures escalate
 * to the caller — runRule/finalize wrap drain in try/catch + sendLog. The
 * drain() try/finally has NO catch block: clearInterval runs in `finally`
 * so the timer is cleared even when flush rejects, while the rejection
 * continues to propagate (escalation contract preserved).
 */
export class BufferingTransport implements Transport {
  private buffer: any[] = [];
  private retryBatch: any[] | undefined;
  private timer: ReturnType<typeof setInterval> | undefined;
  // Serializes concurrent flush calls onto a single promise chain so that
  // inner.sendResult is never invoked twice in parallel for batch flushes
  // (protects non-reentrant transports and prevents item duplication when
  // tick + drain race). See flush() for the chaining protocol.
  private inflight: Promise<void> = Promise.resolve();

  constructor(
    private inner: Transport,
    private policy: { batchSize: number; flushInterval?: number; sendOnFailure?: boolean },
  ) {
    if (policy.flushInterval && policy.flushInterval > 0) {
      this.timer = setInterval(() => {
        this.flush().catch((e: any) => {
          // Best-effort: log via inner transport. sendLog is required on the
          // Transport interface, so no guard needed. The chained .catch
          // guards against the case where the inner transport is *also*
          // failing (network down, MV3 context invalidated) — the exact
          // motivation scenario. Without it, the secondary rejection would
          // leak as an unhandled rejection.
          this.inner
            .sendLog('warn', 'flushInterval tick failed', { error: String(e?.message ?? e) })
            .catch(() => { /* inner transport also failing: nothing more we can do */ });
        });
      }, policy.flushInterval);
    }
  }

  fetchRule(ruleId: string) { return this.inner.fetchRule(ruleId); }
  sendLog(level: any, msg: string, payload?: any) { return this.inner.sendLog(level, msg, payload); }
  sendStatus(status: any, msg?: string) { return this.inner.sendStatus(status, msg); }
  sendHeartbeat(payload?: any) { return this.inner.sendHeartbeat(payload); }
  sendSnapshot(snap: Snapshot) { return this.inner.sendSnapshot(snap); }
  flushResults() { return this.flush(); }
  async requestHuman(req: any) {
    if (!this.inner.requestHuman) throw new Error('HumanInterventionUnavailable');
    return this.inner.requestHuman(req);
  }

  async sendResult(payload: any, immediate?: boolean): Promise<void> {
    if (immediate) {
      return this.inner.sendResult(payload, true);
    }
    // M-8: cap buffer to prevent unbounded memory growth when the inner
    // transport is persistently failing (retryBatch accumulates) or the rule
    // extracts many results faster than flushInterval can drain.
    const maxBufferSize = Math.max(this.policy.batchSize * 10, 1000);
    if (this.buffer.length >= maxBufferSize) {
      this.buffer.shift();
    }
    this.buffer.push(payload);
    if (this.buffer.length >= this.policy.batchSize) await this.flush();
  }

  /**
   * Internal: snapshot the buffer and forward as a raw array. Preserve a
   * failed snapshot separately from rows received while it was in flight so
   * the exact batch can be retried before any newer data.
   * Caller is responsible for serialization — flush() wraps this in the
   * inflight chain.
   */
  private async doFlush(): Promise<void> {
    while (this.retryBatch?.length || this.buffer.length) {
      const items = this.retryBatch ?? this.buffer;
      if (!this.retryBatch) this.buffer = [];
      try {
        await this.inner.sendResult(items, true);
        this.retryBatch = undefined;
      } catch (error) {
        this.retryBatch = items;
        throw error;
      }
    }
  }

  /**
   * Flush the buffer. Concurrent calls are serialized onto the internal
   * inflight chain so inner.sendResult is never invoked twice in parallel
   * for batch flushes — protects against the tick-vs-drain race and any
   * sendResult-triggered flush overlapping with a periodic tick. Each
   * caller awaits its own tail: a rejection propagates to that caller
   * while the chain itself stays intact for subsequent flushes (the chain
   * reference is .catch-swallowed so it never enters a permanently-broken
   * state).
   */
  async flush(): Promise<void> {
    const tail = this.inflight.then(() => this.doFlush());
    this.inflight = tail.catch(() => { /* chain stays intact across rejections */ });
    await tail;
  }

  async drain(status: 'success' | 'failure'): Promise<void> {
    // Wait for any in-flight flush first so drain's final flush observes
    // a stable buffer state and we never return while sendResult is still
    // outstanding. Rejections here were already observed by the original
    // caller; swallow so drain's own error path runs cleanly.
    try { await this.inflight; } catch { /* observed by prior caller */ }
    try {
      if (status === 'success' || this.policy.sendOnFailure !== false) {
        await this.flush();
      } else {
        this.buffer = [];
        this.retryBatch = undefined;
      }
    } finally {
      this.dispose();
    }
  }

  /**
   * Clear the periodic flush timer. Idempotent — safe to call from both
   * drain()'s finally and the executor.ts finally block. No buffer
   * interaction: drain() owns buffer lifecycle.
   */
  dispose(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
  }
}
