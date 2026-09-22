import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { RecordingHud } from './hud';

function createHud(overrides?: {
  startedAt?: number;
  showMarkHint?: boolean;
  requestStop?: () => void;
}): { hud: RecordingHud; requestStop: ReturnType<typeof vi.fn> } {
  const requestStop = vi.fn(overrides?.requestStop ?? (() => undefined));
  const hud = new RecordingHud({
    requestStop,
    startedAt: () => overrides?.startedAt ?? Date.now(),
    showMarkHint: overrides?.showMarkHint,
  });
  return { hud, requestStop };
}

function hudEl(): HTMLElement {
  const host = document.querySelector<HTMLElement>('[data-aegis-recording-hud]');
  if (!host?.shadowRoot) throw new Error('HUD not mounted');
  return host.shadowRoot as unknown as HTMLElement;
}

describe('RecordingHud', () => {
  beforeEach(() => {
    document.querySelectorAll('[data-aegis-recording-hud]').forEach((node) => node.remove());
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('mounts the pill, shows the elapsed timer, and cleans up on unmount', () => {
    const { hud } = createHud({ startedAt: Date.now() - 65000 });
    hud.mount();
    expect(hudEl().querySelector('[data-hud="timer"]')?.textContent).toBe('01:05');
    expect(hudEl().querySelector('[data-hud="stop"]')).toBeTruthy();
    hud.unmount();
    expect(document.querySelector('[data-aegis-recording-hud]')).toBeNull();
  });

  it('shows the Alt+M hint for v2 recordings and hides it otherwise', () => {
    const { hud: v2 } = createHud({ showMarkHint: true });
    v2.mount();
    expect(hudEl().querySelector('[data-hud="mark-hint"]')).toBeTruthy();
    v2.unmount();

    const { hud: v1 } = createHud({ showMarkHint: false });
    v1.mount();
    expect(hudEl().querySelector('[data-hud="mark-hint"]')).toBeNull();
    v1.unmount();
  });

  it('reports live event counts', () => {
    const { hud } = createHud();
    hud.mount();
    hud.setEventCount(42);
    expect(hudEl().querySelector('[data-hud="count"]')?.textContent).toBe('42 步');
    hud.unmount();
  });

  it('routes the stop button through the adapter and disables it after one click', () => {
    const { hud, requestStop } = createHud();
    hud.mount();
    const stopBtn = hudEl().querySelector<HTMLButtonElement>('[data-hud="stop"]');
    if (!stopBtn) throw new Error('stop button missing');
    stopBtn.click();
    stopBtn.click();
    expect(requestStop).toHaveBeenCalledTimes(1);
    expect(stopBtn.disabled).toBe(true);
    // The click must visibly land even though draining a large recording
    // takes seconds: the label flips to a pending state immediately.
    expect(stopBtn.textContent).toBe('正在停止…');
    hud.unmount();
  });

  it('restores the stop button and surfaces an alert when the stop is rejected', () => {
    const { hud } = createHud();
    hud.mount();
    const stopBtn = hudEl().querySelector<HTMLButtonElement>('[data-hud="stop"]');
    if (!stopBtn) throw new Error('stop button missing');
    stopBtn.click();
    hud.setStopFailed('当前页面没有进行中的录制');
    expect(stopBtn.disabled).toBe(false);
    expect(stopBtn.textContent).toBe('停止录制');
    const toast = hudEl().querySelector('.toast');
    expect(toast?.getAttribute('role')).toBe('alert');
    expect(toast?.textContent).toContain('停止录制失败');
    expect(toast?.textContent).toContain('当前页面没有进行中的录制');
    hud.unmount();
  });

  it('renders a dismissible Chinese toast for approaching limits', () => {
    vi.useFakeTimers();
    try {
      const { hud } = createHud();
      hud.mount();
      hud.showLimitWarning('size', 0.83);
      const toasts = hudEl().querySelectorAll('.toast');
      expect(toasts.length).toBe(1);
      expect(toasts[0].textContent).toContain('录制体积已接近上限');
      expect(toasts[0].textContent).toContain('83%');
      vi.advanceTimersByTime(6100);
      expect(hudEl().querySelectorAll('.toast').length).toBe(0);
      hud.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps at most three toasts visible', () => {
    const { hud } = createHud();
    hud.mount();
    hud.showLimitWarning('actions', 0.81);
    hud.showLimitWarning('duration', 0.82);
    hud.showLimitWarning('size', 0.83);
    hud.showLimitWarning('size', 0.9);
    expect(hudEl().querySelectorAll('.toast').length).toBe(3);
    hud.unmount();
  });

  it('does not throw when unmounted before any toast', () => {
    const { hud } = createHud();
    hud.mount();
    hud.unmount();
    expect(() => hud.showLimitWarning('size', 0.9)).not.toThrow();
    expect(() => hud.setEventCount(3)).not.toThrow();
  });
});
