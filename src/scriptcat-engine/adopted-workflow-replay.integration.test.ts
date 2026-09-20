/// <reference types="vitest/globals" />
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import { BrowserEnvironment } from '../worker/BrowserEnvironment';
import { DEFAULT_REPLAY_HUMANIZE, runRule } from './executor';
import type { ExecutionResult, Rule, Snapshot, Transport } from './types';

const fixtureBytes = readFileSync(join(
  process.cwd(),
  'server/internal/api/testdata/adopted-real-executor-replay.json',
));
// The Go platform E2E pins the same digest before using these captured replay
// inputs, making this an exact cross-language bridge rather than a hand-waved
// duplicate fixture.
const fixtureSha256 = '7713eb9aab0457cacf233d7cbb0343db0dde35760108f3828e35a6591fcff7ab';

interface EmittedResult {
  payload: unknown;
  immediate: boolean;
}

interface StatusEvent {
  status: string;
  message: string;
}

interface ReplayCompletion {
  succeeded: boolean;
  diagnostics: {
    executor: 'production-runRule';
    scenario: string;
    executionStatus: ExecutionResult['status'];
    businessResultCount: number;
  };
  output: unknown;
  errorCode?: string;
  errorMessage?: string;
}

interface ReplayScenario {
  name: string;
  taskId: string;
  workerId: string;
  title: string;
  body: string;
  expected: {
    inputValue: string;
    executionResult: ExecutionResult;
    emittedResults: EmittedResult[];
    statusEvents: StatusEvent[];
    replayCompletion: ReplayCompletion;
  };
}

interface ReplayFixture {
  schemaVersion: string;
  rule: Rule;
  variables: Record<string, unknown>;
  scenarios: ReplayScenario[];
}

class RecordingTransport implements Transport {
  readonly emittedResults: EmittedResult[] = [];
  readonly statusEvents: StatusEvent[] = [];

  async fetchRule(): Promise<Rule | null> {
    return null;
  }

  async sendResult(payload: unknown, immediate = false): Promise<void> {
    this.emittedResults.push({ payload, immediate });
  }

  async sendLog(): Promise<void> {}

  async sendHeartbeat(): Promise<{ cancelRequested: boolean }> {
    return { cancelRequested: false };
  }

  async sendStatus(status: string, message = ''): Promise<void> {
    this.statusEvents.push({ status, message });
  }

  async sendSnapshot(_snapshot: Snapshot): Promise<void> {}
}

function installDeterministicLayout(): void {
  for (const element of document.querySelectorAll('*')) {
    Object.defineProperty(element, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({
        x: 0,
        y: 0,
        width: 100,
        height: 20,
        top: 0,
        right: 100,
        bottom: 20,
        left: 0,
        toJSON: () => ({}),
      }),
    });
  }
}

function replayCompletionFrom(
  scenario: ReplayScenario,
  executionResult: ExecutionResult,
  emittedResults: EmittedResult[],
): ReplayCompletion {
  const businessResults = emittedResults.filter(({ payload }) => (
    typeof payload !== 'object'
    || payload === null
    || !('__final' in payload)
  ));
  const output = businessResults.length === 1
    ? businessResults[0].payload
    : executionResult.partialData;
  const completion: ReplayCompletion = {
    succeeded: executionResult.status === 'success',
    diagnostics: {
      executor: 'production-runRule',
      scenario: scenario.name,
      executionStatus: executionResult.status,
      businessResultCount: businessResults.length,
    },
    output,
  };
  if (!completion.succeeded) {
    completion.errorCode = executionResult.error?.type ?? 'REPLAY_FAILED';
    completion.errorMessage = executionResult.message ?? 'production runRule replay failed';
  }
  return completion;
}

describe('adopted workflow real-executor replay bridge', () => {
  it('captures exact production runRule outcomes consumed by the Go workflow API test', async () => {
    expect(createHash('sha256').update(fixtureBytes).digest('hex')).toBe(fixtureSha256);
    const fixture = JSON.parse(fixtureBytes.toString('utf8')) as ReplayFixture;
    expect(fixture.schemaVersion).toBe('aegiscrawler.real-executor-replay.v1');

    for (const scenario of fixture.scenarios) {
      document.title = scenario.title;
      document.body.innerHTML = scenario.body;
      installDeterministicLayout();
      const transport = new RecordingTransport();
      const environment = new BrowserEnvironment(transport, window, false, false);
      const sleep = vi.spyOn(environment, 'sleep').mockResolvedValue(undefined);

      const executionResult = await runRule({
        rule: fixture.rule,
        taskId: scenario.taskId,
        workerId: scenario.workerId,
        variables: fixture.variables,
        env: environment,
        humanizeReplay: true,
      });
      const replayCompletion = replayCompletionFrom(
        scenario,
        executionResult,
        transport.emittedResults,
      );

      expect((document.querySelector('#search') as HTMLInputElement).value).toBe(
        scenario.expected.inputValue,
      );
      const pacingDelays = sleep.mock.calls.map(([ms]) => ms);
      expect(pacingDelays[0]).toBeGreaterThanOrEqual(DEFAULT_REPLAY_HUMANIZE.preDelay![0]);
      expect(pacingDelays[0]).toBeLessThanOrEqual(DEFAULT_REPLAY_HUMANIZE.preDelay![1]);
      const typingDelays = pacingDelays.slice(1, -1);
      expect(typingDelays).toHaveLength(scenario.expected.inputValue.length);
      expect(typingDelays.every((ms) => ms >= DEFAULT_REPLAY_HUMANIZE.typingDelay![0]
        && ms <= DEFAULT_REPLAY_HUMANIZE.typingDelay![1])).toBe(true);
      expect(pacingDelays.at(-1)).toBeGreaterThanOrEqual(DEFAULT_REPLAY_HUMANIZE.postDelay![0]);
      expect(pacingDelays.at(-1)).toBeLessThanOrEqual(DEFAULT_REPLAY_HUMANIZE.postDelay![1]);
      expect(executionResult).toEqual(scenario.expected.executionResult);
      expect(transport.emittedResults).toEqual(scenario.expected.emittedResults);
      expect(transport.statusEvents).toEqual(scenario.expected.statusEvents);
      expect(replayCompletion).toEqual(scenario.expected.replayCompletion);
    }
  });
});
