import type { Action, RuntimeContext, Environment, ExecutionError, ActionHandler } from './types';
import { FlowControlSignal, ExitSignal } from './signals';
import {
  interpolate,
  parseRetry,
  randomBetween,
  resolveExpression,
  resolveSelectorAlias,
} from './utils';
import { EMPTY_PATH, type StepPath, type StepPathFrame } from './step-path';

const DEFAULT_TIMEOUT = 30000;
const DEFAULT_RETRY = 0;

interface StepResult {
  error?: ExecutionError;
}

function checkAborted(env: Environment) {
  if (env.signal?.aborted) {
    const err = new Error('Task was cancelled');
    err.name = 'AbortError';
    throw err;
  }
}

export interface RunStepsOptions {
  /**
   * Path of the leaf step to resume from (Phase 3). When set, runSteps skips
   * prior siblings at this level (trust model — their side effects are
   * already in ctx) and dispatches the recorded child with the deeper resume
   * path. Empty/undefined → start from child 0.
   */
  resumePath?: StepPath;
  /**
   * Chain of ancestor-container frames above THIS runSteps call (Phase 3).
   * Defaults to EMPTY_PATH. The top-level call from runRule passes []; flow-
   * control handlers pass the path of their container step so that children
   * get nested paths.
   */
  pathPrefix?: StepPath;
  /**
   * Factory for the per-child frame at THIS runSteps level. Defaults to a
   * `top` frame. Flow-control handlers override to push `if`/`loop`/`switch`/
   * `group` frames so each child's path uniquely identifies its position.
   */
  frameFor?: (childIdx: number) => StepPathFrame;
  onStepStart?: (path: StepPath, step: Action, ctx: RuntimeContext) => void;
  onStepComplete?: (path: StepPath, ctx: RuntimeContext, step: Action) => void;
  // See §4.1 of docs/replay-fix-plan.md. Fires inside the runSteps loop when a
  // step that may navigate fails, BEFORE the loop returns. The callback receives
  // the failed step path. Correctness depends on runSteps being strictly
  // sequential (no concurrency / reentrancy) so that the prior path equals the
  // last onStepComplete-written path. Gated on actionMayNavigate so non-
  // navigating failures do not trigger rollback (the pre-start checkpoint for
  // those would never have been written either).
  onStepFailure?: (path: StepPath) => void;
}

export function actionMayNavigate(action: Action): boolean {
  const type = action.action as string;
  if (type === 'navigate' || type === 'reload' || type === 'goBack' || type === 'goForward') return true;
  if (type === 'click' || type === 'doubleClick' || type === 'hoverClick') return true;
  if (type === 'pressKey' || type === 'keyCombination') return true;
  if (type === 'type' && (action as unknown as Record<string, unknown>).submit === true) return true;
  if (type === 'submit' || type === 'requestHuman') return true;
  return false;
}

export async function runSteps(
  steps: Action[],
  ctx: RuntimeContext,
  env: Environment,
  executeAction: ActionHandler,
  options?: RunStepsOptions,
): Promise<StepResult> {
  const prefix = options?.pathPrefix ?? EMPTY_PATH;
  const frameFor = options?.frameFor ?? ((childIdx: number): StepPathFrame => ({ kind: 'top', childIdx }));
  // Resume cursor (Phase 3): if resumePath is set, its first frame is the
  // position at THIS level to resume from. Skip prior siblings (trust model —
  // their side effects are in ctx) and start iteration at the recorded
  // childIdx. If resumePath has deeper frames, the recorded child is a
  // container and dispatchFlowControl receives resumePath.slice(1) so the
  // handler enters the right nested position.
  let pendingResume: StepPath | undefined = options?.resumePath;
  let i = 0;
  if (pendingResume && pendingResume.length > 0) {
    const target = pendingResume[0].childIdx;
    if (target > 0) i = target;
  }
  for (; i < steps.length; i++) {
    checkAborted(env);
    ctx.lastStepId = steps[i].id;
    const path: StepPath = [...prefix, frameFor(i)];
    // Compute once: shared between the pre-start checkpoint gate and the
    // onStepFailure rollback trigger so the two stay in sync (ADV-008).
    const mayNavigate = actionMayNavigate(steps[i]);
    // For actions that are likely to trigger a full-page navigation, record the
    // step as completed before it runs. If the page unloads and the userscript
    // restarts, it will resume from the next step instead of repeating the
    // navigation action.
    if (options?.onStepStart && mayNavigate) {
      options.onStepStart(path, steps[i], ctx);
    }
    // Compute deeper-resume payload: if this iteration is the recorded resume
    // target AND has nested frames, the dispatched container receives the
    // slice. Once consumed, pendingResume is cleared so subsequent siblings
    // run normally.
    const isResumeTarget =
      pendingResume && pendingResume.length > 0 && pendingResume[0].childIdx === i;
    const deeperResume: StepPath | undefined =
      isResumeTarget && pendingResume!.length > 1 ? pendingResume!.slice(1) : undefined;
    const dispatchOptions: RunStepsOptions | undefined =
      // H-2: after the resume target is consumed (pendingResume cleared at
      // line ~138), subsequent siblings must NOT inherit the stale resumePath.
      // Without this guard, flow-control handlers spread ...options (including
      // the old resumePath) into child runSteps, causing children to be
      // silently skipped.
      pendingResume
        ? (deeperResume && options ? { ...options, resumePath: deeperResume } : options)
        : (options ? { ...options, resumePath: undefined } : options);
    // Phase 3: short-circuit flow-control inline so options+path propagate to
    // the recursive runSteps calls inside flow-control handlers. Leaf actions
    // still go through runStep. Flow-control steps do not go through
    // executeWithTimeout (the 30s step-level wrapper), so a 100-iteration loop
    // does not fail merely because its total runtime exceeds 30s. Their
    // warn-only softTimeout is scheduled separately below.
    let result: StepResult;
    if (isFlowControlStep(steps[i])) {
      const softTimer = scheduleSoftTimeoutWarning(steps[i], env);
      try {
        result = await dispatchFlowControl(steps[i], ctx, env, executeAction, dispatchOptions, path);
      } finally {
        if (softTimer !== null) clearTimeout(softTimer);
      }
    } else {
      result = await runStep(steps[i], ctx, env, executeAction);
    }
    if (isResumeTarget) pendingResume = undefined;
    if (result.error) {
      // §4.1: onStepFailure fires inside runSteps (not runStep, whose signature
      // has no index/options) between runStep's return and the loop's return.
      // Strictly sequential invariant: prior path == last onStepComplete path.
      if (options?.onStepFailure && mayNavigate) {
        options.onStepFailure(path);
      }
      return result;
    }
    options?.onStepComplete?.(path, ctx, steps[i]);
  }
  return {};
}

async function runStep(step: Action, ctx: RuntimeContext, env: Environment, executeAction: ActionHandler): Promise<StepResult> {
  // condition check
  if (step.condition) {
    const conditionMet = await evaluateCondition(step.condition, ctx, env);
    if (!conditionMet) return {};
  }

  const ruleTimeout = ctx.ruleDefaults?.timeout;
  const timeout = step.hardTimeout ?? (
    step.action === 'requestHuman'
      ? (step.timeout ?? 120000) + 5000
      : step.timeout ?? ruleTimeout ?? DEFAULT_TIMEOUT
  );
  const retry = parseRetry(
    step.retry ?? ctx.ruleDefaults?.retry,
    DEFAULT_RETRY,
    ctx.ruleDefaults?.maxRetries,
  );
  const softTimeout = step.softTimeout;
  let lastError: any;

  for (let attempt = 0; attempt <= retry.maxAttempts; attempt++) {
    try {
      await executeWithTimeout(step, ctx, env, timeout, executeAction, softTimeout);
      return {};
    } catch (err: any) {
      if (err instanceof FlowControlSignal || err instanceof ExitSignal) throw err;
      lastError = err;
      const errorType = classifyError(err);
      const recoverable = isRecoverable(errorType);

      if (!recoverable || attempt === retry.maxAttempts) break;

      const delay = calculateDelay(retry, attempt);
      await env.transport.sendLog('warn', `Retrying step ${step.id ?? step.action}`, { attempt, errorType, delay });
      await env.sleep(delay);
    }
  }

  const error: ExecutionError = {
    type: classifyError(lastError),
    message: lastError?.message ?? String(lastError),
    stepId: step.id,
    retryCount: retry.maxAttempts,
    recoverable: false,
  };

  ctx.lastError = error;

  await env.transport.sendLog('error', `Step failed: ${step.id ?? step.action}`, {
    error: error.message,
    type: error.type,
  });

  if (step.critical) {
    await env.transport.sendResult({ __criticalFailure: true, error }, true);
  }

  const onError = step.onError ?? 'stop';
  if (onError === 'continue' || onError === 'skip') return {};
  if (onError === 'requestHuman') {
    await requestHumanIntervention({
      type: 'generic',
      prompt: `Step ${step.id ?? step.action} requires approved manual recovery before continuing`,
      timeoutMs: step.timeout ?? 120000,
      stepId: step.id ?? step.action,
    }, ctx, env);
    return {};
  }
  if (onError !== 'stop') {
    throw new Error(`ConfigError: unsupported onError strategy: ${String(onError)}`);
  }

  return { error };
}

export async function requestHumanIntervention(
  request: {
    type: 'captcha' | '2fa' | 'confirmation' | 'generic';
    prompt: string;
    timeoutMs: number;
    stepId: string;
  },
  ctx: RuntimeContext,
  env: Environment,
): Promise<void> {
  if (!env.transport.requestHuman) {
    throw new Error('HumanInterventionUnavailable: transport does not support resumable operator decisions');
  }
  const decision = await env.transport.requestHuman({
    type: request.type,
    prompt: request.prompt,
    timeoutMs: request.timeoutMs,
    checkpoint: {
      stepId: request.stepId,
      url: env.getUrl(),
    },
  });
  if (decision.status !== 'approved') {
    throw new Error(`HumanInterventionInvalidStatus: ${decision.status}`);
  }
  await env.transport.sendLog('info', 'human intervention approved', {
    interventionId: decision.id,
    checkpointId: decision.checkpointId,
    stepId: request.stepId,
  });
  ctx.lastError = undefined;
}

async function executeWithTimeout(
  step: Action,
  ctx: RuntimeContext,
  env: Environment,
  timeout: number,
  executeAction: ActionHandler,
  softTimeout?: number,
): Promise<any> {
  return new Promise((resolve, reject) => {
    // Add a small buffer so that internal polling timeouts (e.g. findElement)
    // can complete and throw their specific error (ElementNotFound) before
    // the generic step-level TimeoutError wins the race.
    const softTimer = scheduleSoftTimeoutWarning(step, env, timeout, softTimeout);

    const timer = setTimeout(() => {
      if (softTimer !== null) clearTimeout(softTimer);
      reject(new Error('TimeoutError'));
    }, timeout + 100);

    executeSingleAction(step, ctx, env, executeAction).then(
      (result) => {
        clearTimeout(timer);
        if (softTimer !== null) clearTimeout(softTimer);
        resolve(result);
      },
      (err) => {
        clearTimeout(timer);
        if (softTimer !== null) clearTimeout(softTimer);
        reject(err);
      },
    );
  });
}

function scheduleSoftTimeoutWarning(
  step: Action,
  env: Environment,
  hardTimeout?: number,
  softTimeout = step.softTimeout,
): ReturnType<typeof setTimeout> | null {
  if (softTimeout == null || (hardTimeout != null && softTimeout >= hardTimeout)) {
    return null;
  }
  return setTimeout(() => {
    const details: Record<string, unknown> = {
      stepId: step.id,
      action: step.action,
      softTimeout,
    };
    if (hardTimeout != null) details.hardTimeout = hardTimeout;
    env.transport.sendLog(
      'warn',
      `Soft timeout exceeded for step ${step.id ?? step.action}`,
      details,
    ).catch(() => { /* transport errors must not leak into step */ });
  }, softTimeout);
}

async function executeSingleAction(step: Action, ctx: RuntimeContext, env: Environment, executeAction: ActionHandler): Promise<any> {
  // checkpoint before
  if (step.checkpoint) {
    ctx.checkpoint = {
      stepId: step.id ?? 'unknown',
      url: env.getUrl(),
      variables: { ...ctx.variables },
      extracted: { ...ctx.extracted },
      timestamp: Date.now(),
    };
  }

  const result = await executeAction(step, ctx, env);
  return result;
}

async function evaluateCondition(condition: any, ctx: RuntimeContext, env: Environment): Promise<boolean> {
  const conditionTarget = () => condition.target
    ? interpolate(resolveSelectorAlias(condition.target, ctx.variables.__selectors), ctx)
    : undefined;
  switch (condition.type) {
    case 'elementExists': {
      const target = conditionTarget();
      return !!(target && (await env.findElement(target, condition.timeout ?? 3000)));
    }
    case 'elementNotExists': {
      const target = conditionTarget();
      return !(target && (await env.findElement(target, condition.timeout ?? 3000)));
    }
    case 'textContains': {
      const target = conditionTarget();
      if (!target) return false;
      const el = await env.findElement(target, condition.timeout ?? 3000);
      return el?.textContent?.includes(interpolate(condition.text, ctx)) ?? false;
    }
    case 'urlContains':
      return env.getUrl().includes(interpolate(condition.pattern, ctx));
    case 'urlMatches':
      return new RegExp(interpolate(condition.pattern, ctx)).test(env.getUrl());
    case 'jsTruthy':
      try {
        const scriptCtx = { ...ctx.variables, ...ctx.extracted, ...ctx.evaluated };
        const result = await env.evaluate(`return ${condition.script ?? 'false'}`, scriptCtx, []);
        return !!result;
      } catch {
        return false;
      }
    case 'elementVisible': {
      const target = conditionTarget();
      return !!(target && (await env.findElement(
        { ...target, visible: true } as any,
        condition.timeout ?? 3000,
      )));
    }
    case 'elementHidden': {
      const target = conditionTarget();
      return !(target && (await env.findElement(
        { ...target, visible: true } as any,
        condition.timeout ?? 3000,
      )));
    }
    case 'textEquals': {
      const target = conditionTarget();
      if (!target) return false;
      const el = await env.findElement(target, condition.timeout ?? 3000);
      return String(el?.textContent ?? '').trim() === String(interpolate(condition.text, ctx) ?? '').trim();
    }
    case 'textMatches': {
      // Per Round 6 spec: textMatches intentionally does NOT trim el.textContent
      // (asymmetry vs textEquals). Rule authors anchor patterns with `\s*` if needed.
      const target = conditionTarget();
      if (!target) return false;
      const el = await env.findElement(target, condition.timeout ?? 3000);
      const pattern = interpolate(condition.pattern, ctx);
      try {
        return new RegExp(pattern as string).test(el?.textContent ?? '');
      } catch (e: any) {
        throw new Error(`ConditionError: invalid textMatches pattern: ${String(e?.message ?? e)}`);
      }
    }
    case 'valueEquals': {
      const target = conditionTarget();
      if (!target) return false;
      const el = await env.findElement(target, condition.timeout ?? 3000);
      return (el as any)?.value === String(interpolate(condition.value, ctx));
    }
    case 'networkIdle': {
      if (!env.waitForNetworkIdle) {
        throw new Error('ConditionError: networkIdle requires env.waitForNetworkIdle');
      }
      try {
        await env.waitForNetworkIdle(condition.idleTime ?? 500, condition.timeout ?? 3000);
        return true;
      } catch (e: any) {
        const msg = String(e?.message ?? e);
        if (msg.startsWith('TimeoutError')) return false;
        throw new Error(`ConditionError: networkIdle failed: ${msg}`);
      }
    }
    default:
      throw new Error(`ConditionError: unsupported condition type: ${condition?.type}`);
  }
}

export async function runIfAction(
  action: any,
  ctx: RuntimeContext,
  env: Environment,
  executeAction: ActionHandler,
  options?: RunStepsOptions,
  containerPath?: StepPath,
): Promise<StepResult> {
  // Trust model: if resuming, take the recorded branch directly without
  // re-evaluating the condition. Post-navigation DOM may differ from the
  // original run, so re-eval could pick the wrong branch.
  const resumeFrame = options?.resumePath?.[0];
  let branch: 'then' | 'else';
  let children: any[];
  if (resumeFrame?.kind === 'if') {
    branch = resumeFrame.branch;
    children = branch === 'then' ? action.then : (action.else ?? []);
  } else {
    const met = await evaluateCondition(action.condition, ctx, env);
    branch = met ? 'then' : 'else';
    children = met ? action.then : (action.else ?? []);
  }
  // Thread path so navigation inside the chosen branch checkpoints with a
  // `{kind:'if',branch,childIdx}` frame. The resumePath is passed through
  // unchanged — inner runSteps consumes resumePath[0] (matches frameFor
  // output) and slices deeper frames into dispatched children.
  const childOptions = options && containerPath
    ? {
        ...options,
        pathPrefix: containerPath,
        frameFor: (childIdx: number): StepPathFrame => ({ kind: 'if', branch, childIdx }),
      }
    : undefined;
  const result = await runSteps(children, ctx, env, executeAction, childOptions);
  if (result.error) {
    throw new Error(result.error.message);
  }
  return {};
}

export async function runLoopAction(
  action: any,
  ctx: RuntimeContext,
  env: Environment,
  executeAction: ActionHandler,
  options?: RunStepsOptions,
  containerPath?: StepPath,
): Promise<StepResult> {
  const type = action.type;
  const maxIterations = action.maxIterations ?? 100;

  // Resume target iteration (if any). The resume frame for this loop has
  // {kind:'loop',iter:K,childIdx:M}. We skip iterations 0..K-1 (trust model —
  // their extracted data is in ctx) and re-enter iter K with the resume path
  // so its body picks up at child M.
  const resumeFrame = options?.resumePath?.[0];
  const resumeIter = resumeFrame?.kind === 'loop' ? resumeFrame.iter : -1;

  // Per-iteration childOptions: each iteration's children get a fresh
  // {kind:'loop',iter,childIdx} frame so paths distinguish iterations. Only
  // the resume-target iter inherits options.resumePath; subsequent iters run
  // normally.
  const buildChildOptions = (iter: number): RunStepsOptions | undefined => {
    if (!options || !containerPath) return undefined;
    const base: RunStepsOptions = {
      ...options,
      pathPrefix: containerPath,
      frameFor: (childIdx: number): StepPathFrame => ({ kind: 'loop', iter, childIdx }),
      resumePath: iter === resumeIter ? options.resumePath : undefined,
    };
    return base;
  };

  if (type === 'fixedCount') {
    const count = Number(interpolate(action.count, ctx)) || 0;
    for (let i = 0; i < count && i < maxIterations; i++) {
      checkAborted(env);
      ctx.loopIndex = i;
      // Trust model: iterations before the resume target are already done.
      if (i < resumeIter) continue;
      try {
        const result = await runSteps(action.steps, ctx, env, executeAction, buildChildOptions(i));
        if (result.error) throw new Error(result.error.message);
      } catch (err: any) {
        if (err instanceof FlowControlSignal) {
          if (err.signal === 'break') break;
          if (err.signal === 'continue') continue;
        }
        throw err;
      }
    }
  } else if (type === 'whileElementExists') {
    // Note: navigation inside whileElementExists is rejected at preflight, so
    // resumeIter is always -1 here in practice. The loop below behaves
    // exactly as before in that case.
    for (let i = 0; i < maxIterations; i++) {
      checkAborted(env);
      const el = action.target ? await env.findElement(interpolate(action.target, ctx), 2000) : null;
      if (!el) break;
      ctx.loopIndex = i;
      if (i < resumeIter) continue;
      try {
        const result = await runSteps(action.steps, ctx, env, executeAction, buildChildOptions(i));
        if (result.error) throw new Error(result.error.message);
      } catch (err: any) {
        if (err instanceof FlowControlSignal) {
          if (err.signal === 'break') break;
          if (err.signal === 'continue') continue;
        }
        throw err;
      }
    }
  } else if (type === 'whileElementNotExists') {
    // Inverse of whileElementExists: loop body runs while target is absent.
    // Exits as soon as the element appears (or maxIterations is reached).
    if (!action.target) throw new Error('ConfigError: whileElementNotExists requires a target');
    for (let i = 0; i < maxIterations; i++) {
      checkAborted(env);
      const el = await env.findElement(interpolate(action.target, ctx), 2000);
      if (el) break;
      ctx.loopIndex = i;
      if (i < resumeIter) continue;
      try {
        const result = await runSteps(action.steps, ctx, env, executeAction, buildChildOptions(i));
        if (result.error) throw new Error(result.error.message);
      } catch (err: any) {
        if (err instanceof FlowControlSignal) {
          if (err.signal === 'break') break;
          if (err.signal === 'continue') continue;
        }
        throw err;
      }
    }
  } else if (type === 'whileCondition') {
    // Generic condition loop: body runs while condition evaluates truthy.
    // Exits when condition becomes false (or maxIterations is reached).
    if (!action.condition) throw new Error('ConfigError: whileCondition requires a condition');
    for (let i = 0; i < maxIterations; i++) {
      checkAborted(env);
      const met = await evaluateCondition(action.condition, ctx, env);
      if (!met) break;
      ctx.loopIndex = i;
      if (i < resumeIter) continue;
      try {
        const result = await runSteps(action.steps, ctx, env, executeAction, buildChildOptions(i));
        if (result.error) throw new Error(result.error.message);
      } catch (err: any) {
        if (err instanceof FlowControlSignal) {
          if (err.signal === 'break') break;
          if (err.signal === 'continue') continue;
        }
        throw err;
      }
    }
  } else if (type === 'forEach') {
    // Full-value templates preserve arrays; direct expression paths are the
    // legacy form and remain supported as a fallback.
    const interpolatedItems = interpolate(action.items, ctx);
    const items = Array.isArray(interpolatedItems)
      ? interpolatedItems
      : typeof action.items === 'string'
        ? resolveExpression(action.items, ctx)
        : interpolatedItems;
    if (Array.isArray(items)) {
      for (let i = 0; i < items.length && i < maxIterations; i++) {
        checkAborted(env);
        ctx.loopIndex = i;
        ctx.loopItem = items[i];
        if (i < resumeIter) continue;
        try {
          const result = await runSteps(action.steps, ctx, env, executeAction, buildChildOptions(i));
          if (result.error) throw new Error(result.error.message);
        } catch (err: any) {
          if (err instanceof FlowControlSignal) {
            if (err.signal === 'break') break;
            if (err.signal === 'continue') continue;
          }
          throw err;
        }
      }
    }
  }

  return {};
}

/**
 * Phase 3: handleSwitch moved here from actions/index.ts so the inline
 * flow-control dispatcher in runSteps can propagate options+containerPath.
 * Encodes the chosen case index so resume takes the same case directly (trust
 * model — no expression re-evaluation). caseIdx -1 represents the default
 * branch (no case matched).
 */
async function handleSwitch(
  action: any,
  ctx: RuntimeContext,
  env: Environment,
  executeAction: ActionHandler,
  options?: RunStepsOptions,
  containerPath?: StepPath,
): Promise<void> {
  if (!Array.isArray(action.cases)) {
    throw new Error(`ScriptError: switch requires a cases array`);
  }
  // Trust model: if resuming, take the recorded caseIdx directly without
  // re-evaluating the expression. caseIdx -1 represents the default branch.
  const resumeFrame = options?.resumePath?.[0];
  let caseIdx: number;
  let steps: any[];
  if (resumeFrame?.kind === 'switch') {
    caseIdx = resumeFrame.caseIdx;
    if (caseIdx >= 0 && caseIdx < action.cases.length) {
      steps = action.cases[caseIdx].steps ?? [];
    } else {
      steps = action.default ?? [];
    }
  } else {
    let value: any = interpolate(action.expression, ctx);
    if (
      value === action.expression &&
      !action.expression.includes('{{') &&
      !action.expression.includes('}}') &&
      looksLikeExpression(action.expression)
    ) {
      value = resolveExpression(action.expression, ctx);
    }
    value = String(value ?? '');
    const matchedCaseIdx = action.cases.findIndex((c: any) => String(interpolate(c.value, ctx)) === value);
    caseIdx = matchedCaseIdx >= 0 ? matchedCaseIdx : -1;
    steps = matchedCaseIdx >= 0 ? (action.cases[matchedCaseIdx].steps ?? []) : (action.default ?? []);
  }
  const childOptions = options && containerPath
    ? {
        ...options,
        pathPrefix: containerPath,
        frameFor: (childIdx: number): StepPathFrame => ({ kind: 'switch', caseIdx, childIdx }),
      }
    : undefined;
  const result = await runSteps(steps, ctx, env, executeAction, childOptions);
  if (result.error) {
    throw new Error(result.error.message);
  }
}

async function handleGroup(
  action: any,
  ctx: RuntimeContext,
  env: Environment,
  executeAction: ActionHandler,
  options?: RunStepsOptions,
  containerPath?: StepPath,
): Promise<void> {
  const childOptions = options && containerPath
    ? {
        ...options,
        pathPrefix: containerPath,
        frameFor: (childIdx: number): StepPathFrame => ({ kind: 'group', childIdx }),
      }
    : undefined;
  const result = await runSteps(action.steps, ctx, env, executeAction, childOptions);
  if (result.error) {
    throw new Error(result.error.message);
  }
}

function looksLikeExpression(expr: string): boolean {
  const roots = ['variables', 'extracted', 'evaluated', 'captured', 'loopItem', 'loopIndex', 'page'];
  return roots.some((r) => expr === r || expr.startsWith(`${r}.`));
}

/**
 * Returns true for flow-control actions that runSteps short-circuits past the
 * leaf dispatcher (Phase 3). `retry` is intentionally excluded — its inner
 * runSteps calls do not propagate options (plan §"Out of Scope").
 */
export function isFlowControlStep(step: Action): boolean {
  const t = step.action as string;
  return t === 'if' || t === 'loop' || t === 'switch' || t === 'group';
}

async function dispatchFlowControl(
  step: Action,
  ctx: RuntimeContext,
  env: Environment,
  executeAction: ActionHandler,
  options: RunStepsOptions | undefined,
  containerPath: StepPath,
): Promise<StepResult> {
  switch (step.action) {
    case 'if':
      return runIfAction(step, ctx, env, executeAction, options, containerPath);
    case 'loop':
      return runLoopAction(step, ctx, env, executeAction, options, containerPath);
    case 'switch':
      try {
        await handleSwitch(step, ctx, env, executeAction, options, containerPath);
        return {};
      } catch (err: any) {
        if (err instanceof FlowControlSignal || err instanceof ExitSignal) throw err;
        return { error: { type: classifyError(err), message: String(err?.message ?? err), recoverable: false } };
      }
    case 'group':
      try {
        await handleGroup(step, ctx, env, executeAction, options, containerPath);
        return {};
      } catch (err: any) {
        if (err instanceof FlowControlSignal || err instanceof ExitSignal) throw err;
        return { error: { type: classifyError(err), message: String(err?.message ?? err), recoverable: false } };
      }
    default:
      return { error: { type: 'ScriptError', message: `Unreachable flow-control action: ${step.action}`, recoverable: false } };
  }
}

export function classifyError(err: any): string {
  const msg = String(err?.message ?? err);
  if (msg.includes('ScriptError')) return 'ScriptError';
  if (msg.startsWith('ElementNotFound')) return 'ElementNotFound';
  if (msg.startsWith('ElementDisabled')) return 'ElementDisabled';
  if (msg.startsWith('ElementVerificationFailed')) return 'ElementVerificationFailed';
  if (msg.startsWith('ConditionError')) return 'ConditionError';
  if (msg.startsWith('ValidationError')) return 'ValidationError';
  if (msg.includes('ConfigError')) return 'ConfigError';
  if (msg.startsWith('TimeoutError')) return 'TimeoutError';
  if (msg.includes('Navigation')) return 'NavigationError';
  if (msg.includes('Network')) return 'NetworkError';
  if (msg.includes('Authentication') || msg.includes('login')) return 'AuthenticationError';
  if (msg.includes('Captcha')) return 'CaptchaError';
  if (msg.includes('Rate')) return 'RateLimited';
  if (msg.includes('Blocked')) return 'Blocked';
  if (msg.includes('Session')) return 'SessionExpired';
  if (msg.startsWith('QuotaExceeded')) return 'QuotaExceeded';
  if (msg.startsWith('HumanTimeout')) return 'HumanTimeout';
  if (msg.includes('UnsupportedInEnvironment')) return 'UnsupportedInEnvironment';
  if (msg.includes('Aborted')) return 'UnknownError';
  return 'UnknownError';
}

export function isRecoverable(errorType: string): boolean {
  return ['ElementNotFound', 'TimeoutError', 'NetworkError', 'RateLimited', 'Blocked', 'SessionExpired', 'HumanTimeout'].includes(errorType);
}

export function calculateDelay(retry: any, attempt: number): number {
  const base = retry.delay ?? 1000;
  const delay = Array.isArray(base) ? randomBetween(base as [number, number]) : base;
  switch (retry.backoff) {
    case 'linear':
      return delay * (attempt + 1);
    case 'exponential':
      return delay * Math.pow(2, attempt);
    default:
      return delay;
  }
}
