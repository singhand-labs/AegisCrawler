/// <reference types="vitest/globals" />
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createEnvironment, loadTaskFromHash, isTrustedServerUrl, getTrustedServerOrigins, getWorkerApiKey } from './environment';
import type { Transport } from './types';

const mockTransport: Transport = {
  fetchRule: async () => null,
  sendResult: async () => {},
  sendLog: async () => {},
  sendHeartbeat: async () => ({ cancelRequested: false }),
  sendStatus: async () => {},
  sendSnapshot: async () => {},
};

function setHash(payload: Record<string, unknown>): void {
  const encoded = btoa(JSON.stringify(payload));
  (window.location as any).hash = `#scrape=${encoded}`;
}

function clearHash(): void {
  (window.location as any).hash = '';
}

describe('isTrustedServerUrl', () => {
  let savedOrigins: any;
  let savedGM: any;

  beforeEach(() => {
    savedOrigins = (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    savedGM = (globalThis as any).GM_getValue;
    delete (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    delete (globalThis as any).GM_getValue;
  });

  afterEach(() => {
    if (savedOrigins !== undefined) (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = savedOrigins;
    else delete (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    if (savedGM !== undefined) (globalThis as any).GM_getValue = savedGM;
    else delete (globalThis as any).GM_getValue;
  });

  it('returns false when allowlist is empty (fail closed)', () => {
    expect(isTrustedServerUrl('https://api.example.com')).toBe(false);
  });

  it('returns true for URL matching a trusted origin', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    expect(isTrustedServerUrl('https://api.example.com/results')).toBe(true);
  });

  it('returns false for URL not matching any trusted origin', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    expect(isTrustedServerUrl('https://evil.com/results')).toBe(false);
  });

  it('matches origin (scheme+host+port), not path', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['http://localhost:8080'];
    expect(isTrustedServerUrl('http://localhost:8080/api/v2/rules/r1')).toBe(true);
    expect(isTrustedServerUrl('http://localhost:9090/api')).toBe(false);
  });

  it('returns false for invalid URL', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    expect(isTrustedServerUrl('not-a-url')).toBe(false);
  });

  it('reads from GM_getValue when __TRUSTED_SERVER_ORIGINS__ is absent', () => {
    (globalThis as any).GM_getValue = (key: string) => {
      if (key === 'trustedServerOrigins') return ['https://gm-server.example.com'];
      return undefined;
    };
    expect(isTrustedServerUrl('https://gm-server.example.com/heartbeat')).toBe(true);
  });

  it('prioritizes __TRUSTED_SERVER_ORIGINS__ over GM_getValue', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://override.example.com'];
    (globalThis as any).GM_getValue = () => ['https://gm.example.com'];
    expect(isTrustedServerUrl('https://override.example.com')).toBe(true);
    expect(isTrustedServerUrl('https://gm.example.com')).toBe(false);
  });
});

describe('loadTaskFromHash', () => {
  let savedOrigins: any;

  beforeEach(() => {
    savedOrigins = (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
  });

  afterEach(() => {
    clearHash();
    if (savedOrigins !== undefined) (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = savedOrigins;
    else delete (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
  });

  it('returns null when no scrape hash is present', () => {
    delete (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    clearHash();
    expect(loadTaskFromHash()).toBeNull();
  });

  it('returns null when allowlist is empty (fail closed)', () => {
    delete (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    setHash({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://api.example.com',
      workerId: 'w1',
      variables: {},
    });
    expect(loadTaskFromHash()).toBeNull();
  });

  it('returns null for untrusted serverUrl', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    setHash({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://evil.com',
      workerId: 'w1',
      variables: {},
    });
    expect(loadTaskFromHash()).toBeNull();
  });

  it('returns task descriptor for trusted serverUrl', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    setHash({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://api.example.com/rules/r1',
      workerId: 'w1',
      variables: { key: 'value' },
    });
    const result = loadTaskFromHash();
    expect(result).toEqual({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://api.example.com/rules/r1',
      workerId: 'w1',
      variables: { key: 'value' },
    });
  });

  it('returns null for malformed base64', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    (window.location as any).hash = '#scrape=!!!invalid-base64!!!';
    expect(loadTaskFromHash()).toBeNull();
  });

  it('passes a well-formed attemptId through', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    setHash({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://api.example.com',
      workerId: 'w1',
      attemptId: 'a1b2c3d4-e5f6-7890-abcd-ef0123456789',
      variables: {},
    });
    expect(loadTaskFromHash()?.attemptId).toBe('a1b2c3d4-e5f6-7890-abcd-ef0123456789');
  });

  it('rejects a malformed attemptId (fail closed)', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    for (const malformed of ['', 42, 'x'.repeat(129)]) {
      setHash({
        taskId: 't1',
        ruleId: 'r1',
        serverUrl: 'https://api.example.com',
        workerId: 'w1',
        attemptId: malformed,
        variables: {},
      });
      expect(loadTaskFromHash()).toBeNull();
    }
  });

  it('passes a boolean closeOnDone through and rejects non-boolean values', () => {
    (globalThis as any).__TRUSTED_SERVER_ORIGINS__ = ['https://api.example.com'];
    setHash({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://api.example.com',
      workerId: 'w1',
      closeOnDone: true,
      variables: {},
    });
    expect(loadTaskFromHash()?.closeOnDone).toBe(true);

    setHash({
      taskId: 't1',
      ruleId: 'r1',
      serverUrl: 'https://api.example.com',
      workerId: 'w1',
      closeOnDone: 'yes',
      variables: {},
    });
    expect(loadTaskFromHash()).toBeNull();
  });
});

describe('getWorkerApiKey', () => {
  const savedGlobal = (globalThis as any).__OC_WORKER_API_KEY__;
  const savedGM = (globalThis as any).GM_getValue;

  afterEach(() => {
    delete (globalThis as any).__OC_WORKER_API_KEY__;
    if (savedGM !== undefined) (globalThis as any).GM_getValue = savedGM;
    else delete (globalThis as any).GM_getValue;
    if (savedGlobal !== undefined) (globalThis as any).__OC_WORKER_API_KEY__ = savedGlobal;
  });

  it('prefers the injection override over manager storage', () => {
    (globalThis as any).__OC_WORKER_API_KEY__ = 'injected-key';
    (globalThis as any).GM_getValue = (key: string) => (key === 'workerApiKey' ? 'stored-key' : undefined);
    expect(getWorkerApiKey()).toBe('injected-key');
  });

  it('falls back to manager storage when no override exists', () => {
    delete (globalThis as any).__OC_WORKER_API_KEY__;
    (globalThis as any).GM_getValue = (key: string) => (key === 'workerApiKey' ? 'stored-key' : undefined);
    expect(getWorkerApiKey()).toBe('stored-key');
  });

  it('returns undefined (unauthenticated) when nothing is configured', () => {
    delete (globalThis as any).__OC_WORKER_API_KEY__;
    delete (globalThis as any).GM_getValue;
    expect(getWorkerApiKey()).toBeUndefined();
  });

  it('ignores non-string stored values', () => {
    delete (globalThis as any).__OC_WORKER_API_KEY__;
    (globalThis as any).GM_getValue = () => 12345;
    expect(getWorkerApiKey()).toBeUndefined();
  });
});

describe('createEnvironment evaluate gating', () => {
  it('throws when allowEvaluate is false (default)', async () => {
    const env = createEnvironment(mockTransport);
    await expect(env.evaluate('return 1 + 2')).rejects.toThrow(
      'Security error: evaluate is disabled in userscript environment',
    );
  });

  it('evaluates through sandbox when allowEvaluate is true', async () => {
    const env = createEnvironment(mockTransport, true);
    const result = await env.evaluate('return 1 + 2');
    expect(result).toBe(3);
  });

  it('blocks forbidden patterns when enabled', async () => {
    const env = createEnvironment(mockTransport, true);
    await expect(env.evaluate('eval("1+1")')).rejects.toThrow(
      'Security error: forbidden pattern "eval("',
    );
  });

  it('blocks constructor access when enabled', async () => {
    const env = createEnvironment(mockTransport, true);
    await expect(env.evaluate('return ({}).constructor')).rejects.toThrow(
      'Security error: forbidden token "constructor"',
    );
  });

  it('exposes ctx in sandbox', async () => {
    const env = createEnvironment(mockTransport, true);
    const result = await env.evaluate('return ctx.value', { value: 42 });
    expect(result).toBe(42);
  });
});

describe('createEnvironment snapshots', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('captures a canvas screenshot when a 2D context is available', async () => {
    const drawWindow = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ drawWindow } as any);
    vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/png;base64,test');

    const snapshot = await createEnvironment(mockTransport).screenshot('page');

    expect(snapshot).toEqual({
      name: 'page',
      type: 'screenshot',
      data: 'data:image/png;base64,test',
    });
    expect(drawWindow).toHaveBeenCalled();
  });

  it('captures a screenshot when drawWindow is unavailable', async () => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({} as any);
    vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/png;base64,test');

    await expect(createEnvironment(mockTransport).screenshot('page')).resolves.toMatchObject({
      type: 'screenshot',
    });
  });

  it('returns null when no canvas context is available', async () => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
    await expect(createEnvironment(mockTransport).screenshot('page')).resolves.toBeNull();
  });

  it('returns null when screenshot capture throws', async () => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() => {
      throw new Error('canvas failure');
    });
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    await expect(createEnvironment(mockTransport).screenshot('page')).resolves.toBeNull();
    expect(errorSpy).toHaveBeenCalledWith('[AegisCrawler] screenshot failed', expect.any(Error));
  });

  it('captures html and dom snapshots and rejects screenshot snapshots', async () => {
    document.documentElement.innerHTML = '<head></head><body><main>content</main></body>';
    const env = createEnvironment(mockTransport);

    await expect(env.saveSnapshot('html')).resolves.toMatchObject({
      name: 'html',
      type: 'html',
      data: expect.stringContaining('<main>content</main>'),
    });
    await expect(env.saveSnapshot('dom', 'dom')).resolves.toMatchObject({
      name: 'dom',
      type: 'dom',
      data: expect.stringContaining('<html>'),
    });
    await expect(env.saveSnapshot('screen', 'screenshot')).resolves.toBeNull();
  });

  it('returns null when DOM snapshot serialization throws', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    const htmlSpy = vi.spyOn(Element.prototype, 'innerHTML', 'get').mockImplementation(() => {
      throw new Error('serialization failure');
    });

    await expect(createEnvironment(mockTransport).saveSnapshot('html')).resolves.toBeNull();
    expect(errorSpy).toHaveBeenCalledWith('[AegisCrawler] saveSnapshot failed', expect.any(Error));
    htmlSpy.mockRestore();
  });
});

describe('getTrustedServerOrigins failures', () => {
  it('fails closed when trusted-origin storage throws', () => {
    delete (globalThis as any).__TRUSTED_SERVER_ORIGINS__;
    (globalThis as any).GM_getValue = () => {
      throw new Error('storage unavailable');
    };

    expect(getTrustedServerOrigins()).toEqual([]);
    delete (globalThis as any).GM_getValue;
  });
});
