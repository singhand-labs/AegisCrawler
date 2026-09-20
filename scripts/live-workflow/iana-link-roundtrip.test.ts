import { describe, expect, it } from 'vitest';
import {
  IANA_ACTION_PAUSE_MS,
  IANA_ARTICLE_ROOT_SELECTOR,
  IANA_DESTINATION_URL,
  IANA_ENTRY_URL,
  IANA_LINK_TEXT,
  IANA_NON_CONTENT_SELECTOR,
  assertIANARoundtripRecording,
  assertIANARoundtripRule,
  assertIANARows,
  assertReviewedIANAURL,
  findIANABoundary,
  ianaLinkRoundtripDemo,
  prepareIANAContext,
  prepareIANAPage,
} from './iana-link-roundtrip';

describe('IANA link round-trip full-workflow contract', () => {
  it('clicks the visible link, enters it, and exits with browser Back', async () => {
    const actions: string[] = [];
    let currentURL = IANA_ENTRY_URL;
    const page = {
      bringToFront: async () => { actions.push('front'); },
      url: () => currentURL,
      title: async () => currentURL === IANA_ENTRY_URL ? 'Example Domains' : IANA_LINK_TEXT,
      locator: (selector: string) => {
        expect(selector).toBe('body');
        return { innerText: async () => 'ordinary public technical documentation' };
      },
      getByRole: (role: string, options: unknown) => {
        expect(role).toBe('link');
        expect(options).toEqual({ name: IANA_LINK_TEXT, exact: true });
        return { first: () => ({
          waitFor: async () => { actions.push('link:visible'); },
          click: async () => { actions.push('link:click'); currentURL = IANA_DESTINATION_URL; },
        }) };
      },
      waitForURL: async (url: string) => { actions.push(`url:${url}`); expect(currentURL).toBe(url); },
      waitForTimeout: async (ms: number) => { actions.push(`pause:${ms}`); },
      goBack: async () => { actions.push('browser:back'); currentURL = IANA_ENTRY_URL; return {}; },
    };
    await ianaLinkRoundtripDemo(page as never, async () => { actions.push('recording:ready'); });
    expect(actions).toEqual([
      'front', 'link:visible', 'link:click', `url:${IANA_DESTINATION_URL}`,
      'recording:ready', `pause:${IANA_ACTION_PAUSE_MS}`, 'browser:back', `url:${IANA_ENTRY_URL}`,
      'recording:ready',
      `pause:${IANA_ACTION_PAUSE_MS}`,
    ]);
  });

  it('allows only the two reviewed HTTPS IANA pages', () => {
    expect(() => assertReviewedIANAURL(IANA_ENTRY_URL)).not.toThrow();
    expect(() => assertReviewedIANAURL(`${IANA_DESTINATION_URL}#example-domains`)).not.toThrow();
    expect(() => assertReviewedIANAURL('http://www.iana.org/help/example-domains'))
      .toThrow('requires HTTPS');
    expect(() => assertReviewedIANAURL('https://www.iana.org/domains/root/db'))
      .toThrow('unreviewed page');
    expect(() => assertReviewedIANAURL('https://example.com/help/example-domains'))
      .toThrow('www.iana.org');
  });

  it('blocks unreviewed documents while allowing page resources', async () => {
    let handler: ((route: {
      request(): { resourceType(): string; url(): string };
      continue(): Promise<void>;
      abort(reason: string): Promise<void>;
    }) => Promise<void>) | undefined;
    await prepareIANAContext({
      route: async (_pattern: string, candidate: typeof handler) => { handler = candidate; },
    } as never);
    const exercise = async (resourceType: string, url: string) => {
      const events: string[] = [];
      await handler?.({
        request: () => ({ resourceType: () => resourceType, url: () => url }),
        continue: async () => { events.push('continue'); },
        abort: async (reason) => { events.push(`abort:${reason}`); },
      });
      return events;
    };
    expect(await exercise('document', IANA_DESTINATION_URL)).toEqual(['continue']);
    expect(await exercise('document', 'https://www.iana.org/domains/root/db'))
      .toEqual(['abort:blockedbyclient']);
    expect(await exercise('style', 'https://www.iana.org/_css/2015.1/screen.css'))
      .toEqual(['continue']);
  });

  it('removes navigation chrome while preserving the reviewed visible link', async () => {
    document.body.innerHTML = [
      '<header><nav><a href="/domains">Domains</a></nav></header>',
      '<main><h1>Example Domains</h1><p>Introduction</p>',
      `<ul><li><a href="/domains/reserved">${IANA_LINK_TEXT}</a></li></ul></main>`,
      '<footer><a href="/about">Terms of Service</a></footer>',
    ].join('');
    await prepareIANAPage({
      evaluate: async (callback: (selector: string) => void, selector: string) => callback(selector),
      getByRole: () => ({ first: () => ({ waitFor: async () => undefined }) }),
      locator: (selector: string) => {
        if (selector === IANA_NON_CONTENT_SELECTOR) {
          return { count: async () => document.querySelectorAll(selector).length };
        }
        if (selector === 'body') return { innerText: async () => document.body.textContent ?? '' };
        throw new Error(`unexpected selector ${selector}`);
      },
      url: () => IANA_ENTRY_URL,
      title: async () => 'Example Domains',
    } as never);
    expect(document.querySelector('header')).toBeNull();
    expect(document.querySelector('footer')).toBeNull();
    expect(document.querySelector('main')).not.toBeNull();
    expect(document.querySelector('main a')?.textContent).toBe(IANA_LINK_TEXT);
  });

  it('requires exact click, enter, and return recording order', () => {
    const target = {
      index: 7, tagName: 'a', selector: 'main li > a', text: IANA_LINK_TEXT,
      boundingRect: { x: 1, y: 1, width: 100, height: 20 },
    };
    const recording = {
      version: '2.0.0',
      meta: { startUrl: IANA_ENTRY_URL },
      events: [
        { type: 'click', index: 7, timestamp: 1 },
        { type: 'navigate', url: IANA_DESTINATION_URL, timestamp: 2 },
        { type: 'navigate', url: IANA_ENTRY_URL, timestamp: 3 },
      ],
      snapshots: Array.from({ length: 5 }, () => ({
        selectorMap: { 7: target }, capture: { status: 'complete' },
      })),
    };
    expect(() => assertIANARoundtripRecording(recording as never)).not.toThrow();
    expect(() => assertIANARoundtripRecording({
      ...recording,
      snapshots: recording.snapshots.map((snapshot) => ({
        ...snapshot,
        selectorMap: { 7: { ...target, text: 'sanitized or normalized label' } },
      })),
    } as never)).not.toThrow();
    expect(() => assertIANARoundtripRecording({
      ...recording,
      snapshots: recording.snapshots.map((snapshot) => ({
        ...snapshot,
        selectorMap: { 7: { ...target, tagName: 'button' } },
      })),
    } as never)).toThrow('recorded anchor target');
    expect(() => assertIANARoundtripRecording({
      ...recording,
      events: [recording.events[0], recording.events[2], recording.events[1]],
    } as never)).toThrow('entry navigation');
    expect(() => assertIANARoundtripRecording({
      ...recording, events: recording.events.slice(1), snapshots: recording.snapshots.slice(1),
    } as never)).toThrow('exactly click, enter, and return');
  });

  it('requires replay of the complete round trip before visible extraction', () => {
    const valid = {
      selectors: {
        reviewedLink: {
          selector: 'main li > a', role: 'link', roleName: IANA_LINK_TEXT, text: IANA_LINK_TEXT,
        },
      },
      steps: [
        { action: 'click', target: { $ref: 'reviewedLink' } },
        { action: 'navigate', url: IANA_DESTINATION_URL },
        { action: 'navigate', url: IANA_ENTRY_URL },
        { action: 'extract', target: { selector: IANA_ARTICLE_ROOT_SELECTOR, visible: true }, fields: {
          title: { type: 'text', selector: 'h1', visible: true },
          introduction: { type: 'text', selector: 'p', visible: true },
        } },
      ],
    };
    expect(() => assertIANARoundtripRule(valid)).not.toThrow();
    expect(() => assertIANARoundtripRule({
      ...valid,
      steps: valid.steps.map((step, index) => index === 3 ? {
        ...step,
        target: { selector: '#body', visible: true },
        fields: {
          title: { type: 'text', selector: 'h1', visible: true },
          introduction: {
            type: 'text', selector: '.hemmed > main > article > p', visible: true,
          },
        },
      } : step),
    })).not.toThrow();
    expect(() => assertIANARoundtripRule({ ...valid, steps: valid.steps.slice(1) }))
      .toThrow('click, enter, return');
    expect(() => assertIANARoundtripRule({
      ...valid, steps: [valid.steps[0], valid.steps[2], valid.steps[1], valid.steps[3]],
    })).toThrow('click, enter, return');
    expect(() => assertIANARoundtripRule({
      ...valid,
      selectors: {
        reviewedLink: {
          selector: 'li > a', role: 'link', roleName: 'Terms of Service', text: 'Terms of Service',
        },
      },
    })).toThrow('reviewed IANA-managed Reserved Domains anchor');
    expect(() => assertIANARoundtripRule({
      ...valid,
      steps: valid.steps.map((step, index) => index === 3
        ? { ...step, target: { selector: '#body' } }
        : step),
    })).toThrow('isolated visible main content');
    expect(() => assertIANARoundtripRule({
      ...valid,
      steps: valid.steps.map((step, index) => index === 3 ? {
        ...step,
        target: { selector: '#body', visible: true },
      } : step),
    })).toThrow('descend through the isolated main content');
  });

  it('requires exact independent-oracle equality and fails on boundaries', () => {
    const expected = { title: 'Example Domains', introduction: 'A sufficiently long introduction.' };
    expect(() => assertIANARows([expected], {}, expected, 'replay')).not.toThrow();
    expect(() => assertIANARows([{
      ...expected,
      introduction: 'A sufficiently\n  long introduction.',
    }], {}, expected, 'replay')).not.toThrow();
    expect(() => assertIANARows([{ ...expected, title: 'Other' }], {}, expected, 'task'))
      .toThrow('independent visible oracle');
    expect(() => assertIANARows([{
      ...expected,
      introduction: 'A sufficiently long but different introduction.',
    }], {}, expected, 'task')).toThrow('independent visible oracle');
    expect(findIANABoundary('ordinary technical documentation', 'Example Domains')).toBeUndefined();
    expect(findIANABoundary('Please verify you are human', 'Challenge'))
      .toBe('verify you are human');
  });
});
