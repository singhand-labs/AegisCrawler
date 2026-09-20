import type { Rule } from '../scriptcat-engine/types';
import { runRule } from '../scriptcat-engine/executor';
import { HttpTransport, HttpTransportError, type ClaimedTask, type ResultSubmissionState } from './HttpTransport';
import type { BrowserEnvironment } from './BrowserEnvironment';

export interface RuleExecutorContext {
  rule: Rule;
  taskId: string;
  workerId: string;
  variables: Record<string, any>;
  signal: AbortSignal;
  transport: HttpTransport;
}

export interface RuleExecutorResult {
  status: 'success' | 'failure' | 'cancelled';
  message?: string;
  /** Extracted data forwarded as the final execution summary payload. */
  data?: any;
}

export interface WorkerOptions {
  baseUrl: string;
  workerId: string;
  /** Named persistent browser profile in which this worker instance runs. */
  browserProfileId?: string;
  apiKey?: string;
  traceId?: string;
  /**
   * Builds the in-process execution environment used by the default runRule
   * path. Required unless ruleExecutor is provided.
   */
  envFactory?: (transport: HttpTransport, taskId: string, ruleId: string, allowEvaluate?: boolean, allowEvaluateDOM?: boolean) => BrowserEnvironment;
  /**
   * Optional execution seam: when set, runTask delegates rule execution to this
   * callback instead of envFactory + runRule (used by the real-browser worker
   * host, which executes the DSL inside a Chromium page). Claim, heartbeat,
   * cancellation, final summary, and terminal status handling are unchanged.
   */
  ruleExecutor?: (ctx: RuleExecutorContext) => Promise<RuleExecutorResult>;
  allowEvaluate?: boolean;
  allowEvaluateDOM?: boolean;
  pollIntervalMs?: number;
  heartbeatIntervalMs?: number;
  claimBackoffMs?: number;
  maxClaimBackoffMs?: number;
  serverErrorBackoffMs?: number;
}

export class Worker {
  private baseUrl: string;
  private workerId: string;
  private browserProfileId?: string;
  private apiKey?: string;
  private envFactory: WorkerOptions['envFactory'];
  private ruleExecutor?: WorkerOptions['ruleExecutor'];
  private allowEvaluate: boolean;
  private allowEvaluateDOM: boolean;
  private pollIntervalMs: number;
  private heartbeatIntervalMs: number;
  private claimBackoffMs: number;
  private maxClaimBackoffMs: number;
  private serverErrorBackoffMs: number;
  private currentClaimBackoff: number;
  private running = false;
  private traceId?: string;
  private transport: HttpTransport;
  private abortController: AbortController;
  private currentTaskAbort?: AbortController;

  constructor(options: WorkerOptions) {
    if (!options.envFactory && !options.ruleExecutor) {
      throw new Error('Worker requires either envFactory or ruleExecutor');
    }
    this.baseUrl = options.baseUrl.replace(/\/$/, '');
    this.workerId = options.workerId;
    this.browserProfileId = options.browserProfileId?.trim() || undefined;
    this.apiKey = options.apiKey;
    this.traceId = options.traceId;
    this.envFactory = options.envFactory;
    this.ruleExecutor = options.ruleExecutor;
    this.allowEvaluate = options.allowEvaluate ?? false;
    this.allowEvaluateDOM = options.allowEvaluateDOM ?? false;
    this.pollIntervalMs = options.pollIntervalMs ?? 5000;
    this.heartbeatIntervalMs = options.heartbeatIntervalMs ?? 30000;
    this.claimBackoffMs = options.claimBackoffMs ?? 1000;
    this.maxClaimBackoffMs = options.maxClaimBackoffMs ?? 30000;
    this.serverErrorBackoffMs = options.serverErrorBackoffMs ?? 1000;
    this.currentClaimBackoff = this.claimBackoffMs;
    this.abortController = new AbortController();
    this.transport = this.createTransport(this.abortController.signal);
  }

  async start(): Promise<void> {
    this.running = true;
    this.abortController = new AbortController();
    this.currentClaimBackoff = this.claimBackoffMs;
    this.transport = this.createTransport(this.abortController.signal);
    while (this.running) {
      try {
        const task = await this.transport.claimTask();
        this.currentClaimBackoff = this.claimBackoffMs;
        if (task) {
          await this.runTask(task);
        } else {
          await this.sleep(this.pollIntervalMs);
        }
      } catch (err: unknown) {
        if (this.isAbortError(err)) {
          break;
        }
        const error = err as Error;
        const status = err instanceof HttpTransportError ? err.status : 0;
        const retryable = status === 0 || status >= 500;
        if (retryable) {
          // eslint-disable-next-line no-console
          console.error('[Worker] claim error:', error?.message ?? error, error?.stack ?? '');
          this.currentClaimBackoff = Math.min(this.currentClaimBackoff * 2, this.maxClaimBackoffMs);
        } else {
          this.currentClaimBackoff = this.claimBackoffMs;
        }
        await this.sleep(this.currentClaimBackoff);
      }
    }
  }

  stop(): void {
    this.running = false;
    this.abortController.abort();
    this.currentTaskAbort?.abort();
  }

  private isAbortError(err: unknown): boolean {
    return typeof err === 'object' && err !== null && (err as { name?: string }).name === 'AbortError';
  }

  private createTransport(signal?: AbortSignal): HttpTransport {
    return new HttpTransport({
      baseUrl: this.baseUrl,
      workerId: this.workerId,
      taskId: this.workerId,
      browserProfileId: this.browserProfileId,
      apiKey: this.apiKey,
      traceId: this.traceId,
      signal,
      retryDelayMs: this.serverErrorBackoffMs,
      bufferCapacity: 100,
    });
  }

  private createTaskTransport(task: ClaimedTask, signal?: AbortSignal, resultState?: ResultSubmissionState): HttpTransport {
    return new HttpTransport({
      baseUrl: this.baseUrl,
      workerId: this.workerId,
      taskId: task.taskId,
      apiKey: this.apiKey,
      traceId: this.transport.getTraceId(),
      signal,
      retryDelayMs: this.serverErrorBackoffMs,
      bufferCapacity: 100,
      resultState,
    });
  }

  private async runTask(task: ClaimedTask): Promise<void> {
    const taskAbortController = new AbortController();
    this.currentTaskAbort = taskAbortController;
    const taskSignal = taskAbortController.signal;
    const resultState = task.attemptId ? { attemptId: task.attemptId, nextSequence: 0 } : undefined;
    const transport = this.createTaskTransport(task, taskSignal, resultState);
    const terminalTransport = this.createTaskTransport(task, undefined, resultState);

    const env = this.ruleExecutor
      ? undefined
      : this.envFactory!(transport, task.taskId, task.ruleId, this.allowEvaluate, this.allowEvaluateDOM);

    const heartbeatTimer = setInterval(() => {
      transport.sendHeartbeat({ ts: Date.now() })
        .then((resp) => {
          if (resp.cancelRequested) {
            taskAbortController.abort();
          }
        })
        .catch(() => {});
    }, this.heartbeatIntervalMs);

    try {
      await transport.sendStatus('running', `Task ${task.taskId} started`);

      const rule = task.rule ?? await transport.fetchRule(task.ruleId);
      if (!rule) {
        throw new Error(`Rule ${task.ruleId} not found`);
      }

      let status: 'success' | 'failure' | 'cancelled';
      let message: string | undefined;
      let partialData: any;
      if (this.ruleExecutor) {
        const executed = await this.ruleExecutor({
          rule,
          taskId: task.taskId,
          workerId: this.workerId,
          variables: task.variables,
          signal: taskSignal,
          transport,
        });
        status = executed.status;
        message = executed.message;
        partialData = executed.data;
      } else {
        const result = await runRule({ rule, taskId: task.taskId, workerId: this.workerId, variables: task.variables, env: env!, signal: taskSignal });
        status = result.status;
        message = result.message;
        partialData = result.partialData;
      }

      await terminalTransport.sendFinalSummary({ status, message, data: partialData });
      if (status === 'cancelled') {
        await terminalTransport.sendStatus('cancelled', message ?? 'cancelled');
      } else {
        await terminalTransport.sendStatus(status === 'success' ? 'done' : 'failed', message ?? '');
      }
    } catch (err: unknown) {
      if (this.isAbortError(err)) {
        await terminalTransport.sendStatus('cancelled', 'Task cancelled by operator');
        return;
      }
      const error = err as Error;
      await terminalTransport.sendLog('error', String(error?.message ?? error), { stack: error?.stack });
      await terminalTransport.sendStatus('failed', String(error?.message ?? error));
    } finally {
      clearInterval(heartbeatTimer);
      if (this.currentTaskAbort === taskAbortController) {
        this.currentTaskAbort = undefined;
      }
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      if (this.abortController.signal.aborted) {
        resolve();
        return;
      }
      const timer = setTimeout(() => {
        this.abortController.signal.removeEventListener('abort', onAbort);
        resolve();
      }, ms);
      const onAbort = () => {
        clearTimeout(timer);
        this.abortController.signal.removeEventListener('abort', onAbort);
        resolve();
      };
      this.abortController.signal.addEventListener('abort', onAbort);
    });
  }
}
