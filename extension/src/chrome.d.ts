interface ChromeStorageArea {
  get(keys?: string | string[] | Record<string, unknown> | null): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
  remove(keys: string | string[]): Promise<void>;
  clear(): Promise<void>;
}

interface ChromeStorage {
  local: ChromeStorageArea;
  session: ChromeStorageArea;
}

interface ChromeTab {
  id?: number;
  url?: string;
  status?: string;
}

interface ChromeWebNavigationFrame {
  frameId: number;
  parentFrameId: number;
  url: string;
}

interface ChromeWebNavigation {
  getAllFrames(details: { tabId: number }): Promise<ChromeWebNavigationFrame[]>;
}

interface ChromeAlarm {
  name: string;
  scheduledTime: number;
  periodInMinutes?: number;
}

interface ChromeAlarmsCreateInfo {
  when?: number;
  delayInMinutes?: number;
  periodInMinutes?: number;
}

interface ChromeAlarms {
  create(name: string, alarmInfo: ChromeAlarmsCreateInfo): Promise<void>;
  clear(name: string): Promise<void>;
  onAlarm: {
    addListener(callback: (alarm: ChromeAlarm) => void | Promise<void>): void;
  };
}

interface ChromeTabChangeInfo {
  status?: string;
}

interface ChromeTabs {
  query(queryInfo: { active?: boolean; currentWindow?: boolean }): Promise<ChromeTab[]>;
  get(tabId: number): Promise<ChromeTab>;
  sendMessage(tabId: number, message: unknown, options?: { frameId?: number }): Promise<unknown>;
  create(createProperties: { url?: string; active?: boolean }): Promise<ChromeTab>;
  remove(tabId: number): Promise<void>;
  onUpdated: {
    addListener(callback: (tabId: number, changeInfo: ChromeTabChangeInfo, tab?: ChromeTab) => void): void;
    removeListener(callback: (tabId: number, changeInfo: ChromeTabChangeInfo, tab?: ChromeTab) => void): void;
  };
  onRemoved: {
    addListener(callback: (tabId: number, removeInfo: { windowId: number; isWindowClosing: boolean }) => void): void;
    removeListener(callback: (tabId: number, removeInfo: { windowId: number; isWindowClosing: boolean }) => void): void;
  };
}

interface ChromeScripting {
  executeScript(injection: {
    target: { tabId: number };
    files?: string[];
    func?: Function;
    args?: any[];
  }): Promise<unknown[]>;
}

interface ChromeDownloads {
  download(options: { url: string; filename?: string; saveAs?: boolean }): Promise<number>;
}

interface ChromeRuntimeMessageSender {
  tab?: ChromeTab;
  url?: string;
  origin?: string;
  frameId?: number;
}

interface ChromeRuntimePort {
  name?: string;
  postMessage(message: unknown): void;
  disconnect(): void;
  onMessage: {
    addListener(callback: (message: unknown) => void): void;
    removeListener(callback: (message: unknown) => void): void;
  };
  onDisconnect: {
    addListener(callback: (port: ChromeRuntimePort) => void): void;
    removeListener(callback: (port: ChromeRuntimePort) => void): void;
  };
}

interface ChromeRuntime {
  sendMessage(message: unknown): Promise<unknown>;
  getURL(path: string): string;
  connect(connectInfo?: { name?: string }): ChromeRuntimePort;
  onMessage: {
    addListener(
      callback: (message: unknown, sender: ChromeRuntimeMessageSender, sendResponse: (response?: unknown) => void) => boolean | void,
    ): void;
    removeListener(
      callback: (message: unknown, sender: ChromeRuntimeMessageSender, sendResponse: (response?: unknown) => void) => boolean | void,
    ): void;
  };
  onConnect: {
    addListener(callback: (port: ChromeRuntimePort) => void): void;
  };
  onStartup: {
    addListener(callback: () => void | Promise<void>): void;
  };
}

interface ChromeAction {
  setBadgeText(details: { text: string; tabId?: number }): Promise<void>;
  setBadgeBackgroundColor(details: { color: string | [number, number, number, number]; tabId?: number }): Promise<void>;
}

interface Chrome {
  storage: ChromeStorage;
  tabs: ChromeTabs;
  scripting: ChromeScripting;
  downloads: ChromeDownloads;
  runtime: ChromeRuntime;
  webNavigation: ChromeWebNavigation;
  alarms: ChromeAlarms;
  action?: ChromeAction;
}

declare const chrome: Chrome;
