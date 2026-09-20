/// <reference types="vitest/globals" />
import { vi } from 'vitest';
import { runRule } from './executor';
import { classifyError, isRecoverable, calculateDelay } from './executor-utils';
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

async function runWithSelectors(
  steps: any[],
  selectors: NonNullable<Rule['selectors']>,
  env?: Environment,
) {
  return runRule({
    rule: { ...rule(steps), selectors },
    taskId: 't1',
    workerId: 'w1',
    env: env ?? createMockEnv(),
  });
}

describe('executor - advanced mouse', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="source" style="width:100px;height:100px;">S</div>
      <div id="target" style="width:100px;height:100px;">T</div>
      <div id="slide" style="width:100px;height:100px;">Slide</div>
    `;
  });

  it('right clicks element', async () => {
    const events: string[] = [];
    const el = document.getElementById('source')!;
    el.addEventListener('contextmenu', () => events.push('contextmenu'));
    const res = await run([{ action: 'rightClick', target: { selector: '#source' } }]);
    expect(res.status).toBe('success');
    expect(events).toContain('contextmenu');
  });

  it('middle clicks element', async () => {
    const events: MouseEvent[] = [];
    const el = document.getElementById('source')!;
    el.addEventListener('click', (e) => events.push(e));
    const res = await run([{ action: 'middleClick', target: { selector: '#source' } }]);
    expect(res.status).toBe('success');
    expect(events[events.length - 1]?.button).toBe(1);
  });

  it('moves mouse to coordinates', async () => {
    const moves: { x: number; y: number }[] = [];
    document.addEventListener('mousemove', (e) => moves.push({ x: e.clientX, y: e.clientY }));
    const res = await run([{ action: 'moveMouse', to: { x: 200, y: 150 }, duration: 50, path: 'linear' }]);
    expect(res.status).toBe('success');
    expect((window as any).__lastMouseX).toBe(200);
    expect((window as any).__lastMouseY).toBe(150);
    expect(moves.length).toBeGreaterThan(0);
  });

  it('drags and drops', async () => {
    const events: string[] = [];
    const source = document.getElementById('source')!;
    const target = document.getElementById('target')!;
    source.addEventListener('mousedown', () => events.push('source-mousedown'));
    target.addEventListener('mouseup', () => events.push('target-mouseup'));
    target.addEventListener('drop', () => events.push('target-drop'));
    const res = await run([{ action: 'dragAndDrop', source: { selector: '#source' }, target: { selector: '#target' }, duration: 50 }]);
    expect(res.status).toBe('success');
    expect(events).toContain('source-mousedown');
    expect(events).toContain('target-drop');
  });

  it('drags by delta', async () => {
    const events: string[] = [];
    const source = document.getElementById('source')!;
    source.addEventListener('mousedown', () => events.push('mousedown'));
    source.addEventListener('mouseup', () => events.push('mouseup'));
    const res = await run([{ action: 'dragBy', source: { selector: '#source' }, delta: { x: 50, y: 30 }, duration: 50 }]);
    expect(res.status).toBe('success');
    expect(events).toContain('mousedown');
    expect(events).toContain('mouseup');
  });

  it('slides element', async () => {
    const events: string[] = [];
    const el = document.getElementById('slide')!;
    el.addEventListener('mousedown', () => events.push('mousedown'));
    el.addEventListener('mousemove', () => events.push('mousemove'));
    el.addEventListener('mouseup', () => events.push('mouseup'));
    const res = await run([{ action: 'slide', target: { selector: '#slide' }, direction: 'right', distance: 80, duration: 50 }]);
    expect(res.status).toBe('success');
    expect(events).toContain('mousedown');
    expect(events).toContain('mouseup');
  });
});

describe('executor - scroll', () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="tall" style="height:2000px;"></div>';
    window.scrollTo(0, 0);
  });

  it('scrolls by direction and distance', async () => {
    let called = false;
    const original = window.scrollBy;
    window.scrollBy = (options?: ScrollToOptions | number) => {
      called = true;
      if (typeof options === 'object' && options?.top) {
        (window as any).scrollY = options.top;
      }
    };
    const res = await run([{ action: 'scrollBy', direction: 'down', distance: 300 }]);
    window.scrollBy = original;
    expect(res.status).toBe('success');
    expect(called).toBe(true);
    expect(window.scrollY).toBe(300);
  });

  it('converts page units using the matching viewport dimension', async () => {
    const original = window.scrollBy;
    const calls: ScrollToOptions[] = [];
    window.scrollBy = ((options?: ScrollToOptions | number) => {
      if (typeof options === 'object') calls.push(options);
    }) as typeof window.scrollBy;
    try {
      const vertical = await run([{ action: 'scrollBy', direction: 'up', distance: 2, unit: 'pages' }]);
      const horizontal = await run([{ action: 'scrollBy', direction: 'right', distance: 3, unit: 'pages' }]);
      expect(vertical.status).toBe('success');
      expect(horizontal.status).toBe('success');
      expect(calls).toEqual([
        { top: -2 * window.innerHeight, left: 0, behavior: 'smooth' },
        { top: 0, left: 3 * window.innerWidth, behavior: 'smooth' },
      ]);
    } finally {
      window.scrollBy = original;
    }
  });

  it('scrolls an indexed container instead of the window', async () => {
    document.body.innerHTML = '<div id="results"></div>';
    const container = document.getElementById('results') as HTMLElement;
    container.scrollBy = vi.fn();
    const windowScroll = vi.spyOn(window, 'scrollBy');
    try {
      const res = await run([{
        action: 'scrollBy',
        target: { selector: '#results' },
        direction: 'down',
        distance: 180,
      }]);
      expect(res.status).toBe('success');
      expect(container.scrollBy).toHaveBeenCalledWith({ top: 180, left: 0, behavior: 'smooth' });
      expect(windowScroll).not.toHaveBeenCalled();
    } finally {
      windowScroll.mockRestore();
    }
  });

  it('scrolls to element', async () => {
    let called = false;
    const el = document.getElementById('tall')!;
    el.scrollIntoView = () => { called = true; };
    const res = await run([{ action: 'scrollTo', target: { selector: '#tall' }, align: 'center' }]);
    expect(res.status).toBe('success');
    expect(called).toBe(true);
  });
});

describe('executor - wait', () => {
  it('waits for text to appear', async () => {
    document.body.innerHTML = '<div id="status">loading</div>';
    setTimeout(() => {
      document.getElementById('status')!.textContent = 'ready';
    }, 150);
    const res = await run([{ action: 'waitForText', target: { selector: '#status' }, text: 'ready', timeout: 1000 }]);
    expect(res.status).toBe('success');
  });

  it('waits for element removed from DOM', async () => {
    document.body.innerHTML = '<div id="popup">x</div>';
    setTimeout(() => {
      document.getElementById('popup')!.remove();
    }, 100);
    const res = await run([{ action: 'waitForElementHidden', target: { selector: '#popup' }, timeout: 1000 }]);
    expect(res.status).toBe('success');
  });

  it('waits for URL to match', async () => {
    const env = createMockEnv();
    const urls = ['https://example.com/loading', 'https://example.com/done'];
    let idx = 0;
    env.getUrl = () => urls[idx++];
    env.sleep = async () => {};
    const res = await run([{ action: 'waitForUrl', pattern: '/done', matchType: 'contains', timeout: 1000 }], env);
    expect(res.status).toBe('success');
  });

  it('times out waiting for URL', async () => {
    const env = createMockEnv();
    env.sleep = async () => {};
    const res = await run([{ action: 'waitForUrl', pattern: '/never', matchType: 'contains', timeout: 100 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('TimeoutError');
  });

  it('waits for element visible', async () => {
    document.body.innerHTML = '<div id="popup" style="display:none">x</div>';
    setTimeout(() => {
      document.getElementById('popup')!.style.display = 'block';
      document.getElementById('popup')!.getBoundingClientRect = () => ({ width: 10, height: 10 } as DOMRect);
    }, 50);
    const res = await run([{ action: 'waitForElementVisible', target: { selector: '#popup' }, timeout: 1000 }]);
    expect(res.status).toBe('success');
  });

  it('times out waiting for element visible', async () => {
    document.body.innerHTML = '<div id="popup" style="display:none">x</div>';
    const env = createMockEnv();
    env.sleep = async () => {};
    const res = await run([{ action: 'waitForElementVisible', target: { selector: '#popup' }, timeout: 100 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('TimeoutError');
  });

  it('waits for function to return true', async () => {
    const env = createMockEnv();
    let calls = 0;
    env.evaluate = async () => ++calls >= 2;
    const res = await run([{ action: 'waitForFunction', script: 'return false', pollingInterval: 10, timeout: 200 }], env);
    expect(res.status).toBe('success');
  });

  it('times out waiting for function', async () => {
    const env = createMockEnv();
    env.evaluate = async () => false;
    env.sleep = async () => {};
    const res = await run([{ action: 'waitForFunction', script: 'return false', pollingInterval: 10, timeout: 100 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('TimeoutError');
  });

  it('pauses for reading', async () => {
    const env = createMockEnv();
    let slept = 0;
    env.sleep = async (ms) => { slept += ms; };
    const res = await run([{ action: 'readPause', ms: 50 }], env);
    expect(res.status).toBe('success');
    expect(slept).toBe(50);
  });

  it('pauses for reading with [min, max] range', async () => {
    const env = createMockEnv();
    let slept = 0;
    env.sleep = async (ms) => { slept += ms; };
    const res = await run([{ action: 'readPause', ms: [80, 120] }], env);
    expect(res.status).toBe('success');
    expect(slept).toBeGreaterThanOrEqual(80);
    expect(slept).toBeLessThanOrEqual(120);
  });
});

describe('executor - data operations', () => {
  it('transforms data with number and map ops', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'arr',
        script: 'return [{ a: 1 }, { a: 2 }]',
      },
      {
        action: 'transform',
        name: 'mapped',
        from: 'evaluated.arr',
        operations: [{ type: 'map', params: { rename: { a: 'value' } } }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).mapped[0].value).toBe(1);
  });

  it('returns data unchanged for unknown transform op', async () => {
    const steps = [
      { action: 'evaluate', name: 'arr', script: 'return [1, 2, 3]' },
      { action: 'transform', name: 'same', from: 'evaluated.arr', operations: [{ type: 'unknown' as any }] },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).same).toEqual([1, 2, 3]);
  });

  it('transforms a single number', async () => {
    const steps = [
      { action: 'evaluate', name: 'num', script: 'return "42"' },
      { action: 'transform', name: 'n', from: 'evaluated.num', operations: [{ type: 'number' }] },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).n).toBe(42);
  });

  it('filters with all supported ops', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'list',
        script: 'return [{ v: 1, t: "a" }, { v: 2, t: "b" }, { v: 3, t: "c" }, { v: 0, t: "" }]',
      },
      { action: 'filter', name: 'eq', from: 'evaluated.list', criteria: { field: 'v', op: 'eq', value: 2 } },
      { action: 'filter', name: 'ne', from: 'evaluated.list', criteria: { field: 'v', op: 'ne', value: 2 } },
      { action: 'filter', name: 'contains', from: 'evaluated.list', criteria: { field: 't', op: 'contains', value: 'a' } },
      { action: 'filter', name: 'lt', from: 'evaluated.list', criteria: { field: 'v', op: 'lt', value: 2 } },
      { action: 'filter', name: 'lte', from: 'extracted.lt', criteria: { field: 'v', op: 'lte', value: 1 } },
      { action: 'filter', name: 'notEmpty', from: 'evaluated.list', criteria: { field: 'v', op: 'notEmpty' } },
      { action: 'filter', name: 'defaultTrue', from: 'evaluated.list', criteria: { field: 'v', op: 'unknown' as any } },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).eq).toHaveLength(1);
    expect((res.partialData as any).ne).toHaveLength(3);
    expect((res.partialData as any).contains).toHaveLength(1);
    expect((res.partialData as any).lt).toHaveLength(2);
    expect((res.partialData as any).lte).toHaveLength(2);
    expect((res.partialData as any).notEmpty).toHaveLength(4);
    expect((res.partialData as any).defaultTrue).toHaveLength(4);
  });

  it('filters and deduplicates array', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'list',
        script: 'return [{ id: 1, v: 20 }, { id: 1, v: 30 }, { id: 2, v: 10 }]',
      },
      {
        action: 'filter',
        name: 'big',
        from: 'evaluated.list',
        criteria: { field: 'v', op: 'gte', value: 20 },
      },
      {
        action: 'deduplicate',
        name: 'unique',
        from: 'extracted.big',
        keys: ['id'],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).unique).toHaveLength(1);
  });

  it('validates data and logs on invalid (placeholder always true)', async () => {
    const steps = [
      { action: 'sendResult', payload: { data: { ok: true } } },
      { action: 'validateData', from: 'extracted.data', schema: {}, onInvalid: 'fail' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
  });
});

describe('executor - production ops', () => {
  it('opens circuit breaker and runs onOpen steps', async () => {
    const env = createMockEnv();
    (window as any).__ocCircuitBreakers = {
      cb1: { failures: [Date.now()], open: true, openedAt: Date.now() },
    };
    const steps = [
      {
        action: 'circuitBreaker',
        name: 'cb1',
        failureThreshold: 3,
        windowMs: 60000,
        cooldownMs: 60000,
        onOpen: [{ action: 'evaluate', script: 'window.__cbOpened = true; return true' }],
      },
    ];
    const res = await run(steps, env);
    expect(res.status).toBe('failure');
    expect((window as any).__cbOpened).toBe(true);
  });

  it('propagates onOpen step failure through circuit breaker', async () => {
    const env = createMockEnv();
    (window as any).__ocCircuitBreakers = {
      cb1: { failures: [Date.now()], open: true, openedAt: Date.now() },
    };
    const steps = [
      {
        action: 'circuitBreaker',
        name: 'cb1',
        failureThreshold: 3,
        windowMs: 60000,
        cooldownMs: 60000,
        onOpen: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      },
    ];
    const res = await run(steps, env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('recovers from checkpoint and runs steps', async () => {
    const env = createMockEnv();
    const steps = [
      { action: 'checkpoint', name: 'cp1' },
      {
        action: 'recover',
        checkpointName: 'cp1',
        steps: [{ action: 'evaluate', script: 'window.__recovered = true; return true' }],
      },
    ];
    const res = await run(steps, env);
    expect(res.status).toBe('success');
    expect((window as any).__recovered).toBe(true);
  });

  it('saves per-step checkpoint', async () => {
    const env = createMockEnv();
    const res = await run([
      { action: 'evaluate', script: 'return 1', checkpoint: true, id: 'cp-step' },
    ], env);
    expect(res.status).toBe('success');
  });

  it('requestHuman times out with short timeout', async () => {
    const env = createMockEnv();
    env.transport.requestHuman = vi.fn(async () => { throw new Error('HumanTimeout: generic'); });
    const res = await run([{ action: 'requestHuman', type: 'generic', prompt: 'help', timeout: 50 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('HumanTimeout');
  });

  it('solveCaptcha falls back to human', async () => {
    const env = createMockEnv();
    const res = await run([{ action: 'solveCaptcha', fallbackToHuman: true, timeout: 50 }], env);
    expect(res.status).toBe('failure');
  });

  it('solveCaptcha throws when no provider and no fallback', async () => {
    const env = createMockEnv();
    const res = await run([{ action: 'solveCaptcha', fallbackToHuman: false }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('CaptchaError');
  });

  it('skips action when quota exceeded with onExceeded=skip', async () => {
    const key = 'oc_quota_requestsPerMinute_2';
    localStorage.setItem(key, JSON.stringify({ count: 5, resetAt: Date.now() + 60000 }));
    const res = await run([
      { action: 'checkQuota', type: 'requestsPerMinute', limit: 2, onExceeded: 'skip' },
      { action: 'evaluate', script: 'window.__quotaSkipped = true; return true' },
    ]);
    localStorage.removeItem(key);
    expect(res.status).toBe('success');
    expect((window as any).__quotaSkipped).toBe(true);
  });

  it('sends critical failure flag for critical steps', async () => {
    const results: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p, immediate) => { results.push({ p, immediate }); };
    const res = await run(
      [{ action: 'click', target: { selector: '#missing' }, timeout: 50, critical: true, id: 'critical-step' }],
      env,
    );
    expect(res.status).toBe('failure');
    expect(results.some((r) => r.p.__criticalFailure)).toBe(true);
  });
});
describe('executor - keyboard & input extras', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <input id="name" value="" />
      <div id="suggestions" style="display:none"></div>
    `;
  });

  it('pastes value into input', async () => {
    const res = await run([{ action: 'paste', target: { selector: '#name' }, value: 'pasted' }]);
    expect(res.status).toBe('success');
    expect((document.getElementById('name') as HTMLInputElement).value).toBe('pasted');
  });

  it('presses keys', async () => {
    const keys: string[] = [];
    document.addEventListener('keydown', (e) => keys.push(e.key));
    const res = await run([{ action: 'pressKey', keys: ['a', 'b', 'c'] }]);
    expect(res.status).toBe('success');
    expect(keys).toEqual(['a', 'b', 'c']);
  });

  it('presses key combination', async () => {
    const keys: string[] = [];
    document.addEventListener('keydown', (e) => keys.push(e.key));
    const res = await run([{ action: 'keyCombination', keys: ['Control', 'c'] }]);
    expect(res.status).toBe('success');
    expect(keys).toContain('Control');
    expect(keys).toContain('c');
  });

  it('tabs forward', async () => {
    const keys: string[] = [];
    document.addEventListener('keydown', (e) => keys.push(e.key));
    const res = await run([{ action: 'tabToNext', count: 2 }]);
    expect(res.status).toBe('success');
    const tabCount = keys.filter((k) => k === 'Tab').length;
    expect(tabCount).toBe(2);
  });

  it('tabs backward', async () => {
    const keys: { key: string; shift: boolean }[] = [];
    document.addEventListener('keydown', (e) => keys.push({ key: e.key, shift: e.shiftKey }));
    const res = await run([{ action: 'tabToPrevious' }]);
    expect(res.status).toBe('success');
    expect(keys.some((k) => k.key === 'Tab' && k.shift)).toBe(true);
  });
});

describe('executor - extraction helpers', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <article class="card" data-id="42">
        <h2 class="title">Product A</h2>
        <a href="/item/42">link</a>
      </article>
    `;
  });

  it('extracts text directly', async () => {
    const res = await run([{ action: 'extractText', name: 'title', target: { selector: '.title' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).title).toBe('Product A');
  });

  it('extracts attribute', async () => {
    const res = await run([{ action: 'extractAttribute', name: 'href', target: { selector: 'a' }, attr: 'href' }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).href).toBe('/item/42');
  });
});

describe('executor - output & state helpers', () => {
  it('flushes results', async () => {
    const payloads: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p, immediate) => { payloads.push({ p, immediate }); };
    const flush = vi.fn(async () => {});
    env.transport.flushResults = flush;
    const res = await run([{ action: 'flushResults' }], env);
    expect(res.status).toBe('success');
    expect(flush).toHaveBeenCalledOnce();
    expect(payloads.some((x) => x.p?.__flush)).toBe(false);
  });

  it('sends heartbeat', async () => {
    let received: any;
    const env = createMockEnv();
    env.transport.sendHeartbeat = async (p) => { received = p; return { cancelRequested: false }; };
    const res = await run([{ action: 'heartbeat', payload: { ok: true } }], env);
    expect(res.status).toBe('success');
    expect(received).toEqual({ ok: true });
  });

  it('sets tag and logs metric', async () => {
    const logs: any[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message, extra) => { logs.push({ level, message, extra }); };
    const res = await run(
      [
        { action: 'setTag', tags: { source: 'test' } },
        { action: 'logMetric', name: 'latency', value: 120, unit: 'ms' },
      ],
      env,
    );
    expect(res.status).toBe('success');
    expect(logs.some((l) => l.message === 'setTag')).toBe(true);
    expect(logs.some((l) => l.message === 'metric' && l.extra.name === 'latency')).toBe(true);
  });
});

describe('executor - navigation', () => {
  let originalHref: string;

  beforeEach(() => {
    originalHref = location.href;
  });

  afterEach(() => {
    location.href = originalHref;
  });

  it('navigates when the environment observes the committed URL', async () => {
    const env = createMockEnv();
    let currentUrl = 'https://example.com/';
    env.getUrl = () => currentUrl;
    env.sleep = async () => { currentUrl = 'https://example.com/next'; };
    const res = await run([{ action: 'navigate', url: '/next', waitUntil: 'domcontentloaded' }], env);
    expect(res.status).toBe('success');
  });
});

describe('executor - abort', () => {
  it('aborts execution with flush', async () => {
    const payloads: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p, immediate) => { payloads.push({ p, immediate }); };
    const flush = vi.fn(async () => {});
    env.transport.flushResults = flush;
    const res = await run([{ action: 'abort', reason: 'manual', flushBeforeAbort: true }], env);
    expect(res.status).toBe('failure');
    expect(flush).toHaveBeenCalledOnce();
    expect(payloads.some((x) => x.p?.__flush)).toBe(false);
  });
});

function ruleWithHooks(steps: any[], hooks: any): Rule {
  return { ...rule(steps), hooks };
}

async function runWithHooks(steps: any[], hooks: any, env?: Environment) {
  return runRule({ rule: ruleWithHooks(steps, hooks), taskId: 't1', workerId: 'w1', env: env ?? createMockEnv() });
}

describe('executor - page & element ops', () => {
  it('scrolls to top', async () => {
    const env = createMockEnv();
    let scrolled = false;
    window.scrollTo = () => { scrolled = true; };
    const res = await run([{ action: 'scrollToTop' }], env);
    expect(res.status).toBe('success');
    expect(scrolled).toBe(true);
  });

  it('pages down', async () => {
    const env = createMockEnv();
    let by = 0;
    window.scrollBy = (options?: ScrollToOptions | number) => {
      if (typeof options === 'object') by = options.top ?? 0;
    };
    const res = await run([{ action: 'pageDown', count: 2 }], env);
    expect(res.status).toBe('success');
    expect(by).toBeGreaterThan(0);
  });

  it('pages up', async () => {
    const env = createMockEnv();
    let by = 0;
    window.scrollBy = (options?: ScrollToOptions | number) => {
      if (typeof options === 'object') by = options.top ?? 0;
    };
    const res = await run([{ action: 'pageUp', count: 1 }], env);
    expect(res.status).toBe('success');
    expect(by).toBeLessThan(0);
  });

  it('sets element style', async () => {
    document.body.innerHTML = '<div id="box">x</div>';
    const res = await run([{ action: 'setStyle', target: { selector: '#box' }, style: { color: 'red' } }]);
    expect(res.status).toBe('success');
    expect((document.getElementById('box') as HTMLElement).style.color).toBe('red');
  });

  it('fails setStyle when element missing', async () => {
    const res = await run([{ action: 'setStyle', target: { selector: '#missing' }, style: { color: 'red' }, timeout: 50 }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('removes element', async () => {
    document.body.innerHTML = '<div id="box">x</div>';
    const res = await run([{ action: 'removeElement', target: { selector: '#box' } }]);
    expect(res.status).toBe('success');
    expect(document.getElementById('box')).toBeNull();
  });

  it('fails removeElement when element missing', async () => {
    const res = await run([{ action: 'removeElement', target: { selector: '#missing' }, timeout: 50 }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('scrolls element into view', async () => {
    document.body.innerHTML = '<div id="box">x</div>';
    let called = false;
    document.getElementById('box')!.scrollIntoView = () => { called = true; };
    const env = createMockEnv();
    env.sleep = async () => {};
    const res = await run([{ action: 'scrollIntoView', target: { selector: '#box' }, align: 'start' }], env);
    expect(res.status).toBe('success');
    expect(called).toBe(true);
  });

  it('fails scrollIntoView when element missing', async () => {
    const res = await run([{ action: 'scrollIntoView', target: { selector: '#missing' }, timeout: 50 }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });
});

describe('executor - hooks & retry', () => {
  it('runs beforeAll hook and fails fast when it errors', async () => {
    const statuses: string[] = [];
    const env = createMockEnv();
    env.transport.sendStatus = async (s) => { statuses.push(s); };
    const hooks = {
      beforeAll: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
    };
    const res = await runWithHooks([{ action: 'evaluate', script: 'return 1' }], hooks, env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
    expect(statuses).toContain('failed');
  });

  it('runs afterAll and onError hooks on failure', async () => {
    const logs: string[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (_level, message) => { logs.push(message); };
    const hooks = {
      afterAll: [{ action: 'evaluate', script: 'window.__afterAll = true; return true' }],
      onError: [{ action: 'evaluate', script: 'window.__onError = true; return true' }],
    };
    const res = await runWithHooks(
      [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      hooks,
      env,
    );
    expect(res.status).toBe('failure');
    expect((window as any).__afterAll).toBe(true);
    expect((window as any).__onError).toBe(true);
  });

  it('logs warning when afterAll hook fails', async () => {
    const logs: { level: string; message: string }[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message) => { logs.push({ level, message }); };
    const hooks = {
      afterAll: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
    };
    const res = await runWithHooks([{ action: 'evaluate', script: 'return 1' }], hooks, env);
    expect(res.status).toBe('success');
    expect(logs.some((l) => l.message.includes('afterAll hook failed'))).toBe(true);
  });

  it('logs warning when onError hook fails', async () => {
    const logs: { level: string; message: string }[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message) => { logs.push({ level, message }); };
    const hooks = {
      onError: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
    };
    const res = await runWithHooks([{ action: 'click', target: { selector: '#missing' }, timeout: 50 }], hooks, env);
    expect(res.status).toBe('failure');
    expect(logs.some((l) => l.message.includes('onError hook failed'))).toBe(true);
  });

  it('uses linear retry backoff', async () => {
    const start = Date.now();
    const res = await run([
      { action: 'click', target: { selector: '#missing' }, timeout: 50, retry: { maxAttempts: 2, delay: 30, backoff: 'linear' } },
    ]);
    const elapsed = Date.now() - start;
    expect(res.status).toBe('failure');
    // attempts: 0 (no delay) + 30 + 60 >= 90
    expect(elapsed).toBeGreaterThanOrEqual(80);
  });

  it('uses exponential retry backoff', async () => {
    const start = Date.now();
    const res = await run([
      { action: 'click', target: { selector: '#missing' }, timeout: 50, retry: { maxAttempts: 2, delay: 20, backoff: 'exponential' } },
    ]);
    const elapsed = Date.now() - start;
    expect(res.status).toBe('failure');
    // attempts: 0 + 20 + 40 >= 60
    expect(elapsed).toBeGreaterThanOrEqual(50);
  });
});

describe('executor - hover click & snapshots', () => {
  it('hovers then clicks menu item', async () => {
    document.body.innerHTML = `
      <div id="menu">Menu</div>
      <div id="item" style="display:none">Item</div>
    `;
    const item = document.getElementById('item')!;
    item.style.display = 'block';
    const clicks: string[] = [];
    item.addEventListener('click', () => clicks.push('item'));
    const res = await run([
      { action: 'hoverClick', hoverTarget: { selector: '#menu' }, clickTarget: { selector: '#item' }, menuAppearTimeout: 200 },
    ]);
    expect(res.status).toBe('success');
    expect(clicks).toContain('item');
  });

  it('sends screenshot when available', async () => {
    const snapshots: any[] = [];
    const env = createMockEnv();
    env.screenshot = async (name) => ({ name, type: 'screenshot', data: 'data:image/png;base64,abc' });
    env.transport.sendSnapshot = async (s) => { snapshots.push(s); };
    const res = await run([{ action: 'sendScreenshot', name: 'homepage' }], env);
    expect(res.status).toBe('success');
    expect(snapshots[0].name).toBe('homepage');
  });

  it('saves snapshot html', async () => {
    const snapshots: any[] = [];
    const env = createMockEnv();
    env.saveSnapshot = async (name, type) => ({ name, type: type ?? 'html', data: '<html></html>' });
    env.transport.sendSnapshot = async (s) => { snapshots.push(s); };
    const res = await run([{ action: 'saveSnapshot', name: 'page', type: 'html' }], env);
    expect(res.status).toBe('success');
    expect(snapshots[0].type).toBe('html');
  });
});

describe('executor - conditions', () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="flag">hello world</div>';
  });

  it('evaluates elementNotExists condition', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'elementNotExists', target: { selector: '#missing' }, timeout: 100 },
        then: [{ action: 'evaluate', script: 'window.__notExists = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__notExists).toBe(true);
  });

  it('resolves a selector alias before an elementNotExists pagination guard', async () => {
    document.body.innerHTML = '<a id="next">next</a>';
    const steps = [{
      action: 'loop',
      type: 'fixedCount',
      count: 1,
      steps: [{
        action: 'if',
        condition: { type: 'elementNotExists', target: { $ref: 'next' }, timeout: 100 },
        then: [{ action: 'break' }],
        else: [{ action: 'evaluate', script: 'window.__paginationContinued = true; return true' }],
      }],
    }];

    const res = await runWithSelectors(steps, { next: { selector: '#next' } });

    expect(res.status).toBe('success');
    expect((window as any).__paginationContinued).toBe(true);
  });

  it.each([
    {
      type: 'elementExists',
      condition: {},
      element: { textContent: 'hello world' },
      expectedVisible: undefined,
    },
    {
      type: 'elementNotExists',
      condition: {},
      element: null,
      expectedVisible: undefined,
    },
    {
      type: 'textContains',
      condition: { text: 'world' },
      element: { textContent: 'hello world' },
      expectedVisible: undefined,
    },
    {
      type: 'elementVisible',
      condition: {},
      element: { textContent: 'hello world' },
      expectedVisible: true,
    },
    {
      type: 'elementHidden',
      condition: {},
      element: null,
      expectedVisible: true,
    },
    {
      type: 'textEquals',
      condition: { text: 'hello world' },
      element: { textContent: ' hello world ' },
      expectedVisible: undefined,
    },
    {
      type: 'textMatches',
      condition: { pattern: '^hello world$' },
      element: { textContent: 'hello world' },
      expectedVisible: undefined,
    },
    {
      type: 'valueEquals',
      condition: { value: 'expected' },
      element: { value: 'expected' },
      expectedVisible: undefined,
    },
  ])('resolves selector aliases for $type condition targets', async ({
    type,
    condition,
    element,
    expectedVisible,
  }) => {
    const env = createMockEnv();
    const findElementSpy = vi.fn(async () => element as Element | null);
    env.findElement = findElementSpy;
    const res = await runWithSelectors([{
      action: 'if',
      condition: {
        type,
        target: { $ref: 'flag', index: 2 },
        timeout: 100,
        ...condition,
      },
      then: [{ action: 'evaluate', script: 'window.__aliasConditionMatched = true; return true' }],
    }], {
      flag: { selector: '#flag', visible: false },
    }, env);

    expect(res.status).toBe('success');
    expect((window as any).__aliasConditionMatched).toBe(true);
    expect(findElementSpy).toHaveBeenCalledOnce();
    expect(findElementSpy).toHaveBeenCalledWith({
      selector: '#flag',
      visible: expectedVisible ?? false,
      index: 2,
      $ref: undefined,
    }, 100);
  });

  it('fails closed when a condition target references an unknown selector alias', async () => {
    const env = createMockEnv();
    env.findElement = vi.fn();

    const res = await runWithSelectors([{
      action: 'if',
      condition: { type: 'elementNotExists', target: { $ref: 'missing' }, timeout: 100 },
      then: [{ action: 'evaluate', script: 'window.__unknownAliasTreatedAsAbsent = true; return true' }],
    }], {}, env);

    expect(res.status).toBe('failure');
    expect(res.error?.message).toContain('Selector alias not found: missing');
    expect(env.findElement).not.toHaveBeenCalled();
    expect((window as any).__unknownAliasTreatedAsAbsent).toBeUndefined();
  });

  it('preserves inline condition targets', async () => {
    const env = createMockEnv();
    const findElementSpy = vi.fn(async () => ({ textContent: 'inline' }) as unknown as Element);
    env.findElement = findElementSpy;

    const res = await run([{
      action: 'if',
      condition: { type: 'textEquals', target: { selector: '#flag' }, text: 'inline', timeout: 100 },
      then: [{ action: 'evaluate', script: 'window.__inlineConditionMatched = true; return true' }],
    }], env);

    expect(res.status).toBe('success');
    expect((window as any).__inlineConditionMatched).toBe(true);
    expect(findElementSpy).toHaveBeenCalledWith({ selector: '#flag' }, 100);
  });

  it('evaluates textContains condition', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'textContains', target: { selector: '#flag' }, text: 'world' },
        then: [{ action: 'evaluate', script: 'window.__textOk = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__textOk).toBe(true);
  });

  it('evaluates urlContains condition', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'urlContains', pattern: 'example.com' },
        then: [{ action: 'evaluate', script: 'window.__urlOk = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__urlOk).toBe(true);
  });

  it('evaluates jsTruthy condition', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'jsTruthy', script: '1 + 1 === 2' },
        then: [{ action: 'evaluate', script: 'window.__jsOk = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__jsOk).toBe(true);
  });

  it('evaluates urlMatches condition', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'urlMatches', pattern: 'example\\.com' },
        then: [{ action: 'evaluate', script: 'window.__urlMatch = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__urlMatch).toBe(true);
  });

  it('throws ConditionError when condition type is unknown', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: '__nonexistentCondition' as any, target: { selector: '#flag' } },
        then: [{ action: 'evaluate', script: 'window.__unknown = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.message).toMatch(/^ConditionError:/);
    expect(res.error?.type).toBe('ConditionError');
    expect((window as any).__unknown).toBeUndefined();
  });

  it('treats throwing jsTruthy script as false', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'jsTruthy', script: 'throw new Error("bad")' },
        then: [{ action: 'evaluate', script: 'window.__jsThrow = true; return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__jsThrow).toBeUndefined();
  });
});

describe('executor - loops', () => {
  it('loops over array with forEach', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'items',
        script: 'return ["a", "b", "c"]',
      },
      {
        action: 'loop',
        type: 'forEach',
        items: 'evaluated.items',
        as: 'item',
        steps: [{ action: 'evaluate', script: 'window.__lastItem = ctx.loopItem; return ctx.loopItem' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__lastItem).toBe('c');
  });
});

describe('executor - mouse bezier path', () => {
  it('moves mouse along bezier path', async () => {
    const moves: { x: number; y: number }[] = [];
    document.addEventListener('mousemove', (e) => moves.push({ x: e.clientX, y: e.clientY }));
    const res = await run([{ action: 'moveMouse', to: { x: 200, y: 150 }, duration: 50, path: 'bezier' }]);
    expect(res.status).toBe('success');
    expect((window as any).__lastMouseX).toBe(200);
    expect((window as any).__lastMouseY).toBe(150);
    expect(moves.length).toBeGreaterThan(0);
  });
});

describe('executor - input advanced', () => {
  it('types and selects suggestion', async () => {
    document.body.innerHTML = `
      <input id="autocomplete" />
      <div class="suggestion">Apple</div>
      <div class="suggestion">Banana</div>
    `;
    const clicks: string[] = [];
    document.querySelectorAll('.suggestion').forEach((el) => {
      el.addEventListener('click', () => clicks.push(el.textContent ?? ''));
    });
    const res = await run([
      {
        action: 'typeAndSelect',
        target: { selector: '#autocomplete' },
        value: 'Ban',
        suggestionSelector: '.suggestion',
        matchBy: 'startsWith',
        waitForSuggestions: 200,
      },
    ]);
    expect(res.status).toBe('success');
    expect(clicks).toContain('Banana');
  });
});

describe('executor - reload & quota', () => {
  it('reloads page', async () => {
    const reloadFn = vi.fn();
    const originalLocation = window.location;
    vi.stubGlobal('location', { ...originalLocation, reload: reloadFn });
    const env = createMockEnv();
    env.sleep = async () => {};
    const res = await run([{ action: 'reload' }], env);
    vi.unstubAllGlobals();
    expect(res.status).toBe('success');
    expect(reloadFn).toHaveBeenCalled();
  });

  it('waits when quota exceeded with onExceeded=wait', async () => {
    const key = 'oc_quota_custom_2';
    const now = Date.now();
    localStorage.setItem(key, JSON.stringify({ count: 3, resetAt: now + 50 }));
    const env = createMockEnv();
    env.sleep = async () => {};
    const start = Date.now();
    const res = await run([{ action: 'checkQuota', type: 'custom', limit: 2, onExceeded: 'wait', cooldownMs: 100 }], env);
    localStorage.removeItem(key);
    expect(res.status).toBe('success');
    expect(Date.now() - start).toBeLessThan(200);
  });

  it('resets quota counter after window expires', async () => {
    const key = 'oc_quota_custom_2';
    const now = Date.now();
    localStorage.setItem(key, JSON.stringify({ count: 10, resetAt: now - 10 }));
    const res = await run([{ action: 'checkQuota', type: 'custom', limit: 2, cooldownMs: 100 }]);
    const stored = JSON.parse(localStorage.getItem(key)!);
    localStorage.removeItem(key);
    expect(res.status).toBe('success');
    expect(stored.count).toBe(1);
  });
});

describe('executor - onError & cleanup branches', () => {
  it('resumes an onError=requestHuman step only after approval', async () => {
    const env = createMockEnv();
    const requestHuman = vi.fn(async () => ({
      id: 'human-on-error', checkpointId: 'checkpoint-on-error', status: 'approved' as const,
      expiresAt: new Date(Date.now() + 1000).toISOString(),
    }));
    env.transport.requestHuman = requestHuman;
    const res = await run(
      [{ action: 'click', target: { selector: '#missing' }, timeout: 50, onError: 'requestHuman', id: 's1' }],
      env,
    );
    expect(res.status).toBe('success');
    expect(requestHuman).toHaveBeenCalledWith(expect.objectContaining({
      type: 'generic', checkpoint: { stepId: 's1', url: 'https://example.com/' },
    }));
    delete env.transport.requestHuman;
  });

  it('logs warning when cleanup hook fails', async () => {
    const logs: { level: string; message: string }[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message) => { logs.push({ level, message }); };
    const hooks = {
      cleanup: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
    };
    const res = await runWithHooks([{ action: 'evaluate', script: 'return 1' }], hooks, env);
    expect(res.status).toBe('success');
    expect(logs.some((l) => l.message.includes('cleanup hook failed'))).toBe(true);
  });

  // Regression: ctx.page must be re-synced before onError / cleanup hooks so
  // {{page.url}} interpolation reflects any navigation performed during main
  // steps. Without the sync, hooks observe the stale URL captured at runRule
  // entry.
  it('syncs ctx.page before onError hook after main-step navigation', async () => {
    const payloads: any[] = [];
    const env = createMockEnv();
    (window as any).__testUrl = 'https://example.com/start';
    env.getUrl = () => (window as any).__testUrl;
    env.transport.sendResult = async (p) => { payloads.push(p); };
    // First main step simulates a navigation (mutates the URL the env reads),
    // second step fails so the onError hook fires.
    const res = await runRule({
      rule: ruleWithHooks(
        [
          { action: 'evaluate', script: 'window.__testUrl = "https://example.com/after-nav"; return true' },
          { action: 'click', target: { selector: '#missing' }, timeout: 50 },
        ],
        {
          onError: [
            { action: 'sendResult', payload: { observed: '{{page.url}}' } },
          ],
        },
      ),
      taskId: 't1',
      workerId: 'w1',
      env,
    });
    expect(res.status).toBe('failure');
    const observed = payloads.find((p) => p.observed)?.observed;
    expect(observed).toBe('https://example.com/after-nav');
    delete (window as any).__testUrl;
  });

  it('syncs ctx.page before cleanup hook after main-step navigation', async () => {
    const payloads: any[] = [];
    const env = createMockEnv();
    (window as any).__testUrl = 'https://example.com/start';
    env.getUrl = () => (window as any).__testUrl;
    env.transport.sendResult = async (p) => { payloads.push(p); };
    const res = await runRule({
      rule: ruleWithHooks(
        [
          { action: 'evaluate', script: 'window.__testUrl = "https://example.com/cleanup-nav"; return true' },
        ],
        {
          cleanup: [
            { action: 'sendResult', payload: { observed: '{{page.url}}' } },
          ],
        },
      ),
      taskId: 't1',
      workerId: 'w1',
      env,
    });
    expect(res.status).toBe('success');
    const observed = payloads.find((p) => p.observed)?.observed;
    expect(observed).toBe('https://example.com/cleanup-nav');
    delete (window as any).__testUrl;
  });
});


describe('executor - flow control extras', () => {
  it('executes matching switch case', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'choice',
        script: 'return "b"',
      },
      {
        action: 'switch',
        expression: 'evaluated.choice',
        cases: [
          { value: 'a', steps: [{ action: 'evaluate', script: 'window.__switch = 1; return 1' }] },
          { value: 'b', steps: [{ action: 'evaluate', script: 'window.__switch = 2; return 2' }] },
        ],
        default: [{ action: 'evaluate', script: 'window.__switch = 0; return 0' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__switch).toBe(2);
  });

  it('falls back to switch default when no case matches', async () => {
    const steps = [
      {
        action: 'switch',
        expression: 'evaluated.unknown',
        cases: [
          { value: 'a', steps: [{ action: 'evaluate', script: 'window.__switchDefault = 1; return 1' }] },
        ],
        default: [{ action: 'evaluate', script: 'window.__switchDefault = 0; return 0' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__switchDefault).toBe(0);
  });

  it('supports interpolated switch expression', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'choice',
        script: 'return "c"',
      },
      {
        action: 'switch',
        expression: '{{evaluated.choice}}',
        cases: [
          { value: 'c', steps: [{ action: 'evaluate', script: 'window.__switchInterp = 3; return 3' }] },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__switchInterp).toBe(3);
  });

  it('succeeds when no switch case matches and there is no default', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'choice',
        script: 'return "z"',
      },
      {
        action: 'switch',
        expression: 'evaluated.choice',
        cases: [
          { value: 'a', steps: [{ action: 'evaluate', script: 'window.__switchNoMatch = 1; return 1' }] },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__switchNoMatch).toBeUndefined();
  });

  it('keeps literal switch expression when it does not resolve', async () => {
    const steps = [
      {
        action: 'switch',
        expression: 'literalValue',
        cases: [
          { value: 'literalValue', steps: [{ action: 'evaluate', script: 'window.__literalSwitch = 1; return 1' }] },
          { value: 'undefined', steps: [{ action: 'evaluate', script: 'window.__literalSwitch = 2; return 2' }] },
        ],
        default: [{ action: 'evaluate', script: 'window.__literalSwitch = 0; return 0' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__literalSwitch).toBe(1);
  });

  it('treats ctx.foo as a literal switch expression', async () => {
    const steps = [
      {
        action: 'switch',
        expression: 'ctx.foo',
        cases: [
          { value: 'ctx.foo', steps: [{ action: 'evaluate', script: 'window.__ctxLiteral = 1; return 1' }] },
        ],
        default: [{ action: 'evaluate', script: 'window.__ctxLiteral = 0; return 0' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__ctxLiteral).toBe(1);
  });

  it('retries failed group', async () => {
    let attempts = 0;
    const env = createMockEnv();
    env.evaluate = async (script) => {
      attempts++;
      if (attempts < 2) throw new Error('not yet');
      return true;
    };
    const res = await run(
      [
        {
          action: 'retry',
          config: { maxAttempts: 2, delay: 10 },
          steps: [{ action: 'evaluate', script: 'return true' }],
        },
      ],
      env,
    );
    expect(res.status).toBe('success');
  });

  it('fails when retry is exhausted', async () => {
    const env = createMockEnv();
    env.evaluate = async () => {
      throw new Error('always fails');
    };
    const res = await run(
      [
        {
          action: 'retry',
          config: { maxAttempts: 2, delay: 10 },
          steps: [{ action: 'evaluate', script: 'return true' }],
        },
      ],
      env,
    );
    expect(res.status).toBe('failure');
  });

  it('break exits loop', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 5,
        steps: [
          { action: 'evaluate', script: 'window.__loopCount = (window.__loopCount ?? 0) + 1; return true' },
          { action: 'break' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__loopCount).toBe(1);
  });

  it('continue skips rest of iteration', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 3,
        steps: [
          { action: 'continue' },
          { action: 'evaluate', script: 'window.__afterContinue = true; return true' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__afterContinue).toBeUndefined();
  });

  it('break exits whileElementExists loop', async () => {
    document.body.innerHTML = '<div id="item">x</div>';
    const steps = [
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '#item' },
        steps: [
          { action: 'evaluate', script: 'window.__whileCount = (window.__whileCount ?? 0) + 1; return true' },
          { action: 'break' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__whileCount).toBe(1);
  });

  it('continue skips rest of whileElementExists iteration', async () => {
    document.body.innerHTML = '<div id="item">x</div>';
    const steps = [
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '#item' },
        maxIterations: 2,
        steps: [
          { action: 'continue' },
          { action: 'evaluate', script: 'window.__whileContinue = true; return true' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__whileContinue).toBeUndefined();
  });

  it('whileElementNotExists loops until target appears', async () => {
    // Start with no element; have the body add it after 2 polls.
    document.body.innerHTML = '';
    const steps = [
      {
        action: 'loop',
        type: 'whileElementNotExists',
        target: { selector: '#late' },
        maxIterations: 10,
        steps: [
          {
            action: 'evaluate',
            script:
              'window.__whileNotExistsCount = (window.__whileNotExistsCount ?? 0) + 1; ' +
              'if (window.__whileNotExistsCount >= 2 && !document.getElementById("late")) { ' +
              '  const d = document.createElement("div"); d.id = "late"; document.body.appendChild(d); ' +
              '} return true',
          },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__whileNotExistsCount).toBeGreaterThanOrEqual(2);
    // After exit, element must exist (loop only exits when target appears or maxIterations hit).
    expect(document.getElementById('late')).not.toBeNull();
  });

  it('whileElementNotExists without target throws ConfigError', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'whileElementNotExists',
        maxIterations: 5,
        steps: [{ action: 'evaluate', script: 'return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.message).toMatch(/^ConfigError:/);
  });

  it('whileCondition loops until condition is false', async () => {
    // condition checks ctx.counter < 3; body increments counter each iteration.
    document.body.innerHTML = '';
    const steps = [
      {
        action: 'evaluate',
        name: 'counter',
        script: 'return 0',
      },
      {
        action: 'loop',
        type: 'whileCondition',
        condition: { type: 'jsTruthy', script: 'ctx.counter < 3' },
        maxIterations: 10,
        steps: [
          { action: 'evaluate', script: 'window.__whileCondCount = (window.__whileCondCount ?? 0) + 1; return true' },
          {
            action: 'evaluate',
            name: 'counter',
            script: 'return ctx.evaluated.counter + 1',
          },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__whileCondCount).toBe(3);
  });

  it('whileCondition without condition throws ConfigError', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'whileCondition',
        maxIterations: 5,
        steps: [{ action: 'evaluate', script: 'return true' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.message).toMatch(/^ConfigError:/);
  });

  it('whileCondition respects maxIterations', async () => {
    // condition always true; maxIterations caps the body at exactly 3 runs.
    document.body.innerHTML = '';
    const steps = [
      {
        action: 'loop',
        type: 'whileCondition',
        condition: { type: 'jsTruthy', script: 'true' },
        maxIterations: 3,
        steps: [
          { action: 'evaluate', script: 'window.__whileCondMax = (window.__whileCondMax ?? 0) + 1; return true' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__whileCondMax).toBe(3);
  });

  it('break exits forEach loop', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'items',
        script: 'return ["a", "b", "c"]',
      },
      {
        action: 'loop',
        type: 'forEach',
        items: 'evaluated.items',
        steps: [
          { action: 'evaluate', script: 'window.__forEachCount = (window.__forEachCount ?? 0) + 1; return true' },
          { action: 'break' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__forEachCount).toBe(1);
  });

  it('continue skips rest of forEach iteration', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'items',
        script: 'return ["a", "b", "c"]',
      },
      {
        action: 'loop',
        type: 'forEach',
        items: 'evaluated.items',
        steps: [
          { action: 'continue' },
          { action: 'evaluate', script: 'window.__forEachAfterContinue = true; return true' },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__forEachAfterContinue).toBeUndefined();
  });

  it('exits with success status', async () => {
    const res = await run([
      { action: 'evaluate', script: 'window.__exitTest = 1; return 1' },
      { action: 'exit', status: 'success' },
      { action: 'evaluate', script: 'window.__exitTest = 2; return 2' },
    ]);
    expect(res.status).toBe('success');
    expect((window as any).__exitTest).toBe(1);
  });

  it('still runs afterAll and cleanup hooks after exit', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'exit', status: 'success' }],
      {
        afterAll: [{ action: 'evaluate', script: 'window.__afterAllExit = true; return true' }],
        cleanup: [{ action: 'evaluate', script: 'window.__cleanupExit = true; return true' }],
      },
      env,
    );
    expect(res.status).toBe('success');
    expect((window as any).__afterAllExit).toBe(true);
    expect((window as any).__cleanupExit).toBe(true);
  });

  it('runs onError hook after exit with failure status', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'exit', status: 'failure', message: 'planned' }],
      {
        onError: [{ action: 'evaluate', script: 'window.__onErrorExit = true; return true' }],
      },
      env,
    );
    expect(res.status).toBe('failure');
    expect((window as any).__onErrorExit).toBe(true);
  });

  it('exits with failure status and message', async () => {
    const res = await run([
      { action: 'exit', status: 'failure', message: 'custom exit reason' },
      { action: 'evaluate', script: 'window.__exitFailure = true; return true' },
    ]);
    expect(res.status).toBe('failure');
    expect(res.message).toBe('custom exit reason');
    expect((window as any).__exitFailure).toBeUndefined();
  });

  it('classifies exit status failure as ExitFailure', async () => {
    const res = await run([
      { action: 'exit', status: 'failure', message: 'planned failure' },
      { action: 'evaluate', script: 'window.__exitFailureType = true; return true' },
    ]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ExitFailure');
    expect(res.error?.message).toBe('planned failure');
    expect((window as any).__exitFailureType).toBeUndefined();
  });

  it('exits with cancelled status and stops execution', async () => {
    const res = await run([
      { action: 'evaluate', script: 'window.__cancelledTest = 1; return 1' },
      { action: 'exit', status: 'cancelled', message: 'user cancelled' },
      { action: 'evaluate', script: 'window.__cancelledTest = 2; return 2' },
    ]);
    expect(res.status).toBe('cancelled');
    expect(res.message).toBe('user cancelled');
    expect((window as any).__cancelledTest).toBe(1);
  });

  it('fails with invalid exit status', async () => {
    const res = await run([{ action: 'exit', status: 'invalid' }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ScriptError');
    expect(res.error?.message).toContain('invalid exit status invalid');
  });

  it('exit in beforeAll skips main and runs afterAll/cleanup', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'evaluate', script: 'window.__mainRan = true; return true' }],
      {
        beforeAll: [{ action: 'exit', status: 'success' }],
        afterAll: [{ action: 'evaluate', script: 'window.__afterAllExit = true; return true' }],
        cleanup: [{ action: 'evaluate', script: 'window.__cleanupExit = true; return true' }],
      },
      env,
    );
    expect(res.status).toBe('success');
    expect((window as any).__mainRan).toBeUndefined();
    expect((window as any).__afterAllExit).toBe(true);
    expect((window as any).__cleanupExit).toBe(true);
  });

  it('exit with failure in beforeAll runs onError and cleanup', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'evaluate', script: 'window.__mainRan = true; return true' }],
      {
        beforeAll: [{ action: 'exit', status: 'failure', message: 'early stop' }],
        onError: [{ action: 'evaluate', script: 'window.__onErrorExit = true; return true' }],
        cleanup: [{ action: 'evaluate', script: 'window.__cleanupExit = true; return true' }],
      },
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.message).toBe('early stop');
    expect((window as any).__mainRan).toBeUndefined();
    expect((window as any).__onErrorExit).toBe(true);
    expect((window as any).__cleanupExit).toBe(true);
  });

  it('exit in afterAll sets status gracefully and runs cleanup', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'evaluate', script: 'return true' }],
      {
        afterAll: [{ action: 'exit', status: 'failure', message: 'afterAll exit' }],
        cleanup: [{ action: 'evaluate', script: 'window.__cleanupAfterAllExit = true; return true' }],
      },
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.message).toBe('afterAll exit');
    expect((window as any).__cleanupAfterAllExit).toBe(true);
  });

  it('exit in cleanup sets status gracefully', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'evaluate', script: 'return true' }],
      {
        cleanup: [{ action: 'exit', status: 'failure', message: 'cleanup exit' }],
      },
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.message).toBe('cleanup exit');
  });

  it('does not let success exit in afterAll clear a previous fatal error', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      {
        afterAll: [{ action: 'exit', status: 'success' }],
      },
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('lets failure exit in onError override the result message', async () => {
    const env = createMockEnv();
    const res = await runWithHooks(
      [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      {
        onError: [{ action: 'exit', status: 'failure', message: 'override from onError' }],
      },
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.message).toBe('override from onError');
  });

  it('group runs nested steps', async () => {
    const res = await run([
      {
        action: 'group',
        steps: [{ action: 'evaluate', script: 'window.__group = 42; return 42' }],
      },
    ]);
    expect(res.status).toBe('success');
    expect((window as any).__group).toBe(42);
  });

  it('group fails when inner step fails', async () => {
    const res = await run([
      {
        action: 'group',
        steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      },
    ]);
    expect(res.status).toBe('failure');
  });

  it('retry does not catch exit action', async () => {
    const logs: string[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (_level, message) => { logs.push(message); };
    const start = Date.now();
    const res = await run(
      [
        {
          action: 'retry',
          config: { maxAttempts: 3, delay: 1000 },
          steps: [{ action: 'exit', status: 'success' }],
        },
        { action: 'evaluate', script: 'window.__afterExitRetry = true; return true' },
      ],
      env,
    );
    expect(res.status).toBe('success');
    expect((window as any).__afterExitRetry).toBeUndefined();
    expect(logs.some((m) => m.startsWith('Retrying group'))).toBe(false);
    expect(Date.now() - start).toBeLessThan(500);
  });

  it('retry does not catch break inside loop', async () => {
    const logs: string[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (_level, message) => { logs.push(message); };
    const start = Date.now();
    const res = await run(
      [
        {
          action: 'loop',
          type: 'fixedCount',
          count: 5,
          steps: [
            { action: 'evaluate', script: 'window.__breakRetryCount = (window.__breakRetryCount ?? 0) + 1; return true' },
            {
              action: 'retry',
              config: { maxAttempts: 3, delay: 1000 },
              steps: [{ action: 'break' }],
            },
          ],
        },
      ],
      env,
    );
    expect(res.status).toBe('success');
    expect((window as any).__breakRetryCount).toBe(1);
    expect(logs.some((m) => m.startsWith('Retrying group'))).toBe(false);
    expect(Date.now() - start).toBeLessThan(500);
  });

  it('retry does not catch continue inside loop', async () => {
    const logs: string[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (_level, message) => { logs.push(message); };
    const res = await run(
      [
        {
          action: 'loop',
          type: 'fixedCount',
          count: 3,
          steps: [
            { action: 'evaluate', script: 'window.__continueRetryCount = (window.__continueRetryCount ?? 0) + 1; return true' },
            {
              action: 'retry',
              config: { maxAttempts: 3, delay: 1000 },
              steps: [{ action: 'continue' }],
            },
            { action: 'evaluate', script: 'window.__afterContinueRetry = true; return true' },
          ],
        },
      ],
      env,
    );
    expect(res.status).toBe('success');
    expect((window as any).__continueRetryCount).toBe(3);
    expect((window as any).__afterContinueRetry).toBeUndefined();
    expect(logs.some((m) => m.startsWith('Retrying group'))).toBe(false);
  });

  it('propagates unexpected errors from step execution', async () => {
    const env = createMockEnv();
    env.findElement = async () => {
      throw new Error('boom');
    };
    const res = await run(
      [{ action: 'click', target: { selector: '#x' }, condition: { type: 'elementExists', target: { selector: '#x' } } }],
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.error?.message).toBe('boom');
  });

  it('propagates unexpected errors inside fixedCount loop', async () => {
    const env = createMockEnv();
    env.findElement = async () => {
      throw new Error('loop boom');
    };
    const res = await run(
      [
        {
          action: 'loop',
          type: 'fixedCount',
          count: 2,
          steps: [{ action: 'click', target: { selector: '#x' }, condition: { type: 'elementExists', target: { selector: '#x' } } }],
        },
      ],
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.error?.message).toBe('loop boom');
  });

  it('propagates unexpected errors inside whileElementExists loop', async () => {
    document.body.innerHTML = '<div id="item">x</div>';
    const env = createMockEnv();
    env.findElement = async (target: any) => {
      if (target.selector === '#item') return document.getElementById('item');
      throw new Error('while boom');
    };
    const res = await run(
      [
        {
          action: 'loop',
          type: 'whileElementExists',
          target: { selector: '#item' },
          steps: [{ action: 'click', target: { selector: '#x' }, condition: { type: 'elementExists', target: { selector: '#x' } } }],
        },
      ],
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.error?.message).toBe('while boom');
  });

  it('propagates unexpected errors inside forEach loop', async () => {
    const env = createMockEnv();
    env.findElement = async () => {
      throw new Error('forEach boom');
    };
    const res = await run(
      [
        {
          action: 'evaluate',
          name: 'items',
          script: 'return [1]',
        },
        {
          action: 'loop',
          type: 'forEach',
          items: 'evaluated.items',
          steps: [{ action: 'click', target: { selector: '#x' }, condition: { type: 'elementExists', target: { selector: '#x' } } }],
        },
      ],
      env,
    );
    expect(res.status).toBe('failure');
    expect(res.error?.message).toBe('forEach boom');
  });

  // Per docs/dsl-design.md §10.4: loopIndex and loopItem retain their last
  // value after a loop completes, so post-loop steps can reference them.
  it('preserves loopIndex / loopItem after forEach ends', async () => {
    const steps = [
      { action: 'evaluate', name: 'items', script: 'return [10, 20, 30]' },
      {
        action: 'loop',
        type: 'forEach',
        items: 'evaluated.items',
        steps: [
          { action: 'evaluate', script: 'window.__noop = true; return true' },
        ],
      },
      // Post-loop step interpolates {{loopIndex}} / {{loopItem}} directly —
      // must reflect the last iteration's values (idx=2, item=30), not blank.
      { action: 'sendResult', payload: { idx: '{{loopIndex}}', item: '{{loopItem}}' } },
    ];
    const payloads: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p) => { payloads.push(p); };
    const res = await run(steps, env);
    expect(res.status).toBe('success');
    const captured = payloads.find((p) => p.idx !== undefined);
    expect(captured.idx).toBe(2);
    expect(captured.item).toBe(30);
  });

  it('preserves loopIndex after fixedCount ends', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 3,
        steps: [
          {
            action: 'evaluate',
            name: 'counter',
            script: 'return (ctx.evaluated.counter ?? 0) + 1',
          },
        ],
      },
      // Post-loop step uses {{loopIndex}} interpolation — should be the last
      // iteration index (2), not undefined.
      { action: 'sendResult', payload: { finalLoopIndex: '{{loopIndex}}', counter: '{{evaluated.counter}}' } },
    ];
    const payloads: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p) => { payloads.push(p); };
    const res = await run(steps, env);
    expect(res.status).toBe('success');
    const captured = payloads.find((p) => p.finalLoopIndex !== undefined);
    expect(captured.finalLoopIndex).toBe(2);
    expect(captured.counter).toBe(3);
  });
});

describe('executor - extraction & output extras', () => {
  it('extracts HTML', async () => {
    document.body.innerHTML = '<div id="html-source"><span>inner</span></div>';
    const res = await run([{ action: 'extractHtml', name: 'html', target: { selector: '#html-source' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).html).toContain('span');
  });

  it('extracts JSON with path', async () => {
    document.body.innerHTML = '<div id="json-source">{"data":{"value":42}}</div>';
    const res = await run([{ action: 'extractJson', name: 'json', target: { selector: '#json-source' }, path: 'data.value' }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).json).toBe(42);
  });

  it('extracts JSON without path', async () => {
    document.body.innerHTML = '<div id="json-source">{"ok":true}</div>';
    const res = await run([{ action: 'extractJson', name: 'json', target: { selector: '#json-source' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).json).toEqual({ ok: true });
  });

  it('fails extractJson with invalid JSON', async () => {
    document.body.innerHTML = '<div id="json-source">not-json</div>';
    const res = await run([{ action: 'extractJson', name: 'json', target: { selector: '#json-source' } }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ScriptError');
  });

  it('extracts table', async () => {
    document.body.innerHTML = `
      <table id="tbl">
        <thead><tr><th>Name</th><th>Age</th></tr></thead>
        <tbody><tr><td>Alice</td><td>30</td></tr></tbody>
      </table>
    `;
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#tbl' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).table.rows[0].Name).toBe('Alice');
  });

  it('extracts table with header mapping', async () => {
    document.body.innerHTML = `
      <table id="tbl">
        <thead><tr><th>Name</th><th>Age</th></tr></thead>
        <tbody><tr><td>Bob</td><td>25</td></tr></tbody>
      </table>
    `;
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#tbl' }, headers: { Name: 'n', Age: 'a' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).table.rows[0]).toEqual({ n: 'Bob', a: '25' });
  });

  it('fails extractTable when element missing', async () => {
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#missing' }, timeout: 50 }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('auto-detects headers from first row and excludes it from data by default', async () => {
    document.body.innerHTML = `
      <table id="tbl">
        <tr><td>Name</td><td>Age</td></tr>
        <tr><td>Alice</td><td>30</td></tr>
        <tr><td>Bob</td><td>25</td></tr>
      </table>
    `;
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#tbl' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).table.headers).toEqual(['Name', 'Age']);
    expect((res.partialData as any).table.rows).toHaveLength(2);
    expect((res.partialData as any).table.rows[0]).toEqual({ Name: 'Alice', Age: '30' });
  });

  it('includes header row as data when includeHeader is true and there is no thead', async () => {
    document.body.innerHTML = `
      <table id="tbl">
        <tr><td>Name</td><td>Age</td></tr>
        <tr><td>Alice</td><td>30</td></tr>
      </table>
    `;
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#tbl' }, includeHeader: true }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).table.headers).toEqual(['Name', 'Age']);
    expect((res.partialData as any).table.rows).toHaveLength(2);
    expect((res.partialData as any).table.rows[0]).toEqual({ Name: 'Name', Age: 'Age' });
    expect((res.partialData as any).table.rows[1]).toEqual({ Name: 'Alice', Age: '30' });
  });

  it('uses thead for headers and tbody for data regardless of includeHeader', async () => {
    document.body.innerHTML = `
      <table id="tbl">
        <thead><tr><th>Name</th><th>Age</th></tr></thead>
        <tbody><tr><td>Alice</td><td>30</td></tr></tbody>
      </table>
    `;
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#tbl' }, includeHeader: true }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).table.headers).toEqual(['Name', 'Age']);
    expect((res.partialData as any).table.rows).toHaveLength(1);
    expect((res.partialData as any).table.rows[0]).toEqual({ Name: 'Alice', Age: '30' });
  });

  it('merges arrays with concat', async () => {
    const steps = [
      { action: 'evaluate', name: 'a', script: 'return [1, 2]' },
      { action: 'evaluate', name: 'b', script: 'return [3, 4]' },
      { action: 'merge', name: 'merged', from: ['evaluated.a', 'evaluated.b'], strategy: 'concat' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).merged).toEqual([1, 2, 3, 4]);
  });

  it('merges objects with assign', async () => {
    const steps = [
      { action: 'evaluate', name: 'a', script: 'return { x: 1 }' },
      { action: 'evaluate', name: 'b', script: 'return { y: 2 }' },
      { action: 'merge', name: 'merged', from: ['evaluated.a', 'evaluated.b'], strategy: 'assign' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).merged).toEqual({ x: 1, y: 2 });
  });

  it('merges arrays with zip', async () => {
    const steps = [
      { action: 'evaluate', name: 'a', script: 'return [{ x: 1 }, { x: 2 }]' },
      { action: 'evaluate', name: 'b', script: 'return [{ y: 3 }, { y: 4 }]' },
      { action: 'merge', name: 'merged', from: ['evaluated.a', 'evaluated.b'], strategy: 'zip' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).merged).toEqual([{ x: 1, y: 3 }, { x: 2, y: 4 }]);
  });

  it('sends HTML result', async () => {
    const results: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p) => { results.push(p); };
    document.body.innerHTML = '<div id="html-out">x</div>';
    const res = await run([{ action: 'sendHtml', name: 'fragment', target: { selector: '#html-out' } }], env);
    expect(res.status).toBe('success');
    expect(results[0].fragment).toContain('html-out');
  });

  it('sends full page HTML when no target', async () => {
    const results: any[] = [];
    const env = createMockEnv();
    env.transport.sendResult = async (p) => { results.push(p); };
    const res = await run([{ action: 'sendHtml', name: 'page' }], env);
    expect(res.status).toBe('success');
    expect(results[0].page).toContain('<body');
  });

  it('fails sendHtml when target missing', async () => {
    const env = createMockEnv();
    const res = await run([{ action: 'sendHtml', name: 'fragment', target: { selector: '#missing' }, timeout: 50 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('emits custom event', async () => {
    let detail: any;
    document.addEventListener('oc:test', (e) => { detail = (e as CustomEvent).detail; });
    const res = await run([{ action: 'emitEvent', event: 'oc:test', payload: { ok: true } }]);
    expect(res.status).toBe('success');
    expect(detail).toEqual({ ok: true });
  });

  it('emits custom event with interpolated payload', async () => {
    let detail: any;
    document.addEventListener('oc:interp', (e) => { detail = (e as CustomEvent).detail; });
    const steps = [
      { action: 'evaluate', name: 'v', script: 'return 99' },
      { action: 'emitEvent', event: 'oc:interp', payload: { value: '{{evaluated.v}}' } },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect(detail).toEqual({ value: 99 });
  });

  it('fails extractHtml when target missing', async () => {
    const env = createMockEnv();
    env.findElement = async () => null;
    const res = await run([{ action: 'extractHtml', name: 'html', target: { selector: '#missing' } }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('fails extractJson when target missing', async () => {
    const env = createMockEnv();
    env.findElement = async () => null;
    const res = await run([{ action: 'extractJson', name: 'json', target: { selector: '#missing' } }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('fails merge when from is missing', async () => {
    const res = await run([{ action: 'merge', name: 'merged', from: undefined, strategy: 'concat' }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ScriptError');
    expect(res.error?.message).toContain("merge requires a 'from' array");
  });

  it('returns empty array for merge with empty from', async () => {
    const res = await run([{ action: 'merge', name: 'merged', from: [], strategy: 'concat' }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).merged).toEqual([]);
  });

  it('fails merge with unsupported strategy', async () => {
    const steps = [
      { action: 'evaluate', name: 'a', script: 'return [1, 2]' },
      { action: 'merge', name: 'merged', from: ['evaluated.a'], strategy: 'unknown' },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ScriptError');
  });

  it('fails emitEvent when event name is missing', async () => {
    const res = await run([{ action: 'emitEvent', payload: { ok: true } }]);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ScriptError');
  });

  it('emits event with empty payload when payload missing', async () => {
    let detail: any;
    document.addEventListener('oc:empty', (e) => { detail = (e as CustomEvent).detail; });
    const res = await run([{ action: 'emitEvent', event: 'oc:empty' }]);
    expect(res.status).toBe('success');
    expect(detail).toEqual({});
  });
});

describe('executor-utils - classifyError', () => {
  it('handles null/undefined errors', () => {
    expect(classifyError(null)).toBe('UnknownError');
    expect(classifyError(undefined)).toBe('UnknownError');
  });

  it('classifies navigation errors', () => {
    expect(classifyError(new Error('Navigation failed'))).toBe('NavigationError');
  });

  it('classifies network errors', () => {
    expect(classifyError(new Error('Network unreachable'))).toBe('NetworkError');
  });

  it('classifies authentication errors', () => {
    expect(classifyError(new Error('Authentication required'))).toBe('AuthenticationError');
  });

  it('classifies rate limited errors', () => {
    expect(classifyError(new Error('Rate limit exceeded'))).toBe('RateLimited');
  });

  it('classifies blocked errors', () => {
    expect(classifyError(new Error('Blocked by firewall'))).toBe('Blocked');
  });

  it('classifies session expired errors', () => {
    expect(classifyError(new Error('Session expired'))).toBe('SessionExpired');
  });

  it('classifies ScriptError before ElementNotFound substring', () => {
    expect(classifyError(new Error('ScriptError: ElementNotFound fallback'))).toBe('ScriptError');
  });

  it('classifies disabled form controls precisely', () => {
    expect(classifyError(new Error('ElementDisabled: {"selector":"#next"}'))).toBe('ElementDisabled');
  });

  it('classifyError identifies QuotaExceeded (non-recoverable)', () => {
    expect(classifyError(new Error('QuotaExceeded: checkQuota limit 100'))).toBe('QuotaExceeded');
  });

  it('classifyError identifies HumanTimeout (recoverable)', () => {
    expect(classifyError(new Error('HumanTimeout: requestHuman'))).toBe('HumanTimeout');
  });

  it('isRecoverable returns false for QuotaExceeded, true for HumanTimeout', () => {
    expect(isRecoverable('QuotaExceeded')).toBe(false);
    expect(isRecoverable('HumanTimeout')).toBe(true);
  });
});

describe('executor-utils - calculateDelay', () => {
  it('calculates delay from array range', () => {
    const delay = calculateDelay({ delay: [10, 20], backoff: 'fixed' }, 0);
    expect(delay).toBeGreaterThanOrEqual(10);
    expect(delay).toBeLessThanOrEqual(20);
  });

  it('falls back to default delay', () => {
    const delay = calculateDelay({ backoff: 'fixed' }, 0);
    expect(delay).toBe(1000);
  });

  it('uses numeric delay directly', () => {
    const delay = calculateDelay({ delay: 500, backoff: 'fixed' }, 0);
    expect(delay).toBe(500);
  });
});

describe('executor - hook throws', () => {
  it('finalizes when beforeAll hook throws flow control', async () => {
    const env = createMockEnv();
    const hooks = {
      beforeAll: [{ action: 'break' }],
    };
    const res = await runWithHooks([{ action: 'evaluate', script: 'return 1' }], hooks, env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('UnknownError');
    expect(res.error?.message).toContain('FlowControl');
  });

  it('logs warning when afterAll hook throws flow control', async () => {
    const logs: { level: string; message: string }[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message) => { logs.push({ level, message }); };
    const hooks = {
      afterAll: [{ action: 'break' }],
    };
    const res = await runWithHooks([{ action: 'evaluate', script: 'return 1' }], hooks, env);
    expect(res.status).toBe('success');
    expect(logs.some((l) => l.message.includes('afterAll hook failed'))).toBe(true);
  });

  it('logs warning when onError hook throws flow control', async () => {
    const logs: { level: string; message: string }[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message) => { logs.push({ level, message }); };
    const hooks = {
      onError: [{ action: 'break' }],
    };
    const res = await runWithHooks(
      [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      hooks,
      env,
    );
    expect(res.status).toBe('failure');
    expect(logs.some((l) => l.message.includes('onError hook failed'))).toBe(true);
  });

  it('logs warning when cleanup hook throws flow control', async () => {
    const logs: { level: string; message: string }[] = [];
    const env = createMockEnv();
    env.transport.sendLog = async (level, message) => { logs.push({ level, message }); };
    const hooks = {
      cleanup: [{ action: 'break' }],
    };
    const res = await runWithHooks([{ action: 'evaluate', script: 'return 1' }], hooks, env);
    expect(res.status).toBe('success');
    expect(logs.some((l) => l.message.includes('cleanup hook failed'))).toBe(true);
  });
});

describe('executor - humanize moveMouse randomOffset', () => {
  beforeEach(() => {
    document.body.innerHTML = '<button id="btn">Click</button>';
  });

  it('moves mouse without randomOffset', async () => {
    const res = await run([
      { action: 'click', target: { selector: '#btn' }, humanize: { moveMouse: true } },
    ]);
    expect(res.status).toBe('success');
  });

  it('moves mouse with numeric randomOffset', async () => {
    const res = await run([
      { action: 'click', target: { selector: '#btn' }, humanize: { moveMouse: true, randomOffset: 20 } },
    ]);
    expect(res.status).toBe('success');
  });

  it('moves mouse with object randomOffset', async () => {
    const res = await run([
      { action: 'click', target: { selector: '#btn' }, humanize: { moveMouse: true, randomOffset: { x: 10, y: 20 } } },
    ]);
    expect(res.status).toBe('success');
  });
});

describe('executor - resolveExpressionSource fallback', () => {
  it('filter reads data from plain extracted key', async () => {
    document.body.innerHTML = '<div class="item">1</div><div class="item">2</div>';
    const steps = [
      { action: 'extract', name: 'raw', target: { selector: '.item' }, multiple: true, fields: { text: { type: 'text' } } },
      { action: 'filter', name: 'one', from: 'raw', criteria: { field: 'text', op: 'eq', value: '1' } },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((res.partialData as any).one).toHaveLength(1);
  });
});


describe('executor - final review fixes', () => {
  it('treats dot-containing string as literal switch expression', async () => {
    const steps = [
      {
        action: 'switch',
        expression: 'v1.0',
        cases: [
          { value: 'v1.0', steps: [{ action: 'evaluate', script: 'window.__dotLiteral = 1; return 1' }] },
          { value: 'undefined', steps: [{ action: 'evaluate', script: 'window.__dotLiteral = 2; return 2' }] },
        ],
        default: [{ action: 'evaluate', script: 'window.__dotLiteral = 0; return 0' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('success');
    expect((window as any).__dotLiteral).toBe(1);
  });

  it('extracts table with explicit headers and no tbody', async () => {
    document.body.innerHTML = `
      <table id="tbl">
        <tr><td>Alice</td><td>30</td></tr>
        <tr><td>Bob</td><td>25</td></tr>
      </table>
    `;
    const res = await run([{ action: 'extractTable', name: 'table', target: { selector: '#tbl' }, headers: { Name: 'n', Age: 'a' } }]);
    expect(res.status).toBe('success');
    expect((res.partialData as any).table.rows).toHaveLength(2);
    expect((res.partialData as any).table.rows[0]).toEqual({ n: 'Alice', a: '30' });
  });
});


describe('executor - whole-branch review fixes', () => {
  it('propagates nested step failure from switch case', async () => {
    const steps = [
      {
        action: 'evaluate',
        name: 'choice',
        script: 'return "b"',
      },
      {
        action: 'switch',
        expression: 'evaluated.choice',
        cases: [
          { value: 'a', steps: [{ action: 'evaluate', script: 'window.__switchFailA = 1; return 1' }] },
          { value: 'b', steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }] },
        ],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('propagates nested step failure from switch default', async () => {
    const steps = [
      {
        action: 'switch',
        expression: 'no-match',
        cases: [{ value: 'a', steps: [{ action: 'evaluate', script: 'return 1' }] }],
        default: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('fails switch with malformed cases', async () => {
    const steps = [
      {
        action: 'switch',
        expression: 'x',
        cases: 'not-an-array' as any,
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ScriptError');
    expect(res.error?.message).toContain('switch requires a cases array');
  });

  it('propagates nested step failure from if then branch', async () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: 'body' } },
        then: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
        else: [{ action: 'evaluate', script: 'return 1' }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('propagates nested step failure from fixedCount loop body', async () => {
    const steps = [
      {
        action: 'loop',
        type: 'fixedCount',
        count: 3,
        steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('propagates nested step failure from whileElementExists loop body', async () => {
    document.body.innerHTML = '<div id="item">x</div>';
    const steps = [
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '#item' },
        maxIterations: 2,
        steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('propagates nested step failure from forEach loop body', async () => {
    const steps = [
      { action: 'evaluate', name: 'items', script: 'return ["a", "b"]' },
      {
        action: 'loop',
        type: 'forEach',
        items: 'evaluated.items',
        steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 50 }],
      },
    ];
    const res = await run(steps);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('ElementNotFound');
  });

  it('times out waiting for text and reports TimeoutError', async () => {
    document.body.innerHTML = '<div id="status">loading</div>';
    const env = createMockEnv();
    env.sleep = async () => {};
    const res = await run([{ action: 'waitForText', target: { selector: '#status' }, text: 'ready', timeout: 100 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('TimeoutError');
  });

  it('times out waiting for element hidden and reports TimeoutError', async () => {
    document.body.innerHTML = '<div id="popup">x</div>';
    const env = createMockEnv();
    env.sleep = async () => {};
    const res = await run([{ action: 'waitForElementHidden', target: { selector: '#popup' }, timeout: 100 }], env);
    expect(res.status).toBe('failure');
    expect(res.error?.type).toBe('TimeoutError');
  });
});

describe('executor - classifyError', () => {
  it('classifyError identifies ConditionError', () => {
    expect(classifyError(new Error('ConditionError: unsupported condition type: foo'))).toBe('ConditionError');
    expect(classifyError(new Error('ConditionError: ...'))).not.toBe('UnknownError');
  });

  it('classifyError identifies ConfigError', () => {
    expect(classifyError(new Error('ConfigError: unsupported onError strategy: bogus'))).toBe('ConfigError');
  });
});

describe('executor - unknown onError strategy', () => {
  it('throws ConfigError on unknown onError strategy', async () => {
    const res = await run(
      [{ action: 'click', target: { selector: '#nonexistent' }, timeout: 50, onError: 'bogusStrategy' }],
    );
    expect(res.status).toBe('failure');
    expect(res.error?.message).toMatch(/^ConfigError:/);
    expect(res.error?.type).toBe('ConfigError');
  });
});
