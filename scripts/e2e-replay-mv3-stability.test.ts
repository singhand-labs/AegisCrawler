import { describe, expect, it } from 'vitest';
import {
  REPLAY_STABILITY_SCENARIOS,
  buildReplayStabilitySchedule,
  serviceWorkerRestartPhase,
} from './e2e-replay-mv3-stability';

describe('MV3 replay stability schedule', () => {
  it.each([
    [20, {
      'navigation-reinjection': 6,
      'duplicate-terminal': 4,
      'service-worker-restart': 4,
      'retained-tab-supersession': 2,
      'approval-abort-cleanup': 2,
      'terminal-cleanup-matrix': 2,
    }],
    [50, {
      'navigation-reinjection': 15,
      'duplicate-terminal': 10,
      'service-worker-restart': 10,
      'retained-tab-supersession': 5,
      'approval-abort-cleanup': 5,
      'terminal-cleanup-matrix': 5,
    }],
  ] as const)('uses the weighted lifecycle matrix across %i iterations', (iterations, expectedCounts) => {
    const schedule = buildReplayStabilitySchedule(iterations, 20_260_723);
    const counts = Object.fromEntries(
      REPLAY_STABILITY_SCENARIOS.map((scenario) => [
        scenario,
        schedule.filter((candidate) => candidate === scenario).length,
      ]),
    );

    expect(schedule).toHaveLength(iterations);
    expect(new Set(schedule)).toEqual(new Set(REPLAY_STABILITY_SCENARIOS));
    expect(counts).toEqual(expectedCounts);
  });

  it('is deterministic for a seed and reshuffles each weighted block', () => {
    const first = buildReplayStabilitySchedule(20, 17);
    const second = buildReplayStabilitySchedule(20, 17);

    expect(second).toEqual(first);
    expect(new Set(first.slice(0, 10))).toEqual(new Set(REPLAY_STABILITY_SCENARIOS));
    expect(new Set(first.slice(10, 20))).toEqual(new Set(REPLAY_STABILITY_SCENARIOS));
    expect(first.slice(10, 20)).not.toEqual(first.slice(0, 10));
  });

  it('distributes restart occurrences across all three lifecycle phases', () => {
    const routine = Array.from({ length: 4 }, (_, index) => serviceWorkerRestartPhase(index));
    const qualification = Array.from({ length: 10 }, (_, index) => serviceWorkerRestartPhase(index));

    expect(routine).toEqual(['pre-navigation', 'checkpoint', 'post-navigation', 'pre-navigation']);
    expect(qualification.filter((phase) => phase === 'pre-navigation')).toHaveLength(4);
    expect(qualification.filter((phase) => phase === 'checkpoint')).toHaveLength(3);
    expect(qualification.filter((phase) => phase === 'post-navigation')).toHaveLength(3);
  });

  it('rejects invalid counts and seeds', () => {
    expect(() => buildReplayStabilitySchedule(0, 1)).toThrow(/positive integer/);
    expect(() => buildReplayStabilitySchedule(1, -1)).toThrow(/unsigned 32-bit/);
    expect(() => serviceWorkerRestartPhase(-1)).toThrow(/non-negative integer/);
  });
});
