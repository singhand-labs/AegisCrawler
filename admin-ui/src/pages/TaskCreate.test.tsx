import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import TaskCreate from './TaskCreate';

const mocks = {
  navigate: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
  listRules: vi.fn(),
  listRuleVersions: vi.fn(),
  createTask: vi.fn(),
  createSchedule: vi.fn(),
  previewSchedule: vi.fn(),
};

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
}));

vi.mock('antd', async () => {
  const actual = await vi.importActual<typeof import('antd')>('antd');
  return {
    ...actual,
    message: {
      success: (...args: unknown[]) => mocks.messageSuccess(...args),
      error: (...args: unknown[]) => mocks.messageError(...args),
    },
  };
});

vi.mock('../api/client', () => ({
  listRules: (...args: unknown[]) => mocks.listRules(...args),
  listRuleVersions: (...args: unknown[]) => mocks.listRuleVersions(...args),
  createTask: (...args: unknown[]) => mocks.createTask(...args),
  createSchedule: (...args: unknown[]) => mocks.createSchedule(...args),
  previewSchedule: (...args: unknown[]) => mocks.previewSchedule(...args),
}));

describe('TaskCreate', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listRules.mockResolvedValue({
      rules: [
        { id: 'rule-1', version: '1.0.0', name: '规则一', approvalStatus: 'approved' } as import('../api/types').Rule,
      ],
      total: 1,
    });
    mocks.previewSchedule.mockResolvedValue({
      expression: '0 * * * *',
      timezone: 'UTC',
      runs: ['2024-01-01T01:00:00Z', '2024-01-01T02:00:00Z'],
    });
    mocks.listRuleVersions.mockResolvedValue({
      ruleVersions: [{
        workspaceId: 'default', ruleId: 'rule-1', version: 2, versionLabel: '1.0.0',
        contentHash: '1234567890abcdef', status: 'approved', owner: 'admin', source: 'pageagent',
        createdAt: '2026-01-01T00:00:00Z', rule: {},
      }],
      contracts: [{
        workspaceId: 'default', ruleId: 'rule-1', version: 2,
        inputSchema: {
          type: 'object', required: ['query'], properties: {
            query: { type: 'string', default: 'books' },
            limit: { type: 'integer', default: 10, minimum: 1 },
            filters: { type: 'object' },
          },
        },
        outputSchema: { type: 'object', properties: { title: { type: 'string' } } },
        browserProfileId: 'profile-main', createdAt: '2026-01-01T00:00:00Z',
      }],
    });
  });

  it('renders form with default once mode', async () => {
    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    expect(screen.getByText('创建任务', { selector: 'h3' })).toBeInTheDocument();
    expect(screen.getByLabelText('选择规则模板')).toBeInTheDocument();
    expect(screen.getByLabelText('执行时间（可选，留空立即执行）')).toBeInTheDocument();
  });

  it('only shows approved rules in the dropdown', async () => {
    const user = userEvent.setup();
    mocks.listRules.mockResolvedValue({
      rules: [
        { id: 'rule-1', version: '1.0.0', name: '待审核规则', approvalStatus: 'pending' } as import('../api/types').Rule,
        { id: 'rule-2', version: '1.0.0', name: '已通过规则', approvalStatus: 'approved' } as import('../api/types').Rule,
      ],
      total: 2,
    });

    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    await user.click(screen.getByLabelText('选择规则模板'));
    expect(screen.queryByText('待审核规则 (rule-1)')).not.toBeInTheDocument();
    expect(screen.getByText('已通过规则 (rule-2)')).toBeInTheDocument();
  });

  it('switches to scheduled mode', async () => {
    const user = userEvent.setup();
    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    await user.click(screen.getByText('定时执行'));

    await waitFor(() => {
      expect(screen.getByLabelText('计划名称')).toBeInTheDocument();
      expect(screen.getByLabelText('Cron 表达式')).toBeInTheDocument();
      expect(screen.getByLabelText('漏跑策略')).toBeInTheDocument();
    });
  });

  it('creates a single task and navigates to /tasks', async () => {
    const user = userEvent.setup();
    mocks.createTask.mockResolvedValue({ taskId: 'task-123' });

    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    await user.click(screen.getByLabelText('选择规则模板'));
    await user.click(screen.getByText('规则一 (rule-1)'));

    await waitFor(() => expect(screen.getByLabelText('Immutable rule version')).toBeInTheDocument());

    const maxRetries = screen.getByLabelText('最大重试次数');
    await user.clear(maxRetries);
    await user.type(maxRetries, '2');

    await user.click(screen.getByRole('button', { name: '创建任务' }));

    await waitFor(() => {
      expect(mocks.createTask).toHaveBeenCalledTimes(1);
    });

    expect(mocks.createTask).toHaveBeenCalledWith(
      expect.objectContaining({
        ruleId: 'rule-1',
        ruleVersionNumber: 2,
        variables: { query: 'books', limit: 10 },
        browserProfileId: 'profile-main',
        maxRetries: 2,
      })
    );
    expect(mocks.messageSuccess).toHaveBeenCalledWith('创建成功');
    expect(mocks.navigate).toHaveBeenCalledWith('/tasks');
  });

  it('creates a schedule and navigates to /schedules', async () => {
    const user = userEvent.setup();
    mocks.createSchedule.mockResolvedValue({ id: 'schedule-1' } as import('../api/types').Schedule);

    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    await user.click(screen.getByText('定时执行'));

    await waitFor(() => {
      expect(screen.getByLabelText('计划名称')).toBeInTheDocument();
    });

    await user.click(screen.getByLabelText('选择规则模板'));
    await user.click(screen.getByText('规则一 (rule-1)'));

    await waitFor(() => expect(screen.getByLabelText('Immutable rule version')).toBeInTheDocument());

    await user.type(screen.getByLabelText('计划名称'), '每小时执行');
    fireEvent.change(screen.getByLabelText('Cron 表达式'), { target: { value: '0 * * * *' } });

    const maxRetries = screen.getByLabelText('最大重试次数');
    await user.clear(maxRetries);
    await user.type(maxRetries, '1');

    await user.click(screen.getByRole('button', { name: '创建调度计划' }));

    await waitFor(
      () => {
        expect(mocks.createSchedule).toHaveBeenCalledTimes(1);
      },
      { timeout: 2000 },
    );

    expect(mocks.createSchedule).toHaveBeenCalledWith(
      expect.objectContaining({
        ruleId: 'rule-1',
        ruleVersionNumber: 2,
        name: '每小时执行',
        expression: '0 * * * *',
        type: 'cron',
        maxRetries: 1,
        catchup: 'skip',
        enabled: true,
        timezone: 'UTC',
      })
    );
    expect(mocks.messageSuccess).toHaveBeenCalledWith('创建成功');
    expect(mocks.navigate).toHaveBeenCalledWith('/schedules');
  });

  it('shows validation error for an invalid structured input', async () => {
    const user = userEvent.setup();
    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    await user.click(screen.getByLabelText('选择规则模板'));
    await user.click(screen.getByText('规则一 (rule-1)'));

    await waitFor(() => expect(screen.getByLabelText('filters')).toBeInTheDocument());
    await user.type(screen.getByLabelText('filters'), 'not-json');
    await user.click(screen.getByRole('button', { name: '创建任务' }));

    await waitFor(() => {
      expect(screen.getByText('Unexpected token', { exact: false })).toBeInTheDocument();
    });
    expect(mocks.createTask).not.toHaveBeenCalled();
  });

  it('shows cron preview when expression is entered', async () => {
    const user = userEvent.setup();
    render(<TaskCreate />);

    await waitFor(() => {
      expect(screen.getByText('单次执行')).toBeInTheDocument();
    });

    await user.click(screen.getByText('定时执行'));

    await waitFor(() => {
      expect(screen.getByLabelText('Cron 表达式')).toBeInTheDocument();
    });

    fireEvent.change(screen.getByLabelText('Cron 表达式'), { target: { value: '0 * * * *' } });

    await waitFor(
      () => {
        expect(mocks.previewSchedule).toHaveBeenCalledWith('0 * * * *', 5, 'UTC');
      },
      { timeout: 2000 },
    );

    await waitFor(() => {
      expect(screen.getByText(/下次执行时间/)).toBeInTheDocument();
    });
    expect(screen.getAllByRole('listitem')).toHaveLength(2);
  });
});
