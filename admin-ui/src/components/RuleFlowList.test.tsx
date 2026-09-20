import { render, screen, cleanup } from '@testing-library/react';
import { describe, expect, it, vi, afterEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import RuleFlowList from './RuleFlowList';
import type { Action, Rule } from '../types/rule';
import type { ActionPath } from '../utils/ruleEdit';

function makeRule(steps: Action[], hooks?: Rule['hooks']): Rule {
  return {
    id: 'r',
    version: '1.0.0',
    name: 'Rule',
    domain: 'example.com',
    urlPattern: undefined,
    enabled: true,
    priority: 'normal',
    entry: 'https://example.com',
    variables: {},
    selectors: {},
    humanize: {},
    steps,
    output: {},
    sendPolicy: {},
    hooks: hooks ?? {},
    tags: {},
    owner: 'x',
    approvalStatus: 'approved',
    createdAt: '',
    updatedAt: '',
  };
}

afterEach(() => {
  cleanup();
});

describe('RuleFlowList', () => {
  it('renders top-level steps', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'click', target: { selector: '#btn' } },
    ]);
    render(<RuleFlowList rule={rule} />);
    expect(screen.getByText('页面跳转')).toBeInTheDocument();
    expect(screen.getByText('点击')).toBeInTheDocument();
  });

  it('expands an if action', () => {
    const rule = makeRule([
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '.x' } },
        then: [{ action: 'sendResult' }],
        else: [{ action: 'sendLog', level: 'warn', message: 'miss' }],
      },
    ]);
    render(<RuleFlowList rule={rule} />);
    expect(screen.getByText('条件判断')).toBeInTheDocument();
    expect(screen.getByText('满足条件时执行')).toBeInTheDocument();
    expect(screen.getByText('不满足条件时执行')).toBeInTheDocument();
  });

  it('renders hooks section', () => {
    const rule = makeRule([], {
      onError: [{ action: 'sendLog', level: 'error', message: 'fail' }],
    });
    render(<RuleFlowList rule={rule} />);
    expect(screen.getByText('生命周期钩子')).toBeInTheDocument();
    expect(screen.getByText('异常处理')).toBeInTheDocument();
  });

  it('does not crash on unknown actions', () => {
    const rule = makeRule([{ action: 'weird', foo: 1 } as Action]);
    const { container } = render(<RuleFlowList rule={rule} />);
    expect(container.querySelector('.rule-flow-list')).toBeInTheDocument();
  });

  it('calls onSelectAction with ActionPath when clicking an action card', async () => {
    const onSelectAction = vi.fn();
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'click', target: { selector: '#btn' } },
    ]);
    const user = userEvent.setup();
    render(<RuleFlowList rule={rule} onSelectAction={onSelectAction} />);

    await user.click(screen.getByText('页面跳转').closest('[data-testid^="action-card-"]')!);

    expect(onSelectAction).toHaveBeenCalledTimes(1);
    expect(onSelectAction).toHaveBeenCalledWith(['steps', 0] as ActionPath);
  });

  it('highlights the selected action card', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'click', target: { selector: '#btn' } },
    ]);
    render(<RuleFlowList rule={rule} selectedPath={['steps', 1]} />);

    const selected = screen.getByText('点击').closest('[data-testid^="action-card-"]') as HTMLElement;
    const unselected = screen.getByText('页面跳转').closest('[data-testid^="action-card-"]') as HTMLElement;

    expect(selected.style.backgroundColor).toBe('rgb(230, 247, 255)');
    expect(unselected.style.backgroundColor).toBe('');
  });

  it('does not trigger onSelectAction when clicking expand/collapse tag', async () => {
    const onSelectAction = vi.fn();
    const rule = makeRule([
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '.x' } },
        then: [{ action: 'sendResult' }],
        else: [{ action: 'sendLog', level: 'warn', message: 'miss' }],
      },
    ]);
    const user = userEvent.setup();
    render(<RuleFlowList rule={rule} onSelectAction={onSelectAction} />);

    const expandTag = screen.getByText('收起');
    await user.click(expandTag);

    expect(onSelectAction).not.toHaveBeenCalled();
  });

  it('supports selecting actions inside hooks', async () => {
    const onSelectAction = vi.fn();
    const rule = makeRule([], {
      onError: [{ action: 'sendLog', level: 'error', message: 'fail' }],
    });
    const user = userEvent.setup();
    render(<RuleFlowList rule={rule} onSelectAction={onSelectAction} />);

    await user.click(screen.getByText('记录日志').closest('[data-testid^="action-card-"]')!);

    expect(onSelectAction).toHaveBeenCalledTimes(1);
    expect(onSelectAction).toHaveBeenCalledWith(['hooks', 'onError', 0] as ActionPath);
  });

  it('renders switch default branch', () => {
    const rule = makeRule([
      {
        action: 'switch',
        expression: '{{status}}',
        cases: [{ value: 'ok', steps: [{ action: 'sendResult' }] }],
        default: [{ action: 'sendLog', level: 'info', message: 'unknown' }],
      },
    ]);
    render(<RuleFlowList rule={rule} />);
    expect(screen.getByText('默认')).toBeInTheDocument();
    expect(screen.getByText('记录日志')).toBeInTheDocument();
  });

  it('calls onSelectAction with default branch path', async () => {
    const onSelectAction = vi.fn();
    const rule = makeRule([
      {
        action: 'switch',
        expression: '{{status}}',
        cases: [],
        default: [{ action: 'sendLog', level: 'info', message: 'unknown' }],
      },
    ]);
    const user = userEvent.setup();
    render(<RuleFlowList rule={rule} onSelectAction={onSelectAction} />);

    await user.click(screen.getByText('记录日志').closest('[data-testid^="action-card-"]')!);

    expect(onSelectAction).toHaveBeenCalledTimes(1);
    expect(onSelectAction).toHaveBeenCalledWith(['steps', 0, 'default', 0] as ActionPath);
  });

  it('uses a thicker left border for selected cards', () => {
    const rule = makeRule([{ action: 'navigate', url: 'https://example.com' }]);
    render(<RuleFlowList rule={rule} selectedPath={['steps', 0]} />);

    const card = screen.getByText('页面跳转').closest('[data-testid^="action-card-"]') as HTMLElement;
    expect(card.style.borderLeftWidth).toBe('6px');
  });
});
