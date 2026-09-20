import { deepStrictEqual, strictEqual } from 'node:assert';
import type { BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';

export const IANA_ENTRY_URL = 'https://www.iana.org/help/example-domains';
export const IANA_DESTINATION_URL = 'https://www.iana.org/domains/reserved';
export const IANA_COST_BUDGET_USD = 1.5;
export const IANA_ACTION_PAUSE_MS = 2_000;
export const IANA_ARTICLE_ROOT_SELECTOR = 'main';
export const IANA_LINK_TEXT = 'IANA-managed Reserved Domains';
export const IANA_NON_CONTENT_SELECTOR = 'header,footer,nav,#sidenav,[role="navigation"]';

export const IANA_LINK_ROUNDTRIP_REQUIREMENT = {
  title: 'Collect the visible IANA Example Domains summary after a link round trip',
  description: [
    'On the public IANA Example Domains page, visibly click the IANA-managed Reserved Domains link,',
    'enter that destination page, then return to the original Example Domains page.',
    'After the round trip, collect exactly one visible summary with title and introduction strings.',
    'Use the visible main content as the extraction root and do not navigate outside www.iana.org.',
  ].join(' '),
  requiredInputs: [],
  optionalInputs: [],
  outputFields: [
    { name: 'title', type: 'string', description: 'Visible page heading after returning' },
    { name: 'introduction', type: 'string', description: 'First visible content paragraph after returning' },
  ],
  sampleOutput: {
    title: 'Example Domains',
    introduction: 'A visible introductory paragraph.',
  },
};

export interface IANAArticleRow {
  title: string;
  introduction: string;
}

export function canonicalIANAURL(rawURL: string): string {
  const url = new URL(rawURL);
  strictEqual(url.protocol, 'https:', 'IANA round trip requires HTTPS');
  strictEqual(url.hostname, 'www.iana.org', 'IANA round trip must remain on www.iana.org');
  url.hash = '';
  url.search = '';
  return url.toString();
}

export function assertReviewedIANAURL(rawURL: string): void {
  const canonical = canonicalIANAURL(rawURL);
  if (canonical !== IANA_ENTRY_URL && canonical !== IANA_DESTINATION_URL) {
    throw new Error(`IANA round trip blocked unreviewed page: ${canonical}`);
  }
}

export function findIANABoundary(bodyText: string, title: string): string | undefined {
  const sample = `${title}\n${bodyText.slice(0, 30_000)}`.toLowerCase();
  return [
    'verify you are human', 'complete the security check', 'captcha',
    'sign in to continue', 'log in to continue', 'rate limit exceeded',
  ].find((phrase) => sample.includes(phrase));
}

export async function assertIANAPageSafe(page: Page): Promise<void> {
  assertReviewedIANAURL(page.url());
  const boundary = findIANABoundary(
    await page.locator('body').innerText(),
    await page.title(),
  );
  if (boundary) throw new Error(`IANA environment blocked: ${boundary}`);
}

export async function ianaLinkRoundtripDemo(
  page: Page,
  ensureRecordingReady?: () => Promise<void>,
): Promise<void> {
  await page.bringToFront();
  await assertIANAPageSafe(page);
  const link = page.getByRole('link', { name: IANA_LINK_TEXT, exact: true }).first();
  await link.waitFor({ state: 'visible', timeout: 30_000 });
  await link.click();
  await page.waitForURL(IANA_DESTINATION_URL, { waitUntil: 'domcontentloaded' });
  await ensureRecordingReady?.();
  await assertIANAPageSafe(page);
  await page.waitForTimeout(IANA_ACTION_PAUSE_MS);
  const response = await page.goBack({ waitUntil: 'domcontentloaded' });
  if (!response && page.url() !== IANA_ENTRY_URL) {
    throw new Error('IANA browser Back did not return a document response');
  }
  await page.waitForURL(IANA_ENTRY_URL, { waitUntil: 'domcontentloaded' });
  await ensureRecordingReady?.();
  await assertIANAPageSafe(page);
  await page.waitForTimeout(IANA_ACTION_PAUSE_MS);
}

export async function prepareIANAContext(context: BrowserContext): Promise<void> {
  await context.route('**/*', async (route) => {
    const request = route.request();
    if (request.resourceType() !== 'document') {
      await route.continue();
      return;
    }
    try {
      assertReviewedIANAURL(request.url());
      await route.continue();
    } catch {
      await route.abort('blockedbyclient');
    }
  });
}

export async function prepareIANAPage(page: Page): Promise<void> {
  await page.evaluate((selector) => {
    document.querySelectorAll(selector).forEach((element) => element.remove());
  }, IANA_NON_CONTENT_SELECTOR);
  const link = page.getByRole('link', { name: IANA_LINK_TEXT, exact: true }).first();
  await link.waitFor({ state: 'visible', timeout: 10_000 });
  if (await page.locator(IANA_NON_CONTENT_SELECTOR).count() !== 0) {
    throw new Error('IANA page preparation could not exclude non-content navigation');
  }
  await assertIANAPageSafe(page);
}

export async function collectIANAArticleOracle(page: Page): Promise<IANAArticleRow> {
  await assertIANAPageSafe(page);
  strictEqual(canonicalIANAURL(page.url()), IANA_ENTRY_URL,
    'IANA extraction must occur after returning to the entry page');
  const main = page.locator(IANA_ARTICLE_ROOT_SELECTOR).first();
  const title = (await main.locator('h1').first().innerText()).trim();
  const introduction = (await main.locator('p').filter({ hasText: /\S/ }).first().innerText()).trim();
  strictEqual(title, 'Example Domains', 'unexpected IANA page heading');
  if (introduction.length < 80) throw new Error('IANA introduction was unexpectedly short');
  return { title, introduction };
}

export function assertIANARows(
  rows: unknown[],
  _outputSchema: unknown,
  expected: IANAArticleRow,
  phase: string,
): void {
  strictEqual(rows.length, 1, `${phase} must emit exactly one IANA page row`);
  const normalizeRow = (value: unknown): unknown => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return value;
    return Object.fromEntries(Object.entries(value).map(([name, field]) => [
      name,
      typeof field === 'string' ? field.replace(/\s+/g, ' ').trim() : field,
    ]));
  };
  deepStrictEqual(
    normalizeRow(rows[0]),
    normalizeRow(expected),
    `${phase} IANA row must match the independent visible oracle`,
  );
}

export function assertIANARoundtripRecording(recording: PageAgentRecording): void {
  strictEqual(canonicalIANAURL(recording.meta.startUrl), IANA_ENTRY_URL,
    'IANA recording must start on the reviewed entry page');
  strictEqual(recording.events.length, 3,
    'IANA recording must contain exactly click, enter, and return events');
  strictEqual(recording.snapshots.length, recording.events.length + 2,
    'IANA recording must contain initial, per-action, and final snapshots');
  const [click, enter, exit] = recording.events;
  strictEqual(click.type, 'click', 'IANA first recorded action must be the link click');
  strictEqual(enter.type, 'navigate', 'IANA link entry must be recorded as navigation');
  strictEqual(exit.type, 'navigate', 'IANA browser Back must be recorded as navigation');
  if (enter.type === 'navigate') {
    strictEqual(canonicalIANAURL(enter.url), IANA_DESTINATION_URL,
      'IANA entry navigation must reach the reviewed destination');
  }
  if (exit.type === 'navigate') {
    strictEqual(canonicalIANAURL(exit.url), IANA_ENTRY_URL,
      'IANA exit navigation must return to the reviewed entry page');
  }
  if (click.type === 'click') {
    const target = recording.snapshots
      .map((snapshot) => snapshot.selectorMap[click.index])
      .find(Boolean);
    if (!target || target.tagName.toLowerCase() !== 'a' || !target.selector.trim()) {
      throw new Error('IANA click must resolve to a recorded anchor target');
    }
  }
  if (recording.snapshots.some((snapshot) => snapshot.capture?.status !== 'complete')) {
    throw new Error('IANA recording requires complete semantic snapshots');
  }
  if (recording.events.some((event) => event.type === 'executeJavascript')) {
    throw new Error('IANA recording forbids script execution');
  }
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

export function assertIANARoundtripRule(rule: unknown): void {
  if (!plainObject(rule) || !Array.isArray(rule.steps)) throw new Error('IANA rule must contain steps');
  const actions = rule.steps.filter(plainObject);
  const clickIndex = actions.findIndex((step) => step.action === 'click');
  const destinationIndex = actions.findIndex((step) =>
    step.action === 'navigate' && step.url === IANA_DESTINATION_URL);
  const returnIndex = actions.findIndex((step) =>
    step.action === 'navigate' && step.url === IANA_ENTRY_URL);
  const extractIndex = actions.findIndex((step) => step.action === 'extract');
  if (!(clickIndex >= 0 && destinationIndex > clickIndex
    && returnIndex > destinationIndex && extractIndex > returnIndex)) {
    throw new Error('IANA rule must replay click, enter, return, then extraction in order');
  }
  const clickTarget = actions[clickIndex].target;
  const selectors = plainObject(rule.selectors) ? rule.selectors : {};
  const ref = plainObject(clickTarget) && typeof clickTarget.$ref === 'string'
    ? clickTarget.$ref
    : undefined;
  const referencedClick = ref ? selectors[ref] : undefined;
  const resolvedClick = plainObject(clickTarget) && plainObject(referencedClick)
    ? { ...referencedClick, ...clickTarget }
    : clickTarget;
  const clickSelector = plainObject(resolvedClick) ? resolvedClick.selector : undefined;
  const clickText = plainObject(resolvedClick) ? resolvedClick.text : undefined;
  const clickRole = plainObject(resolvedClick) ? resolvedClick.role : undefined;
  const clickRoleName = plainObject(resolvedClick) ? resolvedClick.roleName : undefined;
  if (typeof clickSelector !== 'string'
    || !/(^|[\s>+~])a(?:$|[\s:\[.#>+~])/i.test(clickSelector)
    || clickRole !== 'link'
    || (clickText !== IANA_LINK_TEXT && clickRoleName !== IANA_LINK_TEXT)) {
    throw new Error(`IANA replay click must resolve to the reviewed ${IANA_LINK_TEXT} anchor`);
  }
  const extract = actions[extractIndex];
  const extractTarget = plainObject(extract.target) ? extract.target : undefined;
  const target = extractTarget?.selector;
  if ((target !== IANA_ARTICLE_ROOT_SELECTOR && target !== '#body')
    || extractTarget?.visible !== true) {
    throw new Error('IANA extraction must target the isolated visible main content');
  }
  if (!plainObject(extract.fields)
    || Object.keys(extract.fields).sort().join(',') !== 'introduction,title') {
    throw new Error('IANA extraction must contain exactly title and introduction');
  }
  for (const [name, tag] of [['title', 'h1'], ['introduction', 'p']] as const) {
    const field = extract.fields[name];
    if (!plainObject(field) || field.visible !== true || typeof field.selector !== 'string'
      || !new RegExp(`(^|[\\s>+~])${tag}(?:$|[\\s:\\[.#>+~])`, 'i').test(field.selector)) {
      throw new Error(`IANA field ${name} requires its reviewed visible semantic selector`);
    }
  }
  const introduction = extract.fields.introduction;
  if (target === '#body' && plainObject(introduction)
    && !/(^|[\s>+~])main(?:$|[\s:\[.#>+~])/i.test(String(introduction.selector))) {
    throw new Error('IANA body-root extraction must descend through the isolated main content');
  }
}
