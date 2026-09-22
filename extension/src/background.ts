import { convert, convertToYaml, writeYaml, enhanceWithIntent } from '../../src/rule-generator';
import type { PageAgentRecording, PageMark, Rule, IntentCandidate } from '../../src/rule-generator';
import { preprocess } from './recording/preprocessor';
import { aggregateFrameDom } from './recording/frame-aggregator';
import type { AggregateOptions } from './recording/frame-aggregator';
import { createRecordingStore } from './recording/recording-store';
import { isCollectionRequirementSpec } from './intent/intent-types';

const recordingStore = createRecordingStore();

type RecordingState = 'idle' | 'recording';

interface ServerConfig {
  baseUrl: string;
}

interface ServerKeys {
  apiKey?: string;
  adminApiKey?: string;
}

interface RecordingCapabilities {
  features?: { recordingV2?: boolean; workflowV2?: boolean; pageMarks?: boolean };
  recordingLimits?: {
    maxActions?: number;
    maxDurationMs?: number;
    maxCompressedBytes?: number;
  };
}

interface RequirementJob {
  id: string;
  recordingId: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  source: 'llm' | 'manual';
  requirementId?: string;
  errorCode?: string;
  errorMessage?: string;
  chunkCount?: number;
  completedChunks?: number;
  result?: { candidates?: unknown[]; requirement?: unknown };
}

type ResumableRequirementJob = Pick<RequirementJob, 'id' | 'status' | 'chunkCount' | 'completedChunks'>;

interface RequirementJobResponse {
  job?: RequirementJob;
  statusUrl?: string;
}

interface CollectionRequirementResponse {
  requirement?: {
    id?: string;
    recordingId?: string;
    status?: string;
    requirement?: unknown;
  };
}

interface DSLWorkflow {
  id: string;
  requirementId: string;
  recordingId: string;
  status: 'generating' | 'awaiting_replay' | 'replaying' | 'repairing' | 'awaiting_confirmation' | 'failed' | 'approved';
  browserProfileId: string;
  currentJobId?: string;
  repairCount: number;
  maxRepairs: number;
  provisionalRule?: Rule;
  provisionalYaml?: string;
  errorCode?: string;
  errorMessage?: string;
  approvedRuleId?: string;
  approvedVersion?: number;
}

interface DSLJob {
  id: string;
  workflowId: string;
  kind: 'generate' | 'repair';
  status: 'pending' | 'running' | 'completed' | 'failed';
  errorCode?: string;
  errorMessage?: string;
  chunkCount?: number;
  completedChunks?: number;
}

interface DSLJobResponse {
  job?: DSLJob;
  statusUrl?: string;
}

interface DSLReplayAttempt {
  id: string;
  workflowId: string;
  sequence: number;
  status: 'running' | 'succeeded' | 'failed';
  outputValid: boolean;
  errorCode?: string;
  errorMessage?: string;
}

interface DSLWorkflowResponse {
  workflow?: DSLWorkflow;
  job?: DSLJob;
}

interface DSLReplayResponse {
  replay?: DSLReplayAttempt;
  repairJob?: DSLJob;
}

interface ActiveRecordingSession {
  tabId: number;
  startedAt: number;
  options: Record<string, unknown>;
  selectorToIndex?: Array<[string, { index: number; lastUsedAt: number }]>;
  nextIndex?: number;
  lastRecordedUrl?: string | null;
  statusMessage?: string;
}

export class ServerRequestError extends Error {
  readonly code: string;
  readonly status: number;
  readonly blockingFlags?: string[];
  constructor(message: string, code: string, status: number, blockingFlags?: string[]) {
    super(message);
    this.name = 'ServerRequestError';
    this.code = code;
    this.status = status;
    this.blockingFlags = blockingFlags;
  }
}

let recordingState: RecordingState = 'idle';
let serverConfig: ServerConfig = { baseUrl: '' };
let serverKeys: ServerKeys = {};
let activeRecording: ActiveRecordingSession | null = null;
let lastCapabilities: RecordingCapabilities | null = null;
const RECORDING_SESSION_KEY = 'oc_recording_session';
// D-3: disk-backed checkpoint key per tab. The RECORDING_CHECKPOINT handler
// writes here as defense-in-depth alongside IndexedDB + .session so a single
// storage layer failing mid-write (or SW eviction losing .session) cannot
// lose the recording. Cleared on tab close, START_RECORDING, and final persist.
function recordingCheckpointKey(tabId: number): string {
  return `oc_recording_chk_${tabId}`;
}
const REQUIREMENT_CANDIDATE_JOB_KEY = 'oc_requirement_candidate_job';
const REQUIREMENT_NORMALIZE_JOB_KEY = 'oc_requirement_normalize_job';
const DSL_WORKFLOW_SESSION_KEY = 'oc_dsl_workflow';

interface ReplaySession {
  tabId: number;
  ruleId: string;
  taskId: string; // UUID v4 — onAlarm / storage authorization boundary (SEC-002)
  startedAt: number;
  hopCount: number;
  lastProgressAt: number;
  expectedDomains: string[]; // SEC-005: every centrally approved replay hostname
  /** Legacy persisted sessions created before expectedDomains was introduced. */
  expectedDomain?: string;
  // Listener refs are NOT serialized — persistReplaySession's allowlist omits
  // them. SW restart re-binds closures via registerReplayListeners.
  onTabUpdated?: (tabId: number, info: ChromeTabChangeInfo, tab?: ChromeTab) => void;
  onTabRemoved?: (tabId: number) => void;
}

type ReplayRule = Omit<Rule, 'entry'> & { entry?: { url: string } | string };

let activeReplay: ReplaySession | null = null;
// WI-6: tracks whether the intent-wizard page is connected. The fallback in
// cleanupReplaySession uses this to decide whether to eager-complete the DSL
// replay directly with the server. Also a prerequisite for WI-12 (KeepAlive
// reconnect) in a future batch.
let currentWizardPort: ChromeRuntimePort | null = null;
// Phase 2 watchdog thresholds (§6). chrome.alarms survive SW eviction unlike
// the Phase 1 transitional setTimeout.
const REPLAY_TOTAL_TIMEOUT_MS = 30 * 60 * 1000;
const REPLAY_IDLE_TIMEOUT_MS = 120_000;
const REPLAY_ALARM_MIN_INTERVAL_MIN = 0.5; // MV3 minimum alarm interval
const REPLAY_HOP_LIMIT = 50;
const TAB_LOAD_TIMEOUT_MS = 30000;
const FETCH_TIMEOUT_MS = 15000;
const LLM_FETCH_TIMEOUT_MS = 180000;
const LLM_PROGRESS_STALL_TIMEOUT_MS = 600000;
const RECORDING_UPLOAD_TIMEOUT_MS = 60000;
const LOG_FETCH_TIMEOUT_MS = 5000;
const REQUIREMENT_POLL_INTERVAL_MS = 750;

export interface ProgressDeadline {
  expired: () => boolean;
  observe: (completed: number) => void;
}

// A progress-aware wait deadline for durable LLM jobs: forward progress
// (including the first "analysis started" report at 0 completed chunks)
// extends the wait by a full stall window, so the wait only expires after a
// whole stall window with no observed progress. Jobs that never report chunk
// progress keep the original fixed base timeout.
export function createProgressDeadline(
  baseTimeoutMs: number,
  stallTimeoutMs: number,
  maxTotalMs: number = 30 * 60 * 1000,
): ProgressDeadline {
  // M-3: absolute ceiling. Even with continuous forward progress, the
  // deadline cannot extend past startedAt + maxTotalMs. Stops a buggy or
  // malicious server from keeping the poll loop alive indefinitely by
  // incrementing completedChunks every <stallTimeoutMs.
  const startedAt = Date.now();
  const absoluteDeadline = startedAt + maxTotalMs;
  let deadline = Math.min(startedAt + baseTimeoutMs, absoluteDeadline);
  let lastCompleted = -1;
  return {
    expired: () => Date.now() >= deadline,
    observe: (completed: number) => {
      if (completed > lastCompleted) {
        lastCompleted = completed;
        deadline = Math.min(Date.now() + stallTimeoutMs, absoluteDeadline);
      }
    },
  };
}

const REPLAY_SESSION_KEY = 'oc_replay_session';
const RETAINED_REPLAY_TAB_KEY = 'oc_replay_retained_tab';
// M-4: TTL for retained replay tabs. A successful replay's tab is kept for
// the wizard to preview before confirming; if the wizard is closed without
// confirm/abort, recovery releases the tab after this window.
const RETAINED_TAB_TTL_MS = 30 * 60 * 1000;
const replayPayloadKey = (taskId: string) => `oc_replay_payload_${taskId}`;
// Collected sendResult rows per replay task, used by the WI-6 no-wizard
// fallback completion. Same lifetime as the service worker / active session.
const replayResultRows = new Map<string, unknown[]>();
const replayContextKey = (taskId: string) => `oc_replay_ctx_${taskId}`;

// Actions that mutate extension/server state and must only be accepted from
// trusted extension pages (popup, intent wizard), never from content scripts
// running in arbitrary web pages. Content scripts legitimately send only
// AGGREGATE_DOM plus the REPLAY_* events (handled before this guard).
const PRIVILEGED_ACTIONS = new Set([
  'GET_STATE',
  'START_RECORDING',
  'ENSURE_RECORDING_READY',
  'STOP_RECORDING',
  'GET_LAST_RECORDING',
  'GENERATE_RULE',
  'DOWNLOAD_RULE',
  'PREDICT_INTENT',
      'GET_REQUIREMENT_WORKFLOW',
      'UPDATE_RECORDING_MARKS',
      'NORMALIZE_REQUIREMENT',
  'RETRY_REQUIREMENT_JOB',
  'CONFIRM_REQUIREMENT',
  'CREATE_DSL_WORKFLOW',
  'GET_DSL_WORKFLOW_BASELINE',
  'CORRECT_DSL_WORKFLOW',
  'RESUME_DSL_WORKFLOW',
  'START_DSL_REPLAY',
  'COMPLETE_DSL_REPLAY',
  'CONFIRM_DSL_WORKFLOW',
  'GENERATE_DSL_FROM_INTENT',
  'GENERATE_DSL_FROM_INTENT_SERVER',
  'UPLOAD_CONFIRMED_RULE',
  'ENHANCE_RULE',
  'SET_SERVER_CONFIG',
  'START_REPLAY',
  'ABORT_REPLAY',
]);

function isFromContentScript(sender: ChromeRuntimeMessageSender): boolean {
  if (sender.tab == null) {
    return false;
  }
  const extensionRoot = chrome.runtime.getURL('');
  const senderUrls = [sender.url, sender.origin ? `${sender.origin}/` : undefined, sender.tab.url];
  return !senderUrls.some((url) => url?.startsWith(extensionRoot));
}

function fetchWithTimeout(url: string, options: RequestInit = {}, timeoutMs = FETCH_TIMEOUT_MS): Promise<Response> {
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), timeoutMs);

  return fetch(url, { ...options, signal: controller.signal }).finally(() => {
    clearTimeout(timeoutId);
  });
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError';
}

function generateTaskId(): string {
  // Cryptographically random — chrome.alarms.onAlarm / storage.session key
  // matching is the authorization boundary for session teardown; predictable
  // IDs would let a live session be torn down by guessing (SEC-002).
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `replay-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function broadcastReplayProgress(payload: unknown): void {
  chrome.runtime.sendMessage({ action: 'REPLAY_PROGRESS', payload }).catch(() => undefined);
}

function broadcastReplayComplete(payload: unknown): void {
  chrome.runtime.sendMessage({ action: 'REPLAY_COMPLETE', payload }).catch(() => undefined);
}

function broadcastLLMJobProgress(job: { chunkCount?: number; completedChunks?: number }): void {
  if (!job.chunkCount || job.chunkCount <= 0) return;
  chrome.runtime.sendMessage({
    action: 'LLM_JOB_PROGRESS',
    payload: { chunkCount: job.chunkCount, completedChunks: job.completedChunks ?? 0 },
  }).catch(() => undefined);
}

function normalizeBaseUrl(url: string): string {
  return url.replace(/\/+$/, '');
}

// Validate that a URL is http/https. Used at trust boundaries (user-configured
// server baseUrl, replay entry URL) to prevent redirecting API calls — which
// carry Bearer tokens — to file://, data:, javascript:, or attacker hosts.
function assertHttpUrl(url: string, label: string): void {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    throw new Error(`${label}不是合法 URL`);
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error(`${label}必须是 http/https 地址`);
  }
}

// Build the common admin-request headers (Content-Type, X-Trace-Id, optional
// Bearer authorization). Shared by every admin endpoint handler.
function buildAdminHeaders(traceId: string): Record<string, string> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'X-Trace-Id': traceId,
  };
  if (serverKeys.adminApiKey) {
    headers['Authorization'] = `Bearer ${serverKeys.adminApiKey}`;
  }
  return headers;
}

function generateTraceId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `trace-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

async function loadActiveRecording(): Promise<ActiveRecordingSession | null> {
  if (activeRecording) return activeRecording;
  const stored = await chrome.storage.session.get(RECORDING_SESSION_KEY);
  activeRecording = (stored[RECORDING_SESSION_KEY] as ActiveRecordingSession | undefined) ?? null;
  if (activeRecording) recordingState = 'recording';
  return activeRecording;
}

async function saveActiveRecording(session: ActiveRecordingSession | null): Promise<void> {
  activeRecording = session;
  recordingState = session ? 'recording' : 'idle';
  if (session) await chrome.storage.session.set({ [RECORDING_SESSION_KEY]: session });
  else await chrome.storage.session.remove(RECORDING_SESSION_KEY);
}

async function resolveRecordingOptions(payload: unknown): Promise<Record<string, unknown>> {
  const requested = payload && typeof payload === 'object' ? { ...(payload as Record<string, unknown>) } : {};
  if (!serverConfig.baseUrl) return requested;
  try {
    const response = await fetchWithTimeout(
      `${normalizeBaseUrl(serverConfig.baseUrl)}/api/v1/capabilities`,
      {},
      5000,
    );
    if (!response.ok) return requested;
    const capabilities = await response.json() as RecordingCapabilities;
    lastCapabilities = capabilities;
    if (!capabilities.features?.recordingV2) return requested;
    const limits = capabilities.recordingLimits ?? {};
    return {
      ...requested,
      protocolVersion: '2.0.0',
      maxEvents: limits.maxActions ?? 500,
      maxDurationMs: limits.maxDurationMs ?? 2 * 60 * 60 * 1000,
      maxRecordingBytes: limits.maxCompressedBytes ?? 25 * 1024 * 1024,
      warningThreshold: 0.8,
      captureSnapshotBeforeEachEvent: true,
    };
  } catch {
    return requested;
  }
}

async function persistRecordingV2(recording: PageAgentRecording): Promise<string | null> {
  if (recording.version !== '2.0.0') return null;
  if (!recording.termination?.complete) {
    throw new Error('semantic recording is incomplete');
  }
  if (recording.meta.serverRecordingId) return recording.meta.serverRecordingId;
  if (!serverConfig.baseUrl) throw new Error('semantic recording requires a configured server');
  const traceId = generateTraceId();
  const response = await fetchWithTimeout(
    `${normalizeBaseUrl(serverConfig.baseUrl)}/api/v1/recordings`,
    {
      method: 'POST',
      headers: buildAdminHeaders(traceId),
      body: JSON.stringify({
        recording,
        startedAt: recording.meta.recordedAt,
        endedAt: recording.meta.endedAt,
      }),
    },
    RECORDING_UPLOAD_TIMEOUT_MS,
  );
  if (!response.ok) {
    const detail = await response.text().catch(() => '');
    throw new Error(`recording persistence failed: ${response.status} ${detail}`.trim());
  }
  const data = await response.json() as { recording?: { id?: string } };
  const id = data.recording?.id;
  if (!id) throw new Error('recording persistence response did not include an id');
  recording.meta.serverRecordingId = id;
  await recordingStore.set(recording);
  return id;
}

async function ensureRecordingReadyForGeneration(recording: PageAgentRecording): Promise<string | null> {
  if (recording.version !== '2.0.0') return null;
  try {
    await persistRecordingV2(recording);
    return null;
  } catch (error) {
    return `录制尚未安全持久化：${error instanceof Error ? error.message : String(error)}`;
  }
}

async function loadCapabilities(): Promise<RecordingCapabilities | null> {
  if (!serverConfig.baseUrl) return null;
  try {
    const response = await fetchWithTimeout(
      `${normalizeBaseUrl(serverConfig.baseUrl)}/api/v1/capabilities`,
      {},
      5000,
    );
    if (!response.ok) return null;
    lastCapabilities = await response.json() as RecordingCapabilities;
    return lastCapabilities;
  } catch {
    return null;
  }
}

async function requirementRecording(): Promise<{ recording: PageAgentRecording; recordingId: string }> {
  const recording = await recordingStore.get();
  if (!recording) throw new Error('没有可用录制');
  if (recording.version !== '2.0.0') throw new Error('采集需求工作流需要语义录制 v2');
  const readinessError = await ensureRecordingReadyForGeneration(recording);
  if (readinessError) throw new Error(readinessError);
  const recordingId = recording.meta.serverRecordingId;
  if (!recordingId) throw new Error('录制尚未获得服务端 ID');
  return { recording, recordingId };
}

function marksOverrideForServer(recording: PageAgentRecording, capabilities: RecordingCapabilities | null): unknown[] | undefined {
  if (!capabilities?.features?.pageMarks) return undefined;
  return Array.isArray(recording.marks) ? recording.marks : [];
}

function marksFingerprint(recording: PageAgentRecording): string {
  const marks = Array.isArray(recording.marks) ? recording.marks : [];
  const stable = JSON.stringify(marks.map((mark) => ({
    id: mark.id,
    role: mark.role,
    note: mark.note,
    selector: mark.element?.selector,
    actionIndex: mark.actionIndex,
    snapshotSequence: mark.snapshotSequence,
    state: mark.state,
  })));
  let hash = 5381;
  for (let index = 0; index < stable.length; index++) {
    hash = ((hash << 5) + hash) ^ stable.charCodeAt(index);
  }
  return `${marks.length}:${(hash >>> 0).toString(36)}`;
}

function validPageMarkArray(value: unknown): value is PageMark[] {
  const roles = new Set(['listItem', 'field', 'nextPage', 'input', 'exclude']);
  if (!Array.isArray(value) || value.length > 24) return false;
  return value.every((mark) => {
    if (!mark || typeof mark !== 'object') return false;
    const candidate = mark as Partial<PageMark>;
    return typeof candidate.id === 'string'
      && typeof candidate.timestamp === 'number'
      && typeof candidate.url === 'string'
      && typeof candidate.role === 'string'
      && roles.has(candidate.role)
      && typeof candidate.note === 'string'
      && [...candidate.note].length <= 200
      && Boolean(candidate.element)
      && typeof candidate.element?.selector === 'string'
      && candidate.element.selector.trim().length > 0;
  });
}

async function requirementRequest<T>(path: string, options: RequestInit = {}, timeoutMs = FETCH_TIMEOUT_MS): Promise<T> {
  const traceId = generateTraceId();
  const response = await fetchWithTimeout(
    `${normalizeBaseUrl(serverConfig.baseUrl)}${path}`,
    { ...options, headers: buildAdminHeaders(traceId) },
    timeoutMs,
  );
  if (!response.ok) {
    const body = await response.json().catch(() => null) as {
      error?: string;
      code?: string;
      blocking_flags?: string[];
      details?: string;
    } | null;
    const message = body?.error || `服务端返回 ${response.status}`;
    const code = body?.code || 'HTTP_ERROR';
    const blockingFlags = Array.isArray(body?.blocking_flags) ? body!.blocking_flags : undefined;
    const safePhases = new Set([
      'required-fields', 'requirement-schema', 'recording-domain-evidence',
      'baseline-copy', 'baseline-validation', 'baseline-map',
      'baseline-security-scan', 'workflow-input',
      'baseline-action-schema', 'baseline-navigation-policy',
      'baseline-required-fields', 'baseline-entry-domain', 'baseline-structure',
    ]);
    const phase = body?.details && safePhases.has(body.details) ? ` [phase: ${body.details}]` : '';
    throw new ServerRequestError(
      `${message}${phase} (traceId: ${traceId})`,
      code,
      response.status,
      blockingFlags,
    );
  }
  return await response.json() as T;
}

async function pollRequirementJob(jobId: string): Promise<RequirementJob> {
  const deadline = createProgressDeadline(LLM_FETCH_TIMEOUT_MS, LLM_PROGRESS_STALL_TIMEOUT_MS);
  while (!deadline.expired()) {
    const response = await requirementRequest<RequirementJobResponse>(`/api/v1/requirement-jobs/${encodeURIComponent(jobId)}`);
    if (!response.job) throw new Error('服务端未返回采集需求任务');
    if (response.job.status === 'completed' || response.job.status === 'failed') {
      return response.job;
    }
    broadcastLLMJobProgress(response.job);
    if ((response.job.chunkCount ?? 0) > 0) deadline.observe(response.job.completedChunks ?? 0);
    await sleep(REQUIREMENT_POLL_INTERVAL_MS);
  }
  throw new Error('采集需求任务等待超时；任务已保留，可重新打开向导继续');
}

/**
 * Reads persisted candidate/normalize RequirementJob keys and fetches their
 * current server status. Called from RESUME_DSL_WORKFLOW so the wizard can
 * re-enter polling for an LLM job that was in-flight when the SW was evicted
 * (the workflow session key is only written once the workflow is created, so
 * a mid-candidates eviction would otherwise orphan the server-side job).
 *
 * Returns an empty object when neither key is present or the server is
 * unreachable; callers spread the result into the response and only the
 * wizard consumes the optional `candidateJob`/`normalizeJob` fields.
 */
async function reconcileOrphanedRequirementJobs(): Promise<{
  candidateJob?: ResumableRequirementJob;
  normalizeJob?: ResumableRequirementJob;
}> {
  const result: {
    candidateJob?: ResumableRequirementJob;
    normalizeJob?: ResumableRequirementJob;
  } = {};
  for (const [storageKey, field] of [
    [REQUIREMENT_CANDIDATE_JOB_KEY, 'candidateJob'],
    [REQUIREMENT_NORMALIZE_JOB_KEY, 'normalizeJob'],
  ] as const) {
    try {
      const stored = await chrome.storage.session.get(storageKey);
      const entry = stored[storageKey] as { jobId?: string } | undefined;
      if (!entry?.jobId) continue;
      const response = await requirementRequest<{ job?: ResumableRequirementJob }>(
        `/api/v1/requirement-jobs/${encodeURIComponent(entry.jobId)}`,
      );
      if (response.job) result[field] = response.job;
    } catch {
      // Server unreachable / 404: leave the entry in storage for the wizard
      // to surface via retry. The response omits the field in that case.
    }
  }
  return result;
}

function requirementJobResult(job: RequirementJob): Record<string, unknown> {
  if (job.status === 'failed') {
    return {
      success: false,
      workflowV2: true,
      job,
      error: job.errorMessage || '采集需求生成失败；可以重试或手动填写结构化需求',
    };
  }
  return {
    success: true,
    workflowV2: true,
    job,
    candidates: job.result?.candidates,
    requirement: job.result?.requirement,
    requirementId: job.requirementId,
  };
}

async function pollDSLWorkflow(workflowId: string): Promise<DSLWorkflow> {
  const deadline = createProgressDeadline(LLM_FETCH_TIMEOUT_MS, LLM_PROGRESS_STALL_TIMEOUT_MS);
  while (!deadline.expired()) {
    const response = await requirementRequest<DSLWorkflowResponse>(
      `/api/v1/dsl-workflows/${encodeURIComponent(workflowId)}`,
    );
    if (!response.workflow) throw new Error('服务端未返回 DSL 工作流');
    if (response.workflow.status !== 'generating' && response.workflow.status !== 'repairing') {
      return response.workflow;
    }
    const job = await broadcastCurrentDSLJobProgress(response.workflow);
    if ((job?.chunkCount ?? 0) > 0) deadline.observe(job?.completedChunks ?? 0);
    await sleep(REQUIREMENT_POLL_INTERVAL_MS);
  }
  throw new Error('DSL 工作流等待超时；任务已保留，可重新打开向导继续');
}

// Chunk progress for the active generation/repair job is best-effort: a
// failed or missing progress fetch must never break workflow polling.
async function broadcastCurrentDSLJobProgress(workflow: DSLWorkflow): Promise<DSLJob | undefined> {
  if (!workflow.currentJobId) return undefined;
  try {
    const response = await requirementRequest<DSLJobResponse>(
      `/api/v1/dsl-jobs/${encodeURIComponent(workflow.currentJobId)}`,
    );
    if (response.job) broadcastLLMJobProgress(response.job);
    return response.job;
  } catch {
    // Ignore progress-only failures.
    return undefined;
  }
}

// M-2: validate a server-returned provisionalRule before persisting it as
// lastRule (and before START_DSL_REPLAY will execute it). Without this, a
// compromised or buggy server could inject an arbitrary rule shape that
// later runs in the user's browser with the user's session cookies. The
// checks are intentionally structural — full schema validation happens
// server-side — but they reject the dangerous cases (non-http(s) entry,
// non-array steps, missing domain) that would let malformed input reach
// the executor.
function validateProvisionalRule(rule: unknown): string[] {
  const errors: string[] = [];
  if (typeof rule !== 'object' || rule === null) {
    return ['provisionalRule is not an object'];
  }
  const r = rule as Record<string, unknown>;
  const entryRaw = r.entry;
  let entryUrl: string | undefined;
  if (typeof entryRaw === 'string') {
    entryUrl = entryRaw;
  } else if (entryRaw && typeof entryRaw === 'object' && typeof (entryRaw as { url?: unknown }).url === 'string') {
    entryUrl = (entryRaw as { url: string }).url;
  } else {
    errors.push('provisionalRule.entry must be a string or { url: string }');
  }
  if (entryUrl !== undefined) {
    try {
      const parsed = new URL(entryUrl);
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
        errors.push(`provisionalRule.entry must be http(s), got ${parsed.protocol}`);
      }
    } catch {
      errors.push('provisionalRule.entry is not a valid URL');
    }
  }
  if (typeof r.domain !== 'string' || r.domain.trim() === '') {
    errors.push('provisionalRule.domain must be a non-empty string');
  }
  if (!Array.isArray(r.steps)) {
    errors.push('provisionalRule.steps must be an array');
  } else if (r.steps.length === 0) {
    errors.push('provisionalRule.steps must not be empty');
  } else {
    r.steps.forEach((step, i) => {
      if (typeof step !== 'object' || step === null || typeof (step as { action?: unknown }).action !== 'string') {
        errors.push(`provisionalRule.steps[${i}] must have a string action`);
      }
    });
  }
  return errors;
}

async function rememberDSLWorkflow(workflow: DSLWorkflow, replayId?: string, baselineRule?: Rule): Promise<void> {
  // M-2: validate provisionalRule before persisting. If invalid, log and
  // skip lastRule/lastRuleYaml so subsequent START_DSL_REPLAY cannot pick
  // up a malformed rule. The workflow session key is still saved so the
  // wizard can recover / resubmit.
  let safeRule: Rule | undefined;
  let safeYaml: string | undefined;
  if (workflow.provisionalRule) {
    const ruleErrors = validateProvisionalRule(workflow.provisionalRule);
    if (ruleErrors.length > 0) {
      console.warn(
        '[background] server returned malformed provisionalRule; skipping persist:',
        ruleErrors.join('; '),
      );
    } else {
      safeRule = workflow.provisionalRule;
      safeYaml = workflow.provisionalYaml;
    }
  } else if (workflow.provisionalYaml !== undefined) {
    safeYaml = workflow.provisionalYaml;
  }
  await chrome.storage.session.set({
    [DSL_WORKFLOW_SESSION_KEY]: {
      workflowId: workflow.id,
      requirementId: workflow.requirementId,
      recordingId: workflow.recordingId,
      browserProfileId: workflow.browserProfileId,
      replayId,
      // The recording-derived baseline submitted at creation. Human
      // corrections must preserve its id/version/entry/domain, so keep it
      // alongside the session for the wizard's correction editor.
      ...(baselineRule ? { baselineRule } : {}),
    },
    ...(safeRule ? { lastRule: safeRule } : {}),
    ...(safeYaml !== undefined ? { lastRuleYaml: safeYaml } : {}),
  });
}

async function loadWorkflowBaseline(workflowId: string): Promise<Rule | undefined> {
  const stored = await chrome.storage.session.get(DSL_WORKFLOW_SESSION_KEY);
  const session = stored[DSL_WORKFLOW_SESSION_KEY] as
    | { workflowId?: string; baselineRule?: Rule }
    | undefined;
  if (!session || session.workflowId !== workflowId) return undefined;
  return session.baselineRule;
}

function dslWorkflowResult(workflow: DSLWorkflow, extras: Record<string, unknown> = {}): Record<string, unknown> {
  if (workflow.status === 'failed') {
    return {
      success: false,
      workflow,
      error: workflow.errorMessage || 'DSL 生成或修复失败',
      ...extras,
    };
  }
  return { success: true, workflow, ...extras };
}

function workflowCanEnterReplay(workflow: DSLWorkflow): boolean {
  return workflow.status === 'awaiting_replay'
    || workflow.status === 'replaying';
}

async function confirmedWorkflowRequirement(workflow: DSLWorkflow): Promise<unknown> {
  const response = await requirementRequest<CollectionRequirementResponse>(
    `/api/v1/requirements/${encodeURIComponent(workflow.requirementId)}`,
  );
  const stored = response.requirement;
  if (stored?.id !== workflow.requirementId
    || stored.recordingId !== workflow.recordingId
    || stored.status !== 'confirmed'
    || !isCollectionRequirementSpec(stored.requirement)) {
    throw new Error('服务端未返回工作流绑定的已确认采集需求');
  }
  return stored.requirement;
}

async function sendLog(traceId: string, level: string, message: string, extra?: Record<string, unknown>): Promise<void> {
  if (!serverConfig.baseUrl) return;
  const baseUrl = normalizeBaseUrl(serverConfig.baseUrl);
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (serverKeys.apiKey) {
    headers['Authorization'] = `Bearer ${serverKeys.apiKey}`;
  }
  headers['X-Trace-Id'] = traceId;
  // Logging is best-effort; cap it with a short timeout so a slow/unresponsive
  // /logs endpoint cannot hold connections open for the full default window.
  await fetchWithTimeout(
    `${baseUrl}/logs`,
    {
      method: 'POST',
      headers,
      body: JSON.stringify({
        traceId,
        level,
        message,
        extra,
      }),
    },
    LOG_FETCH_TIMEOUT_MS,
  );
}

async function loadServerConfig(): Promise<void> {
  const [localResult, sessionResult] = await Promise.all([
    chrome.storage.local.get('serverConfig'),
    chrome.storage.session.get('serverKeys'),
  ]);
  if (localResult.serverConfig) {
    const loadedConfig = localResult.serverConfig as ServerConfig;
    serverConfig = { baseUrl: normalizeBaseUrl(loadedConfig.baseUrl) };
  }
  if (sessionResult.serverKeys) {
    serverKeys = sessionResult.serverKeys as ServerKeys;
  }
}

// H-3: defensive guard against the SW cold-start race. Module init kicks off
// loadServerConfig fire-and-forget; a message arriving before it resolves
// would read the empty initial serverConfig. Server-dependent handlers
// await this promise so they always see the persisted config.
let serverConfigLoadPromise: Promise<void> | null = null;
function ensureServerConfigLoaded(): Promise<void> {
  if (!serverConfigLoadPromise) {
    serverConfigLoadPromise = loadServerConfig();
  }
  return serverConfigLoadPromise;
}

async function saveServerConfig(config: ServerConfig): Promise<void> {
  // Allow empty (to clear), but reject non-http(s) schemes so API calls
  // carrying Bearer tokens cannot be redirected to file://, data:, etc.
  if (config.baseUrl) {
    assertHttpUrl(config.baseUrl, '服务端地址');
  }
  const normalizedConfig = { baseUrl: normalizeBaseUrl(config.baseUrl) };
  await chrome.storage.local.set({ serverConfig: normalizedConfig });
  serverConfig = normalizedConfig;
}

async function saveServerKeys(keys: ServerKeys): Promise<void> {
  await chrome.storage.session.set({ serverKeys: keys });
  serverKeys = keys;
}

async function getActiveTab(): Promise<ChromeTab | undefined> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  return tab;
}

async function ensureContentScript(tabId: number): Promise<void> {
  try {
    await chrome.scripting.executeScript({
      target: { tabId },
      files: ['content.js'],
    });
  } catch (err) {
    console.warn('[background] content script injection failed:', err);
  }
}

async function sendToContentScript(tabId: number, message: unknown): Promise<unknown> {
  // Recording ownership lives in the top frame. Without an explicit frameId,
  // Chrome may return the response from any all-frames content script, which
  // can produce an empty recording on pages containing iframes.
  return chrome.tabs.sendMessage(tabId, message, { frameId: 0 });
}

interface RecordingHandoffState {
  version: 1;
  recordingFlag: true;
  recording: PageAgentRecording;
  options: Record<string, unknown>;
  selectorToIndex?: ActiveRecordingSession['selectorToIndex'];
  nextIndex?: number;
  lastRecordedUrl?: string | null;
}

async function transferActiveRecording(
  session: ActiveRecordingSession,
  target: ChromeTab,
): Promise<{ success: boolean; error?: string; session?: ActiveRecordingSession }> {
  if (!target.id || !target.url) return { success: false, error: 'recording handoff target is unavailable' };
  try {
    const url = new URL(target.url);
    if (url.protocol !== 'http:' && url.protocol !== 'https:') {
      return { success: false, error: 'recording handoff target must be an http/https page' };
    }
  } catch {
    return { success: false, error: 'recording handoff target URL is invalid' };
  }
  const exported = await sendToContentScript(session.tabId, { action: 'EXPORT_RECORDING_HANDOFF' }) as {
    success?: boolean;
    state?: RecordingHandoffState;
    error?: string;
  } | undefined;
  if (exported?.success !== true || exported.state?.recording.version !== '2.0.0'
    || exported.state.recording.termination) {
    return { success: false, error: exported?.error ?? 'source recording handoff export failed' };
  }
  await ensureContentScript(target.id);
  const imported = await sendToContentScript(target.id, {
    action: 'IMPORT_RECORDING_HANDOFF',
    payload: exported.state,
  }) as { success?: boolean; state?: RecordingHandoffState; error?: string } | undefined;
  if (imported?.success !== true || imported.state?.recording.version !== '2.0.0'
    || imported.state.recording.termination) {
    await sendToContentScript(target.id, { action: 'DISCARD_RECORDING_HANDOFF' }).catch(() => undefined);
    return { success: false, error: imported?.error ?? 'destination recording handoff import failed' };
  }
  const transferred: ActiveRecordingSession = {
    ...session,
    tabId: target.id,
    options: imported.state.options,
    selectorToIndex: imported.state.selectorToIndex,
    nextIndex: imported.state.nextIndex,
    lastRecordedUrl: imported.state.lastRecordedUrl,
  };
  try {
    await recordingStore.set(imported.state.recording);
    await saveActiveRecording(transferred);
    const discarded = await sendToContentScript(session.tabId, {
      action: 'DISCARD_RECORDING_HANDOFF',
    }) as { success?: boolean } | undefined;
    if (discarded?.success !== true) throw new Error('source recording handoff suspension failed');
    await chrome.storage.local.remove(recordingCheckpointKey(session.tabId)).catch(() => undefined);
    return { success: true, session: transferred };
  } catch (error) {
    await recordingStore.set(exported.state.recording).catch(() => undefined);
    await saveActiveRecording(session).catch(() => undefined);
    await sendToContentScript(target.id, { action: 'DISCARD_RECORDING_HANDOFF' }).catch(() => undefined);
    return { success: false, error: error instanceof Error ? error.message : String(error) };
  }
}

function waitForTabLoad(tabId: number, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    // Explicit guard so only the first of {listener, timer, tabs.get} to fire
    // settles the promise. Without this, a late tabs.get callback can call
    // cleanup()+resolve() after the timeout already rejected.
    let settled = false;

    const cleanup = () => {
      chrome.tabs.onUpdated.removeListener(listener);
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
    };

    const listener = (updatedTabId: number, changeInfo: ChromeTabChangeInfo) => {
      if (updatedTabId === tabId && changeInfo.status === 'complete') {
        if (settled) return;
        settled = true;
        cleanup();
        resolve();
      }
    };

    timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(new Error('标签页加载超时'));
    }, timeoutMs);

    chrome.tabs.onUpdated.addListener(listener);

    // Guard against the tab completing before the listener was attached.
    chrome.tabs
      .get(tabId)
      .then((tab) => {
        if (tab.status === 'complete') {
          if (settled) return;
          settled = true;
          cleanup();
          resolve();
        }
      })
      .catch(() => undefined);
  });
}

// ==================== Replay orchestration ====================
//
// Phase 1 (§3 / §10 of docs/replay-fix-plan.md): the bypass step-by-step path
// is deleted; the runner now uses runRule with cross-navigation resume. The
// background injects replay-runner.js and invokes __ocReplayBoot via
// chrome.scripting.executeScript({func, args}) — no onMessage race.
//
// Phase 2 (deferred): chrome.alarms + chrome.runtime.onStartup +
// recoverReplaySessionOnStartup + registerReplayListeners (tabs.onUpdated
// reinjection). For now cross-navigation survival depends on the runner's
// sessionStorage checkpoint; the background does not yet auto-reinject.

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function ruleDomainList(rule: ReplayRule): string[] {
  const d = rule.domain;
  return Array.isArray(d) ? d.map(String) : [String(d)];
}

function hostMatchesDomain(host: string, domains: string[]): boolean {
  return domains.some((d) => host === d || host.endsWith('.' + d));
}

// H-5: a domain entry must look like a registered name (at least one dot,
// so bare TLDs like 'com' / 'net' / 'org' are rejected). Without this, a
// rule with domain='com' would match every .com host via hostMatchesDomain's
// suffix check, allowing replay to run on attacker-controlled pages.
function isValidDomainEntry(d: string): boolean {
  // Loopback exemption: 'localhost' exact-matches only the loopback host and
  // any '*.localhost' suffix also resolves to loopback in Chromium, so it adds
  // no public-site replay surface. Public dotless TLDs stay rejected.
  if (d === 'localhost') return true;
  return d.length >= 3 && d.includes('.') && !d.startsWith('.') && !d.endsWith('.');
}

// Pre-validate the START_REPLAY entry URL against rule.domain. Defense-in-depth
// before chrome.tabs.create — the runner re-checks after injection.
function assertEntryUrlDomain(entryUrl: string, rule: ReplayRule): void {
  let parsed: URL;
  try {
    parsed = new URL(entryUrl);
  } catch {
    throw new Error('入口 URL 不是合法 URL');
  }
  const domains = ruleDomainList(rule);
  const invalid = domains.filter((d) => !isValidDomainEntry(d));
  if (invalid.length > 0) {
    throw new Error(`rule.domain 包含过宽的域名项 [${invalid.join(', ')}]，拒绝回放以防越权`);
  }
  if (!hostMatchesDomain(parsed.hostname, domains)) {
    throw new Error(`入口 URL 域名 ${parsed.hostname} 不在 rule.domain [${domains.join(', ')}] 中`);
  }
}

async function persistReplaySession(session: ReplaySession): Promise<void> {
  // SEC-004: allowlist of serializable fields. listener refs / timeoutId are
  // in-memory only and must not be written to storage.
  const serializable = {
    tabId: session.tabId,
    ruleId: session.ruleId,
    taskId: session.taskId,
    startedAt: session.startedAt,
    hopCount: session.hopCount,
    lastProgressAt: session.lastProgressAt,
    expectedDomains: session.expectedDomains,
  };
  await chrome.storage.session.set({ [REPLAY_SESSION_KEY]: serializable });
}

async function clearReplayStorage(taskId: string): Promise<void> {
  await chrome.storage.session.remove([
    REPLAY_SESSION_KEY,
    replayPayloadKey(taskId),
    replayContextKey(taskId),
  ]);
}

// Wait for the entry tab to load, then inject replay-runner.js and invoke
// __ocReplayBoot via chrome.scripting.executeScript({func, args}). Race-free
// vs. the old START_REPLAY_RUNNER onMessage handshake (P0 #1).
async function runReplayBoot(tabId: number, replayPayload: unknown): Promise<void> {
  try {
    await waitForTabLoad(tabId, TAB_LOAD_TIMEOUT_MS);
  } catch {
    // Tab may already be complete; continue with injection anyway.
  }
  await chrome.scripting.executeScript({
    target: { tabId },
    files: ['intent/replay-runner.js'],
  });
  await chrome.scripting.executeScript({
    target: { tabId },
    func: (p: unknown) => {
      if (typeof (globalThis as any).__ocReplayBoot !== 'function') {
        throw new Error('replay-runner not loaded');
      }
      (globalThis as any).__ocReplayBoot(p);
    },
    args: [replayPayload],
  });
}

async function cleanupReplaySession(
  status: 'success' | 'failure' | 'cancelled',
  message: string,
  options: { closeTab?: boolean; abortRunner?: boolean; session?: ReplaySession } = {},
): Promise<void> {
  // Allow callers (notably the alarm watchdog) to supply a session rebuilt
  // from storage when activeReplay is null (e.g., after SW restart before
  // recoverReplaySessionOnStartup completes). Without this, the alarm would
  // silently no-op and the replay tab would leak indefinitely.
  const session = options.session ?? activeReplay;
  if (!session) return;
  if (session === activeReplay) activeReplay = null;
  // Phase 2: clear alarms + remove listeners by ref (survive SW restart).
  if (session.onTabUpdated) chrome.tabs.onUpdated.removeListener(session.onTabUpdated);
  if (session.onTabRemoved) chrome.tabs.onRemoved.removeListener(session.onTabRemoved);
  await chrome.alarms.clear(`replay_total_${session.taskId}`).catch(() => undefined);
  await chrome.alarms.clear(`replay_idle_${session.taskId}`).catch(() => undefined);
  if (options.abortRunner !== false) {
    await chrome.tabs.sendMessage(session.tabId, { action: 'ABORT_REPLAY' }).catch(() => undefined);
  }
  await clearReplayStorage(session.taskId);
  if (options.closeTab !== false) {
    await chrome.tabs.remove(session.tabId).catch(() => undefined);
  }
  broadcastReplayComplete({ status, message });
  // WI-6: if no wizard page is connected to consume the REPLAY_COMPLETE
  // broadcast, the DSL replay attempt would be orphaned until the server's
  // 35-minute reaper runs. Read the persisted DSL workflow/replay IDs from
  // session storage and call the server's complete endpoint directly.
  // The collected sendResult rows are submitted with a successful completion:
  // without them the server's per-row output gate fails the replay with
  // OUTPUT_SCHEMA_INVALID even though the browser execution succeeded.
  if (currentWizardPort === null) {
    try {
      const stored = await chrome.storage.session.get(DSL_WORKFLOW_SESSION_KEY);
      const dslSession = stored[DSL_WORKFLOW_SESSION_KEY] as
        | { workflowId?: string; replayId?: string }
        | undefined;
      if (dslSession?.workflowId && dslSession?.replayId) {
        // C-3: derive succeeded / errorCode from the status parameter rather
        // than hardcoding failure. A successful replay whose wizard tab was
        // closed must still reach the server as a success, otherwise the
        // workflow can never advance to awaiting_confirmation.
        const succeeded = status === 'success';
        const errorCode = succeeded
          ? undefined
          : status === 'cancelled'
            ? 'REPLAY_CANCELLED'
            : 'REPLAY_FAILED';
        const body: Record<string, unknown> = { succeeded, errorMessage: message };
        if (errorCode !== undefined) body.errorCode = errorCode;
        if (succeeded) {
          let rows = replayResultRows.get(session.taskId);
          if (!Array.isArray(rows) || rows.length === 0) {
            const storedRows = await chrome.storage.session.get(`oc_replay_rows_${session.taskId}`);
            rows = storedRows[`oc_replay_rows_${session.taskId}`] as unknown[] | undefined;
          }
          if (Array.isArray(rows) && rows.length > 0) {
            body.output = rows
              .flatMap((row) => Array.isArray(row) ? row : [row])
              .filter((row) => row && typeof row === 'object' && !Array.isArray(row))
              .slice(0, 5000);
          }
        }
        await requirementRequest(
          `/api/v1/dsl-workflows/${encodeURIComponent(dslSession.workflowId)}/replays/${encodeURIComponent(dslSession.replayId)}/complete`,
          { method: 'POST', body: JSON.stringify(body) },
          LLM_FETCH_TIMEOUT_MS,
        ).catch(() => undefined);
      }
    } catch { /* best effort: never block cleanup */ }
  }
  replayResultRows.delete(session.taskId);
  void chrome.storage.session.remove(`oc_replay_rows_${session.taskId}`).catch(() => undefined);
}

interface RetainedReplayTab {
  tabId: number;
  taskId?: string;
}

function replayTaskMatches(reportedTaskId: string | undefined, expectedTaskId: unknown): boolean {
  return reportedTaskId === undefined
    || typeof expectedTaskId !== 'string'
    || reportedTaskId === expectedTaskId;
}

async function retainSuccessfulReplayTab(tabId: number, taskId?: string): Promise<void> {
  // M-4: record retainedAt so startup recovery can release abandoned tabs
  // whose wizard never confirmed/aborted.
  await chrome.storage.session.set({
    [RETAINED_REPLAY_TAB_KEY]: { tabId, taskId, retainedAt: Date.now() },
  });
}

async function closeRetainedReplayTab(): Promise<boolean> {
  const stored = await chrome.storage.session.get(RETAINED_REPLAY_TAB_KEY);
  const retained = stored[RETAINED_REPLAY_TAB_KEY] as Partial<RetainedReplayTab> | undefined;
  await chrome.storage.session.remove(RETAINED_REPLAY_TAB_KEY);
  if (!Number.isInteger(retained?.tabId)) return false;
  await chrome.tabs.remove(retained!.tabId as number).catch(() => undefined);
  return true;
}

// Bind tabs.onUpdated / onRemoved closures onto the session. Called from
// START_REPLAY (initial) and recoverReplaySessionOnStartup (post-SW-restart).
// §5.2 lines 356-395.
function registerReplayListeners(session: ReplaySession): void {
  session.onTabUpdated = async (tabId, info, _tab) => {
    if (tabId !== session.tabId || info.status !== 'complete') return;
    // SEC-005: background-side post-navigation domain check (defense-in-depth
    // alongside the content-side __ocReplayAutoResume check).
    let tab: ChromeTab | undefined;
    try {
      tab = await chrome.tabs.get(tabId);
    } catch {
      return;
    }
    if (!tab?.url) return;
    let tabHost: string;
    try {
      tabHost = new URL(tab.url).hostname;
    } catch {
      return;
    }
    if (!hostMatchesDomain(tabHost, session.expectedDomains)) {
      await cleanupReplaySession('failure', `post-navigation domain mismatch: ${tabHost}`, {
        abortRunner: true,
        closeTab: true,
      });
      return;
    }
    session.hopCount += 1;
    if (session.hopCount > REPLAY_HOP_LIMIT) {
      await cleanupReplaySession('failure', '导航跳数超限，疑似重定向循环', {
        abortRunner: true,
        closeTab: true,
      });
      return;
    }
    await persistReplaySession(session);
    // Cross-nav resume: re-inject runner and invoke __ocReplayAutoResume with
    // the persisted payload + ctx.
    const stored = await chrome.storage.session.get([
      replayPayloadKey(session.taskId),
      replayContextKey(session.taskId),
    ]);
    const replayPayload = stored[replayPayloadKey(session.taskId)];
    const replayContext = stored[replayContextKey(session.taskId)] ?? null;
    if (!replayPayload) {
      await cleanupReplaySession('failure', 'missing persisted replay payload', {
        abortRunner: true,
        closeTab: true,
      });
      return;
    }
    try {
      await chrome.scripting.executeScript({
        target: { tabId },
        files: ['intent/replay-runner.js'],
      });
      await chrome.scripting.executeScript({
        target: { tabId },
        func: (p: unknown, c: unknown) => {
          if (typeof (globalThis as any).__ocReplayAutoResume !== 'function') {
            throw new Error('replay-runner not loaded');
          }
          (globalThis as any).__ocReplayAutoResume(p, c);
        },
        args: [replayPayload, replayContext],
      });
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : String(err);
      await cleanupReplaySession('failure', errorMessage, { abortRunner: true, closeTab: true });
    }
  };
  session.onTabRemoved = async (tabId) => {
    if (tabId !== session.tabId) return;
    await cleanupReplaySession('failure', '回放标签页被关闭', { abortRunner: false, closeTab: false });
  };
  chrome.tabs.onUpdated.addListener(session.onTabUpdated);
  chrome.tabs.onRemoved.addListener(session.onTabRemoved);
}

// D-3: global recording-checkpoint cleanup. The per-session listener above
// (line 897) is replay-scoped and bound to a specific session.tabId; this is
// a separate top-level listener that clears the disk-backed recording
// checkpoint whenever any tab closes, so the .local store doesn't leak keys
// for tabs that were closed without a clean STOP_RECORDING.
chrome.tabs.onRemoved.addListener((tabId) => {
  void chrome.storage.local.remove(recordingCheckpointKey(tabId)).catch(() => undefined);
});

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  const msg = message as { action?: string; payload?: unknown };

  // Sender gate: content scripts run in arbitrary web pages and must not be
  // able to drive privileged actions (SET_SERVER_CONFIG, START_REPLAY, rule
  // upload, etc.). Extension pages remain trusted even when Chrome supplies a
  // sender.tab for a wizard opened in a normal browser tab.
  if (PRIVILEGED_ACTIONS.has(msg.action ?? '') && isFromContentScript(sender)) {
    sendResponse({ success: false, error: '拒绝来自网页的特权请求' });
    return false;
  }

  // Replay events are broadcast from the replay runner content script. Handle
  // them directly so we can respond immediately and avoid routing them through
  // the async handleMessage switch.
  //
  // Sender validation (§5.2 REPLAY_PROGRESS security): any content script on
  // any page (manifest matches *://*/*) can send runtime messages. Before we
  // touch activeReplay or rebroadcast, require sender.tab.id to match the
  // active session's tab. Otherwise a random page could drive REPLAY_PROGRESS
  // to mask a stuck replay, or poison REPLAY_CONTEXT.
  if (msg.action === 'REPLAY_PROGRESS') {
    if (activeReplay && sender.tab?.id === activeReplay.tabId) {
      activeReplay.lastProgressAt = Date.now();
      // WI-6 fix: accumulate result rows in memory so the no-wizard fallback
      // completion can submit the collected output. Without them a successful
      // replay whose wizard tab was closed completes with no output and the
      // server fails it with OUTPUT_SCHEMA_INVALID.
      if ((msg.payload as { type?: string } | undefined)?.type === 'result') {
        const rows = replayResultRows.get(activeReplay.taskId) ?? [];
        rows.push((msg.payload as { payload: unknown }).payload);
        const bounded = rows.slice(-5000);
        replayResultRows.set(activeReplay.taskId, bounded);
        // session storage survives service-worker eviction; the in-memory map
        // alone is lost if the SW restarts mid-replay
        void chrome.storage.session.set({ [`oc_replay_rows_${activeReplay.taskId}`]: bounded }).catch(() => undefined);
      }
      // hopCount bounded decrement (§5.2 #5 ADV-004): legitimate refresh +
      // extract cycles shed load, but pure refresh loops still accumulate.
      activeReplay.hopCount = Math.max(0, activeReplay.hopCount - 10);
      void persistReplaySession(activeReplay);
      // M-1: only the active replay tab may broadcast progress to extension
      // pages. Previously the broadcast was unconditional, letting any web
      // page inject fake progress into the wizard UI.
      broadcastReplayProgress(msg.payload);
    }
    sendResponse({ received: true });
    return true;
  }
  if (msg.action === 'REPLAY_CONTEXT') {
    // Sender gate: only the active replay tab may write context.
    if (activeReplay && sender.tab?.id === activeReplay.tabId) {
      // Payload shape: { lastCompletedStepPath: StepPath (array of frames),
      //   extracted, evaluated, captured, control?, hooksCompleted? }. `control`
      //   mirrors only the bounded ReplayState needed when an approved origin
      //   change loses the page's origin-scoped sessionStorage. Phase 3 renamed
      //   lastCompletedStepIndex (number) → lastCompletedStepPath (StepPath).
      //   D-2 added hooksCompleted for hook checkpointing. Stored opaquely;
      //   the re-injection handler just passes it through to __ocReplayAutoResume.
      const payload = msg.payload as { lastCompletedStepPath?: unknown; extracted?: unknown;
        evaluated?: unknown; captured?: unknown; control?: unknown; hooksCompleted?: unknown } | undefined;
      if (payload) {
        chrome.storage.session
          .set({ [replayContextKey(activeReplay.taskId)]: payload })
          .catch(() => undefined);
      }
    }
    sendResponse({ received: true });
    return true;
  }
  if (msg.action === 'REPLAY_COMPLETE') {
    // Null-safe: after SW eviction activeReplay may be null even though the
    // runner finished. Successful tabs remain observable until approval;
    // failed/cancelled tabs are closed immediately.
    const payload = (msg.payload ?? { status: 'failure', message: '回放结束' }) as {
      status: 'success' | 'failure' | 'cancelled';
      message?: string;
      taskId?: string;
    };
    const fromTabId = sender.tab?.id;
    if (activeReplay && fromTabId === activeReplay.tabId) {
      if (!replayTaskMatches(payload.taskId, activeReplay.taskId)) {
        sendResponse({ received: true });
        return true;
      }
      if (payload.status === 'success') {
        const completedTaskId = activeReplay.taskId;
        void (async () => {
          try {
            await retainSuccessfulReplayTab(fromTabId, completedTaskId);
            await cleanupReplaySession('success', payload.message ?? '', {
              abortRunner: false,
              closeTab: false,
            });
          } catch {
            await cleanupReplaySession('failure', '无法保留成功回放标签页', {
              abortRunner: false,
              closeTab: true,
            });
          }
        })();
      } else {
        void cleanupReplaySession(payload.status === 'cancelled' ? 'cancelled' : 'failure', payload.message ?? '', {
          abortRunner: false, // runner already finished
          closeTab: true,
        });
      }
    } else if (fromTabId != null) {
      // SW restarted mid-run (Phase 1 transitional): authorize against the
      // persisted session before retaining a successful tab. An unrelated
      // content script must not be able to nominate a tab for later closure.
      void (async () => {
        if (activeReplay) {
          await chrome.tabs.remove(fromTabId).catch(() => undefined);
          return;
        }
        const stored = await chrome.storage.session.get([REPLAY_SESSION_KEY, RETAINED_REPLAY_TAB_KEY]);
        const retained = stored[RETAINED_REPLAY_TAB_KEY] as Partial<RetainedReplayTab> | undefined;
        if (retained?.tabId === fromTabId) {
          // Navigation/reinjection can race two terminal messages from the
          // same runner. The first success is authoritative; later terminal
          // messages must neither close the oracle tab nor re-complete the
          // durable replay.
          return;
        }
        const session = stored[REPLAY_SESSION_KEY] as { tabId?: unknown } | undefined;
        const persisted = session as { tabId?: unknown; taskId?: unknown } | undefined;
        if (
          persisted?.tabId !== fromTabId
          || !replayTaskMatches(payload.taskId, persisted.taskId)
        ) {
          await chrome.tabs.remove(fromTabId).catch(() => undefined);
          return;
        }
        if (payload.status === 'success') {
          await retainSuccessfulReplayTab(
            fromTabId,
            typeof persisted.taskId === 'string' ? persisted.taskId : payload.taskId,
          ).catch(() => undefined);
        } else {
          await chrome.tabs.remove(fromTabId).catch(() => undefined);
        }
        await chrome.storage.session.remove([REPLAY_SESSION_KEY]).catch(() => undefined);
        broadcastReplayComplete(payload);
      })();
    } else {
      broadcastReplayComplete(payload);
    }
    sendResponse({ received: true });
    return true;
  }

  // M-5/M-6: serialize state-mutating handlers through a single Promise chain
  // so concurrent messages that both read+write activeRecording/activeReplay
  // cannot interleave their read-modify-write windows. Read-only and
  // long-running poll handlers run concurrently as before.
  const runHandler = () =>
    handleMessage(message as { action: string; payload?: unknown }, sender)
      .then((response) => sendResponse(response))
      .catch((err) => sendResponse({ success: false, error: String(err) }));

  if (STATE_MUTATING_ACTIONS.has(msg.action ?? '')) {
    serializeStateMutation(runHandler);
  } else {
    runHandler();
  }
  return true;
});

// M-5/M-6: serial queue for state-mutating handlers. Each handler runs to
// completion before the next state-mutating handler starts, eliminating
// read-modify-write races on activeRecording / activeReplay / serverConfig.
let stateMutationChain: Promise<void> = Promise.resolve();
function serializeStateMutation(work: () => Promise<void>): void {
  // Swallow prior rejection so a failing handler does not permanently break
  // the queue; then chain the new work.
  stateMutationChain = stateMutationChain
    .catch(() => undefined)
    .then(work);
}

// Actions whose handlers read and mutate shared in-memory state
// (activeRecording / activeReplay / serverConfig). These must run
// exclusively with respect to each other; non-listed actions (GET_STATE,
// GET_LAST_RECORDING, long-running LLM/DSL polls) run concurrently.
const STATE_MUTATING_ACTIONS = new Set([
  'START_RECORDING',
  'STOP_RECORDING',
  'RECORDING_CHECKPOINT',
  'RESUME_RECORDING',
  'RECORDING_STATUS',
  'UPDATE_RECORDING_MARKS',
  'CLEAR_RECORDING_CHECKPOINT',
  'SET_SERVER_CONFIG',
  'START_REPLAY',
  'ABORT_REPLAY',
  'START_DSL_REPLAY',
  'COMPLETE_DSL_REPLAY',
  'CONFIRM_DSL_WORKFLOW',
  'UPLOAD_CONFIRMED_RULE',
  'ENHANCE_RULE',
  'GENERATE_DSL_FROM_INTENT',
  'DOWNLOAD_RULE',
]);

async function handleMessage(
  message: { action: string; payload?: unknown },
  sender: ChromeRuntimeMessageSender,
): Promise<unknown> {
  switch (message.action) {
    case 'GET_STATE': {
      const session = await loadActiveRecording();
      return { state: session ? 'recording' : recordingState, statusMessage: session?.statusMessage };
    }

    case 'RECORDING_CHECKPOINT': {
      const session = await loadActiveRecording();
      if (!session || sender.tab?.id !== session.tabId) {
        return { success: false, error: 'recording checkpoint sender does not match the active tab' };
      }
      const payload = message.payload as {
        recording?: PageAgentRecording;
        options?: Record<string, unknown>;
        selectorToIndex?: ActiveRecordingSession['selectorToIndex'];
        nextIndex?: number;
        lastRecordedUrl?: string | null;
      } | undefined;
      if (!payload?.recording || payload.recording.version !== '2.0.0') {
        return { success: false, error: 'invalid semantic recording checkpoint' };
      }
      await recordingStore.set(payload.recording);
      await saveActiveRecording({
        ...session,
        options: payload.options ?? session.options,
        selectorToIndex: payload.selectorToIndex,
        nextIndex: payload.nextIndex,
        lastRecordedUrl: payload.lastRecordedUrl,
      });
      // D-3: defense-in-depth chrome.storage.local copy. IndexedDB + .session
      // already persist; .local adds a disk-backed copy that survives browser
      // restart and survives even if the IndexedDB write is in-flight at crash.
      const chkKey = recordingCheckpointKey(session.tabId);
      await chrome.storage.local.set({
        [chkKey]: {
          snapshot: payload.recording,
          options: payload.options ?? session.options,
          selectorToIndex: payload.selectorToIndex,
          nextIndex: payload.nextIndex,
          lastRecordedUrl: payload.lastRecordedUrl,
          savedAt: Date.now(),
          tabId: session.tabId,
          sessionId: session.startedAt,
        },
      }).catch((e: unknown) => {
        // Best-effort: do not block recording on .local quota failure.
        console.warn(`[RECORDING_CHECKPOINT] chrome.storage.local write failed: ${String((e as Error)?.message ?? e)}`);
      });
      return { success: true };
    }

    case 'CLEAR_RECORDING_CHECKPOINT': {
      // H-1: ignore payload.tabId — only the sender's own tab may be cleared.
      // Otherwise any web page could delete another tab's disk-backed
      // recording checkpoint by passing { payload: { tabId: <victim> } }.
      const tabId = sender.tab?.id;
      if (tabId != null) {
        await chrome.storage.local.remove(recordingCheckpointKey(tabId)).catch(() => undefined);
      }
      return { success: true };
    }

    case 'RESUME_RECORDING': {
      const session = await loadActiveRecording();
      if (!session || sender.tab?.id !== session.tabId) {
        // D-3 fallback: SW has no in-memory session (lost to eviction or the
        // tab reloaded while .session was cleared). Try the disk-backed
        // .local checkpoint so the content script can still resume.
        const fallbackTabId = sender.tab?.id;
        if (fallbackTabId != null) {
          const stored = await chrome.storage.local.get(recordingCheckpointKey(fallbackTabId));
          const chk = stored[recordingCheckpointKey(fallbackTabId)] as {
            snapshot?: PageAgentRecording;
            options?: Record<string, unknown>;
            selectorToIndex?: ActiveRecordingSession['selectorToIndex'];
            nextIndex?: number;
            lastRecordedUrl?: string | null;
            savedAt?: number;
            sessionId?: number;
          } | undefined;
          if (chk?.snapshot) {
            return { active: false, localCheckpoint: chk };
          }
        }
        return { active: false };
      }
      const recording = await recordingStore.get();
      if (!recording || recording.version !== '2.0.0' || recording.termination) return { active: false };
      return {
        active: true,
        state: {
          version: 2,
          recordingFlag: true,
          recording,
          options: session.options,
          selectorToIndex: session.selectorToIndex ?? [],
          nextIndex: session.nextIndex ?? 1,
          lastRecordedUrl: session.lastRecordedUrl ?? null,
        },
      };
    }

    case 'RECORDING_STATUS': {
      const session = await loadActiveRecording();
      if (!session || sender.tab?.id !== session.tabId) {
        return { success: false, error: 'recording status sender does not match the active tab' };
      }
      const payload = message.payload as {
        status?: 'warning' | 'stopped';
        message?: string;
        recording?: PageAgentRecording;
      } | undefined;
      if (payload?.status === 'warning') {
        await saveActiveRecording({ ...session, statusMessage: payload.message });
      } else if (payload?.status === 'stopped' && payload.recording) {
        // H-2: persistence path is privileged — restrict to the top frame.
        // manifest has all_frames:true, so an iframe shares sender.tab.id
        // with the recording tab; without this check an iframe ad could
        // inject a crafted recording that gets POSTed to the admin server.
        if (sender.frameId !== undefined && sender.frameId !== 0) {
          return { success: false, error: 'RECORDING_STATUS stopped must originate from the top frame' };
        }
        await recordingStore.set(payload.recording);
        await saveActiveRecording(null);
        if (payload.recording.version === '2.0.0' && payload.recording.termination?.complete) {
          persistRecordingV2(payload.recording).catch((error) =>
            console.warn('[background] automatic recording persistence failed:', error instanceof Error ? error.message : String(error)),
          );
        }
      }
      return { success: true };
    }

    case 'AGGREGATE_DOM': {
      const tabId = sender.tab?.id;
      if (!tabId) {
        return { success: false, error: '缺少标签页 ID' };
      }
      const aggregateMessage = message as { options?: AggregateOptions; payload?: { options?: AggregateOptions } };
      const options = aggregateMessage.options ?? aggregateMessage.payload?.options ?? {};
      const frameTree = await aggregateFrameDom(tabId, options);
      return { success: true, frameTree };
    }

    case 'START_RECORDING': {
      const existing = await loadActiveRecording();
      if (existing) {
        return { success: false, error: '已在录制，请先停止当前录制' };
      }
      const tab = await getActiveTab();
      if (!tab?.id) {
        return { success: false, error: '没有活动标签页' };
      }
      const options = await resolveRecordingOptions(message.payload);
      await recordingStore.remove();
      // D-3: clear any residual disk-backed checkpoint from a prior session
      // on this tab before starting fresh.
      await chrome.storage.local.remove(recordingCheckpointKey(tab.id)).catch(() => undefined);
      await saveActiveRecording({ tabId: tab.id, startedAt: Date.now(), options });
      try {
        await ensureContentScript(tab.id);
        const contentPayload = Object.keys(options).length > 0 ? options : message.payload;
        const csResponse = (await sendToContentScript(tab.id, { action: 'START_RECORDING', payload: contentPayload })) as
          | { success?: boolean; error?: string }
          | undefined;
        // If the content script explicitly failed, propagate the error rather
        // than reporting recording as active.
        if (csResponse && csResponse.success === false) {
          await saveActiveRecording(null);
          return { success: false, error: csResponse.error ?? '录制启动失败' };
        }
      } catch (error) {
        await saveActiveRecording(null);
        return {
          success: false,
          error: `录制启动失败：${error instanceof Error ? error.message : String(error)}`,
        };
      }
      return { success: true, protocolVersion: options.protocolVersion ?? '1.0.0' };
    }

    case 'ENSURE_RECORDING_READY': {
      let session = await loadActiveRecording();
      if (!session) {
        return { success: false, error: '没有活动录制会话' };
      }
      const target = await getActiveTab();
      if (!target?.id) return { success: false, error: 'recording readiness target is unavailable' };
      if (target.id !== session.tabId) {
        const transferred = await transferActiveRecording(session, target);
        if (!transferred.success || !transferred.session) return transferred;
        session = transferred.session;
      }
      await ensureContentScript(session.tabId);
      const response = await sendToContentScript(session.tabId, {
        action: 'ENSURE_RECORDING_READY',
      }) as {
        success?: boolean;
        active?: boolean;
        protocolVersion?: string;
        error?: string;
      } | undefined;
      if (response?.success !== true || response.active !== true) {
        return { success: false, error: response?.error ?? '录制内容脚本尚未就绪' };
      }
      const expectedProtocol = typeof session.options.protocolVersion === 'string'
        ? session.options.protocolVersion
        : undefined;
      if (expectedProtocol && response.protocolVersion !== expectedProtocol) {
        return {
          success: false,
          error: `录制协议不匹配：期望 ${expectedProtocol}，收到 ${response.protocolVersion ?? 'unknown'}`,
        };
      }
      return {
        success: true,
        active: true,
        protocolVersion: response.protocolVersion ?? expectedProtocol ?? '1.0.0',
      };
    }

    case 'STOP_RECORDING': {
      const session = await loadActiveRecording();
      const tab = session ? { id: session.tabId } : await getActiveTab();
      if (!tab?.id) {
        return { success: false, error: '没有活动标签页' };
      }
      // A full-page navigation replaces the document and its isolated content
      // script. Ensure the idempotent singleton is attached to the current
      // top-frame document before asking it to drain and stop the recording.
      await ensureContentScript(tab.id);
      // Draining a very large recording (heavy React pages can accumulate tens
      // of megabytes of snapshots) can stall the message channel long past any
      // reasonable UI wait. Bound the drain and fall back to the durable disk
      // checkpoint so STOP always answers instead of hanging the popup.
      const STOP_DRAIN_TIMEOUT_MS = 30000;
      let response: {
        recording?: PageAgentRecording;
        success?: boolean;
        error?: string;
      } | undefined;
      let drainTimedOut = false;
      try {
        response = (await Promise.race([
          sendToContentScript(tab.id, { action: 'STOP_RECORDING' }),
          new Promise((_, reject) => setTimeout(() => reject(new Error('stop-drain-timeout')), STOP_DRAIN_TIMEOUT_MS)),
        ])) as typeof response;
      } catch (drainError) {
        const messageText = drainError instanceof Error ? drainError.message : String(drainError);
        if (messageText !== 'stop-drain-timeout') throw drainError;
        drainTimedOut = true;
        const checkpoint = await chrome.storage.local.get(recordingCheckpointKey(tab.id)).catch(() => undefined);
        const checkpointState = checkpoint?.[recordingCheckpointKey(tab.id)] as { recording?: PageAgentRecording } | undefined;
        if (!checkpointState?.recording) {
          return {
            success: false,
            error: '停止录制超时：录制数据过大且没有可用的检查点，请重试或关闭该标签页后重新录制',
          };
        }
        response = { recording: checkpointState.recording, success: true };
      }
      if (drainTimedOut && response?.recording) {
        // The checkpoint snapshot lags the live buffer; mark the recording as
        // not cleanly terminated so downstream gates treat it honestly.
        const prior = response.recording.termination;
        response.recording.termination = {
          reason: prior?.reason ?? 'user',
          message: prior?.message ?? 'stopped via checkpoint fallback after drain timeout',
          timestamp: prior?.timestamp ?? Date.now(),
          complete: false,
        };
      }
      // Don't trust the content-script response shape: if no recording was
      // returned, surface the error instead of storing undefined and reporting
      // success.
      if (!response?.recording) {
        return { success: false, error: response?.error ?? '录制未返回有效数据' };
      }
      const recording = response.recording;
      // Cross-origin merge artifacts can leave a stale 'final' snapshot
      // mid-list; the recording contract requires the final snapshot last.
      // Reorder defensively before persisting.
      if (Array.isArray(recording.snapshots)) {
        const finalIndex = recording.snapshots.findIndex((s: { phase?: string }) => s.phase === 'final');
        if (finalIndex !== -1 && finalIndex !== recording.snapshots.length - 1) {
          const [finalSnapshot] = recording.snapshots.splice(finalIndex, 1);
          recording.snapshots.push(finalSnapshot);
        }
      }
      const expectedProtocol = typeof session?.options.protocolVersion === 'string'
        ? session.options.protocolVersion
        : undefined;
      if (expectedProtocol && recording.version !== expectedProtocol) {
        return {
          success: false,
          error: `录制协议不匹配：期望 ${expectedProtocol}，收到 ${recording.version}`,
        };
      }
      await recordingStore.set(recording);
      await saveActiveRecording(null);
      // D-3: clear the disk-backed checkpoint now that the final recording is
      // durably persisted to recordingStore (IndexedDB).
      if (tab.id != null) {
        await chrome.storage.local.remove(recordingCheckpointKey(tab.id)).catch(() => undefined);
      }
      let persistenceWarning: string | undefined;
      if (recording.version === '2.0.0' && recording.termination?.complete) {
        try {
          await persistRecordingV2(recording);
        } catch (error) {
          persistenceWarning = error instanceof Error ? error.message : String(error);
        }
      }
      return { success: true, recording, persistenceWarning };
    }

    case 'GET_LAST_RECORDING': {
      const recording = await recordingStore.get();
      return { recording };
    }

    case 'UPDATE_RECORDING_MARKS': {
      const payload = message.payload as { marks?: unknown } | undefined;
      if (!validPageMarkArray(payload?.marks)) {
        return { success: false, error: 'invalid page marks' };
      }
      const recording = await recordingStore.get();
      if (!recording || recording.version !== '2.0.0') {
        return { success: false, error: '没有可更新的语义录制' };
      }
      recording.marks = payload.marks;
      await recordingStore.set(recording);
      await chrome.storage.session.remove([
        REQUIREMENT_CANDIDATE_JOB_KEY,
        REQUIREMENT_NORMALIZE_JOB_KEY,
        DSL_WORKFLOW_SESSION_KEY,
      ]);
      return { success: true, marks: recording.marks };
    }

    case 'GENERATE_RULE': {
      const recording = await recordingStore.get();
      if (!recording) {
        return { success: false, error: '没有可用录制' };
      }
      const readinessError = await ensureRecordingReadyForGeneration(recording);
      if (readinessError) return { success: false, error: readinessError };
      const yaml = convertToYaml(recording, { ruleIdPrefix: 'ext' });
      await chrome.storage.session.set({ lastRuleYaml: yaml });
      return { success: true, yaml };
    }

    case 'DOWNLOAD_RULE': {
      const result = await chrome.storage.session.get(['lastRuleYaml']);
      const yaml = result.lastRuleYaml as string | undefined;
      if (!yaml) {
        return { success: false, error: '没有可用规则' };
      }
      const recording = await recordingStore.get();
      // Sanitize domain: strip path separators and other filename-unsafe chars
      // so a crafted/corrupted recording.meta.domain cannot traverse paths.
      const rawDomain = recording?.meta?.domain || 'unknown';
      const domain = rawDomain.replace(/[/\\]+/g, '-').replace(/[^\w.-]/g, '_');
      const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
      const filename = `opencrawler-rule-${domain}-${timestamp}.yaml`;
      const blob = new Blob([yaml], { type: 'text/yaml' });
      const url = URL.createObjectURL(blob);
      try {
        const downloadId = await chrome.downloads.download({ url, filename, saveAs: false });
        return { success: true, downloadId };
      } finally {
        // The download API copies the data; release the blob URL now to avoid
        // leaking it for the lifetime of the service worker.
        URL.revokeObjectURL(url);
      }
    }

    case 'GET_REQUIREMENT_WORKFLOW': {
      // WI-1: resume path — poll an existing job without creating a new one.
      // Used by restoreFromReconciledJobs when the wizard reopens after SW
      // eviction with a live candidate/normalize job already in flight.
      const payload = message.payload as { startCandidates?: boolean; resumeJobId?: string } | undefined;
      if (payload?.resumeJobId) {
        try {
          return requirementJobResult(await pollRequirementJob(payload.resumeJobId));
        } catch (error) {
          return {
            success: false,
            workflowV2: true,
            error: error instanceof Error ? error.message : String(error),
          };
        }
      }
      if (!serverConfig.baseUrl) return { success: true, workflowV2: false };
      const capabilities = await loadCapabilities();
      if (!capabilities?.features?.workflowV2) {
        return { success: true, workflowV2: false };
      }
      // Candidate generation is the most expensive workflow phase, so it only
      // runs on explicit request. Without startCandidates this is a capability
      // probe: report workflowV2 and resume a stored job, but never create one.
      const startCandidates = payload?.startCandidates === true;
      try {
        const { recording, recordingId } = await requirementRecording();
        const marksHash = marksFingerprint(recording);
        const marksOverride = marksOverrideForServer(recording, capabilities);
        const stored = await chrome.storage.session.get(REQUIREMENT_CANDIDATE_JOB_KEY);
        const resumable = stored[REQUIREMENT_CANDIDATE_JOB_KEY] as { recordingId?: string; marksHash?: string; jobId?: string } | undefined;
        let jobId = resumable?.recordingId === recordingId && resumable.marksHash === marksHash ? resumable.jobId : undefined;
        if (!jobId) {
          if (!startCandidates) {
            return { success: true, workflowV2: true };
          }
          const submitted = await requirementRequest<RequirementJobResponse>(
            `/api/v1/recordings/${encodeURIComponent(recordingId)}/requirement-jobs`,
            { method: 'POST', body: JSON.stringify(marksOverride === undefined ? {} : { marksOverride }) },
            LLM_FETCH_TIMEOUT_MS,
          );
          jobId = submitted.job?.id;
          if (!jobId) throw new Error('服务端未返回采集需求任务 ID');
          await chrome.storage.session.set({ [REQUIREMENT_CANDIDATE_JOB_KEY]: { recordingId, marksHash, jobId } });
        }
        return requirementJobResult(await pollRequirementJob(jobId));
      } catch (error) {
        return { success: false, workflowV2: true, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'NORMALIZE_REQUIREMENT': {
      try {
        const { recording, recordingId } = await requirementRecording();
        const marksHash = marksFingerprint(recording);
        const marksOverride = marksOverrideForServer(recording, lastCapabilities);
        const payload = message.payload as {
          requirement?: unknown;
          customText?: string;
          candidateJobId?: string;
          candidateId?: string;
        } | undefined;
        const requestBody: Record<string, unknown> = {
          requirement: payload?.requirement,
          customText: payload?.customText,
          candidateJobId: payload?.candidateJobId,
          candidateId: payload?.candidateId,
        };
        if (marksOverride !== undefined) requestBody.marksOverride = marksOverride;
        const submitted = await requirementRequest<RequirementJobResponse>(
          `/api/v1/recordings/${encodeURIComponent(recordingId)}/requirement-jobs/normalize`,
          {
            method: 'POST',
            body: JSON.stringify(requestBody),
          },
          LLM_FETCH_TIMEOUT_MS,
        );
        const jobId = submitted.job?.id;
        if (!jobId) throw new Error('服务端未返回规范化任务 ID');
        await chrome.storage.session.set({ [REQUIREMENT_NORMALIZE_JOB_KEY]: { recordingId, marksHash, jobId } });
        return requirementJobResult(await pollRequirementJob(jobId));
      } catch (error) {
        return { success: false, workflowV2: true, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'RETRY_REQUIREMENT_JOB': {
      const payload = message.payload as { jobId?: string } | undefined;
      if (!payload?.jobId) return { success: false, workflowV2: true, error: '缺少采集需求任务 ID' };
      try {
        const response = await requirementRequest<RequirementJobResponse>(
          `/api/v1/requirement-jobs/${encodeURIComponent(payload.jobId)}/retry`,
          { method: 'POST', body: '{}' },
        );
        const jobId = response.job?.id ?? payload.jobId;
        return requirementJobResult(await pollRequirementJob(jobId));
      } catch (error) {
        return { success: false, workflowV2: true, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'CONFIRM_REQUIREMENT': {
      const payload = message.payload as { requirementId?: string } | undefined;
      if (!payload?.requirementId) return { success: false, workflowV2: true, error: '缺少采集需求 ID' };
      try {
        const response = await requirementRequest<{ requirement?: unknown }>(
          `/api/v1/requirements/${encodeURIComponent(payload.requirementId)}/confirm`,
          { method: 'POST', body: '{}' },
        );
        return { success: true, workflowV2: true, requirement: response.requirement };
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        // Older servers reject a repeat confirm of an already-confirmed
        // requirement. Treat that as success by re-reading the confirmed
        // requirement so the wizard can resume forward instead of dead-ending.
        if (message.includes('already confirmed')) {
          try {
            const existing = await requirementRequest<{ requirement?: unknown }>(
              `/api/v1/requirements/${encodeURIComponent(payload.requirementId)}`,
            );
            if (existing.requirement) {
              return { success: true, workflowV2: true, requirement: existing.requirement };
            }
          } catch {
            // fall through to the original error
          }
        }
        return { success: false, workflowV2: true, error: message };
      }
    }

    case 'CREATE_DSL_WORKFLOW': {
      const payload = message.payload as { requirementId?: string; browserProfileId?: string } | undefined;
      if (!payload?.requirementId || !payload.browserProfileId?.trim()) {
        return { success: false, error: '缺少已确认需求或浏览器配置引用' };
      }
      try {
        const { recording } = await requirementRecording();
        const baseline = convert(recording, { ruleIdPrefix: 'ext' });
        const submitted = await requirementRequest<DSLWorkflowResponse>(
          `/api/v1/requirements/${encodeURIComponent(payload.requirementId)}/dsl-workflows`,
          {
            method: 'POST',
            body: JSON.stringify({
              browserProfileId: payload.browserProfileId.trim(),
              baselineRule: baseline,
            }),
          },
          LLM_FETCH_TIMEOUT_MS,
        );
        const workflowId = submitted.workflow?.id;
        if (!workflowId) throw new Error('服务端未返回 DSL 工作流 ID');
        await rememberDSLWorkflow(submitted.workflow!, undefined, baseline);
        const workflow = await pollDSLWorkflow(workflowId);
        await rememberDSLWorkflow(workflow, undefined, baseline);
        return dslWorkflowResult(workflow, { job: submitted.job });
      } catch (error) {
        return { success: false, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'GET_DSL_WORKFLOW_BASELINE': {
      const payload = message.payload as { workflowId?: string } | undefined;
      if (!payload?.workflowId) {
        return { success: false, error: '缺少 DSL 工作流 ID' };
      }
      const baseline = await loadWorkflowBaseline(payload.workflowId);
      if (!baseline) {
        return { success: false, error: '该工作流的录制基线已不可用（浏览器会话已重置）；请重新录制并生成' };
      }
      return { success: true, baselineRule: baseline };
    }

    case 'CORRECT_DSL_WORKFLOW': {
      const payload = message.payload as { workflowId?: string; rule?: unknown } | undefined;
      if (!payload?.workflowId || !payload.rule) {
        return { success: false, error: '缺少工作流 ID 或完整规则' };
      }
      try {
        // The server validates the corrected rule against the recording-
        // derived baseline (id/version/entry/domain must be preserved) and
        // runs the same safety gates as provider output. The PUT moves the
        // workflow to awaiting_replay for the normal replay flow.
        const response = await requirementRequest<DSLWorkflowResponse>(
          `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}/provisional`,
          { method: 'PUT', body: JSON.stringify({ rule: payload.rule }) },
          LLM_FETCH_TIMEOUT_MS,
        );
        const workflow = response.workflow;
        if (!workflow) throw new Error('服务端未返回工作流');
        const baseline = await loadWorkflowBaseline(payload.workflowId);
        await rememberDSLWorkflow(workflow, undefined, baseline);
        return dslWorkflowResult(workflow);
      } catch (error) {
        return { success: false, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'RESUME_DSL_WORKFLOW': {
      try {
        // SW eviction mid-candidates/normalize job orphans the server-side
        // job: the workflow session key is written later, so consult the job
        // keys first and surface their status to the wizard for re-entry.
        const orphanedJobs = await reconcileOrphanedRequirementJobs();
        const stored = await chrome.storage.session.get(DSL_WORKFLOW_SESSION_KEY);
        const session = stored[DSL_WORKFLOW_SESSION_KEY] as {
          workflowId?: string;
          replayId?: string;
          requirementId?: string;
          recordingId?: string;
        } | undefined;
        if (!session?.workflowId) return { success: true, active: false, ...orphanedJobs };
        // A stored workflow is only resumable for the recording it was created
        // from. When a newer recording exists, the stored workflow is stale
        // (e.g. already approved) and the wizard must start the requirement
        // phase for the new recording instead of resuming the old terminal state.
        if (session.recordingId) {
          try {
            const { recordingId } = await requirementRecording();
            if (session.recordingId !== recordingId) {
              await chrome.storage.session.remove(DSL_WORKFLOW_SESSION_KEY);
              return { success: true, active: false, ...orphanedJobs };
            }
          } catch {
            // Without a readable current recording the session cannot be
            // validated; fall through to the legacy resume behavior.
          }
        }
        const workflow = await pollDSLWorkflow(session.workflowId);
        if (workflow.id !== session.workflowId
          || (session.requirementId && workflow.requirementId !== session.requirementId)
          || (session.recordingId && workflow.recordingId !== session.recordingId)) {
          throw new Error('服务端返回的 DSL 工作流与恢复会话不匹配');
        }
        await rememberDSLWorkflow(workflow, session.replayId);
        const requirement = workflowCanEnterReplay(workflow)
          ? await confirmedWorkflowRequirement(workflow)
          : undefined;
        return dslWorkflowResult(workflow, {
          active: true,
          replayId: session.replayId,
          ...(requirement ? { requirement } : {}),
          ...orphanedJobs,
        });
      } catch (error) {
        return { success: false, active: true, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'START_DSL_REPLAY': {
      const payload = message.payload as { workflowId?: string } | undefined;
      if (!payload?.workflowId) return { success: false, error: '缺少 DSL 工作流 ID' };
      try {
        // WI-2: if a replayId is already persisted and the workflow is still
        // in 'replaying' status, reuse the existing attempt instead of POSTing
        // /replays. The server remains the source of truth (it would refuse
        // the concurrent POST anyway), but skipping the round-trip avoids a
        // duplicate-attempt error path and keeps the wizard's replayAttemptId
        // stable across SW-eviction recovery.
        const stored = await chrome.storage.session.get(DSL_WORKFLOW_SESSION_KEY);
        const session = stored[DSL_WORKFLOW_SESSION_KEY] as
          | { workflowId?: string; replayId?: string }
          | undefined;
        if (session?.replayId && session.workflowId === payload.workflowId) {
          const existing = await requirementRequest<DSLWorkflowResponse>(
            `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}`,
          );
          if (existing.workflow?.status === 'replaying') {
            return {
              success: true,
              replay: {
                id: session.replayId,
                status: 'running',
                workflowId: payload.workflowId,
                // Synthesized for the reuse path: the wizard only reads `id` today, but
                // include these so the object matches DSLReplayAttempt structurally and
                // any future reader gets safe defaults instead of undefined.
                sequence: 0,
                outputValid: false,
              },
              workflow: existing.workflow,
            };
          }
        }
        const response = await requirementRequest<DSLReplayResponse>(
          `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}/replays`,
          { method: 'POST', body: '{}' },
        );
        if (!response.replay?.id) throw new Error('服务端未返回回放尝试 ID');
        const workflowResponse = await requirementRequest<DSLWorkflowResponse>(
          `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}`,
        );
        if (workflowResponse.workflow) await rememberDSLWorkflow(workflowResponse.workflow, response.replay.id);
        return { success: true, replay: response.replay, workflow: workflowResponse.workflow };
      } catch (error) {
        return { success: false, error: error instanceof Error ? error.message : String(error) };
      }
    }

    case 'COMPLETE_DSL_REPLAY': {
      const payload = message.payload as {
        workflowId?: string;
        replayId?: string;
        succeeded?: boolean;
        diagnostics?: unknown;
        output?: unknown;
        artifacts?: unknown[];
        errorCode?: string;
        errorMessage?: string;
      } | undefined;
      if (!payload?.workflowId || !payload.replayId) {
        return { success: false, error: '缺少 DSL 工作流或回放尝试 ID' };
      }
      try {
        const response = await requirementRequest<DSLReplayResponse>(
          `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}/replays/${encodeURIComponent(payload.replayId)}/complete`,
          {
            method: 'POST',
            body: JSON.stringify({
              succeeded: payload.succeeded === true,
              diagnostics: payload.diagnostics,
              output: payload.output,
              artifacts: payload.artifacts,
              errorCode: payload.errorCode,
              errorMessage: payload.errorMessage,
            }),
          },
          LLM_FETCH_TIMEOUT_MS,
        );
        const workflow = response.repairJob
          ? await pollDSLWorkflow(payload.workflowId)
          : (await requirementRequest<DSLWorkflowResponse>(
            `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}`,
          )).workflow;
        if (!workflow) throw new Error('服务端未返回更新后的 DSL 工作流');
        await rememberDSLWorkflow(workflow, payload.replayId);
        return dslWorkflowResult(workflow, { replay: response.replay, repairJob: response.repairJob });
      } catch (error) {
        const code = error instanceof ServerRequestError ? error.code : undefined;
        return {
          success: false,
          error: error instanceof Error ? error.message : String(error),
          ...(code ? { code } : {}),
        };
      }
    }

    case 'CONFIRM_DSL_WORKFLOW': {
      const payload = message.payload as { workflowId?: string } | undefined;
      if (!payload?.workflowId) return { success: false, error: '缺少 DSL 工作流 ID' };
      try {
        const response = await requirementRequest<{ ruleVersion?: { ruleId?: string; version?: number } }>(
          `/api/v1/dsl-workflows/${encodeURIComponent(payload.workflowId)}/confirm`,
          { method: 'POST', body: '{}' },
        );
        if (!response.ruleVersion?.ruleId || !response.ruleVersion.version) {
          throw new Error('服务端未返回不可变规则版本');
        }
        const stored = await chrome.storage.session.get(DSL_WORKFLOW_SESSION_KEY);
        const session = stored[DSL_WORKFLOW_SESSION_KEY] as Record<string, unknown> | undefined;
        await chrome.storage.session.set({
          [DSL_WORKFLOW_SESSION_KEY]: { ...session, workflowId: payload.workflowId, approved: true },
        });
        await closeRetainedReplayTab();
        return { success: true, ruleVersion: response.ruleVersion };
      } catch (error) {
        const code = error instanceof ServerRequestError ? error.code : undefined;
        const blockingFlags = error instanceof ServerRequestError ? error.blockingFlags : undefined;
        return {
          success: false,
          error: error instanceof Error ? error.message : String(error),
          ...(code ? { code } : {}),
          ...(blockingFlags && blockingFlags.length ? { blocking_flags: blockingFlags } : {}),
        };
      }
    }

    case 'PREDICT_INTENT': {
      const payload = message.payload as { recording?: PageAgentRecording } | undefined;
      let recording: PageAgentRecording | undefined = payload?.recording;
      if (!recording) {
        recording = await recordingStore.get() ?? undefined;
      }
      if (!recording) {
        return { success: false, error: '没有可用录制' };
      }
      const readinessError = await ensureRecordingReadyForGeneration(recording);
      if (readinessError) return { success: false, error: readinessError };
      // H-3: await cold-start config load before checking serverConfig.baseUrl.
      await ensureServerConfigLoaded();
      if (!serverConfig.baseUrl) {
        return { success: false, error: '未配置服务端地址' };
      }
      const baseUrl = normalizeBaseUrl(serverConfig.baseUrl);
      const traceId = generateTraceId();
      const adminHeaders = buildAdminHeaders(traceId);
      try {
        const response = await fetchWithTimeout(
          `${baseUrl}/admin/rules/predict-intent`,
          {
            method: 'POST',
            headers: adminHeaders,
            body: JSON.stringify({ recording }),
          },
          LLM_FETCH_TIMEOUT_MS,
        );
        if (!response.ok) {
          const text = await response.text().catch(() => '');
          const errorDetail = `意图预测失败：${response.status} ${text} (traceId: ${traceId})`;
          console.warn('[background] PREDICT_INTENT failed:', errorDetail);
          await sendLog(traceId, 'error', 'predict intent failed', { status: response.status, body: text }).catch(() => undefined);
          return { success: false, error: errorDetail, traceId };
        }
        const data = await response.json() as {
          candidates?: unknown[];
          fallbackIntent?: unknown;
          model?: string;
          cacheHit?: boolean;
        };
        await sendLog(traceId, 'info', 'intent predicted', {
          model: data.model,
          cacheHit: data.cacheHit,
          count: Array.isArray(data.candidates) ? data.candidates.length : 0,
        }).catch(() => undefined);
        return { success: true, ...data };
      } catch (err) {
        const errorMessage = isAbortError(err) ? '请求超时' : err instanceof Error ? err.message : String(err);
        const errorDetail = `意图预测请求失败：${errorMessage} (traceId: ${traceId})`;
        console.warn('[background] PREDICT_INTENT request failed:', errorDetail);
        await sendLog(traceId, 'error', 'predict intent request failed', { error: errorMessage }).catch(() => undefined);
        return { success: false, error: errorDetail, traceId };
      }
    }

    case 'GENERATE_DSL_FROM_INTENT': {
      const recording = await recordingStore.get();
      if (!recording) {
        return { success: false, error: '没有可用录制' };
      }
      const readinessError = await ensureRecordingReadyForGeneration(recording);
      if (readinessError) return { success: false, error: readinessError };
      const payload = message.payload as { intent?: IntentCandidate } | undefined;
      const intent = payload?.intent;
      if (!intent) {
        return { success: false, error: '未选择意图' };
      }
      const baseline = convert(recording, { ruleIdPrefix: 'ext' });
      const enhanced = enhanceWithIntent(baseline, intent);
      const yaml = writeYaml(enhanced);
      await chrome.storage.session.set({ lastRuleYaml: yaml, lastRule: enhanced });
      return { success: true, rule: enhanced, yaml };
    }

    case 'GENERATE_DSL_FROM_INTENT_SERVER': {
      const recording = await recordingStore.get();
      if (!recording) {
        return { success: false, error: '没有可用录制' };
      }
      const readinessError = await ensureRecordingReadyForGeneration(recording);
      if (readinessError) return { success: false, error: readinessError };
      // H-3: await cold-start config load before checking serverConfig.baseUrl.
      await ensureServerConfigLoaded();
      if (!serverConfig.baseUrl) {
        return { success: false, error: '未配置服务端地址' };
      }
      const payload = message.payload as { intent?: IntentCandidate; customDescription?: string } | undefined;
      const baseline = convert(recording, { ruleIdPrefix: 'ext' });
      const baseUrl = normalizeBaseUrl(serverConfig.baseUrl);
      const traceId = generateTraceId();
      const adminHeaders = buildAdminHeaders(traceId);
      try {
        const response = await fetchWithTimeout(
          `${baseUrl}/admin/rules/generate-from-intent`,
          {
            method: 'POST',
            headers: adminHeaders,
            body: JSON.stringify({
              recording,
              baselineRule: baseline,
              intent: payload?.intent,
              customDescription: payload?.customDescription,
            }),
          },
          LLM_FETCH_TIMEOUT_MS,
        );
        if (!response.ok) {
          const text = await response.text().catch(() => '');
          const errorDetail = `服务端生成失败：${response.status} ${text} (traceId: ${traceId})`;
          console.warn('[background] GENERATE_DSL_FROM_INTENT_SERVER failed:', errorDetail);
          await sendLog(traceId, 'error', 'generate from intent failed', { status: response.status, body: text }).catch(() => undefined);
          return { success: false, error: errorDetail, traceId };
        }
        const data = (await response.json()) as { rule?: Rule; yaml?: string };
        await chrome.storage.session.set({ lastRule: data.rule, lastRuleYaml: data.yaml });
        await sendLog(traceId, 'info', 'generated rule from intent', { ruleId: (data.rule as Record<string, string> | undefined)?.id }).catch(() => undefined);
        return { success: true, ...data };
      } catch (err) {
        const errorMessage = isAbortError(err) ? '请求超时' : err instanceof Error ? err.message : String(err);
        const errorDetail = `生成 DSL 请求失败：${errorMessage} (traceId: ${traceId})`;
        console.warn('[background] GENERATE_DSL_FROM_INTENT_SERVER request failed:', errorDetail);
        await sendLog(traceId, 'error', 'generate from intent request failed', { error: errorMessage }).catch(() => undefined);
        return { success: false, error: errorDetail, traceId };
      }
    }

    case 'UPLOAD_CONFIRMED_RULE': {
      const result = await chrome.storage.session.get(['lastRule', 'lastRuleYaml']);
      const rule = result.lastRule as Rule | undefined;
      if (!rule) {
        return { success: false, error: '没有已确认的规则' };
      }
      if (!serverConfig.baseUrl) {
        return { success: false, error: '未配置服务端地址' };
      }
      const baseUrl = normalizeBaseUrl(serverConfig.baseUrl);
      const traceId = generateTraceId();
      const adminHeaders = buildAdminHeaders(traceId);
      const ruleToSave = { ...rule, approvalStatus: 'pending', source: 'pageagent' } as Rule & { approvalStatus: string; source: string };
      try {
        const response = await fetchWithTimeout(`${baseUrl}/admin/rules`, {
          method: 'POST',
          headers: adminHeaders,
          body: JSON.stringify(ruleToSave),
        });
        if (!response.ok) {
          const text = await response.text().catch(() => '');
          await sendLog(traceId, 'error', 'confirmed rule upload failed', { status: response.status, body: text }).catch(() => undefined);
          return { success: false, error: `规则保存失败：${response.status} ${text}` };
        }
        await sendLog(traceId, 'info', 'confirmed rule saved as pending', { ruleId: rule.id }).catch(() => undefined);
        return { success: true, ruleId: rule.id };
      } catch (err) {
        const errorMessage = err instanceof Error ? err.message : String(err);
        await sendLog(traceId, 'error', 'confirmed rule upload request failed', { error: errorMessage }).catch(() => undefined);
        return { success: false, error: `规则保存请求失败：${errorMessage}` };
      }
    }

    case 'ENHANCE_RULE': {
      const recording = await recordingStore.get();
      if (!recording) {
        return { success: false, error: '没有可用录制' };
      }
      const readinessError = await ensureRecordingReadyForGeneration(recording);
      if (readinessError) return { success: false, error: readinessError };
      // H-3: await cold-start config load before checking serverConfig.baseUrl.
      await ensureServerConfigLoaded();
      if (!serverConfig.baseUrl) {
        return { success: false, error: '未配置服务端地址' };
      }
      const baseline = convert(recording, { ruleIdPrefix: 'ext' });
      const preprocessed = preprocess(recording);
      const payload = message.payload as { userHint?: string } | undefined;
      const traceId = generateTraceId();
      const baseUrl = normalizeBaseUrl(serverConfig.baseUrl);
      const adminHeaders = buildAdminHeaders(traceId);
      try {
        const response = await fetchWithTimeout(
          `${baseUrl}/admin/rules/enhance`,
          {
            method: 'POST',
            headers: adminHeaders,
            body: JSON.stringify({
              recording: preprocessed,
              baselineRule: baseline,
              userHint: payload?.userHint ?? '',
            }),
          },
          LLM_FETCH_TIMEOUT_MS,
        );
        if (!response.ok) {
          const text = await response.text().catch(() => '');
          await sendLog(traceId, 'error', 'enhance rule failed', { status: response.status, body: text }).catch(() => undefined);
          return { success: false, error: `增强失败：${response.status} ${text}` };
        }
        const data = await response.json() as { ruleId?: string; enhancementId?: string; safetyFlags?: unknown[] };
        await sendLog(traceId, 'info', 'rule enhanced', { ruleId: data.ruleId, enhancementId: data.enhancementId }).catch(() => undefined);
        return { success: true, ruleId: data.ruleId ?? '', safetyFlags: data.safetyFlags ?? [] };
      } catch (err) {
        const errorMessage = isAbortError(err) ? '请求超时' : err instanceof Error ? err.message : String(err);
        await sendLog(traceId, 'error', 'enhance rule request failed', { error: errorMessage }).catch(() => undefined);
        return { success: false, error: `增强请求失败：${errorMessage}` };
      }
    }

    case 'SET_SERVER_CONFIG': {
      const payload = message.payload as ServerConfig & ServerKeys & { rememberSession?: boolean };
      await saveServerConfig({ baseUrl: payload.baseUrl });
      lastCapabilities = null;
      if (payload.rememberSession !== false) {
        await saveServerKeys({ apiKey: payload.apiKey, adminApiKey: payload.adminApiKey });
      } else {
        await chrome.storage.session.remove('serverKeys');
        serverKeys = {};
      }
      return { success: true };
    }

    case 'START_REPLAY': {
      const payload = message.payload as {
        rule?: ReplayRule;
        variables?: Record<string, unknown>;
      } | undefined;
      const rule = payload?.rule;
      if (!rule) {
        return { success: false, error: '没有可用规则' };
      }
      const entryUrl = typeof rule.entry === 'string' ? rule.entry : rule.entry?.url;
      if (!entryUrl) {
        return { success: false, error: '规则缺少入口 URL' };
      }
      try {
        assertHttpUrl(entryUrl, '入口 URL');
        assertEntryUrlDomain(entryUrl, rule);
      } catch (err) {
        return { success: false, error: err instanceof Error ? err.message : String(err) };
      }
      // ADV-005: clear any pre-existing session (incl. stale SW-restart
      // recovery state) before starting fresh.
      await closeRetainedReplayTab();
      if (activeReplay) {
        await cleanupReplaySession('failure', 'superseded by new replay', { abortRunner: true, closeTab: true });
      }
      const taskId = generateTaskId();
      const tab = await chrome.tabs.create({ url: entryUrl, active: false });
      if (!tab.id) {
        return { success: false, error: '无法创建回放标签页' };
      }
      const replayTabId = tab.id;
      const domains = ruleDomainList(rule);
      const session: ReplaySession = {
        tabId: replayTabId,
        ruleId: rule.id,
        taskId,
        startedAt: Date.now(),
        hopCount: 0,
        lastProgressAt: Date.now(),
        expectedDomains: domains,
      };
      // Persist session + full payload (coherence F1): __ocReplayAutoResume in
      // Phase 2 will reconstruct the run from chrome.storage.session when the
      // background re-injects after navigation.
      const replayPayload = {
        rule,
        variables: payload?.variables ?? {},
        taskId,
        workerId: 'replay-worker',
      };
      try {
        await chrome.storage.session.set({
          [REPLAY_SESSION_KEY]: {
            tabId: session.tabId,
            ruleId: session.ruleId,
            taskId: session.taskId,
            startedAt: session.startedAt,
            hopCount: session.hopCount,
            lastProgressAt: session.lastProgressAt,
            expectedDomains: session.expectedDomains,
          },
          [replayPayloadKey(taskId)]: replayPayload,
        });
      } catch (err) {
        chrome.tabs.remove(replayTabId).catch(() => undefined);
        return { success: false, error: `持久化回放会话失败：${err instanceof Error ? err.message : String(err)}` };
      }
      activeReplay = session;

      // Phase 2 watchdog (§6): dual chrome.alarms — total (30min cap) + idle
      // (120s no-progress). Both survive SW eviction; cleared by cleanup.
      registerReplayListeners(session);
      await chrome.alarms.create(`replay_total_${taskId}`, {
        delayInMinutes: REPLAY_TOTAL_TIMEOUT_MS / 60_000,
      });
      await chrome.alarms.create(`replay_idle_${taskId}`, {
        delayInMinutes: REPLAY_ALARM_MIN_INTERVAL_MIN,
        periodInMinutes: REPLAY_ALARM_MIN_INTERVAL_MIN,
      });

      // Wait for the entry tab to finish loading, then inject replay-runner
      // and invoke __ocReplayBoot via executeScript({func, args}). No onMessage
      // listener registration race (P0 #1).
      runReplayBoot(replayTabId, replayPayload).catch((err) => {
        const errorMessage = err instanceof Error ? err.message : String(err);
        if (activeReplay?.tabId === replayTabId) {
          void cleanupReplaySession('failure', errorMessage, { abortRunner: true, closeTab: true });
        }
      });

      return { success: true, taskId };
    }

    case 'ABORT_REPLAY': {
      if (!activeReplay) {
        if (await closeRetainedReplayTab()) return { success: true };
        return { success: false, error: '没有正在进行的回放' };
      }
      await cleanupReplaySession('cancelled', '用户取消回放', { abortRunner: true, closeTab: true });
      return { success: true };
    }

    default:
      return { success: false, error: `未知操作：${message.action}` };
  }
}

// H-3: kick off the shared config-load promise at module init. Handlers
// awaiting ensureServerConfigLoaded() will share this in-flight load.
ensureServerConfigLoaded().catch((err) => console.error('[background] failed to load server config:', err));

// Phase 2 §5.2 + §6: chrome.alarms.onAlarm top-level dispatcher. Registered
// UNCONDITIONALLY at module top so it survives SW restarts (would double-
// register if attached inside recoverReplaySessionOnStartup).
//
// SEC-003 namespace assumption: MV3 alarms are extension-internal with no
// cross-extension injection. Authorization depends on no other module in
// this extension creating alarms matching /^replay_(total|idle)_/. The
// wizard keep-alive alarm ('wizard-keep-alive') does not collide.
chrome.alarms.onAlarm.addListener(async (alarm) => {
  const totalMatch = alarm.name.match(/^replay_total_(.+)$/);
  const idleMatch = alarm.name.match(/^replay_idle_(.+)$/);
  const taskId = (totalMatch ?? idleMatch)?.[1];
  if (!taskId) return;
  // Read session from storage — do NOT trust in-memory activeReplay, the SW
  // may have just woken and the alarm is the only signal we have.
  const stored = await chrome.storage.session.get(REPLAY_SESSION_KEY);
  const session = stored[REPLAY_SESSION_KEY] as ReplaySession | undefined;
  if (!session || session.taskId !== taskId) return;
  if (totalMatch) {
    // C-1/C-2: do NOT gate on activeReplay — after SW restart the in-memory
    // reference is null until recovery completes, but the alarm already
    // validated the session against storage. Let cleanupReplaySession take
    // the storage-built session via options.session so cleanup still runs.
    await cleanupReplaySession('failure', '总时长超限（30min）', {
      abortRunner: true,
      closeTab: true,
      session,
    });
    return;
  }
  // idle: periodic — check lastProgressAt and tear down if stuck.
  if (Date.now() - session.lastProgressAt > REPLAY_IDLE_TIMEOUT_MS) {
    await cleanupReplaySession('failure', '空闲超时（120s 无进度）', {
      abortRunner: true,
      closeTab: true,
      session,
    });
  }
});

// Phase 2 §5.2: SW restart recovery. Reconstructs listeners, rearms alarms
// (total uses REMAINING time — F2 fix), and re-triggers onTabUpdated if the
// navigation completed while the SW was dead (ADV-007).
let recoveryInFlight = false;
async function recoverReplaySessionOnStartup(): Promise<void> {
  // SEC-001 idempotency: module-top + onStartup both fire on SW start.
  if (recoveryInFlight || activeReplay) return;
  recoveryInFlight = true;
  try {
    // M-4: release any retained tab whose TTL has expired. This runs before
    // the active-session check so an abandoned tab is reclaimed even when no
    // replay session exists.
    try {
      const retainedStored = await chrome.storage.session.get(RETAINED_REPLAY_TAB_KEY);
      const retained = retainedStored[RETAINED_REPLAY_TAB_KEY] as
        | { tabId?: number; retainedAt?: number }
        | undefined;
      if (retained?.tabId != null && typeof retained.retainedAt === 'number') {
        if (Date.now() - retained.retainedAt > RETAINED_TAB_TTL_MS) {
          await closeRetainedReplayTab().catch(() => undefined);
        }
      }
    } catch {
      // Best-effort cleanup; ignore storage errors.
    }
    const stored = await chrome.storage.session.get(REPLAY_SESSION_KEY);
    const session = stored[REPLAY_SESSION_KEY] as
      | Omit<ReplaySession, 'onTabUpdated' | 'onTabRemoved'>
      | undefined;
    if (!session) return;
    // Stale guard: a session older than the total cap is dead. Clear all 3
    // keys so a fresh START_REPLAY is not blocked by stale state.
    if (Date.now() - session.startedAt > REPLAY_TOTAL_TIMEOUT_MS) {
      await clearReplayStorage(session.taskId);
      return;
    }
    let tab: ChromeTab | undefined;
    try {
      tab = await chrome.tabs.get(session.tabId);
    } catch {
      // Tab was closed while SW was down.
      await clearReplayStorage(session.taskId);
      return;
    }
    const legacyDomain = session.expectedDomain;
    const expectedDomains = Array.isArray(session.expectedDomains)
      ? session.expectedDomains.map(String)
      : (typeof legacyDomain === 'string' && legacyDomain ? [legacyDomain] : []);
    if (expectedDomains.length === 0) {
      await clearReplayStorage(session.taskId);
      return;
    }
    const restored: ReplaySession = { ...session, expectedDomains };
    activeReplay = restored;
    registerReplayListeners(restored);
    // Total alarm uses remaining time so the 30min cap is not reset on SW restart.
    const remainingMs = Math.max(0, REPLAY_TOTAL_TIMEOUT_MS - (Date.now() - session.startedAt));
    const remainingMin = Math.max(REPLAY_ALARM_MIN_INTERVAL_MIN, remainingMs / 60_000);
    await chrome.alarms.create(`replay_total_${session.taskId}`, { delayInMinutes: remainingMin });
    await chrome.alarms.create(`replay_idle_${session.taskId}`, {
      delayInMinutes: REPLAY_ALARM_MIN_INTERVAL_MIN,
      periodInMinutes: REPLAY_ALARM_MIN_INTERVAL_MIN,
    });
    // ADV-007: if the tab completed loading during SW death, fire onTabUpdated
    // immediately so cross-nav resume is not stuck waiting for the next nav.
    if (tab.status === 'complete') {
      void restored.onTabUpdated?.(session.tabId, { status: 'complete' }, tab);
    }
  } finally {
    recoveryInFlight = false;
  }
}

recoverReplaySessionOnStartup().catch((err) =>
  console.error('[background] replay session recovery failed:', err),
);
chrome.runtime.onStartup.addListener(() =>
  recoverReplaySessionOnStartup().catch((err) =>
    console.error('[background] replay session recovery (onStartup) failed:', err),
  ),
);

const WIZARD_KEEP_ALIVE_ALARM_NAME = 'wizard-keep-alive';
const WIZARD_KEEP_ALIVE_INTERVAL_MINUTES = 0.5; // 30 seconds (minimum allowed by alarms API)

function startWizardKeepAlive(): void {
  try {
    chrome.alarms.create(WIZARD_KEEP_ALIVE_ALARM_NAME, {
      periodInMinutes: WIZARD_KEEP_ALIVE_INTERVAL_MINUTES,
    });
  } catch (err) {
    console.warn('[background] failed to create wizard keep-alive alarm:', err);
  }
}

function stopWizardKeepAlive(): void {
  try {
    chrome.alarms.clear(WIZARD_KEEP_ALIVE_ALARM_NAME);
  } catch (err) {
    console.warn('[background] failed to clear wizard keep-alive alarm:', err);
  }
}

// Keep the service worker alive while the intent wizard is open. MV3 may
// terminate an idle worker even when a fetch is in flight, which cancels
// long-running LLM requests from the wizard. A connected port plus a
// periodic alarm keeps the worker alive for the lifetime of the wizard page.
chrome.runtime.onConnect.addListener((port) => {
  if (port.name === 'intent-wizard') {
    currentWizardPort = port;
    startWizardKeepAlive();
    port.onMessage.addListener((message: unknown) => {
      const msg = message as { type?: string };
      if (msg.type === 'ping') {
        try {
          port.postMessage({ type: 'pong' });
        } catch {
          // Port closed between message receipt and response; ignore.
        }
      }
    });
    port.onDisconnect.addListener(() => {
      if (currentWizardPort === port) currentWizardPort = null;
      stopWizardKeepAlive();
    });
  }
});

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === WIZARD_KEEP_ALIVE_ALARM_NAME) {
    // No-op: the alarm event itself keeps the service worker alive.
  }
});

export { handleMessage, recordingState, serverConfig, serverKeys, normalizeBaseUrl, saveServerConfig, saveServerKeys };
