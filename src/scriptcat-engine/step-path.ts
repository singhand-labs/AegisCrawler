/**
 * Structured step position within a rule's step tree — Phase 3 of
 * docs/replay-fix-plan.md §8 ("递归检查点后续工作").
 *
 * A StepPath encodes the DFS position of a step (usually a leaf) so that
 * navigation inside flow-control blocks (if/loop/switch/group) can be
 * checkpointed and resumed. Phase 1/2 stored a flat `lastCompletedStepIndex`;
 * Phase 3 replaces it with `lastCompletedStepPath: StepPath`.
 *
 * Trust-model re-entry (plan §"Design: Trust-Model Re-entry"): on resume we
 * jump straight to the recorded frame without re-evaluating conditions or
 * re-running prior siblings/iterations. This is safe because all persistent
 * state lives in ctx.extracted/evaluated/captured, which onCheckpoint already
 * persists.
 *
 * Pure data. Bounds-handling (childIdx >= container length, loop iter
 * exhausted) is the runtime's responsibility in runSteps — these utilities
 * only manipulate the encoding.
 */

export type StepPathFrame =
  | { kind: 'top'; childIdx: number }
  | { kind: 'if'; branch: 'then' | 'else'; childIdx: number }
  | { kind: 'loop'; iter: number; childIdx: number }
  | { kind: 'switch'; caseIdx: number; childIdx: number }
  | { kind: 'group'; childIdx: number };

export type StepPath = StepPathFrame[];

/** Sentinel for "no step has completed yet" — initial state of a fresh run. */
export const EMPTY_PATH: StepPath = [];

/**
 * Compute the next pending step path (the step to run next) given the path of
 * the last completed step. Empty path → first top-level step.
 *
 * Purely structural: increments the deepest frame's childIdx by 1. The runtime
 * is responsible for detecting childIdx >= container length and either popping
 * to the parent's next sibling or exhausting the loop's iter range.
 */
export function nextPendingPath(prev: StepPath): StepPath {
  if (prev.length === 0) return [{ kind: 'top', childIdx: 0 }];
  const out = prev.slice();
  const last = out.pop() as StepPathFrame;
  out.push({ ...last, childIdx: last.childIdx + 1 });
  return out;
}

/**
 * Compute the prior pending step path. Used by onStepFailure to roll back so
 * the failed step is retried on resume (Phase 1 §4.1 sequential invariant,
 * generalised to nested paths).
 *
 * Decrements the deepest frame's childIdx by 1. Negative values are permitted
 * and interpreted by the runtime as "container re-entry" (re-run from child 0
 * of the same frame, e.g. retry the first child of an if-branch).
 *
 * Empty path → empty path (nothing to roll back; should not occur because
 * onStepFailure only fires after onStepStart wrote a real path).
 */
export function prevPendingPath(path: StepPath): StepPath {
  if (path.length === 0) return EMPTY_PATH;
  const out = path.slice();
  const last = out.pop() as StepPathFrame;
  out.push({ ...last, childIdx: last.childIdx - 1 });
  return out;
}

/**
 * Lexicographic DFS-order comparison. Returns -1 if a < b (a is earlier in
 * DFS), 0 if equal, +1 if a > b.
 *
 * Drives the monotonicity check in replay-runner.ts: if ctxFromBg.path <
 * sessionStorage.path, the background's context is stale (the REPLAY_CONTEXT
 * message was lost when the SW died) — fail-fast.
 *
 * Frame order: by kind rank (top < if < loop < switch < group), then by
 * numeric fields in declared order. Cross-kind comparisons at the same depth
 * should not arise for the same rule; the rank gives a deterministic total
 * order regardless.
 */
export function comparePath(a: StepPath, b: StepPath): -1 | 0 | 1 {
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) {
    const c = compareFrame(a[i], b[i]);
    if (c !== 0) return c;
  }
  if (a.length < b.length) return -1;
  if (a.length > b.length) return 1;
  return 0;
}

function compareFrame(a: StepPathFrame, b: StepPathFrame): -1 | 0 | 1 {
  const ta = frameTuple(a);
  const tb = frameTuple(b);
  const n = Math.min(ta.length, tb.length);
  for (let i = 0; i < n; i++) {
    if (ta[i] < tb[i]) return -1;
    if (ta[i] > tb[i]) return 1;
  }
  if (ta.length < tb.length) return -1;
  if (ta.length > tb.length) return 1;
  return 0;
}

function frameTuple(f: StepPathFrame): number[] {
  switch (f.kind) {
    case 'top':
      return [0, f.childIdx];
    case 'if':
      return [1, f.branch === 'then' ? 0 : 1, f.childIdx];
    case 'loop':
      return [2, f.iter, f.childIdx];
    case 'switch':
      return [3, f.caseIdx, f.childIdx];
    case 'group':
      return [4, f.childIdx];
  }
}

/**
 * Human-readable representation for logs / debug. Not part of the round-trip
 * contract — paths are JSON-serialised as fields of their parent object
 * (ReplayState / ReplayContext / CheckpointState).
 */
export function formatPath(path: StepPath): string {
  if (path.length === 0) return '<empty>';
  return path.map(formatFrame).join('/');
}

function formatFrame(f: StepPathFrame): string {
  switch (f.kind) {
    case 'top':
      return `top[${f.childIdx}]`;
    case 'if':
      return `if.${f.branch}[${f.childIdx}]`;
    case 'loop':
      return `loop{iter=${f.iter}}[${f.childIdx}]`;
    case 'switch':
      return `switch{case=${f.caseIdx}}[${f.childIdx}]`;
    case 'group':
      return `group[${f.childIdx}]`;
  }
}
