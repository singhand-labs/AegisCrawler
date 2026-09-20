import type { Action, Rule, Target, Condition, RetryConfig, ExtractField, Humanize } from '../rule-engine/types/action';

export { Action, Rule, Target, Condition, RetryConfig, ExtractField, Humanize };

export interface RuntimeContext {
  taskId: string;
  ruleId: string;
  ruleVersion: string;
  workerId: string;
  variables: Record<string, any>;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
  loopIndex?: number;
  loopItem?: any;
  page?: { url: string; title: string };
  lastStepId?: string;
  lastError?: ExecutionError;
  checkpoint?: CheckpointState;
  resultsSent: number;
  logsSent: number;
  /**
   * Legacy tag set accumulated by `setTag` actions and attached only to
   * `sendLog` extras. It must never alter schema-validated result payloads.
   * Scope enforcement and checkpoint/navigation resume are not implemented.
   */
  tags?: Record<string, string>;
  ruleDefaults?: {
    humanize?: Humanize;
    retry?: number | RetryConfig;
    timeout?: number;
    maxRetries?: number;
  };
}

export interface CheckpointState {
  stepId: string;
  url: string;
  variables: Record<string, any>;
  extracted: Record<string, any>;
  timestamp: number;
}

export interface ExecutionError {
  type: string;
  message: string;
  stepId?: string;
  retryCount?: number;
  stack?: string;
  recoverable: boolean;
}

export interface ExecutionResult {
  status: 'success' | 'failure' | 'cancelled';
  message?: string;
  partialData?: any;
  error?: ExecutionError;
  snapshots?: Snapshot[];
}

export interface Snapshot {
  name: string;
  type: 'html' | 'dom' | 'screenshot';
  data: string;
}

export interface HumanInterventionRequest {
  type: 'captcha' | '2fa' | 'confirmation' | 'generic';
  prompt: string;
  timeoutMs: number;
  checkpoint: {
    stepId: string;
    url: string;
  };
}

export interface HumanInterventionDecision {
  id: string;
  checkpointId: string;
  status: 'pending' | 'approved' | 'rejected' | 'expired' | 'cancelled';
  expiresAt: string;
}

export interface ActionHandler<T extends Action = Action> {
  (action: T, ctx: RuntimeContext, env: Environment): Promise<any>;
}

export interface Environment {
  /** 获取元素 */
  findElement(target: Target, timeout?: number): Promise<Element | null>;
  findElements(target: Target, timeout?: number): Promise<Element[]>;
  /** 等待 */
  sleep(ms: number): Promise<void>;
  /** 当前时间 */
  now(): number;
  /** 与服务端通信 */
  transport: Transport;
  /** 页面 URL */
  getUrl(): string;
  /** 页面标题 */
  getTitle(): string;
  /** 执行自定义脚本；ctx 包含 variables/extracted/evaluated/loopIndex/loopItem */
  evaluate(script: string, ctx?: any, args?: any[]): Promise<any>;
  /** 截图 */
  screenshot(name: string): Promise<Snapshot | null>;
  /** 保存快照 */
  saveSnapshot(name: string, type?: 'html' | 'dom' | 'screenshot'): Promise<Snapshot | null>;
  /** Browser history back. Throws UnsupportedInEnvironment if unsupported. */
  goBack?(): Promise<void>;
  /** Browser history forward. Throws UnsupportedInEnvironment if unsupported. */
  goForward?(): Promise<void>;
  /**
   * Resize the page viewport. Throws UnsupportedInEnvironment under
   * content-script hosts. Even where window.resizeTo works (popup tabs),
   * deviceScaleFactor / isMobile / hasTouch / userAgent require DevTools
   * Protocol access — only Playwright Worker can honor the full contract.
   */
  setViewport?(viewport: {
    width: number;
    height: number;
    deviceScaleFactor?: number;
    isMobile?: boolean;
    hasTouch?: boolean;
    userAgent?: string;
  }): Promise<void>;
  /**
   * Resolve when the page has had no newly completed resource entries for
   * idleTimeMs. This browser-level heuristic cannot count requests that were
   * already in flight when observation began.
   * Reject with TimeoutError when timeoutMs elapses without idle. Throws
   * UnsupportedInEnvironment when no network observation mechanism exists.
   */
  waitForNetworkIdle?(idleTimeMs: number, timeoutMs: number): Promise<void>;
  /** 任务取消信号 */
  signal?: AbortSignal;
}

/** Authoritative execution outcome reported once per attempt (versioned tasks). */
export interface FinalSummaryPayload {
  status: 'success' | 'failure' | 'cancelled';
  message?: string;
  data?: unknown;
}

export interface Transport {
  fetchRule(ruleId: string): Promise<Rule | null>;
  sendResult(payload: any, immediate?: boolean): Promise<void>;
  /** Flush locally buffered result rows without emitting a control payload. */
  flushResults?(): Promise<void>;
  /**
   * Submit the single authoritative execution summary for an attempt-scoped
   * task. Legacy transports without attempt state fall back to a result row.
   */
  sendFinalSummary?(payload: FinalSummaryPayload): Promise<void>;
  sendLog(level: string, message: string, extra?: Record<string, any>): Promise<void>;
  sendHeartbeat(payload?: Record<string, any>): Promise<{ cancelRequested: boolean }>;
  sendStatus(status: string, message?: string): Promise<void>;
  sendSnapshot(snapshot: Snapshot): Promise<void>;
  /** Pause this exact execution attempt until a checkpoint-bound operator decision. */
  requestHuman?(request: HumanInterventionRequest): Promise<HumanInterventionDecision>;
}
