import assert from 'node:assert/strict';

export interface ProviderAttemptCallEvidence {
  callCount?: unknown;
}

export function sumProviderAttemptCalls(
  attempts: readonly ProviderAttemptCallEvidence[],
  label = 'provider attempts',
): number {
  let total = 0;
  for (const [index, attempt] of attempts.entries()) {
    assert(
      Number.isSafeInteger(attempt.callCount) && Number(attempt.callCount) >= 0,
      `${label} attempt ${index} must expose a non-negative integer callCount`,
    );
    total += Number(attempt.callCount);
    assert(Number.isSafeInteger(total), `${label} callCount total exceeds the safe integer range`);
  }
  return total;
}
