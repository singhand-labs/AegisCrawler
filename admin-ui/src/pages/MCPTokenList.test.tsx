import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import MCPTokenList from './MCPTokenList';

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  revoke: vi.fn(),
  error: vi.fn(),
  success: vi.fn(),
}));

vi.mock('antd', async () => {
  const actual = await vi.importActual<typeof import('antd')>('antd');
  return { ...actual, message: { error: mocks.error, success: mocks.success } };
});

vi.mock('../api/client', () => ({
  listMCPTokens: (...args: unknown[]) => mocks.list(...args),
  createMCPToken: (...args: unknown[]) => mocks.create(...args),
  revokeMCPToken: (...args: unknown[]) => mocks.revoke(...args),
}));

describe('MCPTokenList', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.list.mockResolvedValue({
      tokens: [
        {
          id: 'active', workspaceId: 'default', name: 'Automation', tokenPrefix: 'aegis_mcp_abcd',
          permissions: ['read', 'write'], createdBy: 'admin', createdAt: '2026-01-01T00:00:00Z',
        },
        {
          id: 'revoked', workspaceId: 'default', name: 'Old client', tokenPrefix: 'aegis_mcp_efgh',
          permissions: ['read'], createdBy: 'admin', createdAt: '2026-01-01T00:00:00Z', revokedAt: '2026-02-01T00:00:00Z',
        },
      ],
    });
  });

  it('lists token metadata, permissions, and revocation state', async () => {
    render(<MCPTokenList />);

    await waitFor(() => expect(screen.getByText('Automation')).toBeInTheDocument());
    expect(screen.getByText('Old client')).toBeInTheDocument();
    expect(screen.getAllByText('read')).toHaveLength(2);
    expect(screen.getByText('write')).toBeInTheDocument();
    expect(screen.getByText('有效')).toBeInTheDocument();
    expect(screen.getByText('已撤销')).toBeInTheDocument();
    expect(screen.queryByText(/plaintext/)).not.toBeInTheDocument();
  });
});
