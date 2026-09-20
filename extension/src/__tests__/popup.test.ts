import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// IDs the popup logic (popup.ts) and these tests depend on. The drift guard
// test below asserts that the real popup.html contains every one of them, so
// renaming or removing an element in popup.html without updating both the
// fixture below and popup.ts will fail CI instead of silently breaking the UI.
const POPUP_ELEMENT_IDS = [
  'status',
  'baseUrl',
  'apiKey',
  'adminApiKey',
  'remember-session',
  'save-config',
  'start-recording',
  'stop-recording',
  'download-rule',
  'enhance-rule',
  'userHint',
] as const;


const POPUP_HTML = `
  <header>
    <h1>AegisCrawler 录制器</h1>
    <div id="status">就绪</div>
  </header>

  <section class="config-section">
    <h2>服务端配置</h2>
    <label for="baseUrl">服务端地址</label>
    <input id="baseUrl" type="text" placeholder="http://localhost:8080" />
    <label for="apiKey">Worker API Key</label>
    <input id="apiKey" type="password" placeholder="可选" />
    <label for="adminApiKey">Admin API Key</label>
    <input id="adminApiKey" type="password" placeholder="可选" />
    <label class="checkbox-label">
      <input id="remember-session" type="checkbox" checked />
      在当前浏览器会话中记住密钥
    </label>
    <p class="warning-text">API 密钥不会跨浏览器重启保留。</p>
    <button id="save-config">保存配置</button>
  </section>

  <section class="recording-section">
    <h2>录制</h2>
    <button id="start-recording">开始录制</button>
    <button id="stop-recording" disabled>停止录制</button>
  </section>

  <section class="rule-section">
    <h2>规则操作</h2>
    <button id="download-rule" disabled>下载规则</button>
  </section>

  <section class="rule-section">
    <h2>AI 增强</h2>
    <label for="userHint">优化意图（可选）</label>
    <input id="userHint" type="text" placeholder="例如：把商品标题和价格一起抓下来" />
    <button id="enhance-rule" disabled>AI 增强生成</button>
  </section>
`;

const sampleRecording = {
  version: '1.0.0',
  meta: {
    startUrl: 'https://example.com/',
    title: 'Example',
    recordedAt: '2026-07-06T00:00:00Z',
    domain: 'example.com',
  },
  events: [],
  snapshots: [],
};

function createChromeMock() {
  // Default to a configured baseUrl so enhance-rule tests can exercise the
  // happy path without each one re-running save-config. Tests that need an
  // empty config can overwrite chromeMock.localStorage.serverConfig.
  const localStorage: Record<string, unknown> = {
    serverConfig: { baseUrl: 'http://localhost:8080' },
  };
  const sessionStorage: Record<string, unknown> = {};
  const options = {
    startError: undefined as string | undefined,
    startReturnsUndefined: false,
    startReject: false,
    stopError: undefined as string | undefined,
    stopReject: false,
    stopPersistenceWarning: undefined as string | undefined,
    saveConfigError: undefined as string | undefined,
    saveConfigReject: false,
    enhanceError: undefined as string | undefined,
    enhanceReject: false,
    enhanceNoRuleId: false,
    enhanceRuleId: undefined as string | undefined,
    downloadError: undefined as string | undefined,
    downloadReject: false,
    loadStateReject: false,
  };

  const tabsCreate = vi.fn();

  const sendMessage = vi.fn(async (message: { action: string; payload?: unknown }) => {
    switch (message.action) {
      case 'GET_STATE':
        if (options.loadStateReject) {
          throw new Error('state failed');
        }
        return { state: 'idle' };
      case 'GET_LAST_RECORDING':
        return { recording: null };
      case 'START_RECORDING':
        if (options.startReturnsUndefined) {
          return undefined;
        }
        if (options.startError) {
          return { success: false, error: options.startError };
        }
        if (options.startReject) {
          throw new Error('start failed');
        }
        return { success: true };
      case 'STOP_RECORDING':
        if (options.stopError) {
          return { success: false, error: options.stopError };
        }
        if (options.stopReject) {
          throw new Error('stop failed');
        }
        return {
          success: true,
          recording: sampleRecording,
          persistenceWarning: options.stopPersistenceWarning,
        };
      case 'GENERATE_RULE':
        return {
          success: true,
          yaml: 'id: ext-1\nname: Sample rule\ndomain: example.com',
        };
      case 'DOWNLOAD_RULE':
        if (options.downloadError) {
          return { success: false, error: options.downloadError };
        }
        if (options.downloadReject) {
          throw new Error('download failed');
        }
        return { success: true, downloadId: 7 };
      case 'ENHANCE_RULE':
        if (options.enhanceError) {
          return { success: false, error: options.enhanceError };
        }
        if (options.enhanceReject) {
          throw new Error('enhance failed');
        }
        if (options.enhanceNoRuleId) {
          return { success: true };
        }
        return { success: true, ruleId: options.enhanceRuleId ?? 'rule-enhanced-1' };
      case 'SET_SERVER_CONFIG':
        if (options.saveConfigError) {
          return { success: false, error: options.saveConfigError };
        }
        if (options.saveConfigReject) {
          throw new Error('save failed');
        }
        storage.serverConfig = message.payload;
        return { success: true };
      default:
        return { success: false, error: `unknown action: ${message.action}` };
    }
  });

  function createStorageArea(storage: Record<string, unknown>) {
    return {
      get: vi.fn(async (keys?: string | string[] | Record<string, unknown> | null) => {
        if (keys === undefined || keys === null) {
          return { ...storage };
        }
        if (typeof keys === 'string') {
          return keys in storage ? { [keys]: storage[keys] } : {};
        }
        if (Array.isArray(keys)) {
          return keys.reduce((acc, key) => {
            if (key in storage) acc[key] = storage[key];
            return acc;
          }, {} as Record<string, unknown>);
        }
        return Object.entries(keys).reduce((acc, [key, defaultValue]) => {
          acc[key] = key in storage ? storage[key] : defaultValue;
          return acc;
        }, {} as Record<string, unknown>);
      }),
      set: vi.fn(async (items: Record<string, unknown>) => {
        Object.assign(storage, items);
      }),
      remove: vi.fn(async (keys: string | string[]) => {
        const keyArray = Array.isArray(keys) ? keys : [keys];
        for (const key of keyArray) {
          delete storage[key];
        }
      }),
      clear: vi.fn(async () => {
        for (const key of Object.keys(storage)) {
          delete storage[key];
        }
      }),
    };
  }

  const storage = localStorage;
  const storageMock = {
    local: createStorageArea(localStorage),
    session: createStorageArea(sessionStorage),
  };

  return { options, storage, localStorage, sessionStorage, sendMessage, storageMock, tabsCreate };
}

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

describe('popup UI', () => {
  let chromeMock: ReturnType<typeof createChromeMock>;

  beforeEach(async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    chromeMock = createChromeMock();
    (globalThis as Record<string, unknown>).chrome = {
      runtime: { sendMessage: chromeMock.sendMessage, getURL: vi.fn((path: string) => `chrome-extension://fake-id/${path}`) },
      storage: chromeMock.storageMock,
      tabs: { create: chromeMock.tabsCreate },
    };
    await import('../popup');
    await flushPromises();
  });

  afterEach(() => {
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
    Object.defineProperty(document, 'readyState', {
      value: 'complete',
      configurable: true,
      writable: true,
    });
  });

  function getButton(id: string): HTMLButtonElement {
    return document.getElementById(id) as HTMLButtonElement;
  }

  function getStatus(): HTMLDivElement {
    return document.getElementById('status') as HTMLDivElement;
  }

  it('fetches initial state and config on load', async () => {
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'GET_STATE' });
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'GET_LAST_RECORDING' });
    expect(chromeMock.storageMock.local.get).toHaveBeenCalledWith('serverConfig');
    expect(chromeMock.storageMock.session.get).toHaveBeenCalledWith('serverKeys');
  });

  it('renders buttons correctly based on initial state', () => {
    expect(getButton('start-recording').disabled).toBe(false);
    expect(getButton('stop-recording').disabled).toBe(true);
    expect(getButton('download-rule').disabled).toBe(true);
    expect(getButton('enhance-rule').disabled).toBe(true);
  });

  it('updates UI when starting and stopping a recording', async () => {
    getButton('start-recording').click();
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'START_RECORDING' });
    expect(getButton('start-recording').disabled).toBe(true);
    expect(getButton('stop-recording').disabled).toBe(false);
    expect(getStatus().textContent).toBe('录制已开始');

    getButton('stop-recording').click();
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'STOP_RECORDING' });
    expect(getButton('start-recording').disabled).toBe(false);
    expect(getButton('stop-recording').disabled).toBe(true);
    expect(getButton('download-rule').disabled).toBe(false);
    expect(getButton('enhance-rule').disabled).toBe(false);
    expect(getStatus().textContent).toContain('录制已停止');
    expect(chromeMock.tabsCreate).toHaveBeenCalledWith({
      url: 'chrome-extension://fake-id/intent/intent-page.html',
    });
  });

  it('keeps a local recording usable when server persistence must be retried', async () => {
    chromeMock.options.stopPersistenceWarning = 'storage unavailable';
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    expect(getStatus().textContent).toContain('录制已安全保存在本地');
    expect(getStatus().textContent).toContain('storage unavailable');
    expect(getStatus().className).toBe('warning');
    expect(getButton('download-rule').disabled).toBe(false);
    expect(chromeMock.tabsCreate).toHaveBeenCalledWith({
      url: 'chrome-extension://fake-id/intent/intent-page.html',
    });
  });

  it('downloads a rule when the download button is clicked', async () => {
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    getButton('download-rule').click();
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'DOWNLOAD_RULE' });
    expect(getStatus().textContent).toBe('规则已下载');
  });

  it('enhances a rule with user hint when enhance button is clicked', async () => {
    const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement;
    baseUrlInput.value = 'http://localhost:8080/';

    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    const userHintInput = document.getElementById('userHint') as HTMLInputElement;
    userHintInput.value = '抓取标题和价格';

    getButton('enhance-rule').click();
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({
      action: 'ENHANCE_RULE',
      payload: { userHint: '抓取标题和价格' },
    });
    expect(getStatus().textContent).toContain('增强完成，规则 ID：rule-enhanced-1');
  });

  it('saves server config with rememberSession flag when save button is clicked', async () => {
    const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement;
    const apiKeyInput = document.getElementById('apiKey') as HTMLInputElement;
    const adminApiKeyInput = document.getElementById('adminApiKey') as HTMLInputElement;
    baseUrlInput.value = 'http://localhost:8080';
    apiKeyInput.value = 'secret-key';
    adminApiKeyInput.value = 'admin-secret-key';

    getButton('save-config').click();
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({
      action: 'SET_SERVER_CONFIG',
      payload: {
        baseUrl: 'http://localhost:8080',
        apiKey: 'secret-key',
        adminApiKey: 'admin-secret-key',
        rememberSession: true,
      },
    });
    expect(getStatus().textContent).toBe('配置已保存');
  });

  it('normalizes trailing slashes in base URL when saving config', async () => {
    const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement;
    baseUrlInput.value = 'http://localhost:8080///';

    getButton('save-config').click();
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', apiKey: '', adminApiKey: '', rememberSession: true },
    });
    expect(getStatus().textContent).toBe('配置已保存');
  });

  it('prefills keys from session storage on load', async () => {
    chromeMock.sessionStorage.serverKeys = { apiKey: 'session-key', adminApiKey: 'session-admin' };
    chromeMock.localStorage.serverConfig = { baseUrl: 'http://example.com' };

    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    await import('../popup');
    await flushPromises();

    expect((document.getElementById('baseUrl') as HTMLInputElement).value).toBe('http://example.com');
    expect((document.getElementById('apiKey') as HTMLInputElement).value).toBe('session-key');
    expect((document.getElementById('adminApiKey') as HTMLInputElement).value).toBe('session-admin');
  });

  it('leaves key inputs empty when session storage has no keys', async () => {
    chromeMock.localStorage.serverConfig = { baseUrl: 'http://example.com' };

    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    await import('../popup');
    await flushPromises();

    expect((document.getElementById('baseUrl') as HTMLInputElement).value).toBe('http://example.com');
    expect((document.getElementById('apiKey') as HTMLInputElement).value).toBe('');
    expect((document.getElementById('adminApiKey') as HTMLInputElement).value).toBe('');
  });

  it('displays error messages in the status area', async () => {
    chromeMock.options.startError = '没有活动标签页';

    getButton('start-recording').click();
    await flushPromises();

    expect(getStatus().textContent).toBe('没有活动标签页');
    expect(getStatus().classList.contains('error')).toBe(true);
    expect(getButton('start-recording').disabled).toBe(false);
  });

  it('displays error when saving config fails', async () => {
    chromeMock.options.saveConfigError = '配置保存失败';

    getButton('save-config').click();
    await flushPromises();

    expect(getStatus().textContent).toBe('配置保存失败');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when enhance rule fails', async () => {
    chromeMock.options.enhanceError = '增强失败';

    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    const userHintInput = document.getElementById('userHint') as HTMLInputElement;
    userHintInput.value = '抓取标题';

    getButton('enhance-rule').click();
    await flushPromises();

    expect(getStatus().textContent).toBe('增强失败');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when stop recording fails', async () => {
    chromeMock.options.stopError = '停止失败';

    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    expect(getStatus().textContent).toBe('停止失败');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when download rule fails', async () => {
    chromeMock.options.downloadError = '下载失败';

    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    getButton('download-rule').click();
    await flushPromises();

    expect(getStatus().textContent).toBe('下载失败');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('handles undefined service worker response', async () => {
    chromeMock.options.startReturnsUndefined = true;

    getButton('start-recording').click();
    await flushPromises();

    expect(getStatus().textContent).toBe('未收到 service worker 响应');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('dispatches initPopup when document is already ready', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    Object.defineProperty(document, 'readyState', { value: 'complete', configurable: true, writable: true });
    chromeMock = createChromeMock();
    (globalThis as Record<string, unknown>).chrome = {
      runtime: { sendMessage: chromeMock.sendMessage, getURL: vi.fn((path: string) => `chrome-extension://fake-id/${path}`) },
      storage: chromeMock.storageMock,
      tabs: { create: chromeMock.tabsCreate },
    };
    await import('../popup');
    await flushPromises();

    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'GET_STATE' });
  });

  it('initializes via DOMContentLoaded when document is still loading', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    Object.defineProperty(document, 'readyState', { value: 'loading', configurable: true, writable: true });
    await import('../popup');
    await flushPromises();
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'GET_STATE' });
  });

  it('tolerates missing status element', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML.replace(/<div id="status">[^]*?<\/div>/, '');
    await import('../popup');
    await flushPromises();
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'GET_STATE' });
  });

  it('tolerates missing action buttons', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML
      .replace(/<button id="start-recording".*?<\/button>/, '')
      .replace(/<button id="stop-recording".*?<\/button>/, '')
      .replace(/<button id="download-rule".*?<\/button>/, '')
      .replace(/<button id="enhance-rule".*?<\/button>/, '');
    await import('../popup');
    await flushPromises();
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({ action: 'GET_STATE' });
  });

  it('tolerates missing config inputs', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML
      .replace(/id="baseUrl"/, '')
      .replace(/id="apiKey"/, '')
      .replace(/id="adminApiKey"/, '');
    await import('../popup');
    await flushPromises();
    expect(chromeMock.storageMock.local.get).toHaveBeenCalledWith('serverConfig');
  });

  it('shows success status when recording starts', async () => {
    getButton('start-recording').click();
    await flushPromises();
    expect(getStatus().textContent).toBe('录制已开始');
    expect(getStatus().classList.contains('success')).toBe(true);
  });

  it('displays error when initial state load fails', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    chromeMock.options.loadStateReject = true;
    await import('../popup');
    await flushPromises();
    expect(getStatus().textContent).toContain('state failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when initial config load fails', async () => {
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    chromeMock.storageMock.local.get.mockRejectedValueOnce(new Error('storage failed'));
    await import('../popup');
    await flushPromises();
    expect(getStatus().textContent).toContain('storage failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when start recording throws', async () => {
    chromeMock.options.startReject = true;
    getButton('start-recording').click();
    await flushPromises();
    expect(getStatus().textContent).toContain('start failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when stop recording throws', async () => {
    chromeMock.options.stopReject = true;
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();
    expect(getStatus().textContent).toContain('stop failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when download rule throws', async () => {
    chromeMock.options.downloadReject = true;
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();
    getButton('download-rule').click();
    await flushPromises();
    expect(getStatus().textContent).toContain('download failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when enhance rule throws', async () => {
    chromeMock.options.enhanceReject = true;
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();
    getButton('enhance-rule').click();
    await flushPromises();
    expect(getStatus().textContent).toContain('enhance failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('displays error when save config throws', async () => {
    chromeMock.options.saveConfigReject = true;
    getButton('save-config').click();
    await flushPromises();
    expect(getStatus().textContent).toContain('save failed');
    expect(getStatus().classList.contains('error')).toBe(true);
  });

  it('enhances without a user hint and shows warning when ruleId is missing', async () => {
    chromeMock.options.enhanceNoRuleId = true;
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();
    document.getElementById('userHint')?.remove();
    getButton('enhance-rule').click();
    await flushPromises();
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({
      action: 'ENHANCE_RULE',
      payload: { userHint: '' },
    });
    expect(getStatus().textContent).toBe('增强已完成，但服务端未返回规则 ID');
    expect(getStatus().className).toBe('warning');
    // The stop-recording flow opens the intent page; ensure no admin URL was
    // opened in response to the enhance click.
    const openedUrls = chromeMock.tabsCreate.mock.calls.map((c) => (c[0] as { url: string }).url);
    expect(openedUrls.some((u) => u.includes('/admin/rules/'))).toBe(false);
  });

  it('disables enhance-rule button when no baseUrl is configured', async () => {
    // Overwrite the default storage with an empty config and re-init.
    chromeMock.localStorage.serverConfig = { baseUrl: '' };
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    await import('../popup');
    await flushPromises();

    // Start + stop to set hasRecording=true; enhance should still be disabled
    // because hasBaseUrl=false.
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();
    expect(getButton('download-rule').disabled).toBe(false);
    expect(getButton('enhance-rule').disabled).toBe(true);
  });

  it('enables enhance-rule after saving a baseUrl', async () => {
    chromeMock.localStorage.serverConfig = { baseUrl: '' };
    vi.resetModules();
    document.body.innerHTML = POPUP_HTML;
    await import('../popup');
    await flushPromises();

    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();
    expect(getButton('enhance-rule').disabled).toBe(true);

    const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement;
    baseUrlInput.value = 'http://localhost:8080';
    getButton('save-config').click();
    await flushPromises();
    expect(getButton('enhance-rule').disabled).toBe(false);
  });

  it('encodes the ruleId when opening the admin URL after enhance', async () => {
    chromeMock.options.enhanceRuleId = 'weird/id?x=1#frag';
    const baseUrlInput = document.getElementById('baseUrl') as HTMLInputElement;
    baseUrlInput.value = 'http://localhost:8080/';
    getButton('start-recording').click();
    await flushPromises();
    getButton('stop-recording').click();
    await flushPromises();

    getButton('enhance-rule').click();
    await flushPromises();

    const adminCalls = chromeMock.tabsCreate.mock.calls
      .map((c) => (c[0] as { url: string }).url)
      .filter((u) => u.includes('/admin/rules/'));
    expect(adminCalls).toHaveLength(1);
    expect(adminCalls[0]).toBe('http://localhost:8080/admin/rules/weird%2Fid%3Fx%3D1%23frag');
  });

  it('remembers missing remember-session checkbox defaults to true', async () => {
    document.getElementById('remember-session')?.remove();
    getButton('save-config').click();
    await flushPromises();
    expect(chromeMock.sendMessage).toHaveBeenCalledWith({
      action: 'SET_SERVER_CONFIG',
      payload: expect.objectContaining({ rememberSession: true }),
    });
  });
});

describe('popup.html drift guard', () => {
  // Reads the real popup.html and asserts every ID the popup logic depends on
  // is present. Catches drift between the fixture above (POPUP_HTML) and the
  // shipped HTML so the two can no longer silently diverge.
  it('contains every element ID referenced by popup.ts', () => {
    const popupHtmlPath = resolve(__dirname, '..', 'popup.html');
    const html = readFileSync(popupHtmlPath, 'utf8');
    const missing = POPUP_ELEMENT_IDS.filter((id) => !new RegExp(`id=["']${id}["']`).test(html));
    expect(missing).toEqual([]);
  });

  it('POPUP_HTML fixture matches popup.html for the IDs the tests rely on', () => {
    const popupHtmlPath = resolve(__dirname, '..', 'popup.html');
    const html = readFileSync(popupHtmlPath, 'utf8');
    // Extract every id="..." from both documents and compare sets.
    const extractIds = (s: string): Set<string> => {
      const ids = new Set<string>();
      const re = /id=["']([^"']+)["']/g;
      let m: RegExpExecArray | null;
      while ((m = re.exec(s)) !== null) ids.add(m[1]);
      return ids;
    };
    const realIds = extractIds(html);
    const fixtureIds = extractIds(POPUP_HTML);
    // Fixture must not advertise IDs the real file has dropped (forward drift,
    // e.g. preview-section/ruleYaml removed but fixture still references them).
    const staleInFixture = [...fixtureIds].filter((id) => !realIds.has(id));
    expect(staleInFixture).toEqual([]);
  });
});

