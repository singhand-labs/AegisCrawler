import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { plainObject } from './model-contract';

export const DUCKDUCKGO_ENTRY_URL = 'https://duckduckgo.com/';
export const DUCKDUCKGO_QUERY = 'how do ocean tides work';
export const DUCKDUCKGO_COST_BUDGET_USD = 1.5;
const DUCKDUCKGO_HOSTS = new Set(['duckduckgo.com', 'www.duckduckgo.com']);
const TYPING_DELAYS_MS = [105, 135, 115, 165, 125, 145] as const;
const SCROLL_STEPS = [180, 220, 160] as const;
const READING_PAUSE_MS = 1_500;

export interface DuckDuckGoResultChoice extends Record<string, string> { title: string; href: string; host: string }
export interface DuckDuckGoSelectedResult { title: string; host: string }
export interface DuckDuckGoClassifierDiagnostics {
  explicitRoots: number;
  fallbackRoots: number;
  selectedRoots: number;
  titleLinks: number;
  allLinks: number;
}

export const DUCKDUCKGO_REQUIREMENT = {
  title: 'Report one recorded visible DuckDuckGo result without opening it',
  description: [
    'Search DuckDuckGo with keyword and wait for visible ordinary organic results. ',
    'Locate the visible HTTPS result identified by target_title and target_host using semantic text and link evidence, never rank or index. ',
    'Return exactly its visible title and destination host without opening a result. ',
    'Exclude advertisements, sponsored cards, answer modules, hidden content, navigation links, and unsafe URLs.',
  ].join(''),
  requiredInputs: [
    { name: 'keyword', type: 'string', description: 'Exact reviewed DuckDuckGo query' },
    { name: 'target_title', type: 'string', description: 'Exact visible organic result level-two heading' },
    { name: 'target_host', type: 'string', description: 'HTTPS organic result destination host' },
  ], optionalInputs: [], outputFields: [
    { name: 'title', type: 'string', description: 'Visible organic result level-two heading' },
    { name: 'website', type: 'string', description: 'Visible organic result destination citation host' },
  ], sampleOutput: { title: 'Ocean Tides', website: 'example.org' },
};

function normalize(value: unknown): string { return String(value ?? '').replace(/\s+/g, ' ').trim(); }

export function duckDuckGoEnvironmentBlock(bodyText: string, title = ''): string | undefined {
  const sample = `${title}\n${bodyText}`.toLowerCase();
  return ['verify you are human', 'verification required', 'complete the security check', 'captcha',
    'unusual traffic', 'automated requests', 'consent required',
    // DuckDuckGo localizes its image challenge. Keep these markers narrowly
    // tied to explicit human-verification instructions; ordinary Chinese
    // result content must not become a challenge signal.
    '请完成以下挑战', '确认这项搜索由真人进行', '选择所有包含鸭子的正方形',
  ].find((phrase) => sample.includes(phrase));
}

export function duckDuckGoResultsPageBlock(rawURL: string, expectedQuery = DUCKDUCKGO_QUERY): string {
  let url: URL; try { url = new URL(rawURL); } catch { return 'invalid URL'; }
  if (url.protocol !== 'https:') return 'non-HTTPS page';
  if (!DUCKDUCKGO_HOSTS.has(url.hostname)) return `unexpected host ${url.hostname}`;
  if (normalize(url.searchParams.get('q')).toLowerCase() !== normalize(expectedQuery).toLowerCase()) return 'unexpected DuckDuckGo query';
  return '';
}

export function canonicalDuckDuckGoDestination(rawHref: string, baseURL = DUCKDUCKGO_ENTRY_URL): URL | undefined {
  let url: URL; try { url = new URL(rawHref, baseURL); } catch { return undefined; }
  if (DUCKDUCKGO_HOSTS.has(url.hostname) && url.pathname === '/l/') {
    const encoded = url.searchParams.get('uddg'); if (!encoded) return undefined;
    try { url = new URL(encoded); } catch { return undefined; }
  }
  if (url.protocol !== 'https:' || !url.hostname || DUCKDUCKGO_HOSTS.has(url.hostname)) return undefined;
  if (url.username || url.password || url.port) return undefined;
  url.hash = ''; return url;
}

/** Self-contained bounded diagnostics; retains no DOM, text, or URLs. */
export function collectDuckDuckGoClassifierDiagnostics(): DuckDuckGoClassifierDiagnostics {
  const explicit = Array.from(document.querySelectorAll('[data-testid="mainline"], #links'));
  const fallback = Array.from(document.querySelectorAll('main, [role="main"]'));
  const roots = explicit.length > 0 ? explicit : fallback;
  const titleSelector = 'h2 a[href], [data-testid="result-title-a"][href], .result__title a[href]';
  return {
    explicitRoots: explicit.length,
    fallbackRoots: fallback.length,
    selectedRoots: roots.length,
    titleLinks: new Set(roots.flatMap((root) => Array.from(root.querySelectorAll(titleSelector)))).size,
    allLinks: new Set(roots.flatMap((root) => Array.from(root.querySelectorAll('a[href]')))).size,
  };
}

/** Self-contained visible-DOM oracle for Playwright page evaluation. */
export function collectVisibleDuckDuckGoChoices(): DuckDuckGoResultChoice[] {
  const clean = (value: unknown): string => String(value ?? '').replace(/\s+/g, ' ').trim();
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      const style = getComputedStyle(current);
      if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true'
        || style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return false;
      current = current.parentElement;
    }
    const rect = element.getBoundingClientRect(); return rect.width > 0 && rect.height > 0;
  };
  const canonical = (rawHref: string): URL | undefined => {
    let url: URL; try { url = new URL(rawHref, location.href); } catch { return undefined; }
    const redirect = (url.hostname === 'duckduckgo.com' || url.hostname === 'www.duckduckgo.com')
      && url.pathname === '/l/';
    if (redirect) {
      const encoded = url.searchParams.get('uddg'); if (!encoded) return undefined;
      try { url = new URL(encoded); } catch { return undefined; }
    }
    const internal = url.hostname === 'duckduckgo.com' || url.hostname === 'www.duckduckgo.com';
    if (url.protocol !== 'https:' || !url.hostname || internal || url.username || url.password || url.port) return undefined;
    url.hash = ''; return url;
  };
  const explicitRoots = Array.from(document.querySelectorAll('[data-testid="mainline"], #links'));
  const roots = explicitRoots.length > 0
    ? explicitRoots
    : Array.from(document.querySelectorAll('main, [role="main"]'));
  if (roots.length === 0) return [];
  const rows: DuckDuckGoResultChoice[] = []; const seen = new Set<string>();
  const links = roots.flatMap((root) => Array.from(root.querySelectorAll<HTMLAnchorElement>(
    'h2 a[href], [data-testid="result-title-a"][href], .result__title a[href]',
  )));
  for (const link of [...new Set(links)]) {
    if (!visible(link)) continue;
    const card = link.closest('article, [data-testid="result"], .result, .results_links') ?? link.parentElement;
    if (!card || !visible(card)) continue;
    const marker = `${(card as HTMLElement).className ?? ''} ${card.getAttribute('data-testid') ?? ''} ${card.getAttribute('aria-label') ?? ''}`.toLowerCase();
    const cardText = clean(card.textContent).toLowerCase();
    if (/\b(ad|ads|sponsored|answer|related|pagination)\b/.test(marker) || /^(ad|sponsored)\b/.test(cardText)
      || card.querySelector('[data-testid*="ad"], [aria-label="Ad"], [aria-label="Advertisement"], [aria-label="Sponsored"]')) continue;
    const title = clean(link.innerText || link.textContent); const destination = canonical(link.href);
    if (!title || !destination) continue;
    const key = `${title.toLowerCase()}\u0000${destination.hostname.toLowerCase()}`; if (seen.has(key)) continue;
    seen.add(key); rows.push({ title, href: destination.href, host: destination.hostname.toLowerCase() });
  }
  return rows;
}

async function assertEnvironmentSafe(page: Page): Promise<void> {
  const boundary = duckDuckGoEnvironmentBlock(await page.locator('body').innerText(), await page.title());
  if (boundary) throw new Error(`DuckDuckGo environment blocked: ${boundary}`);
}

async function waitForVisibleChoice(page: Page): Promise<DuckDuckGoResultChoice[]> {
  const deadline = Date.now() + 60_000;
  for (;;) {
    const pageBlock = duckDuckGoResultsPageBlock(page.url()); assert(!pageBlock, `DuckDuckGo environment blocked: ${pageBlock}`);
    await assertEnvironmentSafe(page);
    try { const choices = await page.evaluate(collectVisibleDuckDuckGoChoices); if (choices.length > 0) return choices; }
    catch (error) { if (!/Execution context was destroyed/i.test(String(error))) throw error; await page.waitForLoadState('domcontentloaded', { timeout: 60_000 }); }
    if (Date.now() >= deadline) {
      const diagnostics = await page.evaluate(collectDuckDuckGoClassifierDiagnostics);
      throw new Error(`DuckDuckGo environment blocked: no visible organic HTTPS result; classifier=${JSON.stringify(diagnostics)}`);
    }
    await page.waitForTimeout(250);
  }
}

export async function duckDuckGoSearchReadonlyDemo(page: Page, ensureRecordingReady?: (activePage?: Page) => Promise<void>, onSelected?: (selected: DuckDuckGoSelectedResult) => void): Promise<void> {
  await page.waitForLoadState('domcontentloaded', { timeout: 60_000 }); await assertEnvironmentSafe(page);
  const searches = page.locator('input[name="q"]:visible, textarea[name="q"]:visible, [role="searchbox"]:visible');
  assert.equal(await searches.count(), 1, 'DuckDuckGo must expose exactly one visible search box');
  const search = searches.first(); await search.click();
  for (let index = 0; index < DUCKDUCKGO_QUERY.length; index += 1) {
    await page.keyboard.type(DUCKDUCKGO_QUERY[index]); await page.waitForTimeout(TYPING_DELAYS_MS[index % TYPING_DELAYS_MS.length]);
  }
  assert.equal(normalize(await search.inputValue()), DUCKDUCKGO_QUERY, 'DuckDuckGo search input did not retain the exact reviewed query');
  await page.waitForTimeout(READING_PAUSE_MS);
  await Promise.all([page.waitForURL((url) => duckDuckGoResultsPageBlock(url.toString()) === '', { waitUntil: 'commit', timeout: 45_000 }), page.keyboard.press('Enter', { delay: 180 })]);
  await ensureRecordingReady?.(page);
  const choices = await waitForVisibleChoice(page); onSelected?.({ title: choices[0].title, host: choices[0].host });
  for (const distance of SCROLL_STEPS) { await page.mouse.wheel(0, distance); await page.waitForTimeout(READING_PAUSE_MS); }
  assert.equal(duckDuckGoResultsPageBlock(page.url()), '', 'DuckDuckGo read-only journey left its results page');
}

export function assertDuckDuckGoRecording(recording: PageAgentRecording): void {
  assert.equal(recording.termination?.complete, true, 'DuckDuckGo recording must be complete');
  assert(recording.snapshots.every((snapshot) => snapshot.capture?.status === 'complete'), 'DuckDuckGo recording snapshots must all be complete');
  const events = recording.events.map((event, order) => ({ event, order })).sort((a, b) => a.event.timestamp - b.event.timestamp || a.order - b.order).map(({ event }) => event);
  const navigationIndex = events.findIndex((event) => event.type === 'navigate' && duckDuckGoResultsPageBlock(event.url) === '');
  assert(navigationIndex >= 0, 'DuckDuckGo recording omitted exact results navigation');
  const beforeNavigation = events.slice(0, navigationIndex);
  const inputIndex = beforeNavigation.findIndex((event) =>
    event.type === 'inputText' && normalize(event.text) === DUCKDUCKGO_QUERY);
  assert(inputIndex >= 0, 'DuckDuckGo recording omitted the exact completed query input');
  assert(beforeNavigation.slice(0, inputIndex).some((event) => event.type === 'click'),
    'DuckDuckGo recording omitted the visible search-box click before input');
  const submissions = beforeNavigation.filter((event) => event.type === 'submitForm' || (event.type === 'inputText' && event.submit === true));
  assert(submissions.length <= 1, 'DuckDuckGo recording contains duplicate direct submissions');
  if (submissions.length === 0) {
    let lastCompletedInput = -1;
    for (let index = beforeNavigation.length - 1; index >= 0; index -= 1) {
      const event = beforeNavigation[index];
      if (event.type === 'inputText' && normalize(event.text) === DUCKDUCKGO_QUERY) {
        lastCompletedInput = index;
        break;
      }
    }
    assert(lastCompletedInput >= 0, 'DuckDuckGo recording omitted completed input provenance');
    const interrupted = beforeNavigation.slice(lastCompletedInput + 1).some((event) =>
      event.type === 'click' || event.type === 'navigate');
    assert(!interrupted,
      'DuckDuckGo recording lacks an uninterrupted exact-input to results-navigation submission');
  }
  assert(events.slice(navigationIndex + 1).some((event) => event.type === 'scroll'), 'DuckDuckGo recording omitted result scrolling');
  assert(!events.slice(navigationIndex + 1).some((event) => event.type === 'click'),
    'DuckDuckGo read-only recording clicked a result-page target');
  assert(!events.slice(navigationIndex + 1).some((event) => event.type === 'navigate' && duckDuckGoResultsPageBlock(event.url) !== ''), 'DuckDuckGo read-only recording left its results page');
}

export function assertDuckDuckGoRequirement(value: unknown): void {
  assert(plainObject(value), 'DuckDuckGo requirement must be an object');
  const inputs = Array.isArray(value.requiredInputs) ? value.requiredInputs.filter(plainObject) : [];
  assert.deepEqual(inputs.map((input) => [input.name, input.type]), [['keyword', 'string'], ['target_title', 'string'], ['target_host', 'string']]);
  const outputs = Array.isArray(value.outputFields) ? value.outputFields.filter(plainObject) : [];
  assert.deepEqual(outputs.map((field) => [field.name, field.type]).sort(), [['title', 'string'], ['website', 'string']]);
}

export function assertDuckDuckGoRule(rule: unknown): void {
  assert(plainObject(rule), 'DuckDuckGo rule must be an object'); const serialized = JSON.stringify(rule);
  assert(serialized.includes('{{keyword}}'), 'DuckDuckGo rule must bind keyword');
  assert(serialized.includes('{{target_title}}'), 'DuckDuckGo rule must bind target_title');
  assert(serialized.includes('{{target_host}}'), 'DuckDuckGo rule must bind target_host');
  assert(JSON.stringify(rule.domain).includes('duckduckgo.com'), 'DuckDuckGo rule must remain scoped to DuckDuckGo');
  assert(/waitForElementVisible/.test(serialized), 'DuckDuckGo rule must use observable result readiness');
  assert(!/"index"\s*:/.test(serialized), 'DuckDuckGo rule must not target a result by rank or index');
  assert(!/executeJavascript|solveCaptcha|uploadFile|download/i.test(serialized), 'DuckDuckGo rule contains an unsafe action');
}

export function assertDuckDuckGoRows(rows: unknown[], schema: unknown, expected: DuckDuckGoSelectedResult, phase: string): void {
  assert(plainObject(schema), `${phase} DuckDuckGo output schema is missing`); assert.equal(rows.length, 1, `${phase} must return exactly one recorded DuckDuckGo result`);
  const row = rows[0]; assert(plainObject(row), `${phase} DuckDuckGo row must be an object`);
  assert.deepEqual(Object.keys(row).sort(), ['title', 'website'], `${phase} DuckDuckGo row has schema drift`);
  assert.deepEqual({ title: normalize(row.title), host: normalize(row.website).toLowerCase() }, { title: normalize(expected.title), host: expected.host.toLowerCase() }, `${phase} must match the contemporaneously recorded result identity`);
}

export async function assertDuckDuckGoSelectionVisible(page: Page, expected: DuckDuckGoSelectedResult, phase: string): Promise<void> {
  await assertEnvironmentSafe(page); const choices = await page.evaluate(collectVisibleDuckDuckGoChoices);
  assert(choices.some((choice) => normalize(choice.title) === normalize(expected.title) && choice.host === expected.host.toLowerCase()), `${phase} DuckDuckGo page does not visibly contain the recorded result identity`);
}
