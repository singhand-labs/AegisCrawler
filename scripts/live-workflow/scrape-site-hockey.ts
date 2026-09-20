import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import type { Browser, BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { assertVisibleModelExtractionRule, plainObject } from './model-contract';

export const HOCKEY_ORIGIN = 'https://www.scrapethissite.com';
export const HOCKEY_ENTRY_URL = `${HOCKEY_ORIGIN}/pages/forms/`;
export const HOCKEY_REPLAY_QUERY = 'New York';
export const HOCKEY_BOSTON_QUERY = 'Boston Bruins';
export const HOCKEY_DETROIT_QUERY = 'Detroit Red Wings';
export const HOCKEY_MAX_PAGES = 10;
export const HOCKEY_COST_BUDGET_USD = 1.50;

export const HOCKEY_FIELDS = [
  'goal_difference', 'goals_against', 'goals_for', 'losses',
  'team_name', 'win_percentage', 'wins', 'year',
] as const;
const INTEGER_FIELDS = new Set(['goal_difference', 'goals_against', 'goals_for', 'losses', 'wins', 'year']);

export interface HockeyRow extends Record<string, string | number> {
  team_name: string;
  year: number;
  wins: number;
  losses: number;
  win_percentage: number;
  goals_for: number;
  goals_against: number;
  goal_difference: number;
}

export interface HockeyOracle {
  query: string;
  pages: string[];
  rows: HockeyRow[];
}

export const HOCKEY_REQUIREMENT = {
  title: 'Collect every hockey team season matching a team search',
  description: [
    'Open the reviewed Scrape This Site hockey form and enter the required team_query in the visible team-search control.',
    'Submit the form exactly once and collect every complete visible result row in page and DOM order.',
    'Before clicking page 1, preserve the recorded same-origin committed query navigation with q bound to team_query and no page_num; this is the required observable readiness boundary for the submitted search.',
    'After submission, if the results expose a unique visible numbered page-1 control, click it to canonicalize the query-only result state before extracting; single-page results render an empty pagination list with no numbered controls and their query-only state is already canonical, so extraction may proceed directly. Never construct or navigate to a page URL.',
    `Use a fixedCount loop bounded at ${HOCKEY_MAX_PAGES}; emit the current page, stop when no unique visible Next control remains, and otherwise click that control without constructing a page URL.`,
    'Return exactly team_name, year, wins, losses, win_percentage, goals_for, goals_against, and goal_difference, parsing every numeric value from its visible cell. Omit OT losses because the source legitimately mixes blank and numeric cells while the requirement contract has no nullable scalar type.',
  ].join(' '),
  requiredInputs: [{
    name: 'team_query',
    type: 'string',
    description: 'Visible team-name search text',
    constraints: { pattern: '^[A-Za-z]+(?: [A-Za-z]+)*$', maxLength: 64 },
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'team_name', type: 'string', description: 'Visible team name' },
    { name: 'year', type: 'number', description: 'Visible season year parsed as an integer-valued number' },
    { name: 'wins', type: 'number', description: 'Visible wins parsed as an integer-valued number' },
    { name: 'losses', type: 'number', description: 'Visible losses parsed as an integer-valued number' },
    { name: 'win_percentage', type: 'number', description: 'Visible win percentage parsed as a number' },
    { name: 'goals_for', type: 'number', description: 'Visible goals for parsed as an integer-valued number' },
    { name: 'goals_against', type: 'number', description: 'Visible goals against parsed as an integer-valued number' },
    { name: 'goal_difference', type: 'number', description: 'Visible goal difference parsed as an integer-valued number' },
  ],
  sampleOutput: {
    team_name: 'Example Team', year: 2000, wins: 40, losses: 30,
    win_percentage: 0.5, goals_for: 250, goals_against: 240, goal_difference: 10,
  },
};

function normalizedText(value: unknown): string {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

export function assertHockeyQuery(value: string): string {
  assert.equal(value, value.trim(), 'Hockey query must not contain surrounding whitespace');
  assert(/^[A-Za-z]+(?: [A-Za-z]+)*$/.test(value), 'Hockey query must contain safe ASCII words');
  assert(value.length <= 64, 'Hockey query exceeds 64 characters');
  return value;
}

function hockeyURL(raw: string, label: string): URL {
  const url = new URL(raw);
  assert.equal(url.protocol, 'https:', `${label} must use HTTPS`);
  assert.equal(url.hostname, 'www.scrapethissite.com', `${label} must stay on the reviewed host`);
  assert.equal(url.port, '', `${label} must not use a non-default port`);
  assert(!url.username && !url.password && !url.hash, `${label} must not contain credentials or a fragment`);
  assert.equal(url.pathname, '/pages/forms/', `${label} must stay on the hockey form path`);
  for (const key of url.searchParams.keys()) {
    assert(['page_num', 'per_page', 'q'].includes(key), `${label} contains unreviewed query parameter ${key}`);
  }
  return url;
}

export function assertHockeyPageURL(raw: string, query: string): URL {
  const url = hockeyURL(raw, 'Hockey result URL');
  assert.equal(url.searchParams.get('q'), query, 'Hockey result URL must retain the exact query');
  const page = Number(url.searchParams.get('page_num') ?? '1');
  assert(Number.isSafeInteger(page) && page >= 1 && page <= HOCKEY_MAX_PAGES,
    `Hockey page number must be within ${HOCKEY_MAX_PAGES}`);
  return url;
}

/** Self-contained for Playwright page evaluation. */
export function collectVisibleHockeyRows(): Array<Record<string, string>> {
  const norm = (value: unknown) => String(value ?? '').replace(/\s+/g, ' ').trim();
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true') return false;
      const style = getComputedStyle(current);
      if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return false;
      current = current.parentElement;
    }
    const rect = element.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  };
  return Array.from(document.querySelectorAll('tr.team'))
    .filter(visible)
    .map((row) => {
      const cell = (name: string) => norm(row.querySelector(`.${name}`)?.textContent);
      return {
        team_name: cell('name'), year: cell('year'), wins: cell('wins'), losses: cell('losses'),
        win_percentage: cell('pct'), goals_for: cell('gf'),
        goals_against: cell('ga'), goal_difference: cell('diff'),
      };
    });
}

/** Self-contained for Playwright page evaluation. */
export function collectVisibleHockeyNextHrefs(): string[] {
  return Array.from(document.querySelectorAll<HTMLAnchorElement>('ul.pagination li:not(.disabled) a[href]'))
    .filter((link) => /^(next|»|›)$/i.test((link.textContent ?? '').replace(/\s+/g, ' ').trim()))
    .filter((link) => {
      const rect = link.getBoundingClientRect();
      const style = getComputedStyle(link);
      return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden';
    })
    .map((link) => link.href);
}

/** Self-contained for Playwright page evaluation. */
export function collectVisibleHockeyPageOneHrefs(): string[] {
  return Array.from(document.querySelectorAll<HTMLAnchorElement>('ul.pagination li:not(.disabled) a[href]'))
    .filter((link) => (link.textContent ?? '').replace(/\s+/g, ' ').trim() === '1')
    .filter((link) => {
      const rect = link.getBoundingClientRect();
      const style = getComputedStyle(link);
      return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden';
    })
    .map((link) => link.href);
}

export function assertHockeyCanonicalPageOneHref(links: readonly string[], query: string): URL {
  assert.equal(links.length, 1, 'Hockey query-only result must expose one visible page-1 control');
  const canonical = assertHockeyPageURL(links[0], query);
  assert.equal(canonical.searchParams.get('page_num'), '1', 'Hockey page-1 control must retain canonical page 1');
  return canonical;
}

export function hockeyCanonicalPageOneIndex(
  candidates: readonly { href: string; text: string }[],
  canonicalHref: string,
): number {
  const matches = candidates
    .map((candidate, index) => ({ candidate, index }))
    .filter(({ candidate }) => normalizedText(candidate.text) === '1' && candidate.href === canonicalHref);
  assert.equal(matches.length, 1, 'Hockey canonical page-1 Playwright target must be unique');
  return matches[0].index;
}

async function canonicalizeHockeyFirstPage(page: Page, query: string): Promise<void> {
  const links = await page.evaluate(collectVisibleHockeyPageOneHrefs);
  if (!links.length) {
    // Single-page results render an empty pagination list: no page-1 anchor
    // exists to click and the query-only result state is already canonical.
    const next = await page.evaluate(collectVisibleHockeyNextHrefs);
    assert(!next.length, 'Hockey page exposes a Next control without page-1 controls');
    const url = new URL(page.url());
    assert.equal(url.searchParams.get('q'), query, 'Hockey single-page result must retain the exact query');
    assert(!url.searchParams.has('page_num'), 'Hockey single-page result must stay on the canonical query-only state');
    return;
  }
  const canonical = assertHockeyCanonicalPageOneHref(links, query);
  const visibleLinks = page.locator('ul.pagination li:not(.disabled) a[href]:visible');
  const candidates = await visibleLinks.evaluateAll((elements) => elements.map((element) => ({
    href: (element as HTMLAnchorElement).href,
    text: element.textContent ?? '',
  })));
  const pageOne = visibleLinks.nth(hockeyCanonicalPageOneIndex(candidates, canonical.toString()));
  await Promise.all([
    page.waitForURL((url) => url.searchParams.get('q') === query
      && url.searchParams.get('page_num') === '1', { waitUntil: 'domcontentloaded', timeout: 45_000 }),
    pageOne.click(),
  ]);
}

function parseInteger(value: string, label: string): number {
  assert(/^-?\d+$/.test(value), `${label} must be a canonical integer`);
  const parsed = Number(value);
  assert(Number.isSafeInteger(parsed), `${label} is outside the safe integer range`);
  return parsed;
}

export function canonicalHockeyRows(values: readonly unknown[], query: string, label: string): HockeyRow[] {
  assertHockeyQuery(query);
  const identities = new Set<string>();
  return values.map((value, index) => {
    assert(plainObject(value), `${label} row ${index} must be an object`);
    assert.deepEqual(Object.keys(value).sort(), [...HOCKEY_FIELDS], `${label} row ${index} has schema drift`);
    const team_name = normalizedText(value.team_name);
    assert(team_name && team_name.toLowerCase().includes(query.toLowerCase()),
      `${label} row ${index} does not match the exact retained query`);
    const numeric = (field: string): number => {
      const raw = value[field];
      if (typeof raw === 'number') {
        assert(Number.isFinite(raw), `${label} row ${index} ${field} must be finite`);
        if (INTEGER_FIELDS.has(field)) assert(Number.isSafeInteger(raw), `${label} row ${index} ${field} must be integer`);
        return raw;
      }
      return INTEGER_FIELDS.has(field)
        ? parseInteger(normalizedText(raw), `${label} row ${index} ${field}`)
        : Number(normalizedText(raw));
    };
    const row: HockeyRow = {
      team_name, year: numeric('year'), wins: numeric('wins'), losses: numeric('losses'),
      win_percentage: numeric('win_percentage'), goals_for: numeric('goals_for'),
      goals_against: numeric('goals_against'), goal_difference: numeric('goal_difference'),
    };
    assert(Number.isFinite(row.win_percentage) && row.win_percentage >= 0 && row.win_percentage <= 1,
      `${label} row ${index} win_percentage must be within 0..1`);
    const identity = `${row.team_name}\u0000${row.year}`;
    assert(!identities.has(identity), `${label} contains duplicate ${row.team_name} ${row.year}`);
    identities.add(identity);
    return row;
  });
}

function browserForOracle(source: Browser | BrowserContext): Browser {
  if ('newContext' in source) return source;
  const browser = source.browser();
  assert(browser, 'Hockey oracle requires a browser-backed context');
  return browser;
}

export async function collectHockeyOracle(source: Browser | BrowserContext, query: string): Promise<HockeyOracle> {
  assertHockeyQuery(query);
  const context = await browserForOracle(source).newContext();
  try {
    const page = await context.newPage();
    await page.goto(HOCKEY_ENTRY_URL, { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await page.locator('#q').fill(query);
    await Promise.all([
      page.waitForURL((url) => url.searchParams.get('q') === query, { waitUntil: 'domcontentloaded', timeout: 45_000 }),
      page.locator('form.form-inline button[type="submit"], form.form-inline input[type="submit"]').first().click(),
    ]);
    await canonicalizeHockeyFirstPage(page, query);
    const pages: string[] = [];
    const rows: HockeyRow[] = [];
    while (true) {
      const url = assertHockeyPageURL(page.url(), query);
      assert(!pages.includes(url.toString()), 'Hockey pagination cycle detected');
      pages.push(url.toString());
      assert(pages.length <= HOCKEY_MAX_PAGES, `Hockey pagination exceeds ${HOCKEY_MAX_PAGES} pages`);
      await page.locator('tr.team').first().waitFor({ state: 'visible', timeout: 30_000 });
      const current = canonicalHockeyRows(await page.evaluate(collectVisibleHockeyRows), query, `Hockey ${query}`);
      assert(current.length > 0, `Hockey ${query} page has no complete visible rows`);
      rows.push(...current);
      const next = await page.evaluate(collectVisibleHockeyNextHrefs);
      assert(next.length <= 1, 'Hockey page exposes multiple visible Next controls');
      if (!next.length) break;
      const nextURL = assertHockeyPageURL(next[0], query);
      assert.equal(Number(nextURL.searchParams.get('page_num')), pages.length + 1,
        'Hockey Next must advance exactly one page');
      await page.goto(nextURL.toString(), { waitUntil: 'domcontentloaded', timeout: 45_000 });
    }
    return { query, pages, rows: canonicalHockeyRows(rows, query, `Hockey ${query} oracle`) };
  } finally {
    await context.close();
  }
}

export function hockeyRowsHash(rows: readonly unknown[], query: string): string {
  return createHash('sha256').update(JSON.stringify(canonicalHockeyRows(rows, query, 'Hockey hash'))).digest('hex');
}

export function assertHockeyRowsEqual(rows: unknown[], schema: unknown, expected: readonly HockeyRow[], query: string, label: string): void {
  assert(plainObject(schema) && plainObject(schema.properties), `${label} output schema must be an object`);
  assert.deepEqual(Object.keys(schema.properties).sort(), [...HOCKEY_FIELDS], `${label} output schema fields changed`);
  const actual = canonicalHockeyRows(rows, query, label);
  assert.deepEqual(actual, canonicalHockeyRows(expected, query, `${label} oracle`), `${label} rows differ from oracle`);
}

export function assertHockeyRequirement(value: unknown): void {
  assert(plainObject(value), 'Hockey requirement must be an object');
  assert(Array.isArray(value.requiredInputs) && value.requiredInputs.length === 1, 'Hockey requires one input');
  const input = value.requiredInputs[0];
  assert(plainObject(input));
  assert.deepEqual([input.name, input.type], ['team_query', 'string']);
  assert.deepEqual(value.optionalInputs, []);
  assert(Array.isArray(value.outputFields));
  const fields = value.outputFields.filter(plainObject).map((field) => String(field.name)).sort();
  assert.deepEqual(fields, [...HOCKEY_FIELDS]);
}

export async function hockeyDemo(page: Page, ensureRecordingReady: () => Promise<void> = async () => undefined): Promise<void> {
  await page.getByRole('heading', { name: 'Hockey Teams: Forms, Searching and Pagination' }).waitFor({ timeout: 45_000 });
  const search = page.locator('#q:visible');
  await search.click();
  await search.pressSequentially(HOCKEY_REPLAY_QUERY, { delay: 120 });
  await Promise.all([
    page.waitForURL((url) => url.searchParams.get('q') === HOCKEY_REPLAY_QUERY, { waitUntil: 'domcontentloaded', timeout: 45_000 }),
    search.press('Enter'),
  ]);
  await canonicalizeHockeyFirstPage(page, HOCKEY_REPLAY_QUERY);
  await ensureRecordingReady();
  await page.locator('tr.team:visible').first().waitFor({ timeout: 30_000 });
  for (let i = 0; i < HOCKEY_MAX_PAGES; i += 1) {
    await page.mouse.wheel(0, 500);
    await page.waitForTimeout(400);
    const next = page.locator('ul.pagination li:not(.disabled) a:visible').filter({ hasText: /^(Next|»|›)$/i });
    const count = await next.count();
    assert(count <= 1, 'Hockey demo found ambiguous Next controls');
    if (!count) return;
    const before = page.url();
    await next.click();
    await page.waitForURL((url) => url.toString() !== before, { waitUntil: 'domcontentloaded', timeout: 45_000 });
    assertHockeyPageURL(page.url(), HOCKEY_REPLAY_QUERY);
    await ensureRecordingReady();
  }
  throw new Error(`Hockey demo exceeded ${HOCKEY_MAX_PAGES} pages`);
}

export function assertHockeyRecording(recording: PageAgentRecording): void {
  assert.equal(recording.version, '2.0.0');
  assert.equal(recording.meta.startUrl, HOCKEY_ENTRY_URL);
  assert.equal(recording.termination?.complete, true);
  assert(!recording.events.some((event) => event.type === 'executeJavascript'));
  assert(recording.events.some((event) => event.type === 'inputText'), 'Hockey recording omitted query input');
  assert(recording.events.some((event) => event.type === 'submitForm'), 'Hockey recording omitted form submission');
  assert.equal(recording.snapshots.length, recording.events.length + 2);
  for (const [index, snapshot] of recording.snapshots.entries()) {
    assert.equal(snapshot.capture?.status, 'complete', `Hockey snapshot ${index} must be complete`);
    if (index > 0 && snapshot.url !== HOCKEY_ENTRY_URL) assertHockeyPageURL(snapshot.url, HOCKEY_REPLAY_QUERY);
  }
}

function actions(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) return value.flatMap(actions);
  if (!plainObject(value)) return [];
  return [...(typeof value.action === 'string' ? [value] : []), ...Object.values(value).flatMap(actions)];
}

export function assertHockeyRule(rule: unknown): void {
  assert(plainObject(rule), 'Hockey rule must be an object');
  assert.deepEqual(Array.isArray(rule.domain) ? rule.domain : [rule.domain], ['www.scrapethissite.com']);
  assert.equal(rule.entry, HOCKEY_ENTRY_URL);
  const serialized = JSON.stringify(rule);
  assert(serialized.includes('{{team_query}}'), 'Hockey rule must bind team_query');
  for (const query of [HOCKEY_REPLAY_QUERY, HOCKEY_BOSTON_QUERY, HOCKEY_DETROIT_QUERY]) {
    assert(!serialized.toLowerCase().includes(query.toLowerCase()), `Hockey rule hardcodes ${query}`);
  }
  assert(!/executeJavascript|solveCaptcha|page_num/.test(serialized), 'Hockey rule contains unsafe or constructed navigation');
  assertVisibleModelExtractionRule(rule, 'Hockey');
  const all = actions([rule.steps, rule.hooks]);
  const queryTypes = all.filter((action) => action.action === 'type'
    && JSON.stringify(action).includes('{{team_query}}'));
  assert.equal(queryTypes.length, 1, 'Hockey rule must type team_query exactly once');
  assert(queryTypes[0].submit === true
    || all.some((action) => action.action === 'pressKey'
      && Array.isArray(action.keys) && action.keys.includes('Enter')),
  'Hockey rule must submit the typed search exactly once');
  const selectors = plainObject(rule.selectors) ? rule.selectors : {};
  const pageOneRefs = new Set(Object.entries(selectors)
    .filter(([, selector]) => /"(?:text|value)":"1"/.test(JSON.stringify(selector)))
    .map(([name]) => name));
  const topLevel = Array.isArray(rule.steps) ? rule.steps.filter(plainObject) : [];
  const paginationLoopIndex = topLevel.findIndex((step) => step.action === 'loop' && step.type === 'fixedCount');
  assert(paginationLoopIndex > 0, 'Hockey rule must place bounded pagination after canonical page selection');
  const queryTypeIndex = topLevel.findIndex((step) => step === queryTypes[0]);
  const isPageOneTarget = (target: unknown): boolean => {
    if (!plainObject(target)) return false;
    // Model-facing intermediate forms carry value+family; the canonical
    // Target contract expresses the same visible text match as an inline
    // text field. Accept both shapes — semantics, not syntax.
    if (target.value === '1' && ['text', 'textVisible'].includes(String(target.family))) return true;
    if (target.text === '1' && target.index === undefined) return true;
    return typeof target.$ref === 'string' && pageOneRefs.has(target.$ref);
  };
  const isPageOneClick = (step: unknown): boolean => plainObject(step) && step.action === 'click' && isPageOneTarget(step.target);
  const canonicalClickIndex = topLevel.findIndex((step, index) => {
    if (index >= paginationLoopIndex) return false;
    if (isPageOneClick(step)) return true;
    // Conditional canonicalization: single-page results expose no page-1
    // control, so the click may be guarded by if(elementExists page-1).
    return plainObject(step) && step.action === 'if' && plainObject(step.condition)
      && String(step.condition.type) === 'elementExists' && isPageOneTarget(step.condition.target)
      && Array.isArray(step.then) && step.then.some(isPageOneClick);
  });
  assert(canonicalClickIndex > queryTypeIndex,
    'Hockey rule must click (or conditionally click) the visible numbered page-1 control before extraction');
  assert(topLevel.slice(queryTypeIndex + 1, canonicalClickIndex).some((step) => {
    if (step.action !== 'navigate' || typeof step.url !== 'string' || step.url.includes('page_num')) return false;
    try {
      const committed = new URL(step.url.replace('{{team_query}}', 'NewYorkSentinel'));
      return committed.origin === HOCKEY_ORIGIN
        && committed.pathname === '/pages/forms/'
        && committed.searchParams.get('q') === 'NewYorkSentinel';
    } catch {
      return false;
    }
  }), 'Hockey rule must await the recorded committed query navigation before page 1');
  const loops = all.filter((action) => action.action === 'loop' && action.type === 'fixedCount');
  assert(loops.some((loop) => Number(loop.count) > 0 && Number(loop.count) <= HOCKEY_MAX_PAGES
    && actions(loop.steps).some((step) => step.action === 'break')
    && actions(loop.steps).some((step) => step.action === 'sendResult')),
  'Hockey rule must use bounded terminal pagination and emit rows');
}

export function bindHockeyInput(inputs: Record<string, unknown>, query: string, label: string): Record<string, unknown> {
  assertHockeyQuery(query);
  assert.deepEqual(Object.keys(inputs), ['team_query'], `${label} must receive only team_query`);
  return { team_query: query };
}

export async function assertHockeyExecutionPage(page: Page, oracle: HockeyOracle): Promise<void> {
  assertHockeyPageURL(page.url(), oracle.query);
  assert.equal((await page.evaluate(collectVisibleHockeyNextHrefs)).length, 0,
    'Hockey execution must finish without a visible Next control');
}
