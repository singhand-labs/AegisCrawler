import type { LiveWorkflowTaskTerminalStatus } from './task-cases';

export const MODEL_FIXTURE_INPUT_TOKEN_BUDGET = 150_000;

interface ReplayRequirementField {
  name: string;
  type: string;
  description?: string;
}

interface ReplayRequirement {
  outputFields: ReplayRequirementField[];
}

type ReplaySemanticAssertion = (rows: unknown[], outputSchema: unknown) => void;

/**
 * Apply a scenario's semantic oracle to the server-persisted successful replay
 * before the immutable rule is approved. The schema is reconstructed from the
 * already-confirmed requirement, matching the execution contract that approval
 * will create without depending on a not-yet-created rule version.
 */
export function assertPersistedReplaySemanticRows(
  replays: Record<string, unknown>[],
  requirement: ReplayRequirement,
  assertRows: ReplaySemanticAssertion,
): void {
  const successful = replays.filter((replay) =>
    replay.status === 'succeeded' && replay.outputValid === true);
  if (successful.length !== 1) {
    throw new Error(`semantic replay qualification expected one successful persisted replay, found ${successful.length}`);
  }
  const output = successful[0].output;
  const rows = Array.isArray(output) ? output : output == null ? [] : [output];
  const fields = requirement.outputFields;
  if (!Array.isArray(fields) || fields.length === 0) {
    throw new Error('semantic replay qualification found no confirmed output fields');
  }
  const properties = Object.fromEntries(fields.map((field) => [field.name, {
    type: field.type,
    description: field.description ?? '',
  }]));
  assertRows(rows, {
    type: 'object',
    properties,
    required: fields.map((field) => field.name),
    additionalProperties: false,
  });
}

/** Exact fail-fast settings shared by every paid qualification scenario. */
export function strictLLMEnvironment(): Record<string, string> {
  return {
    LLM_TEMPERATURE: '0',
    LLM_MAX_RETRIES: '0',
    LLM_FALLBACK_PROVIDER: '',
    LLM_CACHE_TTL: '0s',
    LLM_ENABLE_REFLECTION: 'false',
    // Qualification defaults to one attempt (no generation feedback retry).
    // The opt-in override exists for feedback-retry verification runs, which
    // are explicitly not one-shot qualifications.
    LLM_JOB_MAX_ATTEMPTS: process.env.AEGIS_LIVE_LLM_JOB_MAX_ATTEMPTS ?? '1',
    // Qualification defaults to zero repairs (one-shot generation). The
    // opt-in override exists for feedback-repair verification runs, which are
    // explicitly not one-shot qualifications.
    LLM_DSL_MAX_REPAIRS: process.env.AEGIS_LIVE_LLM_DSL_MAX_REPAIRS ?? '0',
    LLM_DSL_SELECTOR_REPAIR_ENABLED: 'false',
    LLM_ALLOW_DEGRADED_FALLBACK: 'false',
    LLM_TEMPERATURE_COMPATIBILITY_RETRY: 'false',
    MAX_RETRIES: '0',
  };
}

export interface QualificationTaskSummary {
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

export interface QualificationTaskExpectation {
  caseName: string;
  expectedStatus: LiveWorkflowTaskTerminalStatus;
}

export interface StrictQualificationJob {
  kind: string;
  provider: string;
  model: string;
  attemptCount: number;
  maxAttempts: number;
  cacheHit: boolean;
  degraded: boolean;
}

export interface StrictQualificationEvidence {
  expectedProvider: string;
  expectedModel: string;
  jobs: StrictQualificationJob[];
  dslJobKinds: string[];
  repairCount: number;
  maxRepairs: number;
  replayAttempts: number;
  tasks: QualificationTaskSummary[];
  /** Defaults to the historical single successful `default` task. */
  taskExpectations?: QualificationTaskExpectation[];
}

/**
 * Keep every DSL job in the strict evidence set. Deterministic/manual
 * requirement normalization may make no provider call, but a deterministic
 * DSL job is a degraded generation path and must fail the provider assertion.
 */
export function strictQualificationJobs(
  requirementJobs: StrictQualificationJob[],
  dslJobs: StrictQualificationJob[],
): StrictQualificationJob[] {
  return [
    ...requirementJobs.filter((job) =>
      job.kind !== 'normalize' || (job.provider !== 'manual' && job.provider !== 'deterministic')),
    ...dslJobs,
  ];
}

/** Fail closed unless the paid workflow was exactly one first-pass path. */
export function assertStrictQualification(evidence: StrictQualificationEvidence): void {
  if (evidence.jobs.length === 0) throw new Error('strict qualification observed no durable LLM jobs');
  for (const job of evidence.jobs) {
    if (job.provider !== evidence.expectedProvider || job.model !== evidence.expectedModel) {
      throw new Error(`strict qualification used unexpected provider/model ${job.provider}/${job.model}`);
    }
    if (job.attemptCount !== 1 || job.maxAttempts !== 1) {
      throw new Error(`strict qualification job ${job.kind} did not use exactly one durable attempt`);
    }
    if (job.cacheHit) throw new Error(`strict qualification job ${job.kind} used cached output`);
    if (job.degraded) throw new Error(`strict qualification job ${job.kind} used degraded output`);
  }
  if (evidence.dslJobKinds.length === 0 || evidence.dslJobKinds.some((kind) => kind !== 'generate')) {
    throw new Error(`strict qualification observed non-generation DSL jobs: ${evidence.dslJobKinds.join(',')}`);
  }
  if (evidence.repairCount !== 0 || evidence.maxRepairs !== 0) {
    throw new Error(`strict qualification repair contract was ${evidence.repairCount}/${evidence.maxRepairs}`);
  }
  if (evidence.replayAttempts !== 1) {
    throw new Error(`strict qualification observed ${evidence.replayAttempts} replay attempts`);
  }
  const expectations = evidence.taskExpectations ?? [{
    caseName: 'default',
    expectedStatus: 'done' as const,
  }];
  if (evidence.tasks.length !== expectations.length) {
    throw new Error(
      `strict qualification observed ${evidence.tasks.length} tasks, expected ${expectations.length}`,
    );
  }
  for (let index = 0; index < expectations.length; index += 1) {
    const task = evidence.tasks[index];
    const expected = expectations[index];
    if (task.caseName !== expected.caseName
      || task.expectedStatus !== expected.expectedStatus
      || task.status !== expected.expectedStatus) {
      throw new Error(
        `strict qualification task case ${index} expected `
        + `${expected.caseName}:${expected.expectedStatus}, observed `
        + `${task.caseName}:${task.expectedStatus}:${task.status}`,
      );
    }
    if (task.maxRetries !== 0 || task.retryCount !== 0 || task.executionAttempts !== 1) {
      throw new Error(
        `strict qualification task ${task.caseName} retry contract was `
        + `max=${task.maxRetries} retries=${task.retryCount} attempts=${task.executionAttempts}`,
      );
    }
    if (!task.mcpAgreedWithAdmin || !task.mcpTokenRevoked) {
      throw new Error(
        `strict qualification task ${task.caseName} lacked per-case MCP agreement or token revocation`,
      );
    }
    if (task.expectedStatus === 'done') {
      if (task.validBatches < 1
        || task.invalidBatches !== 0
        || task.rows < 1
        || !task.summary
        || task.summaryStatus !== 'success') {
        throw new Error(`strict qualification task ${task.caseName} lacked complete successful results`);
      }
    } else {
      if (task.validBatches !== 0 || task.invalidBatches !== 0 || task.rows !== 0) {
        throw new Error(`strict qualification expected-failure task ${task.caseName} retained output`);
      }
      if (task.summary && task.summaryStatus !== 'failure') {
        throw new Error(
          `strict qualification expected-failure task ${task.caseName} retained a non-failure summary`,
        );
      }
    }
  }
}

export interface ScenarioVerificationProgress {
  tasks: QualificationTaskSummary[];
  resultsSummary: boolean;
  mcp: { agreedWithAdmin: boolean; tokenRevoked: boolean };
}

export interface VerificationSnapshot {
  results: {
    tasks: QualificationTaskSummary[];
    summary: boolean;
  };
  mcp: { agreedWithAdmin: boolean; tokenRevoked: boolean };
}

/**
 * Snapshot monotonic verification facts for the final report. A later suite
 * invariant (for example the cost guard) must not erase task, summary, MCP, or
 * revocation checks that already completed successfully.
 */
export function snapshotVerificationProgress(
  progress: ScenarioVerificationProgress,
): VerificationSnapshot {
  return {
    results: {
      tasks: [...progress.tasks],
      summary: progress.resultsSummary,
    },
    mcp: { ...progress.mcp },
  };
}
