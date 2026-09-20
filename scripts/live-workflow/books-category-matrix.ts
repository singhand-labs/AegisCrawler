import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Browser, BrowserContext, Page } from 'playwright';
import { convert } from '../../src/rule-generator';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { assertVisibleModelExtractionRule, plainObject } from './model-contract';
import { readGoRawStringConstant } from './go-source-contract';

export const BOOKS_ORIGIN = 'https://books.toscrape.com';
export const BOOKS_ENTRY_URL = `${BOOKS_ORIGIN}/`;
export const BOOKS_REPLAY_CATEGORY = 'Mystery';
export const BOOKS_TRAVEL_CATEGORY = 'Travel';
export const BOOKS_SEQUENTIAL_ART_CATEGORY = 'Sequential Art';
export const BOOKS_MISSING_CATEGORY = 'Aegis Missing Category';
export const BOOKS_MAX_PAGES = 10;
export const BOOKS_COST_BUDGET_USD = 0.60;
export const BOOKS_MAX_ESTIMATED_INPUT_TOKENS = 315_000;
export const BOOKS_MAX_OUTPUT_TOKENS = 4_096;
// Reviewed 2026-07-28:
// https://www.alibabacloud.com/help/en/model-studio/text-generation-model/
export const BOOKS_DEEPSEEK_CONTEXT_TOKENS = 1_000_000;
export const BOOKS_REPLAY_ROUTE = '/catalogue/category/books/mystery_3';

const BOOK_FIELDS = ['availability', 'price', 'product_url', 'rating', 'title'];
const CATEGORY_PATTERN = '^[A-Za-z]+(?:[ -][A-Za-z]+)*$';
const PRICE_PATTERN = /^£[0-9]+\.[0-9]{2}$/;
const RATING_PATTERN = /^star-rating (One|Two|Three|Four|Five)$/;
const CATEGORY_PATH_PATTERN =
  /^\/catalogue\/category\/books\/([a-z0-9]+(?:-[a-z0-9]+)*)_([1-9][0-9]*)\/(index\.html|page-([1-9][0-9]*)\.html)$/;
const PRODUCT_PATH_PATTERN =
  /^\/catalogue\/[a-z0-9]+(?:-[a-z0-9]+)*_([1-9][0-9]*)\/index\.html$/;

export interface BookRow extends Record<string, string> {
  title: string;
  price: string;
  availability: string;
  rating: string;
  product_url: string;
}

export interface BooksCategoryPage {
  url: string;
  route: string;
  page: number;
}

export interface BooksPageOracle extends BooksCategoryPage {
  rows: BookRow[];
}

export interface BooksCategoryOracle {
  category: string;
  pages: BooksPageOracle[];
  rows: BookRow[];
}

export interface BooksCategoryOracleOptions {
  exactPages: number;
  exactRows?: number;
}

export interface BooksDirectRequestForecast {
  recordingBytes: number;
  baselineBytes: number;
  generationSystemPromptBytes: number;
  requirementBytes: number;
  selectorCatalogBytes: number;
  strictSchemaBytes: number;
  fixedPromptReserveBytes: number;
  requiredDirectBytes: number;
  availableDirectBytes: number;
  withinDirectEnvelope: boolean;
}

export function assertBooksAcceptedRequestCostAuthorized(costUSD: number): void {
  assert(Number.isFinite(costUSD) && costUSD > 0,
    'Books accepted-request cost ceiling must be positive and finite');
  assert(costUSD <= BOOKS_COST_BUDGET_USD,
    `Books accepted-request ceiling $${costUSD.toFixed(9)} exceeds `
    + `$${BOOKS_COST_BUDGET_USD.toFixed(2)} authorization`);
}

export const BOOKS_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    title: { type: 'string' },
    price: { type: 'string' },
    availability: { type: 'string' },
    rating: { type: 'string' },
    product_url: { type: 'string' },
  },
  required: ['title', 'price', 'availability', 'rating', 'product_url'],
  additionalProperties: false,
} as const;

export const BOOKS_CATEGORY_REQUIREMENT = {
  title: 'Collect every book in a named category',
  description: [
    'Start at the public Books to Scrape catalogue root and click the one visible sidebar category whose text exactly equals the required category input.',
    'Do not construct or navigate directly to a numeric category route.',
    'Use a fixedCount pagination loop with a positive count of at most 10 to collect every visible book in page and DOM order.',
    'On each page, emit the current rows, then stop when no unique visible Next link remains; otherwise click Next and continue.',
    'Return exactly title, price, availability, rating, and product_url as strings.',
    'Read title from the title attribute of the visible book link, rating from the class attribute of the visible star-rating element, and product_url as a resolved same-origin absolute href.',
  ].join(' '),
  requiredInputs: [{
    name: 'category',
    type: 'string',
    description: 'Exact visible category label from the catalogue sidebar',
    constraints: { pattern: CATEGORY_PATTERN, maxLength: 64 },
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'title', type: 'string', description: 'Full title attribute of the visible book link' },
    { name: 'price', type: 'string', description: 'Visible GBP price such as £51.77' },
    { name: 'availability', type: 'string', description: 'Normalized visible stock text' },
    { name: 'rating', type: 'string', description: 'Canonical star-rating class such as star-rating Three' },
    { name: 'product_url', type: 'string', description: 'Canonical same-origin absolute book detail URL' },
  ],
  sampleOutput: {
    title: 'Example Book',
    price: '£12.34',
    availability: 'In stock',
    rating: 'star-rating Three',
    product_url: `${BOOKS_ORIGIN}/catalogue/example-book_1/index.html`,
  },
};

function normalizedText(value: unknown): string {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

/** Self-contained so Playwright can serialize it into the public page. */
export function collectVisibleBookRows(): BookRow[] {
  const normalize = (value: unknown): string =>
    String(value ?? '').replace(/\s+/g, ' ').trim();
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      if (current.hasAttribute('hidden')
        || current.getAttribute('aria-hidden')?.trim().toLowerCase() === 'true'
        || (current.tagName.toLowerCase() === 'input'
          && current.getAttribute('type')?.trim().toLowerCase() === 'hidden')) return false;
      const style = getComputedStyle(current);
      if (style.display === 'none'
        || style.visibility === 'hidden'
        || style.visibility === 'collapse'
        || style.contentVisibility === 'hidden'
        || style.opacity === '0') return false;
      if (current.parentElement) {
        current = current.parentElement;
        continue;
      }
      const root = current.getRootNode();
      current = 'host' in root ? (root as ShadowRoot).host : null;
    }
    const rect = element.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  };
  const text = (element: Element): string =>
    normalize((element as HTMLElement).innerText || element.textContent);
  const rows: BookRow[] = [];
  for (const card of Array.from(document.querySelectorAll('article.product_pod'))) {
    if (!visible(card)) continue;
    const link = card.querySelector<HTMLAnchorElement>('h3 a[title][href]');
    const priceElement = card.querySelector('.price_color');
    const availabilityElement = card.querySelector('.availability');
    const ratingElement = card.querySelector('.star-rating');
    if (!link || !priceElement || !availabilityElement || !ratingElement
      || !visible(link) || !visible(priceElement)
      || !visible(availabilityElement) || !visible(ratingElement)) continue;
    const title = normalize(link.getAttribute('title'));
    const price = text(priceElement);
    const availability = text(availabilityElement);
    const rating = normalize(ratingElement.getAttribute('class'));
    const href = link.getAttribute('href');
    if (!title || !price || !availability || !rating || !href) continue;
    let product_url = '';
    try {
      product_url = new URL(href, document.baseURI).toString();
    } catch {
      continue;
    }
    rows.push({ title, price, availability, rating, product_url });
  }
  return rows;
}

export function collectVisibleNextHrefs(): string[] {
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      if (current.hasAttribute('hidden')
        || current.getAttribute('aria-hidden')?.trim().toLowerCase() === 'true'
        || (current.tagName.toLowerCase() === 'input'
          && current.getAttribute('type')?.trim().toLowerCase() === 'hidden')) return false;
      const style = getComputedStyle(current);
      if (style.display === 'none'
        || style.visibility === 'hidden'
        || style.visibility === 'collapse'
        || style.contentVisibility === 'hidden'
        || style.opacity === '0') return false;
      if (current.parentElement) {
        current = current.parentElement;
        continue;
      }
      const root = current.getRootNode();
      current = 'host' in root ? (root as ShadowRoot).host : null;
    }
    const rect = element.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  };
  return Array.from(document.querySelectorAll<HTMLAnchorElement>('li.next > a[href]'))
    .filter(visible)
    .map((link) => link.href);
}

export function assertValidBooksCategory(value: string): string {
  assert.equal(value, value.trim(), 'Books category must not contain surrounding whitespace');
  assert(new RegExp(CATEGORY_PATTERN).test(value),
    'Books category must contain only safe ASCII words separated by spaces or hyphens');
  assert(value.length <= 64, 'Books category must not exceed 64 characters');
  return value;
}

function booksURL(rawURL: string, label: string): URL {
  let url: URL;
  try {
    url = new URL(rawURL);
  } catch {
    throw new Error(`${label} is not an absolute URL`);
  }
  assert.equal(url.protocol, 'https:', `${label} must use HTTPS`);
  assert.equal(url.hostname, 'books.toscrape.com', `${label} must remain on books.toscrape.com`);
  assert.equal(url.port, '', `${label} must not use a non-default port`);
  assert(!url.username && !url.password, `${label} must not contain credentials`);
  assert(!url.search && !url.hash, `${label} must not contain query parameters or fragments`);
  return url;
}

export function validateBooksCategoryPageURL(
  rawURL: string,
  expectedRoute?: string,
): BooksCategoryPage {
  const url = booksURL(rawURL, 'Books category URL');
  const match = CATEGORY_PATH_PATTERN.exec(url.pathname);
  assert(match, 'Books category URL must use the canonical category page route');
  const route = `/catalogue/category/books/${match[1]}_${match[2]}`;
  if (expectedRoute !== undefined) {
    assert.equal(route, expectedRoute, 'Books pagination must remain within the selected category route');
  }
  const page = match[3] === 'index.html' ? 1 : Number(match[4]);
  assert(page >= 1 && page <= BOOKS_MAX_PAGES,
    `Books category page exceeds the ${BOOKS_MAX_PAGES}-page bound`);
  assert(page !== 1 || match[3] === 'index.html',
    'Books category page 1 must use canonical index.html');
  const canonicalPath = page === 1 ? `${route}/index.html` : `${route}/page-${page}.html`;
  assert.equal(url.pathname, canonicalPath, 'Books category URL is not canonical');
  return { url: `${BOOKS_ORIGIN}${canonicalPath}`, route, page };
}

export async function waitForBooksCategoryPage(
  page: Page,
  expectedRoute: string,
  expectedPage: number,
): Promise<void> {
  assert(Number.isSafeInteger(expectedPage)
    && expectedPage >= 1
    && expectedPage <= BOOKS_MAX_PAGES,
  `Books expected page must be between 1 and ${BOOKS_MAX_PAGES}`);
  await page.waitForURL((url) => {
    try {
      const parsed = validateBooksCategoryPageURL(url.toString(), expectedRoute);
      return parsed.page === expectedPage;
    } catch {
      return false;
    }
  }, { waitUntil: 'domcontentloaded', timeout: 30_000 });
}

function canonicalBookRows(values: readonly unknown[], label: string): BookRow[] {
  const urls = new Set<string>();
  return values.map((value, index) => {
    assert(plainObject(value), `${label} row ${index} must be an object`);
    assert.deepEqual(Object.keys(value).sort(), BOOK_FIELDS,
      `${label} row ${index} must contain exactly ${BOOK_FIELDS.join(', ')}`);
    for (const field of BOOK_FIELDS) {
      assert.equal(typeof value[field], 'string', `${label} row ${index} field ${field} must be a string`);
    }
    const row = value as BookRow;
    assert(row.title && row.title === normalizedText(row.title),
      `${label} row ${index} title must already be non-empty normalized text`);
    assert(PRICE_PATTERN.test(row.price),
      `${label} row ${index} price must be a canonical visible GBP value`);
    assert.equal(row.availability, 'In stock',
      `${label} row ${index} availability must be the normalized visible In stock value`);
    assert(RATING_PATTERN.test(row.rating),
      `${label} row ${index} rating must be the canonical visible star-rating class`);
    const productURL = booksURL(row.product_url, `${label} row ${index} product_url`);
    assert(PRODUCT_PATH_PATTERN.test(productURL.pathname),
      `${label} row ${index} product_url must use the canonical detail route`);
    assert.equal(row.product_url, productURL.toString(),
      `${label} row ${index} product_url must already be canonical and absolute`);
    assert(!urls.has(row.product_url),
      `${label} results contain a duplicate product_url at row ${index}`);
    urls.add(row.product_url);
    return {
      availability: row.availability,
      price: row.price,
      product_url: row.product_url,
      rating: row.rating,
      title: row.title,
    };
  });
}

export function booksRowsHash(rows: readonly unknown[]): string {
  return createHash('sha256')
    .update(JSON.stringify(canonicalBookRows(rows, 'Books hash')))
    .digest('hex');
}

function browserForOracle(source: Browser | BrowserContext): Browser {
  if ('newContext' in source) return source;
  const browser = source.browser();
  assert(browser, 'Books category oracle requires a browser-backed context');
  return browser;
}

async function exactVisibleCategoryHref(page: Page, category: string): Promise<string> {
  return page.evaluate((expected) => {
    const normalize = (value: unknown): string =>
      String(value ?? '').replace(/\s+/g, ' ').trim();
    const visible = (element: Element): boolean => {
      let current: Element | null = element;
      while (current) {
        if (current.hasAttribute('hidden')
          || current.getAttribute('aria-hidden')?.trim().toLowerCase() === 'true'
          || (current.tagName.toLowerCase() === 'input'
            && current.getAttribute('type')?.trim().toLowerCase() === 'hidden')) return false;
        const style = getComputedStyle(current);
        if (style.display === 'none'
          || style.visibility === 'hidden'
          || style.visibility === 'collapse'
          || style.contentVisibility === 'hidden'
          || style.opacity === '0') return false;
        if (current.parentElement) {
          current = current.parentElement;
          continue;
        }
        const root = current.getRootNode();
        current = 'host' in root ? (root as ShadowRoot).host : null;
      }
      const rect = element.getBoundingClientRect();
      return rect.width > 0 && rect.height > 0;
    };
    const matches = Array.from(document.querySelectorAll<HTMLAnchorElement>('.side_categories a[href]'))
      .filter((element) => visible(element) && normalize(element.textContent) === expected);
    if (matches.length !== 1) {
      throw new Error(`expected one exact visible category link, found ${matches.length}`);
    }
    return matches[0].href;
  }, category);
}

/**
 * Collect a contemporaneous oracle in a fresh context. Category route IDs are
 * discovered only through the exact visible root-page label.
 */
export async function collectBooksCategoryOracle(
  source: Browser | BrowserContext,
  category: string,
  options: BooksCategoryOracleOptions,
): Promise<BooksCategoryOracle> {
  assertValidBooksCategory(category);
  assert(Number.isSafeInteger(options.exactPages)
    && options.exactPages > 0
    && options.exactPages <= BOOKS_MAX_PAGES,
  'Books oracle exactPages must be within the reviewed pagination bound');
  const context = await browserForOracle(source).newContext();
  try {
    const page = await context.newPage();
    await page.goto(BOOKS_ENTRY_URL, { waitUntil: 'domcontentloaded', timeout: 30_000 });
    assert.equal(page.url(), BOOKS_ENTRY_URL, 'Books oracle root URL changed');
    const firstHref = await exactVisibleCategoryHref(page, category);
    const first = validateBooksCategoryPageURL(firstHref);
    assert.equal(first.page, 1, 'Books category root link must lead to page 1');
    let nextURL: string | undefined = first.url;
    const pages: BooksPageOracle[] = [];
    const rows: BookRow[] = [];
    const visited = new Set<string>();
    const productURLs = new Set<string>();
    while (nextURL) {
      assert(pages.length < BOOKS_MAX_PAGES,
        `Books pagination exceeds the ${BOOKS_MAX_PAGES}-page bound`);
      await page.goto(nextURL, { waitUntil: 'domcontentloaded', timeout: 30_000 });
      const parsed = validateBooksCategoryPageURL(page.url(), first.route);
      assert.equal(parsed.page, pages.length + 1,
        'Books pagination must advance exactly one page at a time');
      assert(!visited.has(parsed.url), `Books pagination cycle detected at page ${parsed.page}`);
      visited.add(parsed.url);
      const identity = await page.evaluate(() => {
        const normalize = (value: unknown): string =>
          String(value ?? '').replace(/\s+/g, ' ').trim();
        return {
          heading: normalize(document.querySelector('h1')?.textContent),
          breadcrumb: normalize(document.querySelector('.breadcrumb li.active')?.textContent),
        };
      });
      assert.deepEqual(identity, { heading: category, breadcrumb: category },
        `Books page ${parsed.page} does not identify the selected category exactly`);
      const pageRows = canonicalBookRows(
        await page.evaluate(collectVisibleBookRows),
        `Books ${category} page ${parsed.page}`,
      );
      assert(pageRows.length > 0, `Books ${category} page ${parsed.page} has no complete visible rows`);
      for (const row of pageRows) {
        assert(!productURLs.has(row.product_url),
          `Books ${category} repeats ${row.product_url} across pages`);
        productURLs.add(row.product_url);
        rows.push(row);
      }
      pages.push({ ...parsed, rows: pageRows });
      const nextHrefs = await page.evaluate(collectVisibleNextHrefs);
      assert(nextHrefs.length <= 1,
        `Books ${category} page ${parsed.page} has multiple visible Next links`);
      if (nextHrefs.length === 0) {
        nextURL = undefined;
      } else {
        const next = validateBooksCategoryPageURL(nextHrefs[0], first.route);
        assert.equal(next.page, parsed.page + 1,
          'Books Next link must advance exactly one page');
        assert(!visited.has(next.url), `Books pagination cycle detected at page ${next.page}`);
        nextURL = next.url;
      }
    }
    assert.equal(pages.length, options.exactPages,
      `Books ${category} topology changed from ${options.exactPages} page(s)`);
    if (options.exactRows !== undefined) {
      assert.equal(rows.length, options.exactRows,
        `Books ${category} topology changed from ${options.exactRows} row(s)`);
    }
    return { category, pages, rows };
  } finally {
    await context.close();
  }
}

export function assertBooksRowsEqual(
  rows: unknown[],
  outputSchema: unknown,
  expected: readonly BookRow[],
  phase: string,
): void {
  assert(plainObject(outputSchema), `${phase} Books output schema must be an object`);
  const properties = plainObject(outputSchema.properties) ? outputSchema.properties : {};
  assert.deepEqual(Object.keys(properties).sort(), BOOK_FIELDS,
    `${phase} Books output schema must contain exactly ${BOOK_FIELDS.join(', ')}`);
  assert.deepEqual(
    (Array.isArray(outputSchema.required) ? outputSchema.required.map(String) : []).sort(),
    BOOK_FIELDS,
    `${phase} Books output fields must all be required`,
  );
  assert.equal(outputSchema.additionalProperties, false,
    `${phase} Books output schema must reject additional properties`);
  for (const field of BOOK_FIELDS) {
    assert.equal(plainObject(properties[field]) ? properties[field].type : undefined, 'string',
      `${phase} Books output field ${field} must have type string`);
  }
  assert.deepEqual(
    canonicalBookRows(rows, phase),
    canonicalBookRows(expected, `${phase} oracle`),
    `${phase} Books rows must exactly match the independent page/DOM-order oracle`,
  );
}

export function assertBooksRequirement(requirement: unknown): void {
  assert(plainObject(requirement), 'normalized Books requirement must be an object');
  assert(Array.isArray(requirement.requiredInputs), 'Books requiredInputs must be an array');
  const required = requirement.requiredInputs.filter(plainObject);
  assert.deepEqual(required.map((input) => [input.name, input.type]), [['category', 'string']],
    'Books requirement must declare category:string as its only required input');
  const constraints = plainObject(required[0]?.constraints) ? required[0].constraints : {};
  assert.equal(constraints.pattern, CATEGORY_PATTERN);
  assert.equal(constraints.maxLength, 64);
  assert(!Array.isArray(constraints.enum), 'Books category input must not enumerate only known categories');
  assert.deepEqual(requirement.optionalInputs, [], 'Books requirement must not declare optional inputs');
  assert(Array.isArray(requirement.outputFields), 'Books outputFields must be an array');
  const outputs = requirement.outputFields.filter(plainObject);
  assert.deepEqual(outputs.map((field) => [field.name, field.type]).sort(), BOOK_FIELDS.map((name) =>
    [name, 'string']),
  'Books requirement must preserve exactly five string output fields');
}

export async function booksCategoryDemo(
  page: Page,
  ensureRecordingReady: () => Promise<void> = async () => undefined,
): Promise<void> {
  const category = page.locator('.side_categories a:visible', { hasText: BOOKS_REPLAY_CATEGORY })
    .filter({ hasText: new RegExp(`^\\s*${BOOKS_REPLAY_CATEGORY}\\s*$`) });
  assert.equal(await category.count(), 1, 'Books demo requires one exact visible Mystery category link');
  await Promise.all([
    waitForBooksCategoryPage(page, BOOKS_REPLAY_ROUTE, 1),
    category.evaluate((element: HTMLAnchorElement) => element.click()),
  ]);
  await ensureRecordingReady();
  await page.locator('article.product_pod:visible').first().waitFor({ timeout: 30_000 });
  const next = page.locator('li.next > a:visible');
  assert.equal(await next.count(), 1, 'Books Mystery page 1 requires one visible Next link');
  await Promise.all([
    waitForBooksCategoryPage(page, BOOKS_REPLAY_ROUTE, 2),
    next.evaluate((element: HTMLAnchorElement) => element.click()),
  ]);
  await ensureRecordingReady();
  await page.locator('article.product_pod:visible').first().waitFor({ timeout: 30_000 });
  const final = validateBooksCategoryPageURL(page.url(), BOOKS_REPLAY_ROUTE);
  assert.equal(final.page, 2, 'Books demo must finish on Mystery page 2');
}

export function assertBooksRecording(recording: PageAgentRecording): void {
  assert.equal(recording.version, '2.0.0', 'Books recording must use recording v2');
  assert.equal(recording.meta.sanitizationVersion, 'extension-v2',
    'Books recording must retain extension-v2 sanitization provenance');
  assert.equal(recording.meta.startUrl, BOOKS_ENTRY_URL, 'Books recording must start at the catalogue root');
  assert.equal(recording.termination?.complete, true, 'Books recording must terminate completely');
  assert(recording.events.length >= 2 && recording.events.length <= 12,
    'Books recording must contain between 2 and 12 scripted events');
  assert(recording.events.filter((event) => event.type === 'click').length >= 2,
    'Books recording must contain the category and Next clicks');
  assert(!recording.events.some((event) => event.type === 'executeJavascript'),
    'Books recording must not contain executeJavascript');
  assert.equal(recording.snapshots.length, recording.events.length + 2,
    'Books recording must retain initial, pre-action, and final snapshots');
  const pages = recording.snapshots.map((snapshot, index) => {
    assert.equal(snapshot.capture?.status, 'complete', `Books snapshot ${index} must be complete`);
    assert(!(snapshot.capture?.frames ?? []).some((frame) => frame.status !== 'captured'),
      `Books snapshot ${index} contains an unavailable frame`);
    if (snapshot.url === BOOKS_ENTRY_URL) return 0;
    return validateBooksCategoryPageURL(snapshot.url, BOOKS_REPLAY_ROUTE).page;
  });
  assert.equal(pages[0], 0, 'Books recording initial snapshot must be the root page');
  assert(pages.includes(1), 'Books recording must observe Mystery page 1');
  assert.equal(pages.at(-1), 2, 'Books recording must finish on Mystery page 2');
  assert(pages.every((page, index) => index === 0 || page >= pages[index - 1]),
    'Books recording page states must advance monotonically');
}

/**
 * Prove the paid generation remains on the workflow's direct one-call branch.
 * The strict schema and selector catalog use their server-side hard maxima,
 * while the current source prompt, converted baseline, requirement, and
 * recording are measured exactly. A further 128 KiB covers prompt framing and
 * audit text so source wording can grow without silently consuming the margin.
 */
export function forecastBooksDirectRequest(
  recording: PageAgentRecording,
): BooksDirectRequestForecast {
  const workflowSource = fs.readFileSync(
    path.resolve(__dirname, '..', '..', 'server', 'internal', 'llm', 'dsl', 'workflow.go'),
    'utf8',
  );
  const strictSource = fs.readFileSync(
    path.resolve(__dirname, '..', '..', 'server', 'internal', 'llm', 'dsl', 'strict_output.go'),
    'utf8',
  );
  const generationSystemPrompt = readGoRawStringConstant(workflowSource, 'generationSystemPrompt');
  const catalogMatch = workflowSource.match(
    /maxSelectorEvidencePromptBytes\s*=\s*([0-9]+)\s*\*\s*([0-9]+)/,
  );
  const contextMatch = workflowSource.match(
    /workflowContextReserveTokens\s*=\s*([0-9]+)/,
  );
  const schemaMatch = strictSource.match(
    /maxStrictOutputSchemaBytes\s*=\s*([0-9]+)\s*\*\s*([0-9]+)/,
  );
  assert(catalogMatch && contextMatch && schemaMatch,
    'could not load current workflow prompt/schema byte bounds');
  const selectorCatalogBytes = Number(catalogMatch[1]) * Number(catalogMatch[2]);
  const contextReserveTokens = Number(contextMatch[1]);
  const strictSchemaBytes = Number(schemaMatch[1]) * Number(schemaMatch[2]);
  const recordingBytes = Buffer.byteLength(JSON.stringify(recording), 'utf8');
  const baselineBytes = Buffer.byteLength(JSON.stringify(convert(recording)), 'utf8');
  const generationSystemPromptBytes = Buffer.byteLength(generationSystemPrompt, 'utf8');
  const requirementBytes = Buffer.byteLength(JSON.stringify(BOOKS_CATEGORY_REQUIREMENT), 'utf8');
  const fixedPromptReserveBytes = 128 * 1024;
  const requiredDirectBytes = recordingBytes
    + baselineBytes
    + generationSystemPromptBytes
    + requirementBytes
    + selectorCatalogBytes
    + strictSchemaBytes
    + fixedPromptReserveBytes;
  const availableDirectBytes =
    (BOOKS_MAX_ESTIMATED_INPUT_TOKENS - BOOKS_MAX_OUTPUT_TOKENS - contextReserveTokens) * 4;
  return {
    recordingBytes,
    baselineBytes,
    generationSystemPromptBytes,
    requirementBytes,
    selectorCatalogBytes,
    strictSchemaBytes,
    fixedPromptReserveBytes,
    requiredDirectBytes,
    availableDirectBytes,
    withinDirectEnvelope: requiredDirectBytes <= availableDirectBytes,
  };
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

export function assertBooksRule(rule: unknown): void {
  assert(plainObject(rule), 'approved Books rule must be an object');
  const domains = Array.isArray(rule.domain) ? rule.domain : [rule.domain];
  assert.deepEqual(domains, ['books.toscrape.com'],
    'approved Books rule domain must be exactly books.toscrape.com');
  assert.equal(rule.entry, BOOKS_ENTRY_URL, 'approved Books rule must enter at the catalogue root');
  const serialized = JSON.stringify(rule);
  const executable = JSON.stringify([rule.steps, rule.hooks]);
  for (const category of [
    BOOKS_REPLAY_CATEGORY,
    BOOKS_TRAVEL_CATEGORY,
    BOOKS_SEQUENTIAL_ART_CATEGORY,
    BOOKS_MISSING_CATEGORY,
  ]) {
    assert(!executable.toLowerCase().includes(category.toLowerCase()),
      `approved Books rule must not hardcode qualification category ${category}`);
  }
  assert(!/evaluate|executeJavascript|solveCaptcha/.test(serialized),
    'approved Books rule must not contain arbitrary script or CAPTCHA actions');
  assert(!/\/catalogue\/category\/books\//.test(executable),
    'approved Books rule must not construct or navigate directly to a numeric category route');
  const selectors = plainObject(rule.selectors) ? rule.selectors : {};
  const actions = actionObjects([rule.steps, rule.hooks]);
  const categoryClicks = actions.filter((action) => {
    const target = resolvedTarget(action.target, selectors);
    return action.action === 'click'
      && target.text === '{{category}}'
      && target.visible === true
      && Object.keys(target).sort().join(',') === 'text,visible';
  });
  assert.equal(categoryClicks.length, 1,
    'approved Books rule must click exactly one visible text-only category target bound as {{category}}');
  assertVisibleModelExtractionRule(rule, 'Books');
  const rowExtractions = actions.filter((action) =>
    action.action === 'extract'
    && action.multiple === true
    && plainObject(action.fields)
    && Object.keys(action.fields).sort().join(',') === BOOK_FIELDS.join(','));
  assert(rowExtractions.length > 0,
    'approved Books rule must extract the exact five fields from repeated visible book rows');
  for (const extraction of rowExtractions) {
    const fields = extraction.fields as Record<string, Record<string, unknown>>;
    assert.equal(fields.title?.type, 'attr');
    assert.equal(fields.title?.attr, 'title');
    assert.equal(fields.price?.type, 'text');
    assert.equal(fields.availability?.type, 'text');
    assert.equal(fields.rating?.type, 'attr');
    assert.equal(fields.rating?.attr, 'class');
    assert.equal(fields.product_url?.type, 'attr');
    assert.equal(fields.product_url?.attr, 'href');
    assert.equal(fields.product_url?.resolve, true);
  }
  const paginationLoops = actions.filter((action) => {
    if (action.action !== 'loop' || action.type !== 'fixedCount') return false;
    const nested = actionObjects(action.steps);
    return nested.some((step) => step.action === 'click'
      && /next|pagination|pager/i.test(JSON.stringify(resolvedTarget(step.target, selectors))));
  });
  assert(paginationLoops.length > 0,
    'approved Books rule must paginate through a visible Next control');
  for (const loop of paginationLoops) {
    const bound = Number(loop.count);
    assert(Number.isSafeInteger(bound) && bound > 0 && bound <= BOOKS_MAX_PAGES,
      `Books pagination loop must have a positive bound of at most ${BOOKS_MAX_PAGES}`);
    const nested = actionObjects(loop.steps);
    assert(nested.some((action) => action.action === 'extract'),
      'Books pagination loop must extract the current page');
    assert(nested.some((action) => action.action === 'sendResult'),
      'Books pagination loop must emit the current page');
    assert(nested.some((action) => action.action === 'break'),
      'Books pagination loop must break when Next is absent');
    assert(nested.some((action) =>
      action.action === 'if'
      && plainObject(action.condition)
      && action.condition.type === 'elementNotExists'),
    'Books pagination loop must test for an absent Next control before clicking');
  }
  const resultActions = actions.filter((action) => action.action === 'sendResult');
  assert(resultActions.length > 0, 'approved Books rule must send book rows');
  for (const action of resultActions) {
    assert(plainObject(action.payload), 'Books sendResult payload must be an object');
    assert.deepEqual(Object.keys(action.payload).sort(), BOOK_FIELDS,
      'Books sendResult payload must contain exactly the five confirmed fields');
  }
}

export function bindBooksCategoryInput(
  inputs: Record<string, unknown>,
  category: string,
  phase: string,
): Record<string, unknown> {
  assertValidBooksCategory(category);
  assert.deepEqual(Object.keys(inputs).sort(), ['category'],
    `${phase} Books input binding must receive only category`);
  return { category };
}

export async function assertBooksExecutionPage(
  page: Page,
  oracle: BooksCategoryOracle,
): Promise<void> {
  const expected = oracle.pages.at(-1);
  assert(expected, 'Books execution oracle contains no final page');
  const actual = validateBooksCategoryPageURL(page.url(), expected.route);
  assert.equal(actual.url, expected.url, 'Books execution did not finish on the oracle final page');
  const identity = await page.evaluate(() => {
    const normalize = (value: unknown): string =>
      String(value ?? '').replace(/\s+/g, ' ').trim();
    return {
      heading: normalize(document.querySelector('h1')?.textContent),
      breadcrumb: normalize(document.querySelector('.breadcrumb li.active')?.textContent),
    };
  });
  const visibleNext = await page.evaluate(collectVisibleNextHrefs);
  assert.deepEqual(identity, {
    heading: oracle.category,
    breadcrumb: oracle.category,
  }, 'Books execution final page identity or terminal pagination state changed');
  assert.equal(visibleNext.length, 0,
    'Books execution final page still has a visible Next link');
}

export function assertBooksQualificationEnvironment(
  env: NodeJS.ProcessEnv,
  recordOnly: boolean,
): void {
  assert.equal(env.AEGIS_LIVE_WORKFLOW_SCENARIOS, 'books-category-matrix',
    'Books qualification requires exactly the books-category-matrix scenario');
  assert(env.AEGIS_LIVE_HUMAN_DEMO !== '1',
    'Books qualification uses the reviewed scripted demonstration, not human input');
  assert(env.AEGIS_LIVE_HUMAN_REQUIREMENT !== '1',
    'Books qualification uses the reviewed structured requirement');
  assert(env.AEGIS_LIVE_WORKFLOW_HEADED !== '1'
    && env.AEGIS_LIVE_PROFILE_WARMUP !== '1'
    && !env.AEGIS_LIVE_BROWSER_PROFILE
    && !env.AEGIS_LIVE_ALLOW_PERSISTENT_PROFILE,
  'Books qualification forbids headed or persistent browser profiles');
  assert(env.AEGIS_LIVE_REUSE_RECORDING !== '1',
    'Books full qualification must not reuse a recording');
  assert(recordOnly || env.AEGIS_LIVE_SAVE_RECORDING !== '1',
    'Books paid qualification must not persist a recording');
  assert.equal(env.AEGIS_LIVE_LLM_MAX_INPUT_TOKENS, String(BOOKS_MAX_ESTIMATED_INPUT_TOKENS),
    `Books qualification requires an exact ${BOOKS_MAX_ESTIMATED_INPUT_TOKENS}-token source estimate ceiling`);
  assert.equal(env.AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS, String(BOOKS_MAX_OUTPUT_TOKENS),
    `Books qualification requires an exact ${BOOKS_MAX_OUTPUT_TOKENS}-token output cap`);
  if (recordOnly) return;
  assert.equal(env.AEGIS_LOCAL_LLM_PROVIDER, 'openai',
    'Books qualification requires the reviewed OpenAI-compatible provider adapter');
  booksURLForProvider(env.AEGIS_LOCAL_LLM_BASE_URL);
}

function booksURLForProvider(raw: string | undefined): URL {
  let url: URL;
  try {
    url = new URL(raw?.trim() ?? '');
  } catch {
    throw new Error('Books qualification provider URL must be absolute');
  }
  assert.equal(url.protocol, 'https:', 'Books qualification provider URL must use HTTPS');
  assert(!url.username && !url.password && !url.search && !url.hash,
    'Books qualification provider URL must not contain credentials, query, or fragment');
  return url;
}
