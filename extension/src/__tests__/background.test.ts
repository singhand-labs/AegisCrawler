import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { PageAgentRecording, PageMark, Rule } from '../../../src/rule-generator';

function createChromeMock() {
  const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean | void> = [];
  const localStorage: Record<string, unknown> = {};
  const sessionStorage: Record<string, unknown> = {};

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

  const onUpdatedListeners: Array<(tabId: number, changeInfo: { status?: string }) => void> = [];
  const onRemovedListeners: Array<(tabId: number) => void> = [];
  const onAlarmListeners: Array<(alarm: { name: string }) => void | Promise<void>> = [];
  const onStartupListeners: Array<() => void | Promise<void>> = [];

  return {
    listeners,
    localStorage,
    sessionStorage,
    onUpdatedListeners,
    onRemovedListeners,
    onAlarmListeners,
    onStartupListeners,
    mock: {
      runtime: {
        onMessage: {
          addListener: vi.fn((fn) => listeners.push(fn)),
          removeListener: vi.fn(),
        },
        onConnect: {
          addListener: vi.fn(),
        },
        onStartup: {
          addListener: vi.fn((fn) => onStartupListeners.push(fn)),
        },
        sendMessage: vi.fn(async () => undefined),
        getURL: vi.fn((path: string) => `chrome-extension://fake-id/${path}`),
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
        query: vi.fn(async () => [{ id: 42, url: 'https://example.com/' }]),
        get: vi.fn(async (tabId: number): Promise<ChromeTab> => ({ id: tabId, url: 'https://example.com/', status: 'loading' })),
        create: vi.fn(async ({ url }: { url?: string }): Promise<ChromeTab> => {
          // Simulate the tab finishing load so waitForTabLoad resolves.
          setTimeout(() => {
            onUpdatedListeners.forEach((listener) => listener(123, { status: 'complete' }));
          }, 0);
          return { id: 123, url, status: 'loading' };
        }),
        remove: vi.fn(async () => undefined),
        onUpdated: {
          addListener: vi.fn((fn) => onUpdatedListeners.push(fn)),
          removeListener: vi.fn((fn) => {
            const index = onUpdatedListeners.indexOf(fn);
            if (index >= 0) onUpdatedListeners.splice(index, 1);
          }),
        },
        onRemoved: {
          addListener: vi.fn((fn) => onRemovedListeners.push(fn)),
          removeListener: vi.fn((fn) => {
            const index = onRemovedListeners.indexOf(fn);
            if (index >= 0) onRemovedListeners.splice(index, 1);
          }),
        },
        sendMessage: vi.fn(async (_tabId: number, message: unknown, _options?: unknown): Promise<unknown> => {
          const msg = message as { action?: string };
          if (msg.action === 'STOP_RECORDING') {
            return {
              recording: {
                version: '1.0.0',
                meta: {
                  startUrl: 'https://example.com/',
                  title: 'Example',
                  recordedAt: '2026-07-06T00:00:00Z',
                  domain: 'example.com',
                },
                events: [],
                snapshots: [],
              },
            };
          }
          if (msg.action === 'CAPTURE_DOM') {
            return { domTree: { type: 'element', tagName: 'html' } };
          }
          return { success: true };
        }),
      },
      webNavigation: {
        getAllFrames: vi.fn(async () => [
          { frameId: 0, parentFrameId: 0, url: 'https://example.com/' },
          { frameId: 1, parentFrameId: 0, url: 'https://example.com/iframe' },
        ]),
      },
      scripting: {
        executeScript: vi.fn(async () => []),
      },
      action: {
        setBadgeText: vi.fn(async () => undefined),
        setBadgeBackgroundColor: vi.fn(async () => undefined),
      },
      downloads: {
        download: vi.fn(async () => 7),
      },
      alarms: {
        create: vi.fn(async () => undefined),
        clear: vi.fn(async () => undefined),
        onAlarm: {
          addListener: vi.fn((fn) => onAlarmListeners.push(fn)),
        },
      },
    },
  };
}

describe('background service worker', () => {
  let chromeMock: ReturnType<typeof createChromeMock>;

  beforeEach(async () => {
    chromeMock = createChromeMock();
    (globalThis as Record<string, unknown>).chrome = chromeMock.mock;
    global.fetch = vi.fn();
    vi.resetModules();
    await import('../background');
    // Wait for async startup loadServerConfig.
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  afterEach(async () => {
    await sendMessage({ action: 'ABORT_REPLAY' }).catch(() => undefined);
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function sendMessage(message: unknown, sender: ChromeRuntimeMessageSender = {}): Promise<unknown> {
    return new Promise((resolve) => {
      const sendResponse = vi.fn((response) => resolve(response));
      expect(chromeMock.listeners.length).toBeGreaterThanOrEqual(1);
      const handled = chromeMock.listeners[0](message, sender, sendResponse);
      expect(handled).toBe(true);
    });
  }

  function sendRuntimeMessage(
    message: unknown,
    sender: ChromeRuntimeMessageSender = {},
  ): Promise<unknown> {
    return new Promise((resolve) => {
      const sendResponse = vi.fn((response) => resolve(response));
      expect(chromeMock.listeners).toHaveLength(1);
      const handled = chromeMock.listeners[0](message, sender, sendResponse);
      expect(handled).toBe(true);
    });
  }

  function sendRuntimeMessageRaw(
    message: unknown,
    sender: ChromeRuntimeMessageSender = {},
  ): { handled: boolean; response: Promise<unknown> } {
    const sendResponse = vi.fn();
    expect(chromeMock.listeners).toHaveLength(1);
    const handled = chromeMock.listeners[0](message, sender, sendResponse) as boolean;
    return {
      handled,
      response: new Promise((resolve) => {
        const check = () => {
          if (sendResponse.mock.calls.length > 0) {
            resolve(sendResponse.mock.calls[0][0]);
          } else {
            setTimeout(check, 0);
          }
        };
        check();
      }),
    };
  }

  function makeRule(entryUrl?: string, steps: Record<string, unknown>[] = []): Rule {
    return {
      id: 'rule-1',
      version: '1.0.0',
      name: 'Replay Rule',
      domain: 'example.com',
      entry: entryUrl ? { url: entryUrl } : undefined,
      steps,
    } as unknown as Rule;
  }

  function makeV2Recording(complete = true): PageAgentRecording {
    const recording: PageAgentRecording = {
      version: '2.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Example',
        recordedAt: '2026-07-16T00:00:00.000Z',
        domain: 'example.com',
        semanticDomVersion: '1',
        sanitizationVersion: 'extension-v1',
      },
      limits: {
        maxActions: 500,
        maxDurationMs: 7_200_000,
        maxBytes: 26_214_400,
        warningThreshold: 0.8,
      },
      warnings: [],
      events: [],
      snapshots: [
        {
          timestamp: 1,
          url: 'https://example.com/',
          selectorMap: {},
          domTree: { type: 'element', tagName: 'html' },
          phase: 'initial',
          sequence: 0,
          capture: { status: 'complete', nodeCount: 1, redactionCount: 0, removedNodeCount: 0, frames: [] },
        },
      ],
    };
    if (complete) {
      recording.meta.endedAt = '2026-07-16T00:01:00.000Z';
      recording.snapshots.push({
        timestamp: 2,
        url: 'https://example.com/',
        selectorMap: {},
        domTree: { type: 'element', tagName: 'html' },
        phase: 'final',
        sequence: 1,
        capture: { status: 'complete', nodeCount: 1, redactionCount: 0, removedNodeCount: 0, frames: [] },
      });
      recording.termination = { reason: 'user', message: 'done', timestamp: 2, complete: true };
    }
    return recording;
  }

  function pageMark(overrides: Partial<PageMark> = {}): PageMark {
    return {
      id: 'mark-1',
      timestamp: 3,
      url: 'https://example.com/',
      role: 'field',
      note: '商品标题',
      element: {
        index: 1,
        tagName: 'span',
        selector: '.product-title',
        stableSelector: '.product-title',
        text: 'Example product',
        boundingRect: { x: 1, y: 2, width: 100, height: 20 },
      },
      actionIndex: 1,
      snapshotSequence: 1,
      state: 'https://example.com/',
      ...overrides,
    };
  }

  async function configureRecordingV2(fetchMock: ReturnType<typeof vi.fn>): Promise<void> {
    await sendMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
    });
    fetchMock.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({
        features: { recordingV2: true },
        recordingLimits: {
          maxActions: 500,
          maxDurationMs: 7_200_000,
          maxCompressedBytes: 26_214_400,
        },
      }),
    });
  }

  async function flushAsync(): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, 0));
  }

  describe('state management', () => {
    it('returns idle state for GET_STATE', async () => {
      const response = await sendMessage({ action: 'GET_STATE' });
      expect(response).toEqual({ state: 'idle' });
    });

    it('badges the toolbar while a recording is active and clears it on stop', async () => {
      const badge = chromeMock.mock.action;
      if (!badge) throw new Error('action mock missing');
      await sendMessage({ action: 'START_RECORDING' });
      expect(badge.setBadgeText).toHaveBeenCalledWith({ text: 'REC' });
      expect(badge.setBadgeBackgroundColor).toHaveBeenCalledWith({ color: '#dc2626' });
      await sendMessage({ action: 'STOP_RECORDING' });
      expect(badge.setBadgeText).toHaveBeenLastCalledWith({ text: '' });
    });

    it('runs a keepalive alarm while recording and clears it on stop', async () => {
      const alarms = chromeMock.mock.alarms;
      await sendMessage({ action: 'START_RECORDING' });
      expect(alarms.create).toHaveBeenCalledWith('recording-state-keepalive', { periodInMinutes: 1 });
      await sendMessage({ action: 'STOP_RECORDING' });
      expect(alarms.clear).toHaveBeenCalledWith('recording-state-keepalive');
    });

    const completeV2Recording = () => ({
      version: '2.0.0',
      meta: { startUrl: 'https://example.com/', title: 'Example', recordedAt: '2026-07-06T00:00:00Z', domain: 'example.com' },
      events: [],
      snapshots: [],
      termination: { reason: 'size-limit', complete: true, timestamp: Date.now() },
    });

    it('opens the intent wizard when a recording auto-stops at a limit', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      const tabsCreate = chromeMock.mock.tabs.create;
      tabsCreate.mockClear();
      const sender = { tab: { id: 42 }, frameId: 0 };
      await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', message: 'size', recording: completeV2Recording() } },
        sender,
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(tabsCreate).toHaveBeenCalledWith({
        url: 'chrome-extension://fake-id/intent/intent-page.html',
      });
      const state = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(state.state).toBe('idle');
    });

    it('does not stack a second wizard tab when the tracked one is alive', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      // Simulate a previously created wizard tab whose id is tracked in
      // session storage and whose tabs.get probe still succeeds.
      chromeMock.sessionStorage['oc_intent_wizard_tab'] = 321;
      chromeMock.mock.tabs.get.mockImplementation(async (tabId: number) => ({ id: tabId, status: 'complete' }));
      const tabsCreate = chromeMock.mock.tabs.create;
      tabsCreate.mockClear();
      await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', message: 'size', recording: completeV2Recording() } },
        { tab: { id: 42 }, frameId: 0 },
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(chromeMock.mock.tabs.get).toHaveBeenCalledWith(321);
      expect(tabsCreate).not.toHaveBeenCalled();
    });

    it('recreates the wizard tab after the tracked one is closed', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      chromeMock.sessionStorage['oc_intent_wizard_tab'] = 322;
      chromeMock.mock.tabs.get.mockRejectedValueOnce(new Error('No tab with id 322'));
      const tabsCreate = chromeMock.mock.tabs.create;
      tabsCreate.mockClear();
      await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', message: 'size', recording: completeV2Recording() } },
        { tab: { id: 42 }, frameId: 0 },
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(tabsCreate).toHaveBeenCalledWith({ url: 'chrome-extension://fake-id/intent/intent-page.html' });
      expect(chromeMock.sessionStorage['oc_intent_wizard_tab']).toBe(123);
    });

    it('STOP after an auto-stop idempotently returns the stored complete recording', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      const recording = completeV2Recording();
      await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', message: 'size', recording } },
        { tab: { id: 42 }, frameId: 0 },
      );
      chromeMock.mock.tabs.sendMessage.mockClear();
      const response = (await sendMessage({ action: 'STOP_RECORDING' })) as {
        success?: boolean;
        recording?: { termination?: { reason?: string } };
        persistenceWarning?: string;
      };
      expect(response.success).toBe(true);
      expect(response.recording?.termination?.reason).toBe('size-limit');
      // Must not drain the already-dead recording tab again (30s timeout path).
      expect(chromeMock.mock.tabs.sendMessage).not.toHaveBeenCalled();
      expect(typeof response.persistenceWarning).toBe('string');
    });

    it('self-heals the REC badge when the keepalive alarm fires', async () => {
      const badge = chromeMock.mock.action;
      if (!badge) throw new Error('action mock missing');
      await sendMessage({ action: 'START_RECORDING' });
      badge.setBadgeText.mockClear();
      if (chromeMock.onAlarmListeners.length === 0) throw new Error('alarm listener not registered');
      // Chrome delivers one alarm to every registered listener.
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: 'recording-state-keepalive' });
      }
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(badge.setBadgeText).toHaveBeenLastCalledWith({ text: 'REC' });
    });

    it('restores an active recording session from session storage', async () => {
      chromeMock.sessionStorage.oc_recording_session = {
        tabId: 42,
        startedAt: Date.now(),
        options: { protocolVersion: '2.0.0' },
        statusMessage: 'restored after restart',
      };

      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toMatchObject({
        state: 'recording',
        statusMessage: 'restored after restart',
      });
    });

    it('rejects privileged actions from web-page content scripts', async () => {
      const { handled, response } = sendRuntimeMessageRaw(
        { action: 'GET_STATE' },
        { tab: { id: 7, url: 'https://untrusted.example/' }, url: 'https://untrusted.example/' },
      );

      expect(handled).toBe(false);
      await expect(response).resolves.toEqual({ success: false, error: '拒绝来自网页的特权请求' });
    });

    it('allows privileged actions from extension pages opened in a tab', async () => {
      const extensionUrl = 'chrome-extension://fake-id/intent/intent-page.html';
      const response = await sendMessage(
        { action: 'GET_STATE' },
        { tab: { id: 7, url: extensionUrl }, url: extensionUrl, origin: 'chrome-extension://fake-id' },
      );

      expect(response).toEqual({ state: 'idle' });
    });

    it('transitions to recording on START_RECORDING', async () => {
      const response = (await sendMessage({ action: 'START_RECORDING' })) as { success: boolean };
      expect(response.success).toBe(true);
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalledWith({
        target: { tabId: 42 },
        files: ['content.js'],
      });
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenCalledWith(
        42,
        { action: 'START_RECORDING', payload: undefined },
        { frameId: 0 },
      );
      const state = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(state.state).toBe('recording');
    });

    it('serializes concurrent state-mutating messages so the second sees the first (M-5)', async () => {
      // M-5 regression: without serialization, two concurrent
      // START_RECORDING messages each saw loadActiveRecording=null, each
      // proceeded to inject a content script and save a session, with the
      // second silently overwriting the first. With serialization the
      // second message observes the first's session and is rejected by the
      // "已在录制" guard.
      const p1 = sendMessage({ action: 'START_RECORDING' });
      const p2 = sendMessage({ action: 'START_RECORDING' });
      const [r1, r2] = await Promise.all([p1, p2]) as Array<{ success: boolean; error?: string }>;
      expect(r1.success).toBe(true);
      expect(r2.success).toBe(false);
      // The first session is the one that survives (not overwritten).
      const state = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(state.state).toBe('recording');
    });

    it('negotiates semantic recording v2 limits with the server', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);

      const response = (await sendMessage({ action: 'START_RECORDING' })) as {
        success: boolean;
        protocolVersion: string;
      };

      expect(response).toEqual({ success: true, protocolVersion: '2.0.0' });
      expect(fetchMock).toHaveBeenCalledWith(
        'http://localhost:8080/api/v1/capabilities',
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenCalledWith(42, {
        action: 'START_RECORDING',
        payload: {
          protocolVersion: '2.0.0',
          maxEvents: 500,
          maxDurationMs: 7_200_000,
          maxRecordingBytes: 26_214_400,
          warningThreshold: 0.8,
          captureSnapshotBeforeEachEvent: true,
        },
      }, { frameId: 0 });
    });

    it('reattaches and awaits the active v2 recorder before the next action', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'START_RECORDING') {
          return { success: true, protocolVersion: '2.0.0' };
        }
        if (action === 'ENSURE_RECORDING_READY') {
          return { success: true, active: true, protocolVersion: '2.0.0' };
        }
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING' });
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toEqual({
        success: true,
        active: true,
        protocolVersion: '2.0.0',
      });

      expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalledTimes(2);
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenLastCalledWith({
        target: { tabId: 42 },
        files: ['content.js'],
      });
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenLastCalledWith(
        42,
        { action: 'ENSURE_RECORDING_READY' },
        { frameId: 0 },
      );
    });

    it('atomically transfers an active v2 recording to the foreground popup', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording: PageAgentRecording = {
        version: '2.0.0',
        meta: { startUrl: 'https://example.com/', title: 'Source', recordedAt: new Date(0).toISOString(), domain: 'example.com' },
        events: [{ type: 'inputText', index: 1, text: 'query', timestamp: 1 }],
        snapshots: [],
      };
      const sourceState = {
        version: 1 as const, recordingFlag: true as const, recording,
        options: { protocolVersion: '2.0.0' }, selectorToIndex: [], nextIndex: 2,
        lastRecordedUrl: 'https://example.com/',
      };
      const destinationState = {
        ...sourceState,
        recording: {
          ...recording,
          events: [...recording.events, { type: 'navigate' as const, url: 'https://destination.example/', timestamp: 2 }],
        },
        lastRecordedUrl: 'https://destination.example/',
      };
      const returnedState = {
        ...destinationState,
        recording: {
          ...destinationState.recording,
          events: [...destinationState.recording.events, { type: 'navigate' as const, url: 'https://example.com/', timestamp: 3 }],
        },
        lastRecordedUrl: 'https://example.com/',
      };
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'START_RECORDING') return { success: true, protocolVersion: '2.0.0' };
        if (tabId === 42 && action === 'EXPORT_RECORDING_HANDOFF') return { success: true, state: sourceState };
        if (tabId === 99 && action === 'EXPORT_RECORDING_HANDOFF') return { success: true, state: destinationState };
        if (tabId === 99 && action === 'IMPORT_RECORDING_HANDOFF') return { success: true, state: destinationState };
        if (tabId === 42 && action === 'IMPORT_RECORDING_HANDOFF') return { success: true, state: returnedState };
        if (action === 'DISCARD_RECORDING_HANDOFF') return { success: true, active: false };
        if ((tabId === 42 || tabId === 99) && action === 'ENSURE_RECORDING_READY') {
          return { success: true, active: true, protocolVersion: '2.0.0' };
        }
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING' });
      chromeMock.mock.tabs.query.mockResolvedValueOnce([{
        id: 99, url: 'https://destination.example/',
      }]);
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toEqual({
        success: true, active: true, protocolVersion: '2.0.0',
      });

      expect(chromeMock.sessionStorage.oc_recording_session).toMatchObject({
        tabId: 99, nextIndex: 2, lastRecordedUrl: 'https://destination.example/',
      });
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenCalledWith(
        42, { action: 'DISCARD_RECORDING_HANDOFF' }, { frameId: 0 },
      );

      chromeMock.mock.tabs.query.mockResolvedValueOnce([{
        id: 42, url: 'https://example.com/',
      }]);
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toMatchObject({
        success: true, active: true,
      });
      expect(chromeMock.sessionStorage.oc_recording_session).toMatchObject({
        tabId: 42, lastRecordedUrl: 'https://example.com/',
      });
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenCalledWith(
        99, { action: 'DISCARD_RECORDING_HANDOFF' }, { frameId: 0 },
      );
    });

    it('keeps source ownership when a popup handoff import fails', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording: PageAgentRecording = {
        version: '2.0.0',
        meta: { startUrl: 'https://example.com/', title: 'Source', recordedAt: new Date(0).toISOString(), domain: 'example.com' },
        events: [], snapshots: [],
      };
      const state = {
        version: 1 as const, recordingFlag: true as const, recording,
        options: { protocolVersion: '2.0.0' }, selectorToIndex: [], nextIndex: 1,
        lastRecordedUrl: 'https://example.com/',
      };
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'START_RECORDING') return { success: true, protocolVersion: '2.0.0' };
        if (tabId === 42 && action === 'EXPORT_RECORDING_HANDOFF') return { success: true, state };
        if (tabId === 99 && action === 'IMPORT_RECORDING_HANDOFF') {
          return { success: false, error: 'destination rejected state' };
        }
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING' });
      chromeMock.mock.tabs.query.mockResolvedValueOnce([{
        id: 99, url: 'https://destination.example/',
      }]);
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toEqual({
        success: false, error: 'destination rejected state',
      });
      expect(chromeMock.sessionStorage.oc_recording_session).toMatchObject({ tabId: 42 });
      expect(chromeMock.mock.tabs.sendMessage).not.toHaveBeenCalledWith(
        42, { action: 'DISCARD_RECORDING_HANDOFF' }, { frameId: 0 },
      );
    });

    it('rejects a restricted popup target before exporting source state', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'START_RECORDING') {
          return { success: true, protocolVersion: '2.0.0' };
        }
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      chromeMock.mock.tabs.query.mockResolvedValueOnce([{
        id: 99, url: 'chrome://settings/',
      }]);
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toEqual({
        success: false, error: 'recording handoff target must be an http/https page',
      });
      expect(chromeMock.sessionStorage.oc_recording_session).toMatchObject({ tabId: 42 });
      expect(chromeMock.mock.tabs.sendMessage).not.toHaveBeenCalledWith(
        42, { action: 'EXPORT_RECORDING_HANDOFF' }, { frameId: 0 },
      );
    });

    it('fails recorder readiness closed without clearing the active v2 session', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'START_RECORDING') {
          return { success: true, protocolVersion: '2.0.0' };
        }
        if (action === 'ENSURE_RECORDING_READY') {
          return { success: true, active: true, protocolVersion: '1.0.0' };
        }
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING' });
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toEqual({
        success: false,
        error: '录制协议不匹配：期望 2.0.0，收到 1.0.0',
      });
      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toMatchObject({ state: 'recording' });

      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'ENSURE_RECORDING_READY') {
          return { success: false, active: false, error: 'checkpoint unavailable' };
        }
        return { success: true };
      });
      await expect(sendMessage({ action: 'ENSURE_RECORDING_READY' })).resolves.toEqual({
        success: false,
        error: 'checkpoint unavailable',
      });
      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toMatchObject({ state: 'recording' });
    });

    it('keeps requested legacy options when capabilities are unavailable or disabled', async () => {
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: false, status: 503 });

      const unavailable = (await sendMessage({
        action: 'START_RECORDING',
        payload: { maxSnapshots: 7 },
      })) as { protocolVersion: string };
      expect(unavailable.protocolVersion).toBe('1.0.0');
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenLastCalledWith(
        42,
        { action: 'START_RECORDING', payload: { maxSnapshots: 7 } },
        { frameId: 0 },
      );
      await sendMessage({ action: 'STOP_RECORDING' });

      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ features: { recordingV2: false } }),
      });
      const disabled = (await sendMessage({ action: 'START_RECORDING' })) as { protocolVersion: string };
      expect(disabled.protocolVersion).toBe('1.0.0');
    });

    it('uses v2 defaults when the server omits optional recording limits', async () => {
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ features: { recordingV2: true } }),
      });

      await sendMessage({ action: 'START_RECORDING' });
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenLastCalledWith(
        42,
        {
          action: 'START_RECORDING',
          payload: expect.objectContaining({
            protocolVersion: '2.0.0',
            maxEvents: 500,
            maxDurationMs: 7_200_000,
            maxRecordingBytes: 26_214_400,
          }),
        },
        { frameId: 0 },
      );
    });

    it('falls back to legacy recording when capability discovery throws', async () => {
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });
      (global.fetch as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error('capabilities offline'));

      await expect(sendMessage({ action: 'START_RECORDING' })).resolves.toMatchObject({
        success: true,
        protocolVersion: '1.0.0',
      });
    });

    it('clears the active session when content-script startup messaging fails', async () => {
      chromeMock.mock.tabs.sendMessage.mockRejectedValueOnce(new Error('receiving end missing'));

      const response = (await sendMessage({ action: 'START_RECORDING' })) as {
        success: boolean;
        error: string;
      };

      expect(response.success).toBe(false);
      expect(response.error).toContain('receiving end missing');
      expect(chromeMock.sessionStorage.oc_recording_session).toBeUndefined();
      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toEqual({ state: 'idle' });
    });

    it('clears the active session when the content script explicitly rejects startup', async () => {
      chromeMock.mock.tabs.sendMessage.mockResolvedValueOnce({ success: false, error: 'capture denied' });

      await expect(sendMessage({ action: 'START_RECORDING' })).resolves.toEqual({
        success: false,
        error: 'capture denied',
      });
      expect(chromeMock.sessionStorage.oc_recording_session).toBeUndefined();
    });

    it('uses a stable fallback when startup is rejected without an error', async () => {
      chromeMock.mock.tabs.sendMessage.mockResolvedValueOnce({ success: false });

      await expect(sendMessage({ action: 'START_RECORDING' })).resolves.toEqual({
        success: false,
        error: '录制启动失败',
      });
      expect(chromeMock.sessionStorage.oc_recording_session).toBeUndefined();
    });

    it('normalizes non-Error startup failures', async () => {
      chromeMock.mock.tabs.sendMessage.mockRejectedValueOnce('content unavailable');

      await expect(sendMessage({ action: 'START_RECORDING' })).resolves.toEqual({
        success: false,
        error: '录制启动失败：content unavailable',
      });
    });

    it('returns error when no active tab for START_RECORDING', async () => {
      chromeMock.mock.tabs.query.mockResolvedValueOnce([]);
      const response = (await sendMessage({ action: 'START_RECORDING' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有活动标签页');
    });

    it('stops recording, stores it, and returns it', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      const response = (await sendMessage({ action: 'STOP_RECORDING' })) as {
        success: boolean;
        recording: { version: string; meta: { domain: string } };
      };
      expect(response.success).toBe(true);
      expect(response.recording.version).toBe('1.0.0');
      expect(response.recording.meta.domain).toBe('example.com');
      const stored = (await sendMessage({ action: 'GET_LAST_RECORDING' })) as { recording: unknown };
      expect(stored.recording).toBeDefined();
      expect(stored.recording).not.toBeNull();
      const state = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(state.state).toBe('idle');
    });

    it('reattaches the content script before stopping after full-page navigation', async () => {
      let injections = 0;
      chromeMock.mock.scripting.executeScript.mockImplementation(async () => {
        injections++;
        return [];
      });
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'STOP_RECORDING') {
          if (injections < 2) {
            throw new Error('Could not establish connection. Receiving end does not exist.');
          }
          return {
            recording: {
              version: '1.0.0',
              meta: {
                startUrl: 'https://example.com/',
                title: 'Example',
                recordedAt: '2026-07-06T00:00:00Z',
                domain: 'example.com',
              },
              events: [],
              snapshots: [],
            },
          };
        }
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING' });
      const stopped = await sendMessage({ action: 'STOP_RECORDING' }) as {
        success: boolean;
      };

      expect(stopped.success).toBe(true);
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalledTimes(2);
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenLastCalledWith({
        target: { tabId: 42 },
        files: ['content.js'],
      });
    });

    it('rejects a stale legacy response for an active v2 recording session', async () => {
      await sendMessage({ action: 'START_RECORDING', payload: { protocolVersion: '2.0.0' } });

      const stopped = await sendMessage({ action: 'STOP_RECORDING' }) as {
        success: boolean;
        error: string;
      };

      expect(stopped).toEqual({
        success: false,
        error: '录制协议不匹配：期望 2.0.0，收到 1.0.0',
      });
      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toMatchObject({ state: 'recording' });
    });

    it('persists a completed v2 recording before returning from stop', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        const action = (message as { action?: string }).action;
        if (action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: async () => ({ recording: { id: 'recording-v2-1' } }),
      });

      await sendMessage({ action: 'START_RECORDING' });
      const response = (await sendMessage({ action: 'STOP_RECORDING' })) as {
        success: boolean;
        recording: PageAgentRecording;
        persistenceWarning?: string;
      };

      expect(response.success).toBe(true);
      expect(response.persistenceWarning).toBeUndefined();
      expect(response.recording.meta.serverRecordingId).toBe('recording-v2-1');
      const persistenceCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/api/v1/recordings'));
      expect(persistenceCall).toBeDefined();
      expect(persistenceCall![1]).toMatchObject({
        method: 'POST',
        headers: expect.objectContaining({ Authorization: 'Bearer admin-secret' }),
      });
      const body = JSON.parse(persistenceCall![1].body as string) as {
        recording: PageAgentRecording;
        startedAt: string;
        endedAt: string;
      };
      expect(body.recording.version).toBe('2.0.0');
      expect(body.startedAt).toBe(recording.meta.recordedAt);
      expect(body.endedAt).toBe(recording.meta.endedAt);
    });

    it('reuses an existing server recording id without uploading again', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'existing-recording';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING' });
      const response = (await sendMessage({ action: 'STOP_RECORDING' })) as {
        recording: PageAgentRecording;
        persistenceWarning?: string;
      };
      expect(response.recording.meta.serverRecordingId).toBe('existing-recording');
      expect(response.persistenceWarning).toBeUndefined();
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it('persists a completed recording reported by the content script', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: async () => ({ recording: { id: 'automatic-recording' } }),
      });

      const recording = makeV2Recording();
      await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', recording } },
        { tab: { id: 42, url: 'https://example.com/' } },
      );
      await flushAsync();

      const stored = (await sendMessage({ action: 'GET_LAST_RECORDING' })) as {
        recording: PageAgentRecording;
      };
      expect(stored.recording.meta.serverRecordingId).toBe('automatic-recording');
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/recordings'))).toBe(true);
    });

    it('rejects RECORDING_STATUS stopped from an iframe sender (H-2)', async () => {
      // H-2 regression: a content script in an iframe shares sender.tab.id
      // with the top-level tab. Without an explicit frameId check, any iframe
      // (e.g. an ad) could inject a crafted recording and trigger server
      // persistence under the admin Bearer token.
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });

      const response = await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', recording: makeV2Recording() } },
        { tab: { id: 42, url: 'https://example.com/' }, frameId: 1 },
      );
      expect((response as { success: boolean; error?: string }).success).toBe(false);
      // No server persistence should have been attempted.
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/recordings'))).toBe(false);
    });

    it('normalizes non-Error automatic persistence failures', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      const warning = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      fetchMock.mockRejectedValueOnce('recording storage offline');

      await sendMessage(
        { action: 'RECORDING_STATUS', payload: { status: 'stopped', recording: makeV2Recording() } },
        { tab: { id: 42, url: 'https://example.com/' } },
      );
      await flushAsync();

      expect(warning).toHaveBeenCalledWith(
        '[background] automatic recording persistence failed:',
        'recording storage offline',
      );
    });

    it('reports missing persistence configuration and malformed persistence responses', async () => {
      const recordingWithoutServer = makeV2Recording();
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording: recordingWithoutServer };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING', payload: { protocolVersion: '2.0.0' } });
      const noServer = (await sendMessage({ action: 'STOP_RECORDING' })) as { persistenceWarning?: string };
      expect(noServer.persistenceWarning).toContain('configured server');

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      fetchMock.mockResolvedValueOnce({ ok: true, status: 201, json: async () => ({ recording: {} }) });
      await sendMessage({ action: 'START_RECORDING' });
      const malformed = (await sendMessage({ action: 'STOP_RECORDING' })) as { persistenceWarning?: string };
      expect(malformed.persistenceWarning).toContain('did not include an id');
    });

    it('normalizes non-Error stop persistence failures', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      fetchMock.mockRejectedValueOnce('recording storage offline');

      await sendMessage({ action: 'START_RECORDING' });
      await expect(sendMessage({ action: 'STOP_RECORDING' })).resolves.toMatchObject({
        success: true,
        persistenceWarning: 'recording storage offline',
      });
    });

    it('blocks downstream intent work until v2 persistence succeeds', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      fetchMock
        .mockResolvedValueOnce({
          ok: false,
          status: 503,
          text: async () => 'storage unavailable',
        })
        .mockResolvedValueOnce({
          ok: false,
          status: 503,
          text: async () => 'storage unavailable',
        });

      await sendMessage({ action: 'START_RECORDING' });
      const stopped = (await sendMessage({ action: 'STOP_RECORDING' })) as {
        success: boolean;
        persistenceWarning?: string;
      };
      expect(stopped.success).toBe(true);
      expect(stopped.persistenceWarning).toContain('503');

      const predicted = (await sendMessage({ action: 'PREDICT_INTENT' })) as {
        success: boolean;
        error: string;
      };
      expect(predicted.success).toBe(false);
      expect(predicted.error).toContain('录制尚未安全持久化');
      expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/api/v1/recordings'))).toHaveLength(2);
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/admin/rules/predict-intent'))).toBe(false);
    });

    it('blocks every generation path for an incomplete v2 recording', async () => {
      const recording = makeV2Recording(false);
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });

      await sendMessage({ action: 'START_RECORDING', payload: { protocolVersion: '2.0.0' } });
      await sendMessage({ action: 'STOP_RECORDING' });

      const actions = [
        { action: 'GENERATE_RULE' },
        { action: 'PREDICT_INTENT' },
        { action: 'GENERATE_DSL_FROM_INTENT', payload: { intent: { id: 'collect', label: 'Collect', confidence: 1 } } },
        { action: 'GENERATE_DSL_FROM_INTENT_SERVER', payload: { intent: { id: 'collect', label: 'Collect', confidence: 1 } } },
        { action: 'ENHANCE_RULE' },
      ];
      for (const action of actions) {
        await expect(sendMessage(action)).resolves.toMatchObject({
          success: false,
          error: expect.stringContaining('semantic recording is incomplete'),
        });
      }
      expect(global.fetch).not.toHaveBeenCalled();
    });

    it('checkpoints and resumes only from the active recording tab', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      const recording = makeV2Recording(false);
      const checkpoint = {
        action: 'RECORDING_CHECKPOINT',
        payload: {
          recording,
          options: { protocolVersion: '2.0.0' },
          selectorToIndex: [['#collect', { index: 3, lastUsedAt: 1 }]],
          nextIndex: 4,
          lastRecordedUrl: 'https://example.com/',
        },
      };

      await expect(sendMessage(checkpoint, { tab: { id: 7, url: 'https://other.example/' } })).resolves.toMatchObject({ success: false });
      await expect(sendMessage(checkpoint, { tab: { id: 42, url: 'https://example.com/' } })).resolves.toEqual({ success: true });
      await expect(sendMessage({ action: 'RESUME_RECORDING' }, { tab: { id: 7 } })).resolves.toEqual({ active: false });

      const resumed = (await sendMessage(
        { action: 'RESUME_RECORDING' },
        { tab: { id: 42, url: 'https://example.com/' } },
      )) as { active: boolean; state: { recording: PageAgentRecording; nextIndex: number } };
      expect(resumed.active).toBe(true);
      expect(resumed.state.recording).toEqual(recording);
      expect(resumed.state.nextIndex).toBe(4);
    });

    it('validates checkpoints, resume state, and automatic status updates', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      const activeSender = { tab: { id: 42, url: 'https://example.com/' } };

      await expect(sendMessage({ action: 'RECORDING_CHECKPOINT' }, activeSender)).resolves.toMatchObject({ success: false });
      await expect(sendMessage({
        action: 'RECORDING_CHECKPOINT',
        payload: { recording: { ...makeV2Recording(false), version: '1.0.0' } },
      }, activeSender)).resolves.toMatchObject({ success: false });
      await expect(sendMessage({ action: 'RESUME_RECORDING' }, activeSender)).resolves.toEqual({ active: false });

      const inProgress = makeV2Recording(false);
      await sendMessage({ action: 'RECORDING_CHECKPOINT', payload: { recording: inProgress } }, activeSender);
      const resumed = (await sendMessage({ action: 'RESUME_RECORDING' }, activeSender)) as {
        active: boolean;
        state: { selectorToIndex: unknown[]; nextIndex: number; lastRecordedUrl: null };
      };
      expect(resumed.state).toMatchObject({ selectorToIndex: [], nextIndex: 1, lastRecordedUrl: null });

      await expect(sendMessage({
        action: 'RECORDING_STATUS',
        payload: { status: 'warning', message: 'near limit' },
      }, { tab: { id: 7 } })).resolves.toMatchObject({ success: false });
      await sendMessage({
        action: 'RECORDING_STATUS',
        payload: { status: 'warning', message: 'near limit' },
      }, activeSender);
      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toMatchObject({
        state: 'recording',
        statusMessage: 'near limit',
      });

      const completed = makeV2Recording();
      await sendMessage({ action: 'RECORDING_CHECKPOINT', payload: { recording: completed } }, activeSender);
      await expect(sendMessage({ action: 'RESUME_RECORDING' }, activeSender)).resolves.toEqual({ active: false });
      await sendMessage({ action: 'RECORDING_STATUS', payload: { status: 'stopped', recording: inProgress } }, activeSender);
      await expect(sendMessage({ action: 'GET_STATE' })).resolves.toEqual({ state: 'idle' });
    });

    it('returns the last recording for GET_LAST_RECORDING', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const response = (await sendMessage({ action: 'GET_LAST_RECORDING' })) as {
        recording: { meta: { domain: string } } | null;
      };
      expect(response.recording).not.toBeNull();
      expect(response.recording?.meta.domain).toBe('example.com');
    });

    it('AGGREGATE_DOM returns frame tree from sender tab', async () => {
      const response = (await sendMessage({ action: 'AGGREGATE_DOM', options: { maxDepth: 10 } }, { tab: { id: 42 } })) as {
        success: boolean;
        frameTree: { frameId: number; url: string; children: unknown[] };
      };
      expect(response.success).toBe(true);
      expect(response.frameTree.frameId).toBe(0);
      expect(response.frameTree.url).toBe('https://example.com/');
      expect(response.frameTree.children).toHaveLength(1);
      expect(chromeMock.mock.webNavigation.getAllFrames).toHaveBeenCalledWith({ tabId: 42 });
    });

    it('AGGREGATE_DOM fails when sender tab id is missing', async () => {
      const response = (await sendMessage({ action: 'AGGREGATE_DOM' }, {} as ChromeRuntimeMessageSender)) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toContain('标签页 ID');
    });

    it('AGGREGATE_DOM dispatches CAPTURE_DOM to every frame', async () => {
      await sendMessage({ action: 'AGGREGATE_DOM', options: { maxDepth: 5 } }, { tab: { id: 42 } });
      const calls = chromeMock.mock.tabs.sendMessage.mock.calls;
      expect(calls).toHaveLength(2);
      expect(calls[0][0]).toBe(42);
      expect(calls[0][1]).toEqual({ action: 'CAPTURE_DOM', options: { maxDepth: 5 } });
      expect(calls[0][2]).toEqual({ frameId: 0 });
      expect(calls[1][0]).toBe(42);
      expect(calls[1][1]).toEqual({ action: 'CAPTURE_DOM', options: { maxDepth: 5 } });
      expect(calls[1][2]).toEqual({ frameId: 1 });
    });

    it('returns error when the runtime listener catches a rejected handler', async () => {
      chromeMock.mock.webNavigation.getAllFrames.mockRejectedValueOnce(new Error('frames unavailable'));
      const response = (await sendMessage({ action: 'AGGREGATE_DOM', options: {} }, { tab: { id: 1 } })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toContain('frames unavailable');
    });

    it('STOP_RECORDING fails when no active tab', async () => {
      chromeMock.mock.tabs.query.mockResolvedValueOnce([]);
      const response = (await sendMessage({ action: 'STOP_RECORDING' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有活动标签页');
    });

    it('STOP_RECORDING rejects missing and explicit content-script errors', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      chromeMock.mock.tabs.sendMessage.mockResolvedValueOnce({ success: false, error: 'stop refused' });
      await expect(sendMessage({ action: 'STOP_RECORDING' })).resolves.toEqual({
        success: false,
        error: 'stop refused',
      });

      chromeMock.mock.tabs.sendMessage.mockResolvedValueOnce(undefined);
      await expect(sendMessage({ action: 'STOP_RECORDING' })).resolves.toEqual({
        success: false,
        error: '录制未返回有效数据',
      });

      chromeMock.mock.scripting.executeScript.mockRejectedValueOnce(new Error('injection denied'));
      chromeMock.mock.tabs.sendMessage.mockRejectedValueOnce(
        new Error('Could not establish connection. Receiving end does not exist.'),
      );
      await expect(sendMessage({ action: 'STOP_RECORDING' })).resolves.toEqual({
        success: false,
        error: 'Error: Could not establish connection. Receiving end does not exist.',
      });
    });

    it('START_RECORDING succeeds even when content script injection fails', async () => {
      // The manifest already auto-injects content.js at document_idle, so a
      // redundant executeScript rejection must not block START when the
      // content script answers afterwards.
      chromeMock.mock.scripting.executeScript.mockRejectedValueOnce(new Error('injection denied'));
      const response = (await sendMessage({ action: 'START_RECORDING' })) as { success: boolean };
      expect(response.success).toBe(true);
    });

    it('START_RECORDING rejects a non-recordable active tab before any injection', async () => {
      chromeMock.mock.tabs.query.mockResolvedValueOnce([{ id: 77, url: 'chrome://settings/' }]);
      const response = (await sendMessage({ action: 'START_RECORDING' })) as { success: boolean; error?: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('当前页面不支持录制');
      expect(chromeMock.mock.scripting.executeScript).not.toHaveBeenCalled();
      // The failed pre-check must not leave a half-created session behind.
      const state = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(state.state).toBe('idle');
    });

    it('START_RECORDING rejects when the tab URL is hidden (no host permission)', async () => {
      chromeMock.mock.tabs.query.mockResolvedValueOnce([{ id: 78 }] as unknown as { id: number; url: string }[]);
      const response = (await sendMessage({ action: 'START_RECORDING' })) as { success: boolean; error?: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('当前页面不支持录制');
    });

    it('START_RECORDING maps an unreachable content script plus injection failure to Chinese', async () => {
      chromeMock.mock.scripting.executeScript.mockRejectedValueOnce(
        new Error('Cannot access a chrome:// URL "chrome-extension://x/"'),
      );
      chromeMock.mock.tabs.sendMessage.mockRejectedValueOnce(new Error('Could not establish connection. Receiving end does not exist.'));
      const response = (await sendMessage({ action: 'START_RECORDING' })) as { success: boolean; error?: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('录制启动失败');
      expect(response.error).toContain('当前页面不支持录制');
      expect(response.error).not.toContain('Could not establish connection');
    });

    it('GET_LAST_RECORDING returns null when no recording exists', async () => {
      const response = (await sendMessage({ action: 'GET_LAST_RECORDING' })) as { recording: unknown };
      expect(response.recording).toBeNull();
    });

    it('AGGREGATE_DOM reads options from payload', async () => {
      await sendMessage({ action: 'AGGREGATE_DOM', payload: { options: { maxDepth: 3 } } }, { tab: { id: 42 } });
      const calls = chromeMock.mock.tabs.sendMessage.mock.calls;
      expect(calls).toHaveLength(2);
      expect(calls[0][1]).toEqual({ action: 'CAPTURE_DOM', options: { maxDepth: 3 } });
    });
  });

  describe('rule generation and download', () => {
    it('GENERATE_RULE produces YAML from stored recording', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const response = (await sendMessage({ action: 'GENERATE_RULE' })) as { success: boolean; yaml: string };
      expect(response.success).toBe(true);
      expect(response.yaml).toContain('id:');
      expect(response.yaml).toContain('name:');
      expect(response.yaml).toContain('domain: example.com');
      expect(chromeMock.sessionStorage.lastRuleYaml).toBe(response.yaml);
    });

    it('GENERATE_RULE fails without a recording', async () => {
      const response = (await sendMessage({ action: 'GENERATE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有可用录制');
    });

    it('DOWNLOAD_RULE triggers chrome.downloads.download', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'GENERATE_RULE' });
      const response = (await sendMessage({ action: 'DOWNLOAD_RULE' })) as { success: boolean; downloadId: number };
      expect(response.success).toBe(true);
      expect(response.downloadId).toBe(7);
      expect(chromeMock.mock.downloads.download).toHaveBeenCalledOnce();
      const call = ((chromeMock.mock.downloads.download.mock.calls[0] as unknown[])[0]) as { filename: string };
      expect(call.filename).toMatch(/^opencrawler-rule-example\.com-/);
      expect(call.filename).toMatch(/\.yaml$/);
    });

    it('DOWNLOAD_RULE fails without a rule', async () => {
      const response = (await sendMessage({ action: 'DOWNLOAD_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有可用规则');
    });

    it('DOWNLOAD_RULE uses unknown domain when recording metadata is missing', async () => {
      chromeMock.sessionStorage.lastRuleYaml = 'id: test-rule\nname: test';
      const response = (await sendMessage({ action: 'DOWNLOAD_RULE' })) as { success: boolean; downloadId: number };
      expect(response.success).toBe(true);
      const call = ((chromeMock.mock.downloads.download.mock.calls[0] as unknown[])[0]) as { filename: string };
      expect(call.filename).toMatch(/^opencrawler-rule-unknown-/);
    });
  });

  describe('AI rule enhancement', () => {
    it('ENHANCE_RULE posts preprocessed recording and baseline rule to /admin/rules/enhance', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'secret', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          statusText: 'OK',
          json: async () => ({ ruleId: 'rule-enhanced-1', enhancementId: 'enh-1', safetyFlags: ['needs_review'] }),
        })
        .mockResolvedValueOnce({ ok: true, status: 200, statusText: 'OK' });

      const response = (await sendMessage({ action: 'ENHANCE_RULE', payload: { userHint: '抓取标题和价格' } })) as {
        success: boolean;
        ruleId: string;
        safetyFlags: unknown[];
      };

      expect(response.success).toBe(true);
      expect(response.ruleId).toBe('rule-enhanced-1');
      expect(response.safetyFlags).toEqual(['needs_review']);
      expect(fetchMock).toHaveBeenCalledTimes(2);

      const [enhanceCall, logCall] = fetchMock.mock.calls;
      expect(enhanceCall[0]).toBe('http://localhost:8080/admin/rules/enhance');
      expect(enhanceCall[1].method).toBe('POST');
      expect(enhanceCall[1].headers['Authorization']).toBe('Bearer admin-secret');
      expect(enhanceCall[1].headers['X-Trace-Id']).toBeDefined();

      const body = JSON.parse(enhanceCall[1].body as string) as {
        recording: { domSnapshots: unknown[] };
        baselineRule: { id: string; domain: string };
        userHint: string;
      };
      expect(body.baselineRule.id).toMatch(/^ext-\d+$/);
      expect(body.baselineRule.domain).toBe('example.com');
      expect(body.userHint).toBe('抓取标题和价格');
      expect(body.recording.domSnapshots).toEqual([]);

      expect(logCall[0]).toBe('http://localhost:8080/logs');
      const logBody = JSON.parse(logCall[1].body as string) as { traceId: string; level: string };
      expect(logBody.traceId).toBe(enhanceCall[1].headers['X-Trace-Id']);
      expect(logBody.level).toBe('info');
    });

    it('ENHANCE_RULE fails without a recording', async () => {
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });
      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有可用录制');
    });

    it('ENHANCE_RULE fails when baseUrl is empty', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('未配置服务端地址');
    });

    it('ENHANCE_RULE returns error when enhance request fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500, statusText: 'Internal Server Error', text: async () => 'server error' });

      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('增强失败');
    });

    it('ENHANCE_RULE returns error when fetch throws a network error', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));

      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('增强请求失败');
    });

    it('falls back to a trace id when crypto.randomUUID is unavailable', async () => {
      vi.stubGlobal('crypto', undefined);
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, statusText: 'OK', json: async () => ({ candidates: [] }) });

      await sendMessage({ action: 'ENHANCE_RULE' });
      const [enhanceCall] = fetchMock.mock.calls;
      expect(enhanceCall[1].headers['X-Trace-Id']).toMatch(/^trace-/);
    });
  });

  describe('intent prediction and confirmed upload', () => {
    it('updates persisted recording marks and clears stale requirement workflow state', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-update-marks-1';
      recording.marks = [pageMark({ note: '旧备注' })];
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      chromeMock.sessionStorage.oc_requirement_candidate_job = { recordingId: 'recording-update-marks-1', jobId: 'candidate-stale' };
      chromeMock.sessionStorage.oc_requirement_normalize_job = { recordingId: 'recording-update-marks-1', jobId: 'normalize-stale' };
      chromeMock.sessionStorage.oc_dsl_workflow = { workflowId: 'workflow-stale' };

      const updatedMarks = [pageMark({ note: '新备注' })];
      await expect(sendMessage({ action: 'UPDATE_RECORDING_MARKS', payload: { marks: updatedMarks } })).resolves.toEqual({
        success: true,
        marks: updatedMarks,
      });
      await expect(sendMessage({ action: 'GET_LAST_RECORDING' })).resolves.toMatchObject({
        recording: { marks: updatedMarks },
      });
      expect(chromeMock.sessionStorage.oc_requirement_candidate_job).toBeUndefined();
      expect(chromeMock.sessionStorage.oc_requirement_normalize_job).toBeUndefined();
      expect(chromeMock.sessionStorage.oc_dsl_workflow).toBeUndefined();

      await expect(sendMessage({ action: 'UPDATE_RECORDING_MARKS', payload: { marks: [{ id: 'bad' }] } })).resolves.toEqual({
        success: false,
        error: 'invalid page marks',
      });
    });

    it('runs the durable requirement workflow by recording id and resumes the candidate job', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-workflow-1';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      const candidates = [
        { id: 'c1', confidence: 0.9, requirement: { title: 'One' } },
        { id: 'c2', confidence: 0.8, requirement: { title: 'Two' } },
        { id: 'c3', confidence: 0.7, requirement: { title: 'Three' } },
      ];
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({ features: { recordingV2: true, workflowV2: true } }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 202, json: async () => ({ job: { id: 'candidate-job-1', status: 'pending' } }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'candidate-job-1', recordingId: 'recording-workflow-1', status: 'completed', source: 'llm', result: { candidates } },
        }),
      });

      const generated = (await sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })) as {
        success: boolean; workflowV2: boolean; candidates: unknown[];
      };
      expect(generated).toMatchObject({ success: true, workflowV2: true });
      expect(generated.candidates).toHaveLength(3);
      const submitCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/api/v1/recordings/recording-workflow-1/requirement-jobs'));
      expect(submitCall?.[1]).toMatchObject({ method: 'POST', body: '{}' });
      expect(String(submitCall?.[1].body)).not.toContain('snapshots');
      expect(chromeMock.sessionStorage.oc_requirement_candidate_job).toMatchObject({
        recordingId: 'recording-workflow-1', marksHash: expect.any(String), jobId: 'candidate-job-1',
      });

      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({ features: { recordingV2: true, workflowV2: true } }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'candidate-job-1', recordingId: 'recording-workflow-1', status: 'completed', source: 'llm', result: { candidates } },
        }),
      });
      const callsBeforeResume = fetchMock.mock.calls.length;
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toMatchObject({ success: true, candidates });
      const resumeCalls = fetchMock.mock.calls.slice(callsBeforeResume);
      expect(resumeCalls.some(([url]) => String(url).endsWith('/api/v1/recordings/recording-workflow-1/requirement-jobs'))).toBe(false);
      expect(resumeCalls.some(([url]) => String(url).endsWith('/api/v1/requirement-jobs/candidate-job-1'))).toBe(true);
    });

    it('sends page marks only when the server advertises pageMarks and invalidates stale candidate jobs', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-marks-1';
      recording.marks = [pageMark()];
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true, pageMarks: true } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'marks-job-1', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'marks-job-1', recordingId: 'recording-marks-1', status: 'completed', source: 'llm', result: { candidates: [] } },
        }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: true,
        workflowV2: true,
      });
      const markedSubmit = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/api/v1/recordings/recording-marks-1/requirement-jobs'));
      expect(JSON.parse(markedSubmit?.[1].body as string)).toEqual({ marksOverride: recording.marks });
      const firstStoredJob = chromeMock.sessionStorage.oc_requirement_candidate_job as { marksHash?: string; jobId?: string };
      expect(firstStoredJob).toMatchObject({ jobId: 'marks-job-1', marksHash: expect.any(String) });

      recording.marks = [pageMark({ note: '更新后的商品标题' })];
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true, pageMarks: true } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'marks-job-2', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'marks-job-2', recordingId: 'recording-marks-1', status: 'completed', source: 'llm', result: { candidates: [] } },
        }),
      });
      const callsBeforeChangedMarks = fetchMock.mock.calls.length;
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: true,
        workflowV2: true,
      });
      const changedMarkCalls = fetchMock.mock.calls.slice(callsBeforeChangedMarks);
      expect(changedMarkCalls.some(([url]) => String(url).endsWith('/api/v1/requirement-jobs/marks-job-1'))).toBe(false);
      expect(changedMarkCalls.some(([url]) => String(url).endsWith('/api/v1/recordings/recording-marks-1/requirement-jobs'))).toBe(true);
      expect(chromeMock.sessionStorage.oc_requirement_candidate_job).toMatchObject({ jobId: 'marks-job-2', marksHash: expect.any(String) });
      expect((chromeMock.sessionStorage.oc_requirement_candidate_job as { marksHash?: string }).marksHash).not.toBe(firstStoredJob.marksHash);

      recording.marks = [pageMark({ id: 'mark-2', note: '不会发送' })];
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true, pageMarks: false } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'marks-job-3', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'marks-job-3', recordingId: 'recording-marks-1', status: 'completed', source: 'llm', result: { candidates: [] } },
        }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({ success: true });
      const unmarkedSubmit = [...fetchMock.mock.calls]
        .reverse()
        .find(([url]) => String(url).endsWith('/api/v1/recordings/recording-marks-1/requirement-jobs'));
      expect(JSON.parse(unmarkedSubmit?.[1].body as string)).toEqual({});
    });

    it('sends page marks to normalization and stores the matching marks hash', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-normalize-marks-1';
      recording.marks = [pageMark({ role: 'listItem', note: '每个商品卡片' })];
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true, pageMarks: true } }) });
      await sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: false } });

      const requirement = { title: 'Collect products' };
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'normalize-marks-job-1', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: {
            id: 'normalize-marks-job-1', recordingId: 'recording-normalize-marks-1', status: 'completed', source: 'manual',
            requirementId: 'requirement-marks-1', result: { requirement },
          },
        }),
      });

      await expect(sendMessage({ action: 'NORMALIZE_REQUIREMENT', payload: { requirement } })).resolves.toMatchObject({
        success: true,
        requirementId: 'requirement-marks-1',
      });
      const normalizeCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/requirement-jobs/normalize'));
      expect(JSON.parse(normalizeCall?.[1].body as string)).toEqual({ requirement, marksOverride: recording.marks });
      expect(chromeMock.sessionStorage.oc_requirement_normalize_job).toMatchObject({
        recordingId: 'recording-normalize-marks-1', marksHash: expect.any(String), jobId: 'normalize-marks-job-1',
      });
    });

    it('GET_REQUIREMENT_WORKFLOW with resumeJobId polls without POSTing a new job', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      // No recording setup — resumeJobId must short-circuit before requirementRecording.
      fetchMock.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          job: {
            id: 'job-1',
            recordingId: 'recording-resume-1',
            status: 'completed',
            source: 'llm',
            chunkCount: 4,
            completedChunks: 4,
            result: { candidates: [] },
          },
        }),
      });

      const result = (await sendMessage({
        action: 'GET_REQUIREMENT_WORKFLOW',
        payload: { resumeJobId: 'job-1' },
      })) as { success: boolean; workflowV2: boolean };

      expect(result).toMatchObject({ success: true, workflowV2: true });
      // Critical: NO POST happened — only the GET poll.
      const posts = fetchMock.mock.calls.filter((call: unknown[]) => {
        const init = call[1] as RequestInit | undefined;
        return (init?.method ?? 'GET') === 'POST';
      });
      expect(posts).toHaveLength(0);
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/requirement-jobs/job-1'))).toBe(true);
    });

    it('normalizes, retries, and confirms collection requirements through v2 endpoints', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-normalize-1';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      const requirement = {
        title: 'Collect products', description: 'Collect visible products',
        requiredInputs: [], optionalInputs: [],
        outputFields: [{ name: 'name', type: 'string', description: 'Product name' }],
        sampleOutput: { name: 'Example' },
      };
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'normalize-job-1', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: {
            id: 'normalize-job-1', recordingId: 'recording-normalize-1', status: 'completed', source: 'manual',
            requirementId: 'requirement-1', result: { requirement },
          },
        }),
      });
      const normalized = (await sendMessage({ action: 'NORMALIZE_REQUIREMENT', payload: { requirement } })) as {
        success: boolean; requirementId: string; requirement: unknown;
      };
      expect(normalized).toMatchObject({ success: true, requirementId: 'requirement-1', requirement });
      const normalizeCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/requirement-jobs/normalize'));
      expect(JSON.parse(normalizeCall?.[1].body as string)).toEqual({ requirement });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'failed-job', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'failed-job', recordingId: 'recording-normalize-1', status: 'completed', source: 'llm', result: { candidates: [] } },
        }),
      });
      await expect(sendMessage({ action: 'RETRY_REQUIREMENT_JOB', payload: { jobId: 'failed-job' } })).resolves.toMatchObject({ success: true });
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/requirement-jobs/failed-job/retry'))).toBe(true);

      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({ requirement: { id: 'requirement-1', status: 'confirmed', requirement } }),
      });
      await expect(sendMessage({ action: 'CONFIRM_REQUIREMENT', payload: { requirementId: 'requirement-1' } })).resolves.toMatchObject({ success: true });
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/requirements/requirement-1/confirm'))).toBe(true);
    });

    it('drives durable dsl generation, replay repair, resume, and immutable confirmation', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-dsl-1';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      const provisionalRule = {
        id: 'ext-example-com', version: '1.0.0', name: 'Collect products',
        domain: 'example.com', entry: 'https://example.com/',
        steps: [{ action: 'extractText', name: 'name', target: { selector: '.name' } }],
      };
      const generatedWorkflow = {
        id: 'dsl-workflow-1', requirementId: 'requirement-1', recordingId: 'recording-dsl-1',
        status: 'awaiting_replay', browserProfileId: 'current-chrome-profile',
        repairCount: 0, maxRepairs: 3, provisionalRule, provisionalYaml: 'id: ext-example-com',
      };
      const confirmedRequirement = {
        title: 'Collect products',
        description: 'Collect product names for a search keyword.',
        requiredInputs: [{ name: 'keyword', type: 'string', description: 'Search keyword' }],
        optionalInputs: [],
        outputFields: [{ name: 'name', type: 'string', description: 'Product name' }],
        sampleOutput: { name: 'Example' },
      };
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 202, json: async () => ({
          workflow: { ...generatedWorkflow, status: 'generating', provisionalRule: undefined },
          job: { id: 'dsl-job-1', workflowId: 'dsl-workflow-1', kind: 'generate', status: 'pending' },
        }),
      });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ workflow: generatedWorkflow }) });
      const generated = (await sendMessage({
        action: 'CREATE_DSL_WORKFLOW',
        payload: { requirementId: 'requirement-1', browserProfileId: 'current-chrome-profile' },
      })) as { success: boolean; workflow: typeof generatedWorkflow };
      expect(generated).toMatchObject({ success: true, workflow: { id: 'dsl-workflow-1', status: 'awaiting_replay' } });
      const createCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/api/v1/requirements/requirement-1/dsl-workflows'));
      const createBody = JSON.parse(createCall?.[1].body as string);
      expect(createBody.browserProfileId).toBe('current-chrome-profile');
      expect(createBody.baselineRule).toBeDefined();
      expect(createBody.recording).toBeUndefined();
      expect(chromeMock.sessionStorage.oc_dsl_workflow).toMatchObject({ workflowId: 'dsl-workflow-1' });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 201, json: async () => ({ replay: { id: 'replay-1', workflowId: 'dsl-workflow-1', sequence: 1, status: 'running' } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ workflow: { ...generatedWorkflow, status: 'replaying' } }) });
      await expect(sendMessage({ action: 'START_DSL_REPLAY', payload: { workflowId: 'dsl-workflow-1' } })).resolves.toMatchObject({
        success: true, replay: { id: 'replay-1' },
      });

      const repairedWorkflow = {
        ...generatedWorkflow, status: 'awaiting_replay', repairCount: 1,
        provisionalRule: { ...provisionalRule, steps: [{ action: 'extractText', name: 'name', target: { selector: '.product-name' } }] },
      };
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          replay: { id: 'replay-1', workflowId: 'dsl-workflow-1', sequence: 1, status: 'failed', outputValid: false },
          repairJob: { id: 'repair-job-1', workflowId: 'dsl-workflow-1', kind: 'repair', status: 'pending' },
        }),
      });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ workflow: repairedWorkflow }) });
      await expect(sendMessage({
        action: 'COMPLETE_DSL_REPLAY',
        payload: {
          workflowId: 'dsl-workflow-1', replayId: 'replay-1', succeeded: false,
          diagnostics: { message: 'element not found' }, output: {}, artifacts: [],
          errorCode: 'ELEMENT_NOT_FOUND', errorMessage: 'element not found',
        },
      })).resolves.toMatchObject({
        success: true, workflow: { status: 'awaiting_replay', repairCount: 1 }, repairJob: { id: 'repair-job-1' },
      });
      const completeCall = fetchMock.mock.calls.find(([url]) => String(url).includes('/replays/replay-1/complete'));
      expect(JSON.parse(completeCall?.[1].body as string)).toMatchObject({ succeeded: false, errorCode: 'ELEMENT_NOT_FOUND' });
      expect(chromeMock.sessionStorage.oc_dsl_workflow).toMatchObject({
        workflowId: 'dsl-workflow-1', replayId: 'replay-1',
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ workflow: repairedWorkflow }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          requirement: {
            id: 'requirement-1', recordingId: 'recording-dsl-1',
            status: 'confirmed', requirement: confirmedRequirement,
          },
        }),
      });
      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toMatchObject({
        success: true, active: true, replayId: 'replay-1',
        workflow: { id: 'dsl-workflow-1', repairCount: 1 },
        requirement: confirmedRequirement,
      });
      expect(fetchMock.mock.calls.some(([url]) =>
        String(url).endsWith('/api/v1/requirements/requirement-1'))).toBe(true);

      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({ ruleVersion: { ruleId: 'ext-example-com', version: 1 } }),
      });
      chromeMock.sessionStorage.oc_replay_retained_tab = { tabId: 321 };
      await expect(sendMessage({ action: 'CONFIRM_DSL_WORKFLOW', payload: { workflowId: 'dsl-workflow-1' } })).resolves.toEqual({
        success: true, ruleVersion: { ruleId: 'ext-example-com', version: 1 },
      });
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/dsl-workflows/dsl-workflow-1/confirm'))).toBe(true);
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(321);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toBeUndefined();
    });

    it('does not resume a dsl workflow stored for a different recording', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-new';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      // A previous recording's workflow reached a terminal state (e.g. approved);
      // the new recording's wizard must not resume it.
      chromeMock.sessionStorage.oc_dsl_workflow = {
        workflowId: 'dsl-workflow-old', requirementId: 'requirement-old',
        recordingId: 'recording-old', browserProfileId: 'current-chrome-profile',
      };
      fetchMock.mockClear();
      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toEqual({ success: true, active: false });
      expect(chromeMock.sessionStorage.oc_dsl_workflow).toBeUndefined();
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/api/v1/dsl-workflows/'))).toBe(false);
    });

    it('rejects a resumed workflow that does not match the persisted session', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      const originalSession = {
        workflowId: 'workflow-a',
        requirementId: 'requirement-a',
        recordingId: 'recording-a',
        replayId: 'replay-a',
      };
      chromeMock.sessionStorage.oc_dsl_workflow = { ...originalSession };
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          workflow: {
            id: 'workflow-b',
            requirementId: 'requirement-b',
            recordingId: 'recording-b',
            browserProfileId: 'profile-b',
            status: 'awaiting_replay',
            repairCount: 0,
            maxRepairs: 0,
            provisionalRule: { id: 'rule-b', steps: [] },
          },
        }),
      });

      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toEqual({
        success: false,
        active: true,
        error: '服务端返回的 DSL 工作流与恢复会话不匹配',
      });
      expect(chromeMock.sessionStorage.oc_dsl_workflow).toEqual(originalSession);
      expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
        'http://localhost:8080/api/v1/dsl-workflows/workflow-a',
      ]);
    });

    it('fails closed when a replay workflow requirement binding is stale or malformed', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      const workflow = {
        id: 'workflow-bound', requirementId: 'requirement-bound', recordingId: 'recording-bound',
        browserProfileId: 'profile-bound', status: 'awaiting_replay',
        repairCount: 0, maxRepairs: 0, provisionalRule: { id: 'rule-bound', steps: [] },
      };
      const requirement = {
        title: 'Collect products',
        description: 'Collect product names.',
        requiredInputs: [{ name: 'keyword', type: 'string', description: 'Search keyword' }],
        optionalInputs: [],
        outputFields: [{ name: 'name', type: 'string', description: 'Product name' }],
        sampleOutput: { name: 'Example' },
      };
      const cases = [
        {
          name: 'wrong requirement id',
          record: { id: 'requirement-other', recordingId: 'recording-bound', status: 'confirmed', requirement },
        },
        {
          name: 'wrong recording id',
          record: { id: 'requirement-bound', recordingId: 'recording-other', status: 'confirmed', requirement },
        },
        {
          name: 'unconfirmed requirement',
          record: { id: 'requirement-bound', recordingId: 'recording-bound', status: 'draft', requirement },
        },
        {
          name: 'unsupported input type',
          record: {
            id: 'requirement-bound', recordingId: 'recording-bound', status: 'confirmed',
            requirement: {
              ...requirement,
              requiredInputs: [{ name: 'keyword', type: 'bogus', description: 'Search keyword' }],
            },
          },
        },
        {
          name: 'default violates constraints',
          record: {
            id: 'requirement-bound', recordingId: 'recording-bound', status: 'confirmed',
            requirement: {
              ...requirement,
              requiredInputs: [],
              optionalInputs: [{
                name: 'keyword', type: 'string', description: 'Search keyword',
                default: 'x', constraints: { minLength: 2 },
              }],
            },
          },
        },
        {
          name: 'unsafe requirement content',
          record: {
            id: 'requirement-bound', recordingId: 'recording-bound', status: 'confirmed',
            requirement: {
              ...requirement,
              description: 'Collect a user password.',
            },
          },
        },
      ];
      for (const testCase of cases) {
        fetchMock.mockClear();
        chromeMock.sessionStorage.oc_dsl_workflow = { workflowId: workflow.id };
        fetchMock.mockResolvedValueOnce({
          ok: true, status: 200, json: async () => ({ workflow }),
        });
        fetchMock.mockResolvedValueOnce({
          ok: true, status: 200, json: async () => ({ requirement: testCase.record }),
        });
        await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' }), testCase.name).resolves.toMatchObject({
          success: false,
          active: true,
          error: '服务端未返回工作流绑定的已确认采集需求',
        });
        expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
          'http://localhost:8080/api/v1/dsl-workflows/workflow-bound',
          'http://localhost:8080/api/v1/requirements/requirement-bound',
        ]);
      }
    });

    it('reconciles orphaned candidate requirement jobs when no dsl workflow is stored', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      // SW evicted mid-candidates-job: the candidate job key is still in
      // session storage but no DSL workflow has been created yet.
      chromeMock.sessionStorage.oc_requirement_candidate_job = {
        recordingId: 'rec1', jobId: 'job-1',
      };
      // The server still knows about the in-flight candidates job.
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: {
            id: 'job-1', status: 'running', kind: 'candidates',
            chunkCount: 4, completedChunks: 2,
          },
        }),
      });

      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toMatchObject({
        success: true, active: false, candidateJob: { id: 'job-1', status: 'running' },
      });
      expect(fetchMock.mock.calls.some(([url]) =>
        String(url).endsWith('/api/v1/requirement-jobs/job-1'))).toBe(true);
    });

    it('rejects server-returned malformed provisionalRule and skips persistence (M-2)', async () => {
      // M-2 regression: a compromised or buggy server returning a crafted
      // provisionalRule (e.g., javascript: entry URL, non-array steps) must
      // not be persisted as lastRule, otherwise subsequent START_DSL_REPLAY
      // would execute it in the user's browser with the user's session
      // cookies. Validation runs in rememberDSLWorkflow before the storage
      // write; the workflow session key is still saved so the wizard can
      // recover, but lastRule/lastRuleYaml stay unset.
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-m-2';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      const malformedRule = {
        id: 'evil-1',
        // javascript: URL — would let a compromised server escape the http(s)
        // assumption and execute script in the replay tab.
        entry: 'javascript:alert(document.cookie)',
        domain: 'com',
        steps: 'not-an-array',
      };
      const workflowWithMalformedRule = {
        id: 'wf-malformed', requirementId: 'req-1', recordingId: 'recording-m-2',
        status: 'awaiting_replay', browserProfileId: 'profile-1',
        repairCount: 0, maxRepairs: 3,
        provisionalRule: malformedRule, provisionalYaml: 'id: evil-1',
      };
      // CREATE response (generating, no rule yet) + poll response (with the
      // malformed provisionalRule).
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 202, json: async () => ({
          workflow: {
            id: 'wf-malformed', requirementId: 'req-1', recordingId: 'recording-m-2',
            status: 'generating', browserProfileId: 'profile-1',
            repairCount: 0, maxRepairs: 3,
          },
          job: { id: 'j1', workflowId: 'wf-malformed', kind: 'generate', status: 'pending' },
        }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({ workflow: workflowWithMalformedRule }),
      });

      const generated = (await sendMessage({
        action: 'CREATE_DSL_WORKFLOW',
        payload: { requirementId: 'req-1', browserProfileId: 'profile-1' },
      })) as { success: boolean; error?: string };
      expect(generated.success).toBe(true);

      // Workflow session key IS persisted so the wizard can recover.
      expect(chromeMock.sessionStorage.oc_dsl_workflow).toMatchObject({ workflowId: 'wf-malformed' });
      // But the malformed rule was NOT persisted as lastRule/lastRuleYaml.
      expect(chromeMock.sessionStorage.lastRule).toBeUndefined();
      expect(chromeMock.sessionStorage.lastRuleYaml).toBeUndefined();
    });

    it('validates durable dsl requests and reports incomplete server responses safely', async () => {
      await expect(sendMessage({ action: 'CREATE_DSL_WORKFLOW' })).resolves.toEqual({
        success: false, error: '缺少已确认需求或浏览器配置引用',
      });
      await expect(sendMessage({
        action: 'CREATE_DSL_WORKFLOW',
        payload: { requirementId: 'requirement-1', browserProfileId: '   ' },
      })).resolves.toEqual({ success: false, error: '缺少已确认需求或浏览器配置引用' });
      await expect(sendMessage({ action: 'START_DSL_REPLAY' })).resolves.toEqual({
        success: false, error: '缺少 DSL 工作流 ID',
      });
      await expect(sendMessage({ action: 'COMPLETE_DSL_REPLAY' })).resolves.toEqual({
        success: false, error: '缺少 DSL 工作流或回放尝试 ID',
      });
      await expect(sendMessage({ action: 'CONFIRM_DSL_WORKFLOW' })).resolves.toEqual({
        success: false, error: '缺少 DSL 工作流 ID',
      });
      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toEqual({
        success: true, active: false,
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 201, json: async () => ({ replay: {} }) });
      await expect(sendMessage({
        action: 'START_DSL_REPLAY', payload: { workflowId: 'workflow-incomplete' },
      })).resolves.toMatchObject({ success: false, error: '服务端未返回回放尝试 ID' });

      fetchMock.mockResolvedValueOnce({
        ok: true, status: 201, json: async () => ({ replay: { id: 'replay-without-workflow', status: 'running' } }),
      });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({}) });
      await expect(sendMessage({
        action: 'START_DSL_REPLAY', payload: { workflowId: 'workflow-without-response-body' },
      })).resolves.toEqual({
        success: true,
        replay: { id: 'replay-without-workflow', status: 'running' },
        workflow: undefined,
      });

      chromeMock.sessionStorage.oc_dsl_workflow = { workflowId: 'workflow-incomplete' };
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({}) });
      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toMatchObject({
        success: false, active: true, error: '服务端未返回 DSL 工作流',
      });

      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          workflow: {
            id: 'workflow-incomplete', requirementId: 'requirement-1', recordingId: 'recording-1',
            browserProfileId: 'profile-1', status: 'awaiting_replay', repairCount: 0, maxRepairs: 0,
            provisionalRule: { id: 'rule-1', steps: [] },
          },
        }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          requirement: {
            id: 'requirement-1', recordingId: 'recording-1', status: 'confirmed',
          },
        }),
      });
      await expect(sendMessage({ action: 'RESUME_DSL_WORKFLOW' })).resolves.toMatchObject({
        success: false, active: true, error: '服务端未返回工作流绑定的已确认采集需求',
      });

      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({ replay: { id: 'replay-1', status: 'failed' } }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          workflow: {
            id: 'workflow-incomplete', requirementId: 'requirement-1', recordingId: 'recording-1',
            browserProfileId: 'profile-1', status: 'failed', repairCount: 3, maxRepairs: 3,
          },
        }),
      });
      await expect(sendMessage({
        action: 'COMPLETE_DSL_REPLAY',
        payload: { workflowId: 'workflow-incomplete', replayId: 'replay-1', succeeded: false },
      })).resolves.toMatchObject({ success: false, error: 'DSL 生成或修复失败' });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({}) });
      await expect(sendMessage({
        action: 'CONFIRM_DSL_WORKFLOW', payload: { workflowId: 'workflow-incomplete' },
      })).resolves.toMatchObject({ success: false, error: '服务端未返回不可变规则版本' });

      fetchMock.mockRejectedValueOnce('network unavailable');
      await expect(sendMessage({
        action: 'START_DSL_REPLAY', payload: { workflowId: 'workflow-network-error' },
      })).resolves.toEqual({ success: false, error: 'network unavailable' });
    });

    it('START_DSL_REPLAY reuses persisted replayId when the workflow is still replaying', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      // Persisted session has a replayId; the wizard is recovering after SW
      // eviction and re-issues START_DSL_REPLAY for the same workflow.
      chromeMock.sessionStorage.oc_dsl_workflow = {
        workflowId: 'wf-replaying',
        replayId: 'replay-persisted',
        recordingId: 'rec-1',
      };
      fetchMock.mockClear();

      // GET workflow returns status 'replaying' — the attempt is still live.
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          workflow: {
            id: 'wf-replaying', status: 'replaying',
            requirementId: 'requirement-1', recordingId: 'rec-1',
          },
        }),
      });

      const result = (await sendMessage({
        action: 'START_DSL_REPLAY',
        payload: { workflowId: 'wf-replaying' },
      })) as { success: boolean; replay?: { id?: string }; workflow?: { status?: string } };

      expect(result).toMatchObject({
        success: true,
        replay: { id: 'replay-persisted' },
        workflow: { status: 'replaying' },
      });

      // Critical: NO POST /replays happened — the persisted attempt is reused.
      const posts = fetchMock.mock.calls.filter((call: unknown[]) => {
        const [url, init] = call as [string, RequestInit | undefined];
        return (init?.method ?? 'GET') === 'POST' && String(url).includes('/replays');
      });
      expect(posts).toHaveLength(0);

      // Exactly one GET — the workflow status check.
      const gets = fetchMock.mock.calls.filter((call: unknown[]) => {
        const [, init] = call as [string, RequestInit | undefined];
        return (init?.method ?? 'GET') === 'GET';
      });
      expect(gets).toHaveLength(1);
      expect(String(gets[0][0])).toContain('/api/v1/dsl-workflows/wf-replaying');
    });

    it('START_DSL_REPLAY POSTs /replays when persisted workflow is no longer replaying', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      // Persisted replayId exists, but the workflow has moved back to
      // awaiting_replay (e.g. previous attempt completed/failed). A fresh
      // POST /replays is required.
      chromeMock.sessionStorage.oc_dsl_workflow = {
        workflowId: 'wf-awaiting',
        replayId: 'replay-stale',
        recordingId: 'rec-1',
      };
      fetchMock.mockClear();

      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          workflow: {
            id: 'wf-awaiting', status: 'awaiting_replay',
            requirementId: 'requirement-1', recordingId: 'rec-1',
          },
        }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: async () => ({ replay: { id: 'replay-fresh', workflowId: 'wf-awaiting', status: 'running' } }),
      });
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ workflow: { id: 'wf-awaiting', status: 'replaying' } }),
      });

      const result = (await sendMessage({
        action: 'START_DSL_REPLAY',
        payload: { workflowId: 'wf-awaiting' },
      })) as { success: boolean; replay?: { id?: string } };

      expect(result).toMatchObject({ success: true, replay: { id: 'replay-fresh' } });

      const posts = fetchMock.mock.calls.filter((call: unknown[]) => {
        const [url, init] = call as [string, RequestInit | undefined];
        return (init?.method ?? 'GET') === 'POST' && String(url).includes('/replays');
      });
      expect(posts).toHaveLength(1);
    });

    it('START_DSL_REPLAY does not reuse persisted replayId when workflowId differs', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      // Persisted session is for workflow-A; request is for workflow-B.
      chromeMock.sessionStorage.oc_dsl_workflow = {
        workflowId: 'wf-A',
        replayId: 'replay-A',
        recordingId: 'rec-1',
      };
      fetchMock.mockClear();

      // POST response for workflow-B (the fallthrough path).
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: async () => ({
          replay: {
            id: 'replay-B', status: 'running',
            workflowId: 'wf-B', sequence: 1, outputValid: false,
          },
        }),
      });
      // GET workflow-B for rememberDSLWorkflow at the end of the fallthrough path.
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ workflow: { id: 'wf-B', status: 'replaying' } }),
      });

      const result = (await sendMessage({
        action: 'START_DSL_REPLAY',
        payload: { workflowId: 'wf-B' },
      })) as { success: boolean; replay?: { id?: string } };

      // Reuse path was NOT taken — a POST to /replays happened for wf-B.
      const posts = fetchMock.mock.calls.filter((call: unknown[]) => {
        const [url, init] = call as [string, RequestInit | undefined];
        return (init?.method ?? 'GET') === 'POST' && String(url).includes('/replays');
      });
      expect(posts.length).toBeGreaterThanOrEqual(1);
      expect(result.replay?.id).toBe('replay-B'); // NOT replay-A
    });

    it('falls back safely when requirement capabilities or recordings are unavailable', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;

      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toEqual({
        success: true,
        workflowV2: false,
      });

      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });
      fetchMock.mockResolvedValueOnce({ ok: false, status: 503 });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toEqual({
        success: true,
        workflowV2: false,
      });

      fetchMock.mockRejectedValueOnce(new TypeError('capabilities unavailable'));
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toEqual({
        success: true,
        workflowV2: false,
      });

      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ features: { workflowV2: false } }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toEqual({
        success: true,
        workflowV2: false,
      });

      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ features: { workflowV2: true } }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toMatchObject({
        success: false,
        workflowV2: true,
        error: '没有可用录制',
      });
    });

    it('rejects legacy recordings from the durable requirement workflow', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ features: { workflowV2: true } }),
      });

      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW' })).resolves.toMatchObject({
        success: false,
        workflowV2: true,
        error: '采集需求工作流需要语义录制 v2',
      });
    });

    it('returns safe candidate submission, polling, and provider failures', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-errors-1';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({}) });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: false,
        error: '服务端未返回采集需求任务 ID',
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'candidate-errors' } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({}) });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: false,
        error: '服务端未返回采集需求任务',
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          job: { id: 'candidate-errors', recordingId: 'recording-errors-1', status: 'failed', source: 'llm' },
        }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: false,
        error: '采集需求生成失败；可以重试或手动填写结构化需求',
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({
        ok: false,
        status: 429,
        json: async () => ({ error: 'provider rate limited' }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: false,
        error: expect.stringContaining('provider rate limited'),
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({
        ok: false,
        status: 400,
        json: async () => ({ error: 'invalid workflow', details: 'baseline-validation' }),
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: false,
        error: expect.stringContaining('[phase: baseline-validation]'),
      });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({
        ok: false,
        status: 400,
        json: async () => ({ error: 'invalid workflow', details: 'secret selector text' }),
      });
      const unsafeDetail = await sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } });
      expect(unsafeDetail).toMatchObject({ success: false, error: expect.stringContaining('invalid workflow') });
      expect((unsafeDetail as { error: string }).error).not.toContain('secret selector text');

      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({
        ok: false,
        status: 500,
        json: async () => {
          throw new Error('invalid json');
        },
      });
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })).resolves.toMatchObject({
        success: false,
        error: expect.stringContaining('服务端返回 500'),
      });
    });

    it('only creates the candidates job on explicit request', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-probe-1';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      // Capability probe: reports workflowV2 without creating or polling a job.
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      const probed = (await sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: false } })) as {
        success: boolean; workflowV2: boolean; candidates?: unknown[];
      };
      expect(probed).toEqual({ success: true, workflowV2: true });
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/requirement-jobs'))).toBe(false);
      expect(chromeMock.sessionStorage.oc_requirement_candidate_job).toBeUndefined();

      // Explicit request: creates the job and stores it for later resume.
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({ job: { id: 'probe-job-1', status: 'pending' } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'probe-job-1', recordingId: 'recording-probe-1', status: 'completed', source: 'llm', result: { candidates: [] } },
        }),
      });
      const generated = (await sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: true } })) as {
        success: boolean; workflowV2: boolean;
      };
      expect(generated).toMatchObject({ success: true, workflowV2: true });
      expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/recordings/recording-probe-1/requirement-jobs'))).toBe(true);
      expect(chromeMock.sessionStorage.oc_requirement_candidate_job).toMatchObject({
        recordingId: 'recording-probe-1', marksHash: expect.any(String), jobId: 'probe-job-1',
      });

      // A later probe resumes the stored job instead of creating a new one.
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, json: async () => ({ features: { workflowV2: true } }) });
      fetchMock.mockResolvedValueOnce({
        ok: true, status: 200, json: async () => ({
          job: { id: 'probe-job-1', recordingId: 'recording-probe-1', status: 'completed', source: 'llm', result: { candidates: [] } },
        }),
      });
      const callsBeforeResume = fetchMock.mock.calls.length;
      await expect(sendMessage({ action: 'GET_REQUIREMENT_WORKFLOW', payload: { startCandidates: false } })).resolves.toMatchObject({
        success: true,
        workflowV2: true,
      });
      const resumeCalls = fetchMock.mock.calls.slice(callsBeforeResume);
      expect(resumeCalls.some(([url]) => String(url).endsWith('/api/v1/recordings/recording-probe-1/requirement-jobs'))).toBe(false);
      expect(resumeCalls.some(([url]) => String(url).endsWith('/api/v1/requirement-jobs/probe-job-1'))).toBe(true);
    });

    it('validates missing requirement workflow ids and converts thrown values safely', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      const recording = makeV2Recording();
      recording.meta.serverRecordingId = 'recording-id-errors';
      chromeMock.mock.tabs.sendMessage.mockImplementation(async (_tabId: number, message: unknown) => {
        if ((message as { action?: string }).action === 'STOP_RECORDING') return { recording };
        return { success: true };
      });
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });

      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({}) });
      await expect(sendMessage({ action: 'NORMALIZE_REQUIREMENT', payload: {} })).resolves.toMatchObject({
        success: false,
        error: '服务端未返回规范化任务 ID',
      });

      fetchMock.mockRejectedValueOnce('normalization disconnected');
      await expect(sendMessage({ action: 'NORMALIZE_REQUIREMENT', payload: {} })).resolves.toMatchObject({
        success: false,
        error: 'normalization disconnected',
      });

      await expect(sendMessage({ action: 'RETRY_REQUIREMENT_JOB' })).resolves.toMatchObject({
        success: false,
        error: '缺少采集需求任务 ID',
      });
      fetchMock.mockResolvedValueOnce({ ok: true, status: 202, json: async () => ({}) });
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          job: { id: 'retry-original', recordingId: 'recording-id-errors', status: 'failed', source: 'llm', errorMessage: 'still failed' },
        }),
      });
      await expect(sendMessage({ action: 'RETRY_REQUIREMENT_JOB', payload: { jobId: 'retry-original' } })).resolves.toMatchObject({
        success: false,
        error: 'still failed',
      });

      fetchMock.mockRejectedValueOnce('retry disconnected');
      await expect(sendMessage({ action: 'RETRY_REQUIREMENT_JOB', payload: { jobId: 'retry-network' } })).resolves.toMatchObject({
        success: false,
        error: 'retry disconnected',
      });

      await expect(sendMessage({ action: 'CONFIRM_REQUIREMENT' })).resolves.toMatchObject({
        success: false,
        error: '缺少采集需求 ID',
      });
      fetchMock.mockRejectedValueOnce('confirmation disconnected');
      await expect(sendMessage({ action: 'CONFIRM_REQUIREMENT', payload: { requirementId: 'requirement-network' } })).resolves.toMatchObject({
        success: false,
        error: 'confirmation disconnected',
      });
    });

    it('PREDICT_INTENT posts recording to /admin/rules/predict-intent with admin key', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'secret', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => ({
          candidates: [{ id: 'c1', label: '采集标题', description: '', confidence: 0.9 }],
          fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
          model: 'fake',
          cacheHit: false,
        }),
      });

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as {
        success: boolean;
        candidates: { id: string; label: string }[];
        model: string;
      };

      expect(response.success).toBe(true);
      expect(response.candidates).toHaveLength(1);
      expect(response.candidates[0].id).toBe('c1');
      expect(response.model).toBe('fake');

      const [predictCall] = fetchMock.mock.calls;
      expect(predictCall[0]).toBe('http://localhost:8080/admin/rules/predict-intent');
      expect(predictCall[1].method).toBe('POST');
      expect(predictCall[1].headers['Authorization']).toBe('Bearer admin-secret');
      expect(predictCall[1].headers['X-Trace-Id']).toBeDefined();
      const body = JSON.parse(predictCall[1].body as string) as { recording: { meta: { domain: string } } };
      expect(body.recording.meta.domain).toBe('example.com');
    });

    it('PREDICT_INTENT accepts recording from payload', async () => {
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'secret', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => ({
          candidates: [{ id: 'c1', label: '采集标题', description: '', confidence: 0.9 }],
          fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
          model: 'fake',
          cacheHit: false,
        }),
      });

      const recording = {
        version: '1.0.0' as const,
        meta: { startUrl: 'https://shop.example.com/', title: 'Shop', recordedAt: '2026-07-06T00:00:00Z', domain: 'shop.example.com' },
        events: [],
        snapshots: [],
      };
      const response = (await sendMessage({ action: 'PREDICT_INTENT', payload: { recording } })) as {
        success: boolean;
        candidates: { id: string }[];
      };

      expect(response.success).toBe(true);
      expect(response.candidates).toHaveLength(1);
      const [predictCall] = fetchMock.mock.calls;
      const body = JSON.parse(predictCall[1].body as string) as { recording: { meta: { domain: string } } };
      expect(body.recording.meta.domain).toBe('shop.example.com');
    });

    it('PREDICT_INTENT fails without a recording', async () => {
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有可用录制');
    });

    it('PREDICT_INTENT fails when baseUrl is empty', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('未配置服务端地址');
    });

    it('PREDICT_INTENT awaits ensureServerConfigLoaded before reading serverConfig (H-3)', async () => {
      // H-3 regression: during SW cold-start, serverConfig.baseUrl is ''
      // until loadServerConfig resolves. A message arriving in that window
      // must await the config load rather than reading the stale empty value.
      // The test pre-populates storage then blocks the storage read so the
      // startup loadServerConfig stays pending; the handler should still
      // observe the configured baseUrl once the read resolves.
      chromeMock.localStorage.serverConfig = { baseUrl: 'http://localhost:8080' };
      chromeMock.sessionStorage.serverKeys = { adminApiKey: 'admin-secret' };

      const originalLocalGet = chromeMock.mock.storage.local.get;
      let resolveConfigRead!: () => void;
      const configReadBlocked = new Promise<void>((r) => { resolveConfigRead = r; });
      chromeMock.mock.storage.local.get = vi.fn(async (keys?: string | string[] | Record<string, unknown> | null) => {
        if (keys === 'serverConfig') await configReadBlocked;
        return originalLocalGet(keys as string);
      });

      // Re-import: startup loadServerConfig is now blocked on configReadBlocked.
      vi.resetModules();
      await import('../background');

      // After re-import the newest listener sits at the end of the array;
      // dispatch via that one so we exercise the new module's handler.
      const sendViaLatest = (msg: unknown): Promise<unknown> => {
        return new Promise((resolve) => {
          const sendResponse = vi.fn((response) => resolve(response));
          const listener = chromeMock.listeners[chromeMock.listeners.length - 1];
          listener(msg, {}, sendResponse);
        });
      };

      // Set up a recording via the public message API (START_RECORDING /
      // STOP_RECORDING do not depend on cold-start serverConfig).
      await sendViaLatest({ action: 'START_RECORDING' });
      await sendViaLatest({ action: 'STOP_RECORDING' });
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ candidates: [], fallbackIntent: null, model: 'test', cacheHit: false }),
      });

      // Send PREDICT_INTENT — handler must await the in-flight config load
      // rather than reading the empty initial serverConfig.
      const responsePromise = sendViaLatest({ action: 'PREDICT_INTENT' });
      resolveConfigRead();
      const response = (await responsePromise) as { success: boolean; error?: string };

      // With config now loaded, the handler should NOT have failed with the
      // cold-start "未配置服务端地址" error.
      expect(response.error).not.toBe('未配置服务端地址');
    });

    it('PREDICT_INTENT returns error when predict request fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500, statusText: 'Internal Server Error', text: async () => 'server error' });

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('意图预测失败');
    });

    it('PREDICT_INTENT returns error when fetch throws a network error', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('意图预测请求失败');
      expect(response.error).toContain('Failed to fetch');
    });

    it('PREDICT_INTENT returns error when fetch times out', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      vi.useFakeTimers();
      try {
        const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
        fetchMock.mockImplementationOnce((_url, options: RequestInit) => {
          return new Promise((_, reject) => {
            const signal = options.signal as AbortSignal | undefined;
            if (signal?.aborted) {
              reject(new Error('The operation was aborted'));
              return;
            }
            signal?.addEventListener('abort', () => reject(new Error('The operation was aborted')));
          });
        });

        const responsePromise = sendMessage({ action: 'PREDICT_INTENT' });
        await vi.advanceTimersByTimeAsync(181000);
        const response = (await responsePromise) as { success: boolean; error: string };
        expect(response.success).toBe(false);
        expect(response.error).toContain('意图预测请求失败');
      } finally {
        vi.useRealTimers();
      }
    });

    it('PREDICT_INTENT handles a non-array candidates field', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => ({ candidates: 'unexpected', model: 'fake' }),
      });

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; candidates: unknown };
      expect(response.success).toBe(true);
      expect(response.candidates).toBe('unexpected');
    });

    it('GENERATE_DSL_FROM_INTENT converts and enhances rule from intent', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      const response = (await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } })) as {
        success: boolean;
        rule: { id: string };
        yaml: string;
      };
      expect(response.success).toBe(true);
      expect(response.rule.id).toMatch(/^ext-\d+$/);
      expect(response.yaml).toContain('id:');
      expect(chromeMock.sessionStorage.lastRule).toBeDefined();
      expect(chromeMock.sessionStorage.lastRuleYaml).toBe(response.yaml);
    });

    it('GENERATE_DSL_FROM_INTENT fails without a recording', async () => {
      const response = (await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } } })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有可用录制');
    });

    it('GENERATE_DSL_FROM_INTENT fails without an intent', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const response = (await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('未选择意图');
    });

    it('UPLOAD_CONFIRMED_RULE posts rule with approvalStatus pending', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'secret', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, statusText: 'OK' });

      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as { success: boolean; ruleId: string };
      expect(response.success).toBe(true);
      expect(response.ruleId).toMatch(/^ext-\d+$/);

      const [ruleCall] = fetchMock.mock.calls;
      expect(ruleCall[0]).toBe('http://localhost:8080/admin/rules');
      expect(ruleCall[1].method).toBe('POST');
      expect(ruleCall[1].headers['Authorization']).toBe('Bearer admin-secret');
      expect(ruleCall[1].headers['X-Trace-Id']).toBeDefined();
      const uploadedRule = JSON.parse(ruleCall[1].body as string) as { id: string; approvalStatus: string; source: string };
      expect(uploadedRule.approvalStatus).toBe('pending');
      expect(uploadedRule.source).toBe('pageagent');
    });

    it('UPLOAD_CONFIRMED_RULE fails without a generated rule', async () => {
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有已确认的规则');
    });

    it('UPLOAD_CONFIRMED_RULE fails when baseUrl is empty', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('未配置服务端地址');
    });

    it('UPLOAD_CONFIRMED_RULE returns error when upload fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: false, status: 400, statusText: 'Bad Request', text: async () => 'bad request' });

      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('规则保存失败');
    });

    it('UPLOAD_CONFIRMED_RULE returns error when fetch throws a network error', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));

      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('规则保存请求失败');
    });

    it('PREDICT_INTENT uses admin Authorization header when adminApiKey is configured', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => ({
          candidates: [{ id: 'c1', label: '采集标题', description: '', confidence: 0.9 }],
          fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
          model: 'fake',
          cacheHit: false,
        }),
      });

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean };
      expect(response.success).toBe(true);

      const [predictCall] = fetchMock.mock.calls;
      expect(predictCall[1].headers['Authorization']).toBe('Bearer admin-secret');
    });

    it('PREDICT_INTENT succeeds even when the audit log request fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'secret', adminApiKey: 'admin-secret' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          statusText: 'OK',
          json: async () => ({
            candidates: [{ id: 'c1', label: '采集标题', description: '', confidence: 0.9 }],
            fallbackIntent: { id: 'custom', label: '其他目的', description: '', confidence: 0 },
            model: 'fake',
            cacheHit: false,
          }),
        })
        .mockRejectedValueOnce(new Error('log endpoint unreachable'));

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean };
      expect(response.success).toBe(true);
    });

    it('returns error for unknown actions', async () => {
      const response = (await sendMessage({ action: 'UNKNOWN_ACTION' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('未知操作');
      expect(response.error).toContain('UNKNOWN_ACTION');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER fails without a recording', async () => {
      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('没有可用录制');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER fails when baseUrl is empty', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('未配置服务端地址');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER returns error when server request fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: false, status: 500, statusText: 'Internal Server Error', text: async () => 'server error' });

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('服务端生成失败');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER returns error when fetch throws a network error', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('生成 DSL 请求失败');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER returns error when fetch times out', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      vi.useFakeTimers();
      try {
        const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
        fetchMock.mockImplementationOnce((_url, options: RequestInit) => {
          return new Promise((_, reject) => {
            const signal = options.signal as AbortSignal | undefined;
            if (signal?.aborted) {
              reject(new Error('The operation was aborted'));
              return;
            }
            signal?.addEventListener('abort', () => reject(new Error('The operation was aborted')));
          });
        });

        const responsePromise = sendMessage({
          action: 'GENERATE_DSL_FROM_INTENT_SERVER',
          payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
        });
        await vi.advanceTimersByTimeAsync(181000);
        const response = (await responsePromise) as { success: boolean; error: string };
        expect(response.success).toBe(false);
        expect(response.error).toContain('生成 DSL 请求失败');
      } finally {
        vi.useRealTimers();
      }
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER succeeds and stores rule from server', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const serverRule = { id: 'ext-server-1', version: '1.0.0', name: 'Server rule', domain: 'example.com', steps: [] };
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => ({ rule: serverRule, yaml: 'id: ext-server-1\nname: Server rule' }),
      });

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; rule: { id: string }; yaml: string };
      expect(response.success).toBe(true);
      expect(response.rule.id).toBe('ext-server-1');
      expect(response.yaml).toContain('id: ext-server-1');
      expect(chromeMock.sessionStorage.lastRule).toBeDefined();
      expect(chromeMock.sessionStorage.lastRuleYaml).toBe(response.yaml);
    });
  });

  describe('runtime listener routing', () => {
    it('runtime listener handles replay progress messages directly', () => {
      const sendResponse = vi.fn();
      expect(chromeMock.listeners.length).toBeGreaterThanOrEqual(1);
      const handled = chromeMock.listeners[0]({ action: 'REPLAY_PROGRESS', payload: { type: 'log', message: 'test' } }, {}, sendResponse);
      expect(handled).toBe(true);
      expect(sendResponse).toHaveBeenCalledWith({ received: true });
    });

    it('runtime listener handles replay complete messages directly', () => {
      const sendResponse = vi.fn();
      expect(chromeMock.listeners.length).toBeGreaterThanOrEqual(1);
      const handled = chromeMock.listeners[0]({ action: 'REPLAY_COMPLETE', payload: { status: 'success' } }, {}, sendResponse);
      expect(handled).toBe(true);
      expect(sendResponse).toHaveBeenCalledWith({ received: true });
    });
  });

  describe('replay control', () => {
    it('START_REPLAY returns error without rule', async () => {
      const response = (await sendMessage({ action: 'START_REPLAY' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toBe('没有可用规则');
    });

    it('START_REPLAY returns error when rule lacks entry URL', async () => {
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule: makeRule() } })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toBe('规则缺少入口 URL');
    });

    it('START_REPLAY creates tab and injects replay-runner with boot payload', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const response = (await sendMessage({
        action: 'START_REPLAY',
        payload: { rule, variables: { keyword: 'phone' } },
      })) as { success: boolean; taskId: string };
      expect(response.success).toBe(true);
      // §SEC-002: taskId is a cryptographically random UUID (alarms/storage key
      // matching is the authorization boundary for teardown).
      expect(response.taskId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i);
      expect(chromeMock.mock.tabs.create).toHaveBeenCalledWith({ url: 'https://example.com/replay', active: false });

      // Let the mocked tab complete load and the async boot path progress.
      await flushAsync();
      await flushAsync();

      // §5.1: inject replay-runner.js, then invoke __ocReplayBoot via
      // executeScript({func, args}) — race-free vs. the old START_STEP_RUNNER
      // onMessage handshake.
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalledWith({
        target: { tabId: 123 },
        files: ['intent/replay-runner.js'],
      });
      const calls = chromeMock.mock.scripting.executeScript.mock.calls as unknown as Array<
        [{ files?: string[]; func?: Function; args?: unknown[] }]
      >;
      const bootCall = calls.find((c) => !c[0].files);
      expect(bootCall).toBeDefined();
      const bootInjection = bootCall![0];
      expect(typeof bootInjection.func).toBe('function');
      expect(bootInjection.args).toEqual([
        expect.objectContaining({
          rule,
          variables: { keyword: 'phone' },
          taskId: response.taskId,
          workerId: 'replay-worker',
        }),
      ]);
    });

    it('START_REPLAY supersedes an active replay by cleaning up the prior session', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const first = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        taskId: string;
      };
      await flushAsync();
      await flushAsync();

      // ADV-005: second START_REPLAY tears down the first session and starts
      // fresh instead of returning an "already active" error.
      const second = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        taskId: string;
      };
      expect(second.success).toBe(true);
      expect(second.taskId).not.toBe(first.taskId);
      // The prior session's tab is closed during cleanup.
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
    });

    it('REPLAY_PROGRESS is broadcast to the wizard when sender is the active replay tab', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      const payload = { type: 'status', status: 'running' };
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_PROGRESS', payload },
        { tab: { id: 123, url: 'https://example.com/replay' } },
      );
      expect(response).toEqual({ received: true });
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith({ action: 'REPLAY_PROGRESS', payload });
    });

    it('REPLAY_PROGRESS is NOT broadcast when sender is not the active replay tab', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      const broadcastBefore = chromeMock.mock.runtime.sendMessage.mock.calls
        .filter((c: unknown[]) => (c[0] as { action?: string }).action === 'REPLAY_PROGRESS').length;
      await sendRuntimeMessage(
        { action: 'REPLAY_PROGRESS', payload: { type: 'fake' } },
        { tab: { id: 999, url: 'https://attacker.example/' } },
      );
      const broadcastsAfter = chromeMock.mock.runtime.sendMessage.mock.calls
        .filter((c: unknown[]) => (c[0] as { action?: string }).action === 'REPLAY_PROGRESS').length;
      expect(broadcastsAfter).toBe(broadcastBefore);
    });

    it('keeps a successful replay tab for the oracle until the next replay supersedes it', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        taskId: string;
      };
      expect(start.success).toBe(true);
      await flushAsync();
      await flushAsync();

      const response = await sendRuntimeMessage({ action: 'REPLAY_COMPLETE', payload: { status: 'success' } }, { tab: { id: 123 } });
      expect(response).toEqual({ received: true });
      // cleanupReplaySession runs asynchronously after sendResponse; flush.
      await flushAsync();
      await flushAsync();
      expect(chromeMock.mock.tabs.remove).not.toHaveBeenCalledWith(123);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toEqual(expect.objectContaining({
        tabId: 123,
        taskId: start.taskId,
      }));
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({ status: 'success' }),
        }),
      );

      const next = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { success: boolean };
      expect(next.success).toBe(true);
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toBeUndefined();
    });

    it('keeps a retained tab when navigation reinjection emits duplicate success', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        taskId: string;
      };
      await flushAsync();
      await flushAsync();

      await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success', taskId: start.taskId } },
        { tab: { id: 123 } },
      );
      await flushAsync();
      await flushAsync();
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toEqual(expect.objectContaining({
        tabId: 123,
        taskId: start.taskId,
      }));

      chromeMock.mock.tabs.remove.mockClear();
      chromeMock.mock.runtime.sendMessage.mockClear();
      await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success', taskId: start.taskId } },
        { tab: { id: 123 } },
      );
      await flushAsync();

      expect(chromeMock.mock.tabs.remove).not.toHaveBeenCalledWith(123);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toEqual(expect.objectContaining({
        tabId: 123,
        taskId: start.taskId,
      }));
      expect(chromeMock.mock.runtime.sendMessage).not.toHaveBeenCalledWith(
        expect.objectContaining({ action: 'REPLAY_COMPLETE' }),
      );
    });

    it('REPLAY_COMPLETE from a non-active tab falls through without closing the active tab', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();

      // Sender is a different tab than the active replay tab (123).
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success' } },
        { tab: { id: 999 } },
      );
      expect(response).toEqual({ received: true });
      // The active replay tab/session remain intact; only the mismatched sender
      // tab is rejected.
      await flushAsync();
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(999);
      expect(chromeMock.sessionStorage.oc_replay_session).toBeDefined();
    });

    it('ABORT_REPLAY closes tab and broadcasts complete', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();

      const response = (await sendMessage({ action: 'ABORT_REPLAY' })) as { success: boolean };
      expect(response.success).toBe(true);
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenCalledWith(123, { action: 'ABORT_REPLAY' });
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith({
        action: 'REPLAY_COMPLETE',
        payload: { status: 'cancelled', message: '用户取消回放' },
      });
    });

    it('START_REPLAY returns error when created tab lacks an id', async () => {
      chromeMock.mock.tabs.create.mockResolvedValueOnce({ url: 'https://example.com/replay' } as ChromeTab);
      const rule = makeRule('https://example.com/replay');
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toBe('无法创建回放标签页');
    });

    it('START_REPLAY total alarm fires cleanup when scheduled time elapses', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        taskId: string;
      };
      expect(start.success).toBe(true);
      await flushAsync();
      await flushAsync();

      // Phase 2: the 30min total watchdog is a chrome.alarm; firing the
      // captured onAlarm listener stands in for the scheduled wake-up.
      chromeMock.mock.runtime.sendMessage.mockClear();
      chromeMock.mock.tabs.remove.mockClear();
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: `replay_total_${start.taskId}` });
      }

      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith({
        action: 'REPLAY_COMPLETE',
        payload: { status: 'failure', message: '总时长超限（30min）' },
      });
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
      expect(chromeMock.mock.alarms.clear).toHaveBeenCalledWith(`replay_total_${start.taskId}`);
      expect(chromeMock.mock.alarms.clear).toHaveBeenCalledWith(`replay_idle_${start.taskId}`);

      // Active replay should be cleared so a new replay can start.
      const next = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { success: boolean };
      expect(next.success).toBe(true);
    });

    it('START_REPLAY broadcasts failure when replay-runner injection fails', async () => {
      vi.useFakeTimers();
      try {
        chromeMock.mock.scripting.executeScript.mockRejectedValueOnce(new Error('inject failed'));
        const rule = makeRule('https://example.com/replay', [
          { action: 'extract', name: 'x', target: { selector: '#x' } },
        ]);
        const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
          success: boolean;
          taskId: string;
        };
        expect(start.success).toBe(true);
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(0);

        expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
        expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith({
          action: 'REPLAY_COMPLETE',
          payload: { status: 'failure', message: 'inject failed' },
        });
      } finally {
        vi.useRealTimers();
      }
    });

    it('runtime listener routes unknown messages through handleMessage', async () => {
      const { handled, response } = sendRuntimeMessageRaw({ action: 'UNKNOWN_RUNTIME_MESSAGE' });
      expect(handled).toBe(true);
      const resp = (await response) as { success: boolean; error: string };
      expect(resp.success).toBe(false);
      expect(resp.error).toContain('未知操作');
    });
  });

  describe('server config', () => {
    it('SET_SERVER_CONFIG persists baseUrl to local storage and keys to session storage', async () => {
      const response = (await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'key', adminApiKey: 'admin' },
      })) as { success: boolean };
      expect(response.success).toBe(true);
      expect(chromeMock.localStorage.serverConfig).toEqual({
        baseUrl: 'http://localhost:8080',
      });
      expect(chromeMock.sessionStorage.serverKeys).toEqual({
        apiKey: 'key',
        adminApiKey: 'admin',
      });
    });

    it('SET_SERVER_CONFIG normalizes trailing slashes in base URL', async () => {
      const response = (await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080/', apiKey: 'key', adminApiKey: 'admin' },
      })) as { success: boolean };
      expect(response.success).toBe(true);
      expect(chromeMock.localStorage.serverConfig).toEqual({
        baseUrl: 'http://localhost:8080',
      });
      expect(chromeMock.sessionStorage.serverKeys).toEqual({
        apiKey: 'key',
        adminApiKey: 'admin',
      });
    });

    it('SET_SERVER_CONFIG removes session keys when rememberSession is false', async () => {
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'key', adminApiKey: 'admin' },
      });
      expect(chromeMock.sessionStorage.serverKeys).toBeDefined();

      const response = (await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', apiKey: 'key', adminApiKey: 'admin', rememberSession: false },
      })) as { success: boolean };
      expect(response.success).toBe(true);
      expect(chromeMock.sessionStorage.serverKeys).toBeUndefined();
    });

    it('loads server config and keys on startup', async () => {
      chromeMock.localStorage.serverConfig = { baseUrl: 'http://example.com' };
      chromeMock.sessionStorage.serverKeys = { apiKey: 'loaded', adminApiKey: 'admin-loaded' };
      chromeMock.listeners.length = 0;
      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      const response = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(response.state).toBe('idle');
    });

    it('catches storage read errors during startup config loading', async () => {
      chromeMock.mock.storage.local.get.mockRejectedValueOnce(new Error('storage closed'));
      chromeMock.listeners.length = 0;
      vi.resetModules();
      await import('../background');
      await flushAsync();
      const response = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(response.state).toBe('idle');
    });
  });

  describe('additional message coverage', () => {
    it('AGGREGATE_DOM defaults to empty options when none are provided', async () => {
      await sendMessage({ action: 'AGGREGATE_DOM' }, { tab: { id: 42 } });
      const calls = chromeMock.mock.tabs.sendMessage.mock.calls;
      expect(calls[0][1]).toEqual({ action: 'CAPTURE_DOM', options: {} });
    });

    it('REPLAY_PROGRESS broadcast tolerates runtime.sendMessage failures', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      chromeMock.mock.runtime.sendMessage.mockRejectedValueOnce(new Error('port closed'));
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_PROGRESS', payload: { type: 'log' } },
        { tab: { id: 123, url: 'https://example.com/replay' } },
      );
      expect(response).toEqual({ received: true });
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith({
        action: 'REPLAY_PROGRESS',
        payload: { type: 'log' },
      });
    });

    it('REPLAY_COMPLETE broadcast tolerates runtime.sendMessage and tab removal failures', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();

      chromeMock.mock.runtime.sendMessage.mockRejectedValueOnce(new Error('port closed'));
      chromeMock.mock.tabs.remove.mockRejectedValueOnce(new Error('already closed'));
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'failure', message: 'replay failed' } },
        { tab: { id: 123 } },
      );
      expect(response).toEqual({ received: true });
      await flushAsync();
      await flushAsync();
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
    });
  });

  describe('intent prediction and generation edge cases', () => {
    it('PREDICT_INTENT handles failing response text and failing audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: false,
          status: 500,
          statusText: 'Internal Server Error',
          text: async () => {
            throw new Error('read fail');
          },
        })
        .mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('意图预测失败');
    });

    it('PREDICT_INTENT handles non-Error network failures and failing audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce('network down').mockRejectedValueOnce('log fail');

      const response = (await sendMessage({ action: 'PREDICT_INTENT' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('意图预测请求失败');
      expect(response.error).toContain('network down');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER sends admin Authorization header', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-key' },
      });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async () => ({ rule: { id: 'ext-admin-1' }, yaml: 'id: ext-admin-1' }),
      });

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean };
      expect(response.success).toBe(true);

      const [call] = fetchMock.mock.calls;
      expect(call[1].headers['Authorization']).toBe('Bearer admin-key');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER handles failing response text and audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: false,
          status: 500,
          statusText: 'Internal Server Error',
          text: async () => {
            throw new Error('read fail');
          },
        })
        .mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('服务端生成失败');
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER succeeds even when audit log fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          statusText: 'OK',
          json: async () => ({ rule: { id: 'ext-ok-1' }, yaml: 'id: ext-ok-1' }),
        })
        .mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean };
      expect(response.success).toBe(true);
    });

    it('GENERATE_DSL_FROM_INTENT_SERVER handles non-Error network failures and failing audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce('network down').mockRejectedValueOnce('log fail');

      const response = (await sendMessage({
        action: 'GENERATE_DSL_FROM_INTENT_SERVER',
        payload: { intent: { id: 'c1', label: 'x', description: '', confidence: 0.9 } },
      })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('生成 DSL 请求失败');
    });

    it('UPLOAD_CONFIRMED_RULE handles failing response text and audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: false,
          status: 400,
          statusText: 'Bad Request',
          text: async () => {
            throw new Error('read fail');
          },
        })
        .mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toContain('规则保存失败');
    });

    it('UPLOAD_CONFIRMED_RULE succeeds even when audit log fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, statusText: 'OK' }).mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as { success: boolean };
      expect(response.success).toBe(true);
    });

    it('UPLOAD_CONFIRMED_RULE handles non-Error network failures and failing audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      const intent = { id: 'c1', label: '采集商品列表', description: '', confidence: 0.9 };
      await sendMessage({ action: 'GENERATE_DSL_FROM_INTENT', payload: { intent } });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce('net fail').mockRejectedValueOnce('log fail');

      const response = (await sendMessage({ action: 'UPLOAD_CONFIRMED_RULE' })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toContain('规则保存请求失败');
    });

    it('ENHANCE_RULE handles failing response text and audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: false,
          status: 500,
          statusText: 'Internal Server Error',
          text: async () => {
            throw new Error('read fail');
          },
        })
        .mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('增强失败');
    });

    it('ENHANCE_RULE succeeds even when audit log fails', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          statusText: 'OK',
          json: async () => ({ ruleId: 'enhanced-ok' }),
        })
        .mockRejectedValueOnce(new Error('log fail'));

      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean };
      expect(response.success).toBe(true);
    });

    it('ENHANCE_RULE handles non-Error network failures and failing audit log', async () => {
      await sendMessage({ action: 'START_RECORDING' });
      await sendMessage({ action: 'STOP_RECORDING' });
      await sendMessage({ action: 'SET_SERVER_CONFIG', payload: { baseUrl: 'http://localhost:8080' } });

      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      fetchMock.mockRejectedValueOnce('net fail').mockRejectedValueOnce('log fail');

      const response = (await sendMessage({ action: 'ENHANCE_RULE' })) as { success: boolean; error: string };
      expect(response.success).toBe(false);
      expect(response.error).toContain('增强请求失败');
    });
  });

  describe('replay runtime and storage coverage', () => {
    it('handles rule entry as a string and missing rule version', async () => {
      const rule = {
        id: 'rule-string',
        domain: 'example.com',
        entry: 'https://example.com/replay',
        steps: [],
      } as unknown as Rule;
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { success: boolean; taskId?: string };
      expect(response.success).toBe(true);
      expect(response.taskId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i);
    });

    it('START_REPLAY rejects entry URL whose host is outside rule.domain', async () => {
      const rule = makeRule('https://other.example.org/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
      expect(response.error).toContain('不在 rule.domain');
    });

    it('START_REPLAY rejects an overly-broad rule.domain like a bare TLD (H-5)', async () => {
      // H-5 regression: hostMatchesDomain uses host.endsWith('.' + d) which
      // means rule.domain='com' would match every .com host. Reject rules
      // whose domain entry is too short to be a real registered domain.
      const rule = makeRule('https://attacker.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      rule.domain = 'com';
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        error: string;
      };
      expect(response.success).toBe(false);
    });

    it('START_REPLAY persists session and payload to chrome.storage.session', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      expect(chromeMock.sessionStorage.oc_replay_session).toBeDefined();
      const session = chromeMock.sessionStorage.oc_replay_session as { taskId: string; ruleId: string };
      expect(session.taskId).toBe(response.taskId);
      expect(session.ruleId).toBe('rule-1');
      expect(session).toMatchObject({ expectedDomains: ['example.com'] });
      const payloadKey = `oc_replay_payload_${response.taskId}`;
      expect(chromeMock.sessionStorage[payloadKey]).toBeDefined();
    });

    it('recognizes a variety of navigating actions', async () => {
      const rule = makeRule('https://example.com/replay', [
        { action: 'navigate', url: 'https://example.com/1' },
        { action: 'reload' },
        { action: 'pressKey', key: 'Enter' },
        { action: 'keyCombination', keys: ['Ctrl', 'A'] },
        { action: 'type', selector: '#q', value: 'x', submit: true },
        { action: 'submit', selector: '#form' },
        { action: 'requestHuman' },
        { action: 'hoverClick', target: { selector: '#link' } },
      ]);
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { success: boolean };
      expect(response.success).toBe(true);
      await flushAsync();
      await flushAsync();
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalled();
    });

    it('REPLAY_CONTEXT writes context to storage.session from the active tab', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      const ctx = { lastCompletedStepPath: [{ kind: 'top', childIdx: 1 }], extracted: { items: [1] }, evaluated: {}, captured: {} };
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_CONTEXT', payload: ctx },
        { tab: { id: 123 } },
      );
      expect(response).toEqual({ received: true });
      const stored = chromeMock.sessionStorage[`oc_replay_ctx_${start.taskId}`];
      expect(stored).toEqual(ctx);
    });

    it('REPLAY_CONTEXT ignores senders whose tab is not the active replay tab', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      const ctx = { lastCompletedStepPath: [{ kind: 'top', childIdx: 4 }], extracted: { poison: true }, evaluated: {}, captured: {} };
      await sendRuntimeMessage(
        { action: 'REPLAY_CONTEXT', payload: ctx },
        { tab: { id: 999 } },
      );
      expect(chromeMock.sessionStorage[`oc_replay_ctx_${start.taskId}`]).toBeUndefined();
    });

    it('REPLAY_PROGRESS ignores progress from a non-active tab sender', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();

      const before = (chromeMock.sessionStorage.oc_replay_session as { hopCount: number }).hopCount;
      await sendRuntimeMessage(
        { action: 'REPLAY_PROGRESS', payload: { type: 'log' } },
        { tab: { id: 999 } },
      );
      // hopCount decrement only fires for senders matching the active tab.
      const after = (chromeMock.sessionStorage.oc_replay_session as { hopCount: number }).hopCount;
      expect(after).toBe(before);
    });

    it('cleanupReplaySession clears session, payload, and context storage keys', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      // Seed a context key to verify cleanup removes it.
      await chrome.storage.session.set({ [`oc_replay_ctx_${start.taskId}`]: { lastCompletedStepPath: [{ kind: 'top', childIdx: 0 }] } });
      expect(chromeMock.sessionStorage.oc_replay_session).toBeDefined();
      expect(chromeMock.sessionStorage[`oc_replay_payload_${start.taskId}`]).toBeDefined();
      expect(chromeMock.sessionStorage[`oc_replay_ctx_${start.taskId}`]).toBeDefined();

      await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success' } },
        { tab: { id: 123 } },
      );
      await flushAsync();

      expect(chromeMock.sessionStorage.oc_replay_session).toBeUndefined();
      expect(chromeMock.sessionStorage[`oc_replay_payload_${start.taskId}`]).toBeUndefined();
      expect(chromeMock.sessionStorage[`oc_replay_ctx_${start.taskId}`]).toBeUndefined();
    });

    it('REPLAY_COMPLETE retains a successful sender tab after a service-worker restart', async () => {
      // No active session — SW restarted mid-run, in-memory state lost.
      chromeMock.sessionStorage.oc_replay_session = {
        tabId: 777,
        ruleId: 'rule-1',
        taskId: 'lost-task',
      };
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success' } },
        { tab: { id: 777 } },
      );
      expect(response).toEqual({ received: true });
      await flushAsync();
      expect(chromeMock.mock.tabs.remove).not.toHaveBeenCalledWith(777);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toEqual(expect.objectContaining({
        tabId: 777,
        taskId: 'lost-task',
      }));
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({ status: 'success' }),
        }),
      );
    });

    it('REPLAY_COMPLETE does not retain an uncorroborated sender tab', async () => {
      const response = await sendRuntimeMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success' } },
        { tab: { id: 778 } },
      );
      expect(response).toEqual({ received: true });
      await flushAsync();
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(778);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toBeUndefined();
    });

    it('total alarm cleans up even when tab removal fails', async () => {
      chromeMock.mock.tabs.remove.mockRejectedValue(new Error('already closed'));
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const response = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as {
        success: boolean;
        taskId: string;
      };
      expect(response.success).toBe(true);
      await flushAsync();
      await flushAsync();
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: `replay_total_${response.taskId}` });
      }
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({ status: 'failure', message: '总时长超限（30min）' }),
        }),
      );
    });

    it('ABORT_REPLAY tolerates sendMessage and tab removal failures', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();

      chromeMock.mock.tabs.sendMessage.mockReset();
      chromeMock.mock.tabs.sendMessage.mockRejectedValueOnce(new Error('dead'));
      chromeMock.mock.tabs.remove.mockRejectedValueOnce(new Error('already closed'));
      const response = (await sendMessage({ action: 'ABORT_REPLAY' })) as { success: boolean };
      expect(response.success).toBe(true);
      expect(chromeMock.mock.tabs.sendMessage).toHaveBeenCalledWith(123, { action: 'ABORT_REPLAY' });
      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(123);
    });
  });

  describe('replay persistence and SW restart recovery', () => {
    it('alarms.onAlarm ignores alarms with unrelated names', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      const removeBefore = chromeMock.mock.tabs.remove.mock.calls.length;
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: 'wizard-keep-alive' });
      }
      expect(chromeMock.mock.tabs.remove.mock.calls.length).toBe(removeBefore);
    });

    it('alarms.onAlarm ignores alarms whose taskId does not match the active session', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      const removeBefore = chromeMock.mock.tabs.remove.mock.calls.length;
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: 'replay_total_00000000-0000-0000-0000-000000000000' });
      }
      expect(chromeMock.mock.tabs.remove.mock.calls.length).toBe(removeBefore);
    });

    it('idle alarm tears down a stuck session but not an active one', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      // Fresh session: idle alarm should be a no-op.
      let removeBefore = chromeMock.mock.tabs.remove.mock.calls.length;
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: `replay_idle_${start.taskId}` });
      }
      expect(chromeMock.mock.tabs.remove.mock.calls.length).toBe(removeBefore);

      // Stale: rewind lastProgressAt and fire idle again.
      const session = chromeMock.sessionStorage.oc_replay_session as { lastProgressAt: number };
      session.lastProgressAt = Date.now() - 200_000;
      await chrome.storage.session.set({ oc_replay_session: session });
      removeBefore = chromeMock.mock.tabs.remove.mock.calls.length;
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: `replay_idle_${start.taskId}` });
      }
      expect(chromeMock.mock.tabs.remove.mock.calls.length).toBe(removeBefore + 1);
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({ status: 'failure', message: '空闲超时（120s 无进度）' }),
        }),
      );
    });

    it('idle alarm tears down a stuck session even when activeReplay is null (SW restart race)', async () => {
      // Simulate post-SW-restart state: storage holds a stale session but
      // activeReplay is null because recovery hasn't completed (or failed).
      // The alarm handler must tear down the orphaned session based on the
      // persisted session alone, not gate on the in-memory activeReplay.
      const taskId = 'restart-race-1';
      const tabId = 999;
      chromeMock.sessionStorage.oc_replay_session = {
        tabId,
        taskId,
        ruleId: 'rule-1',
        startedAt: Date.now() - 300_000,
        hopCount: 0,
        lastProgressAt: Date.now() - 200_000, // stale → idle threshold crossed
        expectedDomains: ['example.com'],
      };

      const removeBefore = chromeMock.mock.tabs.remove.mock.calls.length;
      const alarmClearBefore = chromeMock.mock.alarms.clear.mock.calls.length;
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: `replay_idle_${taskId}` });
      }
      // Tab must be closed and alarms cleared despite activeReplay being null.
      expect(chromeMock.mock.tabs.remove.mock.calls.length).toBe(removeBefore + 1);
      expect(chromeMock.mock.alarms.clear.mock.calls.length).toBeGreaterThan(alarmClearBefore);
      // Storage must be cleared so a subsequent START_REPLAY is not blocked.
      expect(chromeMock.sessionStorage.oc_replay_session).toBeUndefined();
    });

    it('total-alarm tears down a session even when activeReplay is null (SW restart race)', async () => {
      const taskId = 'restart-race-2';
      const tabId = 998;
      chromeMock.sessionStorage.oc_replay_session = {
        tabId,
        taskId,
        ruleId: 'rule-1',
        startedAt: Date.now() - 3_000_000, // exceeds 30min total cap
        hopCount: 0,
        lastProgressAt: Date.now(),
        expectedDomains: ['example.com'],
      };

      const removeBefore = chromeMock.mock.tabs.remove.mock.calls.length;
      for (const listener of chromeMock.onAlarmListeners) {
        await listener({ name: `replay_total_${taskId}` });
      }
      expect(chromeMock.mock.tabs.remove.mock.calls.length).toBe(removeBefore + 1);
      expect(chromeMock.sessionStorage.oc_replay_session).toBeUndefined();
    });

    it('onTabUpdated re-injects the runner via __ocReplayAutoResume on tab complete', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      // Drive the captured onTabUpdated listener with a same-domain complete
      // before any checkpoint context has been persisted.
      const before = chromeMock.mock.scripting.executeScript.mock.calls.length;
      for (const listener of chromeMock.onUpdatedListeners) {
        await listener(123, { status: 'complete' });
      }
      await flushAsync();
      // Two new executeScript calls: files=['intent/replay-runner.js'] + func.
      expect(chromeMock.mock.scripting.executeScript.mock.calls.length).toBeGreaterThanOrEqual(before + 2);
      const newCalls = chromeMock.mock.scripting.executeScript.mock.calls.slice(before) as unknown as Array<
        [{ files?: string[]; func?: Function; args?: unknown[] }]
      >;
      const autoResumeCall = newCalls.find((c) => c[0].args?.length === 2);
      expect(autoResumeCall).toBeDefined();
      expect(autoResumeCall![0].args).toEqual([
        expect.objectContaining({ taskId: start.taskId, rule }),
        null,
      ]);
    });

    it('onTabUpdated enforces SEC-005 post-navigation domain check', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      chromeMock.mock.tabs.get.mockResolvedValueOnce({ id: 123, url: 'https://evil.com/x', status: 'complete' });

      for (const listener of chromeMock.onUpdatedListeners) {
        await listener(123, { status: 'complete' });
      }
      await flushAsync();
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({
            status: 'failure',
            message: expect.stringContaining('post-navigation domain mismatch'),
          }),
        }),
      );
    });

    it('onTabUpdated revisits a canonical alias listed in rule.domain', async () => {
      const rule = makeRule('https://www.example.com/replay', [
        { action: 'navigate', url: 'https://example.com/canonical' },
      ]);
      rule.domain = ['www.example.com', 'example.com'];
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      expect(chromeMock.sessionStorage.oc_replay_session).toMatchObject({
        expectedDomains: ['www.example.com', 'example.com'],
      });

      const before = chromeMock.mock.scripting.executeScript.mock.calls.length;
      chromeMock.mock.tabs.get.mockResolvedValueOnce({
        id: 123,
        url: 'https://example.com/canonical',
        status: 'complete',
      });
      for (const listener of chromeMock.onUpdatedListeners) {
        await listener(123, { status: 'complete' });
      }
      await flushAsync();

      expect(chromeMock.mock.scripting.executeScript.mock.calls.length)
        .toBeGreaterThanOrEqual(before + 2);
      expect(chromeMock.mock.runtime.sendMessage).not.toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({
            status: 'failure',
            message: expect.stringContaining('post-navigation domain mismatch'),
          }),
        }),
      );
    });

    it('onTabUpdated enforces hopCount limit', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      // Accumulate hops by firing complete events — the in-memory session
      // (not just storage) holds hopCount, so we can't short-circuit by
      // editing storage directly. REPLAY_HOP_LIMIT is 50; the boot tab's
      // initial complete event already consumed 1 hop, so 50 more trips
      // the limit (51 > 50).
      for (let i = 0; i < 55; i++) {
        for (const listener of chromeMock.onUpdatedListeners) {
          await listener(123, { status: 'complete' });
        }
        await flushAsync();
      }
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({ status: 'failure', message: '导航跳数超限，疑似重定向循环' }),
        }),
      );
    });

    it('onTabRemoved tears down the session', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();
      for (const listener of chromeMock.onRemovedListeners) {
        await listener(123);
      }
      await flushAsync();
      expect(chromeMock.mock.runtime.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          action: 'REPLAY_COMPLETE',
          payload: expect.objectContaining({ status: 'failure', message: '回放标签页被关闭' }),
        }),
      );
    });

    it('recoverReplaySessionOnStartup restores session, rearms alarms, and re-injects when tab is complete', async () => {
      const seededSession = {
        tabId: 123,
        ruleId: 'rule-1',
        taskId: 'restore-1',
        startedAt: Date.now() - 5_000,
        hopCount: 0,
        lastProgressAt: Date.now(),
        expectedDomain: 'example.com',
      };
      chromeMock.sessionStorage.oc_replay_session = seededSession;
      chromeMock.sessionStorage['oc_replay_payload_restore-1'] = { rule: makeRule(), variables: {}, taskId: 'restore-1', workerId: 'replay-worker' };
      chromeMock.sessionStorage['oc_replay_ctx_restore-1'] = { lastCompletedStepPath: [{ kind: 'top', childIdx: 0 }] };
      chromeMock.mock.tabs.get.mockResolvedValue({ id: 123, url: 'https://example.com/after', status: 'complete' });

      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      await flushAsync();

      // Listeners re-registered post-recovery.
      expect(chromeMock.onUpdatedListeners.length).toBeGreaterThan(0);
      expect(chromeMock.onRemovedListeners.length).toBeGreaterThan(0);
      // Alarms re-armed: total uses REMAINING time, idle is periodic.
      expect(chromeMock.mock.alarms.create).toHaveBeenCalledWith(
        'replay_total_restore-1',
        expect.objectContaining({ delayInMinutes: expect.any(Number) }),
      );
      expect(chromeMock.mock.alarms.create).toHaveBeenCalledWith(
        'replay_idle_restore-1',
        expect.objectContaining({ periodInMinutes: 0.5 }),
      );
      // ADV-007: tab was already complete → immediate re-inject via autoResume.
      expect(chromeMock.mock.scripting.executeScript).toHaveBeenCalled();
      expect(chromeMock.sessionStorage.oc_replay_session).toMatchObject({
        expectedDomains: ['example.com'],
      });
    });

    it('recoverReplaySessionOnStartup clears stale sessions older than the total cap', async () => {
      chromeMock.sessionStorage.oc_replay_session = {
        tabId: 123,
        ruleId: 'rule-1',
        taskId: 'stale-1',
        startedAt: Date.now() - (31 * 60 * 1000),
        hopCount: 0,
        lastProgressAt: Date.now(),
        expectedDomain: 'example.com',
      };
      chromeMock.mock.tabs.get.mockResolvedValue({ id: 123, url: 'https://example.com/', status: 'complete' });

      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      await flushAsync();

      expect(chromeMock.sessionStorage.oc_replay_session).toBeUndefined();
      expect(chromeMock.sessionStorage['oc_replay_payload_stale-1']).toBeUndefined();
      expect(chromeMock.sessionStorage['oc_replay_ctx_stale-1']).toBeUndefined();
      // No listeners registered for a cleared session.
      const state = (await sendMessage({ action: 'GET_STATE' })) as { state: string };
      expect(state.state).toBe('idle');
    });

    it('recoverReplaySessionOnStartup closes a stale retained replay tab (M-4)', async () => {
      // M-4 regression: when the wizard is closed without confirm/abort,
      // the retained tab (kept for the wizard to preview a successful
      // replay) leaks forever. Recovery must release retained tabs whose
      // retention timestamp exceeds the TTL.
      chromeMock.sessionStorage.oc_replay_retained_tab = {
        tabId: 555,
        taskId: 'abandoned-task',
        retainedAt: Date.now() - (45 * 60 * 1000), // 45 min ago, exceeds 30min TTL
      };
      // No active replay session — the replay already completed.
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toBeDefined();

      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      await flushAsync();

      expect(chromeMock.mock.tabs.remove).toHaveBeenCalledWith(555);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toBeUndefined();
    });

    it('recoverReplaySessionOnStartup preserves a fresh retained replay tab (M-4)', async () => {
      // A recently retained tab (user may reconnect to confirm) survives.
      chromeMock.sessionStorage.oc_replay_retained_tab = {
        tabId: 556,
        taskId: 'active-task',
        retainedAt: Date.now() - (5 * 60 * 1000), // 5 min ago, within TTL
      };

      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      await flushAsync();

      expect(chromeMock.mock.tabs.remove).not.toHaveBeenCalledWith(556);
      expect(chromeMock.sessionStorage.oc_replay_retained_tab).toBeDefined();
    });

    it('recoverReplaySessionOnStartup clears an orphaned session when the tab is gone', async () => {
      chromeMock.sessionStorage.oc_replay_session = {
        tabId: 999,
        ruleId: 'rule-1',
        taskId: 'orphan-1',
        startedAt: Date.now() - 5_000,
        hopCount: 0,
        lastProgressAt: Date.now(),
        expectedDomain: 'example.com',
      };
      chromeMock.mock.tabs.get.mockRejectedValueOnce(new Error('No tab with id: 999'));

      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      await flushAsync();

      expect(chromeMock.sessionStorage.oc_replay_session).toBeUndefined();
      expect(chromeMock.sessionStorage['oc_replay_payload_orphan-1']).toBeUndefined();
      expect(chromeMock.sessionStorage['oc_replay_ctx_orphan-1']).toBeUndefined();
    });

    it('recoverReplaySessionOnStartup is idempotent across module-top + onStartup triggers', async () => {
      chromeMock.sessionStorage.oc_replay_session = {
        tabId: 123,
        ruleId: 'rule-1',
        taskId: 'idem-1',
        startedAt: Date.now() - 5_000,
        hopCount: 0,
        lastProgressAt: Date.now(),
        expectedDomain: 'example.com',
      };
      chromeMock.mock.tabs.get.mockResolvedValue({ id: 123, url: 'https://example.com/', status: 'loading' });

      // Only consider the onStartup listener from the freshly re-imported
      // module — prior imports' listeners operate on stale module state.
      chromeMock.onStartupListeners.length = 0;
      vi.resetModules();
      await import('../background');
      await new Promise((resolve) => setTimeout(resolve, 0));
      await flushAsync();

      // Module-top recovery has run; onStartup should no-op via the guards.
      const addListenerCallsBefore = chromeMock.mock.tabs.onUpdated.addListener.mock.calls.length;
      for (const startup of chromeMock.onStartupListeners) {
        await startup();
      }
      await flushAsync();
      expect(chromeMock.mock.tabs.onUpdated.addListener.mock.calls.length).toBe(addListenerCallsBefore);
    });

    it('cleanupReplaySession clears both total and idle alarms', async () => {
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      await sendMessage({ action: 'ABORT_REPLAY' });
      await flushAsync();
      await flushAsync();

      expect(chromeMock.mock.alarms.clear).toHaveBeenCalledWith(`replay_total_${start.taskId}`);
      expect(chromeMock.mock.alarms.clear).toHaveBeenCalledWith(`replay_idle_${start.taskId}`);
    });

    it('cleanupReplaySession invokes server COMPLETE_DSL_REPLAY when wizard is disconnected', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      await sendMessage({ action: 'START_REPLAY', payload: { rule } });
      await flushAsync();
      await flushAsync();

      // Persist a DSL workflow session — the fallback reads from here.
      chromeMock.sessionStorage.oc_dsl_workflow = {
        workflowId: 'wf-1',
        replayId: 'r-1',
        recordingId: 'rec-1',
      };

      // currentWizardPort is null in the test harness (onConnect listeners
      // are never fired by the mock), simulating a disconnected wizard.
      fetchMock.mockResolvedValue({ ok: true, json: async () => ({ success: true }) });

      // Trigger cleanup via ABORT_REPLAY (calls cleanupReplaySession).
      await sendMessage({ action: 'ABORT_REPLAY' });
      await flushAsync();
      await flushAsync();

      const completePosts = fetchMock.mock.calls.filter(
        (call: unknown[]) => {
          const [url, init] = call as [string, RequestInit | undefined];
          return (init?.method ?? 'GET') === 'POST'
            && String(url).includes('/api/v1/dsl-workflows/wf-1/replays')
            && String(url).includes('/complete');
        },
      );
      expect(completePosts.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(completePosts[0][1].body as string);
      expect(body.succeeded).toBe(false);
    });

    it('cleanupReplaySession reports succeeded:true on the success path even when wizard is disconnected', async () => {
      // C-3 regression: a successful replay whose wizard tab has been closed
      // (currentWizardPort === null) must still report succeeded:true to the
      // server's complete endpoint. Previously the WI-6 fallback hardcoded
      // succeeded:false unconditionally, orphaning successful workflows.
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await sendMessage({
        action: 'SET_SERVER_CONFIG',
        payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
      });
      const rule = makeRule('https://example.com/replay', [{ action: 'click', target: { selector: '#btn' } }]);
      const start = (await sendMessage({ action: 'START_REPLAY', payload: { rule } })) as { taskId: string };
      await flushAsync();
      await flushAsync();

      chromeMock.sessionStorage.oc_dsl_workflow = {
        workflowId: 'wf-success',
        replayId: 'r-success',
        recordingId: 'rec-1',
      };
      fetchMock.mockResolvedValue({ ok: true, json: async () => ({ success: true }) });

      // Trigger success cleanup via REPLAY_COMPLETE from the replay tab.
      await sendMessage(
        { action: 'REPLAY_COMPLETE', payload: { status: 'success', taskId: start.taskId } },
        { tab: { id: 123 } },
      );
      await flushAsync();
      await flushAsync();

      const completePosts = fetchMock.mock.calls.filter(
        (call: unknown[]) => {
          const [url, init] = call as [string, RequestInit | undefined];
          return (init?.method ?? 'GET') === 'POST'
            && String(url).includes('/api/v1/dsl-workflows/wf-success/replays')
            && String(url).includes('/complete');
        },
      );
      expect(completePosts.length).toBeGreaterThanOrEqual(1);
      const body = JSON.parse(completePosts[0][1].body as string);
      expect(body.succeeded).toBe(true);
    });
  });

  describe('D-3: RECORDING_CHECKPOINT persists to chrome.storage.local', () => {
    it('writes checkpoint under oc_recording_chk_<tabId> key', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      const recording = makeV2Recording(false);
      await sendMessage({
        action: 'RECORDING_CHECKPOINT',
        payload: {
          recording,
          options: { protocolVersion: '2.0.0' },
          selectorToIndex: [['#a', { index: 1, lastUsedAt: 5 }]],
          nextIndex: 2,
          lastRecordedUrl: 'https://example.com/page',
        },
      }, { tab: { id: 42, url: 'https://example.com/' } });

      const key = 'oc_recording_chk_42';
      expect(chromeMock.localStorage[key]).toBeDefined();
      const chk = chromeMock.localStorage[key] as Record<string, unknown>;
      expect(chk.snapshot).toEqual(recording);
      expect(chk.options).toEqual({ protocolVersion: '2.0.0' });
      expect(chk.selectorToIndex).toEqual([['#a', { index: 1, lastUsedAt: 5 }]]);
      expect(chk.nextIndex).toBe(2);
      expect(chk.lastRecordedUrl).toBe('https://example.com/page');
      expect(chk.tabId).toBe(42);
      expect(typeof chk.savedAt).toBe('number');
      expect(typeof chk.sessionId).toBe('number');
    });

    it('clears oc_recording_chk_<tabId> on tabs.onRemoved', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      const recording = makeV2Recording(false);
      await sendMessage({
        action: 'RECORDING_CHECKPOINT',
        payload: { recording },
      }, { tab: { id: 42, url: 'https://example.com/' } });
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeDefined();

      // Fire the global recording-checkpoint cleanup listener (last registered).
      const cleanup = chromeMock.onRemovedListeners[chromeMock.onRemovedListeners.length - 1];
      await cleanup(42);
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeUndefined();
    });

    it('START_RECORDING clears any residual oc_recording_chk_<tabId> for the new tab', async () => {
      chromeMock.localStorage['oc_recording_chk_42'] = { snapshot: 'stale', savedAt: 1 };
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeUndefined();
    });

    it('CLEAR_RECORDING_CHECKPOINT message removes the .local key', async () => {
      chromeMock.localStorage['oc_recording_chk_42'] = { snapshot: 'stale', savedAt: 1 };
      await sendMessage({
        action: 'CLEAR_RECORDING_CHECKPOINT',
        payload: { tabId: 42 },
      }, { tab: { id: 42, url: 'https://example.com/' } });
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeUndefined();
    });

    it('CLEAR_RECORDING_CHECKPOINT ignores payload.tabId and only clears the sender own tab (H-1)', async () => {
      // H-1 regression: a content script must not be able to clear another
      // tab's disk-backed recording checkpoint by passing a crafted payload.tabId.
      chromeMock.localStorage['oc_recording_chk_42'] = { snapshot: 'belongs-to-42', savedAt: 1 };
      chromeMock.localStorage['oc_recording_chk_7'] = { snapshot: 'belongs-to-7', savedAt: 1 };
      await sendMessage({
        action: 'CLEAR_RECORDING_CHECKPOINT',
        payload: { tabId: 42 }, // attacker tries to clear tab 42
      }, { tab: { id: 7, url: 'https://attacker.example/' } });
      // tab 42's checkpoint survives; only sender's own (tab 7) is cleared.
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeDefined();
      expect(chromeMock.localStorage['oc_recording_chk_7']).toBeUndefined();
    });

    it('STOP_RECORDING clears the .local checkpoint after final persist', async () => {
      const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
      await configureRecordingV2(fetchMock);
      await sendMessage({ action: 'START_RECORDING' });
      const recording = makeV2Recording(false);
      await sendMessage({
        action: 'RECORDING_CHECKPOINT',
        payload: { recording },
      }, { tab: { id: 42, url: 'https://example.com/' } });
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeDefined();

      // Override the mock to return a v2 recording for STOP_RECORDING so the
      // protocol-version gate passes and the cleanup path executes.
      const completed = makeV2Recording(true);
      chromeMock.mock.tabs.sendMessage.mockResolvedValueOnce({ recording: completed, success: true });

      await sendMessage({ action: 'STOP_RECORDING' });
      expect(chromeMock.localStorage['oc_recording_chk_42']).toBeUndefined();
    });

    it('does not block the handler when chrome.storage.local.set rejects (quota exceeded)', async () => {
      const originalSet = chromeMock.mock.storage.local.set;
      chromeMock.mock.storage.local.set = vi.fn(() =>
        Promise.reject(new Error('QUOTA_BYTES quota exceeded')),
      );
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
      try {
        const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
        await configureRecordingV2(fetchMock);
        await sendMessage({ action: 'START_RECORDING' });
        const recording = makeV2Recording(false);
        const response = await sendMessage({
          action: 'RECORDING_CHECKPOINT',
          payload: { recording },
        }, { tab: { id: 42, url: 'https://example.com/' } });

        // 1. Handler must NOT propagate the rejection.
        expect(response).toEqual({ success: true });
        // 2. The best-effort .catch() should have logged a warning.
        expect(warnSpy).toHaveBeenCalledWith(
          expect.stringMatching(/chrome\.storage\.local write failed/),
        );
        // 3. The IndexedDB + .session writes that precede the .local write
        //    still happened.
        expect(chromeMock.sessionStorage.oc_recording_session).toBeDefined();
      } finally {
        chromeMock.mock.storage.local.set = originalSet;
        warnSpy.mockRestore();
      }
    });
  });
});


describe('createProgressDeadline', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function loadDeadline(): Promise<typeof import('../background').createProgressDeadline> {
    (globalThis as Record<string, unknown>).chrome = createChromeMock().mock;
    global.fetch = vi.fn();
    vi.resetModules();
    const mod = await import('../background');
    return mod.createProgressDeadline;
  }

  it('expires after the base window when no progress is observed', async () => {
    const createProgressDeadline = await loadDeadline();
    const deadline = createProgressDeadline(1000, 5000);
    expect(deadline.expired()).toBe(false);
    vi.advanceTimersByTime(1000);
    expect(deadline.expired()).toBe(true);
  });

  it('extends by the stall window on the first started report and on forward progress', async () => {
    const createProgressDeadline = await loadDeadline();
    const deadline = createProgressDeadline(1000, 5000);
    vi.advanceTimersByTime(900);
    deadline.observe(0);
    vi.advanceTimersByTime(4999);
    expect(deadline.expired()).toBe(false);
    vi.advanceTimersByTime(1);
    expect(deadline.expired()).toBe(true);
  });

  it('extends again on each forward step', async () => {
    const createProgressDeadline = await loadDeadline();
    const deadline = createProgressDeadline(1000, 5000);
    deadline.observe(0);
    vi.advanceTimersByTime(4000);
    deadline.observe(1);
    vi.advanceTimersByTime(4999);
    expect(deadline.expired()).toBe(false);
    vi.advanceTimersByTime(1);
    expect(deadline.expired()).toBe(true);
  });

  it('does not extend for repeated or regressed progress', async () => {
    const createProgressDeadline = await loadDeadline();
    const deadline = createProgressDeadline(1000, 5000);
    deadline.observe(3);
    vi.advanceTimersByTime(4900);
    deadline.observe(3);
    deadline.observe(2);
    vi.advanceTimersByTime(100);
    expect(deadline.expired()).toBe(true);
  });

  it('never extends past the absolute cap regardless of forward progress (M-3)', async () => {
    // M-3 regression: without an absolute cap, a server that increments
    // completedChunks every <stallTimeoutMs keeps pushing the deadline
    // forward indefinitely. The third arg enforces a hard ceiling.
    const createProgressDeadline = await loadDeadline();
    const deadline = createProgressDeadline(1000, 5000, 10_000);
    // Slow increment: observe(0) at t=4s, observe(1) at t=8s — each within
    // the 5s stall window, so without the cap the deadline would never fire.
    vi.advanceTimersByTime(4000);
    deadline.observe(0);
    vi.advanceTimersByTime(4000);
    deadline.observe(1);
    expect(deadline.expired()).toBe(false); // still within 10s absolute cap
    vi.advanceTimersByTime(3000); // total elapsed = 11s, past the 10s cap
    expect(deadline.expired()).toBe(true);
  });
});

describe('requirementRequest server error code propagation', () => {
  let chromeMock: ReturnType<typeof createChromeMock>;

  beforeEach(async () => {
    chromeMock = createChromeMock();
    (globalThis as Record<string, unknown>).chrome = chromeMock.mock;
    global.fetch = vi.fn();
    vi.resetModules();
    await import('../background');
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  afterEach(async () => {
    delete (globalThis as Record<string, unknown>).chrome;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function sendMessage(message: unknown): Promise<unknown> {
    return new Promise((resolve) => {
      const sendResponse = vi.fn((response) => resolve(response));
      chromeMock.listeners[0](message, {}, sendResponse);
    });
  }

  it('surfaces the server code field in CONFIRM_DSL_WORKFLOW response on non-2xx', async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
    await sendMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
    });

    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 409,
      json: async () => ({ error: 'safety blocking', code: 'SAFETY_BLOCKING_FLAG' }),
    });

    await expect(sendMessage({
      action: 'CONFIRM_DSL_WORKFLOW', payload: { workflowId: 'wf-1' },
    })).resolves.toMatchObject({
      success: false,
      code: 'SAFETY_BLOCKING_FLAG',
    });
  });

  it('propagates blocking_flags from SAFETY_BLOCKING_FLAG 409 through CONFIRM_DSL_WORKFLOW', async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
    await sendMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
    });

    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 409,
      json: async () => ({
        error: 'dsl workflow has blocking safety flags requiring override',
        code: 'SAFETY_BLOCKING_FLAG',
        blocking_flags: ['external-resource-load', 'script-tag-in-selector'],
      }),
    });

    // Full path: fetch → requirementRequest → ServerRequestError → handler return shape.
    await expect(sendMessage({
      action: 'CONFIRM_DSL_WORKFLOW', payload: { workflowId: 'wf-1' },
    })).resolves.toMatchObject({
      success: false,
      code: 'SAFETY_BLOCKING_FLAG',
      blocking_flags: ['external-resource-load', 'script-tag-in-selector'],
    });
  });

  it('surfaces the server code field in COMPLETE_DSL_REPLAY response on non-2xx', async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
    await sendMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
    });

    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 409,
      json: async () => ({ error: 'invalid state', code: 'INVALID_STATE' }),
    });

    await expect(sendMessage({
      action: 'COMPLETE_DSL_REPLAY',
      payload: { workflowId: 'wf-1', replayId: 'r-1', succeeded: false },
    })).resolves.toMatchObject({
      success: false,
      code: 'INVALID_STATE',
    });
  });

  it('omits code when the thrown error is not a ServerRequestError', async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
    await sendMessage({
      action: 'SET_SERVER_CONFIG',
      payload: { baseUrl: 'http://localhost:8080', adminApiKey: 'admin-secret' },
    });

    // fetch rejects with a plain TypeError (network failure) — not ServerRequestError.
    fetchMock.mockRejectedValueOnce(new TypeError('network down'));

    await expect(sendMessage({
      action: 'CONFIRM_DSL_WORKFLOW', payload: { workflowId: 'wf-1' },
    })).resolves.toMatchObject({
      success: false,
      error: 'network down',
    });
    // No `code` key should be present.
    const response = await (async () => {
      fetchMock.mockRejectedValueOnce(new TypeError('network down'));
      return sendMessage({
        action: 'CONFIRM_DSL_WORKFLOW', payload: { workflowId: 'wf-2' },
      });
    })();
    expect(response).not.toHaveProperty('code');
  });
});
