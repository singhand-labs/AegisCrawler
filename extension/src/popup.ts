import { sendAction } from './messaging';

interface ServerConfig {
  baseUrl: string;
  apiKey?: string;
  adminApiKey?: string;
}

interface PopupState {
  isRecording: boolean;
  hasRecording: boolean;
  hasBaseUrl: boolean;
}

const state: PopupState = {
  isRecording: false,
  hasRecording: false,
  hasBaseUrl: false,
};

function getElement<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

function setStatus(message: string, type: 'info' | 'error' | 'success' | 'warning' = 'info'): void {
  const statusEl = getElement<HTMLDivElement>('status');
  if (statusEl) {
    statusEl.textContent = message;
    statusEl.className = type;
  }
}

function updateUI(): void {
  const startBtn = getElement<HTMLButtonElement>('start-recording');
  const stopBtn = getElement<HTMLButtonElement>('stop-recording');
  const downloadBtn = getElement<HTMLButtonElement>('download-rule');
  const enhanceBtn = getElement<HTMLButtonElement>('enhance-rule');

  if (startBtn) startBtn.disabled = state.isRecording;
  if (stopBtn) stopBtn.disabled = !state.isRecording;

  const ruleActionsEnabled = state.hasRecording;
  if (downloadBtn) downloadBtn.disabled = !ruleActionsEnabled;
  // Enhance additionally requires a configured server endpoint.
  if (enhanceBtn) enhanceBtn.disabled = !ruleActionsEnabled || !state.hasBaseUrl;
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
    sendAction('GET_STATE') as Promise<{ state: string; statusMessage?: string }>,
    sendAction('GET_LAST_RECORDING') as Promise<{ recording: unknown | null }>,
  ]);

  state.isRecording = stateResponse.state === 'recording';
  state.hasRecording = recordingResponse.recording != null;
  updateUI();
  if (stateResponse.statusMessage) setStatus(stateResponse.statusMessage, 'info');
}

async function startRecording(): Promise<void> {
  setStatus('正在开始录制...');
  const response = (await sendAction('START_RECORDING')) as { success?: boolean; error?: string };
  const error = handleResponse(response);
  if (error) {
    setStatus(error, 'error');
    return;
  }
  state.isRecording = true;
  updateUI();
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

async function saveConfig(): Promise<void> {
  const baseUrl = (getElement<HTMLInputElement>('baseUrl')?.value.trim() || '').replace(/\/+$/, '');
  const apiKey = getElement<HTMLInputElement>('apiKey')?.value.trim() || '';
  const adminApiKey = getElement<HTMLInputElement>('adminApiKey')?.value.trim() || '';
  const rememberSession = getElement<HTMLInputElement>('remember-session')?.checked ?? true;

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

function initPopup(): void {
  loadState().catch((err) => setStatus(`加载状态失败：${String(err)}`, 'error'));
  loadServerConfig().catch((err) => setStatus(`加载配置失败：${String(err)}`, 'error'));

  getElement<HTMLButtonElement>('start-recording')?.addEventListener('click', () => {
    startRecording().catch((err) => setStatus(`开始录制失败：${String(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('stop-recording')?.addEventListener('click', () => {
    stopRecording().catch((err) => setStatus(`停止录制失败：${String(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('download-rule')?.addEventListener('click', () => {
    downloadRule().catch((err) => setStatus(`下载规则失败：${String(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('enhance-rule')?.addEventListener('click', () => {
    enhanceRule().catch((err) => setStatus(`AI 增强失败：${String(err)}`, 'error'));
  });
  getElement<HTMLButtonElement>('save-config')?.addEventListener('click', () => {
    saveConfig().catch((err) => setStatus(`保存配置失败：${String(err)}`, 'error'));
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
