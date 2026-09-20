#!/usr/bin/env ts-node

import assert from 'node:assert/strict';
import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { execFileSync } from 'node:child_process';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { convert } from '../../src/rule-generator';
import { loadLocalLLMConfig, type LocalLLMConfig } from '../local-live-llm-canary';
import {
  BOOKS_CATEGORY_REQUIREMENT,
  BOOKS_COST_BUDGET_USD,
  BOOKS_DEEPSEEK_CONTEXT_TOKENS,
  BOOKS_MAX_OUTPUT_TOKENS,
  assertBooksAcceptedRequestCostAuthorized,
  assertBooksQualificationEnvironment,
  assertBooksRecording,
  assertBooksRequirement,
  assertBooksRule,
  forecastBooksDirectRequest,
} from './books-category-matrix';
import {
  capRealSiteCostBudget,
  estimateContextWindowCostUpperBound,
  estimateCostUpperBound,
  resolveRealSiteCostBudget,
  type RealSiteCostBudget,
} from './real-site-cost';
import {
  loadReusableBooksRecording,
  reusableBooksRecordingPaths,
} from './reusable-recording';
import {
  bootLiveServer,
  shutdownLiveServer,
  sleep,
  type LiveAPI,
  type LiveServerEnvironment,
} from './support';
import { sumProviderAttemptCalls } from './provider-call-budget';

export const BOOKS_GENERATION_ONLY_APPROVAL =
  'I approve one provider-only Books generation canary';
export const BOOKS_GENERATION_CANARY_SCHEMA =
  'aegiscrawler.books-generation-canary.v1';
const BOOKS_GENERATION_PROFILE = 'books-generation-canary';
const TERMINAL_TIMEOUT_MS = 10 * 60_000;
const POLL_INTERVAL_MS = 500;

type JSONRecord = Record<string, any>;

export interface BooksGenerationCanaryAPI {
  admin<T>(method: string, route: string, body?: unknown): Promise<T>;
}

export interface BooksGenerationCanaryEvidence {
  recordingCreation: JSONRecord;
  recording: JSONRecord;
  recordingPayloadHash: string;
  submittedNormalizationJob: JSONRecord;
  normalizationStatusURL: string;
  normalizationJob: JSONRecord;
  normalizationAttempts: JSONRecord[];
  draftRequirement: JSONRecord;
  confirmedRequirement: JSONRecord;
  submittedWorkflow: JSONRecord;
  workflow: JSONRecord;
  dslJob: JSONRecord;
  dslAttempts: JSONRecord[];
  dslAttemptDetail: JSONRecord;
  providerCallContent: JSONRecord;
  tasks: JSONRecord;
  rules: JSONRecord;
}

export interface BooksGenerationCanaryReport {
  schema: typeof BOOKS_GENERATION_CANARY_SCHEMA;
  status: 'passed' | 'failed';
  promotionEligible: false;
  startedAt: string;
  finishedAt: string;
  sourceHead: string;
  provider: string;
  model: string;
  providerHostname: string;
  acceptedRequestCostCeilingUSD: number;
  costBudgetUSD: number;
  inputUSDPerMillion: number;
  outputUSDPerMillion: number;
  /**
   * Zero before a provider-backed job can exist, the exact retained attempt
   * count once it can be read, or null when submission may have reached the
   * server but its durable identity/count cannot be recovered. Never omit this
   * field or convert an unknown paid-call state into a false zero.
   */
  physicalProviderCalls: number | null;
  generationJobs?: number;
  durableGenerationAttempts?: number;
  providerRetries?: number;
  dslRepairs?: number;
  selectorRepairChildJobs?: number;
  replayAttempts?: number;
  cacheHit?: boolean;
  degraded?: boolean;
  fallbackUsed?: boolean;
  actualInputTokens?: number;
  actualOutputTokens?: number;
  estimatedCostUpperBoundUSD?: number;
  recordingId?: string;
  recordingContentHash?: string;
  requirementJobId?: string;
  requirementId?: string;
  workflowId?: string;
  dslJobId?: string;
  providerIRAvailable?: boolean;
  resolvedRuleAvailable?: boolean;
  selectorCatalogHash?: string;
  attemptArtifactHash?: string;
  providerCallArtifactHash?: string;
  evidence?: BooksGenerationCanaryEvidence;
  error?: string;
}

interface RunOptions {
  now?: () => Date;
  sleep?: (ms: number) => Promise<void>;
  timeoutMs?: number;
  sourceHead?: string;
}

function plainObject(value: unknown): value is JSONRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function requiredObject(value: unknown, label: string): JSONRecord {
  assert(plainObject(value), `${label} must be an object`);
  return value;
}

function stringValue(value: unknown, label: string): string {
  assert(typeof value === 'string' && value.length > 0, `${label} must be a non-empty string`);
  return value;
}

function numberValue(value: unknown, label: string): number {
  assert(Number.isSafeInteger(value) && Number(value) >= 0, `${label} must be a non-negative integer`);
  return Number(value);
}

function goMapJSON(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(goMapJSON);
  if (!plainObject(value)) return structuredClone(value);
  const normalized: JSONRecord = {};
  for (const key of Object.keys(value).sort((left, right) =>
    Buffer.from(left).compare(Buffer.from(right)))) {
    normalized[key] = goMapJSON(value[key]);
  }
  return normalized;
}

function jsonField(value: JSONRecord, name: string): unknown {
  return Object.prototype.hasOwnProperty.call(value, name)
    ? goMapJSON(value[name])
    : {};
}

interface RequirementVariableInput {
  name: string;
  type: string;
  default?: unknown;
}

interface RequirementVariableContract {
  requiredInputs?: readonly RequirementVariableInput[];
  optionalInputs?: readonly RequirementVariableInput[];
}

/**
 * Mirror the deterministic variable materialization performed by the Go
 * workflow validator before it persists the trusted generation baseline.
 * Existing converter values remain authoritative; only missing confirmed
 * inputs receive their declared default or the matching Go zero value.
 */
export function materializeRequirementVariables(
  value: JSONRecord,
  requirement: RequirementVariableContract,
): JSONRecord {
  const variables = plainObject(value.variables)
    ? structuredClone(value.variables)
    : {};
  for (const input of [
    ...(requirement.requiredInputs ?? []),
    ...(requirement.optionalInputs ?? []),
  ]) {
    if (Object.prototype.hasOwnProperty.call(variables, input.name)) continue;
    if (input.default !== undefined && input.default !== null) {
      variables[input.name] = structuredClone(input.default);
      continue;
    }
    switch (input.type) {
      case 'string':
        variables[input.name] = '';
        break;
      case 'number':
        variables[input.name] = 0;
        break;
      case 'boolean':
        variables[input.name] = false;
        break;
      case 'object':
        variables[input.name] = {};
        break;
      case 'array':
        variables[input.name] = [];
        break;
      default:
        throw new Error(`unsupported confirmed requirement input type: ${input.type}`);
    }
  }
  return variables;
}

/**
 * Normalize the converter's compact request into the exact validated JSON
 * shape emitted by Go's models.Rule. Missing models.JSON values encode as {},
 * confirmed requirement variables are materialized before persistence, and
 * scalar and timestamp fields retain their Go zero values.
 */
function booksBaselineGoWireShape(value: JSONRecord): JSONRecord {
  const text = (name: string): string =>
    typeof value[name] === 'string' ? value[name] : '';
  return {
    id: text('id'),
    workspaceId: text('workspaceId'),
    version: text('version'),
    name: text('name'),
    domain: jsonField(value, 'domain'),
    urlPattern: jsonField(value, 'urlPattern'),
    enabled: typeof value.enabled === 'boolean' ? value.enabled : false,
    priority: text('priority'),
    entry: text('entry'),
    variables: goMapJSON(materializeRequirementVariables(value, BOOKS_CATEGORY_REQUIREMENT)),
    selectors: jsonField(value, 'selectors'),
    humanize: jsonField(value, 'humanize'),
    steps: jsonField(value, 'steps'),
    output: jsonField(value, 'output'),
    sendPolicy: jsonField(value, 'sendPolicy'),
    hooks: jsonField(value, 'hooks'),
    tags: jsonField(value, 'tags'),
    owner: text('owner'),
    approvalStatus: text('approvalStatus'),
    source: text('source'),
    createdAt: text('createdAt') || '0001-01-01T00:00:00Z',
    updatedAt: text('updatedAt') || '0001-01-01T00:00:00Z',
  };
}

function goJSONBytes(value: unknown): string {
  return JSON.stringify(value).replace(
    /[<>&\u2028\u2029]/g,
    (character) => ({
      '<': '\\u003c',
      '>': '\\u003e',
      '&': '\\u0026',
      '\u2028': '\\u2028',
      '\u2029': '\\u2029',
    })[character]!,
  );
}

function goJSONHash(value: unknown): string {
  return crypto.createHash('sha256').update(goJSONBytes(value)).digest('hex');
}

function safetyFlags(job: JSONRecord): string[] {
  assert(Array.isArray(job.safetyFlags), 'job safetyFlags must be an array');
  return job.safetyFlags.map(String);
}

function assertUncachedAndAuthoritative(flags: readonly string[], label: string): void {
  assert(flags.includes('cache:miss'), `${label} must expose cache:miss`);
  assert(!flags.includes('cache:hit'), `${label} must not use cached output`);
  assert(flags.includes('degraded:false'), `${label} must expose degraded:false`);
  assert(!flags.includes('degraded:true'), `${label} must not use degraded output`);
}

export function assertBooksGenerationCanaryEnvironment(
  env: NodeJS.ProcessEnv,
): void {
  assert.equal(
    env.AEGIS_LIVE_ALLOW_BOOKS_GENERATION_ONLY,
    BOOKS_GENERATION_ONLY_APPROVAL,
    'provider-only Books generation approval is required',
  );
  assertBooksQualificationEnvironment(env, false);
  assert.equal(
    env.AEGIS_LIVE_LLM_REQUEST_TIMEOUT,
    '480s',
    'provider-only Books generation requires the reviewed 480s request timeout',
  );
  for (const name of [
    'AEGIS_LIVE_RECORD_ONLY',
    'AEGIS_LIVE_SAVE_RECORDING',
    'AEGIS_LIVE_REUSE_RECORDING',
    'AEGIS_LIVE_HUMAN_DEMO',
    'AEGIS_LIVE_HUMAN_REQUIREMENT',
    'AEGIS_LIVE_WORKFLOW_HEADED',
    'AEGIS_LIVE_PROFILE_WARMUP',
    'AEGIS_LIVE_BROWSER_PROFILE',
    'AEGIS_LIVE_ALLOW_PERSISTENT_PROFILE',
  ]) {
    assert(!env[name], `provider-only Books generation forbids ${name}`);
  }
}

async function waitForJob(
  api: BooksGenerationCanaryAPI,
  route: string,
  options: Required<Pick<RunOptions, 'sleep' | 'timeoutMs'>>,
): Promise<JSONRecord> {
  const deadline = Date.now() + options.timeoutMs;
  let lastStatus = '';
  while (Date.now() < deadline) {
    const response = requiredObject(await api.admin<unknown>('GET', route), `${route} response`);
    const job = requiredObject(response.job, `${route} job`);
    lastStatus = String(job.status ?? '');
    if (lastStatus === 'completed' || lastStatus === 'failed') return job;
    await options.sleep(POLL_INTERVAL_MS);
  }
  throw new Error(`${route} did not reach a terminal state; last status ${lastStatus || 'unknown'}`);
}

function assertManualNormalization(
  job: JSONRecord,
  attempts: JSONRecord[],
): string {
  assert.equal(job.kind, 'normalize', 'Books requirement job must be normalize');
  assert.equal(job.status, 'completed', 'Books requirement normalization must complete');
  assert.equal(job.source, 'manual', 'Books structured requirement must retain manual source');
  assert.equal(job.provider, 'manual', 'Books structured requirement must use manual provider');
  assert.equal(job.model, 'manual', 'Books structured requirement must use manual model');
  assert.equal(job.attemptCount, 1, 'Books normalization must run exactly once');
  assert.equal(job.maxAttempts, 1, 'Books normalization maxAttempts must be one');
  assert.equal(job.inputTokens, 0, 'Books manual normalization must use zero input tokens');
  assert.equal(job.outputTokens, 0, 'Books manual normalization must use zero output tokens');
  assert.deepEqual(attempts, [], 'Books manual normalization must have zero provider attempts');
  assertUncachedAndAuthoritative(safetyFlags(job), 'Books manual normalization');
  return stringValue(job.requirementId, 'Books normalized requirementId');
}

function assertRequirementLineage(
  value: JSONRecord,
  recordingId: string,
  requirementId: string,
  sourceJobId: string,
  expectedStatus: 'draft' | 'confirmed',
): void {
  assert.equal(value.id, requirementId, 'Books requirement identity changed');
  assert.equal(value.recordingId, recordingId, 'Books requirement recording lineage changed');
  assert.equal(value.sourceJobId, sourceJobId, 'Books requirement source-job lineage changed');
  assert.equal(value.source, 'manual', 'Books requirement source changed');
  assert.equal(value.status, expectedStatus, `Books requirement must be ${expectedStatus}`);
  assertBooksRequirement(value.requirement);
  assert.deepEqual(value.requirement, BOOKS_CATEGORY_REQUIREMENT,
    'Books normalized requirement changed the reviewed structured contract');
  stringValue(value.contentHash, 'Books requirement contentHash');
}

function assertEmptyProductMutations(evidence: BooksGenerationCanaryEvidence): void {
  assert.equal(evidence.tasks.total, 0, 'generation-only canary must create zero tasks');
  assert.deepEqual(evidence.tasks.tasks, [], 'generation-only canary task list must be empty');
  assert.equal(evidence.rules.total, 0, 'generation-only canary must create zero immutable rules');
  assert.deepEqual(evidence.rules.rules, [], 'generation-only canary rule list must be empty');
}

function assertAttemptArtifact(
  evidence: BooksGenerationCanaryEvidence,
  attempt: JSONRecord,
  onlyCall: JSONRecord,
): { providerIR: JSONRecord; resolvedRule: JSONRecord } {
  const detail = evidence.dslAttemptDetail;
  assert.deepEqual(detail.attempt, attempt,
    'provider attempt detail changed from the immutable attempt-list report');
  assert(Array.isArray(detail.calls) && detail.calls.length === 1,
    'provider attempt detail must contain exactly one call');
  assert.deepEqual(detail.calls[0], onlyCall, 'provider call list identity changed');
  const artifact = requiredObject(detail.artifact, 'DSL attempt artifact');
  assert.equal(artifact.schemaVersion, 'aegiscrawler.dsl-attempt.v2',
    'DSL attempt artifact schema must be v2');
  assert(Array.isArray(artifact.calls) && artifact.calls.length === 1,
    'DSL attempt artifact must retain exactly one provider call');
  assert.deepEqual(artifact.calls[0], onlyCall,
    'DSL attempt artifact call lineage changed from the retained provider call');
  assert.equal(artifact.providerIRSourceCallId, onlyCall.id,
    'ProviderIR must bind to the exact terminal provider call');
  assert.equal(artifact.selectorRepair, undefined, 'generation-only canary must have no selector repair lineage');
  const providerIR = requiredObject(artifact.providerIR, 'raw ProviderIR');
  const resolvedRule = requiredObject(artifact.resolvedRule, 'resolved canonical rule');
  const trustedBaseline = requiredObject(artifact.trustedBaseline, 'trusted baseline');
  const selectorCatalog = requiredObject(artifact.selectorCatalog, 'selector catalog');
  const submittedBaseline = requiredObject(
    evidence.submittedWorkflow.jobRequestBaseline,
    'submitted baseline',
  );
  const expectedTrustedBaseline = booksBaselineGoWireShape(submittedBaseline);
  assert.deepEqual(trustedBaseline, expectedTrustedBaseline,
    'attempt trusted baseline changed from the normalized submitted baseline');
  assert.equal(attempt.baselineHash, goJSONHash(expectedTrustedBaseline),
    'attempt baseline hash changed from the normalized submitted baseline');
  assert.equal(selectorCatalog.version, 'selector-catalog-v5',
    'Books generation must use selector-catalog-v5');
  assert(Array.isArray(selectorCatalog.candidates) && selectorCatalog.candidates.length > 0,
    'Books selector catalog must retain candidates');
  assert.equal(selectorCatalog.catalogHash, attempt.selectorCatalogHash,
    'selector catalog payload/report hash binding changed');
  assert.deepEqual(resolvedRule, evidence.workflow.provisionalRule,
    'resolved rule must equal the workflow provisional rule');
  assertBooksRule(resolvedRule);

  assert(Array.isArray(artifact.validation), 'DSL attempt validation must be an array');
  const expectedPhases = [
    'selector-evidence-source',
    'selector-catalog',
    'provider-output',
    'provider-output-parse',
    'selector-candidate-resolution',
    'schema-and-contract-validation',
    'security-scan',
    'selector-evidence',
    'serialization',
  ];
  for (const phase of expectedPhases) {
    const matches: JSONRecord[] = (artifact.validation as unknown[]).filter(
      (entry: unknown): entry is JSONRecord => plainObject(entry) && entry.phase === phase,
    );
    assert.equal(matches.length, 1, `DSL attempt must contain one ${phase} validation`);
    assert.equal(matches[0].status, 'passed', `${phase} validation must pass`);
  }
  assert(artifact.validation.every((entry: unknown) =>
    plainObject(entry) && entry.status === 'passed'),
  'all retained DSL validations must pass');
  return { providerIR, resolvedRule };
}

export function assertBooksGenerationCanaryEvidence(
  evidence: BooksGenerationCanaryEvidence,
  llm: Pick<LocalLLMConfig, 'providerAdapter' | 'model'>,
): {
  physicalProviderCalls: number;
  generationJobs: number;
  durableGenerationAttempts: number;
  providerRetries: number;
  dslRepairs: number;
  selectorRepairChildJobs: number;
  replayAttempts: number;
  cacheHit: boolean;
  degraded: boolean;
  fallbackUsed: boolean;
  inputTokens: number;
  outputTokens: number;
  selectorCatalogHash: string;
  attemptArtifactHash: string;
  providerCallArtifactHash: string;
} {
  const recordingId = stringValue(evidence.recording.id, 'persisted recording id');
  assert.equal(evidence.recording.redactionCount, 0,
    'persisted Books recording must have zero server redactions');
  assert.equal(evidence.recording.removedFieldCount, 0,
    'persisted Books recording must have zero server field removals');
  stringValue(evidence.recordingPayloadHash, 'authenticated recording payload hash');
  assert.deepEqual(evidence.recording, evidence.recordingCreation,
    'authenticated recording metadata changed after creation');
  const recordingHash = stringValue(
    evidence.recording.contentHash,
    'persisted recording contentHash',
  );
  const workspaceId = stringValue(evidence.recording.workspaceId, 'persisted recording workspaceId');
  assert.equal(evidence.normalizationJob.workspaceId, workspaceId,
    'normalization job workspace changed from the persisted recording');
  assert.equal(evidence.normalizationJob.recordingId, recordingId,
    'normalization job recording lineage changed');
  const requirementJobId = stringValue(
    evidence.normalizationJob.id,
    'normalization job id',
  );
  const submittedNormalizationJobId = stringValue(
    evidence.submittedNormalizationJob.id,
    'submitted normalization job id',
  );
  assert.equal(submittedNormalizationJobId, requirementJobId,
    'polled normalization job identity changed from its submission');
  assert.equal(evidence.normalizationStatusURL,
    `/api/v1/requirement-jobs/${encodeURIComponent(submittedNormalizationJobId)}`,
    'normalization submission status URL changed from its job identity');
  assert.equal(evidence.submittedNormalizationJob.workspaceId, workspaceId,
    'submitted normalization job workspace changed from the persisted recording');
  assert.equal(evidence.submittedNormalizationJob.recordingId, recordingId,
    'submitted normalization job recording lineage changed');
  assert.equal(evidence.submittedNormalizationJob.kind, 'normalize',
    'submitted Books requirement job must be normalize');
  assert.equal(evidence.submittedNormalizationJob.status, 'pending',
    'submitted Books normalization must begin pending');
  assert.equal(evidence.submittedNormalizationJob.source, 'manual',
    'submitted Books normalization must retain manual source');
  assert.equal(evidence.submittedNormalizationJob.attemptCount, 0,
    'submitted Books normalization must begin before any attempt');
  assert.equal(evidence.submittedNormalizationJob.maxAttempts, 1,
    'submitted Books normalization maxAttempts must be one');
  const submittedNormalizationRequestHash = stringValue(
    evidence.submittedNormalizationJob.requestHash,
    'submitted normalization requestHash',
  );
  assert.equal(evidence.normalizationJob.requestHash, submittedNormalizationRequestHash,
    'normalization request hash changed between submission and completion');
  const requirementId = assertManualNormalization(
    evidence.normalizationJob,
    evidence.normalizationAttempts,
  );
  assertRequirementLineage(
    evidence.draftRequirement,
    recordingId,
    requirementId,
    requirementJobId,
    'draft',
  );
  assertRequirementLineage(
    evidence.confirmedRequirement,
    recordingId,
    requirementId,
    requirementJobId,
    'confirmed',
  );
  assert.equal(evidence.draftRequirement.workspaceId, workspaceId,
    'draft requirement workspace changed');
  assert.equal(evidence.confirmedRequirement.workspaceId, workspaceId,
    'confirmed requirement workspace changed');
  assert.equal(evidence.draftRequirement.contentHash, evidence.confirmedRequirement.contentHash,
    'requirement confirmation changed the content hash');

  const submitted = evidence.submittedWorkflow;
  const submittedWorkflow = requiredObject(submitted.workflow, 'submitted DSL workflow');
  const submittedJob = requiredObject(submitted.job, 'submitted DSL job');
  const workflowId = stringValue(submittedWorkflow.id, 'submitted DSL workflow id');
  const workflow = evidence.workflow;
  const job = evidence.dslJob;
  const jobId = stringValue(submittedJob.id, 'submitted DSL job id');
  for (const candidate of [submittedWorkflow, workflow]) {
    assert.equal(candidate.workspaceId, workspaceId, 'DSL workflow workspace changed');
    assert.equal(candidate.requirementId, requirementId,
      'DSL workflow requirement lineage changed');
    assert.equal(candidate.recordingId, recordingId,
      'DSL workflow recording lineage changed');
    assert.equal(candidate.browserProfileId, BOOKS_GENERATION_PROFILE,
      'DSL workflow browser profile changed');
  }
  assert.equal(submittedJob.workspaceId, workspaceId, 'submitted DSL job workspace changed');
  assert.equal(submittedJob.workflowId, workflowId, 'submitted DSL job workflow lineage changed');
  assert.equal(submittedWorkflow.currentJobId, jobId, 'submitted workflow currentJobId changed');
  assert.equal(job.id, jobId, 'polled DSL job identity changed');
  assert.equal(job.workspaceId, workspaceId, 'polled DSL job workspace changed');
  assert.equal(job.workflowId, workflowId, 'polled DSL job workflow lineage changed');
  assert.equal(job.kind, 'generate', 'generation-only canary must create only a generate job');
  assert.equal(job.status, 'completed', 'Books provider generation must complete');
  assert.equal(job.provider, llm.providerAdapter, 'Books DSL job provider changed');
  assert.equal(job.model, llm.model, 'Books DSL job model changed');
  assert.equal(job.attemptCount, 1, 'Books DSL job must run exactly one durable attempt');
  assert.equal(job.maxAttempts, 1, 'Books DSL maxAttempts must be one');
  assert(numberValue(job.chunkCount, 'Books DSL chunkCount') > 0,
    'Books DSL chunkCount must be positive');
  assert.equal(job.completedChunks, job.chunkCount, 'Books DSL chunks must all complete');
  assertUncachedAndAuthoritative(safetyFlags(job), 'Books DSL job');

  assert.equal(workflow.id, workflowId, 'polled workflow identity changed');
  assert.equal(workflow.currentJobId, jobId,
    'workflow currentJobId changed, indicating a child job');
  assert.equal(workflow.status, 'awaiting_replay',
    'generation-only success must stop at awaiting_replay');
  assert.equal(workflow.repairCount, 0, 'generation-only workflow must have zero repairs');
  assert.equal(workflow.maxRepairs, 0, 'generation-only workflow maxRepairs must be zero');
  assert.equal(workflow.lastReplaySequence, 0, 'generation-only workflow must have zero replay attempts');
  assert(!workflow.approvedRuleId && !workflow.approvedVersion && !workflow.approvedAt,
    'generation-only workflow must not approve a rule');

  assert.equal(evidence.dslAttempts.length, 1,
    'Books DSL job must retain exactly one provider attempt report');
  const attempt = evidence.dslAttempts[0];
  assert.equal(attempt.attemptNumber, 1, 'Books provider attempt number must be one');
  assert.equal(attempt.workspaceId, workspaceId, 'Books provider attempt workspace changed');
  assert.equal(attempt.recordingId, recordingId, 'Books provider attempt recording lineage changed');
  assert.equal(attempt.jobType, 'dsl', 'Books provider attempt job type changed');
  assert.equal(attempt.jobId, jobId, 'Books provider attempt job lineage changed');
  assert.equal(attempt.outcome, 'succeeded', 'Books provider attempt must succeed');
  assert.equal(attempt.provider, llm.providerAdapter, 'Books attempt provider changed');
  assert.equal(attempt.model, llm.model, 'Books attempt model changed');
  assert.equal(attempt.callCount, 1, 'Books attempt must record exactly one provider call');
  assert.equal(attempt.promptVersion, job.promptVersion,
    'Books provider attempt prompt version changed from the DSL job');
  assert.equal(attempt.recordingHash, recordingHash,
    'Books provider attempt recording hash changed');
  assert.equal(attempt.requirementHash, evidence.confirmedRequirement.contentHash,
    'Books provider attempt requirement hash changed');
  const attemptFlags = Array.isArray(attempt.safetyFlags)
    ? attempt.safetyFlags.map(String)
    : [];
  for (const flag of [
    'artifact-capture:passed',
    'artifact-replayable:true',
    'provider-ir-source:bound',
  ]) {
    assert(attemptFlags.includes(flag), `Books provider attempt must expose ${flag}`);
  }
  assert(!attemptFlags.some((flag) =>
    flag === 'artifact-replayable:false'
    || flag === 'artifact-result-redacted:true'
    || flag === 'artifact-capture:bounded-fallback'),
  'Books provider attempt must retain complete replayable artifact evidence');
  assert.equal(attempt.replayable, true, 'Books provider attempt must be replayable evidence');
  assert.equal(sumProviderAttemptCalls(evidence.dslAttempts, 'Books DSL attempts'), 1,
    'Books attempt list must prove exactly one physical provider call');
  for (const name of [
    'recordingHash',
    'requirementHash',
    'baselineHash',
    'selectorCatalogHash',
    'artifactHash',
  ]) {
    stringValue(attempt[name], `Books attempt ${name}`);
  }

  const calls = evidence.dslAttemptDetail.calls as JSONRecord[];
  const onlyCall = calls[0];
  assert.equal(onlyCall.workspaceId, workspaceId, 'Books provider call workspace changed');
  assert.equal(onlyCall.recordingId, recordingId, 'Books provider call recording lineage changed');
  assert.equal(onlyCall.jobType, 'dsl', 'Books provider call job type changed');
  assert.equal(onlyCall.jobId, jobId, 'Books provider call job lineage changed');
  assert.equal(onlyCall.attemptNumber, 1, 'Books provider call attempt number changed');
  assert.equal(onlyCall.callIndex, 1, 'Books provider call index must be one');
  assert.equal(onlyCall.providerAttempt, 1, 'Books provider attempt lineage must be one');
  assert.equal(onlyCall.callKind, 'provider_response', 'Books success must use one provider response');
  assert.equal(onlyCall.cacheHit, false, 'Books provider call must not be a cache hit');
  assert.equal(onlyCall.provider, llm.providerAdapter, 'Books provider call adapter changed');
  assert.equal(onlyCall.model, llm.model, 'Books provider call model changed');
  assert.equal(onlyCall.phase, 'final', 'Books provider call must be the final strict output call');
  assert.equal(onlyCall.promptVersion, job.promptVersion,
    'Books provider call prompt version changed from the DSL job');
  assert.equal(onlyCall.httpStatus, 200, 'Books provider call must retain HTTP 200');
  assert.equal(onlyCall.originalBytesExact, true,
    'Books provider call must retain an exact original-byte count');
  assert.equal(onlyCall.redacted, false, 'Books provider call must not be redacted');
  assert.equal(onlyCall.truncated, false, 'Books provider call must not be truncated');
  assert.equal(onlyCall.replayable, true, 'Books provider call must be replayable evidence');
  stringValue(onlyCall.requestHash, 'Books provider call requestHash');
  stringValue(onlyCall.responseHash, 'Books provider call responseHash');
  stringValue(onlyCall.artifactHash, 'Books provider call artifactHash');
  assert.deepEqual(evidence.providerCallContent.call, onlyCall,
    'provider call content metadata changed from the retained provider call');
  const callContent = requiredObject(
    evidence.providerCallContent.content,
    'provider call content artifact',
  );
  assert.equal(callContent.schemaVersion, 'aegiscrawler.llm-provider-completion.v1',
    'provider call content schema changed');
  assert.equal(callContent.contentHash, onlyCall.responseHash,
    'provider call content hash changed from the retained response hash');
  assert.equal(callContent.truncated, false,
    'provider call content artifact must not be truncated');
  assert(plainObject(evidence.providerCallContent.content),
    'provider call content artifact must be retained');

  const inputTokens = numberValue(attempt.inputTokens, 'Books attempt inputTokens');
  const outputTokens = numberValue(attempt.outputTokens, 'Books attempt outputTokens');
  assert(inputTokens > 0, 'Books successful provider call must report positive input tokens');
  assert(outputTokens > 0, 'Books successful provider call must report positive output tokens');
  assert.equal(job.inputTokens, inputTokens,
    'Books DSL job input tokens changed from the provider attempt');
  assert.equal(job.outputTokens, outputTokens,
    'Books DSL job output tokens changed from the provider attempt');
  assert.equal(onlyCall.inputTokens, inputTokens,
    'Books provider call input tokens changed from the provider attempt');
  assert.equal(onlyCall.outputTokens, outputTokens,
    'Books provider call output tokens changed from the provider attempt');
  assertAttemptArtifact(evidence, attempt, onlyCall);
  assertEmptyProductMutations(evidence);

  return {
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
    inputTokens,
    outputTokens,
    selectorCatalogHash: attempt.selectorCatalogHash,
    attemptArtifactHash: attempt.artifactHash,
    providerCallArtifactHash: onlyCall.artifactHash,
  };
}

async function runLifecycle(
  api: BooksGenerationCanaryAPI,
  recording: PageAgentRecording,
  llm: LocalLLMConfig,
  budget: RealSiteCostBudget,
  acceptedRequestCostCeilingUSD: number,
  options: Required<Pick<RunOptions, 'now' | 'sleep' | 'timeoutMs' | 'sourceHead'>>,
): Promise<BooksGenerationCanaryReport> {
  const startedAt = options.now();
  let terminalDSLJobObserved = false;
  let physicalCountContradiction = false;
  const report: BooksGenerationCanaryReport = {
    schema: BOOKS_GENERATION_CANARY_SCHEMA,
    status: 'failed',
    promotionEligible: false,
    startedAt: startedAt.toISOString(),
    finishedAt: '',
    sourceHead: options.sourceHead,
    provider: llm.providerAdapter,
    model: llm.model,
    providerHostname: llm.providerHostname,
    acceptedRequestCostCeilingUSD,
    costBudgetUSD: budget.budgetUSD,
    inputUSDPerMillion: budget.inputUSDPerMillion,
    outputUSDPerMillion: budget.outputUSDPerMillion,
    physicalProviderCalls: 0,
  };
  try {
    const persistedSource = structuredClone(recording);
    delete persistedSource.meta.serverRecordingId;
    const recordingResponse = requiredObject(await api.admin<unknown>(
      'POST',
      '/api/v1/recordings',
      {
        recording: persistedSource,
        startedAt: persistedSource.meta.recordedAt,
        endedAt: persistedSource.meta.endedAt,
      },
    ), 'recording response');
    const recordingCreation = requiredObject(recordingResponse.recording, 'created recording');
    const recordingId = stringValue(recordingCreation.id, 'persisted recording id');
    const recordingReadResponse = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/recordings/${encodeURIComponent(recordingId)}`,
    ), 'recording read response');
    const persistedRecording = requiredObject(
      recordingReadResponse.recording,
      'persisted recording',
    );
    const persistedPayload = requiredObject(
      persistedRecording.payload,
      'persisted recording payload',
    );
    assert.deepEqual(persistedPayload, persistedSource,
      'server-restored Books recording changed from the authenticated local source');
    assert.equal(persistedRecording.redactionCount, 0,
      'server-restored Books recording must have zero redactions');
    assert.equal(persistedRecording.removedFieldCount, 0,
      'server-restored Books recording must have zero field removals');
    assert.equal(persistedRecording.actionCount, persistedPayload.events.length,
      'server-restored Books recording action count changed from its payload');
    assert.equal(persistedRecording.snapshotCount, persistedPayload.snapshots.length,
      'server-restored Books recording snapshot count changed from its payload');
    const persistedMetadata = structuredClone(persistedRecording);
    delete persistedMetadata.payload;
    assert.deepEqual(persistedMetadata, recordingCreation,
      'server-restored Books recording metadata changed after creation');
    const recordingPayloadHash = crypto.createHash('sha256')
      .update(JSON.stringify(persistedPayload))
      .digest('hex');
    report.recordingId = recordingId;
    report.recordingContentHash = stringValue(
      persistedRecording.contentHash,
      'persisted recording contentHash',
    );

    const normalizationSubmission = requiredObject(await api.admin<unknown>(
      'POST',
      `/api/v1/recordings/${encodeURIComponent(recordingId)}/requirement-jobs/normalize`,
      { requirement: BOOKS_CATEGORY_REQUIREMENT },
    ), 'normalization submission');
    const submittedNormalizationJob = requiredObject(
      normalizationSubmission.job,
      'submitted normalization job',
    );
    const requirementJobId = stringValue(submittedNormalizationJob.id, 'normalization job id');
    assert.equal(normalizationSubmission.statusUrl,
      `/api/v1/requirement-jobs/${encodeURIComponent(requirementJobId)}`,
      'normalization submission status URL changed from its job identity');
    assert.equal(submittedNormalizationJob.workspaceId, persistedRecording.workspaceId,
      'submitted normalization workspace changed from the persisted recording');
    assert.equal(submittedNormalizationJob.recordingId, recordingId,
      'submitted normalization recording lineage changed');
    assert.equal(submittedNormalizationJob.kind, 'normalize',
      'submitted Books requirement job must be normalize');
    assert.equal(submittedNormalizationJob.status, 'pending',
      'submitted Books normalization must begin pending');
    assert.equal(submittedNormalizationJob.source, 'manual',
      'submitted Books normalization must retain manual source');
    assert.equal(submittedNormalizationJob.attemptCount, 0,
      'submitted Books normalization must begin before any attempt');
    assert.equal(submittedNormalizationJob.maxAttempts, 1,
      'submitted Books normalization maxAttempts must be one');
    stringValue(submittedNormalizationJob.requestHash, 'submitted normalization requestHash');
    report.requirementJobId = requirementJobId;
    const normalizationJob = await waitForJob(
      api,
      `/api/v1/requirement-jobs/${encodeURIComponent(requirementJobId)}`,
      options,
    );
    assert.equal(normalizationJob.id, requirementJobId,
      'polled normalization job identity changed from its submission');
    assert.equal(normalizationJob.requestHash, submittedNormalizationJob.requestHash,
      'normalization request hash changed between submission and completion');
    const normalizationAttemptResponse = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/requirement-jobs/${encodeURIComponent(requirementJobId)}/provider-attempts`,
    ), 'normalization attempts response');
    assert(Array.isArray(normalizationAttemptResponse.attempts),
      'normalization attempts must be an array');
    const requirementId = assertManualNormalization(
      normalizationJob,
      normalizationAttemptResponse.attempts,
    );
    report.requirementId = requirementId;

    const draftResponse = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/requirements/${encodeURIComponent(requirementId)}`,
    ), 'draft requirement response');
    const draftRequirement = requiredObject(draftResponse.requirement, 'draft requirement');
    assertRequirementLineage(
      draftRequirement,
      recordingId,
      requirementId,
      requirementJobId,
      'draft',
    );
    const confirmedResponse = requiredObject(await api.admin<unknown>(
      'POST',
      `/api/v1/requirements/${encodeURIComponent(requirementId)}/confirm`,
    ), 'confirmed requirement response');
    const confirmedMutation = requiredObject(
      confirmedResponse.requirement,
      'confirmed requirement',
    );
    assertRequirementLineage(
      confirmedMutation,
      recordingId,
      requirementId,
      requirementJobId,
      'confirmed',
    );
    const confirmedReadResponse = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/requirements/${encodeURIComponent(requirementId)}`,
    ), 'confirmed requirement read response');
    const confirmedRequirement = requiredObject(
      confirmedReadResponse.requirement,
      'confirmed requirement read',
    );
    assertRequirementLineage(
      confirmedRequirement,
      recordingId,
      requirementId,
      requirementJobId,
      'confirmed',
    );
    assert.deepEqual(confirmedRequirement, confirmedMutation,
      'confirmed Books requirement changed between mutation and authenticated read');
    assert.equal(confirmedRequirement.contentHash, draftRequirement.contentHash,
      'confirmation changed the Books requirement hash');

    const baseline = convert(
      persistedPayload as unknown as PageAgentRecording,
      { ruleIdPrefix: 'ext' },
    );
    // From immediately before submission until durable attempts can be read,
    // zero is not provable: a transport error may occur after server receipt.
    report.physicalProviderCalls = null;
    const workflowSubmission = requiredObject(
      await api.admin<unknown>(
        'POST',
        `/api/v1/requirements/${encodeURIComponent(requirementId)}/dsl-workflows`,
        {
          browserProfileId: BOOKS_GENERATION_PROFILE,
          baselineRule: baseline,
        },
      ),
      'DSL workflow submission',
    );
    workflowSubmission.jobRequestBaseline = baseline;
    const submittedWorkflow = requiredObject(workflowSubmission.workflow, 'submitted workflow');
    const submittedJob = requiredObject(workflowSubmission.job, 'submitted DSL job');
    const workflowId = stringValue(submittedWorkflow.id, 'submitted workflow id');
    const dslJobId = stringValue(submittedJob.id, 'submitted DSL job id');
    report.workflowId = workflowId;
    report.dslJobId = dslJobId;

    const dslJob = await waitForJob(
      api,
      `/api/v1/dsl-jobs/${encodeURIComponent(dslJobId)}`,
      options,
    );
    terminalDSLJobObserved = true;
    // Give the disabled selector-repair gate one extra scheduler interval, then
    // prove the workflow still points to the original generate job.
    await options.sleep(POLL_INTERVAL_MS * 2);
    const workflowResponse = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/dsl-workflows/${encodeURIComponent(workflowId)}`,
    ), 'DSL workflow response');
    const workflow = requiredObject(workflowResponse.workflow, 'DSL workflow');
    const attemptsResponse = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/dsl-jobs/${encodeURIComponent(dslJobId)}/provider-attempts`,
    ), 'DSL provider attempts response');
    assert(Array.isArray(attemptsResponse.attempts), 'DSL provider attempts must be an array');
    report.physicalProviderCalls = sumProviderAttemptCalls(
      attemptsResponse.attempts,
      'Books DSL attempts',
    );
    assert.equal(attemptsResponse.attempts.length, 1,
      'DSL generation must retain exactly one provider attempt');
    const attemptDetail = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/dsl-jobs/${encodeURIComponent(dslJobId)}/provider-attempts/1`,
    ), 'DSL provider attempt detail');
    assert(Array.isArray(attemptDetail.calls),
      'DSL provider attempt detail calls must be an array');
    const summarizedPhysicalCalls = report.physicalProviderCalls;
    if (summarizedPhysicalCalls === attemptDetail.calls.length) {
      report.physicalProviderCalls = summarizedPhysicalCalls;
    } else if (summarizedPhysicalCalls === 0 && attemptDetail.calls.length > 0) {
      // A retained detail row directly proves these calls even if an older
      // summary incorrectly reports zero.
      report.physicalProviderCalls = attemptDetail.calls.length;
    } else {
      // A non-zero summary/detail contradiction cannot prove an exact total.
      report.physicalProviderCalls = null;
      physicalCountContradiction = true;
    }
    assert.equal(summarizedPhysicalCalls, attemptDetail.calls.length,
      'DSL provider attempt summary/detail physical-call counts disagree');
    assert.equal(attemptDetail.calls.length, 1,
      'DSL provider attempt detail must expose exactly one physical call');
    const callId = stringValue(attemptDetail.calls[0].id, 'DSL provider call id');
    const providerCallContent = requiredObject(await api.admin<unknown>(
      'GET',
      `/api/v1/dsl-jobs/${encodeURIComponent(dslJobId)}`
      + `/provider-attempts/1/calls/${encodeURIComponent(callId)}/content`,
    ), 'provider call content');
    const tasks = requiredObject(await api.admin<unknown>(
      'GET',
      '/admin/tasks?limit=1&offset=0',
    ), 'task list');
    const rules = requiredObject(await api.admin<unknown>(
      'GET',
      '/admin/rules?limit=1&offset=0',
    ), 'rule list');

    const evidence: BooksGenerationCanaryEvidence = {
      recordingCreation,
      recording: persistedMetadata,
      recordingPayloadHash,
      submittedNormalizationJob,
      normalizationStatusURL: stringValue(
        normalizationSubmission.statusUrl,
        'normalization submission statusUrl',
      ),
      normalizationJob,
      normalizationAttempts: normalizationAttemptResponse.attempts,
      draftRequirement,
      confirmedRequirement,
      submittedWorkflow: workflowSubmission,
      workflow,
      dslJob,
      dslAttempts: attemptsResponse.attempts,
      dslAttemptDetail: attemptDetail,
      providerCallContent,
      tasks,
      rules,
    };
    report.evidence = evidence;
    report.providerIRAvailable = plainObject(attemptDetail.artifact?.providerIR);
    report.resolvedRuleAvailable = plainObject(attemptDetail.artifact?.resolvedRule);
    const validated = assertBooksGenerationCanaryEvidence(evidence, llm);
    report.generationJobs = validated.generationJobs;
    report.durableGenerationAttempts = validated.durableGenerationAttempts;
    report.providerRetries = validated.providerRetries;
    report.dslRepairs = validated.dslRepairs;
    report.selectorRepairChildJobs = validated.selectorRepairChildJobs;
    report.replayAttempts = validated.replayAttempts;
    report.cacheHit = validated.cacheHit;
    report.degraded = validated.degraded;
    report.fallbackUsed = validated.fallbackUsed;
    report.actualInputTokens = validated.inputTokens;
    report.actualOutputTokens = validated.outputTokens;
    report.estimatedCostUpperBoundUSD = estimateCostUpperBound(
      budget,
      validated.inputTokens,
      validated.outputTokens,
    );
    assert(report.estimatedCostUpperBoundUSD <= budget.budgetUSD,
      `Books generation cost $${report.estimatedCostUpperBoundUSD.toFixed(6)} exceeds `
      + `$${budget.budgetUSD.toFixed(2)}`);
    report.selectorCatalogHash = validated.selectorCatalogHash;
    report.attemptArtifactHash = validated.attemptArtifactHash;
    report.providerCallArtifactHash = validated.providerCallArtifactHash;
    report.status = 'passed';
  } catch (error) {
    let countRecoveryError = '';
    if (
      report.physicalProviderCalls === null
      && terminalDSLJobObserved
      && report.dslJobId
      && !physicalCountContradiction
    ) {
      try {
        const attemptsResponse = requiredObject(await api.admin<unknown>(
          'GET',
          `/api/v1/dsl-jobs/${encodeURIComponent(report.dslJobId)}/provider-attempts`,
        ), 'DSL provider attempts recovery response');
        assert(Array.isArray(attemptsResponse.attempts),
          'DSL provider attempts recovery must be an array');
        report.physicalProviderCalls = sumProviderAttemptCalls(
          attemptsResponse.attempts,
          'Books DSL attempts recovery',
        );
      } catch (countError) {
        countRecoveryError = countError instanceof Error
          ? countError.message
          : String(countError);
      }
    }
    report.error = error instanceof Error ? error.message : String(error);
    if (countRecoveryError) {
      report.error += `; physical provider call count remains unknown: ${countRecoveryError}`;
    }
  } finally {
    report.finishedAt = options.now().toISOString();
  }
  return report;
}

export async function runBooksGenerationCanary(
  api: BooksGenerationCanaryAPI,
  recording: PageAgentRecording,
  llm: LocalLLMConfig,
  budget: RealSiteCostBudget,
  acceptedRequestCostCeilingUSD: number,
  options: RunOptions = {},
): Promise<BooksGenerationCanaryReport> {
  return runLifecycle(api, recording, llm, budget, acceptedRequestCostCeilingUSD, {
    now: options.now ?? (() => new Date()),
    sleep: options.sleep ?? sleep,
    timeoutMs: options.timeoutMs ?? TERMINAL_TIMEOUT_MS,
    sourceHead: options.sourceHead ?? 'unknown',
  });
}

function writePrivateJSON(directory: string, name: string, value: unknown): string {
  const file = path.join(directory, name);
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  return file;
}

function sourceHead(): string {
  try {
    return execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: path.resolve(__dirname, '..', '..'),
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
  } catch {
    return 'unknown';
  }
}

function privateJSONDigest(value: unknown): string {
  return crypto.createHash('sha256')
    .update(`${JSON.stringify(value, null, 2)}\n`)
    .digest('hex');
}

function appendReportFailure(
  report: BooksGenerationCanaryReport,
  error: unknown,
  now: () => Date,
): void {
  const message = error instanceof Error ? error.message : String(error);
  report.status = 'failed';
  report.finishedAt = now().toISOString();
  report.error = report.error ? `${report.error}; ${message}` : message;
}

function initialCommandFailureReport(
  env: NodeJS.ProcessEnv,
  head: string,
  now: () => Date,
): BooksGenerationCanaryReport {
  const timestamp = now().toISOString();
  return {
    schema: BOOKS_GENERATION_CANARY_SCHEMA,
    status: 'failed',
    promotionEligible: false,
    startedAt: timestamp,
    finishedAt: timestamp,
    sourceHead: head,
    provider: env.AEGIS_LOCAL_LLM_PROVIDER?.trim() ?? '',
    model: env.AEGIS_LOCAL_LLM_MODEL?.trim() ?? '',
    providerHostname: '',
    acceptedRequestCostCeilingUSD: 0,
    costBudgetUSD: BOOKS_COST_BUDGET_USD,
    inputUSDPerMillion: 0,
    outputUSDPerMillion: 0,
    physicalProviderCalls: 0,
  };
}

export interface BooksGenerationCanaryCommandDependencies {
  env?: NodeJS.ProcessEnv;
  now?: () => Date;
  head?: () => string;
  makeArtifactsDirectory?: () => string;
  loadRecording?: () => PageAgentRecording;
  validateRecording?: typeof assertBooksRecording;
  forecastRequest?: typeof forecastBooksDirectRequest;
  loadLLM?: (env: NodeJS.ProcessEnv) => LocalLLMConfig;
  bootServer?: typeof bootLiveServer;
  shutdownServer?: typeof shutdownLiveServer;
  runCanary?: typeof runBooksGenerationCanary;
  writeJSON?: typeof writePrivateJSON;
}

export interface BooksGenerationCanaryCommandResult {
  report: BooksGenerationCanaryReport;
  summary: JSONRecord;
  artifactsDir?: string;
  reportFile?: string;
  reportSHA256?: string;
}

function safeSummaryError(
  value: string | undefined,
  secrets: readonly string[],
): string | undefined {
  if (!value) return undefined;
  let safe = value;
  for (const secret of secrets) {
    if (secret) safe = safe.replaceAll(secret, '[REDACTED]');
  }
  return safe.slice(0, 2000);
}

export async function executeBooksGenerationCanaryCommand(
  dependencies: BooksGenerationCanaryCommandDependencies = {},
): Promise<BooksGenerationCanaryCommandResult> {
  const env = dependencies.env ?? process.env;
  const now = dependencies.now ?? (() => new Date());
  const head = (dependencies.head ?? sourceHead)();
  const writeJSON = dependencies.writeJSON ?? writePrivateJSON;
  let artifactsDir: string | undefined;
  let server: LiveServerEnvironment | undefined;
  let report = initialCommandFailureReport(env, head, now);
  let apiKey = '';
  let providerBaseURL = '';
  let providerHostname = '';
  try {
    const makeArtifactsDirectory = dependencies.makeArtifactsDirectory
      ?? (() => fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-books-generation-canary-')));
    artifactsDir = makeArtifactsDirectory();
    fs.chmodSync(artifactsDir, 0o700);
    assertBooksGenerationCanaryEnvironment(env);
    // Authenticate and validate the local recording before provider
    // credentials are opened or a server/provider can be reached.
    const recording = (dependencies.loadRecording ?? (() =>
      loadReusableBooksRecording(reusableBooksRecordingPaths())))();
    (dependencies.validateRecording ?? assertBooksRecording)(recording);
    const directRequest = (dependencies.forecastRequest ?? forecastBooksDirectRequest)(recording);
    assert(directRequest.withinDirectEnvelope,
      `Books recording requires ${directRequest.requiredDirectBytes} direct-request bytes, `
      + `over the ${directRequest.availableDirectBytes}-byte one-call envelope`);
    const budget = capRealSiteCostBudget(
      resolveRealSiteCostBudget(env),
      BOOKS_COST_BUDGET_USD,
    );
    const acceptedRequestCostCeilingUSD = estimateContextWindowCostUpperBound(
      budget,
      BOOKS_DEEPSEEK_CONTEXT_TOKENS,
      BOOKS_MAX_OUTPUT_TOKENS,
    );
    assertBooksAcceptedRequestCostAuthorized(acceptedRequestCostCeilingUSD);

    const llm = (dependencies.loadLLM ?? loadLocalLLMConfig)(env);
    apiKey = llm.apiKey;
    providerBaseURL = llm.providerBaseURL;
    providerHostname = llm.providerHostname;
    report = {
      ...report,
      provider: llm.providerAdapter,
      model: llm.model,
      providerHostname: llm.providerHostname,
      acceptedRequestCostCeilingUSD,
      costBudgetUSD: budget.budgetUSD,
      inputUSDPerMillion: budget.inputUSDPerMillion,
      outputUSDPerMillion: budget.outputUSDPerMillion,
    };
    server = await (dependencies.bootServer ?? bootLiveServer)({ llm });
    report = await (dependencies.runCanary ?? runBooksGenerationCanary)(
      server.api as LiveAPI,
      recording,
      llm,
      budget,
      acceptedRequestCostCeilingUSD,
      { sourceHead: head },
    );
  } catch (error) {
    appendReportFailure(report, error, now);
  } finally {
    if (server) {
      try {
        await (dependencies.shutdownServer ?? shutdownLiveServer)(server, true);
      } catch (error) {
        appendReportFailure(report, new Error(
          `server cleanup failed: ${error instanceof Error ? error.message : String(error)}`,
        ), now);
      }
    }
  }

  if (server && artifactsDir) {
    try {
      let diagnostics = server.diagnostics.value;
      for (const secret of [apiKey, providerBaseURL, providerHostname]) {
        if (secret) diagnostics = diagnostics.replaceAll(secret, '[REDACTED]');
      }
      writeJSON(artifactsDir, 'server-diagnostics.json', {
        diagnostics: diagnostics.slice(-8000),
      });
    } catch (error) {
      appendReportFailure(report, new Error(
        `diagnostics artifact write failed: ${error instanceof Error ? error.message : String(error)}`,
      ), now);
    }
  }

  let reportFile: string | undefined;
  let reportSHA256: string | undefined;
  if (artifactsDir) {
    try {
      reportFile = writeJSON(artifactsDir, 'report.json', report);
      reportSHA256 = privateJSONDigest(report);
    } catch (error) {
      appendReportFailure(report, new Error(
        `report artifact write failed: ${error instanceof Error ? error.message : String(error)}`,
      ), now);
      try {
        reportFile = writeJSON(artifactsDir, 'report-failed.json', report);
        reportSHA256 = privateJSONDigest(report);
      } catch {
        reportFile = undefined;
        reportSHA256 = undefined;
      }
    }
  }

  const summary = {
    schema: report.schema,
    status: report.status,
    promotionEligible: false,
    sourceHead: report.sourceHead,
    provider: report.provider,
    model: report.model,
    physicalProviderCalls: report.physicalProviderCalls,
    generationJobs: report.generationJobs,
    durableGenerationAttempts: report.durableGenerationAttempts,
    providerRetries: report.providerRetries,
    dslRepairs: report.dslRepairs,
    selectorRepairChildJobs: report.selectorRepairChildJobs,
    replayAttempts: report.replayAttempts,
    cacheHit: report.cacheHit,
    degraded: report.degraded,
    fallbackUsed: report.fallbackUsed,
    actualInputTokens: report.actualInputTokens,
    actualOutputTokens: report.actualOutputTokens,
    estimatedCostUpperBoundUSD: report.estimatedCostUpperBoundUSD,
    providerIRAvailable: report.providerIRAvailable,
    resolvedRuleAvailable: report.resolvedRuleAvailable,
    reportFile,
    reportSHA256,
    error: safeSummaryError(report.error, [apiKey, providerBaseURL, providerHostname]),
  };
  return { report, summary, artifactsDir, reportFile, reportSHA256 };
}

async function main(): Promise<void> {
  let result: BooksGenerationCanaryCommandResult | undefined;
  try {
    result = await executeBooksGenerationCanaryCommand();
    const output = `${JSON.stringify(result.summary, null, 2)}\n`;
    if (result.report.status === 'passed') process.stdout.write(output);
    else {
      process.stderr.write(output);
      process.exitCode = 1;
    }
  } catch (error) {
    process.exitCode = 1;
    const fallback = {
      schema: BOOKS_GENERATION_CANARY_SCHEMA,
      status: 'failed',
      promotionEligible: false,
      physicalProviderCalls: result?.report.physicalProviderCalls ?? null,
      error: error instanceof Error ? error.message : String(error),
    };
    process.stderr.write(`${JSON.stringify(fallback)}\n`);
  }
}

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`${JSON.stringify({
      schema: BOOKS_GENERATION_CANARY_SCHEMA,
      status: 'failed',
      promotionEligible: false,
      physicalProviderCalls: null,
      error: error instanceof Error ? error.message : String(error),
    })}\n`);
    process.exitCode = 1;
  });
}
