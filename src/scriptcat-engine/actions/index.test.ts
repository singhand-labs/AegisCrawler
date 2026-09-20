/// <reference types="vitest/globals" />
import { vi } from 'vitest';
import { executeAction } from './index';
import { runSteps } from '../executor-utils';
import type { Environment, RuntimeContext } from '../types';
import type { CircuitBreakerAction } from '../../rule-engine/types/action';

function createCtx(overrides?: Partial<RuntimeContext>): RuntimeContext {
  return {
    taskId: 't1',
    workerId: 'w1',
    ruleId: 'r1',
    ruleVersion: '1.0.0',
    variables: { __selectors: {} },
    extracted: {},
    evaluated: {},
    captured: {},
    resultsSent: 0,
    logsSent: 0,
    ...overrides,
  };
}

function createMockEnv(overrides?: Partial<Environment>): Environment {
  const transport = {
    fetchRule: vi.fn(async () => null),
    sendResult: vi.fn(async () => {}),
    sendLog: vi.fn(async () => {}),
    sendHeartbeat: vi.fn(async () => ({ cancelRequested: false })),
    sendStatus: vi.fn(async () => {}),
    sendSnapshot: vi.fn(async () => {}),
  };
  return {
    findElement: async (target) => {
      if (!target || typeof target !== 'object') return null;
      if ('selector' in target && target.selector) {
        return document.querySelector(target.selector as string);
      }
      return null;
    },
    findElements: async (target) => {
      if (!target || typeof target !== 'object') return [];
      if ('selector' in target && target.selector) {
        return Array.from(document.querySelectorAll(target.selector as string));
      }
      return [];
    },
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    now: () => Date.now(),
    transport,
    getUrl: () => 'https://example.com/',
    getTitle: () => 'Test',
    evaluate: async (script, ctx, args) => {
      const fn = new Function('ctx', 'args', script);
      return fn(ctx, args);
    },
    screenshot: vi.fn(async () => null),
    saveSnapshot: vi.fn(async () => null),
    ...overrides,
  } as Environment;
}

function circuitBreakerAction(
  overrides: Partial<CircuitBreakerAction> = {},
): CircuitBreakerAction {
  return {
    action: 'circuitBreaker',
    name: 'test-breaker',
    failureThreshold: 5,
    windowMs: 60_000,
    cooldownMs: 60_000,
    ...overrides,
  } satisfies CircuitBreakerAction;
}

beforeEach(() => {
  document.body.innerHTML = '';
  localStorage.clear();
  delete (window as any).__ocCircuitBreakers;
  delete (window as any).__lastMouseX;
  delete (window as any).__lastMouseY;
  if (!globalThis.DataTransfer) {
    globalThis.DataTransfer = class DataTransfer {
      items: any[] = [];
      getData() { return ''; }
      setData() {}
    } as any;
  }
  if (!globalThis.ClipboardEvent) {
    globalThis.ClipboardEvent = class ClipboardEvent extends Event {
      clipboardData: DataTransfer | null;
      constructor(type: string, init?: any) {
        super(type, init);
        this.clipboardData = init?.clipboardData ?? null;
      }
    } as any;
  }
});

describe('extract actions', () => {
  it('extracts html, boolean, attr with resolve, css, exists, count and default branches', async () => {
    document.body.innerHTML = `
      <div id="card" data-id="42" data-model="11-inch-display">
        <a href="/item/42">link</a>
        <span class="badge" style="color: red">badge<span class="price">$1,049.00</span></span>
      </div>
    `;
    const ctx = createCtx();
    const res = await executeAction(
      {
        action: 'extract',
        name: 'item',
        target: { selector: '#card' },
        fields: {
          html: { type: 'html' },
          bool: { type: 'boolean' },
          link: { type: 'attr', selector: 'a', attr: 'href', resolve: true },
          displaySize: { type: 'attr', attr: 'data-model', regex: '(?:11|13)-inch' },
          price: { type: 'number', selector: '.price' },
          color: { type: 'css', selector: '.badge', cssProperty: 'color' },
          exists: { type: 'exists', selector: 'a' },
          count: { type: 'count' },
          unknown: { type: 'unknown' as any },
          missing: { type: 'text', selector: '.none', default: 'fallback' },
        },
      } as any,
      ctx,
      createMockEnv(),
    );
    expect(res.link).toBe(new URL('/item/42', location.href).href);
    expect(res.displaySize).toBe('11-inch');
    expect(res.price).toBe(1049);
    expect(res.html).toContain('badge');
    expect(res.bool).toBe(true);
    expect(res.exists).toBe(true);
    expect(res.count).toBe(2);
    expect(res.unknown).toContain('link');
    expect(res.unknown).toContain('badge');
    expect(res.missing).toBe('fallback');
  });

  it('recursively extracts nested fields relative to each selected node', async () => {
    document.body.innerHTML = `
      <article id="card">
        <section class="details">
          <h2 class="title">Product A</h2>
          <span class="code">SKU #ABC-42 available</span>
          <script class="payload" type="application/json">{"items":[{"name":"first"},{"name":"second"}]}</script>
        </section>
      </article>
    `;
    const result = await executeAction(
      {
        action: 'extract',
        name: 'item',
        target: { selector: '#card' },
        fields: {
          details: {
            type: 'exists',
            selector: '.details',
            fields: {
              title: { type: 'text', selector: '.title' },
              code: { type: 'regex', selector: '.code', regex: '#[A-Z]+-\\d+' },
              selected: { type: 'json', selector: '.payload', path: 'items.1.name' },
              self: {
                type: 'exists',
                fields: {
                  titleAgain: { type: 'text', selector: '.title' },
                },
              },
            },
          },
        },
      } as any,
      createCtx(),
      createMockEnv(),
    );

    expect(result).toEqual({
      details: {
        title: 'Product A',
        code: '#ABC-42',
        selected: 'second',
        self: { titleAgain: 'Product A' },
      },
    });
  });

  it('preserves nested object shape independently for every repeated row', async () => {
    document.body.innerHTML = `
      <main id="items">
        <article class="row"><section class="details"><h2>A</h2></section></article>
        <article class="row"><section class="details"><h2>B</h2></section></article>
      </main>
    `;
    const result = await executeAction(
      {
        action: 'extract',
        name: 'items',
        target: { selector: '#items > .row' },
        multiple: true,
        fields: {
          details: {
            type: 'html',
            selector: '.details',
            fields: {
              title: { type: 'text', selector: 'h2' },
            },
          },
        },
      } as any,
      createCtx(),
      createMockEnv(),
    );

    expect(result).toEqual([
      { details: { title: 'A' } },
      { details: { title: 'B' } },
    ]);
  });

  it('extracts typed JSON and regex fields with their public field semantics', async () => {
    document.body.innerHTML = `
      <article id="card">
        <script class="payload" type="application/json">{"items":[{"name":"first"},{"name":"second"}]}</script>
        <span class="code">prefix #ABC-42 suffix</span>
      </article>
    `;
    const result = await executeAction(
      {
        action: 'extract',
        name: 'item',
        target: { selector: '#card' },
        fields: {
          payload: { type: 'json', selector: '.payload' },
          selected: { type: 'json', selector: '.payload', path: 'items.0.name' },
          code: { type: 'regex', selector: '.code', regex: '#[A-Z]+-\\d+' },
          missingCode: { type: 'regex', selector: '.code', regex: '^missing$', default: 'none' },
        },
      } as any,
      createCtx(),
      createMockEnv(),
    );

    expect(result).toEqual({
      payload: { items: [{ name: 'first' }, { name: 'second' }] },
      selected: 'first',
      code: '#ABC-42',
      missingCode: 'none',
    });

    const standalone = await executeAction(
      { action: 'extractJson', name: 'payload', target: { selector: '.payload' }, path: 'items.1.name' } as any,
      createCtx(),
      createMockEnv(),
    );
    expect(standalone).toBe('second');
  });

  it.each([
    { name: 'missing regex', field: { type: 'regex', selector: '.code' }, error: /non-empty pattern/ },
    { name: 'invalid regex', field: { type: 'regex', selector: '.code', regex: '(' }, error: /invalid pattern/ },
    { name: 'invalid JSON', field: { type: 'json', selector: '.code' }, error: /invalid JSON/ },
  ])('fails invalid typed field contract: $name', async ({ field, error }) => {
    document.body.innerHTML = '<article id="card"><span class="code">plain text</span></article>';
    await expect(
      executeAction(
        {
          action: 'extract',
          name: 'item',
          target: { selector: '#card' },
          fields: { value: field },
        } as any,
        createCtx(),
        createMockEnv(),
      ),
    ).rejects.toThrow(error);
  });

  it.each([
    'items.01.name',
    'items.-1.name',
    'items.-0.name',
    'items.+1.name',
    'items.9007199254740992.name',
    'items..name',
  ])(
    'rejects non-canonical JSON array path %s for standalone and typed extraction',
    async (path) => {
      document.body.innerHTML = '<script id="payload" type="application/json">{"items":[{"name":"first"},{"name":"second"}]}</script>';

      await expect(
        executeAction(
          { action: 'extractJson', name: 'payload', target: { selector: '#payload' }, path } as any,
          createCtx(),
          createMockEnv(),
        ),
      ).rejects.toThrow(/JSON path/);

      await expect(
        executeAction(
          {
            action: 'extract',
            name: 'item',
            target: { selector: 'body' },
            fields: {
              selected: { type: 'json', selector: '#payload', path },
            },
          } as any,
          createCtx(),
          createMockEnv(),
        ),
      ).rejects.toThrow(/JSON path/);
    },
  );

  it('extracts outer html', async () => {
    document.body.innerHTML = '<div id="x"><b>hi</b></div>';
    const ctx = createCtx();
    const res = await executeAction({ action: 'extractHtml', name: 'h', target: { selector: '#x' } } as any, ctx, createMockEnv());
    expect(res).toContain('<b>hi</b>');
  });

  it('extracts json and navigates path', async () => {
    document.body.innerHTML = '<div id="json">{"a":{"b":7}}</div>';
    const ctx = createCtx();
    const res = await executeAction(
      { action: 'extractJson', name: 'j', target: { selector: '#json' }, path: 'a.b' } as any,
      ctx,
      createMockEnv(),
    );
    expect(res).toBe(7);
  });

  it('throws on invalid json', async () => {
    document.body.innerHTML = '<div id="json">not json</div>';
    await expect(
      executeAction({ action: 'extractJson', name: 'j', target: { selector: '#json' } } as any, createCtx(), createMockEnv()),
    ).rejects.toThrow('invalid JSON');
  });

  it('extracts table with explicit headers', async () => {
    document.body.innerHTML = `
      <table id="t"><tr><td>A</td><td>B</td></tr><tr><td>1</td><td>2</td></tr></table>
    `;
    const ctx = createCtx();
    const res = await executeAction(
      { action: 'extractTable', name: 'tbl', target: { selector: '#t' }, headers: { A: 'colA', B: 'colB' }, includeHeader: true } as any,
      ctx,
      createMockEnv(),
    );
    expect(res.headers).toEqual(['A', 'B']);
    expect(res.rows).toContainEqual({ colA: '1', colB: '2' });
  });

  it('extracts table using thead default', async () => {
    document.body.innerHTML = `
      <table id="t"><thead><tr><th>X</th></tr></thead><tbody><tr><td>10</td></tr></tbody></table>
    `;
    const ctx = createCtx();
    const res = await executeAction({ action: 'extractTable', name: 'tbl', target: { selector: '#t' } } as any, ctx, createMockEnv());
    expect(res.headers).toEqual(['X']);
    expect(res.rows).toEqual([{ X: '10' }]);
  });

  it('extracts table with first row headers fallback', async () => {
    document.body.innerHTML = `
      <table id="t"><tr><td>X</td><td>Y</td></tr><tr><td>1</td><td>2</td></tr></table>
    `;
    const ctx = createCtx();
    const res = await executeAction({ action: 'extractTable', name: 'tbl', target: { selector: '#t' } } as any, ctx, createMockEnv());
    expect(res.headers).toEqual(['X', 'Y']);
    expect(res.rows).toEqual([{ X: '1', Y: '2' }]);
  });

  it('merges with concat, assign, zip and rejects invalid strategy', async () => {
    const ctx = createCtx({ extracted: { a: [1, 2], b: { x: 1 }, c: [{ p: 1 }, { p: 2 }], d: [{ q: 10 }, { q: 20 }] } });
    await executeAction({ action: 'merge', name: 'm1', from: ['extracted.a', 'extracted.a'] } as any, ctx, createMockEnv());
    expect(ctx.extracted.m1).toEqual([1, 2, 1, 2]);

    await executeAction({ action: 'merge', name: 'm2', from: ['extracted.b', 'extracted.b'], strategy: 'assign' } as any, ctx, createMockEnv());
    expect(ctx.extracted.m2).toEqual({ x: 1 });

    await executeAction({ action: 'merge', name: 'm3', from: ['extracted.c', 'extracted.d'], strategy: 'zip' } as any, ctx, createMockEnv());
    expect(ctx.extracted.m3).toEqual([{ p: 1, q: 10 }, { p: 2, q: 20 }]);

    await expect(
      executeAction({ action: 'merge', name: 'm4', from: ['extracted.a'], strategy: 'bad' } as any, ctx, createMockEnv()),
    ).rejects.toThrow('unsupported merge strategy');
  });

  it('extracts all page info types', async () => {
    const ctx = createCtx();
    const res = await executeAction(
      {
        action: 'extractPageInfo',
        name: 'info',
        fields: { url: { type: 'url' }, title: { type: 'title' }, domain: { type: 'domain' }, ts: { type: 'timestamp' }, ref: { type: 'referrer' }, ua: { type: 'userAgent' } },
      } as any,
      ctx,
      createMockEnv(),
    );
    expect(res.url).toBe('https://example.com/');
    expect(res.title).toBe('Test');
    expect(res.domain).toBe(location.hostname);
    expect(typeof res.ts).toBe('number');
  });
});

describe('input helpers', () => {
  it('binds an exact text click target and fails closed when that label is absent', async () => {
    document.body.innerHTML = '<button id="travel" type="button">Travel</button>';
    const button = document.getElementById('travel')!;
    const listener = vi.fn();
    button.addEventListener('click', listener);
    const findElement = vi.fn(async (target) =>
      target?.text === 'Travel' ? button : null);
    const env = createMockEnv({ findElement });

    await executeAction(
      { action: 'click', target: { text: '{{category}}', visible: true } } as any,
      createCtx({ variables: { __selectors: {}, category: 'Travel' } }),
      env,
    );
    expect(findElement).toHaveBeenCalledWith({ text: 'Travel', visible: true }, 5000);
    expect(listener).toHaveBeenCalledOnce();

    await expect(executeAction(
      { action: 'click', target: { text: '{{category}}', visible: true }, timeout: 0 } as any,
      createCtx({ variables: { __selectors: {}, category: 'Aegis Missing Category' } }),
      env,
    )).rejects.toThrow('ElementNotFound: {"text":"Aegis Missing Category","visible":true}');
  });

  it('rejects a disabled form control without dispatching its click listener', async () => {
    document.body.innerHTML = '<button id="disabled" type="button" disabled>Next</button>';
    const listener = vi.fn();
    document.getElementById('disabled')!.addEventListener('click', listener);

    await expect(
      executeAction({ action: 'click', target: { selector: '#disabled' } } as any, createCtx(), createMockEnv()),
    ).rejects.toThrow('ElementDisabled: {"selector":"#disabled"}');
    expect(listener).not.toHaveBeenCalled();
  });

  it('elementMayNavigate: normal link navigates', async () => {
    document.body.innerHTML = '<a id="link" href="/next">next</a>';
    let url = 'https://example.com/';
    const env = createMockEnv({ getUrl: () => url });
    document.getElementById('link')!.addEventListener('click', () => { url = 'https://example.com/next'; });

    await executeAction({ action: 'click', target: { selector: '#link' } } as any, createCtx(), env);
    expect(url).toBe('https://example.com/next');
  });

  it('elementMayNavigate: hash/javascript/no-href links do not wait for navigation', async () => {
    document.body.innerHTML = `
      <a id="a1" href="#">hash</a>
      <a id="a2" href="javascript:void(0)">js</a>
      <a id="a3">none</a>
    `;
    const env = createMockEnv();
    for (const id of ['#a1', '#a2', '#a3']) {
      await executeAction({ action: 'click', target: { selector: id } } as any, createCtx(), env);
    }
    expect(env.transport.sendLog).not.toHaveBeenCalled();
  });

  it('elementMayNavigate: submit inputs and buttons navigate only with form', async () => {
    document.body.innerHTML = `
      <form id="f"><input id="sub" type="submit" /></form>
      <input id="noform" type="submit" />
      <button id="btnsub" form="f">go</button>
      <button id="btnplain" type="button">plain</button>
    `;
    let url = 'https://example.com/';
    const env = createMockEnv({ getUrl: () => url });
    document.getElementById('sub')!.addEventListener('click', () => { url = 'https://example.com/form'; });
    document.getElementById('btnsub')!.addEventListener('click', () => { url = 'https://example.com/form'; });

    await executeAction({ action: 'click', target: { selector: '#sub' } } as any, createCtx(), env);
    expect(url).toBe('https://example.com/form');

    url = 'https://example.com/';
    await executeAction({ action: 'click', target: { selector: '#btnsub' } } as any, createCtx(), env);
    expect(url).toBe('https://example.com/form');

    await executeAction({ action: 'click', target: { selector: '#noform' } } as any, createCtx(), env);
    await executeAction({ action: 'click', target: { selector: '#btnplain' } } as any, createCtx(), env);
  });

  it('types, clears, appends and submits', async () => {
    document.body.innerHTML = '<form id="f"><input id="q" /></form>';
    let url = 'https://example.com/';
    const env = createMockEnv({ getUrl: () => url });
    const input = document.getElementById('q') as HTMLInputElement;
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') url = 'https://example.com/results'; });

    await executeAction({ action: 'type', target: { selector: '#q' }, value: 'hello' } as any, createCtx(), env);
    expect(input.value).toBe('hello');

    input.value = 'old';
    await executeAction({ action: 'type', target: { selector: '#q' }, value: 'new' } as any, createCtx(), env);
    expect(input.value).toBe('new');

    await executeAction({ action: 'clear', target: { selector: '#q' } } as any, createCtx(), env);
    expect(input.value).toBe('');

    await executeAction({ action: 'type', target: { selector: '#q' }, value: 'x', submit: true, navigationTimeout: 500 } as any, createCtx(), env);
    expect(url).toBe('https://example.com/results');
  });

  it('types with humanization delay', async () => {
    document.body.innerHTML = '<input id="i" />';
    await executeAction(
      { action: 'type', target: { selector: '#i' }, value: 'ab', humanize: { typingDelay: [1, 2] } } as any,
      createCtx(),
      createMockEnv(),
    );
    expect((document.getElementById('i') as HTMLInputElement).value).toBe('ab');
  });

  it('pastes with simulateClipboard', async () => {
    document.body.innerHTML = '<input id="i" />';
    await executeAction(
      { action: 'paste', target: { selector: '#i' }, value: 'pasted', simulateClipboard: true } as any,
      createCtx(),
      createMockEnv(),
    );
    expect((document.getElementById('i') as HTMLInputElement).value).toBe('pasted');
  });

  it('selects dropdown by value, text and index', async () => {
    document.body.innerHTML = `
      <select id="s"><option value="a">Alpha</option><option value="b">Beta</option></select>
    `;
    const sel = document.getElementById('s') as HTMLSelectElement;
    await executeAction({ action: 'select', target: { selector: '#s' }, value: 'b', by: 'value' } as any, createCtx(), createMockEnv());
    expect(sel.value).toBe('b');
    await executeAction({ action: 'select', target: { selector: '#s' }, value: 'Alpha', by: 'text' } as any, createCtx(), createMockEnv());
    expect(sel.value).toBe('a');
    await executeAction({ action: 'select', target: { selector: '#s' }, value: '1', by: 'index' } as any, createCtx(), createMockEnv());
    expect(sel.value).toBe('b');
  });

  it('checks toggles state', async () => {
    document.body.innerHTML = '<input id="c" type="checkbox" />';
    const cb = document.getElementById('c') as HTMLInputElement;
    await executeAction({ action: 'check', target: { selector: '#c' }, state: 'toggle' } as any, createCtx(), createMockEnv());
    expect(cb.checked).toBe(true);
    await executeAction({ action: 'check', target: { selector: '#c' }, state: 'toggle' } as any, createCtx(), createMockEnv());
    expect(cb.checked).toBe(false);
  });

  it('selects radio only when unchecked', async () => {
    document.body.innerHTML = '<input id="r" type="radio" />';
    const r = document.getElementById('r') as HTMLInputElement;
    await executeAction({ action: 'selectRadio', target: { selector: '#r' } } as any, createCtx(), createMockEnv());
    expect(r.checked).toBe(true);
    await executeAction({ action: 'selectRadio', target: { selector: '#r' } } as any, createCtx(), createMockEnv());
    expect(r.checked).toBe(true);
  });

  it('typeAndSelect picks matching suggestion', async () => {
    document.body.innerHTML = '<input id="i" /><ul class="suggestions"><li>Apple</li><li>Banana</li></ul>';
    await executeAction(
      { action: 'typeAndSelect', target: { selector: '#i' }, value: 'Ban', suggestionSelector: '.suggestions li', waitForSuggestions: 300 } as any,
      createCtx(),
      createMockEnv(),
    );
    expect((document.getElementById('i') as HTMLInputElement).value).toBe('Ban');
  });

  it('uploadFile catches blocked file assignment', async () => {
    document.body.innerHTML = '<input id="f" type="file" />';
    const env = createMockEnv();
    await executeAction({ action: 'uploadFile', target: { selector: '#f' }, files: null } as any, createCtx(), env);
    expect(env.transport.sendLog).toHaveBeenCalled();
  });

  it('focuses and blurs with and without target', async () => {
    document.body.innerHTML = '<input id="i" />';
    const input = document.getElementById('i') as HTMLElement;
    await executeAction({ action: 'focus', target: { selector: '#i' } } as any, createCtx(), createMockEnv());
    expect(document.activeElement).toBe(input);

    await executeAction({ action: 'blur', target: { selector: '#i' } } as any, createCtx(), createMockEnv());
    expect(document.activeElement).not.toBe(input);

    input.focus();
    await executeAction({ action: 'blur' } as any, createCtx(), createMockEnv());
    expect(document.activeElement).not.toBe(input);
  });

  it('tabs to previous', async () => {
    await executeAction({ action: 'tabToPrevious', count: 1 } as any, createCtx(), createMockEnv());
  });

  it('pressKey handles nested keys and keyDuration', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'pressKey', keys: [['Control', 'a']], humanize: { keyDuration: [1, 2] } } as any, createCtx(), env);
  });

  it('pressKey sleeps once per key with value in range when step humanize.keyDuration set', async () => {
    const sleep = vi.fn(async () => {});
    const env = createMockEnv({ sleep });
    await executeAction({ action: 'pressKey', keys: [['Control', 'a']], humanize: { keyDuration: [10, 20] } } as any, createCtx(), env);
    const sleeps = sleep.mock.calls.map((c: number[]) => c[0]);
    expect(sleeps).toHaveLength(2);
    for (const v of sleeps) { expect(v).toBeGreaterThanOrEqual(10); expect(v).toBeLessThanOrEqual(20); }
  });

  it('pressKey inherits rule.humanize.keyDuration via resolveHumanize when step omits it', async () => {
    const sleep = vi.fn(async () => {});
    const env = createMockEnv({ sleep });
    const ctx = createCtx({ ruleDefaults: { humanize: { keyDuration: [10, 20] } } } as any);
    await executeAction({ action: 'pressKey', keys: ['Enter'] } as any, ctx, env);
    const sleeps = sleep.mock.calls.map((c: number[]) => c[0]);
    expect(sleeps).toHaveLength(1);
    expect(sleeps[0]).toBeGreaterThanOrEqual(10);
    expect(sleeps[0]).toBeLessThanOrEqual(20);
  });

  it('pressKey step humanize.keyDuration overrides rule.humanize.keyDuration', async () => {
    const sleep = vi.fn(async () => {});
    const env = createMockEnv({ sleep });
    const ctx = createCtx({ ruleDefaults: { humanize: { keyDuration: [100, 200] } } } as any);
    await executeAction({ action: 'pressKey', keys: ['Enter'], humanize: { keyDuration: [10, 20] } } as any, ctx, env);
    const sleeps = sleep.mock.calls.map((c: number[]) => c[0]);
    expect(sleeps).toHaveLength(1);
    expect(sleeps[0]).toBeGreaterThanOrEqual(10);
    expect(sleeps[0]).toBeLessThanOrEqual(20);
  });

  it('pressKey does not sleep between keydown/keyup when keyDuration unset (R5)', async () => {
    const sleep = vi.fn(async () => {});
    const env = createMockEnv({ sleep });
    await executeAction({ action: 'pressKey', keys: ['Enter'] } as any, createCtx(), env);
    expect(sleep.mock.calls).toHaveLength(0);
  });

  it('pressKey with flat keys array sleeps once per key', async () => {
    const sleep = vi.fn(async () => {});
    const env = createMockEnv({ sleep });
    await executeAction({ action: 'pressKey', keys: ['a', 'b', 'c'], humanize: { keyDuration: [5, 15] } } as any, createCtx(), env);
    expect(sleep.mock.calls).toHaveLength(3);
  });

  it('keyCombination ignores humanize.keyDuration (R4 regression lock)', async () => {
    const sleep = vi.fn(async () => {});
    const env = createMockEnv({ sleep });
    await executeAction({ action: 'keyCombination', keys: ['Control', 'a'], humanize: { keyDuration: [10, 20] } } as any, createCtx(), env);
    // keyCombination uses a hard-coded 50ms chord gap; humanize.keyDuration must not affect it.
    expect(sleep.mock.calls.map((c: number[]) => c[0])).toEqual([50]);
  });

  it('keyCombination presses and releases keys', async () => {
    await executeAction({ action: 'keyCombination', keys: ['Control', 'a'] } as any, createCtx(), createMockEnv());
  });
});

describe('wait helpers', () => {
  it('waits for url equals', async () => {
    let url = 'https://example.com/start';
    const env = createMockEnv({ getUrl: () => url });
    setTimeout(() => { url = 'https://example.com/end'; }, 20);
    await executeAction({ action: 'waitForUrl', pattern: 'https://example.com/end', matchType: 'equals', timeout: 500 } as any, createCtx(), env);
  });

  it('waits for url contains', async () => {
    let url = 'https://example.com/start';
    const env = createMockEnv({ getUrl: () => url });
    setTimeout(() => { url = 'https://example.com/items/7'; }, 20);
    await executeAction({ action: 'waitForUrl', pattern: 'items', matchType: 'contains', timeout: 500 } as any, createCtx(), env);
  });

  it('waits for url matches', async () => {
    let url = 'https://example.com/start';
    const env = createMockEnv({ getUrl: () => url });
    setTimeout(() => { url = 'https://example.com/results'; }, 20);
    await executeAction({ action: 'waitForUrl', pattern: 'results$', matchType: 'matches', timeout: 500 } as any, createCtx(), env);
  });

  it('waitForUrl times out', async () => {
    const env = createMockEnv({ getUrl: () => 'https://example.com/start' });
    await expect(
      executeAction({ action: 'waitForUrl', pattern: 'never', matchType: 'contains', timeout: 50 } as any, createCtx(), env),
    ).rejects.toThrow('TimeoutError');
  });

  it('waitForFunction throws on evaluate error and times out', async () => {
    const env = createMockEnv({ evaluate: async () => { throw new Error('boom'); } });
    await expect(
      executeAction({ action: 'waitForFunction', script: 'return true', timeout: 50, pollingInterval: 10 } as any, createCtx(), env),
    ).rejects.toThrow('ScriptError');

    const env2 = createMockEnv({ evaluate: async () => false });
    await expect(
      executeAction({ action: 'waitForFunction', script: 'return false', timeout: 50, pollingInterval: 10 } as any, createCtx(), env2),
    ).rejects.toThrow('TimeoutError');
  });

  it('waits for text present and times out', async () => {
    document.body.innerHTML = '<div id="x">hello world</div>';
    await executeAction({ action: 'waitForText', target: { selector: '#x' }, text: 'world', timeout: 200 } as any, createCtx(), createMockEnv());
    await expect(
      executeAction({ action: 'waitForText', target: { selector: '#x' }, text: 'missing', timeout: 50 } as any, createCtx(), createMockEnv()),
    ).rejects.toThrow('TimeoutError');
  });

  it('waits for element hidden', async () => {
    document.body.innerHTML = '<div id="x"></div>';
    let calls = 0;
    const env = createMockEnv({
      findElement: async () => {
        calls++;
        return calls > 1 ? null : document.getElementById('x');
      },
    });
    await executeAction({ action: 'waitForElementHidden', target: { selector: '#x' }, timeout: 500 } as any, createCtx(), env);

    const env2 = createMockEnv({ findElement: async () => document.createElement('div') });
    await expect(
      executeAction({ action: 'waitForElementHidden', target: { selector: '#missing' }, timeout: 50 } as any, createCtx(), env2),
    ).rejects.toThrow('TimeoutError');
  });

  it('waits for element visible', async () => {
    document.body.innerHTML = '<div id="x"></div>';
    const el = document.getElementById('x')!;
    el.getBoundingClientRect = () => ({ width: 10, height: 10 }) as DOMRect;
    await executeAction({ action: 'waitForElementVisible', target: { selector: '#x' }, timeout: 200 } as any, createCtx(), createMockEnv());

    document.body.innerHTML = '<div id="y"></div>';
    await expect(
      executeAction({ action: 'waitForElementVisible', target: { selector: '#y' }, timeout: 50 } as any, createCtx(), createMockEnv()),
    ).rejects.toThrow('TimeoutError');

    document.body.innerHTML = '<main id="semantic" style="display:contents"><h1>Guide</h1></main>';
    const heading = document.querySelector('h1')!;
    heading.getBoundingClientRect = () => ({ width: 100, height: 20 }) as DOMRect;
    await executeAction(
      { action: 'waitForElementVisible', target: { selector: 'main' }, timeout: 200 } as any,
      createCtx(),
      createMockEnv(),
    );
  });

  it('waits for element found and not found', async () => {
    document.body.innerHTML = '<div id="x"></div>';
    await executeAction({ action: 'waitFor', target: { selector: '#x' }, timeout: 100 } as any, createCtx(), createMockEnv());
    await expect(
      executeAction({ action: 'waitFor', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv()),
    ).rejects.toThrow('ElementNotFound');
  });

  it('waits for timeout range', async () => {
    const start = Date.now();
    await executeAction({ action: 'waitForTimeout', ms: [10, 15] } as any, createCtx(), createMockEnv());
    expect(Date.now() - start).toBeGreaterThanOrEqual(5);
  });
});

describe('scroll and navigate', () => {
  it('scrolls to bottom step by step', async () => {
    document.body.innerHTML = '<div style="height: 2000px"></div>';
    await executeAction({ action: 'scrollToBottom', stepBy: true } as any, createCtx(), createMockEnv());
  });

  it('scrolls by up and right', async () => {
    await executeAction({ action: 'scrollBy', direction: 'up', distance: 10 } as any, createCtx(), createMockEnv());
    await executeAction({ action: 'scrollBy', direction: 'right', distance: 10 } as any, createCtx(), createMockEnv());
  });

  it('fails when a scroll container cannot be resolved', async () => {
    await expect(executeAction(
      { action: 'scrollBy', target: { selector: '#missing' }, direction: 'down', distance: 10 } as any,
      createCtx(),
      createMockEnv(),
    )).rejects.toThrow('ElementNotFound');
  });

  it('scrolls to top', async () => {
    await executeAction({ action: 'scrollToTop' } as any, createCtx(), createMockEnv());
  });

  it('scrolls into view and to element', async () => {
    document.body.innerHTML = '<div id="x"></div>';
    const el = document.getElementById('x')!;
    el.scrollIntoView = vi.fn() as any;
    await executeAction({ action: 'scrollIntoView', target: { selector: '#x' } } as any, createCtx(), createMockEnv());
    expect(el.scrollIntoView).toHaveBeenCalled();
    await executeAction({ action: 'scrollTo', target: { selector: '#x' }, align: 'start' } as any, createCtx(), createMockEnv());
  });

  it('pageUp and pageDown', async () => {
    await executeAction({ action: 'pageUp', count: 1 } as any, createCtx(), createMockEnv());
    await executeAction({ action: 'pageDown', count: 1 } as any, createCtx(), createMockEnv());
  });

  it('reads with scrollWhileReading', async () => {
    await executeAction({ action: 'readPause', ms: 50, scrollWhileReading: true } as any, createCtx(), createMockEnv());
  });

  it('navigates with waitUntil networkidle', async () => {
    let reads = 0;
    const env = createMockEnv({
      getUrl: () => reads++ === 0 ? 'https://example.com/' : 'https://example.com/other',
    });
    await executeAction(
      { action: 'navigate', url: 'https://example.com/other', waitUntil: 'networkidle' } as any,
      createCtx(),
      env,
    );
  });

  it('fails closed when an explicit navigation never commits', async () => {
    let now = 0;
    const env = createMockEnv({
      getUrl: () => 'https://example.com/',
      now: () => now,
      sleep: async (ms) => { now += ms; },
    });
    await expect(executeAction(
      { action: 'navigate', url: 'https://example.com/other', timeout: 500 } as any,
      createCtx(),
      env,
    )).rejects.toThrow('TimeoutError: navigation did not commit');
  });

  it('reloads page', async () => {
    await executeAction({ action: 'reload' } as any, createCtx(), createMockEnv());
  });
});

describe('output and status actions', () => {
  it('sendResult respects immediate false', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'sendResult', payload: { x: 1 }, immediate: false } as any, createCtx(), env);
    expect(env.transport.sendResult).toHaveBeenCalledWith({ x: 1 }, false);
  });

  it('sendLog increments logsSent', async () => {
    const env = createMockEnv();
    const ctx = createCtx();
    await executeAction({ action: 'sendLog', level: 'info', message: 'hello', extra: { a: 1 } } as any, ctx, env);
    expect(env.transport.sendLog).toHaveBeenCalledWith('info', 'hello', { a: 1 });
    expect(ctx.logsSent).toBe(1);
  });

  it('sendScreenshot with and without target', async () => {
    const env = createMockEnv({
      screenshot: vi.fn(async () => ({ name: 's', type: 'screenshot' as const, data: 'data' })),
      saveSnapshot: vi.fn(async () => ({ name: 's', type: 'screenshot' as const, data: 'data' })),
    });
    document.body.innerHTML = '<div id="x"></div>';
    await executeAction({ action: 'sendScreenshot', target: { selector: '#x' }, name: 's' } as any, createCtx(), env);
    expect(env.saveSnapshot).toHaveBeenCalled();
    expect(env.transport.sendSnapshot).toHaveBeenCalled();

    (env.transport.sendSnapshot as any).mockClear();
    await executeAction({ action: 'sendScreenshot', name: 's2' } as any, createCtx(), env);
    expect(env.screenshot).toHaveBeenCalled();
    expect(env.transport.sendSnapshot).toHaveBeenCalled();
  });

  it('sendHtml with and without target', async () => {
    const env = createMockEnv();
    document.body.innerHTML = '<div id="x"><b>hi</b></div>';
    await executeAction({ action: 'sendHtml', target: { selector: '#x' }, name: 'h1' } as any, createCtx(), env);
    expect(env.transport.sendResult).toHaveBeenCalledWith({ h1: expect.stringContaining('<b>hi</b>') }, true);

    (env.transport.sendResult as any).mockClear();
    await executeAction({ action: 'sendHtml', name: 'h2' } as any, createCtx(), env);
    expect(env.transport.sendResult).toHaveBeenCalledWith({ h2: expect.stringContaining('<body>') }, true);
  });

  it('emitEvent requires event name and dispatches custom event', async () => {
    await expect(executeAction({ action: 'emitEvent' } as any, createCtx(), createMockEnv())).rejects.toThrow('requires a non-empty event name');
    let received: any;
    document.addEventListener('myevent', (e) => { received = (e as CustomEvent).detail; });
    await executeAction({ action: 'emitEvent', event: 'myevent', payload: { ok: true } } as any, createCtx(), createMockEnv());
    expect(received).toEqual({ ok: true });
  });

  it('updateStatus sends status', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'updateStatus', status: 'running', message: 'go' } as any, createCtx(), env);
    expect(env.transport.sendStatus).toHaveBeenCalledWith('running', 'go');
  });

  it('checkpoint saves state', async () => {
    const env = createMockEnv();
    const ctx = createCtx({ extracted: { a: 1 } });
    await executeAction({ action: 'checkpoint', name: 'cp1', preserve: ['extracted'] } as any, ctx, env);
    expect(ctx.checkpoint?.stepId).toBe('cp1');
    expect(ctx.checkpoint?.extracted).toEqual({ a: 1 });
  });

  it('flushResults invokes local transport flush without emitting a row', async () => {
    const env = createMockEnv();
    env.transport.flushResults = vi.fn(async () => {});
    await executeAction({ action: 'flushResults' } as any, createCtx(), env);
    expect(env.transport.flushResults).toHaveBeenCalledOnce();
    expect(env.transport.sendResult).not.toHaveBeenCalled();
  });

  it('heartbeat sends payload', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'heartbeat', payload: { ok: true } } as any, createCtx(), env);
    expect(env.transport.sendHeartbeat).toHaveBeenCalledWith({ ok: true });
  });

  it('saveSnapshot sends snapshot when available', async () => {
    const env = createMockEnv({ saveSnapshot: vi.fn(async () => ({ name: 's', type: 'html' as const, data: '<x>' })) });
    await executeAction({ action: 'saveSnapshot', name: 's', type: 'html' } as any, createCtx(), env);
    expect(env.transport.sendSnapshot).toHaveBeenCalled();
  });

  it('abort with and without flush', async () => {
    const env = createMockEnv();
    await expect(executeAction({ action: 'abort', reason: 'stop', flushBeforeAbort: false } as any, createCtx(), env)).rejects.toThrow('Aborted');
    expect(env.transport.sendResult).not.toHaveBeenCalled();

    const env2 = createMockEnv();
    env2.transport.flushResults = vi.fn(async () => {});
    await expect(executeAction({ action: 'abort', reason: 'stop', flushBeforeAbort: true } as any, createCtx(), env2)).rejects.toThrow('Aborted');
    expect(env2.transport.flushResults).toHaveBeenCalledOnce();
    expect(env2.transport.sendResult).not.toHaveBeenCalled();
  });
});

describe('evaluate', () => {
  it('evaluates script and stores result', async () => {
    const ctx = createCtx();
    const res = await executeAction({ action: 'evaluate', script: 'return 5', name: 'n' } as any, ctx, createMockEnv());
    expect(res).toBe(5);
    expect(ctx.evaluated.n).toBe(5);
  });
});

describe('data operations', () => {
  it('transforms with map and number ops', async () => {
    const ctx = createCtx({ evaluated: { raw: [{ a: '1' }, { a: '2' }] } });
    await executeAction(
      { action: 'transform', from: 'evaluated.raw', name: 'out', operations: [{ type: 'map', params: { rename: { a: 'b' } } }, { type: 'number', field: 'b' }] } as any,
      ctx,
      createMockEnv(),
    );
    expect(ctx.extracted.out).toEqual([{ b: 1 }, { b: 2 }]);

    const ctx2 = createCtx();
    await executeAction({ action: 'transform', from: 'extracted.raw', name: 'num', operations: [{ type: 'number' }] } as any, ctx2, createMockEnv());
    expect(ctx2.extracted.num).toBeNull();
  });

  it('filters with all operators', async () => {
    const data = [{ v: 1 }, { v: 2 }, { v: 3 }, { v: 0 }];
    const ops = [
      { op: 'eq', value: 2, expect: 1 },
      { op: 'ne', value: 2, expect: 3 },
      { op: 'gt', value: 1, expect: 2 },
      { op: 'gte', value: 2, expect: 2 },
      { op: 'lt', value: 2, expect: 2 },
      { op: 'lte', value: 2, expect: 3 },
      { op: 'contains', value: '2', expect: 1 },
      { op: 'notEmpty', value: undefined, expect: 4 },
    ];
    for (const t of ops) {
      const res = await executeAction(
        { action: 'filter', from: 'evaluated.data', name: 'f', criteria: { field: 'v', op: t.op, value: t.value } } as any,
        createCtx({ evaluated: { data } }),
        createMockEnv(),
      );
      expect(res).toHaveLength(t.expect);
    }
  });

  it('filters notEmpty excludes empty values', async () => {
    const res = await executeAction(
      { action: 'filter', from: 'evaluated.data', name: 'f', criteria: { field: 'v', op: 'notEmpty' } } as any,
      createCtx({ evaluated: { data: [{ v: 1 }, { v: '' }, { v: null }, { v: undefined }] } }),
      createMockEnv(),
    );
    expect(res).toHaveLength(1);
  });

  it('interpolates task inputs in filter criteria', async () => {
    const ctx = createCtx({
      evaluated: { data: [{ title: 'Tides' }, { title: 'Waves' }] },
      variables: { target_title: 'Tides' },
    });
    const res = await executeAction(
      {
        action: 'filter', from: 'evaluated.data', name: 'selected',
        criteria: { field: 'title', op: 'eq', value: '{{target_title}}' },
      } as any,
      ctx,
      createMockEnv(),
    );
    expect(res).toEqual([{ title: 'Tides' }]);
    expect(ctx.extracted.selected).toEqual([{ title: 'Tides' }]);
  });

  it('deduplicates by keys', async () => {
    const ctx = createCtx({ evaluated: { data: [{ id: 1, n: 'a' }, { id: 2, n: 'b' }, { id: 1, n: 'c' }] } });
    await executeAction({ action: 'deduplicate', from: 'evaluated.data', name: 'uniq', keys: ['id'] } as any, ctx, createMockEnv());
    expect(ctx.extracted.uniq).toHaveLength(2);
  });

  it('validateData returns data', async () => {
    const ctx = createCtx({ extracted: { data: { ok: true } } });
    const res = await executeAction({ action: 'validateData', from: 'extracted.data' } as any, ctx, createMockEnv());
    expect(res).toEqual({ ok: true });
  });

  it('setTag writes to ctx.tags and announces active set', async () => {
    const env = createMockEnv();
    const ctx = createCtx();
    await executeAction({ action: 'setTag', tags: { source: 'test' } } as any, ctx, env);
    expect(ctx.tags).toEqual({ source: 'test' });
    expect(env.transport.sendLog).toHaveBeenCalledWith('info', 'setTag', { set: { source: 'test' }, active: { source: 'test' } });
  });

  it('setTag accumulates across multiple calls', async () => {
    const env = createMockEnv();
    const ctx = createCtx({ tags: { existing: 'a' } });
    await executeAction({ action: 'setTag', tags: { source: 'test' } } as any, ctx, env);
    expect(ctx.tags).toEqual({ existing: 'a', source: 'test' });
  });

  it('logMetric parses string value', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'logMetric', name: 'latency', value: '42', unit: 'ms' } as any, createCtx(), env);
    expect(env.transport.sendLog).toHaveBeenCalledWith('info', 'metric', { name: 'latency', value: 42, unit: 'ms', tags: undefined });
  });
});

describe('flow control', () => {
  it('switch resolves expression and runs default', async () => {
    const ctx = createCtx({ variables: { mode: 'a' } });
    await runSteps(
      [
        {
          action: 'switch',
          expression: 'variables.mode',
          cases: [{ value: 'b', steps: [{ action: 'evaluate', script: 'window.__sw = 1' }] }],
          default: [{ action: 'evaluate', script: 'window.__sw = 2' }],
        } as any,
      ],
      ctx,
      createMockEnv(),
      executeAction,
    );
    expect((window as any).__sw).toBe(2);
  });

  it('retry succeeds and propagates last error', async () => {
    const env = createMockEnv();
    const ctx = createCtx();
    await executeAction(
      { action: 'retry', id: 'r1', config: { maxAttempts: 1, delay: 10 }, steps: [{ action: 'evaluate', script: 'return true' }] } as any,
      ctx,
      env,
    );

    await expect(
      executeAction(
        { action: 'retry', id: 'r2', config: { maxAttempts: 1, delay: 10 }, steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 0 }] } as any,
        createCtx(),
        env,
      ),
    ).rejects.toThrow('ElementNotFound');
    expect(env.transport.sendLog).toHaveBeenCalledWith('warn', expect.stringContaining('Retrying'), expect.anything());
  });

  it('break and continue throw flow control signals', async () => {
    await expect(executeAction({ action: 'break' } as any, createCtx(), createMockEnv())).rejects.toThrow('break');
    await expect(executeAction({ action: 'continue' } as any, createCtx(), createMockEnv())).rejects.toThrow('continue');
  });

  it('exit rejects invalid status and throws ExitSignal for valid status', async () => {
    await expect(executeAction({ action: 'exit', status: 'invalid' } as any, createCtx(), createMockEnv())).rejects.toThrow('invalid exit status');
    await expect(executeAction({ action: 'exit', status: 'success' } as any, createCtx(), createMockEnv())).rejects.toThrow();
  });

  it('group runs steps and propagates errors', async () => {
    const ctx = createCtx();
    await runSteps([{ action: 'group', steps: [{ action: 'evaluate', script: 'window.__grp = 1' }] } as any], ctx, createMockEnv(), executeAction);
    expect((window as any).__grp).toBe(1);

    // After Phase 3, runSteps returns { error } rather than throwing when a
    // descendant step fails (the inline flow-control dispatcher converts the
    // handler's thrown error into a StepResult).
    const failResult = await runSteps(
      [{ action: 'group', steps: [{ action: 'click', target: { selector: '#missing' }, timeout: 0 }] } as any],
      createCtx(),
      createMockEnv(),
      executeAction,
    );
    expect(failResult.error?.type).toBe('ElementNotFound');
  });
});

describe('ops and recover', () => {
  it('circuit breaker open path', async () => {
    (window as any).__ocCircuitBreakers = { cb1: { open: true, openedAt: Date.now(), failures: [Date.now()] } };
    const env = createMockEnv();
    await expect(executeAction(circuitBreakerAction({ name: 'cb1' }), createCtx(), env)).rejects.toThrow('CircuitBreakerOpen');
    expect(env.transport.sendLog).toHaveBeenCalledWith('warn', expect.stringContaining('OPEN'), expect.anything());
  });

  it('checkQuota fail and skip', async () => {
    const key = 'oc_quota_requestsPerMinute_1';
    localStorage.setItem(key, JSON.stringify({ count: 1, resetAt: Date.now() + 60000 }));
    await expect(executeAction({ action: 'checkQuota', type: 'requestsPerMinute', limit: 1, onExceeded: 'fail' } as any, createCtx(), createMockEnv())).rejects.toThrow('QuotaExceeded');

    localStorage.setItem(key, JSON.stringify({ count: 1, resetAt: Date.now() + 60000 }));
    await executeAction({ action: 'checkQuota', type: 'requestsPerMinute', limit: 1, onExceeded: 'skip' } as any, createCtx(), createMockEnv());
    const stored = JSON.parse(localStorage.getItem(key)!);
    expect(stored.count).toBe(1);
  });

  it('checkQuota wait branch', async () => {
    const key = 'oc_quota_requestsPerMinute_1';
    localStorage.setItem(key, JSON.stringify({ count: 1, resetAt: Date.now() + 50 }));
    await executeAction({ action: 'checkQuota', type: 'requestsPerMinute', limit: 1, onExceeded: 'wait', cooldownMs: 100 } as any, createCtx(), createMockEnv());
  });

  it('requestHuman resumes only after a checkpoint-bound approval and runs then steps', async () => {
    const env = createMockEnv();
    env.transport.requestHuman = vi.fn(async () => ({
      id: 'human-1', checkpointId: 'checkpoint-1', status: 'approved' as const, expiresAt: new Date(Date.now() + 1000).toISOString(),
    }));
    const ctx = createCtx();
    await executeAction({
      action: 'requestHuman', id: 'manual-step', type: 'captcha', prompt: 'solve', timeout: 2000,
      then: [{ action: 'evaluate', script: 'ctx.evaluated.resumed = true' }],
    } as any, ctx, env);
    expect(env.transport.requestHuman).toHaveBeenCalledWith({
      type: 'captcha', prompt: 'solve', timeoutMs: 2000,
      checkpoint: { stepId: 'manual-step', url: 'https://example.com/' },
    });
    expect(ctx.evaluated.resumed).toBe(true);
    expect(env.transport.sendLog).toHaveBeenCalledWith('info', 'human intervention approved', expect.objectContaining({
      interventionId: 'human-1', checkpointId: 'checkpoint-1', stepId: 'manual-step',
    }));
  });

  it('requestHuman applies safe defaults and supports an empty continuation', async () => {
    const env = createMockEnv();
    env.transport.requestHuman = vi.fn(async () => ({
      id: 'human-default', checkpointId: 'checkpoint-default', status: 'approved' as const,
      expiresAt: new Date(Date.now() + 1000).toISOString(),
    }));
    await executeAction({ action: 'requestHuman', type: 'generic' } as any, createCtx(), env);
    expect(env.transport.requestHuman).toHaveBeenCalledWith({
      type: 'generic', prompt: 'human intervention required', timeoutMs: 120000,
      checkpoint: { stepId: 'requestHuman:generic', url: 'https://example.com/' },
    });
  });

  it('requestHuman fails closed for invalid decisions and failed continuation steps', async () => {
    const invalidEnv = createMockEnv();
    invalidEnv.transport.requestHuman = vi.fn(async () => ({
      id: 'human-pending', checkpointId: 'checkpoint-pending', status: 'pending' as const,
      expiresAt: new Date(Date.now() + 1000).toISOString(),
    }));
    await expect(executeAction(
      { action: 'requestHuman', type: 'generic', prompt: 'help' } as any, createCtx(), invalidEnv,
    )).rejects.toThrow('HumanInterventionInvalidStatus: pending');

    const failedThenEnv = createMockEnv();
    failedThenEnv.transport.requestHuman = vi.fn(async () => ({
      id: 'human-then', checkpointId: 'checkpoint-then', status: 'approved' as const,
      expiresAt: new Date(Date.now() + 1000).toISOString(),
    }));
    await expect(executeAction({
      action: 'requestHuman', type: 'generic', prompt: 'help',
      then: [{ action: 'click', target: { selector: '#missing' }, timeout: 10 }],
    } as any, createCtx(), failedThenEnv)).rejects.toThrow('ElementNotFound');
  });

  it('requestHuman fails closed on timeout or an unsupported transport', async () => {
    const timeoutEnv = createMockEnv();
    timeoutEnv.transport.requestHuman = vi.fn(async () => { throw new Error('HumanTimeout: captcha'); });
    await expect(executeAction({ action: 'requestHuman', type: 'captcha', prompt: 'solve', timeout: 20 } as any, createCtx(), timeoutEnv)).rejects.toThrow('HumanTimeout');

    const unsupportedEnv = createMockEnv();
    await expect(executeAction({ action: 'requestHuman', type: 'generic', prompt: 'help' } as any, createCtx(), unsupportedEnv)).rejects.toThrow('HumanInterventionUnavailable');
  });

  it('solveCaptcha falls back to human', async () => {
    const env = createMockEnv();
    env.transport.requestHuman = vi.fn(async () => { throw new Error('HumanTimeout: captcha'); });
    await expect(executeAction({ action: 'solveCaptcha', fallbackToHuman: true, timeout: 20 } as any, createCtx(), env)).rejects.toThrow('HumanTimeout');
  });

  it('recover logs when checkpoint matches and runs steps', async () => {
    const env = createMockEnv();
    const ctx = createCtx({ checkpoint: { stepId: 'cp1', url: '', variables: {}, extracted: {}, timestamp: 0 } });
    await executeAction(
      { action: 'recover', checkpointName: 'cp1', steps: [{ action: 'evaluate', script: 'window.__rec = 1' }] } as any,
      ctx,
      env,
    );
    expect(env.transport.sendLog).toHaveBeenCalledWith('info', expect.stringContaining('Recovering'), expect.anything());
    expect((window as any).__rec).toBe(1);
  });
});

describe('additional branch coverage', () => {
  it('extractText and extractAttribute', async () => {
    document.body.innerHTML = '<div id="x" data-v="7">hello world</div>';
    const ctx = createCtx();
    await executeAction({ action: 'extractText', name: 'txt', target: { selector: '#x' } } as any, ctx, createMockEnv());
    expect(ctx.extracted.txt).toBe('hello world');
    await executeAction({ action: 'extractAttribute', name: 'attr', target: { selector: '#x' }, attr: 'data-v' } as any, ctx, createMockEnv());
    expect(ctx.extracted.attr).toBe('7');
  });

  it('extractField covers remaining branches', async () => {
    document.body.innerHTML = `
      <div id="card">
        <span id="t" style="color: blue">  text  </span>
        <img id="img" src="/img.png" />
        <input id="i" type="text" />
      </div>
    `;
    const ctx = createCtx();
    const res = await executeAction(
      {
        action: 'extract',
        name: 'item',
        target: { selector: '#card' },
        fields: {
          rawText: { type: 'text', selector: '#t', trim: false },
          noNumber: { type: 'number', selector: '#t', default: -1 },
          missingAttr: { type: 'attr', selector: '#t', attr: 'missing', default: 'd' },
          resolvedSrc: { type: 'attr', selector: '#img', attr: 'src', resolve: true },
          color: { type: 'css', selector: '#t', cssProperty: 'color' },
          bool: { type: 'boolean' },
          exists: { type: 'exists', selector: '#img' },
          count: { type: 'count' },
          regexDefault: { type: 'text', selector: '#t', regex: '^\\d+$', default: 'none' },
          custom: { type: 'customType' as any },
          missingSelector: { type: 'text', selector: '.none', default: 'miss' },
        },
      } as any,
      ctx,
      createMockEnv(),
    );
    expect(res.rawText).toBe('  text  ');
    expect(res.noNumber).toBe(-1);
    expect(res.missingAttr).toBe('d');
    expect(res.resolvedSrc).toBe(new URL('/img.png', location.href).href);
    expect(res.color).toBeTruthy();
    expect(res.bool).toBe(true);
    expect(res.exists).toBe(true);
    expect(res.count).toBe(3);
    expect(res.regexDefault).toBe('none');
    expect(res.custom).toBe('text');
    expect(res.missingSelector).toBe('miss');
  });

  it('extractJson throws when element missing', async () => {
    await expect(executeAction({ action: 'extractJson', name: 'j', target: { selector: '#missing' } } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('extractTable with explicit headers excluding header row', async () => {
    document.body.innerHTML = '<table id="t"><thead><tr><th>A</th></tr></thead><tbody><tr><td>1</td></tr></tbody></table>';
    const ctx = createCtx();
    const res = await executeAction(
      { action: 'extractTable', name: 'tbl', target: { selector: '#t' }, headers: { A: 'colA' }, includeHeader: false } as any,
      ctx,
      createMockEnv(),
    );
    expect(res.rows).toEqual([{ colA: '1' }]);
  });

  it('merge zip ignores non-array inputs', async () => {
    const ctx = createCtx({ extracted: { obj: { a: 1 }, arr: [1, 2] } });
    const res = await executeAction(
      { action: 'merge', name: 'm', from: ['extracted.obj', 'extracted.arr'], strategy: 'zip' } as any,
      ctx,
      createMockEnv(),
    );
    expect(res).toEqual([]);
  });

  it('doubleClick, rightClick and middleClick dispatch events', async () => {
    document.body.innerHTML = `
      <div id="dbl">dbl</div>
      <div id="right">right</div>
      <div id="middle">middle</div>
    `;
    const dblEvents: string[] = [];
    document.getElementById('dbl')!.addEventListener('dblclick', () => dblEvents.push('dblclick'));
    await executeAction({ action: 'doubleClick', target: { selector: '#dbl' } } as any, createCtx(), createMockEnv());
    expect(dblEvents).toContain('dblclick');

    const rightEvents: string[] = [];
    document.getElementById('right')!.addEventListener('contextmenu', () => rightEvents.push('contextmenu'));
    await executeAction({ action: 'rightClick', target: { selector: '#right' } } as any, createCtx(), createMockEnv());
    expect(rightEvents).toContain('contextmenu');

    const middleEvents: string[] = [];
    document.getElementById('middle')!.addEventListener('click', (e) => middleEvents.push(`click:${(e as MouseEvent).button}`));
    await executeAction({ action: 'middleClick', target: { selector: '#middle' } } as any, createCtx(), createMockEnv());
    expect(middleEvents.some((m) => m.includes('1'))).toBe(true);
  });

  it('hover with duration array and humanize moveMouse', async () => {
    document.body.innerHTML = '<div id="h">H</div>';
    const events: string[] = [];
    const el = document.getElementById('h')!;
    el.addEventListener('mouseenter', () => events.push('enter'));
    await executeAction(
      { action: 'hover', target: { selector: '#h' }, duration: [5, 10], humanize: { moveMouse: true, randomOffset: 2, preDelay: [1, 2], postDelay: [1, 2] } } as any,
      createCtx(),
      createMockEnv(),
    );
    expect(events).toContain('enter');
  });

  it('hoverClick finds and clicks menu item', async () => {
    document.body.innerHTML = '<div id="hover">H</div><div id="menu">M</div>';
    let clicked = false;
    document.getElementById('menu')!.addEventListener('click', () => { clicked = true; });
    await executeAction(
      { action: 'hoverClick', hoverTarget: { selector: '#hover' }, clickTarget: { selector: '#menu' }, menuAppearTimeout: 100 } as any,
      createCtx(),
      createMockEnv(),
    );
    expect(clicked).toBe(true);
  });

  it('moveMouse with linear and random paths', async () => {
    await executeAction({ action: 'moveMouse', to: { x: 10, y: 10 }, duration: 0, path: 'linear' } as any, createCtx(), createMockEnv());
    await executeAction({ action: 'moveMouse', to: { x: 20, y: 20 }, duration: 0, path: 'random' } as any, createCtx(), createMockEnv());
  });

  it('pressAndHold dispatches mousedown and mouseup', async () => {
    document.body.innerHTML = '<div id="ph">P</div>';
    const events: string[] = [];
    document.getElementById('ph')!.addEventListener('mousedown', () => events.push('down'));
    document.getElementById('ph')!.addEventListener('mouseup', () => events.push('up'));
    await executeAction({ action: 'pressAndHold', target: { selector: '#ph' }, duration: 0 } as any, createCtx(), createMockEnv());
    expect(events).toEqual(['down', 'up']);
  });

  it('dragAndDrop moves element', async () => {
    document.body.innerHTML = '<div id="src">S</div><div id="dst">D</div>';
    await executeAction(
      { action: 'dragAndDrop', source: { selector: '#src' }, target: { selector: '#dst' }, duration: 0, humanize: { wobble: 0 } } as any,
      createCtx(),
      createMockEnv(),
    );
  });

  it('dragBy moves element', async () => {
    document.body.innerHTML = '<div id="src">S</div>';
    await executeAction({ action: 'dragBy', source: { selector: '#src' }, delta: { x: 5, y: 5 }, duration: 0 } as any, createCtx(), createMockEnv());
  });

  it('slides element left', async () => {
    document.body.innerHTML = '<div id="s">S</div>';
    await executeAction({ action: 'slide', target: { selector: '#s' }, direction: 'left', distance: 10, duration: 0 } as any, createCtx(), createMockEnv());
  });

  it('setStyle and removeElement', async () => {
    document.body.innerHTML = '<div id="x"></div>';
    const el = document.getElementById('x') as HTMLElement;
    await executeAction({ action: 'setStyle', target: { selector: '#x' }, style: { color: 'red' } } as any, createCtx(), createMockEnv());
    expect(el.style.color).toBe('red');
    await executeAction({ action: 'removeElement', target: { selector: '#x' } } as any, createCtx(), createMockEnv());
    expect(document.getElementById('x')).toBeNull();
  });

  it('scrollToBottom non-stepBy', async () => {
    await executeAction({ action: 'scrollToBottom' } as any, createCtx(), createMockEnv());
  });

  it('readPause without scrolling', async () => {
    const start = Date.now();
    await executeAction({ action: 'readPause', ms: 10 } as any, createCtx(), createMockEnv());
    expect(Date.now() - start).toBeGreaterThanOrEqual(5);
  });

  it('navigates with waitUntil load', async () => {
    let reads = 0;
    const env = createMockEnv({
      getUrl: () => reads++ === 0 ? 'https://example.com/' : 'https://example.com/page',
    });
    await executeAction(
      { action: 'navigate', url: 'https://example.com/page', waitUntil: 'load' } as any,
      createCtx(),
      env,
    );
  });

  it('sleeps with number and array', async () => {
    const start = Date.now();
    await executeAction({ action: 'sleep', ms: [5, 8] } as any, createCtx(), createMockEnv());
    expect(Date.now() - start).toBeGreaterThanOrEqual(2);
    await executeAction({ action: 'sleep', ms: 5 } as any, createCtx(), createMockEnv());
  });

  it('evaluates with args and without name', async () => {
    const res = await executeAction({ action: 'evaluate', script: 'return args[0]', args: ['ok'] } as any, createCtx(), createMockEnv());
    expect(res).toBe('ok');
  });

  it('sendResult defaults to immediate true', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'sendResult', payload: { x: 1 } } as any, createCtx(), env);
    expect(env.transport.sendResult).toHaveBeenCalledWith({ x: 1 }, true);
  });

  it('sendLog without extra', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'sendLog', level: 'warn', message: 'msg' } as any, createCtx(), env);
    expect(env.transport.sendLog).toHaveBeenCalledWith('warn', 'msg', {});
  });

  it('sendHtml respects immediate false', async () => {
    const env = createMockEnv();
    document.body.innerHTML = '<div id="x"></div>';
    await executeAction({ action: 'sendHtml', target: { selector: '#x' }, name: 'h', immediate: false } as any, createCtx(), env);
    expect(env.transport.sendResult).toHaveBeenCalledWith(expect.anything(), false);
  });

  it('sendScreenshot without target sends snapshot when available', async () => {
    const env = createMockEnv({ screenshot: vi.fn(async () => ({ name: 's', type: 'screenshot' as const, data: 'data' })) });
    await executeAction({ action: 'sendScreenshot', name: 's' } as any, createCtx(), env);
    expect(env.screenshot).toHaveBeenCalled();
    expect(env.transport.sendSnapshot).toHaveBeenCalled();
  });

  it('saveSnapshot does not send when snapshot is null', async () => {
    const env = createMockEnv({ saveSnapshot: vi.fn(async () => null) });
    await executeAction({ action: 'saveSnapshot', name: 's', type: 'html' } as any, createCtx(), env);
    expect(env.transport.sendSnapshot).not.toHaveBeenCalled();
  });

  it('heartbeat without payload', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'heartbeat' } as any, createCtx(), env);
    expect(env.transport.sendHeartbeat).toHaveBeenCalledWith({});
  });

  it('focus, clear, select, check, selectRadio and paste throw when element missing', async () => {
    await expect(executeAction({ action: 'focus', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'clear', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'select', target: { selector: '#missing' }, timeout: 0, value: 'x' } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'check', target: { selector: '#missing' }, timeout: 0, state: true } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'selectRadio', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'paste', target: { selector: '#missing' }, timeout: 0, value: 'x' } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('switch matches a case', async () => {
    const ctx = createCtx({ variables: { mode: 'a' } });
    await runSteps(
      [
        {
          action: 'switch',
          expression: '{{variables.mode}}',
          cases: [{ value: 'a', steps: [{ action: 'evaluate', script: 'window.__sw2 = 3' }] }],
          default: [{ action: 'evaluate', script: 'window.__sw2 = 4' }],
        } as any,
      ],
      ctx,
      createMockEnv(),
      executeAction,
    );
    expect((window as any).__sw2).toBe(3);
  });

  it('circuit breaker closed path', async () => {
    await expect(executeAction(circuitBreakerAction({ name: 'cb2' }), createCtx(), createMockEnv())).resolves.toBeUndefined();
  });

  it('checkQuota resets expired window', async () => {
    const key = 'oc_quota_requestsPerMinute_1';
    localStorage.setItem(key, JSON.stringify({ count: 5, resetAt: Date.now() - 10 }));
    await executeAction({ action: 'checkQuota', type: 'requestsPerMinute', limit: 1, onExceeded: 'fail', cooldownMs: 100 } as any, createCtx(), createMockEnv());
    const stored = JSON.parse(localStorage.getItem(key)!);
    expect(stored.count).toBe(1);
  });

  it('recover without checkpointName runs steps', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'recover', steps: [{ action: 'evaluate', script: 'window.__rec2 = 2' }] } as any, createCtx(), env);
    expect(env.transport.sendLog).not.toHaveBeenCalled();
    expect((window as any).__rec2).toBe(2);
  });

  it('recover with non-matching checkpoint does not log recovery', async () => {
    const env = createMockEnv();
    const ctx = createCtx({ checkpoint: { stepId: 'cpA', url: '', variables: {}, extracted: {}, timestamp: 0 } });
    await executeAction({ action: 'recover', checkpointName: 'cpB', steps: [{ action: 'evaluate', script: 'window.__rec3 = 3' }] } as any, ctx, env);
    expect(env.transport.sendLog).not.toHaveBeenCalled();
    expect((window as any).__rec3).toBe(3);
  });

  it('filter default op returns true and handles non-array input', async () => {
    const ctx = createCtx({ extracted: { data: 'not-array' } });
    const res = await executeAction({ action: 'filter', from: 'extracted.data', name: 'f', criteria: { field: 'x', op: 'unknown' } } as any, ctx, createMockEnv());
    expect(res).toEqual([]);
  });

  it('transform scalar number', async () => {
    const ctx = createCtx({ extracted: { value: '42.5' } });
    await executeAction({ action: 'transform', from: 'extracted.value', name: 'num', operations: [{ type: 'number' }] } as any, ctx, createMockEnv());
    expect(ctx.extracted.num).toBe(42.5);
  });

  it('logMetric accepts number value', async () => {
    const env = createMockEnv();
    await executeAction({ action: 'logMetric', name: 'm', value: 7 } as any, createCtx(), env);
    expect(env.transport.sendLog).toHaveBeenCalledWith('info', 'metric', { name: 'm', value: 7, unit: undefined, tags: undefined });
  });

  it('exit defaults status to success', async () => {
    await expect(executeAction({ action: 'exit' } as any, createCtx(), createMockEnv())).rejects.toThrow();
  });

  it('pageDown without count uses default', async () => {
    await executeAction({ action: 'pageDown' } as any, createCtx(), createMockEnv());
  });

  it('extractText and extractAttribute return defaults when element missing', async () => {
    const ctx1 = createCtx();
    await executeAction({ action: 'extractText', name: 't', target: { selector: '#missing' }, timeout: 0 } as any, ctx1, createMockEnv());
    expect(ctx1.extracted.t).toBe('');
    const ctx2 = createCtx();
    await executeAction({ action: 'extractAttribute', name: 'a', target: { selector: '#missing' }, attr: 'x', timeout: 0 } as any, ctx2, createMockEnv());
    expect(ctx2.extracted.a).toBeNull();
  });

  it('fails visible extractText and extractAttribute when the resolved target is missing', async () => {
    const ctx = createCtx({ variables: { __selectors: {}, id: 'missing' } });
    const target = { selector: '#{{variables.id}}', visible: true };
    await expect(
      executeAction({ action: 'extractText', name: 'text', target } as any, ctx, createMockEnv()),
    ).rejects.toThrow('ElementNotFound: {"selector":"#missing","visible":true}');
    await expect(
      executeAction({ action: 'extractAttribute', name: 'attr', target, attr: 'href' } as any, ctx, createMockEnv()),
    ).rejects.toThrow('ElementNotFound: {"selector":"#missing","visible":true}');
  });

  it('honors extract onEmpty fail and fails a missing visible singular target', async () => {
    await expect(executeAction({
      action: 'extract',
      name: 'single',
      target: { selector: '#missing' },
      fields: { text: { type: 'text' } },
      onEmpty: 'fail',
    } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');

    await expect(executeAction({
      action: 'extract',
      name: 'many',
      target: { selector: '.missing' },
      multiple: true,
      fields: { text: { type: 'text' } },
      onEmpty: 'fail',
    } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');

    await expect(executeAction({
      action: 'extract',
      name: 'visible',
      target: { selector: '#missing', visible: true },
      fields: { text: { type: 'text' } },
    } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('preserves legacy empty extract defaults without visible or onEmpty fail', async () => {
    const ctx = createCtx();
    await expect(executeAction({
      action: 'extract',
      name: 'single',
      target: { selector: '#missing' },
      fields: { text: { type: 'text' } },
    } as any, ctx, createMockEnv())).resolves.toBeUndefined();
    await expect(executeAction({
      action: 'extract',
      name: 'many',
      target: { selector: '.missing' },
      multiple: true,
      fields: { text: { type: 'text' } },
    } as any, ctx, createMockEnv())).resolves.toEqual([]);
  });

  it('fails a visible provider field on a hidden live descendant without using its default', async () => {
    document.body.innerHTML = '<article id="row"><div hidden><span class="secret">live secret</span></div></article>';
    const row = document.getElementById('row')!;
    const secret = document.querySelector('.secret')!;
    secret.getBoundingClientRect = () => ({ width: 100, height: 20 } as DOMRect);

    await expect(executeAction({
      action: 'extract',
      name: 'rows',
      target: { selector: '#row' },
      fields: {
        text: {
          type: 'text',
          selector: '.secret',
          visible: true,
          default: 'fabricated fallback',
        },
      },
    } as any, createCtx(), createMockEnv({ findElement: async () => row })))
      .rejects.toThrow('ElementNotFound: {"selector":".secret","visible":true}');
  });

  it('enforces visible on structural provider fields before recursing', async () => {
    document.body.innerHTML = '<article id="row"><div class="hidden-object" aria-hidden="true"><span>secret</span></div></article>';
    const row = document.getElementById('row')!;
    const hiddenObject = document.querySelector('.hidden-object')!;
    hiddenObject.getBoundingClientRect = () => ({ width: 100, height: 20 } as DOMRect);

    await expect(executeAction({
      action: 'extract',
      name: 'rows',
      target: { selector: '#row' },
      fields: {
        object: {
          type: 'exists',
          selector: '.hidden-object',
          visible: true,
          fields: { text: { type: 'text', visible: true } },
        },
      },
    } as any, createCtx(), createMockEnv({ findElement: async () => row })))
      .rejects.toThrow('ElementNotFound: {"selector":".hidden-object","visible":true}');
  });

  it('leaves legacy hidden-field extraction unchanged when visible is omitted', async () => {
    document.body.innerHTML = '<article id="row"><div hidden><span class="secret">legacy value</span></div></article>';
    const row = document.getElementById('row')!;
    const ctx = createCtx();

    await expect(executeAction({
      action: 'extract',
      name: 'row',
      target: { selector: '#row' },
      fields: { text: { type: 'text', selector: '.secret' } },
    } as any, ctx, createMockEnv({ findElement: async () => row }))).resolves.toEqual({
      text: 'legacy value',
    });
  });

  it('passes configured and default timeouts to extract element lookup', async () => {
    const findElement = vi.fn(async () => null);
    const findElements = vi.fn(async () => []);
    await executeAction({
      action: 'extract',
      name: 'single',
      target: { selector: '#missing' },
      fields: { text: { type: 'text' } },
      timeout: 321,
    } as any, createCtx(), createMockEnv({ findElement }));
    await executeAction({
      action: 'extract',
      name: 'many',
      target: { selector: '.missing' },
      multiple: true,
      fields: { text: { type: 'text' } },
    } as any, createCtx(), createMockEnv({ findElements }));

    expect(findElement).toHaveBeenCalledWith({ selector: '#missing' }, 321);
    expect(findElements).toHaveBeenCalledWith({ selector: '.missing' }, 5000);
  });

  it('mouse actions throw when element missing', async () => {
    await expect(executeAction({ action: 'doubleClick', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'rightClick', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'middleClick', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'hover', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'pressAndHold', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'slide', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'moveMouse', to: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('dragAndDrop and dragBy throw when source missing', async () => {
    await expect(executeAction({ action: 'dragAndDrop', source: { selector: '#missing' }, target: { selector: '#x' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'dragBy', source: { selector: '#missing' }, delta: { x: 1, y: 1 }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('scrollTo and scrollIntoView throw when element missing', async () => {
    await expect(executeAction({ action: 'scrollTo', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'scrollIntoView', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('setStyle and removeElement throw when element missing', async () => {
    await expect(executeAction({ action: 'setStyle', target: { selector: '#missing' }, style: {}, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'removeElement', target: { selector: '#missing' }, timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('typeAndSelect and uploadFile throw when element missing', async () => {
    await expect(executeAction({ action: 'typeAndSelect', target: { selector: '#missing' }, value: 'x', suggestionSelector: '.s', timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    await expect(executeAction({ action: 'uploadFile', target: { selector: '#missing' }, files: ['x'], timeout: 0 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('hoverClick throws when hover or click target missing', async () => {
    await expect(executeAction({ action: 'hoverClick', hoverTarget: { selector: '#missing' }, clickTarget: { selector: '#x' }, timeout: 0, menuAppearTimeout: 50 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
    document.body.innerHTML = '<div id="hover">H</div>';
    await expect(executeAction({ action: 'hoverClick', hoverTarget: { selector: '#hover' }, clickTarget: { selector: '#missing' }, timeout: 0, menuAppearTimeout: 50 } as any, createCtx(), createMockEnv())).rejects.toThrow('ElementNotFound');
  });

  it('type appends value when append true', async () => {
    document.body.innerHTML = '<input id="i" value="ab" />';
    await executeAction({ action: 'type', target: { selector: '#i' }, value: 'cd', append: true } as any, createCtx(), createMockEnv());
    expect((document.getElementById('i') as HTMLInputElement).value).toBe('abcd');
  });

  it('waitForTimeout with number', async () => {
    const start = Date.now();
    await executeAction({ action: 'waitForTimeout', ms: 10 } as any, createCtx(), createMockEnv());
    expect(Date.now() - start).toBeGreaterThanOrEqual(5);
  });

  it('sendHtml with default immediate true', async () => {
    const env = createMockEnv();
    document.body.innerHTML = '<div id="x"></div>';
    await executeAction({ action: 'sendHtml', target: { selector: '#x' }, name: 'h' } as any, createCtx(), env);
    expect(env.transport.sendResult).toHaveBeenCalledWith(expect.anything(), true);
  });

  it('sendScreenshot target with null snapshot does not send', async () => {
    const env = createMockEnv({ saveSnapshot: vi.fn(async () => null) });
    document.body.innerHTML = '<div id="x"></div>';
    await executeAction({ action: 'sendScreenshot', target: { selector: '#x' }, name: 's' } as any, createCtx(), env);
    expect(env.transport.sendSnapshot).not.toHaveBeenCalled();
  });

  it('moveMouse to element with duration array', async () => {
    document.body.innerHTML = '<div id="x"></div>';
    await executeAction({ action: 'moveMouse', to: { selector: '#x' }, duration: [1, 2] } as any, createCtx(), createMockEnv());
  });

  it('pressAndHold with duration array', async () => {
    document.body.innerHTML = '<div id="x">X</div>';
    await executeAction({ action: 'pressAndHold', target: { selector: '#x' }, duration: [1, 2] } as any, createCtx(), createMockEnv());
  });

  it('dragAndDrop with duration array and wobble', async () => {
    document.body.innerHTML = '<div id="src">S</div><div id="dst">D</div>';
    await executeAction(
      { action: 'dragAndDrop', source: { selector: '#src' }, target: { selector: '#dst' }, duration: [1, 2], humanize: { wobble: 2 } } as any,
      createCtx(),
      createMockEnv(),
    );
  });

  it('dragBy with duration array and wobble', async () => {
    document.body.innerHTML = '<div id="src">S</div>';
    await executeAction({ action: 'dragBy', source: { selector: '#src' }, delta: { x: 2, y: 2 }, duration: [1, 2], humanize: { wobble: 2 } } as any, createCtx(), createMockEnv());
  });

  it('slide up', async () => {
    document.body.innerHTML = '<div id="s">S</div>';
    await executeAction({ action: 'slide', target: { selector: '#s' }, direction: 'up', distance: 5, duration: 0 } as any, createCtx(), createMockEnv());
  });

  it('switch with literal expression', async () => {
    const ctx = createCtx();
    await runSteps(
      [{ action: 'switch', expression: 'lit', cases: [{ value: 'lit', steps: [{ action: 'evaluate', script: 'window.__sw3 = 1' }] }] } as any],
      ctx,
      createMockEnv(),
      executeAction,
    );
    expect((window as any).__sw3).toBe(1);
  });

  it('transform map without rename', async () => {
    const ctx = createCtx({ evaluated: { data: [{ a: 1 }] } });
    await executeAction({ action: 'transform', from: 'evaluated.data', name: 'out', operations: [{ type: 'map' }] } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ a: 1 }]);
  });

  it('filter without field uses whole item', async () => {
    const ctx = createCtx({ evaluated: { data: [1, 2, 3] } });
    const res = await executeAction({ action: 'filter', from: 'evaluated.data', name: 'f', criteria: { op: 'gt', value: 1 } } as any, ctx, createMockEnv());
    expect(res).toEqual([2, 3]);
  });

  it('circuit breaker with stored closed state', async () => {
    (window as any).__ocCircuitBreakers = { cb3: { open: false, openedAt: 0, failures: [] } };
    await expect(executeAction(circuitBreakerAction({ name: 'cb3' }), createCtx(), createMockEnv())).resolves.toBeUndefined();
  });

  it('checkQuota first usage stores window', async () => {
    await executeAction({ action: 'checkQuota', type: 'requestsPerMinute', limit: 5 } as any, createCtx(), createMockEnv());
    const key = 'oc_quota_requestsPerMinute_5';
    const stored = JSON.parse(localStorage.getItem(key)!);
    expect(stored.count).toBe(1);
    expect(stored.resetAt).toBeGreaterThan(Date.now());
  });
});

describe('circuit breaker counter', () => {
  beforeEach(() => {
    delete (window as any).__ocCircuitBreakers;
  });

  it('record:true increments failure counter and persists to window', async () => {
    const action = {
      action: 'circuitBreaker',
      name: 'cb-count',
      failureThreshold: 3,
      windowMs: 60_000,
      cooldownMs: 60_000,
      record: true,
    } satisfies CircuitBreakerAction;
    await executeAction(action, createCtx(), createMockEnv());
    await executeAction(action, createCtx(), createMockEnv());
    const stored = (window as any).__ocCircuitBreakers['cb-count'];
    expect(stored.failures.length).toBe(2);
    expect(stored.open).toBe(false);
    expect(stored.halfOpen).toBe(false);
  });

  it('reaches threshold opens breaker and runs onOpen', async () => {
    (window as any).__ocCircuitBreakers = {
      'cb-thresh': { open: false, openedAt: 0, failures: [Date.now(), Date.now()] },
    };
    const env = createMockEnv();
    const action = {
      action: 'circuitBreaker',
      name: 'cb-thresh',
      failureThreshold: 3,
      windowMs: 60_000,
      cooldownMs: 60_000,
      record: true,
      onOpen: [{ action: 'evaluate', script: 'window.__cbOpened = true' }],
    } satisfies CircuitBreakerAction;
    await expect(executeAction(
      action,
      createCtx(),
      env,
    )).rejects.toThrow('CircuitBreakerOpen');
    expect((window as any).__ocCircuitBreakers['cb-thresh'].open).toBe(true);
    expect((window as any).__ocCircuitBreakers['cb-thresh'].openedAt).toBeGreaterThan(0);
    expect((window as any).__cbOpened).toBe(true);
    expect(env.transport.sendLog).toHaveBeenCalledWith('warn', expect.stringContaining('OPENED'), expect.anything());
  });

  it('sliding window prunes failures older than windowMs', async () => {
    const old = Date.now() - 120000; // outside 60s window
    const fresh = Date.now() - 1000;
    (window as any).__ocCircuitBreakers = {
      'cb-win': { open: false, openedAt: 0, failures: [old, old, fresh] },
    };
    await executeAction(
      circuitBreakerAction({ name: 'cb-win', failureThreshold: 3, record: true }),
      createCtx(),
      createMockEnv(),
    );
    // After prune: 1 fresh + 1 new = 2; below threshold, stays closed
    const stored = (window as any).__ocCircuitBreakers['cb-win'];
    expect(stored.failures.length).toBe(2);
    expect(stored.open).toBe(false);
  });

  it('admits one half-open probe after cooldown without closing early', async () => {
    const now = Date.now();
    const past = now - 120_000;
    (window as any).__ocCircuitBreakers = {
      'cb-half': { open: true, openedAt: past, failures: [past, past, past] },
    };
    const env = createMockEnv({ now: () => now });
    await expect(executeAction(
      circuitBreakerAction({ name: 'cb-half', failureThreshold: 3 }),
      createCtx(),
      env,
    )).resolves.toBeUndefined();
    const stored = (window as any).__ocCircuitBreakers['cb-half'];
    expect(stored.open).toBe(false);
    expect(stored.halfOpen).toBe(true);
    expect(stored.openedAt).toBe(0);
    expect(stored.probeStartedAt).toBe(now);
    expect(stored.failures.length).toBe(0);
  });

  it('half-open probe failure reopens immediately below the normal threshold', async () => {
    const now = Date.now();
    const past = now - 120_000;
    (window as any).__ocCircuitBreakers = {
      'cb-reopen': { open: true, openedAt: past, failures: [past, past, past] },
    };
    const env = createMockEnv({ now: () => now });
    const gate = circuitBreakerAction({
      name: 'cb-reopen',
      failureThreshold: 5,
      onOpen: [{ action: 'evaluate', script: 'window.__cbReopened = true' }],
    });
    await executeAction(gate, createCtx(), env);

    const failure = { ...gate, record: true } satisfies CircuitBreakerAction;
    await expect(executeAction(
      failure,
      createCtx(),
      env,
    )).rejects.toThrow('CircuitBreakerOpen');
    const stored = (window as any).__ocCircuitBreakers['cb-reopen'];
    expect(stored.open).toBe(true);
    expect(stored.halfOpen).toBe(false);
    expect(stored.openedAt).toBe(now);
    expect(stored.failures.length).toBe(1);
    expect((window as any).__cbReopened).toBe(true);
    expect(env.transport.sendLog).toHaveBeenCalledWith(
      'warn',
      expect.stringContaining('REOPENED'),
      expect.anything(),
    );
  });

  it('record:false explicitly closes a successful half-open probe', async () => {
    const now = Date.now();
    const past = now - 120_000;
    (window as any).__ocCircuitBreakers = {
      'cb-success': { open: true, openedAt: past, failures: [past, past, past] },
    };
    const env = createMockEnv({ now: () => now });
    const gate = circuitBreakerAction({ name: 'cb-success', failureThreshold: 3 });
    await executeAction(gate, createCtx(), env);

    const success = { ...gate, record: false } satisfies CircuitBreakerAction;
    await executeAction(success, createCtx(), env);

    expect((window as any).__ocCircuitBreakers['cb-success']).toEqual({
      failures: [],
      open: false,
      openedAt: 0,
      halfOpen: false,
      probeStartedAt: 0,
    });
  });

  it('next sequential gate infers probe success for legacy rules', async () => {
    const now = Date.now();
    const past = now - 120_000;
    (window as any).__ocCircuitBreakers = {
      'cb-legacy-gate': { open: true, openedAt: past, failures: [past] },
    };
    const env = createMockEnv({ now: () => now });
    const gate = circuitBreakerAction({ name: 'cb-legacy-gate' });

    await executeAction(gate, createCtx(), env);
    expect((window as any).__ocCircuitBreakers['cb-legacy-gate'].halfOpen).toBe(true);

    await executeAction(gate, createCtx(), env);
    expect((window as any).__ocCircuitBreakers['cb-legacy-gate'].halfOpen).toBe(false);
  });

  it('normalizes the pre-sliding-window numeric counter state', async () => {
    const now = Date.now();
    (window as any).__ocCircuitBreakers = {
      'cb-legacy-state': { open: false, failures: 2, lastFailure: now - 1_000 },
    };
    const action = circuitBreakerAction({
      name: 'cb-legacy-state',
      failureThreshold: 3,
      record: true,
    });

    await expect(executeAction(
      action,
      createCtx(),
      createMockEnv({ now: () => now }),
    )).rejects.toThrow('CircuitBreakerOpen');
    expect((window as any).__ocCircuitBreakers['cb-legacy-state'].failures).toHaveLength(3);
  });
});

describe('transform op types', () => {
  it('map renames fields via params.rename', async () => {
    const ctx = createCtx({ evaluated: { data: [{ a: 1, b: 2 }] } });
    await executeAction({
      action: 'transform', from: 'evaluated.data', name: 'out',
      operations: [{ type: 'map', params: { rename: { a: 'x' } } }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ x: 1, b: 2 }]);
  });

  it('number coerces numeric strings (scalar + array + field)', async () => {
    const ctx = createCtx({ evaluated: { arr: [{ p: '$10' }], scalar: '$42' } });
    await executeAction({
      action: 'transform', from: 'evaluated.arr', name: 'a',
      operations: [{ type: 'number', field: 'p' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.a).toEqual([{ p: 10 }]);
    await executeAction({
      action: 'transform', from: 'evaluated.scalar', name: 's',
      operations: [{ type: 'number' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.s).toBe(42);
  });

  it('filter op uses params.criteria to narrow array', async () => {
    const ctx = createCtx({ evaluated: { data: [{ n: 1 }, { n: 5 }, { n: 10 }] } });
    await executeAction({
      action: 'transform', from: 'evaluated.data', name: 'out',
      operations: [{ type: 'filter', params: { criteria: { field: 'n', op: 'gte', value: 5 } } }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ n: 5 }, { n: 10 }]);
  });

  it('regex op extracts capture group from string', async () => {
    const ctx = createCtx({ evaluated: { code: 'Order #ABC-123 done' } });
    await executeAction({
      action: 'transform', from: 'evaluated.code', name: 'out',
      operations: [{ type: 'regex', params: { pattern: /#([A-Z]+-\d+)/, group: 1 } as any }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('ABC-123');
  });

  it('regex op returns original when no match', async () => {
    const ctx = createCtx({ evaluated: { code: 'no match here' } });
    await executeAction({
      action: 'transform', from: 'evaluated.code', name: 'out',
      operations: [{ type: 'regex', params: { pattern: /\d+/ } as any }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('no match here');
  });

  it('replace op substitutes via regexp', async () => {
    const ctx = createCtx({ evaluated: { text: 'hello world' } });
    await executeAction({
      action: 'transform', from: 'evaluated.text', name: 'out',
      operations: [{ type: 'replace', params: { pattern: 'world', replacement: 'there' } }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('hello there');
  });

  it('trim op strips whitespace from string', async () => {
    const ctx = createCtx({ evaluated: { text: '  hi  ' } });
    await executeAction({
      action: 'transform', from: 'evaluated.text', name: 'out',
      operations: [{ type: 'trim' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('hi');
  });

  it('date op parses to ISO string', async () => {
    const ctx = createCtx({ evaluated: { d: '2024-01-15T10:30:00Z' } });
    await executeAction({
      action: 'transform', from: 'evaluated.d', name: 'out',
      operations: [{ type: 'date' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('2024-01-15T10:30:00.000Z');
  });

  it('date op leaves invalid date unchanged', async () => {
    const ctx = createCtx({ evaluated: { d: 'not-a-date' } });
    await executeAction({
      action: 'transform', from: 'evaluated.d', name: 'out',
      operations: [{ type: 'date' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('not-a-date');
  });

  it('jsonParse op parses JSON string', async () => {
    const ctx = createCtx({ evaluated: { raw: '{"a":1,"b":[2,3]}' } });
    await executeAction({
      action: 'transform', from: 'evaluated.raw', name: 'out',
      operations: [{ type: 'jsonParse' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual({ a: 1, b: [2, 3] });
  });

  it('jsonParse op leaves invalid JSON unchanged', async () => {
    const ctx = createCtx({ evaluated: { raw: '{not json' } });
    await executeAction({
      action: 'transform', from: 'evaluated.raw', name: 'out',
      operations: [{ type: 'jsonParse' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toBe('{not json');
  });

  it('custom op is a no-op (scripted transforms out of scope in MV3)', async () => {
    const ctx = createCtx({ evaluated: { data: [{ x: 1 }] } });
    await executeAction({
      action: 'transform', from: 'evaluated.data', name: 'out',
      operations: [{ type: 'custom', script: 'data.map(x => x * 2)' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ x: 1 }]);
  });

  it('field-scoped ops apply to nested field in array', async () => {
    const ctx = createCtx({ evaluated: { items: [{ q: '  a  ' }, { q: '  b  ' }] } });
    await executeAction({
      action: 'transform', from: 'evaluated.items', name: 'out',
      operations: [{ type: 'trim', field: 'q' }],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ q: 'a' }, { q: 'b' }]);
  });

  it('chained ops compose map → filter → number', async () => {
    const ctx = createCtx({ evaluated: { items: [{ p: '$1' }, { p: '$2' }, { p: '$3' }] } });
    await executeAction({
      action: 'transform', from: 'evaluated.items', name: 'out',
      operations: [
        { type: 'number', field: 'p' },
        { type: 'filter', params: { criteria: { field: 'p', op: 'gte', value: 2 } } },
      ],
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ p: 2 }, { p: 3 }]);
  });
});

describe('filter op types', () => {
  it('matches op applies regex against value', async () => {
    const ctx = createCtx({ evaluated: { items: ['foo@example.com', 'nope', 'bar@example.com'] } });
    await executeAction({
      action: 'filter', from: 'evaluated.items', name: 'out',
      criteria: { op: 'matches', value: '@example\\.com$' },
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual(['foo@example.com', 'bar@example.com']);
  });

  it('matches op returns no match on invalid regex', async () => {
    const ctx = createCtx({ evaluated: { items: ['abc'] } });
    await executeAction({
      action: 'filter', from: 'evaluated.items', name: 'out',
      criteria: { op: 'matches', value: '(' },
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([]);
  });

  it('exists op filters items where field is non-null', async () => {
    const ctx = createCtx({ evaluated: { items: [{ id: 1, name: 'a' }, { id: 2 }, { id: 3, name: 'c' }] } });
    await executeAction({
      action: 'filter', from: 'evaluated.items', name: 'out',
      criteria: { field: 'name', op: 'exists' },
    } as any, ctx, createMockEnv());
    expect(ctx.extracted.out).toEqual([{ id: 1, name: 'a' }, { id: 3, name: 'c' }]);
  });

  it('exists op returns false for null-ish field values', async () => {
    const ctx = createCtx({ evaluated: { items: [{ a: null }, { a: 0 }, { a: '' }] } });
    await executeAction({
      action: 'filter', from: 'evaluated.items', name: 'out',
      criteria: { field: 'a', op: 'exists' },
    } as any, ctx, createMockEnv());
    // null → false; 0 and '' are not null/undefined → true
    expect(ctx.extracted.out).toEqual([{ a: 0 }, { a: '' }]);
  });
});

describe('unsupported action', () => {
  it('throws for unsupported action type', async () => {
    await expect(executeAction({ action: 'noSuchAction' } as any, createCtx(), createMockEnv())).rejects.toThrow('Unsupported action');
  });
});

describe('goBack / goForward / setViewport actions', () => {
  it('goBack: invokes env.goBack()', async () => {
    const goBack = vi.fn(async () => {});
    const env = createMockEnv({ goBack } as any);
    await executeAction({ action: 'goBack' } as any, createCtx(), env);
    expect(goBack).toHaveBeenCalledTimes(1);
  });

  it('goBack: throws UnsupportedInEnvironment when env.goBack absent', async () => {
    const env = createMockEnv();
    await expect(
      executeAction({ action: 'goBack' } as any, createCtx(), env),
    ).rejects.toThrow(/UnsupportedInEnvironment/);
  });

  it('goForward: invokes env.goForward()', async () => {
    const goForward = vi.fn(async () => {});
    const env = createMockEnv({ goForward } as any);
    await executeAction({ action: 'goForward' } as any, createCtx(), env);
    expect(goForward).toHaveBeenCalledTimes(1);
  });

  it('setViewport: passes all provided fields to env.setViewport', async () => {
    const setViewport = vi.fn(async () => {});
    const env = createMockEnv({ setViewport } as any);
    await executeAction({
      action: 'setViewport',
      width: 1280, height: 720,
      deviceScaleFactor: 2, isMobile: true, hasTouch: true, userAgent: 'X',
    } as any, createCtx(), env);
    expect(setViewport).toHaveBeenCalledWith({
      width: 1280, height: 720,
      deviceScaleFactor: 2, isMobile: true, hasTouch: true, userAgent: 'X',
    });
  });

  it('setViewport: passes undefined optional fields as-is (env destructures with defaults)', async () => {
    const setViewport = vi.fn(async () => {});
    const env = createMockEnv({ setViewport } as any);
    await executeAction({
      action: 'setViewport', width: 100, height: 100,
    } as any, createCtx(), env);
    expect(setViewport).toHaveBeenCalledWith({
      width: 100, height: 100,
      deviceScaleFactor: undefined, isMobile: undefined, hasTouch: undefined, userAgent: undefined,
    });
  });

  it('setViewport: throws UnsupportedInEnvironment when absent', async () => {
    const env = createMockEnv();
    await expect(
      executeAction({ action: 'setViewport', width: 1, height: 1 } as any, createCtx(), env),
    ).rejects.toThrow(/UnsupportedInEnvironment/);
  });
});

describe('waitForNetworkIdle action', () => {
  it('uses defaults idleTime=500 timeout=30000 when omitted', async () => {
    const waitForNetworkIdle = vi.fn(async () => {});
    const env = createMockEnv({ waitForNetworkIdle } as any);
    await executeAction({ action: 'waitForNetworkIdle' } as any, createCtx(), env);
    expect(waitForNetworkIdle).toHaveBeenCalledWith(500, 30000);
  });

  it('passes explicit idleTime and timeout', async () => {
    const waitForNetworkIdle = vi.fn(async () => {});
    const env = createMockEnv({ waitForNetworkIdle } as any);
    await executeAction({ action: 'waitForNetworkIdle', idleTime: 200, timeout: 5000 } as any, createCtx(), env);
    expect(waitForNetworkIdle).toHaveBeenCalledWith(200, 5000);
  });

  it('escalates TimeoutError from env method', async () => {
    const waitForNetworkIdle = vi.fn(async () => { throw new Error('TimeoutError: idle'); });
    const env = createMockEnv({ waitForNetworkIdle } as any);
    await expect(
      executeAction({ action: 'waitForNetworkIdle' } as any, createCtx(), env),
    ).rejects.toThrow(/TimeoutError/);
  });

  it('throws UnsupportedInEnvironment when method absent', async () => {
    const env = createMockEnv();
    await expect(
      executeAction({ action: 'waitForNetworkIdle' } as any, createCtx(), env),
    ).rejects.toThrow(/UnsupportedInEnvironment/);
  });
});
