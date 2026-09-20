import type { Action, Rule, RuntimeContext, Environment, ExecutionResult, ExecutionError, Snapshot, Humanize } from './types';
import { runSteps, classifyError } from './executor-utils';
import { ExitSignal } from './signals';
import { executeAction } from './actions';
import { EMPTY_PATH, type StepPath } from './step-path';
import { BufferingTransport } from './buffering-transport';
import { TaggedTransport } from './tagged-transport';
import { validateOutput } from './output-validation';
import { interpolate } from './utils';

export interface CheckpointNavigationExpectation {
  phase: 'before-navigation' | 'after-step';
  expectedUrl: string;
  urlPolicy: 'expected-path' | 'same-origin';
}

export type HookPhase = 'beforeAll' | 'afterAll' | 'onError' | 'cleanup';

export interface CheckpointState {
  lastCompletedStepPath: StepPath;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
  /**
   * Hook phases that have completed during this run. Resume skips these
   * to avoid duplicate side effects (D-2). Hooks remain atomic: a phase
   * interrupted mid-execution by SW eviction is re-run from its start.
   */
  hooksCompleted?: HookPhase[];
  /**
   * Browser replay hint for validating the page that loads after a checkpoint.
   * Explicit navigate/reload actions have a known target path. Event-driven
   * actions such as click can navigate, but their destination is not reliably
   * known before dispatch, so they are constrained to the current origin.
   */
  navigation?: CheckpointNavigationExpectation;
}

export interface ExecutorOptions {
  rule: Rule;
  taskId: string;
  workerId: string;
  variables?: Record<string, any>;
  env: Environment;
  signal?: AbortSignal;
  resumeStepPath?: StepPath;
  initialContext?: Partial<Pick<RuntimeContext, 'extracted' | 'evaluated' | 'captured'>>;
  /** Hook phases already completed before this run (resume path). */
  initialHooksCompleted?: HookPhase[];
  onCheckpoint?: (checkpoint: CheckpointState) => void;
  // §4.1 / §5.6 of docs/replay-fix-plan.md, generalised to nested paths in
  // Phase 3. Forwarded to the top-level runSteps call; flow-control handlers
  // thread it through their inner runSteps so navigation inside if/loop/switch/
  // group triggers rollback at the right path. Defaults to undefined → zero
  // behavior change for worker / userscript entry points.
  onStepFailure?: (path: StepPath) => void;
  /** Apply bounded human-like defaults when this invocation is an interactive DSL replay. */
  humanizeReplay?: boolean;
}

/**
 * Conservative pacing for replayed rules that do not declare a root policy.
 *
 * Keep this at the executor boundary so generated, corrected, imported, and
 * hand-authored rules behave consistently without requiring an LLM to emit
 * timing steps. An explicitly present root `humanize` object (including `{}`)
 * remains authoritative, and individual steps still override root fields.
 */
export const DEFAULT_REPLAY_HUMANIZE: Humanize = {
  preDelay: [80, 180],
  postDelay: [100, 240],
  moveMouse: true,
  randomOffset: 3,
  typingDelay: [45, 110],
  keyDuration: [35, 90],
};

export async function runRule(options: ExecutorOptions): Promise<ExecutionResult> {
  const { rule, taskId, workerId, variables = {}, env, signal, resumeStepPath, initialContext, initialHooksCompleted, onCheckpoint, onStepFailure, humanizeReplay = false } = options;

  const hooksCompleted: HookPhase[] = initialHooksCompleted ? [...initialHooksCompleted] : [];

  env.signal = signal;

  const originalTransport = env.transport;
  const batchSize = rule.sendPolicy?.batchSize;
  const buffering = batchSize
    ? new BufferingTransport(originalTransport, {
        batchSize,
        flushInterval: rule.sendPolicy?.flushInterval,
        sendOnFailure: rule.sendPolicy?.sendOnFailure,
      })
    : null;
  if (buffering) env.transport = buffering;

  try {
    // ctx declared below; the legacy tag wrapper reads ctx.tags lazily and
    // adds them to log metadata only. Result business payloads stay unchanged.
    let ctxRef: RuntimeContext | undefined;
    const tagged = new TaggedTransport(env.transport, () => ctxRef?.tags);
    env.transport = tagged;

    const ctx: RuntimeContext = {
    taskId,
    ruleId: rule.id,
    ruleVersion: rule.version,
    workerId,
    variables: { ...rule.variables, ...variables, __selectors: rule.selectors ?? {} },
    extracted: initialContext?.extracted ? { ...initialContext.extracted } : {},
    evaluated: initialContext?.evaluated ? { ...initialContext.evaluated } : {},
    captured: initialContext?.captured ? { ...initialContext.captured } : {},
    page: { url: env.getUrl(), title: env.getTitle() },
    resultsSent: 0,
    logsSent: 0,
    ruleDefaults: (
      humanizeReplay ||
      rule.humanize != null ||
      rule.retry != null ||
      rule.timeout != null ||
      rule.maxRetries != null
    )
      ? {
          humanize: rule.humanize ?? (humanizeReplay ? DEFAULT_REPLAY_HUMANIZE : undefined),
          retry: rule.retry,
          timeout: rule.timeout,
          maxRetries: rule.maxRetries,
        }
      : undefined,
  };
  ctxRef = ctx;

  const snapshots: Snapshot[] = [];
  let status: ExecutionResult['status'] = 'success';
  let message = '';
  let fatalError: ExecutionError | undefined;
  let skipMain = false;

  await env.transport.sendStatus('running', `Started rule ${rule.id}`);

  let screenshotCaptured = false;
  async function markFatalError(err: ExecutionError): Promise<void> {
    fatalError = err;
    status = 'failure';
    message = err.message;
    if (rule.screenshotOnError && !screenshotCaptured) {
      screenshotCaptured = true;
      try {
        const snap = await env.saveSnapshot('error', 'html');
        if (snap) await env.transport.sendSnapshot(snap);
      } catch (e: any) {
        await env.transport.sendLog('warn', 'screenshotOnError failed', { error: String(e?.message ?? e) });
      }
    }
  }

  async function applyExit(err: any): Promise<boolean> {
    if (err instanceof ExitSignal) {
      const newStatus = err.status;
      if (newStatus === 'failure') {
        const fatal = err.message
          ? { type: 'ExitFailure', message: err.message, recoverable: false }
          : fatalError ?? { type: 'ExitFailure', message, recoverable: false };
        await markFatalError(fatal);
      } else if (newStatus === 'cancelled') {
        status = 'cancelled';
        message = err.message;
      } else {
        // success: preserve an existing fatal error; only mark success when nothing failed yet
        if (!fatalError) {
          status = 'success';
          message = err.message;
        }
      }
      return true;
    }
    return false;
  }

  function applyAbort(err: any): boolean {
    if (err?.name === 'AbortError') {
      status = 'cancelled';
      message = err.message || 'cancelled';
      return true;
    }
    return false;
  }

  function checkpoint(
    path: StepPath,
    phase: CheckpointNavigationExpectation['phase'],
    action: Action,
  ): void {
    let expectedUrl = env.getUrl();
    let urlPolicy: CheckpointNavigationExpectation['urlPolicy'] = 'expected-path';
    if (phase === 'before-navigation') {
      if (action.action === 'navigate') {
        try {
          expectedUrl = String(interpolate((action as Action & { url: string }).url, ctx));
        } catch {
          // Preserve the executor's existing error path for an invalid
          // navigation template; the replay guard still constrains recovery
          // to the current origin in the meantime.
          urlPolicy = 'same-origin';
        }
      } else if (action.action !== 'reload') {
        urlPolicy = 'same-origin';
      }
    }
    onCheckpoint?.({
      lastCompletedStepPath: path,
      extracted: ctx.extracted,
      evaluated: ctx.evaluated,
      captured: ctx.captured,
      hooksCompleted: [...hooksCompleted],
      navigation: { phase, expectedUrl, urlPolicy },
    });
  }

  // beforeAll hook
  // When resuming after a page navigation, assume beforeAll already ran.
  // D-2: also skip if beforeAll is in the hooksCompleted set.
  if (rule.hooks?.beforeAll && (!resumeStepPath || resumeStepPath === EMPTY_PATH) && !hooksCompleted.includes('beforeAll')) {
    try {
      ctx.page = { url: env.getUrl(), title: env.getTitle() };
      const beforeResult = await runSteps(rule.hooks.beforeAll, ctx, env, executeAction);
      if (beforeResult.error) return finalize(beforeResult.error, ctx, env, snapshots, buffering);
    } catch (err: any) {
      if (await applyExit(err) || applyAbort(err)) {
        skipMain = true;
      } else {
        return finalize(
          { type: classifyError(err), message: String(err?.message ?? err), recoverable: false },
          ctx,
          env,
          snapshots,
          buffering,
        );
      }
    } finally {
      hooksCompleted.push('beforeAll');
      onCheckpoint?.({
        lastCompletedStepPath: resumeStepPath ?? EMPTY_PATH,
        extracted: ctx.extracted,
        evaluated: ctx.evaluated,
        captured: ctx.captured,
        hooksCompleted: [...hooksCompleted],
      });
    }
  }

  // main steps
  let mainResult: { error?: ExecutionError } = {};
  if (!skipMain) {
    try {
      mainResult = await runSteps(rule.steps, ctx, env, executeAction, {
        resumePath: resumeStepPath,
        onStepStart: (path, action) => checkpoint(path, 'before-navigation', action),
        onStepComplete: (path, _ctx, action) => checkpoint(path, 'after-step', action),
        onStepFailure,
      });
    } catch (err: any) {
      if (!(await applyExit(err)) && !applyAbort(err)) {
        mainResult = { error: { type: classifyError(err), message: String(err?.message ?? err), recoverable: false } };
      }
    }
    if (mainResult.error) {
      await markFatalError(mainResult.error);
    }
  }

  // afterAll hook
  if (rule.hooks?.afterAll && !hooksCompleted.includes('afterAll')) {
    try {
      ctx.page = { url: env.getUrl(), title: env.getTitle() };
      const afterAllResult = await runSteps(rule.hooks.afterAll, ctx, env, executeAction);
      if (afterAllResult.error) {
        await env.transport.sendLog('warn', 'afterAll hook failed', { error: afterAllResult.error.message });
      }
    } catch (err: any) {
      if (await applyExit(err) || applyAbort(err)) {
        // exit handled gracefully; continue with remaining hooks
      } else {
        await env.transport.sendLog('warn', 'afterAll hook failed', { error: String(err?.message ?? err) });
      }
    } finally {
      hooksCompleted.push('afterAll');
      onCheckpoint?.({
        lastCompletedStepPath: resumeStepPath ?? EMPTY_PATH,
        extracted: ctx.extracted,
        evaluated: ctx.evaluated,
        captured: ctx.captured,
        hooksCompleted: [...hooksCompleted],
      });
    }
  }

  // onError hook
  if (fatalError && rule.hooks?.onError && !hooksCompleted.includes('onError')) {
    ctx.lastError = fatalError;
    try {
      ctx.page = { url: env.getUrl(), title: env.getTitle() };
      const onErrorResult = await runSteps(rule.hooks.onError, ctx, env, executeAction);
      if (onErrorResult.error) {
        await env.transport.sendLog('warn', 'onError hook failed', { error: onErrorResult.error.message });
      }
    } catch (err: any) {
      if (await applyExit(err) || applyAbort(err)) {
        // exit handled gracefully; continue with cleanup
      } else {
        await env.transport.sendLog('warn', 'onError hook failed', { error: String(err?.message ?? err) });
      }
    } finally {
      hooksCompleted.push('onError');
      onCheckpoint?.({
        lastCompletedStepPath: resumeStepPath ?? EMPTY_PATH,
        extracted: ctx.extracted,
        evaluated: ctx.evaluated,
        captured: ctx.captured,
        hooksCompleted: [...hooksCompleted],
      });
    }
  }

  // cleanup hook
  if (rule.hooks?.cleanup && !hooksCompleted.includes('cleanup')) {
    try {
      ctx.page = { url: env.getUrl(), title: env.getTitle() };
      const cleanupResult = await runSteps(rule.hooks.cleanup, ctx, env, executeAction);
      if (cleanupResult.error) {
        await env.transport.sendLog('warn', 'cleanup hook failed', { error: cleanupResult.error.message });
      }
    } catch (err: any) {
      if (await applyExit(err) || applyAbort(err)) {
        // exit handled gracefully
      } else {
        await env.transport.sendLog('warn', 'cleanup hook failed', { error: String(err?.message ?? err) });
      }
    } finally {
      hooksCompleted.push('cleanup');
      onCheckpoint?.({
        lastCompletedStepPath: resumeStepPath ?? EMPTY_PATH,
        extracted: ctx.extracted,
        evaluated: ctx.evaluated,
        captured: ctx.captured,
        hooksCompleted: [...hooksCompleted],
      });
    }
  }

  // rule.output JSON Schema validation against ctx.extracted
  if (rule.output) {
    try {
      const result = await validateOutput(
        ctx.extracted,
        rule.output,
        rule.outputOnInvalid ?? 'fail',
        env,
      );
      if (!result.ok) {
        await markFatalError({
          type: 'ValidationError',
          message: `output: ${result.errors}`,
          recoverable: false,
        });
      }
    } catch (e: any) {
      await markFatalError({
        type: 'ConfigError',
        message: `output schema compile failed: ${String(e?.message ?? e)}`,
        recoverable: false,
      });
    }
  }

  if (buffering) {
    try {
      await buffering.drain(status === 'success' ? 'success' : 'failure');
    } catch (e: any) {
      await env.transport.sendLog('error', 'sendPolicy drain failed', { error: String(e?.message ?? e) });
      await markFatalError({
        type: 'TransportError',
        message: `sendPolicy drain failed: ${String(e?.message ?? e)}`,
        recoverable: false,
      });
    }
  }

  // final flush
  await env.transport.sendResult({ __final: true, taskId }, true);

  const result: ExecutionResult = {
    status,
    message,
    partialData: ctx.extracted,
    error: fatalError,
    snapshots,
  };

  await env.transport.sendStatus(status, message);
  return result;
  } finally {
    // dispose() clears the setInterval timer for exit paths that bypass
    // drain() (AbortSignal abort, onStepFailure rollback, or thrown error
    // before drain runs). Idempotent: drain() may have already cleared the
    // timer on the main path. boot() re-entry is not synchronous (guarded
    // by __opencrawlerBootActive in userscript-entry.ts, and checkpoint
    // resume happens via a fresh page load where the old JS realm — and
    // its setInterval — is already gone). Ordering relative to the transport
    // restore is not load-bearing — the tick callback captures `this.inner`
    // at construction, not env.transport — but disposing first keeps the
    // lifecycle easy to reason about.
    buffering?.dispose();
    env.transport = originalTransport;
  }
}

async function finalize(
  error: ExecutionError,
  ctx: RuntimeContext,
  env: Environment,
  snapshots: Snapshot[],
  buffering?: BufferingTransport | null,
): Promise<ExecutionResult> {
  ctx.lastError = error;
  if (buffering) {
    try { await buffering.drain('failure'); }
    catch (e: any) {
      await env.transport.sendLog('error', 'sendPolicy drain failed in finalize', { error: String(e?.message ?? e) });
    }
  }
  await env.transport.sendResult({ __criticalFailure: true, error, partialData: ctx.extracted }, true);
  await env.transport.sendStatus('failed', error.message);
  return { status: 'failure', message: error.message, error, partialData: ctx.extracted, snapshots };
}
