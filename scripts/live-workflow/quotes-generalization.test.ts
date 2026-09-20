// @vitest-environment node
import { describe, expect, it } from 'vitest';
import {
  QUOTES_ONE_PAGE_CONTRACT,
  QUOTES_ONE_PAGE_REQUIREMENT,
  QUOTES_PROJECTION_CONTRACT,
  QUOTES_PROJECTION_REQUIREMENT,
  QUOTES_RENAMED_CONTRACT,
  QUOTES_RENAMED_REQUIREMENT,
} from './quotes-generalization';
import {
  assertQuotesContractRequirement,
  bindQuotesContractInput,
} from './quotes-by-tag';

describe('quotes live generalization contracts', () => {
  it.each([
    ['one-page', QUOTES_ONE_PAGE_REQUIREMENT, QUOTES_ONE_PAGE_CONTRACT],
    ['renamed', QUOTES_RENAMED_REQUIREMENT, QUOTES_RENAMED_CONTRACT],
    ['projection', QUOTES_PROJECTION_REQUIREMENT, QUOTES_PROJECTION_CONTRACT],
  ])('binds the exact %s requirement and unseen task input', (_name, requirement, contract) => {
    expect(() => assertQuotesContractRequirement(requirement, contract)).not.toThrow();
    expect(bindQuotesContractInput(
      { [contract.inputName]: 'placeholder' },
      contract.inputName,
      contract.taskTag,
      'task',
    )).toEqual({ [contract.inputName]: contract.taskTag });
    expect(contract.taskTag).not.toBe(contract.replayTag);
  });

  it('keeps every scenario contract distinct', () => {
    expect(new Set([
      QUOTES_ONE_PAGE_CONTRACT.inputName,
      QUOTES_RENAMED_CONTRACT.inputName,
      QUOTES_PROJECTION_CONTRACT.inputName,
    ]).size).toBe(3);
    expect(Object.keys(QUOTES_RENAMED_CONTRACT.outputMap).sort())
      .toEqual(['profile_url', 'text', 'writer']);
    expect(Object.keys(QUOTES_PROJECTION_CONTRACT.outputMap).sort())
      .toEqual(['author', 'quote']);
  });

  it.each([
    ['renamed', QUOTES_RENAMED_REQUIREMENT],
    ['projection', QUOTES_PROJECTION_REQUIREMENT],
  ])('keeps the replay-supported fixedCount pagination contract in %s', (_name, requirement) => {
    expect(requirement.description).toContain('fixedCount pagination loop with a positive count of at most 10');
    expect(requirement.description).toContain('no visible Next link remains');
  });
});
