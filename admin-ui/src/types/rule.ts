/**
 * Frontend mirror of the AegisCrawler rule DSL.
 *
 * This file intentionally stays close to `src/rule-engine/types/action.ts`
 * but only includes the fields needed to render, validate and edit rules
 * in the admin UI. Unknown/future fields are accepted via Record indexing.
 */

export type Priority = 'low' | 'normal' | 'high';
export type RuleApprovalStatus = 'pending' | 'approved' | 'rejected';

export interface Target {
  $ref?: string;
  selector?: string;
  xpath?: string;
  text?: string;
  ariaLabel?: string;
  role?: string;
  roleName?: string;
  position?: { x: number; y: number };
  frame?: string | number;
  shadowPath?: string[];
  index?: number;
  multiple?: boolean;
  timeout?: number;
  visible?: boolean;
}

export interface Condition {
  type: string;
  target?: Target;
  text?: string;
  pattern?: string;
  value?: string | number | boolean;
  script?: string;
  timeout?: number;
}

export interface Humanize {
  preDelay?: [number, number];
  postDelay?: [number, number];
  moveMouse?: boolean;
  mousePath?: 'linear' | 'bezier' | 'random' | 'natural';
  mouseSpeed?: [number, number];
  randomOffset?: number | { x: number; y: number };
  typingDelay?: [number, number];
  typingMistakeRate?: number;
  typingErrorCorrection?: boolean;
  scrollSpeed?: [number, number];
  scrollSteps?: number;
  scrollPause?: [number, number];
  wobble?: number;
  natural?: boolean;
}

export interface BaseAction {
  id?: string;
  description?: string;
  timeout?: number;
  softTimeout?: number;
  hardTimeout?: number;
  retry?: number | Record<string, unknown>;
  onError?: string;
  critical?: boolean;
  checkpoint?: boolean;
  condition?: Condition;
  humanize?: Humanize;
  tags?: Record<string, string>;
}

export type ActionType =
  // mouse
  | 'click'
  | 'doubleClick'
  | 'rightClick'
  | 'middleClick'
  | 'hover'
  | 'hoverClick'
  | 'moveMouse'
  | 'pressAndHold'
  | 'dragAndDrop'
  | 'dragBy'
  | 'slide'
  // input
  | 'type'
  | 'paste'
  | 'clear'
  | 'select'
  | 'check'
  | 'selectRadio'
  | 'typeAndSelect'
  | 'uploadFile'
  | 'focus'
  | 'blur'
  | 'tabToNext'
  | 'tabToPrevious'
  | 'pressKey'
  | 'keyCombination'
  // scroll/nav/viewport
  | 'scrollTo'
  | 'scrollBy'
  | 'scrollToBottom'
  | 'scrollToTop'
  | 'pageDown'
  | 'pageUp'
  | 'navigate'
  | 'reload'
  | 'goBack'
  | 'goForward'
  | 'setViewport'
  // wait
  | 'waitFor'
  | 'waitForText'
  | 'waitForUrl'
  | 'waitForTimeout'
  | 'waitForElementHidden'
  | 'waitForElementVisible'
  | 'waitForNetworkIdle'
  | 'waitForFunction'
  | 'readPause'
  // extract
  | 'extract'
  | 'extractText'
  | 'extractAttribute'
  | 'extractHtml'
  | 'extractJson'
  | 'extractTable'
  | 'extractPageInfo'
  | 'screenshot'
  | 'captureRequest'
  // transform
  | 'transform'
  | 'filter'
  | 'deduplicate'
  | 'merge'
  | 'validateData'
  | 'saveSnapshot'
  // ops/recovery
  | 'checkpoint'
  | 'flushResults'
  | 'heartbeat'
  | 'cleanup'
  | 'circuitBreaker'
  | 'checkQuota'
  | 'setTag'
  | 'logMetric'
  | 'abort'
  | 'recover'
  // flow control
  | 'if'
  | 'switch'
  | 'loop'
  | 'retry'
  | 'break'
  | 'continue'
  | 'exit'
  | 'group'
  | 'parallel'
  | 'sleep'
  // page
  | 'evaluate'
  | 'setStyle'
  | 'removeElement'
  | 'blockRequest'
  | 'unblockRequest'
  | 'setCookie'
  | 'getCookie'
  | 'deleteCookie'
  | 'setLocalStorage'
  | 'getLocalStorage'
  | 'removeLocalStorage'
  | 'setSessionStorage'
  | 'getSessionStorage'
  | 'removeSessionStorage'
  | 'setAttribute'
  | 'removeAttribute'
  | 'scrollIntoView'
  // browser
  | 'openTab'
  | 'closeTab'
  | 'switchTab'
  | 'handleDialog'
  | 'handleDownload'
  | 'setUserAgent'
  | 'setExtraHeaders'
  | 'setLanguage'
  | 'setTimezone'
  // auth/human
  | 'refreshSession'
  | 'requestHuman'
  | 'solveCaptcha'
  // output
  | 'sendResult'
  | 'sendLog'
  | 'sendScreenshot'
  | 'sendHtml'
  | 'updateStatus'
  | 'emitEvent'
  // fallback
  | string;

export interface Action extends BaseAction, Record<string, unknown> {
  action: ActionType;
  target?: Target;
  steps?: Action[];
  then?: Action[];
  else?: Action[];
  cases?: Array<{ value: string; steps: Action[] }>;
  default?: Action[];
}

export interface RuleHooks {
  beforeAll?: Action[];
  afterAll?: Action[];
  onError?: Action[];
  cleanup?: Action[];
}

export interface Rule {
  id: string;
  version: string;
  name: string;
  domain: unknown;
  urlPattern: unknown;
  enabled: boolean;
  priority: Priority | '';
  entry: string;
  variables: Record<string, unknown>;
  selectors: Record<string, unknown>;
  humanize: Record<string, unknown>;
  steps: Action[];
  output: Record<string, unknown>;
  sendPolicy: Record<string, unknown>;
  hooks: RuleHooks;
  tags: Record<string, unknown>;
  owner: string;
  approvalStatus: RuleApprovalStatus;
  source?: string;
  createdAt: string;
  updatedAt: string;
}
