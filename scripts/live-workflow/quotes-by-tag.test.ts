import type { Browser } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  QUOTES_BY_TAG_ORIGIN,
  QUOTES_BY_TAG_ENTRY_URL,
  QUOTES_BY_TAG_OUTPUT_SCHEMA,
  QUOTES_BY_TAG_REQUIREMENT,
  QUOTES_REPLAY_TAG,
  QUOTES_TASK_TAG,
  assertQuotesRequirement,
  assertQuotesRecording,
  assertQuotesRowsEqual,
  assertQuotesContractRule,
  assertQuotesRule,
  bindQuotesReplayInputs,
  bindQuotesTaskInputs,
  collectQuotesPaginationOracle,
  collectVisibleQuoteRows,
  quotesTagPageBlockForURL,
  validateQuotesTagPageURL,
  type QuoteRow,
} from './quotes-by-tag';

function quoteMarkup(index: number): string {
  return `
    <div class="quote">
      <span class="text">Quote ${index}</span>
      <small class="author">Author ${index}</small>
      <a href="${QUOTES_BY_TAG_ORIGIN}/author/Author-${index}/">about</a>
    </div>`;
}

function quoteRows(count = 12): QuoteRow[] {
  return Array.from({ length: count }, (_, index) => ({
    quote: `Quote ${index + 1}`,
    author: `Author ${index + 1}`,
    author_url: `${QUOTES_BY_TAG_ORIGIN}/author/Author-${index + 1}/`,
  }));
}

function validRule(): Record<string, unknown> {
  return {
    domain: 'quotes.toscrape.com',
    selectors: {
      quotes: { selector: '.quote', visible: true },
      next: { selector: '.pager .next a', visible: true },
    },
    steps: [
      { action: 'navigate', url: `${QUOTES_BY_TAG_ORIGIN}/tag/{{tag}}/` },
      {
        action: 'loop',
        type: 'fixedCount',
        count: 10,
        steps: [
          {
            action: 'extract',
            name: 'quotes',
            target: { $ref: 'quotes' },
            multiple: true,
            fields: {
              quote: { type: 'text', selector: '.text' },
              author: { type: 'text', selector: '.author' },
              author_url: { type: 'attr', selector: 'a[href*="/author/"]', attr: 'href', resolve: true },
            },
          },
          {
            action: 'loop',
            type: 'forEach',
            items: '{{extracted.quotes}}',
            as: 'quoteRow',
            steps: [{
              action: 'sendResult',
              payload: {
                quote: '{{loopItem.quote}}',
                author: '{{loopItem.author}}',
                author_url: '{{loopItem.author_url}}',
              },
            }],
          },
          {
            action: 'if',
            condition: { type: 'elementNotExists', target: { selector: '.pager .next a', visible: true } },
            then: [{ action: 'break' }],
            else: [{ action: 'click', target: { $ref: 'next' } }],
          },
        ],
      },
    ],
  };
}

describe('quotes-by-tag public workflow contract', () => {
  beforeEach(() => {
    document.body.textContent = '';
  });

  it('collects only complete visible rows in DOM order', () => {
    document.body.innerHTML = `
      ${quoteMarkup(1)}
      <section hidden>${quoteMarkup(2)}</section>
      <div class="quote"><span class="text">No author</span></div>
      <div class="quote" style="display:none">
        <span class="text">Hidden</span><small class="author">Hidden author</small>
        <a href="${QUOTES_BY_TAG_ORIGIN}/author/Hidden/">about</a>
      </div>
      ${quoteMarkup(3)}`;
    expect(collectVisibleQuoteRows()).toEqual([
      {
        quote: 'Quote 1',
        author: 'Author 1',
        author_url: `${QUOTES_BY_TAG_ORIGIN}/author/Author-1/`,
      },
      {
        quote: 'Quote 3',
        author: 'Author 3',
        author_url: `${QUOTES_BY_TAG_ORIGIN}/author/Author-3/`,
      },
    ]);
  });

  it('requires exact ordered schema and row equality, not subsets or duplicates', () => {
    const expected = quoteRows();
    expect(() => assertQuotesRowsEqual(
      expected.map((row) => ({ ...row })), QUOTES_BY_TAG_OUTPUT_SCHEMA, expected, 'replay',
    )).not.toThrow();
    expect(() => assertQuotesRowsEqual(
      expected.map((row) => ({ ...row, author_url: new URL(row.author_url).pathname })),
      QUOTES_BY_TAG_OUTPUT_SCHEMA,
      expected,
      'replay',
    )).not.toThrow();
    expect(() => assertQuotesRowsEqual(
      expected.slice(1), QUOTES_BY_TAG_OUTPUT_SCHEMA, expected, 'replay',
    )).toThrow(/exactly match/);
    expect(() => assertQuotesRowsEqual(
      [...expected.slice(0, -1), expected[0]], QUOTES_BY_TAG_OUTPUT_SCHEMA, expected, 'replay',
    )).toThrow(/duplicate row/);
    expect(() => assertQuotesRowsEqual(
      [expected[1], expected[0], ...expected.slice(2)],
      QUOTES_BY_TAG_OUTPUT_SCHEMA,
      expected,
      'task',
    )).toThrow(/page order/);
  });

  it('validates exact-origin canonical tag URLs without leaking query values', () => {
    expect(validateQuotesTagPageURL(`${QUOTES_BY_TAG_ORIGIN}/tag/love/`, 'love')).toEqual({
      url: `${QUOTES_BY_TAG_ORIGIN}/tag/love/`, tag: 'love', page: 1,
    });
    expect(validateQuotesTagPageURL(`${QUOTES_BY_TAG_ORIGIN}/tag/love/page/2/`, 'love').page).toBe(2);
    expect(quotesTagPageBlockForURL(`${QUOTES_BY_TAG_ORIGIN}/tag/humor/`, 'love'))
      .toBe('the quotes page was produced by an unexpected tag');
    expect(quotesTagPageBlockForURL('https://example.com/tag/love/', 'love'))
      .toBe('unexpected cross-origin page at example.com');
    const queryBlock = quotesTagPageBlockForURL(
      `${QUOTES_BY_TAG_ORIGIN}/tag/love/?token=do-not-print`, 'love',
    );
    expect(queryBlock).toBe('unexpected query parameters on the quotes tag page');
    expect(queryBlock).not.toContain('do-not-print');
    expect(quotesTagPageBlockForURL(`${QUOTES_BY_TAG_ORIGIN}/tag/love/page/11/`, 'love'))
      .toMatch(/10-page bound/);
    expect(quotesTagPageBlockForURL(`${QUOTES_BY_TAG_ORIGIN}/tag/love/page/1/`, 'love'))
      .toMatch(/canonical/);
    expect(quotesTagPageBlockForURL(`${QUOTES_BY_TAG_ORIGIN}/tag/l%6fve/`, 'love'))
      .toMatch(/canonical/);
    expect(quotesTagPageBlockForURL(`${QUOTES_BY_TAG_ORIGIN}/tag/love/`, '../love'))
      .toMatch(/safe lowercase slug/);
  });

  it('collects ordered pagination through an isolated mock browser context', async () => {
    const firstURL = `${QUOTES_BY_TAG_ORIGIN}/tag/love/`;
    const secondURL = `${QUOTES_BY_TAG_ORIGIN}/tag/love/page/2/`;
    const html = new Map([
      [firstURL, `${Array.from({ length: 6 }, (_, index) => quoteMarkup(index + 1)).join('')}
        <ul class="pager"><li class="next"><a href="${secondURL}">Next</a></li></ul>`],
      [secondURL, Array.from({ length: 6 }, (_, index) => quoteMarkup(index + 7)).join('')],
    ]);
    let currentURL = 'about:blank';
    let contextClosed = false;
    const page = {
      goto: async (url: string) => {
        currentURL = url;
        const markup = html.get(url);
        if (markup === undefined) throw new Error(`unexpected mock URL: ${url}`);
        document.body.innerHTML = markup;
      },
      url: () => currentURL,
      evaluate: async (fn: () => unknown) => fn(),
    };
    const browser = {
      newContext: async () => ({
        newPage: async () => page,
        close: async () => { contextClosed = true; },
      }),
    } as unknown as Browser;
    const oracle = await collectQuotesPaginationOracle(browser, 'love');
    expect(oracle.pages.map((value) => value.page)).toEqual([1, 2]);
    expect(oracle.rows).toEqual(quoteRows());
    expect(contextClosed).toBe(true);
  });

  it('requires only tag:string and exactly the three string outputs', () => {
    expect(() => assertQuotesRequirement(QUOTES_BY_TAG_REQUIREMENT)).not.toThrow();
    expect(() => assertQuotesRequirement({
      ...QUOTES_BY_TAG_REQUIREMENT,
      requiredInputs: [{ name: 'query', type: 'string' }],
    })).toThrow(/tag:string/);
    expect(() => assertQuotesRequirement({
      ...QUOTES_BY_TAG_REQUIREMENT,
      outputFields: [...QUOTES_BY_TAG_REQUIREMENT.outputFields, { name: 'tags', type: 'array' }],
    })).toThrow(/exactly three string/);
    expect(() => assertQuotesRequirement({
      ...QUOTES_BY_TAG_REQUIREMENT,
      outputFields: QUOTES_BY_TAG_REQUIREMENT.outputFields.map((field) =>
        field.name === 'author_url' ? { ...field, type: 'url' } : field),
    })).toThrow(/three string/);
  });

  it('requires a complete extension-v2 recording of both love pages', () => {
    const snapshot = (url: string, sequence: number) => ({
      timestamp: sequence,
      url,
      selectorMap: {},
      phase: sequence === 0 ? 'initial' as const : sequence === 3 ? 'final' as const : 'before-action' as const,
      sequence,
      ...(sequence > 0 && sequence < 3 ? { actionIndex: sequence - 1 } : {}),
      domTree: { type: 'element' as const, tagName: 'html', children: [] },
      capture: { status: 'complete' as const, nodeCount: 1, redactionCount: 0, removedNodeCount: 0, frames: [] },
    });
    const recording = {
      version: '2.0.0',
      meta: {
        title: 'Quotes to Scrape', domain: 'quotes.toscrape.com', startUrl: QUOTES_BY_TAG_ENTRY_URL,
        recordedAt: new Date(0).toISOString(), semanticDomVersion: '1', sanitizationVersion: 'extension-v2',
      },
      events: [
        { type: 'click', timestamp: 1, index: 0 },
        { type: 'click', timestamp: 2, index: 1 },
      ],
      snapshots: [
        snapshot(QUOTES_BY_TAG_ENTRY_URL, 0),
        snapshot(`${QUOTES_BY_TAG_ORIGIN}/tag/love/`, 1),
        snapshot(`${QUOTES_BY_TAG_ORIGIN}/tag/love/page/2/`, 2),
        snapshot(`${QUOTES_BY_TAG_ORIGIN}/tag/love/page/2/`, 3),
      ],
      termination: { reason: 'user', message: 'done', timestamp: 3, complete: true },
    } as PageAgentRecording;
    expect(() => assertQuotesRecording(recording)).not.toThrow();
    const incomplete = structuredClone(recording);
    incomplete.snapshots[incomplete.snapshots.length - 1].url = `${QUOTES_BY_TAG_ORIGIN}/tag/love/`;
    expect(() => assertQuotesRecording(incomplete)).toThrow(/finish on love page 2/);
  });

  it('accepts only same-origin, input-bound, visible, bounded pagination rules', () => {
    expect(() => assertQuotesRule(validRule())).not.toThrow();

    const relativeNavigation = validRule();
    (relativeNavigation.steps as Array<Record<string, unknown>>)[0].url = '/tag/{{tag}}/';
    expect(() => assertQuotesRule(relativeNavigation)).not.toThrow();

    expect(() => assertQuotesRule({ ...validRule(), domain: ['quotes.toscrape.com', 'example.com'] }))
      .toThrow(/domain must be exactly/);

    const hardcoded = validRule();
    (hardcoded.steps as Array<Record<string, unknown>>)[0].url = `${QUOTES_BY_TAG_ORIGIN}/tag/love/`;
    expect(() => assertQuotesRule(hardcoded)).toThrow(/must not hardcode/);

    const invisible = validRule();
    (invisible.selectors as Record<string, Record<string, unknown>>).quotes.visible = false;
    expect(() => assertQuotesRule(invisible)).toThrow(/explicitly require visible/);

    const unbounded = validRule();
    delete ((unbounded.steps as Array<Record<string, unknown>>)[1]).count;
    expect(() => assertQuotesRule(unbounded)).toThrow(/positive bound/);

    const wrongLoop = validRule();
    (wrongLoop.steps as Array<Record<string, unknown>>)[1].type = 'forEach';
    expect(() => assertQuotesRule(wrongLoop)).toThrow(/fixedCount navigation/);

    const crossOrigin = validRule();
    (crossOrigin.steps as Array<Record<string, unknown>>).push({
      action: 'navigate', url: 'https://example.com/tag/{{tag}}/',
    });
    expect(() => assertQuotesRule(crossOrigin)).toThrow(/exact HTTPS origin/);
  });

  it('allows a recorded qualification tag only in immutable rule entry', () => {
    const contract = {
      inputName: 'label',
      replayTag: 'humor',
      taskTag: 'friendship',
      outputMap: {
        quote: 'quote' as const,
        author: 'author' as const,
        author_url: 'author_url' as const,
      },
    };
    const recordedEntry = validRule();
    recordedEntry.entry = `${QUOTES_BY_TAG_ORIGIN}/tag/humor/`;
    (recordedEntry.steps as Array<Record<string, unknown>>)[0].url =
      `${QUOTES_BY_TAG_ORIGIN}/tag/{{label}}/`;
    expect(() => assertQuotesContractRule(recordedEntry, contract)).not.toThrow();

    const hardcodedAction = structuredClone(recordedEntry);
    (hardcodedAction.steps as Array<Record<string, unknown>>)[0].url =
      `${QUOTES_BY_TAG_ORIGIN}/tag/humor/`;
    expect(() => assertQuotesContractRule(hardcodedAction, contract))
      .toThrow(/must not hardcode qualification tag humor/);
  });

  it('binds distinct fixed tags for replay and task phases', () => {
    expect(bindQuotesReplayInputs({ tag: 'placeholder' })).toEqual({ tag: QUOTES_REPLAY_TAG });
    expect(bindQuotesTaskInputs({ tag: 'placeholder' })).toEqual({ tag: QUOTES_TASK_TAG });
    expect(QUOTES_REPLAY_TAG).not.toBe(QUOTES_TASK_TAG);
    expect(() => bindQuotesReplayInputs({ tag: 'placeholder', extra: true }))
      .toThrow(/only tag/);
    expect(() => bindQuotesTaskInputs({})).toThrow(/only tag/);
  });
});
