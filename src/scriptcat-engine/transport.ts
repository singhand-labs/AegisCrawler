import type { FinalSummaryPayload, Rule, Transport } from './types';

declare const GM_xmlhttpRequest: any;

type HttpMethod = 'GET' | 'POST';

// Retry policy for attempt-scoped (versioned) submissions. Mirrors the worker
// host's HttpTransport: only network failures and 5xx responses are retried;
// 4xx responses are contract violations that must surface, never be retried.
const VERSIONED_MAX_ATTEMPTS = 3;
const VERSIONED_RETRY_DELAY_MS = 1_000;

class GmHttpError extends Error {
  constructor(message: string, public status: number) {
    super(message);
    this.name = 'GmHttpError';
  }
}

function gmRequest(method: HttpMethod, url: string, data?: any, authKey?: string): Promise<any> {
  return new Promise((resolve, reject) => {
    const headers: Record<string, string> = {};
    if (authKey) headers['Authorization'] = `Bearer ${authKey}`;
    let body: string | undefined;
    if (data !== undefined) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(data);
    }
    GM_xmlhttpRequest({
      method,
      url,
      headers,
      data: body,
      // H-4: explicit timeout prevents indefinite hang when the server accepts
      // TCP but never responds. 30s matches the default GM_xmlhttpRequest
      // behavior in most userscript managers but makes it explicit.
      timeout: 30_000,
      onload: (res: any) => {
        if (res.status < 200 || res.status >= 300) {
          reject(new GmHttpError(`HTTP ${res.status}${res.statusText ? ` ${res.statusText}` : ''}`, res.status));
          return;
        }
        if (!res.responseText) {
          resolve(null);
          return;
        }
        try {
          resolve(JSON.parse(res.responseText));
        } catch {
          resolve(res.responseText);
        }
      },
      onerror: (err: any) => reject(err),
      ontimeout: () => reject(new Error('Request timeout')),
    });
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export interface TransportOptions {
  /** Execution attempt id from the task claim; enables attempt-scoped reporting. */
  attemptId?: string;
  /** Worker API key; adds Authorization: Bearer to every server call. */
  workerApiKey?: string;
}

export function createTransport(serverBaseUrl: string, taskId: string, workerId: string, options?: TransportOptions): Transport {
  const base = serverBaseUrl.replace(/\/$/, '');
  const attemptId = options?.attemptId?.trim() || undefined;
  const apiKey = options?.workerApiKey?.trim() || undefined;
  let nextSequence = 0;

  function isInternalResultControl(payload: unknown): boolean {
    if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) return false;
    const marker = payload as Record<string, unknown>;
    return marker.__final === true || marker.__flush === true || marker.__criticalFailure === true;
  }

  async function sendVersionedResult(kind: 'batch' | 'summary', payload: unknown, immediate: boolean): Promise<void> {
    if (!attemptId) throw new Error('versioned result state is required');
    const sequence = ++nextSequence;
    const envelope = {
      taskId,
      workerId,
      attemptId,
      idempotencyKey: `${attemptId}:${sequence}:${kind}`,
      sequence,
      kind,
      immediate,
      payload,
    };
    for (let attempt = 1; attempt <= VERSIONED_MAX_ATTEMPTS; attempt++) {
      try {
        const ack = await gmRequest('POST', `${base}/results`, envelope, apiKey) as
          | { success?: unknown; valid?: unknown; duplicate?: unknown; error?: unknown }
          | null;
        const validAck = typeof ack === 'object' && ack !== null
          && typeof ack.success === 'boolean' && typeof ack.valid === 'boolean';
        if (!validAck) {
          throw new Error('invalid result acknowledgement: success and valid must be boolean');
        }
        if (!(ack as { success: boolean }).success || !(ack as { valid: boolean }).valid) {
          throw new GmHttpError(`result rejected: ${String((ack as { error?: unknown }).error ?? 'invalid result payload')}`, 422);
        }
        return;
      } catch (err) {
        const status = err instanceof GmHttpError ? err.status : 0;
        const retryable = status === 0 || status >= 500;
        if (!retryable || attempt === VERSIONED_MAX_ATTEMPTS) throw err;
        await sleep(VERSIONED_RETRY_DELAY_MS);
      }
    }
  }

  return {
    async fetchRule(ruleId: string): Promise<Rule | null> {
      try {
        return await gmRequest('GET', `${base}/rules/${ruleId}`, undefined, apiKey);
      } catch (e) {
        console.error('[AegisCrawler] fetchRule failed', e);
        return null;
      }
    },

    async sendResult(payload: any, immediate = true): Promise<void> {
      if (attemptId) {
        // Versioned workers submit the authoritative execution summary through
        // sendFinalSummary(). The executor's control markers are internal
        // completion signals and must not be validated as collected rows.
        // Submission failures propagate (after retries) so the run fails
        // visibly — a silently dropped batch makes the attempt permanently
        // ineligible for a done transition.
        if (isInternalResultControl(payload)) return;
        await sendVersionedResult('batch', payload, immediate);
        return;
      }
      try {
        await gmRequest('POST', `${base}/results`, {
          taskId,
          workerId,
          immediate,
          payload,
          timestamp: Date.now(),
        }, apiKey);
      } catch (e) {
        console.error('[AegisCrawler] sendResult failed', e);
      }
    },

    async sendFinalSummary(payload: FinalSummaryPayload): Promise<void> {
      if (!attemptId) {
        await this.sendResult({ __final: true, taskId, ...payload }, true);
        return;
      }
      await sendVersionedResult('summary', payload, true);
    },

    async sendLog(level: string, message: string, extra?: Record<string, any>): Promise<void> {
      try {
        await gmRequest('POST', `${base}/logs`, {
          taskId,
          workerId,
          level,
          message,
          extra,
          timestamp: Date.now(),
        }, apiKey);
      } catch (e) {
        console.error('[AegisCrawler] sendLog failed', e);
      }
    },

    async sendHeartbeat(payload?: Record<string, any>): Promise<{ cancelRequested: boolean }> {
      try {
        const res = await gmRequest('POST', `${base}/heartbeat`, {
          taskId,
          workerId,
          payload,
          timestamp: Date.now(),
        }, apiKey);
        return { cancelRequested: (res as { cancelRequested?: boolean } | null)?.cancelRequested ?? false };
      } catch (e) {
        console.error('[AegisCrawler] sendHeartbeat failed', e);
        return { cancelRequested: false };
      }
    },

    async sendStatus(status: string, message?: string): Promise<void> {
      if (attemptId) {
        // runRule reports execution-level terminal states before this transport
        // submits the authoritative summary and the attempt-bound task status.
        // The versioned worker API never accepts those internal states.
        if (status === 'success' || status === 'failure' || status === 'cancelled') return;
        try {
          await gmRequest('POST', `${base}/status`, {
            taskId,
            workerId,
            attemptId,
            status,
            message,
            timestamp: Date.now(),
          }, apiKey);
        } catch (e) {
          console.error('[AegisCrawler] sendStatus failed', e);
        }
        return;
      }
      try {
        const serverStatus = status === 'success' ? 'done' : status === 'failure' ? 'failed' : status;
        await gmRequest('POST', `${base}/status`, {
          taskId,
          workerId,
          status: serverStatus,
          message,
          timestamp: Date.now(),
        }, apiKey);
      } catch (e) {
        console.error('[AegisCrawler] sendStatus failed', e);
      }
    },

    async sendSnapshot(snapshot: { name: string; type: string; data: string }): Promise<void> {
      try {
        await gmRequest('POST', `${base}/snapshots`, {
          taskId,
          workerId,
          ...snapshot,
          timestamp: Date.now(),
        }, apiKey);
      } catch (e) {
        console.error('[AegisCrawler] sendSnapshot failed', e);
      }
    },
  };
}
