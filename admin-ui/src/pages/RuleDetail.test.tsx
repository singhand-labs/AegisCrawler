import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { message } from 'antd';
import RuleDetail from './RuleDetail';
import type { Rule, Action } from '../types/rule';
import type { ActionPath } from '../utils/ruleEdit';
import type { FlowData, FlowNode } from '../utils/ruleToGraph';

const mocks = {
  navigate: vi.fn(),
  getRule: vi.fn(),
  updateRule: vi.fn(),
  approveRule: vi.fn(),
  rejectRule: vi.fn(),
  deleteRule: vi.fn(),
  getRuleEnhancement: vi.fn(),
  acceptEnhancement: vi.fn(),
  rejectEnhancement: vi.fn(),
  enhanceRule: vi.fn(),
  getEnhancementJob: vi.fn(),
};

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
  useParams: () => ({ id: 'rule-1' }),
}));

vi.mock('../api/client', () => ({
  getRule: (...args: unknown[]) => mocks.getRule(...args),
  updateRule: (...args: unknown[]) => mocks.updateRule(...args),
  approveRule: (...args: unknown[]) => mocks.approveRule(...args),
  rejectRule: (...args: unknown[]) => mocks.rejectRule(...args),
  deleteRule: (...args: unknown[]) => mocks.deleteRule(...args),
  getRuleEnhancement: (...args: unknown[]) => mocks.getRuleEnhancement(...args),
  acceptEnhancement: (...args: unknown[]) => mocks.acceptEnhancement(...args),
  rejectEnhancement: (...args: unknown[]) => mocks.rejectEnhancement(...args),
  enhanceRule: (...args: unknown[]) => mocks.enhanceRule(...args),
  getEnhancementJob: (...args: unknown[]) => mocks.getEnhancementJob(...args),
}));

vi.mock('../components/RuleFlowGraph', () => ({
  default: ({ data, onSelectNode, onEditNode }: { data: FlowData; onSelectNode?: (node: FlowNode) => void; onEditNode?: (node: FlowNode) => void }) => (
    <div data-testid="rule-flow-graph">
      {data.nodes.map((n) => (
        <button
          key={n.id}
          data-testid={`node-${n.id}`}
          onClick={() => onSelectNode?.(n)}
          onDoubleClick={() => onEditNode?.(n)}
        >
          {n.data.label}
        </button>
      ))}
    </div>
  ),
}));

vi.mock('../components/RuleFlowList', () => ({
  default: ({ rule, onSelectAction }: { rule: Rule; onSelectAction?: (path: ActionPath) => void }) => (
    <div data-testid="rule-flow-list">
      {(rule.steps ?? []).map((a, idx) => (
        <button
          key={idx}
          data-testid={`list-action-${idx}`}
          onClick={() => onSelectAction?.(['steps', idx])}
        >
          {a.action}
        </button>
      ))}
    </div>
  ),
}));

vi.mock('../components/ActionEditorPanel', () => ({
  default: ({ action, onChange, onCancel }: { action: Action; onChange: (action: Action) => void; onCancel: () => void }) => (
    <div data-testid="action-editor-panel">
      <span data-testid="editor-action-type">{action.action}</span>
      <button data-testid="editor-apply" onClick={() => onChange({ ...action, description: 'modified' })}>
        应用
      </button>
      <button data-testid="editor-cancel" onClick={onCancel}>
        取消
      </button>
    </div>
  ),
}));

function makeRule(approvalStatus: Rule['approvalStatus'] = 'pending'): Rule {
  return {
    id: 'rule-1',
    version: '1.0.0',
    name: '测试规则',
    domain: 'example.com',
    urlPattern: undefined,
    enabled: true,
    priority: 'normal',
    entry: 'https://example.com',
    variables: {},
    selectors: {},
    humanize: {},
    steps: [
      { action: 'navigate', url: 'https://example.com' },
      { action: 'click', target: { selector: '#btn' } },
    ],
    output: {},
    sendPolicy: {},
    hooks: {},
    tags: {},
    owner: 'alice',
    approvalStatus,
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
  };
}

function makeEnhancement(): ReturnType<typeof mocks.getRuleEnhancement> extends Promise<infer T> ? T : never {
  return {
    id: 'e1',
    ruleId: 'rule-1',
    baseline: makeRule('approved'),
    enhanced: { ...makeRule('approved'), name: 'enhanced' },
    userHint: '抓价格',
    provider: 'openai',
    model: 'gpt-4o',
    inputTokens: 100,
    outputTokens: 50,
    suggestions: ['建议1'],
    safetyFlags: [],
    status: 'pending',
    createdAt: '',
  };
}

describe('RuleDetail', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mocks.getRule.mockResolvedValue(makeRule());
    mocks.updateRule.mockResolvedValue(makeRule());
    mocks.approveRule.mockResolvedValue({ success: true });
    mocks.rejectRule.mockResolvedValue({ success: true });
    mocks.deleteRule.mockResolvedValue({ success: true });
    mocks.getRuleEnhancement.mockRejectedValue(new Error('not found'));
    mocks.acceptEnhancement.mockResolvedValue(makeRule('approved'));
    mocks.rejectEnhancement.mockResolvedValue(undefined);
    mocks.enhanceRule.mockResolvedValue({ jobId: 'job-1', statusUrl: '/admin/rules/enhancements/jobs/job-1' });
    mocks.getEnhancementJob.mockResolvedValue({
      id: 'job-1',
      ruleId: 'rule-1',
      baseline: makeRule('approved'),
      recording: {},
      userHint: '',
      status: 'completed',
      resultError: '',
      provider: 'openai',
      model: 'gpt-4o',
      inputTokens: 100,
      outputTokens: 50,
      suggestions: [],
      safetyFlags: [],
      createdAt: '',
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('fetches rule by id and shows metadata', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(mocks.getRule).toHaveBeenCalledWith('rule-1'));
    expect(await screen.findByText('测试规则')).toBeInTheDocument();
    fireEvent.click(screen.getByText('概览'));
    expect(await screen.findByText('example.com')).toBeInTheDocument();
  });

  it('shows source tag in metadata', async () => {
    const ruleWithSource = { ...makeRule('pending'), source: 'pageagent' };
    mocks.getRule.mockResolvedValue(ruleWithSource);
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByText('测试规则')).toBeInTheDocument());
    fireEvent.click(screen.getByText('概览'));
    expect(await screen.findByText('pageagent')).toBeInTheDocument();
  });

  it('switches to raw JSON tab', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByText('测试规则')).toBeInTheDocument());
    fireEvent.click(screen.getByText('原始 JSON'));
    expect(await screen.findByText(/"id": "rule-1"/)).toBeInTheDocument();
  });

  it('shows approval buttons for pending rules', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByText(/通\s*过/)).toBeInTheDocument());
    expect(screen.getByText(/拒\s*绝/)).toBeInTheDocument();
  });

  it('shows error when getRule fails', async () => {
    mocks.getRule.mockRejectedValue(new Error('not found'));
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByText('not found')).toBeInTheDocument());
  });

  it('loads rule and shows flow graph and toolbar', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /新增动作/ })).toBeInTheDocument();
  });

  it('opens editor when node is double-clicked and shows the action', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.doubleClick(screen.getByTestId('node-steps[1]'));
    await waitFor(() => expect(screen.getByTestId('action-editor-panel')).toBeInTheDocument());
    expect(screen.getByTestId('editor-action-type')).toHaveTextContent('click');
  });

  it('marks dirty after action change and enables save', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.doubleClick(screen.getByTestId('node-steps[1]'));
    await waitFor(() => expect(screen.getByTestId('action-editor-panel')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('editor-apply'));
    expect(screen.getByText('有未保存的修改')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '保存规则' })).not.toBeDisabled();
  });

  it('saves rule and refreshes state on success', async () => {
    const updatedRule = { ...makeRule(), version: '1.0.1' };
    mocks.updateRule.mockResolvedValue(updatedRule);
    const successSpy = vi.spyOn(message, 'success').mockImplementation(vi.fn());
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.doubleClick(screen.getByTestId('node-steps[1]'));
    await waitFor(() => expect(screen.getByTestId('action-editor-panel')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('editor-apply'));
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }));
    await waitFor(() =>
      expect(mocks.updateRule).toHaveBeenCalledWith('rule-1', expect.objectContaining({ steps: expect.any(Array) })),
    );
    await waitFor(() => expect(successSpy).toHaveBeenCalledWith('保存成功'));
    await waitFor(() => expect(screen.getByRole('button', { name: '保存规则' })).toBeDisabled());
    successSpy.mockRestore();
  });

  it('shows error message when save fails', async () => {
    mocks.updateRule.mockRejectedValue(new Error('server error'));
    const errorSpy = vi.spyOn(message, 'error').mockImplementation(vi.fn());
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.doubleClick(screen.getByTestId('node-steps[1]'));
    await waitFor(() => expect(screen.getByTestId('action-editor-panel')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('editor-apply'));
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }));
    await waitFor(() => expect(errorSpy).toHaveBeenCalledWith('server error'));
    errorSpy.mockRestore();
  });

  it('blocks save and shows validation error for invalid rule', async () => {
    const invalidRule = makeRule();
    invalidRule.steps[1] = { action: 'click' } as Action;
    mocks.getRule.mockResolvedValue(invalidRule);
    const errorSpy = vi.spyOn(message, 'error').mockImplementation(vi.fn());
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.doubleClick(screen.getByTestId('node-steps[1]'));
    await waitFor(() => expect(screen.getByTestId('action-editor-panel')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('editor-apply'));
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }));
    await waitFor(() =>
      expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('校验失败')),
    );
    expect(mocks.updateRule).not.toHaveBeenCalled();
    errorSpy.mockRestore();
  });

  it('removes action after delete', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('node-steps[1]'));
    fireEvent.click(screen.getByRole('button', { name: '删除选中动作' }));
    await waitFor(() => expect(screen.queryByTestId('node-steps[1]')).not.toBeInTheDocument());
  });

  it('adds new action and shows it in the graph', async () => {
    const user = userEvent.setup();
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    await user.click(screen.getByRole('button', { name: /新增动作/ }));
    await user.click(screen.getByRole('menuitem', { name: '追加到末尾' }));
    await waitFor(() => expect(screen.getByText('选择动作类型')).toBeInTheDocument());
    await user.click(screen.getByTestId('action-type-click'));
    await waitFor(() => expect(screen.getByTestId('node-steps[2]')).toBeInTheDocument());
  });

  it('does not open editor when virtual start/end node is clicked or double-clicked', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.doubleClick(screen.getByTestId('node-__start__'));
    expect(screen.queryByTestId('action-editor-panel')).not.toBeInTheDocument();
    fireEvent.doubleClick(screen.getByTestId('node-__end__'));
    expect(screen.queryByTestId('action-editor-panel')).not.toBeInTheDocument();
  });

  it('disables move buttons at array boundaries', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('node-steps[0]'));
    expect(screen.getByRole('button', { name: '上移' })).toBeDisabled();
    fireEvent.click(screen.getByTestId('node-steps[1]'));
    expect(screen.getByRole('button', { name: '下移' })).toBeDisabled();
  });

  it('moves action down and updates the flow graph', async () => {
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByTestId('rule-flow-graph')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('node-steps[0]'));
    expect(screen.getByTestId('node-steps[0]')).toHaveTextContent('页面跳转');
    fireEvent.click(screen.getByRole('button', { name: '下移' }));
    await waitFor(() => expect(screen.getByTestId('node-steps[0]')).toHaveTextContent('点击'));
    expect(screen.getByTestId('node-steps[1]')).toHaveTextContent('页面跳转');
  });

  it('shows AI enhance button for approved rules and polls job until completed', async () => {
    mocks.getRule.mockResolvedValue(makeRule('approved'));
    mocks.getRuleEnhancement.mockResolvedValue(makeEnhancement());
    const successSpy = vi.spyOn(message, 'success').mockImplementation(vi.fn());
    render(<RuleDetail />);
    await waitFor(() => expect(screen.getByRole('button', { name: /AI 增强/ })).toBeInTheDocument());

    fireEvent.click(screen.getByRole('button', { name: /AI 增强/ }));
    await waitFor(() => expect(screen.getByText('AI 规则增强')).toBeInTheDocument());

    fireEvent.click(screen.getByTestId('enhance-submit'));
    await waitFor(() => expect(mocks.enhanceRule).toHaveBeenCalled());
    await waitFor(() => expect(mocks.getEnhancementJob).toHaveBeenCalledWith('job-1'));
    await waitFor(() => expect(mocks.getRuleEnhancement).toHaveBeenCalledWith('rule-1'));

    successSpy.mockRestore();
  });
});
