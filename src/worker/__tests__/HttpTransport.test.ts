/// <reference types="vitest/globals" />
import { HttpTransport } from '../HttpTransport';
import type { Rule } from '../../scriptcat-engine/types';

describe('HttpTransport', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  function createTransport(apiKey?: string, traceId?: string, overrides?: Partial<ConstructorParameters<typeof HttpTransport>[0]>) {
    return new HttpTransport({
      baseUrl: 'https://api.example.com/',
      workerId: 'worker-1',
      taskId: 'task-1',
      apiKey,
      traceId,
      retryDelayMs: 10,
      ...overrides,
    });
  }

  function lastCall(): [string, RequestInit] | undefined {
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    return calls[calls.length - 1];
  }

  function mockResponseWithTraceId(traceId: string) {
    return {
      ok: true,
      text: async () => '',
      headers: { get: (name: string) => (name === 'X-Trace-Id' ? traceId : null) },
    };
  }

  function resultAck(duplicate = false) {
    return new Response(JSON.stringify({ success: true, valid: true, duplicate }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  it('advertises its named browser profile when claiming work', async () => {
    const claimed = {
      taskId: 'task-profile', ruleId: 'rule-1', ruleVersion: '1.0.0',
      browserProfileId: 'profile-a', variables: {},
    };
    fetchMock.mockResolvedValue(new Response(JSON.stringify(claimed), { status: 200 }));
    const transport = createTransport(undefined, undefined, { browserProfileId: ' profile-a ' });

    await expect(transport.claimTask()).resolves.toEqual(claimed);

    const [url, options] = lastCall()!;
    expect(url).toBe('https://api.example.com/tasks/claim');
    expect(JSON.parse(options.body as string)).toEqual({
      workerId: 'worker-1', browserProfileId: 'profile-a',
    });
  });

  it('omits browserProfileId for an unprofiled worker', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const transport = createTransport();

    await expect(transport.claimTask()).resolves.toBeNull();

    expect(JSON.parse(lastCall()![1].body as string)).toEqual({ workerId: 'worker-1' });
  });

  it('rejects a mismatched profile claim before execution', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({
      taskId: 'task-profile', ruleId: 'rule-1', ruleVersion: '1.0.0',
      browserProfileId: 'profile-b', variables: {},
    }), { status: 200 }));
    const transport = createTransport(undefined, undefined, { browserProfileId: 'profile-a' });

    await expect(transport.claimTask()).rejects.toMatchObject({
      status: 409,
      message: 'claim profile mismatch: worker profile profile-a cannot execute task profile profile-b',
    });
  });

  it('waits for an approved checkpoint-bound human intervention', async () => {
    fetchMock
      .mockResolvedValueOnce(new Response(JSON.stringify({ intervention: {
        id: 'human-1', taskId: 'task-1', attemptId: 'attempt-1', checkpointId: 'checkpoint-1',
        status: 'pending', expiresAt: '2026-01-01T00:01:00Z',
      } }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ intervention: {
        id: 'human-1', taskId: 'task-1', attemptId: 'attempt-1', checkpointId: 'checkpoint-1',
        status: 'approved', expiresAt: '2026-01-01T00:01:00Z',
      } }), { status: 200 }));
    const transport = createTransport('secret-key', undefined, {
      resultState: { attemptId: 'attempt-1', nextSequence: 0 }, humanPollIntervalMs: 0,
    });

    await expect(transport.requestHuman({
      type: '2fa', prompt: 'Complete 2FA', timeoutMs: 60_000,
      checkpoint: { stepId: 'two-factor', url: 'https://example.com/account' },
    })).resolves.toMatchObject({ status: 'approved', checkpointId: 'checkpoint-1' });

    const [createURL, createInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(createURL).toBe('https://api.example.com/tasks/task-1/human-interventions');
    expect(JSON.parse(createInit.body as string)).toEqual({
      workerId: 'worker-1', attemptId: 'attempt-1', type: '2fa', prompt: 'Complete 2FA', timeoutMs: 60_000,
      checkpoint: { stepId: 'two-factor', url: 'https://example.com/account' },
    });
    expect(String(fetchMock.mock.calls[1][0])).toContain(
      '/tasks/task-1/human-interventions/human-1?workerId=worker-1&attemptId=attempt-1',
    );
  });

  it('fails closed for rejected, expired, cancelled, and attempt-less interventions', async () => {
    const statuses = ['rejected', 'expired', 'cancelled'] as const;
    for (const status of statuses) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ intervention: {
        id: `human-${status}`, taskId: 'task-1', attemptId: 'attempt-1', checkpointId: 'checkpoint-1',
        status, expiresAt: '2026-01-01T00:01:00Z',
      } }), { status: 201 }));
      const transport = createTransport(undefined, undefined, {
        resultState: { attemptId: 'attempt-1', nextSequence: 0 }, humanPollIntervalMs: 0,
      });
      await expect(transport.requestHuman({
        type: 'generic', prompt: 'help', timeoutMs: 60_000,
        checkpoint: { stepId: 'manual', url: 'https://example.com/' },
      })).rejects.toThrow(status === 'rejected' ? 'HumanRejected' : status === 'expired' ? 'HumanTimeout' : 'HumanInterventionCancelled');
    }
    const legacy = createTransport();
    await expect(legacy.requestHuman({
      type: 'generic', prompt: 'help', timeoutMs: 60_000,
      checkpoint: { stepId: 'manual', url: 'https://example.com/' },
    })).rejects.toThrow('execution attempt id is required');
  });

  it('fails closed when intervention creation or polling fails', async () => {
    const request = {
      type: 'generic' as const, prompt: 'help', timeoutMs: 60_000,
      checkpoint: { stepId: 'manual', url: 'https://example.com/' },
    };
    const options = { resultState: { attemptId: 'attempt-1', nextSequence: 0 }, humanPollIntervalMs: 0 };

    fetchMock.mockResolvedValueOnce(new Response('conflict', { status: 409 }));
    await expect(createTransport(undefined, undefined, options).requestHuman(request))
      .rejects.toThrow('human intervention request failed: 409');

    fetchMock.mockReset();
    fetchMock
      .mockResolvedValueOnce(new Response(JSON.stringify({ intervention: {
        id: 'human-poll', checkpointId: 'checkpoint-1', status: 'pending', expiresAt: '2026-01-01T00:01:00Z',
      } }), { status: 201 }))
      .mockResolvedValueOnce(new Response('unavailable', { status: 503 }));
    await expect(createTransport(undefined, undefined, options).requestHuman(request))
      .rejects.toThrow('human intervention poll failed: 503');
  });

  it('rejects an unknown intervention state', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ intervention: {
      id: 'human-invalid', checkpointId: 'checkpoint-1', status: 'unexpected', expiresAt: '2026-01-01T00:01:00Z',
    } }), { status: 201 }));
    const transport = createTransport(undefined, undefined, {
      resultState: { attemptId: 'attempt-1', nextSequence: 0 }, humanPollIntervalMs: 0,
    });
    await expect(transport.requestHuman({
      type: 'generic', prompt: 'help', timeoutMs: 60_000,
      checkpoint: { stepId: 'manual', url: 'https://example.com/' },
    })).rejects.toThrow('HumanInterventionInvalidStatus: unexpected');
  });

  it('sendResult POSTs to /results with correct body and Authorization header', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key');
    const payload = { status: 'success', data: { items: [1, 2] } };
    await transport.sendResult(payload, true);

    const [url, options] = lastCall()!;
    expect(url).toBe('https://api.example.com/results');
    expect(options.method).toBe('POST');
    expect(options.headers).toMatchObject({
      'Content-Type': 'application/json',
      Authorization: 'Bearer secret-key',
    });
    expect(JSON.parse(options.body as string)).toEqual({
      taskId: 'task-1',
      workerId: 'worker-1',
      immediate: true,
      payload,
    });
  });

  it('drains successful POST response bodies for connection reuse', async () => {
    const consume = vi.fn(async () => '{"success":true}');
    fetchMock.mockResolvedValue({ ok: true, text: consume });
    const transport = createTransport('secret-key');

    await transport.sendLog('info', 'connection can be reused');

    expect(consume).toHaveBeenCalledOnce();
  });

  it('uses one monotonic idempotent result stream for batches and final summary', async () => {
    fetchMock.mockImplementation(async () => resultAck());
    const resultState = { attemptId: 'attempt-1', nextSequence: 0 };
    const transport = createTransport('secret-key', undefined, { resultState });

    await transport.sendResult([{ name: 'A' }]);
    await transport.sendResult([{ name: 'B' }], true);
    await transport.sendFinalSummary({ status: 'success', rowCount: 2 });

    const bodies = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/results'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(bodies).toEqual([
      expect.objectContaining({
        taskId: 'task-1', workerId: 'worker-1', attemptId: 'attempt-1',
        idempotencyKey: 'attempt-1:1:batch', sequence: 1, kind: 'batch',
        immediate: false, payload: [{ name: 'A' }],
      }),
      expect.objectContaining({
        attemptId: 'attempt-1', idempotencyKey: 'attempt-1:2:batch',
        sequence: 2, kind: 'batch', immediate: true, payload: [{ name: 'B' }],
      }),
      expect.objectContaining({
        attemptId: 'attempt-1', idempotencyKey: 'attempt-1:3:summary',
        sequence: 3, kind: 'summary', immediate: true,
        payload: { status: 'success', rowCount: 2 },
      }),
    ]);
    expect(resultState.nextSequence).toBe(3);
  });

  it('does not submit the executor final marker as versioned collection data', async () => {
    fetchMock.mockResolvedValue(resultAck());
    const resultState = { attemptId: 'attempt-1', nextSequence: 0 };
    const transport = createTransport('secret-key', undefined, { resultState });

    await transport.sendResult({ __final: true, status: 'completed' }, true);
    await transport.sendFinalSummary({ status: 'success', rowCount: 0 });

    const bodies = fetchMock.mock.calls.map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(bodies).toEqual([
      expect.objectContaining({
        attemptId: 'attempt-1', idempotencyKey: 'attempt-1:1:summary',
        sequence: 1, kind: 'summary', payload: { status: 'success', rowCount: 0 },
      }),
    ]);
    expect(resultState.nextSequence).toBe(1);
  });

  it('preserves the legacy final marker wire behavior without attempt state', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key');

    await transport.sendResult({ __final: true, status: 'completed' }, true);

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(JSON.parse(lastCall()![1].body as string)).toMatchObject({
      immediate: true,
      payload: { __final: true, status: 'completed' },
    });
  });

  it('leaves versioned terminal task status ownership to the worker', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key', undefined, {
      resultState: { attemptId: 'attempt-1', nextSequence: 0 },
    });

    await transport.sendStatus('running', 'executor started');
    await transport.sendStatus('success', 'executor completed');
    await transport.sendStatus('done', 'worker completed');

    const bodies = fetchMock.mock.calls.map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(bodies.map((body) => body.status)).toEqual(['running', 'done']);
  });

  it('preserves legacy execution terminal statuses without attempt state', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key');

    await transport.sendStatus('success', 'legacy executor completed');

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(JSON.parse(lastCall()![1].body as string)).toMatchObject({ status: 'success' });
  });

  it('keeps the versioned idempotency envelope unchanged while retrying', async () => {
    fetchMock
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce(resultAck(true));
    const transport = createTransport(undefined, undefined, {
      maxAttempts: 2,
      retryDelayMs: 0,
      resultState: { attemptId: 'attempt-retry', nextSequence: 0 },
    });

    await transport.sendResult([{ name: 'A' }]);

    const bodies = fetchMock.mock.calls.map(([, init]) => (init as RequestInit).body as string);
    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]);
    expect(JSON.parse(bodies[0])).toMatchObject({
      idempotencyKey: 'attempt-retry:1:batch', sequence: 1, kind: 'batch',
    });
  });

  it('reuses the pending envelope when a later flush retries a lost response', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock
      .mockRejectedValueOnce(new Error('response lost after commit'))
      .mockResolvedValueOnce(resultAck(true));
    const resultState = { attemptId: 'attempt-lost', nextSequence: 0 };
    const transport = createTransport(undefined, undefined, {
      maxAttempts: 1, resultState,
    });
    const batch = [{ name: 'A' }];

    await expect(transport.sendResult(batch, true)).rejects.toThrow('response lost after commit');
    await expect(transport.sendResult([{ name: 'A' }], true)).resolves.toBeUndefined();

    const bodies = fetchMock.mock.calls.map(([, init]) => (init as RequestInit).body as string);
    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toBe(bodies[0]);
    expect(resultState.nextSequence).toBe(1);
    errorSpy.mockRestore();
  });

  it('fails closed on an invalid HTTP-200 result acknowledgement', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock.mockResolvedValue(new Response(JSON.stringify({
      success: false, valid: false, duplicate: false, error: 'missing required price',
    }), { status: 200 }));
    const transport = createTransport(undefined, undefined, {
      resultState: { attemptId: 'attempt-invalid', nextSequence: 0 },
    });

    await expect(transport.sendResult([{ name: 'Book' }]))
      .rejects.toThrow('result rejected: missing required price');
    expect(fetchMock).toHaveBeenCalledOnce();
    fetchMock.mockResolvedValueOnce(resultAck());
    await expect(transport.sendFinalSummary({ status: 'failure' })).resolves.toBeUndefined();
    expect(JSON.parse(lastCall()![1].body as string)).toMatchObject({
      idempotencyKey: 'attempt-invalid:2:summary', sequence: 2, kind: 'summary',
    });
    errorSpy.mockRestore();
  });

  it('suppresses every versioned internal control marker', async () => {
    const transport = createTransport(undefined, undefined, {
      resultState: { attemptId: 'attempt-controls', nextSequence: 0 },
    });
    await transport.sendResult({ __final: true, taskId: 'task-1' }, true);
    await transport.sendResult({ __flush: true, taskId: 'task-1' }, true);
    await transport.sendResult({ __criticalFailure: true, error: 'boom' }, true);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('sendLog POSTs to /logs with correct body and Authorization header', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key');
    await transport.sendLog('error', 'something went wrong', { detail: 'x' });

    const [url, options] = lastCall()!;
    expect(url).toBe('https://api.example.com/logs');
    expect(options.method).toBe('POST');
    expect(options.headers).toMatchObject({
      'Content-Type': 'application/json',
      Authorization: 'Bearer secret-key',
    });
    expect(JSON.parse(options.body as string)).toEqual({
      taskId: 'task-1',
      workerId: 'worker-1',
      level: 'error',
      message: 'something went wrong',
      extra: { detail: 'x' },
    });
  });

  it('sendHeartbeat POSTs to /heartbeat with correct body and Authorization header', async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ cancelRequested: false }), text: async () => '' });
    const transport = createTransport('secret-key');
    await transport.sendHeartbeat({ progress: 50 });

    const [url, options] = lastCall()!;
    expect(url).toBe('https://api.example.com/heartbeat');
    expect(options.method).toBe('POST');
    expect(options.headers).toMatchObject({
      'Content-Type': 'application/json',
      Authorization: 'Bearer secret-key',
    });
    expect(JSON.parse(options.body as string)).toEqual({
      taskId: 'task-1',
      workerId: 'worker-1',
      payload: { progress: 50 },
    });
  });

  it('sendHeartbeat returns cancelRequested from server response', async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ cancelRequested: true }), text: async () => '' });
    const transport = createTransport('secret-key');
    const result = await transport.sendHeartbeat({ progress: 50 });
    expect(result).toEqual({ cancelRequested: true });
  });

  it('sendStatus POSTs to /status with correct body and Authorization header', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key');
    await transport.sendStatus('running', 'Task started');

    const [url, options] = lastCall()!;
    expect(url).toBe('https://api.example.com/status');
    expect(options.method).toBe('POST');
    expect(options.headers).toMatchObject({
      'Content-Type': 'application/json',
      Authorization: 'Bearer secret-key',
    });
    expect(JSON.parse(options.body as string)).toEqual({
      taskId: 'task-1',
      workerId: 'worker-1',
      status: 'running',
      message: 'Task started',
    });
  });

  it('sends attempt lineage and fails closed for versioned terminal status', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 409, text: async () => 'incomplete results' });
    const transport = createTransport(undefined, undefined, {
      maxAttempts: 1,
      resultState: { attemptId: 'attempt-terminal', nextSequence: 0 },
    });
    await expect(transport.sendStatus('done', 'complete')).rejects.toThrow('HTTP 409');
    expect(JSON.parse(lastCall()![1].body as string)).toMatchObject({
      taskId: 'task-1', workerId: 'worker-1', attemptId: 'attempt-terminal', status: 'done',
    });
  });

  it('sendSnapshot POSTs to /snapshots with correct body and Authorization header', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport('secret-key');
    await transport.sendSnapshot({ name: 'page', type: 'html', data: '<html></html>' });

    const [url, options] = lastCall()!;
    expect(url).toBe('https://api.example.com/snapshots');
    expect(options.method).toBe('POST');
    expect(options.headers).toMatchObject({
      'Content-Type': 'application/json',
      Authorization: 'Bearer secret-key',
    });
    expect(JSON.parse(options.body as string)).toEqual({
      taskId: 'task-1',
      workerId: 'worker-1',
      name: 'page',
      type: 'html',
      data: '<html></html>',
    });
  });

  it('omits Authorization header when apiKey is not set', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport();
    await transport.sendResult({ ok: true });

    const [, options] = lastCall()!;
    expect(options.headers).not.toHaveProperty('Authorization');
  });

  it('fetchRule returns parsed rule on 200', async () => {
    const rule: Rule = {
      id: 'rule-1',
      version: '1.0.0',
      name: 'Test Rule',
      domain: 'example.com',
      enabled: true,
      steps: [],
    };
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => rule,
    });
    const transport = createTransport('secret-key');
    const result = await transport.fetchRule('rule-1');

    expect(result).toEqual(rule);
    const [url, options] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('https://api.example.com/rules/rule-1');
    expect(options.headers).toMatchObject({ Authorization: 'Bearer secret-key' });
  });

  it('fetchRule returns null on non-200', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 404, text: async () => 'not found' });
    const transport = createTransport();
    const result = await transport.fetchRule('missing');
    expect(result).toBeNull();
  });

  it('sendResult retries on 5xx and succeeds after retries', async () => {
    vi.useFakeTimers();
    fetchMock
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce({ ok: false, status: 503, text: async () => 'unavailable' })
      .mockResolvedValueOnce({ ok: true, text: async () => '' });

    const transport = createTransport();
    const sendPromise = transport.sendResult({ ok: true });
    await vi.advanceTimersByTimeAsync(30);
    await sendPromise;

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls.every(([url]) => String(url) === 'https://api.example.com/results')).toBe(true);
  });

  it('sendResult does not retry 4xx client errors and swallows failure', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock.mockResolvedValue({ ok: false, status: 400, text: async () => 'bad request' });

    const transport = createTransport();
    await expect(transport.sendResult({ ok: true })).resolves.toBeUndefined();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    errorSpy.mockRestore();
  });

  it('sendLog buffers failed sends and flushes on the next successful send', async () => {
    fetchMock
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce({ ok: true, text: async () => '' })
      .mockResolvedValueOnce({ ok: true, text: async () => '' });

    const transport = createTransport(undefined, undefined, { maxAttempts: 1 });
    await transport.sendLog('info', 'first');
    await transport.sendLog('info', 'second');

    const logCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/logs'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(logCalls).toHaveLength(3);
    expect(logCalls[0].message).toBe('first');
    expect(logCalls[1].message).toBe('first');
    expect(logCalls[2].message).toBe('second');
  });

  it('sendStatus buffers up to capacity and drops oldest entries', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock.mockRejectedValue(new Error('network error'));

    const transport = createTransport(undefined, undefined, { bufferCapacity: 2, maxAttempts: 1 });
    await transport.sendStatus('running', 'status-1');
    await transport.sendStatus('running', 'status-2');
    await transport.sendStatus('running', 'status-3');

    fetchMock.mockReset();
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    await transport.sendStatus('running', 'status-flush');

    const statusCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/status'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(statusCalls).toHaveLength(3);
    expect(statusCalls[0].message).toBe('status-2');
    expect(statusCalls[1].message).toBe('status-3');
    expect(statusCalls[2].message).toBe('status-flush');
    expect(statusCalls).not.toContainEqual(expect.objectContaining({ message: 'status-1' }));
    errorSpy.mockRestore();
  });

  it('sendHeartbeat retries and swallows final failure without buffering', async () => {
    vi.useFakeTimers();
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock.mockRejectedValue(new Error('network error'));

    const transport = createTransport();
    const sendPromise = transport.sendHeartbeat({ ts: 1 });
    await vi.advanceTimersByTimeAsync(100);
    await expect(sendPromise).resolves.toEqual({ cancelRequested: false });

    expect(fetchMock).toHaveBeenCalledTimes(3);
    errorSpy.mockRestore();
  });

  it('retries on network error and 5xx but fails immediately on 4xx', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce({ ok: false, status: 503, text: async () => 'unavailable' })
      .mockResolvedValueOnce({ ok: false, status: 400, text: async () => 'bad request' });

    const transport = createTransport(undefined, undefined, { maxAttempts: 3, retryDelayMs: 10 });
    await expect(transport.sendResult({ ok: true })).resolves.toBeUndefined();

    expect(fetchMock).toHaveBeenCalledTimes(3);
    errorSpy.mockRestore();
  });

  it('serializes concurrent sendLog to avoid duplicate or skipped buffered items', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    fetchMock.mockRejectedValue(new Error('network error'));

    const transport = createTransport(undefined, undefined, { maxAttempts: 1, retryDelayMs: 10 });
    await Promise.all([
      transport.sendLog('info', 'log-1'),
      transport.sendLog('info', 'log-2'),
      transport.sendLog('info', 'log-3'),
    ]);

    fetchMock.mockReset();
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    await transport.sendLog('info', 'flush');

    const logCalls = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith('/logs'))
      .map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(logCalls).toHaveLength(4);
    expect(logCalls.map((c) => c.message)).toEqual(['log-1', 'log-2', 'log-3', 'flush']);
    errorSpy.mockRestore();
  });

  it('sendSnapshot still throws on non-2xx responses', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 500, text: async () => 'server error' });
    const transport = createTransport();
    await expect(transport.sendSnapshot({ name: 'page', type: 'html', data: '<html></html>' })).rejects.toThrow('HTTP 500: server error');
  });

  it('sends provided traceId in X-Trace-Id header', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport(undefined, 'trace-abc-123');
    await transport.sendResult({ ok: true });

    const [, options] = lastCall()!;
    expect(options.headers).toMatchObject({ 'X-Trace-Id': 'trace-abc-123' });
  });

  it('reuses traceId from response header for subsequent requests', async () => {
    fetchMock
      .mockResolvedValueOnce(mockResponseWithTraceId('server-trace-1'))
      .mockResolvedValueOnce({ ok: true, json: async () => ({ cancelRequested: false }), text: async () => '' });
    const transport = createTransport();
    await transport.sendResult({ ok: true });
    await transport.sendHeartbeat({ progress: 10 });

    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    expect(calls[0][1].headers).not.toHaveProperty('X-Trace-Id');
    expect(calls[1][1].headers).toMatchObject({ 'X-Trace-Id': 'server-trace-1' });
  });

  it('includes traceId in sendLog payload', async () => {
    fetchMock.mockResolvedValue({ ok: true, text: async () => '' });
    const transport = createTransport(undefined, 'trace-log-1');
    await transport.sendLog('info', 'hello');

    const [, options] = lastCall()!;
    expect(JSON.parse(options.body as string)).toMatchObject({ traceId: 'trace-log-1' });
  });
});
