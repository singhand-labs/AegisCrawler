import type { Rule, Task, LogEntry, ListRulesResponse, ListTasksResponse, ListAuditLogsResponse, UpdateRuleRequest, Schedule, ListSchedulesResponse, ScheduleType, CatchupMode, Priority, PreviewScheduleResponse, RuleEnhancement, LLMJob, CreateTaskRequest, ListRuleVersionsResponse, TaskResultsResponse, MCPPermission, MCPToken, ListMCPTokensResponse, HumanIntervention, ListHumanInterventionsResponse } from './types';

const BASE_URL = '';

// H-1: per-request timeout. Without this, a slow or hung server (TCP
// established but no response) blocks every admin-UI page's fetch promise
// indefinitely, leaving the user on a permanent spinner.
const REQUEST_TIMEOUT_MS = 30_000;

function getKey(): string | null {
  return sessionStorage.getItem('adminApiKey');
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const key = getKey();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string>),
  };
  if (key) {
    headers['Authorization'] = `Bearer ${key}`;
  }

  // H-1: race the fetch against a timeout; respect a caller-provided signal.
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  if (options.signal) {
    const callerSignal = options.signal;
    callerSignal.addEventListener('abort', () => controller.abort(), { once: true });
  }
  let response: Response;
  try {
    response = await fetch(`${BASE_URL}${path}`, {
      ...options,
      headers,
      signal: controller.signal,
    });
  } catch (err) {
    if (controller.signal.aborted && !(err instanceof DOMException && err.name === 'AbortError' && options.signal?.aborted)) {
      throw new Error(`请求超时（${REQUEST_TIMEOUT_MS / 1000}s），请检查网络或服务端状态`);
    }
    throw err;
  } finally {
    clearTimeout(timeout);
  }

  if (response.status === 401) {
    sessionStorage.removeItem('adminApiKey');
    window.location.reload();
    throw new Error('登录已失效，请重新登录');
  }

  if (!response.ok) {
    let message = `请求失败：${response.status}`;
    try {
      const body = (await response.json()) as { error?: string; message?: string };
      if (body.error || body.message) {
        message = body.error || body.message || message;
      }
    } catch {
      // ignore
    }
    throw new Error(message);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return (await response.json()) as T;
}

export async function verifyKey(): Promise<boolean> {
  try {
    await request('/admin/rules?limit=1');
    return true;
  } catch {
    return false;
  }
}

// Rules
export async function listRules(params?: {
  enabled?: boolean;
  approval_status?: string;
  domain?: string;
  owner?: string;
  limit?: number;
  offset?: number;
}): Promise<ListRulesResponse> {
  const qs = new URLSearchParams();
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        qs.set(k, String(v));
      }
    });
  }
  return request<ListRulesResponse>(`/admin/rules?${qs.toString()}`);
}

export async function getRule(id: string): Promise<Rule> {
  return request<Rule>(`/admin/rules/${encodeURIComponent(id)}`);
}

export async function listRuleVersions(id: string): Promise<ListRuleVersionsResponse> {
  return request<ListRuleVersionsResponse>(`/admin/rules/${encodeURIComponent(id)}/versions`);
}

export async function createRule(rule: Rule): Promise<{ success: boolean }> {
  return request<{ success: boolean }>('/admin/rules', {
    method: 'POST',
    body: JSON.stringify(rule),
  });
}

export async function updateRule(id: string, patch: UpdateRuleRequest): Promise<Rule> {
  return request<Rule>(`/admin/rules/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  });
}

export async function deleteRule(id: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/admin/rules/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function approveRule(id: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/admin/rules/${encodeURIComponent(id)}/approve`, { method: 'POST' });
}

export async function rejectRule(id: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/admin/rules/${encodeURIComponent(id)}/reject`, { method: 'POST' });
}

export async function predictIntent(recording: Record<string, unknown>): Promise<{
  candidates: Array<{
    id: string;
    label: string;
    description: string;
    confidence: number;
    suggestedVariables?: string[];
  }>;
  fallbackIntent: {
    id: string;
    label: string;
    description: string;
    confidence: number;
  };
  model: string;
  cacheHit: boolean;
}> {
  return request('/admin/rules/predict-intent', {
    method: 'POST',
    body: JSON.stringify({ recording }),
  });
}

export async function enhanceRule(data: { recording: Record<string, unknown>; baselineRule: Rule; userHint: string }): Promise<{ jobId: string; statusUrl: string }> {
  return request('/admin/rules/enhance', { method: 'POST', body: JSON.stringify(data) });
}

export async function getEnhancementJob(jobId: string): Promise<LLMJob> {
  return request<LLMJob>(`/admin/rules/enhancements/jobs/${encodeURIComponent(jobId)}`);
}

export async function getRuleEnhancement(ruleId: string): Promise<RuleEnhancement> {
  return request<RuleEnhancement>(`/admin/rules/${encodeURIComponent(ruleId)}/enhancement`);
}

export async function acceptEnhancement(ruleId: string): Promise<Rule> {
  return request<Rule>(`/admin/rules/${encodeURIComponent(ruleId)}/enhancement/accept`, { method: 'POST' });
}

export async function rejectEnhancement(ruleId: string): Promise<void> {
  return request<void>(`/admin/rules/${encodeURIComponent(ruleId)}/enhancement/reject`, { method: 'POST' });
}

// Tasks
export async function listTasks(params?: {
  status?: string;
  rule_id?: string;
  worker_id?: string;
  priority?: string;
  created_after?: string;
  created_before?: string;
  limit?: number;
  offset?: number;
}): Promise<ListTasksResponse> {
  const qs = new URLSearchParams();
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        qs.set(k, String(v));
      }
    });
  }
  return request<ListTasksResponse>(`/admin/tasks?${qs.toString()}`);
}

export async function createTask(task: CreateTaskRequest): Promise<{ taskId: string }> {
  return request<{ taskId: string }>('/admin/tasks', {
    method: 'POST',
    body: JSON.stringify(task),
  });
}

export async function getTask(id: string): Promise<Task> {
  return request<Task>(`/admin/tasks/${encodeURIComponent(id)}`);
}

export async function cancelTask(id: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/admin/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
}

export async function retryTask(id: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/admin/tasks/${encodeURIComponent(id)}/retry`, { method: 'POST' });
}

export async function getTaskResults(id: string, params?: {
  limit?: number;
  offset?: number;
  include_invalid?: boolean;
}): Promise<TaskResultsResponse> {
  const qs = new URLSearchParams();
  if (params?.limit !== undefined) qs.set('limit', String(params.limit));
  if (params?.offset !== undefined) qs.set('offset', String(params.offset));
  if (params?.include_invalid !== undefined) qs.set('include_invalid', String(params.include_invalid));
  return request<TaskResultsResponse>(`/api/v1/tasks/${encodeURIComponent(id)}/results?${qs.toString()}`);
}

export async function getTaskLogs(id: string): Promise<LogEntry[]> {
  return request<LogEntry[]>(`/admin/tasks/${encodeURIComponent(id)}/logs`);
}

export async function listTaskHumanInterventions(id: string): Promise<ListHumanInterventionsResponse> {
  return request<ListHumanInterventionsResponse>(`/admin/tasks/${encodeURIComponent(id)}/human-interventions`);
}

export async function decideHumanIntervention(
  taskId: string,
  interventionId: string,
  decision: 'approved' | 'rejected',
  checkpointId: string,
  note?: string,
): Promise<{ intervention: HumanIntervention }> {
  return request<{ intervention: HumanIntervention }>(
    `/admin/tasks/${encodeURIComponent(taskId)}/human-interventions/${encodeURIComponent(interventionId)}/decision`,
    { method: 'POST', body: JSON.stringify({ decision, checkpointId, note }) },
  );
}

// Schedules
export interface CreateScheduleRequest {
  ruleId: string;
  ruleVersion?: string;
  ruleVersionNumber?: number;
  name: string;
  type: ScheduleType;
  expression: string;
  enabled?: boolean;
  variables?: Record<string, unknown>;
  priority?: Priority;
  maxRetries?: number;
  catchup?: CatchupMode;
  browserProfileId?: string;
  timezone?: string;
}

export async function createSchedule(data: CreateScheduleRequest): Promise<Schedule> {
  return request<Schedule>('/admin/schedules', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function listSchedules(params?: {
  rule_id?: string;
  type?: string;
  enabled?: string;
  limit?: number;
  offset?: number;
}): Promise<ListSchedulesResponse> {
  const qs = new URLSearchParams();
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        qs.set(k, String(v));
      }
    });
  }
  return request<ListSchedulesResponse>(`/admin/schedules?${qs.toString()}`);
}

export async function getSchedule(id: string): Promise<Schedule> {
  return request<Schedule>(`/admin/schedules/${encodeURIComponent(id)}`);
}

export async function updateSchedule(id: string, patch: Partial<CreateScheduleRequest> & { enabled?: boolean }): Promise<Schedule> {
  return request<Schedule>(`/admin/schedules/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  });
}

export async function deleteSchedule(id: string): Promise<void> {
  return request<void>(`/admin/schedules/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function triggerSchedule(id: string): Promise<{ taskId: string }> {
  return request<{ taskId: string }>(`/admin/schedules/${encodeURIComponent(id)}/trigger`, { method: 'POST' });
}

export async function previewSchedule(expression: string, count?: number, timezone?: string, after?: string): Promise<PreviewScheduleResponse> {
  const qs = new URLSearchParams();
  qs.set('expression', expression);
  if (count !== undefined) qs.set('count', String(count));
  if (timezone) qs.set('timezone', timezone);
  if (after) qs.set('after', after);
  return request<PreviewScheduleResponse>(`/admin/schedules/preview?${qs.toString()}`);
}

// Audit logs
export async function listAuditLogs(params?: {
  actor?: string;
  action?: string;
  resource_type?: string;
  resource_id?: string;
  created_after?: string;
  created_before?: string;
  limit?: number;
  offset?: number;
}): Promise<ListAuditLogsResponse> {
  const qs = new URLSearchParams();
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        qs.set(k, String(v));
      }
    });
  }
  return request<ListAuditLogsResponse>(`/admin/audit_logs?${qs.toString()}`);
}

// MCP access tokens
export async function listMCPTokens(): Promise<ListMCPTokensResponse> {
  return request<ListMCPTokensResponse>('/admin/mcp/tokens');
}

export async function createMCPToken(data: {
  name: string;
  permissions: MCPPermission[];
  expiresAt?: string;
}): Promise<MCPToken> {
  return request<MCPToken>('/admin/mcp/tokens', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function revokeMCPToken(id: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/admin/mcp/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' });
}
