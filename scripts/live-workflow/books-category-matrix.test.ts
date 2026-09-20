import type { Browser, Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  BOOKS_CATEGORY_REQUIREMENT,
  BOOKS_ENTRY_URL,
  BOOKS_MISSING_CATEGORY,
  BOOKS_ORIGIN,
  BOOKS_OUTPUT_SCHEMA,
  BOOKS_REPLAY_CATEGORY,
  BOOKS_SEQUENTIAL_ART_CATEGORY,
  BOOKS_TRAVEL_CATEGORY,
  assertBooksAcceptedRequestCostAuthorized,
  assertBooksQualificationEnvironment,
  assertBooksRecording,
  assertBooksRequirement,
  assertBooksRowsEqual,
  assertBooksRule,
  bindBooksCategoryInput,
  booksCategoryDemo,
  booksRowsHash,
  collectBooksCategoryOracle,
  collectVisibleBookRows,
  collectVisibleNextHrefs,
  forecastBooksDirectRequest,
  validateBooksCategoryPageURL,
  waitForBooksCategoryPage,
  type BookRow,
} from './books-category-matrix';

function bookMarkup(index: number, options: { hidden?: boolean } = {}): string {
  return `
    <article class="product_pod"${options.hidden ? ' hidden' : ''}>
      <p class="star-rating ${index % 2 ? 'Three' : 'Five'}"></p>
      <h3><a title="Book ${index}" href="${BOOKS_ORIGIN}/catalogue/book-${index}_${index}/index.html">
        Book ${index}
      </a></h3>
      <p class="price_color">£${index}.00</p>
      <p class="availability"> In   stock </p>
    </article>`;
}

function bookRows(count: number, start = 1): BookRow[] {
  return Array.from({ length: count }, (_, offset) => {
    const index = start + offset;
    return {
      title: `Book ${index}`,
      price: `£${index}.00`,
      availability: 'In stock',
      rating: `star-rating ${index % 2 ? 'Three' : 'Five'}`,
      product_url: `${BOOKS_ORIGIN}/catalogue/book-${index}_${index}/index.html`,
    };
  });
}

function validRule(): Record<string, unknown> {
  return {
    entry: BOOKS_ENTRY_URL,
    domain: 'books.toscrape.com',
    selectors: {
      rows: { selector: 'article.product_pod', visible: true },
      next: { text: 'next' },
    },
    steps: [
      { action: 'click', target: { text: '{{category}}', visible: true } },
      {
        action: 'loop',
        type: 'fixedCount',
        count: 10,
        steps: [
          {
            action: 'extract',
            name: 'books',
            target: { $ref: 'rows' },
            multiple: true,
            fields: {
              title: { type: 'attr', selector: 'h3 a', attr: 'title' },
              price: { type: 'text', selector: '.price_color' },
              availability: { type: 'text', selector: '.availability' },
              rating: { type: 'attr', selector: '.star-rating', attr: 'class' },
              product_url: {
                type: 'attr', selector: 'h3 a', attr: 'href', resolve: true,
              },
            },
          },
          {
            action: 'loop',
            type: 'forEach',
            items: '{{extracted.books}}',
            as: 'book',
            steps: [{
              action: 'sendResult',
              payload: {
                title: '{{loopItem.title}}',
                price: '{{loopItem.price}}',
                availability: '{{loopItem.availability}}',
                rating: '{{loopItem.rating}}',
                product_url: '{{loopItem.product_url}}',
              },
            }],
          },
          {
            action: 'if',
            condition: { type: 'elementNotExists', target: { $ref: 'next' } },
            then: [{ action: 'break' }],
            else: [{ action: 'click', target: { $ref: 'next' } }],
          },
        ],
      },
    ],
  };
}

describe('Books category matrix public workflow contract', () => {
  beforeEach(() => {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({
      width: 100,
      height: 20,
    } as DOMRect);
    document.head.innerHTML = '<base href="https://books.toscrape.com/catalogue/category/books/mystery_3/index.html">';
    document.body.textContent = '';
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('collects complete visible attribute/text rows in DOM order', () => {
    document.body.innerHTML = `${bookMarkup(1)}${bookMarkup(2, { hidden: true })}
      <article class="product_pod"><h3><a title="Incomplete" href="/x">x</a></h3></article>
      <div style="opacity: 0">${bookMarkup(4)}</div>
      <div style="content-visibility: hidden">${bookMarkup(5)}</div>
      ${bookMarkup(3)}`;
    expect(collectVisibleBookRows()).toEqual([...bookRows(1), ...bookRows(1, 3)]);
  });

  it('collects only uniquely visible pagination links', () => {
    document.body.innerHTML = `
      <li class="next"><a href="/visible">next</a></li>
      <li class="next" hidden><a href="/hidden">next</a></li>
      <li class="next" aria-hidden=" TRUE "><a href="/aria-hidden">next</a></li>
      <li class="next" style="opacity: 0"><a href="/transparent">next</a></li>
      <li class="next" style="content-visibility: hidden"><a href="/content-hidden">next</a></li>
      <li class="next"><a id="zero-geometry" href="/zero">next</a></li>`;
    (Element.prototype.getBoundingClientRect as ReturnType<typeof vi.fn>)
      .mockImplementation(function (this: Element) {
        return {
          width: this.id === 'zero-geometry' ? 0 : 100,
          height: this.id === 'zero-geometry' ? 0 : 20,
        } as DOMRect;
      });
    expect(collectVisibleNextHrefs()).toEqual([
      'https://books.toscrape.com/visible',
    ]);
  });

  it('requires exact schema, normalization, URL identity, uniqueness, and order', () => {
    const expected = bookRows(3);
    expect(() => assertBooksRowsEqual(
      structuredClone(expected),
      BOOKS_OUTPUT_SCHEMA,
      expected,
      'replay',
    )).not.toThrow();
    expect(() => assertBooksRowsEqual(
      [expected[1], expected[0], expected[2]],
      BOOKS_OUTPUT_SCHEMA,
      expected,
      'replay',
    )).toThrow(/page\/DOM-order/);
    expect(() => assertBooksRowsEqual(
      [{ ...expected[0], product_url: new URL(expected[0].product_url).pathname }],
      BOOKS_OUTPUT_SCHEMA,
      [expected[0]],
      'task',
    )).toThrow(/absolute URL/);
    expect(() => assertBooksRowsEqual(
      [{ ...expected[0], price: '$1.00' }],
      BOOKS_OUTPUT_SCHEMA,
      [expected[0]],
      'task',
    )).toThrow(/GBP/);
    expect(() => assertBooksRowsEqual(
      [expected[0], { ...expected[1], product_url: expected[0].product_url }],
      BOOKS_OUTPUT_SCHEMA,
      expected.slice(0, 2),
      'task',
    )).toThrow(/duplicate product_url/);
  });

  it('hashes canonical ordered rows without depending on object insertion order', () => {
    const rows = bookRows(2);
    const reorderedKeys = rows.map((row) => ({
      product_url: row.product_url,
      rating: row.rating,
      availability: row.availability,
      price: row.price,
      title: row.title,
    }));
    expect(booksRowsHash(rows)).toBe(booksRowsHash(reorderedKeys));
    expect(booksRowsHash(rows)).not.toBe(booksRowsHash([...rows].reverse()));
  });

  it('validates canonical same-category page routes and bounds', () => {
    const first = validateBooksCategoryPageURL(
      `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`,
    );
    expect(first).toEqual({
      url: `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`,
      route: '/catalogue/category/books/mystery_3',
      page: 1,
    });
    expect(validateBooksCategoryPageURL(
      `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-2.html`,
      first.route,
    ).page).toBe(2);
    expect(() => validateBooksCategoryPageURL(
      `${BOOKS_ORIGIN}/catalogue/category/books/travel_2/index.html`,
      first.route,
    )).toThrow(/selected category/);
    expect(() => validateBooksCategoryPageURL(
      `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-1.html`,
    )).toThrow(/page 1/);
    expect(() => validateBooksCategoryPageURL(
      `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-11.html`,
    )).toThrow(/10-page bound/);
    expect(() => validateBooksCategoryPageURL(
      `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html?token=secret`,
    )).toThrow(/query parameters/);
  });

  it('waits for the exact requested page URL instead of accepting stale rows', async () => {
    const pageTwoURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-2.html`;
    let waitOptions: unknown;
    const page = {
      waitForURL: async (
        predicate: (url: URL) => boolean,
        options: unknown,
      ) => {
        waitOptions = options;
        expect(predicate(new URL(BOOKS_ENTRY_URL))).toBe(false);
        expect(predicate(new URL(
          `${BOOKS_ORIGIN}/catalogue/category/books/travel_2/index.html`,
        ))).toBe(false);
        expect(predicate(new URL(
          `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`,
        ))).toBe(false);
        expect(predicate(new URL(pageTwoURL))).toBe(true);
      },
    } as unknown as Page;
    await waitForBooksCategoryPage(page, '/catalogue/category/books/mystery_3', 2);
    expect(waitOptions).toEqual({ waitUntil: 'domcontentloaded', timeout: 30_000 });
    await expect(waitForBooksCategoryPage(
      page,
      '/catalogue/category/books/mystery_3',
      11,
    )).rejects.toThrow(/between 1 and 10/);
  });

  it('arms exact URL waits before both demo clicks and rejects stale-page rows', async () => {
    const pageOneURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`;
    const pageTwoURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-2.html`;
    let currentURL = BOOKS_ENTRY_URL;
    const rowWaitURLs: string[] = [];
    const clickWaiterCounts: number[] = [];
    const operationOrder: string[] = [];
    const waiters: Array<{
      predicate: (url: URL) => boolean;
      resolve: () => void;
      settled: boolean;
    }> = [];
    const commitURL = (url: string): void => {
      currentURL = url;
      for (const waiter of waiters) {
        if (!waiter.settled && waiter.predicate(new URL(url))) {
          waiter.settled = true;
          waiter.resolve();
        }
      }
    };
    const categoryLocator = {
      filter: () => categoryLocator,
      count: async () => 1,
      evaluate: async (activate: (element: HTMLAnchorElement) => void) => {
        activate({ click: () => operationOrder.push('category-click') } as unknown as HTMLAnchorElement);
        clickWaiterCounts.push(waiters.filter((waiter) => !waiter.settled).length);
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        commitURL(pageOneURL);
      },
    };
    const nextLocator = {
      count: async () => 1,
      evaluate: async (activate: (element: HTMLAnchorElement) => void) => {
        activate({ click: () => operationOrder.push('next-click') } as unknown as HTMLAnchorElement);
        clickWaiterCounts.push(waiters.filter((waiter) => !waiter.settled).length);
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        commitURL(pageTwoURL);
      },
    };
    const rowLocator = {
      first: () => rowLocator,
      waitFor: async () => {
        rowWaitURLs.push(currentURL);
      },
    };
    const page = {
      locator: (selector: string) => {
        if (selector === '.side_categories a:visible') return categoryLocator;
        if (selector === 'li.next > a:visible') return nextLocator;
        if (selector === 'article.product_pod:visible') return rowLocator;
        throw new Error(`unexpected locator ${selector}`);
      },
      waitForURL: async (predicate: (url: URL) => boolean) => {
        if (predicate(new URL(currentURL))) return;
        await new Promise<void>((resolve) => {
          waiters.push({ predicate, resolve, settled: false });
        });
      },
      url: () => currentURL,
      mouse: { wheel: async () => {
        throw new Error('Books scripted demo must not enqueue scroll events');
      } },
      waitForTimeout: async () => {
        throw new Error('Books scripted demo must not depend on timing sleeps');
      },
    } as unknown as Page;

    const readinessURLs: string[] = [];
    await booksCategoryDemo(page, async () => {
      readinessURLs.push(currentURL);
      operationOrder.push(`ready:${currentURL}`);
    });

    expect(clickWaiterCounts).toEqual([1, 1]);
    expect(rowWaitURLs).toEqual([pageOneURL, pageTwoURL]);
    expect(readinessURLs).toEqual([pageOneURL, pageTwoURL]);
    expect(operationOrder).toEqual([
      'category-click',
      `ready:${pageOneURL}`,
      'next-click',
      `ready:${pageTwoURL}`,
    ]);
    expect(currentURL).toBe(pageTwoURL);
  });

  it('discovers the route by exact visible label and collects isolated pagination', async () => {
    const firstURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`;
    const secondURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-2.html`;
    const pageMarkup = (rows: string, next = '') => `
      <ul class="breadcrumb"><li class="active">Mystery</li></ul>
      <h1>Mystery</h1>${rows}${next}`;
    const html = new Map([
      [BOOKS_ENTRY_URL, `<div class="side_categories">
        <a href="${firstURL}"> Mystery </a><a href="/catalogue/category/books/travel_2/index.html">Travel</a>
      </div>`],
      [firstURL, pageMarkup(
        `${bookMarkup(1)}${bookMarkup(2)}`,
        `<li class="next" style="opacity: 0"><a href="/hidden-page.html">next</a></li>
        <li class="next"><a href="${secondURL}">next</a></li>`,
      )],
      [secondURL, pageMarkup(
        bookMarkup(3),
        '<li class="next" aria-hidden=" TRUE "><a href="/hidden-terminal.html">next</a></li>',
      )],
    ]);
    let currentURL = 'about:blank';
    let contextClosed = false;
    const page = {
      goto: async (url: string) => {
        currentURL = url;
        const markup = html.get(url);
        if (markup === undefined) throw new Error(`unexpected mock URL ${url}`);
        document.head.innerHTML = `<base href="${url}">`;
        document.body.innerHTML = markup;
      },
      url: () => currentURL,
      evaluate: async (fn: (...args: any[]) => unknown, arg?: unknown) => fn(arg),
    };
    const browser = {
      newContext: async () => ({
        newPage: async () => page,
        close: async () => { contextClosed = true; },
      }),
    } as unknown as Browser;
    const oracle = await collectBooksCategoryOracle(browser, 'Mystery', {
      exactPages: 2,
      exactRows: 3,
    });
    expect(oracle.pages.map((value) => value.page)).toEqual([1, 2]);
    expect(oracle.rows).toEqual(bookRows(3));
    expect(contextClosed).toBe(true);
  });

  it('preserves the exact open category input and five-field output contract', () => {
    expect(() => assertBooksRequirement(BOOKS_CATEGORY_REQUIREMENT)).not.toThrow();
    expect(() => assertBooksRequirement({
      ...BOOKS_CATEGORY_REQUIREMENT,
      requiredInputs: [{
        ...BOOKS_CATEGORY_REQUIREMENT.requiredInputs[0],
        constraints: {
          ...BOOKS_CATEGORY_REQUIREMENT.requiredInputs[0].constraints,
          enum: ['Mystery'],
        },
      }],
    })).toThrow(/must not enumerate/);
    expect(() => assertBooksRequirement({
      ...BOOKS_CATEGORY_REQUIREMENT,
      outputFields: BOOKS_CATEGORY_REQUIREMENT.outputFields.slice(0, 4),
    })).toThrow(/exactly five/);
  });

  it('requires a complete root-to-Mystery-page-2 extension-v2 recording', () => {
    const firstURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`;
    const secondURL = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-2.html`;
    const snapshot = (url: string, sequence: number) => ({
      timestamp: sequence,
      url,
      selectorMap: {},
      phase: sequence === 0 ? 'initial' as const : sequence === 3 ? 'final' as const : 'before-action' as const,
      sequence,
      ...(sequence > 0 && sequence < 3 ? { actionIndex: sequence - 1 } : {}),
      domTree: { type: 'element' as const, tagName: 'html', children: [] },
      capture: {
        status: 'complete' as const,
        nodeCount: 1,
        redactionCount: 0,
        removedNodeCount: 0,
        frames: [],
      },
    });
    const recording = {
      version: '2.0.0',
      meta: {
        title: 'Books to Scrape',
        domain: 'books.toscrape.com',
        startUrl: BOOKS_ENTRY_URL,
        recordedAt: new Date(0).toISOString(),
        semanticDomVersion: '1',
        sanitizationVersion: 'extension-v2',
      },
      events: [
        { type: 'click', timestamp: 1, index: 0 },
        { type: 'click', timestamp: 2, index: 1 },
      ],
      snapshots: [
        snapshot(BOOKS_ENTRY_URL, 0),
        snapshot(firstURL, 1),
        snapshot(secondURL, 2),
        snapshot(secondURL, 3),
      ],
      termination: { reason: 'user', message: 'done', timestamp: 3, complete: true },
    } as PageAgentRecording;
    expect(() => assertBooksRecording(recording)).not.toThrow();
    const forecast = forecastBooksDirectRequest({
      ...recording,
      events: [],
      snapshots: [recording.snapshots[0], recording.snapshots.at(-1)!],
    });
    expect(forecast).toMatchObject({
      selectorCatalogBytes: 32 * 1024,
      strictSchemaBytes: 512 * 1024,
      fixedPromptReserveBytes: 128 * 1024,
      withinDirectEnvelope: true,
    });
    expect(forecast.requiredDirectBytes).toBeLessThan(forecast.availableDirectBytes);
    const stale = structuredClone(recording);
    stale.snapshots.at(-1)!.url = firstURL;
    expect(() => assertBooksRecording(stale)).toThrow(/finish on Mystery page 2/);
    const crossCategory = structuredClone(recording);
    crossCategory.snapshots[1].url =
      `${BOOKS_ORIGIN}/catalogue/category/books/travel_2/index.html`;
    expect(() => assertBooksRecording(crossCategory)).toThrow(/selected category route/);
  });

  it('requires one input-bound category click and exact bounded row extraction', () => {
    expect(() => assertBooksRule(validRule())).not.toThrow();
    const hardcoded = validRule();
    (hardcoded.steps as Array<Record<string, unknown>>)[0] = {
      action: 'click', target: { text: BOOKS_REPLAY_CATEGORY },
    };
    expect(() => assertBooksRule(hardcoded)).toThrow(/hardcode/);
    const hiddenEligible = validRule();
    (hiddenEligible.steps as Array<Record<string, any>>)[0].target = {
      text: '{{category}}',
    };
    expect(() => assertBooksRule(hiddenEligible)).toThrow(/visible text-only/);
    const route = validRule();
    (route.steps as Array<Record<string, unknown>>).unshift({
      action: 'navigate',
      url: `${BOOKS_ORIGIN}/catalogue/category/books/fiction_10/index.html`,
    });
    expect(() => assertBooksRule(route)).toThrow(/numeric category route/);
    const unbounded = validRule();
    delete (unbounded.steps as Array<Record<string, unknown>>)[1].count;
    expect(() => assertBooksRule(unbounded)).toThrow(/positive bound/);
    const wrongRating = validRule();
    const loop = (wrongRating.steps as Array<Record<string, any>>)[1];
    loop.steps[0].fields.rating = { type: 'text', selector: '.star-rating' };
    expect(() => assertBooksRule(wrongRating)).toThrow();
  });

  it('binds all four reviewed category values without accepting extra inputs', () => {
    for (const value of [
      BOOKS_REPLAY_CATEGORY,
      BOOKS_TRAVEL_CATEGORY,
      BOOKS_SEQUENTIAL_ART_CATEGORY,
      BOOKS_MISSING_CATEGORY,
    ]) {
      expect(bindBooksCategoryInput({ category: 'placeholder' }, value, 'test'))
        .toEqual({ category: value });
    }
    expect(() => bindBooksCategoryInput({ category: 'x', extra: true }, 'Travel', 'test'))
      .toThrow(/only category/);
  });

  it('requires the exact scenario, caps, provider adapter, and scripted mode', () => {
    const valid = {
      AEGIS_LIVE_WORKFLOW_SCENARIOS: 'books-category-matrix',
      AEGIS_LIVE_LLM_MAX_INPUT_TOKENS: '315000',
      AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS: '4096',
      AEGIS_LOCAL_LLM_PROVIDER: 'openai',
      AEGIS_LOCAL_LLM_MODEL: 'deepseek-v4-flash',
      AEGIS_LOCAL_LLM_BASE_URL:
        'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
    };
    expect(() => assertBooksQualificationEnvironment(valid, false)).not.toThrow();
    // The model and endpoint must not be locked to one reviewed provider:
    // any HTTPS OpenAI-compatible endpoint and any model are accepted.
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LOCAL_LLM_MODEL: 'glm-5.2',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://llm-gateway.example.internal/v1',
    }, false)).not.toThrow();
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS: '8192',
    }, false)).toThrow(/4096-token output cap/);
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LOCAL_LLM_BASE_URL: 'http://insecure.example/v1',
    }, false)).toThrow(/HTTPS/);
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LIVE_HUMAN_DEMO: '1',
    }, false)).toThrow(/scripted demonstration/);
    expect(() => assertBooksQualificationEnvironment(
      {
        AEGIS_LIVE_WORKFLOW_SCENARIOS: 'books-category-matrix',
        AEGIS_LIVE_LLM_MAX_INPUT_TOKENS: '315000',
        AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS: '4096',
        AEGIS_LIVE_SAVE_RECORDING: '1',
      },
      true,
    )).not.toThrow();
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LIVE_SAVE_RECORDING: '1',
    }, false)).toThrow(/paid qualification must not persist/);
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LIVE_REUSE_RECORDING: '1',
    }, false)).toThrow(/must not reuse/);
    expect(() => assertBooksQualificationEnvironment({
      ...valid,
      AEGIS_LIVE_WORKFLOW_HEADED: '1',
    }, false)).toThrow(/persistent browser profiles/);
  });

  it('enforces the prospectively authorized hard request ceiling', () => {
    expect(() => assertBooksAcceptedRequestCostAuthorized(0.138561152)).not.toThrow();
    expect(() => assertBooksAcceptedRequestCostAuthorized(0.60)).not.toThrow();
    expect(() => assertBooksAcceptedRequestCostAuthorized(0.600000001))
      .toThrow(/exceeds \$0.60 authorization/);
  });
});
