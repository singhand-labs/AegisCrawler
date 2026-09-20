import { beforeEach, describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import {
  BING_QUERY,
  BING_ENTRY_URL,
  BING_POINTER_STEPS,
  BING_POINTER_HOLD_MS,
  BING_POINTER_STEP_PAUSE_MS,
  BING_INPUT_RECORDING_SETTLE_MS,
  BING_READING_PAUSE_MS,
  BING_SCROLL_STEPS,
  BING_TYPING_DELAYS_MS,
  BING_SEARCH_REQUIREMENT,
  assertBingRequirement,
  assertBingRecording,
  assertBingRows,
  assertBingRule,
  bingPageBlock,
  collectVisibleBingChoices,
  findBingBoundary,
  isSelectableBingDestination,
  isObservableBingDestination,
  movePointerLikeHuman,
  submitBingQuery,
  typeQueryLikeHuman,
} from './bing-search';

describe('Bing full-workflow contract', () => {
  beforeEach(() => {
    document.body.innerHTML = `<main id="b_results">
      <li class="b_algo"><h2><a href="https://science.nasa.gov/eclipses/">NASA eclipse</a></h2><div class="b_caption"><p>Visible NASA summary</p></div></li>
      <li class="b_ad"><h2><a href="https://example.com/ad">Advertisement</a></h2><p>Ad summary</p></li>
      <article><h2><a href="https://en.wikipedia.org/wiki/Solar_eclipse">Wikipedia eclipse</a></h2><p>Visible encyclopedia summary</p></article>
      <li class="b_algo" hidden><h2><a href="https://example.com/hidden">Hidden</a></h2><p>Hidden summary</p></li>
    </main>`;
    Object.defineProperty(HTMLElement.prototype, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ width: 100, height: 20, top: 0, left: 0, right: 100, bottom: 20 }),
    });
  });

  it('paces reproducible human-like pointer, typing, and reading actions', async () => {
    const actions: string[] = [];
    const page = {
      mouse: { move: async (x: number, y: number) => { actions.push(`move:${x}:${y}`); } },
      waitForTimeout: async (ms: number) => { actions.push(`pause:${ms}`); },
    };
    await movePointerLikeHuman(page as never, 400, 240);
    expect(actions.filter((action) => action.startsWith('move:'))).toHaveLength(BING_POINTER_STEPS);
    expect(actions.filter((action) => action === `pause:${BING_POINTER_STEP_PAUSE_MS}`))
      .toHaveLength(BING_POINTER_STEPS);
    expect(BING_SCROLL_STEPS.every((distance) => distance <= 220)).toBe(true);
    expect(BING_READING_PAUSE_MS).toBeGreaterThanOrEqual(1_500);

    const typed: string[] = [];
    let value = '';
    await typeQueryLikeHuman({ inputValue: async () => value } as never, {
      ...page,
      keyboard: { type: async (character: string) => { typed.push(character); value += character; } },
    } as never, 'human');
    expect(typed).toEqual(['h', 'u', 'm', 'a', 'n']);
    expect(new Set(BING_TYPING_DELAYS_MS).size).toBeGreaterThan(1);
    expect(actions.at(-1)).toBe(`pause:${BING_INPUT_RECORDING_SETTLE_MS}`);
    expect(BING_INPUT_RECORDING_SETTLE_MS).toBeGreaterThan(100);
  });

  it('fails closed when Bing does not retain every trusted keystroke', async () => {
    await expect(typeQueryLikeHuman({ inputValue: async () => 'incomplet' } as never, {
      keyboard: { type: async () => undefined },
      waitForTimeout: async () => undefined,
    } as never, 'incomplete')).rejects.toThrow(/retain the exact fixed query/);
  });

  it('fails closed if Bing replaces or changes the input during the recording settle boundary', async () => {
    let checks = 0;
    await expect(typeQueryLikeHuman({
      inputValue: async () => (++checks === 1 ? 'complete' : 'changed'),
    } as never, {
      keyboard: { type: async () => undefined },
      waitForTimeout: async () => undefined,
    } as never, 'complete')).rejects.toThrow(/recording settle boundary/);
  });

  it('submits one held Enter through the exact search locator without retry or click fallback', async () => {
    const presses: Array<{ key: string; delay: number | undefined }> = [];
    const waits: Array<{ waitUntil: string; timeout: number }> = [];
    const page = {
      keyboard: {
        press: async () => { throw new Error('page-level Enter must not be used'); },
      },
      waitForURL: async (_predicate: unknown, options: { waitUntil: string; timeout: number }) => {
        waits.push(options);
      },
    };
    const search = {
      inputValue: async () => BING_QUERY,
      press: async (key: string, options?: { delay?: number }) => {
        presses.push({ key, delay: options?.delay });
      },
    };

    await submitBingQuery(page as never, search as never);

    expect(presses).toEqual([{ key: 'Enter', delay: BING_POINTER_HOLD_MS }]);
    expect(waits).toEqual([{ waitUntil: 'commit', timeout: 45_000 }]);
  });

  it('fails before Enter if Bing changes the exact query', async () => {
    let pressed = false;
    const page = {
      waitForURL: async () => undefined,
    };
    await expect(submitBingQuery(page as never, {
      inputValue: async () => 'different query',
      press: async () => { pressed = true; },
    } as never)).rejects.toThrow(/changed before direct submission/);
    expect(pressed).toBe(false);
  });

  it('fails closed on Bing verification and CAPTCHA boundaries', () => {
    expect(findBingBoundary('ordinary search results', 'Bing')).toBeUndefined();
    expect(findBingBoundary('Enhance your search experience with a Quick verification.'))
      .toBe('quick verification');
    expect(findBingBoundary('Please verify you are human')).toBe('verify you are human');
  });

  it('accepts any HTTPS oracle-recognized link without assuming its card or publisher', () => {
    const choices = [
      { title: 'Why Do Eclipses Happen? - Science@NASA', href: 'https://science.nasa.gov/eclipses/' },
      { title: 'Solar eclipse - Wikipedia', href: 'https://en.wikipedia.org/wiki/Solar_eclipse' },
    ];
    document.body.innerHTML = [
      '<main id="b_results">',
      '<div class="current-bing-card"><h2>',
      '<a href="https://science.nasa.gov/eclipses/">Why Do Eclipses Happen? - Science@NASA</a>',
      '</h2><p>Visible NASA summary</p></div>',
      '</main>',
    ].join('');
    const link = document.querySelector<HTMLAnchorElement>('#b_results h2 a')!;
    expect(link.closest('li, article')).toBeNull();
    expect(isSelectableBingDestination(link.href, link.textContent ?? '', choices)).toBe(true);
    expect(isSelectableBingDestination(link.href, 'Different title', choices)).toBe(false);
    expect(isSelectableBingDestination('http://example.com/', choices[0].title, choices)).toBe(false);
  });

  it('waits through a popup clone until it reaches an HTTPS destination', () => {
    const resultsURL = `https://www.bing.com/search?q=${encodeURIComponent(BING_QUERY)}`;
    expect(isObservableBingDestination('about:blank', resultsURL)).toBe(false);
    expect(isObservableBingDestination(resultsURL, resultsURL)).toBe(false);
    expect(isObservableBingDestination('http://example.com/result', resultsURL)).toBe(false);
    expect(isObservableBingDestination('https://example.com/result', resultsURL)).toBe(true);
  });

  it('offers visible organic links for human choice without asserting result summaries or count', () => {
    expect(collectVisibleBingChoices()).toEqual([
      { title: 'NASA eclipse', href: 'https://science.nasa.gov/eclipses/' },
      { title: 'Wikipedia eclipse', href: 'https://en.wikipedia.org/wiki/Solar_eclipse' },
    ]);
  });

  it('binds the exact query page and rejects unexpected navigation', () => {
    expect(bingPageBlock(`https://www.bing.com/search?q=${encodeURIComponent(BING_QUERY)}`)).toBe('');
    expect(bingPageBlock('https://www.bing.com/search?q=other')).toBe('unexpected Bing query');
    expect(bingPageBlock('https://example.com/search?q=x')).toContain('unexpected host');
  });

  it('binds the recorded choice and ignores unrelated Bing outcomes', () => {
    expect(() => assertBingRequirement(BING_SEARCH_REQUIREMENT)).not.toThrow();
    const selected = { title: 'NASA eclipse', host: 'science.nasa.gov' };
    expect(() => assertBingRows(
      [{ title: 'NASA eclipse', website: 'science.nasa.gov' }],
      { type: 'object' },
      selected,
      'replay',
    )).not.toThrow();
    expect(() => assertBingRows(
      [{ title: 'Different result', website: 'example.com' }],
      { type: 'object' },
      selected,
      'replay',
    )).toThrow(/item\/website selected/);
  });

  it('requires keyword binding and observable result readiness', () => {
    expect(() => assertBingRule({
      domain: ['bing.com', 'any-result.example'],
      steps: [
        { action: 'type', value: '{{keyword}}' },
        { action: 'waitForElementVisible', target: { selector: '#b_results h2 a', visible: true } },
        { action: 'click', target: { text: '{{target_title}}', visible: true } },
        { action: 'extract', fields: {
          title: { selector: 'h1' },
          website: { value: '{{target_host}}' },
        } },
      ],
    })).not.toThrow();
    expect(() => assertBingRule({ steps: [{ action: 'waitForTimeout', ms: 5000 }] }))
      .toThrow(/keyword/);
    expect(() => assertBingRule({
      domain: ['bing.com'],
      steps: [
        { action: 'type', value: '{{keyword}}' },
        { action: 'waitForElementVisible', target: { selector: '#b_results h2 a', visible: true } },
        { action: 'sendResult', payload: { title: '{{target_title}}', website: '{{target_host}}' } },
      ],
    })).toThrow(/target_title/);
    expect(() => assertBingRule({
      domain: ['bing.com'],
      steps: [
        { action: 'type', value: '{{keyword}}' },
        { action: 'waitForElementVisible', target: { selector: '#b_results h2 a' } },
        { action: 'click', target: { text: '{{target_title}}', index: 0 } },
        { action: 'extract', value: '{{target_host}}' },
      ],
    })).toThrow(/position/);
  });

  it('requires complete direct submission and result-destination-return recording order', () => {
    const resultURL = `https://www.bing.com/search?q=${encodeURIComponent(BING_QUERY)}`;
    const recording: PageAgentRecording = {
      version: '2.0.0',
      meta: {
        startUrl: 'https://www.bing.com/', title: 'Bing',
        recordedAt: new Date(0).toISOString(), domain: 'www.bing.com',
        semanticDomVersion: '1', sanitizationVersion: 'extension-v1',
      },
      events: [
        { type: 'inputText', index: 1, text: 'how do solar ', timestamp: 0 },
        { type: 'inputText', index: 2, text: 'eclipses happen', timestamp: 1 },
        { type: 'submitForm', index: 3, timestamp: 1.5 },
        { type: 'navigate', url: resultURL, timestamp: 2 },
        { type: 'click', index: 2, timestamp: 3 },
        { type: 'navigate', url: 'https://unlisted.example/eclipse-guide', timestamp: 4 },
        { type: 'navigate', url: resultURL, timestamp: 5 },
      ],
      snapshots: [
        { url: resultURL, timestamp: 2 },
        { url: 'https://unlisted.example/eclipse-guide', timestamp: 4 },
        { url: resultURL, timestamp: 5 },
      ].map(({ url, timestamp }) => ({
        timestamp, url, selectorMap: {}, phase: 'final' as const,
        capture: {
          status: 'complete' as const, frames: [], nodeCount: 1,
          redactionCount: 0, removedNodeCount: 0,
        },
        domTree: { type: 'element' as const, tagName: 'main', children: [] },
      })),
      termination: { reason: 'user' as const, message: 'done', timestamp: 6, complete: true },
    };
    expect(() => assertBingRecording(recording)).not.toThrow();
    const asynchronouslyAppendedSubmission: PageAgentRecording = {
      ...recording,
      events: [
        ...recording.events.filter((event) => event.type !== 'submitForm'),
        { type: 'submitForm', index: 3, timestamp: 1.5 },
      ],
    };
    expect(() => assertBingRecording(asynchronouslyAppendedSubmission)).not.toThrow();
    expect(() => assertBingRecording({
      ...recording,
      events: recording.events.filter((event) => event.type !== 'submitForm'),
    })).toThrow(/direct query submission/);
    const clickedSubmission: PageAgentRecording = {
      ...recording,
      events: [...recording.events],
    };
    clickedSubmission.events.splice(2, 0, { type: 'click', index: 9, timestamp: 1.25 });
    expect(() => assertBingRecording(clickedSubmission)).toThrow(/direct query submission/);
    const wrongQuery: PageAgentRecording = {
      ...recording,
      events: recording.events.map((event) => event.type === 'inputText'
        ? { ...event, text: 'different query' }
        : event),
    };
    expect(() => assertBingRecording(wrongQuery)).toThrow(/direct query submission/);
    expect(() => assertBingRecording({
      ...recording,
      events: recording.events.filter((event) => event.type !== 'navigate'),
    })).toThrow(/enter the exact results page/);
  });
});
