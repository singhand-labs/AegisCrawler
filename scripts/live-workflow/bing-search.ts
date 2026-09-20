import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { plainObject } from './model-contract';

export const BING_ENTRY_URL = 'https://www.bing.com/?cc=us&setlang=en-US';
export const BING_QUERY = 'how do solar eclipses happen';
export const BING_COST_BUDGET_USD = 1.5;
export const BING_POINTER_STEPS = 12;
export const BING_POINTER_STEP_PAUSE_MS = 90;
export const BING_POINTER_HOLD_MS = 180;
export const BING_READING_PAUSE_MS = 1_500;
export const BING_SCROLL_STEPS = [180, 220, 160] as const;
export const BING_TYPING_DELAYS_MS = [105, 135, 115, 165, 125, 145] as const;
export const BING_INPUT_RECORDING_SETTLE_MS = 500;
const BING_HOST = 'www.bing.com';

export interface BingResultChoice extends Record<string, string> {
  title: string;
  href: string;
}

export interface BingSelectedResult {
  title: string;
  host: string;
}

export const BING_SEARCH_REQUIREMENT = {
  title: 'Find the Bing result selected during a recorded search',
  description: [
    'Search Bing with keyword, then locate the visible organic result identified by target_title and target_host. ',
    'The result may appear at any rank and surrounding results may differ from the recording. ',
    'Open that result and return exactly its visible title and destination host. ',
    'Exclude advertisements, sponsored cards, answer modules, hidden content, and positional matching.',
  ].join(''),
  requiredInputs: [
    { name: 'keyword', type: 'string', description: 'Exact Bing research question' },
    { name: 'target_title', type: 'string', description: 'Visible title of the result chosen in the recording' },
    { name: 'target_host', type: 'string', description: 'Destination host of the result chosen in the recording' },
  ],
  optionalInputs: [],
  outputFields: [
    { name: 'title', type: 'string', description: 'Visible title of the selected result' },
    { name: 'website', type: 'string', description: 'Destination host of the selected result' },
  ],
  sampleOutput: {
    title: 'Why Do Eclipses Happen? - Science@NASA',
    website: 'science.nasa.gov',
  },
};

function normalize(value: string | null | undefined): string {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

export function findBingBoundary(bodyText: string, title = ''): string | undefined {
  const sample = `${title}\n${bodyText}`.toLowerCase();
  return [
    'quick verification',
    'verify you are human',
    'complete the security check',
    'captcha',
    'unusual traffic',
  ].find((phrase) => sample.includes(phrase));
}

async function assertBingPageSafe(page: Page): Promise<void> {
  const boundary = findBingBoundary(
    await page.locator('body').innerText(),
    await page.title(),
  );
  if (boundary) throw new Error(`Bing environment blocked: ${boundary}`);
}

export async function movePointerLikeHuman(
  page: Pick<Page, 'mouse' | 'waitForTimeout'>,
  targetX: number,
  targetY: number,
  startX = Math.max(24, targetX - 210),
  startY = Math.max(24, targetY - 130),
): Promise<void> {
  for (let step = 1; step <= BING_POINTER_STEPS; step += 1) {
    const progress = step / BING_POINTER_STEPS;
    const eased = progress * progress * (3 - (2 * progress));
    const arc = Math.sin(Math.PI * progress) * 22;
    await page.mouse.move(
      Math.round(startX + ((targetX - startX) * eased) + arc),
      Math.round(startY + ((targetY - startY) * eased) - (arc / 2)),
    );
    await page.waitForTimeout(BING_POINTER_STEP_PAUSE_MS);
  }
}

export async function humanClick(page: Page, locator: ReturnType<Page['locator']>): Promise<void> {
  const box = await locator.boundingBox();
  if (!box) throw new Error('Bing visible target has no pointer box');
  await movePointerLikeHuman(page, box.x + (box.width / 2), box.y + (box.height / 2));
  await page.mouse.down();
  await page.waitForTimeout(BING_POINTER_HOLD_MS);
  await page.mouse.up();
  await page.waitForTimeout(BING_READING_PAUSE_MS);
}

async function clickAndResolveResultPage(
  resultsPage: Page,
  target: ReturnType<Page['locator']>,
): Promise<{ destination: Page; openedPopup: boolean }> {
  const context = resultsPage.context();
  const existing = new Set(context.pages());
  const resultsURL = resultsPage.url();
  await humanClick(resultsPage, target);
  const deadline = Date.now() + 60_000;
  for (;;) {
    const popup = context.pages().find((candidate) => !existing.has(candidate));
    if (popup) {
      await popup.waitForURL((url) => isObservableBingDestination(url.toString(), resultsURL), {
        waitUntil: 'load',
        timeout: Math.max(1, deadline - Date.now()),
      });
      return { destination: popup, openedPopup: true };
    }
    if (resultsPage.url() !== resultsURL) {
      await resultsPage.waitForLoadState('load', { timeout: 60_000 });
      return { destination: resultsPage, openedPopup: false };
    }
    if (Date.now() >= deadline) throw new Error('Bing result click opened no observable destination');
    await resultsPage.waitForTimeout(100);
  }
}

export function isObservableBingDestination(rawURL: string, resultsURL: string): boolean {
  try {
    const url = new URL(rawURL);
    return url.protocol === 'https:' && url.href !== new URL(resultsURL).href;
  } catch {
    return false;
  }
}

export async function typeQueryLikeHuman(
  search: ReturnType<Page['locator']>,
  page: Pick<Page, 'keyboard' | 'waitForTimeout'>,
  query = BING_QUERY,
): Promise<void> {
  for (let index = 0; index < query.length; index += 1) {
    await page.keyboard.type(query[index]);
    await page.waitForTimeout(BING_TYPING_DELAYS_MS[index % BING_TYPING_DELAYS_MS.length]);
  }
  assert.equal(normalize(await search.inputValue()), normalize(query),
    'Bing search input did not retain the exact fixed query');
  // Bing may replace the live search input while updating suggestions. Give
  // the extension's 100 ms input debounce a deterministic foreground window
  // to persist the complete value before Enter can detach that element.
  await page.waitForTimeout(BING_INPUT_RECORDING_SETTLE_MS);
  assert.equal(normalize(await search.inputValue()), normalize(query),
    'Bing search input changed before the recording settle boundary');
}

export async function scrollLikeHuman(
  page: Page,
  steps: readonly number[] = BING_SCROLL_STEPS,
): Promise<void> {
  for (const distance of steps) {
    await page.mouse.wheel(0, distance);
    await page.waitForTimeout(BING_READING_PAUSE_MS);
  }
}

/** Self-contained visible-DOM oracle; safe to serialize into Playwright. */
export function collectVisibleBingChoices(): BingResultChoice[] {
  const clean = (value: string | null | undefined): string =>
    String(value ?? '').replace(/\s+/g, ' ').trim();
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      const style = getComputedStyle(current);
      if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true'
        || style.display === 'none' || style.visibility === 'hidden') return false;
      current = current.parentElement;
    }
    const rect = element.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  };
  const root = document.querySelector('#b_results');
  if (!root || !visible(root)) return [];
  const rows: BingResultChoice[] = [];
  const seen = new Set<string>();
  for (const link of Array.from(root.querySelectorAll<HTMLAnchorElement>('h2 a[href]'))) {
    if (!visible(link)) continue;
    const card = link.closest('li, article, .b_algo') ?? link.parentElement;
    if (!card || !visible(card)) continue;
    const classes = String((card as HTMLElement).className ?? '').split(/\s+/)
      .map((token) => token.toLowerCase());
    if (classes.some((token) => ['b_ad', 'b_ans', 'b_pag', 'b_msg'].includes(token))) continue;
    if (card.querySelector('.b_adlabel, [data-testid="ad"], [aria-label="Ad"], '
      + '[aria-label="Advertisement"], [aria-label="Sponsored"]')) continue;
    const title = clean(link.innerText || link.textContent);
    let href = '';
    try {
      const url = new URL(link.href, location.href);
      if (url.protocol !== 'https:') continue;
      href = url.href;
    } catch { continue; }
    if (!title) continue;
    const key = `${title}\u0000${href}`;
    if (seen.has(key)) continue;
    seen.add(key);
    rows.push({ title, href });
  }
  return rows;
}

export function bingPageBlock(rawURL: string, expectedQuery = BING_QUERY): string {
  let url: URL;
  try { url = new URL(rawURL); } catch { return 'invalid URL'; }
  if (url.protocol !== 'https:') return 'non-HTTPS page';
  if (url.hostname !== BING_HOST) return `unexpected host ${url.hostname}`;
  if (url.pathname !== '/search') return 'not a Bing results page';
  if (normalize(url.searchParams.get('q')).toLowerCase() !== normalize(expectedQuery).toLowerCase()) {
    return 'unexpected Bing query';
  }
  return '';
}

export async function waitForBingChoice(page: Page, expectedQuery = BING_QUERY): Promise<BingResultChoice[]> {
  const deadline = Date.now() + 60_000;
  for (;;) {
    assert(!bingPageBlock(page.url(), expectedQuery), `Bing environment blocked: ${bingPageBlock(page.url(), expectedQuery)}`);
    await assertBingPageSafe(page);
    let rows: BingResultChoice[];
    try {
      rows = await page.evaluate(collectVisibleBingChoices);
    } catch (error) {
      if (/Execution context was destroyed/i.test(String(error))) {
        await page.waitForLoadState('load', { timeout: 60_000 });
        await assertBingPageSafe(page);
        continue;
      }
      throw error;
    }
    if (rows.length > 0) return rows;
    if (Date.now() >= deadline) {
      throw new Error('Bing environment blocked: no visible organic HTTPS result');
    }
    await page.waitForTimeout(250);
  }
}

export function isSelectableBingDestination(
  href: string,
  visibleTitle: string,
  choices: readonly BingResultChoice[],
  baseURL = BING_ENTRY_URL,
): boolean {
  let url: URL;
  try {
    url = new URL(href, baseURL);
  } catch {
    return false;
  }
  if (url.protocol !== 'https:') return false;
  const title = normalize(visibleTitle).toLowerCase();
  return Boolean(title) && choices.some((choice) =>
    normalize(choice.title).toLowerCase() === title && choice.href === url.href);
}

export async function bingSearchDemo(
  page: Page,
  ensureRecordingReady?: (activePage?: Page) => Promise<void>,
  onSelectedResult?: (selected: BingSelectedResult) => void,
): Promise<void> {
  await page.waitForLoadState('load', { timeout: 60_000 });
  const search = page.locator('#sb_form_q');
  await search.waitFor({ state: 'visible', timeout: 30_000 });
  await humanClick(page, search);
  await typeQueryLikeHuman(search, page);
  await page.waitForTimeout(BING_READING_PAUSE_MS);
  await submitBingQuery(page, search);
  await ensureRecordingReady?.();
  const choices = await waitForBingChoice(page);
  await scrollLikeHuman(page);
  const reviewed = page.locator('#b_results h2 a[href]');
  let target: ReturnType<Page['locator']> | undefined;
  for (let index = 0; index < await reviewed.count(); index += 1) {
    const candidate = reviewed.nth(index);
    if (!await candidate.isVisible()) continue;
    const href = await candidate.getAttribute('href');
    if (!href) continue;
    const title = normalize(await candidate.innerText());
    if (!isSelectableBingDestination(href, title, choices, page.url())) continue;
    onSelectedResult?.({ title, host: new URL(href, page.url()).hostname });
    target = candidate;
    break;
  }
  assert(target, 'Bing results contain no oracle-recognized HTTPS destination');
  await target.scrollIntoViewIfNeeded();
  const { destination, openedPopup } = await clickAndResolveResultPage(page, target);
  await ensureRecordingReady?.(destination);
  assert(new URL(destination.url()).protocol === 'https:', 'Bing result click reached a non-HTTPS destination');
  assert(bingPageBlock(destination.url()) !== '', 'Bing result click did not leave the results page');
  await destination.locator('body').waitFor({ state: 'visible', timeout: 30_000 });
  await scrollLikeHuman(destination, [220, 240, 180]);
  if (openedPopup) {
    await page.bringToFront();
    await page.waitForLoadState('load', { timeout: 60_000 });
    await ensureRecordingReady?.(page);
    await destination.close();
  } else {
    await page.goBack({ waitUntil: 'load', timeout: 60_000 });
    await ensureRecordingReady?.(page);
  }
  await waitForBingChoice(page);
}

export async function submitBingQuery(
  page: Page,
  search: ReturnType<Page['locator']>,
  expectedQuery = BING_QUERY,
): Promise<void> {
  assert.equal(normalize(await search.inputValue()), normalize(expectedQuery),
    'Bing search input changed before direct submission');
  await Promise.all([
    page.waitForURL((url) => bingPageBlock(url.toString(), expectedQuery) === '', {
      waitUntil: 'commit', timeout: 45_000,
    }),
    search.press('Enter', { delay: BING_POINTER_HOLD_MS }),
  ]);
}

export function assertBingRequirement(requirement: unknown): void {
  assert(plainObject(requirement), 'Bing requirement must be an object');
  const inputs = Array.isArray(requirement.requiredInputs)
    ? requirement.requiredInputs.filter(plainObject) : [];
  assert.deepEqual(inputs.map((input) => [input.name, input.type]), [
    ['keyword', 'string'], ['target_title', 'string'], ['target_host', 'string'],
  ]);
  const outputs = Array.isArray(requirement.outputFields)
    ? requirement.outputFields.filter(plainObject) : [];
  assert.deepEqual(outputs.map((field) => [field.name, field.type]).sort(), [
    ['title', 'string'], ['website', 'string'],
  ]);
}

export function assertBingRows(
  rows: unknown[],
  schema: unknown,
  expected: BingSelectedResult,
  phase: string,
): void {
  assert(plainObject(schema), `${phase} Bing output schema is missing`);
  assert.equal(rows.length, 1, `${phase} must return exactly the recorded Bing selection`);
  const canonical = rows.map((row) => {
    assert(plainObject(row), `${phase} Bing row must be an object`);
    return { title: normalize(String(row.title ?? '')), host: normalize(String(row.website ?? '')).toLowerCase() };
  });
  assert.deepEqual(canonical, [{ title: normalize(expected.title), host: expected.host.toLowerCase() }],
    `${phase} must find the item/website selected in the recording`);
}

export function assertBingRecording(recording: PageAgentRecording): void {
  assert(recording.termination?.complete === true, 'Bing recording must be complete');
  assert(recording.snapshots.every((snapshot) => snapshot.capture?.status === 'complete'),
    'Bing recording snapshots must all be complete');
  const chronologicalEvents = recording.events
    .map((event, order) => ({ event, order }))
    .sort((left, right) => left.event.timestamp - right.event.timestamp || left.order - right.order)
    .map(({ event }) => event);
  const navigations = chronologicalEvents.flatMap((event, index) =>
    event.type === 'navigate' ? [{ index, url: event.url, timestamp: event.timestamp }] : []);
  const resultEntries = navigations.filter((entry) => bingPageBlock(entry.url) === '');
  assert(resultEntries.length >= 1, 'Bing recording must enter the exact results page');
  const firstResult = resultEntries[0];
  const eventsBeforeResults = chronologicalEvents.slice(0, firstResult.index);
  const submitted = eventsBeforeResults.some((event, index) => {
    if (event.type === 'inputText' && event.submit === true) {
      return normalize(event.text).toLowerCase() === BING_QUERY.toLowerCase();
    }
    if (event.type !== 'submitForm') return false;
    let firstInput = index;
    for (let candidateIndex = index - 1; candidateIndex >= 0; candidateIndex -= 1) {
      const candidate = eventsBeforeResults[candidateIndex];
      if (candidate.type === 'click' || candidate.type === 'navigate') break;
      if (candidate.type === 'inputText' && normalize(candidate.text)) firstInput = candidateIndex;
    }
    if (firstInput === index) return false;
    const inputTexts = eventsBeforeResults.slice(firstInput, index)
      .flatMap((candidate) => candidate.type === 'inputText' ? [normalize(candidate.text)] : [])
      .filter(Boolean);
    const expected = BING_QUERY.toLowerCase();
    return inputTexts.some((text) => text.toLowerCase() === expected)
      || normalize(inputTexts.join(' ')).toLowerCase() === expected;
  });
  assert(submitted, 'Bing recording omitted direct query submission');
  const destination = navigations.find((entry) => {
    if (entry.index <= firstResult.index) return false;
    try {
      return new URL(entry.url).protocol === 'https:' && bingPageBlock(entry.url) !== '';
    } catch { return false; }
  });
  assert(destination, 'Bing recording omitted an HTTPS result destination entry');
  assert(firstResult.index < destination.index,
    'Bing recording must preserve results -> destination order');
  const urls = recording.snapshots.map((snapshot) => snapshot.url);
  const restoredResult = recording.snapshots.some((snapshot) =>
    snapshot.timestamp > destination.timestamp && bingPageBlock(snapshot.url) === '');
  assert(restoredResult,
    'Bing recording must restore the exact results context after Back or closing a result tab');
  assert(urls.some((url) => {
    try { return new URL(url).protocol === 'https:' && bingPageBlock(url) !== ''; } catch { return false; }
  }), 'Bing recording omitted the HTTPS result destination');
}

export function assertBingRule(rule: unknown): void {
  assert(plainObject(rule), 'Bing rule must be an object');
  const serialized = JSON.stringify(rule);
  assert(serialized.includes('{{keyword}}'), 'Bing rule must bind the keyword input');
  assert(hasOperationalBingInputReference(rule.steps, 'target_title'),
    'Bing rule must locate the recorded result by target_title');
  assert(hasOperationalBingInputReference(rule.steps, 'target_host'),
    'Bing rule must bind the recorded result destination host');
  assert(JSON.stringify(rule.domain).includes('bing.com'), 'Bing rule must remain scoped to Bing');
  assert(/waitForElementVisible/.test(serialized), 'Bing rule must use observable result readiness');
  assert(!/"index"\s*:/.test(serialized), 'Bing rule must not bind the selected result by position');
  assert(!/solveCaptcha|executeJavascript/.test(serialized), 'Bing rule contains unsafe actions');
}

function hasOperationalBingInputReference(value: unknown, input: string): boolean {
  if (typeof value === 'string') return value.includes(`{{${input}}}`);
  if (Array.isArray(value)) return value.some((child) => hasOperationalBingInputReference(child, input));
  if (!plainObject(value) || value.action === 'sendResult') return false;
  return Object.values(value).some((child) => hasOperationalBingInputReference(child, input));
}
