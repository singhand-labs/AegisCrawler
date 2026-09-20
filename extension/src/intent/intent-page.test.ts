import { describe, it, expect, vi, beforeEach, afterEach, test } from 'vitest';
import type {
  IntentCandidate,
  PredictIntentResult,
  GenerateDslResult,
  UploadConfirmedRuleResult,
  CollectionRequirementSpec,
  RequirementWorkflowResult,
} from './intent-types';
import type { Mock } from 'vitest';


const WIZARD_HTML = `
  <div id="status"></div>
  <div id="job-progress" class="hidden">
    <span id="job-progress-text"></span>
    <progress id="job-progress-bar" value="0" max="1"></progress>
  </div>
  <main id="wizard">
    <section id="step-intent" class="step hidden">
      <p class="hint" id="intent-hint"></p>
      <div id="candidates-list"></div>
      <textarea id="custom-description"></textarea>
      <button id="manual-structured"></button>
      <button id="retry-requirement-job"></button>
    </section>
    <section id="step-requirement" class="step hidden">
      <input id="requirement-title" />
      <textarea id="requirement-description"></textarea>
      <textarea id="requirement-required-inputs"></textarea>
      <textarea id="requirement-optional-inputs"></textarea>
      <textarea id="requirement-output-fields"></textarea>
      <textarea id="requirement-sample-output"></textarea>
    </section>
    <section id="step-requirement-confirmed" class="step hidden">
      <p id="requirement-confirmed-result"></p>
      <input id="browser-profile-id" value="current-chrome-profile" />
      <div id="correct-rule-section-gen" class="correct-rule-section hidden">
        <textarea id="correct-rule-editor-gen"></textarea>
        <button id="correct-rule-submit-gen"></button>
      </div>
    </section>
    <section id="step-preview" class="step hidden">
      <div id="replay-inputs"></div>
      <div id="steps-list"></div>
      <pre id="yaml-preview"><code></code></pre>
    </section>
    <section id="step-replay" class="step hidden">
      <div id="replay-stage"></div>
      <div id="correct-rule-section-replay" class="correct-rule-section hidden">
        <textarea id="correct-rule-editor-replay"></textarea>
        <button id="correct-rule-submit-replay"></button>
      </div>
    </section>
    <section id="step-confirm" class="step hidden">
      <div id="confirm-list"></div>
      <input id="confirm-all" type="checkbox" />
    </section>
    <section id="step-save" class="step hidden">
      <p id="save-result"></p>
      <a id="admin-link" href="#">查看</a>
    </section>
  </main>
  <footer id="wizard-actions" class="actions-bar">
    <button id="action-tertiary" class="tertiary" type="button"></button>
    <button id="action-secondary" class="secondary" type="button"></button>
    <button id="action-primary" class="primary" type="button"></button>
  </footer>
`;

const sampleCandidate: IntentCandidate = {
  id: 'c1',
  label: '采集商品标题和价格',
  description: '在商品列表页抓取标题和价格',
  confidence: 0.88,
  suggestedVariables: ['maxItems'],
};

const sampleRequirement: CollectionRequirementSpec = {
  title: 'Collect product prices',
  description: 'Collect visible product names and prices.',
  requiredInputs: [{ name: 'keyword', type: 'string', description: 'Search keyword' }],
  optionalInputs: [],
  outputFields: [
    { name: 'name', type: 'string', description: 'Product name' },
    { name: 'price', type: 'number', description: 'Product price' },
  ],
  sampleOutput: { name: 'Example', price: 10 },
};

const noInputRequirement: CollectionRequirementSpec = {
  ...sampleRequirement,
  requiredInputs: [],
  optionalInputs: [],
};

const sampleRule = {
  id: 'ext-test-1',
  version: '1.0.0',
  name: 'Test rule',
  domain: 'example.com',
  steps: [
    { action: 'navigate', url: 'https://example.com' },
    { action: 'click', target: { selector: '.item' } },
  ],
};

function createSendMessageMock(
  overrides: Record<string, unknown> = {},
) {
  return vi.fn(async (message: { action: string; payload?: unknown }) => {
    const action = message.action;
    if (overrides[action] !== undefined) {
      const override = overrides[action];
      if (typeof override === 'function') {
        return override(message);
      }
      return override;
    }
    switch (action) {
      case 'GET_REQUIREMENT_WORKFLOW':
        return { success: true, workflowV2: false } as RequirementWorkflowResult;
      case 'PREDICT_INTENT':
        return {
          success: true,
          candidates: [sampleCandidate],
          fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
          model: 'fake',
          cacheHit: false,
        } as PredictIntentResult;
      case 'GENERATE_DSL_FROM_INTENT':
        return {
          success: true,
          rule: sampleRule,
          yaml: 'id: ext-test-1\nname: Test rule',
        } as GenerateDslResult;
      case 'GENERATE_DSL_FROM_INTENT_SERVER':
        return {
          success: true,
          rule: { ...sampleRule, id: 'ext-server-1' },
          yaml: 'id: ext-server-1\nname: Server rule',
        } as GenerateDslResult;
      case 'UPLOAD_CONFIRMED_RULE':
        return { success: true, ruleId: 'ext-test-1' } as UploadConfirmedRuleResult;
      default:
        return { success: false, error: `unknown action: ${action}` };
    }
  });
}

function createStorageMock(
  serverConfig: Record<string, unknown> = {},
  sessionStorage: Record<string, unknown> = {},
) {
  const localStorage: Record<string, unknown> = { serverConfig };
  const makeGet = (store: Record<string, unknown>) =>
    vi.fn(async (keys?: string | string[] | Record<string, unknown> | null) => {
      if (keys === undefined || keys === null) return { ...store };
      if (typeof keys === 'string') return keys in store ? { [keys]: store[keys] } : {};
      if (Array.isArray(keys)) {
        return keys.reduce((acc, key) => {
          if (key in store) acc[key] = store[key];
          return acc;
        }, {} as Record<string, unknown>);
      }
      return Object.entries(keys).reduce((acc, [key, defaultValue]) => {
        acc[key] = key in store ? store[key] : defaultValue;
        return acc;
      }, {} as Record<string, unknown>);
    });
  return {
    local: {
      get: makeGet(localStorage),
      set: vi.fn(async () => undefined),
    },
    session: {
      get: makeGet(sessionStorage),
      set: vi.fn(async () => undefined),
      remove: vi.fn(async () => undefined),
    },
  };
}

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

function clickElement(id: string): void {
  const el = document.getElementById(id);
  if (el) el.click();
}

async function importModule(
  overrides: Record<string, unknown> = {},
  serverConfig?: Record<string, unknown>,
  sessionStorage?: Record<string, unknown>,
) {
  vi.resetModules();
  const sendMessage = createSendMessageMock(overrides);
  const storageMock = createStorageMock(serverConfig, sessionStorage);
  const onMessageListeners: Array<(message: unknown, sender?: { tab?: unknown }) => unknown> = [];
  const onMessageAddListener = vi.fn((listener: (message: unknown, sender?: { tab?: unknown }) => unknown) => {
    onMessageListeners.push(listener);
  });
  (globalThis as Record<string, unknown>).chrome = {
    runtime: {
      sendMessage,
      onMessage: { addListener: onMessageAddListener },
      connect: vi.fn(() => ({
        name: 'intent-wizard',
        postMessage: vi.fn(),
        disconnect: vi.fn(),
        onMessage: { addListener: vi.fn(), removeListener: vi.fn() },
        onDisconnect: { addListener: vi.fn(), removeListener: vi.fn() },
      })),
    },
    storage: storageMock,
  };
  const mod = await import('./intent-page');
  await flushPromises();
  return { mod, sendMessage, storageMock, onMessageListeners };
}

async function importModuleWithConnect(
  connectMock: ReturnType<typeof vi.fn>,
  overrides: Record<string, unknown> = {},
) {
  vi.resetModules();
  const sendMessage = createSendMessageMock(overrides);
  const storageMock = createStorageMock({}, {});
  (globalThis as Record<string, unknown>).chrome = {
    runtime: {
      sendMessage,
      onMessage: { addListener: vi.fn() },
      connect: connectMock,
    },
    storage: storageMock,
  };
  const mod = await import('./intent-page');
  return { mod, sendMessage };
}

describe('intent-page wizard', () => {
  beforeEach(() => {
    document.body.innerHTML = WIZARD_HTML;
    sessionStorage.clear();
  });

  afterEach(() => {
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
  });

  it('loads predictions on init', async () => {
    const { sendMessage } = await importModule();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'PREDICT_INTENT' });
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('请选择最符合的目的');
    expect(status.className).toBe('info');
  });

  it('renders candidate cards', async () => {
    const { mod } = await importModule();
    const container = document.getElementById('candidates-list') as HTMLDivElement;
    expect(container.querySelectorAll('.candidate-card')).toHaveLength(1);
    expect(container.textContent).toContain('采集商品标题和价格');
    expect(mod.state.candidates).toHaveLength(1);
  });

  it('uses workflow v2 to render exactly three structured requirement candidates', async () => {
    const candidates = [
      { id: 'c1', confidence: 0.9, requirement: sampleRequirement },
      { id: 'c2', confidence: 0.8, requirement: { ...sampleRequirement, title: 'Compare product prices' } },
      { id: 'c3', confidence: 0.7, requirement: { ...sampleRequirement, title: 'Monitor product prices' } },
    ];
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? { success: true, workflowV2: true, candidates, job: { id: 'candidate-job', status: 'completed' } }
          : { success: true, workflowV2: true },
    });

    expect(mod.state.requirementCandidates).toHaveLength(0);
    clickElement('action-primary');
    await flushPromises();

    expect(mod.state.workflowV2).toBe(true);
    expect(mod.state.requirementCandidates).toHaveLength(3);
    expect(document.querySelectorAll('input[name="requirement-candidate"]')).toHaveLength(3);
    expect(document.getElementById('candidates-list')?.textContent).toContain('keyword: string');
    expect(document.getElementById('candidates-list')?.textContent).toContain('price: number');
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'PREDICT_INTENT' });
  });

  it('renderCandidates shows badge for synthetic requirement candidates', async () => {
    const candidates = [
      { id: 'c1', confidence: 0.9, requirement: sampleRequirement, source: 'llm' as const },
      {
        id: 'c2',
        confidence: 0.3,
        requirement: { ...sampleRequirement, title: 'Compare product prices' },
        source: 'synthetic' as const,
      },
      {
        id: 'c3',
        confidence: 0.2,
        requirement: { ...sampleRequirement, title: 'Monitor product prices' },
        source: 'synthetic' as const,
      },
    ];
    await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? { success: true, workflowV2: true, candidates, job: { id: 'candidate-job', status: 'completed' } }
          : { success: true, workflowV2: true },
    });

    clickElement('action-primary');
    await flushPromises();

    const badges = document.querySelectorAll('.synthetic-badge');
    expect(badges.length).toBe(2);
  });

  it('does not start candidate generation on boot in v2 mode', async () => {
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true },
    });

    expect(sendMessage).toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: false } });
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
    expect(mod.state.workflowV2).toBe(true);
    expect(mod.state.candidatesRequested).toBe(false);
    expect(mod.state.requirementCandidates).toHaveLength(0);
    expect(mod.state.step).toBe('intent');
    expect(document.getElementById('candidates-list')?.children).toHaveLength(0);
    expect(document.getElementById('status')?.textContent).toContain('生成 3 个候选需求');
    expect(document.getElementById('intent-hint')?.textContent).toContain('生成 3 个候选需求');
    const primary = document.getElementById('action-primary') as HTMLButtonElement;
    expect(primary.textContent).toBe('生成 3 个候选需求');
    expect(primary.disabled).toBe(false);
  });

  it('requests candidates on demand from the intent primary action', async () => {
    const candidates = [
      { id: 'c1', confidence: 0.9, requirement: sampleRequirement },
      { id: 'c2', confidence: 0.8, requirement: sampleRequirement },
      { id: 'c3', confidence: 0.7, requirement: sampleRequirement },
    ];
    const { mod, sendMessage, onMessageListeners } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? { success: true, workflowV2: true, candidates, job: { id: 'candidate-job', status: 'completed' } }
          : { success: true, workflowV2: true },
    });

    clickElement('action-primary');
    onMessageListeners[0]({ action: 'LLM_JOB_PROGRESS', payload: { chunkCount: 10, completedChunks: 3 } });
    expect(document.getElementById('job-progress')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('job-progress-text')?.textContent).toBe('正在分析… (3/10)');
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
    expect(mod.state.candidatesRequested).toBe(true);
    expect(mod.state.requirementJobId).toBe('candidate-job');
    expect(document.querySelectorAll('input[name="requirement-candidate"]')).toHaveLength(3);
    const primary = document.getElementById('action-primary') as HTMLButtonElement;
    expect(primary.textContent).toBe('下一步：查看结构化需求');
    expect(primary.disabled).toBe(true);
  });

  it('continues with typed custom intent without ever starting candidate generation', async () => {
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true },
      NORMALIZE_REQUIREMENT: {
        success: true,
        workflowV2: true,
        requirement: sampleRequirement,
        requirementId: 'requirement-custom',
      },
    });
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '采集商品价格和库存';
    textarea.dispatchEvent(new Event('input'));
    const primary = document.getElementById('action-primary') as HTMLButtonElement;
    expect(primary.textContent).toBe('下一步：查看结构化需求');
    expect(primary.disabled).toBe(false);

    clickElement('action-primary');
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith({ action: 'NORMALIZE_REQUIREMENT', payload: { customText: '采集商品价格和库存' } });
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
    expect(mod.state.candidatesRequested).toBe(false);
    expect(mod.state.step).toBe('requirement');
    expect(mod.state.requirementReviewReady).toBe(true);
  });

  it('keeps the manual structured path free of candidate generation', async () => {
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true },
    });

    clickElement('manual-structured');

    expect(mod.state.step).toBe('requirement');
    expect(document.getElementById('status')?.textContent).toContain('不会被标记为 LLM 生成');
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
  });

  it('resumes a stored candidates job on boot without requesting a new one', async () => {
    const candidates = [
      { id: 'c1', confidence: 0.9, requirement: sampleRequirement },
      { id: 'c2', confidence: 0.8, requirement: sampleRequirement },
      { id: 'c3', confidence: 0.7, requirement: sampleRequirement },
    ];
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? { success: false, workflowV2: true, error: 'must not create a new job' }
          : { success: true, workflowV2: true, candidates, job: { id: 'stored-job', status: 'completed' } },
    }, undefined, { oc_requirement_candidate_job: { recordingId: 'rec-1', jobId: 'stored-job' } });

    expect(mod.state.candidatesRequested).toBe(true);
    expect(mod.state.requirementJobId).toBe('stored-job');
    expect(mod.state.requirementCandidates).toHaveLength(3);
    expect(document.querySelectorAll('input[name="requirement-candidate"]')).toHaveLength(3);
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
    const primary = document.getElementById('action-primary') as HTMLButtonElement;
    expect(primary.textContent).toBe('下一步：查看结构化需求');
  });

  it('persists typed intent across wizard reopens', async () => {
    const first = await importModule({
      GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true },
    });
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '采集价格';
    textarea.dispatchEvent(new Event('input'));
    expect(first.storageMock.session.set).toHaveBeenCalledWith({ oc_intent_custom_description: '采集价格' });

    document.body.innerHTML = WIZARD_HTML;
    const second = await importModule(
      { GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true } },
      undefined,
      { oc_intent_custom_description: '采集价格' },
    );
    const restored = document.getElementById('custom-description') as HTMLTextAreaElement;
    expect(restored.value).toBe('采集价格');
    expect(second.mod.state.customIntentDescription).toBe('采集价格');
    const primary = document.getElementById('action-primary') as HTMLButtonElement;
    expect(primary.textContent).toBe('下一步：查看结构化需求');
    expect(primary.disabled).toBe(false);
  });

  it('keeps the v1 fallback unchanged when workflow v2 is unavailable', async () => {
    const { mod, sendMessage } = await importModule();

    expect(sendMessage).toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: false } });
    expect(sendMessage).toHaveBeenCalledWith({ action: 'PREDICT_INTENT' });
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
    expect(mod.state.workflowV2).toBe(false);
    expect(document.getElementById('intent-hint')?.textContent).toBe('请选择最符合此次操作的目的，或输入自定义目的');
    const primary = document.getElementById('action-primary') as HTMLButtonElement;
    expect(primary.textContent).toBe('下一步');
  });

  it('normalizes an edited candidate before requiring explicit confirmation', async () => {
    const candidate = { id: 'c1', confidence: 0.9, requirement: sampleRequirement };
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? {
              success: true,
              workflowV2: true,
              candidates: [candidate, { ...candidate, id: 'c2' }, { ...candidate, id: 'c3' }],
              job: { id: 'candidate-job', status: 'completed' },
            }
          : { success: true, workflowV2: true },
      NORMALIZE_REQUIREMENT: {
        success: true,
        workflowV2: true,
        requirement: sampleRequirement,
        requirementId: 'requirement-1',
        job: { id: 'normalize-1', status: 'completed' },
      },
      CONFIRM_REQUIREMENT: { success: true, workflowV2: true, requirement: { id: 'requirement-1', status: 'confirmed' } },
    });

    clickElement('action-primary');
    await flushPromises();
    mod.selectRequirementCandidate(candidate);
    await mod.prepareRequirement();
    expect(mod.state.step).toBe('requirement');
    expect((document.getElementById('requirement-title') as HTMLInputElement).value).toBe(sampleRequirement.title);

    await mod.confirmRequirement();
    expect(mod.state.requirementReviewReady).toBe(true);
    expect(mod.state.step).toBe('requirement');
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'NORMALIZE_REQUIREMENT',
      payload: { requirement: sampleRequirement, candidateJobId: 'candidate-job', candidateId: 'c1' },
    });

    await mod.confirmRequirement();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'CONFIRM_REQUIREMENT', payload: { requirementId: 'requirement-1' } });
    expect(mod.state.step).toBe('requirement-confirmed');
    expect(document.getElementById('requirement-confirmed-result')?.textContent).toContain('requirement-1');
  });

  it('normalizes custom text and supports a clearly manual structured fallback', async () => {
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? { success: false, workflowV2: true, error: 'provider unavailable', job: { id: 'failed-job', status: 'failed' } }
          : { success: true, workflowV2: true },
      NORMALIZE_REQUIREMENT: {
        success: true,
        workflowV2: true,
        requirement: sampleRequirement,
        requirementId: 'requirement-custom',
      },
    });
    clickElement('action-primary');
    await flushPromises();
    expect(mod.state.requirementJobId).toBe('failed-job');
    mod.openManualRequirementEditor();
    expect(mod.state.step).toBe('requirement');
    expect(mod.state.requirementReviewReady).toBe(false);
    expect(document.getElementById('status')?.textContent).toContain('不会被标记为 LLM 生成');

    mod.showStep('intent');
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = 'Collect products matching a keyword';
    textarea.dispatchEvent(new Event('input'));
    await mod.prepareRequirement();
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'NORMALIZE_REQUIREMENT',
      payload: { customText: 'Collect products matching a keyword' },
    });
    expect(mod.state.requirementReviewReady).toBe(true);
    expect(mod.state.requirementId).toBe('requirement-custom');
  });

  it('invalidates server review when a normalized requirement is edited', async () => {
    const { mod } = await importModule();
    mod.populateRequirementEditor(sampleRequirement);
    mod.state.requirementId = 'requirement-reviewed';
    mod.state.requirementReviewReady = true;
    mod.showStep('requirement');
    const title = document.getElementById('requirement-title') as HTMLInputElement;
    title.value = 'Edited title';
    title.dispatchEvent(new Event('input'));
    expect(mod.state.requirementReviewReady).toBe(false);
    expect(mod.state.requirementId).toBeNull();
  });

  it('rejects malformed structured requirement editor values', async () => {
    const { mod } = await importModule();
    mod.populateRequirementEditor(sampleRequirement);

    const title = document.getElementById('requirement-title') as HTMLInputElement;
    const requiredInputs = document.getElementById('requirement-required-inputs') as HTMLTextAreaElement;
    const outputFields = document.getElementById('requirement-output-fields') as HTMLTextAreaElement;
    const sampleOutput = document.getElementById('requirement-sample-output') as HTMLTextAreaElement;

    title.value = '';
    expect(() => mod.readRequirementEditor()).toThrow('标题和描述不能为空');
    title.value = sampleRequirement.title;

    requiredInputs.value = '{bad json';
    expect(() => mod.readRequirementEditor()).toThrow('必填输入不是有效 JSON');
    requiredInputs.value = '{}';
    expect(() => mod.readRequirementEditor()).toThrow('输入变量必须是 JSON 数组');
    requiredInputs.value = JSON.stringify(sampleRequirement.requiredInputs);

    outputFields.value = '[]';
    expect(() => mod.readRequirementEditor()).toThrow('至少需要一个输出字段');
    outputFields.value = JSON.stringify(sampleRequirement.outputFields);

    sampleOutput.value = '[]';
    expect(() => mod.readRequirementEditor()).toThrow('示例输出必须是 JSON 对象');
    sampleOutput.value = 'null';
    expect(() => mod.readRequirementEditor()).toThrow('示例输出必须是 JSON 对象');
  });

  it('reports invalid workflow candidate counts and renders candidates without inputs', async () => {
    const noInputRequirement = { ...sampleRequirement, requiredInputs: [], optionalInputs: [] };
    const { mod } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? {
              success: true,
              workflowV2: true,
              candidates: [{ id: 'only', confidence: 0.5, requirement: noInputRequirement }],
            }
          : { success: true, workflowV2: true },
    });

    clickElement('action-primary');
    await flushPromises();
    expect(mod.state.error).toContain('实际返回 1 个');
    expect(document.getElementById('candidates-list')?.textContent).toContain('输入：无');
    const radio = document.querySelector('input[name="requirement-candidate"]') as HTMLInputElement;
    radio.checked = true;
    radio.dispatchEvent(new Event('change'));
    expect(mod.state.selectedRequirementCandidate?.id).toBe('only');
  });

  it('uses safe defaults when the workflow fails without details', async () => {
    const { mod } = await importModule({
      GET_REQUIREMENT_WORKFLOW: { success: false, workflowV2: true },
    });

    expect(mod.state.requirementJobId).toBeNull();
    expect(mod.state.error).toContain('采集需求候选生成失败');
    expect(document.getElementById('status')?.className).toBe('error');
  });

  it('requires a candidate or custom requirement before preparing review', async () => {
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true, candidates: [] },
    });
    mod.state.selectedRequirementCandidate = null;

    await mod.prepareRequirement();

    expect(document.getElementById('status')?.textContent).toContain('请选择候选需求');
    expect(sendMessage).not.toHaveBeenCalledWith(expect.objectContaining({ action: 'NORMALIZE_REQUIREMENT' }));
  });

  it('surfaces incomplete and thrown custom normalization failures', async () => {
    const { mod } = await importModule({
      NORMALIZE_REQUIREMENT: { success: true, workflowV2: true, job: { id: 'normalize-failed', status: 'failed' } },
    });
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = 'Collect a custom result';

    await mod.prepareRequirement();

    expect(mod.state.requirementJobId).toBe('normalize-failed');
    expect(mod.state.error).toContain('自定义需求规范化失败');
    expect(mod.state.loading).toBe(false);

    const thrown = await importModule({
      NORMALIZE_REQUIREMENT: () => {
        throw 'normalizer disconnected';
      },
    });
    (document.getElementById('custom-description') as HTMLTextAreaElement).value = 'Collect another result';
    await thrown.mod.prepareRequirement();
    expect(thrown.mod.state.error).toBe('normalizer disconnected');
  });

  it('normalizes a manual structured requirement without candidate lineage', async () => {
    const { mod, sendMessage } = await importModule({
      NORMALIZE_REQUIREMENT: {
        success: true,
        workflowV2: true,
        requirement: sampleRequirement,
        requirementId: 'manual-requirement',
      },
    });
    mod.populateRequirementEditor(sampleRequirement);
    mod.showStep('requirement');

    await mod.normalizeRequirementEditor();

    expect(sendMessage).toHaveBeenCalledWith({
      action: 'NORMALIZE_REQUIREMENT',
      payload: { requirement: sampleRequirement },
    });
    expect(mod.state.requirementId).toBe('manual-requirement');
    expect(mod.state.requirementReviewReady).toBe(true);
  });

  it('keeps structured normalization errors in the review step', async () => {
    const { mod } = await importModule({
      NORMALIZE_REQUIREMENT: { success: true, workflowV2: true },
    });
    mod.populateRequirementEditor(sampleRequirement);
    mod.showStep('requirement');

    await mod.normalizeRequirementEditor();
    expect(mod.state.error).toBe('结构化需求验证失败');
    expect(mod.state.loading).toBe(false);

    (document.getElementById('requirement-output-fields') as HTMLTextAreaElement).value = 'not json';
    await mod.normalizeRequirementEditor();
    expect(document.getElementById('status')?.textContent).toBe('输出字段不是有效 JSON');
  });

  it('surfaces confirmation failures without leaving requirement review', async () => {
    const { mod } = await importModule({
      CONFIRM_REQUIREMENT: { success: false, workflowV2: true },
    });
    mod.populateRequirementEditor(sampleRequirement);
    mod.state.requirementId = 'requirement-failed';
    mod.state.requirementReviewReady = true;
    mod.showStep('requirement');

    await mod.confirmRequirement();

    expect(mod.state.step).toBe('requirement');
    expect(mod.state.error).toBe('需求确认失败');
    expect(mod.state.loading).toBe(false);
  });

  it('retries candidate jobs and falls back to reloading when no job is known', async () => {
    const candidates = [
      { id: 'c1', confidence: 0.9, requirement: sampleRequirement },
      { id: 'c2', confidence: 0.8, requirement: sampleRequirement },
      { id: 'c3', confidence: 0.7, requirement: sampleRequirement },
    ];
    const { mod, sendMessage } = await importModule({
      RETRY_REQUIREMENT_JOB: { success: true, workflowV2: true, candidates },
    });
    mod.state.requirementJobId = 'retry-job';
    await mod.retryRequirementJob();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'RETRY_REQUIREMENT_JOB', payload: { jobId: 'retry-job' } });
    expect(mod.state.requirementCandidates).toEqual(candidates);
    expect(mod.state.requirementJobId).toBe('retry-job');

    sendMessage.mockClear();
    mod.state.requirementJobId = null;
    await mod.retryRequirementJob();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
  });

  it('shows safe retry errors and preserves the failed job id', async () => {
    const { mod } = await importModule({
      RETRY_REQUIREMENT_JOB: { success: false, workflowV2: true },
    });
    mod.state.requirementJobId = 'failed-job';

    await mod.retryRequirementJob();

    expect(document.getElementById('status')?.textContent).toBe('重试失败');
    expect(mod.state.requirementJobId).toBe('failed-job');
    expect(mod.state.loading).toBe(false);
  });

  it('keeps review valid when editor content is unchanged or the wrong step is active', async () => {
    const { mod } = await importModule();
    mod.populateRequirementEditor(sampleRequirement);
    mod.state.requirementId = 'reviewed';
    mod.state.requirementReviewReady = true;

    mod.showStep('requirement');
    mod.markRequirementDirty();
    expect(mod.state.requirementId).toBe('reviewed');

    mod.showStep('intent');
    (document.getElementById('requirement-title') as HTMLInputElement).value = 'Changed while hidden';
    mod.markRequirementDirty();
    expect(mod.state.requirementId).toBe('reviewed');
  });

  it('routes workflow buttons through preparation, review, back, and dsl generation', async () => {
    const candidate = { id: 'c1', confidence: 0.9, requirement: sampleRequirement };
    const { mod, sendMessage } = await importModule({
      GET_REQUIREMENT_WORKFLOW: (message: { payload?: { startCandidates?: boolean } }) =>
        message.payload?.startCandidates
          ? {
              success: true,
              workflowV2: true,
              candidates: [candidate, { ...candidate, id: 'c2' }, { ...candidate, id: 'c3' }],
              job: { id: 'candidate-job', status: 'completed' },
            }
          : { success: true, workflowV2: true },
      NORMALIZE_REQUIREMENT: {
        success: true,
        workflowV2: true,
        requirement: sampleRequirement,
        requirementId: 'requirement-button',
      },
      CREATE_DSL_WORKFLOW: {
        success: true,
        workflow: {
          id: 'workflow-button',
          requirementId: 'requirement-button',
          recordingId: 'recording-button',
          browserProfileId: 'current-chrome-profile',
          status: 'awaiting_replay',
          repairCount: 0,
          maxRepairs: 3,
          provisionalRule: sampleRule,
          provisionalYaml: 'id: ext-test-1',
        },
      },
    });
    clickElement('action-primary');
    await flushPromises();
    mod.selectRequirementCandidate(candidate);
    clickElement('action-primary');
    await flushPromises();
    expect(mod.state.step).toBe('requirement');

    clickElement('action-primary');
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({ action: 'NORMALIZE_REQUIREMENT' }));
    expect(mod.state.requirementReviewReady).toBe(true);

    clickElement('action-secondary');
    expect(mod.state.step).toBe('intent');

    mod.showStep('requirement-confirmed');
    clickElement('action-primary');
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'CREATE_DSL_WORKFLOW',
      payload: { requirementId: 'requirement-button', browserProfileId: 'current-chrome-profile' },
    });
    expect(mod.state.step).toBe('preview');
  });

  it('displays error when prediction fails', async () => {
    const { mod } = await importModule({
      PREDICT_INTENT: { success: false, error: '服务端未配置' },
    });
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('服务端未配置');
    expect(status.className).toBe('error');
    expect(mod.state.error).toBe('服务端未配置');
  });

  it('selects an intent and enables next button', async () => {
    const { mod } = await importModule();
    mod.selectCandidate(sampleCandidate);
    const btn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
    expect(mod.state.selectedIntent).toEqual(sampleCandidate);
  });

  it('selects intent by clicking candidate radio', async () => {
    const { mod } = await importModule();
    const radio = document.querySelector('input[name="intent"]') as HTMLInputElement;
    expect(radio).not.toBeNull();
    radio.checked = true;
    radio.dispatchEvent(new Event('change'));
    await flushPromises();
    expect(mod.state.selectedIntent).toEqual(sampleCandidate);
  });

  it('uses custom intent input when no candidate selected', async () => {
    const { mod } = await importModule();
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '自定义目的';
    textarea.dispatchEvent(new Event('input'));
    const intent = mod.buildIntentForGeneration();
    expect(intent).toEqual({
      ...mod.state.fallbackIntent,
      label: '自定义目的',
      description: '自定义目的',
    });
  });

  it('generates DSL and moves to preview step', async () => {
    const { mod, sendMessage } = await importModule();
    mod.selectCandidate(sampleCandidate);
    await mod.generateDSL();
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith({
      action: 'GENERATE_DSL_FROM_INTENT',
      payload: { intent: sampleCandidate },
    });
    expect(mod.state.yaml).toBe('id: ext-test-1\nname: Test rule');
    expect(mod.state.step).toBe('preview');
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.className).toBe('success');
  });

  it('renders DSL preview', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.yaml = 'id: ext-test-1';
    mod.renderPreview();
    const stepsList = document.getElementById('steps-list') as HTMLDivElement;
    expect(stepsList.querySelectorAll('.step-item')).toHaveLength(2);
    const yamlPreview = document.getElementById('yaml-preview') as HTMLPreElement;
    expect(yamlPreview.textContent).toBe('id: ext-test-1');
  });

  it('moves to confirm step and renders step checkboxes', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.yaml = 'id: ext-test-1';
    mod.renderConfirmList();
    mod.showStep('confirm');

    const container = document.getElementById('confirm-list') as HTMLDivElement;
    expect(container.querySelectorAll('.confirm-item')).toHaveLength(2);
    expect(mod.state.step).toBe('confirm');
  });

  it('disables save button until all steps are confirmed', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.renderConfirmList();
    mod.showStep('confirm');
    const saveBtn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(true);

    mod.toggleStepConfirmation(0, true);
    expect(saveBtn.disabled).toBe(true);

    mod.toggleStepConfirmation(1, true);
    expect(saveBtn.disabled).toBe(false);
  });

  it('confirms all steps via confirm-all checkbox', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.renderConfirmList();
    mod.showStep('confirm');
    mod.confirmAll(true);
    expect(mod.state.confirmedSteps.size).toBe(2);
    const saveBtn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(false);
    const confirmAllInput = document.getElementById('confirm-all') as HTMLInputElement;
    expect(confirmAllInput.checked).toBe(true);
  });

  it('saves rule after all steps confirmed', async () => {
    const { mod, sendMessage, storageMock } = await importModule({}, { baseUrl: 'http://localhost:8080/' });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith({ action: 'UPLOAD_CONFIRMED_RULE' });
    expect(mod.state.ruleId).toBe('ext-test-1');
    const resultEl = document.getElementById('save-result') as HTMLParagraphElement;
    expect(resultEl.textContent).toContain('ext-test-1');
    const link = document.getElementById('admin-link') as HTMLAnchorElement;
    expect(link.href).toBe('http://localhost:8080/admin/rules/ext-test-1');
    expect(storageMock.local.get).toHaveBeenCalledWith('serverConfig');
  });

  it('shows error when saving fails', async () => {
    const { mod } = await importModule({
      UPLOAD_CONFIRMED_RULE: { success: false, error: '上传失败' },
    });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('上传失败');
    expect(status.className).toBe('error');
  });

  it('prevents saving when not all steps confirmed', async () => {
    const { mod, sendMessage } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0]);
    await mod.saveRule();
    await flushPromises();
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'UPLOAD_CONFIRMED_RULE' });
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('请确认所有步骤后再保存');
  });

  it('clears selection when typing custom intent', async () => {
    const { mod } = await importModule();
    mod.selectCandidate(sampleCandidate);
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '自定义';
    textarea.dispatchEvent(new Event('input'));
    expect(mod.state.selectedIntent).toBeNull();
  });

  it('calls server action for custom intent description', async () => {
    const { mod, sendMessage } = await importModule();
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '自定义采集列表';
    textarea.dispatchEvent(new Event('input'));
    await mod.generateDSL();
    await flushPromises();

    const intent = mod.buildIntentForGeneration();
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'GENERATE_DSL_FROM_INTENT_SERVER',
      payload: { intent, customDescription: '自定义采集列表' },
    });
    expect(mod.state.yaml).toBe('id: ext-server-1\nname: Server rule');
    expect(mod.state.step).toBe('preview');
  });

  it('shows error when server custom intent generation fails', async () => {
    const { mod, sendMessage } = await importModule({
      GENERATE_DSL_FROM_INTENT_SERVER: { success: false, error: '服务端生成失败' },
    });
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '自定义目的';
    textarea.dispatchEvent(new Event('input'));
    await mod.generateDSL();
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith({
      action: 'GENERATE_DSL_FROM_INTENT_SERVER',
      payload: expect.objectContaining({ customDescription: '自定义目的' }),
    });
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('服务端生成失败');
    expect(mod.state.error).toBe('服务端生成失败');
  });

  it('handles thrown errors during custom server generateDSL', async () => {
    const { mod } = await importModule({
      GENERATE_DSL_FROM_INTENT_SERVER: () => {
        throw new Error('server dsl crash');
      },
    });
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '自定义目的';
    textarea.dispatchEvent(new Event('input'));
    await mod.generateDSL();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('生成 DSL 失败：server dsl crash');
  });

  it('handles generate DSL failure', async () => {
    const { mod } = await importModule({
      GENERATE_DSL_FROM_INTENT: { success: false, error: '生成失败' },
    });
    mod.selectCandidate(sampleCandidate);
    await mod.generateDSL();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('生成失败');
    expect(mod.state.error).toBe('生成失败');
  });

  it('updates confirm-all indeterminate state', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.renderConfirmList();
    mod.toggleStepConfirmation(0, true);
    const confirmAllInput = document.getElementById('confirm-all') as HTMLInputElement;
    expect(confirmAllInput.indeterminate).toBe(true);
  });

  it('handles thrown errors during loadPredictions', async () => {
    await importModule({
      PREDICT_INTENT: () => {
        throw new Error('network down');
      },
    });
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('加载预测失败：network down');
    expect(status.className).toBe('error');
  });

  it('handles thrown errors during generateDSL', async () => {
    const { mod } = await importModule({
      GENERATE_DSL_FROM_INTENT: () => {
        throw new Error('dsl crash');
      },
    });
    mod.selectCandidate(sampleCandidate);
    await mod.generateDSL();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('生成 DSL 失败：dsl crash');
  });

  it('handles thrown errors during saveRule', async () => {
    const { mod } = await importModule({
      UPLOAD_CONFIRMED_RULE: () => {
        throw new Error('upload crash');
      },
    });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('保存规则失败：upload crash');
  });

  it('requires an intent before generating DSL', async () => {
    const { mod, sendMessage } = await importModule();
    mod.state.selectedIntent = null;
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '';
    await mod.generateDSL();
    await flushPromises();
    const generateCalls = sendMessage.mock.calls.filter((call) => call[0].action === 'GENERATE_DSL_FROM_INTENT');
    expect(generateCalls).toHaveLength(0);
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('请选择一个目的或输入自定义目的');
  });

  it('enables next button via custom intent input', async () => {
    await importModule();
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = '自定义目的';
    textarea.dispatchEvent(new Event('input'));
    const btn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
  });

  it('navigates preview to replay step via next-preview button', async () => {
    const { mod, sendMessage } = await importModule({ START_REPLAY: { success: true, taskId: 'task-1' } });
    mod.state.rule = sampleRule;
    mod.state.yaml = 'id: ext-test-1';
    mod.showStep('preview');
    clickElement('action-primary');
    await flushPromises();
    expect(mod.state.step).toBe('replay');
    expect(sendMessage).toHaveBeenCalledWith({ action: 'START_REPLAY', payload: { rule: sampleRule } });
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('正在回放');
  });

  it('returns from confirm to replay via back-confirm button', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.showStep('confirm');
    clickElement('action-secondary');
    expect(mod.state.step).toBe('replay');
  });

  it('confirms and unconfirms all steps via confirm-all checkbox click', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.renderConfirmList();
    const confirmAllInput = document.getElementById('confirm-all') as HTMLInputElement;
    confirmAllInput.checked = true;
    confirmAllInput.dispatchEvent(new Event('change'));
    await flushPromises();
    expect(mod.state.confirmedSteps.size).toBe(2);

    confirmAllInput.checked = false;
    confirmAllInput.dispatchEvent(new Event('change'));
    await flushPromises();
    expect(mod.state.confirmedSteps.size).toBe(0);
  });

  it('toggles individual step confirmation by clicking checkbox', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.renderConfirmList();
    const firstCheckbox = document.querySelector('#confirm-list input[type="checkbox"]') as HTMLInputElement;
    firstCheckbox.checked = true;
    firstCheckbox.dispatchEvent(new Event('change'));
    await flushPromises();
    expect(mod.state.confirmedSteps.has(0)).toBe(true);

    firstCheckbox.checked = false;
    firstCheckbox.dispatchEvent(new Event('change'));
    await flushPromises();
    expect(mod.state.confirmedSteps.has(0)).toBe(false);
  });

  it('uploads rule when save button is clicked', async () => {
    const { mod, sendMessage } = await importModule({}, { baseUrl: 'http://localhost:8080' });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    mod.updateSaveButton();
    mod.showStep('confirm');
    clickElement('action-primary');
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'UPLOAD_CONFIRMED_RULE' });
    expect(mod.state.ruleId).toBe('ext-test-1');
  });

  it('generates DSL when next-intent button is clicked', async () => {
    const { mod, sendMessage } = await importModule();
    mod.selectCandidate(sampleCandidate);
    mod.updateNextButton();
    clickElement('action-primary');
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'GENERATE_DSL_FROM_INTENT',
      payload: { intent: sampleCandidate },
    });
    expect(mod.state.step).toBe('preview');
  });

  it('calls window.close when close button is clicked', async () => {
    const closeSpy = vi.spyOn(window, 'close').mockImplementation(() => undefined);
    const { mod } = await importModule();
    mod.showStep('save');
    clickElement('action-primary');
    expect(closeSpy).toHaveBeenCalled();
    closeSpy.mockRestore();
  });

  it('renders preview safely when rule has no steps', async () => {
    const { mod } = await importModule();
    mod.state.rule = { ...sampleRule, steps: undefined };
    mod.state.yaml = '';
    mod.renderPreview();
    const stepsList = document.getElementById('steps-list') as HTMLDivElement;
    expect(stepsList.innerHTML).toBe('');
  });

  it('does not crash when confirm-list container is missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    expect(() => mod.renderConfirmList()).not.toThrow();
  });

  it('keeps admin link default when serverConfig is absent', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();
    const link = document.getElementById('admin-link') as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('#');
  });

  it('starts replay calls START_REPLAY action and resets state', async () => {
    const { mod, sendMessage } = await importModule({ START_REPLAY: { success: true, taskId: 'task-1' } });
    mod.state.rule = sampleRule;
    mod.state.replayLogs = [{ type: 'log', level: 'info', message: 'old' }];
    mod.state.replayStatus = 'success';
    mod.state.replayError = 'old error';
    mod.state.replayExtracted = { old: true };
    await mod.startReplay();
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith({ action: 'START_REPLAY', payload: { rule: sampleRule } });
    expect(mod.state.replayStatus).toBe('running');
    expect(mod.state.replayLogs).toEqual([]);
    expect(mod.state.replayError).toBeNull();
    expect(mod.state.replayExtracted).toEqual({});
    expect(mod.state.step).toBe('replay');
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('正在回放');
  });

  it('binds confirmed required inputs into the browser replay payload', async () => {
    const { mod, sendMessage } = await importModule({
      START_DSL_REPLAY: {
        success: true,
        replay: { id: 'replay-inputs-1', workflowId: 'workflow-inputs-1', sequence: 1, status: 'running' },
      },
      START_REPLAY: { success: true, taskId: 'task-inputs-1' },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-inputs-1';
    mod.state.rule = sampleRule;
    mod.populateRequirementEditor(sampleRequirement);
    mod.renderPreview();

    const keyword = document.querySelector<HTMLInputElement>('[data-replay-input-name="keyword"]');
    expect(keyword).not.toBeNull();
    keyword!.value = 'site:baidu.com 百度搜索帮助';
    sendMessage.mockClear();

    await mod.startReplay();

    expect(sendMessage.mock.calls.map(([message]) => message.action)).toEqual([
      'START_DSL_REPLAY',
      'START_REPLAY',
    ]);
    expect(sendMessage).toHaveBeenLastCalledWith({
      action: 'START_REPLAY',
      payload: {
        rule: sampleRule,
        variables: { keyword: 'site:baidu.com 百度搜索帮助' },
      },
    });
  });

  it('rejects a missing required replay input before creating a durable replay attempt', async () => {
    const { mod, sendMessage } = await importModule();
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-inputs-2';
    mod.state.rule = sampleRule;
    mod.populateRequirementEditor(sampleRequirement);
    mod.renderPreview();
    sendMessage.mockClear();

    await mod.startReplay();

    expect(sendMessage).not.toHaveBeenCalled();
    expect(mod.state.step).toBe('preview');
    expect((document.getElementById('status') as HTMLDivElement).textContent)
      .toContain('keyword');
  });

  it('parses typed replay inputs and preserves optional defaults', async () => {
    const { mod } = await importModule();
    mod.populateRequirementEditor({
      title: 'Typed inputs',
      description: 'Exercise every portable replay input type.',
      requiredInputs: [
        { name: 'limit', type: 'number', description: 'Result limit', constraints: { minimum: 1 } },
        { name: 'enabled', type: 'boolean', description: 'Enable filtering' },
        { name: 'filters', type: 'object', description: 'Filter object' },
        { name: 'tags', type: 'array', description: 'Tag list' },
      ],
      optionalInputs: [
        { name: 'label', type: 'string', description: 'Label', default: 'default-label' },
        { name: 'optionalToken', type: 'string', description: 'Managed elsewhere', secret: true },
      ],
      outputFields: [{ name: 'item', type: 'string', description: 'Item' }],
      sampleOutput: { item: 'example' },
    });
    mod.renderPreview();

    const set = (name: string, value: string) => {
      const field = document.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(
        `[data-replay-input-name="${name}"]`,
      );
      expect(field).not.toBeNull();
      field!.value = value;
    };
    set('limit', '3');
    set('enabled', 'true');
    set('filters', '{"category":"docs"}');
    set('tags', '["visible","ordinary"]');

    expect(mod.readReplayInputs()).toEqual({
      limit: 3,
      enabled: true,
      filters: { category: 'docs' },
      tags: ['visible', 'ordinary'],
      label: 'default-label',
    });
  });

  it('handles startReplay failure response', async () => {
    const { mod } = await importModule({ START_REPLAY: { success: false, error: 'no active tab' } });
    mod.state.rule = sampleRule;
    await mod.startReplay();
    await flushPromises();
    expect(mod.state.replayStatus).toBe('failure');
    expect(mod.state.replayError).toBe('no active tab');
    expect(mod.state.loading).toBe(false);
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('回放失败');
  });

  it('handles startReplay thrown error', async () => {
    const { mod } = await importModule({
      START_REPLAY: () => {
        throw new Error('runtime disconnected');
      },
    });
    mod.state.rule = sampleRule;
    await mod.startReplay();
    await flushPromises();
    expect(mod.state.replayStatus).toBe('failure');
    expect(mod.state.replayError).toBe('runtime disconnected');
    expect(mod.state.loading).toBe(false);
  });

  it('requires a rule before starting replay', async () => {
    const { mod, sendMessage } = await importModule();
    mod.state.rule = null;
    await mod.startReplay();
    await flushPromises();
    expect(sendMessage).not.toHaveBeenCalledWith({ action: 'START_REPLAY', payload: expect.anything() });
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('没有可用规则');
  });

  it('handles replay progress and appends logs', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.handleReplayProgress({ type: 'log', level: 'info', message: 'navigated' });
    expect(mod.state.replayLogs).toHaveLength(1);
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('[info] navigated');
  });

  it('merges result payload into replayExtracted', async () => {
    const { mod } = await importModule();
    mod.handleReplayProgress({ type: 'result', payload: { title: 'Product', price: 100 } });
    expect(mod.state.replayExtracted).toEqual({ title: 'Product', price: 100 });
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('Product');
  });

  it('ignores non-object result payload', async () => {
    const { mod } = await importModule();
    mod.handleReplayProgress({ type: 'result', payload: 'plain text' });
    expect(mod.state.replayExtracted).toEqual({});
  });

  it('handles replay progress status and snapshot logs', async () => {
    const { mod } = await importModule();
    mod.handleReplayProgress({ type: 'status', status: 'done', message: 'ok' });
    mod.handleReplayProgress({ type: 'snapshot', snapshot: { name: 'home', type: 'html', data: '<p/>' } });
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('[status] done: ok');
    expect(stage.textContent).toContain('[snapshot] home');
  });

  it('escapes HTML in replay log output', async () => {
    const { mod } = await importModule();
    mod.handleReplayProgress({
      type: 'log',
      level: 'info"><script>alert(1)</script>',
      message: '<img src=x onerror=alert(2)>',
    });
    mod.handleReplayProgress({ type: 'status', status: '<b>done</b>', message: '<script>alert(3)</script>' });
    mod.handleReplayProgress({ type: 'snapshot', snapshot: { name: '<iframe src="evil">', type: 'html', data: '' } });
    mod.handleReplayProgress({ type: 'result', payload: { key: '<script>alert(4)</script>' } });
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.innerHTML).not.toContain('<script>');
    expect(stage.innerHTML).toContain('&lt;img');
    expect(stage.textContent).toContain('<img src=x onerror=alert(2)>');
    expect(stage.textContent).toContain('<b>done</b>');
    expect(stage.textContent).toContain('<iframe src="evil">');
  });

  it('renders unknown progress type as empty', async () => {
    const { mod } = await importModule();
    mod.handleReplayProgress({ type: 'unknown' } as unknown as import('./intent-types').ReplayProgress);
    expect(mod.state.replayLogs).toHaveLength(1);
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.querySelector('.replay-logs')?.textContent).toBe('');
  });

  it('handles replay complete success and enables confirm button', async () => {
    const { mod } = await importModule();
    mod.showStep('replay');
    mod.handleReplayComplete({ status: 'success' });
    expect(mod.state.replayStatus).toBe('success');
    expect(mod.state.loading).toBe(false);
    const confirmBtn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(confirmBtn.disabled).toBe(false);
  });

  it('handles replay complete failure with error message', async () => {
    const { mod } = await importModule();
    mod.handleReplayComplete({ status: 'failure', error: { message: 'selector not found' } });
    expect(mod.state.replayStatus).toBe('failure');
    expect(mod.state.replayError).toBe('selector not found');
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('selector not found');
    const confirmBtn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(confirmBtn.disabled).toBe(true);
  });

  it('handles replay complete with string error', async () => {
    const { mod } = await importModule();
    mod.handleReplayComplete({ status: 'failure', error: 'crash' });
    expect(mod.state.replayError).toBe('crash');
  });

  it('falls back to failure for unknown replay complete status', async () => {
    const { mod } = await importModule();
    mod.handleReplayComplete({ status: 'unknown-status' });
    expect(mod.state.replayStatus).toBe('failure');
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('回放失败');
    const confirmBtn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(confirmBtn.disabled).toBe(true);
  });

  it('handles replay complete failure with message and no error', async () => {
    const { mod } = await importModule();
    mod.handleReplayComplete({ status: 'failure', message: 'runner reported failure' });
    expect(mod.state.replayStatus).toBe('failure');
    expect(mod.state.replayError).toBe('runner reported failure');
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('runner reported failure');
  });

  it('prefers explicit error over message in replay complete', async () => {
    const { mod } = await importModule();
    mod.handleReplayComplete({ status: 'failure', error: 'explicit error', message: 'runner message' });
    expect(mod.state.replayError).toBe('explicit error');
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('explicit error');
    expect(stage.textContent).not.toContain('runner message');
  });

  it('aborts replay and updates status', async () => {
    const { mod, sendMessage } = await importModule();
    await mod.abortReplay();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'ABORT_REPLAY' });
    expect(mod.state.replayStatus).toBe('cancelled');
    expect(mod.state.loading).toBe(false);
  });

  it('surfaces abort replay send failure', async () => {
    const { mod, sendMessage } = await importModule({
      ABORT_REPLAY: () => {
        throw new Error('port disconnected');
      },
    });
    await mod.abortReplay();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'ABORT_REPLAY' });
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('取消回放失败：port disconnected');
  });

  it('clicks abort replay button and updates status', async () => {
    const { mod, sendMessage } = await importModule();
    mod.showStep('replay');
    clickElement('action-tertiary');
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith({ action: 'ABORT_REPLAY' });
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('已取消');
  });

  it('abortReplay directly completes the DSL replay when a workflow replay is in flight', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: { success: true, workflow: { id: 'wf-1', status: 'awaiting_confirmation' } },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayAttemptId = 'r-1';
    mod.state.replayStatus = 'running';

    await mod.abortReplay();

    const calls = (sendMessage as Mock).mock.calls.map(
      (call: unknown[]) => (call[0] as { action: string }).action,
    );
    expect(calls).toContain('ABORT_REPLAY');
    expect(calls).toContain('COMPLETE_DSL_REPLAY');
    const complete = (sendMessage as Mock).mock.calls.find(
      (call: unknown[]) => (call[0] as { action: string }).action === 'COMPLETE_DSL_REPLAY',
    ) as [{ action: string; payload?: { errorCode?: string } }] | undefined;
    expect(complete?.[0]?.payload?.errorCode).toBe('REPLAY_CANCELLED');
  });

  it('completeWorkflowReplay suppresses INVALID_STATE when status is already terminal', async () => {
    // abortReplay completes the attempt directly; the background's later
    // REPLAY_COMPLETE(failure) broadcast triggers a second completion that the
    // server rejects with ErrReplayState (HTTP 409, code INVALID_STATE). The
    // background surfaces this as `{ success: false, error: <server message> }`
    // (requirementRequest only propagates body.error, not body.code), so detect
    // the duplicate-completion race via the stable server message substring.
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: false,
        error: 'dsl workflow cannot transition from its current state (traceId: t-1)',
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayAttemptId = 'r-1';
    mod.state.replayStatus = 'cancelled'; // already terminal after abortReplay

    await mod.completeWorkflowReplay({ status: 'failure', message: 'racing broadcast' });

    // Status stays 'cancelled' — not overwritten to 'failure'.
    expect(mod.state.replayStatus).toBe('cancelled');
    expect(mod.state.replayError).toBeNull();
    // The racing completion was still sent to the background.
    expect((sendMessage as Mock).mock.calls.some(
      (call: unknown[]) => (call[0] as { action: string }).action === 'COMPLETE_DSL_REPLAY',
    )).toBe(true);
  });

  it('completeWorkflowReplay prefers structured code INVALID_STATE over message text', async () => {
    // When the background handler surfaces `code: 'INVALID_STATE'` (via
    // ServerRequestError threading), the wizard should suppress the conflict
    // even if the error message lacks the legacy Chinese/English substring.
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: false,
        error: 'unexpected translation that does not contain the legacy substring',
        code: 'INVALID_STATE',
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayAttemptId = 'r-1';
    mod.state.replayStatus = 'success'; // already terminal

    await mod.completeWorkflowReplay({ status: 'failure', message: 'racing broadcast' });

    // Status stays 'success' — not overwritten to 'failure'.
    expect(mod.state.replayStatus).toBe('success');
    expect(mod.state.replayError).toBeNull();
    expect((sendMessage as Mock).mock.calls.some(
      (call: unknown[]) => (call[0] as { action: string }).action === 'COMPLETE_DSL_REPLAY',
    )).toBe(true);
  });

  it('proceeds from replay to confirm step', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.replayStatus = 'success';
    mod.showStep('replay');
    clickElement('action-primary');

    expect(mod.state.step).toBe('confirm');
    const container = document.getElementById('confirm-list') as HTMLDivElement;
    expect(container.querySelectorAll('.confirm-item')).toHaveLength(2);
  });

  it('returns from replay to preview via back-replay button', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.showStep('replay');
    clickElement('action-secondary');
    expect(mod.state.step).toBe('preview');
  });

  it('does not render replay monitor when stage element is missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    expect(() => mod.renderReplayMonitor()).not.toThrow();
  });

  it('renders replay monitor with idle status', async () => {
    const { mod } = await importModule();
    mod.renderReplayMonitor();
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('等待回放');
  });

  it('renders result log with undefined payload without throwing', async () => {
    const { mod } = await importModule();
    mod.handleReplayProgress({ type: 'result', payload: undefined });
    expect(() => mod.renderReplayMonitor()).not.toThrow();
    const stage = document.getElementById('replay-stage') as HTMLDivElement;
    expect(stage.textContent).toContain('[result]');
    expect(mod.state.replayExtracted).toEqual({});
  });

  it('registers runtime message listener in initWizard', async () => {
    const { onMessageListeners } = await importModule();
    expect(onMessageListeners).toHaveLength(1);
  });

  it('routes REPLAY_PROGRESS runtime messages to handler', async () => {
    const { mod, onMessageListeners } = await importModule();
    const listener = onMessageListeners[0];
    listener({ action: 'REPLAY_PROGRESS', payload: { type: 'log', level: 'info', message: 'msg' } });
    expect(mod.state.replayLogs).toHaveLength(1);
  });

  it('routes REPLAY_COMPLETE runtime messages to handler', async () => {
    const { mod, onMessageListeners } = await importModule();
    const listener = onMessageListeners[0];
    listener({ action: 'REPLAY_COMPLETE', payload: { status: 'success' } });
    expect(mod.state.replayStatus).toBe('success');
  });

  it('ignores replay messages sent directly by the replay tab', async () => {
    const { mod, onMessageListeners } = await importModule();
    const listener = onMessageListeners[0];
    const tabSender = { tab: { id: 123 } };
    listener({ action: 'REPLAY_PROGRESS', payload: { type: 'log', level: 'info', message: 'duplicate' } }, tabSender);
    listener({ action: 'REPLAY_COMPLETE', payload: { status: 'success' } }, tabSender);
    expect(mod.state.replayLogs).toHaveLength(0);
    expect(mod.state.replayStatus).toBe('idle');
  });

  it('ignores unknown runtime messages', async () => {
    const { mod, onMessageListeners } = await importModule();
    const listener = onMessageListeners[0];
    expect(() => listener({ action: 'UNKNOWN', payload: {} })).not.toThrow();
    expect(mod.state.replayLogs).toHaveLength(0);
  });

  it('renders determinate progress while a chunked llm job is running', async () => {
    const { onMessageListeners } = await importModule();
    const listener = onMessageListeners[0];
    listener({ action: 'LLM_JOB_PROGRESS', payload: { chunkCount: 32, completedChunks: 12 } });

    const container = document.getElementById('job-progress') as HTMLDivElement;
    expect(container.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('job-progress-text')?.textContent).toBe('正在分析… (12/32)');
    const bar = document.getElementById('job-progress-bar') as HTMLProgressElement;
    expect(bar.max).toBe(32);
    expect(bar.value).toBe(12);
  });

  it('clamps out-of-range chunk progress values', async () => {
    const { mod } = await importModule();
    mod.renderJobProgress({ chunkCount: 5, completedChunks: 9 });
    expect(document.getElementById('job-progress-text')?.textContent).toBe('正在分析… (5/5)');
    expect((document.getElementById('job-progress-bar') as HTMLProgressElement).value).toBe(5);
  });

  it('keeps the indeterminate spinner when chunk progress is unknown', async () => {
    const { mod } = await importModule();
    mod.renderJobProgress({ chunkCount: 0, completedChunks: 0 });
    const container = document.getElementById('job-progress') as HTMLDivElement;
    expect(container.classList.contains('hidden')).toBe(true);
  });

  it('hides job progress on the next status update', async () => {
    const { mod } = await importModule();
    mod.renderJobProgress({ chunkCount: 32, completedChunks: 12 });
    const container = document.getElementById('job-progress') as HTMLDivElement;
    expect(container.classList.contains('hidden')).toBe(false);
    mod.setStatus('请选择、编辑或输入自定义采集需求', 'info');
    expect(container.classList.contains('hidden')).toBe(true);
  });

  it('ignores llm progress messages sent directly by a tab', async () => {
    const { onMessageListeners } = await importModule();
    const listener = onMessageListeners[0];
    listener({ action: 'LLM_JOB_PROGRESS', payload: { chunkCount: 32, completedChunks: 12 } }, { tab: { id: 1 } });
    const container = document.getElementById('job-progress') as HTMLDivElement;
    expect(container.classList.contains('hidden')).toBe(true);
  });

  it('uses fallback intent and candidates when response omits them', async () => {
    const { mod } = await importModule({
      PREDICT_INTENT: { success: true },
    });
    expect(mod.state.candidates).toEqual([]);
    expect(mod.state.fallbackIntent).toEqual({
      id: 'custom',
      label: '其他目的（自行输入）',
      description: '',
      confidence: 0,
    });
  });

  it('uses default prediction error message when response error is empty', async () => {
    await importModule({
      PREDICT_INTENT: { success: false },
    });
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('预测失败');
  });

  it('uses non-error object message in loadPredictions catch', async () => {
    await importModule({
      PREDICT_INTENT: () => {
        throw 'string error';
      },
    });
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('加载预测失败：string error');
  });

  it('uses default generate error message when response error is empty', async () => {
    const { mod } = await importModule({
      GENERATE_DSL_FROM_INTENT: { success: false },
    });
    mod.selectCandidate(sampleCandidate);
    await mod.generateDSL();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('生成失败');
  });

  it('handles non-error thrown object during generateDSL', async () => {
    const { mod } = await importModule({
      GENERATE_DSL_FROM_INTENT: () => {
        throw 'dsl string crash';
      },
    });
    mod.selectCandidate(sampleCandidate);
    await mod.generateDSL();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('生成 DSL 失败：dsl string crash');
  });

  it('handles rule and yaml fallback in generateDSL response', async () => {
    const { mod, sendMessage } = await importModule({
      GENERATE_DSL_FROM_INTENT: { success: true },
    });
    mod.selectCandidate(sampleCandidate);
    await mod.generateDSL();
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'GENERATE_DSL_FROM_INTENT',
      payload: { intent: sampleCandidate },
    });
    expect(mod.state.rule).toBeNull();
    expect(mod.state.yaml).toBe('');
  });

  it('renders preview safely when step fields are missing', async () => {
    const { mod } = await importModule();
    mod.state.rule = { steps: [{}, { target: {} }] } as unknown as typeof sampleRule;
    mod.state.yaml = 'yaml';
    mod.renderPreview();
    const stepsList = document.getElementById('steps-list') as HTMLDivElement;
    expect(stepsList.querySelectorAll('.step-item')).toHaveLength(2);
  });

  it('handles non-error thrown object during saveRule', async () => {
    const { mod } = await importModule({
      UPLOAD_CONFIRMED_RULE: () => {
        throw 'upload string crash';
      },
    });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('保存规则失败：upload string crash');
  });

  it('uses default save error message when response error is empty', async () => {
    const { mod } = await importModule({
      UPLOAD_CONFIRMED_RULE: { success: false },
    });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toBe('保存失败');
  });

  it('renders save result when ruleId is missing in response', async () => {
    const { mod } = await importModule({
      UPLOAD_CONFIRMED_RULE: { success: true },
    });
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    await mod.saveRule();
    await flushPromises();
    const resultEl = document.getElementById('save-result') as HTMLParagraphElement;
    expect(resultEl.textContent).toBe('规则已保存，ID：');
  });

  it('renders confirm list safely when rule has no steps', async () => {
    const { mod } = await importModule();
    mod.state.rule = { ...sampleRule, steps: [] };
    mod.renderConfirmList();
    const container = document.getElementById('confirm-list') as HTMLDivElement;
    expect(container.innerHTML).toBe('');
  });

  it('does not update save button when element is missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    expect(() => mod.updateSaveButton()).not.toThrow();
  });

  it('does not update confirm-all checkbox when element is missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    expect(() => mod.updateConfirmAllCheckbox()).not.toThrow();
  });

  it('updateReplayConfirmButton does not throw when element is missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    expect(() => mod.updateReplayConfirmButton()).not.toThrow();
  });

  it('does not set status when status element is missing', async () => {
    document.body.innerHTML = '<div id="candidates-list"></div>';
    const { mod } = await importModule();
    expect(() => mod.setStatus('message', 'info')).not.toThrow();
  });

  it('selects candidate when custom input is missing', async () => {
    document.body.innerHTML = '<div id="status"></div><div id="candidates-list"></div>';
    const { mod } = await importModule();
    expect(() => mod.selectCandidate(sampleCandidate)).not.toThrow();
    expect(mod.state.selectedIntent).toEqual(sampleCandidate);
  });

  it('updateNextButton does not throw when elements are missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    mod.state.selectedIntent = sampleCandidate;
    expect(() => mod.updateNextButton()).not.toThrow();
  });

  it('renderPreview does not throw when preview elements are missing', async () => {
    document.body.innerHTML = '<div id="status"></div>';
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.yaml = 'yaml';
    expect(() => mod.renderPreview()).not.toThrow();
  });

  it('startReplay resets loading on success (replay is async-monitored)', async () => {
    // M-3: after successful START_REPLAY, loading=false because the replay
    // is monitored via REPLAY_PROGRESS/REPLAY_COMPLETE messages, not via
    // the blocking loading flag.
    const { mod } = await importModule({ START_REPLAY: { success: true, taskId: 'task-1' } });
    mod.state.rule = sampleRule;
    await mod.startReplay();
    await flushPromises();
    expect(mod.state.loading).toBe(false);
    expect(mod.state.replayStatus).toBe('running');
    expect(mod.state.step).toBe('replay');
  });

  it('creates a durable replay attempt before starting browser replay', async () => {
    const { mod, sendMessage } = await importModule({
      START_DSL_REPLAY: {
        success: true,
        replay: { id: 'replay-durable-1', workflowId: 'workflow-1', sequence: 1, status: 'running', outputValid: false },
      },
      START_REPLAY: { success: true, taskId: 'browser-replay-1' },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.rule = sampleRule;
    mod.populateRequirementEditor(noInputRequirement);
    sendMessage.mockClear();
    await mod.startReplay();
    expect(sendMessage.mock.calls.map(([message]) => message.action)).toEqual(['START_DSL_REPLAY', 'START_REPLAY']);
    expect(mod.state.replayAttemptId).toBe('replay-durable-1');
    expect(mod.state.replayStatus).toBe('running');
  });

  it('submits replay diagnostics and enables confirmation only after server validation', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: true,
        workflow: {
          id: 'workflow-1', requirementId: 'requirement-1', recordingId: 'recording-1',
          browserProfileId: 'current-chrome-profile', status: 'awaiting_confirmation',
          repairCount: 0, maxRepairs: 3, provisionalRule: sampleRule,
        },
        replay: { id: 'replay-1', workflowId: 'workflow-1', sequence: 1, status: 'succeeded', outputValid: true },
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayAttemptId = 'replay-1';
    mod.state.rule = sampleRule;
    mod.showStep('replay');
    mod.handleReplayProgress({ type: 'result', payload: { title: 'Product', price: 100 } });
    mod.handleReplayProgress({ type: 'log', level: 'info', message: 'complete' });
    sendMessage.mockClear();
    mod.handleReplayComplete({ status: 'success' });
    await flushPromises();
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({
      action: 'COMPLETE_DSL_REPLAY',
      payload: expect.objectContaining({
        workflowId: 'workflow-1', replayId: 'replay-1', succeeded: true,
        output: [{ title: 'Product', price: 100 }],
      }),
    }));
    expect(mod.state.replayStatus).toBe('success');
    expect((document.getElementById('action-primary') as HTMLButtonElement).disabled).toBe(false);
  });

  it('submits every replay result row in mixed object and batch order', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: true,
        workflow: {
          id: 'workflow-rows', requirementId: 'requirement-rows', recordingId: 'recording-rows',
          browserProfileId: 'current-chrome-profile', status: 'awaiting_confirmation',
          repairCount: 0, maxRepairs: 0, provisionalRule: sampleRule,
        },
        replay: {
          id: 'replay-rows', workflowId: 'workflow-rows', sequence: 1,
          status: 'succeeded', outputValid: true,
        },
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-rows';
    mod.state.replayAttemptId = 'replay-rows';
    mod.state.rule = sampleRule;
    mod.handleReplayProgress({ type: 'result', payload: { order: 1 } });
    mod.handleReplayProgress({ type: 'result', payload: [{ order: 2 }, { order: 3 }] });
    mod.handleReplayProgress({ type: 'result', payload: { order: 4 } });
    sendMessage.mockClear();

    mod.handleReplayComplete({ status: 'success' });
    await flushPromises();
    await flushPromises();

    expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({
      action: 'COMPLETE_DSL_REPLAY',
      payload: expect.objectContaining({
        workflowId: 'workflow-rows',
        replayId: 'replay-rows',
        output: [{ order: 1 }, { order: 2 }, { order: 3 }, { order: 4 }],
      }),
    }));
    expect(mod.state.replayExtracted).toEqual({ order: 4 });
  });

  it('bounds verbose replay diagnostics while preserving the terminal failure', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: true,
        workflow: {
          id: 'workflow-1', requirementId: 'requirement-1', recordingId: 'recording-1',
          browserProfileId: 'current-chrome-profile', status: 'failed',
          repairCount: 3, maxRepairs: 3, errorMessage: 'repair exhausted',
        },
        replay: { id: 'replay-1', workflowId: 'workflow-1', status: 'failed', outputValid: false },
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayAttemptId = 'replay-1';
    mod.state.rule = sampleRule;
    mod.state.replayLogs = Array.from({ length: 150 }, (_, index) => ({
      type: 'log' as const,
      level: index === 149 ? 'error' : 'info',
      message: index === 149
        ? `Step failed: waitForNetworkIdle ${'x'.repeat(3000)}`
        : `verbose log ${index} ${'x'.repeat(3000)}`,
      extra: index === 149
        ? {
            error: 'Unsupported action: waitForNetworkIdle',
            type: 'ScriptError',
            page: 'sensitive-page-data'.repeat(20000),
            token: 'secret-token',
          }
        : undefined,
    }));
    sendMessage.mockClear();

    mod.handleReplayComplete({ status: 'failure', message: 'Unsupported action: waitForNetworkIdle' });
    await flushPromises();
    await flushPromises();

    const completion = sendMessage.mock.calls
      .map(([message]) => message)
      .find((message) => message.action === 'COMPLETE_DSL_REPLAY');
    expect(completion).toBeDefined();
    const diagnostics = (completion?.payload as { diagnostics: Record<string, unknown> }).diagnostics;
    expect(new TextEncoder().encode(JSON.stringify(diagnostics)).byteLength).toBeLessThan(48 * 1024);
    expect(diagnostics.truncated).toBe(true);
    expect(JSON.stringify(diagnostics)).toContain('Step failed: waitForNetworkIdle');
    expect(JSON.stringify(diagnostics)).not.toContain('sensitive-page-data');
    expect(JSON.stringify(diagnostics)).not.toContain('secret-token');
  });

  it('bounds replay artifacts and never submits raw HTML snapshots', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: true,
        workflow: {
          id: 'workflow-1', requirementId: 'requirement-1', recordingId: 'recording-1',
          browserProfileId: 'current-chrome-profile', status: 'failed',
          repairCount: 3, maxRepairs: 3, errorMessage: 'repair exhausted',
        },
        replay: { id: 'replay-1', workflowId: 'workflow-1', status: 'failed', outputValid: false },
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayAttemptId = 'replay-1';
    mod.state.rule = sampleRule;
    const oversizedSemanticArtifact = JSON.stringify({
      format: 'semantic-dom-v1',
      domTree: { text: 'x'.repeat(600 * 1024) },
    });
    mod.state.replayLogs = [
      ...Array.from({ length: 7 }, (_, index) => ({
        type: 'snapshot' as const,
        snapshot: { name: `large-${index}`, type: 'dom' as const, data: oversizedSemanticArtifact },
      })),
      {
        type: 'snapshot' as const,
        snapshot: { name: 'raw', type: 'html' as const, data: '<input value="browser-secret">' },
      },
    ];
    sendMessage.mockClear();

    mod.handleReplayComplete({ status: 'failure', message: 'selector timeout' });
    await flushPromises();
    await flushPromises();

    const completion = sendMessage.mock.calls
      .map(([message]) => message)
      .find((message) => message.action === 'COMPLETE_DSL_REPLAY');
    expect(completion).toBeDefined();
    const artifacts = (completion?.payload as { artifacts: Array<{ data: string }> }).artifacts;
    expect(artifacts.length).toBeLessThanOrEqual(4);
    expect(new TextEncoder().encode(JSON.stringify(artifacts)).byteLength).toBeLessThan(512 * 1024);
    expect(JSON.stringify(artifacts)).not.toContain('browser-secret');
    expect(JSON.stringify(artifacts)).not.toContain('x'.repeat(1000));
    expect(artifacts.every((artifact) => JSON.parse(artifact.data).omitted === true)).toBe(true);
  });

  it('automatically replays a repaired provisional rule', async () => {
    const repairedRule = { ...sampleRule, steps: [{ action: 'click', target: { selector: '.repaired' } }] };
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: {
        success: true,
        workflow: {
          id: 'workflow-1', requirementId: 'requirement-1', recordingId: 'recording-1',
          browserProfileId: 'current-chrome-profile', status: 'awaiting_replay',
          repairCount: 1, maxRepairs: 3, provisionalRule: repairedRule, provisionalYaml: 'repaired: true',
        },
        repairJob: { id: 'repair-1', workflowId: 'workflow-1', kind: 'repair', status: 'completed' },
      },
      START_DSL_REPLAY: {
        success: true,
        replay: { id: 'replay-2', workflowId: 'workflow-1', sequence: 2, status: 'running', outputValid: false },
      },
      START_REPLAY: { success: true, taskId: 'browser-replay-2' },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayAttemptId = 'replay-1';
    mod.state.rule = sampleRule;
    mod.populateRequirementEditor(noInputRequirement);
    sendMessage.mockClear();
    mod.handleReplayComplete({ status: 'failure', message: 'not found' });
    await flushPromises();
    await flushPromises();
    await flushPromises();
    expect(mod.state.rule).toEqual(repairedRule);
    expect(mod.state.replayAttemptId).toBe('replay-2');
    expect(sendMessage.mock.calls.map(([message]) => message.action)).toEqual([
      'COMPLETE_DSL_REPLAY', 'START_DSL_REPLAY', 'START_REPLAY',
    ]);
  });

  it('confirms a successful durable workflow as an immutable version', async () => {
    const { mod } = await importModule({
      CONFIRM_DSL_WORKFLOW: { success: true, ruleVersion: { ruleId: 'ext-test-1', version: 2 } },
    }, { baseUrl: 'http://localhost:8080' });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayStatus = 'success';
    await mod.confirmWorkflowRule();
    expect(mod.state.ruleId).toBe('ext-test-1');
    expect(mod.state.step).toBe('save');
    expect(document.getElementById('save-result')?.textContent).toContain('v2');
  });

  it('resumes a persisted durable workflow at provisional preview', async () => {
    const { mod, sendMessage } = await importModule({
      RESUME_DSL_WORKFLOW: {
        success: true,
        active: true,
        requirement: sampleRequirement,
        workflow: {
          id: 'workflow-resume', requirementId: 'requirement-resume', recordingId: 'recording-resume',
          browserProfileId: 'current-chrome-profile', status: 'awaiting_replay',
          repairCount: 1, maxRepairs: 3, provisionalRule: sampleRule, provisionalYaml: 'id: ext-test-1',
        },
      },
      START_DSL_REPLAY: {
        success: true,
        replay: { id: 'replay-resume', workflowId: 'workflow-resume', sequence: 1, status: 'running' },
      },
      START_REPLAY: { success: true, taskId: 'browser-replay-resume' },
    }, undefined, { oc_dsl_workflow: { workflowId: 'workflow-resume' } });
    expect(sendMessage).toHaveBeenCalledWith({ action: 'RESUME_DSL_WORKFLOW' });
    expect(mod.state.dslWorkflowId).toBe('workflow-resume');
    expect(mod.state.step).toBe('preview');
    expect(mod.state.rule).toEqual(sampleRule);
    expect(mod.state.normalizedRequirement).toEqual(sampleRequirement);
    const keyword = document.querySelector<HTMLInputElement>('[data-replay-input-name="keyword"]');
    expect(keyword).not.toBeNull();
    keyword!.value = 'restored keyword';
    sendMessage.mockClear();
    await mod.startReplay();
    expect(sendMessage).toHaveBeenLastCalledWith({
      action: 'START_REPLAY',
      payload: { rule: sampleRule, variables: { keyword: 'restored keyword' } },
    });
  });

  it('resumes every durable workflow terminal state and handles unavailable sessions', async () => {
    const baseWorkflow = {
      id: 'workflow-resume', requirementId: 'requirement-resume', recordingId: 'recording-resume',
      browserProfileId: 'current-chrome-profile', status: 'replaying',
      repairCount: 1, maxRepairs: 3, provisionalRule: sampleRule,
    };
    let resumeResponse: unknown = {
      success: true, active: true, replayId: 'replay-resume',
      requirement: sampleRequirement, workflow: baseWorkflow,
    };
    const { mod } = await importModule({
      RESUME_DSL_WORKFLOW: () => {
        if (resumeResponse === 'throw-string') throw 'resume unavailable';
        if (resumeResponse instanceof Error) throw resumeResponse;
        return resumeResponse;
      },
    }, undefined, { oc_dsl_workflow: { workflowId: 'workflow-resume' } });
    expect(mod.state.replayStatus).toBe('running');
    expect(mod.state.replayAttemptId).toBe('replay-resume');

    resumeResponse = {
      success: true, active: true,
      requirement: sampleRequirement,
      workflow: { ...baseWorkflow, status: 'awaiting_confirmation', currentJobId: 'job-1' },
    };
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(true);
    expect(mod.state.replayStatus).toBe('success');

    resumeResponse = {
      success: true, active: true,
      workflow: { ...baseWorkflow, status: 'approved', approvedRuleId: undefined, approvedVersion: undefined },
    };
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(true);
    expect(mod.state.step).toBe('save');
    expect(document.getElementById('save-result')?.textContent).toContain('规则版本已批准');
    document.getElementById('save-result')?.remove();
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(true);

    resumeResponse = {
      success: false, active: true, error: 'DSL 工作流失败',
      workflow: { ...baseWorkflow, status: 'failed', provisionalRule: undefined, errorMessage: undefined },
    };
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(true);
    expect(mod.state.replayError).toBe('DSL 工作流失败');

    resumeResponse = { success: true, active: true, workflow: { ...baseWorkflow, status: 'generating' } };
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(false);
    resumeResponse = { success: true, active: false };
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(false);
    resumeResponse = 'throw-string';
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(true);
    expect(document.getElementById('status')?.textContent).toContain('resume unavailable');
    resumeResponse = new Error('resume failed');
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(true);
    expect(document.getElementById('status')?.textContent).toContain('resume failed');
  });

  it('fails closed when a replay-capable resumed workflow omits its confirmed requirement', async () => {
    const { mod, sendMessage } = await importModule({
      RESUME_DSL_WORKFLOW: {
        success: true,
        active: true,
        workflow: {
          id: 'workflow-missing-requirement',
          requirementId: 'requirement-missing',
          recordingId: 'recording-resume',
          browserProfileId: 'current-chrome-profile',
          status: 'awaiting_replay',
          repairCount: 0,
          maxRepairs: 0,
          provisionalRule: sampleRule,
        },
      },
    }, undefined, { oc_dsl_workflow: { workflowId: 'workflow-missing-requirement' } });
    await flushPromises();
    expect(mod.state.step).toBe('intent');
    expect(document.getElementById('status')?.textContent).toContain('缺少有效的已确认采集需求');
    expect(document.querySelector('[data-replay-input-name]')).toBeNull();
    expect(sendMessage).not.toHaveBeenCalledWith(expect.objectContaining({ action: 'START_DSL_REPLAY' }));
  });

  it.each([
    [
      'unsupported input type',
      {
        ...sampleRequirement,
        requiredInputs: [{ name: 'keyword', type: 'bogus', description: 'Search keyword' }],
      },
    ],
    [
      'constraint-invalid default',
      {
        ...sampleRequirement,
        requiredInputs: [],
        optionalInputs: [{
          name: 'keyword', type: 'string', description: 'Search keyword',
          default: 'x', constraints: { minLength: 2 },
        }],
      },
    ],
    [
      'unsafe content',
      {
        ...sampleRequirement,
        description: 'Collect a user password.',
      },
    ],
  ])('rejects a malformed resumed requirement before creating a replay: %s', async (_name, malformedRequirement) => {
    const { mod, sendMessage } = await importModule({
      RESUME_DSL_WORKFLOW: {
        success: true,
        active: true,
        requirement: malformedRequirement,
        workflow: {
          id: 'workflow-malformed-requirement',
          requirementId: 'requirement-malformed',
          recordingId: 'recording-resume',
          browserProfileId: 'current-chrome-profile',
          status: 'awaiting_replay',
          repairCount: 0,
          maxRepairs: 0,
          provisionalRule: sampleRule,
        },
      },
    }, undefined, { oc_dsl_workflow: { workflowId: 'workflow-malformed-requirement' } });
    await flushPromises();
    expect(mod.state.step).toBe('intent');
    expect(mod.state.normalizedRequirement).toBeNull();
    expect(document.getElementById('status')?.textContent).toContain('缺少有效的已确认采集需求');
    expect(sendMessage).not.toHaveBeenCalledWith(expect.objectContaining({ action: 'START_DSL_REPLAY' }));
  });

  it('does not start a workflow replay without a hydrated confirmed requirement', async () => {
    const { mod, sendMessage } = await importModule();
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-without-requirement';
    mod.state.rule = sampleRule;
    mod.state.normalizedRequirement = null;
    sendMessage.mockClear();
    await mod.startReplay();
    expect(sendMessage).not.toHaveBeenCalled();
    expect(mod.state.step).toBe('preview');
    expect(document.getElementById('status')?.textContent).toContain('缺少已确认采集需求');
  });

  it('returns false when no durable workflow session is stored', async () => {
    const { mod } = await importModule();
    await expect(mod.resumeDSLWorkflow()).resolves.toBe(false);
  });

  it('resumeDSLWorkflow restores polling when background returns a running candidateJob', async () => {
    // No oc_dsl_workflow in session storage — wizard would historically bail.
    const { mod, sendMessage } = await importModule({
      RESUME_DSL_WORKFLOW: {
        success: true,
        active: false,
        candidateJob: { id: 'job-1', status: 'running', chunkCount: 4, completedChunks: 2 },
      },
      GET_REQUIREMENT_WORKFLOW: { success: true, workflowV2: true, job: { id: 'job-1', status: 'running' } },
    });

    // Module init already invoked resumeDSLWorkflow once (fire-and-forget),
    // so capture the call count before the explicit invocation.
    const isResumeCall = (call: unknown[]): boolean => {
      const msg = call[0] as { action?: string; payload?: { resumeJobId?: string } };
      return msg.action === 'GET_REQUIREMENT_WORKFLOW' && msg.payload?.resumeJobId === 'job-1';
    };
    const callsBefore = sendMessage.mock.calls.filter(isResumeCall).length;

    const result = await mod.resumeDSLWorkflow();
    await flushPromises();

    expect(result).toBe(true);
    expect(mod.state.workflowV2).toBe(true);
    expect(mod.state.candidatesRequested).toBe(true);
    expect(mod.state.requirementJobId).toBe('job-1');
    expect(mod.state.loading).toBe(true);
    // The polling-restart message was sent exactly once by this invocation.
    const resumeCalls = sendMessage.mock.calls.filter(isResumeCall);
    expect(resumeCalls).toHaveLength(callsBefore + 1);
  });

  it('resumeDSLWorkflow clears stale session keys when orphaned job already failed', async () => {
    const { mod, storageMock } = await importModule({
      RESUME_DSL_WORKFLOW: {
        success: true,
        active: false,
        candidateJob: { id: 'job-1', status: 'failed' },
      },
    });

    const result = await mod.resumeDSLWorkflow();

    expect(result).toBe(false);
    expect(storageMock.session.remove).toHaveBeenCalledWith('oc_requirement_candidate_job');
    expect(mod.state.candidatesRequested).toBe(false);
  });

  it('validates durable dsl generation inputs and surfaces generation failures', async () => {
    let createResponse: unknown = {
      success: false,
      workflow: { errorMessage: 'provider unavailable' },
    };
    const { mod } = await importModule({
      CREATE_DSL_WORKFLOW: () => {
        if (createResponse === 'throw-string') throw 'generation unavailable';
        if (createResponse instanceof Error) throw createResponse;
        return createResponse;
      },
    });

    await mod.generateWorkflowDSL();
    expect(document.getElementById('status')?.textContent).toBe('缺少已确认的采集需求');
    mod.state.requirementId = 'requirement-1';
    const profile = document.getElementById('browser-profile-id') as HTMLInputElement;
    profile.value = '   ';
    await mod.generateWorkflowDSL();
    expect(document.getElementById('status')?.textContent).toBe('请输入浏览器配置引用');

    profile.value = 'profile-1';
    await mod.generateWorkflowDSL();
    expect(mod.state.error).toBe('provider unavailable');
    createResponse = { success: false, error: 'explicit generation failure' };
    await mod.generateWorkflowDSL();
    expect(mod.state.error).toBe('explicit generation failure');
    createResponse = { success: true };
    await mod.generateWorkflowDSL();
    expect(mod.state.error).toBe('临时 DSL 生成失败');
    createResponse = 'throw-string';
    await mod.generateWorkflowDSL();
    expect(mod.state.error).toBe('generation unavailable');
    createResponse = new Error('generation failed');
    await mod.generateWorkflowDSL();
    expect(mod.state.error).toBe('generation failed');
    expect(mod.state.loading).toBe(false);
  });

  it('submits array output and artifacts, then reports exhausted replay validation', async () => {
    let completionResponse: unknown = {
      success: true,
      workflow: {
        id: 'workflow-1', requirementId: 'requirement-1', recordingId: 'recording-1',
        browserProfileId: 'profile-1', status: 'failed', repairCount: 3, maxRepairs: 3,
      },
      replay: { id: 'replay-1', workflowId: 'workflow-1', status: 'failed', errorMessage: 'invalid output' },
    };
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: () => {
        if (completionResponse === 'throw-string') throw 'completion unavailable';
        return completionResponse;
      },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayAttemptId = 'replay-1';
    mod.state.rule = sampleRule;
    const semanticArtifact = JSON.stringify({
      format: 'semantic-dom-v1',
      originalType: 'html',
      domTree: { type: 'element', tagName: 'main', attributes: [] },
      capture: { nodeCount: 1, redactionCount: 0, removedNodeCount: 0, truncated: false },
    });
    mod.handleReplayProgress({ type: 'result', payload: [{ name: 'one' }, { name: 'two' }] });
    mod.handleReplayProgress({ type: 'snapshot', snapshot: { name: 'failure', type: 'dom', data: semanticArtifact } });
    mod.handleReplayProgress({ type: 'status', status: 'running', message: 'collecting' });
    mod.handleReplayComplete({ status: 'cancelled', error: { code: 'cancelled' } });
    await flushPromises();
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({
      action: 'COMPLETE_DSL_REPLAY',
      payload: expect.objectContaining({
        output: [{ name: 'one' }, { name: 'two' }],
        artifacts: [{ name: 'failure', type: 'dom', data: semanticArtifact }],
        errorCode: 'REPLAY_CANCELLED',
      }),
    }));
    expect(mod.state.replayError).toBe('invalid output');

    completionResponse = 'throw-string';
    mod.handleReplayComplete({ status: 'failure', error: 'replay transport failed' });
    await flushPromises();
    await flushPromises();
    expect(mod.state.replayError).toBe('completion unavailable');

    completionResponse = { success: false, error: 'completion rejected' };
    mod.handleReplayComplete({ status: 'failure' });
    await flushPromises();
    expect(mod.state.replayError).toBe('completion rejected');
    completionResponse = { success: false };
    mod.handleReplayComplete({ status: 'failure' });
    await flushPromises();
    expect(mod.state.replayError).toBe('服务端未能处理回放结果');
  });

  it('handles durable replay start failures and reports browser launch rejection', async () => {
    let replayResponse: unknown = { success: false };
    const { mod, sendMessage } = await importModule({
      START_DSL_REPLAY: () => {
        if (replayResponse === 'throw-string') throw 'replay unavailable';
        return replayResponse;
      },
      START_REPLAY: { success: false },
      COMPLETE_DSL_REPLAY: {
        success: true,
        workflow: {
          id: 'workflow-1', requirementId: 'requirement-1', recordingId: 'recording-1',
          browserProfileId: 'profile-1', status: 'failed', repairCount: 3, maxRepairs: 3,
        },
      },
    });
    await mod.startReplay();
    expect(document.getElementById('status')?.textContent).toBe('没有可用规则');

    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.rule = sampleRule;
    mod.populateRequirementEditor(noInputRequirement);
    await mod.startReplay();
    expect(mod.state.replayError).toBe('无法创建服务端回放尝试');

    replayResponse = {
      success: true,
      replay: { id: 'replay-started', workflowId: 'workflow-1', sequence: 1, status: 'running' },
    };
    sendMessage.mockClear();
    await mod.startReplay();
    await flushPromises();
    await flushPromises();
    expect(mod.state.replayError).toBe('回放修复次数已用尽');
    expect(sendMessage.mock.calls.map(([message]) => message.action)).toEqual([
      'START_DSL_REPLAY', 'START_REPLAY', 'COMPLETE_DSL_REPLAY',
    ]);
    expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({
      action: 'COMPLETE_DSL_REPLAY',
      payload: expect.objectContaining({ errorMessage: '启动回放失败' }),
    }));

    replayResponse = 'throw-string';
    await mod.startReplay();
    await flushPromises();
    expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({
      action: 'COMPLETE_DSL_REPLAY',
      payload: expect.objectContaining({ errorMessage: 'replay unavailable' }),
    }));
  });

  it('guards durable confirmation and surfaces approval failures', async () => {
    let confirmResponse: unknown = { success: false };
    const { mod } = await importModule({
      CONFIRM_DSL_WORKFLOW: () => {
        if (confirmResponse === 'throw-string') throw 'approval unavailable';
        return confirmResponse;
      },
    });
    await mod.confirmWorkflowRule();
    expect(document.getElementById('status')?.textContent).toContain('只有完整回放');

    mod.state.dslWorkflowId = 'workflow-1';
    mod.state.replayStatus = 'success';
    await mod.confirmWorkflowRule();
    expect(mod.state.error).toBe('规则版本确认失败');
    confirmResponse = 'throw-string';
    await mod.confirmWorkflowRule();
    expect(mod.state.error).toBe('approval unavailable');
    confirmResponse = { success: true, ruleVersion: { ruleId: 'approved-rule', version: 1 } };
    await mod.confirmWorkflowRule();
    expect(mod.state.ruleId).toBe('approved-rule');
    expect(document.getElementById('admin-link')?.getAttribute('href')).toBe('#');
  });

  it('confirmWorkflowRule surfaces SAFETY_BLOCKING_FLAG without retrying', async () => {
    // In production, `blocking_flags` flows from the server 409 body through
    // requirementRequest → ServerRequestError → the CONFIRM_DSL_WORKFLOW
    // handler's return shape. This test bypasses that layer and mocks the
    // handler response directly.
    const { mod, sendMessage } = await importModule({
      CONFIRM_DSL_WORKFLOW: {
        success: false,
        code: 'SAFETY_BLOCKING_FLAG',
        error: 'dsl workflow has blocking safety flags requiring override',
        blocking_flags: ['external-resource-load'],
      },
    });
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayStatus = 'success';

    await mod.confirmWorkflowRule();

    // replayStatus stays 'success' — no flip to 'failure'.
    expect(mod.state.replayStatus).toBe('success');
    // Blocking flag name surfaced to the operator.
    expect(mod.state.error).toContain('external-resource-load');
    // No retry — CONFIRM_DSL_WORKFLOW called exactly once.
    const confirmCalls = sendMessage.mock.calls.filter(
      (call: unknown[]) => (call[0] as { action: string }).action === 'CONFIRM_DSL_WORKFLOW',
    );
    expect(confirmCalls).toHaveLength(1);
  });

  it('startReplay does not read session storage', async () => {
    const { mod, storageMock } = await importModule({ START_REPLAY: { success: true, taskId: 'task-1' } });
    storageMock.session.get.mockClear();
    mod.state.rule = sampleRule;
    await mod.startReplay();
    await flushPromises();
    expect(storageMock.session.get).not.toHaveBeenCalled();
  });

  it('handles confirmAll when rule has no steps', async () => {
    const { mod } = await importModule();
    mod.state.rule = { ...sampleRule, steps: [] };
    mod.state.confirmedSteps = new Set([0]);
    mod.confirmAll(false);
    expect(mod.state.confirmedSteps.size).toBe(0);
  });

  it('updateConfirmAllCheckbox handles zero total steps', async () => {
    const { mod } = await importModule();
    mod.state.rule = { ...sampleRule, steps: [] };
    mod.state.confirmedSteps = new Set();
    mod.updateConfirmAllCheckbox();
    const confirmAllInput = document.getElementById('confirm-all') as HTMLInputElement;
    expect(confirmAllInput.checked).toBe(false);
    expect(confirmAllInput.indeterminate).toBe(false);
  });

  it('updateSaveButton disables save when total steps is zero', async () => {
    const { mod } = await importModule();
    mod.state.rule = { ...sampleRule, steps: [] };
    mod.updateSaveButton();
    const saveBtn = document.getElementById('action-primary') as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(true);
  });

  it('confirmAll skips checkboxes with non-numeric data-index', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.renderConfirmList();
    const container = document.getElementById('confirm-list') as HTMLDivElement;
    const invalidCheckbox = document.createElement('input');
    invalidCheckbox.type = 'checkbox';
    invalidCheckbox.dataset.index = 'abc';
    container.appendChild(invalidCheckbox);
    mod.confirmAll(true);
    expect(mod.state.confirmedSteps.size).toBe(2);
  });

  it('handles confirmAll when no confirm checkboxes exist', async () => {
    const { mod } = await importModule();
    mod.state.rule = sampleRule;
    mod.state.confirmedSteps = new Set([0, 1]);
    mod.confirmAll(false);
    expect(mod.state.confirmedSteps.size).toBe(0);
  });

  describe('renderActions', () => {
    it('intent step: hides tertiary/secondary and shows disabled primary when no selection', async () => {
      const { mod } = await importModule();
      mod.showStep('intent');
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      const secondary = document.getElementById('action-secondary') as HTMLButtonElement;
      const tertiary = document.getElementById('action-tertiary') as HTMLButtonElement;
      expect(primary.classList.contains('hidden')).toBe(false);
      expect(primary.textContent).toBe('下一步');
      expect(primary.disabled).toBe(true);
      expect(secondary.classList.contains('hidden')).toBe(true);
      expect(tertiary.classList.contains('hidden')).toBe(true);
    });

    it('intent step: enables primary when a candidate is selected', async () => {
      const { mod } = await importModule();
      mod.showStep('intent');
      mod.selectCandidate(sampleCandidate);
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      expect(primary.disabled).toBe(false);
    });

    it('preview step: shows secondary back and primary replay-confirm', async () => {
      const { mod } = await importModule();
      mod.showStep('preview');
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      const secondary = document.getElementById('action-secondary') as HTMLButtonElement;
      const tertiary = document.getElementById('action-tertiary') as HTMLButtonElement;
      expect(primary.textContent).toBe('下一步：回放确认');
      expect(primary.classList.contains('hidden')).toBe(false);
      expect(secondary.textContent).toBe('返回');
      expect(secondary.classList.contains('hidden')).toBe(false);
      expect(tertiary.classList.contains('hidden')).toBe(true);
    });

    it('replay step: shows abort, back and disabled confirm by default', async () => {
      const { mod } = await importModule();
      mod.showStep('replay');
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      const secondary = document.getElementById('action-secondary') as HTMLButtonElement;
      const tertiary = document.getElementById('action-tertiary') as HTMLButtonElement;
      expect(tertiary.textContent).toBe('取消回放');
      expect(secondary.textContent).toBe('返回预览');
      expect(primary.textContent).toBe('确认无误，继续');
      expect(primary.disabled).toBe(true);
    });

    it('replay step: enables primary when replay succeeds', async () => {
      const { mod } = await importModule();
      mod.showStep('replay');
      mod.handleReplayComplete({ status: 'success' });
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      expect(primary.disabled).toBe(false);
    });

    it('confirm step: shows secondary back and disabled save until all steps confirmed', async () => {
      const { mod } = await importModule();
      mod.state.rule = sampleRule;
      mod.renderConfirmList();
      mod.showStep('confirm');
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      const secondary = document.getElementById('action-secondary') as HTMLButtonElement;
      const tertiary = document.getElementById('action-tertiary') as HTMLButtonElement;
      expect(secondary.textContent).toBe('返回');
      expect(primary.textContent).toBe('确认无误并保存');
      expect(primary.disabled).toBe(true);
      expect(tertiary.classList.contains('hidden')).toBe(true);
    });

    it('confirm step: enables save when all steps confirmed', async () => {
      const { mod } = await importModule();
      mod.state.rule = sampleRule;
      mod.renderConfirmList();
      mod.showStep('confirm');
      mod.confirmAll(true);
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      expect(primary.disabled).toBe(false);
    });

    it('save step: shows only close button', async () => {
      const { mod } = await importModule();
      mod.showStep('save');
      const primary = document.getElementById('action-primary') as HTMLButtonElement;
      const secondary = document.getElementById('action-secondary') as HTMLButtonElement;
      const tertiary = document.getElementById('action-tertiary') as HTMLButtonElement;
      expect(primary.textContent).toBe('关闭');
      expect(secondary.classList.contains('hidden')).toBe(true);
      expect(tertiary.classList.contains('hidden')).toBe(true);
    });

    it('does not throw when action buttons are missing', async () => {
      document.body.innerHTML = '<div id="status"></div>';
      const { mod } = await importModule();
      expect(() => mod.showStep('intent')).not.toThrow();
      expect(() => mod.showStep('preview')).not.toThrow();
      expect(() => mod.showStep('replay')).not.toThrow();
      expect(() => mod.showStep('confirm')).not.toThrow();
      expect(() => mod.showStep('save')).not.toThrow();
    });
  });

  it('shows error when extension runtime is unavailable after reload', async () => {
    vi.resetModules();
    delete (globalThis as Record<string, unknown>).chrome;
    sessionStorage.setItem('oc-intent-runtime-restored', '1');
    await import('./intent-page');
    await flushPromises();
    const status = document.getElementById('status') as HTMLDivElement;
    expect(status.textContent).toContain('扩展运行时尚未初始化');
  });

  it('prepares runtime restore when extension runtime is unavailable on first load', async () => {
    vi.resetModules();
    delete (globalThis as Record<string, unknown>).chrome;
    sessionStorage.removeItem('oc-intent-runtime-restored');
    const originalLocation = window.location;
    const replaceMock = vi.fn();
    (window as any).location = { ...originalLocation, replace: replaceMock };
    await import('./intent-page');
    await flushPromises();
    expect(sessionStorage.getItem('oc-intent-runtime-restored')).toBe('1');
    expect(replaceMock).toHaveBeenCalled();
    (window as any).location = originalLocation;
  });

  test('withInFlight prevents same-op re-entry within the same tick', async () => {
    const { mod } = await importModule();
    let calls = 0;
    let resolveInner: () => void = () => {};
    const innerPromise = new Promise<void>((resolve) => { resolveInner = resolve; });
    // First call enters the guard and hangs on innerPromise.
    const p1 = mod.withInFlight('start-replay', async () => { calls += 1; await innerPromise; });
    // Second and third calls re-enter synchronously while the first is pending.
    const p2 = mod.withInFlight('start-replay', async () => { calls += 1; });
    const p3 = mod.withInFlight('start-replay', async () => { calls += 1; });
    expect(calls).toBe(1);
    expect(mod.state.inFlight.has('start-replay')).toBe(true);
    resolveInner();
    await Promise.all([p1, p2, p3]);
    expect(calls).toBe(1);
    expect(mod.state.inFlight.has('start-replay')).toBe(false);
  });

  test('withInFlight allows different ops in parallel', async () => {
    const { mod } = await importModule();
    let a = 0;
    let b = 0;
    let resolveA: () => void = () => {};
    let resolveB: () => void = () => {};
    const innerA = new Promise<void>((resolve) => { resolveA = resolve; });
    const innerB = new Promise<void>((resolve) => { resolveB = resolve; });
    const pA = mod.withInFlight('start-replay', async () => { a += 1; await innerA; });
    const pB = mod.withInFlight('complete-replay', async () => { b += 1; await innerB; });
    expect(a).toBe(1);
    expect(b).toBe(1);
    resolveA();
    resolveB();
    await Promise.all([pA, pB]);
  });

  it('readRequirementEditor rejects unsafe terms on fresh submit (W-9)', async () => {
    const { mod } = await importModule();
    mod.populateRequirementEditor(sampleRequirement);
    const description = document.getElementById('requirement-description') as HTMLTextAreaElement;
    description.value = 'Needs password to log in';
    description.dispatchEvent(new Event('input'));
    expect(() => mod.readRequirementEditor()).toThrow('敏感字段');
  });

  it('prepareRequirement rejects unsafe customText before dispatch', async () => {
    const { mod, sendMessage } = await importModule();
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = 'Use the saved password to authenticate users';
    await mod.prepareRequirement();
    expect(document.getElementById('status')?.textContent).toContain('敏感内容');
    expect(sendMessage).not.toHaveBeenCalledWith(expect.objectContaining({ action: 'NORMALIZE_REQUIREMENT' }));
  });

  it('prepareRequirement redacts sensitive patterns in customText before NORMALIZE_REQUIREMENT', async () => {
    const { mod, sendMessage } = await importModule({
      NORMALIZE_REQUIREMENT: {
        success: true,
        workflowV2: true,
        requirement: sampleRequirement,
        requirementId: 'requirement-redacted',
      },
    });
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    textarea.value = 'Reach out to user@example.com for questions';
    await mod.prepareRequirement();
    const normalizeCalls = (sendMessage as Mock).mock.calls.filter(
      (call: unknown[]) => (call[0] as { action: string }).action === 'NORMALIZE_REQUIREMENT',
    );
    expect(normalizeCalls).toHaveLength(1);
    const dispatchedText = (normalizeCalls[0][0] as { payload: { customText: string } }).payload.customText;
    expect(dispatchedText).not.toContain('user@example.com');
    expect(dispatchedText).toContain('[REDACTED]');
    expect(document.getElementById('status')?.textContent).toContain('脱敏');
  });

  it('prepareRequirement rejects customText larger than 8 KiB', async () => {
    const { mod, sendMessage } = await importModule();
    const textarea = document.getElementById('custom-description') as HTMLTextAreaElement;
    // ASCII bytes map 1:1, so 8193 chars > 8192-byte cap.
    textarea.value = 'a'.repeat(8193);
    await mod.prepareRequirement();
    expect(document.getElementById('status')?.textContent).toContain('过长');
    expect(sendMessage).not.toHaveBeenCalledWith(expect.objectContaining({ action: 'NORMALIZE_REQUIREMENT' }));
  });

  test('startReplay called twice synchronously triggers START_DSL_REPLAY once', async () => {
    const { mod, sendMessage } = await importModule({
      START_DSL_REPLAY: {
        success: true,
        replay: { id: 'r-1', workflowId: 'wf-1', sequence: 1, status: 'running', outputValid: false },
      },
      START_REPLAY: { success: true, taskId: 'task-1' },
    });
    mod.state.rule = sampleRule;
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayStatus = 'idle';
    mod.populateRequirementEditor(noInputRequirement);
    (sendMessage as Mock).mockResolvedValue({ success: true, replay: { id: 'r-1' } });

    await Promise.all([mod.startReplay(), mod.startReplay()]);

    const startDslCalls = (sendMessage as Mock).mock.calls.filter(
      (call: unknown[]) => (call[0] as { action: string }).action === 'START_DSL_REPLAY',
    );
    expect(startDslCalls).toHaveLength(1);
  });

  it('replay diagnostics redact Bearer tokens in error fields', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: { success: true, workflow: { id: 'wf-1', status: 'awaiting_confirmation' } },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayAttemptId = 'r-1';
    mod.state.replayResults = [{ item: 'x' }];
    mod.state.replayLogs = [
      { type: 'log', level: 'error', message: 'step failed', extra: { error: 'password=abc123secret rejected' } },
    ];
    mod.state.replayStatus = 'running';

    await mod.completeWorkflowReplay({ status: 'failure' });

    const completeCall = (sendMessage as Mock).mock.calls.find(
      (call: unknown[]) => (call[0] as { action: string }).action === 'COMPLETE_DSL_REPLAY',
    ) as [{ action: string; payload?: { diagnostics?: unknown } }];
    const diagnostics = JSON.stringify(completeCall[0].payload?.diagnostics);
    expect(diagnostics).not.toContain('abc123secret');
    expect(diagnostics).toContain('[REDACTED]');
  });

  it('completeWorkflowReplay truncates output to 1000 rows with sentinel', async () => {
    const { mod, sendMessage } = await importModule({
      COMPLETE_DSL_REPLAY: { success: true, workflow: { id: 'wf-1', status: 'awaiting_confirmation' } },
    });
    mod.state.workflowV2 = true;
    mod.state.dslWorkflowId = 'wf-1';
    mod.state.replayAttemptId = 'r-1';
    mod.state.replayResults = Array.from({ length: 2000 }, (_, i) => ({ item: `row-${i}` }));
    mod.state.replayStatus = 'running';

    await mod.completeWorkflowReplay({ status: 'success' });

    const completeCall = (sendMessage as Mock).mock.calls.find(
      (call: unknown[]) => (call[0] as { action: string }).action === 'COMPLETE_DSL_REPLAY',
    ) as [{ action: string; payload?: { output?: unknown[] } }];
    const output = completeCall[0].payload?.output as unknown[];
    expect(output.length).toBeLessThanOrEqual(1001);
    const sentinel = output[output.length - 1] as { _truncated?: boolean; original_count?: number };
    expect(sentinel._truncated).toBe(true);
    expect(sentinel.original_count).toBe(2000);
  });

  describe('keepAlive reconnect', () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it('reconnects after port disconnect with exponential backoff', async () => {
      const onDisconnectListeners: Array<() => void> = [];
      const connectMock = vi.fn(() => ({
        name: 'intent-wizard',
        postMessage: vi.fn(),
        disconnect: vi.fn(),
        onMessage: { addListener: vi.fn(), removeListener: vi.fn() },
        onDisconnect: {
          addListener: vi.fn((cb: () => void) => onDisconnectListeners.push(cb)),
          removeListener: vi.fn(),
        },
      }));
      await importModuleWithConnect(connectMock);
      await vi.runOnlyPendingTimersAsync();
      expect(connectMock).toHaveBeenCalledTimes(1);

      // First reconnect: 100ms backoff.
      onDisconnectListeners[0]();
      await vi.advanceTimersByTimeAsync(150);
      expect(connectMock).toHaveBeenCalledTimes(2);

      // Second reconnect: 200ms backoff.
      onDisconnectListeners[1]();
      await vi.advanceTimersByTimeAsync(250);
      expect(connectMock).toHaveBeenCalledTimes(3);
    });

    it('surfaces warning after 3 failed reconnects in window', async () => {
      const onDisconnectListeners: Array<() => void> = [];
      // First connect succeeds; subsequent reconnect attempts throw, simulating
      // a runtime that keeps refusing new ports.
      const connectMock = vi.fn((): unknown => {
        if (connectMock.mock.calls.length >= 2) {
          throw new Error('runtime unavailable');
        }
        return {
          name: 'intent-wizard',
          postMessage: vi.fn(),
          disconnect: vi.fn(),
          onMessage: { addListener: vi.fn(), removeListener: vi.fn() },
          onDisconnect: {
            addListener: vi.fn((cb: () => void) => onDisconnectListeners.push(cb)),
            removeListener: vi.fn(),
          },
        };
      });
      await importModuleWithConnect(connectMock);
      await vi.runOnlyPendingTimersAsync();
      expect(connectMock).toHaveBeenCalledTimes(1);

      // Disconnect the initial port; reconnect attempts 1, 2, 3 all throw.
      onDisconnectListeners[0]();
      // Drain pending reconnect timers (exponential backoff: 100, 200, 400 ms).
      await vi.advanceTimersByTimeAsync(1000);

      const status = document.getElementById('status') as HTMLDivElement;
      expect(status.textContent).toContain('不稳定');
      expect(status.className).toBe('error');
    });

    it('reconnects when heartbeat times out (no pong within 5s)', async () => {
      const connectMock = vi.fn(() => ({
        name: 'intent-wizard',
        postMessage: vi.fn(),
        disconnect: vi.fn(),
        onMessage: { addListener: vi.fn(), removeListener: vi.fn() },
        onDisconnect: { addListener: vi.fn(), removeListener: vi.fn() },
      }));
      await importModuleWithConnect(connectMock);
      await vi.runOnlyPendingTimersAsync();
      expect(connectMock).toHaveBeenCalledTimes(1);

      // Heartbeat ticks every 1s; after 5s without a pong, reconnect triggers.
      // Advance 6 seconds to clear the 5s threshold on the next tick.
      await vi.advanceTimersByTimeAsync(6000);
      expect(connectMock).toHaveBeenCalledTimes(2);
    });
  });
});

describe('wizard rule correction', () => {
  const baselineRule = {
    id: 'ext-baseline-1', version: '1.0.0', name: '基线', domain: 'example.com',
    entry: 'https://example.com', steps: [{ action: 'click', target: { selector: '.go' } }],
  };
  const correctedWorkflow = {
    id: 'wf-1',
    status: 'awaiting_replay',
    requirementId: 'req-1',
    recordingId: 'rec-1',
    browserProfileId: 'current-chrome-profile',
    provisionalRule: { ...baselineRule, steps: [{ action: 'click', target: { selector: '.fixed' } }] },
    provisionalYaml: 'id: ext-baseline-1',
    currentJobId: 'job-1',
  };

  beforeEach(() => {
    document.body.innerHTML = WIZARD_HTML;
    sessionStorage.clear();
  });
  afterEach(() => {
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
  });

  it('seeds the correction editor from the stashed recording baseline', async () => {
    const { mod } = await importModule({
      GET_DSL_WORKFLOW_BASELINE: { success: true, baselineRule },
    });
    mod.state.dslWorkflowId = 'wf-1';

    await mod.showCorrectionEditor('gen');

    const section = document.getElementById('correct-rule-section-gen') as HTMLElement;
    expect(section.classList.contains('hidden')).toBe(false);
    const editor = document.getElementById('correct-rule-editor-gen') as HTMLTextAreaElement;
    expect(JSON.parse(editor.value)).toEqual(baselineRule);
  });

  it('reports a clear error when the baseline stash was lost', async () => {
    const { mod } = await importModule({
      GET_DSL_WORKFLOW_BASELINE: { success: false, error: '基线已不可用' },
    });
    mod.state.dslWorkflowId = 'wf-1';

    await mod.showCorrectionEditor('replay');

    const status = document.getElementById('status') as HTMLElement;
    expect(status.className).toBe('error');
    expect(status.textContent).toContain('基线已不可用');
    expect(document.getElementById('correct-rule-section-replay')?.classList.contains('hidden')).toBe(true);
  });

  it('submits a corrected rule and advances to the preview step', async () => {
    const { mod, sendMessage } = await importModule({
      GET_DSL_WORKFLOW_BASELINE: { success: true, baselineRule },
      CORRECT_DSL_WORKFLOW: { success: true, workflow: correctedWorkflow },
    });
    mod.state.dslWorkflowId = 'wf-1';
    await mod.showCorrectionEditor('gen');
    const editor = document.getElementById('correct-rule-editor-gen') as HTMLTextAreaElement;
    editor.value = JSON.stringify(correctedWorkflow.provisionalRule);

    await mod.submitRuleCorrection('gen');

    expect(sendMessage).toHaveBeenCalledWith({
      action: 'CORRECT_DSL_WORKFLOW',
      payload: expect.objectContaining({
        workflowId: 'wf-1',
        rule: correctedWorkflow.provisionalRule,
      }),
    });
    expect(mod.state.rule).toEqual(correctedWorkflow.provisionalRule);
    expect(document.getElementById('step-preview')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('correct-rule-section-gen')?.classList.contains('hidden')).toBe(true);
  });

  it('rejects invalid editor JSON without dispatching the correction', async () => {
    const { mod, sendMessage } = await importModule({
      GET_DSL_WORKFLOW_BASELINE: { success: true, baselineRule },
    });
    mod.state.dslWorkflowId = 'wf-1';
    await mod.showCorrectionEditor('gen');
    const editor = document.getElementById('correct-rule-editor-gen') as HTMLTextAreaElement;
    editor.value = '{not-json';

    await mod.submitRuleCorrection('gen');

    const status = document.getElementById('status') as HTMLElement;
    expect(status.className).toBe('error');
    const correctionCalls = (sendMessage as unknown as Mock).mock.calls.filter(
      (c: unknown[]) => (c[0] as { action?: string })?.action === 'CORRECT_DSL_WORKFLOW',
    );
    expect(correctionCalls).toHaveLength(0);
  });
});

describe('wizard generation failure surfaces the correction editor', () => {
  const failedWorkflow = {
    id: 'wf-failed',
    status: 'failed',
    requirementId: 'req-1',
    recordingId: 'rec-1',
    browserProfileId: 'current-chrome-profile',
    errorMessage: 'the workflow source cannot fit the configured model context',
    currentJobId: 'job-1',
  };
  const baselineRule = {
    id: 'ext-baseline-1', version: '1.0.0', name: '基线', domain: 'example.com',
    entry: 'https://example.com', steps: [{ action: 'click', target: { selector: '.go' } }],
  };

  beforeEach(() => {
    document.body.innerHTML = WIZARD_HTML;
    sessionStorage.clear();
  });
  afterEach(() => {
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
  });

  it('captures the workflow id from a failed CREATE response and seeds the editor', async () => {
    const { mod } = await importModule({
      GET_DSL_WORKFLOW_BASELINE: { success: true, baselineRule },
    });
    // the state a freshly failed generateWorkflowDSL leaves behind
    mod.state.dslWorkflowId = 'wf-failed';
    mod.state.requirementId = 'req-1';

    await mod.showCorrectionEditor('gen');

    expect(mod.state.dslWorkflowId).toBe('wf-failed');
    const section = document.getElementById('correct-rule-section-gen') as HTMLElement;
    expect(section.classList.contains('hidden')).toBe(false);
    const editor = document.getElementById('correct-rule-editor-gen') as HTMLTextAreaElement;
    expect(JSON.parse(editor.value)).toEqual(baselineRule);
  });
});
