import { runRule } from './executor';
import { createTransport } from './transport';
import { createEnvironment, loadTaskFromHash, isTrustedServerUrl, getWorkerApiKey } from './environment';
import { EMPTY_PATH, nextPendingPath, type StepPath } from './step-path';
import type { Rule } from './types';

interface TaskSessionState {
  taskId: string;
  ruleId: string;
  serverUrl: string;
  workerId: string;
  attemptId?: string;
  closeOnDone?: boolean;
  variables: Record<string, any>;
  lastCompletedStepPath: StepPath;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
  status: 'running' | 'done' | 'failed';
}

interface TaskDescriptor {
  taskId: string;
  ruleId: string;
  serverUrl: string;
  workerId: string;
  attemptId?: string;
  closeOnDone?: boolean;
  variables: Record<string, any>;
}

const ACTIVE_TASK_KEY = 'oc_active_task';

function taskStateKey(taskId: string): string {
  return `oc_task_state_${taskId}`;
}

function loadTaskState(taskId: string): TaskSessionState | null {
  try {
    const raw = sessionStorage.getItem(taskStateKey(taskId));
    if (!raw) return null;
    return JSON.parse(raw) as TaskSessionState;
  } catch {
    return null;
  }
}

function saveTaskState(state: TaskSessionState): void {
  try {
    sessionStorage.setItem(taskStateKey(state.taskId), JSON.stringify(state));
    sessionStorage.setItem(ACTIVE_TASK_KEY, state.taskId);
  } catch {
    // ignore
  }
}

function clearTaskState(taskId: string): void {
  try {
    sessionStorage.removeItem(taskStateKey(taskId));
    if (sessionStorage.getItem(ACTIVE_TASK_KEY) === taskId) {
      sessionStorage.removeItem(ACTIVE_TASK_KEY);
    }
  } catch {
    // ignore
  }
}

function loadTaskFromHashOrSession(): TaskDescriptor | null {
  const fromHash = loadTaskFromHash();
  if (fromHash) {
    return fromHash;
  }

  // If a navigation dropped the #scrape= hash, fall back to the active task
  // state stored in sessionStorage so the run can resume.
  try {
    const activeTaskId = sessionStorage.getItem(ACTIVE_TASK_KEY);
    if (!activeTaskId) return null;
    const state = loadTaskState(activeTaskId);
    if (!state || state.status !== 'running') {
      sessionStorage.removeItem(ACTIVE_TASK_KEY);
      return null;
    }
    const descriptor = {
      taskId: state.taskId,
      ruleId: state.ruleId,
      serverUrl: state.serverUrl,
      workerId: state.workerId,
      attemptId: state.attemptId,
      closeOnDone: state.closeOnDone,
      variables: state.variables,
    };
    // loadTaskFromHash already validates, but session-restored tasks must also be checked.
    if (!isTrustedServerUrl(descriptor.serverUrl)) {
      console.error('[AegisCrawler] rejected untrusted serverUrl in session:', descriptor.serverUrl);
      sessionStorage.removeItem(ACTIVE_TASK_KEY);
      return null;
    }
    return descriptor;
  } catch {
    return null;
  }
}

/**
 * Dispatcher opt-in: close this tab once the task reaches a terminal state.
 * Only script-opened tabs (e.g. a GM_openInTab dispatch) may close themselves;
 * in any other context window.close() is a harmless no-op and the dispatcher's
 * tab-lifetime backstop still applies. Long-running dispatch loops without
 * this leak one background tab per task until memory pressure freezes them.
 */
function closeTabIfRequested(descriptor: TaskDescriptor): void {
  if (!descriptor.closeOnDone) return;
  setTimeout(() => {
    try { window.close(); } catch { /* not script-opened; nothing to do */ }
  }, 500);
}

async function boot(): Promise<void> {
  const task = loadTaskFromHashOrSession();
  if (!task) {
    // No active task for this page
    return;
  }

  const { taskId, ruleId, serverUrl, workerId, attemptId, closeOnDone, variables } = task;

  // Prevent duplicate boot calls in the same JavaScript realm.
  const g = globalThis as Record<string, unknown>;
  if (g.__opencrawlerBootActive === taskId) {
    return;
  }
  g.__opencrawlerBootActive = taskId;

  const existingState = loadTaskState(taskId);

  // If this task already finished in this tab session, do not restart it.
  if (existingState?.status === 'done' || existingState?.status === 'failed') {
    clearTaskState(taskId);
    closeTabIfRequested(task);
    return;
  }

  // If a previous run was interrupted by a full-page navigation, resume from
  // the step after the last completed one and restore its context.
  const isResume =
    existingState?.status === 'running' &&
    existingState.ruleId === ruleId;
  const resumeStepPath: StepPath = isResume
    ? nextPendingPath(existingState!.lastCompletedStepPath ?? EMPTY_PATH)
    : EMPTY_PATH;
  const initialContext = isResume
    ? {
        extracted: existingState!.extracted,
        evaluated: existingState!.evaluated,
        captured: existingState!.captured,
      }
    : undefined;

  const transport = createTransport(serverUrl, taskId, workerId, { attemptId, workerApiKey: getWorkerApiKey() });
  const env = createEnvironment(transport);

  // Fetch rule from server
  const rule = await transport.fetchRule(ruleId) as Rule | null;
  if (!rule) {
    await transport.sendStatus('failed', `Rule ${ruleId} not found`);
    clearTaskState(taskId);
    closeTabIfRequested(task);
    return;
  }

  // Match domain
  const domains = Array.isArray(rule.domain) ? rule.domain : [rule.domain];
  const currentHost = location.hostname;
  const matched = domains.some((d) => currentHost === d || currentHost.endsWith('.' + d));
  if (!matched) {
    await transport.sendStatus('failed', `Domain mismatch: ${currentHost} not in ${domains.join(', ')}`);
    clearTaskState(taskId);
    closeTabIfRequested(task);
    return;
  }

  // Start heartbeat loop
  const abortController = new AbortController();
  const heartbeatInterval = setInterval(() => {
    transport
      .sendHeartbeat({ url: location.href })
      .then((resp) => {
        if (resp.cancelRequested) {
          abortController.abort();
        }
      })
      .catch(() => {});
  }, 30000);

  const baseState: TaskSessionState = {
    taskId,
    ruleId,
    serverUrl,
    workerId,
    attemptId,
    closeOnDone,
    variables,
    lastCompletedStepPath: EMPTY_PATH,
    extracted: initialContext?.extracted ?? {},
    evaluated: initialContext?.evaluated ?? {},
    captured: initialContext?.captured ?? {},
    status: 'running',
  };
  if (!isResume) {
    saveTaskState(baseState);
  }

  // Run rule
  const result = await runRule({
    rule,
    taskId,
    workerId,
    variables,
    env,
    signal: abortController.signal,
    resumeStepPath,
    initialContext,
    onCheckpoint: (checkpoint) => {
      saveTaskState({
        ...baseState,
        lastCompletedStepPath: checkpoint.lastCompletedStepPath,
        extracted: checkpoint.extracted,
        evaluated: checkpoint.evaluated,
        captured: checkpoint.captured,
      });
    },
  });

  clearInterval(heartbeatInterval);

  // Terminal marker: the last top-level step is complete. Encoded as
  // [{top, rule.steps.length-1}] so a follow-up resume (defensive — should not
  // happen for a successful run) picks up after the final step.
  const terminalPath: StepPath = rule.steps.length > 0
    ? [{ kind: 'top', childIdx: rule.steps.length - 1 }]
    : EMPTY_PATH;
  if (result.status === 'success') {
    saveTaskState({
      ...baseState,
      lastCompletedStepPath: terminalPath,
      extracted: result.partialData ?? {},
      status: 'done',
    });
  } else {
    saveTaskState({
      ...baseState,
      lastCompletedStepPath: baseState.lastCompletedStepPath,
      extracted: result.partialData ?? {},
      status: 'failed',
    });
  }

  // Attempt-scoped tasks: the versioned worker API requires one summary plus
  // an attempt-bound terminal status; runRule's own terminal reports are
  // skipped by the versioned transport. Mirrors the worker host's Worker.runTask.
  if (attemptId) {
    try {
      await transport.sendFinalSummary?.({
        status: result.status,
        message: result.message,
        data: result.partialData ?? {},
      });
      const taskStatus = result.status === 'cancelled' ? 'cancelled'
        : result.status === 'success' ? 'done' : 'failed';
      await transport.sendStatus(taskStatus, result.message ?? '');
    } catch (e) {
      console.error('[AegisCrawler] final attempt report failed', e);
      try {
        await transport.sendStatus('failed', `final report failed: ${String((e as Error)?.message ?? e)}`);
      } catch { /* transport already failing; lease expiry will retry the task */ }
    }
  }

  closeTabIfRequested(task);
}

// Expose boot for IIFE footer
(globalThis as any).boot = boot;
export { boot };
