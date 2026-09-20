/**
 * Shared support for the opt-in live full-workflow suite (NON-CI, paid LLM).
 *
 * One scenario run covers the complete product workflow against reality:
 * scripted human demonstration recorded by the extension, requirement via the
 * fast (intent) or on-demand candidates path, real-LLM provisional DSL,
 * one in-extension replay without repairs, immutable rule approval,
 * real-browser WorkerHost task execution, and Admin REST + MCP result
 * retrieval. The driver (scripts/live-workflow-test.ts) boots ONE Go server
 * and ONE extension-loaded Chromium context and runs scenarios sequentially.
 *
 * Phase waits are progress-aware (PR #49/#50 model): a short base timeout is
 * extended by a full 10-minute stall window whenever server-side job progress
 * (chunk counts / attempts / statuses, polled from the Admin job APIs) or
 * wizard-side replay activity moves forward. Job IDs are discovered from the
 * extension service worker's chrome.storage.session, never from page JS
 * internals. There are no whole-scenario retries, so paid LLM calls stay
 * bounded.
 *
 * Security boundaries held throughout: no GM shims, no credentials in page
 * JavaScript, the LLM key never leaves the Node process/server, and captured
 * server diagnostics are redacted before they are written or printed.
 */

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { ChildProcess } from 'child_process';
import { createServer, Server } from 'http';
import { chromium, BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { WorkerHost } from '../../src/worker-host/host';
import { extensionChromiumArgs } from '../extension-chromium-launch';
import { buildTaskInputs } from '../staging-task-inputs';
import type { LocalLLMConfig } from '../local-live-llm-canary';
import { prepareDedicatedProfile, type DedicatedProfileConfig } from './dedicated-profile';
import {
  estimateCostUpperBound,
  estimateRecordingInputTokens,
  type RealSiteCostBudget,
} from './real-site-cost';
import {
  assertPersistedReplaySemanticRows,
  assertStrictQualification,
  snapshotVerificationProgress,
  strictLLMEnvironment,
  strictQualificationJobs,
} from './qualification';
import type { ScenarioVerificationProgress } from './qualification';
import { assertRowsConformToOutputSchema } from './output-schema';
import { sumProviderAttemptCalls } from './provider-call-budget';
import {
  scenarioNavigationWaitUntil,
  type NavigationWaitUntil,
} from './navigation-readiness';
import {
  assertTaskCaseResult,
  assertTaskResultSurfacesAgree,
  assertRevokedCredentialRejected,
  captureTaskOracle,
  executeTaskCasesSequentially,
  resolveScenarioTaskCases,
  type LiveWorkflowTaskTerminalStatus,
  type LiveWorkflowScenarioTaskCase,
  type ResolvedLiveWorkflowScenarioTaskCase,
} from './task-cases';
import {
  ADMIN_API_KEY,
  WORKER_API_KEY,
  buildServer,
  getFreePort,
  killServer,
  removeIfExists,
  startServer,
  waitForHealth,
} from '../e2e-deployment-test';

export const LIVE_WORKER_PROFILE = 'live-workflow-profile';
const BASE_PHASE_TIMEOUT_MS = 180_000;
const STALL_WINDOW_MS = 600_000;
const POLL_INTERVAL_MS = 2_000;
const TASK_TERMINAL_TIMEOUT_MS = 10 * 60_000;
const TASK_RESULT_PAGE_LIMIT = 100;
const RECORDING_PERSIST_TIMEOUT_MS = 120_000;
const MAX_REPAIR_BUDGET = 0;
const DIAGNOSTICS_BUDGET = 8000;
const REQUIREMENT_VALUE_TYPES = new Set(['string', 'number', 'boolean', 'object', 'array']);
const QWEN_BEIJING_MAX_REQUEST_INPUT_TOKENS = 983_616;
const DEEPSEEK_V4_MAX_SERVER_SOURCE_ESTIMATE_TOKENS = 393_216;

// Session-storage keys written by the extension background service worker
// (extension/src/background.ts). The driver reads them to learn which durable
// server-side jobs the wizard is waiting on; it never writes them.
const SESSION_CANDIDATE_JOB_KEY = 'oc_requirement_candidate_job';
const SESSION_NORMALIZE_JOB_KEY = 'oc_requirement_normalize_job';
const SESSION_DSL_WORKFLOW_KEY = 'oc_dsl_workflow';

export type LiveWorkflowTaskCase = LiveWorkflowScenarioTaskCase<Page>;

export interface LiveWorkflowScenario {
  name: string;
  entryUrl: string;
  /** Public pages with long-lived third-party resources may stop at DOM readiness. */
  navigationWaitUntil?: NavigationWaitUntil;
  /** Scenario-scoped network policy installed before opening the target page. */
  prepareContext?: (context: BrowserContext) => Promise<void>;
  /** Scenario-scoped visible-page preparation completed before recording starts. */
  preparePage?: (page: Page) => Promise<void>;
  browserProfileId?: string;
  demo: (page: Page, ensureRecordingReady?: (activePage?: Page) => Promise<void>) => Promise<void>;
  requirement:
    | { mode: 'intent'; customText: string; humanInput?: boolean }
    | { mode: 'candidates'; pick: 'first' | 'human' }
    | { mode: 'structured'; spec: {
      title: string;
      description: string;
      requiredInputs: unknown[];
      optionalInputs: unknown[];
      outputFields: unknown[];
      sampleOutput: Record<string, unknown>;
    } };
  /** Inclusive [min, max] collected-row band across all valid batches. */
  rowBand: [number, number];
  /** Optional semantic assertions beyond generic output-schema conformance. */
  assertRows?: (rows: unknown[], outputSchema: unknown) => void;
  /** Exact-page replay oracle started before execution; must stop promptly when signal aborts. */
  captureReplayOracleDuringExecution?: (
    context: BrowserContext,
    signal: AbortSignal,
  ) => Promise<void>;
  /** Dynamic semantic assertions captured from the exact replay/task page. */
  captureReplayOracle?: (context: BrowserContext) => Promise<void>;
  captureTaskOracle?: (page: Page) => Promise<void>;
  assertReplayRows?: (rows: unknown[], outputSchema: unknown) => void;
  assertTaskRows?: (rows: unknown[], outputSchema: unknown) => void;
  /** Optional normalized-requirement assertions before confirmation. */
  assertRequirement?: (requirement: unknown) => void;
  /** Optional recording-integrity assertions before any provider work. */
  assertRecording?: (recording: PageAgentRecording) => void;
  /** Optional provisional-rule assertions after generation and before replay. */
  assertProvisionalRule?: (rule: unknown) => void;
  /** Optional approved-rule assertions before task creation. */
  assertRule?: (rule: unknown) => void;
  /** Optional headed pause for a real human to enter or choose the requirement. */
  completeHumanRequirement?: (
    intent: Page,
    phase: 'intent-input' | 'candidate-selection',
  ) => Promise<void>;
  /** Fail-closed semantic filter for candidates offered to a human chooser. */
  acceptHumanCandidate?: (requirement: unknown) => boolean;
  /** Scenario fails when summed job input tokens exceed this budget. */
  inputTokenBudget?: number;
  /** Model-priced dollar guard for public real-site scenarios. */
  realSiteCostBudget?: RealSiteCostBudget;
  /** Exact physical provider-call count required by a bounded qualification. */
  expectedProviderCalls?: number;
  /**
   * Optional post-processor for schema-derived task inputs, e.g. to bind a
   * search-like required input to a value the fixture actually contains.
   * This is the backwards-compatible single-task form and cannot be combined
   * with taskCases.
   */
  adjustTaskInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  /**
   * Ordered task matrix executed sequentially against one already-approved
   * immutable rule and one WorkerHost ownership boundary.
   */
  taskCases?: LiveWorkflowTaskCase[];
  /** Optional replay-only binding when generalization requires a different task input. */
  adjustReplayInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  /** Real-site tasks may need a visible browser to avoid headless-only blocks. */
  workerHeadless?: boolean;
}

export interface LiveWorkflowRunOptions {
  llm: LocalLLMConfig;
  headed: boolean;
  /** Reuse one previously encrypted, complete recording instead of recording again. */
  reusableRecording?: PageAgentRecording;
  /** Persist a newly captured recording into the local encrypted fixture store. */
  saveRecording?: (recording: PageAgentRecording) => void;
}

export function assertScenarioProvisionalRule(
  scenario: Pick<LiveWorkflowScenario, 'assertProvisionalRule'>,
  workflow: { status?: unknown; provisionalRule?: unknown } | undefined,
): void {
  if (!scenario.assertProvisionalRule) return;
  assert(
    workflow?.status === 'awaiting_replay',
    `scenario provisional-rule canary requires awaiting_replay, observed ${String(workflow?.status ?? 'unknown')}`,
  );
  assert(workflow?.provisionalRule, 'awaiting-replay workflow omitted its provisional rule');
  scenario.assertProvisionalRule(workflow.provisionalRule);
}

export interface JobReport {
  id: string;
  kind: string;
  status: string;
  chunkCount: number;
  completedChunks: number;
  inputTokens: number;
  outputTokens: number;
  attemptCount: number;
  maxAttempts: number;
  provider: string;
  model: string;
  cacheHit: boolean;
  degraded: boolean;
  safetyFlags: string[];
  errorCode?: string;
  errorMessage?: string;
  /** Completed-job result envelope (candidates / normalized requirement). */
  result?: unknown;
}

export interface WorkflowReport {
  id: string;
  status: string;
  repairCount: number;
  maxRepairs: number;
  lastReplaySequence: number;
  errorCode?: string;
  errorMessage?: string;
  approvedRuleId?: string;
  approvedVersion?: number;
}

export interface TaskSummary {
  caseName: string;
  expectedStatus: LiveWorkflowTaskTerminalStatus;
  taskId: string;
  status: string;
  validBatches: number;
  invalidBatches: number;
  rows: number;
  summary: boolean;
  summaryStatus?: string;
  maxRetries: number;
  retryCount: number;
  executionAttempts: number;
  mcpAgreedWithAdmin: boolean;
  mcpTokenRevoked: boolean;
}

export interface LiveWorkflowReport {
  scenario: string;
  status: 'passed' | 'failed';
  provider: string;
  startedAt: string;
  finishedAt: string;
  durationMs: number;
  phases: Record<string, number>;
  recordingId?: string;
  requirementId?: string;
  ruleId?: string;
  ruleVersion?: number;
  repairCount?: number;
  llm: {
    strict: boolean;
    expectedProvider: string;
    expectedModel: string;
    tokenUsageExposed: boolean;
    inputTokens: number;
    outputTokens: number;
    providerCalls?: number;
    inputTokenBudget?: number;
    costBudgetUSD?: number;
    inputUSDPerMillion?: number;
    outputUSDPerMillion?: number;
    estimatedCostUpperBoundUSD?: number;
    requirementJobs: JobReport[];
    dslJobs: JobReport[];
    workflow?: WorkflowReport;
    replayAttempts: number;
  };
  results: {
    tasks: TaskSummary[];
    summary: boolean;
  };
  mcp: { agreedWithAdmin: boolean; tokenRevoked: boolean };
  error?: string;
  artifactsDir?: string;
}

export function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function isPlainObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function asNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function asOptionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined;
}

function liveLLMRequestTimeout(): string {
  const raw = process.env.AEGIS_LIVE_LLM_REQUEST_TIMEOUT?.trim();
  if (!raw) return '120s';
  const match = /^([1-9][0-9]*)(ms|s|m)$/.exec(raw);
  if (!match) throw new Error('AEGIS_LIVE_LLM_REQUEST_TIMEOUT must be a positive integer duration in ms, s, or m');
  const unitMilliseconds = match[2] === 'm' ? 60_000 : match[2] === 's' ? 1_000 : 1;
  const milliseconds = Number(match[1]) * unitMilliseconds;
  if (!Number.isSafeInteger(milliseconds) || milliseconds >= STALL_WINDOW_MS) {
    throw new Error('AEGIS_LIVE_LLM_REQUEST_TIMEOUT must remain below the 10-minute progress stall window');
  }
  return raw;
}

function liveLLMMaxInputTokens(llm?: LocalLLMConfig): string {
  const raw = process.env.AEGIS_LIVE_LLM_MAX_INPUT_TOKENS?.trim() || '200000';
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error('AEGIS_LIVE_LLM_MAX_INPUT_TOKENS must be a positive integer');
  }
  if (isQwen36FlashBeijing(llm) && value > QWEN_BEIJING_MAX_REQUEST_INPUT_TOKENS) {
    throw new Error(
      `AEGIS_LIVE_LLM_MAX_INPUT_TOKENS must not exceed ${QWEN_BEIJING_MAX_REQUEST_INPUT_TOKENS} for qwen3.6-flash in China (Beijing)`,
    );
  }
  if (isAlibabaBeijingDeepSeekV4(llm)
    && value > DEEPSEEK_V4_MAX_SERVER_SOURCE_ESTIMATE_TOKENS) {
    throw new Error(
      `AEGIS_LIVE_LLM_MAX_INPUT_TOKENS must not exceed `
      + `${DEEPSEEK_V4_MAX_SERVER_SOURCE_ESTIMATE_TOKENS} for DeepSeek V4 in China (Beijing)`,
    );
  }
  return String(value);
}

function liveLLMMaxOutputTokens(): string | undefined {
  const raw = process.env.AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS?.trim();
  if (!raw) return undefined;
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error('AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS must be a positive integer');
  }
  return String(value);
}

function isQwen36FlashBeijing(llm?: LocalLLMConfig): boolean {
  return Boolean(llm
    && (llm.model === 'qwen3.6-flash' || llm.model === 'qwen3.6-flash-2026-04-16')
    && llm.providerHostname.endsWith('.cn-beijing.maas.aliyuncs.com'));
}

function isDirectDeepSeekV4(llm?: LocalLLMConfig): boolean {
  return Boolean(llm
    && llm.providerAdapter === 'openai'
    && llm.providerHostname === 'api.deepseek.com'
    && (llm.model === 'deepseek-v4-flash' || llm.model === 'deepseek-v4-pro'));
}

function isAlibabaBeijingDeepSeekV4(llm?: LocalLLMConfig): boolean {
  return Boolean(llm
    && llm.providerAdapter === 'openai'
    && llm.providerHostname.endsWith('.cn-beijing.maas.aliyuncs.com')
    && (llm.model === 'deepseek-v4-flash' || llm.model === 'deepseek-v4-pro'));
}

function isDeepSeekV4(llm?: LocalLLMConfig): boolean {
  return isDirectDeepSeekV4(llm) || isAlibabaBeijingDeepSeekV4(llm);
}

/** Server environment for a live run: v2 flags + live provider from the key file. */
export function liveServerEnvironment(llm?: LocalLLMConfig): Record<string, string> {
  if (isDirectDeepSeekV4(llm) && new URL(llm!.providerBaseURL).pathname.replace(/\/+$/, '') !== '/beta') {
    throw new Error('DeepSeek V4 live workflow requires AEGIS_LOCAL_LLM_BASE_URL=https://api.deepseek.com/beta for strict tool output');
  }
  const environment: Record<string, string> = {
    FEATURE_RECORDING_V2: 'true',
    FEATURE_WORKFLOW_V2: 'true',
    FEATURE_WORKER_PROTOCOL_V2: 'true',
    FEATURE_MCP: 'true',
    LLM_ENABLED: llm ? 'true' : 'false',
    // startServer inherits ambient LLM_* variables. Always close alternate
    // credential/provider carriers so the reviewed single-provider settings
    // below cannot be shadowed by a stale multi-provider shell config.
    LLM_API_KEY_FILE: '',
    LLM_PROVIDER_CONFIGS: '',
    ...strictLLMEnvironment(),
    LLM_MAX_INPUT_TOKENS: liveLLMMaxInputTokens(llm),
    LLM_JOB_WORKER_INTERVAL: '50ms',
    LLM_REQUEST_TIMEOUT: liveLLMRequestTimeout(),
    LLM_OPENAI_ENABLE_THINKING: (isQwen36FlashBeijing(llm) || isDeepSeekV4(llm)) ? 'false' : '',
    // Strict tool output is provider-side schema enforcement for generated
    // DSL; it must apply to every OpenAI-compatible provider, not one model.
    LLM_OPENAI_STRICT_TOOL_OUTPUT: 'true',
    MAX_REQUEST_BODY_BYTES: '67108864',
    MAX_WORKER_TASKS: '100',
    RATE_LIMIT_PER_SECOND: '2000',
    RATE_LIMIT_BURST: '4000',
    WORKER_RATE_LIMIT_PER_SECOND: '2000',
    WORKER_RATE_LIMIT_BURST: '4000',
    SITE_RATE_LIMIT_PER_SECOND: '2000',
    SITE_RATE_LIMIT_BURST: '4000',
    MCP_RATE_LIMIT_PER_SECOND: '2000',
    MCP_RATE_LIMIT_BURST: '4000',
  };
  const maxOutputTokens = liveLLMMaxOutputTokens();
  if (maxOutputTokens) environment.LLM_MAX_OUTPUT_TOKENS = maxOutputTokens;
  if (llm) {
    environment.LLM_PROVIDER = llm.providerAdapter;
    environment.LLM_API_KEY = llm.apiKey;
    environment.LLM_BASE_URL = llm.providerBaseURL;
    environment.LLM_MODEL = llm.model;
  } else {
    // startServer normally passes ambient LLM_* variables through for paid
    // tests. Blank every credential/config carrier so record-only mode cannot
    // inherit an unrelated shell provider configuration.
    environment.LLM_PROVIDER = '';
    environment.LLM_API_KEY = '';
    environment.LLM_BASE_URL = '';
    environment.LLM_MODEL = '';
    environment.LLM_OPENAI_STRICT_TOOL_OUTPUT = '';
  }
  return environment;
}

/** Suite-observed server 5xx responses; asserted empty at scenario end. */
export interface ServerErrorObservation {
  method: string;
  route: string;
  status: number;
}

export class LiveAPI {
  readonly errors5xx: ServerErrorObservation[] = [];

  constructor(private baseUrl: string) {}

  async call<T>(method: string, route: string, token: string, body?: unknown): Promise<T> {
    const response = await fetch(`${this.baseUrl}${route}`, {
      method,
      headers: {
        Authorization: `Bearer ${token}`,
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    if (response.status >= 500) {
      this.errors5xx.push({ method, route, status: response.status });
    }
    if (!response.ok) throw new Error(`${method} ${route} returned ${response.status}: ${text.slice(0, 500)}`);
    return text ? (JSON.parse(text) as T) : ({} as T);
  }

  admin<T>(method: string, route: string, body?: unknown): Promise<T> {
    return this.call<T>(method, route, ADMIN_API_KEY, body);
  }
}

interface MCPClientState {
  token: string;
  sessionId?: string;
  nextId: number;
}

class MCPHTTPError extends Error {
  constructor(
    method: unknown,
    public readonly status: number,
    responseText: string,
  ) {
    super(`MCP ${String(method)} returned ${status}: ${responseText.slice(0, 500)}`);
    this.name = 'MCPHTTPError';
  }
}

function parseMCPResponse(text: string): any {
  const trimmed = text.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith('{')) return JSON.parse(trimmed);
  const payloads = trimmed.split(/\r?\n/)
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice(5).trim())
    .filter((line) => line && line !== '[DONE]');
  assert(payloads.length > 0, `MCP response was neither JSON nor SSE: ${trimmed.slice(0, 500)}`);
  return JSON.parse(payloads[payloads.length - 1]);
}

async function mcpPost(baseUrl: string, client: MCPClientState, payload: Record<string, unknown>): Promise<any> {
  const response = await fetch(`${baseUrl}/mcp`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${client.token}`,
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
      ...(client.sessionId ? { 'Mcp-Session-Id': client.sessionId } : {}),
    },
    body: JSON.stringify(payload),
  });
  const text = await response.text();
  if (!response.ok) throw new MCPHTTPError(payload.method, response.status, text);
  client.sessionId = response.headers.get('mcp-session-id') ?? client.sessionId;
  return parseMCPResponse(text);
}

export async function initializeMCP(baseUrl: string, token: string): Promise<MCPClientState> {
  const client: MCPClientState = { token, nextId: 1 };
  const initialized = await mcpPost(baseUrl, client, {
    jsonrpc: '2.0', id: client.nextId++, method: 'initialize',
    params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'live-workflow-suite', version: '1' } },
  });
  assert(initialized?.result?.protocolVersion, `MCP initialize returned an invalid response: ${JSON.stringify(initialized)}`);
  await mcpPost(baseUrl, client, { jsonrpc: '2.0', method: 'notifications/initialized', params: {} });
  return client;
}

export async function callMCP(baseUrl: string, client: MCPClientState, name: string, args: Record<string, unknown>): Promise<any> {
  const response = await mcpPost(baseUrl, client, {
    jsonrpc: '2.0', id: client.nextId++, method: 'tools/call', params: { name, arguments: args },
  });
  assert(!response?.result?.isError, `MCP ${name} failed: ${JSON.stringify(response)}`);
  return response?.result?.structuredContent;
}

/**
 * Best-effort read of the extension service worker's session storage. The
 * worker may be briefly unavailable while Chromium idles it; progress polling
 * must tolerate that and simply report no new signal.
 */
async function readSessionStorage(context: BrowserContext): Promise<Record<string, unknown>> {
  const worker = context.serviceWorkers()[0];
  if (!worker) return {};
  try {
    const storage = await worker.evaluate(async () => (globalThis as any).chrome.storage.session.get(null));
    return isPlainObject(storage) ? storage : {};
  } catch {
    return {};
  }
}

interface SessionJobRef {
  recordingId?: string;
  jobId?: string;
}

interface SessionWorkflowRef {
  recordingId?: string;
  workflowId?: string;
  replayId?: string;
}

/**
 * API-polled view of the scenario's durable LLM jobs and DSL workflow.
 *
 * The tracker discovers job/workflow IDs from the extension's session storage
 * (scoped to the scenario's recording ID, so stale entries from a previous
 * scenario in the shared browser cannot leak in) and refreshes their status,
 * chunk progress, and token usage from the Admin job APIs. It is the single
 * progress/terminal-error source for progress-aware phase waits and the token
 * accountant for the suite's cost guard.
 */
export class JobTracker {
  private requirementJobIds = new Set<string>();
  private dslJobIds = new Set<string>();
  private replayIds = new Set<string>();
  private candidatesJobId?: string;
  private workflowId?: string;
  private requirementJobs = new Map<string, JobReport>();
  private dslJobs = new Map<string, JobReport>();
  private workflow?: WorkflowReport;
  private workflowArtifact?: Record<string, unknown>;
  private replayAttempts = new Map<string, Record<string, unknown>>();
  private tokenExposure: 'unknown' | 'exposed' | 'missing' = 'unknown';

  constructor(
    private api: LiveAPI,
    private context: BrowserContext,
    private recordingId: string,
  ) {}

  async poll(): Promise<void> {
    // Once a dedicated profile is handed to WorkerHost, its extension context
    // is intentionally closed. Continue refreshing already-discovered durable
    // IDs from the Admin API for final reporting and failure artifacts.
    const session: Record<string, unknown> = await readSessionStorage(this.context).catch(() => ({}));
    const candidateRef = session[SESSION_CANDIDATE_JOB_KEY] as SessionJobRef | undefined;
    if (candidateRef?.recordingId === this.recordingId && typeof candidateRef.jobId === 'string' && candidateRef.jobId) {
      this.candidatesJobId = candidateRef.jobId;
      this.requirementJobIds.add(candidateRef.jobId);
    }
    const normalizeRef = session[SESSION_NORMALIZE_JOB_KEY] as SessionJobRef | undefined;
    if (normalizeRef?.recordingId === this.recordingId && typeof normalizeRef.jobId === 'string' && normalizeRef.jobId) {
      this.requirementJobIds.add(normalizeRef.jobId);
    }
    const workflowRef = session[SESSION_DSL_WORKFLOW_KEY] as SessionWorkflowRef | undefined;
    if (workflowRef?.recordingId === this.recordingId && typeof workflowRef.workflowId === 'string' && workflowRef.workflowId) {
      this.workflowId = workflowRef.workflowId;
      if (typeof workflowRef.replayId === 'string' && workflowRef.replayId) {
        this.replayIds.add(workflowRef.replayId);
      }
    }
    for (const id of this.requirementJobIds) await this.refreshRequirementJob(id);
    if (this.workflowId) await this.refreshWorkflow(this.workflowId);
    for (const id of this.dslJobIds) await this.refreshDSLJob(id);
    for (const id of this.replayIds) await this.refreshReplay(id);
  }

  private noteTokenExposure(job: Record<string, unknown>): void {
    if (this.tokenExposure !== 'unknown') return;
    this.tokenExposure = typeof job.inputTokens === 'number' ? 'exposed' : 'missing';
  }

  private async refreshRequirementJob(id: string): Promise<void> {
    try {
      const response = await this.api.admin<{ job?: Record<string, unknown> }>(
        'GET', `/api/v1/requirement-jobs/${encodeURIComponent(id)}`,
      );
      const job = response?.job;
      if (!isPlainObject(job)) return;
      this.noteTokenExposure(job);
      this.requirementJobs.set(id, {
        id,
        kind: String(job.kind ?? ''),
        status: String(job.status ?? ''),
        chunkCount: asNumber(job.chunkCount),
        completedChunks: asNumber(job.completedChunks),
        inputTokens: asNumber(job.inputTokens),
        outputTokens: asNumber(job.outputTokens),
        attemptCount: asNumber(job.attemptCount),
        maxAttempts: asNumber(job.maxAttempts),
        provider: String(job.provider ?? ''),
        model: String(job.model ?? ''),
        safetyFlags: Array.isArray(job.safetyFlags) ? job.safetyFlags.map(String) : [],
        cacheHit: Array.isArray(job.safetyFlags) && job.safetyFlags.includes('cache:hit'),
        degraded: Array.isArray(job.safetyFlags) && job.safetyFlags.includes('degraded:true'),
        errorCode: asOptionalString(job.errorCode),
        errorMessage: asOptionalString(job.errorMessage),
        result: job.result,
      });
    } catch {
      // best-effort progress read only
    }
  }

  private async refreshWorkflow(id: string): Promise<void> {
    try {
      const response = await this.api.admin<{ workflow?: Record<string, unknown> }>(
        'GET', `/api/v1/dsl-workflows/${encodeURIComponent(id)}`,
      );
      const workflow = response?.workflow;
      if (!isPlainObject(workflow)) return;
      this.workflowArtifact = {
        id,
        requirementId: workflow.requirementId,
        recordingId: workflow.recordingId,
        status: workflow.status,
        browserProfileId: workflow.browserProfileId,
        currentJobId: workflow.currentJobId,
        repairCount: workflow.repairCount,
        maxRepairs: workflow.maxRepairs,
        provisionalHash: workflow.provisionalHash,
        lastReplaySequence: workflow.lastReplaySequence,
        errorCode: workflow.errorCode,
        errorMessage: workflow.errorMessage,
        provisionalRule: workflow.provisionalRule,
      };
      this.workflow = {
        id,
        status: String(workflow.status ?? ''),
        repairCount: asNumber(workflow.repairCount),
        maxRepairs: asNumber(workflow.maxRepairs),
        lastReplaySequence: asNumber(workflow.lastReplaySequence),
        errorCode: asOptionalString(workflow.errorCode),
        errorMessage: asOptionalString(workflow.errorMessage),
        approvedRuleId: asOptionalString(workflow.approvedRuleId),
        approvedVersion: typeof workflow.approvedVersion === 'number' ? workflow.approvedVersion : undefined,
      };
      if (typeof workflow.currentJobId === 'string' && workflow.currentJobId) {
        this.dslJobIds.add(workflow.currentJobId);
      }
    } catch {
      // best-effort progress read only
    }
  }

  private async refreshDSLJob(id: string): Promise<void> {
    try {
      const response = await this.api.admin<{ job?: Record<string, unknown> }>(
        'GET', `/api/v1/dsl-jobs/${encodeURIComponent(id)}`,
      );
      const job = response?.job;
      if (!isPlainObject(job)) return;
      this.noteTokenExposure(job);
      this.dslJobs.set(id, {
        id,
        kind: String(job.kind ?? ''),
        status: String(job.status ?? ''),
        chunkCount: asNumber(job.chunkCount),
        completedChunks: asNumber(job.completedChunks),
        inputTokens: asNumber(job.inputTokens),
        outputTokens: asNumber(job.outputTokens),
        attemptCount: asNumber(job.attemptCount),
        maxAttempts: asNumber(job.maxAttempts),
        provider: String(job.provider ?? ''),
        model: String(job.model ?? ''),
        safetyFlags: Array.isArray(job.safetyFlags) ? job.safetyFlags.map(String) : [],
        cacheHit: Array.isArray(job.safetyFlags) && job.safetyFlags.includes('cache:hit'),
        degraded: Array.isArray(job.safetyFlags) && job.safetyFlags.includes('degraded:true'),
        errorCode: asOptionalString(job.errorCode),
        errorMessage: asOptionalString(job.errorMessage),
      });
    } catch {
      // best-effort progress read only
    }
  }

  private async refreshReplay(id: string): Promise<void> {
    try {
      const response = await this.api.admin<{ replay?: Record<string, unknown> }>(
        'GET', `/api/v1/dsl-replays/${encodeURIComponent(id)}`,
      );
      if (isPlainObject(response?.replay)) this.replayAttempts.set(id, response.replay);
    } catch {
      // best-effort authenticated diagnostic read only
    }
  }

  private static jobSignature(job: JobReport): string {
    return `${job.id}:${job.kind}:${job.status}:${job.completedChunks}/${job.chunkCount}`
      + `:a${job.attemptCount}:t${job.inputTokens}/${job.outputTokens}:${job.errorCode ?? ''}`;
  }

  /** Composite server-side progress signature; any change counts as progress. */
  signature(): string {
    const requirement = [...this.requirementJobs.values()].map(JobTracker.jobSignature).sort();
    const dsl = [...this.dslJobs.values()].map(JobTracker.jobSignature).sort();
    return JSON.stringify({ requirement, dsl, workflow: this.workflow ?? null });
  }

  terminalErrors(): string {
    const failed: string[] = [];
    for (const job of this.requirementJobs.values()) {
      if (job.status === 'failed') failed.push(`requirement job (${job.kind}) failed: ${job.errorCode ?? ''} ${job.errorMessage ?? ''}`.trim());
    }
    if (this.workflow?.status === 'failed') {
      failed.push(`dsl workflow failed: ${this.workflow.errorCode ?? ''} ${this.workflow.errorMessage ?? ''}`.trim());
    }
    for (const job of this.dslJobs.values()) {
      if (job.status === 'failed') failed.push(`dsl job (${job.kind}) failed: ${job.errorCode ?? ''} ${job.errorMessage ?? ''}`.trim());
    }
    return failed.join(' | ');
  }

  candidatesJobCreated(): boolean {
    return this.candidatesJobId !== undefined;
  }

  candidatesJob(): JobReport | undefined {
    return this.candidatesJobId ? this.requirementJobs.get(this.candidatesJobId) : undefined;
  }

  requirementJobList(): JobReport[] {
    return [...this.requirementJobs.values()];
  }

  normalizeJobs(): JobReport[] {
    return this.requirementJobList().filter((job) => job.kind === 'normalize');
  }

  dslJobList(): JobReport[] {
    return [...this.dslJobs.values()];
  }

  dslWorkflow(): WorkflowReport | undefined {
    return this.workflow;
  }

  dslWorkflowArtifact(): Record<string, unknown> | undefined {
    return this.workflowArtifact;
  }

  replayAttemptList(): Record<string, unknown>[] {
    return [...this.replayAttempts.values()];
  }

  replayAttemptCount(): number {
    return this.replayAttempts.size;
  }

  repairCount(): number {
    return this.workflow?.repairCount ?? 0;
  }

  tokenTotals(): { exposed: boolean; inputTokens: number; outputTokens: number } {
    const jobs = [...this.requirementJobs.values(), ...this.dslJobs.values()];
    return {
      exposed: this.tokenExposure === 'exposed',
      inputTokens: jobs.reduce((sum, job) => sum + job.inputTokens, 0),
      outputTokens: jobs.reduce((sum, job) => sum + job.outputTokens, 0),
    };
  }
}

/** Per-scenario run state threaded through the driver phases. */
interface ScenarioRun {
  scenario: LiveWorkflowScenario;
  taskCases: ResolvedLiveWorkflowScenarioTaskCase<Page>[];
  env: LiveEnvironment;
  artifactsDir: string;
  phases: Record<string, number>;
  recording?: PageAgentRecording;
  recordingId?: string;
  requirementId?: string;
  ruleId?: string;
  ruleVersion?: number;
  repairCount?: number;
  tracker?: JobTracker;
}

const RECORDING_TRACE_EVENT_LIMIT = 256;
const RECORDING_TRACE_TEXT_LIMIT = 160;

export function sanitizeRecordingEventTrace(recording: PageAgentRecording): Record<string, unknown> {
  const events = recording.events.slice(0, RECORDING_TRACE_EVENT_LIMIT).map((event, order) => {
    const trace: Record<string, unknown> = { order, type: event.type, timestamp: event.timestamp };
    if ('index' in event && typeof event.index === 'number') trace.index = event.index;
    if (event.type === 'inputText') {
      const normalized = event.text.replace(/\s+/g, ' ').trim();
      trace.text = normalized.slice(0, RECORDING_TRACE_TEXT_LIMIT);
      trace.textLength = normalized.length;
      trace.textTruncated = normalized.length > RECORDING_TRACE_TEXT_LIMIT;
      if (event.submit === true) trace.submit = true;
    } else if (event.type === 'submitForm' && typeof event.submitterIndex === 'number') {
      trace.submitterIndex = event.submitterIndex;
    } else if (event.type === 'navigate') {
      try {
        const url = new URL(event.url);
        trace.url = { origin: url.origin, pathname: url.pathname };
      } catch {
        trace.url = { invalid: true };
      }
    }
    return trace;
  });
  return {
    version: 1,
    eventCount: recording.events.length,
    retainedEventCount: events.length,
    eventsTruncated: recording.events.length > events.length,
    events,
  };
}

export function writeRecordingEventTraceArtifact(
  artifactsDir: string,
  recording: PageAgentRecording,
): string {
  const target = path.join(artifactsDir, 'recording-events.json');
  fs.writeFileSync(target, JSON.stringify(sanitizeRecordingEventTrace(recording), null, 2));
  return target;
}

function markPhase(run: ScenarioRun, name: string, startedAt: number): void {
  run.phases[name] = (run.phases[name] ?? 0) + (Date.now() - startedAt);
}

async function wizardErrorText(page: Page): Promise<string> {
  const status = page.locator('#status');
  const className = (await status.getAttribute('class').catch(() => '')) ?? '';
  if (!className.includes('error')) return '';
  return ((await status.textContent().catch(() => '')) ?? '').replace(/\s+/g, ' ').trim();
}

/**
 * Progress-aware phase waiter (PR #50 model, driven by the Admin job APIs):
 * succeeds as soon as `check` holds; fails fast on a persistent wizard error
 * or a terminally failed durable job; otherwise waits a base timeout and
 * extends it by a full stall window on every observed forward progress.
 */
export async function waitForPhase(
  run: ScenarioRun,
  page: Page,
  description: string,
  check: () => Promise<boolean>,
  options: { baseTimeoutMs?: number; stallTimeoutMs?: number; extraSignature?: () => Promise<string> } = {},
): Promise<void> {
  const baseTimeoutMs = options.baseTimeoutMs ?? BASE_PHASE_TIMEOUT_MS;
  const stallTimeoutMs = options.stallTimeoutMs ?? STALL_WINDOW_MS;
  let deadline = Date.now() + baseTimeoutMs;
  let lastSignature = '';
  let consecutiveErrors = 0;
  for (;;) {
    if (await check()) return;

    if (run.tracker) {
      await run.tracker.poll();
      const jobErrors = run.tracker.terminalErrors();
      if (jobErrors) {
        throw new Error(`${description}: durable LLM work failed terminally — ${jobErrors}`);
      }
    }
    const wizardError = await wizardErrorText(page);
    if (wizardError) {
      consecutiveErrors += 1;
      if (consecutiveErrors >= 2) {
        throw new Error(`${description}: wizard reported a terminal error — ${wizardError}`);
      }
    } else {
      consecutiveErrors = 0;
    }

    let signature = run.tracker?.signature() ?? '';
    if (options.extraSignature) {
      try {
        signature += `|${await options.extraSignature()}`;
      } catch {
        // best-effort extra signal only
      }
    }
    if (signature && signature !== lastSignature) {
      lastSignature = signature;
      deadline = Date.now() + stallTimeoutMs;
    }
    if (Date.now() >= deadline) {
      const jobSummary = lastSignature ? lastSignature.slice(0, 800) : 'no server-side job progress observed';
      throw new Error(
        `${description}: no progress for ${Math.round(stallTimeoutMs / 60000)} minutes; last server-side state: ${jobSummary}`,
      );
    }
    await sleep(POLL_INTERVAL_MS);
  }
}

async function waitPrimaryText(run: ScenarioRun, page: Page, text: string, description: string): Promise<void> {
  await waitForPhase(run, page, description, async () => {
    const label = (await page.locator('#action-primary').textContent().catch(() => '')) ?? '';
    const disabled = await page.locator('#action-primary').isDisabled().catch(() => true);
    return label.includes(text) && !disabled;
  });
}

async function clickPrimaryWhenEnabled(page: Page): Promise<void> {
  const primary = page.locator('#action-primary');
  await primary.waitFor({ state: 'visible', timeout: 15_000 });
  assert(!(await primary.isDisabled()), 'primary wizard action is disabled');
  await primary.click();
}

async function extensionPopup(context: BrowserContext, extensionId: string, baseUrl: string): Promise<Page> {
  const popup = await context.newPage();
  await popup.goto(`chrome-extension://${extensionId}/popup.html`);
  await popup.waitForSelector('#start-recording');
  await popup.evaluate((config) => {
    (document.getElementById('baseUrl') as HTMLInputElement).value = config.baseUrl;
    (document.getElementById('apiKey') as HTMLInputElement).value = config.workerKey;
    (document.getElementById('adminApiKey') as HTMLInputElement).value = config.adminKey;
    (document.getElementById('save-config') as HTMLButtonElement).click();
  }, { baseUrl, workerKey: WORKER_API_KEY, adminKey: ADMIN_API_KEY });
  await sleep(300);
  return popup;
}

async function extensionMessage(
  popup: Page,
  target: Page,
  action: 'START_RECORDING' | 'ENSURE_RECORDING_READY' | 'STOP_RECORDING',
): Promise<Record<string, any>> {
  await target.bringToFront();
  return popup.evaluate(async (requestedAction) => {
    return (globalThis as any).chrome.runtime.sendMessage({ action: requestedAction });
  }, action) as Promise<Record<string, any>>;
}

async function clearAegisExtensionStorage(context: BrowserContext): Promise<void> {
  const runtime = context.serviceWorkers()[0] ?? context.backgroundPages()[0];
  if (!runtime) return;
  await runtime.evaluate(async () => {
    await (globalThis as any).chrome.storage.local.clear();
    await (globalThis as any).chrome.storage.session.clear();
    const request = indexedDB.open('opencrawler-recordings', 1);
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      request.onerror = () => reject(request.error ?? new Error('failed to open recording store for cleanup'));
      request.onsuccess = () => resolve(request.result);
      request.onupgradeneeded = () => {
        const opened = request.result;
        if (!opened.objectStoreNames.contains('recordings')) opened.createObjectStore('recordings');
      };
    });
    await new Promise<void>((resolve, reject) => {
      const transaction = db.transaction(['recordings'], 'readwrite');
      transaction.objectStore('recordings').delete('lastRecording');
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error ?? new Error('recording cleanup failed'));
      transaction.onabort = () => reject(transaction.error ?? new Error('recording cleanup aborted'));
    });
    db.close();
  }).catch(() => undefined);
}

/** Shared environment: one Go server plus one extension-loaded Chromium. */
export interface LiveServerEnvironment {
  baseUrl: string;
  api: LiveAPI;
  serverDir: string;
  dbPath: string;
  serverProc: ChildProcess;
  diagnostics: { value: string };
}

export interface LiveEnvironment extends LiveServerEnvironment {
  userDataDir: string;
  context: BrowserContext;
  extensionId: string;
  popup: Page;
  /** Present only for an explicitly retained Aegis-owned qualification profile. */
  dedicatedProfile?: DedicatedProfileConfig;
  contextClosed: boolean;
}

export interface LiveEnvironmentOptions {
  /** Omit for the free recording-only preflight; the server starts with LLM disabled. */
  llm?: LocalLLMConfig;
  headed: boolean;
  dedicatedProfile?: DedicatedProfileConfig;
}

export interface LiveServerEnvironmentOptions {
  /** Omit only for provider-free server-only checks. */
  llm?: LocalLLMConfig;
}

export interface LiveServerBootDependencies {
  serverDir?: string;
  getFreePort?: typeof getFreePort;
  buildServer?: typeof buildServer;
  startServer?: typeof startServer;
  waitForHealth?: typeof waitForHealth;
}

export interface LiveEnvironmentBootDependencies {
  bootServer?: typeof bootLiveServer;
  makeTemporaryProfile?: () => string;
}

/** Build and boot only the loopback Go server; no browser or public site is opened. */
export async function bootLiveServer(
  options: LiveServerEnvironmentOptions,
  dependencies: LiveServerBootDependencies = {},
): Promise<LiveServerEnvironment> {
  const stamp = `${Date.now()}-${process.pid}`;
  const serverDir = dependencies.serverDir ?? path.resolve(__dirname, '..', '..', 'server');
  const dbPath = path.join(os.tmpdir(), `aegis-live-wf-${stamp}.db`);
  const port = await (dependencies.getFreePort ?? getFreePort)();
  const baseUrl = `http://127.0.0.1:${port}`;
  const diagnostics = { value: '' };
  let serverProc: ChildProcess | undefined;

  try {
    console.log('[live-wf] building the Go server (server/e2e-server.exe)');
    await (dependencies.buildServer ?? buildServer)(serverDir);
    serverProc = (dependencies.startServer ?? startServer)(
      serverDir,
      dbPath,
      port,
      liveServerEnvironment(options.llm),
    );
    const capture = (chunk: unknown): void => {
      const raw = String(chunk);
      const safe = options.llm?.apiKey ? raw.replaceAll(options.llm.apiKey, '[REDACTED]') : raw;
      diagnostics.value = (diagnostics.value + safe).slice(-DIAGNOSTICS_BUDGET);
    };
    serverProc.stdout?.on('data', capture);
    serverProc.stderr?.on('data', capture);
    await (dependencies.waitForHealth ?? waitForHealth)(baseUrl, 30_000);
    return {
      baseUrl,
      api: new LiveAPI(baseUrl),
      serverDir,
      dbPath,
      serverProc,
      diagnostics,
    };
  } catch (error) {
    if (serverProc) await killServer(serverProc);
    for (const file of [dbPath, `${dbPath}-wal`, `${dbPath}-shm`]) removeIfExists(file);
    removeIfExists(path.join(serverDir, 'e2e-server.exe'));
    throw error;
  }
}

export async function shutdownLiveServer(
  env: LiveServerEnvironment,
  removeGeneratedBinary = false,
): Promise<void> {
  try {
    await killServer(env.serverProc);
  } finally {
    for (const file of [env.dbPath, `${env.dbPath}-wal`, `${env.dbPath}-shm`]) {
      removeIfExists(file);
    }
    if (removeGeneratedBinary) {
      removeIfExists(path.join(env.serverDir, 'e2e-server.exe'));
    }
  }
}

/**
 * Build and boot the Go server, then launch one persistent Chromium context
 * with the built extension and configure its popup against the server. When
 * supplied, the LLM API key is only ever written to the child process
 * environment; captured server output is redacted before retention/printing.
 * Recording-only preflights omit the provider configuration entirely.
 */
export async function bootLiveEnvironment(
  options: LiveEnvironmentOptions,
  dependencies: LiveEnvironmentBootDependencies = {},
): Promise<LiveEnvironment> {
  const userDataDir = options.dedicatedProfile
    ? prepareDedicatedProfile(options.dedicatedProfile)
    : (dependencies.makeTemporaryProfile ?? (() =>
      fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-wf-chromium-'))))();

  let server: LiveServerEnvironment | undefined;
  let context: BrowserContext | undefined;
  try {
    server = await (dependencies.bootServer ?? bootLiveServer)({ llm: options.llm });
    const extensionDir = path.resolve(__dirname, '..', '..', 'dist', 'extension');
    assert(fs.existsSync(path.join(extensionDir, 'manifest.json')),
      `built extension missing at ${extensionDir}; run npm run build:extension first`);
    // MV3 extensions do not load in Playwright's default headless shell. The
    // documented workaround is the new headless mode: headless:false plus
    // --headless=new (see Playwright's chrome-extensions docs). Headed stays
    // plain headless:false.
    context = await chromium.launchPersistentContext(userDataDir, {
      headless: false,
      args: extensionChromiumArgs(extensionDir, !options.headed),
    });
    const deadline = Date.now() + 20_000;
    let background = context.backgroundPages()[0] ?? context.serviceWorkers()[0];
    while (!background && Date.now() < deadline) {
      await sleep(250);
      background = context.backgroundPages()[0] ?? context.serviceWorkers()[0];
    }
    assert(background, 'Manifest V3 extension service worker did not start');
    const extensionId = await background.evaluate(() => (globalThis as any).chrome.runtime.id) as string;
    assert(extensionId, 'extension id unavailable');
    const popup = await extensionPopup(context, extensionId, server.baseUrl);
    console.log(`[live-wf] server healthy on ${server.baseUrl}; extension ${extensionId} ready`);
    return {
      ...server,
      userDataDir,
      context,
      extensionId,
      popup,
      dedicatedProfile: options.dedicatedProfile,
      contextClosed: false,
    };
  } catch (error) {
    if (context && options.dedicatedProfile) await clearAegisExtensionStorage(context);
    if (context) await context.close().catch(() => undefined);
    if (server) await shutdownLiveServer(server, !options.llm);
    if (!options.dedicatedProfile) fs.rmSync(userDataDir, { recursive: true, force: true });
    throw error;
  }
}

export async function shutdownLiveEnvironment(
  env: LiveEnvironment,
  removeGeneratedBinary = false,
): Promise<void> {
  if (env.dedicatedProfile && !env.contextClosed) await clearAegisExtensionStorage(env.context);
  await env.context.close().catch(() => undefined);
  env.contextClosed = true;
  await shutdownLiveServer(env, removeGeneratedBinary);
  if (!env.dedicatedProfile) fs.rmSync(env.userDataDir, { recursive: true, force: true });
}

export interface ShopFixture {
  server: Server;
  port: number;
  url: string;
  configuratorUrl: string;
  close: () => Promise<void>;
}

/**
 * Tiny per-run HTTP server (127.0.0.1, dynamic port) serving the static shop
 * collection page. The small DOM keeps recordings — and therefore paid LLM
 * input tokens — small.
 */
export async function startShopFixtureServer(): Promise<ShopFixture> {
  const fixtureDir = path.resolve(__dirname, '..', 'fixtures');
  const html = fs.readFileSync(path.join(fixtureDir, 'live-workflow-shop.html'), 'utf8');
  const configuratorHtml = fs.readFileSync(
    path.join(fixtureDir, 'live-workflow-configurator.html'),
    'utf8',
  );
  const server = createServer((req, res) => {
    if (req.method === 'GET' && (req.url === '/' || req.url === '/shop')) {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
      res.end(html);
      return;
    }
    if (req.method === 'GET' && req.url === '/configurator') {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
      res.end(configuratorHtml);
      return;
    }
    res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
    res.end('not found');
  });
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve());
  });
  const address = server.address();
  assert(address && typeof address === 'object', 'shop fixture server did not bind a port');
  const close = (): Promise<void> => new Promise((resolve) => server.close(() => resolve()));
  const origin = `http://127.0.0.1:${address.port}`;
  return {
    server,
    port: address.port,
    url: `${origin}/shop`,
    configuratorUrl: `${origin}/configurator`,
    close,
  };
}

export async function captureLiveScenarioRecording(
  env: LiveEnvironment,
  scenario: LiveWorkflowScenario,
  page: Page,
): Promise<PageAgentRecording> {
  await scenario.prepareContext?.(env.context);
  await page.goto(scenario.entryUrl, {
    waitUntil: scenarioNavigationWaitUntil(scenario),
    timeout: 90_000,
  });
  await scenario.preparePage?.(page);

  const started = await extensionMessage(env.popup, page, 'START_RECORDING');
  assert(started.success === true && started.protocolVersion === '2.0.0',
    `recording start failed: ${JSON.stringify(started)}`);

  await scenario.demo(page, async (activePage = page) => {
    const ready = await extensionMessage(env.popup, activePage, 'ENSURE_RECORDING_READY');
    assert(ready.success === true && ready.active === true && ready.protocolVersion === '2.0.0',
      `recording did not resume after navigation: ${JSON.stringify(ready)}`);
  });

  const stopped = await extensionMessage(env.popup, page, 'STOP_RECORDING');
  assert(stopped.success === true, `recording stop failed: ${JSON.stringify(stopped)}`);
  let recording = stopped.recording as PageAgentRecording | undefined;
  const deadline = Date.now() + RECORDING_PERSIST_TIMEOUT_MS;
  while (!recording?.meta.serverRecordingId && Date.now() < deadline) {
    const result = await env.popup.evaluate(
      async () => (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_LAST_RECORDING' }),
    ) as { recording?: PageAgentRecording };
    recording = result.recording;
    await sleep(500);
  }
  assert(recording?.meta.serverRecordingId, 'recording was not durably persisted before LLM work');
  const phases = recording.snapshots.map((snapshot) => snapshot.phase);
  assert(phases[0] === 'initial' && phases[phases.length - 1] === 'final' && phases.length >= 2,
    `recording snapshot phases were incomplete: ${JSON.stringify(phases)}`);
  assert(recording.termination?.complete === true, 'recording termination marker is incomplete');

  return recording;
}

async function recordScenario(run: ScenarioRun, page: Page): Promise<PageAgentRecording> {
  const startedAt = Date.now();
  const { env, scenario } = run;
  const recording = await captureLiveScenarioRecording(env, scenario, page);
  const recordingId = recording.meta.serverRecordingId;
  assert(recordingId, 'captured recording lost its durable server id');

  run.recordingId = recordingId;
  run.tracker = new JobTracker(env.api, env.context, recordingId);
  console.log(`[live-wf:${scenario.name}] recording ${run.recordingId} persisted `
    + `(${recording.events.length} events, ${recording.snapshots.length} snapshots)`);
  markPhase(run, 'record', startedAt);
  return recording;
}

async function seedExtensionRecording(context: BrowserContext, recording: PageAgentRecording): Promise<void> {
  const runtime = context.serviceWorkers()[0] ?? context.backgroundPages()[0];
  assert(runtime, 'extension runtime unavailable while restoring reusable recording');
  await runtime.evaluate(async (value) => {
    const request = indexedDB.open('opencrawler-recordings', 1);
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      request.onerror = () => reject(request.error ?? new Error('failed to open recording store'));
      request.onsuccess = () => resolve(request.result);
      request.onupgradeneeded = () => {
        const opened = request.result;
        if (!opened.objectStoreNames.contains('recordings')) opened.createObjectStore('recordings');
      };
    });
    await new Promise<void>((resolve, reject) => {
      const transaction = db.transaction(['recordings'], 'readwrite');
      transaction.objectStore('recordings').put(value, 'lastRecording');
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(transaction.error ?? new Error('failed to seed recording store'));
      transaction.onabort = () => reject(transaction.error ?? new Error('recording seed aborted'));
    });
    db.close();
  }, recording);
}

/** Restore a validated local recording through the same encrypted server and extension stores used by a live run. */
export async function restoreReusableRecording(
  env: LiveEnvironment,
  source: PageAgentRecording,
): Promise<{ recording: PageAgentRecording; recordingId: string }> {
  const recording = structuredClone(source);
  delete recording.meta.serverRecordingId;
  const response = await env.api.admin<{ recording?: { id?: string } }>(
    'POST',
    '/api/v1/recordings',
    {
      recording,
      startedAt: recording.meta.recordedAt,
      endedAt: recording.meta.endedAt,
    },
  );
  const recordingId = response.recording?.id;
  assert(recordingId, 'reusable recording persistence response did not include an id');
  recording.meta.serverRecordingId = recordingId;
  await seedExtensionRecording(env.context, recording);
  const restored = await env.popup.evaluate(
    async () => (globalThis as any).chrome.runtime.sendMessage({ action: 'GET_LAST_RECORDING' }),
  ) as { recording?: PageAgentRecording };
  assert(restored.recording?.meta.serverRecordingId === recordingId,
    'extension did not retain the reusable recording server lineage');
  return { recording, recordingId };
}

async function restoreScenarioRecording(
  run: ScenarioRun,
  source: PageAgentRecording,
): Promise<PageAgentRecording> {
  const startedAt = Date.now();
  const { recording, recordingId } = await restoreReusableRecording(run.env, source);
  run.recordingId = recordingId;
  run.tracker = new JobTracker(run.env.api, run.env.context, recordingId);
  console.log(`[live-wf:${run.scenario.name}] restored encrypted reusable recording as ${recordingId} `
    + `(${recording.events.length} events, ${recording.snapshots.length} snapshots)`);
  markPhase(run, 'record', startedAt);
  return recording;
}

async function openFreshIntentPage(env: LiveEnvironment): Promise<Page> {
  for (const page of env.context.pages()) {
    if (page.url().includes('/intent/intent-page.html')) await page.close().catch(() => undefined);
  }
  const intent = await env.context.newPage();
  await intent.goto(`chrome-extension://${env.extensionId}/intent/intent-page.html`);
  await intent.waitForSelector('#step-intent:not(.hidden)', { timeout: 60_000 });
  return intent;
}

/** Structural assertion of the normalized collection-requirement contract. */
function assertRequirementSpec(spec: unknown, label: string): void {
  assert(isPlainObject(spec), `${label}: requirement is not an object`);
  assert(typeof spec.title === 'string' && spec.title.trim().length > 0, `${label}: missing title`);
  assert(typeof spec.description === 'string' && spec.description.trim().length > 0, `${label}: missing description`);
  for (const listName of ['requiredInputs', 'optionalInputs'] as const) {
    const list = spec[listName];
    assert(Array.isArray(list), `${label}: ${listName} is not an array`);
    for (const input of list) {
      assert(isPlainObject(input) && typeof input.name === 'string' && input.name.trim().length > 0,
        `${label}: ${listName} entry without a name`);
      assert(typeof input.type === 'string' && REQUIREMENT_VALUE_TYPES.has(input.type),
        `${label}: ${listName} "${String(input?.name)}" has invalid type ${String(input?.type)}`);
    }
  }
  const outputFields = spec.outputFields;
  assert(Array.isArray(outputFields) && outputFields.length > 0, `${label}: outputFields must be a non-empty array`);
  const names = new Set<string>();
  for (const field of outputFields) {
    assert(isPlainObject(field) && typeof field.name === 'string' && field.name.trim().length > 0,
      `${label}: output field without a name`);
    assert(typeof field.type === 'string' && REQUIREMENT_VALUE_TYPES.has(field.type),
      `${label}: output field "${String(field?.name)}" has invalid type ${String(field?.type)}`);
    names.add(field.name as string);
  }
  const sample = spec.sampleOutput;
  assert(isPlainObject(sample) && Object.keys(sample).length > 0,
    `${label}: sampleOutput must be a non-empty object keyed by output field names`);
  for (const key of Object.keys(sample)) {
    assert(names.has(key), `${label}: sampleOutput key "${key}" is not a declared output field`);
  }
}

/** Full structural assertion of the candidates job result (exactly three). */
function assertCandidatesStructure(candidates: unknown): void {
  assert(Array.isArray(candidates) && candidates.length === 3,
    `workflow must produce exactly three candidates, got ${Array.isArray(candidates) ? candidates.length : 'none'}`);
  candidates.forEach((candidate, index) => {
    assert(isPlainObject(candidate), `candidate ${index + 1} is not an object`);
    assert(typeof candidate.id === 'string' && candidate.id.length > 0, `candidate ${index + 1} has no id`);
    assert(typeof candidate.confidence === 'number' && candidate.confidence >= 0 && candidate.confidence <= 1,
      `candidate ${index + 1} confidence is not in 0..1: ${String(candidate.confidence)}`);
    assertRequirementSpec(candidate.requirement, `candidate ${index + 1}`);
  });
  console.log('[live-wf] candidates job returned exactly three structurally valid candidates');
}

/** Structural assertion of the three on-demand candidates rendered by the wizard. */
async function assertThreeCandidateCards(intent: Page): Promise<void> {
  const radios = intent.locator('input[name="requirement-candidate"]');
  const count = await radios.count();
  assert(count === 3, `workflow must present exactly three candidates, found ${count}`);
  const cards = intent.locator('.candidate-card.requirement-candidate');
  assert(await cards.count() === 3, 'candidate cards did not render in the required structured form');
  for (let index = 0; index < 3; index += 1) {
    const card = cards.nth(index);
    const title = ((await card.locator('strong').textContent()) ?? '').trim();
    assert(title.length > 0, `candidate ${index + 1} has no title`);
    const confidence = ((await card.locator('.confidence').textContent()) ?? '').trim();
    assert(/^\d+%$/.test(confidence), `candidate ${index + 1} has no confidence score: "${confidence}"`);
    const description = ((await card.locator('.candidate-description').textContent()) ?? '').trim();
    assert(description.length > 0, `candidate ${index + 1} has no description`);
    const details = await card.locator('.candidate-detail').allTextContents();
    assert(details.length === 2 && details.every((detail) => detail.trim().length > 0),
      `candidate ${index + 1} does not declare inputs and output fields: ${JSON.stringify(details)}`);
    assert(details[1].includes(':'), `candidate ${index + 1} declares no typed output fields: ${details[1]}`);
    const sample = ((await card.locator('code.sample-output').textContent()) ?? '').trim();
    let parsed: unknown;
    try {
      parsed = JSON.parse(sample);
    } catch {
      throw new Error(`candidate ${index + 1} sample output is not valid JSON: ${sample.slice(0, 200)}`);
    }
    assert(isPlainObject(parsed) && Object.keys(parsed).length > 0,
      `candidate ${index + 1} sample output is not a non-empty object`);
  }
  console.log('[live-wf] wizard rendered exactly three structured candidate cards');
}

/**
 * Structural check of the normalized requirement BEFORE the confirmation
 * click: the latest completed normalize job must have produced a valid
 * structured requirement, so an ambiguous or malformed normalization is never
 * confirmed into DSL generation.
 */
async function assertNormalizedRequirementReady(run: ScenarioRun): Promise<void> {
  await run.tracker?.poll();
  const normalized = (run.tracker?.normalizeJobs() ?? [])
    .filter((job) => job.status === 'completed')
    .map((job) => (job.result as Record<string, unknown> | undefined)?.requirement);
  assert(normalized.length > 0, 'no completed normalization job produced a requirement');
  const requirement = normalized[normalized.length - 1];
  assertRequirementSpec(requirement, 'normalized requirement');
  run.scenario.assertRequirement?.(requirement);
}

async function driveRequirementPhase(run: ScenarioRun, intent: Page): Promise<void> {
  const startedAt = Date.now();
  const { scenario } = run;
  const requirement = scenario.requirement;
  if (requirement.mode === 'intent') {
    if (requirement.humanInput) {
      assert(scenario.completeHumanRequirement,
        'human-entered intent requires a headed requirement callback');
      await scenario.completeHumanRequirement(intent, 'intent-input');
      const entered = await intent.locator('#custom-description').inputValue();
      assert(entered === requirement.customText,
        'human-entered requirement text did not exactly match the scenario contract');
    } else {
      await intent.locator('#custom-description').fill(requirement.customText);
    }
    await waitPrimaryText(run, intent, '下一步：查看结构化需求', 'intent-step primary did not enable for custom intent');
    await clickPrimaryWhenEnabled(intent);
    // PR #69 fast path: exactly one normalization analysis over the recorded
    // snapshots, and never an on-demand candidates job.
    await waitForPhase(run, intent, 'custom intent normalization', async () =>
      intent.locator('#step-requirement:not(.hidden)').isVisible());
    await run.tracker?.poll();
    assert(!run.tracker?.candidatesJobCreated(), 'intent path unexpectedly created a candidates job');
    const normalizeJobs = run.tracker?.normalizeJobs() ?? [];
    assert(normalizeJobs.length === 1,
      `intent path must run exactly one normalization analysis, observed ${normalizeJobs.length}`);
    await waitPrimaryText(run, intent, '确认此采集需求', 'normalized requirement was not presented for confirmation');
    await assertNormalizedRequirementReady(run);
    await clickPrimaryWhenEnabled(intent);
  } else if (requirement.mode === 'candidates') {
    // Candidates are generated on demand (PR #69). Clear any persisted custom
    // intent from a previous scenario so the primary offers generation.
    await intent.locator('#custom-description').fill('');
    await waitPrimaryText(run, intent, '生成 3 个候选需求', 'intent-step primary did not offer on-demand candidates');
    await clickPrimaryWhenEnabled(intent);
    await waitForPhase(run, intent, 'on-demand candidate generation', async () =>
      (await intent.locator('input[name="requirement-candidate"]').count()) === 3);
    await run.tracker?.poll();
    const candidatesJob = run.tracker?.candidatesJob();
    assert(candidatesJob?.status === 'completed',
      `candidates job did not complete: ${candidatesJob?.status ?? 'unknown'}`);
    const generatedCandidates =
      (candidatesJob.result as Record<string, unknown> | undefined)?.candidates;
    assertCandidatesStructure(generatedCandidates);
    await assertThreeCandidateCards(intent);
    if (requirement.pick === 'human') {
      assert(scenario.completeHumanRequirement,
        'human candidate selection requires a headed requirement callback');
      assert(scenario.acceptHumanCandidate,
        'human candidate selection requires a fail-closed semantic candidate filter');
      const candidates = generatedCandidates as Array<Record<string, unknown>>;
      const eligible = candidates.flatMap((candidate, index) =>
        scenario.acceptHumanCandidate?.(candidate.requirement) ? [index] : []);
      assert(eligible.length > 0,
        'none of the three generated requirements satisfy the human-choice scenario contract');
      console.log(`[live-wf:${scenario.name}] eligible human requirement candidate number(s): `
        + eligible.map((index) => index + 1).join(', '));
      await scenario.completeHumanRequirement(intent, 'candidate-selection');
      const radios = intent.locator('input[name="requirement-candidate"]');
      const checkedIndex = await radios.evaluateAll((inputs) =>
        inputs.findIndex((input) => (input as HTMLInputElement).checked));
      assert(checkedIndex >= 0,
        'human must select exactly one generated requirement candidate');
      assert(eligible.includes(checkedIndex),
        `human selected candidate ${checkedIndex + 1}, which does not satisfy the scenario contract`);
    } else {
      await intent.locator('input[name="requirement-candidate"]').first().check();
    }
    await waitPrimaryText(run, intent, '下一步：查看结构化需求', 'candidate selection did not enable the primary action');
    await clickPrimaryWhenEnabled(intent);
    await waitForPhase(run, intent, 'requirement editor did not open', async () =>
      intent.locator('#step-requirement:not(.hidden)').isVisible(), { baseTimeoutMs: 60_000 });
    await waitPrimaryText(run, intent, '验证并规范化', 'selected candidate was not staged for normalization');
    await clickPrimaryWhenEnabled(intent);
    await waitPrimaryText(run, intent, '确认此采集需求', 'selected candidate normalization did not complete');
    await assertNormalizedRequirementReady(run);
    await clickPrimaryWhenEnabled(intent);
  } else {
    await intent.locator('#manual-structured').click();
    await intent.locator('#step-requirement:not(.hidden)').waitFor({ state: 'visible', timeout: 15_000 });
    const spec = requirement.spec;
    await intent.locator('#requirement-title').fill(spec.title);
    await intent.locator('#requirement-description').fill(spec.description);
    await intent.locator('#requirement-required-inputs').fill(JSON.stringify(spec.requiredInputs, null, 2));
    await intent.locator('#requirement-optional-inputs').fill(JSON.stringify(spec.optionalInputs, null, 2));
    await intent.locator('#requirement-output-fields').fill(JSON.stringify(spec.outputFields, null, 2));
    await intent.locator('#requirement-sample-output').fill(JSON.stringify(spec.sampleOutput, null, 2));
    await waitPrimaryText(run, intent, '验证并规范化', 'manual structured requirement was not editable');
    await clickPrimaryWhenEnabled(intent);
    await waitForPhase(run, intent, 'manual structured normalization', async () => {
      const label = (await intent.locator('#action-primary').textContent().catch(() => '')) ?? '';
      return label.includes('确认此采集需求') && !(await intent.locator('#action-primary').isDisabled().catch(() => true));
    });
    await run.tracker?.poll();
    const normalizeJobs = run.tracker?.normalizeJobs() ?? [];
    assert(normalizeJobs.length === 1,
      `structured path must create exactly one deterministic normalization job, observed ${normalizeJobs.length}`);
    assert(['deterministic', 'manual'].includes(normalizeJobs[0].provider),
      `structured path unexpectedly used provider ${normalizeJobs[0].provider}`);
    await assertNormalizedRequirementReady(run);
    await clickPrimaryWhenEnabled(intent);
  }
  await waitForPhase(run, intent, 'requirement confirmation', async () =>
    intent.locator('#step-requirement-confirmed:not(.hidden)').isVisible(), { baseTimeoutMs: 60_000 });
  const confirmedText = (await intent.locator('#requirement-confirmed-result').textContent()) ?? '';
  const match = confirmedText.match(/ID：(\S+)/);
  assert(match, `could not parse confirmed requirement id: ${confirmedText}`);
  run.requirementId = match[1];
  const confirmed = await run.env.api.admin<{ requirement?: { status?: string; requirement?: unknown } }>(
    'GET', `/api/v1/requirements/${encodeURIComponent(run.requirementId)}`,
  );
  assert(confirmed?.requirement?.status === 'confirmed',
    `confirmed requirement was not persisted as confirmed: ${JSON.stringify(confirmed?.requirement?.status)}`);
  assertRequirementSpec(confirmed.requirement.requirement, 'confirmed requirement');
  console.log(`[live-wf:${scenario.name}] requirement ${run.requirementId} confirmed via ${requirement.mode} path`);
  markPhase(run, 'requirement', startedAt);
}

async function driveDSLPhase(run: ScenarioRun, intent: Page): Promise<void> {
  const startedAt = Date.now();
  const { scenario } = run;
  const profileId = scenario.browserProfileId ?? LIVE_WORKER_PROFILE;
  await intent.locator('#browser-profile-id').fill(profileId);
  await clickPrimaryWhenEnabled(intent); // 生成临时 DSL

  await waitForPhase(run, intent, 'provisional DSL generation', async () =>
    intent.locator('#step-preview:not(.hidden)').isVisible());
  const preview = ((await intent.locator('#yaml-preview').textContent()) ?? '').trim();
  assert(preview.length > 0, 'live provider returned an empty provisional DSL');
  assert(preview.includes('sendResult'), `provisional DSL omitted sendResult: ${preview.slice(0, 500)}`);
  await run.tracker?.poll();
  assert(run.tracker?.dslWorkflow()?.status === 'awaiting_replay',
    `DSL workflow did not reach awaiting_replay: ${run.tracker?.dslWorkflow()?.status ?? 'unknown'}`);
  assertScenarioProvisionalRule(scenario, run.tracker?.dslWorkflowArtifact());
  markPhase(run, 'dsl-generate', startedAt);

  assert(run.requirementId, 'replay input binding requires a confirmed requirement');
  const confirmed = await run.env.api.admin<{
    requirement?: {
      requirement?: {
        requiredInputs?: Array<Record<string, unknown>>;
        optionalInputs?: Array<Record<string, unknown>>;
      };
    };
  }>('GET', `/api/v1/requirements/${encodeURIComponent(run.requirementId)}`);
  const requirement = confirmed?.requirement?.requirement;
  assert(requirement, 'replay input binding could not load the confirmed requirement');
  const requiredInputs = Array.isArray(requirement.requiredInputs) ? requirement.requiredInputs : [];
  const optionalInputs = Array.isArray(requirement.optionalInputs) ? requirement.optionalInputs : [];
  const inputProperties: Record<string, unknown> = {};
  for (const input of [...requiredInputs, ...optionalInputs]) {
    const name = typeof input.name === 'string' ? input.name : '';
    assert(name, 'confirmed requirement contains an input without a name');
    const constraints = isPlainObject(input.constraints) ? input.constraints : {};
    inputProperties[name] = {
      ...constraints,
      type: input.type,
      description: input.description,
      ...('default' in input ? { default: input.default } : {}),
      ...(input.secret === true ? { 'x-secret': true } : {}),
    };
  }
  let replayVariables = buildTaskInputs({
    type: 'object',
    properties: inputProperties,
    required: requiredInputs.map((input) => String(input.name)),
  });
  const adjustReplayInputs = scenario.adjustReplayInputs ?? scenario.adjustTaskInputs;
  if (adjustReplayInputs) replayVariables = adjustReplayInputs(replayVariables);
  const replayFields = intent.locator('[data-replay-input-name]');
  const replayFieldCount = await replayFields.count();
  const replayFieldIndexes = new Map<string, number>();
  for (let index = 0; index < replayFieldCount; index += 1) {
    const name = await replayFields.nth(index).getAttribute('data-replay-input-name');
    if (name) {
      assert(!replayFieldIndexes.has(name), `replay input "${name}" has duplicate wizard controls`);
      replayFieldIndexes.set(name, index);
    }
  }
  for (const [name, value] of Object.entries(replayVariables)) {
    const fieldIndex = replayFieldIndexes.get(name);
    assert(fieldIndex !== undefined, `replay input "${name}" is not declared by the confirmed requirement`);
    const field = replayFields.nth(fieldIndex);
    const tagName = await field.evaluate((element) => element.tagName);
    const serialized = typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value);
    if (tagName === 'SELECT') {
      await field.selectOption(serialized);
    } else {
      await field.fill(serialized);
    }
  }
  if (Object.keys(replayVariables).length > 0) {
    console.log(`[live-wf:${scenario.name}] bound replay inputs: ${Object.keys(replayVariables).join(', ')}`);
  }

  const replayStartedAt = Date.now();
  const replayOracleAbort = scenario.captureReplayOracleDuringExecution
    ? new AbortController()
    : undefined;
  let replayOracleFailure: unknown;
  const replayOracleCapture = scenario.captureReplayOracleDuringExecution?.(
    run.env.context,
    replayOracleAbort!.signal,
  ).then(
    () => ({ ok: true as const }),
    (error: unknown) => {
      replayOracleFailure = error;
      return { ok: false as const, error };
    },
  );

  // Strict qualification permits one replay and no automatic repair cycle.
  // Forward progress is replay-log growth; terminal states are
  // a successful confirmation wait or a failed workflow/job on the server.
  let replayLogCount = 0;
  try {
    await clickPrimaryWhenEnabled(intent); // 下一步：回放确认
    await waitForPhase(run, intent, '#step-replay:not(.hidden) did not appear', async () => {
      if (replayOracleFailure) throw replayOracleFailure;
      return intent.locator('#step-replay:not(.hidden)').isVisible();
    }, { baseTimeoutMs: 60_000 });
    await waitForPhase(run, intent, 'DSL replay/repair', async () => {
      const status = ((await intent.locator('.replay-status').textContent().catch(() => '')) ?? '');
      const success = status.includes('回放成功');
      const enabled = await intent.locator('#action-primary').isEnabled().catch(() => false);
      if (replayOracleFailure) throw replayOracleFailure;
      return success && enabled;
    }, {
      extraSignature: async () => {
        const logs = await intent.locator('.replay-logs .log').count().catch(() => 0);
        const status = ((await intent.locator('.replay-status').textContent().catch(() => '')) ?? '').trim();
        return `logs=${logs};status=${status}`;
      },
    });
  } catch (error) {
    replayOracleAbort?.abort();
    await replayOracleCapture;
    const replayStatus = ((await intent.locator('.replay-status').textContent().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 160);
    const replayError = ((await intent.locator('.replay-error').textContent().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 320);
    const replayLogs = ((await intent.locator('.replay-logs').textContent().catch(() => '')) ?? '')
      .replace(/\s+/g, ' ').trim().slice(0, 1200);
    throw new Error(
      `replay/repair did not succeed: ${error instanceof Error ? error.message : String(error)}; `
      + `status=${replayStatus || 'unavailable'}; error=${replayError || 'unavailable'}; logs=${replayLogs || 'unavailable'}`,
    );
  }
  replayLogCount = await intent.locator('.replay-logs .log').count().catch(() => 0);
  const replayOracleOutcome = await replayOracleCapture;
  replayOracleAbort?.abort();
  if (replayOracleOutcome && !replayOracleOutcome.ok) throw replayOracleOutcome.error;

  await run.tracker?.poll();
  run.repairCount = run.tracker?.repairCount() ?? 0;
  assert(run.repairCount <= MAX_REPAIR_BUDGET, `workflow exceeded the bounded repair budget: ${run.repairCount}`);
  await scenario.captureReplayOracle?.(run.env.context);
  const assertReplayRows = scenario.assertReplayRows ?? scenario.assertRows;
  if (assertReplayRows) {
    assert(run.requirementId, 'semantic replay qualification requires a confirmed requirement');
    const confirmed = await run.env.api.admin<{
      requirement?: { requirement?: { outputFields?: Array<{ name: string; type: string; description?: string }> } };
    }>('GET', `/api/v1/requirements/${encodeURIComponent(run.requirementId)}`);
    const requirement = confirmed?.requirement?.requirement;
    assert(requirement && Array.isArray(requirement.outputFields),
      'semantic replay qualification could not load confirmed output fields');
    assertPersistedReplaySemanticRows(
      run.tracker?.replayAttemptList() ?? [],
      { outputFields: requirement.outputFields },
      assertReplayRows,
    );
    console.log(`[live-wf:${scenario.name}] persisted replay passed the scenario semantic row oracle`);
  }
  console.log(`[live-wf:${scenario.name}] replay verified after ${run.repairCount} repair(s), ${replayLogCount} log entries`);

  await clickPrimaryWhenEnabled(intent); // 确认回放并批准规则版本
  await waitForPhase(run, intent, 'rule version approval', async () =>
    intent.locator('#step-save:not(.hidden)').isVisible(), { baseTimeoutMs: 60_000 });
  const saved = (await intent.locator('#save-result').textContent()) ?? '';
  const match = saved.match(/规则版本已批准：(.+) v(\d+)/);
  assert(match, `could not parse approved immutable rule version: ${saved}`);
  run.ruleId = match[1];
  run.ruleVersion = Number(match[2]);
  await run.tracker?.poll();
  const workflow = run.tracker?.dslWorkflow();
  assert(workflow?.status === 'approved', `server-side DSL workflow did not reach approved status: ${workflow?.status ?? 'unknown'}`);
  assert(workflow.approvedRuleId === run.ruleId && workflow.approvedVersion === run.ruleVersion,
    `approved lineage mismatch: workflow=${workflow.approvedRuleId ?? ''} v${workflow.approvedVersion ?? ''} wizard=${run.ruleId} v${run.ruleVersion}`);
  console.log(`[live-wf:${scenario.name}] approved immutable ${run.ruleId} v${run.ruleVersion}`);
  markPhase(run, 'replay', replayStartedAt);
}

/** Light client-side conformance check against the approved output schema. */
async function countProviderCalls(
  api: LiveAPI,
  requirementJobs: readonly JobReport[],
  dslJobs: readonly JobReport[],
  includedJobs: readonly JobReport[],
): Promise<number> {
  const included = new Set(includedJobs.map((job) => job.id));
  let total = 0;
  for (const [jobType, jobs] of [
    ['requirement-jobs', requirementJobs],
    ['dsl-jobs', dslJobs],
  ] as const) {
    for (const job of jobs) {
      if (!included.has(job.id)) continue;
      const response = await api.admin<{
        attempts?: Array<{ callCount?: number }>;
      }>('GET', `/api/v1/${jobType}/${encodeURIComponent(job.id)}/provider-attempts`);
      assert(Array.isArray(response.attempts),
        `provider attempt list is unavailable for ${jobType}/${job.id}`);
      total += sumProviderAttemptCalls(
        response.attempts,
        `${jobType}/${job.id}`,
      );
    }
  }
  return total;
}

async function createAndRunTasks(
  run: ScenarioRun,
  onTaskComplete?: (task: TaskSummary) => void,
): Promise<TaskSummary[]> {
  const startedAt = Date.now();
  const { env, scenario, taskCases } = run;
  assert(run.ruleId && run.ruleVersion, 'an approved rule version is required for task creation');
  assert(taskCases.length > 0, 'the live workflow task matrix was not resolved');
  const detail = await env.api.admin<any>(
    'GET', `/admin/rules/${encodeURIComponent(run.ruleId)}/versions/${run.ruleVersion}`,
  );
  scenario.assertRule?.(detail?.ruleVersion?.rule);
  assert(detail?.contract?.inputSchema,
    `approved rule ${run.ruleId} v${run.ruleVersion} has no execution contract input schema`);

  const reuseDedicatedProfile = env.dedicatedProfile?.name === (scenario.browserProfileId ?? LIVE_WORKER_PROFILE);
  const profilesDir = reuseDedicatedProfile
    ? env.dedicatedProfile!.profilesDir
    : fs.mkdtempSync(path.join(os.tmpdir(), `aegis-live-wf-profiles-${scenario.name}-`));
  if (reuseDedicatedProfile) {
    // Chromium profile locks permit only one owner. Flush the recording/replay
    // context before WorkerHost opens the exact same on-disk profile.
    await run.tracker?.poll();
    await clearAegisExtensionStorage(env.context);
    await env.context.close();
    env.contextClosed = true;
    console.log(`[live-wf:${scenario.name}] recording browser closed; handing the dedicated profile to WorkerHost`);
  }
  let activeTaskCase: ResolvedLiveWorkflowScenarioTaskCase<Page> | undefined;
  let activeTaskOracleError: unknown;
  const hasTaskOracle = taskCases.some((taskCase) => taskCase.captureOracle);
  const host = new WorkerHost({
    serverUrl: env.baseUrl,
    apiKey: WORKER_API_KEY,
    profile: scenario.browserProfileId ?? LIVE_WORKER_PROFILE,
    profilesDir,
    workers: 1,
    headless: scenario.workerHeadless ?? true,
    workerIdPrefix: 'live-wf',
    pollIntervalMs: 250,
    heartbeatIntervalMs: 2_000,
    verbose: false,
    onPageComplete: hasTaskOracle
      ? async (page) => {
        const owner = activeTaskCase;
        assert(owner, 'Worker page completed without an active task-case owner');
        // Oracle capture is host-only qualification evidence. It must fail
        // qualification independently, never rewrite a successful executor
        // result into a failed production task.
        activeTaskOracleError = await captureTaskOracle(owner.captureOracle, page);
      }
      : undefined,
  });
  let hostError: unknown;
  const hostRun = host.start().catch((err) => {
    hostError = err;
  });

  try {
    return await executeTaskCasesSequentially(taskCases, async (taskCase) => {
      assert(!hostError, `worker host exited before task case ${taskCase.name}: ${String(hostError)}`);
      activeTaskCase = taskCase;
      activeTaskOracleError = undefined;
      try {
        const defaults = buildTaskInputs(detail.contract.inputSchema);
        const variables = taskCase.bindInputs({ ...defaults });
        assert(isPlainObject(variables),
          `task case ${taskCase.name} input binding must return an object`);
        if (Object.keys(variables).length > 0) {
          console.log(
            `[live-wf:${scenario.name}:${taskCase.name}] bound task inputs: `
            + Object.keys(variables).join(', '),
          );
        }
        const created = await env.api.admin<{ taskId: string }>('POST', '/admin/tasks', {
          ruleId: run.ruleId,
          ruleVersionNumber: run.ruleVersion,
          variables,
          maxRetries: 0,
        });
        assert(created.taskId, `task case ${taskCase.name} creation returned no id`);
        const taskId = created.taskId;

        const deadline = Date.now() + TASK_TERMINAL_TIMEOUT_MS;
        let task: any;
        while (Date.now() < deadline) {
          task = await env.api.admin<any>('GET', `/admin/tasks/${encodeURIComponent(taskId)}`);
          if (['done', 'failed', 'dead_letter'].includes(task.status)) break;
          await sleep(2_000);
        }
        assert(['done', 'failed', 'dead_letter'].includes(task?.status),
          `task case ${taskCase.name} did not reach a terminal state: `
          + JSON.stringify(task).slice(0, 800));

        const results = await env.api.admin<any>(
          'GET',
          `/api/v1/tasks/${encodeURIComponent(taskId)}/results`
          + `?limit=${TASK_RESULT_PAGE_LIMIT}&offset=0&include_invalid=true`,
        );
        assert(results.ruleId === run.ruleId && results.ruleVersionNumber === run.ruleVersion,
          `task case ${taskCase.name} lost immutable rule/version lineage: `
          + JSON.stringify({
            ruleId: results.ruleId,
            ruleVersionNumber: results.ruleVersionNumber,
          }));
        assert(results.outputSchema,
          `task case ${taskCase.name} results omitted the bound output schema`);
        const validBatches = results.page?.batches?.length ?? 0;
        const invalidBatches = results.page?.invalidBatches?.length ?? 0;
        const rows = (results.page?.batches ?? []).flatMap((batch: any) =>
          Array.isArray(batch.payload) ? batch.payload : [batch.payload]);
        assertTaskCaseResult(taskCase, {
          status: task.status,
          errorMessage: asOptionalString(task.errorMessage),
          validBatches,
          invalidBatches,
          rows: rows.length,
          hasSummary: results.page?.summary != null,
          summaryStatus: results.page?.summary?.payload?.status,
        });
        if (activeTaskOracleError) {
          throw new Error(
            `task case ${taskCase.name} oracle capture failed after successful execution: `
            + `${activeTaskOracleError instanceof Error
              ? activeTaskOracleError.message
              : String(activeTaskOracleError)}`,
          );
        }
        if (taskCase.expectedStatus === 'done') {
          assertRowsConformToOutputSchema(rows, results.outputSchema, taskCase.rowBand!);
          taskCase.assertRows?.(rows, results.outputSchema);
        }

        const attemptIds = new Set<string>();
        for (const result of [
          ...(results.page?.batches ?? []),
          ...(results.page?.invalidBatches ?? []),
          ...(results.page?.summary ? [results.page.summary] : []),
        ]) {
          if (typeof result?.attemptId === 'string' && result.attemptId) {
            attemptIds.add(result.attemptId);
          }
        }
        if (typeof task.currentAttemptId === 'string' && task.currentAttemptId) {
          attemptIds.add(task.currentAttemptId);
        }
        const mcp = await verifyRetrieval(run, taskId, taskCase.name);
        assert(!hostError,
          `worker host exited during task case ${taskCase.name}: ${String(hostError)}`);
        console.log(
          `[live-wf:${scenario.name}:${taskCase.name}] task ${taskId} ${task.status}: `
          + `${rows.length} row(s), ${validBatches} valid batch(es)`,
        );
        return {
          caseName: taskCase.name,
          expectedStatus: taskCase.expectedStatus,
          taskId,
          status: task.status,
          validBatches,
          invalidBatches,
          rows: rows.length,
          summary: results.page?.summary != null,
          summaryStatus: asOptionalString(results.page?.summary?.payload?.status),
          maxRetries: asNumber(task.maxRetries),
          retryCount: asNumber(task.retryCount),
          executionAttempts: attemptIds.size,
          mcpAgreedWithAdmin: mcp.agreed,
          mcpTokenRevoked: mcp.revoked,
        };
      } finally {
        activeTaskCase = undefined;
      }
    }, (task) => onTaskComplete?.(task));
  } finally {
    await host.stop().catch(() => undefined);
    await hostRun.catch(() => undefined);
    if (!reuseDedicatedProfile) fs.rmSync(profilesDir, { recursive: true, force: true });
    markPhase(run, 'tasks', startedAt);
  }
}

async function verifyRetrieval(
  run: ScenarioRun,
  taskId: string,
  caseName: string,
): Promise<{ agreed: boolean; revoked: boolean }> {
  const startedAt = Date.now();
  const { env } = run;
  const tokenResponse = await env.api.admin<{ id: string; token: string }>('POST', '/admin/mcp/tokens', {
    name: `live-workflow ${run.scenario.name} ${caseName} ${Date.now()}`,
    permissions: ['read'],
  });
  assert(tokenResponse.token && tokenResponse.id, 'MCP token endpoint did not return the one-time bearer token');

  let agreed = false;
  let revoked = false;
  let client: MCPClientState | undefined;
  try {
    const adminResults = await env.api.admin<any>(
      'GET',
      `/api/v1/tasks/${encodeURIComponent(taskId)}/results`
      + `?limit=${TASK_RESULT_PAGE_LIMIT}&offset=0&include_invalid=true`,
    );
    client = await initializeMCP(env.baseUrl, tokenResponse.token);
    const mcpResults = await callMCP(env.baseUrl, client, 'get_task_results', {
      taskId,
      limit: TASK_RESULT_PAGE_LIMIT,
      offset: 0,
      includeInvalid: true,
    });
    const adminBatches = adminResults.page?.batches ?? [];
    const adminInvalid = adminResults.page?.invalidBatches ?? [];
    const mcpBatches = mcpResults?.batches ?? [];
    const mcpInvalid = mcpResults?.invalid ?? [];
    assertTaskResultSurfacesAgree(caseName, {
      taskId: adminResults.taskId,
      ruleId: adminResults.ruleId,
      ruleVersion: adminResults.ruleVersionNumber,
      outputSchema: adminResults.outputSchema,
      sourceKind: adminResults.sourceKind,
      sourceAuthority: adminResults.sourceAuthority,
      sourceArtifactHash: adminResults.sourceArtifactHash,
      sourceExportHash: adminResults.sourceExportHash,
      sourceWorkflowId: adminResults.sourceWorkflowId,
      total: adminResults.page?.total,
      batches: adminBatches,
      invalid: adminInvalid,
      summary: adminResults.page?.summary,
    }, {
      taskId: mcpResults?.taskId,
      ruleId: mcpResults?.ruleId,
      ruleVersion: mcpResults?.ruleVersion,
      outputSchema: mcpResults?.outputSchema,
      sourceKind: mcpResults?.sourceKind,
      sourceAuthority: mcpResults?.sourceAuthority,
      sourceArtifactHash: mcpResults?.sourceArtifactHash,
      sourceExportHash: mcpResults?.sourceExportHash,
      sourceWorkflowId: mcpResults?.sourceWorkflowId,
      total: mcpResults?.total,
      batches: mcpBatches,
      invalid: mcpInvalid,
      summary: mcpResults?.summary,
    });
    agreed = true;
  } finally {
    try {
      await env.api.admin('DELETE', `/admin/mcp/tokens/${encodeURIComponent(tokenResponse.id)}`);
      const revocationProbeClient = client ?? {
        token: tokenResponse.token,
        nextId: 1,
      };
      await assertRevokedCredentialRejected(
        `task case ${caseName} MCP token`,
        () => callMCP(env.baseUrl, revocationProbeClient, 'get_task_results', {
          taskId,
          limit: 1,
          offset: 0,
        }),
        (error) => error instanceof MCPHTTPError && error.status === 401,
      );
      revoked = true;
    } finally {
      markPhase(run, 'retrieval', startedAt);
    }
  }
  return { agreed, revoked };
}

async function captureFailureArtifacts(run: ScenarioRun): Promise<void> {
  const { env, artifactsDir } = run;
  fs.mkdirSync(artifactsDir, { recursive: true });
  const safeWrite = (name: string, content: string) => {
    try {
      fs.writeFileSync(path.join(artifactsDir, name), content);
    } catch {
      // artifact capture is best-effort
    }
  };
  safeWrite('server-diagnostics.txt', env.diagnostics.value.trim() || '(no server diagnostics captured)');
  if (run.recording) {
    try {
      writeRecordingEventTraceArtifact(artifactsDir, run.recording);
    } catch {
      // artifact capture is best-effort
    }
  }
  if (run.tracker) {
    try {
      await run.tracker.poll();
      safeWrite('llm-jobs.json', JSON.stringify({
        requirementJobs: run.tracker.requirementJobList(),
        dslJobs: run.tracker.dslJobList(),
        dslWorkflow: run.tracker.dslWorkflow() ?? null,
      }, null, 2));
      const workflow = run.tracker.dslWorkflowArtifact();
      if (workflow) safeWrite('dsl-workflow.json', JSON.stringify(workflow, null, 2));
      const replays = run.tracker.replayAttemptList();
      if (replays.length > 0) safeWrite('dsl-replays.json', JSON.stringify(replays, null, 2));
      for (const job of run.tracker.dslJobList()) {
        for (let attempt = 1; attempt <= job.attemptCount; attempt += 1) {
          try {
            const detail = await env.api.admin<Record<string, unknown>>(
              'GET',
              `/api/v1/dsl-jobs/${encodeURIComponent(job.id)}/provider-attempts/${attempt}`,
            );
            safeWrite(
              `dsl-provider-attempt-${job.id}-${attempt}.json`,
              JSON.stringify(detail, null, 2),
            );
          } catch {
            // A bounded attempt artifact may be unavailable after an early
            // pre-provider failure; retain the other diagnostics.
          }
        }
      }
    } catch {
      // server may already be unreachable
    }
  }
  const pages = env.contextClosed ? [] : env.context.pages();
  for (let index = 0; index < pages.length; index += 1) {
    const page = pages[index];
    try {
      await page.screenshot({ path: path.join(artifactsDir, `page-${index}.png`), fullPage: false });
      safeWrite(`page-${index}.txt`, `url: ${page.url()}\n${
        (await page.locator('#status').textContent().catch(() => '')) ?? ''
      }\n${(await page.locator('.replay-logs').textContent().catch(() => '')) ?? ''}`);
    } catch {
      // page may already be closed
    }
  }
}

/**
 * Run one scenario against the shared environment. Failures dump bounded
 * sanitized diagnostics into a fresh os.tmpdir() directory (path reported on
 * the report and printed) and never abort the remaining scenarios.
 */
export async function runScenario(
  env: LiveEnvironment,
  scenario: LiveWorkflowScenario,
  options: LiveWorkflowRunOptions,
): Promise<LiveWorkflowReport> {
  const startedAt = new Date();
  const run: ScenarioRun = {
    scenario,
    taskCases: [],
    env,
    artifactsDir: fs.mkdtempSync(path.join(os.tmpdir(), `aegis-live-wf-artifacts-${scenario.name}-`)),
    phases: {},
  };
  const report: LiveWorkflowReport = {
    scenario: scenario.name,
    status: 'failed',
    provider: `${options.llm.providerLabel}/${options.llm.model}`,
    startedAt: startedAt.toISOString(),
    finishedAt: '',
    durationMs: 0,
    phases: run.phases,
    llm: {
      strict: true,
      expectedProvider: options.llm.providerAdapter,
      expectedModel: options.llm.model,
      tokenUsageExposed: false,
      inputTokens: 0,
      outputTokens: 0,
      requirementJobs: [],
      dslJobs: [],
      replayAttempts: 0,
    },
    results: { tasks: [], summary: false },
    mcp: { agreedWithAdmin: false, tokenRevoked: false },
  };
  const errors5xxAtStart = env.api.errors5xx.length;
  const openedPages: Page[] = [];
  const verificationProgress: ScenarioVerificationProgress = {
    tasks: [],
    resultsSummary: false,
    mcp: { agreedWithAdmin: false, tokenRevoked: false },
  };

  try {
    // Validate the full task matrix before recording, public navigation, or
    // provider work can begin.
    run.taskCases = resolveScenarioTaskCases(scenario);
    let recording: PageAgentRecording;
    if (options.reusableRecording) {
      recording = await restoreScenarioRecording(run, options.reusableRecording);
      run.recording = recording;
      scenario.assertRecording?.(recording);
    } else {
      const target = await env.context.newPage();
      openedPages.push(target);
      recording = await recordScenario(run, target);
      run.recording = recording;
      scenario.assertRecording?.(recording);
      options.saveRecording?.(recording);
      // The persisted recording is authoritative. Closing its page prevents a
      // stale demonstration tab from satisfying a later replay-page oracle.
      await target.close();
    }
    if (scenario.realSiteCostBudget) {
      const recordingInputFloor = estimateRecordingInputTokens(recording);
      assert(recordingInputFloor <= scenario.realSiteCostBudget.inputTokenLimit,
        `recording alone forecasts at least ${recordingInputFloor} input tokens, over the `
        + `${scenario.realSiteCostBudget.inputTokenLimit} model-priced real-site ceiling`);
    }

    const intent = await openFreshIntentPage(env);
    openedPages.push(intent);
    await driveRequirementPhase(run, intent);
    await driveDSLPhase(run, intent);

    await run.tracker?.poll();
    const preTaskRequirementJobs = run.tracker?.requirementJobList() ?? [];
    const preTaskDSLJobs = run.tracker?.dslJobList() ?? [];
    const preTaskProviderJobs = [...preTaskRequirementJobs, ...preTaskDSLJobs].filter((job) =>
      job.provider === options.llm.providerAdapter && job.model === options.llm.model);
    const providerCalls = scenario.expectedProviderCalls === undefined
      ? undefined
      : await countProviderCalls(
        env.api,
        preTaskRequirementJobs,
        preTaskDSLJobs,
        preTaskProviderJobs,
    );
    if (scenario.expectedProviderCalls !== undefined) {
      // Chunked DSL generation legitimately expands one logical generation
      // pass into chunkCount analysis calls plus one synthesis call (a
      // recording that fits the budget stays a single final call), so the
      // one-shot guard derives the chunk-aware expectation instead of
      // mistaking that fan-out for retry leakage.
      const chunkFanout = preTaskDSLJobs
        .filter((job) => preTaskProviderJobs.includes(job))
        .reduce((sum, job) => sum + (job.chunkCount > 1 ? job.chunkCount : 0), 0);
      assert(providerCalls === scenario.expectedProviderCalls + chunkFanout,
        `scenario expected exactly ${scenario.expectedProviderCalls + chunkFanout} physical provider call(s) `
        + `(${scenario.expectedProviderCalls} one-shot plus ${chunkFanout} chunk fan-out), observed ${providerCalls}`);
    }

    await createAndRunTasks(run, (task) => {
      verificationProgress.tasks.push(task);
    });
    verificationProgress.resultsSummary = true;
    verificationProgress.mcp = {
      agreedWithAdmin: verificationProgress.tasks.every((task) => task.mcpAgreedWithAdmin),
      tokenRevoked: verificationProgress.tasks.every((task) => task.mcpTokenRevoked),
    };

    // Cost guard: the fixture tier must stay cheap. When the job APIs expose
    // token usage, summed input tokens above the scenario budget fail the run.
    const totals = run.tracker?.tokenTotals() ?? { exposed: false, inputTokens: 0, outputTokens: 0 };
    if (!totals.exposed && scenario.realSiteCostBudget) {
      throw new Error('real-site cost guard requires provider-reported token usage');
    } else if (!totals.exposed) {
      console.log('[live-wf] token usage not exposed by the job APIs; budget guard skipped');
    } else if (scenario.inputTokenBudget !== undefined) {
      assert(totals.inputTokens <= scenario.inputTokenBudget,
        `scenario consumed ${totals.inputTokens} input tokens, over the ${scenario.inputTokenBudget} budget`);
    }
    const realSiteCostBudget = scenario.realSiteCostBudget;
    const estimatedCostUpperBoundUSD = realSiteCostBudget && totals.exposed
      ? estimateCostUpperBound(realSiteCostBudget, totals.inputTokens, totals.outputTokens)
      : undefined;
    if (estimatedCostUpperBoundUSD !== undefined && realSiteCostBudget) {
      assert(estimatedCostUpperBoundUSD <= realSiteCostBudget.budgetUSD,
        `scenario conservative cost estimate $${estimatedCostUpperBoundUSD.toFixed(4)} exceeds `
        + `$${realSiteCostBudget.budgetUSD.toFixed(2)} real-site budget`);
    }

    // Suite-level invariants: every durable LLM job completed, repairs stayed
    // bounded, and the suite observed no new server 5xx during this scenario.
    const requirementJobs = run.tracker?.requirementJobList() ?? [];
    const dslJobs = run.tracker?.dslJobList() ?? [];
    const unfinished = [...requirementJobs, ...dslJobs].filter((job) => job.status !== 'completed');
    assert(unfinished.length === 0,
      `durable LLM jobs did not all complete: ${JSON.stringify(unfinished.map((job) => `${job.kind}:${job.status}`))}`);
    assert((run.repairCount ?? 0) <= MAX_REPAIR_BUDGET, `repair budget exceeded: ${run.repairCount}`);
    const workflow = run.tracker?.dslWorkflow();
    assert(workflow, 'strict qualification did not observe the DSL workflow');
    // A requirement selected from an already structured candidate is normalized
    // deterministically and makes no provider request. It is the only job type
    // excluded from provider/model evidence. DSL jobs remain visible even when
    // they claim to be deterministic so degraded baseline generation cannot pass.
    const strictJobs = strictQualificationJobs(requirementJobs, dslJobs);
    assertStrictQualification({
      expectedProvider: options.llm.providerAdapter,
      expectedModel: options.llm.model,
      jobs: strictJobs,
      dslJobKinds: dslJobs.map((job) => job.kind),
      repairCount: workflow.repairCount,
      maxRepairs: workflow.maxRepairs,
      replayAttempts: run.tracker?.replayAttemptCount() ?? 0,
      tasks: verificationProgress.tasks,
      taskExpectations: run.taskCases.map((taskCase) => ({
        caseName: taskCase.name,
        expectedStatus: taskCase.expectedStatus,
      })),
    });
    const newErrors = env.api.errors5xx.slice(errors5xxAtStart);
    assert(newErrors.length === 0,
      `server returned 5xx during the run: ${JSON.stringify(newErrors.slice(0, 5))}`);

    report.status = 'passed';
    report.llm = {
      strict: true,
      expectedProvider: options.llm.providerAdapter,
      expectedModel: options.llm.model,
      tokenUsageExposed: totals.exposed,
      inputTokens: totals.inputTokens,
      outputTokens: totals.outputTokens,
      providerCalls,
      inputTokenBudget: scenario.inputTokenBudget,
      costBudgetUSD: scenario.realSiteCostBudget?.budgetUSD,
      inputUSDPerMillion: scenario.realSiteCostBudget?.inputUSDPerMillion,
      outputUSDPerMillion: scenario.realSiteCostBudget?.outputUSDPerMillion,
      estimatedCostUpperBoundUSD,
      requirementJobs,
      dslJobs,
      workflow: run.tracker?.dslWorkflow(),
      replayAttempts: run.tracker?.replayAttemptCount() ?? 0,
    };
  } catch (error) {
    report.error = error instanceof Error ? error.message : String(error);
    await captureFailureArtifacts(run);
    const totals = run.tracker?.tokenTotals() ?? { exposed: false, inputTokens: 0, outputTokens: 0 };
    report.llm = {
      strict: true,
      expectedProvider: options.llm.providerAdapter,
      expectedModel: options.llm.model,
      tokenUsageExposed: totals.exposed,
      inputTokens: totals.inputTokens,
      outputTokens: totals.outputTokens,
      providerCalls: undefined,
      inputTokenBudget: scenario.inputTokenBudget,
      costBudgetUSD: scenario.realSiteCostBudget?.budgetUSD,
      inputUSDPerMillion: scenario.realSiteCostBudget?.inputUSDPerMillion,
      outputUSDPerMillion: scenario.realSiteCostBudget?.outputUSDPerMillion,
      estimatedCostUpperBoundUSD: scenario.realSiteCostBudget && totals.exposed
        ? estimateCostUpperBound(scenario.realSiteCostBudget, totals.inputTokens, totals.outputTokens)
        : undefined,
      requirementJobs: run.tracker?.requirementJobList() ?? [],
      dslJobs: run.tracker?.dslJobList() ?? [],
      workflow: run.tracker?.dslWorkflow(),
      replayAttempts: run.tracker?.replayAttemptCount() ?? 0,
    };
    report.artifactsDir = run.artifactsDir;
    console.error(`[live-wf:${scenario.name}] scenario failed: ${report.error}`);
    console.error(`[live-wf:${scenario.name}] failure artifacts preserved at ${run.artifactsDir}`);
  } finally {
    report.recordingId = run.recordingId;
    report.requirementId = run.requirementId;
    report.ruleId = run.ruleId;
    report.ruleVersion = run.ruleVersion;
    report.repairCount = run.repairCount;
    const verified = snapshotVerificationProgress(verificationProgress);
    report.results = verified.results;
    report.mcp = verified.mcp;
    report.finishedAt = new Date().toISOString();
    report.durationMs = Date.now() - startedAt.getTime();
    for (const page of openedPages) await page.close().catch(() => undefined);
    if (report.status === 'passed') {
      fs.rmSync(run.artifactsDir, { recursive: true, force: true });
    }
  }
  return report;
}
