import type { CreateQualificationRunRecordInput } from './evidence';
import type { LoadedQualificationArchive } from './history';

export interface MV3StabilityRunFacts {
  runId: string;
  scenario: string;
  iterations: number;
  passed: boolean;
  routineIterations?: number;
  qualificationIterations?: number;
  tasksExecuted?: number;
  startedAt: string;
  finishedAt: string;
  gitCommit: string;
}

/**
 * Build the exact input expected by `createQualificationRunRecord` for an MV3
 * replay-stability run, binding the loaded source-history archive's hashes and
 * ordered request lineage. The resulting record always carries
 * `authority: 'unverified-local-record'` because that is hardcoded by the
 * record factory; this builder cannot produce an authoritatively promotable
 * record. Pure: performs no I/O.
 */
export function buildMV3StabilityRunRecord(
  archive: LoadedQualificationArchive,
  facts: MV3StabilityRunFacts,
): CreateQualificationRunRecordInput {
  if (archive.payload.archiveKind !== 'source-history') {
    throw new Error('mv3-stability run records must bind a source-history archive');
  }
  if (facts.scenario !== archive.payload.scenario) {
    throw new Error('mv3-stability run scenario must match its source-history archive scenario');
  }
  return {
    runId: facts.runId,
    archiveId: archive.payload.archiveId,
    archivePayloadHash: archive.payloadHash,
    archiveKind: archive.payload.archiveKind,
    scenario: archive.payload.scenario,
    runKind: 'mv3-stability',
    outcome: facts.passed ? 'passed' : 'failed',
    startedAt: facts.startedAt,
    completedAt: facts.finishedAt,
    requestHashes: archive.payload.attempt.calls.map((call) => call.requestHash),
    provisional: {
      providerIRHash: archive.payload.attempt.providerIR.hash,
      resolvedRuleHash: archive.payload.attempt.resolvedRule.hash,
      validationHash: archive.payload.attempt.validation.hash,
    },
    observations: {
      runKind: 'mv3-stability',
      runId: facts.runId,
      scenario: facts.scenario,
      iterations: facts.iterations,
      passed: facts.passed,
      ...(facts.routineIterations === undefined
        ? {}
        : { routineIterations: facts.routineIterations }),
      ...(facts.qualificationIterations === undefined
        ? {}
        : { qualificationIterations: facts.qualificationIterations }),
      ...(facts.tasksExecuted === undefined
        ? {}
        : { tasksExecuted: facts.tasksExecuted }),
      startedAt: facts.startedAt,
      finishedAt: facts.finishedAt,
      gitCommit: facts.gitCommit,
    },
  };
}
