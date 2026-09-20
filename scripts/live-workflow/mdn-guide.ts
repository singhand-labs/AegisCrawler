import { deepStrictEqual, strictEqual } from 'node:assert';
import type { BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';

export const MDN_ENTRY_URL = 'https://developer.mozilla.org/en-US/docs/Web/CSS/Guides/Grid_layout';
export const MDN_CANONICAL_PATH = '/en-US/docs/Web/CSS/Guides/Grid_layout';
export const MDN_COST_BUDGET_USD = 1.5;
export const MDN_VISIBLE_ACTION_PAUSE_MS = 5_000;
export const MDN_POINTER_WAYPOINTS = 4;
export const MDN_POINTER_WAYPOINT_PAUSE_MS = 250;
export const MDN_POINTER_CLICK_PAUSE_MS = 300;
export const MDN_SCROLL_STEPS = [240, 240, 320] as const;
export const MDN_IFRAME_EXCLUSION_CSS =
  'iframe { display: none !important; visibility: hidden !important; }';
export const MDN_VISIBLE_ACTION_CSS = [
  'main h1:hover { outline: 4px solid #ff2d55 !important;',
  'outline-offset: 6px !important; cursor: pointer !important; }',
  'main h1:active { background: rgba(255, 45, 85, 0.18) !important; }',
].join(' ');
export const MDN_NON_CONTENT_SELECTOR = [
  'iframe',
  'header',
  'footer',
  'aside',
  'nav',
  '[role="banner"]',
  '[role="complementary"]',
  '[role="navigation"]',
].join(',');
export const MDN_STABLE_ARTICLE_ROOT_SELECTOR = 'main';

export const MDN_GUIDE_REQUIREMENT = {
  title: 'Collect the visible MDN CSS grid guide summary',
  description: [
    'From the public MDN CSS grid layout guide, collect exactly one visible guide summary.',
    'Return exactly title, introduction, and first_section as strings.',
    'Use the visible main article as the single extraction root and give each field its own visible descendant selector: the h1 heading, the first introductory paragraph after it, and the first article h2 heading.',
    'Use only visible content from the main article and do not navigate outside developer.mozilla.org.',
  ].join(' '),
  requiredInputs: [],
  optionalInputs: [],
  outputFields: [
    { name: 'title', type: 'string', description: 'Visible guide heading' },
    { name: 'introduction', type: 'string', description: 'First visible introductory paragraph' },
    { name: 'first_section', type: 'string', description: 'First visible level-two article section heading' },
  ],
  sampleOutput: {
    title: 'CSS grid layout',
    introduction: 'A visible introductory paragraph.',
    first_section: 'Grid layout in action',
  },
};

export interface MDNGuideRow {
  title: string;
  introduction: string;
  first_section: string;
}

export function assertMDNURL(rawURL: string): void {
  const url = new URL(rawURL);
  strictEqual(url.protocol, 'https:', 'MDN qualification requires HTTPS');
  strictEqual(url.hostname, 'developer.mozilla.org',
    'MDN qualification must remain on developer.mozilla.org');
  strictEqual(url.pathname, MDN_CANONICAL_PATH,
    'MDN qualification must remain on the reviewed CSS grid guide');
}

export function findMDNBoundary(bodyText: string, title: string): string | undefined {
  const sample = `${title}\n${bodyText.slice(0, 30_000)}`.toLowerCase();
  return [
    'verify you are human',
    'complete the security check',
    'captcha',
    'sign in to continue',
    'log in to continue',
    'rate limit exceeded',
  ].find((phrase) => sample.includes(phrase));
}

export async function assertMDNPageSafe(page: Page): Promise<void> {
  assertMDNURL(page.url());
  const boundary = findMDNBoundary(
    await page.locator('body').innerText(),
    await page.title(),
  );
  if (boundary) throw new Error(`MDN environment blocked: ${boundary}`);
}

export async function mdnGuideDemo(page: Page): Promise<void> {
  await page.bringToFront();
  await assertMDNPageSafe(page);
  const heading = page.getByRole('heading', { name: 'CSS grid layout', exact: true });
  await heading.waitFor({ state: 'visible', timeout: 30_000 });
  const box = await heading.boundingBox();
  if (!box) throw new Error('MDN visible heading has no pointer target');
  const targetX = Math.round(box.x + Math.min(box.width / 2, 480));
  const targetY = Math.round(box.y + Math.min(box.height / 3, 320));
  const startX = Math.max(24, targetX - 160);
  const startY = Math.max(24, targetY - 96);
  for (let waypoint = 1; waypoint <= MDN_POINTER_WAYPOINTS; waypoint += 1) {
    const progress = waypoint / MDN_POINTER_WAYPOINTS;
    await page.mouse.move(
      Math.round(startX + ((targetX - startX) * progress)),
      Math.round(startY + ((targetY - startY) * progress)),
    );
    await page.waitForTimeout(MDN_POINTER_WAYPOINT_PAUSE_MS);
  }
  await page.mouse.down();
  await page.waitForTimeout(MDN_POINTER_CLICK_PAUSE_MS);
  await page.mouse.up();
  await page.waitForTimeout(MDN_VISIBLE_ACTION_PAUSE_MS);
  for (const distance of MDN_SCROLL_STEPS) {
    await page.mouse.wheel(0, distance);
    await page.waitForTimeout(MDN_VISIBLE_ACTION_PAUSE_MS);
  }
  await assertMDNPageSafe(page);
}

export async function prepareMDNContext(context: BrowserContext): Promise<void> {
  await context.route('**/*', async (route) => {
    const request = route.request();
    if (request.resourceType() !== 'document') {
      await route.continue();
      return;
    }
    let url: URL;
    try {
      url = new URL(request.url());
    } catch {
      await route.abort('blockedbyclient');
      return;
    }
    if (url.protocol === 'https:' && url.hostname === 'developer.mozilla.org') {
      await route.continue();
      return;
    }
    await route.abort('blockedbyclient');
  });
}

export async function prepareMDNPage(page: Page): Promise<void> {
  await page.addStyleTag({
    content: MDN_IFRAME_EXCLUSION_CSS,
  });
  await page.addStyleTag({
    content: MDN_VISIBLE_ACTION_CSS,
  });
  await page.evaluate((nonContentSelector) => {
    const removeNonContent = (root: ParentNode) => {
      root.querySelectorAll(nonContentSelector).forEach((element) => element.remove());
    };
    removeNonContent(document);
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of Array.from(record.addedNodes)) {
          if (!(node instanceof Element)) continue;
          if (node.matches(nonContentSelector)) node.remove();
          else removeNonContent(node);
        }
      }
    });
    observer.observe(document.documentElement, { childList: true, subtree: true });
  }, MDN_NON_CONTENT_SELECTOR);
  await page.locator(MDN_NON_CONTENT_SELECTOR).waitFor({ state: 'detached', timeout: 10_000 })
    .catch(async () => {
      if (await page.locator(MDN_NON_CONTENT_SELECTOR).count() !== 0) {
        throw new Error('MDN page preparation could not exclude non-content chrome');
      }
    });
  const main = page.locator('main').first();
  await main.locator('h1').first().waitFor({ state: 'visible', timeout: 10_000 });
  await assertMDNPageSafe(page);
}

export async function collectMDNGuideOracle(page: Page): Promise<MDNGuideRow> {
  await assertMDNPageSafe(page);
  const main = page.locator('main').first();
  const title = (await main.locator('h1').first().innerText()).trim();
  const introduction = (await main.locator('h1').first()
    .locator('xpath=following::p[normalize-space()][1]').innerText()).trim();
  const firstSection = (await main.locator('h2').filter({ hasNotText: 'In this article' })
    .first().innerText()).trim();
  if (title !== 'CSS grid layout') throw new Error(`unexpected MDN title: ${title}`);
  if (introduction.length < 80) throw new Error('MDN introduction was unexpectedly short');
  if (!firstSection) throw new Error('MDN first section heading was empty');
  return { title, introduction, first_section: firstSection };
}

export function assertMDNRows(
  rows: unknown[],
  _outputSchema: unknown,
  expected: MDNGuideRow,
  phase: string,
): void {
  strictEqual(rows.length, 1, `${phase} must emit exactly one MDN guide row`);
  deepStrictEqual(rows[0], expected, `${phase} MDN row must match the independent visible oracle`);
}

export function assertMDNRule(rule: unknown): void {
  if (!rule || typeof rule !== 'object') throw new Error('MDN rule must be an object');
  const steps = (rule as { steps?: unknown }).steps;
  if (!Array.isArray(steps)) throw new Error('MDN rule must contain steps');
  const scrolls = steps.filter((step): step is Record<string, unknown> =>
    !!step && typeof step === 'object' && (step as { action?: unknown }).action === 'scrollBy');
  if (scrolls.length !== 3 || scrolls.some((step) =>
    step.direction !== 'down' || step.distance !== 1 || step.unit !== 'pages')) {
    throw new Error('MDN replay must reperform exactly three recorded downward page scrolls');
  }
  const extracts = steps.filter((step): step is Record<string, unknown> =>
    !!step && typeof step === 'object' && (step as { action?: unknown }).action === 'extract');
  if (extracts.length !== 1) throw new Error('MDN rule must contain exactly one extraction step');
  const extract = extracts[0];
  const target = extract.target;
  const targetSelector = target && typeof target === 'object'
    ? (target as { selector?: unknown }).selector
    : undefined;
  if (targetSelector !== MDN_STABLE_ARTICLE_ROOT_SELECTOR) {
    throw new Error('MDN extraction must target the stable semantic main article');
  }
  const fields = extract.fields;
  if (!fields || typeof fields !== 'object' || Array.isArray(fields)) {
    throw new Error('MDN extraction fields must be an object');
  }
  const required = ['title', 'introduction', 'first_section'];
  if (Object.keys(fields as object).sort().join(',') !== required.slice().sort().join(',')) {
    throw new Error('MDN extraction must contain exactly the three required fields');
  }
  const expectedSelectorKinds: Record<string, RegExp> = {
    title: /(^|[\s>+~])h1(?:$|[\s:\[.#>+~])/i,
    introduction: /(^|[\s>+~])p(?:$|[\s:\[.#>+~])/i,
    first_section: /(^|[\s>+~])h2(?:$|[\s:\[.#>+~])/i,
  };
  for (const name of required) {
    const field = (fields as Record<string, unknown>)[name];
    const selector = field && typeof field === 'object'
      ? (field as { selector?: unknown }).selector
      : undefined;
    const visible = field && typeof field === 'object'
      ? (field as { visible?: unknown }).visible
      : undefined;
    if (typeof selector !== 'string' ||
      !expectedSelectorKinds[name].test(selector.trim()) ||
      visible !== true) {
      throw new Error(`MDN field ${name} requires its reviewed visible semantic selector`);
    }
  }
  for (const step of steps) {
    if (!step || typeof step !== 'object' ||
      (step as { action?: unknown }).action !== 'waitForElementVisible') continue;
    const waitTarget = (step as { target?: unknown }).target;
    const waitSelector = waitTarget && typeof waitTarget === 'object'
      ? (waitTarget as { selector?: unknown }).selector
      : undefined;
    if (waitSelector !== MDN_STABLE_ARTICLE_ROOT_SELECTOR) {
      throw new Error('MDN readiness wait must target the stable semantic main article');
    }
  }
}

export function assertMDNRecording(recording: PageAgentRecording): void {
  assertMDNURL(recording.meta.startUrl);
  if (recording.events.length < 2 || recording.events.length > 8) {
    throw new Error('MDN recording must contain 2..8 bounded visible actions');
  }
  if (recording.snapshots.length !== recording.events.length + 2) {
    throw new Error('MDN recording must contain initial, per-action, and final snapshots');
  }
  const incomplete = recording.snapshots.flatMap((snapshot, index) => {
    if (snapshot.capture?.status === 'complete') return [];
    const frameStatuses = snapshot.capture?.frames
      ?.map((frame) => frame.status)
      .join(',') || 'none';
    return [`${index}:${snapshot.capture?.status ?? 'missing'}:frames=${frameStatuses}`];
  });
  if (incomplete.length > 0) {
    throw new Error(`MDN recording requires complete semantic snapshots (${incomplete.join('; ')})`);
  }
  if (recording.events.some((event) => event.type === 'executeJavascript')) {
    throw new Error('MDN recording forbids script execution');
  }
}
