import * as childProcess from 'node:child_process';
import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  assertQualificationPromotion,
  createQualificationRunRecordForGitState,
  loadQualificationRunRecord,
  readQualificationGitState,
  saveQualificationRunRecord,
  validateQualificationRunRecord,
  validateUnverifiedQualificationRunSequenceForGitState,
  type QualificationGitState,
  type QualificationRunKind,
  type QualificationRunRecord,
  type QualificationRunRecordIds,
} from './evidence';
import {
  QUALIFICATION_ARCHIVE_SCHEMA,
  bindQualificationArtifact,
  canonicalQualificationJSON,
  newQualificationArchiveId,
  qualificationProtocolSHA256,
  qualificationSHA256,
  saveQualificationArchive,
  validateQualificationArchivePayload,
  type LoadedQualificationArchive,
  type QualificationArtifactHash,
  type QualificationArchivePayload,
  type QualificationSourceArchiveReference,
} from './history';

const temporaryDirectories: string[] = [];
const gitState: QualificationGitState = {
  commit: 'a'.repeat(40),
  trackedWorktreeClean: true,
};

function minutesFromNow(offset: number): string {
  return new Date(Date.now() + offset * 60_000).toISOString();
}

function temporaryProject(): string {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-qualification-evidence-'));
  temporaryDirectories.push(directory);
  return directory;
}

function archivePayload(
  archiveKind: 'source-history' | 'fresh-canary' = 'source-history',
  sourceHistory: QualificationSourceArchiveReference | null = null,
  capturedAt = minutesFromNow(-60),
  marker: string = archiveKind,
): QualificationArchivePayload {
  const selectorCatalogHash = qualificationProtocolSHA256('catalog');
  const providerIR = bindQualificationArtifact({
    selectorCatalogHash,
    rule: { id: `${marker}-provider-rule`, steps: [] },
  });
  const resolvedRule = bindQualificationArtifact({
    id: `${marker}-resolved-rule`,
    steps: [],
  });
  const validation = bindQualificationArtifact({ marker, status: 'passed' });
  const content = canonicalQualificationJSON(providerIR.value);
  const call = {
    callId: `${marker}-provider-call-1`,
    callIndex: 1,
    callKind: 'provider_response' as const,
    providerAttempt: 1,
    phase: 'final',
    chunkCount: 0,
    provider: 'openai-compatible',
    model: 'qwen3.6-flash',
    requestHash: qualificationSHA256(`${marker}-request`),
    responseHash: qualificationSHA256(content),
    responseId: `${marker}-provider-response-1`,
    finishReason: 'stop',
    httpStatus: 200,
    errorCode: null,
    content,
    inputTokens: 100,
    outputTokens: 20,
    cacheHit: false,
    redacted: false,
    truncated: false,
    replayable: true,
  };
  const report = bindQualificationArtifact({
    bindings: {
      callIds: [call.callId],
      providerIRSourceCallId: call.callId,
      providerIRSourceResponseHash: call.responseHash,
      providerIRHash: providerIR.hash,
      resolvedRuleHash: resolvedRule.hash,
      validationHash: validation.hash,
    },
    status: 'retained',
  });
  return {
    schemaVersion: QUALIFICATION_ARCHIVE_SCHEMA,
    archiveId: newQualificationArchiveId(),
    archiveKind,
    sourceHistory,
    scenario: 'baidu-search',
    capturedAt,
    recordingHash: qualificationSHA256('recording'),
    requirement: bindQualificationArtifact({
      outputFields: [{ name: 'title', type: 'string' }],
    }),
    baseline: bindQualificationArtifact({ id: 'baseline', steps: [] }),
    provider: {
      name: 'openai-compatible',
      model: 'qwen3.6-flash',
      promptVersion: 'dsl-workflow-v25',
      selectorCatalogHash,
      selectorCatalog: bindQualificationArtifact({
        catalogHash: selectorCatalogHash,
        candidates: [],
      }),
    },
    attempt: {
      jobType: 'dsl',
      jobId: `${marker}-dsl-job-1`,
      attemptNumber: 1,
      reportId: `${marker}-attempt-report-1`,
      reportHash: report.hash,
      report,
      outcome: 'succeeded',
      calls: [call],
      providerIRSourceCallId: call.callId,
      providerIR,
      resolvedRule,
      validation,
    },
  };
}

function sourceReference(archive: LoadedQualificationArchive): QualificationSourceArchiveReference {
  return {
    archiveId: archive.payload.archiveId,
    payloadHash: archive.payloadHash,
    providerIRHash: archive.payload.attempt.providerIR.hash,
    resolvedRuleHash: archive.payload.attempt.resolvedRule.hash,
    validationHash: archive.payload.attempt.validation.hash,
  };
}

function saveArchivePair(project: string): {
  source: LoadedQualificationArchive;
  canary: LoadedQualificationArchive;
} {
  const source = saveQualificationArchive(project, archivePayload());
  const canary = saveQualificationArchive(project, archivePayload(
    'fresh-canary',
    sourceReference(source),
    minutesFromNow(-10),
    'canary',
  ));
  return { source, canary };
}

const timing: Record<QualificationRunKind, [number, number]> = {
  'history-validation': [-55, -54],
  'public-replay': [-50, -49],
  'mv3-stability': [-45, -44],
  'full-canary': [-15, -5],
};

function runRecordFor(
  archive: LoadedQualificationArchive,
  runKind: QualificationRunKind,
  state = gitState,
  overrides: Partial<{
    runId: string;
    startedAt: string;
    completedAt: string;
    requestHashes: QualificationArtifactHash[];
    providerIRHash: QualificationArtifactHash;
    resolvedRuleHash: QualificationArtifactHash;
    validationHash: QualificationArtifactHash;
  }> = {},
): QualificationRunRecord {
  const [startOffset, completionOffset] = timing[runKind];
  return createQualificationRunRecordForGitState({
    runId: overrides.runId,
    archiveId: archive.payload.archiveId,
    archivePayloadHash: archive.payloadHash,
    archiveKind: archive.payload.archiveKind,
    scenario: archive.payload.scenario,
    runKind,
    outcome: 'passed',
    startedAt: overrides.startedAt ?? minutesFromNow(startOffset),
    completedAt: overrides.completedAt ?? minutesFromNow(completionOffset),
    requestHashes: overrides.requestHashes
      ?? archive.payload.attempt.calls.map((call) => call.requestHash),
    provisional: {
      providerIRHash: overrides.providerIRHash ?? archive.payload.attempt.providerIR.hash,
      resolvedRuleHash: overrides.resolvedRuleHash ?? archive.payload.attempt.resolvedRule.hash,
      validationHash: overrides.validationHash ?? archive.payload.attempt.validation.hash,
    },
    observations: { runKind, status: 'passed' },
  }, state);
}

function saveRunSequence(
  project: string,
  source: LoadedQualificationArchive,
  canary: LoadedQualificationArchive,
): { records: QualificationRunRecord[]; ids: QualificationRunRecordIds } {
  const records = [
    runRecordFor(source, 'history-validation'),
    runRecordFor(source, 'public-replay'),
    runRecordFor(source, 'mv3-stability'),
    runRecordFor(canary, 'full-canary'),
  ];
  records.forEach((record) => saveQualificationRunRecord(project, record));
  return {
    records,
    ids: {
      historyValidation: records[0].recordId,
      publicReplay: records[1].recordId,
      mv3Stability: records[2].recordId,
      fullCanary: records[3].recordId,
    },
  };
}

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

describe('qualification archive run-record primitives', {
  timeout: process.platform === 'win32' ? 120_000 : 5_000,
}, () => {
  it('reads an exact commit and detects tracked changes without treating local handover as dirty', () => {
    const project = temporaryProject();
    const git = (...args: string[]) => childProcess.execFileSync(
      'git',
      args,
      { cwd: project, stdio: 'ignore' },
    );
    git('init');
    git('config', 'user.name', 'Qualification Test');
    git('config', 'user.email', 'qualification@example.invalid');
    fs.writeFileSync(path.join(project, 'tracked.txt'), 'committed\n');
    git('add', 'tracked.txt');
    git('commit', '-m', 'fixture');

    const clean = readQualificationGitState(project);
    expect(clean.commit).toMatch(/^[0-9a-f]{40}$/);
    expect(clean.trackedWorktreeClean).toBe(true);
    fs.writeFileSync(path.join(project, 'AGENTS.md'), 'local handover\n');
    expect(readQualificationGitState(project)).toEqual(clean);
    fs.writeFileSync(path.join(project, 'tracked.txt'), 'modified\n');
    expect(readQualificationGitState(project).trackedWorktreeClean).toBe(false);
  });

  it('requires distinct authenticated source-history and fresh-canary archives', () => {
    const project = temporaryProject();
    const source = saveQualificationArchive(project, archivePayload());
    const canary = saveQualificationArchive(project, archivePayload(
      'fresh-canary',
      sourceReference(source),
      minutesFromNow(-10),
      'canary',
    ));
    expect(canary.payload.archiveId).not.toBe(source.payload.archiveId);
    expect(canary.payload.sourceHistory).toEqual(sourceReference(source));
    expect(canary.payload.attempt.providerIR.hash)
      .not.toBe(source.payload.attempt.providerIR.hash);

    const wrongLink = archivePayload(
      'fresh-canary',
      { ...sourceReference(source), payloadHash: qualificationSHA256('wrong') },
      minutesFromNow(-8),
      'bad-canary',
    );
    expect(() => saveQualificationArchive(project, wrongLink))
      .toThrow(/exact source history/);

    const selfLinked = archivePayload(
      'fresh-canary',
      sourceReference(source),
      minutesFromNow(-8),
      'self-canary',
    );
    selfLinked.sourceHistory!.archiveId = selfLinked.archiveId;
    expect(() => validateQualificationArchivePayload(selfLinked))
      .toThrow(/distinct identity/);
  });

  it('stores immutable run records bound to exact archive requests and provisional hashes', () => {
    const project = temporaryProject();
    const { source } = saveArchivePair(project);
    const record = runRecordFor(source, 'history-validation');
    const file = saveQualificationRunRecord(project, record);
    if (process.platform !== 'win32') {
      expect(fs.statSync(file).mode & 0o777).toBe(0o600);
    }
    expect(loadQualificationRunRecord(
      project,
      source.payload.scenario,
      source.payload.archiveId,
      record.runKind,
      record.recordId,
    )).toEqual(record);
    expect(() => saveQualificationRunRecord(project, record))
      .toThrow(/cannot be overwritten/);

    const wrongRequest = runRecordFor(source, 'history-validation', gitState, {
      requestHashes: [qualificationSHA256('near-match')],
    });
    expect(() => saveQualificationRunRecord(project, wrongRequest))
      .toThrow(/exact archive and provisional/);
    const wrongRule = runRecordFor(source, 'history-validation', gitState, {
      resolvedRuleHash: qualificationSHA256('different-rule'),
    });
    expect(() => saveQualificationRunRecord(project, wrongRule))
      .toThrow(/exact archive and provisional/);
  });

  it('binds the full-canary record to its own fresh output, not the source provisional', () => {
    const project = temporaryProject();
    const { source, canary } = saveArchivePair(project);
    const incorrect = runRecordFor(canary, 'full-canary', gitState, {
      providerIRHash: source.payload.attempt.providerIR.hash,
      resolvedRuleHash: source.payload.attempt.resolvedRule.hash,
      validationHash: source.payload.attempt.validation.hash,
    });
    expect(() => saveQualificationRunRecord(project, incorrect))
      .toThrow(/exact archive and provisional/);
    expect(() => saveQualificationRunRecord(
      project,
      runRecordFor(canary, 'full-canary'),
    )).not.toThrow();
  });

  it('rejects future, reversed, dirty, and archive-incompatible run records', () => {
    const project = temporaryProject();
    const { source, canary } = saveArchivePair(project);
    expect(() => runRecordFor(source, 'history-validation', {
      ...gitState,
      trackedWorktreeClean: false,
    })).toThrow(/clean tracked worktree/);
    expect(() => runRecordFor(source, 'history-validation', gitState, {
      startedAt: minutesFromNow(-5),
      completedAt: minutesFromNow(-6),
    })).toThrow(/predates its start/);
    expect(() => runRecordFor(source, 'history-validation', gitState, {
      startedAt: minutesFromNow(60),
      completedAt: minutesFromNow(61),
    })).toThrow(/future/);
    expect(() => createQualificationRunRecordForGitState({
      archiveId: source.payload.archiveId,
      archivePayloadHash: source.payloadHash,
      archiveKind: source.payload.archiveKind,
      scenario: source.payload.scenario,
      runKind: 'full-canary',
      outcome: 'passed',
      startedAt: minutesFromNow(-5),
      completedAt: minutesFromNow(-4),
      requestHashes: source.payload.attempt.calls.map((call) => call.requestHash),
      provisional: {
        providerIRHash: source.payload.attempt.providerIR.hash,
        resolvedRuleHash: source.payload.attempt.resolvedRule.hash,
        validationHash: source.payload.attempt.validation.hash,
      },
      observations: {},
    }, gitState)).toThrow(/does not match its archive kind/);

    const canaryBeforeCapture = runRecordFor(canary, 'full-canary', gitState, {
      startedAt: minutesFromNow(-9),
      completedAt: minutesFromNow(-8),
    });
    expect(() => saveQualificationRunRecord(project, canaryBeforeCapture))
      .toThrow(/captured during its run/);
  });

  it('validates distinct chronological records but always returns non-promotable lineage', () => {
    const project = temporaryProject();
    const { source, canary } = saveArchivePair(project);
    const { ids } = saveRunSequence(project, source, canary);
    expect(validateUnverifiedQualificationRunSequenceForGitState(
      project,
      source.payload.scenario,
      source.payload.archiveId,
      canary.payload.archiveId,
      ids,
      gitState,
    )).toMatchObject({
      promotionEligible: false,
      sourceArchiveId: source.payload.archiveId,
      canaryArchiveId: canary.payload.archiveId,
      commit: gitState.commit,
    });
    expect(() => assertQualificationPromotion()).toThrow(
      /unavailable until authoritative harness and consumed-authorization lineage/,
    );
  });

  it('rejects reused run identities and nonchronological gate records', () => {
    const duplicateProject = temporaryProject();
    const duplicatePair = saveArchivePair(duplicateProject);
    const duplicateRunId = crypto.randomUUID();
    const duplicateRecords = [
      runRecordFor(duplicatePair.source, 'history-validation', gitState, { runId: duplicateRunId }),
      runRecordFor(duplicatePair.source, 'public-replay', gitState, { runId: duplicateRunId }),
      runRecordFor(duplicatePair.source, 'mv3-stability'),
      runRecordFor(duplicatePair.canary, 'full-canary'),
    ];
    duplicateRecords.forEach((record) => saveQualificationRunRecord(duplicateProject, record));
    expect(() => validateUnverifiedQualificationRunSequenceForGitState(
      duplicateProject,
      duplicatePair.source.payload.scenario,
      duplicatePair.source.payload.archiveId,
      duplicatePair.canary.payload.archiveId,
      {
        historyValidation: duplicateRecords[0].recordId,
        publicReplay: duplicateRecords[1].recordId,
        mv3Stability: duplicateRecords[2].recordId,
        fullCanary: duplicateRecords[3].recordId,
      },
      gitState,
    )).toThrow(/distinct record and run identities/);

    const chronologyProject = temporaryProject();
    const chronologyPair = saveArchivePair(chronologyProject);
    const history = runRecordFor(chronologyPair.source, 'history-validation');
    const publicReplay = runRecordFor(chronologyPair.source, 'public-replay', gitState, {
      startedAt: minutesFromNow(-55),
      completedAt: minutesFromNow(-53),
    });
    const mv3 = runRecordFor(chronologyPair.source, 'mv3-stability');
    const canary = runRecordFor(chronologyPair.canary, 'full-canary');
    [history, publicReplay, mv3, canary]
      .forEach((record) => saveQualificationRunRecord(chronologyProject, record));
    expect(() => validateUnverifiedQualificationRunSequenceForGitState(
      chronologyProject,
      chronologyPair.source.payload.scenario,
      chronologyPair.source.payload.archiveId,
      chronologyPair.canary.payload.archiveId,
      {
        historyValidation: history.recordId,
        publicReplay: publicReplay.recordId,
        mv3Stability: mv3.recordId,
        fullCanary: canary.recordId,
      },
      gitState,
    )).toThrow(/not chronological/);
  });

  it('rejects observation tampering and self-asserted authority', () => {
    const project = temporaryProject();
    const { source } = saveArchivePair(project);
    const record = runRecordFor(source, 'history-validation');
    const tampered = structuredClone(record);
    (tampered.observations.value as Record<string, unknown>).status = 'different';
    expect(() => validateQualificationRunRecord(tampered)).toThrow(/hash does not match/);
    expect(() => validateQualificationRunRecord({
      ...record,
      authority: 'authoritative-harness',
    })).toThrow(/cannot self-assert/);
  });
});
