import { describe, expect, it, vi } from 'vitest';
import {
  resolveScenarioTaskCases,
  type LiveWorkflowTaskScenarioContract,
} from './task-cases';

type OracleContext = { url: string };

function scenario(
  overrides: Partial<LiveWorkflowTaskScenarioContract<OracleContext>> = {},
): LiveWorkflowTaskScenarioContract<OracleContext> {
  return {
    rowBand: [1, 10],
    ...overrides,
  };
}

describe('live workflow scenario task-case integration', () => {
  it('maps the historical single-task callbacks to a default case', () => {
    const bind = vi.fn((inputs: Record<string, unknown>) => ({
      ...inputs,
      category: 'legacy',
    }));
    const capture = vi.fn(async () => undefined);
    const sharedRows = vi.fn();
    const taskRows = vi.fn();
    const [resolved] = resolveScenarioTaskCases(scenario({
      adjustTaskInputs: bind,
      captureTaskOracle: capture,
      assertRows: sharedRows,
      assertTaskRows: taskRows,
    }));

    expect(resolved).toMatchObject({
      name: 'default',
      expectedStatus: 'done',
      rowBand: [1, 10],
      bindInputs: bind,
      captureOracle: capture,
      assertRows: taskRows,
    });
    expect(resolved.bindInputs({ seed: true })).toEqual({
      seed: true,
      category: 'legacy',
    });
    expect(sharedRows).not.toHaveBeenCalled();
  });

  it('composes shared and case-specific successful row assertions', () => {
    const sharedRows = vi.fn();
    const caseRows = vi.fn();
    const [resolved] = resolveScenarioTaskCases(scenario({
      assertRows: sharedRows,
      taskCases: [{
        name: 'known-category',
        bindInputs: (inputs) => ({ ...inputs, category: 'known' }),
        assertRows: caseRows,
      }],
    }));

    resolved.assertRows?.([{ title: 'Known' }], { type: 'object' });
    expect(sharedRows).toHaveBeenCalledOnce();
    expect(caseRows).toHaveBeenCalledOnce();
  });

  it('rejects ambiguous or incomplete matrix glue before execution', () => {
    expect(() => resolveScenarioTaskCases(scenario({
      adjustTaskInputs: (inputs) => inputs,
      taskCases: [{
        name: 'known-category',
        bindInputs: (inputs) => inputs,
        assertRows: () => undefined,
      }],
    }))).toThrow(/cannot be combined with the legacy adjustTaskInputs/);

    expect(() => resolveScenarioTaskCases(scenario({
      taskCases: [{
        name: 'missing-binding',
        assertRows: () => undefined,
      } as never],
    }))).toThrow(/requires an exact input-binding function/);

    expect(() => resolveScenarioTaskCases(scenario({
      taskCases: [{
        name: 'missing-semantics',
        bindInputs: (inputs) => inputs,
      }],
    }))).toThrow(/requires a semantic row assertion/);

    expect(() => resolveScenarioTaskCases(scenario({
      taskCases: [{
        name: 'expected-failure',
        expectedStatus: 'failed',
        bindInputs: (inputs) => inputs,
        captureOracle: async () => undefined,
      }],
    }))).toThrow(/cannot declare a success oracle or row assertion/);
  });
});
