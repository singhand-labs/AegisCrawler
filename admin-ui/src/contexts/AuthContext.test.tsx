import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent, act } from '@testing-library/react';
import { AuthProvider, useAuth } from './AuthContext';

function TestComponent() {
  const { isAuthenticated, isLoading } = useAuth();
  return <div data-testid="state">{isLoading ? 'loading' : isAuthenticated ? 'authenticated' : 'anonymous'}</div>;
}

describe('AuthContext', () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('starts anonymous when no key is stored', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 401 }));
    render(
      <AuthProvider>
        <TestComponent />
      </AuthProvider>
    );
    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('anonymous'));
  });

  it('authenticates when stored key is valid', async () => {
    sessionStorage.setItem('adminApiKey', 'valid-key');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ rules: [], total: 0 }) }));
    render(
      <AuthProvider>
        <TestComponent />
      </AuthProvider>
    );
    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('authenticated'));
  });

  it('stores the new key before calling verifyKey during login', async () => {
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ rules: [], total: 0 }),
    });
    vi.stubGlobal('fetch', mockFetch);

    function LoginButton() {
      const { login, isAuthenticated } = useAuth();
      return (
        <div>
          <div data-testid="state">{isAuthenticated ? 'authenticated' : 'anonymous'}</div>
          <button onClick={() => login('fresh-key')}>登录</button>
        </div>
      );
    }

    render(
      <AuthProvider>
        <LoginButton />
      </AuthProvider>
    );

    fireEvent.click(screen.getByRole('button', { name: /登录/i }));

    await waitFor(() => expect(screen.getByTestId('state')).toHaveTextContent('authenticated'));

    expect(sessionStorage.getItem('adminApiKey')).toBe('fresh-key');
    expect(mockFetch).toHaveBeenCalled();

    const [, options] = mockFetch.mock.calls[0];
    expect(options.headers['Authorization']).toBe('Bearer fresh-key');
  });

  it('does not expose the raw API key through the context value (M-6)', () => {
    sessionStorage.setItem('adminApiKey', 'secret-key-123');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, status: 200, json: async () => ({ rules: [], total: 0 }),
    }));

    const captured: unknown[] = [];
    function KeyProbe() {
      const ctx = useAuth();
      captured.push(ctx);
      return null;
    }

    render(
      <AuthProvider>
        <KeyProbe />
      </AuthProvider>
    );

    const last = captured[captured.length - 1] as Record<string, unknown>;
    expect(last.key).toBeUndefined();
    expect(Object.keys(last)).not.toContain('key');
  });

  it('auto-logs out after inactivity timeout (M-6)', async () => {
    vi.useFakeTimers();
    sessionStorage.setItem('adminApiKey', 'valid-key');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, status: 200, json: async () => ({ rules: [], total: 0 }),
    }));

    render(
      <AuthProvider>
        <TestComponent />
      </AuthProvider>
    );
    // Flush the initial validate() promise chain with fake timers.
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.getByTestId('state')).toHaveTextContent('authenticated');

    // Advance past the inactivity timeout (30 min) + the check interval.
    await act(async () => { await vi.advanceTimersByTimeAsync(31 * 60 * 1000); });
    expect(screen.getByTestId('state')).toHaveTextContent('anonymous');
    expect(sessionStorage.getItem('adminApiKey')).toBeNull();

    vi.useRealTimers();
  });

  it('does not log out while the user is active (M-6)', async () => {
    vi.useFakeTimers();
    sessionStorage.setItem('adminApiKey', 'valid-key');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, status: 200, json: async () => ({ rules: [], total: 0 }),
    }));

    render(
      <AuthProvider>
        <TestComponent />
      </AuthProvider>
    );
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.getByTestId('state')).toHaveTextContent('authenticated');

    // Simulate activity every 5 minutes — never idle for 30 min.
    for (let i = 0; i < 7; i++) {
      await act(async () => {
        vi.advanceTimersByTime(5 * 60 * 1000);
        fireEvent.click(document.body);
      });
    }
    expect(screen.getByTestId('state')).toHaveTextContent('authenticated');
    expect(sessionStorage.getItem('adminApiKey')).toBe('valid-key');

    vi.useRealTimers();
  });
});
