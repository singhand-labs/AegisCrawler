/// <reference types="vitest/globals" />
import { vi, describe, it, expect } from 'vitest';
import { BrowserEnvironment, waitForNetworkIdleImpl, waitForUrlSettledImpl } from '../BrowserEnvironment';
import { HttpTransport } from '../HttpTransport';

describe('BrowserEnvironment', () => {
  function createEnvironment(allowEvaluate = false, allowEvaluateDOM = false) {
    const transport = new HttpTransport({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      taskId: 'task-1',
    });
    return new BrowserEnvironment(transport, window, allowEvaluate, allowEvaluateDOM);
  }

  it('uses default evaluate flags when omitted', () => {
    const transport = new HttpTransport({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      taskId: 'task-1',
    });
    const env = new BrowserEnvironment(transport, window);
    expect(env).toBeInstanceOf(BrowserEnvironment);
  });

  it('evaluate throws when disabled', async () => {
    const env = createEnvironment(false);
    await expect(env.evaluate('return 1 + 2;')).rejects.toThrow(
      'Security error: evaluate is disabled',
    );
  });

  describe('element lookup', () => {
    it('findElement returns null when target is not found within timeout', async () => {
      const env = createEnvironment();
      const result = await env.findElement({ selector: '#missing-element' }, 0);
      expect(result).toBeNull();
    });

    it('findElements returns empty array when no elements are found within timeout', async () => {
      const env = createEnvironment();
      const result = await env.findElements({ selector: '.missing-elements' }, 0);
      expect(result).toEqual([]);
    });

    it('findElement resolves as soon as the element appears', async () => {
      const env = createEnvironment();
      const div = document.createElement('div');
      div.id = 'dynamic-target';
      setTimeout(() => document.body.appendChild(div), 50);
      const result = await env.findElement({ selector: '#dynamic-target' }, 500);
      expect(result).toBe(div);
      document.body.removeChild(div);
    });

    it('findElements resolves as soon as matching elements appear', async () => {
      const env = createEnvironment();
      const div = document.createElement('div');
      div.className = 'dynamic-items';
      setTimeout(() => document.body.appendChild(div), 50);
      const result = await env.findElements({ selector: '.dynamic-items' }, 500);
      expect(result).toEqual([div]);
      document.body.removeChild(div);
    });
  });

  describe('environment metadata', () => {
    it('returns current timestamp', () => {
      const env = createEnvironment();
      const before = Date.now();
      const result = env.now();
      const after = Date.now();
      expect(result).toBeGreaterThanOrEqual(before);
      expect(result).toBeLessThanOrEqual(after);
    });

    it('returns current url', () => {
      const env = createEnvironment();
      expect(env.getUrl()).toBe(window.location.href);
    });

    it('returns document title', () => {
      const env = createEnvironment();
      document.title = 'Test Title';
      expect(env.getTitle()).toBe('Test Title');
    });
  });

  describe('snapshots', () => {
    it('screenshot returns html snapshot of document element', async () => {
      const env = createEnvironment();
      const result = await env.screenshot('home');
      expect(result).toEqual({
        name: 'home',
        type: 'html',
        data: document.documentElement.outerHTML,
      });
    });

    it('saveSnapshot returns html for the action to send exactly once', async () => {
      const transport = new HttpTransport({
        baseUrl: 'https://api.example.com/',
        workerId: 'worker-1',
        taskId: 'task-1',
      });
      const sendSnapshot = vi.spyOn(transport, 'sendSnapshot').mockResolvedValue(undefined);
      const env = new BrowserEnvironment(transport, window);
      const result = await env.saveSnapshot('home');
      expect(result).toEqual({
        name: 'home',
        type: 'html',
        data: document.documentElement.outerHTML,
      });
      expect(sendSnapshot).not.toHaveBeenCalled();
      sendSnapshot.mockRestore();
    });

    it('saveSnapshot returns dom snapshot without transport side effects', async () => {
      const transport = new HttpTransport({
        baseUrl: 'https://api.example.com/',
        workerId: 'worker-1',
        taskId: 'task-1',
      });
      const sendSnapshot = vi.spyOn(transport, 'sendSnapshot').mockResolvedValue(undefined);
      const env = new BrowserEnvironment(transport, window);
      const result = await env.saveSnapshot('body', 'dom');
      expect(result).toEqual({
        name: 'body',
        type: 'dom',
        data: document.body.innerHTML,
      });
      expect(sendSnapshot).not.toHaveBeenCalled();
      sendSnapshot.mockRestore();
    });

    it('saveSnapshot returns screenshot placeholder without sending it', async () => {
      const transport = new HttpTransport({
        baseUrl: 'https://api.example.com/',
        workerId: 'worker-1',
        taskId: 'task-1',
      });
      const sendSnapshot = vi.spyOn(transport, 'sendSnapshot').mockResolvedValue(undefined);
      const env = new BrowserEnvironment(transport, window);
      const result = await env.saveSnapshot('screen', 'screenshot');
      expect(result).toEqual({
        name: 'screen',
        type: 'screenshot',
        data: '[screenshot-not-implemented]',
      });
      expect(sendSnapshot).not.toHaveBeenCalled();
      sendSnapshot.mockRestore();
    });
  });

  describe('when evaluate is enabled', () => {
    it('evaluate returns the result of an expression', async () => {
      const env = createEnvironment(true);
      const result = await env.evaluate('return 1 + 2;');
      expect(result).toBe(3);
    });

    it('evaluate exposes ctx', async () => {
      const env = createEnvironment(true);
      const result = await env.evaluate('return ctx.value;', { value: 42 });
      expect(result).toBe(42);
    });

    it('evaluate exposes safe built-ins', async () => {
      const env = createEnvironment(true);
      expect(await env.evaluate('return Math.PI;')).toBe(Math.PI);
      expect(await env.evaluate('return JSON.parse(\'{"a":1}\').a;')).toBe(1);
      expect(await env.evaluate('return Array.isArray([1]);')).toBe(true);
      expect(await env.evaluate('return typeof Promise.resolve;')).toBe('function');
    });

    it('evaluate rejects DOM globals by default', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('return document.title;')).rejects.toThrow(
        'Security error: access to "document" is not allowed',
      );
      await expect(env.evaluate('return window.location.href;')).rejects.toThrow(
        'Security error: access to "window" is not allowed',
      );
      await expect(env.evaluate('return localStorage.length;')).rejects.toThrow(
        'Security error: access to "localStorage" is not allowed',
      );
    });

    it('evaluate blocks computed-property window escape', async () => {
      const env = createEnvironment(true);
      await expect(
        env.evaluate("const w = window; const e = w['ev'+'al']; return e('1+1');"),
      ).rejects.toThrow('Security error: access to "window" is not allowed');
    });

    it('evaluate rejects eval()', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('eval("1+1");')).rejects.toThrow(
        'Security error: forbidden pattern "eval("',
      );
    });

    it('evaluate rejects Function()', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('Function("return 1");')).rejects.toThrow(
        'Security error: forbidden pattern "Function("',
      );
    });

    it('evaluate rejects new Function', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('new Function("return 1");')).rejects.toThrow(
        'Security error: forbidden pattern "new Function"',
      );
    });

    it('evaluate rejects import()', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('import("module");')).rejects.toThrow(
        'Security error: forbidden pattern "import("',
      );
    });

    it('evaluate rejects constructor token access', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('return ({}).constructor;')).rejects.toThrow(
        'Security error: forbidden token "constructor"',
      );
    });

    it('evaluate rejects sandbox constructor access via unicode escape', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('return \\u0063onstructor;')).rejects.toThrow(
        'Security error: access to "constructor" is not allowed',
      );
    });

    it('evaluate rejects Function token access', async () => {
      const env = createEnvironment(true);
      await expect(env.evaluate('return Func\u0074ion;')).rejects.toThrow(
        'Security error: forbidden token "Function"',
      );
    });

    it('evaluate runs with undefined this', async () => {
      const env = createEnvironment(true);
      const result = await env.evaluate('return this;');
      expect(result).toBeUndefined();
    });

    it('evaluate allows DOM access only when allowEvaluateDOM is true', async () => {
      const env = createEnvironment(true, true);
      const result = await env.evaluate('return document.title;');
      expect(typeof result).toBe('string');

      const windowResult = await env.evaluate('return window.location.href;');
      expect(typeof windowResult).toBe('string');
    });

    it('evaluate logs a warning when DOM access is enabled', async () => {
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const env = createEnvironment(true, true);
      await env.evaluate('return document.title;');
      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining('allowEvaluateDOM is enabled'),
      );
      warnSpy.mockRestore();
    });

  });

  describe('setViewport', () => {
    it('throws UnsupportedInEnvironment with full rationale', async () => {
      const env = createEnvironment();
      await expect(env.setViewport!({ width: 100, height: 100 })).rejects.toThrow(/UnsupportedInEnvironment/);
    });
  });

  describe('waitForNetworkIdleImpl (factored helper)', () => {
    it('resolves when no resources arrive for idleTimeMs', async () => {
      let time = 0;
      const now = () => time;
      class FakeObserver {
        cb: () => void;
        constructor(cb: () => void) { this.cb = cb; }
        observe() {}
        disconnect() {}
      }
      const obs = {
        performance: { now },
        PerformanceObserver: FakeObserver as any,
      };
      const sleep = async (ms: number) => { time += ms; };
      // idleTime 200ms; no entries; after sleep(100), time=100 < 200; after sleep(100), time=200 >= 200 → resolve
      await waitForNetworkIdleImpl(obs as any, sleep, 200, 5000);
      expect(time).toBeGreaterThanOrEqual(200);
    });

    it('rejects with TimeoutError when timeout elapses without idle', async () => {
      let time = 0;
      const now = () => time;
      let observerRef: { push: () => void } | null = null;
      class FakeObserver {
        cb: () => void;
        constructor(cb: () => void) { this.cb = cb; observerRef = { push: () => this.cb() }; }
        observe() {}
        disconnect() {}
      }
      const obs = { performance: { now }, PerformanceObserver: FakeObserver as any };
      const sleep = async (ms: number) => {
        time += ms;
        // continuously fire entries to keep lastActivityAt = time
        observerRef?.push();
      };
      await expect(
        waitForNetworkIdleImpl(obs as any, sleep, 1000, 500),
      ).rejects.toThrow(/TimeoutError/);
    });
  });

  describe('goBack', () => {
    it('calls history.back (without paying 5s settle cost)', async () => {
      // Integration: jsdom history.back is a no-op; waitForUrlSettledImpl will
      // poll the unchanged URL and resolve silently after 5s. We don't await
      // that — just assert back() was invoked synchronously.
      const backSpy = vi.spyOn(window.history, 'back').mockImplementation(() => {});
      const env = createEnvironment();
      void env.goBack!(); // fire and forget; settle poll is irrelevant to this assertion
      expect(backSpy).toHaveBeenCalledTimes(1);
      backSpy.mockRestore();
    });
  });

  describe('waitForUrlSettledImpl (factored helper)', () => {
    it('returns silently when URL never changes (timeout path, no throw)', async () => {
      let time = 0;
      const now = () => time;
      const sleep = async (ms: number) => { time += ms; };
      const win = { location: { href: 'https://example.com/a' }, document: { readyState: 'complete' as DocumentReadyState } };
      await waitForUrlSettledImpl(win, sleep, now, 'https://example.com/a', 5000);
      expect(time).toBeGreaterThanOrEqual(5000);
    });

    it('resolves once URL changes and readyState reaches interactive', async () => {
      let time = 0;
      const now = () => time;
      const sleep = async (ms: number) => {
        time += ms;
        if (time >= 300 && win.location.href === 'https://example.com/a') {
          win.location.href = 'https://example.com/b';
          win.document.readyState = 'interactive';
        }
      };
      const win = {
        location: { href: 'https://example.com/a' },
        document: { readyState: 'loading' as DocumentReadyState },
      };
      await waitForUrlSettledImpl(win, sleep, now, 'https://example.com/a', 5000);
      expect(win.location.href).toBe('https://example.com/b');
      expect(win.document.readyState).toBe('interactive');
      expect(time).toBeLessThan(5000);
    });

    it('bounds the readyState inner loop (regression: previously unbounded)', async () => {
      // URL changed but readyState stuck at 'loading'. Helper must NOT spin
      // forever — bounded by overall timeoutMs budget.
      let time = 0;
      const now = () => time;
      const sleep = async (ms: number) => { time += ms; };
      const win = {
        location: { href: 'https://example.com/b' },
        document: { readyState: 'loading' as DocumentReadyState },
      };
      await waitForUrlSettledImpl(win, sleep, now, 'https://example.com/a', 1000);
      expect(time).toBeGreaterThanOrEqual(1000);
    });
  });
});
