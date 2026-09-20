import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ReplayTransport } from './replay-transport';

describe('ReplayTransport', () => {
  let transport: ReplayTransport;

  beforeEach(() => {
    transport = new ReplayTransport();
    delete (globalThis as any).chrome;
  });

  it('returns the provided rule from fetchRule', async () => {
    const rule = { id: 'test', version: '1.0.0' } as any;
    transport.setRule(rule);
    const fetched = await transport.fetchRule('test');
    expect(fetched).toBe(rule);
  });

  it('sends log via chrome.runtime.sendMessage', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };
    await transport.sendLog('info', 'hello', { extra: 1 });
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'REPLAY_PROGRESS',
      payload: { type: 'log', level: 'info', message: 'hello', extra: { extra: 1 } },
    });
  });

  it('sends result via chrome.runtime.sendMessage', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };
    await transport.sendResult({ value: 42 }, true);
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'REPLAY_PROGRESS',
      payload: { type: 'result', payload: { value: 42 }, immediate: true },
    });
  });

  it('suppresses the executor terminal result', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };

    await transport.sendResult({ __final: true, value: 42 }, true);

    expect(sendMessage).not.toHaveBeenCalled();
  });

  it('sendHeartbeat returns cancelRequested false', async () => {
    const heartbeat = await transport.sendHeartbeat();
    expect(heartbeat).toEqual({ cancelRequested: false });
  });

  it('sendHeartbeat forwards a heartbeat progress so the background idle timer resets (M-1)', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };
    await transport.sendHeartbeat({ url: 'https://example.com/page' });
    // M-1: heartbeat must send a REPLAY_PROGRESS 'status' message so the
    // background's idle timer (120s without progress) is reset. Without
    // this, a slow page load between steps causes premature teardown.
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'REPLAY_PROGRESS',
      payload: expect.objectContaining({ type: 'status', status: 'heartbeat' }),
    });
  });

  it('sendStatus sends status progress', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };
    await transport.sendStatus('running', 'started');
    expect(sendMessage).toHaveBeenCalledWith({
      action: 'REPLAY_PROGRESS',
      payload: { type: 'status', status: 'running', message: 'started' },
    });
  });

  it('sendSnapshot replaces raw HTML with a redacted semantic snapshot', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };
    document.body.innerHTML = `
      <main><p>visible diagnostic text</p><input type="password" value="live-secret"></main>
      <script>window.rawSecret = 'script-secret'</script>
    `;

    await transport.sendSnapshot({ name: 'snap', type: 'html', data: '<html>source-raw-secret</html>' });

    const message = sendMessage.mock.calls[0][0];
    expect(message.action).toBe('REPLAY_PROGRESS');
    expect(message.payload.type).toBe('snapshot');
    expect(message.payload.snapshot).toMatchObject({ name: 'snap', type: 'dom' });
    const data = message.payload.snapshot.data as string;
    const parsed = JSON.parse(data);
    expect(parsed.format).toBe('semantic-dom-v1');
    expect(data).toContain('visible diagnostic text');
    expect(data).toContain('[REDACTED]');
    expect(data).not.toContain('source-raw-secret');
    expect(data).not.toContain('live-secret');
    expect(data).not.toContain('script-secret');
  });

  it('omits semantic DOM content that exceeds the replay snapshot budget', async () => {
    const sendMessage = vi.fn();
    (globalThis as any).chrome = { runtime: { sendMessage } };
    document.body.innerHTML = Array.from(
      { length: 1500 },
      (_, index) => `<div aria-label="${index}-${'x'.repeat(1000)}">row</div>`,
    ).join('');

    await transport.sendSnapshot({ name: 'large', type: 'html', data: 'raw-data-must-not-pass' });

    const data = sendMessage.mock.calls[0][0].payload.snapshot.data as string;
    const parsed = JSON.parse(data);
    expect(new TextEncoder().encode(data).byteLength).toBeLessThan(384 * 1024);
    expect(parsed).toMatchObject({ format: 'semantic-dom-v1', domOmitted: true });
    expect(data).not.toContain('raw-data-must-not-pass');
  });

  it('does not throw when chrome is undefined', async () => {
    (globalThis as any).chrome = undefined;
    await expect(transport.sendLog('info', 'hello')).resolves.toBeUndefined();
  });

  it('does not throw when chrome.runtime.sendMessage is undefined', async () => {
    (globalThis as any).chrome = { runtime: {} };
    await expect(transport.sendLog('info', 'hello')).resolves.toBeUndefined();
  });
});
