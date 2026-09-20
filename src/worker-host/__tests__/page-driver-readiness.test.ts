import { describe, expect, it, vi } from 'vitest';
import { waitForPageLoad } from '../page-driver';

describe('WorkerHost page navigation readiness', () => {
  it('waits adaptively for the full load event using the configured timeout', async () => {
    let release!: () => void;
    const delayedLoad = new Promise<void>((resolve) => { release = resolve; });
    const waitForLoadState = vi.fn(() => delayedLoad);
    const waiting = waitForPageLoad({ waitForLoadState } as any, 42_000);

    expect(waitForLoadState).toHaveBeenCalledWith('load', { timeout: 42_000 });
    let settled = false;
    void waiting.then(() => { settled = true; });
    await Promise.resolve();
    expect(settled).toBe(false);

    release();
    await expect(waiting).resolves.toBeUndefined();
  });

  it('fails closed when the full load event exceeds the configured timeout', async () => {
    const timeout = new Error('page.waitForLoadState: Timeout 60000ms exceeded');
    const waitForLoadState = vi.fn(async () => { throw timeout; });

    await expect(waitForPageLoad({ waitForLoadState } as any, 60_000))
      .rejects.toThrow('Timeout 60000ms exceeded');
    expect(waitForLoadState).toHaveBeenCalledWith('load', { timeout: 60_000 });
  });
});
