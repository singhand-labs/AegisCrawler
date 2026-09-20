/**
 * Worker page runtime — executes an approved immutable DSL rule inside a real
 * Chromium page under the worker host's supervision.
 *
 * Design (mirrors extension/src/intent/replay-runner.ts):
 *   - Three globals (__ocWorkerBoot / __ocWorkerResume / __ocWorkerAbort)
 *     invoked by the host via page.evaluate after bundle injection.
 *   - The host exposes exactly one async function, window.__ocWorkerBridge,
 *     which is the page's only channel to the server (BridgeTransport). No
 *     credentials exist in this realm: WORKER_API_KEY never leaves the host
 *     process.
 *   - Cross-navigation survival: lightweight control fields in sessionStorage
 *     (WorkerRunState), full extracted/evaluated/captured context held by the
 *     host (checkpointContext bridge calls), strict resume validation
 *     (resume-guard.ts), and host-side navigation policy (page-driver.ts).
 *   - Status authority stays with the host: this runtime reports an execution
 *     outcome through the 'complete' bridge event; the host's Worker
 *     terminalizes the task.
 */

import { runRule } from '../scriptcat-engine/executor';
import { preflightRule } from '../scriptcat-engine/rule-preflight';
import {
  EMPTY_PATH,
  nextPendingPath,
  prevPendingPath,
  type StepPath,
} from '../scriptcat-engine/step-path';
import { BrowserEnvironment } from '../worker/BrowserEnvironment';
import type { Rule } from '../scriptcat-engine/types';
import { BridgeTransport } from './bridge-transport';
import { validateResume, type WorkerResumeContext, type WorkerRunState } from './resume-guard';
import { hostMatchesDomain, normalizeRunUrl, ruleDomainList } from './url-policy';

// ----- payload types -------------------------------------------------------

interface WorkerPagePayload {
  rule: Rule;
  variables?: Record<string, any>;
  taskId: string;
  attemptId?: string;
  workerId: string;
}

interface WorkerCompleteEvent {
  status: 'success' | 'failure' | 'cancelled';
  message?: string;
  error?: { type?: string; message?: string };
  partialData?: unknown;
}

// ----- sessionStorage light state (ported from replay-runner.ts) -----------

const STATE_KEY_PREFIX = 'oc_worker_state_';
const ACTIVE_KEY = 'oc_worker_active';

function stateKey(taskId: string): string {
  return `${STATE_KEY_PREFIX}${taskId}`;
}

function loadWorkerState(taskId: string): WorkerRunState | null {
  try {
    const raw = sessionStorage.getItem(stateKey(taskId));
    if (!raw) return null;
    return JSON.parse(raw) as WorkerRunState;
  } catch {
    return null;
  }
}

function saveWorkerState(state: WorkerRunState): void {
  try {
    sessionStorage.setItem(stateKey(state.taskId), JSON.stringify(state));
    sessionStorage.setItem(ACTIVE_KEY, state.taskId);
  } catch {
    // ignore
  }
}

function clearWorkerState(taskId: string): void {
  try {
    sessionStorage.removeItem(stateKey(taskId));
    if (sessionStorage.getItem(ACTIVE_KEY) === taskId) {
      sessionStorage.removeItem(ACTIVE_KEY);
    }
  } catch {
    // ignore
  }
}

// ----- host reporting ------------------------------------------------------

function bridgeCall(method: string, payload: unknown): Promise<unknown> | null {
  try {
    const fn = (globalThis as Record<string, unknown>).__ocWorkerBridge as
      | ((method: string, payload: unknown) => Promise<unknown>)
      | undefined;
    if (typeof fn !== 'function') return null;
    return Promise.resolve(fn(method, payload));
  } catch {
    return null;
  }
}

function postComplete(payload: WorkerCompleteEvent): void {
  bridgeCall('complete', payload)?.catch(() => undefined);
}

function postCheckpointContext(taskId: string, checkpoint: {
  lastCompletedStepPath: StepPath;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
}): void {
  // Data minimization: the navigation expectation stays in sessionStorage;
  // the host learns only the resumable execution context, never URL query
  // strings or fragments (which can carry tokens).
  bridgeCall('checkpointContext', { taskId, ...checkpoint })?.catch(() => undefined);
}

// ----- abort ---------------------------------------------------------------

let activeAbort: AbortController | null = null;

// ----- entry points --------------------------------------------------------

;(globalThis as any).__ocWorkerBoot = (payload: WorkerPagePayload) => {
  runWithPayload(payload, /*isResume*/ false, /*ctxFromHost*/ undefined);
};

;(globalThis as any).__ocWorkerResume = (payload: WorkerPagePayload, ctxFromHost?: WorkerResumeContext) => {
  runWithPayload(payload, /*isResume*/ true, ctxFromHost);
};

;(globalThis as any).__ocWorkerAbort = () => {
  activeAbort?.abort();
};

async function runWithPayload(
  payload: WorkerPagePayload,
  isResume: boolean,
  ctxFromHost: WorkerResumeContext | undefined,
): Promise<void> {
  const { rule, variables, taskId, workerId } = payload;

  // Realm idempotency guard: prevents boot + resume double-trigger inside one
  // JavaScript realm (same-document navigations keep the realm alive).
  const g = globalThis as Record<string, unknown>;
  if (g.__ocWorkerActive === taskId) return;
  g.__ocWorkerActive = taskId;

  const prior = isResume ? loadWorkerState(taskId) : null;

  if (isResume) {
    const verdict = validateResume(prior, taskId, location.href, ctxFromHost);
    if (!verdict.ok) {
      // A resume refusal after a committed navigation is terminal for the run:
      // report it so the host can terminalize instead of waiting forever.
      postComplete({ status: 'failure', message: verdict.reason });
      delete g.__ocWorkerActive;
      return;
    }
    if (!ctxFromHost) {
      // Every navigation-triggering step writes a checkpoint before dispatch,
      // so a missing host context means checkpoint data was lost. Fail fast
      // rather than silently resuming with empty extraction state.
      postComplete({ status: 'failure', message: 'resume refused: host checkpoint context missing' });
      delete g.__ocWorkerActive;
      return;
    }
  }

  // Preflight (same contract as in-extension replay): reject rules the
  // checkpoint/resume mechanism cannot execute correctly.
  const preflightErrors = preflightRule(rule);
  if (preflightErrors.length > 0) {
    postComplete({
      status: 'failure',
      message: `preflight failed: ${preflightErrors.map((e) => `${e.code}@${e.path} (${e.message})`).join('; ')}`,
    });
    delete g.__ocWorkerActive;
    return;
  }

  // Domain check (parity with replay-runner/userscript-entry).
  if (!hostMatchesDomain(location.hostname, ruleDomainList(rule))) {
    postComplete({ status: 'failure', message: `Domain mismatch: ${location.hostname}` });
    delete g.__ocWorkerActive;
    return;
  }

  const ac = new AbortController();
  activeAbort = ac;

  const transport = new BridgeTransport(ac.signal);
  transport.setRule(rule);
  // Evaluate stays disabled (replay §7.1 invariant): approved rules execute
  // without the script sandbox.
  const env = new BrowserEnvironment(transport, window, false, false);

  const resumeStepPath: StepPath = isResume && prior
    ? nextPendingPath(prior.lastCompletedStepPath ?? EMPTY_PATH)
    : EMPTY_PATH;
  const initialContext = isResume && ctxFromHost
    ? {
        extracted: ctxFromHost.extracted,
        evaluated: ctxFromHost.evaluated,
        captured: ctxFromHost.captured,
      }
    : undefined;

  // Initialize expectedUrl at boot so a rule whose first step is non-navigate
  // still has a verifiable value on resume.
  const baseState: WorkerRunState = {
    taskId,
    ruleId: rule.id,
    lastCompletedStepPath: EMPTY_PATH,
    expectedUrl: normalizeRunUrl(location.href),
    expectedUrlPolicy: 'expected-path',
    status: 'running',
  };
  if (!isResume) saveWorkerState(baseState);

  try {
    const result = await runRule({
      rule,
      taskId,
      workerId,
      variables: variables ?? {},
      env,
      signal: ac.signal,
      resumeStepPath,
      initialContext,
      onCheckpoint: (cp) => {
        let expectedUrl = normalizeRunUrl(location.href);
        let expectedUrlPolicy: WorkerRunState['expectedUrlPolicy'] = 'expected-path';
        if (cp.navigation) {
          expectedUrlPolicy = cp.navigation.urlPolicy;
          try {
            // Keep only origin+pathname: query strings and fragments can
            // contain tokens or other sensitive values.
            expectedUrl = normalizeRunUrl(cp.navigation.expectedUrl, location.href);
          } catch {
            expectedUrl = normalizeRunUrl(location.href);
            expectedUrlPolicy = 'same-origin';
          }
        }
        saveWorkerState({
          ...baseState,
          lastCompletedStepPath: cp.lastCompletedStepPath,
          expectedUrl,
          expectedUrlPolicy,
        });
        postCheckpointContext(taskId, {
          lastCompletedStepPath: cp.lastCompletedStepPath,
          extracted: cp.extracted,
          evaluated: cp.evaluated,
          captured: cp.captured,
        });
      },
      onStepFailure: (path) => {
        // Roll back the light state so resume retries the failed navigation
        // step instead of skipping it (replay §4.1).
        const s = loadWorkerState(taskId);
        if (s) saveWorkerState({ ...s, lastCompletedStepPath: prevPendingPath(path) });
      },
    });
    // Terminal marker: the last top-level step is complete (parity with the
    // userscript/replay entries).
    const terminalPath: StepPath = rule.steps.length > 0
      ? [{ kind: 'top', childIdx: rule.steps.length - 1 }]
      : EMPTY_PATH;
    saveWorkerState({
      ...baseState,
      lastCompletedStepPath: terminalPath,
      status: result.status === 'success' ? 'done' : 'failed',
    });
    postComplete({
      status: result.status,
      message: result.message,
      error: result.error ? { type: result.error.type, message: result.error.message } : undefined,
      partialData: result.partialData ?? transport.finalMarker,
    });
  } catch (err) {
    saveWorkerState({ ...baseState, status: 'failed' });
    postComplete({
      status: 'failure',
      message: err instanceof Error ? err.message : String(err),
    });
  } finally {
    if (activeAbort === ac) activeAbort = null;
    clearWorkerState(taskId);
    delete g.__ocWorkerActive;
  }
}
