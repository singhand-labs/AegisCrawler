import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { sendAction, MESSAGE_RETRY_ATTEMPTS } from './messaging';

describe('sendAction', () => {
  let sendMessage: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    sendMessage = vi.fn();
    (globalThis as Record<string, unknown>).chrome = {
      runtime: { sendMessage },
    };
  });

  afterEach(() => {
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
  });

  it('returns the response on first success', async () => {
    sendMessage.mockResolvedValueOnce({ success: true, data: 'ok' });
    const response = await sendAction('PREDICT_INTENT');
    expect(response).toEqual({ success: true, data: 'ok' });
    expect(sendMessage).toHaveBeenCalledTimes(1);
    expect(sendMessage).toHaveBeenCalledWith({ action: 'PREDICT_INTENT', payload: undefined });
  });

  it('retries on transient connection errors and eventually succeeds', async () => {
    const connectionError = new Error('Could not establish connection. Receiving end does not exist.');
    sendMessage.mockRejectedValueOnce(connectionError).mockResolvedValueOnce({ success: true });
    const response = await sendAction('PREDICT_INTENT');
    expect(response).toEqual({ success: true });
    expect(sendMessage).toHaveBeenCalledTimes(2);
  });

  it('retries up to MESSAGE_RETRY_ATTEMPTS then throws', async () => {
    const connectionError = new Error('The message port closed before a response was received.');
    sendMessage.mockRejectedValue(connectionError);
    await expect(sendAction('PREDICT_INTENT')).rejects.toThrow('The message port closed before a response was received.');
    expect(sendMessage).toHaveBeenCalledTimes(MESSAGE_RETRY_ATTEMPTS);
  });

  it('does not retry non-transient errors', async () => {
    const otherError = new Error('Some other error');
    sendMessage.mockRejectedValueOnce(otherError);
    await expect(sendAction('PREDICT_INTENT')).rejects.toThrow('Some other error');
    expect(sendMessage).toHaveBeenCalledTimes(1);
  });

  it('passes payload through', async () => {
    sendMessage.mockResolvedValueOnce({ success: true });
    await sendAction('GENERATE_DSL_FROM_INTENT', { intent: { id: 'c1' } });
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'GENERATE_DSL_FROM_INTENT',
      payload: { intent: { id: 'c1' } },
    });
  });

  it('throws a clear error when chrome is undefined', async () => {
    delete (globalThis as Record<string, unknown>).chrome;
    await expect(sendAction('PREDICT_INTENT')).rejects.toThrow(/Chrome extension runtime is not available/);
  });

  it('throws a clear error when chrome.runtime is undefined', async () => {
    (globalThis as Record<string, unknown>).chrome = {};
    await expect(sendAction('PREDICT_INTENT')).rejects.toThrow(/Chrome extension runtime is not available/);
  });

  it('throws a clear error when chrome.runtime.sendMessage is not a function', async () => {
    (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage: 'not-a-function' } };
    await expect(sendAction('PREDICT_INTENT')).rejects.toThrow(/Chrome extension runtime is not available/);
  });
});
