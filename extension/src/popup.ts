import { sendAction } from './messaging';

interface ServerConfig {
  baseUrl: string;
  apiKey?: string;
  adminApiKey?: string;
}

interface RecordingStateResponse {
  state: string;
  statusMessage?: string;
  startedAt?: number;
}

interface PopupState {
  isRecording: boolean;
  hasRecording: boolean;
  hasBaseUrl: boolean;
  busy: boolean;
  recordingStartedAt?: number;
}

const state: PopupState = {
  isRecording: false,
  hasRecording: false,
  hasBaseUrl: false,
  busy: false,
};

let liveTimer: ReturnType<typeof setInterval> | undefined;

function getElement<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

function setStatus(message: string, type: 'info' | 'error' | 'success' | 'warning' = 'info'): void {
  const statusEl = getElement<HTMLDivElement>('status');
  if (statusEl) {
    statusEl.textContent = message;
    statusEl.className = `status-pill ${type}`.trim();
  }
}

/** Reduce raw exceptions to a single user-facing line; full detail stays in the console. */
function formatUserError(err: unknown): string {
  const text = err instanceof Error ? err.message : String(err);
  const firstLine = text.split('\n')[0].trim();
  return firstLine || '未知错误';
}

function updateUI(): void {
  const startBtn = getElement<HTMLButtonElement>('start-recording');
  const stopBtn = getElement<HTMLButtonElement>('stop-recording');
  const downloadBtn = getElement<HTMLButtonElement>('download-rule');
  const enhanceBtn = getElement<HTMLButtonElement>('enhance-rule');

  const busy = state.busy;
  if (startBtn) startBtn.disabled = busy || state.isRecording;
  if (stopBtn) stopBtn.disabled = busy || !state.isRecording;

  const ruleActionsEnabled = !busy && state.hasRecording;
  if (downloadBtn) downloadBtn.disabled = !ruleActionsEnabled;
  // Enhance additionally requires a configured server endpoint.
  if (enhanceBtn) enhanceBtn.disabled = !ruleActionsEnabled || !state.hasBaseUrl;
  if (getElement<HTMLButtonElement>('save-config')) {
    (getElement<HTMLButtonElement>('save-config') as HTMLButtonElement).disabled = busy;
  }
}

/** Disable every action while one is in flight so a double click cannot fire duplicates. */
async function withBusy(action: () => Promise<void>): Promise<void> {
  if (state.busy) return;
  state.busy = true;
  updateUI();
  try {
    await action();
  } finally {
    state.busy = false;
    updateUI();
  }
}

function formatElapsed(startedAt: number): string {
  const seconds = Math.max(0, Math.floor((Date.now() - startedAt) / 1000));
  const mm = String(Math.floor(seconds / 60)).padStart(2, '0');
  const ss = String(seconds % 60).padStart(2, '0');
  const hours = Math.floor(seconds / 3600);
  return hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
}

function stopLiveIndicator(): void {
  if (liveTimer !== undefined) {
    clearInterval(liveTimer);
    liveTimer = undefined;
  }
  getElement<HTMLElement>('recording-live')?.classList.add('hidden');
}

function startLiveIndicator(startedAt?: number): void {
  stopLiveIndicator();
  const anchor = getElement<HTMLElement>('recording-live');
  if (!anchor) return;
  const base = startedAt ?? Date.now();
  state.recordingStartedAt = base;
  anchor.classList.remove('hidden');
  const render = (): void => {
    const timerEl = getElement<HTMLElement>('recording-timer');
    if (timerEl && state.recordingStartedAt) timerEl.textContent = formatElapsed(state.recordingStartedAt);
  };
  render();
  // Pure UI ticking from the known start time: no background messages, so a
  // leaked interval after unmount is a harmless no-op on a detached DOM.
  liveTimer = setInterval(render, 1000);
}

function handleResponse(response: { success?: boolean; error?: string } | undefined): string | null {
  if (!response) {
    return '未收到 service worker 响应';
  }
  // Be conservative: any response that does not explicitly assert success is
  // treated as a failure. Defends against handlers returning {} on an
  // unexpected code path.
  if (response.success !== true) {
    return response.error || '未知错误';
  }
  return null;
}

async function loadServerConfig(): Promise<void> {
  const [localResult, sessionResult] = await Promise.all([
    chrome.storage.local.get('serverConfig'),
    chrome.storage.session.get('serverKeys'),
  ]);
  const config = (localResult.serverConfig as ServerConfig | undefined) ?? { baseUrl: '' };
  const keys = (sessionResult.serverKeys as Pick<ServerConfig, 'apiKey' | 'adminApiKey'> | undefined) ?? {};

  const baseUrlInput = getElement<HTMLInputElement>('baseUrl');
  const apiKeyInput = getElement<HTMLInputElement>('apiKey');
  const adminApiKeyInput = getElement<HTMLInputElement>('adminApiKey');
  if (baseUrlInput) baseUrlInput.value = config.baseUrl || '';
  if (apiKeyInput) apiKeyInput.value = keys.apiKey || '';
  if (adminApiKeyInput) adminApiKeyInput.value = keys.adminApiKey || '';

  state.hasBaseUrl = !!config.baseUrl;
  updateUI();
}

async function loadState(): Promise<void> {
  const [stateResponse, recordingResponse] = await Promise.all([
    sendAction('GET_STATE') as Promise<RecordingStateResponse>,
    sendAction('GET_LAST_RECORDING') as Promise<{ recording: unknown | null }>,
  ]);

  state.isRecording = stateResponse.state === 'recording';
  state.hasRecording = recordingResponse.recording != null;
  updateUI();
  if (state.isRecording) {
    startLiveIndicator(stateResponse.startedAt);
  } else {
    stopLiveIndicator();
  }
  if (stateResponse.statusMessage) setStatus(stateResponse.statusMessage, 'info');
}

/** Ask before silently discarding a previous recording. jsdom's unimplemented
 *  confirm() returns undefined, so only an explicit `false` cancels. */
function confirmOverwritePreviousRecording(): boolean {
  if (!state.hasRecording || state.isRecording) return true;
  try {
    return window.confirm('重新录制会覆盖上一次录制的内容（已生成的未保存规则也会失效），确定继续吗？') !== false;
  } catch {
    return true;
  }
}

async function startRecording(): Promise<void> {
  if (!confirmOverwritePreviousRecording()) return;
  setStatus('正在开始录制...');
  const response = (await sendAction('START_RECORDING')) as { success?: boolean; error?: string };
  const error = handleResponse(response);
  if (error) {
    setStatus(error, 'error');
    return;
  }
  state.isRecording = true;
  updateUI();
  startLiveIndicator(Date.now());
  setStatus('录制已开始', 'success');
}

async function stopRecording(): Promise<void> {
  setStatus('正在停止录制...');
  const response = (await sendAction('STOP_RECORDING')) as {
    success?: boolean;
    error?: string;
    recording?: unknown;
    persistenceWarning?: string;
  };
  const error = handleResponse(response);
  if (error) {
    setStatus(error, 'error');
    return;
  }
  state.isRecording = false;
  state.hasRecording = response.recording != null;
  updateUI();
  stopLiveIndicator();
  if (response.persistenceWarning) {
    // Recording succeeded locally; only server-side persistence is deferred.
    // Render as a warning, not an error, so users don't think recording failed.
    setStatus(`录制已安全保存在本地；服务端持久化将在生成前重试：${response.persistenceWarning}`, 'warning');
    openIntentPage();
    return;
  }
  setStatus('录制已停止，正在打开意图确认向导...', 'success');
  openIntentPage();
}

function openIntentPage(): void {
  const intentUrl = chrome.runtime.getURL('intent/intent-page.html');
  chrome.tabs.create({ url: intentUrl });
}

async function downloadRule(): Promise<void> {
  setStatus('正在下载规则...');
  const response = (await sendAction('DOWNLOAD_RULE')) as { success?: boolean; error?: string };
  const error = handleResponse(response);
  if (error) {
    setStatus(error, 'error');
    return;
  }
  setStatus('规则已下载', 'success');
}

async function enhanceRule(): Promise<void> {
  const userHint = getElement<HTMLInputElement>('userHint')?.value.trim() ?? '';
  setStatus('正在 AI 增强...');
  const response = (await sendAction('ENHANCE_RULE', { userHint })) as {
    success?: boolean;
    error?: string;
    ruleId?: string;
  };
  const error = handleResponse(response);
  if (error) {
    setStatus(error, 'error');
    return;
  }
  // Defend against a server response that signals success but returns no
  // ruleId — display a warning instead of bragging about an empty ID.
  if (!response.ruleId) {
    setStatus('增强已完成，但服务端未返回规则 ID', 'warning');
    return;
  }
  setStatus(`增强完成，规则 ID：${response.ruleId}`, 'success');
  const baseUrl = getElement<HTMLInputElement>('baseUrl')?.value.trim() ?? '';
  if (baseUrl) {
    // Encode the ruleId so a malformed server value cannot produce an
    // unexpected URL path (e.g. traversal via ../).
    chrome.tabs.create({
      url: `${baseUrl.replace(/\/+$/, '')}/admin/rules/${encodeURIComponent(response.ruleId)}`,
    });
  }
}

function validateBaseUrl(baseUrl: string): string | null {
  if (!baseUrl) return null;
  try {
    const parsed = new URL(baseUrl);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return '服务端地址必须以 http:// 或 https:// 开头';
    }
    return null;
  } catch {
    return '服务端地址不是合法 URL，例如 http://localhost:8080';
  }
}

async function saveConfig(): Promise<void> {
  const baseUrl = (getElement<HTMLInputElement>('baseUrl')?.value.trim() || '').replace(/\/+$/, '');
  const apiKey = getElement<HTMLInputElement>('apiKey')?.value.trim() || '';
  const adminApiKey = getElement<HTMLInputElement>('adminApiKey')?.value.trim() || '';
  const rememberSession = getElement<HTMLInputElement>('remember-session')?.checked ?? true;

  const invalidUrl = validateBaseUrl(baseUrl);
  if (invalidUrl) {
    setStatus(invalidUrl, 'warning');
    return;
  }

  setStatus('正在保存配置...');
  const response = (await sendAction('SET_SERVER_CONFIG', {
    baseUrl,
    apiKey,
    adminApiKey,
    rememberSession,
  })) as { success?: boolean; error?: string };
  const error = handleResponse(response);
  if (error) {
    setStatus(error, 'error');
    return;
  }
  state.hasBaseUrl = !!baseUrl;
  updateUI();
  setStatus('配置已保存', 'success');
}

function wireKeyToggles(): void {
  document.querySelectorAll<HTMLButtonElement>('.key-toggle').forEach((toggle) => {
    toggle.addEventListener('click', () => {
      const targetId = toggle.getAttribute('data-key-input');
      const input = targetId ? getElement<HTMLInputElement>(targetId) : null;
      if (!input) return;
      const reveal = input.type === 'password';
      input.type = reveal ? 'text' : 'password';
      toggle.textContent = reveal ? '隐藏' : '显示';
    });
  });
}

function initPopup(): void {
  stopLiveIndicator();
  loadState().catch((err) => setStatus(`加载状态失败：${formatUserError(err)}`, 'error'));
  loadServerConfig().catch((err) => setStatus(`加载配置失败：${formatUserError(err)}`, 'error'));
  wireKeyToggles();

  getElement<HTMLButtonElement>('start-recording')?.addEventListener('click', () => {
    withBusy(() => startRecording()).catch((err) => setStatus(`开始录制失败：${formatUserError(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('stop-recording')?.addEventListener('click', () => {
    withBusy(() => stopRecording()).catch((err) => setStatus(`停止录制失败：${formatUserError(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('download-rule')?.addEventListener('click', () => {
    withBusy(() => downloadRule()).catch((err) => setStatus(`下载规则失败：${formatUserError(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('enhance-rule')?.addEventListener('click', () => {
    withBusy(() => enhanceRule()).catch((err) => setStatus(`AI 增强失败：${formatUserError(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('save-config')?.addEventListener('click', () => {
    withBusy(() => saveConfig()).catch((err) => setStatus(`保存配置失败：${formatUserError(err)}`, 'error'));
  });
}

function onReady(): void {
  initPopup();
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', onReady);
} else {
  onReady();
}

export { initPopup };
