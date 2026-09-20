import { describe, expect, it } from 'vitest';
import { sumProviderAttemptCalls } from './provider-call-budget';

describe('physical provider-call evidence', () => {
  it('sums every durable attempt call count including explicit zeroes', () => {
    expect(sumProviderAttemptCalls([
      { callCount: 0 },
      { callCount: 1 },
      { callCount: 2 },
    ], 'dsl job')).toBe(3);
  });

  it.each([
    {},
    { callCount: -1 },
    { callCount: 1.5 },
    { callCount: Number.NaN },
  ])('fails closed on missing or invalid call evidence: %j', (attempt) => {
    expect(() => sumProviderAttemptCalls([attempt], 'dsl job'))
      .toThrow(/non-negative integer callCount/);
  });
});
