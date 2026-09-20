import assert from 'node:assert/strict';
import type { Browser, BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { assertVisibleModelExtractionRule, plainObject } from './model-contract';

export const QUOTES_BY_TAG_ORIGIN = 'https://quotes.toscrape.com';
export const QUOTES_BY_TAG_ENTRY_URL = `${QUOTES_BY_TAG_ORIGIN}/`;
export const QUOTES_REPLAY_TAG = 'love';
export const QUOTES_TASK_TAG = 'humor';
export const QUOTES_MAX_PAGES = 10;

export interface QuoteRow extends Record<string, string> {
  quote: string;
  author: string;
  author_url: string;
}

export interface QuotesPageOracle {
  url: string;
  tag: string;
  page: number;
  rows: QuoteRow[];
}

export interface QuotesPaginationOracle {
  tag: string;
  pages: QuotesPageOracle[];
  rows: QuoteRow[];
}

export interface QuotesPaginationOracleOptions {
  minPages?: number;
  minRows?: number;
}

export interface QuotesContract {
  inputName: string;
  replayTag: string;
  taskTag: string;
  outputMap: Record<string, keyof QuoteRow>;
}

export const QUOTES_BY_TAG_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    quote: { type: 'string' },
    author: { type: 'string' },
    author_url: { type: 'string' },
  },
  required: ['quote', 'author', 'author_url'],
  additionalProperties: false,
} as const;

export const QUOTES_BY_TAG_REQUIREMENT = {
  title: 'Collect every quote for a tag',
  description: [
    'Navigate to the public Quotes to Scrape tag page using the required tag input.',
    'Use a fixedCount pagination loop with a positive count of at most 10 to collect every visible quote in page order across all pages.',
    'After each page, stop the loop when no visible Next link remains; otherwise click Next and continue.',
    'Return exactly quote, author, and author_url as strings.',
  ].join(' '),
  requiredInputs: [{
    name: 'tag',
    type: 'string',
    description: 'Lowercase quote tag slug',
    constraints: { pattern: '^[a-z0-9]+(?:-[a-z0-9]+)*$', maxLength: 64 },
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'quote', type: 'string', description: 'Visible quote text' },
    { name: 'author', type: 'string', description: 'Visible author name' },
    { name: 'author_url', type: 'string', description: 'Same-origin author detail URL' },
  ],
  sampleOutput: {
    quote: 'A visible quotation.',
    author: 'Example Author',
    author_url: `${QUOTES_BY_TAG_ORIGIN}/author/Example-Author/`,
  },
};

/** Self-contained so Playwright can serialize it directly into a page. */
export function collectVisibleQuoteRows(): QuoteRow[] {
  const normalize = (value: string | null | undefined): string =>
    String(value ?? '').replace(/\s+/g, ' ').trim();
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true') return false;
      const style = getComputedStyle(current);
      if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse') return false;
      current = current.parentElement;
    }
    return true;
  };
  const text = (element: Element): string =>
    normalize((element as HTMLElement).innerText || element.textContent);
  const rows: QuoteRow[] = [];
  for (const card of Array.from(document.querySelectorAll('.quote'))) {
    if (!visible(card)) continue;
    const quoteElement = card.querySelector('.text');
    const authorElement = card.querySelector('.author');
    const authorLink = card.querySelector<HTMLAnchorElement>('a[href*="/author/"]');
    if (!quoteElement || !authorElement || !authorLink
      || !visible(quoteElement) || !visible(authorElement) || !visible(authorLink)) continue;
    const quote = text(quoteElement);
    const author = text(authorElement);
    const href = authorLink.getAttribute('href');
    if (!quote || !author || !href) continue;
    let author_url = '';
    try {
      author_url = new URL(href, document.baseURI).toString();
    } catch {
      continue;
    }
    rows.push({ quote, author, author_url });
  }
  return rows;
}

export interface QuotesTagPage {
  url: string;
  tag: string;
  page: number;
}

export function assertValidQuotesTag(value: string): string {
  assert.equal(value, value.trim(), 'quotes tag must not contain surrounding whitespace');
  assert(/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(value) && value.length <= 64,
    'quotes tag must be a lowercase slug of at most 64 characters');
  return value;
}

export function quotesTagPageBlockForURL(rawURL: string, expectedTag: string): string {
  try {
    assertValidQuotesTag(expectedTag);
  } catch {
    return 'the expected quotes tag is not a safe lowercase slug';
  }
  let url: URL;
  try {
    url = new URL(rawURL);
  } catch {
    return 'the browser is not on a valid Quotes to Scrape URL';
  }
  if (url.protocol !== 'https:') return `unexpected ${url.protocol} protocol`;
  if (url.hostname !== 'quotes.toscrape.com' || url.port || url.username || url.password) {
    return `unexpected cross-origin page at ${url.hostname || 'unknown host'}`;
  }
  if (url.search) return 'unexpected query parameters on the quotes tag page';
  if (url.hash) return 'unexpected fragment on the quotes tag page';
  const match = url.pathname.match(/^\/tag\/([^/]+)\/(?:page\/([1-9]\d*)\/)?$/);
  if (!match) return 'the browser is not on a canonical quotes tag page';
  let actualTag = '';
  try {
    actualTag = decodeURIComponent(match[1]);
  } catch {
    return 'the quotes tag URL is malformed';
  }
  if (actualTag !== expectedTag) return 'the quotes page was produced by an unexpected tag';
  if (match[1] !== encodeURIComponent(expectedTag) || match[2] === '1') {
    return 'the browser is not on a canonical quotes tag page';
  }
  const page = match[2] === undefined ? 1 : Number(match[2]);
  if (!Number.isSafeInteger(page) || page < 1 || page > QUOTES_MAX_PAGES) {
    return `the quotes page exceeds the ${QUOTES_MAX_PAGES}-page bound`;
  }
  return '';
}

export function validateQuotesTagPageURL(rawURL: string, expectedTag: string): QuotesTagPage {
  const block = quotesTagPageBlockForURL(rawURL, expectedTag);
  assert(!block, `Quotes to Scrape URL rejected: ${block}`);
  const url = new URL(rawURL);
  const pageMatch = url.pathname.match(/\/page\/([1-9]\d*)\/$/);
  const page = pageMatch ? Number(pageMatch[1]) : 1;
  return {
    url: page === 1
      ? `${QUOTES_BY_TAG_ORIGIN}/tag/${encodeURIComponent(expectedTag)}/`
      : `${QUOTES_BY_TAG_ORIGIN}/tag/${encodeURIComponent(expectedTag)}/page/${page}/`,
    tag: expectedTag,
    page,
  };
}

function browserForOracle(source: Browser | BrowserContext): Browser {
  if ('newContext' in source) return source;
  const browser = source.browser();
  assert(browser, 'quotes pagination oracle requires a browser-backed context');
  return browser;
}

/**
 * Collects a second, independent view of all tag pages. It deliberately uses a
 * fresh context, so replay/task cookies and page state cannot satisfy the oracle.
 */
export async function collectQuotesPaginationOracle(
  source: Browser | BrowserContext,
  tag: string,
  options: QuotesPaginationOracleOptions = {},
): Promise<QuotesPaginationOracle> {
  assertValidQuotesTag(tag);
  const context = await browserForOracle(source).newContext();
  try {
    const page = await context.newPage();
    let nextURL: string | undefined = `${QUOTES_BY_TAG_ORIGIN}/tag/${encodeURIComponent(tag)}/`;
    const pages: QuotesPageOracle[] = [];
    const rows: QuoteRow[] = [];
    const visited = new Set<string>();
    const rowKeys = new Set<string>();
    while (nextURL) {
      assert(pages.length < QUOTES_MAX_PAGES,
        `quotes pagination exceeds the ${QUOTES_MAX_PAGES}-page bound`);
      await page.goto(nextURL, { waitUntil: 'domcontentloaded', timeout: 30_000 });
      const parsed = validateQuotesTagPageURL(page.url(), tag);
      assert.equal(parsed.page, pages.length + 1, 'quotes pagination must advance one page at a time');
      assert(!visited.has(parsed.url), `quotes pagination cycle detected at page ${parsed.page}`);
      visited.add(parsed.url);
      const pageRows = canonicalQuoteRows(
        await page.evaluate(collectVisibleQuoteRows),
        `quotes page ${parsed.page}`,
      );
      assert(pageRows.length > 0, `quotes page ${parsed.page} has no complete visible rows`);
      for (const row of pageRows) {
        const key = JSON.stringify(row);
        assert(!rowKeys.has(key), `quotes pagination returned a duplicate row on page ${parsed.page}`);
        rowKeys.add(key);
        rows.push(row);
      }
      pages.push({ ...parsed, rows: pageRows });
      const href = await page.evaluate(() => {
        const normalize = (value: string | null | undefined): string =>
          String(value ?? '').replace(/\s+/g, ' ').trim();
        const visible = (element: Element): boolean => {
          let current: Element | null = element;
          while (current) {
            if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true') return false;
            const style = getComputedStyle(current);
            if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse') return false;
            current = current.parentElement;
          }
          return true;
        };
        const candidates = Array.from(document.querySelectorAll<HTMLAnchorElement>(
          'li.next > a, .pager .next > a, a[rel="next"]',
        )).filter(visible);
        if (candidates.length === 0) return undefined;
        if (candidates.length !== 1) throw new Error('quotes page has multiple visible next-page links');
        const candidate = candidates[0];
        if (!/next/i.test(normalize(candidate.textContent)) && candidate.getAttribute('rel') !== 'next') {
          throw new Error('quotes next-page link is not labeled as next');
        }
        return candidate.href;
      });
      if (!href) {
        nextURL = undefined;
        continue;
      }
      const next = validateQuotesTagPageURL(href, tag);
      assert.equal(next.page, parsed.page + 1, 'quotes next link must advance exactly one page');
      assert(!visited.has(next.url), `quotes pagination cycle detected at page ${next.page}`);
      nextURL = next.url;
    }
    const minPages = options.minPages ?? 2;
    const minRows = options.minRows ?? 11;
    assert(pages.length >= minPages,
      `quotes pagination oracle requires at least ${minPages} page(s)`);
    assert(rows.length >= minRows,
      `quotes pagination oracle requires at least ${minRows} unique row(s)`);
    return { tag, pages, rows };
  } finally {
    await context.close();
  }
}

const QUOTE_FIELDS = ['author', 'author_url', 'quote'];

function canonicalQuoteRows(values: readonly unknown[], label: string): QuoteRow[] {
  const keys = new Set<string>();
  return values.map((value, index) => {
    assert(plainObject(value), `${label} row ${index} must be an object`);
    assert.deepEqual(Object.keys(value).sort(), QUOTE_FIELDS,
      `${label} row ${index} must contain exactly author, author_url, and quote`);
    const quote = String(value.quote ?? '').replace(/\s+/g, ' ').trim();
    const author = String(value.author ?? '').replace(/\s+/g, ' ').trim();
    const author_url = String(value.author_url ?? '').trim();
    assert(quote, `${label} row ${index} has an empty quote`);
    assert(author, `${label} row ${index} has an empty author`);
    let url: URL;
    try {
      url = new URL(author_url, QUOTES_BY_TAG_ORIGIN);
    } catch {
      throw new Error(`${label} row ${index} has an invalid author_url`);
    }
    assert.equal(url.origin, QUOTES_BY_TAG_ORIGIN,
      `${label} row ${index} author_url must remain on quotes.toscrape.com`);
    assert(/^\/author\/[^/]+\/?$/.test(url.pathname) && !url.search && !url.hash,
      `${label} row ${index} has a noncanonical author_url`);
    assert(!url.username && !url.password,
      `${label} row ${index} author_url must not contain credentials`);
    const row = { quote, author, author_url: url.toString() };
    const key = JSON.stringify(row);
    assert(!keys.has(key), `${label} results contain a duplicate row at index ${index}`);
    keys.add(key);
    return row;
  });
}

export function assertQuotesRowsEqual(
  rows: unknown[],
  outputSchema: unknown,
  expected: readonly QuoteRow[],
  phase: string,
): void {
  assert(plainObject(outputSchema), `${phase} quotes output schema must be an object`);
  const properties = plainObject(outputSchema.properties) ? outputSchema.properties : {};
  assert.deepEqual(Object.keys(properties).sort(), QUOTE_FIELDS,
    `${phase} quotes output schema must contain only author, author_url, and quote`);
  assert.deepEqual(
    (Array.isArray(outputSchema.required) ? outputSchema.required.map(String) : []).sort(),
    QUOTE_FIELDS,
    `${phase} quotes output fields must all be required`,
  );
  assert.equal(outputSchema.additionalProperties, false,
    `${phase} quotes output schema must reject additional properties`);
  for (const field of QUOTE_FIELDS) {
    assert.equal(plainObject(properties[field]) ? properties[field].type : undefined, 'string',
      `${phase} quotes output field ${field} must have type string`);
  }
  const actualRows = canonicalQuoteRows(rows, phase);
  const expectedRows = canonicalQuoteRows(expected, `${phase} oracle`);
  assert.deepEqual(actualRows, expectedRows,
    `${phase} quotes results must exactly match the independent pagination oracle in page order`);
}

export function assertQuotesContractRowsEqual(
  rows: unknown[],
  outputSchema: unknown,
  expected: readonly QuoteRow[],
  contract: QuotesContract,
  phase: string,
): void {
  assert(plainObject(outputSchema), `${phase} quotes output schema must be an object`);
  const properties = plainObject(outputSchema.properties) ? outputSchema.properties : {};
  const outputNames = Object.keys(contract.outputMap).sort();
  assert.deepEqual(Object.keys(properties).sort(), outputNames,
    `${phase} quotes output schema must contain exactly ${outputNames.join(', ')}`);
  assert.deepEqual(
    (Array.isArray(outputSchema.required) ? outputSchema.required.map(String) : []).sort(),
    outputNames,
    `${phase} quotes output fields must all be required`,
  );
  assert.equal(outputSchema.additionalProperties, false,
    `${phase} quotes output schema must reject additional properties`);
  for (const field of outputNames) {
    assert.equal(plainObject(properties[field]) ? properties[field].type : undefined, 'string',
      `${phase} quotes output field ${field} must have type string`);
  }

  const canonicalExpected = canonicalQuoteRows(expected, `${phase} oracle`).map((row) =>
    Object.fromEntries(Object.entries(contract.outputMap).map(([outputName, sourceName]) =>
      [outputName, row[sourceName]])));
  const seen = new Set<string>();
  const actual = rows.map((value, index) => {
    assert(plainObject(value), `${phase} row ${index} must be an object`);
    assert.deepEqual(Object.keys(value).sort(), outputNames,
      `${phase} row ${index} must contain exactly ${outputNames.join(', ')}`);
    const normalized = Object.fromEntries(Object.entries(contract.outputMap).map(([outputName, sourceName]) => {
      const raw = String(value[outputName] ?? '');
      if (sourceName !== 'author_url') {
        const text = raw.replace(/\s+/g, ' ').trim();
        assert(text, `${phase} row ${index} has an empty ${outputName}`);
        return [outputName, text];
      }
      let url: URL;
      try {
        url = new URL(raw.trim(), QUOTES_BY_TAG_ORIGIN);
      } catch {
        throw new Error(`${phase} row ${index} has an invalid ${outputName}`);
      }
      assert.equal(url.origin, QUOTES_BY_TAG_ORIGIN,
        `${phase} row ${index} ${outputName} must remain on quotes.toscrape.com`);
      assert(/^\/author\/[^/]+\/?$/.test(url.pathname) && !url.search && !url.hash,
        `${phase} row ${index} has a noncanonical ${outputName}`);
      return [outputName, url.toString()];
    }));
    const key = JSON.stringify(normalized);
    assert(!seen.has(key), `${phase} results contain a duplicate row at index ${index}`);
    seen.add(key);
    return normalized;
  });
  assert.deepEqual(actual, canonicalExpected,
    `${phase} quotes results must exactly match the independent pagination oracle in page order`);
}

export function assertQuotesRequirement(requirement: unknown): void {
  assert(plainObject(requirement), 'normalized quotes requirement must be an object');
  assert(Array.isArray(requirement.requiredInputs), 'quotes requiredInputs must be an array');
  const required = requirement.requiredInputs.filter(plainObject);
  assert.deepEqual(required.map((input) => [input.name, input.type]), [['tag', 'string']],
    'quotes requirement must declare tag:string as its only required input');
  const constraints = plainObject(required[0]?.constraints) ? required[0].constraints : {};
  assert.equal(constraints.pattern, '^[a-z0-9]+(?:-[a-z0-9]+)*$');
  assert.equal(constraints.maxLength, 64);
  assert.deepEqual(requirement.optionalInputs, [], 'quotes requirement must not declare optional inputs');
  assert(Array.isArray(requirement.outputFields), 'quotes outputFields must be an array');
  const outputs = requirement.outputFields.filter(plainObject);
  assert.deepEqual(outputs.map((field) => [field.name, field.type]).sort(), [
    ['author', 'string'],
    ['author_url', 'string'],
    ['quote', 'string'],
  ], 'quotes requirement must preserve exactly three string output fields');
}

export function assertQuotesContractRequirement(
  requirement: unknown,
  contract: QuotesContract,
): void {
  assert(plainObject(requirement), 'normalized quotes requirement must be an object');
  assert(Array.isArray(requirement.requiredInputs), 'quotes requiredInputs must be an array');
  const required = requirement.requiredInputs.filter(plainObject);
  assert.deepEqual(required.map((input) => [input.name, input.type]), [[contract.inputName, 'string']],
    `quotes requirement must declare ${contract.inputName}:string as its only required input`);
  const constraints = plainObject(required[0]?.constraints) ? required[0].constraints : {};
  assert.equal(constraints.pattern, '^[a-z0-9]+(?:-[a-z0-9]+)*$');
  assert.equal(constraints.maxLength, 64);
  assert.deepEqual(requirement.optionalInputs, [], 'quotes requirement must not declare optional inputs');
  assert(Array.isArray(requirement.outputFields), 'quotes outputFields must be an array');
  const outputs = requirement.outputFields.filter(plainObject);
  const expected = Object.keys(contract.outputMap).sort().map((name) => [name, 'string']);
  assert.deepEqual(outputs.map((field) => [field.name, field.type]).sort(), expected,
    `quotes requirement must preserve exactly ${Object.keys(contract.outputMap).join(', ')}`);
}

export function assertQuotesRecording(recording: PageAgentRecording): void {
  assert.equal(recording.version, '2.0.0', 'quotes recording must use recording v2');
  assert.equal(recording.meta.sanitizationVersion, 'extension-v2',
    'quotes recording must retain extension-v2 sanitization provenance');
  assert.equal(recording.termination?.complete, true, 'quotes recording must terminate completely');
  assert(recording.events.length >= 2 && recording.events.length <= 12,
    'quotes recording must contain between 2 and 12 human events');
  assert(recording.events.filter((event) => event.type === 'click').length >= 2,
    'quotes recording must contain the tag and Next clicks');
  assert(!recording.events.some((event) => event.type === 'executeJavascript'),
    'quotes recording must not contain executeJavascript');
  assert(recording.snapshots.length === recording.events.length + 2,
    'quotes recording must retain initial, pre-action, and final snapshots');
  const pages = recording.snapshots.map((snapshot, index) => {
    assert(snapshot.capture?.status === 'complete', `quotes snapshot ${index} must be complete`);
    assert(!(snapshot.capture?.frames ?? []).some((frame) => frame.status !== 'captured'),
      `quotes snapshot ${index} contains an unavailable frame`);
    const url = new URL(snapshot.url);
    assert.equal(url.origin, QUOTES_BY_TAG_ORIGIN);
    if (url.pathname === '/' && !url.search && !url.hash) return 0;
    return validateQuotesTagPageURL(snapshot.url, QUOTES_REPLAY_TAG).page;
  });
  assert.equal(pages.at(-1), 2, 'quotes recording must finish on love page 2');
  assert(pages.includes(1), 'quotes recording must observe the first love tag page');
  assert(pages.includes(2), 'quotes recording must observe the second love tag page');
  assert(pages.every((page, index) => index === 0 || page >= pages[index - 1]),
    'quotes recording page states must advance monotonically');
}

function actionObjects(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) return value.flatMap(actionObjects);
  if (!plainObject(value)) return [];
  return [
    ...(typeof value.action === 'string' ? [value] : []),
    ...Object.values(value).flatMap(actionObjects),
  ];
}

function resolvedTarget(target: unknown, selectors: Record<string, unknown>): Record<string, unknown> {
  if (!plainObject(target)) return {};
  const ref = typeof target.$ref === 'string' ? target.$ref.trim() : '';
  return ref && plainObject(selectors[ref]) ? { ...selectors[ref], ...target } : target;
}

export function assertQuotesRule(rule: unknown): void {
  assertQuotesContractRule(rule, {
    inputName: 'tag',
    replayTag: QUOTES_REPLAY_TAG,
    taskTag: QUOTES_TASK_TAG,
    outputMap: { quote: 'quote', author: 'author', author_url: 'author_url' },
  });
}

export function assertQuotesContractRule(rule: unknown, contract: QuotesContract): void {
  assert(plainObject(rule), 'approved quotes rule must be an object');
  const domains = Array.isArray(rule.domain) ? rule.domain : [rule.domain];
  assert.deepEqual(domains, ['quotes.toscrape.com'],
    'approved quotes rule domain must be exactly quotes.toscrape.com');
  const serialized = JSON.stringify(rule);
  const executable = JSON.stringify([rule.steps, rule.hooks]);
  for (const tag of [contract.replayTag, contract.taskTag]) {
    assert(!executable.toLowerCase().includes(tag.toLowerCase()),
      `approved quotes rule must not hardcode qualification tag ${tag}`);
  }
  assert(!/evaluate|executeJavascript|solveCaptcha/.test(serialized),
    'approved quotes rule must not contain arbitrary script or CAPTCHA actions');
  const actions = actionObjects([rule.steps, rule.hooks]);
  const navigations = actions.filter((action) => action.action === 'navigate');
  assert(navigations.length > 0, 'approved quotes rule must navigate to the tag route');
  let hasBoundTagNavigation = false;
  for (const navigation of navigations) {
    const rawURL = String(navigation.url ?? '');
    const template = `{{${contract.inputName}}}`;
    const parsed = new URL(rawURL.replaceAll(template, 'aegis-tag'), QUOTES_BY_TAG_ORIGIN);
    assert.equal(parsed.origin, QUOTES_BY_TAG_ORIGIN,
      'every quotes navigation must remain on the exact HTTPS origin');
    assert(!parsed.search && !parsed.hash,
      'quotes navigation must not include query parameters or fragments');
    if (rawURL.includes(template)) {
      assert.equal(parsed.pathname, '/tag/aegis-tag/',
        'tag-bound quotes navigation must use the canonical /tag/{{tag}}/ route');
      hasBoundTagNavigation = true;
    }
  }
  assert(hasBoundTagNavigation,
    `approved quotes rule must use {{${contract.inputName}}} in its navigate URL`);
  assertVisibleModelExtractionRule(rule, 'quotes');
  const selectors = plainObject(rule.selectors) ? rule.selectors : {};
  const paginationLoops = actions.filter((action) => {
    if (action.action !== 'loop') return false;
    const nested = actionObjects(action.steps);
    return nested.some((step) => {
      if (step.action !== 'click') return false;
      return /next|pagination|pager|rel["': ]+next/i.test(
        JSON.stringify(resolvedTarget(step.target, selectors)),
      );
    });
  });
  assert(paginationLoops.length > 0,
    'approved quotes rule must advance pagination through a next-page control');
  for (const loop of paginationLoops) {
    assert.equal(loop.type, 'fixedCount',
      'quotes pagination loop must use replay-supported fixedCount navigation');
    const bound = Number(loop.count);
    assert(Number.isInteger(bound) && bound > 0 && bound <= QUOTES_MAX_PAGES,
      `quotes pagination loop must have a positive bound of at most ${QUOTES_MAX_PAGES}`);
    const nested = actionObjects(loop.steps);
    assert(nested.some((action) => action.action === 'extract'),
      'quotes pagination loop must extract the current page');
    assert(nested.some((action) => action.action === 'sendResult'),
      'quotes pagination loop must emit each current-page row');
    assert(nested.some((action) => action.action === 'break'),
      'quotes pagination loop must stop when no next page remains');
  }
  const resultActions = actions.filter((action) => action.action === 'sendResult');
  assert(resultActions.length > 0, 'approved quotes rule must send quote rows');
  for (const action of resultActions) {
    assert(plainObject(action.payload), 'quotes sendResult payload must be an object');
    const outputNames = Object.keys(contract.outputMap).sort();
    assert.deepEqual(Object.keys(action.payload).sort(), outputNames,
      `quotes sendResult payload must contain exactly ${outputNames.join(', ')}`);
  }
}

export async function assertQuotesExecutionPage(
  page: Page,
  tag: string,
  expectedFinalPage = 2,
): Promise<void> {
  const parsed = validateQuotesTagPageURL(page.url(), tag);
  assert.equal(parsed.page, expectedFinalPage,
    `quotes ${tag} execution stopped on page ${parsed.page}, expected ${expectedFinalPage}`);
  const visibleNextLinks = await page.locator('li.next > a:visible, .pager .next > a:visible, a[rel="next"]:visible').count();
  assert.equal(visibleNextLinks, 0, `quotes ${tag} execution stopped before the final page`);
}

function bindQuotesInput(
  inputs: Record<string, unknown>,
  tag: string,
  phase: string,
): Record<string, unknown> {
  assert.deepEqual(Object.keys(inputs), ['tag'], `${phase} quotes inputs must contain only tag`);
  return { tag };
}

export function bindQuotesContractInput(
  inputs: Record<string, unknown>,
  inputName: string,
  tag: string,
  phase: string,
): Record<string, unknown> {
  assert.deepEqual(Object.keys(inputs), [inputName],
    `${phase} quotes inputs must contain only ${inputName}`);
  return { [inputName]: tag };
}

export function bindQuotesReplayInputs(inputs: Record<string, unknown>): Record<string, unknown> {
  return bindQuotesInput(inputs, QUOTES_REPLAY_TAG, 'replay');
}

export function bindQuotesTaskInputs(inputs: Record<string, unknown>): Record<string, unknown> {
  return bindQuotesInput(inputs, QUOTES_TASK_TAG, 'task');
}
