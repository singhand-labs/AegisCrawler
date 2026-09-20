import type { ActionType } from '../../src/rule-engine/types/action';

export type CoverageClass =
  | 'covered-public'
  | 'covered-fixture'
  | 'blocked-by-policy'
  | 'unsupported';

export interface CoverageEntry {
  id: string;
  surface: 'browser-action' | 'extension' | 'admin' | 'mcp' | 'lifecycle';
  classification: CoverageClass;
  evidence: readonly string[];
  rationale: string;
}

const PUBLIC_ACTIONS = [
  'click', 'type', 'focus', 'scrollBy', 'scrollIntoView', 'navigate', 'goBack',
  'waitFor', 'waitForText', 'waitForUrl', 'waitForNetworkIdle', 'extract',
  'extractText', 'extractAttribute', 'extractTable', 'extractPageInfo',
  'screenshot', 'sendResult',
] as const satisfies readonly ActionType[];

const FIXTURE_ACTIONS = [
  'doubleClick', 'rightClick', 'middleClick', 'hover', 'hoverClick', 'moveMouse',
  'pressAndHold', 'dragAndDrop', 'dragBy', 'slide', 'paste', 'clear', 'select',
  'check', 'selectRadio', 'typeAndSelect', 'uploadFile', 'blur', 'tabToNext',
  'tabToPrevious', 'pressKey', 'keyCombination', 'scrollTo', 'scrollToBottom',
  'scrollToTop', 'pageDown', 'pageUp', 'reload', 'goForward', 'setViewport',
  'waitForTimeout', 'waitForElementHidden', 'waitForElementVisible',
  'waitForFunction', 'readPause', 'extractHtml', 'extractJson', 'captureRequest',
  'transform', 'filter', 'deduplicate', 'merge', 'validateData', 'saveSnapshot',
  'checkpoint', 'flushResults', 'heartbeat', 'cleanup', 'circuitBreaker',
  'checkQuota', 'setTag', 'logMetric', 'abort', 'recover', 'if', 'switch', 'loop',
  'retry', 'break', 'continue', 'exit', 'group', 'parallel', 'sleep', 'evaluate',
  'setStyle', 'removeElement', 'blockRequest', 'unblockRequest', 'setCookie',
  'getCookie', 'deleteCookie', 'setLocalStorage', 'getLocalStorage',
  'removeLocalStorage', 'setSessionStorage', 'getSessionStorage',
  'removeSessionStorage', 'setAttribute', 'removeAttribute', 'openTab', 'closeTab',
  'switchTab', 'handleDialog', 'handleDownload', 'setUserAgent',
  'setExtraHeaders', 'setLanguage', 'setTimezone', 'refreshSession',
  'requestHuman', 'sendLog', 'sendScreenshot', 'sendHtml', 'updateStatus',
  'emitEvent',
] as const satisfies readonly ActionType[];

const POLICY_BLOCKED_ACTIONS = ['solveCaptcha'] as const satisfies readonly ActionType[];
const UNSUPPORTED_ACTIONS = [] as const satisfies readonly ActionType[];

type ClassifiedAction =
  | typeof PUBLIC_ACTIONS[number]
  | typeof FIXTURE_ACTIONS[number]
  | typeof POLICY_BLOCKED_ACTIONS[number]
  | typeof UNSUPPORTED_ACTIONS[number];
type AssertNever<T extends never> = T;
type _AllActionsClassified = AssertNever<Exclude<ActionType, ClassifiedAction>>;
type _NoUnknownActions = AssertNever<Exclude<ClassifiedAction, ActionType>>;

function browserEntries(
  actions: readonly ActionType[],
  classification: CoverageClass,
  evidence: readonly string[],
  rationale: string,
): CoverageEntry[] {
  return actions.map((action) => ({
    id: `action:${action}`,
    surface: 'browser-action',
    classification,
    evidence,
    rationale,
  }));
}

const PRODUCT_JOURNEYS: readonly CoverageEntry[] = [
  {
    id: 'extension:record-stop-resume',
    surface: 'extension',
    classification: 'covered-fixture',
    evidence: ['npm run test:e2e:staging', 'npm run test:e2e:replay-mv3-stability'],
    rationale: 'Recording and production-MV3 replay lifecycle use owned pages.',
  },
  {
    id: 'extension:requirement-select-enter-confirm',
    surface: 'extension',
    classification: 'covered-fixture',
    evidence: ['npm run test:e2e:staging', 'npm run test:e2e:live-workflow'],
    rationale: 'Candidate selection, manual entry, generation, replay, and repair are covered.',
  },
  {
    id: 'admin:authentication-navigation',
    surface: 'admin',
    classification: 'covered-fixture',
    evidence: ['npm test', 'npm run test:e2e:staging'],
    rationale: 'Login, protected navigation, dashboard, logout, and errors remain local.',
  },
  {
    id: 'admin:rule-lifecycle-editor',
    surface: 'admin',
    classification: 'covered-fixture',
    evidence: ['npm test', 'npm run test:e2e:staging'],
    rationale: 'Inspect, edit, undo, save, approve/reject, toggle, enhance, and delete are covered.',
  },
  {
    id: 'admin:task-lifecycle-export',
    surface: 'admin',
    classification: 'covered-fixture',
    evidence: ['npm test', 'npm run test:e2e:staging', 'npm run test:e2e:worker'],
    rationale: 'Create, inspect, cancel, retry, paginate, export, and worker execution are covered.',
  },
  {
    id: 'admin:schedule-lifecycle',
    surface: 'admin',
    classification: 'covered-fixture',
    evidence: ['npm test', 'npm run test:e2e:staging'],
    rationale: 'Create, validate, toggle, trigger, and delete remain local mutations.',
  },
  {
    id: 'mcp:token-and-tool-lifecycle',
    surface: 'mcp',
    classification: 'covered-fixture',
    evidence: ['npm run test:e2e:staging', 'cd server && go test ./...'],
    rationale: 'Token lifecycle, permissions, isolation, tools, and Admin parity are covered.',
  },
  {
    id: 'lifecycle:restart-backup-audit',
    surface: 'lifecycle',
    classification: 'covered-fixture',
    evidence: ['npm run test:e2e:staging', 'npm run test:e2e:production'],
    rationale: 'Persistence, backup, lineage, audit, and recovery are deterministic.',
  },
  {
    id: 'public:wikipedia-readonly',
    surface: 'lifecycle',
    classification: 'covered-public',
    evidence: ['npm run test:e2e:mainstream-readonly'],
    rationale: 'Provider-free navigation and extraction use an independent oracle.',
  },
  {
    id: 'public:github-readonly',
    surface: 'lifecycle',
    classification: 'covered-public',
    evidence: ['npm run test:e2e:mainstream-readonly'],
    rationale: 'Provider-free public repository navigation uses an independent oracle.',
  },
  {
    id: 'public:mainstream-search-browse',
    surface: 'lifecycle',
    classification: 'covered-public',
    evidence: ['npm run test:e2e:mainstream-readonly'],
    rationale: 'Provider-free Bing and DuckDuckGo adapters share suggestion browsing, result scrolling, reviewed reading, and Back-restoration visible-DOM contracts.',
  },
  {
    id: 'policy:authentication-consent-captcha',
    surface: 'lifecycle',
    classification: 'blocked-by-policy',
    evidence: ['npm run test:e2e:mainstream-readonly'],
    rationale: 'Public qualification stops at login, consent, CAPTCHA, or rate limiting.',
  },
] as const;

export const USER_ACTION_COVERAGE: readonly CoverageEntry[] = [
  ...browserEntries(PUBLIC_ACTIONS, 'covered-public',
    ['npm run test:e2e:mainstream-readonly', 'npm run test:e2e:live-workflow'],
    'Safe read-only interaction runs on predeclared public pages.'),
  ...browserEntries(FIXTURE_ACTIONS, 'covered-fixture',
    ['npm test', 'npm run test:e2e:browser', 'npm run test:e2e:staging', 'npm run test:e2e:worker'],
    'Mutation, browser state, advanced input, and failures run only on owned fixtures.'),
  ...browserEntries(POLICY_BLOCKED_ACTIONS, 'blocked-by-policy',
    ['docs/security.md', 'docs/testing.md'],
    'Aegis must request a human or stop; automated CAPTCHA solving is never qualification behavior.'),
  ...browserEntries(UNSUPPORTED_ACTIONS, 'unsupported', ['docs/dsl-design.md'],
    'Unsupported actions remain explicit in the report.'),
  ...PRODUCT_JOURNEYS,
];

export function buildCoverageReport() {
  const ids = new Set<string>();
  for (const entry of USER_ACTION_COVERAGE) {
    if (ids.has(entry.id)) throw new Error(`duplicate coverage id: ${entry.id}`);
    ids.add(entry.id);
    if (entry.evidence.length === 0) throw new Error(`coverage entry lacks evidence: ${entry.id}`);
  }
  const counts = USER_ACTION_COVERAGE.reduce<Record<CoverageClass, number>>(
    (result, entry) => {
      result[entry.classification] += 1;
      return result;
    },
    { 'covered-public': 0, 'covered-fixture': 0, 'blocked-by-policy': 0, unsupported: 0 },
  );
  return {
    schema: 'aegiscrawler.user-action-coverage.v1',
    generatedAt: new Date().toISOString(),
    counts,
    entries: USER_ACTION_COVERAGE,
  };
}
