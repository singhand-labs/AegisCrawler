/**
 * Replay-specific rule preflight (§7.2 of docs/replay-fix-plan.md).
 *
 * Runs at both the START_REPLAY entry (extension background) and the replay-runner
 * boot (content script). Rejects rules that the v1 replay checkpoint mechanism
 * cannot handle correctly:
 *
 *   1. Navigation inside containers whose re-entry is non-deterministic across
 *      a page navigation — `loop.whileElementExists` (historical DOM required to
 *      know if the loop should continue), `retry` (attempt-count semantics across
 *      navigation are ambiguous), or any hook (`beforeAll`/`afterAll`/`onError`/
 *      `cleanup` — hooks run without onStepStart/onCheckpoint).
 *      Navigation inside `if`/`loop.fixedCount`/`loop.forEach`/`switch`/`group`
 *      IS supported as of Phase 3 (recursive checkpoints via StepPath).
 *   2. evaluate / waitForFunction actions and jsTruthy conditions — the replay
 *      path runs with the script sandbox disabled (allowEvaluate=false), so these
 *      would fail mid-run rather than at the gate.
 *   3. Static cross-origin navigate targets (literal URL only — dynamic URLs are
 *      caught at runtime in handleNavigate).
 *   4. History navigation (`goBack` / `goForward`) — the destination is not
 *      knowable before dispatch, so replay cannot prove that it stays inside
 *      the approved origin.
 *
 * Domain check uses rule.domain (string or string[]).
 */

import { actionMayNavigate } from './executor-utils';
import type { Rule } from './types';

export type PreflightErrorCode =
  | 'while_element_exists_navigation_unsupported'
  | 'retry_navigation_unsupported'
  | 'hook_navigation'
  | 'evaluate_disabled'
  | 'evaluate_recorded_untrusted'
  | 'history_navigation_unsupported'
  | 'static_cross_origin_navigate';

export interface PreflightError {
  code: PreflightErrorCode;
  message: string;
  /** Step path like "steps[2].then[1]" or "hooks.beforeAll[0]". */
  path: string;
  /** First 200 chars of an evaluate/waitForFunction script (when applicable). */
  scriptPreview?: string;
}

/**
 * Tracks the stack of containers enclosing the current step as a list of
 * "container kind" descriptors. The descriptor for a loop includes its type
 * (`loop.fixedCount` / `loop.forEach` / `loop.whileElementExists`). Hooks are
 * represented by the sentinel `'hook'`.
 */
type ContainerKind = 'top' | 'if' | 'switch' | 'group' | 'retry' | 'hook'
  | 'loop.fixedCount' | 'loop.forEach' | 'loop.whileElementExists';

export function preflightRule(rule: Rule): PreflightError[] {
  const errors: PreflightError[] = [];

  (rule.steps ?? []).forEach((step, i) => {
    walkStep(step, `steps[${i}]`, /*containers*/ ['top'], rule, errors);
  });

  // Hooks never receive onStepStart/onCheckpoint (executor.ts calls runSteps on
  // hooks without options). Any navigation inside any hook is rejected.
  const hookNames = ['beforeAll', 'afterAll', 'onError', 'cleanup'] as const;
  for (const name of hookNames) {
    const hookSteps = rule.hooks?.[name];
    if (!Array.isArray(hookSteps)) continue;
    hookSteps.forEach((step, i) => {
      walkStep(step, `hooks.${name}[${i}]`, /*containers*/ ['hook'], rule, errors);
    });
  }

  return errors;
}

function walkStep(
  step: any,
  path: string,
  containers: ContainerKind[],
  rule: Rule,
  errors: PreflightError[],
): void {
  if (!step || typeof step !== 'object') return;

  // #2: evaluate / waitForFunction actions and jsTruthy conditions
  const action: string = typeof step.action === 'string' ? step.action : '';
  if (action === 'evaluate' || action === 'waitForFunction') {
    if (step.trusted !== true) {
      errors.push({
        code: 'evaluate_recorded_untrusted',
        message:
          `evaluate action from recording is untrusted. ` +
          `To enable replay, set \`trusted: true\` on this step after reviewing the script.`,
        path,
        scriptPreview: typeof step.script === 'string' ? step.script.slice(0, 200) : undefined,
      });
    }
  }
  if (step.condition?.type === 'jsTruthy') {
    errors.push({
      code: 'evaluate_disabled',
      message: "replay v1 disabled the script sandbox; condition 'jsTruthy' is not supported",
      path: `${path}.condition`,
    });
  }

  // #1: navigation gating. Walk up the container stack to find the *innermost
  // navigation-relevant* container: hook > retry > whileElementExists > others.
  // Each container kind has its own rule.
  if (actionMayNavigate(step)) {
    gateNavigation(step, path, containers, rule, errors);
  }

  // Recurse into flow-control / container bodies, pushing the appropriate
  // container kind onto the stack.
  // `if` — then / else
  if (Array.isArray(step.then)) traverse(step.then, `${path}.then`, withContainer(containers, 'if'), rule, errors);
  if (Array.isArray(step.else)) traverse(step.else, `${path}.else`, withContainer(containers, 'if'), rule, errors);

  // `loop` — dispatch by type
  if (action === 'loop') {
    const loopType: string = typeof step.type === 'string' ? step.type : '';
    const loopKind: ContainerKind =
      loopType === 'fixedCount' ? 'loop.fixedCount'
      : loopType === 'forEach' ? 'loop.forEach'
      : loopType === 'whileElementExists' ? 'loop.whileElementExists'
      : 'loop.fixedCount';
    if (Array.isArray(step.steps)) {
      traverse(step.steps, `${path}.steps`, withContainer(containers, loopKind), rule, errors);
    }
  } else if (action === 'switch') {
    // `switch` — cases[] + default
    if (Array.isArray(step.cases)) {
      step.cases.forEach((c: any, ci: number) => {
        if (Array.isArray(c?.steps)) {
          traverse(c.steps, `${path}.cases[${ci}].steps`, withContainer(containers, 'switch'), rule, errors);
        }
      });
    }
    if (Array.isArray(step.default)) {
      traverse(step.default, `${path}.default`, withContainer(containers, 'switch'), rule, errors);
    }
  } else if (action === 'group') {
    if (Array.isArray(step.steps)) {
      traverse(step.steps, `${path}.steps`, withContainer(containers, 'group'), rule, errors);
    }
  } else if (action === 'retry') {
    if (Array.isArray(step.steps)) {
      traverse(step.steps, `${path}.steps`, withContainer(containers, 'retry'), rule, errors);
    }
  } else if (Array.isArray(step.steps)) {
    // Defensive: any other action with a `steps` array (future DSL feature).
    // Treat as an opaque container — navigation policy uses the inherited stack.
    traverse(step.steps, `${path}.steps`, containers, rule, errors);
  }
}

function withContainer(stack: ContainerKind[], kind: ContainerKind): ContainerKind[] {
  return [...stack, kind];
}

function traverse(
  steps: any[],
  basePath: string,
  containers: ContainerKind[],
  rule: Rule,
  errors: PreflightError[],
): void {
  steps.forEach((step, i) => {
    walkStep(step, `${basePath}[${i}]`, containers, rule, errors);
  });
}

/**
 * Apply the navigation policy based on the container stack. Order of checks
 * matters: hooks beat any inner container; `retry` beats inner loops; a
 * `whileElementExists` loop anywhere on the stack is non-resumable.
 */
function gateNavigation(
  step: any,
  path: string,
  containers: ContainerKind[],
  rule: Rule,
  errors: PreflightError[],
): void {
  if (step.action === 'goBack' || step.action === 'goForward') {
    errors.push({
      code: 'history_navigation_unsupported',
      message: `${step.action} is not supported by replay: the history destination cannot be validated before navigation`,
      path,
    });
    return;
  }
  // Hooks are flagged via the `path.startsWith('hooks.')` heuristic AND via the
  // 'hook' container kind (set for hook-entry-level steps). Either way: reject.
  const inHook = containers.includes('hook') || path.startsWith('hooks.');
  if (inHook) {
    errors.push({
      code: 'hook_navigation',
      message: `navigation inside hook is not checkpointed by replay v1; move to top-level or post-hook steps`,
      path,
    });
    return;
  }
  if (containers.includes('retry')) {
    errors.push({
      code: 'retry_navigation_unsupported',
      message: `navigation inside retry is not supported: attempt-count semantics are ambiguous across page navigation`,
      path,
    });
    return;
  }
  // whileElementExists is non-resumable at any nesting depth — historical
  // DOM is needed to decide whether the loop should continue.
  if (containers.includes('loop.whileElementExists')) {
    errors.push({
      code: 'while_element_exists_navigation_unsupported',
      message: `navigation inside loop.whileElementExists is not supported: past iterations cannot be re-entered without replaying historical DOM`,
      path,
    });
    return;
  }
  // if / loop.fixedCount / loop.forEach / switch / group / top: supported.
  // Run the static cross-origin check on literal navigate URLs anywhere.
  checkStaticCrossOrigin(step, path, rule, errors);
}

function checkStaticCrossOrigin(
  step: any,
  path: string,
  rule: Rule,
  errors: PreflightError[],
): void {
  if (step.action !== 'navigate' && step.action !== 'reload') return;
  // handleNavigate reads action.url; reload has no URL.
  const raw: unknown = step.url;
  if (typeof raw !== 'string') return;
  // Skip dynamic / interpolated URLs — runtime assertion in handleNavigate
  // catches those.
  if (raw.includes('{{') || raw.includes('}}')) return;
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    // Relative URLs inherit the current origin; not static cross-origin.
    return;
  }
  const allowedDomains = Array.isArray(rule.domain) ? rule.domain : [rule.domain];
  const host = parsed.hostname;
  const ok = allowedDomains.some(
    (d) => typeof d === 'string' && (host === d || host.endsWith('.' + d)),
  );
  if (!ok) {
    errors.push({
      code: 'static_cross_origin_navigate',
      message: `replay v1 only supports same-origin navigation; literal target '${raw}' is cross-origin to rule.domain [${allowedDomains.join(', ')}]`,
      path,
    });
  }
}
