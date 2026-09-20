/// <reference types="vitest/globals" />
import { describe, it, expect, vi } from 'vitest';
import type { Environment, RuntimeContext } from './types';

// evaluateCondition is not exported; we exercise it via runIfAction, which IS exported.
import { runIfAction } from './executor-utils';
import { executeAction } from './actions';

// Delegate to the real executeAction so that 'then' branches containing `exit`
// actually throw ExitSignal (subclass of Error). If a negative test's condition
// mistakenly evaluated to true (taking 'then'), the ExitSignal would propagate
// out of runIfAction and fail the test with an unhandled rejection. With a
// no-op stub, those negative tests would pass even if the 'else' branch were
// never taken — which is why we route through executeAction here.
const exec = (action: any, ctx: RuntimeContext, env: Environment) =>
  executeAction(action, ctx, env);

function createCtx(): RuntimeContext {
  return {
    taskId: 't1', workerId: 'w1', ruleId: 'r1', ruleVersion: '1.0.0',
    variables: { __selectors: {} },
    extracted: {}, evaluated: {}, captured: {},
    resultsSent: 0, logsSent: 0,
  };
}

function envWith(findElementImpl: Environment['findElement']): Environment {
  return {
    findElement: findElementImpl,
    findElements: async () => [],
    sleep: async () => {},
    now: () => 0,
    transport: {
      fetchRule: async () => null,
      sendResult: async () => {},
      sendLog: async () => {},
      sendHeartbeat: async () => ({ cancelRequested: false }),
      sendStatus: async () => {},
      sendSnapshot: async () => {},
    },
    getUrl: () => 'https://example.com/',
    getTitle: () => '',
    evaluate: async () => undefined,
    screenshot: async () => null,
    saveSnapshot: async () => null,
  } as Environment;
}

async function selectedBranch(condition: Record<string, unknown>, env: Environment): Promise<'then' | 'else'> {
  const selected: string[] = [];
  await runIfAction(
    {
      condition,
      then: [{ action: 'waitForTimeout', id: 'then', ms: 0 }],
      else: [{ action: 'waitForTimeout', id: 'else', ms: 0 }],
    },
    createCtx(),
    env,
    async (action) => { selected.push(action.id ?? ''); },
  );
  expect(selected).toHaveLength(1);
  return selected[0] as 'then' | 'else';
}

describe('evaluateCondition: elementVisible', () => {
  it('returns true when findElement resolves with visible:true', async () => {
    // findElement receives an interpolated target with visible:true merged in.
    const env = envWith(async (target: any) => {
      expect(target.visible).toBe(true);
      return { tagName: 'div' } as any;
    });
    await expect(selectedBranch(
      { type: 'elementVisible', target: { selector: '.x' } },
      env,
    )).resolves.toBe('then');
  });

  it('returns false when findElement returns null', async () => {
    const env = envWith(async () => null);
    await expect(selectedBranch(
      { type: 'elementVisible', target: { selector: '.x' } },
      env,
    )).resolves.toBe('else');
  });
});

describe('evaluateCondition: elementHidden', () => {
  it('returns true when visible filter finds nothing', async () => {
    const env = envWith(async () => null);
    await expect(selectedBranch(
      { type: 'elementHidden', target: { selector: '.x' } },
      env,
    )).resolves.toBe('then');
  });

  it('returns false when element is visible', async () => {
    const env = envWith(async () => ({ tagName: 'div' }) as any);
    await expect(selectedBranch(
      { type: 'elementHidden', target: { selector: '.x' } },
      env,
    )).resolves.toBe('else');
  });
});

describe('evaluateCondition: textEquals', () => {
  it('trims both observed and expected text before strict equality', async () => {
    const env = envWith(async () => ({ textContent: '  Save  ' }) as any);
    await expect(selectedBranch(
      { type: 'textEquals', target: { selector: '.btn' }, text: '  Save  ' },
      env,
    )).resolves.toBe('then');
  });

  it('case-sensitive (save ≠ Save)', async () => {
    const env = envWith(async () => ({ textContent: 'Save' }) as any);
    await expect(selectedBranch(
      { type: 'textEquals', target: { selector: '.btn' }, text: 'save' },
      env,
    )).resolves.toBe('else');
  });

  it('coerces non-string interpolation via String()', async () => {
    const env = envWith(async () => ({ textContent: '42' }) as any);
    await expect(selectedBranch(
      { type: 'textEquals', target: { selector: '.n' }, text: 42 },
      env,
    )).resolves.toBe('then');
  });
});

describe('evaluateCondition: textMatches', () => {
  it('bare-string regex test()', async () => {
    const env = envWith(async () => ({ textContent: 'abc foo123' }) as any);
    await expect(selectedBranch(
      { type: 'textMatches', target: { selector: '.x' }, pattern: 'foo\\d+' },
      env,
    )).resolves.toBe('then');
  });

  it('throws ConditionError on invalid pattern (not swallowed)', async () => {
    const env = envWith(async () => ({ textContent: 'x' }) as any);
    await expect(
      runIfAction(
        { condition: { type: 'textMatches', target: { selector: '.x' }, pattern: '[invalid' }, then: [] },
        createCtx(), env, exec,
      ),
    ).rejects.toThrow(/ConditionError/);
  });
});

describe('evaluateCondition: valueEquals', () => {
  it('input.value matches string interpolation', async () => {
    const env = envWith(async () => ({ tagName: 'INPUT', value: '42' }) as any);
    await expect(selectedBranch(
      { type: 'valueEquals', target: { selector: '#qty' }, value: '42' },
      env,
    )).resolves.toBe('then');
  });

  it('numeric condition.value coerced via String()', async () => {
    const env = envWith(async () => ({ tagName: 'INPUT', value: '42' }) as any);
    await expect(selectedBranch(
      { type: 'valueEquals', target: { selector: '#qty' }, value: 42 },
      env,
    )).resolves.toBe('then');
  });

  it('non-form element returns false (no attribute fallback)', async () => {
    const env = envWith(async () => ({ tagName: 'DIV', getAttribute: () => '42' }) as any);
    await expect(selectedBranch(
      { type: 'valueEquals', target: { selector: '.d' }, value: '42' },
      env,
    )).resolves.toBe('else');
  });
});

describe('evaluateCondition: networkIdle', () => {
  it('returns true when env.waitForNetworkIdle resolves', async () => {
    const env: Environment = {
      ...envWith(async () => null),
      waitForNetworkIdle: vi.fn(async () => {}),
    };
    await expect(selectedBranch(
      { type: 'networkIdle', idleTime: 200, timeout: 1000 },
      env,
    )).resolves.toBe('then');
    expect((env as any).waitForNetworkIdle).toHaveBeenCalledWith(200, 1000);
  });

  it('applies defaults idleTime=500, timeout=3000 when omitted', async () => {
    const env: Environment = {
      ...envWith(async () => null),
      waitForNetworkIdle: vi.fn(async () => {}),
    };
    await expect(selectedBranch({ type: 'networkIdle' }, env)).resolves.toBe('then');
    expect((env as any).waitForNetworkIdle).toHaveBeenCalledWith(500, 3000);
  });

  it('returns false when env.waitForNetworkIdle throws TimeoutError', async () => {
    const env: Environment = {
      ...envWith(async () => null),
      waitForNetworkIdle: vi.fn(async () => { throw new Error('TimeoutError: idle'); }),
    };
    await expect(selectedBranch({ type: 'networkIdle' }, env)).resolves.toBe('else');
  });

  it('rethrows as ConditionError on non-Timeout rejection', async () => {
    const env: Environment = {
      ...envWith(async () => null),
      waitForNetworkIdle: vi.fn(async () => { throw new Error('weird'); }),
    };
    await expect(
      runIfAction(
        { condition: { type: 'networkIdle' }, then: [] },
        createCtx(), env, exec,
      ),
    ).rejects.toThrow(/ConditionError/);
  });

  it('throws ConditionError when env.waitForNetworkIdle absent', async () => {
    const env = envWith(async () => null); // no waitForNetworkIdle
    await expect(
      runIfAction(
        { condition: { type: 'networkIdle' }, then: [] },
        createCtx(), env, exec,
      ),
    ).rejects.toThrow(/ConditionError/);
  });
});
