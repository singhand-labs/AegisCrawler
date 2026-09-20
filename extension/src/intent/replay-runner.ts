/**
 * Replay runner — executes a rule via the full runRule lifecycle with
 * cross-navigation resume support.
 *
 * Design (docs/replay-fix-plan.md §5.1):
 *   - Two globals (__ocReplayBoot / __ocReplayAutoResume) invoked by the
 *     background via chrome.scripting.executeScript({func, args}). This removes
 *     the chrome.runtime.onMessage registration race that the old
 *     START_REPLAY_RUNNER listener suffered from.
 *   - Cross-navigation survival mirrors userscript-entry.ts: lightweight
 *     control fields in sessionStorage (ReplayState), full context in
 *     chrome.storage.session via the REPLAY_CONTEXT message (data minimization,
 *     §7.3).
 *   - §4.1: onStepFailure rolls lastCompletedStepPath back via prevPendingPath
 *     so a failed navigation action is retried rather than skipped on resume.
 *     Phase 3 generalises the rollback to nested paths (recursive checkpoints).
 *
 * Trust boundary (SEC): the injected func runs in the isolated world; page main
 * world cannot redefine these globals. Do NOT set world: 'MAIN'.
 */

import { runRule, type HookPhase } from '../../../src/scriptcat-engine/executor';
import { preflightRule } from '../../../src/scriptcat-engine/rule-preflight';
import {
  EMPTY_PATH,
  nextPendingPath,
  prevPendingPath,
  comparePath,
  formatPath,
  type StepPath,
} from '../../../src/scriptcat-engine/step-path';
import { BrowserEnvironment } from '../../../src/worker/BrowserEnvironment';
import type { Rule } from '../../../src/scriptcat-engine/types';
import { ReplayTransport } from './replay-transport';

// ----- types ---------------------------------------------------------------

interface ReplayState {
  taskId: string;
  ruleId: string;
  lastCompletedStepPath: StepPath;
  expectedUrl: string;
  expectedUrlPolicy?: 'expected-path' | 'same-origin';
  status: 'running' | 'done' | 'failed';
}

interface ReplayContext {
  lastCompletedStepPath: StepPath;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
  /** Extension-isolated fallback when an approved origin change loses sessionStorage. */
  control?: ReplayState;
  hooksCompleted?: HookPhase[];
}

interface ReplayPayload {
  rule: Rule;
  variables?: Record<string, any>;
  taskId: string;
  workerId: string;
}

// ----- domain validation (parity with background.ts H-5) -------------------

function isValidDomainEntry(d: string): boolean {
  // Loopback exemption: see background.ts H-5 ('*.localhost' resolves to loopback).
  if (d === 'localhost') return true;
  return d.length >= 3 && d.includes('.') && !d.startsWith('.') && !d.endsWith('.');
}

// ----- storage helpers (ported from userscript-entry.ts) -------------------

const STATE_KEY_PREFIX = 'oc_replay_state_';
const ACTIVE_KEY = 'oc_replay_active';

function stateKey(taskId: string): string {
  return `${STATE_KEY_PREFIX}${taskId}`;
}

function loadReplayState(taskId: string): ReplayState | null {
  try {
    const raw = sessionStorage.getItem(stateKey(taskId));
    if (!raw) return null;
    return JSON.parse(raw) as ReplayState;
  } catch {
    return null;
  }
}

function readActiveReplayState(): ReplayState | null {
  try {
    const activeTaskId = sessionStorage.getItem(ACTIVE_KEY);
    if (!activeTaskId) return null;
    return loadReplayState(activeTaskId);
  } catch {
    return null;
  }
}

function saveReplayState(state: ReplayState): void {
  try {
    sessionStorage.setItem(stateKey(state.taskId), JSON.stringify(state));
    sessionStorage.setItem(ACTIVE_KEY, state.taskId);
  } catch {
    // ignore
  }
}

function clearReplayState(taskId: string): void {
  try {
    sessionStorage.removeItem(stateKey(taskId));
    if (sessionStorage.getItem(ACTIVE_KEY) === taskId) {
      sessionStorage.removeItem(ACTIVE_KEY);
    }
  } catch {
    // ignore
  }
}

function replayUrl(rawUrl: string): string {
  const url = new URL(rawUrl, location.href);
  return `${url.origin}${url.pathname}`;
}

function pathnameMatches(actual: string, expected: string): boolean {
  if (actual === expected) return true;
  const childPrefix = expected.endsWith('/') ? expected : `${expected}/`;
  return actual.startsWith(childPrefix);
}

// ----- messaging -----------------------------------------------------------

function postComplete(taskId: string, payload: {
  status: 'success' | 'failure' | 'cancelled';
  message?: string;
  error?: unknown;
}): void {
  try {
    Promise.resolve(
      chrome.runtime.sendMessage({ action: 'REPLAY_COMPLETE', payload: { ...payload, taskId } }),
    ).catch(() => undefined);
  } catch {
    // ignore
  }
}

// ----- abort ---------------------------------------------------------------

function registerAbortListener(ac: AbortController): void {
  try {
    const listener = (message: any) => {
      if (message?.action === 'ABORT_REPLAY') {
        // No origin check: chrome.runtime messages only reach this listener
        // from the same extension context (background / SW).
        ac.abort();
      }
    };
    chrome.runtime.onMessage.addListener(listener);
    // Best-effort cleanup: clear listener when the tab unloads. The AbortController
    // is also consumed by runRule; on tab close the realm dies anyway.
    window.addEventListener('pagehide', () => {
      try {
        chrome.runtime.onMessage.removeListener(listener);
      } catch {
        // ignore
      }
    });
  } catch {
    // ignore
  }
}

// ----- entry points --------------------------------------------------------

;(globalThis as any).__ocReplayBoot = (payload: ReplayPayload) => {
  runWithPayload(payload, /*isResume*/ false, /*initialCtx*/ undefined);
};

;(globalThis as any).__ocReplayAutoResume = (payload: ReplayPayload, ctxFromBg?: ReplayContext) => {
  const prior = readActiveReplayState() ?? ctxFromBg?.control ?? null;
  if (!prior || prior.status !== 'running') return;
  if (!payload || payload.taskId !== prior.taskId) return;
  runWithPayload(payload, /*isResume*/ true, ctxFromBg);
};

async function runWithPayload(
  payload: ReplayPayload,
  isResume: boolean,
  ctxFromBg: ReplayContext | undefined,
): Promise<void> {
  const { rule, variables, taskId, workerId } = payload;

  // Realm idempotency guard: prevents boot + autoResume double-trigger.
  const g = globalThis as Record<string, unknown>;
  if (g.__ocReplayActive === taskId) return;
  g.__ocReplayActive = taskId;

  const localPrior = isResume ? loadReplayState(taskId) : null;
  const persistedPrior = isResume
    && ctxFromBg?.control?.taskId === taskId
    && ctxFromBg.control.ruleId === rule.id
    && ctxFromBg.control.status === 'running'
    ? ctxFromBg.control
    : null;
  const prior = localPrior ?? persistedPrior;

  if (isResume) {
    if (!prior || prior.status !== 'running') {
      delete g.__ocReplayActive;
      return;
    }
    // sessionStorage is origin-scoped. Seed the approved destination origin
    // from the extension-isolated checkpoint before continuing.
    if (!localPrior) saveReplayState(prior);
    // §5.1: empty expectedUrl means a first-step pre-checkpoint anomaly —
    // fail-fast rather than silently skipping.
    if (!prior.expectedUrl) {
      postComplete(taskId, {
        status: 'failure',
        message: 'resume refused: expectedUrl empty (first-step pre-checkpoint anomaly)',
      });
      delete g.__ocReplayActive;
      return;
    }
    // A known navigate/reload target requires the expected path. Event-driven
    // navigation (click, submit, key press) has no trustworthy destination at
    // dispatch time, so its pre-navigation checkpoint permits a path change
    // only within the same origin. The rule-domain check below and the
    // background listener remain defense-in-depth against cross-domain hops.
    let expected: URL;
    let actual: URL;
    try {
      expected = new URL(prior.expectedUrl);
      actual = new URL(location.href);
    } catch {
      postComplete(taskId, { status: 'failure', message: `resume URL parse failed: ${location.href}` });
      delete g.__ocReplayActive;
      return;
    }
    const pathOk = prior.expectedUrlPolicy === 'same-origin'
      || pathnameMatches(actual.pathname, expected.pathname);
    const hostOk = actual.host === expected.host;
    const protoOk = actual.protocol === expected.protocol;
    if (!hostOk || !protoOk || !pathOk) {
      postComplete(taskId, {
        status: 'failure',
        message: `resume URL mismatch: ${location.href} vs expected ${prior.expectedUrl}`,
      });
      delete g.__ocReplayActive;
      return;
    }
    // Monotonicity check: if background's ctx lags sessionStorage's
    // lastCompletedStepPath, the last REPLAY_CONTEXT message was lost when SW
    // died — data is stale. Fail-fast rather than recover with wrong data.
    // Phase 3: compare paths lexicographically (DFS order).
    if (ctxFromBg && comparePath(ctxFromBg.lastCompletedStepPath, prior.lastCompletedStepPath) < 0) {
      postComplete(taskId, {
        status: 'failure',
        message: `resume refused: ctx stale (ctx=${formatPath(ctxFromBg.lastCompletedStepPath)} < ss=${formatPath(prior.lastCompletedStepPath)})`,
      });
      delete g.__ocReplayActive;
      return;
    }
  }

  // Preflight (§7.2). Fail-fast on rules the v1 checkpoint cannot handle.
  const preflightErrors = preflightRule(rule);
  if (preflightErrors.length > 0) {
    postComplete(taskId, {
      status: 'failure',
      message: `preflight failed: ${preflightErrors.map((e) => `${e.code}@${e.path} (${e.message})`).join('; ')}`,
    });
    delete g.__ocReplayActive;
    return;
  }

  // Domain check (parity with userscript-entry.ts:148-155 and background.ts H-5).
  const domains = Array.isArray(rule.domain) ? rule.domain : [rule.domain];
  const invalidDomains = domains.filter((d) => !isValidDomainEntry(String(d)));
  if (invalidDomains.length > 0) {
    postComplete(taskId, { status: 'failure', message: `Domain entries too broad: ${invalidDomains.join(', ')}` });
    delete g.__ocReplayActive;
    return;
  }
  if (!domains.some((d) => location.hostname === d || location.hostname.endsWith('.' + d))) {
    postComplete(taskId, { status: 'failure', message: `Domain mismatch: ${location.hostname}` });
    delete g.__ocReplayActive;
    return;
  }

  const ac = new AbortController();
  registerAbortListener(ac);

  const transport = new ReplayTransport();
  transport.setRule(rule);
  // §7.1 invariant: allowEvaluate/allowEvaluateDOM hardcoded false. The script
  // sandbox RCE chain closed in 94e25be stays closed.
  const env = new BrowserEnvironment(transport, window, false, false);

  const resumeStepPath: StepPath = isResume && prior
    ? nextPendingPath(prior.lastCompletedStepPath ?? EMPTY_PATH)
    : EMPTY_PATH;
  const initialContext = isResume && ctxFromBg
    ? {
        extracted: ctxFromBg.extracted,
        evaluated: ctxFromBg.evaluated,
        captured: ctxFromBg.captured,
      }
    : undefined;
  const initialHooksCompleted = isResume && ctxFromBg?.hooksCompleted
    ? ctxFromBg.hooksCompleted
    : undefined;

  // Initialize expectedUrl at boot so a rule whose first step is non-navigate
  // still has a verifiable value on resume.
  const bootUrl = new URL(location.href);
  const baseState: ReplayState = {
    taskId,
    ruleId: rule.id,
    lastCompletedStepPath: EMPTY_PATH,
    expectedUrl: `${bootUrl.origin}${bootUrl.pathname}`,
    expectedUrlPolicy: 'expected-path',
    status: 'running',
  };
  if (!isResume) saveReplayState(baseState);

  const heartbeat = setInterval(() => {
    transport
      .sendHeartbeat({ url: location.href })
      .then((r) => {
        if (r.cancelRequested) ac.abort();
      })
      .catch(() => {
        // ignore
      });
  }, 30000);

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
      initialHooksCompleted,
      humanizeReplay: true,
      onCheckpoint: (cp) => {
        // §7.3: control fields in sessionStorage; full context via background
        // (chrome.storage.session is extension-isolated, unreadable by page).
        let expectedUrl = replayUrl(location.href);
        let expectedUrlPolicy: NonNullable<ReplayState['expectedUrlPolicy']> = 'expected-path';
        if (cp.navigation) {
          expectedUrlPolicy = cp.navigation.urlPolicy;
          try {
            // Keep only origin+pathname: query strings and fragments can
            // contain tokens or other sensitive values.
            expectedUrl = replayUrl(cp.navigation.expectedUrl);
          } catch {
            // Invalid explicit destinations fail in the executor itself. Until
            // that happens, do not let the recovery guard broaden beyond the
            // current origin.
            expectedUrl = replayUrl(location.href);
            expectedUrlPolicy = 'same-origin';
          }
        }
        const nextState: ReplayState = {
          ...baseState,
          lastCompletedStepPath: cp.lastCompletedStepPath,
          expectedUrl,
          expectedUrlPolicy,
        };
        saveReplayState(nextState);
        try {
          Promise.resolve(
            chrome.runtime.sendMessage({
              action: 'REPLAY_CONTEXT',
              payload: {
                taskId,
                lastCompletedStepPath: cp.lastCompletedStepPath,
                extracted: cp.extracted,
                evaluated: cp.evaluated,
                captured: cp.captured,
                control: nextState,
                hooksCompleted: cp.hooksCompleted,
              },
            }),
          ).catch(() => undefined);
        } catch {
          // ignore
        }
      },
      onStepFailure: (path) => {
        // §4.1: rollback control field to prev path so resume retries the
        // failed step. Phase 3: prevPendingPath generalises idx-1 to nested
        // paths.
        const s = loadReplayState(taskId);
        if (s) saveReplayState({ ...s, lastCompletedStepPath: prevPendingPath(path) });
      },
    });
    // L-2: terminal save removed — clearReplayState in finally would clear it
    // immediately, making the save dead code. postComplete is the authoritative
    // terminal signal to the background.
    postComplete(taskId, { status: result.status, message: result.message, error: result.error });
  } catch (err) {
    postComplete(taskId, {
      status: 'failure',
      message: err instanceof Error ? err.message : String(err),
    });
  } finally {
    clearInterval(heartbeat);
    clearReplayState(taskId);
    delete g.__ocReplayActive;
  }
}
