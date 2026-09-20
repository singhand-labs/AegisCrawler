/**
 * Resume validation for the worker page runtime — the pure, DOM-free core of
 * the resume guard. Ported from extension/src/intent/replay-runner.ts
 * (__ocReplayAutoResume checks) so the worker host and unit tests can share
 * exactly one validation implementation.
 *
 * Validation layers on resume:
 *   1. sessionStorage light state exists and is still 'running' for this task;
 *   2. expectedUrl is non-empty (first-step pre-checkpoint anomaly → fail fast);
 *   3. the actual page URL matches the checkpoint expectation: same host and
 *      protocol always; the pathname must match for 'expected-path' policy
 *      (known navigate/reload targets) and may differ only within the same
 *      origin for 'same-origin' policy (event-driven navigation);
 *   4. host-held context monotonicity: a context older than the session's
 *      lastCompletedStepPath means checkpoint data was lost — fail fast rather
 *      than resume with stale extraction state.
 */

import { comparePath, formatPath, type StepPath } from '../scriptcat-engine/step-path';
import { pathnameMatches } from './url-policy';

export interface WorkerRunState {
  taskId: string;
  ruleId: string;
  lastCompletedStepPath: StepPath;
  expectedUrl: string;
  expectedUrlPolicy: 'expected-path' | 'same-origin';
  status: 'running' | 'done' | 'failed';
}

export interface WorkerResumeContext {
  lastCompletedStepPath: StepPath;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
}

export type ResumeVerdict = { ok: true } | { ok: false; reason: string };

export function validateResume(
  prior: WorkerRunState | null,
  taskId: string,
  currentUrl: string,
  ctxFromHost: WorkerResumeContext | undefined,
): ResumeVerdict {
  if (!prior || prior.status !== 'running') {
    return { ok: false, reason: 'no running checkpoint state for this task' };
  }
  if (prior.taskId !== taskId) {
    return { ok: false, reason: `checkpoint task ${prior.taskId} does not match resume task ${taskId}` };
  }
  if (!prior.expectedUrl) {
    return { ok: false, reason: 'resume refused: expectedUrl empty (first-step pre-checkpoint anomaly)' };
  }
  let expected: URL;
  let actual: URL;
  try {
    expected = new URL(prior.expectedUrl);
    actual = new URL(currentUrl);
  } catch {
    return { ok: false, reason: `resume URL parse failed: ${currentUrl}` };
  }
  const pathOk = prior.expectedUrlPolicy === 'same-origin'
    || pathnameMatches(actual.pathname, expected.pathname);
  const hostOk = actual.host === expected.host;
  const protoOk = actual.protocol === expected.protocol;
  if (!hostOk || !protoOk || !pathOk) {
    return { ok: false, reason: `resume URL mismatch: ${currentUrl} vs expected ${prior.expectedUrl}` };
  }
  if (ctxFromHost && comparePath(ctxFromHost.lastCompletedStepPath, prior.lastCompletedStepPath) < 0) {
    return {
      ok: false,
      reason: `resume refused: host context stale (ctx=${formatPath(ctxFromHost.lastCompletedStepPath)} < session=${formatPath(prior.lastCompletedStepPath)})`,
    };
  }
  return { ok: true };
}
