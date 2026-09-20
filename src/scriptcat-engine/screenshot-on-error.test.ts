import { describe, it, expect, vi } from 'vitest';
import { runRule } from './executor';

function makeEnv(snapshotReturn: any = { name: 'snap', type: 'html', data: '<html></html>' }) {
  const snapshotsSent: any[] = [];
  return {
    env: {
      signal: undefined,
      getUrl: () => 'https://example.com/',
      getTitle: () => 'T',
      sleep: vi.fn(async () => {}),
      evaluate: vi.fn(async () => undefined),
      findElement: vi.fn(async () => null),
      findElements: vi.fn(async () => []),
      now: () => 0,
      screenshot: vi.fn(async () => null),
      saveSnapshot: vi.fn(async () => snapshotReturn),
      transport: {
        fetchRule: vi.fn(async () => null),
        sendResult: vi.fn(async () => {}),
        sendLog: vi.fn(async () => {}),
        sendStatus: vi.fn(async () => {}),
        sendHeartbeat: vi.fn(async () => ({ cancelRequested: false })),
        sendSnapshot: vi.fn(async (s: any) => { snapshotsSent.push(s); }),
      },
    } as any,
    snapshotsSent,
  };
}

describe('runRule screenshotOnError', () => {
  it('triggers saveSnapshot + sendSnapshot on first fatalError when flag is true', async () => {
    const { env, snapshotsSent } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'missing' } }],
      screenshotOnError: true,
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.saveSnapshot as any).mock.calls.length).toBe(1);
    expect(snapshotsSent).toHaveLength(1);
  });

  it('does NOT trigger saveSnapshot when flag is absent/false', async () => {
    const { env, snapshotsSent } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'missing' } }],
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.saveSnapshot as any).mock.calls.length).toBe(0);
    expect(snapshotsSent).toHaveLength(0);
  });

  it('is idempotent — multiple fatalError assignments trigger screenshot only once', async () => {
    const { env, snapshotsSent } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      // Site B: main step fails with default onError:'stop' → mainResult.error
      // → markFatalError fires (1st invocation, screenshot captured).
      steps: [{ action: 'click', target: { selector: 'missing' } }],
      screenshotOnError: true,
      hooks: {
        // Site A: afterAll throws ExitSignal('failure') → applyExit catches it
        // → markFatalError fires (2nd invocation, idempotent guard skips).
        afterAll: [{ action: 'exit', status: 'failure', message: 'afterAll failed' }],
      },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.saveSnapshot as any).mock.calls.length).toBe(1);
    expect(snapshotsSent).toHaveLength(1);
  });

  it('saveSnapshot failure does not escalate (logs warn, continues)', async () => {
    const { env, snapshotsSent } = makeEnv(null);
    (env.saveSnapshot as any).mockRejectedValueOnce(new Error('canvas blocked'));
    const rule: any = {
      id: 'r', version: '1',
      steps: [{ action: 'click', target: { selector: 'missing' } }],
      screenshotOnError: true,
    };
    const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(result.status).toBe('failure');
    expect(snapshotsSent).toHaveLength(0);
  });

  it('does not trigger on beforeAll failure (finalize path)', async () => {
    const { env, snapshotsSent } = makeEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      screenshotOnError: true,
      hooks: {
        beforeAll: [{ action: 'click', target: { selector: 'missing' } }],
      },
    };
    await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect((env.saveSnapshot as any).mock.calls.length).toBe(0);
    expect(snapshotsSent).toHaveLength(0);
  });
});
