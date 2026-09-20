import { describe, expect, it, vi } from 'vitest';
import {
  MODEL_FIXTURE_INPUT_TOKEN_BUDGET,
  assertPersistedReplaySemanticRows,
  assertStrictQualification,
  snapshotVerificationProgress,
  strictLLMEnvironment,
  strictQualificationJobs,
} from './qualification';

function successfulTask(caseName = 'default') {
  return {
    caseName,
    expectedStatus: 'done' as const,
    taskId: `task-${caseName}`,
    status: 'done',
    validBatches: 1,
    invalidBatches: 0,
    rows: 8,
    summary: true,
    summaryStatus: 'success',
    maxRetries: 0,
    retryCount: 0,
    executionAttempts: 1,
    mcpAgreedWithAdmin: true,
    mcpTokenRevoked: true,
  };
}

describe('live workflow qualification reporting', () => {
  it('caps fixture-models at 150,000 summed input tokens', () => {
    expect(MODEL_FIXTURE_INPUT_TOKEN_BUDGET).toBe(150_000);
  });

  it('boots the paid harness with fail-fast strict settings', () => {
    const env = strictLLMEnvironment();
    expect(env).toMatchObject({
      LLM_TEMPERATURE: '0',
      LLM_MAX_RETRIES: '0',
      LLM_FALLBACK_PROVIDER: '',
      LLM_CACHE_TTL: '0s',
      LLM_ENABLE_REFLECTION: 'false',
      LLM_JOB_MAX_ATTEMPTS: '1',
      LLM_DSL_MAX_REPAIRS: '0',
      LLM_DSL_SELECTOR_REPAIR_ENABLED: 'false',
      LLM_ALLOW_DEGRADED_FALLBACK: 'false',
      LLM_TEMPERATURE_COMPATIBILITY_RETRY: 'false',
      MAX_RETRIES: '0',
    });
  });

  it('preserves completed task and MCP facts after a later invariant fails', () => {
    const snapshot = snapshotVerificationProgress({
      tasks: [successfulTask()],
      resultsSummary: true,
      mcp: { agreedWithAdmin: true, tokenRevoked: true },
    });

    expect(snapshot).toEqual({
      results: {
        tasks: [successfulTask()],
        summary: true,
      },
      mcp: { agreedWithAdmin: true, tokenRevoked: true },
    });
  });

  it('requires a single uncached, non-degraded first-pass workflow', () => {
    const evidence = {
      expectedProvider: 'openai',
      expectedModel: 'qwen3.6-flash',
      jobs: [{
        kind: 'normalize', provider: 'openai', model: 'qwen3.6-flash',
        attemptCount: 1, maxAttempts: 1, cacheHit: false, degraded: false,
      }, {
        kind: 'generate', provider: 'openai', model: 'qwen3.6-flash',
        attemptCount: 1, maxAttempts: 1, cacheHit: false, degraded: false,
      }],
      dslJobKinds: ['generate'],
      repairCount: 0,
      maxRepairs: 0,
      replayAttempts: 1,
      tasks: [successfulTask()],
    };

    expect(() => assertStrictQualification(evidence)).not.toThrow();
    expect(() => assertStrictQualification({
      ...evidence,
      jobs: [{ ...evidence.jobs[0], cacheHit: true }, evidence.jobs[1]],
    })).toThrow(/cached output/);
    expect(() => assertStrictQualification({ ...evidence, dslJobKinds: ['generate', 'repair'] }))
      .toThrow(/non-generation/);
    expect(() => assertStrictQualification({
      ...evidence,
      jobs: [evidence.jobs[0], { ...evidence.jobs[1], provider: 'deterministic', degraded: true }],
    })).toThrow(/unexpected provider\/model/);
    expect(() => assertStrictQualification({ ...evidence, replayAttempts: 2 }))
      .toThrow(/2 replay attempts/);
  });

  it('qualifies an ordered multi-case matrix including an expected failure', () => {
    const jobs = [{
      kind: 'generate',
      provider: 'openai',
      model: 'qwen3.6-flash',
      attemptCount: 1,
      maxAttempts: 1,
      cacheHit: false,
      degraded: false,
    }];
    const failure = {
      caseName: 'unknown-category',
      expectedStatus: 'failed' as const,
      taskId: 'task-unknown-category',
      status: 'failed',
      validBatches: 0,
      invalidBatches: 0,
      rows: 0,
      summary: false,
      maxRetries: 0,
      retryCount: 0,
      executionAttempts: 1,
      mcpAgreedWithAdmin: true,
      mcpTokenRevoked: true,
    };
    const evidence = {
      expectedProvider: 'openai',
      expectedModel: 'qwen3.6-flash',
      jobs,
      dslJobKinds: ['generate'],
      repairCount: 0,
      maxRepairs: 0,
      replayAttempts: 1,
      taskExpectations: [
        { caseName: 'small-category', expectedStatus: 'done' as const },
        { caseName: 'large-category', expectedStatus: 'done' as const },
        { caseName: 'unknown-category', expectedStatus: 'failed' as const },
      ],
      tasks: [
        successfulTask('small-category'),
        successfulTask('large-category'),
        failure,
      ],
    };

    expect(() => assertStrictQualification(evidence)).not.toThrow();
    expect(() => assertStrictQualification({
      ...evidence,
      tasks: [evidence.tasks[1], evidence.tasks[0], failure],
    })).toThrow(/task case 0 expected small-category:done/);
    expect(() => assertStrictQualification({
      ...evidence,
      tasks: [evidence.tasks[0], evidence.tasks[1], {
        ...failure,
        validBatches: 1,
        rows: 1,
      }],
    })).toThrow(/retained output/);
    expect(() => assertStrictQualification({
      ...evidence,
      tasks: [evidence.tasks[0], {
        ...evidence.tasks[1],
        mcpTokenRevoked: false,
      }, failure],
    })).toThrow(/lacked per-case MCP agreement or token revocation/);
    expect(() => assertStrictQualification({
      ...evidence,
      tasks: [evidence.tasks[0], evidence.tasks[1], {
        ...failure,
        summary: true,
        summaryStatus: 'success',
      }],
    })).toThrow(/retained a non-failure summary/);
    expect(() => assertStrictQualification({
      ...evidence,
      tasks: [evidence.tasks[0], evidence.tasks[1], {
        ...failure,
        summary: true,
        summaryStatus: 'failure',
      }],
    })).not.toThrow();
  });

  it('never filters a deterministic DSL fallback from strict evidence', () => {
    const job = (kind: string, provider: string, degraded = false) => ({
      kind,
      provider,
      model: provider === 'openai' ? 'qwen3.6-flash' : '',
      attemptCount: 1,
      maxAttempts: 1,
      cacheHit: false,
      degraded,
    });
    const jobs = strictQualificationJobs(
      [job('normalize', 'deterministic'), job('candidates', 'deterministic', true)],
      [job('generate', 'deterministic', true)],
    );

    expect(jobs).toHaveLength(2);
    expect(jobs.map(({ kind }) => kind)).toEqual(['candidates', 'generate']);
    expect(jobs[1]).toMatchObject({ provider: 'deterministic', degraded: true });
    expect(() => assertStrictQualification({
      expectedProvider: 'openai',
      expectedModel: 'qwen3.6-flash',
      jobs,
      dslJobKinds: ['generate'],
      repairCount: 0,
      maxRepairs: 0,
      replayAttempts: 1,
      tasks: [{ ...successfulTask(), rows: 1 }],
    })).toThrow(/unexpected provider\/model/);
  });

  it('applies the semantic oracle to every persisted replay row before approval', () => {
    const oracle = vi.fn();
    const rows = [{ size: '11-inch', price: '$599.00' }, { size: '13-inch', price: '$799.00' }];
    assertPersistedReplaySemanticRows([{
      id: 'replay-1', status: 'succeeded', outputValid: true, output: rows,
    }], {
      outputFields: [
        { name: 'size', type: 'string' },
        { name: 'price', type: 'string' },
      ],
    }, oracle);

    expect(oracle).toHaveBeenCalledWith(rows, {
      type: 'object',
      properties: {
        size: { type: 'string', description: '' },
        price: { type: 'string', description: '' },
      },
      required: ['size', 'price'],
      additionalProperties: false,
    });
  });

  it('fails closed when no successful persisted replay is available', () => {
    expect(() => assertPersistedReplaySemanticRows([{
      id: 'replay-1', status: 'failed', outputValid: false, output: [{ title: 'stale' }],
    }], {
      outputFields: [{ name: 'title', type: 'string' }],
    }, () => undefined)).toThrow(/expected one successful persisted replay/);
  });
});
