export type LiveWorkflowTaskTerminalStatus = 'done' | 'failed' | 'dead_letter';

export interface LiveWorkflowTaskCaseContract {
  name: string;
  expectedStatus?: LiveWorkflowTaskTerminalStatus;
  rowBand?: [number, number];
}

export interface LiveWorkflowScenarioTaskCase<TOracleContext>
  extends LiveWorkflowTaskCaseContract {
  /** Bind this case's exact inputs from a fresh schema-derived input object. */
  bindInputs: (inputs: Record<string, unknown>) => Record<string, unknown>;
  /** Dynamic semantic oracle captured from this case's exact Worker page. */
  captureOracle?: (context: TOracleContext) => Promise<void>;
  /** Case-specific semantic result assertion. */
  assertRows?: (rows: unknown[], outputSchema: unknown) => void;
}

export interface ResolvedLiveWorkflowTaskCaseContract {
  name: string;
  expectedStatus: LiveWorkflowTaskTerminalStatus;
  rowBand?: [number, number];
}

export interface ResolvedLiveWorkflowScenarioTaskCase<TOracleContext>
  extends ResolvedLiveWorkflowTaskCaseContract {
  bindInputs: (inputs: Record<string, unknown>) => Record<string, unknown>;
  captureOracle?: (context: TOracleContext) => Promise<void>;
  assertRows?: (rows: unknown[], outputSchema: unknown) => void;
}

export interface LiveWorkflowTaskScenarioContract<TOracleContext> {
  rowBand: [number, number];
  assertRows?: (rows: unknown[], outputSchema: unknown) => void;
  adjustTaskInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  captureTaskOracle?: (context: TOracleContext) => Promise<void>;
  assertTaskRows?: (rows: unknown[], outputSchema: unknown) => void;
  taskCases?: readonly LiveWorkflowScenarioTaskCase<TOracleContext>[];
}

export interface LiveWorkflowTaskCaseResultEvidence {
  status: string;
  errorMessage?: string;
  validBatches: number;
  invalidBatches: number;
  rows: number;
  hasSummary: boolean;
  summaryStatus?: unknown;
}

/** Capture host-only oracle evidence without changing the executor's terminal status. */
export async function captureTaskOracle<T>(
  capture: ((context: T) => Promise<void>) | undefined,
  context: T,
): Promise<unknown> {
  try {
    await capture?.(context);
    return undefined;
  } catch (error) {
    return error;
  }
}

export interface LiveWorkflowTaskResultSurface {
  taskId: unknown;
  ruleId: unknown;
  ruleVersion: unknown;
  outputSchema: unknown;
  sourceKind?: unknown;
  sourceAuthority?: unknown;
  sourceArtifactHash?: unknown;
  sourceExportHash?: unknown;
  sourceWorkflowId?: unknown;
  total: unknown;
  batches: readonly unknown[];
  invalid: readonly unknown[];
  summary?: unknown;
}

const TASK_CASE_NAME_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const TERMINAL_STATUSES = new Set<LiveWorkflowTaskTerminalStatus>([
  'done',
  'failed',
  'dead_letter',
]);

function assertRowBand(value: unknown, label: string): asserts value is [number, number] {
  if (!Array.isArray(value)
    || value.length !== 2
    || !value.every((item) => Number.isSafeInteger(item))
    || value[0] < 1
    || value[1] < value[0]) {
    throw new Error(`${label} must be an inclusive positive integer [min, max] tuple`);
  }
}

/**
 * Resolve and validate the centrally reviewed task-case contract before a
 * WorkerHost or task is created.
 */
export function resolveTaskCaseContracts(
  cases: readonly LiveWorkflowTaskCaseContract[],
  defaultRowBand: [number, number],
): ResolvedLiveWorkflowTaskCaseContract[] {
  if (cases.length === 0) throw new Error('live workflow taskCases must not be empty');
  assertRowBand(defaultRowBand, 'live workflow scenario rowBand');

  const names = new Set<string>();
  return cases.map((taskCase, index) => {
    const label = `live workflow taskCases[${index}]`;
    const name = taskCase.name;
    if (typeof name !== 'string'
      || name !== name.trim()
      || name.length > 64
      || !TASK_CASE_NAME_PATTERN.test(name)) {
      throw new Error(`${label}.name must be a lowercase kebab-case identifier of at most 64 characters`);
    }
    if (names.has(name)) throw new Error(`live workflow task case name "${name}" is duplicated`);
    names.add(name);

    const expectedStatus = taskCase.expectedStatus ?? 'done';
    if (!TERMINAL_STATUSES.has(expectedStatus)) {
      throw new Error(`${label}.expectedStatus is not a reviewed terminal task status`);
    }
    if (expectedStatus !== 'done' && taskCase.rowBand !== undefined) {
      throw new Error(`${label} cannot declare a success rowBand for expected status ${expectedStatus}`);
    }
    const rowBand = expectedStatus === 'done' ? (taskCase.rowBand ?? defaultRowBand) : undefined;
    if (rowBand) assertRowBand(rowBand, `${label}.rowBand`);
    return { name, expectedStatus, rowBand };
  });
}

/** Resolve legacy and matrix scenario callbacks into one exact task-case list. */
export function resolveScenarioTaskCases<TOracleContext>(
  scenario: LiveWorkflowTaskScenarioContract<TOracleContext>,
): ResolvedLiveWorkflowScenarioTaskCase<TOracleContext>[] {
  if (!scenario.taskCases) {
    const [contract] = resolveTaskCaseContracts([{ name: 'default' }], scenario.rowBand);
    return [{
      ...contract,
      bindInputs: scenario.adjustTaskInputs ?? ((inputs) => inputs),
      captureOracle: scenario.captureTaskOracle,
      assertRows: scenario.assertTaskRows ?? scenario.assertRows,
    }];
  }

  if (scenario.adjustTaskInputs) {
    throw new Error('taskCases cannot be combined with the legacy adjustTaskInputs callback');
  }
  if (scenario.captureTaskOracle) {
    throw new Error('taskCases cannot be combined with the legacy captureTaskOracle callback');
  }
  if (scenario.assertTaskRows) {
    throw new Error('taskCases cannot be combined with the legacy assertTaskRows callback');
  }
  const contracts = resolveTaskCaseContracts(scenario.taskCases, scenario.rowBand);
  return contracts.map((contract, index) => {
    const taskCase = scenario.taskCases![index];
    if (typeof taskCase.bindInputs !== 'function') {
      throw new Error(`task case ${contract.name} requires an exact input-binding function`);
    }
    if (contract.expectedStatus === 'done') {
      if (!taskCase.assertRows && !scenario.assertRows) {
        throw new Error(`successful task case ${contract.name} requires a semantic row assertion`);
      }
    } else if (taskCase.captureOracle || taskCase.assertRows) {
      throw new Error(
        `expected-failure task case ${contract.name} cannot declare a success oracle or row assertion`,
      );
    }
    const assertRows = contract.expectedStatus === 'done'
      ? (rows: unknown[], outputSchema: unknown) => {
        scenario.assertRows?.(rows, outputSchema);
        taskCase.assertRows?.(rows, outputSchema);
      }
      : undefined;
    return {
      ...contract,
      bindInputs: taskCase.bindInputs,
      captureOracle: taskCase.captureOracle,
      assertRows,
    };
  });
}

/** Assert the business-result side of one reviewed task case. */
export function assertTaskCaseResult(
  taskCase: ResolvedLiveWorkflowTaskCaseContract,
  evidence: LiveWorkflowTaskCaseResultEvidence,
): void {
  if (evidence.status !== taskCase.expectedStatus) {
    const detail = evidence.errorMessage?.trim()
      ? `: ${evidence.errorMessage.trim()}`
      : '';
    throw new Error(
      `task case ${taskCase.name} expected ${taskCase.expectedStatus}, observed ${evidence.status}${detail}`,
    );
  }
  for (const [field, value] of Object.entries({
    validBatches: evidence.validBatches,
    invalidBatches: evidence.invalidBatches,
    rows: evidence.rows,
  })) {
    if (!Number.isSafeInteger(value) || value < 0) {
      throw new Error(`task case ${taskCase.name} reported invalid ${field}=${String(value)}`);
    }
  }

  if (taskCase.expectedStatus === 'done') {
    if (evidence.validBatches < 1) {
      throw new Error(`task case ${taskCase.name} persisted no valid result batch`);
    }
    if (evidence.invalidBatches !== 0) {
      throw new Error(
        `task case ${taskCase.name} quarantined ${evidence.invalidBatches} invalid batch(es)`,
      );
    }
    if (!evidence.hasSummary) {
      throw new Error(`task case ${taskCase.name} omitted its final execution summary`);
    }
    if (evidence.summaryStatus !== 'success') {
      throw new Error(
        `task case ${taskCase.name} completed but its summary status was `
        + `${String(evidence.summaryStatus)}`,
      );
    }
    const rowBand = taskCase.rowBand;
    if (!rowBand || evidence.rows < rowBand[0] || evidence.rows > rowBand[1]) {
      throw new Error(
        `task case ${taskCase.name} collected ${evidence.rows} row(s), outside `
        + `${rowBand?.[0] ?? '?'}..${rowBand?.[1] ?? '?'}`,
      );
    }
    return;
  }

  if (evidence.validBatches !== 0 || evidence.invalidBatches !== 0 || evidence.rows !== 0) {
    throw new Error(
      `expected-failure task case ${taskCase.name} retained stale or accepted output: `
      + `${evidence.rows} row(s), ${evidence.validBatches} valid batch(es), `
      + `${evidence.invalidBatches} invalid batch(es)`,
    );
  }
  if (evidence.hasSummary && evidence.summaryStatus !== 'failure') {
    throw new Error(
      `expected-failure task case ${taskCase.name} retained a non-failure summary: `
      + `${String(evidence.summaryStatus)}`,
    );
  }
}

function canonicalValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, item]) => [key, canonicalValue(item)]),
    );
  }
  return value;
}

function canonicalResult(value: unknown): unknown {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value;
  const result = value as Record<string, unknown>;
  return canonicalValue({
    id: result.id,
    attemptId: result.attemptId,
    sequence: result.sequence,
    kind: result.kind,
    payload: result.payload,
    valid: result.valid,
    validationError: result.validationError ?? '',
  });
}

function canonicalResultSurface(surface: LiveWorkflowTaskResultSurface): unknown {
  return canonicalValue({
    taskId: surface.taskId,
    ruleId: surface.ruleId,
    ruleVersion: surface.ruleVersion,
    outputSchema: surface.outputSchema,
    sourceKind: surface.sourceKind ?? '',
    sourceAuthority: surface.sourceAuthority ?? '',
    sourceArtifactHash: surface.sourceArtifactHash ?? '',
    sourceExportHash: surface.sourceExportHash ?? '',
    sourceWorkflowId: surface.sourceWorkflowId ?? '',
    total: surface.total,
    batches: surface.batches.map(canonicalResult),
    invalid: surface.invalid.map(canonicalResult),
    summary: surface.summary == null ? null : canonicalResult(surface.summary),
  });
}

function assertCompleteResultSurface(
  caseName: string,
  label: string,
  surface: LiveWorkflowTaskResultSurface,
): void {
  if (!Number.isSafeInteger(surface.total) || (surface.total as number) < 0) {
    throw new Error(
      `task case ${caseName} ${label} results reported invalid total=${String(surface.total)}`,
    );
  }
  if (surface.batches.length !== surface.total) {
    throw new Error(
      `task case ${caseName} ${label} result page is incomplete: `
      + `${surface.batches.length}/${String(surface.total)} validated batch(es)`,
    );
  }
}

/** Require stable business-result and immutable-lineage equality across APIs. */
export function assertTaskResultSurfacesAgree(
  caseName: string,
  admin: LiveWorkflowTaskResultSurface,
  mcp: LiveWorkflowTaskResultSurface,
): void {
  assertCompleteResultSurface(caseName, 'Admin API', admin);
  assertCompleteResultSurface(caseName, 'MCP', mcp);
  if (JSON.stringify(canonicalResultSurface(admin)) !== JSON.stringify(canonicalResultSurface(mcp))) {
    throw new Error(`task case ${caseName} MCP results disagree with Admin API`);
  }
}

/** Prove that a credential rejected a harmless probe after revocation. */
export async function assertRevokedCredentialRejected(
  label: string,
  probe: () => Promise<unknown>,
  isExpectedRejection: (error: unknown) => boolean,
): Promise<void> {
  try {
    await probe();
  } catch (error) {
    if (isExpectedRejection(error)) return;
    throw error;
  }
  throw new Error(`${label} remained usable after revocation`);
}

/** Execute cases one at a time so one active case owns the shared WorkerHost. */
export async function executeTaskCasesSequentially<TCase, TResult>(
  cases: readonly TCase[],
  execute: (taskCase: TCase, index: number) => Promise<TResult>,
  onCompleted?: (result: TResult, taskCase: TCase, index: number) => Promise<void> | void,
): Promise<TResult[]> {
  const results: TResult[] = [];
  for (let index = 0; index < cases.length; index += 1) {
    const result = await execute(cases[index], index);
    results.push(result);
    await onCompleted?.(result, cases[index], index);
  }
  return results;
}
