import type {
  IntentCandidate,
  PredictIntentResponse,
  PredictIntentResult,
  GenerateDslResult,
  UploadConfirmedRuleResult,
  WizardState,
  ReplayProgress,
  CollectionRequirementSpec,
  RequirementCandidate,
  RequirementWorkflowResult,
  DSLWorkflowResult,
  LLMJobProgress,
  InFlightOp,
} from './intent-types';
import { isCollectionRequirementSpec, containsUnsafeRequirement } from './intent-types';
import { redactString, hasSensitiveContent } from './client-redact';
import { sendAction } from '../messaging';

let keepAlivePort: ReturnType<typeof chrome.runtime.connect> | null = null;

const MAX_RECONNECT_ATTEMPTS = 3;
const RECONNECT_WINDOW_MS = 10_000;
const HEARTBEAT_TIMEOUT_MS = 5_000;
let reconnectAttempts = 0;
let reconnectWindowStart = 0;
let lastPongAt = Date.now();

function connectKeepAlive(): void {
  try {
    keepAlivePort = chrome.runtime.connect({ name: 'intent-wizard' });
    reconnectAttempts = 0;
    reconnectWindowStart = 0;
    lastPongAt = Date.now();

    keepAlivePort.onMessage.addListener((msg: unknown) => {
      if ((msg as { type?: string })?.type === 'pong') lastPongAt = Date.now();
    });

    keepAlivePort.onDisconnect.addListener(() => {
      keepAlivePort = null;
      attemptReconnect();
    });
  } catch {
    keepAlivePort = null;
    attemptReconnect();
  }
}

function attemptReconnect(): void {
  const now = Date.now();
  if (reconnectWindowStart === 0 || now - reconnectWindowStart > RECONNECT_WINDOW_MS) {
    reconnectWindowStart = now;
    reconnectAttempts = 0;
  }
  reconnectAttempts++;
  if (reconnectAttempts > MAX_RECONNECT_ATTEMPTS) {
    setStatus('扩展运行时连接不稳定，长任务可能中断', 'error');
    return;
  }
  const delay = 100 * Math.pow(2, reconnectAttempts - 1);
  setTimeout(connectKeepAlive, delay);
}

const MAX_REPLAY_DIAGNOSTIC_CLIENT_BYTES = 48 * 1024;
const MAX_REPLAY_DIAGNOSTIC_LOGS = 100;
const MAX_REPLAY_DIAGNOSTIC_FALLBACK_LOGS = 20;
const MAX_REPLAY_DIAGNOSTIC_EXTRA_BYTES = 4 * 1024;
const MAX_REPLAY_ARTIFACT_CLIENT_BYTES = 512 * 1024;
const MAX_REPLAY_ARTIFACT_DATA_BYTES = 384 * 1024;
const MAX_REPLAY_ARTIFACTS = 4;
const MAX_REPLAY_OUTPUT_ROWS = 1000;
const MAX_REPLAY_OUTPUT_CLIENT_BYTES = 1024 * 1024; // 1 MiB
// H-1: cap in-memory replay log/result arrays to prevent OOM on long replays.
const MAX_REPLAY_LOGS_IN_MEMORY = 500;

const CUSTOM_INTENT_STORAGE_KEY = 'oc_intent_custom_description';

export const state: WizardState = {
  step: 'intent',
  candidates: [],
  fallbackIntent: { id: 'custom', label: '其他目的（自行输入）', description: '', confidence: 0 },
  selectedIntent: null,
  customIntentDescription: '',
  workflowV2: false,
  candidatesRequested: false,
  requirementCandidates: [],
  selectedRequirementCandidate: null,
  normalizedRequirement: null,
  requirementBaseline: '',
  requirementId: null,
  requirementJobId: null,
  requirementReviewReady: false,
  dslWorkflowId: null,
  dslJobId: null,
  replayAttemptId: null,
  browserProfileId: 'current-chrome-profile',
  rule: null,
  yaml: '',
  confirmedSteps: new Set(),
  ruleId: null,
  error: null,
  loading: false,
  inFlight: new Set<InFlightOp>(),
  replayLogs: [],
  replayStatus: 'idle',
  replayError: null,
  replayVariables: {},
  replayExtracted: {},
  replayResults: [],
};

export function getEl<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

export function withInFlight<T>(op: InFlightOp, fn: () => Promise<T>): Promise<T | undefined> {
  if (state.inFlight.has(op)) return Promise.resolve(undefined);
  state.inFlight.add(op);
  renderActions();
  return fn().then(
    (result) => {
      state.inFlight.delete(op);
      renderActions();
      return result;
    },
    (err) => {
      state.inFlight.delete(op);
      renderActions();
      throw err;
    },
  );
}

export function setStatus(message: string, type: 'info' | 'error' | 'success' = 'info'): void {
  const el = getEl<HTMLDivElement>('status');
  if (el) {
    el.textContent = message;
    el.className = type;
  }
  hideJobProgress();
}

function hideJobProgress(): void {
  getEl<HTMLDivElement>('job-progress')?.classList.add('hidden');
}

// renderJobProgress shows determinate per-chunk progress while a durable LLM
// job is running. Unknown totals keep the existing indeterminate spinner text.
export function renderJobProgress(progress: LLMJobProgress): void {
  const container = getEl<HTMLDivElement>('job-progress');
  if (!container) return;
  const total = progress.chunkCount ?? 0;
  if (total <= 0) {
    container.classList.add('hidden');
    return;
  }
  const completed = Math.min(Math.max(progress.completedChunks ?? 0, 0), total);
  container.classList.remove('hidden');
  const text = getEl<HTMLSpanElement>('job-progress-text');
  if (text) text.textContent = `正在分析… (${completed}/${total})`;
  const bar = getEl<HTMLProgressElement>('job-progress-bar');
  if (bar) {
    bar.max = total;
    bar.value = completed;
  }
}

export function showStep(step: WizardState['step']): void {
  state.step = step;
  document.querySelectorAll('.step').forEach((el) => el.classList.add('hidden'));
  const section = getEl<HTMLElement>(`step-${step}`);
  if (section) section.classList.remove('hidden');
  if (step === 'intent') renderIntentHint();
  renderActions();
}

// The intent-step hint reflects the current mode: legacy predictions, fresh
// on-demand candidate generation, or an already requested candidate set.
function renderIntentHint(): void {
  const hint = getEl<HTMLParagraphElement>('intent-hint');
  if (!hint) return;
  if (!state.workflowV2) {
    hint.textContent = '请选择最符合此次操作的目的，或输入自定义目的';
    return;
  }
  hint.textContent = state.candidatesRequested
    ? '选择或编辑一个候选需求；也可以直接输入采集意图继续'
    : '输入采集意图直接继续；或点击“生成 3 个候选需求”后从中选择';
}

function persistCustomIntent(value: string): void {
  state.customIntentDescription = value;
  void chrome.storage.session.set({ [CUSTOM_INTENT_STORAGE_KEY]: value });
}

// Restores the typed intent from session storage so closing and reopening the
// wizard does not lose it.
async function restoreCustomIntent(): Promise<void> {
  const stored = await chrome.storage.session.get(CUSTOM_INTENT_STORAGE_KEY);
  const saved = stored[CUSTOM_INTENT_STORAGE_KEY];
  const input = getEl<HTMLTextAreaElement>('custom-description');
  if (typeof saved === 'string' && saved.trim().length > 0 && input && !input.value.trim()) {
    input.value = saved;
    state.customIntentDescription = saved;
  }
}

export async function loadPredictions(): Promise<void> {
  state.loading = true;
  setStatus('正在加载采集需求工作流...');
  try {
    await restoreCustomIntent();
    // Capability probe only: candidate generation is the most expensive phase
    // and stays on demand until the user explicitly requests it.
    const workflow = (await sendAction('GET_REQUIREMENT_WORKFLOW', { startCandidates: false })) as RequirementWorkflowResult;
    if (workflow.workflowV2) {
      applyRequirementWorkflow(workflow);
      return;
    }
    const response = (await sendAction('PREDICT_INTENT')) as PredictIntentResult;
    if (response.success === false) {
      const detail = response.error || '预测失败';
      console.error('[intent-page] PREDICT_INTENT failed:', detail);
      setStatus(detail, 'error');
      state.error = detail;
      // Reveal the intent step so the user can fall back to the custom intent
      // textarea instead of staring at an empty wizard.
      showStep('intent');
      return;
    }
    state.candidates = response.candidates || [];
    state.fallbackIntent = response.fallbackIntent || state.fallbackIntent;
    renderCandidates();
    setStatus('请选择最符合的目的', 'info');
    showStep('intent');
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    setStatus(`加载预测失败：${message}`, 'error');
    state.error = message;
    // Fall back to manual custom-intent entry when prediction is unavailable.
    showStep('intent');
  } finally {
    state.loading = false;
    renderActions();
  }
}

// applyRequirementWorkflow renders the intent step from a requirement workflow
// response. A response without candidates is the cheap capability probe used on
// wizard open; a response with candidates or a failure comes from a requested
// or resumed candidates job.
function applyRequirementWorkflow(workflow: RequirementWorkflowResult): void {
  state.workflowV2 = true;
  state.requirementJobId = workflow.job?.id ?? null;
  if (workflow.success === false) {
    state.candidatesRequested = true;
    const detail = workflow.error || '采集需求候选生成失败；可以重试或手动填写结构化需求';
    setStatus(detail, 'error');
    state.error = detail;
    renderCandidates();
    showStep('intent');
    return;
  }
  if (workflow.candidates) {
    state.candidatesRequested = true;
    state.requirementCandidates = workflow.candidates;
    if (state.requirementCandidates.length !== 3) {
      const detail = `服务端必须返回 3 个候选需求，实际返回 ${state.requirementCandidates.length} 个`;
      setStatus(detail, 'error');
      state.error = detail;
    } else {
      setStatus('请选择、编辑或输入自定义采集需求', 'info');
    }
  } else {
    state.requirementCandidates = [];
    setStatus('输入采集意图后直接继续；或点击“生成 3 个候选需求”查看建议', 'info');
  }
  renderCandidates();
  showStep('intent');
}

export function requestRequirementCandidates(): Promise<void> {
  return withInFlight('candidates', async () => {
    state.candidatesRequested = true;
    state.loading = true;
    setStatus('正在生成 3 个候选需求；重新打开向导可继续等待结果...', 'info');
    renderActions();
    try {
      const workflow = (await sendAction('GET_REQUIREMENT_WORKFLOW', { startCandidates: true })) as RequirementWorkflowResult;
      if (!workflow.workflowV2) {
        throw new Error('服务端不支持采集需求工作流');
      }
      applyRequirementWorkflow(workflow);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setStatus(`候选需求生成失败：${message}`, 'error');
      state.error = message;
    } finally {
      state.loading = false;
      renderActions();
    }
  }) as Promise<void>;
}

export async function resumeDSLWorkflow(): Promise<boolean> {
  const stored = await chrome.storage.session.get('oc_dsl_workflow');
  if (!stored.oc_dsl_workflow) {
    // WI-1: background may have reconciled an in-flight requirement
    // candidate/normalize job. Re-enter polling instead of returning false.
    return restoreFromReconciledJobs();
  }
  state.normalizedRequirement = null;
  state.replayVariables = {};
  try {
    const response = (await sendAction('RESUME_DSL_WORKFLOW')) as DSLWorkflowResult & {
      active?: boolean;
      replayId?: string;
    };
    if (response.success === false && response.active && !response.workflow) {
      throw new Error(response.error || '服务端无法恢复 DSL 工作流');
    }
    if (!response.active || !response.workflow) return false;
    const workflow = response.workflow;
    const replayCapable = workflow.status === 'awaiting_replay'
      || workflow.status === 'replaying';
    if (replayCapable) {
      const requirement = response.requirement;
      if (!isCollectionRequirementSpec(requirement)) {
        throw new Error('恢复的 DSL 工作流缺少有效的已确认采集需求');
      }
      populateRequirementEditor(requirement);
    }
    state.workflowV2 = true;
    state.requirementId = workflow.requirementId;
    state.dslWorkflowId = workflow.id;
    state.dslJobId = workflow.currentJobId ?? null;
    state.replayAttemptId = response.replayId ?? null;
    state.browserProfileId = workflow.browserProfileId;
    state.rule = workflow.provisionalRule ?? null;
    state.yaml = workflow.provisionalYaml ?? '';
    if (workflow.status === 'awaiting_replay' && state.rule) {
      renderPreview();
      setStatus('已恢复临时 DSL 工作流，请继续完整回放', 'info');
      showStep('preview');
      return true;
    }
    if (workflow.status === 'replaying') {
      state.replayStatus = 'running';
      setStatus('已恢复正在运行的完整回放', 'info');
      showStep('replay');
      renderReplayMonitor();
      return true;
    }
    if (workflow.status === 'awaiting_confirmation') {
      state.replayStatus = 'success';
      setStatus('已恢复已验证工作流，请确认并批准规则版本', 'success');
      showStep('replay');
      renderReplayMonitor();
      return true;
    }
    if (workflow.status === 'approved') {
      state.ruleId = workflow.approvedRuleId ?? null;
      const result = getEl<HTMLParagraphElement>('save-result');
      if (result) result.textContent = `规则版本已批准：${workflow.approvedRuleId ?? ''} v${workflow.approvedVersion ?? ''}`;
      setStatus('规则版本已批准', 'success');
      showStep('save');
      return true;
    }
    if (workflow.status === 'failed') {
      state.replayStatus = 'failure';
      state.replayError = workflow.errorMessage || 'DSL 工作流失败';
      setStatus(state.replayError, 'error');
      showStep('replay');
      renderReplayMonitor();
      if (!workflow.provisionalRule) {
        showCorrectionEditor('replay').catch(() => undefined);
      }
      return true;
    }
    return false;
  } catch (error) {
    setStatus(`恢复 DSL 工作流失败：${error instanceof Error ? error.message : String(error)}`, 'error');
    showStep('intent');
    return true;
  }
}

// restoreFromReconciledJobs consults the candidateJob/normalizeJob fields
// the background's RESUME_DSL_WORKFLOW handler surfaces after SW eviction.
// If either is live (running/pending), re-enter polling on the intent step.
// If either is terminal (failed/completed), clear the stale session key and
// return false so the wizard lands on a clean intent step.
async function restoreFromReconciledJobs(): Promise<boolean> {
  const response = (await sendAction('RESUME_DSL_WORKFLOW')) as DSLWorkflowResult & {
    active?: boolean;
    candidateJob?: { id: string; status: string };
    normalizeJob?: { id: string; status: string };
  };
  const pickLive = (
    job: { id: string; status: string } | undefined,
    kind: 'candidate' | 'normalize',
  ): { kind: 'candidate' | 'normalize'; job: { id: string; status: string } } | null => {
    if (!job) return null;
    if (job.status === 'running' || job.status === 'pending') return { kind, job };
    return null;
  };
  const live =
    pickLive(response.candidateJob, 'candidate') ||
    pickLive(response.normalizeJob, 'normalize');
  if (!live) {
    // Stale terminal entries: clear so the wizard shows a clean intent step
    // and the user can use the existing retry affordance.
    if (response.candidateJob || response.normalizeJob) {
      void chrome.storage.session.remove('oc_requirement_candidate_job');
      void chrome.storage.session.remove('oc_requirement_normalize_job');
    }
    return false;
  }
  state.workflowV2 = true;
  state.candidatesRequested = true;
  state.requirementJobId = live.job.id;
  state.loading = true;
  setStatus(
    live.kind === 'candidate'
      ? '正在生成候选需求；重新打开向导可继续等待结果...'
      : '正在规范化需求；重新打开向导可继续等待结果...',
    'info',
  );
  renderCandidates();
  showStep('intent');
  // WI-1: restart the background's pollRequirementJob loop for the orphaned
  // job. Without this, no LLM_JOB_PROGRESS messages arrive and the spinner
  // is static. Fire-and-forget — the existing onMessage listener handles
  // progress + terminal.
  void sendAction('GET_REQUIREMENT_WORKFLOW', { resumeJobId: live.job.id })
    .then((response) => {
      const result = response as RequirementWorkflowResult;
      if (result.success === false && result.job?.status !== 'running') {
        setStatus(result.error || '采集需求任务失败；可以重试或手动填写结构化需求', 'error');
        state.loading = false;
        renderActions();
      } else if (result.success && result.job?.status === 'completed' && result.candidates) {
        // Server already finished between eviction and reopen.
        state.requirementCandidates = result.candidates ?? [];
        state.requirementJobId = result.job?.id ?? live.job.id;
        state.candidatesRequested = true;
        state.loading = false;
        renderCandidates();
        setStatus('请选择、编辑或输入自定义采集需求', 'info');
        renderActions();
      }
    })
    .catch((err) => {
      // M-8: if sendAction itself rejects (SW unreachable, port closed),
      // surface the error rather than leaving the user on a permanent spinner.
      setStatus(`恢复需求任务失败：${err instanceof Error ? err.message : String(err)}`, 'error');
      state.loading = false;
      renderActions();
    });
  return true;
}

export function renderCandidates(): void {
  const container = getEl<HTMLDivElement>('candidates-list');
  if (!container) return;
  container.innerHTML = '';
  if (state.workflowV2) {
    state.requirementCandidates.forEach((candidate) => {
      const card = document.createElement('div');
      const requirement = candidate.requirement;
      card.className = 'candidate-card requirement-candidate';
      card.innerHTML = `
        <label>
          <input type="radio" name="requirement-candidate" value="${escapeHtml(candidate.id)}" />
          <strong>${escapeHtml(requirement.title)}</strong>
          ${candidate.source === 'synthetic' ? '<span class="synthetic-badge">示例候选（非 LLM 生成）</span>' : ''}
          <span class="confidence">${typeof candidate.confidence === 'number' ? (candidate.confidence * 100).toFixed(0) : '?'}%</span>
          <span class="candidate-description">${escapeHtml(requirement.description)}</span>
          <span class="candidate-detail"><b>输入：</b>${escapeHtml(formatRequirementInputs(requirement))}</span>
          <span class="candidate-detail"><b>输出：</b>${escapeHtml(requirement.outputFields.map((field) => `${field.name}: ${field.type}`).join(', '))}</span>
          <code class="sample-output">${escapeHtml(JSON.stringify(requirement.sampleOutput))}</code>
        </label>
      `;
      card.querySelector('input')?.addEventListener('change', () => selectRequirementCandidate(candidate));
      container.appendChild(card);
    });
    return;
  }
  state.candidates.forEach((candidate) => {
    const card = document.createElement('div');
    card.className = 'candidate-card';
    card.innerHTML = `
      <label>
        <input type="radio" name="intent" value="${escapeHtml(candidate.id)}" />
        <strong>${escapeHtml(candidate.label)}</strong>
        <span class="confidence">${(candidate.confidence * 100).toFixed(0)}%</span>
        <span class="candidate-description">${escapeHtml(candidate.description)}</span>
      </label>
    `;
    card.querySelector('input')?.addEventListener('change', () => {
      selectCandidate(candidate);
    });
    container.appendChild(card);
  });
}

function formatRequirementInputs(requirement: CollectionRequirementSpec): string {
  const required = requirement.requiredInputs.map((input) => `${input.name}: ${input.type} (必填)`);
  const optional = requirement.optionalInputs.map((input) => `${input.name}: ${input.type} (可选)`);
  return [...required, ...optional].join(', ') || '无';
}

export function selectRequirementCandidate(candidate: RequirementCandidate): void {
  state.selectedRequirementCandidate = candidate;
  state.selectedIntent = null;
  const customInput = getEl<HTMLTextAreaElement>('custom-description');
  if (customInput) customInput.value = '';
  persistCustomIntent('');
  updateNextButton();
}

export function selectCandidate(candidate: IntentCandidate): void {
  state.selectedIntent = candidate;
  const customInput = getEl<HTMLTextAreaElement>('custom-description');
  if (customInput) customInput.value = '';
  persistCustomIntent('');
  state.selectedRequirementCandidate = null;
  updateNextButton();
}

export function updateNextButton(): void {
  if (state.step !== 'intent') return;
  renderActions();
}

export function buildIntentForGeneration(): IntentCandidate | null {
  const custom = getEl<HTMLTextAreaElement>('custom-description')?.value.trim() ?? '';
  state.customIntentDescription = custom;
  if (custom.length > 0) {
    return { ...state.fallbackIntent, label: custom, description: custom };
  }
  return state.selectedIntent;
}

function manualRequirementTemplate(): CollectionRequirementSpec {
  return {
    title: '',
    description: '',
    requiredInputs: [],
    optionalInputs: [],
    outputFields: [{ name: 'item', type: 'string', description: '采集结果字段' }],
    sampleOutput: { item: '' },
  };
}

export function populateRequirementEditor(requirement: CollectionRequirementSpec): void {
  const setValue = (id: string, value: string) => {
    const element = getEl<HTMLInputElement | HTMLTextAreaElement>(id);
    if (element) element.value = value;
  };
  setValue('requirement-title', requirement.title);
  setValue('requirement-description', requirement.description);
  setValue('requirement-required-inputs', JSON.stringify(requirement.requiredInputs, null, 2));
  setValue('requirement-optional-inputs', JSON.stringify(requirement.optionalInputs, null, 2));
  setValue('requirement-output-fields', JSON.stringify(requirement.outputFields, null, 2));
  setValue('requirement-sample-output', JSON.stringify(requirement.sampleOutput, null, 2));
  state.normalizedRequirement = requirement;
  state.requirementBaseline = JSON.stringify(requirement);
  state.replayVariables = {};
}

function parseEditorJSON<T>(id: string, label: string): T {
  const raw = getEl<HTMLTextAreaElement>(id)?.value.trim() ?? '';
  try {
    return JSON.parse(raw) as T;
  } catch {
    throw new Error(`${label}不是有效 JSON`);
  }
}

export function readRequirementEditor(): CollectionRequirementSpec {
  const requirement: CollectionRequirementSpec = {
    title: getEl<HTMLInputElement>('requirement-title')?.value.trim() ?? '',
    description: getEl<HTMLTextAreaElement>('requirement-description')?.value.trim() ?? '',
    requiredInputs: parseEditorJSON('requirement-required-inputs', '必填输入'),
    optionalInputs: parseEditorJSON('requirement-optional-inputs', '可选输入'),
    outputFields: parseEditorJSON('requirement-output-fields', '输出字段'),
    sampleOutput: parseEditorJSON('requirement-sample-output', '示例输出'),
  };
  if (!requirement.title || !requirement.description) throw new Error('标题和描述不能为空');
  if (!Array.isArray(requirement.requiredInputs) || !Array.isArray(requirement.optionalInputs)) {
    throw new Error('输入变量必须是 JSON 数组');
  }
  if (!Array.isArray(requirement.outputFields) || requirement.outputFields.length === 0) {
    throw new Error('至少需要一个输出字段');
  }
  if (!requirement.sampleOutput || Array.isArray(requirement.sampleOutput) || typeof requirement.sampleOutput !== 'object') {
    throw new Error('示例输出必须是 JSON 对象');
  }
  if (containsUnsafeRequirement(requirement)) {
    throw new Error('需求包含敏感字段（password/token/cookie 等），请移除后重试');
  }
  return requirement;
}

export function openManualRequirementEditor(): void {
  state.workflowV2 = true;
  state.selectedRequirementCandidate = null;
  state.requirementReviewReady = false;
  state.requirementId = null;
  populateRequirementEditor(manualRequirementTemplate());
  setStatus('手动填写结构化需求；此内容不会被标记为 LLM 生成', 'info');
  showStep('requirement');
}

export function prepareRequirement(): Promise<void> {
  // Shares the 'normalize' op with normalizeRequirementEditor below. Safe because the
  // step-based UI only surfaces one of them at a time (intent step vs requirement step).
  return withInFlight('normalize', async () => {
    const custom = getEl<HTMLTextAreaElement>('custom-description')?.value.trim() ?? '';
    if (custom) {
      // WI-10: client-side defense in depth. Length cap, unsafe-term scan, and
      // lightweight redaction run BEFORE NORMALIZE_REQUIREMENT is dispatched.
      const MAX_CUSTOM_TEXT_BYTES = 8 * 1024;
      if (new TextEncoder().encode(custom).length > MAX_CUSTOM_TEXT_BYTES) {
        setStatus('采集意图过长，请精简后重试', 'error');
        return;
      }
      if (containsUnsafeRequirement(custom)) {
        setStatus('采集意图包含敏感内容（password/token/cookie 等），请移除后重试', 'error');
        return;
      }
      const redactedCustom = redactString(custom);
      const wasRedacted = hasSensitiveContent(custom);
      if (wasRedacted) {
        setStatus('检测到可能的敏感内容，已自动脱敏', 'info');
      } else {
        setStatus('正在规范化自定义采集需求...');
      }
      state.loading = true;
      try {
        const response = (await sendAction('NORMALIZE_REQUIREMENT', { customText: redactedCustom })) as RequirementWorkflowResult;
        if (!response.success || !response.requirement || !response.requirementId) {
          state.requirementJobId = response.job?.id ?? null;
          throw new Error(response.error || '自定义需求规范化失败；可以重试或手动填写结构化需求');
        }
        populateRequirementEditor(response.requirement);
        state.requirementId = response.requirementId;
        state.requirementReviewReady = true;
        setStatus(wasRedacted ? '检测到可能的敏感内容，已自动脱敏；需求已规范化，请检查后确认' : '需求已规范化，请检查后确认', 'success');
        showStep('requirement');
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        state.error = message;
        setStatus(message, 'error');
      } finally {
        state.loading = false;
      }
      return;
    }
    if (!state.selectedRequirementCandidate) {
      setStatus('请选择候选需求、输入自定义需求或手动填写', 'error');
      return;
    }
    populateRequirementEditor(state.selectedRequirementCandidate.requirement);
    state.requirementId = null;
    state.requirementReviewReady = false;
    setStatus('可编辑结构化需求，然后进行服务端规范化', 'info');
    showStep('requirement');
  }) as Promise<void>;
}

export function normalizeRequirementEditor(): Promise<void> {
  return withInFlight('normalize', async () => {
    let requirement: CollectionRequirementSpec;
    try {
      requirement = readRequirementEditor();
    } catch (error) {
      setStatus(error instanceof Error ? error.message : String(error), 'error');
      return;
    }
    state.loading = true;
    setStatus('正在验证并规范化结构化需求...');
    try {
      const lineage = state.selectedRequirementCandidate && state.requirementJobId
        ? { candidateJobId: state.requirementJobId, candidateId: state.selectedRequirementCandidate.id }
        : {};
      const response = (await sendAction('NORMALIZE_REQUIREMENT', { requirement, ...lineage })) as RequirementWorkflowResult;
      if (!response.success || !response.requirement || !response.requirementId) {
        state.requirementJobId = response.job?.id ?? null;
        throw new Error(response.error || '结构化需求验证失败');
      }
      populateRequirementEditor(response.requirement);
      state.requirementId = response.requirementId;
      state.requirementReviewReady = true;
      setStatus('规范化完成。请检查字段和示例输出，然后确认', 'success');
      renderActions();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      state.error = message;
      setStatus(message, 'error');
    } finally {
      state.loading = false;
    }
  }) as Promise<void>;
}

export function confirmRequirement(): Promise<void> {
  return withInFlight('confirm-requirement', async () => {
    if (!state.requirementReviewReady || !state.requirementId) {
      await normalizeRequirementEditor();
      return;
    }
    state.loading = true;
    setStatus('正在确认采集需求...');
    try {
      const response = (await sendAction('CONFIRM_REQUIREMENT', { requirementId: state.requirementId })) as RequirementWorkflowResult;
      if (!response.success) throw new Error(response.error || '需求确认失败');
      const result = getEl<HTMLParagraphElement>('requirement-confirmed-result');
      if (result) result.textContent = `采集需求已确认，ID：${state.requirementId}`;
      const profile = getEl<HTMLInputElement>('browser-profile-id');
      if (profile) profile.value = state.browserProfileId;
      setStatus('采集需求已确认。请选择当前浏览器配置引用并生成临时 DSL', 'success');
      showStep('requirement-confirmed');
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      state.error = message;
      setStatus(message, 'error');
    } finally {
      state.loading = false;
    }
  }) as Promise<void>;
}


/**
 * Human rule correction for a FAILED generation workflow. Loads the
 * recording-derived baseline (preserving id/version/entry/domain is
 * mandatory), seeds the editor, and submits PUT /provisional through the
 * background service worker. On success the workflow moves to
 * awaiting_replay and the wizard continues with the normal preview flow.
 */
export async function showCorrectionEditor(scope: 'gen' | 'replay'): Promise<void> {
  if (!state.dslWorkflowId) {
    setStatus('没有可修正的 DSL 工作流', 'error');
    return;
  }
  const section = getEl<HTMLElement>(`correct-rule-section-${scope}`);
  const editor = getEl<HTMLTextAreaElement>(`correct-rule-editor-${scope}`);
  if (!section || !editor) return;
  if (!section.classList.contains('hidden') && editor.value.trim() !== '') return;
  setStatus('正在加载录制派生的基线规则...');
  const baselineResponse = (await sendAction('GET_DSL_WORKFLOW_BASELINE', {
    workflowId: state.dslWorkflowId,
  })) as { success?: boolean; baselineRule?: unknown; error?: string };
  if (!baselineResponse.success || !baselineResponse.baselineRule) {
    setStatus(baselineResponse.error || '基线规则不可用', 'error');
    return;
  }
  editor.value = JSON.stringify(baselineResponse.baselineRule, null, 2);
  section.classList.remove('hidden');
  setStatus('已载入基线规则；编辑后提交人工修正', 'info');
}

export async function submitRuleCorrection(scope: 'gen' | 'replay'): Promise<void> {
  if (!state.dslWorkflowId) {
    setStatus('没有可修正的 DSL 工作流', 'error');
    return;
  }
  const editor = getEl<HTMLTextAreaElement>(`correct-rule-editor-${scope}`);
  if (!editor) return;
  let rule: unknown;
  try {
    rule = JSON.parse(editor.value);
  } catch (error) {
    setStatus(`规则 JSON 无效：${error instanceof Error ? error.message : String(error)}`, 'error');
    return;
  }
  state.loading = true;
  setStatus('正在提交人工修正；服务端将执行与模型输出相同的安全校验...');
  try {
    const response = (await sendAction('CORRECT_DSL_WORKFLOW', {
      workflowId: state.dslWorkflowId,
      rule,
    })) as DSLWorkflowResult;
    if (!response.success || !response.workflow?.provisionalRule) {
      throw new Error(response.error || response.workflow?.errorMessage || '人工修正被服务端拒绝');
    }
    state.rule = response.workflow.provisionalRule;
    state.yaml = response.workflow.provisionalYaml ?? '';
    state.confirmedSteps.clear();
    getEl<HTMLElement>(`correct-rule-section-${scope}`)?.classList.add('hidden');
    renderPreview();
    setStatus('人工修正已通过校验，请预览后完整回放', 'success');
    showStep('preview');
  } catch (error) {
    setStatus(error instanceof Error ? error.message : String(error), 'error');
  } finally {
    state.loading = false;
  }
}

export function generateWorkflowDSL(): Promise<void> {
  return withInFlight('workflow-dsl', async () => {
    if (!state.requirementId) {
      setStatus('缺少已确认的采集需求', 'error');
      return;
    }
    const profileId = getEl<HTMLInputElement>('browser-profile-id')?.value.trim() ?? '';
    if (!profileId) {
      setStatus('请输入浏览器配置引用', 'error');
      return;
    }
    state.browserProfileId = profileId;
    state.loading = true;
    setStatus('正在生成并验证临时 DSL；任务可在重新打开向导后继续...', 'info');
    try {
      const response = (await sendAction('CREATE_DSL_WORKFLOW', {
        requirementId: state.requirementId,
        browserProfileId: profileId,
      })) as DSLWorkflowResult;
      // Capture the workflow id even from a failed generation: the workflow
      // exists server-side with its stashed baseline, and the human
      // correction editor needs the id to target it.
      if (response.workflow?.id) {
        state.dslWorkflowId = response.workflow.id;
      }
      if (!response.success || !response.workflow?.provisionalRule) {
        throw new Error(response.error || response.workflow?.errorMessage || '临时 DSL 生成失败');
      }
      state.dslJobId = response.job?.id ?? response.workflow.currentJobId ?? null;
      state.rule = response.workflow.provisionalRule;
      state.yaml = response.workflow.provisionalYaml ?? '';
      state.confirmedSteps.clear();
      renderPreview();
      setStatus('临时 DSL 已通过服务端校验，请预览后完整回放', 'success');
      showStep('preview');
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      state.error = message;
      setStatus(message, 'error');
      // A failed generation is recoverable through the human correction
      // editor whenever the workflow (and its stashed baseline) exists.
      if (state.dslWorkflowId) {
        showCorrectionEditor('gen').catch(() => undefined);
      }
    } finally {
      state.loading = false;
    }
  }) as Promise<void>;
}

export function retryRequirementJob(): Promise<void> {
  return withInFlight('retry-requirement', async () => {
    if (!state.requirementJobId) {
      await requestRequirementCandidates();
      return;
    }
    state.loading = true;
    setStatus('正在重试采集需求任务...');
    try {
      const response = (await sendAction('RETRY_REQUIREMENT_JOB', { jobId: state.requirementJobId })) as RequirementWorkflowResult;
      if (!response.success || !response.candidates) throw new Error(response.error || '重试失败');
      state.requirementCandidates = response.candidates;
      state.requirementJobId = response.job?.id ?? state.requirementJobId;
      renderCandidates();
      setStatus('候选需求已生成', 'success');
    } catch (error) {
      setStatus(error instanceof Error ? error.message : String(error), 'error');
    } finally {
      state.loading = false;
    }
  }) as Promise<void>;
}

export function markRequirementDirty(): void {
  if (state.step !== 'requirement') return;
  let current = '';
  try {
    current = JSON.stringify(readRequirementEditor());
  } catch {
    current = '';
  }
  if (current !== state.requirementBaseline) {
    state.requirementReviewReady = false;
    state.requirementId = null;
    renderActions();
  }
}

export function generateDSL(): Promise<void> {
  return withInFlight('generate-dsl', async () => {
    const intent = buildIntentForGeneration();
    if (!intent) {
      setStatus('请选择一个目的或输入自定义目的', 'error');
      return;
    }
    state.loading = true;
    setStatus('正在生成 DSL...');
    try {
      const isCustom = intent.id === 'custom' && state.customIntentDescription.length > 0;
      const action = isCustom ? 'GENERATE_DSL_FROM_INTENT_SERVER' : 'GENERATE_DSL_FROM_INTENT';
      const payload = isCustom
        ? { intent, customDescription: state.customIntentDescription }
        : { intent };
      const response = (await sendAction(action, payload)) as GenerateDslResult;
      if (response.success === false) {
        const detail = response.error || '生成失败';
        console.error('[intent-page] generate DSL failed:', detail);
        setStatus(detail, 'error');
        state.error = detail;
        return;
      }
      state.rule = response.rule ?? null;
      state.yaml = response.yaml ?? '';
      state.confirmedSteps.clear();
      renderPreview();
      setStatus('DSL 已生成，请预览', 'success');
      showStep('preview');
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setStatus(`生成 DSL 失败：${message}`, 'error');
      state.error = message;
    } finally {
      state.loading = false;
    }
  }) as Promise<void>;
}

export function renderPreview(): void {
  renderReplayInputs();
  const stepsList = getEl<HTMLDivElement>('steps-list');
  const yamlPreview = getEl<HTMLPreElement>('yaml-preview');
  const steps = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
    ? ((state.rule as Record<string, unknown>).steps as unknown[])
    : [];
  if (stepsList) {
    stepsList.innerHTML = steps
      .map((step: unknown, i: number) => {
        const s = step as Record<string, unknown>;
        const action = String(s.action || '');
        const targetSelector =
          (s.target as Record<string, unknown> | undefined)?.selector || s.url || '';
        return `
          <div class="step-item">
            <span class="step-num">${i + 1}</span>
            <span class="step-action">${escapeHtml(action)}</span>
            <span class="step-target" title="${escapeHtml(String(targetSelector))}">${escapeHtml(String(targetSelector))}</span>
          </div>
        `;
      })
      .join('');
  }
  if (yamlPreview) {
    yamlPreview.textContent = state.yaml;
  }
}

function replayInputValue(input: { name: string; default?: unknown }): unknown {
  if (Object.prototype.hasOwnProperty.call(state.replayVariables, input.name)) {
    return state.replayVariables[input.name];
  }
  return input.default;
}

function setReplayInputValue(
  field: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement,
  type: string,
  value: unknown,
): void {
  if (value === undefined) return;
  if (type === 'object' || type === 'array') {
    field.value = JSON.stringify(value);
  } else {
    field.value = String(value);
  }
}

export function renderReplayInputs(): void {
  const container = getEl<HTMLDivElement>('replay-inputs');
  if (!container) return;
  container.replaceChildren();
  const requirement = state.normalizedRequirement;
  const required = requirement?.requiredInputs ?? [];
  const optional = requirement?.optionalInputs ?? [];
  const inputs = [...required, ...optional];
  container.classList.toggle('hidden', inputs.length === 0);
  if (inputs.length === 0) return;

  const heading = document.createElement('h3');
  heading.textContent = '本次回放输入';
  const hint = document.createElement('p');
  hint.className = 'hint';
  hint.textContent = '这些值只用于当前临时 DSL 回放，不会写入规则。';
  const grid = document.createElement('div');
  grid.className = 'replay-input-grid';
  const requiredNames = new Set(required.map((input) => input.name));

  inputs.forEach((input, index) => {
    const label = document.createElement('label');
    label.className = 'replay-input-field';
    const title = document.createElement('span');
    title.textContent = `${input.name}${requiredNames.has(input.name) ? '（必填）' : '（可选）'}`;
    label.appendChild(title);

    let field: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
    if (input.type === 'boolean') {
      const select = document.createElement('select');
      for (const [value, text] of [['', '请选择'], ['true', '是'], ['false', '否']]) {
        const option = document.createElement('option');
        option.value = value;
        option.textContent = text;
        select.appendChild(option);
      }
      field = select;
    } else if (input.type === 'object' || input.type === 'array') {
      const textarea = document.createElement('textarea');
      textarea.rows = 3;
      textarea.placeholder = input.type === 'array' ? '[]' : '{}';
      field = textarea;
    } else {
      const element = document.createElement('input');
      element.type = input.secret ? 'password' : input.type === 'number' ? 'number' : 'text';
      if (input.type === 'number') element.step = 'any';
      element.autocomplete = 'off';
      field = element;
    }
    field.id = `replay-input-${index}`;
    field.dataset.replayInputName = input.name;
    field.dataset.replayInputType = input.type;
    field.dataset.replayInputRequired = String(requiredNames.has(input.name));
    if (input.secret) {
      field.disabled = true;
      field.title = '敏感输入必须通过命名浏览器配置或受管密钥提供';
    }
    if (!input.secret) setReplayInputValue(field, input.type, replayInputValue(input));
    label.appendChild(field);
    if (input.description || input.secret) {
      const description = document.createElement('small');
      description.textContent = input.secret
        ? '敏感输入不能作为普通回放变量；请使用命名浏览器配置或受管密钥。'
        : input.description;
      label.appendChild(description);
    }
    grid.appendChild(label);
  });
  container.append(heading, hint, grid);
}

function validateReplayConstraint(
  name: string,
  value: unknown,
  constraints: Record<string, unknown> | undefined,
): void {
  if (!constraints) return;
  if (Array.isArray(constraints.enum) && !constraints.enum.some((item) => Object.is(item, value))) {
    throw new Error(`回放输入 ${name} 不在允许值范围内`);
  }
  if (typeof value === 'string') {
    if (typeof constraints.minLength === 'number' && value.length < constraints.minLength) {
      throw new Error(`回放输入 ${name} 长度不能少于 ${constraints.minLength}`);
    }
    if (typeof constraints.maxLength === 'number' && value.length > constraints.maxLength) {
      throw new Error(`回放输入 ${name} 长度不能超过 ${constraints.maxLength}`);
    }
    if (typeof constraints.pattern === 'string') {
      let matches = false;
      try {
        matches = new RegExp(constraints.pattern).test(value);
      } catch {
        throw new Error(`回放输入 ${name} 的约束表达式无效`);
      }
      if (!matches) throw new Error(`回放输入 ${name} 格式不符合约束`);
    }
  }
  if (typeof value === 'number') {
    if (typeof constraints.minimum === 'number' && value < constraints.minimum) {
      throw new Error(`回放输入 ${name} 不能小于 ${constraints.minimum}`);
    }
    if (typeof constraints.maximum === 'number' && value > constraints.maximum) {
      throw new Error(`回放输入 ${name} 不能大于 ${constraints.maximum}`);
    }
  }
  if (Array.isArray(value)) {
    if (typeof constraints.minItems === 'number' && value.length < constraints.minItems) {
      throw new Error(`回放输入 ${name} 项数不能少于 ${constraints.minItems}`);
    }
    if (typeof constraints.maxItems === 'number' && value.length > constraints.maxItems) {
      throw new Error(`回放输入 ${name} 项数不能超过 ${constraints.maxItems}`);
    }
  }
}

export function readReplayInputs(): Record<string, unknown> {
  const requirement = state.normalizedRequirement;
  if (!requirement) return {};
  const requiredNames = new Set(requirement.requiredInputs.map((input) => input.name));
  const inputs = [...requirement.requiredInputs, ...requirement.optionalInputs];
  const fields = new Map(
    Array.from(document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(
      '[data-replay-input-name]',
    )).map((field) => [field.dataset.replayInputName ?? '', field]),
  );
  const values: Record<string, unknown> = {};
  const seen = new Set<string>();
  for (const input of inputs) {
    if (!input.name || seen.has(input.name)) throw new Error(`回放输入名称无效或重复：${input.name || '空名称'}`);
    seen.add(input.name);
    if (input.secret && requiredNames.has(input.name)) {
      throw new Error(`敏感回放输入 ${input.name} 必须通过命名浏览器配置或受管密钥提供`);
    }
    if (input.secret) continue;
    const field = fields.get(input.name);
    if (!field) throw new Error(`缺少回放输入控件：${input.name}`);
    const raw = field.value;
    if (raw === '') {
      if (requiredNames.has(input.name)) throw new Error(`请填写必填回放输入：${input.name}`);
      continue;
    }
    let value: unknown;
    if (input.type === 'number') {
      value = Number(raw);
      if (!Number.isFinite(value)) throw new Error(`回放输入 ${input.name} 必须是有限数字`);
    } else if (input.type === 'boolean') {
      if (raw !== 'true' && raw !== 'false') throw new Error(`回放输入 ${input.name} 必须是布尔值`);
      value = raw === 'true';
    } else if (input.type === 'object' || input.type === 'array') {
      try {
        value = JSON.parse(raw);
      } catch {
        throw new Error(`回放输入 ${input.name} 必须是有效 JSON`);
      }
      if (input.type === 'array' ? !Array.isArray(value) : value === null || Array.isArray(value) || typeof value !== 'object') {
        throw new Error(`回放输入 ${input.name} 必须是${input.type === 'array' ? '数组' : '对象'}`);
      }
    } else {
      value = raw;
    }
    validateReplayConstraint(input.name, value, input.constraints);
    values[input.name] = value;
  }
  return values;
}

export function handleReplayProgress(progress: ReplayProgress): void {
  state.replayLogs.push(progress);
  // H-1: cap in-memory arrays at push time to prevent OOM and O(n²) DOM
  // rebuild in renderReplayMonitor on long replays. The serialization-time
  // bounds (MAX_REPLAY_DIAGNOSTIC_LOGS=100) still apply for server payloads.
  if (state.replayLogs.length > MAX_REPLAY_LOGS_IN_MEMORY) {
    state.replayLogs.shift();
  }
  if (progress.type === 'result') {
    state.replayResults.push(progress.payload);
    if (state.replayResults.length > MAX_REPLAY_LOGS_IN_MEMORY) {
      state.replayResults.shift();
    }
    if (progress.payload && !Array.isArray(progress.payload) && typeof progress.payload === 'object') {
      Object.assign(state.replayExtracted, progress.payload);
    }
  }
  renderReplayMonitor();
}

export function handleReplayComplete(payload: { status: string; error?: unknown; message?: string }): void {
  if (state.workflowV2 && state.dslWorkflowId && state.replayAttemptId) {
    // M-2: don't overwrite a terminal status with 'running' on duplicate/
    // delayed REPLAY_COMPLETE messages. The subsequent completeWorkflowReplay
    // still runs (the server deduplicates), but the UI retains the correct
    // terminal status instead of flickering to 'running'.
    const isTerminal = state.replayStatus === 'success'
      || state.replayStatus === 'failure'
      || state.replayStatus === 'cancelled';
    if (!isTerminal) {
      state.replayStatus = 'running';
    }
    state.loading = true;
    setStatus('正在验证完整回放结果并决定是否需要修复...', 'info');
    renderReplayMonitor();
    void completeWorkflowReplay(payload);
    return;
  }
  const allowedStatuses: WizardState['replayStatus'][] = ['idle', 'running', 'success', 'failure', 'cancelled'];
  state.replayStatus = allowedStatuses.includes(payload.status as WizardState['replayStatus'])
    ? (payload.status as WizardState['replayStatus'])
    : 'failure';
  const errorString = payload.error ? ((payload.error as { message?: string }).message ?? String(payload.error)) : null;
  state.replayError = errorString ?? payload.message ?? null;
  state.loading = false;
  renderReplayMonitor();
  updateReplayConfirmButton();
}

function replayOutput(): unknown[] {
  // sendResult may submit one row or a batch. Preserve the complete ordered
  // stream exactly as the worker will deliver it: flatten each batch in place
  // and retain every individual payload. replayExtracted remains a UI preview
  // only; merging it here used to discard all but the final object row.
  return state.replayResults.flatMap((payload) => Array.isArray(payload) ? payload : [payload]);
}

function boundReplayOutput(rows: unknown[]): unknown[] {
  if (rows.length <= MAX_REPLAY_OUTPUT_ROWS) {
    const serialized = JSON.stringify(rows);
    if (serialized.length <= MAX_REPLAY_OUTPUT_CLIENT_BYTES) return rows;
  }
  let truncated = rows.slice(0, MAX_REPLAY_OUTPUT_ROWS);
  while (JSON.stringify(truncated).length > MAX_REPLAY_OUTPUT_CLIENT_BYTES && truncated.length > 1) {
    truncated = truncated.slice(0, -1);
  }
  truncated.push({ _truncated: true, original_count: rows.length });
  return truncated;
}

function diagnosticText(value: unknown, maxLength: number): string {
  const text = typeof value === 'string' ? value : value == null ? '' : String(value);
  return text.length > maxLength ? text.slice(0, maxLength) : text;
}

function diagnosticJSONBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    return Number.POSITIVE_INFINITY;
  }
}

const REPLAY_DIAGNOSTIC_EXTRA_KEYS = ['error', 'type', 'errorType', 'stepId', 'attempt', 'delay'];

function redactDiagnosticExtra(extra: Record<string, unknown>): Record<string, unknown> {
  const redacted: Record<string, unknown> = {};
  for (const key of Object.keys(extra)) {
    const value = extra[key];
    if (typeof value === 'string' && REPLAY_DIAGNOSTIC_EXTRA_KEYS.includes(key)) {
      redacted[key] = redactString(value);
    } else {
      redacted[key] = value;
    }
  }
  return redacted;
}

function boundedReplayDiagnosticExtra(extra: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  if (!extra) return undefined;
  if (diagnosticJSONBytes(extra) <= MAX_REPLAY_DIAGNOSTIC_EXTRA_BYTES) return redactDiagnosticExtra(extra);
  const summary: Record<string, unknown> = { truncated: true };
  for (const key of REPLAY_DIAGNOSTIC_EXTRA_KEYS) {
    const value = extra[key];
    if (typeof value === 'string') summary[key] = redactString(diagnosticText(value, 1000));
    else if (typeof value === 'number' || typeof value === 'boolean') summary[key] = value;
  }
  summary.omittedKeyCount = Math.max(0, Object.keys(extra).length - (Object.keys(summary).length - 1));
  return summary;
}

function replayDiagnosticLog(item: ReplayProgress): Record<string, unknown> | null {
  if (item.type === 'log') {
    return {
      type: item.type,
      level: diagnosticText(item.level, 100),
      message: redactString(diagnosticText(item.message, 1000)),
      extra: boundedReplayDiagnosticExtra(item.extra),
    };
  }
  if (item.type === 'status') {
    return {
      type: item.type,
      status: diagnosticText(item.status, 100),
      message: redactString(diagnosticText(item.message, 1000)),
    };
  }
  return null;
}

function buildReplayDiagnostics(payload: { status: string }, errorString: string | undefined): Record<string, unknown> {
  const allLogs = state.replayLogs
    .map(replayDiagnosticLog)
    .filter((item): item is Record<string, unknown> => item !== null);
  const logs = allLogs.slice(-MAX_REPLAY_DIAGNOSTIC_LOGS);
  const diagnostics: Record<string, unknown> = {
    terminalStatus: diagnosticText(payload.status, 100),
    message: diagnosticText(errorString, 2000),
    logs,
  };
  if (allLogs.length > logs.length) {
    diagnostics.truncated = true;
    diagnostics.omittedLogCount = allLogs.length - logs.length;
  }
  if (diagnosticJSONBytes(diagnostics) <= MAX_REPLAY_DIAGNOSTIC_CLIENT_BYTES) return diagnostics;

  const fallbackLogs = logs.slice(-MAX_REPLAY_DIAGNOSTIC_FALLBACK_LOGS).map((log) => ({
    type: log.type,
    level: log.level,
    status: log.status,
    message: diagnosticText(log.message, 1000),
  }));
  const fallback: Record<string, unknown> = {
    terminalStatus: diagnostics.terminalStatus,
    message: diagnosticText(diagnostics.message, 1000),
    logs: fallbackLogs,
    truncated: true,
    omittedLogCount: allLogs.length - fallbackLogs.length,
    truncationReason: 'browser replay diagnostics exceeded the client submission budget',
  };
  if (diagnosticJSONBytes(fallback) <= MAX_REPLAY_DIAGNOSTIC_CLIENT_BYTES) return fallback;

  return {
    terminalStatus: diagnosticText(payload.status, 100),
    message: diagnosticText(errorString, 1000),
    logs: fallbackLogs.slice(-5).map((log) => ({
      type: log.type,
      level: log.level,
      status: log.status,
      message: diagnosticText(log.message, 256),
    })),
    truncated: true,
    omittedLogCount: Math.max(0, allLogs.length - 5),
    truncationReason: 'browser replay diagnostics exceeded the client submission budget',
  };
}

function replayArtifactSummary(
  originalType: string,
  originalBytes: number,
  reason: string,
  omittedArtifactCount = 0,
): string {
  return JSON.stringify({
    format: 'replay-artifact-summary-v1',
    originalType: diagnosticText(originalType, 100),
    originalBytes,
    omitted: true,
    omittedArtifactCount,
    reason,
  });
}

type BoundedReplayArtifact = { name: string; type: 'html' | 'dom' | 'screenshot'; data: string };

function boundedReplayArtifacts(): BoundedReplayArtifact[] {
  const snapshots = state.replayLogs
    .filter((item): item is Extract<ReplayProgress, { type: 'snapshot' }> => item.type === 'snapshot')
    .map((item) => item.snapshot);
  const selectedLimit = snapshots.length > MAX_REPLAY_ARTIFACTS
    ? MAX_REPLAY_ARTIFACTS - 1
    : MAX_REPLAY_ARTIFACTS;
  const omittedArtifactCount = Math.max(0, snapshots.length - selectedLimit);
  const selected = snapshots.slice(-selectedLimit);
  const artifacts: BoundedReplayArtifact[] = [];
  if (omittedArtifactCount > 0) {
    artifacts.push({
      name: 'replay-artifacts-summary',
      type: 'dom',
      data: replayArtifactSummary(
        'multiple',
        0,
        'older replay artifacts omitted to preserve the client artifact count limit',
        omittedArtifactCount,
      ),
    });
  }

  for (const snapshot of selected) {
    const rawData = typeof snapshot.data === 'string' ? snapshot.data : '';
    const originalBytes = new TextEncoder().encode(rawData).byteLength;
    let data = rawData;
    let semantic = false;
    if (snapshot.type === 'dom') {
      try {
        const parsed = JSON.parse(rawData) as { format?: unknown };
        semantic = parsed?.format === 'semantic-dom-v1';
      } catch {
        semantic = false;
      }
    }
    if (!semantic) {
      data = replayArtifactSummary(
        snapshot.type,
        originalBytes,
        'raw replay snapshot data is not submitted',
      );
    } else if (originalBytes > MAX_REPLAY_ARTIFACT_DATA_BYTES) {
      data = replayArtifactSummary(
        snapshot.type,
        originalBytes,
        'sanitized replay snapshot exceeded the client artifact budget',
      );
    }

    let artifact: BoundedReplayArtifact = {
      name: diagnosticText(snapshot.name, 200),
      type: 'dom',
      data,
    };
    if (diagnosticJSONBytes([...artifacts, artifact]) >= MAX_REPLAY_ARTIFACT_CLIENT_BYTES) {
      artifact = {
        ...artifact,
        type: 'dom',
        data: replayArtifactSummary(
          snapshot.type,
          originalBytes,
          'replay artifact omitted to preserve the total client submission budget',
        ),
      };
    }
    if (diagnosticJSONBytes([...artifacts, artifact]) >= MAX_REPLAY_ARTIFACT_CLIENT_BYTES) break;
    artifacts.push(artifact);
  }
  return artifacts;
}

export function completeWorkflowReplay(
  payload: { status: string; error?: unknown; message?: string },
  opts?: { force?: boolean },
): Promise<void> {
  const run = async () => {
    const workflowId = state.dslWorkflowId;
    const replayId = state.replayAttemptId;
    if (!workflowId || !replayId) return;
    // M-4: track whether the repair path was taken so the finally block
    // doesn't reset loading=false while the repair replay is still running.
    let repairStarted = false;
    const errorString = payload.error
      ? ((payload.error as { message?: string }).message ?? String(payload.error))
      : payload.message;
    const artifacts = boundedReplayArtifacts();
    const diagnostics = buildReplayDiagnostics(payload, errorString);
    let completeErrorCode: string | undefined;
    try {
      const response = (await sendAction('COMPLETE_DSL_REPLAY', {
        workflowId,
        replayId,
        succeeded: payload.status === 'success',
        diagnostics,
        output: boundReplayOutput(replayOutput()),
        artifacts,
        errorCode: payload.status === 'cancelled' ? 'REPLAY_CANCELLED' : payload.status === 'success' ? '' : 'REPLAY_FAILED',
        errorMessage: errorString ?? '',
      })) as DSLWorkflowResult;
      if (!response.success || !response.workflow) {
        completeErrorCode = response.code;
        throw new Error(response.error || '服务端未能处理回放结果');
      }
      state.dslJobId = response.repairJob?.id ?? response.workflow.currentJobId ?? null;
      if (response.workflow.status === 'awaiting_replay' && response.workflow.provisionalRule) {
        state.rule = response.workflow.provisionalRule;
        state.yaml = response.workflow.provisionalYaml ?? '';
        state.replayStatus = 'idle';
        state.replayError = null;
        renderPreview();
        setStatus(`修复 ${response.workflow.repairCount}/${response.workflow.maxRepairs} 已完成，正在重新完整回放`, 'info');
        state.loading = false;
        repairStarted = true;
        await startReplay();
        return;
      }
      if (response.workflow.status === 'awaiting_confirmation') {
        state.replayStatus = 'success';
        state.replayError = null;
        setStatus('完整回放和输出结构验证成功，请明确确认规则版本', 'success');
      } else {
        state.replayStatus = 'failure';
        state.replayError = response.workflow.errorMessage || response.replay?.errorMessage || '回放修复次数已用尽';
        setStatus(state.replayError, 'error');
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      // If the server rejected a duplicate completion (e.g. abortReplay raced
      // the background's REPLAY_COMPLETE broadcast) and the wizard already has
      // a terminal status, the current status is authoritative — do not
      // overwrite it with a confusing state-conflict error. Prefer the
      // structured code field surfaced by requirementRequest via the handler
      // response (INVALID_STATE); fall back to the stable server message
      // substring when code is absent (older code paths, network errors).
      const isStateConflict = completeErrorCode === 'INVALID_STATE'
        || (!completeErrorCode && message.includes('cannot transition from its current state'));
      const isAlreadyTerminal =
        state.replayStatus === 'cancelled'
        || state.replayStatus === 'success'
        || state.replayStatus === 'failure';
      if (isStateConflict && isAlreadyTerminal) {
        return;
      }
      state.replayStatus = 'failure';
      state.replayError = message;
      setStatus(state.replayError, 'error');
    } finally {
      // M-4: don't reset loading if the repair path started a new replay —
      // startReplay manages loading independently.
      if (!repairStarted) {
        state.loading = false;
      }
      renderReplayMonitor();
      updateReplayConfirmButton();
    }
  };

  // H-2: allow callers to bypass the in-flight dedup. When startReplay fails
  // inside the repair path, its catch block calls completeWorkflowReplay with
  // force=true to avoid being silently dropped by the still-held in-flight op.
  if (opts?.force) return run();
  return withInFlight('complete-replay', run) as Promise<void>;
}

export function startReplay(): Promise<void> {
  return withInFlight('start-replay', async () => {
    if (state.replayStatus === 'running') return;
    if (!state.rule) {
      setStatus('没有可用规则', 'error');
      return;
    }
    if (state.workflowV2 && !state.normalizedRequirement) {
      setStatus('缺少已确认采集需求，无法启动完整回放', 'error');
      showStep('preview');
      return;
    }
    let variables: Record<string, unknown>;
    try {
      variables = readReplayInputs();
    } catch (error) {
      setStatus(error instanceof Error ? error.message : String(error), 'error');
      showStep('preview');
      return;
    }
    state.replayVariables = variables;
    state.loading = true;
    state.replayStatus = 'running';
    state.replayLogs = [];
    state.replayError = null;
    state.replayExtracted = {};
    state.replayResults = [];
    renderReplayMonitor();
    setStatus('正在真实浏览器中回放 DSL...', 'info');
    showStep('replay');
    try {
      if (state.workflowV2 && state.dslWorkflowId) {
        const replay = (await sendAction('START_DSL_REPLAY', { workflowId: state.dslWorkflowId })) as DSLWorkflowResult;
        if (!replay.success || !replay.replay?.id) {
          throw new Error(replay.error || '无法创建服务端回放尝试');
        }
        state.replayAttemptId = replay.replay.id;
      }
      const browserReplayPayload = Object.keys(variables).length > 0
        ? { rule: state.rule, variables }
        : { rule: state.rule };
      const response = (await sendAction('START_REPLAY', browserReplayPayload)) as { success: boolean; error?: string; taskId?: string };
      if (response.success === false) {
        state.replayStatus = 'failure';
        state.replayError = response.error || '启动回放失败';
        state.loading = false;
        renderReplayMonitor();
        if (state.workflowV2 && state.replayAttemptId) {
          void completeWorkflowReplay({ status: 'failure', message: state.replayError }, { force: true });
        }
      } else {
        // M-3: the replay started successfully; it's now monitored via
        // REPLAY_PROGRESS/REPLAY_COMPLETE messages, not via state.loading.
        state.loading = false;
      }
    } catch (err) {
      state.replayStatus = 'failure';
      state.replayError = err instanceof Error ? err.message : String(err);
      state.loading = false;
      renderReplayMonitor();
      if (state.workflowV2 && state.replayAttemptId) {
        void completeWorkflowReplay({ status: 'failure', message: state.replayError }, { force: true });
      }
    }
  }) as Promise<void>;
}

export function abortReplay(): Promise<void> {
  return withInFlight('abort-replay', async () => {
    await sendAction('ABORT_REPLAY').catch((err) => {
      setStatus(`取消回放失败：${err instanceof Error ? err.message : String(err)}`, 'error');
    });
    state.replayStatus = 'cancelled';
    state.loading = false;
    renderReplayMonitor();
    // WI-6: when a workflow V2 replay is in flight, directly complete the
    // DSL attempt so the server does not have to wait for the reaper (35m
    // default). The background's REPLAY_COMPLETE(failure) broadcast, if it
    // arrives, is deduped server-side via CompleteDSLReplay's status guard.
    if (state.workflowV2 && state.dslWorkflowId && state.replayAttemptId) {
      await completeWorkflowReplay({ status: 'cancelled' });
    }
  }) as Promise<void>;
}

export function renderReplayMonitor(): void {
  const stage = getEl<HTMLDivElement>('replay-stage');
  if (!stage) return;
  const statusText = {
    idle: '等待回放',
    running: '正在回放...',
    success: '回放成功',
    failure: '回放失败',
    cancelled: '已取消',
  }[state.replayStatus];

  let logsHtml = state.replayLogs
    .map((log) => {
      if (log.type === 'log') {
        const safeLevel = String(log.level).replace(/[^a-zA-Z0-9_-]/g, '-');
        return `<div class="log log-${safeLevel}">[${escapeHtml(log.level)}] ${escapeHtml(log.message)}</div>`;
      }
      if (log.type === 'status')
        return `<div class="log log-status">[status] ${escapeHtml(log.status)}: ${escapeHtml(log.message ?? '')}</div>`;
      if (log.type === 'result')
        return `<div class="log log-result">[result] ${escapeHtml(JSON.stringify(log.payload) ?? '')}</div>`;
      if (log.type === 'snapshot') return `<div class="log log-snapshot">[snapshot] ${escapeHtml(log.snapshot.name)}</div>`;
      return '';
    })
    .join('');

  if (Object.keys(state.replayExtracted).length > 0) {
    logsHtml += `<pre class="extracted-preview">${escapeHtml(`提取结果：${JSON.stringify(state.replayExtracted, null, 2)}`)}</pre>`;
  }

  if (state.replayError) {
    logsHtml += `<div class="replay-error">错误：${escapeHtml(state.replayError)}</div>`;
  }

  stage.innerHTML = `<div class="replay-status">${statusText}</div><div class="replay-logs">${logsHtml}</div>`;
}

export function updateReplayConfirmButton(): void {
  if (state.step !== 'replay') return;
  const primary = getEl<HTMLButtonElement>('action-primary');
  if (primary) primary.disabled = state.replayStatus !== 'success';
}

export function renderConfirmList(): void {
  const container = getEl<HTMLDivElement>('confirm-list');
  if (!container) return;
  const steps = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
    ? ((state.rule as Record<string, unknown>).steps as unknown[])
    : [];
  container.innerHTML = '';
  steps.forEach((step: unknown, i: number) => {
    const div = document.createElement('div');
    div.className = 'confirm-item';
    const s = step as Record<string, unknown>;
    div.innerHTML = `
      <label>
        <input type="checkbox" data-index="${i}" />
        <strong>Step ${i + 1}: ${escapeHtml(String(s.action || ''))}</strong>
        <pre>${escapeHtml(JSON.stringify(step, null, 2))}</pre>
      </label>
    `;
    div.querySelector('input')?.addEventListener('change', (e) => {
      const checked = (e.target as HTMLInputElement).checked;
      toggleStepConfirmation(i, checked);
    });
    container.appendChild(div);
  });
  updateConfirmAllCheckbox();
  updateSaveButton();
}

export function toggleStepConfirmation(index: number, confirmed: boolean): void {
  if (confirmed) {
    state.confirmedSteps.add(index);
  } else {
    state.confirmedSteps.delete(index);
  }
  updateConfirmAllCheckbox();
  updateSaveButton();
}

export function confirmAll(confirmed: boolean): void {
  const steps = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
    ? ((state.rule as Record<string, unknown>).steps as unknown[])
    : [];
  document.querySelectorAll<HTMLInputElement>('#confirm-list input[type="checkbox"]').forEach((el) => {
    el.checked = confirmed;
    const idx = Number(el.dataset.index);
    if (!Number.isNaN(idx)) {
      if (confirmed) state.confirmedSteps.add(idx);
      else state.confirmedSteps.delete(idx);
    }
  });
  if (confirmed) {
    steps.forEach((_, i) => state.confirmedSteps.add(i));
  } else {
    state.confirmedSteps.clear();
  }
  updateConfirmAllCheckbox();
  updateSaveButton();
}

export function updateConfirmAllCheckbox(): void {
  const total = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
    ? ((state.rule as Record<string, unknown>).steps as unknown[]).length
    : 0;
  const confirmAllInput = getEl<HTMLInputElement>('confirm-all');
  if (confirmAllInput) {
    confirmAllInput.checked = total > 0 && state.confirmedSteps.size === total;
    confirmAllInput.indeterminate =
      state.confirmedSteps.size > 0 && state.confirmedSteps.size < total;
  }
}

export function updateSaveButton(): void {
  if (state.step !== 'confirm') return;
  const total = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
    ? ((state.rule as Record<string, unknown>).steps as unknown[]).length
    : 0;
  const primary = getEl<HTMLButtonElement>('action-primary');
  if (primary) primary.disabled = state.confirmedSteps.size < total || total === 0;
}

export function confirmWorkflowRule(): Promise<void> {
  return withInFlight('confirm-workflow', async () => {
    if (!state.dslWorkflowId || state.replayStatus !== 'success') {
      setStatus('只有完整回放和输出验证成功后才能确认规则', 'error');
      return;
    }
    state.loading = true;
    setStatus('正在创建不可变规则版本...', 'info');
    try {
      const response = (await sendAction('CONFIRM_DSL_WORKFLOW', {
        workflowId: state.dslWorkflowId,
      })) as DSLWorkflowResult;
      if (!response.success || !response.ruleVersion?.ruleId) {
        if (response.code === 'SAFETY_BLOCKING_FLAG') {
          const flags = response.blocking_flags ?? [];
          state.error = `检测到阻断型安全旗标：${flags.join(', ')}。请联系运维通过 admin 接口审批（POST /api/v1/dsl-workflows/${state.dslWorkflowId}/confirm?override_safety=true）。`;
          setStatus(state.error, 'error');
          state.loading = false;
          renderActions();
          return;
        }
        throw new Error(response.error || '规则版本确认失败');
      }
      state.ruleId = response.ruleVersion.ruleId;
      const resultEl = getEl<HTMLParagraphElement>('save-result');
      if (resultEl) {
        resultEl.textContent = `不可变规则版本已批准：${response.ruleVersion.ruleId} v${response.ruleVersion.version}`;
      }
      const stored = await chrome.storage.local.get('serverConfig');
      const baseUrl = (stored.serverConfig as { baseUrl?: string } | undefined)?.baseUrl ?? '';
      const link = getEl<HTMLAnchorElement>('admin-link');
      if (link && baseUrl) {
        link.href = `${baseUrl.replace(/\/+$/, '')}/admin/rules/${encodeURIComponent(response.ruleVersion.ruleId)}`;
        link.classList.remove('hidden');
      }
      setStatus('规则版本已批准并固化', 'success');
      showStep('save');
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      state.error = message;
      setStatus(message, 'error');
    } finally {
      state.loading = false;
      // M-7: clear persisted custom intent so a new workflow doesn't inherit
      // the previous workflow's text.
      persistCustomIntent('');
    }
  }) as Promise<void>;
}

export function saveRule(): Promise<void> {
  return withInFlight('save-rule', async () => {
    const total = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
      ? ((state.rule as Record<string, unknown>).steps as unknown[]).length
      : 0;
    if (state.confirmedSteps.size < total) {
      setStatus('请确认所有步骤后再保存', 'error');
      return;
    }
    state.loading = true;
    setStatus('正在保存规则...');
    try {
      const response = (await sendAction('UPLOAD_CONFIRMED_RULE')) as UploadConfirmedRuleResult;
      if (response.success === false) {
        setStatus(response.error || '保存失败', 'error');
        state.error = response.error || '保存失败';
        return;
      }
      state.ruleId = response.ruleId ?? null;
      const resultEl = getEl<HTMLParagraphElement>('save-result');
      if (resultEl) resultEl.textContent = `规则已保存，ID：${state.ruleId ?? ''}`;
      const stored = await chrome.storage.local.get('serverConfig');
      const baseUrl = (stored.serverConfig as { baseUrl?: string } | undefined)?.baseUrl ?? '';
      const link = getEl<HTMLAnchorElement>('admin-link');
      if (link && baseUrl && state.ruleId) {
        link.href = `${baseUrl.replace(/\/+$/, '')}/admin/rules/${encodeURIComponent(state.ruleId)}`;
        link.classList.remove('hidden');
      }
      setStatus('保存成功', 'success');
      showStep('save');
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setStatus(`保存规则失败：${message}`, 'error');
      state.error = message;
    } finally {
      state.loading = false;
    }
  }) as Promise<void>;
}

type ActionButtonConfig = {
  visible: boolean;
  label: string;
  primary?: boolean;
  disabled?: boolean;
};

function configureButton(
  btn: HTMLButtonElement | null,
  config: ActionButtonConfig,
): void {
  if (!btn) return;
  btn.classList.toggle('hidden', !config.visible);
  if (config.visible) {
    btn.textContent = config.label;
    btn.classList.toggle('primary', config.primary ?? false);
    btn.disabled = (config.disabled ?? false) || state.inFlight.size > 0;
  }
}

export function renderActions(): void {
  const tertiary = getEl<HTMLButtonElement>('action-tertiary');
  const secondary = getEl<HTMLButtonElement>('action-secondary');
  const primary = getEl<HTMLButtonElement>('action-primary');

  switch (state.step) {
    case 'intent': {
      configureButton(tertiary, { visible: false, label: '' });
      configureButton(secondary, { visible: false, label: '' });
      const custom = getEl<HTMLTextAreaElement>('custom-description')?.value.trim() ?? '';
      const hasCustom = custom.length > 0;
      if (state.workflowV2) {
        // Candidates are generated on demand: until the user asks for them (or
        // types an intent), the primary action starts generation instead of
        // continuing to the requirement step.
        const awaitingCandidates = !state.candidatesRequested && !hasCustom;
        configureButton(primary, {
          visible: true,
          label: awaitingCandidates ? '生成 3 个候选需求' : '下一步：查看结构化需求',
          primary: true,
          disabled: awaitingCandidates
            ? state.loading
            : state.selectedRequirementCandidate === null && !hasCustom,
        });
      } else {
        configureButton(primary, {
          visible: true,
          label: '下一步',
          primary: true,
          disabled: state.selectedIntent === null && !hasCustom,
        });
      }
      break;
    }
    case 'requirement': {
      configureButton(tertiary, { visible: false, label: '' });
      configureButton(secondary, { visible: true, label: '返回候选需求' });
      configureButton(primary, {
        visible: true,
        label: state.requirementReviewReady ? '确认此采集需求' : '验证并规范化',
        primary: true,
      });
      break;
    }
    case 'requirement-confirmed': {
      configureButton(tertiary, { visible: false, label: '' });
      configureButton(secondary, { visible: true, label: '返回修改需求' });
      configureButton(primary, { visible: true, label: '生成临时 DSL', primary: true });
      break;
    }
    case 'preview': {
      configureButton(tertiary, { visible: false, label: '' });
      configureButton(secondary, { visible: true, label: '返回' });
      configureButton(primary, { visible: true, label: '下一步：回放确认', primary: true });
      break;
    }
    case 'replay': {
      configureButton(tertiary, { visible: true, label: '取消回放' });
      // M-1: disable back-to-preview while replay is running to prevent the
      // user from getting stuck on the preview step (startReplay early-returns
      // when replayStatus === 'running').
      configureButton(secondary, {
        visible: true,
        label: '返回预览',
        disabled: state.replayStatus === 'running',
      });
      configureButton(primary, {
        visible: true,
        label: state.workflowV2 ? '确认回放并批准规则版本' : '确认无误，继续',
        primary: true,
        disabled: state.replayStatus !== 'success',
      });
      break;
    }
    case 'confirm': {
      configureButton(tertiary, { visible: false, label: '' });
      configureButton(secondary, { visible: true, label: '返回' });
      const total = Array.isArray((state.rule as Record<string, unknown> | null)?.steps)
        ? ((state.rule as Record<string, unknown>).steps as unknown[]).length
        : 0;
      configureButton(primary, {
        visible: true,
        label: '确认无误并保存',
        primary: true,
        disabled: state.confirmedSteps.size < total || total === 0,
      });
      break;
    }
    case 'save': {
      configureButton(tertiary, { visible: false, label: '' });
      configureButton(secondary, { visible: false, label: '' });
      configureButton(primary, { visible: true, label: '关闭', primary: true });
      break;
    }
    default:
      break;
  }
}

function handlePrimaryAction(): void {
  switch (state.step) {
    case 'intent':
      if (state.workflowV2) {
        const custom = getEl<HTMLTextAreaElement>('custom-description')?.value.trim() ?? '';
        if (!state.candidatesRequested && custom.length === 0) {
          requestRequirementCandidates().catch((err) => setStatus(String(err), 'error'));
        } else {
          prepareRequirement().catch((err) => setStatus(String(err), 'error'));
        }
      } else {
        generateDSL().catch((err) => setStatus(String(err), 'error'));
      }
      break;
    case 'requirement':
      confirmRequirement().catch((err) => setStatus(String(err), 'error'));
      break;
    case 'requirement-confirmed':
      generateWorkflowDSL().catch((err) => setStatus(String(err), 'error'));
      break;
    case 'preview':
      startReplay().catch((err) => setStatus(String(err), 'error'));
      break;
    case 'replay':
      if (state.workflowV2) {
        confirmWorkflowRule().catch((err) => setStatus(String(err), 'error'));
      } else {
        renderConfirmList();
        showStep('confirm');
      }
      break;
    case 'confirm':
      saveRule().catch((err) => setStatus(String(err), 'error'));
      break;
    case 'save':
      window.close();
      break;
    default:
      break;
  }
}

function handleSecondaryAction(): void {
  switch (state.step) {
    case 'requirement':
      showStep('intent');
      break;
    case 'requirement-confirmed':
      showStep('requirement');
      break;
    case 'preview':
      showStep('intent');
      break;
    case 'replay':
      showStep('preview');
      break;
    case 'confirm':
      showStep('replay');
      break;
    default:
      break;
  }
}

function handleTertiaryAction(): void {
  if (state.step === 'replay') {
    abortReplay().catch((err) => setStatus(String(err), 'error'));
  }
}

export function initWizard(): void {
  // Keep the MV3 service worker alive while long-running LLM requests are in
  // flight from this wizard page. A connected port alone is not always enough
  // to prevent Chrome from terminating an idle worker, so we also send a
  // periodic heartbeat through the port. If the port drops, we reconnect with
  // exponential backoff (3 attempts in a 10s window) before surfacing a
  // non-blocking warning and gracefully degrading.
  connectKeepAlive();
  const heartbeatInterval = window.setInterval(() => {
    if (Date.now() - lastPongAt > HEARTBEAT_TIMEOUT_MS) {
      if (keepAlivePort) {
        try {
          keepAlivePort.disconnect();
        } catch {
          // Already dead; nothing to clean up.
        }
        keepAlivePort = null;
        attemptReconnect();
      }
    } else {
      try {
        keepAlivePort?.postMessage({ type: 'ping' });
      } catch {
        // Port may have disconnected; reconnect path is handled by
        // onDisconnect or the next heartbeat-timeout tick.
      }
    }
  }, 1000);
  window.addEventListener('beforeunload', () => {
    window.clearInterval(heartbeatInterval);
    keepAlivePort?.disconnect();
    keepAlivePort = null;
  });

  resumeDSLWorkflow()
    .then((resumed) => {
      if (!resumed) return loadPredictions();
      return undefined;
    })
    .catch((error) => setStatus(String(error), 'error'));

  chrome.runtime.onMessage.addListener((message: unknown, sender?: { tab?: unknown }) => {
    const msg = message as { action?: string; payload?: unknown };
    // Replay tabs send messages to the service worker, but runtime messages are
    // also delivered directly to extension pages. Only consume the worker's
    // authoritative relay (which has no tab sender) to avoid duplicate logs,
    // results, and terminal workflow submissions.
    if (sender?.tab && (msg.action === 'REPLAY_PROGRESS' || msg.action === 'REPLAY_COMPLETE' || msg.action === 'LLM_JOB_PROGRESS')) {
      return false;
    }
    if (msg.action === 'LLM_JOB_PROGRESS') {
      renderJobProgress(msg.payload as LLMJobProgress);
      return false;
    }
    if (msg.action === 'REPLAY_PROGRESS') {
      handleReplayProgress(msg.payload as ReplayProgress);
      return false;
    }
    if (msg.action === 'REPLAY_COMPLETE') {
      handleReplayComplete(msg.payload as { status: string; error?: unknown });
      return false;
    }
    return false;
  });

  getEl<HTMLTextAreaElement>('custom-description')?.addEventListener('input', () => {
    state.selectedIntent = null;
    state.selectedRequirementCandidate = null;
    document.querySelectorAll<HTMLInputElement>('input[name="intent"], input[name="requirement-candidate"]').forEach((el) => {
      el.checked = false;
    });
    persistCustomIntent(getEl<HTMLTextAreaElement>('custom-description')?.value ?? '');
    updateNextButton();
  });

  [
    'requirement-title',
    'requirement-description',
    'requirement-required-inputs',
    'requirement-optional-inputs',
    'requirement-output-fields',
    'requirement-sample-output',
  ].forEach((id) => getEl<HTMLInputElement | HTMLTextAreaElement>(id)?.addEventListener('input', markRequirementDirty));

  getEl<HTMLButtonElement>('manual-structured')?.addEventListener('click', openManualRequirementEditor);
  for (const scope of ['gen', 'replay'] as const) {
    getEl<HTMLButtonElement>(`correct-rule-submit-${scope}`)?.addEventListener('click', () => {
      submitRuleCorrection(scope).catch((err) => setStatus(String(err), 'error'));
    });
  }
  getEl<HTMLButtonElement>('retry-requirement-job')?.addEventListener('click', () => {
    retryRequirementJob().catch((error) => setStatus(String(error), 'error'));
  });

  getEl<HTMLButtonElement>('action-primary')?.addEventListener('click', () => {
    handlePrimaryAction();
  });
  getEl<HTMLButtonElement>('action-secondary')?.addEventListener('click', () => {
    handleSecondaryAction();
  });
  getEl<HTMLButtonElement>('action-tertiary')?.addEventListener('click', () => {
    handleTertiaryAction();
  });

  getEl<HTMLInputElement>('confirm-all')?.addEventListener('change', (e) => {
    confirmAll((e.target as HTMLInputElement).checked);
  });
}

function escapeHtml(input: string): string {
  return input
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

const RUNTIME_RESTORED_KEY = 'oc-intent-runtime-restored';

/**
 * Playwright-driven Chromium sometimes opens extension pages via
 * chrome.tabs.create without a fully initialized chrome.runtime API.
 * A direct reload of the same chrome-extension:// URL restores it.
 * We use sessionStorage to avoid an infinite reload loop if the API is
 * genuinely unavailable.
 */
function ensureExtensionRuntime(): boolean {
  if (typeof chrome !== 'undefined' && typeof chrome.runtime !== 'undefined') {
    return true;
  }

  if (typeof sessionStorage !== 'undefined' && sessionStorage.getItem(RUNTIME_RESTORED_KEY)) {
    setStatus('扩展运行时尚未初始化，无法加载向导。请刷新本页面或重新打开扩展。', 'error');
    return false;
  }

  if (typeof sessionStorage !== 'undefined') {
    sessionStorage.setItem(RUNTIME_RESTORED_KEY, '1');
  }
  if (typeof window !== 'undefined') {
    window.location.replace(window.location.href);
  }
  return false;
}

if (typeof document !== 'undefined' && ensureExtensionRuntime()) {
  initWizard();
}
