import * as crypto from 'crypto';
import * as fs from 'fs';
import * as http from 'http';
import * as os from 'os';
import * as path from 'path';
import type { AddressInfo } from 'net';
import { chromium, type BrowserContext, type Page, type Worker } from 'playwright';
import { extensionChromiumArgs } from './extension-chromium-launch';
import {
  createQualificationRunRecord,
  readQualificationGitState,
  saveQualificationRunRecord,
} from './qualification/evidence';
import { loadQualificationArchive } from './qualification/history';
import { buildMV3StabilityRunRecord } from './qualification/mv3-evidence';

const DEFAULT_SEED = 20_260_723;
const ROUTINE_ITERATIONS = 20;
const QUALIFICATION_ITERATIONS = 50;
const TERMINAL_TIMEOUT_MS = 15_000;
const RUNNER_START_TIMEOUT_MS = 35_000;
const CLEANUP_TIMEOUT_MS = 5_000;
const EVENT_QUIET_MS = 200;
const DIAGNOSTIC_BUDGET = 8_000;
const QUALIFICATION_SOURCE_ARCHIVE_ENV = 'AEGIS_QUALIFICATION_SOURCE_ARCHIVE';

export const REPLAY_STABILITY_SCENARIOS = [
  'navigation-reinjection',
  'duplicate-terminal',
  'service-worker-restart',
  'retained-tab-supersession',
  'approval-abort-cleanup',
  'terminal-cleanup-matrix',
] as const;

export type ReplayStabilityScenario = typeof REPLAY_STABILITY_SCENARIOS[number];
export type ServiceWorkerRestartPhase = 'pre-navigation' | 'checkpoint' | 'post-navigation';

const REPLAY_STABILITY_BLOCK: ReplayStabilityScenario[] = [
  'navigation-reinjection',
  'navigation-reinjection',
  'navigation-reinjection',
  'duplicate-terminal',
  'duplicate-terminal',
  'service-worker-restart',
  'service-worker-restart',
  'retained-tab-supersession',
  'approval-abort-cleanup',
  'terminal-cleanup-matrix',
];

interface ReplayEvent {
  sequence: number;
  receivedAt: number;
  action: 'REPLAY_COMPLETE' | 'REPLAY_PROGRESS';
  status?: string;
  progressType?: string;
  message?: string;
  result?: string;
  taskId?: string;
  senderTabId: number | null;
  senderUrl?: string;
}

interface ReplayStart {
  taskId: string;
  marker: number;
}

interface ReplaySessionSnapshot {
  tabId?: number;
  taskId?: string;
}

interface RetainedReplaySnapshot {
  tabId?: number;
  taskId?: string;
}

interface RuntimeState {
  storageKeys: string[];
  replayStorage: Record<string, unknown>;
  alarmNames: string[];
  tabs: Array<{
    id?: number;
    url?: string;
    pendingUrl?: string;
    status?: string;
  }>;
}

interface DeferredGate {
  arrived: Promise<void>;
  release: () => void;
}

interface InternalGate {
  arrived: Promise<void>;
  markArrived: () => void;
  released: Promise<void>;
  release: () => void;
}

interface FixtureServers {
  primaryOrigin: string;
  crossOrigin: string;
  armRestartGate: (token: string) => DeferredGate;
  close: () => Promise<void>;
}

interface ScenarioContext {
  context: BrowserContext;
  popup: Page;
  extensionId: string;
  fixtures: FixtureServers;
  seed: number;
  iteration: number;
  scenario: ReplayStabilityScenario;
  restartPhase?: ServiceWorkerRestartPhase;
  taskIds: string[];
  lifecycle: string[];
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function seededRandom(seed: number): () => number {
  let state = seed >>> 0;
  return () => {
    state += 0x6d2b79f5;
    let value = state;
    value = Math.imul(value ^ (value >>> 15), value | 1);
    value ^= value + Math.imul(value ^ (value >>> 7), value | 61);
    return ((value ^ (value >>> 14)) >>> 0) / 4_294_967_296;
  };
}

export function buildReplayStabilitySchedule(
  iterations: number,
  seed: number,
): ReplayStabilityScenario[] {
  if (!Number.isInteger(iterations) || iterations <= 0) {
    throw new Error(`iterations must be a positive integer, got ${iterations}`);
  }
  if (!Number.isInteger(seed) || seed < 0 || seed > 0xffff_ffff) {
    throw new Error(`seed must be an unsigned 32-bit integer, got ${seed}`);
  }
  const random = seededRandom(seed);
  const schedule: ReplayStabilityScenario[] = [];
  while (schedule.length < iterations) {
    const block = [...REPLAY_STABILITY_BLOCK];
    for (let index = block.length - 1; index > 0; index -= 1) {
      const swapIndex = Math.floor(random() * (index + 1));
      [block[index], block[swapIndex]] = [block[swapIndex], block[index]];
    }
    schedule.push(...block.slice(0, iterations - schedule.length));
  }
  return schedule;
}

export function serviceWorkerRestartPhase(
  occurrence: number,
): ServiceWorkerRestartPhase {
  if (!Number.isInteger(occurrence) || occurrence < 0) {
    throw new Error(`restart occurrence must be a non-negative integer, got ${occurrence}`);
  }
  return (['pre-navigation', 'checkpoint', 'post-navigation'] as const)[occurrence % 3];
}

function boundedJson(value: unknown): string {
  let serialized: string;
  try {
    serialized = JSON.stringify(value);
  } catch {
    serialized = JSON.stringify({ diagnosticError: 'diagnostics were not serializable' });
  }
  return serialized.length <= DIAGNOSTIC_BUDGET
    ? serialized
    : `${serialized.slice(0, DIAGNOSTIC_BUDGET)}...[truncated]`;
}

function htmlEscape(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function fixturePage(body: string): string {
  return `<!doctype html>
<html lang="en">
  <head><meta charset="utf-8"><title>Aegis MV3 replay fixture</title></head>
  <body>${body}</body>
</html>`;
}

async function listen(
  server: http.Server,
  host: string,
  advertisedHost = host,
): Promise<string> {
  await new Promise<void>((resolve, reject) => {
    const onError = (error: Error): void => reject(error);
    server.once('error', onError);
    server.listen(0, host, () => {
      server.off('error', onError);
      resolve();
    });
  });
  const address = server.address() as AddressInfo | null;
  assert(address && typeof address.port === 'number', `fixture server on ${host} did not expose a port`);
  return `http://${advertisedHost}:${address.port}`;
}

function closeServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) reject(error);
      else resolve();
    });
    server.closeAllConnections?.();
  });
}

async function startFixtureServers(): Promise<FixtureServers> {
  const restartGates = new Map<string, InternalGate>();
  let primaryOrigin = '';
  let crossOrigin = '';

  const primary = http.createServer((request, response) => {
    void (async () => {
      const requestUrl = new URL(request.url ?? '/', primaryOrigin || 'http://127.0.0.1');
      if (request.method === 'POST' && /^\/api\/v1\/dsl-workflows\/[^/]+\/confirm$/.test(requestUrl.pathname)) {
        response.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
        response.end(JSON.stringify({ ruleVersion: { ruleId: 'loopback-approved', version: 1 } }));
        return;
      }
      if (requestUrl.pathname === '/entry') {
        const entryToken = requestUrl.searchParams.get('token') ?? 'missing';
        const crossTarget = `${crossOrigin}/cross-target?token=${encodeURIComponent(entryToken)}`;
        const decoy = requestUrl.searchParams.get('navigation-decoy') === '1'
          ? `<p id="result">entry:${htmlEscape(entryToken)}</p>`
          : '';
        response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        response.end(fixturePage(`
          <main>
            <p id="entry">entry:${htmlEscape(entryToken)}</p>
            ${decoy}
            <button id="cross-nav" type="button">cross domain</button>
          </main>
          <script>
            document.getElementById('cross-nav').addEventListener('click', () => {
              location.href = ${JSON.stringify(crossTarget)};
            });
          </script>
        `));
        return;
      }
      if (requestUrl.pathname === '/result') {
        const resultToken = requestUrl.searchParams.get('token') ?? 'missing';
        const delay = Number(requestUrl.searchParams.get('delay') ?? '0');
        if (Number.isSafeInteger(delay) && delay > 0 && delay <= 5_000) {
          await sleep(delay);
        }
        response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        response.end(fixturePage(
          `<main style="display:contents"><p id="result">result:${htmlEscape(resultToken)}</p></main>`,
        ));
        return;
      }
      if (requestUrl.pathname === '/restart-target') {
        const restartToken = requestUrl.searchParams.get('token') ?? '';
        const gate = restartGates.get(restartToken);
        if (!gate) {
          response.writeHead(409, { 'Content-Type': 'text/plain; charset=utf-8' });
          response.end('restart gate was not armed');
          return;
        }
        gate.markArrived();
        await gate.released;
        restartGates.delete(restartToken);
        response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        response.end(fixturePage(`<p id="result">restart:${htmlEscape(restartToken)}</p>`));
        return;
      }
      response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
      response.end('not found');
    })().catch((error) => {
      if (!response.headersSent) response.writeHead(500, { 'Content-Type': 'text/plain; charset=utf-8' });
      response.end(error instanceof Error ? error.message : String(error));
    });
  });

  const cross = http.createServer((request, response) => {
    const requestUrl = new URL(request.url ?? '/', crossOrigin || 'http://localhost');
    if (requestUrl.pathname === '/cross-target') {
      const crossToken = requestUrl.searchParams.get('token') ?? 'missing';
      response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      response.end(fixturePage(`<p id="cross-result">cross:${htmlEscape(crossToken)}</p>`));
      return;
    }
    response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
    response.end('not found');
  });

  try {
    crossOrigin = await listen(cross, '127.0.0.1', 'localhost');
    primaryOrigin = await listen(primary, '127.0.0.1');
  } catch (error) {
    await Promise.allSettled([closeServer(primary), closeServer(cross)]);
    throw error;
  }

  return {
    primaryOrigin,
    crossOrigin,
    armRestartGate: (token: string) => {
      assert(!restartGates.has(token), `duplicate restart gate token ${token}`);
      const arrival = deferred();
      const release = deferred();
      restartGates.set(token, {
        arrived: arrival.promise,
        markArrived: arrival.resolve,
        released: release.promise,
        release: release.resolve,
      });
      return { arrived: arrival.promise, release: release.resolve };
    },
    close: async () => {
      for (const gate of restartGates.values()) gate.release();
      await Promise.all([closeServer(primary), closeServer(cross)]);
    },
  };
}

function replayRule(
  id: string,
  entry: string,
  steps: Array<Record<string, unknown>>,
): Record<string, unknown> {
  return {
    id,
    version: '1.0.0',
    name: `MV3 stability ${id}`,
    domain: '127.0.0.1',
    entry,
    variables: {},
    steps,
  };
}

function navigationRule(id: string, entry: string, target: string): Record<string, unknown> {
  return replayRule(id, entry, [
    { action: 'navigate', url: target },
    {
      action: 'waitForElementVisible',
      target: { selector: 'main' },
      timeout: 5_000,
    },
    { action: 'extractText', name: 'result', target: { selector: '#result', visible: true } },
    { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true },
  ]);
}

function immediateSuccessRule(id: string, entry: string): Record<string, unknown> {
  return replayRule(id, entry, [
    { action: 'extractText', name: 'entry', target: { selector: '#entry', visible: true } },
    { action: 'sendResult', payload: { entry: '{{extracted.entry}}' }, immediate: true },
  ]);
}

function longRunningRule(id: string, entry: string): Record<string, unknown> {
  return replayRule(id, entry, [
    {
      action: 'waitForElementVisible',
      target: { selector: '#released-by-test', visible: true },
      timeout: 60_000,
    },
  ]);
}

function events(popup: Page): Promise<ReplayEvent[]> {
  return popup.evaluate(() => {
    const current = (globalThis as any).__aegisMv3ReplayEvents;
    return Array.isArray(current) ? current : [];
  }) as Promise<ReplayEvent[]>;
}

async function eventMarker(popup: Page): Promise<number> {
  return (await events(popup)).at(-1)?.sequence ?? 0;
}

async function startReplay(
  popup: Page,
  rule: Record<string, unknown>,
  variables: Record<string, unknown> = {},
): Promise<ReplayStart> {
  const marker = await eventMarker(popup);
  const response = await popup.evaluate(async ({ replayRule, replayVariables }) => {
    return (globalThis as any).chrome.runtime.sendMessage({
      action: 'START_REPLAY',
      payload: { rule: replayRule, variables: replayVariables },
    });
  }, { replayRule: rule, replayVariables: variables }) as {
    success?: boolean;
    taskId?: string;
    error?: string;
  };
  assert(response.success === true && typeof response.taskId === 'string',
    `START_REPLAY failed: ${boundedJson(response)}`);
  return { marker, taskId: response.taskId };
}

async function waitFor(
  description: string,
  probe: () => Promise<boolean>,
  timeoutMs: number,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      if (await probe()) return;
    } catch (error) {
      lastError = error;
    }
    await sleep(25);
  }
  const suffix = lastError instanceof Error ? `: ${lastError.message}` : '';
  throw new Error(`${description} timed out after ${timeoutMs}ms${suffix}`);
}

async function waitForSession(popup: Page, taskId: string): Promise<ReplaySessionSnapshot> {
  let session: ReplaySessionSnapshot | undefined;
  await waitFor('active replay session', async () => {
    session = await popup.evaluate(async () => {
      const stored = await (globalThis as any).chrome.storage.session.get('oc_replay_session');
      return stored.oc_replay_session;
    }) as ReplaySessionSnapshot | undefined;
    return session?.taskId === taskId && Number.isInteger(session.tabId);
  }, 5_000);
  return session!;
}

async function retainedReplay(popup: Page, taskId: string): Promise<RetainedReplaySnapshot> {
  let retained: RetainedReplaySnapshot | undefined;
  await waitFor('retained replay tab', async () => {
    retained = await popup.evaluate(async () => {
      const stored = await (globalThis as any).chrome.storage.session.get('oc_replay_retained_tab');
      return stored.oc_replay_retained_tab;
    }) as RetainedReplaySnapshot | undefined;
    return retained?.taskId === taskId && Number.isInteger(retained.tabId);
  }, 5_000);
  return retained!;
}

async function injectFixtureMarker(popup: Page, tabId: number, markerId: string): Promise<void> {
  await popup.evaluate(async ({ targetTabId, id }) => {
    await (globalThis as any).chrome.scripting.executeScript({
      target: { tabId: targetTabId },
      func: (elementId: string) => {
        const marker = document.createElement('div');
        marker.id = elementId;
        marker.textContent = elementId;
        document.body.append(marker);
      },
      args: [id],
    });
  }, { targetTabId: tabId, id: markerId });
}

async function waitForTabPath(popup: Page, tabId: number, pathname: string): Promise<void> {
  await waitFor(`replay tab path ${pathname}`, async () => {
    return popup.evaluate(async ({ targetTabId, expectedPath }) => {
      try {
        const tab = await (globalThis as any).chrome.tabs.get(targetTabId);
        return typeof tab.url === 'string' && new URL(tab.url).pathname === expectedPath;
      } catch {
        return false;
      }
    }, { targetTabId: tabId, expectedPath: pathname });
  }, 5_000);
}

function acceptedEvents(allEvents: ReplayEvent[], marker: number): ReplayEvent[] {
  return allEvents.filter((event) =>
    event.sequence > marker
    && event.action === 'REPLAY_COMPLETE'
    && event.senderTabId === null);
}

async function waitForRunnerStarted(
  popup: Page,
  start: ReplayStart,
  tabId: number,
  minimumStarts = 1,
): Promise<void> {
  await waitFor(`replay runner start #${minimumStarts}`, async () => {
    const observed = await events(popup);
    const starts = observed.filter((event) =>
      event.sequence > start.marker
      && event.action === 'REPLAY_PROGRESS'
      && event.senderTabId === tabId
      && event.progressType === 'status'
      && event.status === 'running');
    return starts.length >= minimumStarts;
  }, RUNNER_START_TIMEOUT_MS);
}

async function expectOneTerminal(
  popup: Page,
  start: ReplayStart,
  expectedStatus: 'success' | 'failure' | 'cancelled',
): Promise<ReplayEvent> {
  await waitFor(`accepted ${expectedStatus} terminal`, async () => {
    return acceptedEvents(await events(popup), start.marker).length >= 1;
  }, TERMINAL_TIMEOUT_MS);
  await sleep(EVENT_QUIET_MS);
  const observed = acceptedEvents(await events(popup), start.marker);
  assert(observed.length === 1,
    `task ${start.taskId} produced ${observed.length} accepted terminal outcomes: ${boundedJson(observed)}`);
  assert(observed[0].status === expectedStatus,
    `task ${start.taskId} terminal status was ${String(observed[0].status)}, expected ${expectedStatus}`);
  return observed[0];
}

async function abortReplay(popup: Page): Promise<void> {
  const response = await popup.evaluate(async () =>
    (globalThis as any).chrome.runtime.sendMessage({ action: 'ABORT_REPLAY' })) as {
    success?: boolean;
    error?: string;
  };
  assert(response.success === true, `ABORT_REPLAY failed: ${boundedJson(response)}`);
}

async function approveReplay(popup: Page, origin: string, workflowId: string): Promise<void> {
  const configured = await popup.evaluate(async (baseUrl) =>
    (globalThis as any).chrome.runtime.sendMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl, rememberSession: false },
    }), origin) as { success?: boolean; error?: string };
  assert(configured.success === true, `loopback approval server configuration failed: ${boundedJson(configured)}`);

  const approved = await popup.evaluate(async (id) =>
    (globalThis as any).chrome.runtime.sendMessage({
      action: 'CONFIRM_DSL_WORKFLOW',
      payload: { workflowId: id },
    }), workflowId) as { success?: boolean; error?: string };
  assert(approved.success === true, `CONFIRM_DSL_WORKFLOW failed: ${boundedJson(approved)}`);
  await popup.evaluate(async () =>
    (globalThis as any).chrome.storage.session.remove('oc_dsl_workflow'));
}

async function tabExists(popup: Page, tabId: number): Promise<boolean> {
  return popup.evaluate(async (id) => {
    try {
      await (globalThis as any).chrome.tabs.get(id);
      return true;
    } catch {
      return false;
    }
  }, tabId);
}

async function runtimeState(popup: Page): Promise<RuntimeState> {
  return popup.evaluate(async () => {
    const chromeApi = (globalThis as any).chrome;
    const [storage, alarms, tabs] = await Promise.all([
      chromeApi.storage.session.get(null),
      chromeApi.alarms.getAll(),
      chromeApi.tabs.query({}),
    ]);
    const replayStorage = Object.fromEntries(
      Object.entries(storage).filter(([key]) =>
        key === 'oc_replay_session'
        || key === 'oc_replay_retained_tab'
        || key.startsWith('oc_replay_payload_')
        || key.startsWith('oc_replay_ctx_')),
    );
    return {
      storageKeys: Object.keys(storage).sort(),
      replayStorage,
      alarmNames: alarms.map((alarm: { name: string }) => alarm.name).sort(),
      tabs: tabs.map((tab: any) => ({
        id: tab.id,
        url: tab.url,
        pendingUrl: tab.pendingUrl,
        status: tab.status,
      })),
    };
  }) as Promise<RuntimeState>;
}

function isFixtureTab(
  tab: RuntimeState['tabs'][number],
  fixtures: FixtureServers,
): boolean {
  return [tab.url, tab.pendingUrl].some((candidate) =>
    typeof candidate === 'string'
    && (candidate.startsWith(`${fixtures.primaryOrigin}/`)
      || candidate.startsWith(`${fixtures.crossOrigin}/`)));
}

async function assertCleanReplayState(ctx: ScenarioContext): Promise<void> {
  let state: RuntimeState | undefined;
  await waitFor('replay cleanup', async () => {
    state = await runtimeState(ctx.popup);
    const replayAlarms = state.alarmNames.filter((name) =>
      name.startsWith('replay_total_') || name.startsWith('replay_idle_'));
    return Object.keys(state.replayStorage).length === 0
      && replayAlarms.length === 0
      && !state.tabs.some((tab) => isFixtureTab(tab, ctx.fixtures));
  }, CLEANUP_TIMEOUT_MS);

  const replayAlarms = state!.alarmNames.filter((name) =>
    name.startsWith('replay_total_') || name.startsWith('replay_idle_'));
  assert(Object.keys(state!.replayStorage).length === 0,
    `replay storage leaked: ${boundedJson(state!.replayStorage)}`);
  assert(replayAlarms.length === 0, `replay alarms leaked: ${boundedJson(replayAlarms)}`);
  assert(!state!.tabs.some((tab) => isFixtureTab(tab, ctx.fixtures)),
    `replay tabs leaked: ${boundedJson(state!.tabs.filter((tab) => isFixtureTab(tab, ctx.fixtures)))}`);
}

async function waitForServiceWorker(
  context: BrowserContext,
  extensionId?: string,
  timeoutMs = 15_000,
): Promise<Worker> {
  let worker: Worker | undefined;
  await waitFor('Manifest V3 extension service worker', async () => {
    worker = context.serviceWorkers().find((candidate) =>
      !extensionId || candidate.url() === `chrome-extension://${extensionId}/background.js`);
    return worker !== undefined;
  }, timeoutMs);
  return worker!;
}

async function forceServiceWorkerRestart(ctx: ScenarioContext): Promise<void> {
  const workerUrl = `chrome-extension://${ctx.extensionId}/background.js`;
  await waitForServiceWorker(ctx.context, ctx.extensionId);
  // The ServiceWorker domain is exposed on renderer-target sessions in the
  // Chromium build used by Playwright; the browser-target session rejects
  // ServiceWorker.enable as an unknown method.
  const cdp = await ctx.context.newCDPSession(ctx.popup);
  try {
    const versionReady = deferred();
    const versionStopped = deferred();
    const versionRestarted = deferred();
    let runningVersionId: string | undefined;
    let stopRequested = false;
    let sawStopped = false;
    const onVersionUpdate = (event: {
      versions?: Array<{
        versionId: string;
        scriptURL: string;
        runningStatus?: string;
      }>;
    }): void => {
      const matching = (event.versions ?? []).find((version) => version.scriptURL === workerUrl);
      if (!matching) return;
      if (matching.runningStatus === 'running') {
        runningVersionId = matching.versionId;
        versionReady.resolve();
        if (sawStopped) versionRestarted.resolve();
      }
      if (stopRequested && matching.runningStatus === 'stopped') {
        sawStopped = true;
        versionStopped.resolve();
      }
    };
    cdp.on('ServiceWorker.workerVersionUpdated', onVersionUpdate);
    await cdp.send('ServiceWorker.enable');
    await Promise.race([
      versionReady.promise,
      sleep(5_000).then(() => {
        throw new Error(`CDP did not report a running service-worker version for ${workerUrl}`);
      }),
    ]);
    assert(runningVersionId, 'CDP service-worker version lacked an ID');

    stopRequested = true;
    await cdp.send('ServiceWorker.stopWorker', { versionId: runningVersionId });
    await Promise.race([
      versionStopped.promise,
      sleep(10_000).then(() => {
        throw new Error('CDP service-worker version did not transition to stopped');
      }),
    ]);
    ctx.lifecycle.push(`worker-stopped:${runningVersionId.slice(0, 12)}`);

    const wake = ctx.popup.evaluate(async () =>
      (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_STATE' }));
    await Promise.all([
      wake,
      Promise.race([
        versionRestarted.promise,
        sleep(10_000).then(() => {
          throw new Error('CDP service-worker version did not restart after wake');
        }),
      ]),
    ]);
    ctx.lifecycle.push('worker-restarted');
    cdp.off('ServiceWorker.workerVersionUpdated', onVersionUpdate);
  } finally {
    await cdp.detach().catch(() => undefined);
  }
}

function tokenFor(ctx: ScenarioContext, suffix = ''): string {
  return `${ctx.seed}-${ctx.iteration}-${ctx.scenario}${suffix}`;
}

function entryUrl(ctx: ScenarioContext, suffix = ''): string {
  return `${ctx.fixtures.primaryOrigin}/entry?token=${encodeURIComponent(tokenFor(ctx, suffix))}`;
}

async function navigationReinjection(ctx: ScenarioContext): Promise<void> {
  const entry = `${entryUrl(ctx)}&navigation-decoy=1`;
  const token = tokenFor(ctx);
  const canonicalAliasOrigin = ctx.fixtures.primaryOrigin.replace('127.0.0.1', 'localhost');
  const target = `${canonicalAliasOrigin}/result?token={{token}}&delay=2500`;
  const rule = navigationRule(`nav-${token}`, entry, target);
  rule.domain = ['127.0.0.1', 'localhost'];
  rule.variables = { token: '' };
  const start = await startReplay(
    ctx.popup,
    rule,
    { token },
  );
  ctx.taskIds.push(start.taskId);
  await expectOneTerminal(ctx.popup, start, 'success');
  const results = (await events(ctx.popup))
    .filter((event) => event.sequence > start.marker && event.progressType === 'result')
    .map((event) => event.result);
  assert(results.includes(JSON.stringify({ result: `result:${token}` })),
    `variable-bound navigation extracted stale entry content: ${boundedJson(results)}`);
  await retainedReplay(ctx.popup, start.taskId);
  await abortReplay(ctx.popup);
}

async function serviceWorkerRestart(ctx: ScenarioContext): Promise<void> {
  const restartToken = tokenFor(ctx);
  const phase = ctx.restartPhase;
  assert(phase, 'service-worker restart scenario did not declare a lifecycle phase');
  const entry = entryUrl(ctx);
  const normalTarget = `${ctx.fixtures.primaryOrigin}/result?token=${encodeURIComponent(restartToken)}`;
  const checkpointTarget = `${ctx.fixtures.primaryOrigin}/restart-target?token=${encodeURIComponent(restartToken)}`;
  const target = phase === 'checkpoint' ? checkpointTarget : normalTarget;
  const phaseSteps: Array<Record<string, unknown>> = [];
  if (phase === 'pre-navigation') {
    phaseSteps.push({
      action: 'waitForElementVisible',
      target: { selector: '#pre-navigation-release', visible: true },
      timeout: 10_000,
    });
  }
  phaseSteps.push(
    { action: 'navigate', url: target },
    ...(phase === 'post-navigation'
      ? [{
          action: 'waitForElementVisible',
          target: { selector: '#post-navigation-release', visible: true },
          timeout: 10_000,
        }]
      : []),
    {
      action: 'waitForElementVisible',
      target: { selector: '#result', visible: true },
      timeout: 5_000,
    },
    { action: 'extractText', name: 'result', target: { selector: '#result', visible: true } },
    { action: 'sendResult', payload: { result: '{{extracted.result}}' }, immediate: true },
  );
  const checkpointGate = phase === 'checkpoint'
    ? ctx.fixtures.armRestartGate(restartToken)
    : undefined;
  const start = await startReplay(
    ctx.popup,
    replayRule(`restart-${phase}-${restartToken}`, entry, phaseSteps),
  );
  ctx.taskIds.push(start.taskId);

  if (phase === 'pre-navigation') {
    const session = await waitForSession(ctx.popup, start.taskId);
    assert(Number.isInteger(session.tabId), 'pre-navigation restart replay had no tab ID');
    await waitForRunnerStarted(ctx.popup, start, session.tabId!);
    ctx.lifecycle.push('pre-navigation-paused');
    await forceServiceWorkerRestart(ctx);
    await injectFixtureMarker(ctx.popup, session.tabId!, 'pre-navigation-release');
    ctx.lifecycle.push('pre-navigation-released');
  } else if (phase === 'checkpoint') {
    assert(checkpointGate, 'checkpoint restart gate was not armed');
    await Promise.race([
      checkpointGate.arrived,
      sleep(TERMINAL_TIMEOUT_MS).then(() => {
        throw new Error('restart-target navigation did not reach the fixture');
      }),
    ]);
    ctx.lifecycle.push('checkpoint-navigation-paused');
    try {
      await forceServiceWorkerRestart(ctx);
    } finally {
      checkpointGate.release();
    }
    ctx.lifecycle.push('checkpoint-navigation-released');
  } else {
    const session = await waitForSession(ctx.popup, start.taskId);
    assert(Number.isInteger(session.tabId), 'post-navigation restart replay had no tab ID');
    await waitForTabPath(ctx.popup, session.tabId!, '/result');
    await waitForRunnerStarted(ctx.popup, start, session.tabId!, 2);
    ctx.lifecycle.push('post-navigation-paused');
    await forceServiceWorkerRestart(ctx);
    await injectFixtureMarker(ctx.popup, session.tabId!, 'post-navigation-release');
    ctx.lifecycle.push('post-navigation-released');
  }

  await expectOneTerminal(ctx.popup, start, 'success');
  await retainedReplay(ctx.popup, start.taskId);
  await abortReplay(ctx.popup);
}

async function duplicateTerminal(ctx: ScenarioContext): Promise<void> {
  const entry = entryUrl(ctx);
  const start = await startReplay(ctx.popup, longRunningRule(`duplicate-${tokenFor(ctx)}`, entry));
  ctx.taskIds.push(start.taskId);
  const session = await waitForSession(ctx.popup, start.taskId);
  assert(Number.isInteger(session.tabId), 'active duplicate-terminal replay had no tab ID');
  await ctx.popup.evaluate(async ({ tabId, taskId }) => {
    try {
      await (globalThis as any).chrome.scripting.executeScript({
        target: { tabId },
        func: async (id: string) => {
          const chromeApi = (globalThis as any).chrome;
          await Promise.all([
            chromeApi.runtime.sendMessage({
              action: 'REPLAY_COMPLETE',
              payload: { status: 'success', message: 'seeded duplicate A', taskId: id },
            }),
            chromeApi.runtime.sendMessage({
              action: 'REPLAY_COMPLETE',
              payload: { status: 'success', message: 'seeded duplicate B', taskId: id },
            }),
          ]);
        },
        args: [taskId],
      });
    } catch {
      // The first terminal can close or supersede the execution realm while
      // executeScript is resolving. Terminal accounting below is authoritative.
    }
  }, { tabId: session.tabId!, taskId: start.taskId });
  await expectOneTerminal(ctx.popup, start, 'success');
  const observed = (await events(ctx.popup)).filter((event) => event.sequence > start.marker);
  const rawForTask = observed.filter((event) =>
    event.action === 'REPLAY_COMPLETE'
    && event.senderTabId === session.tabId
    && event.taskId === start.taskId);
  assert(rawForTask.length >= 2,
    `duplicate-terminal scenario emitted only ${rawForTask.length} tab-origin terminal message(s)`);
  await abortReplay(ctx.popup);
}

async function retainedTabSupersession(ctx: ScenarioContext): Promise<void> {
  const first = await startReplay(
    ctx.popup,
    immediateSuccessRule(`retained-first-${tokenFor(ctx)}`, entryUrl(ctx, '-first')),
  );
  ctx.taskIds.push(first.taskId);
  await expectOneTerminal(ctx.popup, first, 'success');
  const retained = await retainedReplay(ctx.popup, first.taskId);
  assert(Number.isInteger(retained.tabId), 'first retained replay had no tab ID');

  const second = await startReplay(
    ctx.popup,
    immediateSuccessRule(`retained-second-${tokenFor(ctx)}`, entryUrl(ctx, '-second')),
  );
  ctx.taskIds.push(second.taskId);
  await waitFor('superseded retained tab closure', async () => !(await tabExists(ctx.popup, retained.tabId!)), 5_000);
  await expectOneTerminal(ctx.popup, second, 'success');
  await retainedReplay(ctx.popup, second.taskId);
  await abortReplay(ctx.popup);
}

async function approvalCleanup(ctx: ScenarioContext): Promise<void> {
  const start = await startReplay(
    ctx.popup,
    immediateSuccessRule(`approval-${tokenFor(ctx)}`, entryUrl(ctx)),
  );
  ctx.taskIds.push(start.taskId);
  await expectOneTerminal(ctx.popup, start, 'success');
  await retainedReplay(ctx.popup, start.taskId);
  await approveReplay(ctx.popup, ctx.fixtures.primaryOrigin, `workflow-${tokenFor(ctx)}`);
}

async function retainedAbortCleanup(ctx: ScenarioContext): Promise<void> {
  const start = await startReplay(
    ctx.popup,
    immediateSuccessRule(`retained-abort-${tokenFor(ctx)}`, entryUrl(ctx, '-retained-abort')),
  );
  ctx.taskIds.push(start.taskId);
  await expectOneTerminal(ctx.popup, start, 'success');
  await retainedReplay(ctx.popup, start.taskId);
  await abortReplay(ctx.popup);
}

async function tabRemoval(ctx: ScenarioContext): Promise<void> {
  const start = await startReplay(ctx.popup, longRunningRule(`remove-${tokenFor(ctx)}`, entryUrl(ctx)));
  ctx.taskIds.push(start.taskId);
  const session = await waitForSession(ctx.popup, start.taskId);
  assert(Number.isInteger(session.tabId), 'active tab-removal replay had no tab ID');
  await ctx.popup.evaluate(async (tabId) =>
    (globalThis as any).chrome.tabs.remove(tabId), session.tabId!);
  await expectOneTerminal(ctx.popup, start, 'failure');
}

async function ruleCancelled(ctx: ScenarioContext): Promise<void> {
  const rule = replayRule(`cancel-${tokenFor(ctx)}`, entryUrl(ctx), [
    { action: 'exit', status: 'cancelled', message: 'seeded rule cancellation' },
  ]);
  const start = await startReplay(ctx.popup, rule);
  ctx.taskIds.push(start.taskId);
  await expectOneTerminal(ctx.popup, start, 'cancelled');
}

async function ruleFailure(ctx: ScenarioContext): Promise<void> {
  const rule = replayRule(`failure-${tokenFor(ctx)}`, entryUrl(ctx), [
    {
      action: 'click',
      target: { selector: '#missing-for-seeded-failure', visible: true },
      timeout: 100,
    },
  ]);
  const start = await startReplay(ctx.popup, rule);
  ctx.taskIds.push(start.taskId);
  await expectOneTerminal(ctx.popup, start, 'failure');
}

async function crossDomainRejection(ctx: ScenarioContext): Promise<void> {
  const rule = replayRule(`cross-domain-${tokenFor(ctx)}`, entryUrl(ctx), [
    { action: 'click', target: { selector: '#cross-nav', visible: true } },
    {
      action: 'waitForElementVisible',
      target: { selector: '#cross-result', visible: true },
      timeout: 5_000,
    },
  ]);
  const start = await startReplay(ctx.popup, rule);
  ctx.taskIds.push(start.taskId);
  const terminal = await expectOneTerminal(ctx.popup, start, 'failure');
  assert(terminal.message?.includes('post-navigation domain mismatch'),
    `cross-domain replay failed for an unexpected reason: ${String(terminal.message)}`);
}

async function approvalAbortCleanup(ctx: ScenarioContext): Promise<void> {
  await approvalCleanup(ctx);
  await assertCleanReplayState(ctx);
  await retainedAbortCleanup(ctx);
}

async function terminalCleanupMatrix(ctx: ScenarioContext): Promise<void> {
  await tabRemoval(ctx);
  await assertCleanReplayState(ctx);
  await ruleCancelled(ctx);
  await assertCleanReplayState(ctx);
  await ruleFailure(ctx);
  await assertCleanReplayState(ctx);
  await crossDomainRejection(ctx);
}

const SCENARIO_RUNNERS: Record<
  ReplayStabilityScenario,
  (ctx: ScenarioContext) => Promise<void>
> = {
  'navigation-reinjection': navigationReinjection,
  'service-worker-restart': serviceWorkerRestart,
  'duplicate-terminal': duplicateTerminal,
  'retained-tab-supersession': retainedTabSupersession,
  'approval-abort-cleanup': approvalAbortCleanup,
  'terminal-cleanup-matrix': terminalCleanupMatrix,
};

async function installReplayObserver(popup: Page): Promise<void> {
  await popup.evaluate(() => {
    const target = globalThis as any;
    target.__aegisMv3ReplayEvents = [];
    target.__aegisMv3ReplaySequence = 0;
    (globalThis as any).chrome.runtime.onMessage.addListener((message: any, sender: any) => {
      if (message?.action !== 'REPLAY_COMPLETE' && message?.action !== 'REPLAY_PROGRESS') return;
      target.__aegisMv3ReplaySequence += 1;
      target.__aegisMv3ReplayEvents.push({
        sequence: target.__aegisMv3ReplaySequence,
        receivedAt: Date.now(),
        action: message.action,
        status: message.payload?.status,
        progressType: message.payload?.type,
        message: typeof message.payload?.message === 'string'
          ? message.payload.message.slice(0, 300)
          : undefined,
        result: message.payload?.type === 'result'
          ? JSON.stringify(message.payload.payload).slice(0, 300)
          : undefined,
        taskId: message.payload?.taskId,
        senderTabId: Number.isInteger(sender?.tab?.id) ? sender.tab.id : null,
        senderUrl: typeof sender?.url === 'string' ? sender.url.slice(0, 300) : undefined,
      });
      if (target.__aegisMv3ReplayEvents.length > 400) {
        target.__aegisMv3ReplayEvents.splice(0, target.__aegisMv3ReplayEvents.length - 400);
      }
    });
  });
}

function parseUnsignedInteger(raw: string | undefined, fallback: number, label: string): number {
  if (raw === undefined || raw === '') return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 0 || value > 0xffff_ffff) {
    throw new Error(`${label} must be an unsigned 32-bit integer, got ${raw}`);
  }
  return value;
}

function parsePositiveInteger(raw: string | undefined, fallback: number, label: string): number {
  if (raw === undefined || raw === '') return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value <= 0 || value > 1_000) {
    throw new Error(`${label} must be an integer from 1 to 1000, got ${raw}`);
  }
  return value;
}

function parseRestartPhaseOverride(raw: string | undefined): ServiceWorkerRestartPhase | undefined {
  if (raw === undefined || raw === '') return undefined;
  if (
    raw === 'pre-navigation'
    || raw === 'checkpoint'
    || raw === 'post-navigation'
  ) {
    return raw;
  }
  throw new Error(
    'AEGIS_REPLAY_STABILITY_RESTART_PHASE must be pre-navigation, checkpoint, or post-navigation',
  );
}

function qualificationSourceArchiveReference(raw: string | undefined): {
  scenario: string;
  archiveId: string;
} | undefined {
  if (raw === undefined || raw === '') return undefined;
  const parts = raw.split('/');
  if (parts.length !== 2 || parts.some((part) => part.length === 0)) {
    throw new Error(
      `${QUALIFICATION_SOURCE_ARCHIVE_ENV} must be scenario/archiveId`,
    );
  }
  return { scenario: parts[0], archiveId: parts[1] };
}

function persistMV3StabilityEvidence(
  projectRoot: string,
  reference: { scenario: string; archiveId: string } | undefined,
  facts: Parameters<typeof buildMV3StabilityRunRecord>[1],
): void {
  if (reference === undefined) return;
  try {
    const archive = loadQualificationArchive(projectRoot, reference.scenario, reference.archiveId);
    const record = createQualificationRunRecord(
      projectRoot,
      buildMV3StabilityRunRecord(archive, facts),
    );
    const file = saveQualificationRunRecord(projectRoot, record);
    console.log(`[mv3-replay] evidence=${file} authority=${record.authority}`);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.warn(`[mv3-replay] qualification evidence was not persisted: ${message}`);
  }
}

async function main(): Promise<void> {
  const qualification = process.argv.includes('--qualification');
  const defaultIterations = qualification ? QUALIFICATION_ITERATIONS : ROUTINE_ITERATIONS;
  const iterations = parsePositiveInteger(
    process.env.AEGIS_REPLAY_STABILITY_ITERATIONS,
    defaultIterations,
    'AEGIS_REPLAY_STABILITY_ITERATIONS',
  );
  const seed = parseUnsignedInteger(
    process.env.AEGIS_REPLAY_STABILITY_SEED,
    DEFAULT_SEED,
    'AEGIS_REPLAY_STABILITY_SEED',
  );
  const headed = process.env.AEGIS_REPLAY_STABILITY_HEADED === '1';
  const restartPhaseOverride = parseRestartPhaseOverride(
    process.env.AEGIS_REPLAY_STABILITY_RESTART_PHASE,
  );
  const sourceArchive = qualificationSourceArchiveReference(
    process.env[QUALIFICATION_SOURCE_ARCHIVE_ENV],
  );
  const projectRoot = path.resolve(__dirname, '..');
  const runStartedAt = new Date().toISOString();
  const schedule = buildReplayStabilitySchedule(iterations, seed);
  const extensionDir = path.resolve(__dirname, '..', 'dist', 'extension');
  assert(fs.existsSync(path.join(extensionDir, 'manifest.json')),
    `built extension missing at ${extensionDir}; run npm run build:extension first`);

  const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-mv3-replay-'));
  const fixtures = await startFixtureServers();
  let context: BrowserContext | undefined;
  let activeScenario: ScenarioContext | undefined;

  console.log(`[mv3-replay] seed=${seed} iterations=${iterations} mode=${qualification ? 'qualification' : 'routine'}`);
  console.log(`[mv3-replay] loopback=${fixtures.primaryOrigin},${fixtures.crossOrigin}`);

  try {
    context = await chromium.launchPersistentContext(userDataDir, {
      headless: false,
      args: [
        ...extensionChromiumArgs(extensionDir, !headed),
        '--disable-background-networking',
        '--no-default-browser-check',
        '--no-first-run',
      ],
    });
    const worker = await waitForServiceWorker(context);
    const extensionId = await worker.evaluate(() => (globalThis as any).chrome.runtime.id) as string;
    assert(extensionId, 'extension service worker did not expose chrome.runtime.id');

    const popup = await context.newPage();
    await popup.goto(`chrome-extension://${extensionId}/popup.html`);
    await installReplayObserver(popup);

    let restartOccurrence = 0;
    let tasksExecuted = 0;
    for (let index = 0; index < schedule.length; index += 1) {
      const scenario = schedule[index];
      const restartPhase = scenario === 'service-worker-restart'
        ? restartPhaseOverride ?? serviceWorkerRestartPhase(restartOccurrence++)
        : undefined;
      activeScenario = {
        context,
        popup,
        extensionId,
        fixtures,
        seed,
        iteration: index + 1,
        scenario,
        restartPhase,
        taskIds: [],
        lifecycle: [],
      };
      const startedAt = Date.now();
      try {
        await SCENARIO_RUNNERS[scenario](activeScenario);
        await assertCleanReplayState(activeScenario);
      } catch (error) {
        const state = await runtimeState(popup).catch(() => undefined);
        const recentEvents = (await events(popup).catch(() => [])).slice(-12);
        const diagnostics = {
          seed,
          iteration: index + 1,
          scenario,
          taskIds: activeScenario.taskIds,
          lifecycle: activeScenario.lifecycle,
          serviceWorkers: context.serviceWorkers().map((candidate) => candidate.url()),
          replayStorage: state?.replayStorage,
          replayAlarms: state?.alarmNames.filter((name) => name.startsWith('replay_')),
          fixtureTabs: state?.tabs.filter((tab) => isFixtureTab(tab, fixtures)),
          recentEvents,
        };
        const message = error instanceof Error ? error.message : String(error);
        throw new Error(`${message}\n[mv3-replay] diagnostics=${boundedJson(diagnostics)}`);
      }
      console.log(
        `[mv3-replay] ${index + 1}/${iterations} ${scenario}`
        + `${restartPhase ? `:${restartPhase}` : ''} ok `
        + `tasks=${activeScenario.taskIds.length} elapsedMs=${Date.now() - startedAt}`,
      );
      tasksExecuted += activeScenario.taskIds.length;
      activeScenario = undefined;
    }
    if (sourceArchive !== undefined) {
      try {
        const gitState = readQualificationGitState(projectRoot);
        persistMV3StabilityEvidence(projectRoot, sourceArchive, {
          runId: crypto.randomUUID(),
          scenario: sourceArchive.scenario,
          iterations,
          passed: true,
          routineIterations: qualification ? undefined : iterations,
          qualificationIterations: qualification ? iterations : undefined,
          tasksExecuted,
          startedAt: runStartedAt,
          finishedAt: new Date().toISOString(),
          gitCommit: gitState.commit,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        console.warn(`[mv3-replay] qualification evidence was not persisted: ${message}`);
      }
    }
    console.log(`[mv3-replay] PASS seed=${seed} iterations=${iterations}`);
  } finally {
    if (activeScenario && context) {
      await activeScenario.popup.evaluate(async () => {
        try {
          await (globalThis as any).chrome.runtime.sendMessage({ action: 'ABORT_REPLAY' });
        } catch {
          // Browser shutdown below is the final containment boundary.
        }
      }).catch(() => undefined);
    }
    await context?.close().catch(() => undefined);
    await fixtures.close().catch(() => undefined);
    fs.rmSync(userDataDir, { recursive: true, force: true });
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.stack ?? error.message : String(error));
    process.exitCode = 1;
  });
}
