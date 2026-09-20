/// <reference types="vitest/globals" />
import { runRule, type CheckpointNavigationExpectation, type CheckpointState } from './executor';
import { runIfAction } from './executor-utils';
import { executeAction } from './actions';
import { prevPendingPath, type StepPath } from './step-path';
import type { Environment, Rule, Transport } from './types';
import { findElement, findElements } from './selectors';

const mockTransport: Transport = {
  fetchRule: async () => null,
  sendResult: async () => {},
  sendLog: async () => {},
  sendHeartbeat: async () => ({ cancelRequested: false }),
  sendStatus: async () => {},
  sendSnapshot: async () => {},
};

function createMockEnv(): Environment {
  return {
    findElement: (target, timeout) => findElement(target, timeout),
    findElements: (target, timeout) => findElements(target, timeout),
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    now: () => Date.now(),
    transport: mockTransport,
    getUrl: () => 'https://example.com/',
    getTitle: () => 'Test',
    evaluate: async (script, ctx, args) => {
      const fn = new Function('ctx', 'args', script);
      return fn(ctx, args);
    },
    screenshot: async () => null,
    saveSnapshot: async () => null,
  };
}

function rule(steps: any[]): Rule {
  return {
    id: 'test',
    version: '1.0.0',
    name: 'Test',
    domain: 'example.com',
    enabled: true,
    steps,
  };
}

async function run(steps: any[], env?: Environment) {
  return runRule({ rule: rule(steps), taskId: 't1', workerId: 'w1', env: env ?? createMockEnv() });
}

describe('executor - context & lifecycle', () => {
  it.each(['goBack', 'goForward'] as const)('checkpoints %s as unknown-destination navigation', async (action) => {
    const checkpoints: CheckpointNavigationExpectation[] = [];
    const env: Environment = {
      ...createMockEnv(),
      [action]: async () => {},
    };

    const res = await runRule({
      rule: rule([{ action }]),
      taskId: 't1',
      workerId: 'w1',
      env,
      onCheckpoint: (checkpoint) => {
        if (checkpoint.navigation) checkpoints.push(checkpoint.navigation);
      },
    });

    expect(res.status).toBe('success');
    expect(checkpoints).toEqual([
      {
        phase: 'before-navigation',
        expectedUrl: 'https://example.com/',
        urlPolicy: 'same-origin',
      },
      {
        phase: 'after-step',
        expectedUrl: 'https://example.com/',
        urlPolicy: 'expected-path',
      },
    ]);
  });

  it('describes target-path and same-origin navigation checkpoint expectations', async () => {
    document.body.innerHTML = '<button id="next">next</button>';
    const checkpoints: CheckpointNavigationExpectation[] = [];
    const env = createMockEnv();
    let currentUrl = 'https://example.com/start';
    env.getUrl = () => currentUrl;
    env.sleep = async () => { currentUrl = 'https://example.com/'; };

    const res = await runRule({
      rule: rule([
        { action: 'navigate', url: 'https://example.com/' },
        { action: 'click', target: { selector: '#next' }, timeout: 50 },
      ]),
      taskId: 't1',
      workerId: 'w1',
      env,
      onCheckpoint: (checkpoint) => {
        if (checkpoint.navigation) checkpoints.push(checkpoint.navigation);
      },
    });

    expect(res.status).toBe('success');
    expect(checkpoints).toEqual([
      {
        phase: 'before-navigation',
        expectedUrl: 'https://example.com/',
        urlPolicy: 'expected-path',
      },
      {
        phase: 'after-step',
        expectedUrl: 'https://example.com/',
        urlPolicy: 'expected-path',
      },
      {
        phase: 'before-navigation',
        expectedUrl: 'https://example.com/',
        urlPolicy: 'same-origin',
      },
      {
        phase: 'after-step',
        expectedUrl: 'https://example.com/',
        urlPolicy: 'expected-path',
      },
    ]);
  });

  it('accepts initial context data', async () => {
    const res = await runRule({
      rule: rule([{ action: 'evaluate', script: 'return 1', name: 'ok' }]),
      taskId: 't1',
      workerId: 'w1',
      env: createMockEnv(),
      initialContext: { extracted: { a: 1 }, evaluated: { b: 2 }, captured: { c: 3 } },
    });
    expect(res.status).toBe('success');
    expect((res.partialData as any).a).toBe(1);
  });

  it('returns failure on exit action with failure status', async () => {
    const res = await run([{ action: 'exit', status: 'failure', message: 'bad' }]);
    expect(res.status).toBe('failure');
    expect(res.message).toBe('bad');
  });

  it('returns cancelled on exit action with cancelled status', async () => {
    const res = await run([{ action: 'exit', status: 'cancelled', message: 'stop' }]);
    expect(res.status).toBe('cancelled');
    expect(res.message).toBe('stop');
  });

  it('finalizes when beforeAll hook fails', async () => {
    const r = rule([{ action: 'evaluate', script: 'return 1' }]);
    r.hooks = { beforeAll: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }] };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('logs warning when afterAll hook returns an error', async () => {
    const r = rule([{ action: 'evaluate', script: 'return 1' }]);
    r.hooks = { afterAll: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }] };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('success');
  });

  it('logs warning when onError hook returns an error', async () => {
    const r = rule([{ action: 'click', target: { selector: '#missing' }, timeout: 50 }]);
    r.hooks = { onError: [{ action: 'click', target: { selector: '#missing2' }, timeout: 50 }] };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('failure');
  });

  it('logs warning when cleanup hook returns an error', async () => {
    const r = rule([{ action: 'evaluate', script: 'return 1' }]);
    r.hooks = { cleanup: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }] };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('success');
  });
});

describe('executor - extract', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="app">
        <article class="card" data-id="42">
          <h2 class="title">Product A</h2>
          <span class="price">$12.50</span>
          <a href="/item/42">link</a>
          <img src="/img/42.png" alt="thumb">
        </article>
        <article class="card" data-id="43">
          <h2 class="title">Product B</h2>
          <span class="price">$8.00</span>
        </article>
      </div>
    `;
  });

  it('extracts single item fields', async () => {
    const steps = [
      {
        action: 'extract',
        name: 'item',
        target: { selector: '.card' },
        fields: {
          id: { type: 'attr', attr: 'data-id' },
          title: { type: 'text', selector: '.title' },
          price: { type: 'number', selector: '.price', regex: '\\d+\\.?\\d*' },
        },
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).item).toMatchObject({ id: '42', title: 'Product A', price: 12.5 });
  });

  it('extracts multiple items', async () => {
    const steps = [
      {
        action: 'extract',
        name: 'items',
        target: { selector: '.card' },
        multiple: true,
        fields: {
          title: { type: 'text', selector: '.title' },
          link: { type: 'attr', selector: 'a', attr: 'href', resolve: true },
        },
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).items).toHaveLength(2);
    expect((res.partialData as any).items[1].title).toBe('Product B');
  });

  it('uses default values for missing fields', async () => {
    const steps = [
      {
        action: 'extract',
        name: 'item',
        target: { selector: '.card:nth-child(2)' },
        fields: {
          missing: { type: 'text', selector: '.no-such', default: 'fallback' },
          count: { type: 'count' },
        },
      },
    ];
    const res = await run(steps);
    expect((res.partialData as any).item.missing).toBe('fallback');
    expect((res.partialData as any).item.count).toBe(2); // h2 + span children
  });

  it('extracts page info', async () => {
    const steps = [{ action: 'extractPageInfo', name: 'info', fields: { url: { type: 'url' }, domain: { type: 'domain' } } }];
    const res = await run(steps);
    expect((res.partialData as any).info.url).toBe('https://example.com/');
  });
});

describe('executor - input & form', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <form>
        <input id="name" value="">
        <input id="pwd" type="password">
        <select id="country"><option value="cn">China</option><option value="us">USA</option></select>
        <input id="agree" type="checkbox">
        <input id="gender-f" type="radio" name="gender" value="f">
        <input id="gender-m" type="radio" name="gender" value="m">
        <input id="file" type="file">
      </form>
    `;
  });

  it('types into input with humanization', async () => {
    const steps = [{ action: 'type', target: { selector: '#name' }, value: 'Alice', humanize: { typingDelay: [1, 2] } }];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((document.getElementById('name') as HTMLInputElement).value).toBe('Alice');
  });

  it('clears input before typing when append=false', async () => {
    (document.getElementById('name') as HTMLInputElement).value = 'old';
    const steps = [{ action: 'type', target: { selector: '#name' }, value: 'new' }];
    const res = await run(steps);
    expect((document.getElementById('name') as HTMLInputElement).value).toBe('new');
  });

  it('selects dropdown option by value', async () => {
    const steps = [{ action: 'select', target: { selector: '#country' }, value: 'us', by: 'value' }];
    const res = await run(steps);
    expect((document.getElementById('country') as HTMLSelectElement).value).toBe('us');
  });

  it('checks and unchecks checkbox', async () => {
    const steps = [
      { action: 'check', target: { selector: '#agree' }, state: true },
      { action: 'check', target: { selector: '#agree' }, state: false },
    ];
    const res = await run(steps);
    expect((document.getElementById('agree') as HTMLInputElement).checked).toBe(false);
  });

  it('selects radio button', async () => {
    const steps = [{ action: 'selectRadio', target: { selector: '#gender-f' } }];
    const res = await run(steps);
    expect((document.getElementById('gender-f') as HTMLInputElement).checked).toBe(true);
  });

  it('uploads file to input', async () => {
    const steps = [{ action: 'uploadFile', target: { selector: '#file' }, files: ['content'] }];
    const res = await run(steps);
    // jsdom does not fully support setting input.files, but action should not throw
    expect(res.status).toBe('success');
  });

  it('focuses and blurs element', async () => {
    const steps = [{ action: 'focus', target: { selector: '#name' } }, { action: 'blur' }];
    const res = await run(steps);
    expect(document.activeElement).toBe(document.body);
  });

  it('waits for URL change after submit', async () => {
    document.body.innerHTML = '<form id="f"><input id="q" /></form>';
    let url = 'https://example.com/form';
    const env = {
      ...createMockEnv(),
      getUrl: () => url,
    };

    const form = document.getElementById('f') as HTMLFormElement;
    form.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        url = 'https://example.com/results';
      }
    });

    const steps = [{ action: 'type', target: { selector: '#q' }, value: 'test', submit: true }];
    const res = await run(steps, env);
    expect(res.status).toBe('success');
    expect(env.getUrl()).toBe('https://example.com/results');
  });

  it('waits for URL change after clicking a link', async () => {
    document.body.innerHTML = '<a id="link" href="/next">Next</a>';
    let url = 'https://example.com/';
    const env = {
      ...createMockEnv(),
      getUrl: () => url,
    };

    const link = document.getElementById('link') as HTMLAnchorElement;
    link.addEventListener('click', () => {
      url = 'https://example.com/next';
    });

    const res = await run([{ action: 'click', target: { selector: '#link' } }], env);
    expect(res.status).toBe('success');
    expect(env.getUrl()).toBe('https://example.com/next');
  });
});

describe('executor - mouse', () => {
  it('clicks element and dispatches full event sequence', async () => {
    document.body.innerHTML = '<button id="btn">Click</button>';
    const events: string[] = [];
    const btn = document.getElementById('btn')!;
    ['mousedown', 'mouseup', 'click'].forEach((t) => btn.addEventListener(t, () => events.push(t)));
    const res = await run([{ action: 'click', target: { selector: '#btn' } }]);
    expect(res.status).toBe('success');
    expect(events).toContain('click');
  });

  it('double clicks element', async () => {
    document.body.innerHTML = '<div id="target">T</div>';
    let count = 0;
    document.getElementById('target')!.addEventListener('dblclick', () => count++);
    const res = await run([{ action: 'doubleClick', target: { selector: '#target' } }]);
    expect(res.status).toBe('success');
    expect(count).toBe(1);
  });

  it('hovers element', async () => {
    document.body.innerHTML = '<div id="target">T</div>';
    const events: string[] = [];
    const el = document.getElementById('target')!;
    el.addEventListener('mouseenter', () => events.push('enter'));
    el.addEventListener('mouseout', () => events.push('out'));
    const res = await run([{ action: 'hover', target: { selector: '#target' }, duration: 10 }]);
    expect(events).toContain('enter');
  });

  it('presses and holds element', async () => {
    document.body.innerHTML = '<div id="target">T</div>';
    const events: string[] = [];
    const el = document.getElementById('target')!;
    el.addEventListener('mousedown', () => events.push('down'));
    el.addEventListener('mouseup', () => events.push('up'));
    const res = await run([{ action: 'pressAndHold', target: { selector: '#target' }, duration: 20 }]);
    expect(events).toEqual(['down', 'up']);
  });
});

describe('executor - scroll & navigate', () => {
  it('scrolls to bottom in steps', async () => {
    const res = await run([{ action: 'scrollToBottom', stepBy: true }]);
    expect(res.status).toBe('success');
  });

  it('waits for element', async () => {
    document.body.innerHTML = '<div id="late"></div>';
    const res = await run([{ action: 'waitFor', target: { selector: '#late' }, timeout: 100 }]);
    expect(res.status).toBe('success');
  });

  it('waits for timeout range', async () => {
    const start = Date.now();
    const res = await run([{ action: 'waitForTimeout', ms: [20, 40] }]);
    expect(Date.now() - start).toBeGreaterThanOrEqual(15);
  });
});

describe('executor - flow control', () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="flag">yes</div><div class="item">1</div><div class="item">2</div><div class="item">3</div>';
  });

  it('executes if branch when condition true', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '#flag' } },
        then: [{ action: 'evaluate', script: 'window.__ifResult = 1; return 1' }],
        else: [{ action: 'evaluate', script: 'window.__ifResult = 2; return 2' }],
      },
    ];
    const res = await run(steps);
    expect((window as any).__ifResult).toBe(1);
  });

  it('loops fixed count', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 3,
        as: 'i',
        steps: [{ action: 'evaluate', script: 'window.__loopIdx = ctx.loopIndex; return ctx.loopIndex' }],
      },
    ];
    const res = await run(steps);
    expect((window as any).__loopIdx).toBe(2);
  });

  it('loops while element exists with max iterations', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '.item' },
        maxIterations: 2,
        steps: [{ action: 'evaluate', script: 'return true', name: 'ok' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
  });
});

describe('executor - data operations', () => {
  it('filters array', async () => {
    const steps = [
      { action: 'evaluate', script: 'return [{a:1},{a:2},{a:3}]', name: 'raw' },
      { action: 'filter', from: 'evaluated.raw', name: 'even', criteria: { field: 'a', op: 'gt', value: 1 } },
    ];
    const res = await run(steps);
    expect((res.partialData as any).even).toHaveLength(2);
  });

  it('deduplicates array', async () => {
    const steps = [
      { action: 'evaluate', script: 'return [{id:1},{id:2},{id:1}]', name: 'raw' },
      { action: 'deduplicate', from: 'evaluated.raw', name: 'uniq', keys: ['id'] },
    ];
    const res = await run(steps);
    expect((res.partialData as any).uniq).toHaveLength(2);
  });

  it('transforms data', async () => {
    const steps = [
      { action: 'evaluate', script: 'return [{priceText:"10"},{priceText:"20"}]', name: 'raw' },
      {
        action: 'transform',
        from: 'evaluated.raw',
        name: 'out',
        operations: [{ type: 'number', field: 'priceText' }],
      },
    ];
    const res = await run(steps);
    expect((res.partialData as any).out[0].priceText).toBe(10);
  });
});

describe('executor - production actions', () => {
  it('saves checkpoint', async () => {
    const steps = [{ action: 'checkpoint', name: 'cp1', preserve: ['extracted'] }];
    const res = await run(steps);
    expect(res.status).toBe('success');
  });

  it('aborts with flush flag', async () => {
    const steps = [{ action: 'abort', reason: 'test', flushBeforeAbort: false }];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.message).toContain('Aborted');
  });

  it('checks quota and increments', async () => {
    localStorage.clear();
    const steps = [
      { action: 'checkQuota', type: 'requestsPerMinute', limit: 2, onExceeded: 'fail' },
      { action: 'checkQuota', type: 'requestsPerMinute', limit: 2, onExceeded: 'fail' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
  });

  it('throws when quota exceeded', async () => {
    localStorage.clear();
    const steps = [
      { action: 'checkQuota', type: 'requestsPerMinute', limit: 1, onExceeded: 'fail' },
      { action: 'checkQuota', type: 'requestsPerMinute', limit: 1, onExceeded: 'fail' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.message).toContain('QuotaExceeded');
  });

  it('sends logs and status', async () => {
    const logs: any[] = [];
    const statuses: any[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message, extra) => { logs.push({ level, message, extra }); };
    env.transport.sendStatus = async (status, message) => { statuses.push({ status, message }); };
    const steps = [
      { action: 'sendLog', level: 'info', message: 'hello' },
      { action: 'updateStatus', status: 'running', message: 'go' },
    ];
    const res = await run(steps, env);
    expect(res.status).toBe('success');
    expect(logs[0].message).toBe('hello');
    expect(statuses[0].status).toBe('running');
  });
});

describe('executor - error handling', () => {
  it('fails when element not found', async () => {
    const steps = [{ action: 'click', target: { selector: '#missing' }, timeout: 300 }];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('retries recoverable errors', async () => {
    const steps = [{ action: 'click', target: { selector: '#missing' }, timeout: 300, retry: { maxAttempts: 2, delay: 10 } }];
    const start = Date.now();
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(Date.now() - start).toBeGreaterThanOrEqual(20);
  });

  it('continues on non-critical errors with onError=continue', async () => {
    const steps = [
      { action: 'click', target: { selector: '#missing' }, timeout: 300, onError: 'continue' },
      { action: 'evaluate', script: 'window.__continueResult = 1; return 1' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__continueResult).toBe(1);
  });

  it('cancels execution when abort signal is already triggered', async () => {
    const controller = new AbortController();
    controller.abort('user cancelled');
    const res = await runRule({ rule: rule([{ action: 'evaluate', script: 'return 1' }]), taskId: 't1', workerId: 'w1', env: createMockEnv(), signal: controller.signal });
    expect(res.status).toBe('cancelled');
  });

  it('handles steps throwing non-Error objects', async () => {
    const steps = [{ action: 'evaluate', script: 'throw {msg:"oops"}' }];
    const res = await run(steps);
    expect(res.status).toBe('failure');
  });
});

describe('executor - step conditions & loops', () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="flag">yes</div><div class="item">1</div>';
  });

  it('skips step when condition not met', async () => {
    const steps = [
      {
        action: 'evaluate',
        condition: { type: 'elementNotExists', target: { selector: '#flag' } },
        script: 'window.__skipped = 1; return 1',
        name: 'skipped',
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__skipped).toBeUndefined();
  });

  it('evaluates textContains condition', async () => {
    const steps = [
      {
        action: 'extractText',
        condition: { type: 'textContains', target: { selector: '#flag' }, text: 'yes' },
        target: { selector: '#flag' },
        name: 'ok',
      },
    ];
    const res = await run(steps);
    expect((res.partialData as any).ok).toBe('yes');
  });

  it('handles jsTruthy condition that throws', async () => {
    const steps = [
      {
        action: 'evaluate',
        condition: { type: 'jsTruthy', script: 'throw new Error("x")' },
        script: 'return 1',
        name: 'ok',
      },
    ];
    const res = await run(steps);
    expect((res.partialData as any).ok).toBeUndefined();
  });

  it('uses checkpoint fallback when step has no id', async () => {
    const steps = [{ action: 'checkpoint' }];
    const res = await run(steps);
    expect(res.status).toBe('success');
  });

  it('runs else branch via runIfAction when condition is false', async () => {
    const ctx: any = {
      variables: {}, extracted: {}, evaluated: {}, captured: {}, resultsSent: 0, logsSent: 0,
      taskId: 't1', workerId: 'w1', ruleId: 'r1', loopIndex: 0,
    };
    const env = createMockEnv();
    await runIfAction(
      {
        action: 'if',
        condition: { type: 'elementNotExists', target: { selector: '#flag' } },
        then: [{ action: 'extractText', target: { selector: '#flag' }, name: 'thenResult' }],
        else: [{ action: 'extractText', target: { selector: '#flag' }, name: 'elseResult' }],
      },
      ctx,
      env,
      async (action: any, c: any, e: any) => executeAction(action, c, e),
    );
    expect(ctx.extracted.thenResult).toBeUndefined();
    expect(ctx.extracted.elseResult).toBe('yes');
  });

  it('runs then branch when inner if condition is true', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '#flag' } },
        then: [{ action: 'extractText', target: { selector: '#flag' }, name: 'thenResult' }],
        else: [{ action: 'extractText', target: { selector: '#flag' }, name: 'elseResult' }],
      },
    ];
    const res = await run(steps);
    expect((res.partialData as any).thenResult).toBe('yes');
    expect((res.partialData as any).elseResult).toBeUndefined();
  });

  it('loops fixedCount falls back to zero for invalid count', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 'not-a-number',
        steps: [{ action: 'evaluate', script: 'window.__loopZero = (window.__loopZero || 0) + 1; return 1' }],
      },
    ];
    await run(steps);
    expect((window as any).__loopZero).toBeUndefined();
  });

  it('loops while element exists breaks when element disappears', async () => {
    document.body.innerHTML = '<div class="item">1</div>';
    const steps = [
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '.item' },
        maxIterations: 5,
        steps: [{ action: 'evaluate', script: 'document.querySelector(".item")?.remove(); return 1' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
  });

  it('loops forEach over items expression', async () => {
    const steps = [
      { action: 'evaluate', script: 'return [1,2,3]', name: 'items' },
      {
        action: 'loop',
        type: 'forEach',
        items: 'evaluated.items',
        steps: [{ action: 'evaluate', script: 'window.__lastItem = ctx.loopItem; return ctx.loopItem', name: 'item' }],
      },
    ];
    await run(steps);
    expect((window as any).__lastItem).toBe(3);
  });

  it('emits every extracted row when forEach items uses a full-value template', async () => {
    document.body.innerHTML = `
      <div id="content_left">
        <div class="result"><a class="t">First</a><span role="text">Summary one</span></div>
        <div class="result"><a class="t">Second</a><span role="text">Summary two</span></div>
      </div>
    `;
    for (const row of Array.from(document.querySelectorAll('.result'))) {
      row.getBoundingClientRect = () => ({ width: 300, height: 80 } as DOMRect);
    }
    const sent: unknown[] = [];
    const env: Environment = {
      ...createMockEnv(),
      transport: {
        ...mockTransport,
        sendResult: async (payload) => {
          if (!(payload as Record<string, unknown>).__final) sent.push(payload);
        },
      },
    };

    const res = await run([
      {
        action: 'extract',
        name: 'items',
        target: { selector: '#content_left > .result', visible: true },
        multiple: true,
        onEmpty: 'fail',
        fields: {
          title: { type: 'text', selector: '.t' },
          summary: { type: 'text', selector: '[role="text"]' },
        },
      },
      {
        action: 'loop',
        type: 'forEach',
        items: '{{extracted.items}}',
        as: 'item',
        steps: [{
          action: 'sendResult',
          payload: {
            title: '{{loopItem.title}}',
            summary: '{{loopItem.summary}}',
          },
          immediate: true,
        }],
      },
    ], env);

    expect(res.status).toBe('success');
    expect(sent).toEqual([
      { title: 'First', summary: 'Summary one' },
      { title: 'Second', summary: 'Summary two' },
    ]);
  });

  it('handles loop break signal', async () => {
    (window as any).__breakCounter = 0;
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 5,
        steps: [
          { action: 'evaluate', script: 'window.__breakCounter++; window.__breakIdx = ctx.loopIndex; return ctx.loopIndex', name: 'idx' },
          { action: 'break', condition: { type: 'jsTruthy', script: 'window.__breakCounter === 3' } },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__breakIdx).toBe(2);
  });
});

describe('executor - edge branches', () => {
  function silentError(): Error {
    const err = new Error();
    (err as any).message = undefined;
    (err as any).stack = undefined;
    return err;
  }

  it('exits with failure status without message', async () => {
    const res = await run([{ action: 'exit', status: 'failure' }]);
    expect(res.status).toBe('failure');
  });

  it('cancels when abort signal has no reason', async () => {
    const controller = new AbortController();
    controller.abort();
    const res = await runRule({ rule: rule([{ action: 'evaluate', script: 'return 1' }]), taskId: 't1', workerId: 'w1', env: createMockEnv(), signal: controller.signal });
    expect(res.status).toBe('cancelled');
  });

  it('runs main steps when beforeAll succeeds', async () => {
    const r = rule([{ action: 'evaluate', script: 'window.__beforeOk = 1; return 1', name: 'ok' }]);
    r.hooks = { beforeAll: [{ action: 'evaluate', script: 'return 1' }] };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('success');
    expect((window as any).__beforeOk).toBe(1);
  });

  it('finalizes when beforeAll throws without message', async () => {
    const r = rule([{ action: 'evaluate', script: 'return 1' }]);
    r.hooks = {
      beforeAll: [{ action: 'evaluate', script: 'throw { message: undefined, stack: undefined }' }],
    };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('failure');
  });

  it('handles main step error without message', async () => {
    const env = createMockEnv();
    env.evaluate = async () => {
      throw silentError();
    };
    const res = await runRule({ rule: rule([{ action: 'evaluate', script: 'return 1' }]), taskId: 't1', workerId: 'w1', env });
    expect(res.status).toBe('failure');
  });

  it('handles afterAll error without message', async () => {
    const r = rule([{ action: 'evaluate', script: 'return 1' }]);
    r.hooks = {
      afterAll: [{ action: 'evaluate', script: 'throw { message: undefined, stack: undefined }' }],
    };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('success');
  });

  it('handles onError error without message', async () => {
    const r = rule([{ action: 'evaluate', script: 'throw new Error("main")' }]);
    r.hooks = {
      onError: [{ action: 'evaluate', script: 'throw { message: undefined, stack: undefined }' }],
    };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('failure');
  });

  it('handles cleanup error without message', async () => {
    const r = rule([{ action: 'evaluate', script: 'return 1' }]);
    r.hooks = {
      cleanup: [{ action: 'evaluate', script: 'throw { message: undefined, stack: undefined }' }],
    };
    const res = await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    expect(res.status).toBe('success');
  });
});

describe('executor - onStepFailure', () => {
  it('fires onStepFailure for navigate action failure', async () => {
    const failures: StepPath[] = [];
    const res = await runRule({
      rule: rule([{ action: 'click', target: { selector: '#missing' }, timeout: 50 }]),
      taskId: 't1',
      workerId: 'w1',
      env: createMockEnv(),
      onStepFailure: (path) => { failures.push(path); },
    });
    expect(res.status).toBe('failure');
    expect(failures).toEqual([[{ kind: 'top', childIdx: 0 }]]);
  });

  it('does not fire onStepFailure for non-navigate action failure', async () => {
    const failures: StepPath[] = [];
    const res = await runRule({
      rule: rule([{ action: 'evaluate', script: 'throw new Error("x")' }]),
      taskId: 't1',
      workerId: 'w1',
      env: createMockEnv(),
      onStepFailure: (path) => { failures.push(path); },
    });
    expect(res.status).toBe('failure');
    expect(failures).toEqual([]);
  });

  it('rolls back to prev path for sequential invariant (first-step edge)', async () => {
    let rolledBackTo: StepPath | undefined;
    document.body.innerHTML = '<button id="first">x</button>';
    let lastCompleted: StepPath = [];
    const res = await runRule({
      rule: rule([
        { action: 'click', target: { selector: '#first' }, timeout: 50 },
        { action: 'click', target: { selector: '#missing' }, timeout: 50 },
      ]),
      taskId: 't1',
      workerId: 'w1',
      env: createMockEnv(),
      onCheckpoint: (cp) => { lastCompleted = cp.lastCompletedStepPath; },
      onStepFailure: (path) => {
        rolledBackTo = prevPendingPath(path);
        lastCompleted = rolledBackTo;
      },
    });
    expect(res.status).toBe('failure');
    // Failed step is top[1]; prevPendingPath returns top[0] (the last fully
    // completed step before the failure).
    expect(rolledBackTo).toEqual([{ kind: 'top', childIdx: 0 }]);
    expect(lastCompleted).toEqual([{ kind: 'top', childIdx: 0 }]);
  });

  it('onStepFailure defaults to undefined (no behavior change for existing entry points)', async () => {
    const res = await runRule({
      rule: rule([{ action: 'click', target: { selector: '#missing' }, timeout: 50 }]),
      taskId: 't1',
      workerId: 'w1',
      env: createMockEnv(),
    });
    expect(res.status).toBe('failure');
  });
});

// ---------------------------------------------------------------------------
// D-2: hook checkpointing — completed hooks are skipped on resume
// ---------------------------------------------------------------------------

describe('D-2: hook checkpointing', () => {
  function hookRule(hooks: Partial<NonNullable<Rule['hooks']>>): Rule {
    const r = rule([{ action: 'evaluate', script: 'return 1', name: 'ok' }]);
    r.hooks = hooks;
    return r;
  }

  function spyEnv(): { env: Environment; logs: Array<{ level: string; message: string }> } {
    const logs: Array<{ level: string; message: string }> = [];
    const env = createMockEnv();
    env.transport = {
      ...mockTransport,
      sendLog: async (level, message) => { logs.push({ level, message }); },
    };
    return { env, logs };
  }

  // Hook step that produces a distinctive sendLog so we can detect execution.
  const hookMarker = (name: string) => [
    { action: 'sendLog', level: 'info', message: `${name}-ran` } as any,
  ];

  it('skips beforeAll when initialHooksCompleted includes beforeAll', async () => {
    const { env, logs } = spyEnv();
    const r = hookRule({ beforeAll: hookMarker('beforeAll') });
    await runRule({
      rule: r, taskId: 't1', workerId: 'w1', env,
      resumeStepPath: [{ kind: 'top', childIdx: 0 }],
      initialHooksCompleted: ['beforeAll'],
    });
    expect(logs.find((l) => l.message === 'beforeAll-ran')).toBeUndefined();
  });

  it('skips afterAll when initialHooksCompleted includes afterAll', async () => {
    const { env, logs } = spyEnv();
    const r = hookRule({ afterAll: hookMarker('afterAll') });
    await runRule({
      rule: r, taskId: 't1', workerId: 'w1', env,
      resumeStepPath: [{ kind: 'top', childIdx: 0 }],
      initialHooksCompleted: ['afterAll'],
    });
    expect(logs.find((l) => l.message === 'afterAll-ran')).toBeUndefined();
  });

  it('skips onError when initialHooksCompleted includes onError', async () => {
    const { env, logs } = spyEnv();
    // Main step fails to trigger onError.
    const r = rule([{ action: 'click', target: { selector: '#missing' }, timeout: 50 }]);
    r.hooks = { onError: hookMarker('onError') };
    await runRule({
      rule: r, taskId: 't1', workerId: 'w1', env,
      resumeStepPath: [{ kind: 'top', childIdx: 0 }],
      initialHooksCompleted: ['onError'],
    });
    expect(logs.find((l) => l.message === 'onError-ran')).toBeUndefined();
  });

  it('skips cleanup when initialHooksCompleted includes cleanup', async () => {
    const { env, logs } = spyEnv();
    const r = hookRule({ cleanup: hookMarker('cleanup') });
    await runRule({
      rule: r, taskId: 't1', workerId: 'w1', env,
      resumeStepPath: [{ kind: 'top', childIdx: 0 }],
      initialHooksCompleted: ['cleanup'],
    });
    expect(logs.find((l) => l.message === 'cleanup-ran')).toBeUndefined();
  });

  // D-2 negative: hook NOT in completed list still runs
  it('runs afterAll when initialHooksCompleted is empty', async () => {
    const { env, logs } = spyEnv();
    const r = hookRule({ afterAll: hookMarker('afterAll') });
    await runRule({
      rule: r, taskId: 't1', workerId: 'w1', env,
      resumeStepPath: [{ kind: 'top', childIdx: 0 }],
    });
    expect(logs.find((l) => l.message === 'afterAll-ran')).toBeDefined();
  });

  // D-2 regression: beforeAll completed does not suppress onError trigger
  // from a main-step failure.
  it('preserves onError trigger when beforeAll is completed but onError is not', async () => {
    const { env, logs } = spyEnv();
    const r = rule([{ action: 'click', target: { selector: '#missing' }, timeout: 50 }]);
    r.hooks = {
      beforeAll: hookMarker('beforeAll'),
      onError: hookMarker('onError'),
    };
    const res = await runRule({
      rule: r, taskId: 't1', workerId: 'w1', env,
      // resume: beforeAll is in completed (skip it), but main step still
      // fails → onError must still fire because onError is NOT completed.
      resumeStepPath: [{ kind: 'top', childIdx: 0 }],
      initialHooksCompleted: ['beforeAll'],
    });
    expect(res.status).toBe('failure');
    expect(logs.find((l) => l.message === 'beforeAll-ran')).toBeUndefined();
    expect(logs.find((l) => l.message === 'onError-ran')).toBeDefined();
  });

  // Regression: step-level checkpoints must include hooksCompleted so that
  // background storage is not clobbered on SW eviction between hook and step.
  it('step checkpoints preserve hooksCompleted (no clobbering)', async () => {
    const r = hookRule({ beforeAll: hookMarker('beforeAll') });
    const checkpoints: CheckpointState[] = [];
    await runRule({
      rule: r,
      taskId: 't1',
      workerId: 'w1',
      env: createMockEnv(),
      onCheckpoint: (cp) => { checkpoints.push(cp); },
    });
    // Find the first checkpoint where beforeAll is marked completed.
    const beforeAllIdx = checkpoints.findIndex(
      (cp) => cp.hooksCompleted?.includes('beforeAll'),
    );
    expect(beforeAllIdx).toBeGreaterThanOrEqual(0);
    // Every subsequent (step-level) checkpoint must also carry hooksCompleted
    // containing 'beforeAll' — otherwise background storage would be clobbered.
    for (let i = beforeAllIdx + 1; i < checkpoints.length; i++) {
      expect(checkpoints[i].hooksCompleted).toContain('beforeAll');
    }
  });
});

// ---------------------------------------------------------------------------
// Phase 3: recursive checkpoints -- nested flow-control resume tests
// (docs/replay-fix-plan.md §8). These exercise the trust-model re-entry:
// on resume, the executor jumps to the recorded frame WITHOUT re-evaluating
// conditions, re-running prior siblings, or re-running prior loop iterations.
// ---------------------------------------------------------------------------

describe('executor - Phase 3 nested resume', () => {
  it('resumes into if.then without re-evaluating the condition', async () => {
    // On resume, condition selector points at an absent element so a fresh
    // evaluation would pick `else`. With the recorded branch = 'then', the
    // executor must take `then` directly. findElement for the condition
    // selector should NOT be called.
    let conditionEvals = 0;
    const env = createMockEnv();
    const realFindElement = env.findElement;
    env.findElement = (target: any, timeout?: number) => {
      const sel = typeof target === 'string' ? target : (target?.selector ?? '');
      if (sel === '#cond') { conditionEvals++; return Promise.resolve(null); }
      return realFindElement(target, timeout!);
    };
    document.body.innerHTML = '<button id="target">x</button>';

    const checkpoints: StepPath[] = [];
    const res = await runRule({
      rule: rule([
        {
          action: 'if',
          condition: { type: 'elementExists', target: { selector: '#cond' } },
          then: [
            { action: 'click', target: { selector: '#target' }, timeout: 50 },
          ],
          else: [],
        },
      ]),
      taskId: 't1', workerId: 'w1', env,
      resumeStepPath: [{ kind: 'top', childIdx: 0 }, { kind: 'if', branch: 'then', childIdx: 0 }],
      onCheckpoint: (cp) => { checkpoints.push(cp.lastCompletedStepPath); },
    });

    expect(res.status).toBe('success');
    expect(conditionEvals).toBe(0); // trust model: no re-eval
    // onStepStart checkpoint for the navigating click inside then.
    expect(checkpoints.some((p) => p.length === 2 && p[0].kind === 'top' && p[1].kind === 'if' && (p[1] as any).branch === 'then')).toBe(true);
  });

  it('resumes a fixedCount loop mid-iteration, skipping prior iterations', async () => {
    // Loop count=3. Each iteration records ctx.loopIndex. Resume target =
    // iter 1, childIdx 0. Prior iter (0) must NOT re-run; only iters 1 and 2
    // should execute. ctx.loopIndex on entry to iter 1 must be 1.
    document.body.innerHTML = '<div id="row">value</div>';
    (window as any).__its = undefined;
    const res = await runRule({
      rule: rule([
        {
          action: 'loop',
          type: 'fixedCount',
          count: 3,
          steps: [
            {
              action: 'evaluate',
              name: 'rec',
              script: 'window.__its = window.__its || []; window.__its.push(ctx.loopIndex); return ctx.loopIndex',
            },
          ],
        },
      ]),
      taskId: 't1', workerId: 'w1', env: createMockEnv(),
      resumeStepPath: [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 1, childIdx: 0 }],
      initialContext: { extracted: { items: ['iter0'] }, evaluated: {}, captured: {} },
    });

    expect(res.status).toBe('success');
    // Only iters 1 and 2 ran — iter 0 was skipped (trust model).
    expect((window as any).__its).toEqual([1, 2]);
  });

  it('resumes a forEach loop restoring loopItem per iteration', async () => {
    // items resolved from rule.variables.items = ['a','b','c']. Resume at
    // iter 2 childIdx 0. Prior iters (0,1) are skipped. iter 2 sees loopItem='c'.
    (window as any).__items = undefined;
    const r = rule([
      {
        action: 'loop',
        type: 'forEach',
        items: 'variables.items',
        as: 'item',
        steps: [
          {
            action: 'evaluate',
            name: 'rec',
            script: 'window.__items = window.__items || []; window.__items.push(ctx.loopItem); return ctx.loopItem',
          },
        ],
      },
    ]);
    (r as any).variables = { items: ['a', 'b', 'c'] };

    await runRule({
      rule: r,
      taskId: 't1', workerId: 'w1', env: createMockEnv(),
      resumeStepPath: [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 2, childIdx: 0 }],
    });

    expect((window as any).__items).toEqual(['c']);
  });

  it('resumes a switch into the recorded case without re-evaluating expression', async () => {
    // expression returns 'a' but recorded caseIdx = 1 (would normally match
    // case 'b'). On resume, the executor must take caseIdx 1 directly.
    document.body.innerHTML = '<button id="b1">x</button>';
    const checkpoints: StepPath[] = [];
    const res = await runRule({
      rule: rule([
        {
          action: 'switch',
          expression: 'a',
          cases: [
            { value: 'a', steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 10 }] },
            { value: 'b', steps: [{ action: 'click', target: { selector: '#b1' }, timeout: 50 }] },
          ],
        },
      ]),
      taskId: 't1', workerId: 'w1', env: createMockEnv(),
      resumeStepPath: [{ kind: 'top', childIdx: 0 }, { kind: 'switch', caseIdx: 1, childIdx: 0 }],
      onCheckpoint: (cp) => { checkpoints.push(cp.lastCompletedStepPath); },
    });

    expect(res.status).toBe('success');
    expect(checkpoints.some((p) => p.length === 2 && p[1].kind === 'switch' && (p[1] as any).caseIdx === 1)).toBe(true);
  });

  it('resumes a 3-level nested path (loop → if → loop)', async () => {
    // Outer loop fixedCount=2; each iter runs an if (then) that contains an
    // inner loop fixedCount=1 with a leaf evaluate. Resume target:
    // outer iter 1 → if.then childIdx 0 → inner loop iter 0 childIdx 0.
    // Trust model: outer iter 0 entirely skipped → leaf runs once, not twice.
    document.body.innerHTML = 'x';
    (window as any).__leafCount = 0;
    const r = rule([
      {
        action: 'loop',
        type: 'fixedCount',
        count: 2,
        steps: [
          {
            action: 'if',
            condition: { type: 'textContains', target: { selector: 'body' }, text: 'x' },
            then: [
              {
                action: 'loop',
                type: 'fixedCount',
                count: 1,
                steps: [
                  { action: 'evaluate', name: 'leaf', script: 'window.__leafCount++' },
                ],
              },
            ],
          },
        ],
      },
    ]);
    (window as any).__leafCount = 0;
    // Sanity: without resume, leaf runs 2× (outer count=2 × inner count=1).
    await runRule({ rule: r, taskId: 't1', workerId: 'w1', env: createMockEnv() });
    const baselineLeafCount = (window as any).__leafCount;
    expect(baselineLeafCount).toBe(2);

    // With resume targeting outer iter 1: iter 0 skipped → leaf runs once.
    (window as any).__leafCount = 0;
    await runRule({
      rule: r,
      taskId: 't1', workerId: 'w1', env: createMockEnv(),
      resumeStepPath: [
        { kind: 'top', childIdx: 0 },
        { kind: 'loop', iter: 1, childIdx: 0 },
        { kind: 'if', branch: 'then', childIdx: 0 },
        { kind: 'loop', iter: 0, childIdx: 0 },
      ],
    });
    expect((window as any).__leafCount).toBe(1);
  });

  it('onStepFailure rolls back within a loop iteration, not to top-level', async () => {
    // Loop count=1; iter 0 has [click#ok, click#missing]. Failure at childIdx 1
    // must roll back to the within-iteration path (iter 0, childIdx 0), not
    // to a top-level frame.
    document.body.innerHTML = '<button id="ok">x</button>';
    let rolledBackTo: StepPath | undefined;
    const res = await runRule({
      rule: rule([
        {
          action: 'loop',
          type: 'fixedCount',
          count: 1,
          steps: [
            { action: 'click', target: { selector: '#ok' }, timeout: 50 },
            { action: 'click', target: { selector: '#missing' }, timeout: 50 },
          ],
        },
      ]),
      taskId: 't1', workerId: 'w1', env: createMockEnv(),
      onStepFailure: (path) => { rolledBackTo = prevPendingPath(path); },
    });

    expect(res.status).toBe('failure');
    // Failure at iter0.childIdx1 → rollback target is iter0.childIdx0.
    expect(rolledBackTo).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 0, childIdx: 0 },
    ]);
  });

  it('rejects javascript: URLs in navigate action (H-1)', async () => {
    const res = await run([{ action: 'navigate', url: 'javascript:alert(document.cookie)' }]);
    expect(res.status).toBe('failure');
    expect(res.message).toMatch(/scheme|javascript/i);
  });

  it('rejects data: URLs in navigate action (H-1)', async () => {
    const res = await run([{ action: 'navigate', url: 'data:text/html,<script>alert(1)</script>' }]);
    expect(res.status).toBe('failure');
  });
});
