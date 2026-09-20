import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  listRules, verifyKey, createTask, createSchedule, listSchedules, getSchedule,
  updateSchedule, deleteSchedule, triggerSchedule, predictIntent, listRuleVersions,
  getTaskResults, previewSchedule,
  listMCPTokens, createMCPToken, revokeMCPToken,
  updateRule, deleteRule, approveRule, rejectRule, getTask, cancelTask, retryTask,
} from './client';

describe('api client', () => {
  beforeEach(() => {
    sessionStorage.setItem('adminApiKey', 'test-key');
  });

  afterEach(() => {
    sessionStorage.removeItem('adminApiKey');
    vi.restoreAllMocks();
  });

  it('sends Authorization header with stored key', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ rules: [], total: 0 }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await listRules();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.headers).toMatchObject({ Authorization: 'Bearer test-key' });
  });

  it('returns false when verifyKey fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 401 }));
    const ok = await verifyKey();
    expect(ok).toBe(false);
  });

  it('throws on non-ok response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 500,
        json: async () => ({ error: 'internal error' }),
      })
    );
    await expect(listRules()).rejects.toThrow('internal error');
  });

  it('creates a single task with scheduledAt', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ taskId: 'task-123' }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await createTask({
      ruleId: 'rule-1',
      ruleVersionNumber: 2,
      priority: 'high',
      maxRetries: 5,
      scheduledAt: '2026-01-01T00:00:00.000Z',
    });

    expect(result).toEqual({ taskId: 'task-123' });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/admin/tasks');
    expect(init.method).toBe('POST');
    expect(init.headers).toMatchObject({ Authorization: 'Bearer test-key' });
    expect(JSON.parse(init.body as string)).toMatchObject({ ruleId: 'rule-1', scheduledAt: '2026-01-01T00:00:00.000Z' });
  });

  it('lists immutable rule versions and their contracts', async () => {
    const response = { ruleVersions: [{ ruleId: 'rule-1', version: 2 }], contracts: [{ ruleId: 'rule-1', version: 2 }] };
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => response });
    vi.stubGlobal('fetch', fetchMock);

    await expect(listRuleVersions('rule/1')).resolves.toEqual(response);
    expect(fetchMock.mock.calls[0][0]).toBe('/admin/rules/rule%2F1/versions');
  });

  it('gets paginated versioned task results with invalid diagnostics', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true, status: 200,
      json: async () => ({ taskId: 'task-1', page: { batches: [], total: 0, limit: 25, offset: 25 } }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await getTaskResults('task/1', { limit: 25, offset: 25, include_invalid: true });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/tasks/task%2F1/results?limit=25&offset=25&include_invalid=true');
  });

  it('creates a schedule', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        id: 'schedule-1',
        ruleId: 'rule-1',
        ruleVersion: '1.0.0',
        name: '每小时执行',
        type: 'cron',
        expression: '0 * * * *',
        enabled: true,
        variables: {},
        priority: 'normal',
        maxRetries: 3,
        catchup: 'skip',
        createdAt: '2026-01-01T00:00:00Z',
        updatedAt: '2026-01-01T00:00:00Z',
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const result = await createSchedule({
      ruleId: 'rule-1',
      name: '每小时执行',
      type: 'cron',
      expression: '0 * * * *',
      enabled: true,
      priority: 'normal',
      maxRetries: 3,
      catchup: 'skip',
    });

    expect(result.id).toBe('schedule-1');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/admin/schedules');
    expect(init.method).toBe('POST');
  });

  it('lists schedules with filters', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ schedules: [], total: 0 }),
    }));

    await listSchedules({ rule_id: 'rule-1', enabled: 'true', limit: 10, offset: 0 });

    const [url] = (vi.mocked(fetch).mock.calls[0] || []) as [string];
    expect(url).toContain('/admin/schedules?');
    expect(url).toContain('rule_id=rule-1');
    expect(url).toContain('enabled=true');
    expect(url).toContain('limit=10');
    expect(url).toContain('offset=0');
  });

  it('gets a schedule by id', async () => {
    const schedule = {
      id: 'schedule-1',
      ruleId: 'rule-1',
      ruleVersion: '1.0.0',
      name: '测试计划',
      type: 'cron',
      expression: '*/5 * * * *',
      enabled: true,
      variables: {},
      priority: 'normal',
      maxRetries: 3,
      catchup: 'skip',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    };
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => schedule,
    }));

    const result = await getSchedule('schedule-1');
    expect(result).toEqual(schedule);
  });

  it('updates a schedule', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ id: 'schedule-1', enabled: false }),
    }));

    const result = await updateSchedule('schedule-1', { enabled: false });
    expect(result.id).toBe('schedule-1');
    const [url, init] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/admin/schedules/schedule-1');
    expect(init.method).toBe('PATCH');
  });

  it('deletes a schedule', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 204,
    }));

    await expect(deleteSchedule('schedule-1')).resolves.toBeUndefined();
    const [url, init] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/admin/schedules/schedule-1');
    expect(init.method).toBe('DELETE');
  });

  it('triggers a schedule', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ taskId: 'task-xyz' }),
    }));

    const result = await triggerSchedule('schedule-1');
    expect(result).toEqual({ taskId: 'task-xyz' });
    const [url, init] = vi.mocked(fetch).mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/admin/schedules/schedule-1/trigger');
    expect(init.method).toBe('POST');
  });

  it('previews a cron expression in an IANA timezone', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true, status: 200,
      json: async () => ({ expression: '0 * * * *', timezone: 'Asia/Taipei', runs: [] }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await previewSchedule('0 * * * *', 5, 'Asia/Taipei', '2026-01-01T00:00:00Z');
    const url = fetchMock.mock.calls[0][0] as string;
    expect(url).toContain('expression=0+*+*+*+*');
    expect(url).toContain('timezone=Asia%2FTaipei');
    expect(url).toContain('after=2026-01-01T00%3A00%3A00Z');
  });

  it('predicts intent from recording', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        candidates: [{ id: 'c1', label: '采集标题', description: '...', confidence: 0.9 }],
        fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
        model: 'gpt-4o',
        cacheHit: false,
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const recording = { meta: { startUrl: 'https://example.com' }, events: [] };
    const result = await predictIntent(recording);

    expect(result.candidates).toHaveLength(1);
    expect(result.model).toBe('gpt-4o');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/admin/rules/predict-intent');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ recording });
  });

  it('manages MCP token metadata without changing the returned token', async () => {
    const token = {
      id: 'token/1', workspaceId: 'default', name: 'automation', tokenPrefix: 'aegis_mcp_abcd',
      permissions: ['read', 'write'], createdBy: 'admin', createdAt: '2026-01-01T00:00:00Z', token: 'aegis_mcp_plaintext',
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ tokens: [token] }) })
      .mockResolvedValueOnce({ ok: true, status: 201, json: async () => token })
      .mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ success: true }) });
    vi.stubGlobal('fetch', fetchMock);

    await expect(listMCPTokens()).resolves.toEqual({ tokens: [token] });
    await expect(createMCPToken({ name: 'automation', permissions: ['read', 'write'] })).resolves.toEqual(token);
    await expect(revokeMCPToken('token/1')).resolves.toEqual({ success: true });

    expect(fetchMock.mock.calls[0][0]).toBe('/admin/mcp/tokens');
    expect(fetchMock.mock.calls[1][0]).toBe('/admin/mcp/tokens');
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe('POST');
    expect(fetchMock.mock.calls[2][0]).toBe('/admin/mcp/tokens/token%2F1');
    expect((fetchMock.mock.calls[2][1] as RequestInit).method).toBe('DELETE');
  });

  it('encodes path parameters with special characters in all ID-bearing functions (M-7)', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ success: true }),
    });
    vi.stubGlobal('fetch', fetchMock);
    const evilId = 'id/with?special#chars';
    const encoded = encodeURIComponent(evilId);

    // Each of these previously interpolated the raw ID into the URL path,
    // allowing path traversal and query injection via crafted IDs.
    await updateRule(evilId, { name: 'test' });
    await deleteRule(evilId);
    await approveRule(evilId);
    await rejectRule(evilId);

    await getTask(evilId);
    await cancelTask(evilId);
    await retryTask(evilId);

    await getSchedule(evilId);
    await updateSchedule(evilId, { ruleId: 'r1', type: 'once' });
    await deleteSchedule(evilId);
    await triggerSchedule(evilId);

    for (const call of fetchMock.mock.calls) {
      const url = call[0] as string;
      // The raw ID must never appear unencoded in any URL.
      expect(url).not.toContain(evilId);
      // The encoded form must be present.
      expect(url).toContain(encoded);
    }
  });
});
