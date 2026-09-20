import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createTransport } from './transport';

describe('userscript transport', () => {
  const requests: any[] = [];

  beforeEach(() => {
    requests.length = 0;
    vi.stubGlobal('GM_xmlhttpRequest', vi.fn((options: any) => {
      requests.push(options);
      options.onload({ status: 200, statusText: 'OK', responseText: '{"success":true}' });
    }));
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('maps executor terminal statuses to task API statuses', async () => {
    const transport = createTransport('http://127.0.0.1:8080/', 'task-1', 'worker-1');

    await transport.sendStatus('running', 'started');
    await transport.sendStatus('success', 'complete');
    await transport.sendStatus('failure', 'broken');

    expect(requests.map((request) => JSON.parse(request.data).status)).toEqual([
      'running',
      'done',
      'failed',
    ]);
    expect(requests.map((request) => request.url)).toEqual([
      'http://127.0.0.1:8080/status',
      'http://127.0.0.1:8080/status',
      'http://127.0.0.1:8080/status',
    ]);
  });

  it('reports non-success HTTP responses instead of silently accepting them', async () => {
    vi.stubGlobal('GM_xmlhttpRequest', vi.fn((options: any) => {
      options.onload({ status: 400, statusText: 'Bad Request', responseText: '{"error":"invalid"}' });
    }));
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1');

    await transport.sendStatus('success');

    expect(error).toHaveBeenCalledWith(
      '[AegisCrawler] sendStatus failed',
      expect.objectContaining({ message: 'HTTP 400 Bad Request' }),
    );
  });
});

describe('attempt-scoped (versioned) transport', () => {
  const requests: any[] = [];

  beforeEach(() => {
    requests.length = 0;
    vi.stubGlobal('GM_xmlhttpRequest', vi.fn((options: any) => {
      requests.push(options);
      options.onload({ status: 200, statusText: 'OK', responseText: '{"success":true,"valid":true,"duplicate":false}' });
    }));
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('submits attempt-scoped batches with idempotency keys and sequences', async () => {
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', { attemptId: 'att-1' });

    await transport.sendResult({ title: 'a' });
    await transport.sendResult({ title: 'b' });

    const bodies = requests.map((r) => JSON.parse(r.data));
    expect(bodies.map((b) => b.attemptId)).toEqual(['att-1', 'att-1']);
    expect(bodies.map((b) => b.kind)).toEqual(['batch', 'batch']);
    expect(bodies.map((b) => b.sequence)).toEqual([1, 2]);
    expect(bodies.map((b) => b.idempotencyKey)).toEqual(['att-1:1:batch', 'att-1:2:batch']);
  });

  it('drops executor control markers instead of submitting them as batches', async () => {
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', { attemptId: 'att-1' });

    await transport.sendResult({ __final: true, taskId: 'task-1' }, true);
    await transport.sendResult({ __flush: true }, true);
    await transport.sendResult({ __criticalFailure: true, error: {}, partialData: {} }, true);

    expect(requests).toHaveLength(0);
  });

  it('submits exactly one summary through sendFinalSummary', async () => {
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', { attemptId: 'att-1' });

    await transport.sendResult({ title: 'a' });
    await transport.sendFinalSummary!({ status: 'success', message: 'done', data: {} });

    const bodies = requests.map((r) => JSON.parse(r.data));
    expect(bodies.map((b) => b.kind)).toEqual(['batch', 'summary']);
    expect(bodies[1].idempotencyKey).toBe('att-1:2:summary');
    expect(bodies[1].immediate).toBe(true);
  });

  it('rejects a batch the server marks invalid', async () => {
    vi.stubGlobal('GM_xmlhttpRequest', vi.fn((options: any) => {
      options.onload({
        status: 200,
        statusText: 'OK',
        responseText: '{"success":false,"valid":false,"duplicate":false,"error":"row 0 must be object"}',
      });
    }));
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', { attemptId: 'att-1' });

    await expect(transport.sendResult({ nope: 1 })).rejects.toThrow(/result rejected/);
  });

  it('skips executor-internal statuses and sends attempt-bound task statuses', async () => {
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', { attemptId: 'att-1' });

    await transport.sendStatus('running', 'started');
    await transport.sendStatus('success', 'internal');
    await transport.sendStatus('failure', 'internal');
    await transport.sendStatus('done', 'complete');

    const bodies = requests.map((r) => JSON.parse(r.data));
    expect(bodies.map((b) => b.status)).toEqual(['running', 'done']);
    expect(bodies.every((b) => b.attemptId === 'att-1')).toBe(true);
  });

  it('adds a bearer header to every server call when a worker API key is configured', async () => {
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', {
      attemptId: 'att-1',
      workerApiKey: 'secret-key',
    });

    await transport.sendResult({ title: 'a' });
    await transport.sendStatus('done', 'complete');

    expect(requests.map((r) => r.headers.Authorization)).toEqual(['Bearer secret-key', 'Bearer secret-key']);
  });

  it('omits the authorization header entirely when no key is configured', async () => {
    const transport = createTransport('http://127.0.0.1:8080', 'task-1', 'worker-1', { attemptId: 'att-1' });

    await transport.sendResult({ title: 'a' });

    expect(requests[0].headers.Authorization).toBeUndefined();
  });
});
