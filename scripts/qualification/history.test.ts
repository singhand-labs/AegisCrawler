import * as fs from 'node:fs';
import { execFileSync } from 'node:child_process';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  QUALIFICATION_ARCHIVE_SCHEMA,
  bindQualificationArtifact,
  canonicalQualificationJSON,
  exactQualificationLoopbackCall,
  loadQualificationArchive,
  loadQualificationSidecar,
  newQualificationArchiveId,
  qualificationHistoryPaths,
  qualificationProtocolSHA256,
  qualificationSHA256,
  saveQualificationArchive,
  saveQualificationSidecar,
  validateQualificationArchivePayload,
  type QualificationArchivePayload,
} from './history';
import { secureWindowsPrivatePath } from './windows-private-storage';

const temporaryDirectories: string[] = [];

function temporaryProject(): string {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-qualification-history-'));
  temporaryDirectories.push(directory);
  return directory;
}

function providerCall(content: string) {
  return {
    callId: 'provider-call-1',
    callIndex: 1,
    callKind: 'provider_response' as const,
    providerAttempt: 1,
    phase: 'final',
    chunkCount: 0,
    provider: 'openai-compatible',
    model: 'qwen3.6-flash',
    requestHash: qualificationSHA256('exact-request'),
    responseHash: qualificationSHA256(content),
    responseId: 'provider-response-1',
    finishReason: 'stop',
    httpStatus: 200,
    errorCode: null,
    content,
    inputTokens: 123,
    outputTokens: 45,
    cacheHit: false,
    redacted: false,
    truncated: false,
    replayable: true,
  };
}

function archivePayload(
  archiveId = newQualificationArchiveId(),
  providerIRValue?: Record<string, unknown>,
): QualificationArchivePayload {
  const selectorCatalogHash = qualificationProtocolSHA256('catalog');
  const providerIR = bindQualificationArtifact(providerIRValue ?? {
    selectorCatalogHash,
    rule: { steps: [] },
  });
  const resolvedRule = bindQualificationArtifact({ id: 'resolved-rule', steps: [] });
  const validation = bindQualificationArtifact({ status: 'passed', diagnostics: [] });
  const call = providerCall(canonicalQualificationJSON(providerIR.value));
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
    archiveId,
    archiveKind: 'source-history',
    sourceHistory: null,
    scenario: 'baidu-search',
    capturedAt: '2026-07-23T12:00:00.000Z',
    recordingHash: qualificationSHA256('recording-is-stored-elsewhere'),
    requirement: bindQualificationArtifact({
      title: 'Search Baidu',
      outputFields: [{ name: 'title', type: 'string' }],
    }),
    baseline: bindQualificationArtifact({ id: 'recorded-baseline', steps: [] }),
    provider: {
      name: 'openai-compatible',
      model: 'qwen3.6-flash',
      promptVersion: 'dsl-workflow-v25',
      selectorCatalogHash,
      selectorCatalog: bindQualificationArtifact({
        catalogHash: selectorCatalogHash,
        candidates: [{ id: 'row_opaque', capability: 'row' }],
      }),
    },
    attempt: {
      jobType: 'dsl',
      jobId: 'dsl-job-1',
      attemptNumber: 1,
      reportId: 'attempt-report-1',
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

function refreshReport(source: QualificationArchivePayload): void {
  source.attempt.report = bindQualificationArtifact({
    bindings: {
      callIds: source.attempt.calls.map((call) => call.callId),
      providerIRSourceCallId: source.attempt.providerIRSourceCallId,
      providerIRSourceResponseHash: source.attempt.providerIRSourceCallId === null
        ? null
        : source.attempt.calls.find((call) =>
          call.callId === source.attempt.providerIRSourceCallId)?.responseHash ?? null,
      providerIRHash: source.attempt.providerIR.hash,
      resolvedRuleHash: source.attempt.resolvedRule.hash,
      validationHash: source.attempt.validation.hash,
    },
    status: 'retained',
  });
  source.attempt.reportHash = source.attempt.report.hash;
}

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

describe('encrypted qualification history', {
  timeout: process.platform === 'win32' ? 60_000 : 5_000,
}, () => {
  it('round-trips an immutable bound payload without plaintext workflow artifacts', () => {
    const project = temporaryProject();
    const source = archivePayload();
    const saved = saveQualificationArchive(project, source);
    const paths = qualificationHistoryPaths(project, source.scenario, source.archiveId);

    expect(saved.payloadHash).toMatch(/^[0-9a-f]{64}$/);
    if (process.platform !== 'win32') {
      expect(fs.statSync(paths.root).mode & 0o777).toBe(0o700);
      expect(fs.statSync(paths.scenarioDirectory).mode & 0o777).toBe(0o700);
      expect(fs.statSync(paths.keyFile).mode & 0o777).toBe(0o600);
      expect(fs.statSync(paths.artifact).mode & 0o777).toBe(0o600);
    } else {
      const aclProject = temporaryProject();
      const aclSource = archivePayload();
      saveQualificationArchive(aclProject, aclSource);
      const aclPaths = qualificationHistoryPaths(
        aclProject,
        aclSource.scenario,
        aclSource.archiveId,
      );
      execFileSync('icacls.exe', [aclPaths.root, '/inheritance:e', '/Q']);
      expect(() => loadQualificationArchive(aclProject, aclSource.scenario, aclSource.archiveId))
        .toThrow(/private Windows DACL/);
    }
    const envelope = fs.readFileSync(paths.artifact, 'utf8');
    expect(envelope).not.toContain('qwen3.6-flash');
    expect(envelope).not.toContain('recorded-baseline');
    expect(envelope).not.toContain('recording-is-stored-elsewhere');

    const loaded = loadQualificationArchive(project, source.scenario, source.archiveId);
    expect(loaded).toEqual(saved);
    expect(loaded.payload.recordingHash).toBe(source.recordingHash);
    expect(JSON.stringify(loaded.payload)).not.toContain('recording-is-stored-elsewhere');
  });

  it('refuses archive and authenticated-sidecar overwrite', () => {
    const project = temporaryProject();
    const source = archivePayload();
    saveQualificationArchive(project, source);
    expect(() => saveQualificationArchive(project, source)).toThrow(/cannot be overwritten|already exists/);

    saveQualificationSidecar(
      project,
      source.scenario,
      source.archiveId,
      'receipt-history-validation',
      'qualification-receipt:history-validation',
      { status: 'passed' },
    );
    expect(() => saveQualificationSidecar(
      project,
      source.scenario,
      source.archiveId,
      'receipt-history-validation',
      'qualification-receipt:history-validation',
      { status: 'different' },
    )).toThrow(/cannot be overwritten|already exists/);
  });

  it('rejects ciphertext, sidecar, key, and authenticated-identity tampering', () => {
    const ciphertextProject = temporaryProject();
    const source = archivePayload();
    saveQualificationArchive(ciphertextProject, source);
    const paths = qualificationHistoryPaths(ciphertextProject, source.scenario, source.archiveId);
    const envelope = JSON.parse(fs.readFileSync(paths.artifact, 'utf8')) as { ciphertext: string };
    const ciphertext = Buffer.from(envelope.ciphertext, 'base64');
    ciphertext[0] ^= 0x01;
    envelope.ciphertext = ciphertext.toString('base64');
    fs.writeFileSync(paths.artifact, JSON.stringify(envelope));
    expect(() => loadQualificationArchive(
      ciphertextProject,
      source.scenario,
      source.archiveId,
    )).toThrow(/authentication failed/);

    const keyProject = temporaryProject();
    const keySource = archivePayload();
    saveQualificationArchive(keyProject, keySource);
    const keyPaths = qualificationHistoryPaths(keyProject, keySource.scenario, keySource.archiveId);
    fs.writeFileSync(keyPaths.keyFile, '0'.repeat(64));
    expect(() => loadQualificationArchive(keyProject, keySource.scenario, keySource.archiveId))
      .toThrow(/authentication failed/);

    const identityProject = temporaryProject();
    const identitySource = archivePayload();
    saveQualificationArchive(identityProject, identitySource);
    const originalPaths = qualificationHistoryPaths(
      identityProject,
      identitySource.scenario,
      identitySource.archiveId,
    );
    const otherId = newQualificationArchiveId();
    const otherPaths = qualificationHistoryPaths(identityProject, 'other-scenario', otherId);
    fs.mkdirSync(otherPaths.scenarioDirectory, { recursive: true, mode: 0o700 });
    fs.chmodSync(otherPaths.scenarioDirectory, 0o700);
    secureWindowsPrivatePath(otherPaths.scenarioDirectory, 'directory');
    const movedEnvelope = JSON.parse(fs.readFileSync(originalPaths.artifact, 'utf8')) as {
      archiveId: string; scenario: string;
    };
    movedEnvelope.archiveId = otherId;
    movedEnvelope.scenario = 'other-scenario';
    fs.writeFileSync(otherPaths.artifact, JSON.stringify(movedEnvelope), { mode: 0o600 });
    secureWindowsPrivatePath(otherPaths.artifact, 'file');
    expect(() => loadQualificationArchive(identityProject, 'other-scenario', otherId))
      .toThrow(/authentication failed/);

    const sidecarProject = temporaryProject();
    const sidecarSource = archivePayload();
    saveQualificationArchive(sidecarProject, sidecarSource);
    const sidecar = saveQualificationSidecar(
      sidecarProject,
      sidecarSource.scenario,
      sidecarSource.archiveId,
      'receipt-history-validation',
      'qualification-receipt:history-validation',
      { status: 'passed' },
    );
    const sidecarEnvelope = JSON.parse(fs.readFileSync(sidecar, 'utf8')) as {
      ciphertext: string;
    };
    const sidecarCiphertext = Buffer.from(sidecarEnvelope.ciphertext, 'base64');
    sidecarCiphertext[0] ^= 0x01;
    sidecarEnvelope.ciphertext = sidecarCiphertext.toString('base64');
    fs.writeFileSync(sidecar, JSON.stringify(sidecarEnvelope));
    expect(() => loadQualificationSidecar(
      sidecarProject,
      sidecarSource.scenario,
      sidecarSource.archiveId,
      'receipt-history-validation',
      'qualification-receipt:history-validation',
    )).toThrow(/hash mismatch|authentication failed/);
  });

  it('authenticates archive creation time and exact sidecar filename identity', () => {
    const project = temporaryProject();
    const source = archivePayload();
    saveQualificationArchive(project, source);
    const paths = qualificationHistoryPaths(project, source.scenario, source.archiveId);
    const envelope = JSON.parse(fs.readFileSync(paths.artifact, 'utf8')) as {
      createdAt: string;
    };
    envelope.createdAt = '2026-07-23T11:59:00.000Z';
    fs.writeFileSync(paths.artifact, JSON.stringify(envelope));
    expect(() => loadQualificationArchive(project, source.scenario, source.archiveId))
      .toThrow(/authentication failed/);

    const sidecarProject = temporaryProject();
    const sidecarSource = archivePayload();
    saveQualificationArchive(sidecarProject, sidecarSource);
    const original = saveQualificationSidecar(
      sidecarProject,
      sidecarSource.scenario,
      sidecarSource.archiveId,
      'run-history-one',
      'qualification-run:history-validation',
      { status: 'passed' },
    );
    const copied = original.replace('run-history-one.json', 'run-history-two.json');
    fs.copyFileSync(original, copied, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(copied, 0o600);
    secureWindowsPrivatePath(copied, 'file');
    expect(() => loadQualificationSidecar(
      sidecarProject,
      sidecarSource.scenario,
      sidecarSource.archiveId,
      'run-history-two',
      'qualification-run:history-validation',
    )).toThrow(/identity does not match/);

    const copiedEnvelope = JSON.parse(fs.readFileSync(copied, 'utf8')) as { name: string };
    copiedEnvelope.name = 'run-history-two';
    fs.writeFileSync(copied, JSON.stringify(copiedEnvelope));
    expect(() => loadQualificationSidecar(
      sidecarProject,
      sidecarSource.scenario,
      sidecarSource.archiveId,
      'run-history-two',
      'qualification-run:history-validation',
    )).toThrow(/authentication failed/);
  });

  it('rejects symlinked layouts, path traversal, and unsafe modes', () => {
    const project = temporaryProject();
    const target = temporaryProject();
    fs.symlinkSync(target, path.join(project, '.env'));
    const source = archivePayload();
    expect(() => saveQualificationArchive(project, source)).toThrow(/non-symlink/);
    expect(fs.readdirSync(target)).toHaveLength(0);

    expect(() => qualificationHistoryPaths(project, '../baidu', source.archiveId))
      .toThrow(/lowercase slug/);
    expect(() => qualificationHistoryPaths(project, 'baidu-search', '../archive'))
      .toThrow(/canonical UUID/);

    if (process.platform !== 'win32') {
      const modeProject = temporaryProject();
      const modeSource = archivePayload();
      saveQualificationArchive(modeProject, modeSource);
      const modePaths = qualificationHistoryPaths(
        modeProject,
        modeSource.scenario,
        modeSource.archiveId,
      );
      fs.chmodSync(modePaths.root, 0o755);
      expect(() => loadQualificationArchive(modeProject, modeSource.scenario, modeSource.archiveId))
        .toThrow(/mode 0700/);
      fs.chmodSync(modePaths.root, 0o700);
      fs.chmodSync(modePaths.keyFile, 0o644);
      expect(() => loadQualificationArchive(modeProject, modeSource.scenario, modeSource.archiveId))
        .toThrow(/mode 0600/);
      fs.chmodSync(modePaths.keyFile, 0o600);
      fs.chmodSync(modePaths.artifact, 0o644);
      expect(() => loadQualificationArchive(modeProject, modeSource.scenario, modeSource.archiveId))
        .toThrow(/mode 0600/);
    }

    const noFollowProject = temporaryProject();
    const noFollowSource = archivePayload();
    saveQualificationArchive(noFollowProject, noFollowSource);
    const noFollowPaths = qualificationHistoryPaths(
      noFollowProject,
      noFollowSource.scenario,
      noFollowSource.archiveId,
    );
    const realArtifact = `${noFollowPaths.artifact}.real`;
    fs.renameSync(noFollowPaths.artifact, realArtifact);
    fs.symlinkSync(realArtifact, noFollowPaths.artifact);
    expect(() => loadQualificationArchive(
      noFollowProject,
      noFollowSource.scenario,
      noFollowSource.archiveId,
    )).toThrow(/non-symlink/);

    const swappedDirectoryProject = temporaryProject();
    const swappedDirectorySource = archivePayload();
    saveQualificationArchive(swappedDirectoryProject, swappedDirectorySource);
    const swappedDirectoryPaths = qualificationHistoryPaths(
      swappedDirectoryProject,
      swappedDirectorySource.scenario,
      swappedDirectorySource.archiveId,
    );
    const realScenarioDirectory = `${swappedDirectoryPaths.scenarioDirectory}.real`;
    fs.renameSync(swappedDirectoryPaths.scenarioDirectory, realScenarioDirectory);
    fs.symlinkSync(realScenarioDirectory, swappedDirectoryPaths.scenarioDirectory);
    expect(() => loadQualificationArchive(
      swappedDirectoryProject,
      swappedDirectorySource.scenario,
      swappedDirectorySource.archiveId,
    )).toThrow(/non-symlink directory/);
  });

  it('enforces the absolute bound through a lower test-only limit', () => {
    const project = temporaryProject();
    const source = archivePayload(newQualificationArchiveId(), {
      selectorCatalogHash: qualificationProtocolSHA256('catalog'),
      rule: { steps: [], padding: 'x'.repeat(2_000) },
    });
    expect(() => saveQualificationArchive(project, source, { maxArchiveBytes: 1_024 }))
      .toThrow(/bounded size/);
    expect(() => saveQualificationArchive(project, source, { maxArchiveBytes: 65 * 1024 * 1024 }))
      .toThrow(/limit must be between/);
  });

  it('rejects malformed schemas, mismatched artifact hashes, and noncontiguous calls', () => {
    const source = archivePayload();
    const unknown = structuredClone(source) as QualificationArchivePayload & { extra?: boolean };
    unknown.extra = true;
    expect(() => validateQualificationArchivePayload(unknown)).toThrow(/unsupported field extra/);

    const badHash = structuredClone(source) as unknown as {
      requirement: { hash: string };
    };
    badHash.requirement.hash = '0'.repeat(64);
    expect(() => validateQualificationArchivePayload(badHash)).toThrow(/does not match/);

    const hexCatalogProtocol = structuredClone(source) as unknown as {
      provider: { selectorCatalogHash: string };
    };
    hexCatalogProtocol.provider.selectorCatalogHash = qualificationSHA256('catalog');
    expect(() => validateQualificationArchivePayload(hexCatalogProtocol))
      .toThrow(/canonical base64url SHA-256/);

    const badCall = structuredClone(source);
    badCall.attempt.calls[0].callIndex = 2;
    expect(() => validateQualificationArchivePayload(badCall)).toThrow(/contiguous and ordered/);

    const unboundReport = structuredClone(source);
    unboundReport.attempt.reportHash = qualificationSHA256('unbound report');
    expect(() => validateQualificationArchivePayload(unboundReport))
      .toThrow(/does not bind the retained report/);

    const wrongProviderIR = structuredClone(source);
    const different = '{"selectorCatalogHash":"different","rule":{"steps":[]}}';
    wrongProviderIR.attempt.calls[0].content = different;
    wrongProviderIR.attempt.calls[0].responseHash = qualificationSHA256(different);
    expect(() => validateQualificationArchivePayload(wrongProviderIR))
      .toThrow(/does not bind exact calls and provisional artifacts/);
  });

  it('canonicalizes hostile JSON keys without prototype mutation or data loss', () => {
    const hostile = JSON.parse('{"__proto__":{"polluted":true},"constructor":"kept"}') as {
      __proto__: { polluted: boolean };
      constructor: string;
    };
    const bound = bindQualificationArtifact(hostile);
    expect(Object.prototype).not.toHaveProperty('polluted');
    expect(Object.prototype.hasOwnProperty.call(bound.value, '__proto__')).toBe(true);
    expect(JSON.stringify(bound.value)).toContain('"__proto__"');
    const source = archivePayload();
    source.requirement = bound;
    expect(validateQualificationArchivePayload(source).requirement).toEqual(bound);
    expect(Object.prototype).not.toHaveProperty('polluted');
  });

  it('rejects credential plaintext and never treats redacted provider output as replayable', () => {
    const sensitive = archivePayload();
    const secretContent = '{"apiToken":"provider-secret"}';
    sensitive.attempt.calls[0].content = secretContent;
    sensitive.attempt.calls[0].responseHash = qualificationSHA256(secretContent);
    expect(() => validateQualificationArchivePayload(sensitive))
      .toThrow(/sensitive plaintext/);

    const unsafeRequirement = archivePayload();
    unsafeRequirement.requirement = bindQualificationArtifact({
      outputFields: [{ name: 'apiToken', type: 'string' }],
    });
    expect(() => validateQualificationArchivePayload(unsafeRequirement))
      .toThrow(/sensitive plaintext/);

    const redacted = archivePayload();
    const redactedContent = '{"apiToken":"[REDACTED]"}';
    redacted.attempt.calls[0].content = redactedContent;
    redacted.attempt.calls[0].responseHash = qualificationSHA256(redactedContent);
    expect(() => validateQualificationArchivePayload(redacted))
      .toThrow(/marked redacted and non-replayable/);
    redacted.attempt.calls[0].redacted = true;
    redacted.attempt.calls[0].replayable = false;
    redacted.attempt.outcome = 'failed';
    redacted.attempt.providerIR = bindQualificationArtifact(null);
    redacted.attempt.providerIRSourceCallId = null;
    refreshReport(redacted);
    expect(validateQualificationArchivePayload(redacted).attempt.calls[0])
      .toMatchObject({ redacted: true, replayable: false });
  });

  it('detects credential signatures without rejecting ordinary email or long numeric data', () => {
    const ordinary = archivePayload();
    ordinary.requirement = bindQualificationArtifact({
      contact: 'collector@example.test',
      cookiePolicy: 'accept none',
      productNumber: '1234567890123456789',
      secretary: 'Ada',
    });
    expect(() => validateQualificationArchivePayload(ordinary)).not.toThrow();

    for (const secret of [
      'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature12345678',
      'sk-abcdefghijklmnopqrstuvwx',
      '-----BEGIN PRIVATE KEY-----',
    ]) {
      const unsafe = archivePayload();
      unsafe.requirement = bindQualificationArtifact({ note: secret });
      expect(() => validateQualificationArchivePayload(unsafe))
        .toThrow(/sensitive plaintext/);
    }

    for (const marker of ['<ReDaCtEd>', 'redacted']) {
      const mixedRedaction = archivePayload();
      const redactedContent = JSON.stringify({ sessionId: marker });
      mixedRedaction.attempt.calls[0].content = redactedContent;
      mixedRedaction.attempt.calls[0].responseHash = qualificationSHA256(redactedContent);
      expect(() => validateQualificationArchivePayload(mixedRedaction))
        .toThrow(/marked redacted and non-replayable/);
    }
  });

  it('requires successful response metadata and an ordered terminal provider phase', () => {
    const invalidResponses: Array<(source: QualificationArchivePayload) => void> = [
      (source) => { source.attempt.calls[0].httpStatus = 500; },
      (source) => { source.attempt.calls[0].responseId = null; },
      (source) => { source.attempt.calls[0].finishReason = null; },
      (source) => { source.attempt.calls[0].phase = 'repair'; },
    ];
    invalidResponses.forEach((mutate) => {
      const source = archivePayload();
      mutate(source);
      expect(() => validateQualificationArchivePayload(source)).toThrow();
    });

    const missingTerminal = archivePayload();
    missingTerminal.attempt.calls[0].phase = 'analysis';
    missingTerminal.attempt.calls[0].chunkIndex = 0;
    missingTerminal.attempt.calls[0].chunkCount = 2;
    expect(() => validateQualificationArchivePayload(missingTerminal))
      .toThrow(/completed terminal provider phase/);
  });

  it('accepts complete ordered chunk lineage and rejects duplicate or reordered chunks', () => {
    const chunked = archivePayload();
    const terminal = structuredClone(chunked.attempt.calls[0]);
    const analysis = (index: number) => {
      const content = canonicalQualificationJSON({ chunk: index });
      return {
        ...terminal,
        callId: `provider-analysis-${index}`,
        callIndex: index + 1,
        phase: 'analysis',
        chunkIndex: index,
        chunkCount: 2,
        responseId: `provider-analysis-response-${index}`,
        content,
        responseHash: qualificationSHA256(content),
      };
    };
    terminal.callId = 'provider-synthesis';
    terminal.callIndex = 3;
    terminal.phase = 'synthesis';
    terminal.chunkCount = 2;
    terminal.responseId = 'provider-synthesis-response';
    chunked.attempt.calls = [analysis(0), analysis(1), terminal];
    chunked.attempt.providerIRSourceCallId = terminal.callId;
    refreshReport(chunked);
    expect(() => validateQualificationArchivePayload(chunked)).not.toThrow();

    const reordered = structuredClone(chunked);
    reordered.attempt.calls[0].chunkIndex = 1;
    reordered.attempt.calls[1].chunkIndex = 0;
    expect(() => validateQualificationArchivePayload(reordered))
      .toThrow(/cover each index exactly once/);

    const duplicate = structuredClone(chunked);
    duplicate.attempt.calls[1].chunkIndex = 0;
    expect(() => validateQualificationArchivePayload(duplicate)).toThrow();
  });

  it('allows loopback only for an exact replayable ordered request and model', () => {
    const source = archivePayload();
    const call = source.attempt.calls[0];
    expect(exactQualificationLoopbackCall(source, {
      callIndex: 1,
      provider: call.provider,
      model: call.model,
      phase: call.phase,
      requestHash: call.requestHash,
    }).content).toBe(call.content);
    expect(() => exactQualificationLoopbackCall(source, {
      callIndex: 1,
      provider: call.provider,
      model: 'qwen3.6-flash-near-match',
      phase: call.phase,
      requestHash: call.requestHash,
    })).toThrow(/exact ordered/);
    expect(() => exactQualificationLoopbackCall(source, {
      callIndex: 1,
      provider: call.provider,
      model: call.model,
      phase: call.phase,
      requestHash: qualificationSHA256('near-match'),
    })).toThrow(/exact ordered/);

    const redacted = structuredClone(source);
    redacted.attempt.calls[0].replayable = false;
    redacted.attempt.calls[0].redacted = true;
    expect(() => exactQualificationLoopbackCall(redacted, {
      callIndex: 1,
      provider: call.provider,
      model: call.model,
      phase: call.phase,
      requestHash: call.requestHash,
    })).toThrow(/replayable/);
  });
});
