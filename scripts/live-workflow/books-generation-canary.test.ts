// @vitest-environment node

import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import type { LocalLLMConfig } from '../local-live-llm-canary';
import {
  BOOKS_GENERATION_ONLY_APPROVAL,
  assertBooksGenerationCanaryEnvironment,
  assertBooksGenerationCanaryEvidence,
  executeBooksGenerationCanaryCommand,
  materializeRequirementVariables,
  runBooksGenerationCanary,
  type BooksGenerationCanaryAPI,
  type BooksGenerationCanaryEvidence,
  type BooksGenerationCanaryReport,
} from './books-generation-canary';
import { BOOKS_CATEGORY_REQUIREMENT, BOOKS_ENTRY_URL } from './books-category-matrix';
import type { RealSiteCostBudget } from './real-site-cost';

function booksRule(): Record<string, unknown> {
  return {
    id: 'ext-test',
    version: '1.0.0',
    name: 'Books by category',
    entry: BOOKS_ENTRY_URL,
    domain: 'books.toscrape.com',
    selectors: {
      rows: { selector: 'article.product_pod', visible: true },
      next: { text: 'next' },
    },
    steps: [
      { action: 'click', target: { text: '{{category}}', visible: true } },
      {
        action: 'loop',
        type: 'fixedCount',
        count: 10,
        steps: [
          {
            action: 'extract',
            name: 'books',
            target: { $ref: 'rows' },
            multiple: true,
            fields: {
              title: { type: 'attr', selector: 'h3 a', attr: 'title' },
              price: { type: 'text', selector: '.price_color' },
              availability: { type: 'text', selector: '.availability' },
              rating: { type: 'attr', selector: '.star-rating', attr: 'class' },
              product_url: {
                type: 'attr',
                selector: 'h3 a',
                attr: 'href',
                resolve: true,
              },
            },
          },
          {
            action: 'loop',
            type: 'forEach',
            items: '{{extracted.books}}',
            as: 'book',
            steps: [{
              action: 'sendResult',
              payload: {
                title: '{{loopItem.title}}',
                price: '{{loopItem.price}}',
                availability: '{{loopItem.availability}}',
                rating: '{{loopItem.rating}}',
                product_url: '{{loopItem.product_url}}',
              },
            }],
          },
          {
            action: 'if',
            condition: { type: 'elementNotExists', target: { $ref: 'next' } },
            then: [{ action: 'break' }],
            else: [{ action: 'click', target: { $ref: 'next' } }],
          },
        ],
      },
    ],
  };
}

function goRuleWireShape(value: Record<string, any>): Record<string, unknown> {
  const goMapJSON = (candidate: unknown): unknown => {
    if (Array.isArray(candidate)) return candidate.map(goMapJSON);
    if (candidate === null || typeof candidate !== 'object') return structuredClone(candidate);
    const normalized: Record<string, unknown> = {};
    for (const key of Object.keys(candidate).sort((left, right) =>
      Buffer.from(left).compare(Buffer.from(right)))) {
      normalized[key] = goMapJSON((candidate as Record<string, unknown>)[key]);
    }
    return normalized;
  };
  const jsonField = (name: string): unknown =>
    Object.prototype.hasOwnProperty.call(value, name)
      ? goMapJSON(value[name])
      : {};
  const text = (name: string): string =>
    typeof value[name] === 'string' ? value[name] : '';
  return {
    id: text('id'),
    workspaceId: text('workspaceId'),
    version: text('version'),
    name: text('name'),
    domain: jsonField('domain'),
    urlPattern: jsonField('urlPattern'),
    enabled: typeof value.enabled === 'boolean' ? value.enabled : false,
    priority: text('priority'),
    entry: text('entry'),
    variables: goMapJSON(materializeRequirementVariables(value, BOOKS_CATEGORY_REQUIREMENT)),
    selectors: jsonField('selectors'),
    humanize: jsonField('humanize'),
    steps: jsonField('steps'),
    output: jsonField('output'),
    sendPolicy: jsonField('sendPolicy'),
    hooks: jsonField('hooks'),
    tags: jsonField('tags'),
    owner: text('owner'),
    approvalStatus: text('approvalStatus'),
    source: text('source'),
    createdAt: text('createdAt') || '0001-01-01T00:00:00Z',
    updatedAt: text('updatedAt') || '0001-01-01T00:00:00Z',
  };
}

function goRuleWireHash(value: Record<string, any>): string {
  const encoded = JSON.stringify(goRuleWireShape(value)).replace(
    /[<>&\u2028\u2029]/g,
    (character) => ({
      '<': '\\u003c',
      '>': '\\u003e',
      '&': '\\u0026',
      '\u2028': '\\u2028',
      '\u2029': '\\u2029',
    })[character]!,
  );
  return crypto.createHash('sha256').update(encoded).digest('hex');
}

function evidence(): BooksGenerationCanaryEvidence {
  const resolvedRule = booksRule();
  const providerIR = {
    ...booksRule(),
    id: '',
    version: '',
    name: '',
    domain: null,
    entry: '',
  };
  const baseline = {
    id: 'ext-test',
    version: '1.0.0',
    name: 'Recorded workflow',
    entry: BOOKS_ENTRY_URL,
    domain: 'books.toscrape.com',
    steps: [{ action: 'click', target: { text: 'Mystery', visible: true } }],
  };
  const validation = [
    'selector-evidence-source',
    'selector-catalog',
    'provider-output',
    'provider-output-parse',
    'selector-candidate-resolution',
    'schema-and-contract-validation',
    'security-scan',
    'selector-evidence',
    'serialization',
  ].map((phase) => ({ phase, status: 'passed' }));
  const attempt = {
    id: 'attempt-1',
    workspaceId: 'workspace-1',
    recordingId: 'recording-1',
    jobType: 'dsl',
    jobId: 'job-1',
    attemptNumber: 1,
    outcome: 'succeeded',
    provider: 'openai',
    model: 'deepseek-v4-flash',
    promptVersion: 'dsl-workflow-test',
    callCount: 1,
    inputTokens: 90_000,
    outputTokens: 500,
    recordingHash: 'recording-hash',
    requirementHash: 'requirement-hash',
    baselineHash: goRuleWireHash(baseline),
    selectorCatalogHash: 'catalog-hash',
    artifactHash: 'attempt-artifact-hash',
    replayable: true,
    safetyFlags: [
      'artifact-capture:passed',
      'artifact-replayable:true',
      'provider-ir-source:bound',
    ],
  };
  const call = {
    id: 'call-1',
    workspaceId: 'workspace-1',
    recordingId: 'recording-1',
    jobType: 'dsl',
    jobId: 'job-1',
    attemptNumber: 1,
    callIndex: 1,
    providerAttempt: 1,
    callKind: 'provider_response',
    cacheHit: false,
    provider: 'openai',
    model: 'deepseek-v4-flash',
    promptVersion: 'dsl-workflow-test',
    phase: 'final',
    httpStatus: 200,
    inputTokens: 90_000,
    outputTokens: 500,
    originalBytesExact: true,
    redacted: false,
    truncated: false,
    replayable: true,
    requestHash: 'request-hash',
    responseHash: 'response-hash',
    artifactHash: 'call-artifact-hash',
  };
  const recordingCreation = {
    id: 'recording-1',
    workspaceId: 'workspace-1',
    contentHash: 'recording-hash',
    redactionCount: 0,
    removedFieldCount: 0,
    actionCount: 0,
    snapshotCount: 0,
  };
  return {
    recordingCreation,
    recording: { ...recordingCreation },
    recordingPayloadHash: 'recording-payload-hash',
    submittedNormalizationJob: {
      id: 'normalize-1',
      workspaceId: 'workspace-1',
      recordingId: 'recording-1',
      kind: 'normalize',
      status: 'pending',
      source: 'manual',
      attemptCount: 0,
      maxAttempts: 1,
      requestHash: 'normalization-request-hash',
    },
    normalizationStatusURL: '/api/v1/requirement-jobs/normalize-1',
    normalizationJob: {
      id: 'normalize-1',
      workspaceId: 'workspace-1',
      recordingId: 'recording-1',
      kind: 'normalize',
      status: 'completed',
      source: 'manual',
      provider: 'manual',
      model: 'manual',
      attemptCount: 1,
      maxAttempts: 1,
      inputTokens: 0,
      outputTokens: 0,
      requestHash: 'normalization-request-hash',
      requirementId: 'requirement-1',
      safetyFlags: ['schema-validation:passed', 'sensitive-data-scan:passed', 'cache:miss', 'degraded:false'],
    },
    normalizationAttempts: [],
    draftRequirement: {
      id: 'requirement-1',
      workspaceId: 'workspace-1',
      recordingId: 'recording-1',
      sourceJobId: 'normalize-1',
      source: 'manual',
      status: 'draft',
      contentHash: 'requirement-hash',
      requirement: BOOKS_CATEGORY_REQUIREMENT,
    },
    confirmedRequirement: {
      id: 'requirement-1',
      workspaceId: 'workspace-1',
      recordingId: 'recording-1',
      sourceJobId: 'normalize-1',
      source: 'manual',
      status: 'confirmed',
      contentHash: 'requirement-hash',
      requirement: BOOKS_CATEGORY_REQUIREMENT,
    },
    submittedWorkflow: {
      workflow: {
        id: 'workflow-1',
        workspaceId: 'workspace-1',
        requirementId: 'requirement-1',
        recordingId: 'recording-1',
        browserProfileId: 'books-generation-canary',
        currentJobId: 'job-1',
      },
      job: {
        id: 'job-1',
        workspaceId: 'workspace-1',
        workflowId: 'workflow-1',
      },
      jobRequestBaseline: baseline,
    },
    workflow: {
      id: 'workflow-1',
      workspaceId: 'workspace-1',
      requirementId: 'requirement-1',
      recordingId: 'recording-1',
      browserProfileId: 'books-generation-canary',
      currentJobId: 'job-1',
      status: 'awaiting_replay',
      repairCount: 0,
      maxRepairs: 0,
      lastReplaySequence: 0,
      provisionalRule: resolvedRule,
    },
    dslJob: {
      id: 'job-1',
      workspaceId: 'workspace-1',
      workflowId: 'workflow-1',
      kind: 'generate',
      status: 'completed',
      provider: 'openai',
      model: 'deepseek-v4-flash',
      promptVersion: 'dsl-workflow-test',
      inputTokens: 90_000,
      outputTokens: 500,
      attemptCount: 1,
      maxAttempts: 1,
      chunkCount: 5,
      completedChunks: 5,
      safetyFlags: ['selector-candidates:resolved', 'cache:miss', 'degraded:false'],
    },
    dslAttempts: [attempt],
    dslAttemptDetail: {
      attempt,
      calls: [call],
      artifact: {
        schemaVersion: 'aegiscrawler.dsl-attempt.v2',
        calls: [call],
        providerIRSourceCallId: 'call-1',
        providerIR,
        resolvedRule,
        trustedBaseline: goRuleWireShape(baseline),
        selectorCatalog: {
          version: 'selector-catalog-v5',
          catalogHash: 'catalog-hash',
          candidates: [{ id: 'candidate-1' }],
        },
        validation,
      },
    },
    providerCallContent: {
      call,
      content: {
        schemaVersion: 'aegiscrawler.llm-provider-completion.v1',
        contentHash: 'response-hash',
        truncated: false,
      },
    },
    tasks: { total: 0, tasks: [] },
    rules: { total: 0, rules: [] },
  };
}

const llm = { providerAdapter: 'openai' as const, model: 'deepseek-v4-flash' };
const lifecycleLLM: LocalLLMConfig = {
  providerLabel: 'openai',
  providerAdapter: 'openai',
  providerBaseURL: 'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
  providerHostname: 'workspace.cn-beijing.maas.aliyuncs.com',
  model: 'deepseek-v4-flash',
  apiKey: 'not-used-by-the-fake-api',
};
const lifecycleBudget: RealSiteCostBudget = {
  budgetUSD: 0.6,
  model: 'deepseek-v4-flash',
  pricingRegion: 'test',
  pricingSource: 'https://example.com/pricing',
  inputUSDPerMillion: 0.138,
  outputUSDPerMillion: 0.275,
  inputTokenLimit: 4_347_826,
};

function commandEnvironment(): NodeJS.ProcessEnv {
  return {
    AEGIS_LIVE_ALLOW_BOOKS_GENERATION_ONLY: BOOKS_GENERATION_ONLY_APPROVAL,
    AEGIS_LIVE_WORKFLOW_SCENARIOS: 'books-category-matrix',
    AEGIS_LIVE_LLM_MAX_INPUT_TOKENS: '315000',
    AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS: '4096',
    AEGIS_LIVE_LLM_REQUEST_TIMEOUT: '480s',
    AEGIS_LOCAL_LLM_PROVIDER: 'openai',
    AEGIS_LOCAL_LLM_MODEL: 'deepseek-v4-flash',
    AEGIS_LOCAL_LLM_BASE_URL:
      'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
  };
}

function successfulCommandReport(): BooksGenerationCanaryReport {
  return {
    schema: 'aegiscrawler.books-generation-canary.v1',
    status: 'passed',
    promotionEligible: false,
    startedAt: '2026-07-28T00:00:00.000Z',
    finishedAt: '2026-07-28T00:01:00.000Z',
    sourceHead: 'test-head',
    provider: 'openai',
    model: 'deepseek-v4-flash',
    providerHostname: 'workspace.cn-beijing.maas.aliyuncs.com',
    acceptedRequestCostCeilingUSD: 0.138561152,
    costBudgetUSD: 0.6,
    inputUSDPerMillion: 0.138,
    outputUSDPerMillion: 0.275,
    physicalProviderCalls: 1,
  };
}

function commandForecast() {
  return {
    recordingBytes: 1,
    baselineBytes: 1,
    generationSystemPromptBytes: 1,
    requirementBytes: 1,
    selectorCatalogBytes: 1,
    strictSchemaBytes: 1,
    fixedPromptReserveBytes: 1,
    requiredDirectBytes: 7,
    availableDirectBytes: 8,
    withinDirectEnvelope: true,
  };
}

function lifecycleRecording(): PageAgentRecording {
  return {
    version: '2.0.0',
    meta: {
      serverRecordingId: 'ephemeral-id',
      title: 'Books to Scrape',
      domain: 'books.toscrape.com',
      startUrl: BOOKS_ENTRY_URL,
      recordedAt: '2026-07-28T00:00:00.000Z',
      endedAt: '2026-07-28T00:01:00.000Z',
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v2',
    },
    limits: {
      maxActions: 500,
      maxDurationMs: 7_200_000,
      maxBytes: 64 * 1024 * 1024,
      warningThreshold: 0.8,
    },
    events: [],
    snapshots: [],
    termination: {
      reason: 'user',
      message: 'done',
      complete: true,
      timestamp: 1,
    },
  };
}

function lifecycleAPI(
  mode:
    | 'success'
    | 'provider-failure'
    | 'submission-unknown'
    | 'summary-zero'
    | 'summary-two'
    | 'recording-tampered'
    | 'recording-missing-payload'
    | 'recording-metadata-mismatch'
    | 'recording-redacted'
    | 'normalization-id-mismatch'
    | 'normalization-hash-mismatch' = 'success',
): {
  api: BooksGenerationCanaryAPI;
  calls: Array<{ method: string; route: string }>;
} {
  const value = evidence();
  const roundTripPayload = structuredClone(lifecycleRecording());
  delete roundTripPayload.meta.serverRecordingId;
  const recordingRead: Record<string, any> = {
    ...value.recording,
    payload: roundTripPayload,
  };
  value.normalizationJob.requirementId = 'requirement-1';
  if (mode === 'provider-failure') {
    value.dslJob.status = 'failed';
    value.dslAttempts[0].outcome = 'failed';
    delete value.dslAttemptDetail.artifact.providerIR;
    delete value.dslAttemptDetail.artifact.resolvedRule;
  } else if (mode === 'summary-zero') {
    value.dslAttempts[0].callCount = 0;
  } else if (mode === 'summary-two') {
    value.dslAttempts[0].callCount = 2;
  } else if (mode === 'recording-tampered') {
    recordingRead.payload.meta.title = 'tampered';
  } else if (mode === 'recording-missing-payload') {
    delete recordingRead.payload;
  } else if (mode === 'recording-metadata-mismatch') {
    recordingRead.contentHash = 'other-recording-hash';
  } else if (mode === 'recording-redacted') {
    recordingRead.redactionCount = 1;
  } else if (mode === 'normalization-id-mismatch') {
    value.normalizationJob.id = 'normalize-other';
  } else if (mode === 'normalization-hash-mismatch') {
    value.normalizationJob.requestHash = 'other-normalization-request-hash';
  }
  const calls: Array<{ method: string; route: string }> = [];
  let requirementReads = 0;
  const api: BooksGenerationCanaryAPI = {
    async admin<T>(method: string, route: string, body?: unknown): Promise<T> {
      calls.push({ method, route });
      let response: unknown;
      if (method === 'POST' && route === '/api/v1/recordings') {
        response = { recording: value.recordingCreation };
      } else if (method === 'GET' && route === '/api/v1/recordings/recording-1') {
        response = { recording: recordingRead };
      } else if (
        method === 'POST'
        && route === '/api/v1/recordings/recording-1/requirement-jobs/normalize'
      ) {
        response = {
          job: value.submittedNormalizationJob,
          statusUrl: value.normalizationStatusURL,
        };
      } else if (method === 'GET' && route === '/api/v1/requirement-jobs/normalize-1') {
        response = { job: value.normalizationJob };
      } else if (
        method === 'GET'
        && route === '/api/v1/requirement-jobs/normalize-1/provider-attempts'
      ) {
        response = { attempts: value.normalizationAttempts };
      } else if (method === 'GET' && route === '/api/v1/requirements/requirement-1') {
        response = {
          requirement: requirementReads++ === 0
            ? value.draftRequirement
            : value.confirmedRequirement,
        };
      } else if (
        method === 'POST'
        && route === '/api/v1/requirements/requirement-1/confirm'
      ) {
        response = { requirement: value.confirmedRequirement };
      } else if (
        method === 'POST'
        && route === '/api/v1/requirements/requirement-1/dsl-workflows'
      ) {
        if (mode === 'submission-unknown') {
          throw new Error('simulated ambiguous submission transport');
        }
        const baseline = (body as { baselineRule: Record<string, unknown> }).baselineRule;
        value.submittedWorkflow.jobRequestBaseline = baseline;
        value.dslAttemptDetail.artifact.trustedBaseline = goRuleWireShape(baseline);
        value.dslAttempts[0].baselineHash = goRuleWireHash(baseline);
        response = {
          workflow: value.submittedWorkflow.workflow,
          job: value.submittedWorkflow.job,
        };
      } else if (method === 'GET' && route === '/api/v1/dsl-jobs/job-1') {
        response = { job: value.dslJob };
      } else if (method === 'GET' && route === '/api/v1/dsl-workflows/workflow-1') {
        response = { workflow: value.workflow };
      } else if (
        method === 'GET'
        && route === '/api/v1/dsl-jobs/job-1/provider-attempts'
      ) {
        response = { attempts: value.dslAttempts };
      } else if (
        method === 'GET'
        && route === '/api/v1/dsl-jobs/job-1/provider-attempts/1'
      ) {
        response = value.dslAttemptDetail;
      } else if (
        method === 'GET'
        && route === '/api/v1/dsl-jobs/job-1/provider-attempts/1/calls/call-1/content'
      ) {
        response = value.providerCallContent;
      } else if (method === 'GET' && route === '/admin/tasks?limit=1&offset=0') {
        response = value.tasks;
      } else if (method === 'GET' && route === '/admin/rules?limit=1&offset=0') {
        response = value.rules;
      } else {
        throw new Error(`unexpected canary API call ${method} ${route}`);
      }
      return response as T;
    },
  };
  return { api, calls };
}

describe('Books provider-only generation canary contract', () => {
  it('requires the explicit non-browser Books command environment', () => {
    const valid = {
      AEGIS_LIVE_ALLOW_BOOKS_GENERATION_ONLY: BOOKS_GENERATION_ONLY_APPROVAL,
      AEGIS_LIVE_WORKFLOW_SCENARIOS: 'books-category-matrix',
      AEGIS_LIVE_LLM_MAX_INPUT_TOKENS: '315000',
      AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS: '4096',
      AEGIS_LIVE_LLM_REQUEST_TIMEOUT: '480s',
      AEGIS_LOCAL_LLM_PROVIDER: 'openai',
      AEGIS_LOCAL_LLM_MODEL: 'deepseek-v4-flash',
      AEGIS_LOCAL_LLM_BASE_URL:
        'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
    };
    expect(() => assertBooksGenerationCanaryEnvironment(valid)).not.toThrow();
    expect(() => assertBooksGenerationCanaryEnvironment({
      ...valid,
      AEGIS_LIVE_ALLOW_BOOKS_GENERATION_ONLY: undefined,
    })).toThrow(/approval/);
    expect(() => assertBooksGenerationCanaryEnvironment({
      ...valid,
      AEGIS_LIVE_WORKFLOW_HEADED: '1',
    })).toThrow(/headed or persistent|forbids/);
    expect(() => assertBooksGenerationCanaryEnvironment({
      ...valid,
      AEGIS_LIVE_REUSE_RECORDING: '1',
    })).toThrow(/must not reuse|forbids/);
  });

  it('loads credentials after recording validation and always cleans up before artifact writes', async () => {
    const artifacts = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-books-command-test-'));
    const events: string[] = [];
    const server = {
      api: {},
      baseUrl: 'http://127.0.0.1:1',
      serverDir: '/tmp/aegis-books-server',
      dbPath: '/tmp/aegis-books.db',
      serverProc: {},
      diagnostics: {
        value: `key=not-used-by-the-fake-api endpoint=${lifecycleLLM.providerBaseURL}`,
      },
    };
    try {
      const result = await executeBooksGenerationCanaryCommand({
        env: commandEnvironment(),
        head: () => 'test-head',
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        makeArtifactsDirectory: () => artifacts,
        loadRecording: () => {
          events.push('load-recording');
          return lifecycleRecording();
        },
        validateRecording: () => {
          events.push('validate-recording');
        },
        forecastRequest: () => commandForecast(),
        loadLLM: () => {
          events.push('load-credentials');
          return lifecycleLLM;
        },
        bootServer: async () => {
          events.push('boot-server');
          return server as any;
        },
        runCanary: async () => {
          events.push('run-canary');
          return successfulCommandReport();
        },
        shutdownServer: async (_server, removeGeneratedBinary) => {
          events.push('shutdown-server');
          expect(removeGeneratedBinary).toBe(true);
        },
        writeJSON: (directory, name, value) => {
          events.push(`write-${name}`);
          if (name === 'server-diagnostics.json') {
            expect(JSON.stringify(value)).not.toContain(lifecycleLLM.apiKey);
            expect(JSON.stringify(value)).not.toContain(lifecycleLLM.providerBaseURL);
            throw new Error('simulated diagnostics write failure');
          }
          const file = path.join(directory, name);
          fs.writeFileSync(file, `${JSON.stringify(value)}\n`, { mode: 0o600 });
          return file;
        },
      });

      expect(events).toEqual([
        'load-recording',
        'validate-recording',
        'load-credentials',
        'boot-server',
        'run-canary',
        'shutdown-server',
        'write-server-diagnostics.json',
        'write-report.json',
      ]);
      expect(result.report.status).toBe('failed');
      expect(result.report.physicalProviderCalls).toBe(1);
      expect(result.report.error).toMatch(/diagnostics artifact write failed/);
      expect(result.summary.physicalProviderCalls).toBe(1);
      expect(result.reportFile).toBe(path.join(artifacts, 'report.json'));
    } finally {
      fs.rmSync(artifacts, { recursive: true, force: true });
    }
  });

  it('preserves physical-call evidence when both report artifact writes fail', async () => {
    const artifacts = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-books-command-test-'));
    let cleaned = false;
    try {
      const result = await executeBooksGenerationCanaryCommand({
        env: commandEnvironment(),
        head: () => 'test-head',
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        makeArtifactsDirectory: () => artifacts,
        loadRecording: lifecycleRecording,
        validateRecording: () => {},
        forecastRequest: () => commandForecast(),
        loadLLM: () => lifecycleLLM,
        bootServer: async () => ({
          api: {},
          baseUrl: 'http://127.0.0.1:1',
          serverDir: '/tmp/aegis-books-server',
          dbPath: '/tmp/aegis-books.db',
          serverProc: {},
          diagnostics: { value: '' },
        } as any),
        runCanary: async () => successfulCommandReport(),
        shutdownServer: async () => {
          cleaned = true;
        },
        writeJSON: (directory, name, value) => {
          if (name.startsWith('report')) throw new Error('simulated report write failure');
          const file = path.join(directory, name);
          fs.writeFileSync(file, `${JSON.stringify(value)}\n`, { mode: 0o600 });
          return file;
        },
      });

      expect(cleaned).toBe(true);
      expect(result.report.status).toBe('failed');
      expect(result.report.physicalProviderCalls).toBe(1);
      expect(result.summary.physicalProviderCalls).toBe(1);
      expect(result.reportFile).toBeUndefined();
      expect(result.reportSHA256).toBeUndefined();
      expect(result.report.error).toMatch(/report artifact write failed/);
    } finally {
      fs.rmSync(artifacts, { recursive: true, force: true });
    }
  });

  it('accepts one uncached provider attempt and stops at awaiting_replay', () => {
    expect(assertBooksGenerationCanaryEvidence(evidence(), llm)).toEqual({
      physicalProviderCalls: 1,
      generationJobs: 1,
      durableGenerationAttempts: 1,
      providerRetries: 0,
      dslRepairs: 0,
      selectorRepairChildJobs: 0,
      replayAttempts: 0,
      cacheHit: false,
      degraded: false,
      fallbackUsed: false,
      inputTokens: 90_000,
      outputTokens: 500,
      selectorCatalogHash: 'catalog-hash',
      attemptArtifactHash: 'attempt-artifact-hash',
      providerCallArtifactHash: 'call-artifact-hash',
    });
  });

  it('matches Go map ordering and JSON escaping for the trusted baseline hash', () => {
    const value = evidence();
    const submittedBaseline = value.submittedWorkflow.jobRequestBaseline;
    submittedBaseline.name = '<books>&\u2028\u2029';
    submittedBaseline.variables = {
      zeta: { z: 1, a: 2 },
      alpha: '&first',
    };
    value.dslAttemptDetail.artifact.trustedBaseline = goRuleWireShape(submittedBaseline);
    value.dslAttempts[0].baselineHash = goRuleWireHash(submittedBaseline);

    expect(() => assertBooksGenerationCanaryEvidence(value, llm)).not.toThrow();
  });

  it('materializes confirmed requirement variables exactly like the Go workflow validator', () => {
    const source = {
      variables: {
        preserved: 'value',
        existingNumber: 7,
      },
    };
    const requirement = {
      requiredInputs: [
        { name: 'text', type: 'string' },
        { name: 'existingNumber', type: 'number' },
        { name: 'enabled', type: 'boolean' },
      ],
      optionalInputs: [
        { name: 'limit', type: 'number', default: 10 },
        { name: 'options', type: 'object' },
        { name: 'items', type: 'array' },
      ],
    };

    expect(materializeRequirementVariables(source, requirement)).toEqual({
      preserved: 'value',
      existingNumber: 7,
      text: '',
      enabled: false,
      limit: 10,
      options: {},
      items: [],
    });
    expect(source.variables).toEqual({
      preserved: 'value',
      existingNumber: 7,
    });
  });

  it('rejects an unsupported confirmed requirement variable type', () => {
    expect(() => materializeRequirementVariables({}, {
      requiredInputs: [{ name: 'unsupported', type: 'date' }],
    })).toThrow(/unsupported confirmed requirement input type/);
  });

  it('orchestrates the exact generation-only API lifecycle without replay or mutations', async () => {
    const fake = lifecycleAPI();
    const report = await runBooksGenerationCanary(
      fake.api,
      lifecycleRecording(),
      lifecycleLLM,
      lifecycleBudget,
      0.138561152,
      {
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        sleep: async () => {},
        timeoutMs: 100,
        sourceHead: 'test-head',
      },
    );

    expect(report.status).toBe('passed');
    expect(report.physicalProviderCalls).toBe(1);
    expect(report).toMatchObject({
      generationJobs: 1,
      durableGenerationAttempts: 1,
      providerRetries: 0,
      dslRepairs: 0,
      selectorRepairChildJobs: 0,
      replayAttempts: 0,
      cacheHit: false,
      degraded: false,
      fallbackUsed: false,
    });
    expect(report.providerIRAvailable).toBe(true);
    expect(report.resolvedRuleAvailable).toBe(true);
    expect(fake.calls.slice(0, 3)).toEqual([
      { method: 'POST', route: '/api/v1/recordings' },
      { method: 'GET', route: '/api/v1/recordings/recording-1' },
      {
        method: 'POST',
        route: '/api/v1/recordings/recording-1/requirement-jobs/normalize',
      },
    ]);
    expect(fake.calls.filter(({ method }) => method === 'POST').map(({ route }) => route))
      .toEqual([
        '/api/v1/recordings',
        '/api/v1/recordings/recording-1/requirement-jobs/normalize',
        '/api/v1/requirements/requirement-1/confirm',
        '/api/v1/requirements/requirement-1/dsl-workflows',
      ]);
    expect(fake.calls.some(({ route }) =>
      /replay|approve|mcp|tokens/.test(route))).toBe(false);
    expect(fake.calls.some(({ method, route }) =>
      method !== 'GET' && (/admin\/tasks|admin\/rules/.test(route)))).toBe(false);
  });

  it('retains the exact physical call count and unavailable outputs on provider failure', async () => {
    const fake = lifecycleAPI('provider-failure');
    const report = await runBooksGenerationCanary(
      fake.api,
      lifecycleRecording(),
      lifecycleLLM,
      lifecycleBudget,
      0.138561152,
      {
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        sleep: async () => {},
        timeoutMs: 100,
        sourceHead: 'test-head',
      },
    );

    expect(report.status).toBe('failed');
    expect(report.physicalProviderCalls).toBe(1);
    expect(report.providerIRAvailable).toBe(false);
    expect(report.resolvedRuleAvailable).toBe(false);
    expect(report.error).toMatch(/must complete/);
    expect(fake.calls.some(({ route }) =>
      /replay|approve|mcp|tokens/.test(route))).toBe(false);
  });

  it('reports an ambiguous post-submission transport as null instead of a false zero', async () => {
    const fake = lifecycleAPI('submission-unknown');
    const report = await runBooksGenerationCanary(
      fake.api,
      lifecycleRecording(),
      lifecycleLLM,
      lifecycleBudget,
      0.138561152,
      {
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        sleep: async () => {},
        timeoutMs: 100,
        sourceHead: 'test-head',
      },
    );

    expect(report.status).toBe('failed');
    expect(report.physicalProviderCalls).toBeNull();
    expect(report.error).toMatch(/ambiguous submission transport/);
  });

  it('retains a directly observed call when the attempt summary falsely says zero', async () => {
    const fake = lifecycleAPI('summary-zero');
    const report = await runBooksGenerationCanary(
      fake.api,
      lifecycleRecording(),
      lifecycleLLM,
      lifecycleBudget,
      0.138561152,
      {
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        sleep: async () => {},
        timeoutMs: 100,
        sourceHead: 'test-head',
      },
    );

    expect(report.status).toBe('failed');
    expect(report.physicalProviderCalls).toBe(1);
    expect(report.error).toMatch(/summary\/detail physical-call counts disagree/);
  });

  it('reports a non-zero summary/detail contradiction as unknown', async () => {
    const fake = lifecycleAPI('summary-two');
    const report = await runBooksGenerationCanary(
      fake.api,
      lifecycleRecording(),
      lifecycleLLM,
      lifecycleBudget,
      0.138561152,
      {
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        sleep: async () => {},
        timeoutMs: 100,
        sourceHead: 'test-head',
      },
    );

    expect(report.status).toBe('failed');
    expect(report.physicalProviderCalls).toBeNull();
    expect(report.error).toMatch(/summary\/detail physical-call counts disagree/);
  });

  it.each([
    ['changed payload', 'recording-tampered' as const, /recording changed/],
    ['missing payload', 'recording-missing-payload' as const, /payload must be an object/],
    ['changed metadata', 'recording-metadata-mismatch' as const, /metadata changed/],
    ['server redaction', 'recording-redacted' as const, /zero redactions/],
    ['normalization identity change', 'normalization-id-mismatch' as const, /identity changed/],
    ['normalization hash change', 'normalization-hash-mismatch' as const, /request hash changed/],
  ])('fails before provider submission on %s', async (_label, mode, pattern) => {
    const fake = lifecycleAPI(mode);
    const report = await runBooksGenerationCanary(
      fake.api,
      lifecycleRecording(),
      lifecycleLLM,
      lifecycleBudget,
      0.138561152,
      {
        now: () => new Date('2026-07-28T00:00:00.000Z'),
        sleep: async () => {},
        timeoutMs: 100,
        sourceHead: 'test-head',
      },
    );

    expect(report.status).toBe('failed');
    expect(report.physicalProviderCalls).toBe(0);
    expect(report.error).toMatch(pattern);
    expect(fake.calls).not.toContainEqual({
      method: 'POST',
      route: '/api/v1/requirements/requirement-1/dsl-workflows',
    });
  });

  it.each([
    ['recording redaction', (value: BooksGenerationCanaryEvidence) => {
      value.recording.redactionCount = 1;
    }, /zero server redactions/],
    ['recording removal', (value: BooksGenerationCanaryEvidence) => {
      value.recording.removedFieldCount = 1;
    }, /zero server field removals/],
    ['recording metadata mutation', (value: BooksGenerationCanaryEvidence) => {
      value.recording.workspaceId = 'other-workspace';
    }, /metadata changed/],
    ['normalization submission identity', (value: BooksGenerationCanaryEvidence) => {
      value.submittedNormalizationJob.id = 'normalize-other';
    }, /normalization job identity/],
    ['normalization status URL', (value: BooksGenerationCanaryEvidence) => {
      value.normalizationStatusURL = '/api/v1/requirement-jobs/other';
    }, /status URL/],
    ['normalization request hash', (value: BooksGenerationCanaryEvidence) => {
      value.normalizationJob.requestHash = 'other-request-hash';
    }, /request hash changed/],
    ['trusted baseline Go zero field', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttemptDetail.artifact.trustedBaseline.workspaceId = 'unexpected';
    }, /normalized submitted baseline/],
    ['trusted baseline materialized input', (value: BooksGenerationCanaryEvidence) => {
      delete value.dslAttemptDetail.artifact.trustedBaseline.variables.category;
    }, /normalized submitted baseline/],
    ['trusted baseline nested field', (value: BooksGenerationCanaryEvidence) => {
      value.submittedWorkflow.jobRequestBaseline.steps[0].target.text = 'Other';
    }, /normalized submitted baseline/],
    ['trusted baseline hash', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttempts[0].baselineHash = 'other-baseline-hash';
    }, /baseline hash/],
    ['zero calls', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttempts[0].callCount = 0;
    }, /exactly one provider call/],
    ['two calls', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttempts[0].callCount = 2;
    }, /exactly one provider call/],
    ['cache hit', (value: BooksGenerationCanaryEvidence) => {
      value.dslJob.safetyFlags = ['cache:hit', 'degraded:false'];
    }, /cache:miss|cached/],
    ['degraded output', (value: BooksGenerationCanaryEvidence) => {
      value.dslJob.safetyFlags = ['cache:miss', 'degraded:true'];
    }, /degraded:false|degraded output/],
    ['selector repair child', (value: BooksGenerationCanaryEvidence) => {
      value.workflow.currentJobId = 'child-job';
    }, /child job/],
    ['replay', (value: BooksGenerationCanaryEvidence) => {
      value.workflow.lastReplaySequence = 1;
    }, /zero replay/],
    ['approval', (value: BooksGenerationCanaryEvidence) => {
      value.workflow.approvedRuleId = 'rule-1';
    }, /must not approve/],
    ['missing ProviderIR', (value: BooksGenerationCanaryEvidence) => {
      delete value.dslAttemptDetail.artifact.providerIR;
    }, /raw ProviderIR/],
    ['missing resolved rule', (value: BooksGenerationCanaryEvidence) => {
      delete value.dslAttemptDetail.artifact.resolvedRule;
    }, /resolved canonical rule/],
    ['catalog mismatch', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttemptDetail.artifact.selectorCatalog.catalogHash = 'other';
    }, /hash binding/],
    ['failed validation', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttemptDetail.artifact.validation[4].status = 'failed';
    }, /must pass|all retained/],
    ['task mutation', (value: BooksGenerationCanaryEvidence) => {
      value.tasks = { total: 1, tasks: [{ id: 'task-1' }] };
    }, /zero tasks/],
    ['workflow requirement lineage', (value: BooksGenerationCanaryEvidence) => {
      value.workflow.requirementId = 'other-requirement';
    }, /requirement lineage/],
    ['job workflow lineage', (value: BooksGenerationCanaryEvidence) => {
      value.dslJob.workflowId = 'other-workflow';
    }, /job workflow lineage/],
    ['attempt recording hash', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttempts[0].recordingHash = 'other-recording-hash';
    }, /recording hash/],
    ['attempt requirement hash', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttempts[0].requirementHash = 'other-requirement-hash';
    }, /requirement hash/],
    ['attempt detail metadata', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttemptDetail.attempt = {
        ...value.dslAttemptDetail.attempt,
        jobId: 'other-job',
      };
    }, /detail changed/],
    ['artifact call lineage', (value: BooksGenerationCanaryEvidence) => {
      value.dslAttemptDetail.artifact.calls[0] = {
        ...value.dslAttemptDetail.artifact.calls[0],
        id: 'other-call',
      };
    }, /artifact call lineage/],
    ['call content metadata', (value: BooksGenerationCanaryEvidence) => {
      value.providerCallContent.call = {
        ...value.providerCallContent.call,
        requestHash: 'other-request',
      };
    }, /content metadata changed/],
    ['call content hash', (value: BooksGenerationCanaryEvidence) => {
      value.providerCallContent.content.contentHash = 'other-response';
    }, /content hash changed/],
    ['zero input usage', (value: BooksGenerationCanaryEvidence) => {
      value.dslJob.inputTokens = 0;
      value.dslAttempts[0].inputTokens = 0;
      value.dslAttemptDetail.calls[0].inputTokens = 0;
      value.dslAttemptDetail.artifact.calls[0].inputTokens = 0;
      value.providerCallContent.call.inputTokens = 0;
    }, /positive input tokens/],
    ['job/call output usage mismatch', (value: BooksGenerationCanaryEvidence) => {
      value.dslJob.outputTokens = 499;
    }, /job output tokens changed/],
  ])('rejects %s', (_label, mutate, pattern) => {
    const value = evidence();
    mutate(value);
    expect(() => assertBooksGenerationCanaryEvidence(value, llm)).toThrow(pattern);
  });
});
