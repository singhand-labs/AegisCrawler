import type { Rule, Action, RuleHooks } from '../types/rule';

export type { Rule, Action, RuleHooks };
export type RuleApprovalStatus = Rule['approvalStatus'];
export type Priority = Rule['priority'];
export type TaskStatus =
  | 'pending'
  | 'leased'
  | 'running'
  | 'done'
  | 'failed'
  | 'cancelled'
  | 'waiting_for_human'
  | 'dead_letter';
export type ScheduleType = 'once' | 'cron';
export type CatchupMode = 'skip' | 'run_once';
export type JSONSchemaType = 'string' | 'number' | 'integer' | 'boolean' | 'object' | 'array' | 'null';

export interface JSONSchema {
  $schema?: string;
  title?: string;
  description?: string;
  type?: JSONSchemaType;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  additionalProperties?: boolean | JSONSchema;
  items?: JSONSchema;
  enum?: unknown[];
  default?: unknown;
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  minimum?: number;
  maximum?: number;
  minItems?: number;
  maxItems?: number;
  'x-secret'?: boolean;
  [key: string]: unknown;
}

export interface RuleVersion {
  workspaceId: string;
  ruleId: string;
  version: number;
  versionLabel: string;
  rule: Rule;
  contentHash: string;
  status: RuleApprovalStatus;
  owner: string;
  source: string;
  recordingId?: string;
  createdAt: string;
  approvedAt?: string;
  approvedBy?: string;
  rejectedAt?: string;
  rejectedBy?: string;
}

export interface RuleVersionContract {
  workspaceId: string;
  ruleId: string;
  version: number;
  inputSchema: JSONSchema;
  outputSchema: JSONSchema;
  browserProfileId: string;
  sourceKind?: string;
  sourceAuthority?: string;
  sourceArtifactHash?: string;
  sourceExportHash?: string;
  sourceWorkflowId?: string;
  createdAt: string;
}

export interface ListRuleVersionsResponse {
  ruleVersions: RuleVersion[];
  contracts: RuleVersionContract[];
}

export interface Task {
  id: string;
  workspaceId: string;
  ruleId: string;
  ruleVersion: string;
  ruleVersionNumber: number;
  status: TaskStatus;
  priority: Priority;
  variables: Record<string, unknown>;
  workerId: string;
  leaseUntil?: string;
  retryCount: number;
  maxRetries: number;
  inputSchema: JSONSchema;
  outputSchema: JSONSchema;
  sendPolicy: Record<string, unknown>;
  createdAt: string;
  updatedAt: string;
  scheduledAt?: string;
  completedAt?: string;
  scheduleId?: string;
  errorType: string;
  errorMessage: string;
  currentAttemptId: string;
  browserProfileId: string;
  sourceKind?: string;
  sourceAuthority?: string;
  sourceArtifactHash?: string;
  sourceExportHash?: string;
  sourceWorkflowId?: string;
  cancelRequested: boolean;
}

export interface Result {
  id: string;
  workspaceId: string;
  taskId: string;
  workerId: string;
  attemptId: string;
  idempotencyKey: string;
  sequence: number;
  kind: 'batch' | 'summary';
  payload: unknown;
  payloadHash: string;
  valid: boolean;
  validationError?: string;
  immediate: boolean;
  createdAt: string;
}

export interface ResultPage {
  batches: Result[];
  invalidBatches?: Result[];
  summary?: Result;
  total: number;
  limit: number;
  offset: number;
}

export interface TaskResultsResponse {
  taskId: string;
  ruleId: string;
  ruleVersion: string;
  ruleVersionNumber: number;
  outputSchema: JSONSchema;
  sourceKind?: string;
  sourceAuthority?: string;
  sourceArtifactHash?: string;
  sourceExportHash?: string;
  sourceWorkflowId?: string;
  page: ResultPage;
}

export type HumanInterventionStatus = 'pending' | 'approved' | 'rejected' | 'expired' | 'cancelled';

export interface HumanIntervention {
  id: string;
  workspaceId: string;
  taskId: string;
  attemptId: string;
  workerId: string;
  checkpointId: string;
  checkpoint: Record<string, unknown>;
  type: 'captcha' | '2fa' | 'confirmation' | 'generic';
  prompt: string;
  requestedAction: string;
  targetOrigin: string;
  status: HumanInterventionStatus;
  expiresAt: string;
  createdAt: string;
  decidedAt?: string;
  decidedBy?: string;
  decisionNote?: string;
  browserProfileId: string;
  ruleId: string;
  ruleVersionNumber: number;
}

export interface ListHumanInterventionsResponse {
  interventions: HumanIntervention[];
}

export interface LogEntry {
  id: string;
  taskId: string;
  workerId: string;
  level: string;
  message: string;
  extra: Record<string, unknown>;
  createdAt: string;
}

export interface AuditLog {
  id: string;
  actor: string;
  action: string;
  resourceType: string;
  resourceId: string;
  payload: Record<string, unknown>;
  createdAt: string;
}

export interface ListRulesResponse {
  rules: Rule[];
  total: number;
}

export interface ListTasksResponse {
  tasks: Task[];
  total: number;
}

export interface ListAuditLogsResponse {
  logs: AuditLog[];
  total: number;
}

export type MCPPermission = 'read' | 'write';

export interface MCPToken {
  id: string;
  workspaceId: string;
  name: string;
  tokenPrefix: string;
  permissions: MCPPermission[];
  createdBy: string;
  createdAt: string;
  expiresAt?: string;
  revokedAt?: string;
  lastUsedAt?: string;
  token?: string;
}

export interface ListMCPTokensResponse {
  tokens: MCPToken[];
}

export interface Schedule {
  id: string;
  workspaceId: string;
  ruleId: string;
  ruleVersion: string;
  ruleVersionNumber: number;
  name: string;
  type: ScheduleType;
  expression: string;
  enabled: boolean;
  nextRunAt?: string;
  lastRunAt?: string;
  variables: Record<string, unknown>;
  inputSchema: JSONSchema;
  browserProfileId: string;
  timezone: string;
  priority: Priority;
  maxRetries: number;
  catchup: CatchupMode;
  createdAt: string;
  updatedAt: string;
}

export interface ListSchedulesResponse {
  schedules: Schedule[];
  total: number;
}

export interface PreviewScheduleResponse {
  expression: string;
  timezone: string;
  runs: string[];
}

export interface CreateTaskRequest {
  ruleId: string;
  ruleVersion?: string;
  ruleVersionNumber?: number;
  variables?: Record<string, unknown>;
  priority?: Priority;
  maxRetries?: number;
  scheduledAt?: string;
  browserProfileId?: string;
}

export interface UpdateRuleRequest {
  version?: string;
  name?: string;
  domain?: unknown;
  urlPattern?: unknown;
  enabled?: boolean;
  priority?: Priority;
  entry?: string;
  variables?: Record<string, unknown>;
  selectors?: Record<string, unknown>;
  humanize?: Record<string, unknown>;
  steps?: Action[];
  output?: Record<string, unknown>;
  sendPolicy?: Record<string, unknown>;
  hooks?: RuleHooks;
  tags?: Record<string, unknown>;
  owner?: string;
  approvalStatus?: RuleApprovalStatus;
}

export interface SafetyFlag {
  stepIndex: string;
  action: string;
  reason: string;
}

export interface PatchOperation {
  op: 'add' | 'remove' | 'replace' | 'move' | 'copy' | 'test';
  path: string;
  value?: unknown;
  from?: string;
}

export interface RuleEnhancement {
  id: string;
  ruleId: string;
  baseline: Rule;
  enhanced: Rule;
  patch?: PatchOperation[];
  userHint: string;
  provider: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  suggestions: string[];
  safetyFlags: SafetyFlag[];
  status: 'pending' | 'accepted' | 'rejected';
  resultError?: string;
  createdAt: string;
}

export type LLMJobStatus = 'pending' | 'running' | 'completed' | 'failed';

export interface LLMJob {
  id: string;
  ruleId: string;
  baseline: Rule;
  recording: Record<string, unknown>;
  userHint: string;
  status: LLMJobStatus;
  resultRule?: Rule;
  resultError: string;
  provider: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  safetyFlags: SafetyFlag[];
  suggestions: string[];
  patch?: PatchOperation[];
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
}
