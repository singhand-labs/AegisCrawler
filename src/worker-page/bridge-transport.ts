/**
 * BridgeTransport — the page-side implementation of the DSL Transport that
 * forwards every server interaction to the worker host through the single
 * exposed-function channel `window.__ocWorkerBridge(method, payload)`.
 *
 * Security contract:
 *   - The bridge is the ONLY host channel available to page JS. It carries no
 *     credentials; the host owns WORKER_API_KEY and performs the actual HTTP.
 *   - fetchRule resolves from the boot payload held in page memory; it never
 *     triggers any network access.
 *   - The executor's terminal `{__final: true}` marker is intercepted here and
 *     never forwarded as a result batch (the host's Worker submits the
 *     authoritative final summary, mirroring HttpTransport's drop logic).
 *   - Snapshots are re-serialized with strict bounds before crossing.
 */

import type {
  HumanInterventionDecision,
  HumanInterventionRequest,
  Rule,
  Snapshot,
  Transport,
} from '../scriptcat-engine/types';
import { sanitizeWorkerSnapshot } from './snapshot';

export type WorkerBridgeFn = (method: string, payload: unknown) => Promise<unknown>;

// H-2: per-call timeout. Without this, a hung host (e.g., server unreachable,
// slow HTTP response) blocks the page executor indefinitely. Generous enough
// for requestHuman (operator can take 120s+) while still bounding the wait.
const BRIDGE_CALL_TIMEOUT_MS = 180_000;

export class BridgeTransport implements Transport {
  private rule: Rule | null = null;
  private signal?: AbortSignal;
  /** The executor's intercepted terminal marker, exposed for the completion event. */
  finalMarker: unknown;

  constructor(signal?: AbortSignal) {
    this.signal = signal;
  }

  setRule(rule: Rule): void {
    this.rule = rule;
  }

  private bridge(): WorkerBridgeFn {
    const fn = (globalThis as Record<string, unknown>).__ocWorkerBridge as WorkerBridgeFn | undefined;
    if (typeof fn !== 'function') {
      throw new Error('WorkerBridgeUnavailable: window.__ocWorkerBridge is not installed by the host');
    }
    return fn;
  }

  private async call(method: string, payload: unknown): Promise<unknown> {
    try {
      // H-2: race the bridge call against a timeout so a slow/hung host
      // surfaces as an error instead of blocking the executor forever.
      return await Promise.race([
        this.bridge()(method, payload),
        new Promise<never>((_, reject) => {
          const timer = setTimeout(() => {
            reject(new Error(`BridgeTimeout: ${method} did not respond within ${BRIDGE_CALL_TIMEOUT_MS}ms`));
          }, BRIDGE_CALL_TIMEOUT_MS);
          // Node's timer has unref; browsers don't — best-effort.
          (timer as unknown as { unref?: () => void }).unref?.();
        }),
      ]);
    } catch (err) {
      // A rejected bridge call during cooperative shutdown must surface as an
      // AbortError so the executor classifies it as cancellation, not failure.
      if (this.signal?.aborted) {
        const abortErr = new Error('Task was cancelled');
        abortErr.name = 'AbortError';
        throw abortErr;
      }
      throw err;
    }
  }

  async fetchRule(_ruleId: string): Promise<Rule | null> {
    return this.rule;
  }

  async sendResult(payload: any, immediate = false): Promise<void> {
    // The __final marker is an internal completion signal, not collected data.
    // The completion event carries the run's partialData; the host's Worker
    // owns sendFinalSummary (same contract as HttpTransport with attempt state).
    if (payload && (payload.__final === true || payload.__flush === true || payload.__criticalFailure === true)) {
      if (payload.__final === true) this.finalMarker = payload;
      return;
    }
    await this.call('sendResult', { payload, immediate });
  }

  async flushResults(): Promise<void> {
    // BufferingTransport, when installed, owns the local buffer. With no
    // buffering configured there is nothing to send.
  }

  async sendLog(level: string, message: string, extra?: Record<string, any>): Promise<void> {
    await this.call('sendLog', { level, message, extra });
  }

  async sendHeartbeat(payload?: Record<string, any>): Promise<{ cancelRequested: boolean }> {
    const resp = await this.call('sendHeartbeat', { payload });
    return { cancelRequested: Boolean((resp as { cancelRequested?: unknown } | null)?.cancelRequested) };
  }

  async sendStatus(status: string, message?: string): Promise<void> {
    await this.call('sendStatus', { status, message });
  }

  async sendSnapshot(snapshot: Snapshot): Promise<void> {
    const safe = sanitizeWorkerSnapshot(snapshot, document.documentElement);
    await this.call('sendSnapshot', safe);
  }

  async requestHuman(request: HumanInterventionRequest): Promise<HumanInterventionDecision> {
    const decision = (await this.call('requestHuman', request)) as HumanInterventionDecision;
    // Return only the decision fields the executor consumes; anything else the
    // host sent stays on the host side.
    return {
      id: String(decision?.id ?? ''),
      checkpointId: String(decision?.checkpointId ?? ''),
      status: decision?.status,
      expiresAt: String(decision?.expiresAt ?? ''),
    };
  }
}
