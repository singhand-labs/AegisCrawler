import type { Environment, Snapshot, Target, Transport } from '../scriptcat-engine/types';
import { resolveTarget, ElementVerificationError } from '../scriptcat-engine/selectors';
import { evaluateInSandbox } from '../scriptcat-engine/sandbox';

export interface NetworkIdleObservable {
  performance: { now(): number };
  PerformanceObserver: typeof PerformanceObserver;
}

export interface UrlSettleWindow {
  location: { href: string };
  document: { readyState: DocumentReadyState };
}

/**
 * Poll until `window.location.href` differs from `startUrl` AND `readyState`
 * reaches at least `'interactive'`, OR until `timeoutMs` elapses (silent).
 * Bounded by `timeoutMs` across both phases — no unbounded inner loops.
 * Exported so tests can drive it with a fake `{location, document}` literal
 * without paying the 5s real-timer cost of an integration test.
 */
export async function waitForUrlSettledImpl(
  win: UrlSettleWindow,
  sleep: (ms: number) => Promise<void>,
  now: () => number,
  startUrl: string,
  timeoutMs: number,
): Promise<void> {
  const start = now();
  while (now() - start < timeoutMs) {
    if (win.location.href !== startUrl) {
      while (
        now() - start < timeoutMs &&
        win.document.readyState !== 'interactive' &&
        win.document.readyState !== 'complete'
      ) {
        await sleep(50);
      }
      return;
    }
    await sleep(100);
  }
  // Timeout — navigation may not have happened (pushState without load event).
  // Do not throw; matches existing post-navigate polling behavior.
}

/**
 * Wait for a quiet period between completed PerformanceResourceTiming entries.
 * PerformanceObserver reports resource completion, not the current in-flight
 * request count, so this is intentionally only an environment-level heuristic.
 * Server-generated provisional workflows use target-specific waits instead.
 */
export async function waitForNetworkIdleImpl(
  obs: NetworkIdleObservable,
  sleep: (ms: number) => Promise<void>,
  idleTimeMs: number,
  timeoutMs: number,
): Promise<void> {
  const start = obs.performance.now();
  let lastActivityAt = start;
  const observer = new obs.PerformanceObserver(() => {
    lastActivityAt = obs.performance.now();
  });
  observer.observe({ entryTypes: ['resource'] } as any);
  try {
    while (true) {
      const now = obs.performance.now();
      if (now - lastActivityAt >= idleTimeMs) return;
      if (now - start > timeoutMs) throw new Error('TimeoutError: waitForNetworkIdle');
      await sleep(Math.min(idleTimeMs, 100));
    }
  } finally {
    observer.disconnect();
  }
}

export class BrowserEnvironment implements Environment {
  transport: Transport;
  private window: Window;
  private allowEvaluate: boolean;
  private allowEvaluateDOM: boolean;

  constructor(
    transport: Transport,
    window: Window,
    allowEvaluate = false,
    allowEvaluateDOM = false,
  ) {
    this.transport = transport;
    this.window = window;
    this.allowEvaluate = allowEvaluate;
    this.allowEvaluateDOM = allowEvaluateDOM;
  }

  private get document(): Document {
    return this.window.document;
  }

  async findElement(target: Target, timeout = 5000): Promise<Element | null> {
    const start = Date.now();
    let verificationError: unknown = null;
    while (Date.now() - start < timeout) {
      let el: Element | Element[] | null = null;
      try {
        el = resolveTarget(target, this.document, false);
      } catch (e) {
        if (e instanceof ElementVerificationError) {
          // Wrong-element match — surface after timeout (see selectors.findElement).
          verificationError = e;
        } else {
          throw e;
        }
      }
      if (el && !Array.isArray(el)) return el;
      await this.sleep(100);
    }
    if (verificationError) throw verificationError;
    return null;
  }

  async findElements(target: Target, timeout = 5000): Promise<Element[]> {
    const start = Date.now();
    let verificationError: unknown = null;
    while (Date.now() - start < timeout) {
      let els: Element | Element[] | null = null;
      try {
        els = resolveTarget(target, this.document, true);
      } catch (e) {
        if (e instanceof ElementVerificationError) {
          verificationError = e;
        } else {
          throw e;
        }
      }
      if (Array.isArray(els) && els.length > 0) return els;
      await this.sleep(100);
    }
    if (verificationError) throw verificationError;
    return [];
  }

  async sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  now(): number {
    return Date.now();
  }

  getUrl(): string {
    return this.window.location.href;
  }

  getTitle(): string {
    return this.document.title;
  }

  async evaluate(script: string, ctx?: any, args?: any[]): Promise<any> {
    if (!this.allowEvaluate) {
      throw new Error('Security error: evaluate is disabled. Enable with allowEvaluate: true');
    }

    return evaluateInSandbox(script, ctx, args, {
      globalObj: this.allowEvaluateDOM ? this.window : (globalThis as any),
      allowDOM: this.allowEvaluateDOM,
      window: this.window,
      document: this.document,
    });
  }

  async screenshot(name: string): Promise<Snapshot | null> {
    return { name, type: 'html', data: this.document.documentElement.outerHTML };
  }

  async saveSnapshot(name: string, type: 'html' | 'dom' | 'screenshot' = 'html'): Promise<Snapshot | null> {
    let data = '';
    if (type === 'html') data = this.document.documentElement.outerHTML;
    else if (type === 'dom') data = this.document.body.innerHTML;
    else data = '[screenshot-not-implemented]';
    // The saveSnapshot action owns transport delivery. Keeping environment
    // capture side-effect free prevents every artifact from being submitted
    // twice and matches the userscript environment contract.
    return { name, type, data };
  }

  async goBack(): Promise<void> {
    const startUrl = this.window.location.href;
    this.window.history.back();
    await waitForUrlSettledImpl(this.window, (ms) => this.sleep(ms), () => Date.now(), startUrl, 5000);
  }

  async goForward(): Promise<void> {
    const startUrl = this.window.location.href;
    this.window.history.forward();
    await waitForUrlSettledImpl(this.window, (ms) => this.sleep(ms), () => Date.now(), startUrl, 5000);
  }

  async setViewport(_viewport: {
    width: number; height: number;
    deviceScaleFactor?: number; isMobile?: boolean; hasTouch?: boolean; userAgent?: string;
  }): Promise<void> {
    throw new Error(
      'UnsupportedInEnvironment: setViewport requires Playwright Worker ' +
      '(window.resizeTo is blocked for nonpopup tabs; deviceScaleFactor / ' +
      'isMobile / hasTouch / userAgent need DevTools Protocol access)',
    );
  }

  async waitForNetworkIdle(idleTimeMs: number, timeoutMs: number): Promise<void> {
    // PerformanceObserver is a global `declare var`, not modeled as a Window
    // property by lib.dom.d.ts — but it is present on `window` at runtime.
    await waitForNetworkIdleImpl(
      {
        performance: this.window.performance,
        PerformanceObserver: (this.window as any).PerformanceObserver,
      },
      (ms) => this.sleep(ms),
      idleTimeMs,
      timeoutMs,
    );
  }
}
