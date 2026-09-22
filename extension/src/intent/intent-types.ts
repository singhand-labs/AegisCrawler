import type { PageMark } from '../../../src/rule-generator';

export interface IntentCandidate {
  id: string;
  label: string;
  description: string;
  confidence: number;
  suggestedVariables?: string[];
  source?: 'llm' | 'synthetic';
}

export type RequirementValueType = 'string' | 'number' | 'boolean' | 'object' | 'array';

export interface RequirementInput {
  name: string;
  type: RequirementValueType;
  description: string;
  default?: unknown;
  constraints?: Record<string, unknown>;
  secret?: boolean;
}

export interface RequirementOutputField {
  name: string;
  type: RequirementValueType;
  description: string;
}

export interface CollectionRequirementSpec {
  title: string;
  description: string;
  requiredInputs: RequirementInput[];
  optionalInputs: RequirementInput[];
  outputFields: RequirementOutputField[];
  sampleOutput: Record<string, unknown>;
}

const requirementValueTypes = new Set<RequirementValueType>([
  'string', 'number', 'boolean', 'object', 'array',
]);
const requirementNamePattern = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;
const inputConstraintNames = new Set([
  'enum', 'minLength', 'maxLength', 'pattern',
  'minimum', 'maximum', 'minItems', 'maxItems',
]);
const unsafeRequirementTerms = [
  'password', 'passwd', 'cookie', 'token', 'credit card',
  '身份证', '密码', '登录凭证', '信用卡',
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

function requirementValueMatches(value: unknown, type: RequirementValueType): boolean {
  switch (type) {
    case 'string': return typeof value === 'string';
    case 'number': return typeof value === 'number' && Number.isFinite(value);
    case 'boolean': return typeof value === 'boolean';
    case 'object': return isRecord(value);
    case 'array': return Array.isArray(value);
  }
}

function jsonValuesEqual(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    return Array.isArray(left) && Array.isArray(right)
      && left.length === right.length
      && left.every((item, index) => jsonValuesEqual(item, right[index]));
  }
  if (!isRecord(left) || !isRecord(right)) return false;
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  return leftKeys.length === rightKeys.length
    && leftKeys.every((key) => Object.prototype.hasOwnProperty.call(right, key)
      && jsonValuesEqual(left[key], right[key]));
}

function valueSatisfiesConstraints(
  value: unknown,
  constraints: Record<string, unknown>,
): boolean {
  const allowed = constraints.enum;
  if (Array.isArray(allowed) && !allowed.some((candidate) => jsonValuesEqual(candidate, value))) {
    return false;
  }
  if (typeof value === 'number') {
    if (typeof constraints.minimum === 'number' && value < constraints.minimum) return false;
    if (typeof constraints.maximum === 'number' && value > constraints.maximum) return false;
  }
  if (typeof value === 'string') {
    const length = [...value].length;
    if (typeof constraints.minLength === 'number' && length < constraints.minLength) return false;
    if (typeof constraints.maxLength === 'number' && length > constraints.maxLength) return false;
    if (typeof constraints.pattern === 'string' && !new RegExp(constraints.pattern).test(value)) return false;
  }
  if (Array.isArray(value)) {
    if (typeof constraints.minItems === 'number' && value.length < constraints.minItems) return false;
    if (typeof constraints.maxItems === 'number' && value.length > constraints.maxItems) return false;
  }
  return true;
}

export function containsUnsafeRequirement(value: unknown): boolean {
  try {
    const encoded = JSON.stringify(value);
    return typeof encoded !== 'string'
      || unsafeRequirementTerms.some((term) => encoded.toLowerCase().includes(term));
  } catch {
    return true;
  }
}

function validRequirementField(value: unknown): value is RequirementOutputField {
  if (!isRecord(value)) return false;
  return typeof value.name === 'string'
    && requirementNamePattern.test(value.name)
    && typeof value.description === 'string'
    && value.description.trim().length > 0
    && typeof value.type === 'string'
    && requirementValueTypes.has(value.type as RequirementValueType);
}

function validRequirementInput(value: unknown): value is RequirementInput {
  if (!validRequirementField(value)) return false;
  if ('secret' in value && typeof value.secret !== 'boolean') return false;
  if ('default' in value && !requirementValueMatches(value.default, value.type)) return false;
  if (!('constraints' in value)) return true;
  if (!isRecord(value.constraints)) return false;
  for (const [name, constraint] of Object.entries(value.constraints)) {
    if (!inputConstraintNames.has(name)) return false;
    if (name === 'enum') {
      if (!Array.isArray(constraint) || !constraint.every((item) => requirementValueMatches(item, value.type))) {
        return false;
      }
      continue;
    }
    if (name === 'pattern') {
      if (typeof constraint !== 'string') return false;
      try {
        new RegExp(constraint);
      } catch {
        return false;
      }
      continue;
    }
    if (typeof constraint !== 'number' || !Number.isFinite(constraint)) return false;
  }
  return !('default' in value) || valueSatisfiesConstraints(value.default, value.constraints);
}

// isCollectionRequirementSpec validates the portable confirmed requirement at
// the MV3 resume boundary before it can influence replay controls or payloads.
export function isCollectionRequirementSpec(value: unknown): value is CollectionRequirementSpec {
  if (!isRecord(value)
    || typeof value.title !== 'string' || value.title.trim().length === 0
    || typeof value.description !== 'string' || value.description.trim().length === 0
    || !Array.isArray(value.requiredInputs)
    || !Array.isArray(value.optionalInputs)
    || !Array.isArray(value.outputFields) || value.outputFields.length === 0
    || !isRecord(value.sampleOutput)
    || containsUnsafeRequirement(value)) {
    return false;
  }
  const inputs = [...value.requiredInputs, ...value.optionalInputs];
  if (!inputs.every(validRequirementInput) || !value.outputFields.every(validRequirementField)) return false;
  const inputNames = new Set<string>();
  for (const input of inputs) {
    const key = input.name.toLowerCase();
    if (inputNames.has(key)) return false;
    inputNames.add(key);
  }
  const outputNames = new Set<string>();
  for (const output of value.outputFields) {
    const key = output.name.toLowerCase();
    if (outputNames.has(key) || !Object.prototype.hasOwnProperty.call(value.sampleOutput, output.name)
      || !requirementValueMatches(value.sampleOutput[output.name], output.type)) {
      return false;
    }
    outputNames.add(key);
  }
  return true;
}

export interface RequirementCandidate {
  id: string;
  confidence: number;
  requirement: CollectionRequirementSpec;
  source?: 'llm' | 'synthetic';
}

export interface RequirementJob {
  id: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  errorCode?: string;
  errorMessage?: string;
  requirementId?: string;
  chunkCount?: number;
  completedChunks?: number;
}

export interface LLMJobProgress {
  chunkCount: number;
  completedChunks: number;
}

export interface RequirementWorkflowResult {
  success: boolean;
  workflowV2: boolean;
  candidates?: RequirementCandidate[];
  requirement?: CollectionRequirementSpec;
  requirementId?: string;
  job?: RequirementJob;
  error?: string;
}

export type DSLWorkflowStatus =
  | 'generating'
  | 'awaiting_replay'
  | 'replaying'
  | 'repairing'
  | 'awaiting_confirmation'
  | 'failed'
  | 'approved';

export interface DSLWorkflow {
  id: string;
  requirementId: string;
  recordingId: string;
  status: DSLWorkflowStatus;
  browserProfileId: string;
  currentJobId?: string;
  repairCount: number;
  maxRepairs: number;
  provisionalRule?: unknown;
  provisionalYaml?: string;
  provisionalHash?: string;
  errorCode?: string;
  errorMessage?: string;
  approvedRuleId?: string;
  approvedVersion?: number;
}

export interface DSLJob {
  id: string;
  workflowId: string;
  kind: 'generate' | 'repair';
  status: 'pending' | 'running' | 'completed' | 'failed';
  errorCode?: string;
  errorMessage?: string;
  chunkCount?: number;
  completedChunks?: number;
}

export interface DSLReplayAttempt {
  id: string;
  workflowId: string;
  sequence: number;
  status: 'running' | 'succeeded' | 'failed';
  outputValid: boolean;
  errorCode?: string;
  errorMessage?: string;
}

export interface DSLWorkflowResult {
  success: boolean;
  workflow?: DSLWorkflow;
  requirement?: CollectionRequirementSpec;
  job?: DSLJob;
  replay?: DSLReplayAttempt;
  repairJob?: DSLJob;
  ruleVersion?: { ruleId: string; version: number };
  error?: string;
  code?: string;
  blocking_flags?: string[];
}

export interface PredictIntentResponse {
  candidates: IntentCandidate[];
  fallbackIntent: IntentCandidate;
  model: string;
  cacheHit: boolean;
}

export type InFlightOp =
  | 'candidates'
  | 'normalize'
  | 'confirm-requirement'
  | 'generate-dsl'
  | 'workflow-dsl'
  | 'start-replay'
  | 'complete-replay'
  | 'abort-replay'
  | 'confirm-workflow'
  | 'save-rule'
  | 'retry-requirement';

export type WizardStep = 'intent' | 'requirement' | 'requirement-confirmed' | 'preview' | 'replay' | 'confirm' | 'save';

export type ReplayProgress =
  | { type: 'log'; level: string; message: string; extra?: Record<string, unknown> }
  | { type: 'result'; payload: unknown; immediate?: boolean }
  | { type: 'status'; status: string; message?: string }
  | { type: 'snapshot'; snapshot: { name: string; type: string; data: string } };

export interface WizardState {
  step: WizardStep;
  candidates: IntentCandidate[];
  fallbackIntent: IntentCandidate;
  selectedIntent: IntentCandidate | null;
  customIntentDescription: string;
  workflowV2: boolean;
  candidatesRequested: boolean;
  requirementCandidates: RequirementCandidate[];
  selectedRequirementCandidate: RequirementCandidate | null;
  normalizedRequirement: CollectionRequirementSpec | null;
  requirementBaseline: string;
  requirementId: string | null;
  requirementJobId: string | null;
  requirementReviewReady: boolean;
  dslWorkflowId: string | null;
  dslJobId: string | null;
  replayAttemptId: string | null;
  browserProfileId: string;
  rule: unknown | null;
  yaml: string;
  confirmedSteps: Set<number>;
  ruleId: string | null;
  error: string | null;
  loading: boolean;
  inFlight: Set<InFlightOp>;
  replayLogs: ReplayProgress[];
  replayStatus: 'idle' | 'running' | 'success' | 'failure' | 'cancelled';
  replayError: string | null;
  replayVariables: Record<string, unknown>;
  replayExtracted: Record<string, unknown>;
  replayResults: unknown[];
  pageMarks: PageMark[];
}

export interface PredictIntentResult extends PredictIntentResponse {
  success?: boolean;
  error?: string;
  traceId?: string;
}

export interface GenerateDslResult {
  success?: boolean;
  rule?: unknown;
  yaml?: string;
  error?: string;
  traceId?: string;
}

export interface UploadConfirmedRuleResult {
  success?: boolean;
  ruleId?: string;
  error?: string;
}
