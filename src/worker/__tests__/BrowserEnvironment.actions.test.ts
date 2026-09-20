/// <reference types="vitest/globals" />
import { vi } from 'vitest';
import { executeAction } from '../../scriptcat-engine/actions';
import type { RuntimeContext } from '../../scriptcat-engine/types';
import { BrowserEnvironment } from '../BrowserEnvironment';
import { HttpTransport } from '../HttpTransport';

function createContext(): RuntimeContext {
  return {
    taskId: 'task-1',
    workerId: 'worker-1',
    ruleId: 'rule-1',
    ruleVersion: '1.0.0',
    variables: {},
    extracted: {},
    evaluated: {},
    captured: {},
    resultsSent: 0,
    logsSent: 0,
  };
}

function createTransport(): HttpTransport {
  return new HttpTransport({
    baseUrl: 'https://api.example.com/',
    workerId: 'worker-1',
    taskId: 'task-1',
  });
}

describe('action dispatcher with BrowserEnvironment', () => {
  it.each([
    { action: 'goBack' as const, method: 'back' as const, nextUrl: 'https://example.com/back' },
    { action: 'goForward' as const, method: 'forward' as const, nextUrl: 'https://example.com/forward' },
  ])('preserves the BrowserEnvironment receiver for $action', async ({ action, method, nextUrl }) => {
    const location = { href: 'https://example.com/current' };
    const history = {
      back: vi.fn(() => { location.href = nextUrl; }),
      forward: vi.fn(() => { location.href = nextUrl; }),
    };
    const browserWindow = {
      location,
      history,
      document: { readyState: 'complete' },
    } as unknown as Window;
    const env = new BrowserEnvironment(createTransport(), browserWindow);

    await executeAction({ action }, createContext(), env);

    expect(history[method]).toHaveBeenCalledTimes(1);
    expect(location.href).toBe(nextUrl);
  });

  it('preserves the BrowserEnvironment receiver for waitForNetworkIdle', async () => {
    const observe = vi.fn();
    const disconnect = vi.fn();
    class FakePerformanceObserver {
      constructor(_callback: PerformanceObserverCallback) {}
      observe = observe;
      disconnect = disconnect;
    }
    const browserWindow = {
      performance: { now: () => 10 },
      PerformanceObserver: FakePerformanceObserver,
    } as unknown as Window;
    const env = new BrowserEnvironment(createTransport(), browserWindow);

    await executeAction(
      { action: 'waitForNetworkIdle', idleTime: 0, timeout: 1234 },
      createContext(),
      env,
    );

    expect(observe).toHaveBeenCalledWith({ entryTypes: ['resource'] });
    expect(disconnect).toHaveBeenCalledTimes(1);
  });
});
