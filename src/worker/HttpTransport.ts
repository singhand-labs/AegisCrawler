import type {
  HumanInterventionDecision,
  HumanInterventionRequest,
  Rule,
  Transport,
} from '../scriptcat-engine/types';

export interface ClaimedTask {
  taskId: string;
  attemptId?: string;
  ruleId: string;
  ruleVersion: string;
  ruleVersionNumber?: number;
  rule?: Rule;
  browserProfileId?: string;
  sourceKind?: string;
  sourceAuthority?: string;
  sourceArtifactHash?: string;
  sourceExportHash?: string;
  sourceWorkflowId?: string;
  variables: Record<string, any>;
}

export interface ResultSubmissionState {
  attemptId: string;
  nextSequence: number;
  pending?: {
    kind: 'batch' | 'summary';
    payloadJSON: string;
    immediate: boolean;
    envelope: Record<string, unknown>;
  };
}

export interface HttpTransportOptions {
  baseUrl: string;
  workerId: string;
  taskId: string;
  /** Named persistent browser profile in which this worker is running. */
  browserProfileId?: string;
  apiKey?: string;
  traceId?: string;
  signal?: AbortSignal;
  /** Maximum number of attempts for buffered sends before giving up. */
  maxAttempts?: number;
  retryDelayMs?: number;
  bufferCapacity?: number;
  resultState?: ResultSubmissionState;
  humanPollIntervalMs?: number;
}

interface BufferEntry {
  path: string;
  body: unknown;
}

interface ResultSubmissionAck {
  success: boolean;
  valid: boolean;
  duplicate: boolean;
  error?: string;
}

export class HttpTransportError extends Error {
  constructor(message: string, public status: number) {
    super(message);
    this.name = 'HttpTransportError';
  }
}

export class HttpTransport implements Transport {
  private baseUrl: string;
  private workerId: string;
  private taskId: string;
  private browserProfileId?: string;
  private apiKey?: string;
  private traceId?: string;
  private signal?: AbortSignal;
  private maxAttempts: number;
  private retryDelayMs: number;
  private bufferCapacity: number;
  private buffer: BufferEntry[] = [];
  private flushMutex: Promise<void> = Promise.resolve();
  private resultState?: ResultSubmissionState;
  private humanPollIntervalMs: number;

  constructor(options: HttpTransportOptions) {
    this.baseUrl = options.baseUrl.replace(/\/$/, '');
    this.workerId = options.workerId;
    this.taskId = options.taskId;
    this.browserProfileId = options.browserProfileId?.trim() || undefined;
    this.apiKey = options.apiKey;
    this.traceId = options.traceId;
    this.signal = options.signal;
    this.maxAttempts = options.maxAttempts ?? 3;
    this.retryDelayMs = options.retryDelayMs ?? 1000;
    this.bufferCapacity = options.bufferCapacity ?? 100;
    this.resultState = options.resultState;
    this.humanPollIntervalMs = options.humanPollIntervalMs ?? 1000;
  }

  getTraceId(): string | undefined {
    return this.traceId;
  }

  private isAbortError(err: unknown): boolean {
    return typeof err === 'object' && err !== null && (err as { name?: string }).name === 'AbortError';
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      if (this.signal?.aborted) {
        resolve();
        return;
      }
      const timer = setTimeout(() => {
        this.signal?.removeEventListener('abort', onAbort);
        resolve();
      }, ms);
      const onAbort = () => {
        clearTimeout(timer);
        this.signal?.removeEventListener('abort', onAbort);
        resolve();
      };
      this.signal?.addEventListener('abort', onAbort, { once: true });
    });
  }

  private async withFlushLock<T>(fn: () => Promise<T>): Promise<T> {
    const next = this.flushMutex.then(fn, fn);
    this.flushMutex = next.then(() => {}, () => {});
    return next;
  }

  private async post(path: string, body: unknown): Promise<void> {
    const res = await this.request('POST', path, body);
    if (!res.ok) {
      const text = await res.text();
      throw new HttpTransportError(`HTTP ${res.status}: ${text}`, res.status);
    }
    // Drain successful bodies so Node/Undici can promptly release each
    // connection back to its pool during concurrent worker traffic.
    await res.text();
  }

  private async postResultWithRetry(body: unknown): Promise<ResultSubmissionAck> {
    for (let attempt = 1; attempt <= this.maxAttempts; attempt++) {
      try {
        const res = await this.request('POST', '/results', body);
        const text = await res.text();
        if (!res.ok) {
          throw new HttpTransportError(`HTTP ${res.status}: ${text}`, res.status);
        }
        let ack: unknown;
        try {
          ack = JSON.parse(text);
        } catch {
          throw new Error('invalid result acknowledgement: response is not JSON');
        }
        if (typeof ack !== 'object' || ack === null) {
          throw new Error('invalid result acknowledgement: response is not an object');
        }
        const candidate = ack as Partial<ResultSubmissionAck> & { error?: unknown };
        if (typeof candidate.success !== 'boolean' || typeof candidate.valid !== 'boolean' || typeof candidate.duplicate !== 'boolean') {
          throw new Error('invalid result acknowledgement: success, valid, and duplicate must be boolean');
        }
        return {
          success: candidate.success,
          valid: candidate.valid,
          duplicate: candidate.duplicate,
          ...(typeof candidate.error === 'string' ? { error: candidate.error } : {}),
        };
      } catch (err) {
        if (this.isAbortError(err)) throw err;
        const status = err instanceof HttpTransportError ? err.status : 0;
        const retryable = status === 0 || status >= 500;
        if (!retryable || attempt === this.maxAttempts) {
          this.logFailedSend('/results', body, err);
          throw err;
        }
        await this.sleep(this.retryDelayMs);
      }
    }
    throw new Error('result submission exhausted retries');
  }

  private async sendVersionedResult(
    kind: 'batch' | 'summary',
    payload: unknown,
    immediate: boolean,
  ): Promise<void> {
    const state = this.resultState;
    if (!state?.attemptId) {
      throw new Error('versioned result state is required');
    }
    const payloadJSON = JSON.stringify(payload);
    let pending = state.pending;
    if (pending) {
      if (pending.kind !== kind || pending.payloadJSON !== payloadJSON || pending.immediate !== immediate) {
        throw new Error(`pending ${pending.kind} result must be acknowledged before sending ${kind}`);
      }
    } else {
      const sequence = ++state.nextSequence;
      pending = {
        kind,
        payloadJSON,
        immediate,
        envelope: {
          taskId: this.taskId,
          workerId: this.workerId,
          attemptId: state.attemptId,
          idempotencyKey: `${state.attemptId}:${sequence}:${kind}`,
          sequence,
          kind,
          immediate,
          payload,
        },
      };
      state.pending = pending;
    }
    const ack = await this.postResultWithRetry(pending.envelope);
    if (state.pending === pending) state.pending = undefined;
    if (!ack.success || !ack.valid) {
      throw new HttpTransportError(
        `result rejected: ${ack.error || 'invalid result payload'}`,
        422,
      );
    }
  }

  private async sendWithRetry(path: string, body: unknown): Promise<void> {
    for (let attempt = 1; attempt <= this.maxAttempts; attempt++) {
      try {
        await this.post(path, body);
        return;
      } catch (err) {
        if (this.isAbortError(err)) {
          throw err;
        }
        const status = err instanceof HttpTransportError ? err.status : 0;
        const retryable = status === 0 || status >= 500;
        if (!retryable || attempt === this.maxAttempts) {
          this.logFailedSend(path, body, err);
          throw err;
        }
        await this.sleep(this.retryDelayMs);
      }
    }
  }

  private logFailedSend(path: string, body: unknown, err?: unknown): void {
    console.error(`[HttpTransport] ${path} failed:`, err);
  }

  private enqueue(entry: BufferEntry): Promise<void> {
    return this.withFlushLock(async () => {
      if (this.buffer.length >= this.bufferCapacity) {
        this.buffer.shift();
      }
      this.buffer.push(entry);
    });
  }

  private async flushBuffer(): Promise<void> {
    return this.withFlushLock(async () => {
      while (this.buffer.length > 0) {
        const entry = this.buffer[0];
        try {
          await this.sendWithRetry(entry.path, entry.body);
          this.buffer.shift();
        } catch {
          break;
        }
      }
    });
  }

  private async postWithRetry(path: string, body: unknown, buffer: boolean): Promise<void> {
    await this.flushBuffer();
    try {
      await this.sendWithRetry(path, body);
      await this.flushBuffer();
    } catch (err) {
      if (this.isAbortError(err)) {
        return;
      }
      const status = err instanceof HttpTransportError ? err.status : 0;
      const retryable = status === 0 || status >= 500;
      if (buffer && retryable) {
        await this.enqueue({ path, body });
      }
    }
  }

  private async request(method: string, path: string, body?: unknown): Promise<Response> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (this.apiKey) headers['Authorization'] = `Bearer ${this.apiKey}`;
    if (this.traceId) headers['X-Trace-Id'] = this.traceId;
    const res = await fetch(`${this.baseUrl}${path}`, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: this.signal,
    });
    const responseTraceId = typeof res.headers?.get === 'function' ? res.headers.get('X-Trace-Id') : null;
    if (responseTraceId) {
      this.traceId = responseTraceId;
    }
    return res;
  }

  async fetchRule(ruleId: string): Promise<Rule | null> {
    const res = await this.request('GET', `/rules/${encodeURIComponent(ruleId)}`);
    if (!res.ok) return null;
    return (await res.json()) as Rule;
  }

  async claimTask(): Promise<ClaimedTask | null> {
    const res = await this.request('POST', '/tasks/claim', {
      workerId: this.workerId,
      ...(this.browserProfileId ? { browserProfileId: this.browserProfileId } : {}),
    });
    if (res.status === 204) return null;
    if (!res.ok) throw new HttpTransportError(`claim failed: ${res.status}`, res.status);
    const task = (await res.json()) as ClaimedTask;
    if (task.browserProfileId && task.browserProfileId !== this.browserProfileId) {
      throw new HttpTransportError(
        `claim profile mismatch: worker profile ${this.browserProfileId ?? '<unbound>'} cannot execute task profile ${task.browserProfileId}`,
        409,
      );
    }
    return task;
  }

  async requestHuman(request: HumanInterventionRequest): Promise<HumanInterventionDecision> {
    const attemptId = this.resultState?.attemptId;
    if (!attemptId) {
      throw new Error('HumanInterventionUnavailable: execution attempt id is required');
    }
    const created = await this.request('POST', `/tasks/${encodeURIComponent(this.taskId)}/human-interventions`, {
      workerId: this.workerId,
      attemptId,
      type: request.type,
      prompt: request.prompt,
      timeoutMs: request.timeoutMs,
      checkpoint: request.checkpoint,
    });
    if (!created.ok) {
      const body = await created.text();
      throw new HttpTransportError(`human intervention request failed: ${created.status}: ${body}`, created.status);
    }
    let decision = ((await created.json()) as { intervention: HumanInterventionDecision }).intervention;
    const deadline = Date.now() + request.timeoutMs + Math.max(5000, this.humanPollIntervalMs * 2);
    while (decision.status === 'pending') {
      if (this.signal?.aborted) {
        const error = new Error('HumanInterventionCancelled: task was cancelled');
        error.name = 'AbortError';
        throw error;
      }
      if (Date.now() > deadline) {
        throw new Error(`HumanTimeout: ${request.type}`);
      }
      await this.sleep(this.humanPollIntervalMs);
      const query = new URLSearchParams({ workerId: this.workerId, attemptId });
      const response = await this.request(
        'GET',
        `/tasks/${encodeURIComponent(this.taskId)}/human-interventions/${encodeURIComponent(decision.id)}?${query}`,
      );
      if (!response.ok) {
        const body = await response.text();
        throw new HttpTransportError(`human intervention poll failed: ${response.status}: ${body}`, response.status);
      }
      decision = ((await response.json()) as { intervention: HumanInterventionDecision }).intervention;
    }
    switch (decision.status) {
      case 'approved':
        return decision;
      case 'rejected':
        throw new Error('HumanRejected: operator rejected the intervention');
      case 'expired':
        throw new Error(`HumanTimeout: ${request.type}`);
      case 'cancelled': {
        const error = new Error('HumanInterventionCancelled: task was cancelled');
        error.name = 'AbortError';
        throw error;
      }
      default:
        throw new Error(`HumanInterventionInvalidStatus: ${decision.status}`);
    }
  }

  async sendResult(payload: any, immediate?: boolean): Promise<void> {
    // Versioned workers submit the authoritative execution summary through
    // sendFinalSummary(). The executor's __final payload is only an internal
    // completion marker and must not be validated as collected output data.
    // Legacy transports have no attempt state, so retain their existing wire
    // behavior for backward compatibility.
    if (this.resultState?.attemptId && this.isInternalResultControl(payload)) {
      return;
    }
    const envelope: Record<string, unknown> = {
      taskId: this.taskId,
      workerId: this.workerId,
      immediate: immediate ?? false,
      payload,
    };
    if (this.resultState?.attemptId) {
      await this.sendVersionedResult('batch', payload, immediate ?? false);
      return;
    }
    await this.postWithRetry('/results', envelope, true);
  }

  async flushResults(): Promise<void> {
    // The engine's BufferingTransport owns result buffering. An unbuffered
    // transport has no local rows to flush and must not emit a sentinel row.
  }

  async sendFinalSummary(payload: any): Promise<void> {
    if (!this.resultState?.attemptId) {
      await this.sendResult(payload, true);
      return;
    }
    await this.sendVersionedResult('summary', payload, true);
  }

  async sendLog(level: string, message: string, extra?: Record<string, any>): Promise<void> {
    await this.postWithRetry('/logs', {
      taskId: this.taskId,
      workerId: this.workerId,
      level,
      message,
      traceId: this.traceId,
      extra,
    }, true);
  }

  async sendHeartbeat(payload?: Record<string, any>): Promise<{ cancelRequested: boolean }> {
    for (let attempt = 1; attempt <= this.maxAttempts; attempt++) {
      try {
        const res = await this.request('POST', '/heartbeat', {
          taskId: this.taskId,
          workerId: this.workerId,
          payload,
        });
        if (!res.ok) {
          const text = await res.text();
          throw new HttpTransportError(`heartbeat failed: ${res.status}: ${text}`, res.status);
        }
        const body = (await res.json()) as { cancelRequested?: boolean };
        return { cancelRequested: body.cancelRequested ?? false };
      } catch (err) {
        if (this.isAbortError(err)) {
          return { cancelRequested: false };
        }
        if (attempt === this.maxAttempts) {
          console.error('[HttpTransport] heartbeat failed after retries:', err);
          return { cancelRequested: false };
        }
        await this.sleep(this.retryDelayMs);
      }
    }
    return { cancelRequested: false };
  }

  async sendStatus(status: string, message?: string): Promise<void> {
    // runRule reports execution-level terminal states before Worker submits the
    // authoritative summary and task-level terminal state. Do not send those
    // internal states to the versioned worker API (which uses done/failed/
    // cancelled); legacy transports retain their historical behavior.
    if (this.resultState?.attemptId && ['success', 'failure', 'cancelled'].includes(status)) {
      return;
    }
    const body = {
      taskId: this.taskId,
      workerId: this.workerId,
      ...(this.resultState?.attemptId ? { attemptId: this.resultState.attemptId } : {}),
      status,
      message,
    };
    if (this.resultState?.attemptId && ['done', 'failed', 'cancelled', 'dead_letter'].includes(status)) {
      await this.sendWithRetry('/status', body);
      return;
    }
    await this.postWithRetry('/status', body, true);
  }

  private isInternalResultControl(payload: unknown): boolean {
    if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) return false;
    const marker = payload as Record<string, unknown>;
    return marker.__final === true || marker.__flush === true || marker.__criticalFailure === true;
  }

  async sendSnapshot(snapshot: { name: string; type: 'html' | 'dom' | 'screenshot'; data: string }): Promise<void> {
    await this.post('/snapshots', {
      taskId: this.taskId,
      workerId: this.workerId,
      name: snapshot.name,
      type: snapshot.type,
      data: snapshot.data,
    });
  }
}
