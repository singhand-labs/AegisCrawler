import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  createQualificationRunRecordForGitState,
  loadQualificationRunRecord,
  saveQualificationRunRecord,
  validateQualificationRunRecord,
  type QualificationGitState,
  type QualificationRunRecord,
} from './evidence';
import {
  QUALIFICATION_ARCHIVE_SCHEMA,
  bindQualificationArtifact,
  canonicalQualificationJSON,
  newQualificationArchiveId,
  qualificationProtocolSHA256,
  qualificationSHA256,
  saveQualificationArchive,
  loadQualificationArchive,
  type LoadedQualificationArchive,
  type QualificationArchivePayload,
} from './history';
import { buildMV3StabilityRunRecord, type MV3StabilityRunFacts } from './mv3-evidence';

const temporaryDirectories: string[] = [];
const gitState: QualificationGitState = {
  commit: 'f'.repeat(40),
  trackedWorktreeClean: true,
};

function minutesFromNow(offset: number): string {
  return new Date(Date.now() + offset * 60_000).toISOString();
}

function temporaryProject(): string {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-qualification-mv3-'));
  temporaryDirectories.push(directory);
  return directory;
}

function archivePayload(
  capturedAt = minutesFromNow(-60),
  marker = 'mv3-source',
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
    archiveKind: 'source-history',
    sourceHistory: null,
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

function directArchive(payload: QualificationArchivePayload): LoadedQualificationArchive {
  return {
    payload,
    payloadHash: qualificationSHA256(canonicalQualificationJSON(payload)),
    artifactPath: '<direct-test>',
  };
}

function factsFor(
  archive: LoadedQualificationArchive,
  overrides: Partial<MV3StabilityRunFacts> = {},
): MV3StabilityRunFacts {
  return {
    runId: crypto.randomUUID(),
    scenario: archive.payload.scenario,
    iterations: 20,
    passed: true,
    routineIterations: 20,
    tasksExecuted: 200,
    startedAt: minutesFromNow(-5),
    finishedAt: minutesFromNow(-4),
    gitCommit: gitState.commit,
    ...overrides,
  };
}

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

describe('buildMV3StabilityRunRecord', {
  timeout: process.platform === 'win32' ? 60_000 : 5_000,
}, () => {
  it('binds the archive hashes, ordered requests, and mv3-stability run kind', () => {
    const archive = directArchive(archivePayload());
    const facts = factsFor(archive);
    const input = buildMV3StabilityRunRecord(archive, facts);

    expect(input.runKind).toBe('mv3-stability');
    expect(input.archiveId).toBe(archive.payload.archiveId);
    expect(input.archivePayloadHash).toBe(archive.payloadHash);
    expect(input.archiveKind).toBe('source-history');
    expect(input.scenario).toBe(archive.payload.scenario);
    expect(input.outcome).toBe('passed');
    expect(input.requestHashes).toEqual(
      archive.payload.attempt.calls.map((call) => call.requestHash),
    );
    expect(input.provisional).toEqual({
      providerIRHash: archive.payload.attempt.providerIR.hash,
      resolvedRuleHash: archive.payload.attempt.resolvedRule.hash,
      validationHash: archive.payload.attempt.validation.hash,
    });
    expect(input.runId).toBe(facts.runId);
  });

  it('reflects failed outcome and optional facts in observations', () => {
    const archive = directArchive(archivePayload());
    const facts = factsFor(archive, {
      passed: false,
      iterations: 50,
      routineIterations: undefined,
      qualificationIterations: 50,
      tasksExecuted: 500,
    });
    const input = buildMV3StabilityRunRecord(archive, facts);

    expect(input.outcome).toBe('failed');
    const observations = input.observations as Record<string, unknown>;
    expect(observations.passed).toBe(false);
    expect(observations.iterations).toBe(50);
    expect(observations.qualificationIterations).toBe(50);
    expect(observations.tasksExecuted).toBe(500);
    expect(observations.routineIterations).toBeUndefined();
  });

  it('produces a record that validates and carries unverified-local authority', () => {
    const archive = directArchive(archivePayload());
    const facts = factsFor(archive);
    const input = buildMV3StabilityRunRecord(archive, facts);

    const record = createQualificationRunRecordForGitState(input, gitState);
    expect(record.authority).toBe('unverified-local-record');
    expect(record.runKind).toBe('mv3-stability');
    expect(record.archiveKind).toBe('source-history');
    expect(record.outcome).toBe('passed');
    expect(record.runId).toBe(facts.runId);
    expect(record.requestHashes).toEqual(input.requestHashes);
    expect(record.provisional).toEqual(input.provisional);
    expect(validateQualificationRunRecord(record)).toEqual(record);
  });

  it('round-trips through the encrypted sidecar store bound to a saved archive', () => {
    const project = temporaryProject();
    const payload = archivePayload();
    saveQualificationArchive(project, payload);
    const archive = loadQualificationArchive(
      project,
      payload.scenario,
      payload.archiveId,
    );
    const facts = factsFor(archive);
    const input = buildMV3StabilityRunRecord(archive, facts);

    const record: QualificationRunRecord = createQualificationRunRecordForGitState(
      input,
      gitState,
    );
    const file = saveQualificationRunRecord(project, record);
    if (process.platform !== 'win32') {
      expect(fs.statSync(file).mode & 0o777).toBe(0o600);
    }
    expect(
      loadQualificationRunRecord(
        project,
        payload.scenario,
        payload.archiveId,
        'mv3-stability',
        record.recordId,
      ),
    ).toEqual(record);
  });

  it('rejects request hashes that do not match the archive', () => {
    const project = temporaryProject();
    const payload = archivePayload();
    saveQualificationArchive(project, payload);
    const archive = loadQualificationArchive(
      project,
      payload.scenario,
      payload.archiveId,
    );
    const facts = factsFor(archive);
    const input = buildMV3StabilityRunRecord(archive, facts);
    input.requestHashes = [qualificationSHA256('mismatched-request')];

    const tampered = createQualificationRunRecordForGitState(input, gitState);
    expect(() => saveQualificationRunRecord(project, tampered)).toThrow(
      /exact archive and provisional/,
    );
  });

  it('rejects a scenario that does not match the archive scenario', () => {
    const archive = directArchive(archivePayload());
    const facts = factsFor(archive, { scenario: 'wrong-scenario' });
    expect(() => buildMV3StabilityRunRecord(archive, facts)).toThrow(
      /scenario must match/,
    );
  });

  it('rejects a fresh-canary archive', () => {
    const sourcePayload = archivePayload();
    const sourceArchive = directArchive(sourcePayload);
    const canaryPayload = archivePayload(minutesFromNow(-10), 'mv3-canary');
    canaryPayload.archiveKind = 'fresh-canary';
    canaryPayload.sourceHistory = {
      archiveId: sourcePayload.archiveId,
      payloadHash: sourceArchive.payloadHash,
      providerIRHash: sourcePayload.attempt.providerIR.hash,
      resolvedRuleHash: sourcePayload.attempt.resolvedRule.hash,
      validationHash: sourcePayload.attempt.validation.hash,
    };
    const canaryArchive = directArchive(canaryPayload);
    const facts = factsFor(canaryArchive);
    expect(() => buildMV3StabilityRunRecord(canaryArchive, facts)).toThrow(
      /source-history archive/,
    );
  });
});
