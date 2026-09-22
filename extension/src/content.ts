import {
  PageAgentRecorder,
  PageControllerLike,
  RecorderOptions,
} from '../../src/rule-generator/recorder/PageAgentRecorder';
import {
  captureLocalDomWithReport,
  collectFrameReports,
  mergeFrameTreeIntoDom,
  requestAggregateFromBackground,
  type AggregateOptions,
  type FrameTreeNode,
} from './recording/frame-aggregator';
import {
  isSensitiveElement,
  redactSensitiveText,
  sanitizeRecordingUrl,
  type SerializationReport,
} from './recording/dom-serializer';
import type {
  PageAgentRecording,
  PageAgentEvent,
  DomElementInfo,
  DomNode,
  DomSnapshot,
  PageMark,
  PageMarkRole,
  RecordingLimitKind,
  RecordingStopReason,
} from '../../src/rule-generator/types';
import { effectiveRole } from '../../src/rule-engine/aria-roles';
import { PageMarkOverlay } from './marking/overlay';
import type { PageMarkOverlayAdapter } from './marking/overlay';
import { RecordingHud } from './recording/hud';

export type { RecorderOptions };

const RESTRICTED_SCHEMES = [
  'chrome://',
  'devtools://',
  'chrome-extension://',
  'moz-extension://',
  'edge://',
  'about:',
];

function isRestrictedUrl(url: string): boolean {
  return RESTRICTED_SCHEMES.some((scheme) => url.startsWith(scheme));
}

const IS_TOP_FRAME = window.top === window.self;

function escapeIdentifier(value: string): string {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') {
    return CSS.escape(value);
  }
  return cssEscapeFallback(value);
}

function cssEscapeFallback(value: string): string {
  const codePoint = (char: string) => char.charCodeAt(0).toString(16).toUpperCase().padStart(2, '0');
  let result = '';

  for (let i = 0; i < value.length; i++) {
    const char = value[i];
    const code = char.charCodeAt(0);

    if (code === 0x0000) {
      result += '\uFFFD';
      continue;
    }

    if ((code >= 0x0001 && code <= 0x001f) || code === 0x007f) {
      result += `\\${codePoint(char)} `;
      continue;
    }

    if (i === 0 && code >= 0x0030 && code <= 0x0039) {
      result += `\\${codePoint(char)} `;
      continue;
    }

    if (i === 0 && code === 0x002d && value.length === 1) {
      result += `\\${codePoint(char)} `;
      continue;
    }

    if (i === 1 && code >= 0x0030 && code <= 0x0039 && value.charCodeAt(0) === 0x002d) {
      result += `\\${codePoint(char)} `;
      continue;
    }

    if (
      code >= 0x0080 ||
      code === 0x002d ||
      code === 0x005f ||
      (code >= 0x0030 && code <= 0x0039) ||
      (code >= 0x0041 && code <= 0x005a) ||
      (code >= 0x0061 && code <= 0x007a)
    ) {
      result += char;
      continue;
    }

    result += `\\${codePoint(char)} `;
  }

  return result;
}

function createEmptyRecording(options: RecorderOptions = {}): PageAgentRecording {
  const version = options.protocolVersion ?? '1.0.0';
  const startedAt = new Date();
  const recording: PageAgentRecording = {
    version,
    meta: {
      startUrl: typeof window !== 'undefined' ? sanitizeRecordingUrl(window.location.href) : '',
      title: typeof document !== 'undefined' ? redactSensitiveText(document.title).value : '',
      recordedAt: startedAt.toISOString(),
      domain: typeof window !== 'undefined' ? window.location.hostname : '',
      userAgent: typeof navigator !== 'undefined' ? navigator.userAgent : undefined,
    },
    events: [],
    snapshots: [],
  };
  if (version === '2.0.0') {
    recording.marks = [];
    recording.meta.semanticDomVersion = '1';
    recording.meta.sanitizationVersion = 'extension-v2';
    recording.limits = {
      maxActions: options.maxEvents ?? 500,
      maxDurationMs: options.maxDurationMs ?? 2 * 60 * 60 * 1000,
      maxBytes: options.maxRecordingBytes ?? 20 * 1024 * 1024,
      warningThreshold: options.warningThreshold ?? 0.8,
    };
    recording.warnings = [];
  }
  return recording;
}

function isPageControllerLike(value: unknown): value is PageControllerLike {
  const controller = value as Partial<PageControllerLike> | null | undefined;
  if (!controller) {
    return false;
  }
  const requiredMethods: Array<keyof PageControllerLike> = [
    'clickElement',
    'inputText',
    'selectOption',
    'scroll',
    'scrollHorizontally',
    'executeJavascript',
  ];
  return requiredMethods.every((method) => typeof controller[method] === 'function');
}

function findPageController(): PageControllerLike | null {
  const win = window as unknown as Record<string, unknown>;
  if (isPageControllerLike(win.PageController)) {
    return win.PageController;
  }
  if (isPageControllerLike(win.__pageAgentController)) {
    return win.__pageAgentController as PageControllerLike;
  }
  return null;
}

interface ListenerEntry {
  target: EventTarget;
  event: string;
  handler: EventListener;
  options?: AddEventListenerOptions;
}

/**
 * Content-script recorder that produces a PageAgentRecording.
 *
 * It prefers a PageController-like object when available (proxy mode) and
 * falls back to DOM event listeners otherwise.
 */
const SESSION_STORAGE_KEY = '__ocRecordingState';
const DEFAULT_MAX_MARKS = 24;
const MAX_MARK_NOTE_CHARS = 200;

interface PersistedRecordingState {
  version: number;
  recordingFlag: boolean;
  recording: PageAgentRecording;
  options?: RecorderOptions;
  selectorToIndex: Array<[string, { index: number; lastUsedAt: number }]>;
  nextIndex: number;
  lastRecordedUrl: string | null;
}

// D-3: shape written to chrome.storage.local by the RECORDING_CHECKPOINT
// handler. Used as the response.localCheckpoint field on RESUME_RECORDING
// fallback.
interface RecordingLocalCheckpoint {
  snapshot: PageAgentRecording;
  options?: Record<string, unknown>;
  selectorToIndex?: Array<[string, { index: number; lastUsedAt: number }]>;
  nextIndex?: number;
  lastRecordedUrl?: string | null;
  savedAt?: number;
  sessionId?: number;
}

export class ContentRecorder {
  private recording: PageAgentRecording | null = null;
  private options: RecorderOptions;
  private controllerRecorder: PageAgentRecorder | null = null;
  private listeners: ListenerEntry[] = [];
  private selectorToIndex = new Map<string, { index: number; lastUsedAt: number }>();
  private nextIndex = 1;
  private scrollTimeouts = new Map<Element, ReturnType<typeof setTimeout>>();
  private scrollState = new WeakMap<Element, { top: number; left: number }>();
  private inputTimeouts = new Map<Element, ReturnType<typeof setTimeout>>();
  private originalPushState: typeof history.pushState | null = null;
  private originalReplaceState: typeof history.replaceState | null = null;
  private messageHandler: ((message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean) | null = null;
  private recordingFlag = false;
  private eventCount = 0;
  private snapshotCount = 0;
  private selectorCleanupInterval: ReturnType<typeof setInterval> | null = null;
  private lastRecording: PageAgentRecording | null = null;
  private eventQueue: Promise<void> = Promise.resolve();
  private lastEnterSubmit: { form: HTMLFormElement; time: number } | null = null;
  private lastClickedSubmitter: { element: Element; time: number } | null = null;
  private lastRecordedUrl: string | null = null;
  private snapshotSequence = 0;
  private startedAtMs = 0;
  private durationLimitTimer: ReturnType<typeof setTimeout> | null = null;
  private warnedLimits = new Set<RecordingLimitKind>();
  private limitStopRequested = false;
  private stopping = false;
  private stopPromise: Promise<PageAgentRecording> | null = null;
  private backgroundResumePromise: Promise<boolean> | null = null;
  private markOverlay: PageMarkOverlay | null = null;
  private recordingHud: RecordingHud | null = null;
  /**
   * Canonical content strings of the most recent full (non-reference) v2
   * snapshot, used to collapse identical adjacent snapshots into references.
   * Heavy pages can carry ~1.5 MB per snapshot; consecutive actions on an
   * unchanged page otherwise duplicate that payload for every event.
   */
  private lastContentSnapshot: {
    sequence: number;
    url: string;
    selectorMap: string;
    domTree: string;
    capture: string;
  } | null = null;
  // H-1: incremental byte-length tracking. Maintained per-push to avoid the
  // O(n²) cost of JSON.stringify(this.recording) on every event. Recalibrated
  // to the exact full-serialization value at checkpoint intervals.
  private currentByteLength = 0;
  // D-3: throttle checkpoint sends so we don't spam the SW on every event.
  // Forced flush on beforeunload / explicit stop bypasses the throttle.
  private static readonly CHECKPOINT_MIN_INTERVAL_MS = 5000;
  private static readonly CHECKPOINT_EVENT_INTERVAL = 20;
  private lastCheckpointAt = 0;
  private eventsSinceCheckpoint = 0;

  constructor(options: RecorderOptions = {}) {
    this.options = {
      captureSnapshotBeforeEachEvent: true,
      maxSnapshots: 200,
      selectorIndexTTLMs: 30000,
      ...options,
    };
  }

  private isV2(): boolean {
    return this.recording?.version === '2.0.0' || this.options.protocolVersion === '2.0.0';
  }

  start(options?: RecorderOptions): void {
    if (!IS_TOP_FRAME) {
      return;
    }

    if (this.recordingFlag) {
      return;
    }

    if (options) {
      this.options = { ...this.options, ...options };
    }

    if (isRestrictedUrl(window.location.href)) {
      console.warn('[ContentRecorder] recording is disabled on restricted URLs');
      return;
    }

    this.recording = createEmptyRecording(this.options);
    this.selectorToIndex.clear();
    this.nextIndex = 1;
    this.eventCount = 0;
    this.snapshotCount = 0;
    this.lastRecording = null;
    this.eventQueue = Promise.resolve();
    this.lastEnterSubmit = null;
    this.lastClickedSubmitter = null;
    // The page is already open when recording starts. Seed its URL so a late
    // pageshow for that same document is not misclassified as a user
    // navigation; genuine hash/history/full-page changes still differ and are
    // recorded by handleNavigate.
    this.lastRecordedUrl = sanitizeRecordingUrl(window.location.href);
    this.snapshotSequence = 0;
    this.startedAtMs = Date.now();
    this.warnedLimits.clear();
    this.limitStopRequested = false;
    this.stopping = false;
    this.stopPromise = null;
    this.recordingFlag = true;

    this.clearPersistedState();

    const controller = this.isV2() ? null : findPageController();
    if (controller) {
      this.controllerRecorder = new PageAgentRecorder(controller, this.options);
      this.recording = this.controllerRecorder.getRecording();
    } else {
      this.attachDomListeners();
    }

    this.attachNavigationListeners();
    this.wrapRecordingArrays(this.recording);
    this.attachMarkOverlay();
    this.attachRecordingHud();
    // H-1: initialize incremental byte counter.
    this.recalibrateByteLength();
    this.startSelectorCleanup();
    if (this.isV2()) {
      this.eventQueue = this.eventQueue.then(async () => {
        if (this.recordingFlag) {
          await this.captureSnapshot(Date.now(), 'initial');
          this.persistState();
        }
      });
      const maxDurationMs = this.recording.limits?.maxDurationMs ?? 0;
      if (maxDurationMs > 0) {
        this.durationLimitTimer = setTimeout(() => {
          this.requestLimitStop('duration-limit', `maximum duration of ${maxDurationMs}ms reached`);
        }, maxDurationMs);
      }
    }
  }

  stop(): PageAgentRecording {
    if (!this.recordingFlag || !this.recording) {
      // If this content-script instance was created after a full-page navigation,
      // the recording lives in sessionStorage. Resume it so we can return the
      // complete recording to the caller.
      this.resumeIfNeeded();
    }

    if (!this.recordingFlag || !this.recording) {
      this.clearPersistedState();
      return this.lastRecording ?? createEmptyRecording(this.options);
    }

    // Merged cross-origin state can carry a stale 'final' snapshot that is
    // not last; drop any such snapshot so the required phase ordering
    // (initial ... final-last) always holds after an explicit stop. Filter
    // in place: reassigning the array would silently drop the wrapped push
    // installed by wrapRecordingArrays (v2 identical-snapshot dedup and the
    // v1 FIFO bound both live there).
    if (this.isV2()) {
      this.removeFinalSnapshotsInPlace();
      this.recording.snapshots.push(this.captureLocalSnapshot(Date.now(), 'final'));
    }

    return this.finalizeStop('user', 'recording stopped by user', true);
  }

  async stopAsync(
    reason: RecordingStopReason = 'user',
    message = 'recording stopped by user',
    complete = true,
  ): Promise<PageAgentRecording> {
    if ((!this.recordingFlag || !this.recording) && !this.resumeIfNeeded()) {
      await this.resumeFromBackgroundIfNeeded();
    }
    if (!this.recording || !this.recordingFlag) {
      return this.lastRecording ?? createEmptyRecording(this.options);
    }
    if (!this.isV2()) return this.stop();
    if (this.stopPromise) return this.stopPromise;
    this.stopPromise = (async () => {
      this.stopping = true;
      this.flushInputTimeouts();
      this.flushScrollTimeouts();
      await this.eventQueue;
      try {
        // Replace any stale merged 'final' so it is always the last
        // snapshot — in place, so the wrapped push (identical-snapshot
        // dedup) installed at start keeps working for the new final push.
        this.removeFinalSnapshotsInPlace();
        await this.captureSnapshot(Date.now(), 'final');
      } catch (error) {
        reason = 'capture-error';
        message = error instanceof Error ? error.message : 'final snapshot capture failed';
        complete = false;
      }
      return this.finalizeStop(reason, message, complete);
    })();
    try {
      return await this.stopPromise;
    } finally {
      this.stopPromise = null;
    }
  }

  /** Remove existing 'final' snapshots from the live array without replacing
   *  it — wrapRecordingArrays installs the dedup/FIFO behavior on the array
   *  object itself, and a reassignment would silently discard it. */
  private removeFinalSnapshotsInPlace(): void {
    if (!this.recording) return;
    for (let index = this.recording.snapshots.length - 1; index >= 0; index -= 1) {
      if (this.recording.snapshots[index]?.phase === 'final') {
        const removed = this.recording.snapshots.splice(index, 1);
        for (const snapshot of removed) {
          this.currentByteLength -= ContentRecorder.computeItemByteLength(snapshot);
        }
      }
    }
  }

  private finalizeStop(
    reason: RecordingStopReason,
    message: string,
    complete: boolean,
  ): PageAgentRecording {
    if (!this.recording) return this.lastRecording ?? createEmptyRecording(this.options);

    this.recordingFlag = false;

    if (this.controllerRecorder) {
      this.controllerRecorder.detach();
      this.recording = this.controllerRecorder.getRecording();
      this.controllerRecorder = null;
    }

    this.markOverlay?.unmount();
    this.markOverlay = null;
    this.recordingHud?.unmount();
    this.recordingHud = null;

    this.detachAllListeners();

    for (const timeout of this.scrollTimeouts.values()) {
      clearTimeout(timeout);
    }
    this.scrollTimeouts.clear();

    for (const timeout of this.inputTimeouts.values()) {
      clearTimeout(timeout);
    }
    this.inputTimeouts.clear();

    this.lastEnterSubmit = null;
    this.lastClickedSubmitter = null;

    this.stopSelectorCleanup();
    if (this.durationLimitTimer) {
      clearTimeout(this.durationLimitTimer);
      this.durationLimitTimer = null;
    }

    if (this.recording.version === '2.0.0') {
      this.recording.snapshots.sort(
        (left, right) => (left.sequence ?? Number.MAX_SAFE_INTEGER) - (right.sequence ?? Number.MAX_SAFE_INTEGER),
      );
      const endedAt = new Date();
      this.recording.meta.endedAt = endedAt.toISOString();
      this.recording.termination = {
        reason,
        message,
        timestamp: endedAt.getTime(),
        complete,
      };
    }

    // Clone the result so that any late async tasks still touching the old
    // in-memory recording cannot mutate the returned recording.
    const capped = this.recording.version === '2.0.0' ? this.recording : this.capRecordingSize(this.recording);
    const result = typeof structuredClone === 'function'
      ? structuredClone(capped)
      : (JSON.parse(JSON.stringify(capped)) as PageAgentRecording);
    this.recording = null;
    this.eventCount = 0;
    this.snapshotCount = 0;
    this.lastRecording = result;
    this.stopping = false;
    this.clearPersistedState();
    return result;
  }

  private clearPersistedState(): void {
    try {
      sessionStorage.removeItem(SESSION_STORAGE_KEY);
    } catch {
      // sessionStorage may be unavailable in some sandboxed contexts.
    }
  }

  private persistState(forceCheckpoint = false): void {
    if (!this.recording || !this.recordingFlag || this.options.disableCrossPagePersistence) {
      return;
    }
    // sessionStorage is quota-bound (~5-10MB) and unlimitedStorage cannot
    // extend it. For a v2 recording past that budget the full-state write
    // ALWAYS throws after serializing tens of megabytes on the main thread —
    // pure jank plus warning spam. Skip it and drop any stale smaller entry
    // so a later navigation resumes from the authoritative background
    // checkpoint (chrome.storage.local) instead of outdated page state.
    const exceedsSessionBudget = this.isV2() && this.currentByteLength > 4 * 1024 * 1024;
    if (exceedsSessionBudget) {
      try {
        sessionStorage.removeItem(SESSION_STORAGE_KEY);
      } catch {
        // A hostile or locked storage surface: the background checkpoint
        // remains the durable fallback.
      }
    } else {
      try {
        const state: PersistedRecordingState = {
          version: 1,
          recordingFlag: this.recordingFlag,
          recording: this.recording,
          options: this.options,
          selectorToIndex: Array.from(this.selectorToIndex.entries()),
          nextIndex: this.nextIndex,
          lastRecordedUrl: this.lastRecordedUrl,
        };
        const serialized = JSON.stringify(state);
        if (serialized.length > 4 * 1024 * 1024 && !this.isV2()) {
          // Drop heavy domTrees before writing to sessionStorage so we stay under
          // the typical 5-10 MB quota without losing events/selectors.
          const cappedRecording = this.capRecordingSize(this.recording);
          sessionStorage.setItem(
            SESSION_STORAGE_KEY,
            JSON.stringify({ ...state, recording: cappedRecording }),
          );
        } else {
          sessionStorage.setItem(SESSION_STORAGE_KEY, serialized);
        }
      } catch (err) {
        console.warn('[ContentRecorder] failed to persist recording state:', err);
      }
    }
    if (this.isV2()) this.checkpointToBackground(forceCheckpoint);
  }

  private async exportRecordingHandoff(): Promise<PersistedRecordingState | null> {
    if (!this.recordingFlag || !this.recording || !this.isV2()) return null;
    this.flushInputTimeouts();
    this.flushScrollTimeouts();
    await this.eventQueue;
    return {
      version: 1,
      recordingFlag: true,
      recording: this.recording,
      options: this.options,
      selectorToIndex: Array.from(this.selectorToIndex.entries()),
      nextIndex: this.nextIndex,
      lastRecordedUrl: this.lastRecordedUrl,
    };
  }

  private discardRecordingHandoff(): void {
    this.recordingFlag = false;
    this.detachAllListeners();
    for (const timeout of this.scrollTimeouts.values()) clearTimeout(timeout);
    for (const timeout of this.inputTimeouts.values()) clearTimeout(timeout);
    this.scrollTimeouts.clear();
    this.inputTimeouts.clear();
    this.stopSelectorCleanup();
    if (this.durationLimitTimer) clearTimeout(this.durationLimitTimer);
    this.durationLimitTimer = null;
    this.recording = null;
    this.eventQueue = Promise.resolve();
    this.clearPersistedState();
  }

  private async importRecordingHandoff(state: PersistedRecordingState): Promise<PersistedRecordingState | null> {
    if (!state?.recording || state.recording.version !== '2.0.0' || state.recording.termination) return null;
    if (this.recordingFlag) this.discardRecordingHandoff();
    if (!this.restorePersistedState(state)) return null;
    this.handleNavigate();
    await this.eventQueue;
    return this.exportRecordingHandoff();
  }

  private checkpointToBackground(force = false): void {
    if (!this.recording || !this.recordingFlag) return;
    this.eventsSinceCheckpoint += 1;
    const now = Date.now();
    const due = force
      || this.eventsSinceCheckpoint >= ContentRecorder.CHECKPOINT_EVENT_INTERVAL
      || now - this.lastCheckpointAt >= ContentRecorder.CHECKPOINT_MIN_INTERVAL_MS;
    if (!due) return;
    this.lastCheckpointAt = now;
    this.eventsSinceCheckpoint = 0;
    // H-1: recalibrate the incremental byte counter at checkpoint intervals
    // to correct drift from per-push approximation.
    this.recalibrateByteLength();
    const runtime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { sendMessage?: (message: unknown) => Promise<unknown> } }
      | undefined;
    runtime?.runtime?.sendMessage?.({
      action: 'RECORDING_CHECKPOINT',
      payload: {
        recording: this.recording,
        options: this.options,
        selectorToIndex: Array.from(this.selectorToIndex.entries()),
        nextIndex: this.nextIndex,
        lastRecordedUrl: this.lastRecordedUrl,
      },
    }).catch((err: unknown) => {
      // M-1: surface checkpoint failures in diagnostics. The throttled
      // checkpoint loop naturally retries on the next event/interval.
      console.warn('[ContentRecorder] checkpoint to background failed; will retry on next interval:', err);
    });
  }

  private notifyRecordingStatus(
    status: 'warning' | 'stopped',
    message: string,
    recording?: PageAgentRecording,
  ): void {
    const runtime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { sendMessage?: (message: unknown) => Promise<unknown> } }
      | undefined;
    runtime?.runtime?.sendMessage?.({
      action: 'RECORDING_STATUS',
      payload: { status, message, recording },
    }).catch(() => undefined);
  }

  private recordingByteLength(): number {
    if (!this.recording) return 0;
    return new TextEncoder().encode(JSON.stringify(this.recording)).byteLength;
  }

  // H-1: recalibrate the incremental byte counter from a full serialization.
  // Called at checkpoint intervals to correct drift from incremental tracking.
  private recalibrateByteLength(): void {
    this.currentByteLength = this.recordingByteLength();
  }

  private static computeItemByteLength(item: unknown): number {
    // +2 for the JSON array separator `, ` or surrounding `[` `]`.
    return new TextEncoder().encode(JSON.stringify(item)).byteLength + 2;
  }

  private evaluateLimits(): void {
    if (!this.recording || this.recording.version !== '2.0.0' || this.stopping) return;
    const limits = this.recording.limits;
    if (!limits) return;
    const elapsed = Date.now() - this.startedAtMs;
    // H-1: use the incremental counter instead of JSON.stringify on every call.
    const bytes = this.currentByteLength;
    const values: Array<{ kind: RecordingLimitKind; value: number; max: number }> = [
      { kind: 'actions', value: this.recording.events.length, max: limits.maxActions },
      { kind: 'duration', value: elapsed, max: limits.maxDurationMs },
      { kind: 'size', value: bytes, max: limits.maxBytes },
    ];
    for (const { kind, value, max } of values) {
      if (max <= 0) continue;
      if (value >= max * limits.warningThreshold && !this.warnedLimits.has(kind)) {
        this.warnedLimits.add(kind);
        const message = `${kind} recording limit is approaching (${value}/${max})`;
        this.recording.warnings?.push({ kind, message, timestamp: Date.now() });
        this.notifyRecordingStatus('warning', message);
        this.recordingHud?.showLimitWarning(kind, value / max);
      }
    }
    if (bytes >= limits.maxBytes) {
      this.requestLimitStop('size-limit', `maximum recording size of ${limits.maxBytes} bytes reached`);
    } else if (elapsed >= limits.maxDurationMs) {
      this.requestLimitStop('duration-limit', `maximum duration of ${limits.maxDurationMs}ms reached`);
    }
  }

  private requestLimitStop(reason: RecordingStopReason, message: string): void {
    if (this.limitStopRequested || !this.recordingFlag) return;
    this.limitStopRequested = true;
    console.warn(`[ContentRecorder] ${message}; stopping recording`);
    queueMicrotask(() => {
      void this.stopAsync(reason, message, true).then((recording) => {
        this.notifyRecordingStatus('stopped', message, recording);
      });
    });
  }

  private readPersistedState(): PersistedRecordingState | null {
    try {
      const raw = sessionStorage.getItem(SESSION_STORAGE_KEY);
      if (!raw) return null;
      const parsed = JSON.parse(raw) as PersistedRecordingState;
      // M-8: basic schema validation. sessionStorage is shared with the page,
      // which can inject crafted payloads to corrupt the recording pipeline.
      if (!parsed || typeof parsed !== 'object') return null;
      if (!parsed.recordingFlag || typeof parsed.recordingFlag !== 'boolean') return null;
      if (!parsed.recording || typeof parsed.recording !== 'object') return null;
      const rec = parsed.recording as unknown as Record<string, unknown>;
      if (!Array.isArray(rec.events) || !Array.isArray(rec.snapshots)) return null;
      if (rec.marks !== undefined && !this.validPersistedMarks(rec.marks)) {
        (rec as { marks?: unknown }).marks = [];
      }
      if (!rec.meta || typeof rec.meta !== 'object') return null;
      return parsed;
    } catch (err) {
      console.warn('[ContentRecorder] failed to read persisted recording state:', err);
      return null;
    }
  }

  private validPersistedMarks(value: unknown): boolean {
    if (!Array.isArray(value)) return false;
    if (value.length > (this.options.maxMarks ?? DEFAULT_MAX_MARKS)) return false;
    return value.every((item) => {
      const mark = item as Partial<PageMark> | null;
      return !!mark
        && typeof mark.id === 'string'
        && typeof mark.timestamp === 'number'
        && typeof mark.url === 'string'
        && ['listItem', 'field', 'nextPage', 'input', 'exclude'].includes(String(mark.role))
        && typeof mark.note === 'string'
        && mark.note.length <= MAX_MARK_NOTE_CHARS
        && typeof mark.element?.selector === 'string';
    });
  }

  resumeIfNeeded(): boolean {
    if (this.recordingFlag) {
      return true;
    }
    if (!IS_TOP_FRAME || this.options.disableCrossPagePersistence) {
      return false;
    }
    const state = this.readPersistedState();
    if (!state) {
      return false;
    }

    return this.restorePersistedState(state);
  }

  async resumeFromBackgroundIfNeeded(): Promise<boolean> {
    if (this.recordingFlag || !IS_TOP_FRAME || this.options.disableCrossPagePersistence) return this.recordingFlag;
    if (this.backgroundResumePromise) return this.backgroundResumePromise;
    const runtime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { sendMessage?: (message: unknown) => Promise<unknown> } }
      | undefined;
    if (!runtime?.runtime?.sendMessage) return false;
    const resume = (async (): Promise<boolean> => {
      try {
        const response = await runtime.runtime!.sendMessage!({ action: 'RESUME_RECORDING' }) as {
          active?: boolean;
          state?: PersistedRecordingState;
          localCheckpoint?: RecordingLocalCheckpoint;
        };
        if (response?.active && response.state) {
          return this.restorePersistedState(response.state);
        }
        // D-3 fallback: SW has no in-memory session. Try the disk-backed
        // .local checkpoint so the recording survives SW eviction.
        if (response?.localCheckpoint) {
          return this.restoreFromLocalCheckpoint(response.localCheckpoint);
        }
        return false;
      } catch {
        return false;
      }
    })();
    this.backgroundResumePromise = resume;
    try {
      return await resume;
    } finally {
      if (this.backgroundResumePromise === resume) this.backgroundResumePromise = null;
    }
  }

  // After a cross-origin round trip back to the original origin, the locally
  // restored sessionStorage checkpoint predates the events recorded on the
  // intermediate document (which were flushed to the background session at
  // beforeunload). When the background session carries strictly more events,
  // adopt it so the destination events are not silently dropped.
  private async adoptNewerBackgroundCheckpoint(): Promise<void> {
    if (!this.recordingFlag || !this.recording || !IS_TOP_FRAME
      || this.options.disableCrossPagePersistence) return;
    const runtime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { sendMessage?: (message: unknown) => Promise<unknown> } }
      | undefined;
    if (!runtime?.runtime?.sendMessage) return;
    try {
      const response = await runtime.runtime!.sendMessage!({ action: 'RESUME_RECORDING' }) as {
        active?: boolean;
        state?: PersistedRecordingState;
      };
      const remote = response?.active ? response.state?.recording : undefined;
      if (!remote || remote.termination) return;
      // Cross-origin round trips split the recording state: the intermediate
      // document force-checkpointed its events to the background while the
      // original origin's sessionStorage kept events the unload flush lost
      // on the way there. Neither copy is a superset, so merge by event and
      // snapshot identity (type+timestamp / timestamp) instead of adopting
      // whichever side happens to be longer.
      const local = this.recording;
      const eventKey = (e: { type: string; timestamp: number }): string => `${e.type}@${e.timestamp}`;
      const localEventKeys = new Set(local.events.map(eventKey));
      const missingEvents = remote.events.filter((e) => !localEventKeys.has(eventKey(e)));
      const snapshotKey = (s: { timestamp: number }): string => `snap@${s.timestamp}`;
      const localSnapshotKeys = new Set(local.snapshots.map(snapshotKey));
      // A 'final' snapshot from the background belongs to a terminated state
      // (e.g. a partial stop on an intermediate document); merging it mid-list
      // breaks the required initial...final phase ordering. The active stop
      // path always captures its own authoritative final snapshot.
      const missingSnapshots = remote.snapshots.filter((s) => s.phase !== 'final'
        && !localSnapshotKeys.has(snapshotKey(s)));
      if (missingEvents.length === 0 && missingSnapshots.length === 0) return;
      // Stable sort by timestamp preserves intra-source order.
      local.events = [...local.events, ...missingEvents]
        .sort((a, b) => a.timestamp - b.timestamp);
      local.snapshots = [...local.snapshots, ...missingSnapshots]
        .sort((a, b) => a.timestamp - b.timestamp);
      const remoteSelectorMap = new Map(response.state!.selectorToIndex ?? []);
      for (const [selector, index] of remoteSelectorMap) {
        if (!this.selectorToIndex.has(selector)) this.selectorToIndex.set(selector, index);
      }
      this.nextIndex = Math.max(this.nextIndex, response.state!.nextIndex ?? 0);
      this.persistState(true);
    } catch {
      // Keep the locally restored state when the background is unreachable.
    }
  }

  // D-3: Reconstruct recording state from the disk-backed .local checkpoint.
  // The checkpoint's `sessionId` (the SW's session.startedAt) must match the
  // current realm's `startedAtMs` when one exists; a mismatch means the
  // checkpoint is stale from a previous recording session and must be discarded.
  private restoreFromLocalCheckpoint(chk: RecordingLocalCheckpoint): boolean {
    if (this.recordingFlag) return true;
    if (!chk?.snapshot || chk.snapshot.version !== '2.0.0' || chk.snapshot.termination) {
      return false;
    }
    // SessionId guard: if we have a startedAtMs from a prior restore (e.g.,
    // sessionStorage), reject checkpoints from a different session.
    if (this.startedAtMs > 0 && chk.sessionId != null && chk.sessionId !== this.startedAtMs) {
      this.sendClearCheckpoint();
      return false;
    }
    const state: PersistedRecordingState = {
      version: 1,
      recordingFlag: true,
      recording: chk.snapshot,
      options: { ...this.options, ...chk.options, protocolVersion: chk.snapshot.version },
      selectorToIndex: chk.selectorToIndex ?? [],
      nextIndex: chk.nextIndex ?? 1,
      lastRecordedUrl: chk.lastRecordedUrl ?? null,
    };
    return this.restorePersistedState(state);
  }

  private sendClearCheckpoint(): void {
    const runtime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { sendMessage?: (message: unknown) => Promise<unknown> } }
      | undefined;
    runtime?.runtime?.sendMessage?.({ action: 'CLEAR_RECORDING_CHECKPOINT' }).catch(() => undefined);
  }

  private restorePersistedState(state: PersistedRecordingState): boolean {
    if (this.recordingFlag) return true;

    this.recording = state.recording;
    this.options = { ...this.options, ...state.options, protocolVersion: state.recording.version };
    this.recordingFlag = true;
    this.selectorToIndex = new Map(state.selectorToIndex ?? []);
    this.nextIndex = state.nextIndex ?? 1;
    this.lastRecordedUrl = state.lastRecordedUrl ?? null;
    this.eventCount = this.recording.events.length;
    this.snapshotCount = this.recording.snapshots.length;
    this.snapshotSequence = this.recording.snapshots.reduce(
      (max, snapshot) => Math.max(max, (snapshot.sequence ?? -1) + 1),
      0,
    );
    this.startedAtMs = Date.parse(this.recording.meta.recordedAt) || Date.now();
    // M-2: repopulate warnedLimits from the persisted recording's warnings
    // so evaluateLimits doesn't re-emit duplicate threshold warnings after
    // a cross-navigation resume.
    this.warnedLimits = new Set(
      (this.recording.warnings ?? []).map((w) => w.kind as RecordingLimitKind),
    );
    this.eventQueue = Promise.resolve();
    this.lastEnterSubmit = null;
    this.lastClickedSubmitter = null;

    const controller = this.isV2() ? null : findPageController();
    if (controller) {
      this.controllerRecorder = new PageAgentRecorder(controller, this.options);
      // Best-effort: copy recorded events into the controller recorder so far.
      for (const event of this.recording.events) {
        this.controllerRecorder.getRecording().events.push(event);
      }
      // M-3: preserve pre-navigation snapshots alongside events. Previously
      // only events were copied, silently losing all DOM snapshots.
      const newRecording = this.controllerRecorder.getRecording();
      for (const snapshot of this.recording.snapshots) {
        newRecording.snapshots.push(snapshot);
      }
      for (const mark of this.recording.marks ?? []) {
        this.controllerRecorder.addMark(mark);
      }
      this.recording = newRecording;
    } else {
      this.attachDomListeners();
    }

    this.attachNavigationListeners();
    this.wrapRecordingArrays(this.recording);
    this.attachMarkOverlay();
    this.attachRecordingHud();
    // H-1: initialize incremental byte counter.
    this.recalibrateByteLength();
    this.startSelectorCleanup();
    if (this.isV2()) {
      const remaining = (this.recording.limits?.maxDurationMs ?? 0) - (Date.now() - this.startedAtMs);
      if (remaining > 0) {
        this.durationLimitTimer = setTimeout(() => {
          this.requestLimitStop('duration-limit', 'maximum recording duration reached');
        }, remaining);
      } else {
        this.requestLimitStop('duration-limit', 'maximum recording duration reached');
      }
    }
    return true;
  }

  private capRecordingSize(recording: PageAgentRecording, maxBytes: number = 4 * 1024 * 1024): PageAgentRecording {
    const json = JSON.stringify(recording);
    if (json.length <= maxBytes) return recording;
    // First attempt: keep only the last snapshot's domTree.
    for (let i = 0; i < recording.snapshots.length - 1; i++) {
      delete recording.snapshots[i].domTree;
    }
    if (JSON.stringify(recording).length <= maxBytes) return recording;
    // Last resort: drop all domTrees; selectorMap/events remain intact.
    for (const snapshot of recording.snapshots) {
      delete snapshot.domTree;
    }
    return recording;
  }

  addPageMark(mark: PageMark): PageMark {
    if (!this.recording || !this.recordingFlag) {
      throw new Error('recording is not active');
    }
    this.validatePageMark(mark);
    if (!this.recording.marks) {
      this.recording.marks = [];
    }
    const existingIndex = this.recording.marks.findIndex((item) => item.id === mark.id);
    if (existingIndex >= 0) {
      const previous = this.recording.marks[existingIndex];
      this.currentByteLength -= ContentRecorder.computeItemByteLength(previous);
      this.recording.marks[existingIndex] = mark;
      this.currentByteLength += ContentRecorder.computeItemByteLength(mark);
    } else {
      const maxMarks = this.options.maxMarks ?? DEFAULT_MAX_MARKS;
      if (this.recording.marks.length >= maxMarks) {
        throw new Error(`maximum mark count of ${maxMarks} reached`);
      }
      this.recording.marks.push(mark);
      this.currentByteLength += ContentRecorder.computeItemByteLength(mark);
    }
    if (this.controllerRecorder) {
      this.controllerRecorder.addMark(mark);
    }
    this.evaluateLimits();
    this.persistState();
    this.markOverlay?.refresh();
    return mark;
  }

  removePageMark(id: string): boolean {
    if (!this.recording?.marks) return false;
    const index = this.recording.marks.findIndex((item) => item.id === id);
    if (index < 0) return false;
    const [removed] = this.recording.marks.splice(index, 1);
    this.currentByteLength -= ContentRecorder.computeItemByteLength(removed);
    this.controllerRecorder?.removeMark(id);
    this.persistState();
    this.markOverlay?.refresh();
    return true;
  }

  private listPageMarks(): PageMark[] {
    return this.recording?.marks ?? [];
  }

  private attachMarkOverlay(): void {
    if (!IS_TOP_FRAME || !this.isV2() || !this.recordingFlag || this.markOverlay) return;
    const adapter: PageMarkOverlayAdapter = {
      buildMarkForElement: (element, role, note) => this.buildPageMark(element, role, note),
      saveMark: (mark) => this.addPageMark(mark),
      removeMark: (id) => this.removePageMark(id),
      listMarks: () => this.listPageMarks(),
    };
    this.markOverlay = new PageMarkOverlay(adapter);
    this.markOverlay.mount();
  }

  private attachRecordingHud(): void {
    // Visible for every recording protocol (v1 and v2), top frame only. The
    // HUD is purely presentational: stop requests go through the background
    // service worker so the normal stop/finalize path is reused.
    if (!IS_TOP_FRAME || !this.recordingFlag || this.recordingHud) return;
    const hud = new RecordingHud({
      requestStop: () => {
        const runtime = (globalThis as Record<string, unknown>).chrome as
          | { runtime?: { sendMessage?: (message: unknown) => Promise<unknown> } }
          | undefined;
        // NOT the privileged STOP_RECORDING: the sender gate rejects that
        // from content scripts. This tab-scoped variant is honored only for
        // the tab that owns the active session; surface a failure instead of
        // swallowing it so the HUD button never dies silently.
        runtime?.runtime?.sendMessage?.({ action: 'REQUEST_STOP_RECORDING' })
          .then((response) => {
            const result = response as { success?: boolean; error?: string } | undefined;
            if (result && result.success !== true) {
              this.recordingHud?.setStopFailed(result.error ?? '未知错误');
            }
          })
          .catch((err: unknown) => {
            this.recordingHud?.setStopFailed(err instanceof Error ? err.message : String(err));
          });
      },
      startedAt: () => this.startedAtMs,
      showMarkHint: this.isV2(),
    });
    this.recordingHud = hud;
    hud.mount();
    hud.setEventCount(this.recording?.events.length ?? 0);
  }

  private buildPageMark(element: Element, role: PageMarkRole, note: string): PageMark {
    const index = this.getElementIndex(element);
    const selector = this.inferSelector(element);
    const lastSnapshot = this.recording?.snapshots[this.recording.snapshots.length - 1];
    return {
      id: this.generateMarkId(),
      timestamp: Date.now(),
      url: sanitizeRecordingUrl(window.location.href),
      role,
      note: note.slice(0, MAX_MARK_NOTE_CHARS),
      element: this.buildDomElementInfo(element, index, selector),
      actionIndex: this.recording?.events.length,
      snapshotSequence: lastSnapshot?.sequence,
      state: sanitizeRecordingUrl(window.location.href),
    };
  }

  private generateMarkId(): string {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
      return crypto.randomUUID();
    }
    return `mark-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  }

  private validatePageMark(mark: PageMark): void {
    if (!mark || typeof mark !== 'object' || !mark.id || !mark.role || !mark.element?.selector) {
      throw new Error('page mark is missing required id, role, or selector evidence');
    }
    if (mark.note.length > MAX_MARK_NOTE_CHARS) {
      throw new Error(`page mark note exceeds ${MAX_MARK_NOTE_CHARS} characters`);
    }
  }

  isRecording(): boolean {
    return this.recordingFlag;
  }

  private getDomAggregateOptions(): AggregateOptions {
    if (this.isV2()) {
      return {
        maxDepth: this.options.maxDomTreeDepth,
        maxNodes: this.options.maxDomTreeNodes,
        maxTextLength: this.options.maxDomTreeTextLength,
        strictLimits: true,
      };
    }
    return {
      maxDepth: this.options.maxDomTreeDepth ?? 30,
      maxNodes: this.options.maxDomTreeNodes ?? 2000,
      maxTextLength: this.options.maxDomTreeTextLength ?? 200,
    };
  }

  private wrapRecordingArrays(recording: PageAgentRecording): void {
    // Seed identical-adjacent-snapshot dedup from any snapshots already in
    // the recording (fresh start: none; resume: possibly containing refs).
    this.restoreSnapshotDedupBaseline(recording);
    const maxEvents = this.options.maxEvents ?? (recording.version === '2.0.0' ? 500 : 5000);
    const maxSnapshots = this.options.maxSnapshots ?? 200;

    const originalEventsPush = recording.events.push.bind(recording.events);
    Object.defineProperty(recording.events, 'push', {
      value: (...items: PageAgentEvent[]) => {
        const result = originalEventsPush(...items);
        this.eventCount = recording.events.length;
        this.recordingHud?.setEventCount(this.eventCount);
        // H-1: track bytes incrementally per event.
        for (const item of items) {
          this.currentByteLength += ContentRecorder.computeItemByteLength(item);
        }
        if (recording.version === '2.0.0') {
          this.evaluateLimits();
          if (this.eventCount >= maxEvents) {
            this.requestLimitStop('action-limit', `maximum action count of ${maxEvents} reached`);
          }
        } else if (this.eventCount >= maxEvents) {
          console.warn(`[ContentRecorder] maxEvents (${maxEvents}) reached; stopping recording`);
          this.lastRecording = this.stop();
        }
        return result;
      },
      writable: true,
      configurable: true,
    });

    const originalSnapshotsPush = recording.snapshots.push.bind(recording.snapshots);
    Object.defineProperty(recording.snapshots, 'push', {
      value: (...items: DomSnapshot[]) => {
        // V1 shifts old snapshots to bound memory; V2 relies on the byte
        // limit in evaluateLimits (which must fire promptly — see H-1
        // incremental byteLength fix) plus the action limit (maxEvents=500).
        if (recording.version !== '2.0.0' && recording.snapshots.length >= maxSnapshots) {
          const shifted = recording.snapshots.shift();
          if (shifted) this.currentByteLength -= ContentRecorder.computeItemByteLength(shifted);
        }
        // V2: collapse snapshots whose content (url, selectorMap, domTree,
        // capture) is exactly identical to the last content snapshot into
        // lightweight references. Identical content is equivalent per-action
        // evidence, so the contract keeps one snapshot per action while the
        // payload is stored once. Positional fields (phase, sequence,
        // actionIndex, timestamp) stay per-snapshot.
        const pushed: DomSnapshot[] = recording.version === '2.0.0'
          ? items.map((item) => this.deduplicateSnapshot(item))
          : items;
        const result = originalSnapshotsPush(...pushed);
        // H-1: track bytes incrementally per snapshot (references are small,
        // so the byte budget now reflects the deduplicated payload).
        for (const item of pushed) {
          this.currentByteLength += ContentRecorder.computeItemByteLength(item);
        }
        this.snapshotCount = recording.snapshots.length;
        if (recording.version === '2.0.0') this.evaluateLimits();
        return result;
      },
      writable: true,
      configurable: true,
    });
  }

  /**
   * Returns a reference snapshot when the candidate's content equals the last
   * content snapshot; otherwise remembers the candidate as the new content
   * baseline and returns it unchanged. Snapshots without a domTree (capture
   * failures) never participate: they are already small and carry no
   * referenceable content.
   */
  private deduplicateSnapshot(candidate: DomSnapshot): DomSnapshot {
    if (candidate.domTree === undefined) return candidate;
    if (this.snapshotContentEqualsBaseline(candidate)) {
      const reference: DomSnapshot = {
        timestamp: candidate.timestamp,
        url: candidate.url,
        selectorMap: {},
        phase: candidate.phase,
        sequence: candidate.sequence,
        actionIndex: candidate.actionIndex,
        ref: this.lastContentSnapshot!.sequence,
      };
      return reference;
    }
    this.rememberContentSnapshot(candidate);
    return candidate;
  }

  private snapshotContentEqualsBaseline(candidate: DomSnapshot): boolean {
    const baseline = this.lastContentSnapshot;
    if (!baseline) return false;
    return baseline.url === JSON.stringify(candidate.url)
      && baseline.capture === JSON.stringify(candidate.capture ?? null)
      && baseline.selectorMap === JSON.stringify(candidate.selectorMap ?? {})
      && baseline.domTree === JSON.stringify(candidate.domTree ?? null);
  }

  private rememberContentSnapshot(snapshot: DomSnapshot): void {
    this.lastContentSnapshot = {
      sequence: snapshot.sequence ?? this.snapshotSequence - 1,
      url: JSON.stringify(snapshot.url),
      selectorMap: JSON.stringify(snapshot.selectorMap ?? {}),
      domTree: JSON.stringify(snapshot.domTree ?? null),
      capture: JSON.stringify(snapshot.capture ?? null),
    };
  }

  /** Seed the dedup baseline from the newest full snapshot already stored in
   *  the recording (covers both a fresh start and resume from a checkpoint
   *  that itself contains references). */
  private restoreSnapshotDedupBaseline(recording: PageAgentRecording): void {
    this.lastContentSnapshot = null;
    if (recording.version !== '2.0.0') return;
    for (let index = recording.snapshots.length - 1; index >= 0; index -= 1) {
      const snapshot = recording.snapshots[index];
      if (snapshot?.domTree !== undefined) {
        this.rememberContentSnapshot(snapshot);
        return;
      }
    }
  }

  private startSelectorCleanup(): void {
    const ttlMs = this.options.selectorIndexTTLMs ?? 30000;
    if (ttlMs <= 0) {
      return;
    }
    this.selectorCleanupInterval = setInterval(() => {
      this.cleanupSelectorIndex();
    }, Math.max(1000, Math.floor(ttlMs / 2)));
  }

  private stopSelectorCleanup(): void {
    if (this.selectorCleanupInterval) {
      clearInterval(this.selectorCleanupInterval);
      this.selectorCleanupInterval = null;
    }
  }

  private cleanupSelectorIndex(): void {
    const ttlMs = this.options.selectorIndexTTLMs ?? 30000;
    const now = Date.now();
    for (const [selector, entry] of this.selectorToIndex) {
      if (now - entry.lastUsedAt > ttlMs) {
        this.selectorToIndex.delete(selector);
      }
    }
  }

  attachChromeMessaging(): void {
    const chromeRuntime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { onMessage?: { addListener: (handler: unknown) => void; removeListener: (handler: unknown) => void } } }
      | undefined;
    if (!chromeRuntime?.runtime?.onMessage) {
      return;
    }

    this.messageHandler = (message: unknown, _sender: unknown, sendResponse: (response?: unknown) => void) => {
      const msg = message as { action?: string; payload?: RecorderOptions; options?: unknown };
      if (msg.action === 'START_RECORDING') {
        this.start(msg.payload);
        if (!this.isV2()) {
          sendResponse({ success: true });
          return true;
        }
        (async () => {
          try {
            await this.eventQueue;
            sendResponse({ success: this.recordingFlag, protocolVersion: this.recording?.version });
          } catch (error) {
            const message = error instanceof Error ? error.message : 'initial snapshot capture failed';
            const recording = this.finalizeStop('capture-error', message, false);
            this.notifyRecordingStatus('stopped', message, recording);
            sendResponse({ success: false, error: message, recording });
          }
        })();
        return true;
      }
      if (msg.action === 'ENSURE_RECORDING_READY') {
        (async () => {
          const active = this.recordingFlag
            || this.resumeIfNeeded()
            || await this.resumeFromBackgroundIfNeeded();
          if (active) {
            // Returning to the original origin restores the local
            // sessionStorage checkpoint, which is staler than the background
            // session after a cross-origin round trip (events recorded on the
            // intermediate document were flushed there at beforeunload).
            // Adopt the background state when it carries more events.
            await this.adoptNewerBackgroundCheckpoint();
            // A full-page navigation can replace the isolated content script
            // after `pageshow` has already fired. Reconcile the restored
            // checkpoint with the current document URL here; handleNavigate
            // deduplicates the event when the normal listener already saw it.
            this.handleNavigate();
            await this.eventQueue;
          }
          sendResponse({
            success: active,
            active,
            protocolVersion: this.recording?.version,
            ...(active ? {} : { error: 'recording checkpoint is unavailable' }),
          });
        })();
        return true;
      }
      if (msg.action === 'EXPORT_RECORDING_HANDOFF') {
        (async () => {
          const state = await this.exportRecordingHandoff();
          sendResponse(state
            ? { success: true, active: true, protocolVersion: '2.0.0', state }
            : { success: false, active: false, error: 'active V2 recording is unavailable' });
        })();
        return true;
      }
      if (msg.action === 'IMPORT_RECORDING_HANDOFF') {
        (async () => {
          const state = await this.importRecordingHandoff(msg.payload as unknown as PersistedRecordingState);
          sendResponse(state
            ? { success: true, active: true, protocolVersion: '2.0.0', state }
            : { success: false, active: false, error: 'invalid V2 recording handoff state' });
        })();
        return true;
      }
      if (msg.action === 'DISCARD_RECORDING_HANDOFF') {
        this.discardRecordingHandoff();
        sendResponse({ success: true, active: false });
        return true;
      }
      if (msg.action === 'STOP_RECORDING') {
        (async () => {
          let recording: PageAgentRecording;
          if (this.isV2()) {
            recording = await this.stopAsync();
          } else {
            await this.eventQueue;
            if (this.recordingFlag && this.options.captureSnapshotBeforeEachEvent) {
              try {
                await this.captureSnapshot();
              } catch (err) {
                console.warn('[ContentRecorder] final snapshot failed:', err);
              }
            }
            recording = this.stop();
          }
          sendResponse({ recording });
        })();
        return true;
      }
      if (msg.action === 'CAPTURE_DOM') {
        const options = (msg.options ?? {}) as AggregateOptions;
        const captured = captureLocalDomWithReport(options);
        sendResponse({ domTree: captured.domTree, serialization: captured.report });
        return true;
      }
      return false;
    };

    chromeRuntime.runtime.onMessage.addListener(this.messageHandler);
  }

  detachChromeMessaging(): void {
    const chromeRuntime = (globalThis as Record<string, unknown>).chrome as
      | { runtime?: { onMessage?: { removeListener: (handler: unknown) => void } } }
      | undefined;
    if (this.messageHandler && chromeRuntime?.runtime?.onMessage) {
      chromeRuntime.runtime.onMessage.removeListener(this.messageHandler);
      this.messageHandler = null;
    }
  }

  private enqueueEvent(task: () => Promise<void>): void {
    this.eventQueue = this.eventQueue.then(async () => {
      if (!this.recordingFlag || !this.recording) {
        return;
      }
      try {
        await task();
        this.persistState();
      } catch (err) {
        console.warn('[ContentRecorder] event task failed:', err);
      }
    });
  }

  private linkSnapshotToNextEvent(snapshot: DomSnapshot | void): void {
    if (snapshot?.phase === 'before-action' && this.recording) {
      snapshot.actionIndex = this.recording.events.length;
      // The stored copy may be a deduplicated reference snapshot distinct
      // from this candidate object (captures can also resolve out of order),
      // so write the action link through to the stored snapshot by sequence.
      if (snapshot.sequence !== undefined) {
        const stored = this.recording.snapshots.find((item) => item.sequence === snapshot.sequence);
        if (stored && stored !== snapshot) {
          stored.actionIndex = snapshot.actionIndex;
        }
      }
    }
  }

  private recordEventSync(event: PageAgentEvent): void {
    if (!this.recordingFlag || !this.recording) {
      return;
    }
    this.recording.events.push(event);
    this.persistState();
  }

  private elementWillNavigate(el: Element): boolean {
    const tag = el.tagName.toLowerCase();
    if (tag === 'a') {
      const href = el.getAttribute('href');
      return href !== null && href !== '#' && !href.startsWith('javascript:');
    }
    if (tag === 'input') {
      const input = el as HTMLInputElement;
      return (input.type === 'submit' || input.type === 'image') && input.form !== null;
    }
    if (tag === 'button') {
      const button = el as HTMLButtonElement;
      const explicitType = el.getAttribute('type')?.toLowerCase();
      const isSubmitType = explicitType === 'submit' || explicitType === '' || explicitType === null;
      return isSubmitType && button.form !== null;
    }
    return false;
  }

  private captureLocalSnapshot(
    timestamp: number,
    phase?: 'initial' | 'before-action' | 'final',
    actionIndex?: number,
  ): DomSnapshot {
    const captured = captureLocalDomWithReport(this.getDomAggregateOptions());
    const snapshot: DomSnapshot = {
      timestamp,
      url: sanitizeRecordingUrl(window.location.href),
      selectorMap: {},
    };
    if (this.isV2()) {
      snapshot.phase = phase ?? 'before-action';
      snapshot.sequence = this.snapshotSequence++;
      snapshot.actionIndex = actionIndex;
      snapshot.capture = {
        status: captured.report.truncated ? 'partial' : 'complete',
        nodeCount: captured.report.nodeCount,
        redactionCount: captured.report.redactionCount,
        removedNodeCount: captured.report.removedNodeCount,
        // Navigation clicks must persist their pre-action evidence before the
        // document is torn down, so this path cannot await frame aggregation.
        // The top document itself is nevertheless fully captured and must
        // carry the same root audit shape as the asynchronous aggregator.
        frames: [{
          frameId: 0,
          parentFrameId: -1,
          url: snapshot.url,
          status: 'captured',
        }],
      };
    }
    for (const el of this.findInteractableElements()) {
      const index = this.getElementIndex(el);
      const selector = this.inferSelector(el);
      snapshot.selectorMap[index] = this.buildDomElementInfo(el, index, selector);
    }
    if (captured.domTree) {
      snapshot.domTree = captured.domTree;
    }
    return snapshot;
  }

  private attachDomListeners(): void {
    // Capture click intent before target/page bubble handlers mutate or
    // navigate the document. The serializer runs synchronously before its
    // asynchronous frame aggregation, preserving the pre-action top DOM.
    this.addListener(document, 'click', this.handleClick, { capture: true });
    this.addListener(document, 'input', this.handleInput);
    this.addListener(document, 'change', this.handleChange);
    this.addListener(document, 'scroll', this.handleScroll, { capture: true, passive: true });
    this.addListener(document, 'keydown', this.handleKeydown as unknown as EventListener);
    this.addListener(document, 'submit', this.handleSubmit, { capture: true });
  }

  private attachNavigationListeners(): void {
    // H-7: SPA frameworks (React Router, Vue Router, Angular Router) use
    // history.pushState/replaceState for client-side routing. These don't
    // fire popstate, so the recorder would silently miss the navigation.
    // Monkey-patch to dispatch a custom event after each call.
    this.originalPushState = history.pushState.bind(history);
    this.originalReplaceState = history.replaceState.bind(history);
    const self = this;
    history.pushState = function (...args: Parameters<typeof history.pushState>) {
      const result = self.originalPushState!(...args);
      window.dispatchEvent(new Event('__ocHistoryNav'));
      return result;
    } as typeof history.pushState;
    history.replaceState = function (...args: Parameters<typeof history.replaceState>) {
      const result = self.originalReplaceState!(...args);
      window.dispatchEvent(new Event('__ocHistoryNav'));
      return result;
    } as typeof history.replaceState;

    this.addListener(window, 'popstate', this.handleNavigate);
    this.addListener(window, 'hashchange', this.handleNavigate);
    this.addListener(window, 'pageshow', this.handleNavigate);
    // Custom event from our pushState/replaceState patch. Call handleNavigate
    // without the event argument to bypass the isTrusted check (this event
    // is dispatched by our own code, not by the page).
    this.addListener(window, '__ocHistoryNav', (() => { this.handleNavigate(); }) as unknown as EventListener);
    this.addListener(window, 'beforeunload', this.handleBeforeUnload);
  }

  private handleBeforeUnload = (): void => {
    if (!this.recordingFlag || !this.recording) {
      return;
    }
    // Flush pending input/scroll so they are not lost when the content script
    // is torn down during a full-page navigation.
    this.flushInputTimeouts();
    this.flushScrollTimeouts();
    this.persistState(true);
  };

  private detachAllListeners(): void {
    for (const { target, event, handler, options } of this.listeners) {
      target.removeEventListener(event, handler, options);
    }
    this.listeners = [];
    // H-7: restore original history methods.
    if (this.originalPushState) {
      history.pushState = this.originalPushState;
      this.originalPushState = null;
    }
    if (this.originalReplaceState) {
      history.replaceState = this.originalReplaceState;
      this.originalReplaceState = null;
    }
  }

  private addListener(
    target: EventTarget,
    event: string,
    handler: EventListener,
    options?: AddEventListenerOptions,
  ): void {
    target.addEventListener(event, handler, options);
    this.listeners.push({ target, event, handler, options });
  }

  private handleClick = (event: Event): void => {
    if (!this.recordingFlag || (this.options.enforceIsTrusted && !event.isTrusted)) {
      return;
    }
    const el = this.resolveInteractableClickTarget(event);
    if (!el) {
      return;
    }

    if (this.isFormSubmitter(el)) {
      this.lastClickedSubmitter = { element: el, time: Date.now() };
      // When the user presses Enter inside a form, the browser synthesizes a
      // click on the default submit button. Do not record that synthetic click
      // as a separate step; the submitForm event from handleKeydown already
      // captures the submission.
      if (this.isSyntheticSubmitterClickFromEnter(el)) {
        return;
      }
    }

    // Make sure any pending inputText is recorded before a click that may
    // navigate away from the page.
    this.flushInputTimeouts();

    const index = this.getElementIndex(el);
    const timestamp = Date.now();

    // For clicks that will trigger a full-page navigation (links, submit
    // buttons), record the event synchronously and capture a local snapshot
    // before the page is torn down. Otherwise the async event queue may be
    // discarded before the click is ever persisted.
    if (this.elementWillNavigate(el)) {
      if (this.options.captureSnapshotBeforeEachEvent) {
        this.recording!.snapshots.push(
          this.captureLocalSnapshot(timestamp, 'before-action', this.recording!.events.length),
        );
      }
      this.recordEventSync({ type: 'click', index, timestamp });
      return;
    }

    const snapshotPromise = this.options.captureSnapshotBeforeEachEvent
      ? this.captureSnapshot(timestamp, 'before-action')
      : Promise.resolve();
    this.enqueueEvent(async () => {
      this.linkSnapshotToNextEvent(await snapshotPromise);
      this.recording!.events.push({ type: 'click', index, timestamp });
    });
  };

  private resolveInteractableClickTarget(event: Event): Element | null {
    const path = typeof event.composedPath === 'function' ? event.composedPath() : [];
    for (const value of path) {
      if (value instanceof Element && this.isInteractable(value)) {
        return value;
      }
    }

    let element = event.target instanceof Element ? event.target : null;
    while (element) {
      if (this.isInteractable(element)) {
        return element;
      }
      element = element.parentElement;
    }
    return null;
  }

  private handleInput = (event: Event): void => {
    if (!this.recordingFlag || (this.options.enforceIsTrusted && !event.isTrusted)) {
      return;
    }
    const el = event.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
    if (!el || el.tagName.toLowerCase() === 'select') {
      return;
    }
    this.debouncedTextInput(el as HTMLInputElement | HTMLTextAreaElement);
  };

  private handleChange = (event: Event): void => {
    if (!this.recordingFlag || (this.options.enforceIsTrusted && !event.isTrusted)) {
      return;
    }
    const el = event.target as HTMLSelectElement;
    if (el?.tagName?.toLowerCase() !== 'select') {
      return;
    }
    const index = this.getElementIndex(el);
    const option = el.options[el.selectedIndex];
    const optionText = redactSensitiveText(option?.textContent?.trim() || '').value;
    const timestamp = Date.now();
    const snapshotPromise = this.options.captureSnapshotBeforeEachEvent
      ? this.captureSnapshot(timestamp, 'before-action')
      : Promise.resolve();
    this.enqueueEvent(async () => {
      this.linkSnapshotToNextEvent(await snapshotPromise);
      this.recording!.events.push({ type: 'selectOption', index, optionText, timestamp });
    });
  };

  private debouncedTextInput(el: HTMLInputElement | HTMLTextAreaElement): void {
    const existing = this.inputTimeouts.get(el);
    if (existing) {
      clearTimeout(existing);
    }
    this.inputTimeouts.set(
      el,
      setTimeout(() => {
        this.recordTextInput(el);
        this.inputTimeouts.delete(el);
      }, 100),
    );
  }

  private recordTextInput(el: HTMLInputElement | HTMLTextAreaElement, sync = false): void {
    const index = this.getElementIndex(el);
    const rawText = el.value || '';
    const text = isSensitiveElement(el) ? '[REDACTED]' : redactSensitiveText(rawText).value;
    const timestamp = Date.now();
    if (sync) {
      if (this.options.captureSnapshotBeforeEachEvent) {
        // Capture synchronously: the page may navigate immediately after this
        // flushed input (e.g. typing then clicking a submit button).
        this.recording!.snapshots.push(
          this.captureLocalSnapshot(timestamp, 'before-action', this.recording!.events.length),
        );
      }
      this.recordEventSync({ type: 'inputText', index, text, timestamp });
      return;
    }
    const snapshotPromise = this.options.captureSnapshotBeforeEachEvent
      ? this.captureSnapshot(timestamp, 'before-action')
      : Promise.resolve();
    this.enqueueEvent(async () => {
      this.linkSnapshotToNextEvent(await snapshotPromise);
      this.recording!.events.push({ type: 'inputText', index, text, timestamp });
    });
  }

  private flushInputTimeouts(): void {
    for (const [el, timeout] of this.inputTimeouts) {
      clearTimeout(timeout);
      // H-5: skip elements that have been removed from the DOM. Their Map
      // entries are strong references that would otherwise pin detached
      // subtrees in memory until stop().
      if (el.isConnected) {
        this.recordTextInput(el as HTMLInputElement | HTMLTextAreaElement, true);
      }
      this.inputTimeouts.delete(el);
    }
  }

  private flushScrollTimeouts(): void {
    for (const [el, timeout] of this.scrollTimeouts) {
      clearTimeout(timeout);
      // H-5: skip elements that have been removed from the DOM.
      if (el.isConnected) {
        // H-3: sync=true ensures the event is pushed synchronously before
        // persistState(true) runs in handleBeforeUnload.
        this.recordScroll(el, true);
      }
      this.scrollTimeouts.delete(el);
    }
  }

  private handleScroll = (event: Event): void => {
    if (!this.recordingFlag || (this.options.enforceIsTrusted && !event.isTrusted)) {
      return;
    }
    const el = this.resolveScrollTarget(event);
    const existing = this.scrollTimeouts.get(el);
    if (existing) {
      clearTimeout(existing);
    }
    this.scrollTimeouts.set(
      el,
      setTimeout(() => {
        this.recordScroll(el);
        this.scrollTimeouts.delete(el);
      }, 250),
    );
  };

  private resolveScrollTarget(event: Event): Element {
    if (event.target === document || event.target === window) {
      return document.documentElement as Element;
    }
    return event.target as Element;
  }

  private recordScroll(el: Element, sync = false): void {
    const prev = this.scrollState.get(el) || { top: 0, left: 0 };
    const htmlEl = document.documentElement as HTMLElement;
    const currentTop = el === htmlEl ? window.scrollY : (el as HTMLElement).scrollTop;
    const currentLeft = el === htmlEl ? window.scrollX : (el as HTMLElement).scrollLeft;
    const deltaY = currentTop - prev.top;
    const deltaX = currentLeft - prev.left;

    if (Math.abs(deltaY) < 1 && Math.abs(deltaX) < 1) {
      return;
    }

    this.scrollState.set(el, { top: currentTop, left: currentLeft });

    const index = this.getElementIndex(el);
    const isWindow = el === htmlEl;
    const viewportHeight = window.innerHeight || 1;
    const timestamp = Date.now();
    const event: PageAgentEvent =
      Math.abs(deltaY) >= Math.abs(deltaX)
        ? {
            type: 'scroll',
            direction: deltaY > 0 ? 'down' : 'up',
            amount: isWindow ? Math.max(1, Math.round(Math.abs(deltaY) / viewportHeight)) : Math.abs(deltaY),
            unit: isWindow ? 'pages' : 'pixels',
            index,
            timestamp,
          }
        : {
            type: 'scroll',
            direction: deltaX > 0 ? 'right' : 'left',
            amount: Math.abs(deltaX),
            unit: 'pixels',
            index,
            timestamp,
          };

    // H-3: sync path for beforeunload flush — events pushed synchronously
    // so persistState(true) includes them. Mirror recordTextInput's pattern.
    if (sync) {
      if (this.options.captureSnapshotBeforeEachEvent) {
        this.recording!.snapshots.push(
          this.captureLocalSnapshot(timestamp, 'before-action', this.recording!.events.length),
        );
      }
      this.recordEventSync(event);
      return;
    }
    const snapshotPromise = this.options.captureSnapshotBeforeEachEvent
      ? this.captureSnapshot(timestamp, 'before-action')
      : Promise.resolve();
    this.enqueueEvent(async () => {
      this.linkSnapshotToNextEvent(await snapshotPromise);
      this.recording!.events.push(event);
    });
  }

  private handleKeydown = (event: KeyboardEvent): void => {
    if (!this.recordingFlag || (this.options.enforceIsTrusted && !event.isTrusted)) {
      return;
    }
    if (event.key !== 'Enter') {
      return;
    }
    const el = event.target as Element;
    if (!el) {
      return;
    }
    const index = this.getElementIndex(el);
    const timestamp = Date.now();

    if (this.isEnterSubmissionElement(el)) {
      const form = (el as HTMLInputElement | HTMLTextAreaElement).form;
      if (form) {
        this.lastEnterSubmit = { form, time: Date.now() };
      }
      this.flushInputTimeouts();
      // Submit via Enter may navigate; record synchronously before teardown.
      if (this.options.captureSnapshotBeforeEachEvent) {
        this.recording!.snapshots.push(
          this.captureLocalSnapshot(timestamp, 'before-action', this.recording!.events.length),
        );
      }
      this.recordEventSync({ type: 'submitForm', index, timestamp });
      return;
    }

    const snapshotPromise = this.options.captureSnapshotBeforeEachEvent
      ? this.captureSnapshot(timestamp, 'before-action')
      : Promise.resolve();
    this.enqueueEvent(async () => {
      this.linkSnapshotToNextEvent(await snapshotPromise);
      this.recording!.events.push({ type: 'click', index, timestamp });
    });
  };

  private handleSubmit = (event: Event): void => {
    if (!this.recordingFlag || (this.options.enforceIsTrusted && !event.isTrusted)) {
      return;
    }
    const submitEvent = event as SubmitEvent;
    const form = submitEvent.target as HTMLFormElement;
    const submitter = (submitEvent.submitter as Element | null) || null;

    // Avoid duplicate submit events when the submit was already captured by
    // pressing Enter inside a submittable input/textarea.
    if (this.lastEnterSubmit && this.lastEnterSubmit.form === form && Date.now() - this.lastEnterSubmit.time < 50) {
      return;
    }

    // Avoid duplicate submit events when the submit was triggered by clicking a
    // submit button; the click event is already recorded separately.
    if (
      submitter &&
      this.lastClickedSubmitter &&
      this.lastClickedSubmitter.element === submitter &&
      Date.now() - this.lastClickedSubmitter.time < 50
    ) {
      return;
    }

    this.flushInputTimeouts();
    const index = this.getElementIndex(form);
    const submitterIndex = submitter ? this.getElementIndex(submitter) : undefined;
    const timestamp = Date.now();
    // Form submission may navigate; record synchronously before teardown.
    if (this.options.captureSnapshotBeforeEachEvent) {
      this.recording!.snapshots.push(
        this.captureLocalSnapshot(timestamp, 'before-action', this.recording!.events.length),
      );
    }
    this.recordEventSync({ type: 'submitForm', index, submitterIndex, timestamp });
  };

  private isEnterSubmissionElement(el: Element): boolean {
    const tag = el.tagName.toLowerCase();
    if (tag !== 'input' && tag !== 'textarea') {
      return false;
    }
    const form = (el as HTMLInputElement | HTMLTextAreaElement).form;
    const inputType = (el as HTMLInputElement).type;
    if (form) return inputType !== 'button' && inputType !== 'submit' && inputType !== 'reset';
    if (tag !== 'input') return false;
    return ['text', 'search', 'email', 'url', 'tel', 'password', 'number'].includes(inputType);
  }

  private isFormSubmitter(el: Element): boolean {
    const tag = el.tagName.toLowerCase();
    if (tag === 'button') {
      const type = el.getAttribute('type')?.toLowerCase();
      return type === 'submit' || type === undefined || type === null || type === '';
    }
    if (tag === 'input') {
      const inputType = (el as HTMLInputElement).type;
      return inputType === 'submit' || inputType === 'image';
    }
    return false;
  }

  private isSyntheticSubmitterClickFromEnter(el: Element): boolean {
    if (!this.lastEnterSubmit) {
      return false;
    }
    if (Date.now() - this.lastEnterSubmit.time > 100) {
      return false;
    }
    const form = this.lastEnterSubmit.form;
    if (!form || !form.contains(el)) {
      return false;
    }
    // Only treat the click as synthetic if the element is the form's default
    // submitter (the first submit button/input).
    const defaultSubmitter = form.querySelector('button[type="submit"], input[type="submit"], input[type="image"]');
    return defaultSubmitter === el;
  }

  private handleNavigate = (event?: Event): void => {
    // H-6: reject synthetic navigation events dispatched by the page.
    if (event && this.options.enforceIsTrusted && !event.isTrusted) {
      return;
    }
    if (!this.recordingFlag) {
      // A navigation may have created a fresh content-script instance. Resume
      // the persisted recording so we can record the navigate event.
      if (!this.resumeIfNeeded()) {
        return;
      }
    }
    const url = sanitizeRecordingUrl(window.location.href);
    // Avoid duplicate navigate events for the same URL (e.g. initial pageshow
    // on the start page, or repeated hashchange/popstate without real change).
    if (this.lastRecordedUrl === url) {
      return;
    }
    this.lastRecordedUrl = url;
    const timestamp = Date.now();
    // Bound the pre-navigation snapshot: on some destination documents the
    // snapshot promise never settles, which would hang the event queue and
    // silently drop the navigation from the recording. A timed-out snapshot
    // still records the navigate event without a paired snapshot.
    const snapshotPromise = this.options.captureSnapshotBeforeEachEvent
      ? this.withTimeout(this.captureSnapshot(timestamp, 'before-action'), 5_000, 'navigate snapshot timed out')
      : Promise.resolve();
    this.enqueueEvent(async () => {
      // The navigate event must survive snapshot failures on the destination
      // document: enqueueEvent swallows task errors, and lastRecordedUrl was
      // already advanced, so a throwing snapshot would silently drop the
      // navigation from the recording forever.
      try {
        this.linkSnapshotToNextEvent(await snapshotPromise);
      } catch (err) {
        console.warn('[ContentRecorder] navigate snapshot failed; recording event without it:', err);
      }
      this.recording!.events.push({ type: 'navigate', url, timestamp });
      // Navigations are rare and cross-origin documents are short-lived: a
      // forced background checkpoint here lets the event survive document
      // teardown even when the beforeunload async flush is dropped.
      this.persistState(true);
    });
  };

  private async withTimeout<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
    let timer: ReturnType<typeof setTimeout> | null = null;
    let didTimeout = false;
    try {
      const timeout = new Promise<never>((_, reject) => {
        timer = setTimeout(() => {
          didTimeout = true;
          reject(new Error(message));
        }, ms);
      });
      const guarded = promise.catch((err: unknown) => {
        if (!didTimeout) {
          throw err;
        }
      });
      return await Promise.race([guarded as Promise<T>, timeout]);
    } finally {
      if (timer !== null) {
        clearTimeout(timer);
      }
    }
  }

  private async captureSnapshot(
    timestamp?: number,
    phase?: 'initial' | 'before-action' | 'final',
    actionIndex?: number,
  ): Promise<DomSnapshot> {
    const localCapture = captureLocalDomWithReport(this.getDomAggregateOptions());
    const snapshot: DomSnapshot = {
      timestamp: timestamp ?? Date.now(),
      url: sanitizeRecordingUrl(window.location.href),
      selectorMap: {},
    };
    if (this.isV2()) {
      snapshot.phase = phase ?? 'before-action';
      snapshot.sequence = this.snapshotSequence++;
      snapshot.actionIndex = actionIndex;
      snapshot.capture = {
        status: localCapture.report.truncated ? 'partial' : 'complete',
        nodeCount: localCapture.report.nodeCount,
        redactionCount: localCapture.report.redactionCount,
        removedNodeCount: localCapture.report.removedNodeCount,
        frames: [],
      };
    }

    for (const el of this.findInteractableElements()) {
      const index = this.getElementIndex(el);
      const selector = this.inferSelector(el);
      snapshot.selectorMap[index] = this.buildDomElementInfo(el, index, selector);
    }

    const localTree = localCapture.domTree;

    if (localTree && IS_TOP_FRAME) {
      try {
        const frameTree = await this.withTimeout(
          requestAggregateFromBackground(this.getDomAggregateOptions()),
          3000,
          'frame aggregation timeout',
        );
        if (frameTree) {
          snapshot.domTree = mergeFrameTreeIntoDom(localTree, frameTree);
          if (snapshot.capture) {
            snapshot.capture.frames = collectFrameReports(frameTree);
            const frameSerialization = this.sumChildFrameSerialization(frameTree);
            snapshot.capture.nodeCount += frameSerialization.nodeCount;
            snapshot.capture.redactionCount += frameSerialization.redactionCount;
            snapshot.capture.removedNodeCount += frameSerialization.removedNodeCount;
            if (snapshot.capture.frames.some((frame) => frame.status !== 'captured')) {
              snapshot.capture.status = 'partial';
            }
          }
        } else {
          snapshot.domTree = localTree;
          if (snapshot.capture && this.domContainsIframe(localTree)) snapshot.capture.status = 'partial';
        }
      } catch {
        console.warn('[ContentRecorder] frame aggregation failed; using the sanitized local DOM');
        snapshot.domTree = localTree;
        if (snapshot.capture) {
          snapshot.capture.status = 'partial';
          snapshot.capture.frames = [{
            frameId: 0,
            parentFrameId: -1,
            url: sanitizeRecordingUrl(window.location.href),
            status: 'error',
            error: 'frame-aggregation-failed',
          }];
        }
      }
    } else {
      snapshot.domTree = localTree ?? undefined;
    }

    if (!this.recording) {
      return snapshot;
    }
    this.recording.snapshots.push(snapshot);
    return snapshot;
  }

  private sumChildFrameSerialization(frameTree: FrameTreeNode): SerializationReport {
    const total: SerializationReport = { nodeCount: 0, redactionCount: 0, removedNodeCount: 0, truncated: false };
    const visit = (node: FrameTreeNode) => {
      if (node.serialization) {
        total.nodeCount += node.serialization.nodeCount;
        total.redactionCount += node.serialization.redactionCount;
        total.removedNodeCount += node.serialization.removedNodeCount;
        total.truncated ||= node.serialization.truncated;
      }
      node.children.forEach(visit);
    };
    frameTree.children.forEach(visit);
    return total;
  }

  private domContainsIframe(node: DomNode): boolean {
    if (node.tagName === 'iframe') return true;
    return node.children?.some((child) => this.domContainsIframe(child)) ?? false;
  }

  private findInteractableElements(): Element[] {
    const selector = [
      'button',
      'a',
      'input',
      'select',
      'textarea',
      '[role="button"]',
      '[role="link"]',
      '[onclick]',
      '[contenteditable="true"]',
    ].join(', ');

    const found = new Set<Element>();
    const addIfVisible = (el: Element) => {
      if (this.isVisible(el)) {
        found.add(el);
      }
    };

    for (const el of Array.from(document.querySelectorAll(selector))) {
      addIfVisible(el);
    }

    for (const el of Array.from(document.querySelectorAll('*'))) {
      if (this.isScrollable(el)) {
        addIfVisible(el);
      }
    }

    // Include root elements so window-level scroll events can reference a valid
    // index in the snapshot selector map.
    for (const root of [document.documentElement, document.body]) {
      if (root && this.isVisible(root)) {
        found.add(root);
      }
    }

    return Array.from(found);
  }

  private isVisible(el: Element): boolean {
    if (!el.isConnected) {
      return false;
    }
    if ((el as HTMLElement).hidden) {
      return false;
    }
    const style = window.getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden') {
      return false;
    }
    return true;
  }

  private isScrollable(el: Element): boolean {
    if (el === document.documentElement) {
      return false;
    }
    const style = window.getComputedStyle(el);
    const overflow = `${style.overflow}${style.overflowX}${style.overflowY}`;
    if (!/auto|scroll/.test(overflow)) {
      return false;
    }
    const htmlEl = el as HTMLElement;
    return htmlEl.scrollHeight > htmlEl.clientHeight || htmlEl.scrollWidth > htmlEl.clientWidth;
  }

  private isInteractable(el: Element): boolean {
    const tag = el.tagName.toLowerCase();
    if (['button', 'a', 'input', 'select', 'textarea'].includes(tag)) {
      return true;
    }
    const role = el.getAttribute('role');
    if (role === 'button' || role === 'link') {
      return true;
    }
    if (el.hasAttribute('onclick')) {
      return true;
    }
    if (el.getAttribute('contenteditable') === 'true') {
      return true;
    }
    return this.isScrollable(el);
  }

  private getElementIndex(el: Element): number {
    const selector = this.inferSelector(el);
    const identity = this.elementIdentityKey(el, selector);
    const existing = this.selectorToIndex.get(identity);
    const now = Date.now();
    if (existing) {
      existing.lastUsedAt = now;
      return existing.index;
    }
    const index = this.nextIndex++;
    this.selectorToIndex.set(identity, { index, lastUsedAt: now });
    return index;
  }

  private elementIdentityKey(el: Element, selector: string): string {
    const semantic = [
      el.getAttribute('aria-label') ?? '',
      effectiveRole(el) ?? '',
      (el.textContent ?? '').replace(/\s+/g, ' ').trim(),
    ].join('\u0000');
    let fingerprint = 0x811c9dc5;
    for (let index = 0; index < semantic.length; index += 1) {
      fingerprint ^= semantic.charCodeAt(index);
      fingerprint = Math.imul(fingerprint, 0x01000193);
    }
    return `${selector}\u0000${(fingerprint >>> 0).toString(16).padStart(8, '0')}`;
  }

  private inferSelector(el: Element): string {
    if (el.id) {
      return `#${escapeIdentifier(el.id)}`;
    }
    const testId = el.getAttribute('data-testid');
    if (testId) {
      return `[data-testid="${escapeIdentifier(testId)}"]`;
    }
    const tag = el.tagName.toLowerCase();
    const stableClasses = this.getStableClasses(el);
    if (stableClasses) {
      return `${tag}.${stableClasses}`;
    }

    const parent = el.parentElement;
    const siblings = Array.from(parent?.children || []).filter((e) => e.tagName === el.tagName);
    const position = siblings.indexOf(el) + 1;
    const nth = siblings.length > 1 ? `:nth-of-type(${position})` : '';
    if (parent && !['body', 'html'].includes(parent.tagName.toLowerCase())) {
      return `${parent.tagName.toLowerCase()} > ${tag}${nth}`;
    }
    return `${tag}${nth}`;
  }

  private getStableClasses(el: Element): string {
    return Array.from(el.classList)
      .filter((c) => c.length > 0 && !/[a-f0-9]{8,}/i.test(c))
      .map((c) => escapeIdentifier(c))
      .join('.');
  }

  private semanticElementText(el: Element): string {
    const parts: string[] = [];
    const visit = (node: Node): void => {
      if (node.nodeType === Node.TEXT_NODE) {
        const text = node.textContent?.replace(/\s+/g, ' ').trim();
        if (text) parts.push(text);
        return;
      }
      if (node.nodeType !== Node.ELEMENT_NODE) return;
      const element = node as Element;
      if (['script', 'style', 'noscript', 'template'].includes(element.tagName.toLowerCase())) return;
      element.childNodes.forEach(visit);
    };
    el.childNodes.forEach(visit);
    return parts.join(' ').replace(/\s+/g, ' ').trim();
  }

  private buildDomElementInfo(el: Element, index: number, selector: string): DomElementInfo {
    const rect =
      typeof el.getBoundingClientRect === 'function'
        ? el.getBoundingClientRect()
        : { x: 0, y: 0, width: 0, height: 0 };
    const normalize = (value: string | null | undefined): string | undefined => {
      if (!value) return undefined;
      const redacted = redactSensitiveText(value).value;
      return this.isV2() ? redacted : redacted.slice(0, 200);
    };
    const sensitive = isSensitiveElement(el);
    const elementText = this.semanticElementText(el);
    const ariaLabel = el.getAttribute('aria-label');
    const placeholder = (el as HTMLInputElement).placeholder || el.getAttribute('placeholder');
    return {
      index,
      tagName: el.tagName.toLowerCase(),
      selector,
      stableSelector: selector,
      text: sensitive && elementText ? '[REDACTED]' : normalize(elementText),
      ariaLabel: sensitive && ariaLabel ? '[REDACTED]' : normalize(ariaLabel),
      role: effectiveRole(el),
      placeholder: sensitive && placeholder ? '[REDACTED]' : normalize(placeholder),
      name: (el as HTMLInputElement).name || el.getAttribute('name') || undefined,
      boundingRect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
    };
  }
}

// Bootstrap: create a singleton recorder for the content script and wire
// Chrome messaging if available. Guard against double-injection.
//
// Note: `window` here is the content-script isolated world's window, not the
// page's window. MV3 content scripts run in an isolated world that shares the
// DOM but has its own JS globals. `win.__openCrawlerRecorder` is therefore
// invisible to the page and only serves to dedupe re-injection of this script.
if (typeof window !== 'undefined' && typeof document !== 'undefined') {
  const win = window as unknown as Record<string, unknown>;
  if (!win.__openCrawlerRecorder) {
    const defaultRecorder = new ContentRecorder();
    try {
      defaultRecorder.attachChromeMessaging();
    } catch (err) {
      console.warn('[ContentRecorder] failed to attach chrome messaging:', err);
    }
    try {
      if (!defaultRecorder.resumeIfNeeded()) {
        void defaultRecorder.resumeFromBackgroundIfNeeded();
      }
    } catch (err) {
      console.warn('[ContentRecorder] failed to resume recording:', err);
    }
    win.__openCrawlerRecorder = defaultRecorder;
  }
}
