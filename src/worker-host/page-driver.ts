/**
 * Page driver — executes one claimed task inside a real Chromium page of the
 * worker slot's persistent profile.
 *
 * Responsibilities:
 *   - Validate the rule entry URL against rule.domain BEFORE any navigation
 *     (port of the extension background's assertEntryUrlDomain spirit).
 *   - Open a fresh page per task (state isolation between tasks), install the
 *     single __ocWorkerBridge exposed function exactly once, inject the
 *     in-memory worker page bundle, and boot the run.
 *   - Mediate every server interaction the page runtime requests (bridge
 *     whitelist + payload validation in bridge-protocol.ts). The page never
 *     sees WORKER_API_KEY or constructs requests itself.
 *   - Enforce navigation policy host-side: only cross-document navigations
 *     count as hops; every committed target must stay inside rule.domain;
 *     the hop limit kills redirect loops. Valid navigations trigger bundle
 *     re-injection and __ocWorkerResume with the host-held checkpoint context.
 *   - Report the page's execution outcome back to the Worker, which keeps
 *     terminal status authority (summary + done/failed/cancelled).
 */

import type { BrowserContext, Frame, Page } from 'playwright';
import type { Rule } from '../scriptcat-engine/types';
import { comparePath } from '../scriptcat-engine/step-path';
import { interpolate } from '../scriptcat-engine/utils';
import type { RuleExecutorContext, RuleExecutorResult } from '../worker/Worker';
import {
  BridgeValidationError,
  validateBridgeCall,
  type BridgeCheckpointContext,
  type BridgeCompletePayload,
} from './bridge-protocol';
import { assertEntryUrlAllowed, isNavigationAllowed } from '../worker-page/url-policy';

export interface PageDriverOptions {
  context: BrowserContext;
  bundle: string;
  verbose?: boolean;
  /** Host-only test/diagnostic observation after success, before page close. */
  onPageComplete?: (page: Page, result: RuleExecutorResult) => Promise<void> | void;
  /** Maximum cross-document navigations per run (redirect-loop guard). */
  navigationHopLimit?: number;
  /** Grace period for a page run to wind down after an abort request. */
  abortGraceMs?: number;
  /** Timeout for the initial entry navigation. */
  navigationTimeoutMs?: number;
}

const DEFAULT_HOP_LIMIT = 50;
const DEFAULT_ABORT_GRACE_MS = 5000;
const DEFAULT_NAVIGATION_TIMEOUT_MS = 60000;

export async function waitForPageLoad(
  page: Pick<Page, 'waitForLoadState'>,
  timeoutMs: number,
): Promise<void> {
  await page.waitForLoadState('load', { timeout: timeoutMs });
}

interface WorkerPagePayload {
  rule: Rule;
  variables: Record<string, any>;
  taskId: string;
  workerId: string;
}

function resolveEntryUrl(rule: Rule, variables: Record<string, any>): string {
  const raw = typeof rule.entry === 'string' ? rule.entry : (rule.entry as { url?: string } | undefined)?.url;
  if (!raw) {
    throw new Error(`Rule ${rule.id} has no entry URL`);
  }
  const resolved = interpolate(raw, {
    variables: { ...rule.variables, ...variables },
    extracted: {},
    evaluated: {},
    captured: {},
  });
  return String(resolved);
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Execute the host-built runtime through Playwright's trusted execution
 * context. Adding an inline script element makes otherwise safe approved-rule
 * execution depend on the target site's script-src policy (MDN forbids it).
 * The bundle is built locally by WorkerHost and never contains page input.
 */
async function installWorkerRuntime(page: Page, bundle: string): Promise<void> {
  await page.evaluate(bundle);
}

export function createPageRuleExecutor(
  options: PageDriverOptions,
): (ctx: RuleExecutorContext) => Promise<RuleExecutorResult> {
  const hopLimit = options.navigationHopLimit ?? DEFAULT_HOP_LIMIT;
  const abortGraceMs = options.abortGraceMs ?? DEFAULT_ABORT_GRACE_MS;
  const navigationTimeoutMs = options.navigationTimeoutMs ?? DEFAULT_NAVIGATION_TIMEOUT_MS;
  const log = (message: string): void => {
    if (options.verbose) {
      // eslint-disable-next-line no-console
      console.log(`[page-driver] ${message}`);
    }
  };

  return async function executeInPage(ctx: RuleExecutorContext): Promise<RuleExecutorResult> {
    const { rule, taskId, workerId, variables, signal, transport } = ctx;
    const entryUrl = resolveEntryUrl(rule, variables ?? {});
    // Pre-navigation entry validation: parse, HTTP(S) only, host must be
    // covered by rule.domain. Throws before the browser ever sees the URL.
    assertEntryUrlAllowed(entryUrl, rule);

    const page: Page = await options.context.newPage();
    const bootPayload: WorkerPagePayload = { rule, variables: variables ?? {}, taskId, workerId };

    const state = {
      armed: false,
      settled: false,
      tornDown: false,
      checkpoint: null as BridgeCheckpointContext | null,
      hopCount: 0,
    };

    let settle!: (result: RuleExecutorResult) => void;
    const completion = new Promise<RuleExecutorResult>((resolve) => {
      settle = resolve;
    });
    const finishOnce = (result: RuleExecutorResult): void => {
      if (state.settled) return;
      state.settled = true;
      settle(result);
    };

    const abortPageRun = (): void => {
      void page.evaluate(() => (globalThis as any).__ocWorkerAbort?.()).catch(() => undefined);
    };

    const failRun = (message: string): void => {
      log(`task ${taskId} failed by host policy: ${message}`);
      void transport.sendLog('warn', `worker host policy failure: ${message}`).catch(() => undefined);
      abortPageRun();
      finishOnce({ status: 'failure', message });
    };

    const settleFromPage = (payload: BridgeCompletePayload): void => {
      finishOnce({
        status: payload.status,
        message: payload.message ?? payload.error?.message,
        data: payload.partialData,
      });
    };

    // The one and only page→host channel. Page JS can reach nothing else:
    // this closure never exposes credentials, URLs it did not validate, or
    // methods outside the bridge whitelist.
    const bridgeHandler = async (method: unknown, rawPayload: unknown): Promise<unknown> => {
      const call = validateBridgeCall(method, rawPayload);
      if (state.settled && call.method !== 'complete') {
        throw new BridgeValidationError('run already finished');
      }
      switch (call.method) {
        case 'sendResult':
          await transport.sendResult(call.payload.payload, call.payload.immediate);
          return null;
        case 'sendLog':
          await transport.sendLog(call.payload.level, call.payload.message, call.payload.extra);
          return null;
        case 'sendStatus':
          await transport.sendStatus(call.payload.status, call.payload.message);
          return null;
        case 'sendSnapshot':
          await transport.sendSnapshot(call.payload);
          return null;
        case 'sendHeartbeat':
          return transport.sendHeartbeat(call.payload.payload);
        case 'requestHuman':
          if (typeof transport.requestHuman !== 'function') {
            throw new BridgeValidationError('human interventions are unavailable for this task');
          }
          return transport.requestHuman(call.payload);
        case 'checkpointContext': {
          if (call.payload.taskId !== taskId) {
            throw new BridgeValidationError('checkpoint task mismatch');
          }
          // Monotonic guard: a dying realm's in-flight checkpoint must not
          // overwrite a newer one from a successor realm.
          if (!state.checkpoint
            || comparePath(call.payload.lastCompletedStepPath, state.checkpoint.lastCompletedStepPath) >= 0) {
            state.checkpoint = call.payload;
          }
          return null;
        }
        case 'complete':
          settleFromPage(call.payload);
          return null;
      }
    };
    await page.exposeFunction('__ocWorkerBridge', bridgeHandler);

    const resumeContext = (): Record<string, unknown> | undefined => {
      if (!state.checkpoint) return undefined;
      return {
        lastCompletedStepPath: state.checkpoint.lastCompletedStepPath,
        extracted: state.checkpoint.extracted,
        evaluated: state.checkpoint.evaluated,
        captured: state.checkpoint.captured,
      };
    };

    let navChain: Promise<void> = Promise.resolve();
    const handleNavigation = async (url: string): Promise<void> => {
      try {
        await waitForPageLoad(page, navigationTimeoutMs);
        if (state.settled || state.tornDown) return;
        // Same-document navigations (pushState/replaceState/hash) keep the
        // realm alive: the run is still in progress there, so no hop is
        // counted and no resume is needed.
        const activeMarker = await page
          .evaluate(() => (globalThis as any).__ocWorkerActive ?? null)
          .catch(() => null);
        if (state.settled || state.tornDown) return;
        if (activeMarker === taskId) return;
        state.hopCount += 1;
        if (state.hopCount > hopLimit) {
          failRun(`navigation hop limit ${hopLimit} exceeded`);
          return;
        }
        if (!isNavigationAllowed(url, rule)) {
          let host = '<unparseable>';
          try {
            host = new URL(url).hostname;
          } catch {
            // keep fallback
          }
          failRun(`navigation to ${host} blocked: outside rule.domain`);
          return;
        }
        await installWorkerRuntime(page, options.bundle);
        if (state.settled || state.tornDown) return;
        await page.evaluate(([p, c]) => {
          const resume = (globalThis as any).__ocWorkerResume;
          if (typeof resume !== 'function') throw new Error('worker page runtime not loaded');
          resume(p, c);
        }, [bootPayload, resumeContext()] as const);
      } catch (err) {
        if (!state.settled && !state.tornDown) {
          failRun(`resume after navigation failed: ${errorMessage(err)}`);
        }
      }
    };
    const onNavigated = (frame: Frame): void => {
      if (frame !== page.mainFrame() || !state.armed || state.settled || state.tornDown) return;
      const url = frame.url();
      navChain = navChain.then(() => handleNavigation(url));
    };
    page.on('framenavigated', onNavigated);
    page.on('crash', () => failRun('page crashed during execution'));
    page.on('close', () => {
      if (!state.tornDown && !state.settled) {
        finishOnce({ status: 'failure', message: 'page closed unexpectedly during execution' });
      }
    });

    const onAbort = (): void => {
      void (async () => {
        abortPageRun();
        await sleep(abortGraceMs);
        finishOnce({ status: 'cancelled', message: 'Task cancelled by operator' });
      })();
    };
    if (signal.aborted) {
      onAbort();
    } else {
      signal.addEventListener('abort', onAbort, { once: true });
    }

    try {
      await page.goto(entryUrl, { waitUntil: 'load', timeout: navigationTimeoutMs });
      // Redirects during the entry navigation bypass the armed handler, so
      // validate the landed URL explicitly.
      if (!isNavigationAllowed(page.url(), rule)) {
        throw new Error(`entry URL redirected outside rule.domain: ${page.url()}`);
      }
      await installWorkerRuntime(page, options.bundle);
      await page.evaluate((p) => {
        const boot = (globalThis as any).__ocWorkerBoot;
        if (typeof boot !== 'function') throw new Error('worker page runtime not loaded');
        boot(p);
      }, bootPayload);
      // No event can interleave between the resolved evaluate and this
      // statement, so navigation handling cannot miss the first commit.
      state.armed = true;
      const result = await completion;
      if (result.status === 'success') {
        await options.onPageComplete?.(page, result);
      }
      return result;
    } finally {
      state.tornDown = true;
      signal.removeEventListener('abort', onAbort);
      page.removeListener('framenavigated', onNavigated);
      await page.close().catch(() => undefined);
    }
  };
}
