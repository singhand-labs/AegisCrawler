import * as childProcess from 'node:child_process';
import * as crypto from 'node:crypto';
import {
  QUALIFICATION_ARCHIVE_SCHEMA,
  bindQualificationArtifact,
  loadQualificationArchive,
  loadQualificationSidecar,
  saveQualificationSidecar,
  type BoundQualificationArtifact,
  type QualificationArtifactHash,
  type QualificationArchiveKind,
  type QualificationJSON,
} from './history';

export const QUALIFICATION_RUN_RECORD_SCHEMA =
  'aegiscrawler.qualification-run-record.v2' as const;

export type QualificationRunKind =
  | 'history-validation'
  | 'public-replay'
  | 'mv3-stability'
  | 'full-canary';

export interface QualificationGitState {
  commit: string;
  trackedWorktreeClean: boolean;
}

export interface QualificationProvisionalHashes {
  providerIRHash: QualificationArtifactHash;
  resolvedRuleHash: QualificationArtifactHash;
  validationHash: QualificationArtifactHash;
}

/**
 * This record is encrypted and immutable, but its observations are not an
 * authorization or harness attestation. The later integration slice must
 * derive authoritative records from durable harness/run state before
 * promotion can ever become available.
 */
export interface QualificationRunRecord {
  schemaVersion: typeof QUALIFICATION_RUN_RECORD_SCHEMA;
  recordId: string;
  runId: string;
  authority: 'unverified-local-record';
  archiveSchemaVersion: typeof QUALIFICATION_ARCHIVE_SCHEMA;
  archiveId: string;
  archivePayloadHash: QualificationArtifactHash;
  archiveKind: QualificationArchiveKind;
  scenario: string;
  commit: string;
  trackedWorktreeClean: true;
  runKind: QualificationRunKind;
  outcome: 'passed' | 'failed';
  startedAt: string;
  completedAt: string;
  requestHashes: QualificationArtifactHash[];
  provisional: QualificationProvisionalHashes;
  observations: BoundQualificationArtifact;
}

export interface CreateQualificationRunRecordInput {
  recordId?: string;
  runId?: string;
  archiveId: string;
  archivePayloadHash: QualificationArtifactHash;
  archiveKind: QualificationArchiveKind;
  scenario: string;
  runKind: QualificationRunKind;
  outcome: 'passed' | 'failed';
  startedAt: string;
  completedAt: string;
  requestHashes: QualificationArtifactHash[];
  provisional: QualificationProvisionalHashes;
  observations: unknown;
}

export interface QualificationRunRecordIds {
  historyValidation: string;
  publicReplay: string;
  mv3Stability: string;
  fullCanary: string;
}

export interface QualificationRunSequence {
  promotionEligible: false;
  sourceArchiveId: string;
  canaryArchiveId: string;
  scenario: string;
  commit: string;
  recordIds: string[];
}

const HASH_PATTERN = /^[0-9a-f]{64}$/;
const COMMIT_PATTERN = /^[0-9a-f]{40}$/;
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const SCENARIO_PATTERN = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/;
const MAX_CLOCK_SKEW_MS = 30_000;
const RUN_KINDS: QualificationRunKind[] = [
  'history-validation',
  'public-replay',
  'mv3-stability',
  'full-canary',
];

function asRecord(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)
      || Object.getPrototypeOf(value) !== Object.prototype) {
    throw new Error(`${label} must be a JSON object`);
  }
  return value as Record<string, unknown>;
}

function exactKeys(
  value: Record<string, unknown>,
  required: readonly string[],
  label: string,
): void {
  const expected = new Set(required);
  for (const key of required) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) {
      throw new Error(`${label} is missing ${key}`);
    }
  }
  for (const key of Object.keys(value)) {
    if (!expected.has(key)) throw new Error(`${label} contains unsupported field ${key}`);
  }
}

function canonicalTimestamp(value: unknown, label: string): string {
  if (typeof value !== 'string' || value.length > 64) {
    throw new Error(`${label} must be a canonical ISO timestamp`);
  }
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString() !== value) {
    throw new Error(`${label} must be a canonical ISO timestamp`);
  }
  if (parsed > Date.now() + MAX_CLOCK_SKEW_MS) {
    throw new Error(`${label} cannot be in the future`);
  }
  return value;
}

function requiredHash(value: unknown, label: string): QualificationArtifactHash {
  if (typeof value !== 'string' || !HASH_PATTERN.test(value)) {
    throw new Error(`${label} must be a lowercase SHA-256 hash`);
  }
  return value as QualificationArtifactHash;
}

function validateProvisional(value: unknown): QualificationProvisionalHashes {
  const provisional = asRecord(value, 'qualification provisional hashes');
  exactKeys(
    provisional,
    ['providerIRHash', 'resolvedRuleHash', 'validationHash'],
    'qualification provisional hashes',
  );
  return {
    providerIRHash: requiredHash(
      provisional.providerIRHash,
      'qualification provider IR hash',
    ),
    resolvedRuleHash: requiredHash(
      provisional.resolvedRuleHash,
      'qualification resolved rule hash',
    ),
    validationHash: requiredHash(
      provisional.validationHash,
      'qualification validation hash',
    ),
  };
}

function validateBoundArtifact(
  value: unknown,
  label: string,
): BoundQualificationArtifact {
  const artifact = asRecord(value, label);
  exactKeys(artifact, ['hash', 'value'], label);
  const rebound = bindQualificationArtifact(artifact.value);
  if (artifact.hash !== rebound.hash) {
    throw new Error(`${label} hash does not match its canonical value`);
  }
  return rebound;
}

export function validateQualificationRunRecord(value: unknown): QualificationRunRecord {
  const record = asRecord(value, 'qualification run record');
  exactKeys(record, [
    'schemaVersion', 'recordId', 'runId', 'authority', 'archiveSchemaVersion',
    'archiveId', 'archivePayloadHash', 'archiveKind', 'scenario', 'commit',
    'trackedWorktreeClean', 'runKind', 'outcome', 'startedAt', 'completedAt',
    'requestHashes', 'provisional', 'observations',
  ], 'qualification run record');
  if (record.schemaVersion !== QUALIFICATION_RUN_RECORD_SCHEMA
      || record.archiveSchemaVersion !== QUALIFICATION_ARCHIVE_SCHEMA) {
    throw new Error('unsupported qualification run-record schema');
  }
  if (typeof record.recordId !== 'string' || !UUID_PATTERN.test(record.recordId)
      || typeof record.runId !== 'string' || !UUID_PATTERN.test(record.runId)
      || typeof record.archiveId !== 'string' || !UUID_PATTERN.test(record.archiveId)) {
    throw new Error('qualification run-record identifiers must be canonical UUIDs');
  }
  if (record.authority !== 'unverified-local-record') {
    throw new Error('qualification run record cannot self-assert authoritative lineage');
  }
  const archivePayloadHash = requiredHash(
    record.archivePayloadHash,
    'qualification run-record archive hash',
  );
  if (record.archiveKind !== 'source-history' && record.archiveKind !== 'fresh-canary') {
    throw new Error('qualification run-record archive kind is unsupported');
  }
  if (typeof record.scenario !== 'string' || !SCENARIO_PATTERN.test(record.scenario)) {
    throw new Error('qualification run-record scenario must be a lowercase slug');
  }
  if (typeof record.commit !== 'string' || !COMMIT_PATTERN.test(record.commit)) {
    throw new Error('qualification run-record commit must be an exact lowercase Git commit');
  }
  if (record.trackedWorktreeClean !== true) {
    throw new Error('qualification run record requires a clean tracked worktree');
  }
  if (!RUN_KINDS.includes(record.runKind as QualificationRunKind)) {
    throw new Error('qualification run kind is unsupported');
  }
  if (record.outcome !== 'passed' && record.outcome !== 'failed') {
    throw new Error('qualification run outcome is unsupported');
  }
  const startedAt = canonicalTimestamp(record.startedAt, 'qualification run start time');
  const completedAt = canonicalTimestamp(record.completedAt, 'qualification run completion time');
  if (Date.parse(completedAt) < Date.parse(startedAt)) {
    throw new Error('qualification run completion predates its start');
  }
  if (!Array.isArray(record.requestHashes)
      || record.requestHashes.length === 0
      || record.requestHashes.length > 2_048
      || record.requestHashes.some((hash) =>
        typeof hash !== 'string' || !HASH_PATTERN.test(hash))) {
    throw new Error('qualification request hashes must be a non-empty ordered SHA-256 list');
  }
  const runKind = record.runKind as QualificationRunKind;
  const archiveKind = record.archiveKind as QualificationArchiveKind;
  if (runKind === 'full-canary' ? archiveKind !== 'fresh-canary' : archiveKind !== 'source-history') {
    throw new Error('qualification run kind does not match its archive kind');
  }
  return {
    schemaVersion: QUALIFICATION_RUN_RECORD_SCHEMA,
    recordId: record.recordId,
    runId: record.runId,
    authority: 'unverified-local-record',
    archiveSchemaVersion: QUALIFICATION_ARCHIVE_SCHEMA,
    archiveId: record.archiveId,
    archivePayloadHash,
    archiveKind,
    scenario: record.scenario,
    commit: record.commit,
    trackedWorktreeClean: true,
    runKind,
    outcome: record.outcome,
    startedAt,
    completedAt,
    requestHashes: [...record.requestHashes] as QualificationArtifactHash[],
    provisional: validateProvisional(record.provisional),
    observations: validateBoundArtifact(record.observations, 'qualification run observations'),
  };
}

export function readQualificationGitState(repoRoot: string): QualificationGitState {
  const commit = childProcess.execFileSync(
    'git',
    ['rev-parse', '--verify', 'HEAD'],
    { cwd: repoRoot, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] },
  ).trim();
  const status = childProcess.execFileSync(
    'git',
    ['status', '--porcelain=v1', '--untracked-files=no'],
    { cwd: repoRoot, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] },
  );
  if (!COMMIT_PATTERN.test(commit)) throw new Error('qualification requires an exact Git commit');
  return { commit, trackedWorktreeClean: status.trim().length === 0 };
}

/** Deterministic constructor for tests; its output remains explicitly unverified. */
export function createQualificationRunRecordForGitState(
  input: CreateQualificationRunRecordInput,
  gitState: QualificationGitState,
): QualificationRunRecord {
  if (!gitState.trackedWorktreeClean) {
    throw new Error('qualification run records require a clean tracked worktree');
  }
  return validateQualificationRunRecord({
    schemaVersion: QUALIFICATION_RUN_RECORD_SCHEMA,
    recordId: input.recordId ?? crypto.randomUUID(),
    runId: input.runId ?? crypto.randomUUID(),
    authority: 'unverified-local-record',
    archiveSchemaVersion: QUALIFICATION_ARCHIVE_SCHEMA,
    archiveId: input.archiveId,
    archivePayloadHash: input.archivePayloadHash,
    archiveKind: input.archiveKind,
    scenario: input.scenario,
    commit: gitState.commit,
    trackedWorktreeClean: true,
    runKind: input.runKind,
    outcome: input.outcome,
    startedAt: input.startedAt,
    completedAt: input.completedAt,
    requestHashes: input.requestHashes,
    provisional: input.provisional,
    observations: bindQualificationArtifact(input.observations),
  });
}

export function createQualificationRunRecord(
  projectRoot: string,
  input: CreateQualificationRunRecordInput,
): QualificationRunRecord {
  return createQualificationRunRecordForGitState(input, readQualificationGitState(projectRoot));
}

function runRecordSidecarName(runKind: QualificationRunKind, recordId: string): string {
  if (!UUID_PATTERN.test(recordId)) {
    throw new Error('qualification run-record id must be a canonical UUID');
  }
  return `run-${runKind}-${recordId}`;
}

function assertRecordBindsArchive(
  record: QualificationRunRecord,
  archive: ReturnType<typeof loadQualificationArchive>,
): void {
  const expectedRequests = archive.payload.attempt.calls.map((call) => call.requestHash);
  if (archive.payloadHash !== record.archivePayloadHash
      || archive.payload.archiveKind !== record.archiveKind
      || record.requestHashes.length !== expectedRequests.length
      || record.requestHashes.some((hash, index) => hash !== expectedRequests[index])
      || record.provisional.providerIRHash !== archive.payload.attempt.providerIR.hash
      || record.provisional.resolvedRuleHash !== archive.payload.attempt.resolvedRule.hash
      || record.provisional.validationHash !== archive.payload.attempt.validation.hash) {
    throw new Error('qualification run record does not bind the exact archive and provisional');
  }
  const capturedAt = Date.parse(archive.payload.capturedAt);
  const startedAt = Date.parse(record.startedAt);
  const completedAt = Date.parse(record.completedAt);
  if (record.archiveKind === 'source-history') {
    if (startedAt < capturedAt) {
      throw new Error('qualification source-history run predates its archive');
    }
  } else if (startedAt > capturedAt || completedAt < capturedAt) {
    throw new Error('qualification fresh-canary archive must be captured during its run');
  }
}

export function saveQualificationRunRecord(
  projectRoot: string,
  source: QualificationRunRecord,
): string {
  const record = validateQualificationRunRecord(source);
  const archive = loadQualificationArchive(
    projectRoot,
    record.scenario,
    record.archiveId,
  );
  assertRecordBindsArchive(record, archive);
  return saveQualificationSidecar(
    projectRoot,
    record.scenario,
    record.archiveId,
    runRecordSidecarName(record.runKind, record.recordId),
    `qualification-run:${record.runKind}`,
    record,
  );
}

export function loadQualificationRunRecord(
  projectRoot: string,
  scenario: string,
  archiveId: string,
  runKind: QualificationRunKind,
  recordId: string,
): QualificationRunRecord {
  const value = loadQualificationSidecar(
    projectRoot,
    scenario,
    archiveId,
    runRecordSidecarName(runKind, recordId),
    `qualification-run:${runKind}`,
  );
  const record = validateQualificationRunRecord(value);
  if (record.recordId !== recordId
      || record.runKind !== runKind
      || record.archiveId !== archiveId
      || record.scenario !== scenario) {
    throw new Error('qualification run-record identity does not match its authenticated path');
  }
  const archive = loadQualificationArchive(projectRoot, scenario, archiveId);
  assertRecordBindsArchive(record, archive);
  return record;
}

function assertRecordForSequence(
  record: QualificationRunRecord,
  expectedKind: QualificationRunKind,
  commit: string,
): void {
  if (record.runKind !== expectedKind
      || record.commit !== commit
      || !record.trackedWorktreeClean
      || record.outcome !== 'passed') {
    throw new Error('qualification run sequence contains mismatched or failed records');
  }
}

/**
 * Validate only storage identity, hashes, distinct runs, and chronology.
 * The returned literal false is intentional: these local records cannot
 * attest real MV3/public execution or consumed user authorization.
 */
export function validateUnverifiedQualificationRunSequenceForGitState(
  projectRoot: string,
  scenario: string,
  sourceArchiveId: string,
  canaryArchiveId: string,
  recordIds: QualificationRunRecordIds,
  gitState: QualificationGitState,
): QualificationRunSequence {
  if (!gitState.trackedWorktreeClean) {
    throw new Error('qualification run sequence requires a clean tracked worktree');
  }
  const source = loadQualificationArchive(projectRoot, scenario, sourceArchiveId);
  const canary = loadQualificationArchive(projectRoot, scenario, canaryArchiveId);
  if (source.payload.archiveKind !== 'source-history'
      || canary.payload.archiveKind !== 'fresh-canary'
      || canary.payload.sourceHistory?.archiveId !== sourceArchiveId
      || canary.payload.sourceHistory.payloadHash !== source.payloadHash) {
    throw new Error('qualification run sequence requires explicitly linked source and canary archives');
  }
  const records = [
    loadQualificationRunRecord(
      projectRoot, scenario, sourceArchiveId, 'history-validation', recordIds.historyValidation,
    ),
    loadQualificationRunRecord(
      projectRoot, scenario, sourceArchiveId, 'public-replay', recordIds.publicReplay,
    ),
    loadQualificationRunRecord(
      projectRoot, scenario, sourceArchiveId, 'mv3-stability', recordIds.mv3Stability,
    ),
    loadQualificationRunRecord(
      projectRoot, scenario, canaryArchiveId, 'full-canary', recordIds.fullCanary,
    ),
  ];
  const kinds: QualificationRunKind[] = [
    'history-validation', 'public-replay', 'mv3-stability', 'full-canary',
  ];
  records.forEach((record, index) =>
    assertRecordForSequence(record, kinds[index], gitState.commit));
  if (new Set(records.map((record) => record.recordId)).size !== records.length
      || new Set(records.map((record) => record.runId)).size !== records.length) {
    throw new Error('qualification run sequence requires distinct record and run identities');
  }
  for (let index = 1; index < records.length; index += 1) {
    if (Date.parse(records[index].startedAt) < Date.parse(records[index - 1].completedAt)) {
      throw new Error('qualification run sequence is not chronological');
    }
  }
  return {
    promotionEligible: false,
    sourceArchiveId,
    canaryArchiveId,
    scenario,
    commit: gitState.commit,
    recordIds: records.map((record) => record.recordId),
  };
}

export function validateUnverifiedQualificationRunSequence(
  projectRoot: string,
  scenario: string,
  sourceArchiveId: string,
  canaryArchiveId: string,
  recordIds: QualificationRunRecordIds,
): QualificationRunSequence {
  return validateUnverifiedQualificationRunSequenceForGitState(
    projectRoot,
    scenario,
    sourceArchiveId,
    canaryArchiveId,
    recordIds,
    readQualificationGitState(projectRoot),
  );
}

/**
 * Promotion deliberately remains unavailable in this primitives-only slice.
 * A later harness integration must derive immutable run/auth records from
 * authoritative execution state rather than caller-supplied observations.
 */
export function assertQualificationPromotion(..._arguments: unknown[]): never {
  throw new Error(
    'qualification promotion is unavailable until authoritative harness and consumed-authorization lineage is integrated',
  );
}

/** Test-state variant is also deliberately fail-closed. */
export function assertQualificationPromotionForGitState(..._arguments: unknown[]): never {
  return assertQualificationPromotion();
}

export function qualificationRunRecordAsJSON(
  record: QualificationRunRecord,
): QualificationJSON {
  return validateQualificationRunRecord(record) as unknown as QualificationJSON;
}
