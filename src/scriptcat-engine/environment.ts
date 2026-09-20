import type { Environment, RuntimeContext, Transport, Target, Snapshot } from './types';
import { findElement, findElements } from './selectors';
import { sleep } from './utils';
import { evaluateInSandbox } from './sandbox';

declare const unsafeWindow: Window;

export function createEnvironment(transport: Transport, allowEvaluate = false): Environment {
  return {
    findElement: (target: Target, timeout?: number) => findElement(target, timeout),
    findElements: (target: Target, timeout?: number) => findElements(target, timeout),
    sleep,
    now: () => Date.now(),
    transport,
    getUrl: () => location.href,
    getTitle: () => document.title,
    evaluate: async (script: string, ctx?: any, args?: any[]) => {
      if (!allowEvaluate) {
        throw new Error('Security error: evaluate is disabled in userscript environment');
      }
      return evaluateInSandbox(script, ctx, args, {
        globalObj: globalThis as any,
      });
    },
    screenshot: async (name: string): Promise<Snapshot | null> => {
      try {
        const canvas = document.createElement('canvas');
        canvas.width = window.innerWidth;
        canvas.height = window.innerHeight;
        const ctx = canvas.getContext('2d');
        if (!ctx) return null;
        (ctx as any).drawWindow?.(window, 0, 0, canvas.width, canvas.height, 'rgb(255,255,255)');
        return { name, type: 'screenshot', data: canvas.toDataURL('image/png') };
      } catch (e) {
        console.error('[AegisCrawler] screenshot failed', e);
        return null;
      }
    },
    saveSnapshot: async (name: string, type?: 'html' | 'dom' | 'screenshot'): Promise<Snapshot | null> => {
      try {
        if (type === 'screenshot') return null; // screenshot not reliably available in MV3 content script
        const data = type === 'dom' ? document.documentElement.outerHTML : document.documentElement.innerHTML;
        return { name, type: type ?? 'html', data };
      } catch (e) {
        console.error('[AegisCrawler] saveSnapshot failed', e);
        return null;
      }
    },
  };
}

/**
 * Trusted server origins. Priority:
 * 1. globalThis.__TRUSTED_SERVER_ORIGINS__ (test/injection override)
 * 2. GM_getValue('trustedServerOrigins') (production, set by extension on install)
 * Empty = fail closed (no hash-based tasks accepted).
 */
export function getTrustedServerOrigins(): string[] {
  try {
    const injected = (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    if (Array.isArray(injected) && injected.length > 0) return injected;
    const stored = (globalThis as any).GM_getValue?.('trustedServerOrigins');
    if (Array.isArray(stored) && stored.length > 0) return stored;
  } catch { /* ignore */ }
  return [];
}

export function isTrustedServerUrl(url: string): boolean {
  const trusted = getTrustedServerOrigins();
  if (trusted.length === 0) return false;
  try {
    const parsed = new URL(url);
    return trusted.some((origin) => parsed.origin === new URL(origin).origin);
  } catch {
    return false;
  }
}

/**
 * Worker API key for the collection server. Priority:
 * 1. globalThis.__OC_WORKER_API_KEY__ (deployment/injection override)
 * 2. GM_getValue('workerApiKey') (set once via the userscript manager's storage)
 * Undefined = unauthenticated transport (legacy deployments without WORKER_API_KEY).
 * The key is read from manager storage at call time and never baked into the
 * userscript source.
 */
export function getWorkerApiKey(): string | undefined {
  try {
    const injected = (globalThis as any).__OC_WORKER_API_KEY__;
    if (typeof injected === 'string' && injected.length > 0) return injected;
    const stored = (globalThis as any).GM_getValue?.('workerApiKey');
    if (typeof stored === 'string' && stored.length > 0) return stored;
  } catch { /* ignore */ }
  return undefined;
}

export interface TaskDescriptorFromHash {
  taskId: string;
  ruleId: string;
  serverUrl: string;
  workerId: string;
  variables: Record<string, any>;
  /** Present when the task was claimed through the versioned worker protocol. */
  attemptId?: string;
  /** Dispatcher hint: close this tab once the task reaches a terminal state. */
  closeOnDone?: boolean;
}

export function loadTaskFromHash(): TaskDescriptorFromHash | null {
  const hash = location.hash;
  const match = hash.match(/#scrape=([A-Za-z0-9+/=_-]+)/);
  if (!match) return null;
  try {
    const json = atob(decodeURIComponent(match[1]));
    const result = JSON.parse(json);
    if (!isTrustedServerUrl(result.serverUrl)) {
      console.error('[AegisCrawler] rejected untrusted serverUrl in hash:', result.serverUrl);
      return null;
    }
    if (result.attemptId !== undefined
      && (typeof result.attemptId !== 'string' || result.attemptId.length === 0 || result.attemptId.length > 128)) {
      console.error('[AegisCrawler] rejected malformed attemptId in hash');
      return null;
    }
    if (result.closeOnDone !== undefined && typeof result.closeOnDone !== 'boolean') {
      console.error('[AegisCrawler] rejected malformed closeOnDone in hash');
      return null;
    }
    return result;
  } catch (e) {
    console.error('[AegisCrawler] failed to parse scrape hash', e);
    return null;
  }
}
