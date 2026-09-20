import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ScheduleList from './ScheduleList';

const mocks = {
  navigate: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
  listSchedules: vi.fn(),
  listRules: vi.fn(),
  deleteSchedule: vi.fn(),
  updateSchedule: vi.fn(),
  triggerSchedule: vi.fn(),
  createSchedule: vi.fn(),
  listRuleVersions: vi.fn(),
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
  listSchedules: (...args: unknown[]) => mocks.listSchedules(...args),
  listRules: (...args: unknown[]) => mocks.listRules(...args),
  deleteSchedule: (...args: unknown[]) => mocks.deleteSchedule(...args),
  updateSchedule: (...args: unknown[]) => mocks.updateSchedule(...args),
  triggerSchedule: (...args: unknown[]) => mocks.triggerSchedule(...args),
  createSchedule: (...args: unknown[]) => mocks.createSchedule(...args),
  listRuleVersions: (...args: unknown[]) => mocks.listRuleVersions(...args),
  previewSchedule: (...args: unknown[]) => mocks.previewSchedule(...args),
}));

const sampleSchedule: import('../api/types').Schedule = {
  id: 'schedule-1',
  workspaceId: 'default',
  ruleId: 'rule-1',
  ruleVersion: '1.0.0',
  ruleVersionNumber: 2,
  name: '测试计划',
  type: 'cron',
  expression: '*/5 * * * *',
  enabled: true,
  variables: {},
  inputSchema: { type: 'object', properties: {} },
  browserProfileId: 'profile-main',
  timezone: 'Asia/Taipei',
  priority: 'normal',
  maxRetries: 3,
  catchup: 'skip',
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};

describe('ScheduleList', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listSchedules.mockResolvedValue({ schedules: [sampleSchedule], total: 1 });
    mocks.listRules.mockResolvedValue({
      rules: [{ id: 'rule-1', version: '1.0.0', name: '规则一' } as import('../api/types').Rule],
      total: 1,
    });
  });

  it('renders schedule list', async () => {
    render(<ScheduleList />);

    await waitFor(() => {
      expect(screen.getByText('测试计划')).toBeInTheDocument();
    });
    expect(screen.getByText('*/5 * * * *')).toBeInTheDocument();
    expect(screen.getByText('v2 (1.0.0)')).toBeInTheDocument();
    expect(screen.getByText('Asia/Taipei')).toBeInTheDocument();
    expect(screen.getByText('profile-main')).toBeInTheDocument();
  });

  it('triggers a schedule', async () => {
    const user = userEvent.setup();
    mocks.triggerSchedule.mockResolvedValue({ taskId: 'task-123' });

    render(<ScheduleList />);

    await waitFor(() => {
      expect(screen.getByText('测试计划')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /手动触发/ }));

    await waitFor(() => {
      expect(mocks.triggerSchedule).toHaveBeenCalledWith('schedule-1');
    });
    expect(mocks.messageSuccess).toHaveBeenCalled();
  });

  it('toggles enabled status', async () => {
    const user = userEvent.setup();
    mocks.updateSchedule.mockResolvedValue({ ...sampleSchedule, enabled: false });

    render(<ScheduleList />);

    await waitFor(() => {
      expect(screen.getByText('测试计划')).toBeInTheDocument();
    });

    const switchEl = screen.getByRole('switch');
    expect(switchEl).toBeChecked();

    await user.click(switchEl);

    await waitFor(() => {
      expect(mocks.updateSchedule).toHaveBeenCalledWith('schedule-1', { enabled: false });
    });
    expect(mocks.messageSuccess).toHaveBeenCalledWith('状态已更新');
  });

  it('deletes a schedule after confirmation', async () => {
    const user = userEvent.setup();
    mocks.deleteSchedule.mockResolvedValue(undefined);

    render(<ScheduleList />);

    await waitFor(() => {
      expect(screen.getByText('测试计划')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: '删除' }));

    await waitFor(() => {
      expect(screen.getByText('删除调度计划')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /确\s*认/ }));

    await waitFor(() => {
      expect(mocks.deleteSchedule).toHaveBeenCalledWith('schedule-1');
    });
    expect(mocks.messageSuccess).toHaveBeenCalledWith('调度计划已删除');
  });

  it('filters by rule and enabled status', async () => {
    const user = userEvent.setup();
    render(<ScheduleList />);

    await waitFor(() => {
      expect(screen.getByText('测试计划')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('combobox', { name: '按规则筛选' }));
    await user.click(screen.getByText('规则一 (rule-1)', { selector: '.ant-select-item-option-content' }));

    await waitFor(() => {
      expect(mocks.listSchedules).toHaveBeenLastCalledWith(
        expect.objectContaining({ rule_id: 'rule-1' })
      );
    });
  });
});

describe('ScheduleList create modal', () => {
  const approvedRule = {
    id: 'rule-approved',
    version: '1.0.0',
    name: '已批准规则',
    approvalStatus: 'approved',
  } as import('../api/types').Rule;
  const draftRule = {
    id: 'rule-draft',
    version: '1.0.0',
    name: '草稿规则',
    approvalStatus: 'draft',
  } as unknown as import('../api/types').Rule;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listSchedules.mockResolvedValue({ schedules: [], total: 0 });
    mocks.listRules.mockResolvedValue({ rules: [approvedRule, draftRule], total: 2 });
    mocks.listRuleVersions.mockResolvedValue({
      ruleVersions: [
        { ruleId: 'rule-approved', version: 2, versionLabel: '2.0.0', status: 'approved' },
        { ruleId: 'rule-approved', version: 1, versionLabel: '1.0.0', status: 'rejected' },
      ],
    });
    mocks.previewSchedule.mockResolvedValue({
      expression: '*/5 * * * *', timezone: 'Asia/Shanghai',
      runs: ['2026-01-01T00:05:00Z', '2026-01-01T00:10:00Z', '2026-01-01T00:15:00Z'],
    });
  });

    const selectByKeyboard = async (index: number, presses: number) => {
      // antd v5's virtualized options cannot be clicked under jsdom (the
      // listbox renders zero-height); rc-select's keyboard path works: open
      // with mousedown, then arrow-down N times and Enter.
      const combo = screen.getAllByRole('combobox')[index];
      fireEvent.mouseDown(combo);
      await screen.findByRole('listbox');
      for (let i = 0; i < presses; i++) {
        fireEvent.keyDown(combo, { key: 'ArrowDown', keyCode: 40 });
      }
      fireEvent.keyDown(combo, { key: 'Enter', keyCode: 13 });
    };

  it('creates a schedule through the modal form', async () => {
    const user = userEvent.setup();
    mocks.createSchedule.mockResolvedValue({ id: 'schedule-new' });

    render(<ScheduleList />);

    await user.click(await screen.findByRole('button', { name: /新建调度/ }));

    await user.type(await screen.findByLabelText(/名称/), '公告采集-每5分钟');

    // combobox order: [filter-rule, filter-enabled, modal rule, modal version, type, priority, catchup]
    // rule picker only offers approved rules (draft rule filtered out):
    // first arrow-down highlights it, Enter selects.
    await selectByKeyboard(2, 1);
    await waitFor(() => {
      expect(mocks.listRuleVersions).toHaveBeenCalledWith('rule-approved');
    });
    // version picker only offers approved versions (1.0.0 was rejected)
    await selectByKeyboard(3, 1);

    await user.clear(screen.getByLabelText(/Cron 表达式/));
    await user.type(screen.getByLabelText(/Cron 表达式/), '*/5 * * * *');

    await user.click(screen.getByRole('button', { name: /创 建|创建/ }));

    await waitFor(() => {
      expect(mocks.createSchedule).toHaveBeenCalledWith(expect.objectContaining({
        name: '公告采集-每5分钟',
        ruleId: 'rule-approved',
        ruleVersion: '2.0.0',
        type: 'cron',
        expression: '*/5 * * * *',
        timezone: 'Asia/Shanghai',
        variables: {},
        priority: 'normal',
        catchup: 'skip',
        enabled: true,
      }));
    });
    expect(mocks.messageSuccess).toHaveBeenCalled();
  });

  it('blocks submission when the variables JSON is invalid', async () => {
    const user = userEvent.setup();
    render(<ScheduleList />);

    await user.click(await screen.findByRole('button', { name: /新建调度/ }));
    await user.type(await screen.findByLabelText(/名称/), 'bad-vars');

    await selectByKeyboard(2, 1);
    await waitFor(() => {
      expect(mocks.listRuleVersions).toHaveBeenCalledWith('rule-approved');
    });
    await selectByKeyboard(3, 1);
    await user.type(screen.getByLabelText(/Cron 表达式/), '*/5 * * * *');

    await user.clear(screen.getByLabelText(/固定任务变量/));
    await user.type(screen.getByLabelText(/固定任务变量/), '{{not-json');

    await user.click(screen.getByRole('button', { name: /创 建|创建/ }));

    await waitFor(() => {
      expect(mocks.messageError).toHaveBeenCalledWith(expect.stringContaining('JSON'));
    });
    expect(mocks.createSchedule).not.toHaveBeenCalled();
  });
});
