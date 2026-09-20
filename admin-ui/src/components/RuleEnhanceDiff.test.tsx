import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import RuleEnhanceDiff from './RuleEnhanceDiff';
import type { RuleEnhancement, LLMJob } from '../api/types';

function makeEnhancement(): RuleEnhancement {
  return {
    id: 'e1',
    ruleId: 'r1',
    baseline: { id: 'r1', name: 'base', version: '1.0.0', domain: 'example.com', urlPattern: null, enabled: false, priority: 'normal', entry: 'http://example.com', variables: {}, selectors: {}, humanize: {}, steps: [{ action: 'navigate', url: 'http://example.com' }], output: {}, sendPolicy: {}, hooks: {}, tags: {}, owner: '', approvalStatus: 'pending', createdAt: '', updatedAt: '' },
    enhanced: { id: 'r1', name: 'enhanced', version: '1.0.0', domain: 'example.com', urlPattern: null, enabled: false, priority: 'normal', entry: 'http://example.com', variables: {}, selectors: {}, humanize: {}, steps: [{ action: 'navigate', url: 'http://example.com' }], output: {}, sendPolicy: {}, hooks: {}, tags: {}, owner: '', approvalStatus: 'pending', createdAt: '', updatedAt: '' },
    patch: [{ op: 'replace', path: '/name', value: 'enhanced' }],
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

function makeJob(status: LLMJob['status']): LLMJob {
  return {
    id: 'job-1',
    ruleId: 'r1',
    baseline: makeEnhancement().baseline,
    recording: {},
    userHint: '抓价格',
    status,
    resultError: '',
    provider: 'openai',
    model: 'gpt-4o',
    inputTokens: 100,
    outputTokens: 50,
    suggestions: ['建议1'],
    safetyFlags: [],
    createdAt: '',
  };
}

describe('RuleEnhanceDiff', () => {
  it('renders suggestions and calls accept', () => {
    const onAccept = vi.fn();
    render(<RuleEnhanceDiff enhancement={makeEnhancement()} onAccept={onAccept} onReject={vi.fn()} />);
    expect(screen.getByText('AI 规则增强建议')).toBeInTheDocument();
    fireEvent.click(screen.getByText('接受增强'));
    expect(onAccept).toHaveBeenCalled();
  });

  it('disables accept/reject while job is running', () => {
    render(<RuleEnhanceDiff enhancement={makeEnhancement()} job={makeJob('running')} onAccept={vi.fn()} onReject={vi.fn()} />);
    expect(screen.getByText('增强中')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /接受增强/ })).toBeDisabled();
    expect(screen.getByRole('button', { name: /拒绝并删除/ })).toBeDisabled();
  });

  it('shows error alert when job failed', () => {
    const job = makeJob('failed');
    job.resultError = 'LLM timeout';
    render(<RuleEnhanceDiff enhancement={makeEnhancement()} job={job} onAccept={vi.fn()} onReject={vi.fn()} />);
    expect(screen.getByText('增强任务异常')).toBeInTheDocument();
    expect(screen.getByText('LLM timeout')).toBeInTheDocument();
  });

  it('shows error alert when job completed with resultError', () => {
    const enhancement = makeEnhancement();
    enhancement.resultError = 'LLM returned baseline';
    render(<RuleEnhanceDiff enhancement={enhancement} job={makeJob('completed')} onAccept={vi.fn()} onReject={vi.fn()} />);
    expect(screen.getByText('增强任务异常')).toBeInTheDocument();
    expect(screen.getByText('LLM returned baseline')).toBeInTheDocument();
  });

  it('switches to JSON Diff tab', () => {
    render(<RuleEnhanceDiff enhancement={makeEnhancement()} onAccept={vi.fn()} onReject={vi.fn()} />);
    fireEvent.click(screen.getByText('JSON Diff'));
    expect(document.querySelector('.rule-json-diff')).toBeInTheDocument();
  });

  it('switches to Patch 编辑 tab and applies selected ops', () => {
    const onAccept = vi.fn();
    render(<RuleEnhanceDiff enhancement={makeEnhancement()} onAccept={onAccept} onReject={vi.fn()} />);
    fireEvent.click(screen.getByText('Patch 编辑'));
    expect(screen.getByText('应用选中项')).toBeInTheDocument();

    fireEvent.click(screen.getByText('应用选中项'));
    expect(screen.getByTestId('active-patch-count')).toHaveTextContent('已启用 1 / 1 条 patch');
  });
});
