import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { ContentRecorder as ContentRecorderType } from '../content';

function createChromeMock() {
  const runtimeListeners: Array<
    (message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean | void
  > = [];
  const localStorage: Record<string, unknown> = {};
  const sessionStorage: Record<string, unknown> = {};

  function createStorageArea(storage: Record<string, unknown>) {
    return {
      get: vi.fn(
        async (keys?: string | string[] | Record<string, unknown> | null): Promise<Record<string, unknown>> => {
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
        },
      ),
      set: vi.fn(async (items: Record<string, unknown>): Promise<void> => {
        Object.assign(storage, items);
      }),
      remove: vi.fn(async () => undefined),
      clear: vi.fn(async () => undefined),
    };
  }

  return {
    runtimeListeners,
    storage: localStorage,
    localStorage,
    sessionStorage,
    mock: {
      runtime: {
        onMessage: {
          addListener: vi.fn((fn) => runtimeListeners.push(fn)),
          removeListener: vi.fn(),
        },
        onConnect: {
          addListener: vi.fn(),
        },
        onStartup: {
          addListener: vi.fn(),
        },
        sendMessage: vi.fn(),
        connect: vi.fn(() => ({
          name: 'intent-wizard',
          postMessage: vi.fn(),
          disconnect: vi.fn(),
          onMessage: { addListener: vi.fn(), removeListener: vi.fn() },
          onDisconnect: { addListener: vi.fn(), removeListener: vi.fn() },
        })),
      },
      storage: {
        local: createStorageArea(localStorage),
        session: createStorageArea(sessionStorage),
      },
      tabs: {
        query: vi.fn(async () => [{ id: 42, url: 'http://localhost:3000/' }]),
        sendMessage: vi.fn(),
        onUpdated: { addListener: vi.fn(), removeListener: vi.fn() },
        onRemoved: { addListener: vi.fn(), removeListener: vi.fn() },
      },
      scripting: {
        executeScript: vi.fn(async () => []),
      },
      webNavigation: {
        getAllFrames: vi.fn(async () => [
          { frameId: 0, parentFrameId: -1, url: 'http://localhost:3000/' },
          { frameId: 10, parentFrameId: 0, url: 'http://other.example.com/' },
        ]),
      },
      downloads: {
        download: vi.fn(async () => 7),
      },
      alarms: {
        create: vi.fn(),
        clear: vi.fn(),
        onAlarm: {
          addListener: vi.fn(),
        },
      },
    },
  };
}

describe('extension integration flow', () => {
  let chromeMock: ReturnType<typeof createChromeMock>;
  let handleMessage: (message: { action: string; payload?: unknown }, sender: ChromeRuntimeMessageSender) => Promise<unknown>;
  let recorder: ContentRecorderType;

  function sendBackgroundMessage(message: { action: string; payload?: unknown }): Promise<unknown> {
    return handleMessage(message, { tab: { id: 42 } });
  }

  beforeEach(async () => {
    vi.useFakeTimers();
    document.body.innerHTML = '';
    global.fetch = vi.fn();

    chromeMock = createChromeMock();
    (globalThis as Record<string, unknown>).chrome = chromeMock.mock;

    vi.resetModules();

    // Load the content script module first so its singleton recorder attaches
    // to our mocked chrome.runtime.onMessage.
    await import('../content');
    recorder = (window as unknown as Record<string, unknown>).__openCrawlerRecorder as ContentRecorderType;
    expect(recorder).toBeDefined();

    // Load the background module to obtain its message handler.
    const backgroundModule = await import('../background');
    handleMessage = backgroundModule.handleMessage;

    // Drain microtasks so the background's startup loadServerConfig finishes.
    await Promise.resolve();

    // Route background -> content-script messages to the content recorder listener.
    const contentListener = chromeMock.runtimeListeners[0];
    expect(contentListener).toBeDefined();
    chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown, options?: { frameId?: number }) => {
      if ((message as { action?: string }).action === 'CAPTURE_DOM' && options?.frameId === 10) {
        return { domTree: { type: 'element', tagName: 'html', children: [{ type: 'element', tagName: 'body', children: [{ type: 'element', tagName: 'button', attributes: [{ name: 'id', value: 'inside-frame' }] }] }] } };
      }
      return new Promise((resolve) => {
        const handled = contentListener(message, { tabId: 42, frameId: 0 }, (response?: unknown) => resolve(response));
        expect(handled).toBe(true);
      });
    });

    // Route content-script -> background runtime messages back to the background handler.
    chromeMock.mock.runtime.sendMessage.mockImplementation(async (message: unknown) => {
      return handleMessage(message as { action: string; payload?: unknown }, { tab: { id: 42 } });
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    delete (globalThis as Record<string, unknown>).chrome;
    delete (globalThis as Record<string, unknown>).fetch;
    delete (window as unknown as Record<string, unknown>).__openCrawlerRecorder;
    vi.restoreAllMocks();
  });

  it('records a DOM click, stops, and generates a rule YAML', async () => {
    document.body.innerHTML = '<button id="submit" data-testid="submit-btn">Submit</button>';

    // Popup -> background: START_RECORDING
    const startResponse = (await sendBackgroundMessage({ action: 'START_RECORDING' })) as { success: boolean };
    expect(startResponse.success).toBe(true);
    expect(recorder.isRecording()).toBe(true);
    expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalledWith({
      target: { tabId: 42 },
      files: ['content.js'],
    });

    // User interacts with the page.
    const btn = document.getElementById('submit')!;
    btn.click();

    // Popup -> background: STOP_RECORDING
    const stopResponse = (await sendBackgroundMessage({ action: 'STOP_RECORDING' })) as {
      success: boolean;
      recording: { events: unknown[]; meta: { domain: string } };
    };
    expect(stopResponse.success).toBe(true);
    expect(recorder.isRecording()).toBe(false);
    expect(stopResponse.recording.events).toHaveLength(1);
    expect(stopResponse.recording.events[0]).toMatchObject({ type: 'click' });
    const storedAfterStop = (await sendBackgroundMessage({ action: 'GET_LAST_RECORDING' })) as { recording: unknown };
    expect(storedAfterStop.recording).toEqual(stopResponse.recording);

    // Popup -> background: GENERATE_RULE
    const genResponse = (await sendBackgroundMessage({ action: 'GENERATE_RULE' })) as {
      success: boolean;
      yaml: string;
    };
    expect(genResponse.success).toBe(true);
    expect(genResponse.yaml).toContain('id:');
    expect(genResponse.yaml).toContain('name:');
    expect(genResponse.yaml).toContain('domain: localhost');
    expect(genResponse.yaml).toContain('entry: http://localhost:3000/');
    expect(genResponse.yaml).toContain('action: click');
    expect(chromeMock.sessionStorage.lastRuleYaml).toBe(genResponse.yaml);
  });

  it('records, predicts intent, confirms, and uploads rule as pending', async () => {
    document.body.innerHTML = '<button id="search" data-testid="search-btn">Search</button>';

    // Start and stop recording to populate lastRecording.
    const startResponse = (await sendBackgroundMessage({ action: 'START_RECORDING' })) as { success: boolean };
    expect(startResponse.success).toBe(true);

    const btn = document.getElementById('search')!;
    btn.click();

    const stopResponse = (await sendBackgroundMessage({ action: 'STOP_RECORDING' })) as {
      success: boolean;
      recording: { meta: { domain: string } };
    };
    expect(stopResponse.success).toBe(true);
    const storedAfterStop = (await sendBackgroundMessage({ action: 'GET_LAST_RECORDING' })) as { recording: unknown };
    expect(storedAfterStop.recording).toEqual(stopResponse.recording);

    // Configure the server and mock a successful predict-intent response.
    await sendBackgroundMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', apiKey: 'secret', adminApiKey: 'admin-secret' },
    });

    const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
    fetchMock.mockResolvedValueOnce({
      ok: true,
      status: 200,
      statusText: 'OK',
      json: async () => ({
        candidates: [
          { id: 'c1', label: '采集商品列表', description: '抓取列表页数据', confidence: 0.9 },
          { id: 'c2', label: '搜索关键词', description: '搜索并采集结果', confidence: 0.1 },
        ],
        fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
        model: 'fake',
        cacheHit: false,
      }),
    });

    const predictResponse = (await sendBackgroundMessage({ action: 'PREDICT_INTENT' })) as {
      success: boolean;
      candidates: { id: string; label: string }[];
      model: string;
    };
    expect(predictResponse.success).toBe(true);
    expect(predictResponse.candidates).toHaveLength(2);
    expect(predictResponse.candidates[0].id).toBe('c1');
    expect(predictResponse.model).toBe('fake');

    const [predictCall] = fetchMock.mock.calls;
    expect(predictCall[0]).toBe('http://localhost:8080/admin/rules/predict-intent');
    expect(predictCall[1].method).toBe('POST');
    expect(predictCall[1].headers['Authorization']).toBe('Bearer admin-secret');
    expect(predictCall[1].headers['X-Trace-Id']).toBeDefined();

    // Generate the DSL from the selected intent.
    const intent = predictResponse.candidates[0];
    const generateResponse = (await sendBackgroundMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } })) as {
      success: boolean;
      rule: { id: string; steps: unknown[] };
      yaml: string;
    };
    expect(generateResponse.success).toBe(true);
    expect(generateResponse.rule.id).toMatch(/^ext-\d+$/);
    expect(generateResponse.yaml).toContain('id:');
    expect(chromeMock.sessionStorage.lastRule).toBeDefined();
    expect(chromeMock.sessionStorage.lastRuleYaml).toBe(generateResponse.yaml);

    // Mock the rule save endpoint and upload the confirmed rule.
    fetchMock.mockResolvedValueOnce({ ok: true, status: 200, statusText: 'OK' });

    const uploadResponse = (await sendBackgroundMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as {
      success: boolean;
      ruleId: string;
    };
    expect(uploadResponse.success).toBe(true);
    expect(uploadResponse.ruleId).toBe(generateResponse.rule.id);

    const ruleCall = fetchMock.mock.calls.find((call) => call[0] === 'http://localhost:8080/admin/rules');
    expect(ruleCall).toBeDefined();
    expect(ruleCall![1].method).toBe('POST');
    expect(ruleCall![1].headers['Authorization']).toBe('Bearer admin-secret');
    const uploadedRule = JSON.parse(ruleCall![1].body as string) as { id: string; approvalStatus: string };
    expect(uploadedRule.id).toBe(generateResponse.rule.id);
    expect(uploadedRule.approvalStatus).toBe('pending');
  });

  it('embeds cross-origin iframe DOM into the top-frame snapshot', async () => {
    document.body.innerHTML = '<iframe id="cross" src="http://other.example.com/"></iframe>';

    const startResponse = (await handleMessage({ action: 'START_RECORDING' }, { tab: { id: 42 } })) as { success: boolean };
    expect(startResponse.success).toBe(true);

    const stopResponse = (await handleMessage({ action: 'STOP_RECORDING' }, { tab: { id: 42 } })) as {
      success: boolean;
      recording: { snapshots: { domTree?: { tagName?: string; children?: unknown[] } }[] };
    };
    expect(stopResponse.success).toBe(true);
    expect(stopResponse.recording.snapshots).toHaveLength(1);
    const tree = stopResponse.recording.snapshots[0].domTree;
    expect(tree).toBeDefined();
    const body = tree!.children?.find((c: any) => c.tagName === 'body') as any;
    const iframeNode = body?.children?.find((c: any) => c.tagName === 'iframe') as any;
    expect(iframeNode).toBeDefined();
    expect(iframeNode.children).toHaveLength(1);
    const innerBody = iframeNode.children[0].children?.find((c: any) => c.tagName === 'body') as any;
    expect(innerBody).toBeDefined();
    expect(innerBody.children?.some((c: any) => c.tagName === 'button' && c.attributes?.some((a: any) => a.name === 'id' && a.value === 'inside-frame'))).toBe(true);
  });
});
