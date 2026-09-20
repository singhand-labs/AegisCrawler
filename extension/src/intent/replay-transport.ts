import type { Transport, Rule, Snapshot } from '../../../src/scriptcat-engine/types';
import { serializeDomWithReport } from '../recording/dom-serializer';

const MAX_REPLAY_SNAPSHOT_BYTES = 384 * 1024;
const MAX_REPLAY_SNAPSHOT_NODES = 1500;
const MAX_REPLAY_SNAPSHOT_DEPTH = 40;
const MAX_REPLAY_SNAPSHOT_TEXT_LENGTH = 1000;

function jsonBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    return Number.POSITIVE_INFINITY;
  }
}

function safeReplaySnapshot(snapshot: Snapshot): Snapshot {
  try {
    const serialized = serializeDomWithReport(document.documentElement, {
      maxNodes: MAX_REPLAY_SNAPSHOT_NODES,
      maxDepth: MAX_REPLAY_SNAPSHOT_DEPTH,
      maxTextLength: MAX_REPLAY_SNAPSHOT_TEXT_LENGTH,
    });
    const artifact = {
      format: 'semantic-dom-v1',
      originalType: snapshot.type,
      domTree: serialized.domTree,
      capture: serialized.report,
    };
    const originalBytes = jsonBytes(artifact);
    const data = originalBytes <= MAX_REPLAY_SNAPSHOT_BYTES
      ? JSON.stringify(artifact)
      : JSON.stringify({
          format: 'semantic-dom-v1',
          originalType: snapshot.type,
          domOmitted: true,
          originalBytes,
          capture: { ...serialized.report, truncated: true },
          reason: 'sanitized replay snapshot exceeded the browser artifact budget',
        });
    return { name: snapshot.name, type: 'dom', data };
  } catch {
    return {
      name: snapshot.name,
      type: 'dom',
      data: JSON.stringify({
        format: 'semantic-dom-v1',
        originalType: snapshot.type,
        domOmitted: true,
        reason: 'sanitized replay snapshot serialization failed',
      }),
    };
  }
}

export class ReplayTransport implements Transport {
  private rule: Rule | null = null;
  private cancelled = false;

  setRule(rule: Rule): void {
    this.rule = rule;
  }

  setCancelled(cancelled: boolean): void {
    this.cancelled = cancelled;
  }

  async fetchRule(_ruleId: string): Promise<Rule | null> {
    return this.rule;
  }

  async sendResult(payload: any, immediate = false): Promise<void> {
    // §5.3: __final is the executor's terminal flush and would be broadcast as
    // REPLAY_PROGRESS here, racing the authoritative REPLAY_COMPLETE from
    // replay-runner. Suppress it so intent-page sees one terminal signal only.
    if (payload && payload.__final) return;
    this.postProgress({ type: 'result', payload, immediate });
  }

  async sendLog(level: string, message: string, extra?: Record<string, any>): Promise<void> {
    this.postProgress({ type: 'log', level, message, extra });
  }

  async sendHeartbeat(payload?: Record<string, any>): Promise<{ cancelRequested: boolean }> {
    // M-1: forward heartbeat as a REPLAY_PROGRESS 'status' message so the
    // background's idle timer (120s without progress) is reset. Without this,
    // a slow page load between steps causes the background to tear down the
    // replay even though the runner is alive and heartbeating every 30s.
    this.postProgress({ type: 'status', status: 'heartbeat', message: payload?.url ?? '' });
    return { cancelRequested: this.cancelled };
  }

  async sendStatus(status: string, message?: string): Promise<void> {
    this.postProgress({ type: 'status', status, message });
  }

  async sendSnapshot(snapshot: Snapshot): Promise<void> {
    // BrowserEnvironment snapshots contain literal outerHTML. Never forward
    // that raw page data across the extension boundary: reconstruct a bounded,
    // redacted semantic snapshot and explicitly mark any omitted oversized DOM.
    this.postProgress({ type: 'snapshot', snapshot: safeReplaySnapshot(snapshot) });
  }

  private postProgress(payload: unknown): void {
    if (typeof chrome !== 'undefined' && chrome.runtime?.sendMessage) {
      Promise.resolve(chrome.runtime.sendMessage({ action: 'REPLAY_PROGRESS', payload })).catch(() => undefined);
    }
  }
}
