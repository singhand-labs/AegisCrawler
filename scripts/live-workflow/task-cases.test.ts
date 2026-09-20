import { describe, expect, it, vi } from 'vitest';
import {
  assertTaskCaseResult,
  assertTaskResultSurfacesAgree,
  assertRevokedCredentialRejected,
  captureTaskOracle,
  executeTaskCasesSequentially,
  resolveTaskCaseContracts,
} from './task-cases';

describe('live workflow task-case contracts', () => {
  it('keeps host-only oracle failures separate from executor status', async () => {
    const failure = new Error('raw worker page oracle failed');
    await expect(captureTaskOracle(async () => { throw failure; }, {})).resolves.toBe(failure);
    await expect(captureTaskOracle(async () => undefined, {})).resolves.toBeUndefined();
  });
  it('resolves ordered success and expected-failure cases', () => {
    expect(resolveTaskCaseContracts([
      { name: 'small-category', rowBand: [1, 20] },
      { name: 'large-category' },
      { name: 'unknown-category', expectedStatus: 'failed' },
    ], [1, 100])).toEqual([
      { name: 'small-category', expectedStatus: 'done', rowBand: [1, 20] },
      { name: 'large-category', expectedStatus: 'done', rowBand: [1, 100] },
      { name: 'unknown-category', expectedStatus: 'failed', rowBand: undefined },
    ]);
  });

  it('rejects empty, ambiguous, or malformed matrices before execution', () => {
    expect(() => resolveTaskCaseContracts([], [1, 10])).toThrow(/must not be empty/);
    expect(() => resolveTaskCaseContracts([
      { name: 'same-case' },
      { name: 'same-case' },
    ], [1, 10])).toThrow(/duplicated/);
    expect(() => resolveTaskCaseContracts([{ name: 'Bad Case' }], [1, 10]))
      .toThrow(/lowercase kebab-case/);
    expect(() => resolveTaskCaseContracts([{
      name: 'bad-status',
      expectedStatus: 'cancelled' as never,
    }], [1, 10])).toThrow(/reviewed terminal/);
    expect(() => resolveTaskCaseContracts([{
      name: 'failed-with-rows',
      expectedStatus: 'failed',
      rowBand: [1, 10],
    }], [1, 10])).toThrow(/cannot declare a success rowBand/);
    expect(() => resolveTaskCaseContracts([{ name: 'bad-band' }], [0, 10]))
      .toThrow(/positive integer/);
  });

  it('accepts exact successful and failed result evidence', () => {
    const [success, failure] = resolveTaskCaseContracts([
      { name: 'success', rowBand: [2, 4] },
      { name: 'failure', expectedStatus: 'failed' },
    ], [1, 10]);

    expect(() => assertTaskCaseResult(success, {
      status: 'done',
      validBatches: 3,
      invalidBatches: 0,
      rows: 3,
      hasSummary: true,
      summaryStatus: 'success',
    })).not.toThrow();
    expect(() => assertTaskCaseResult(failure, {
      status: 'failed',
      validBatches: 0,
      invalidBatches: 0,
      rows: 0,
      hasSummary: false,
    })).not.toThrow();
  });

  it('fails closed on wrong terminal state, stale failure rows, and weak success evidence', () => {
    const [success, failure] = resolveTaskCaseContracts([
      { name: 'success', rowBand: [2, 4] },
      { name: 'failure', expectedStatus: 'failed' },
    ], [1, 10]);

    expect(() => assertTaskCaseResult(success, {
      status: 'failed',
      errorMessage: 'worker page callback failed',
      validBatches: 0,
      invalidBatches: 0,
      rows: 0,
      hasSummary: false,
    })).toThrow(/expected done, observed failed: worker page callback failed/);
    expect(() => assertTaskCaseResult(success, {
      status: 'done',
      validBatches: 1,
      invalidBatches: 0,
      rows: 1,
      hasSummary: true,
      summaryStatus: 'success',
    })).toThrow(/outside 2..4/);
    expect(() => assertTaskCaseResult(success, {
      status: 'done',
      validBatches: 1,
      invalidBatches: 0,
      rows: 2,
      hasSummary: false,
    })).toThrow(/omitted its final execution summary/);
    expect(() => assertTaskCaseResult(success, {
      status: 'done',
      validBatches: 1,
      invalidBatches: 0,
      rows: 2,
      hasSummary: true,
      summaryStatus: 'failure',
    })).toThrow(/summary status was failure/);
    expect(() => assertTaskCaseResult(failure, {
      status: 'failed',
      validBatches: 1,
      invalidBatches: 0,
      rows: 1,
      hasSummary: true,
    })).toThrow(/stale or accepted output/);
    expect(() => assertTaskCaseResult(failure, {
      status: 'failed',
      validBatches: 0,
      invalidBatches: 0,
      rows: 0,
      hasSummary: true,
      summaryStatus: 'success',
    })).toThrow(/retained a non-failure summary: success/);
    expect(() => assertTaskCaseResult(failure, {
      status: 'failed',
      validBatches: 0,
      invalidBatches: 0,
      rows: 0,
      hasSummary: true,
      summaryStatus: 'failure',
    })).not.toThrow();
  });

  it('requires canonical Admin/MCP payload, schema, lineage, and summary agreement', () => {
    const admin = {
      taskId: 'task-1',
      ruleId: 'rule-1',
      ruleVersion: 2,
      outputSchema: {
        type: 'object',
        properties: { title: { type: 'string' }, price: { type: 'number' } },
      },
      sourceKind: 'dsl-workflow',
      sourceAuthority: 'authoritative',
      sourceArtifactHash: 'a'.repeat(64),
      sourceExportHash: 'b'.repeat(64),
      sourceWorkflowId: 'workflow-1',
      total: 1,
      batches: [{
        id: 'result-1',
        attemptId: 'attempt-1',
        sequence: 1,
        kind: 'batch',
        payload: { title: 'Example', price: 12.5 },
        valid: true,
        validationError: '',
        adminOnly: 'ignored',
      }],
      invalid: [],
      summary: {
        id: 'summary-1',
        attemptId: 'attempt-1',
        sequence: 2,
        kind: 'summary',
        payload: { validRows: 1, invalidRows: 0 },
        valid: true,
      },
    };
    const mcp = {
      ...admin,
      outputSchema: {
        properties: { price: { type: 'number' }, title: { type: 'string' } },
        type: 'object',
      },
      batches: [{
        id: 'result-1',
        valid: true,
        payload: { price: 12.5, title: 'Example' },
        kind: 'batch',
        sequence: 1,
        attemptId: 'attempt-1',
      }],
    };

    expect(() => assertTaskResultSurfacesAgree('example', admin, mcp)).not.toThrow();
    expect(() => assertTaskResultSurfacesAgree('example', admin, {
      ...mcp,
      batches: [{
        ...mcp.batches[0],
        id: 'different-result',
      }],
    })).toThrow(/MCP results disagree with Admin API/);
    expect(() => assertTaskResultSurfacesAgree('example', admin, {
      ...mcp,
      batches: [{
        ...mcp.batches[0],
        payload: { price: 99, title: 'Example' },
      }],
    })).toThrow(/MCP results disagree with Admin API/);
    expect(() => assertTaskResultSurfacesAgree('example', admin, {
      ...mcp,
      invalid: [{
        id: 'invalid-1',
        attemptId: 'attempt-1',
        sequence: 3,
        kind: 'batch',
        payload: { title: 'stale' },
        valid: false,
        validationError: 'missing price',
      }],
    })).toThrow(/MCP results disagree with Admin API/);
    expect(() => assertTaskResultSurfacesAgree('example', {
      ...admin,
      total: 2,
    }, mcp)).toThrow(/Admin API result page is incomplete: 1\/2/);
    expect(() => assertTaskResultSurfacesAgree('example', admin, {
      ...mcp,
      total: 'one',
    })).toThrow(/MCP results reported invalid total=one/);
  });

  it('proves a revoked credential is unusable and rejects ambiguous probes', async () => {
    const inactive = new Error('inactive');
    await expect(assertRevokedCredentialRejected(
      'MCP token',
      async () => {
        throw inactive;
      },
      (error) => error === inactive,
    )).resolves.toBeUndefined();
    await expect(assertRevokedCredentialRejected(
      'MCP token',
      async () => ({ still: 'authorized' }),
      () => false,
    )).rejects.toThrow(/remained usable after revocation/);
    await expect(assertRevokedCredentialRejected(
      'MCP token',
      async () => {
        throw new Error('network failed');
      },
      () => false,
    )).rejects.toThrow(/network failed/);
  });

  it('executes cases sequentially and stops after the first thrown case', async () => {
    let releaseFirst!: () => void;
    const firstBlocked = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    const execute = vi.fn(async (name: string) => {
      if (name === 'first') await firstBlocked;
      if (name === 'second') throw new Error('second failed');
      return `${name}-done`;
    });
    const completed: string[] = [];

    const pending = executeTaskCasesSequentially(
      ['first', 'second', 'third'],
      execute,
      (result) => {
        completed.push(result);
      },
    );
    await Promise.resolve();
    expect(execute).toHaveBeenCalledTimes(1);
    releaseFirst();
    await expect(pending).rejects.toThrow('second failed');
    expect(execute.mock.calls.map(([name]) => name)).toEqual(['first', 'second']);
    expect(completed).toEqual(['first-done']);
  });
});
