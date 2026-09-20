#!/usr/bin/env ts-node
import { strict as assert } from 'node:assert';
import { BrowserContext, chromium, Page } from 'playwright';
import { buildCoverageReport } from './qualification/user-action-coverage';

const APPROVAL = 'I approve one read-only mainstream-site qualification';
const BING_ENTRY_URL = 'https://www.bing.com/?cc=us&setlang=en-US';
const BING_QUERY = 'how do solar eclipses happen';
const BING_HOST = 'www.bing.com';
const DUCKDUCKGO_HOST = 'duckduckgo.com';
const REVIEWED_RESULT_HOSTS = new Set([
  'science.nasa.gov',
  'www.britannica.com',
  'en.wikipedia.org',
]);
const ALLOWED_HOSTS = new Set([
  BING_HOST, DUCKDUCKGO_HOST, ...REVIEWED_RESULT_HOSTS, 'github.com',
]);

export type SearchJourneyAction =
  | 'focus-search' | 'type-query' | 'browse-suggestion' | 'select-suggestion'
  | 'submit-query'
  | 'results-ready' | 'scroll-results' | 'open-result' | 'destination-ready'
  | 'scroll-destination' | 'go-back' | 'results-restored';

export interface OrganicResultCandidate { title: string; href: string }

export interface OrganicResultSnapshot extends OrganicResultCandidate {
  visible: boolean;
  excluded: boolean;
}

export function classifyOrganicResults(
  snapshots: readonly OrganicResultSnapshot[],
): OrganicResultCandidate[] {
  const seen = new Set<string>();
  return snapshots.flatMap((snapshot) => {
    const title = snapshot.title.replace(/\s+/g, ' ').trim();
    if (!snapshot.visible || snapshot.excluded || !title) return [];
    let href: string;
    try {
      const parsed = new URL(snapshot.href);
      if (parsed.protocol !== 'https:') return [];
      parsed.hash = '';
      href = parsed.toString();
    } catch { return []; }
    const key = `${title.toLowerCase()}\u0000${href}`;
    if (seen.has(key)) return [];
    seen.add(key);
    return [{ title, href }];
  });
}

export interface SuggestionIdentitySnapshot {
  activeDescendant: string;
  options: Array<{
    id: string;
    text: string;
    visible: boolean;
    ariaSelected: string;
    className: string;
  }>;
}

interface SearchEngineAdapter {
  site: 'bing' | 'duckduckgo';
  host: string;
  entryUrl: string;
  searchBoxSelector: string;
  suggestionRootSelector: string;
  suggestionOptionSelector: string;
  resultRootSelector: string;
  resultLinkSelector: string;
}

const BING_ADAPTER: SearchEngineAdapter = {
  site: 'bing', host: BING_HOST, entryUrl: BING_ENTRY_URL,
  searchBoxSelector: '#sb_form_q',
  suggestionRootSelector: '#sa_ul, [role="listbox"], .sa_drw',
  suggestionOptionSelector: 'li, [role="option"], .sa_sg',
  resultRootSelector: '#b_results',
  resultLinkSelector: 'h2 a[href]',
};

const DUCKDUCKGO_ADAPTER: SearchEngineAdapter = {
  site: 'duckduckgo', host: DUCKDUCKGO_HOST, entryUrl: 'https://duckduckgo.com/',
  searchBoxSelector: '#searchbox_input, input[name="q"]',
  suggestionRootSelector: '[role="listbox"], [data-testid="searchbox_suggestions"]',
  suggestionOptionSelector: '[role="option"], [data-testid="searchbox_suggestion"], li',
  resultRootSelector: '#links, [data-testid="mainline"]',
  resultLinkSelector: '[data-testid="result-title-a"], h2 a',
};

export type MainstreamJourney =
  | 'wikipedia' | 'github' | 'bing-search' | 'duckduckgo-search';

export function resolveMainstreamJourneys(env: NodeJS.ProcessEnv): MainstreamJourney[] {
  const raw = env.AEGIS_MAINSTREAM_JOURNEYS?.trim();
  if (!raw) return ['wikipedia', 'github', 'bing-search'];
  const selected = normalizeVisibleTexts(raw.split(',')) as MainstreamJourney[];
  assert(selected.length > 0, 'AEGIS_MAINSTREAM_JOURNEYS must select at least one journey');
  const allowed = new Set<MainstreamJourney>([
    'wikipedia', 'github', 'bing-search', 'duckduckgo-search',
  ]);
  for (const journey of selected) {
    assert(allowed.has(journey), `unknown mainstream journey: ${journey}`);
  }
  return selected;
}

export function resolveMainstreamEntryPauseMs(env: NodeJS.ProcessEnv): number {
  const raw = env.AEGIS_MAINSTREAM_ENTRY_PAUSE_MS?.trim();
  if (!raw) return 0;
  assert(/^\d+$/.test(raw), 'AEGIS_MAINSTREAM_ENTRY_PAUSE_MS must be an integer');
  const value = Number(raw);
  assert(value === 60_000,
    'AEGIS_MAINSTREAM_ENTRY_PAUSE_MS diagnostic mode permits only exactly 60000');
  return value;
}

export function resolveSkipSuggestions(env: NodeJS.ProcessEnv): boolean {
  const raw = env.AEGIS_MAINSTREAM_SKIP_SUGGESTIONS?.trim();
  if (!raw) return false;
  assert(raw === '1', 'AEGIS_MAINSTREAM_SKIP_SUGGESTIONS must be exactly 1');
  return true;
}

export function requireMainstreamApproval(env: NodeJS.ProcessEnv): void {
  assert.equal(
    env.AEGIS_MAINSTREAM_READONLY_APPROVAL,
    APPROVAL,
    `set AEGIS_MAINSTREAM_READONLY_APPROVAL exactly to "${APPROVAL}"`,
  );
}

export function assertMainstreamUrlAllowed(url: string): void {
  const parsed = new URL(url);
  assert.equal(parsed.protocol, 'https:', `public qualification requires HTTPS: ${url}`);
  assert(ALLOWED_HOSTS.has(parsed.hostname),
    `public qualification blocked unapproved host: ${parsed.hostname}`);
}

async function guardedGoto(page: Page, url: string): Promise<void> {
  assertMainstreamUrlAllowed(url);
  await page.goto(url, { waitUntil: 'load', timeout: 60_000 });
  assertMainstreamUrlAllowed(page.url());
  await waitForDocumentReady(page);
}

async function waitForDocumentReady(page: Page, timeout = 60_000): Promise<void> {
  await page.waitForLoadState('load', { timeout });
  await page.waitForFunction(() => document.readyState === 'complete', undefined, { timeout });
}

export function findPublicBoundary(bodyText: string, title: string): string | undefined {
  const sample = `${title}\n${bodyText.slice(0, 20_000)}`.toLowerCase();
  return [
    'verify you are human',
    'unusual traffic from your computer network',
    'rate limit exceeded',
    'sign in to continue',
    'complete the security check',
    'before you continue to bing',
    'accept all cookies',
  ].find((phrase) => sample.includes(phrase));
}

async function assertNoBoundary(page: Page): Promise<void> {
  const hit = findPublicBoundary(
    await page.locator('body').innerText(),
    await page.title(),
  );
  assert(!hit, `public qualification stopped at human/auth/rate boundary: ${hit}`);
}

async function wikipediaJourney(page: Page) {
  await guardedGoto(page, 'https://en.wikipedia.org/wiki/Special:Search?search=web+scraping');
  await assertNoBoundary(page);
  if (!isWikipediaWebScrapingArticle(page.url())) {
    const result = page.locator('.mw-search-result-heading a').first();
    await result.waitFor({ state: 'visible', timeout: 20_000 });
    assert(await result.getAttribute('href'), 'Wikipedia search result lacks href');
    await result.click();
    await waitForDocumentReady(page);
    assertMainstreamUrlAllowed(page.url());
  }
  await page.getByRole('heading', { name: 'Web scraping', exact: true }).waitFor();
  await page.mouse.wheel(0, 600);
  const title = await page.locator('#firstHeading').innerText();
  const paragraph = await page.locator('#mw-content-text p')
    .filter({ hasText: /\S/ }).first().innerText();
  assert.equal(title.trim(), 'Web scraping');
  assert(paragraph.trim().length > 80,
    'Wikipedia independent text oracle was unexpectedly small');
  return { site: 'wikipedia', url: page.url(), title, paragraphLength: paragraph.length };
}

export function isWikipediaWebScrapingArticle(url: string): boolean {
  const parsed = new URL(url);
  const articlePath = decodeURIComponent(parsed.pathname).toLowerCase().replaceAll(' ', '_');
  return parsed.hostname === 'en.wikipedia.org'
    && articlePath === '/wiki/web_scraping';
}

export function normalizeVisibleTexts(values: readonly string[]): string[] {
  const seen = new Set<string>();
  return values.flatMap((value) => {
    const normalized = value.replace(/\s+/g, ' ').trim();
    if (!normalized || seen.has(normalized)) return [];
    seen.add(normalized);
    return [normalized];
  });
}

export function resolveActiveSuggestionText(
  snapshot: SuggestionIdentitySnapshot,
): string | undefined {
  const visible = snapshot.options.filter((option) => option.visible);
  const activeDescendant = snapshot.activeDescendant.trim();
  const active = activeDescendant
    ? visible.find((option) => option.id === activeDescendant)
    : undefined;
  const selected = active ?? visible.find((option) =>
    option.ariaSelected.toLowerCase() === 'true'
    || option.className.split(/\s+/).some((token) =>
      ['sa_hv', 'active', 'selected'].includes(token.toLowerCase())));
  const text = selected?.text.replace(/\s+/g, ' ').trim();
  return text || undefined;
}

export function visibleSuggestionTexts(snapshot: SuggestionIdentitySnapshot): string[] {
  return normalizeVisibleTexts(snapshot.options
    .filter((option) => option.visible)
    .map((option) => option.text));
}

async function captureSuggestionSnapshot(
  page: Page,
  adapter: SearchEngineAdapter,
): Promise<SuggestionIdentitySnapshot> {
  return page.evaluate((selectors): SuggestionIdentitySnapshot => {
    const input = document.querySelector(selectors.searchBox);
    const candidates = new Set<Element>();
    const addOptions = (root: Element): void => {
      if (root.matches(selectors.options)) candidates.add(root);
      root.querySelectorAll(selectors.options)
        .forEach((option) => candidates.add(option));
    };
    for (const attribute of ['aria-controls', 'aria-owns']) {
      for (const id of (input?.getAttribute(attribute) ?? '').split(/\s+/).filter(Boolean)) {
        const root = document.getElementById(id);
        if (root) addOptions(root);
      }
    }
    document.querySelectorAll(selectors.roots)
      .forEach(addOptions);
    return {
      activeDescendant: input?.getAttribute('aria-activedescendant') ?? '',
      options: Array.from(candidates).map((option) => {
        const element = option as HTMLElement;
        const style = getComputedStyle(element);
        const rect = element.getBoundingClientRect();
        return {
          id: element.id,
          text: element.innerText || element.textContent || '',
          visible: !element.hidden
            && element.getAttribute('aria-hidden') !== 'true'
            && style.display !== 'none'
            && style.visibility !== 'hidden'
            && rect.width > 0
            && rect.height > 0,
          ariaSelected: element.getAttribute('aria-selected') ?? '',
          className: element.className,
        };
      }),
    };
  }, {
    searchBox: adapter.searchBoxSelector,
    roots: adapter.suggestionRootSelector,
    options: adapter.suggestionOptionSelector,
  });
}

export function selectReviewedOrganicResult(
  candidates: readonly OrganicResultCandidate[],
): OrganicResultCandidate | undefined {
  return candidates.find((candidate) => {
    if (!candidate.title.trim()) return false;
    try {
      const url = new URL(candidate.href);
      return url.protocol === 'https:' && REVIEWED_RESULT_HOSTS.has(url.hostname);
    } catch { return false; }
  });
}

export function assertSearchJourneyActionOrder(
  actions: readonly SearchJourneyAction[],
  skipSuggestions = false,
): void {
  const prefix: SearchJourneyAction[] = skipSuggestions
    ? ['focus-search', 'type-query', 'submit-query']
    : [
    'focus-search', 'type-query',
    'browse-suggestion', 'browse-suggestion', 'browse-suggestion',
    'select-suggestion',
  ];
  assert.deepEqual(actions, [
    ...prefix, 'results-ready',
    'scroll-results', 'scroll-results', 'scroll-results',
    'open-result', 'destination-ready',
    'scroll-destination', 'scroll-destination',
    'go-back', 'results-restored',
  ], 'mainstream search journey actions are incomplete or out of order');
}

function normalizeQuery(value: string): string {
  return value.replace(/\s+/g, ' ').trim().toLowerCase();
}

function isSearchResultsPage(
  url: string,
  expectedQuery: string,
  adapter: SearchEngineAdapter,
): boolean {
  const parsed = new URL(url);
  const validPath = adapter.site === 'bing'
    ? parsed.pathname === '/search'
    : parsed.pathname === '/' || parsed.pathname === '/html/';
  return parsed.hostname === adapter.host && validPath
    && normalizeQuery(parsed.searchParams.get('q') ?? '') === normalizeQuery(expectedQuery);
}

async function mainstreamSearchJourney(
  page: Page,
  adapter: SearchEngineAdapter,
  entryPauseMs: number,
  skipSuggestions: boolean,
) {
  const actions: SearchJourneyAction[] = [];
  await guardedGoto(page, adapter.entryUrl);
  if (entryPauseMs > 0) await page.waitForTimeout(entryPauseMs);
  await assertNoBoundary(page);
  const searchBox = page.locator(adapter.searchBoxSelector).first();
  await searchBox.waitFor({ state: 'visible', timeout: 20_000 });
  await searchBox.click();
  actions.push('focus-search');
  await searchBox.pressSequentially(BING_QUERY, { delay: 85 });
  actions.push('type-query');

  let suggestionTexts: string[] = [];
  const browsedSuggestions: string[] = [];
  if (!skipSuggestions) {
    const suggestionsDeadline = Date.now() + 15_000;
    do {
      suggestionTexts = visibleSuggestionTexts(await captureSuggestionSnapshot(page, adapter));
      if (suggestionTexts.length >= 3) break;
      await page.waitForTimeout(100);
    } while (Date.now() < suggestionsDeadline);
    assert(suggestionTexts.length >= 3,
      `${adapter.site} autocomplete exposed ${suggestionTexts.length} visible suggestions; at least 3 are required`);
    for (let index = 0; index < 3; index += 1) {
      await searchBox.press('ArrowDown');
      const deadline = Date.now() + 3_000;
      let selected: string | undefined;
      do {
        const snapshot = await captureSuggestionSnapshot(page, adapter);
        selected = resolveActiveSuggestionText(snapshot);
        if (selected && !browsedSuggestions.some((value) =>
          normalizeQuery(value) === normalizeQuery(selected!))) break;
        selected = undefined;
        await page.waitForTimeout(100);
      } while (Date.now() < deadline);
      assert(selected,
        `${adapter.site} autocomplete action ${index + 1} has no distinct visible active suggestion`);
      browsedSuggestions.push(selected);
      actions.push('browse-suggestion');
    }
    assert(new Set(browsedSuggestions.map(normalizeQuery)).size === 3,
      `${adapter.site} autocomplete keyboard browsing did not visit three distinct suggestions`);
    for (const selected of browsedSuggestions) {
      assert(suggestionTexts.some((suggestion) => normalizeQuery(suggestion) === normalizeQuery(selected)),
        `${adapter.site} autocomplete selected an unobserved suggestion: ${selected}`);
    }
  }

  const selectedQuery = skipSuggestions ? BING_QUERY : browsedSuggestions.at(-1)!;
  await Promise.all([
    page.waitForURL((url) =>
      isSearchResultsPage(url.toString(), selectedQuery, adapter), {
      timeout: 60_000, waitUntil: 'load',
    }),
    searchBox.press('Enter'),
  ]);
  actions.push(skipSuggestions ? 'submit-query' : 'select-suggestion');
  await waitForDocumentReady(page);
  await assertNoBoundary(page);
  const resultRoot = page.locator(adapter.resultRootSelector).first();
  await resultRoot.waitFor({ state: 'visible', timeout: 20_000 });
  const resultLinks = resultRoot.locator(adapter.resultLinkSelector);
  const snapshots = await resultLinks.evaluateAll((anchors): OrganicResultSnapshot[] =>
    anchors.map((anchor) => {
      const element = anchor as HTMLAnchorElement;
      const card = element.closest([
        'li', 'article', '[data-testid="result"]', '.b_algo', '.result',
      ].join(', ')) ?? element.parentElement;
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      const cardClass = String((card as HTMLElement | null)?.className ?? '');
      const excludedClass = cardClass.split(/\s+/).some((token) => [
        'b_ad', 'b_ans', 'b_pag', 'b_msg', 'ad', 'advertisement', 'sponsored',
      ].includes(token.toLowerCase()));
      const explicitAdLabel = card?.querySelector([
        '.b_adlabel', '[data-testid="ad"]', '[aria-label="Ad"]',
        '[aria-label="Advertisement"]', '[aria-label="Sponsored"]',
      ].join(', '));
      return {
        title: element.innerText || element.textContent || '',
        href: element.href,
        visible: !element.hidden
          && element.getAttribute('aria-hidden') !== 'true'
          && style.display !== 'none'
          && style.visibility !== 'hidden'
          && rect.width > 0
          && rect.height > 0,
        excluded: excludedClass || explicitAdLabel !== null,
      };
    }));
  const candidates = classifyOrganicResults(snapshots);
  assert(candidates.length >= 5,
    `${adapter.site} exposed ${candidates.length} ordinary organic results; at least 5 are required`);
  actions.push('results-ready');
  for (let index = 0; index < 3; index += 1) {
    await page.mouse.wheel(0, 520);
    await page.waitForTimeout(450);
    actions.push('scroll-results');
  }
  const selectedResult = selectReviewedOrganicResult(candidates);
  assert(selectedResult,
    `${adapter.site} ordinary results contain no reviewed informational destination`);
  const selectedIndex = await resultLinks.evaluateAll((anchors, selected) =>
    anchors.findIndex((anchor) => {
      const element = anchor as HTMLAnchorElement;
      const title = String(element.innerText || element.textContent || '')
        .replace(/\s+/g, ' ').trim();
      const href = new URL(element.href);
      href.hash = '';
      return title === selected.title && href.toString() === selected.href;
    }), selectedResult);
  assert(selectedIndex >= 0, 'selected organic result disappeared before click');
  const selectedAnchor = resultLinks.nth(selectedIndex);
  await selectedAnchor.scrollIntoViewIfNeeded();
  await Promise.all([
    page.waitForURL((url) => {
      try {
        return REVIEWED_RESULT_HOSTS.has(url.hostname);
      } catch { return false; }
    }, { timeout: 60_000, waitUntil: 'load' }),
    selectedAnchor.click(),
  ]);
  actions.push('open-result');
  await waitForDocumentReady(page);
  assertMainstreamUrlAllowed(page.url());
  assert.notEqual(new URL(page.url()).hostname, adapter.host,
    `reviewed result click did not leave ${adapter.site}`);
  await assertNoBoundary(page);
  const heading = page.locator('h1').filter({ hasText: /\S/ }).first();
  await heading.waitFor({ state: 'visible', timeout: 20_000 });
  const destinationTitle = (await heading.innerText()).replace(/\s+/g, ' ').trim();
  const paragraph = page.locator('main p, article p, #mw-content-text p').filter({ hasText: /\S/ }).first();
  const destinationParagraph = (await paragraph.innerText()).replace(/\s+/g, ' ').trim();
  assert(destinationTitle, 'reviewed destination has no visible heading');
  assert(destinationParagraph.length >= 80,
    'reviewed destination independent visible paragraph oracle is unexpectedly small');
  actions.push('destination-ready');
  for (let index = 0; index < 2; index += 1) {
    await page.mouse.wheel(0, 540);
    await page.waitForTimeout(450);
    actions.push('scroll-destination');
  }
  await Promise.all([
    page.waitForURL((url) =>
      isSearchResultsPage(url.toString(), selectedQuery, adapter), {
      timeout: 60_000, waitUntil: 'load',
    }),
    page.goBack({ waitUntil: 'load', timeout: 60_000 }),
  ]);
  actions.push('go-back');
  await waitForDocumentReady(page);
  assert(isSearchResultsPage(page.url(), selectedQuery, adapter),
    `browser Back did not restore the exact selected ${adapter.site} query`);
  await resultRoot.waitFor({ state: 'visible', timeout: 20_000 });
  await assertNoBoundary(page);
  actions.push('results-restored');
  assertSearchJourneyActionOrder(actions, skipSuggestions);
  return {
    site: adapter.site, url: page.url(), typedQuery: BING_QUERY, selectedQuery,
    suggestionCount: suggestionTexts.length, browsedSuggestions,
    organicResultCount: candidates.length, openedResult: selectedResult,
    destinationTitle, destinationParagraphLength: destinationParagraph.length, actions,
  };
}

async function githubJourney(page: Page, context: BrowserContext) {
  await guardedGoto(page, 'https://github.com/microsoft/vscode');
  await assertNoBoundary(page);
  const oracleResponse = await context.request.get(
    'https://api.github.com/repos/microsoft/vscode',
    { headers: { Accept: 'application/vnd.github+json' }, timeout: 30_000 },
  );
  assert.equal(oracleResponse.status(), 200,
    `GitHub public repository oracle returned ${oracleResponse.status()}`);
  const oracle = await oracleResponse.json() as unknown;
  assert(isReviewedGithubRepository(oracle),
    'GitHub API identity does not match the reviewed public repository');
  return {
    site: 'github',
    url: page.url(),
    repository: 'microsoft/vscode',
    oracle: 'api.github.com/repos/microsoft/vscode',
  };
}

export function isReviewedGithubRepository(value: unknown): boolean {
  if (!value || typeof value !== 'object') return false;
  const repository = value as Record<string, unknown>;
  return repository.full_name === 'microsoft/vscode'
    && repository.html_url === 'https://github.com/microsoft/vscode';
}

export async function runMainstreamReadonlyQualification(env = process.env) {
  requireMainstreamApproval(env);
  const selectedJourneys = resolveMainstreamJourneys(env);
  const entryPauseMs = resolveMainstreamEntryPauseMs(env);
  const skipSuggestions = resolveSkipSuggestions(env);
  const browser = await chromium.launch({ headless: env.AEGIS_MAINSTREAM_HEADED !== '1' });
  const context = await browser.newContext({ acceptDownloads: false, serviceWorkers: 'block' });
  await context.route('**/*', async (route) => {
    if (route.request().resourceType() === 'document') {
      try {
        assertMainstreamUrlAllowed(route.request().url());
      } catch {
        await route.abort('blockedbyclient');
        return;
      }
    }
    await route.continue();
  });
  const page = await context.newPage();
  const startedAt = new Date().toISOString();
  try {
    const journeys = [];
    for (const journey of selectedJourneys) {
      if (journey === 'wikipedia') journeys.push(await wikipediaJourney(page));
      if (journey === 'github') journeys.push(await githubJourney(page, context));
      if (journey === 'bing-search') {
        journeys.push(await mainstreamSearchJourney(
          page, BING_ADAPTER, entryPauseMs, skipSuggestions,
        ));
      }
      if (journey === 'duckduckgo-search') {
        journeys.push(await mainstreamSearchJourney(
          page, DUCKDUCKGO_ADAPTER, entryPauseMs, skipSuggestions,
        ));
      }
    }
    return {
      schema: 'aegiscrawler.mainstream-readonly-qualification.v1',
      startedAt,
      finishedAt: new Date().toISOString(),
      journeys,
      coverage: buildCoverageReport(),
    };
  } finally {
    await context.close();
    await browser.close();
  }
}

if (require.main === module) {
  runMainstreamReadonlyQualification()
    .then((report) => console.log(JSON.stringify(report, null, 2)))
    .catch((error) => {
      console.error(`[mainstream-readonly] ${error instanceof Error ? error.message : String(error)}`);
      process.exitCode = 1;
    });
}
