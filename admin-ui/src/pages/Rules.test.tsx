import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import Rules from './Rules';
import type { Rule } from '../types/rule';

const mocks = {
  navigate: vi.fn(),
  listRules: vi.fn(),
  createRule: vi.fn(),
  updateRule: vi.fn(),
  deleteRule: vi.fn(),
  approveRule: vi.fn(),
  rejectRule: vi.fn(),
};

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
}));

vi.mock('../api/client', () => ({
  listRules: (...args: unknown[]) => mocks.listRules(...args),
  createRule: (...args: unknown[]) => mocks.createRule(...args),
  updateRule: (...args: unknown[]) => mocks.updateRule(...args),
  deleteRule: (...args: unknown[]) => mocks.deleteRule(...args),
  approveRule: (...args: unknown[]) => mocks.approveRule(...args),
  rejectRule: (...args: unknown[]) => mocks.rejectRule(...args),
}));

function makeRule(id: string, approvalStatus: Rule['approvalStatus'], source?: string): Rule {
  return {
    id,
    version: '1.0.0',
    name: `规则 ${id}`,
    domain: 'example.com',
    urlPattern: undefined,
    enabled: true,
    priority: 'normal',
    entry: 'https://example.com',
    variables: {},
    selectors: {},
    humanize: {},
    steps: [],
    output: {},
    sendPolicy: {},
    hooks: {},
    tags: {},
    owner: 'alice',
    approvalStatus,
    source,
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
  };
}

describe('Rules', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listRules.mockResolvedValue({
      rules: [
        makeRule('rule-1', 'pending', 'pageagent'),
        makeRule('rule-2', 'approved', 'manual'),
      ],
      total: 2,
    });
    mocks.approveRule.mockResolvedValue({ success: true });
    mocks.rejectRule.mockResolvedValue({ success: true });
    mocks.updateRule.mockResolvedValue({ success: true });
    mocks.deleteRule.mockResolvedValue({ success: true });
  });

  it('renders rules with approval status and source tag', async () => {
    render(<Rules />);
    await waitFor(() => expect(screen.getByText('规则 rule-1')).toBeInTheDocument());
    expect(screen.getByText('规则 rule-2')).toBeInTheDocument();
    expect(screen.getAllByText('pageagent').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('manual').length).toBeGreaterThanOrEqual(1);
  });

  it('shows approve and reject buttons for pending rules', async () => {
    render(<Rules />);
    await waitFor(() => expect(screen.getByText('规则 rule-1')).toBeInTheDocument());
    const row = screen.getByText('规则 rule-1').closest('tr');
    expect(row).toBeTruthy();
    const approveBtn = row?.querySelector('button') as HTMLButtonElement | undefined;
    expect(approveBtn).toBeTruthy();
  });

  it('calls approveRule when approve button is clicked', async () => {
    render(<Rules />);
    await waitFor(() => expect(screen.getByText('规则 rule-1')).toBeInTheDocument());
    const buttons = screen.getAllByRole('button', { name: /通过/ });
    fireEvent.click(buttons[0]);
    await waitFor(() => expect(mocks.approveRule).toHaveBeenCalledWith('rule-1'));
  });

  it('filters rules by approval status', async () => {
    render(<Rules />);
    await waitFor(() => expect(screen.getByText('规则 rule-1')).toBeInTheDocument());
    expect(mocks.listRules).toHaveBeenCalledWith(expect.objectContaining({ approval_status: '' }));
  });

  it('prevents double-submit on the rule form (M-4)', async () => {
    // Simulate a slow server: createRule's promise is deferred, so the
    // first submit is still in flight when subsequent submissions land.
    let resolveCreate!: () => void;
    const deferred = new Promise<{ success: boolean }>((res) => {
      resolveCreate = () => res({ success: true });
    });
    mocks.createRule.mockReturnValueOnce(deferred);

    render(<Rules />);
    await waitFor(() => expect(screen.getByText('规则 rule-1')).toBeInTheDocument());

    fireEvent.click(screen.getByRole('button', { name: /新建规则/ }));
    const textarea = await screen.findByPlaceholderText('粘贴规则 JSON');
    fireEvent.change(textarea, { target: { value: JSON.stringify(makeRule('new-rule', 'pending')) } });

    const form = textarea.closest('form');
    expect(form).not.toBeNull();
    // Submit the form multiple times before the server responds.
    fireEvent.submit(form!);
    fireEvent.submit(form!);
    await new Promise((r) => setTimeout(r, 10));
    fireEvent.submit(form!);

    resolveCreate();
    await waitFor(() => expect(mocks.createRule).toHaveBeenCalledTimes(1));
  });
});
