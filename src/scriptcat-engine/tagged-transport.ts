import type { Transport, Snapshot, RuntimeContext } from './types';

/**
 * Legacy transport proxy that exposes `ctx.tags` only as log metadata.
 * Collection payloads are business data governed by the approved output
 * schema, so sendResult must remain byte-for-byte structurally unchanged.
 *
 * Other Transport methods, including the local flushResults hook, delegate
 * to the wrapped transport unchanged.
 */
export class TaggedTransport implements Transport {
  constructor(
    private inner: Transport,
    private getTags: () => RuntimeContext['tags'],
  ) {}

  fetchRule(ruleId: string) { return this.inner.fetchRule(ruleId); }
  sendStatus(status: any, msg?: string) { return this.inner.sendStatus(status, msg); }
  sendHeartbeat(payload?: any) { return this.inner.sendHeartbeat(payload); }
  sendSnapshot(snap: Snapshot) { return this.inner.sendSnapshot(snap); }
  async flushResults() { await this.inner.flushResults?.(); }
  async requestHuman(req: any) {
    if (!this.inner.requestHuman) throw new Error('HumanInterventionUnavailable');
    return this.inner.requestHuman(req);
  }

  async sendResult(payload: any, immediate?: boolean): Promise<void> {
    return this.inner.sendResult(payload, immediate);
  }

  async sendLog(level: string, message: string, extra?: Record<string, any>): Promise<void> {
    const tags = this.getTags();
    if (tags && extra && typeof extra === 'object') {
      // Existing extra keys win over ctx.tags — action-specific context is
      // more specific than rule-level accumulated tags.
      extra = { ...tags, ...extra };
    } else if (tags && !extra) {
      extra = { ...tags };
    }
    return this.inner.sendLog(level, message, extra);
  }
}
