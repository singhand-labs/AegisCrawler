import type { Rule, Action, Target, Humanize } from '../rule-engine/types/action';

export { Rule, Action, Target, Humanize };

export interface PageAgentRecording {
  version: '1.0.0' | '2.0.0';
  meta: RecordingMeta;
  events: PageAgentEvent[];
  snapshots: DomSnapshot[];
  /** User-authored, bounded collection-intent marks captured during recording. */
  marks?: PageMark[];
  /** Client-enforced limits for semantic recording v2. */
  limits?: RecordingLimits;
  /** Non-fatal warnings emitted before a configured recording limit. */
  warnings?: RecordingWarning[];
  /** Why and when a completed v2 recording stopped. */
  termination?: RecordingTermination;
}

export type PageMarkRole = 'listItem' | 'field' | 'nextPage' | 'input' | 'exclude';

export interface PageMark {
  id: string;
  timestamp: number;
  url: string;
  role: PageMarkRole;
  /** User-authored intent note. It is not page evidence and must stay bounded. */
  note: string;
  element: DomElementInfo;
  /** Event count at the time the mark was captured, used for prompt timeline placement. */
  actionIndex?: number;
  /** Semantic snapshot sequence that authenticated the marked element, when available. */
  snapshotSequence?: number;
  /** Server canonical page state (usually derived from the marked snapshot URL), when available. */
  state?: string;
  /** Server-side stable id derived without note/page text; used in selector-catalog metadata. */
  canonicalId?: string;
}

export interface RecordingMeta {
  startUrl: string;
  title: string;
  recordedAt: string;
  domain: string;
  userAgent?: string;
  endedAt?: string;
  /** ID returned after the sanitized recording is durably persisted by the server. */
  serverRecordingId?: string;
  semanticDomVersion?: '1';
  sanitizationVersion?: 'extension-v1' | 'extension-v2';
}

export interface RecordingLimits {
  maxActions: number;
  maxDurationMs: number;
  maxBytes: number;
  warningThreshold: number;
}

export type RecordingLimitKind = 'actions' | 'duration' | 'size';

export interface RecordingWarning {
  kind: RecordingLimitKind | 'capture';
  message: string;
  timestamp: number;
}

export type RecordingStopReason =
  | 'user'
  | 'action-limit'
  | 'duration-limit'
  | 'size-limit'
  | 'capture-error';

export interface RecordingTermination {
  reason: RecordingStopReason;
  message: string;
  timestamp: number;
  complete: boolean;
}

export type PageAgentEvent =
  | NavigateEvent
  | ClickEvent
  | InputTextEvent
  | SelectOptionEvent
  | ScrollEvent
  | ExecuteJavascriptEvent
  | WaitEvent
  | ExtractEvent
  | SubmitFormEvent;

export interface BaseEvent {
  type: string;
  timestamp: number;
}

export interface NavigateEvent extends BaseEvent {
  type: 'navigate';
  url: string;
}

export interface ClickEvent extends BaseEvent {
  type: 'click';
  index: number;
  annotation?: EventAnnotation;
}

export interface InputTextEvent extends BaseEvent {
  type: 'inputText';
  index: number;
  text: string;
  submit?: boolean;
}

export interface SubmitFormEvent extends BaseEvent {
  type: 'submitForm';
  index: number;
  submitterIndex?: number;
}

export interface SelectOptionEvent extends BaseEvent {
  type: 'selectOption';
  index: number;
  optionText: string;
}

export interface ScrollEvent extends BaseEvent {
  type: 'scroll';
  direction: 'down' | 'up' | 'left' | 'right';
  amount: number;
  unit: 'pages' | 'pixels';
  index?: number;
}

export interface ExecuteJavascriptEvent extends BaseEvent {
  type: 'executeJavascript';
  script: string;
}

export interface WaitEvent extends BaseEvent {
  type: 'wait';
  duration: number;
}

export interface ExtractEvent extends BaseEvent {
  type: 'extract';
  index: number;
  fieldName: string;
  mode: 'text' | 'html' | 'attribute';
  attribute?: string;
  annotation?: 'listItem' | 'field';
}

export type EventAnnotation = 'nextPage' | 'login' | 'captcha' | 'listItem' | 'field';

export interface DomAttribute {
  name: string;
  value: string;
}

export interface DomSanitizationEvidence {
  /**
   * True when the semantic representation cannot prove that live HTML is
   * byte-for-byte free of removed or transformed markup.
   */
  markupAltered?: true;
  /**
   * True when live text/JSON/count/table content may include omitted,
   * redacted, normalized, or truncated descendants.
   */
  contentOmitted?: true;
  /**
   * Retained attribute names whose recorded values differ from live markup.
   * A single "*" means the bounded exact-name set was unavailable.
   */
  alteredAttributes?: string[];
}

export interface DomNode {
  type: 'element' | 'text';
  tagName?: string;
  attributes?: DomAttribute[];
  children?: DomNode[];
  text?: string;
  /**
   * `false` when safe recording-time evidence proves this node or an ancestor
   * was non-rendered. Omitted for rendered nodes and by older recordings.
   */
  rendered?: boolean;
  /** Fail-closed provenance for sanitized or normalized live-DOM material. */
  sanitization?: DomSanitizationEvidence;
  /** Only present when this node represents an iframe boundary. */
  frameOrigin?: 'same-origin' | 'cross-origin';
  /** Present for cross-origin iframes where we cannot read contentDocument. */
  frameSrc?: string;
  /** Present when this iframe node is a placeholder waiting for background aggregation. */
  framePlaceholder?: boolean;
  /** Present when an iframe placeholder was not merged with a captured frame because the URLs did not match. */
  frameMatched?: boolean;
  /** Explicit v2 health state for this iframe boundary. */
  frameStatus?: 'captured' | 'unavailable' | 'error';
  /** Stable, sanitized error code when frame capture was unavailable. */
  frameError?: string;
}

export interface DomSnapshot {
  timestamp: number;
  url: string;
  selectorMap: Record<number, DomElementInfo>;
  /** Optional full DOM tree captured at this snapshot. */
  domTree?: DomNode;
  /** Semantic recording v2 snapshot position. */
  phase?: 'initial' | 'before-action' | 'final';
  sequence?: number;
  actionIndex?: number;
  capture?: SnapshotCaptureReport;
}

export interface FrameCaptureReport {
  frameId: number;
  parentFrameId: number;
  url: string;
  status: 'captured' | 'unavailable' | 'error';
  error?: string;
}

export interface SnapshotCaptureReport {
  status: 'complete' | 'partial' | 'failed';
  nodeCount: number;
  redactionCount: number;
  removedNodeCount: number;
  frames: FrameCaptureReport[];
}

export interface DomElementInfo {
  index: number;
  tagName: string;
  selector: string;
  stableSelector?: string;
  text?: string;
  ariaLabel?: string;
  role?: string;
  placeholder?: string;
  name?: string;
  boundingRect: {
    x: number;
    y: number;
    width: number;
    height: number;
  };
}

export interface ConvertOptions {
  optimizeSelectors?: boolean;
  guessVariables?: boolean;
  defaultHumanize?: Humanize;
  ruleIdPrefix?: string;
}
