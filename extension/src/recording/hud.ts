import type { RecordingLimitKind } from '../../../src/rule-generator/types';

/** Surfaces recording state inside the page so the user always knows the
 *  recorder is live, can stop it without reopening the popup, and sees
 *  limit warnings before a hard stop happens. Rendered inside an open
 *  Shadow DOM (same pattern as the page-mark overlay) so hostile page CSS
 *  cannot restyle or displace it. */
export interface RecordingHudAdapter {
  /** Called when the user clicks the HUD stop button. */
  requestStop(): void;
  /** Recording start time in ms for the elapsed timer. */
  startedAt(): number;
  /** Show the Alt+M marking hint (v2 recordings only). */
  showMarkHint?: boolean;
}

const HOST_ATTR = 'data-aegis-recording-hud';

const KIND_LABELS: Record<RecordingLimitKind, string> = {
  actions: '操作数',
  duration: '时长',
  size: '体积',
};

export class RecordingHud {
  private readonly adapter: RecordingHudAdapter;
  private host: HTMLElement | null = null;
  private timerEl: HTMLElement | null = null;
  private countEl: HTMLElement | null = null;
  private toastArea: HTMLElement | null = null;
  private stopBtn: HTMLButtonElement | null = null;
  private timerId: ReturnType<typeof setInterval> | undefined;

  constructor(adapter: RecordingHudAdapter) {
    this.adapter = adapter;
  }

  mount(): void {
    if (this.host?.isConnected) return;
    this.unmount();
    const host = document.createElement('div');
    host.setAttribute(HOST_ATTR, '');
    const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
    root.innerHTML = this.template();
    this.host = host;
    this.timerEl = root.querySelector('[data-hud="timer"]');
    this.countEl = root.querySelector('[data-hud="count"]');
    this.toastArea = root.querySelector('[data-hud="toasts"]');
    this.stopBtn = root.querySelector<HTMLButtonElement>('[data-hud="stop"]');
    this.stopBtn?.addEventListener('click', (event) => {
      event.preventDefault();
      event.stopPropagation();
      this.setStopPending();
      this.adapter.requestStop();
    });
    if (this.adapter.showMarkHint !== true) {
      root.querySelector('[data-hud="mark-hint"]')?.remove();
    }
    this.renderElapsed();
    this.setEventCount(0);
    this.timerId = setInterval(() => this.renderElapsed(), 1000);
    (document.documentElement || document.body).appendChild(host);
  }

  unmount(): void {
    if (this.timerId !== undefined) {
      clearInterval(this.timerId);
      this.timerId = undefined;
    }
    this.host?.remove();
    this.host = null;
    this.timerEl = null;
    this.countEl = null;
    this.toastArea = null;
    this.stopBtn = null;
  }

  setEventCount(count: number): void {
    if (this.countEl) this.countEl.textContent = `${count} 步`;
  }

  /** Draining a large recording takes seconds; the click must visibly land. */
  setStopPending(): void {
    if (!this.stopBtn) return;
    this.stopBtn.disabled = true;
    this.stopBtn.textContent = '正在停止…';
  }

  /** A rejected stop must not leave a dead button behind. */
  setStopFailed(message: string): void {
    if (this.stopBtn) {
      this.stopBtn.disabled = false;
      this.stopBtn.textContent = '停止录制';
    }
    this.showStopError(message);
  }

  private showStopError(message: string): void {
    if (!this.toastArea || !this.host?.isConnected) return;
    const toast = document.createElement('div');
    toast.className = 'toast';
    toast.setAttribute('role', 'alert');
    toast.textContent = `停止录制失败：${message}`;
    while (this.toastArea.children.length >= 3) {
      this.toastArea.firstElementChild?.remove();
    }
    this.toastArea.appendChild(toast);
    setTimeout(() => toast.remove(), 6000);
  }

  /** Chinese toast for an approaching recording limit; auto-dismisses. */
  showLimitWarning(kind: RecordingLimitKind, percent: number): void {
    if (!this.toastArea || !this.host?.isConnected) return;
    const label = KIND_LABELS[kind] ?? kind;
    const toast = document.createElement('div');
    toast.className = 'toast';
    toast.setAttribute('role', 'status');
    toast.textContent = `录制${label}已接近上限（约 ${Math.min(99, Math.round(percent * 100))}%），请尽快完成操作并停止录制`;
    // Keep at most three toasts visible.
    while (this.toastArea.children.length >= 3) {
      this.toastArea.firstElementChild?.remove();
    }
    this.toastArea.appendChild(toast);
    setTimeout(() => toast.remove(), 6000);
  }

  /** @internal exposed for tests that do not want to wait a real second */
  renderElapsed(): void {
    if (!this.timerEl) return;
    const seconds = Math.max(0, Math.floor((Date.now() - this.adapter.startedAt()) / 1000));
    const mm = String(Math.floor(seconds / 60) % 60).padStart(2, '0');
    const ss = String(seconds % 60).padStart(2, '0');
    const hours = Math.floor(seconds / 3600);
    this.timerEl.textContent = hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
  }

  private template(): string {
    return `
<style>
  :host { all: initial; color-scheme: light; }
  * { box-sizing: border-box; }
  .wrap {
    position: fixed;
    top: 14px;
    right: 14px;
    z-index: 2147483647;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px 8px 14px;
    border-radius: 999px;
    background: rgba(17, 24, 39, 0.92);
    color: #f9fafb;
    font: 500 13px/1.2 system-ui, -apple-system, 'Segoe UI', Roboto, 'PingFang SC', sans-serif;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
    user-select: none;
  }
  .dot {
    width: 9px; height: 9px; border-radius: 50%;
    background: #f87171; flex: none;
    animation: aegis-hud-pulse 1.2s ease-in-out infinite;
  }
  @keyframes aegis-hud-pulse {
    0%, 100% { box-shadow: 0 0 0 0 rgba(248, 113, 113, 0.55); }
    50% { box-shadow: 0 0 0 6px rgba(248, 113, 113, 0); }
  }
  .label { font-weight: 600; }
  .timer {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-variant-numeric: tabular-nums;
    color: #e5e7eb;
  }
  .count {
    font-size: 11px;
    color: #9ca3af;
    white-space: nowrap;
  }
  .mark-hint {
    font-size: 11px;
    color: #9ca3af;
    border-left: 1px solid rgba(255, 255, 255, 0.18);
    padding-left: 10px;
    white-space: nowrap;
  }
  button.stop {
    appearance: none;
    border: none;
    border-radius: 999px;
    padding: 5px 14px;
    background: #ef4444;
    color: #fff;
    font: 600 12px/1.2 inherit;
    cursor: pointer;
    white-space: nowrap;
    transition: background 140ms ease;
  }
  button.stop:hover { background: #dc2626; }
  button.stop:disabled { opacity: 0.6; cursor: not-allowed; }
  .toasts {
    position: fixed;
    top: 62px;
    right: 14px;
    z-index: 2147483647;
    display: flex;
    flex-direction: column;
    gap: 8px;
    pointer-events: none;
    font: 400 12px/1.5 system-ui, -apple-system, 'Segoe UI', Roboto, 'PingFang SC', sans-serif;
  }
  .toast {
    max-width: 320px;
    padding: 10px 14px;
    border-radius: 10px;
    background: #fffbeb;
    border: 1px solid #fde68a;
    color: #92400e;
    box-shadow: 0 6px 18px rgba(0, 0, 0, 0.14);
    animation: aegis-hud-slide 200ms ease;
  }
  @keyframes aegis-hud-slide {
    from { opacity: 0; transform: translateY(-6px); }
    to { opacity: 1; transform: translateY(0); }
  }
</style>
<div class="wrap" part="hud">
  <span class="dot" aria-hidden="true"></span>
  <span class="label">录制中</span>
  <span class="timer" data-hud="timer">00:00</span>
  <span class="count" data-hud="count">0 步</span>
  <span class="mark-hint" data-hud="mark-hint">Alt+M 圈选标注意图</span>
  <button type="button" class="stop" data-hud="stop">停止录制</button>
</div>
<div class="toasts" data-hud="toasts"></div>`;
  }
}
