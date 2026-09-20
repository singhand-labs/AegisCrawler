import { describe, expect, it } from 'vitest';
import {
  MDN_CANONICAL_PATH,
  MDN_ENTRY_URL,
  MDN_POINTER_CLICK_PAUSE_MS,
  MDN_POINTER_WAYPOINT_PAUSE_MS,
  MDN_POINTER_WAYPOINTS,
  MDN_SCROLL_STEPS,
  MDN_STABLE_ARTICLE_ROOT_SELECTOR,
  MDN_IFRAME_EXCLUSION_CSS,
  MDN_NON_CONTENT_SELECTOR,
  MDN_VISIBLE_ACTION_CSS,
  MDN_VISIBLE_ACTION_PAUSE_MS,
  assertMDNRecording,
  assertMDNRule,
  assertMDNRows,
  assertMDNURL,
  findMDNBoundary,
  mdnGuideDemo,
  prepareMDNContext,
  prepareMDNPage,
} from './mdn-guide';

describe('MDN public full-workflow contract', () => {
  it('keeps the simulated human demonstration visible and paced', () => {
    expect(MDN_SCROLL_STEPS).toEqual([240, 240, 320]);
    expect(MDN_POINTER_WAYPOINTS).toBeGreaterThanOrEqual(4);
    expect(MDN_POINTER_WAYPOINT_PAUSE_MS).toBeGreaterThanOrEqual(200);
    expect(MDN_POINTER_CLICK_PAUSE_MS).toBeGreaterThanOrEqual(250);
    expect(MDN_VISIBLE_ACTION_CSS).toContain('h1:hover');
    expect(MDN_VISIBLE_ACTION_CSS).toContain('h1:active');
    expect(MDN_VISIBLE_ACTION_PAUSE_MS).toBeGreaterThanOrEqual(5_000);
  });

  it('moves through visible paced pointer waypoints before paced scrolling', async () => {
    const actions: string[] = [];
    await mdnGuideDemo({
      bringToFront: async () => { actions.push('front'); },
      url: () => MDN_ENTRY_URL,
      title: async () => 'CSS grid layout - CSS | MDN',
      getByRole: () => ({
        waitFor: async () => undefined,
        boundingBox: async () => ({ x: 200, y: 120, width: 400, height: 90 }),
      }),
      locator: (selector: string) => {
        expect(selector).toBe('body');
        return { innerText: async () => 'ordinary public documentation' };
      },
      mouse: {
        move: async (x: number, y: number) => { actions.push(`move:${x}:${y}`); },
        down: async () => { actions.push('pointer:down'); },
        up: async () => { actions.push('pointer:up'); },
        wheel: async (_x: number, y: number) => { actions.push(`wheel:${y}`); },
      },
      waitForTimeout: async (ms: number) => { actions.push(`pause:${ms}`); },
    } as never);

    const moves = actions.filter((action) => action.startsWith('move:'));
    expect(moves).toHaveLength(MDN_POINTER_WAYPOINTS);
    expect(actions.filter((action) =>
      action === `pause:${MDN_POINTER_WAYPOINT_PAUSE_MS}`)).toHaveLength(MDN_POINTER_WAYPOINTS);
    expect(actions).toContain('pointer:down');
    expect(actions).toContain(`pause:${MDN_POINTER_CLICK_PAUSE_MS}`);
    expect(actions).toContain('pointer:up');
    expect(actions.indexOf('pointer:down')).toBeLessThan(actions.indexOf('pointer:up'));
    expect(actions.filter((action) => action.startsWith('wheel:')))
      .toEqual(MDN_SCROLL_STEPS.map((distance) => `wheel:${distance}`));
    expect(actions.indexOf(moves.at(-1) as string))
      .toBeLessThan(actions.indexOf(`wheel:${MDN_SCROLL_STEPS[0]}`));
  });

  it('allows only the exact reviewed HTTPS guide', () => {
    expect(() => assertMDNURL(
      `https://developer.mozilla.org${MDN_CANONICAL_PATH}#reference`,
    )).not.toThrow();
    expect(() => assertMDNURL('http://developer.mozilla.org/en-US/docs/Web/CSS/Guides/Grid_layout'))
      .toThrow('requires HTTPS');
    expect(() => assertMDNURL('https://developer.mozilla.org/en-US/docs/Web/CSS/Guides/Flexbox'))
      .toThrow('reviewed CSS grid guide');
    expect(() => assertMDNURL('https://example.com/en-US/docs/Web/CSS/Guides/Grid_layout'))
      .toThrow('developer.mozilla.org');
  });

  it('fails closed on authentication, CAPTCHA, and rate boundaries', () => {
    expect(findMDNBoundary('ordinary public documentation', 'CSS grid layout')).toBeUndefined();
    expect(findMDNBoundary('Please verify you are human', 'Challenge')).toBe('verify you are human');
    expect(findMDNBoundary('Sign in to continue', 'Authentication')).toBe('sign in to continue');
    expect(findMDNBoundary('Rate limit exceeded', 'Slow down')).toBe('rate limit exceeded');
  });

  it('requires exact one-row oracle equality', () => {
    const expected = {
      title: 'CSS grid layout',
      introduction: 'A sufficiently descriptive introduction.',
      first_section: 'Grid layout in action',
    };
    expect(() => assertMDNRows([expected], {}, expected, 'task')).not.toThrow();
    expect(() => assertMDNRows([{ ...expected, title: 'Grid' }], {}, expected, 'task'))
      .toThrow('independent visible oracle');
    expect(() => assertMDNRows([], {}, expected, 'task')).toThrow('exactly one');
  });

  it('blocks cross-origin document frames while allowing MDN and non-document resources', async () => {
    let handler: ((route: {
      request(): { resourceType(): string; url(): string };
      continue(): Promise<void>;
      abort(reason: string): Promise<void>;
    }) => Promise<void>) | undefined;
    await prepareMDNContext({
      route: async (_pattern: string, candidate: typeof handler) => {
        handler = candidate;
      },
    } as never);
    expect(handler).toBeTypeOf('function');
    const exercise = async (resourceType: string, url: string) => {
      const events: string[] = [];
      await handler?.({
        request: () => ({ resourceType: () => resourceType, url: () => url }),
        continue: async () => { events.push('continue'); },
        abort: async (reason) => { events.push(`abort:${reason}`); },
      });
      return events;
    };
    expect(await exercise('document', MDN_ENTRY_URL)).toEqual(['continue']);
    expect(await exercise('document', 'https://ads.example/frame')).toEqual(['abort:blockedbyclient']);
    expect(await exercise('script', 'https://cdn.example/app.js')).toEqual(['continue']);
  });

  it('excludes non-content page chrome before recording while retaining the reviewed article', async () => {
    const styles: string[] = [];
    let preparedSelector: string | undefined;
    document.body.innerHTML = [
      '<header>chrome</header>',
      '<main id="content" class="main-page-content">',
      '<h1>CSS grid layout</h1><p>Introduction</p><h2>Grid layout in action</h2>',
      '</main>',
    ].join('');
    await prepareMDNPage({
      addStyleTag: async ({ content }: { content: string }) => { styles.push(content); },
      evaluate: async (callback: (selector: string) => void, selector: string) => {
        preparedSelector = selector;
        callback(selector);
      },
      url: () => MDN_ENTRY_URL,
      title: async () => 'CSS grid layout - CSS | MDN',
      locator: (selector: string) => {
        if (selector === MDN_NON_CONTENT_SELECTOR) {
          return {
            waitFor: async () => undefined,
            count: async () => 0,
          };
        }
        if (selector === 'main') {
          return {
            first: () => ({
              locator: (child: string) => {
                expect(child).toBe('h1');
                return { first: () => ({ waitFor: async () => undefined }) };
              },
            }),
          };
        }
        if (selector === 'body') {
          return { innerText: async () => 'ordinary public documentation' };
        }
        throw new Error(`unexpected selector ${selector}`);
      },
    } as never);
    expect(styles).toEqual([MDN_IFRAME_EXCLUSION_CSS, MDN_VISIBLE_ACTION_CSS]);
    expect(preparedSelector).toBe(MDN_NON_CONTENT_SELECTOR);
    expect(document.querySelector('header')).toBeNull();
    expect(document.querySelector('main')).not.toBeNull();
    expect(document.querySelector('main')?.id).toBe('content');
    expect(document.querySelector('main')?.className).toBe('main-page-content');
    expect(document.querySelector('main h1')?.textContent).toBe('CSS grid layout');
    expect(document.querySelector('main p')?.textContent).toBe('Introduction');
    expect(document.querySelector('main h2')?.textContent).toBe('Grid layout in action');
    expect(MDN_NON_CONTENT_SELECTOR).toContain('[role="complementary"]');
    expect(MDN_NON_CONTENT_SELECTOR).toContain('nav');
  });

  it('accepts only a bounded complete visible-action recording', () => {
    const recording = {
      meta: { startUrl: MDN_ENTRY_URL },
      events: [
        { type: 'scroll' },
        { type: 'scroll' },
      ],
      snapshots: [
        { capture: { status: 'complete' } },
        { capture: { status: 'complete' } },
        { capture: { status: 'complete' } },
        { capture: { status: 'complete' } },
      ],
    };
    expect(() => assertMDNRecording(recording as never)).not.toThrow();
    expect(() => assertMDNRecording({
      ...recording,
      events: [{ type: 'executeJavascript' }, { type: 'scroll' }],
    } as never)).toThrow('forbids script execution');
    expect(() => assertMDNRecording({
      ...recording,
      snapshots: [{ capture: { status: 'partial' } }],
    } as never)).toThrow();
  });

  it('rejects empty-field and off-article extraction plans before replay', () => {
    const valid = {
      steps: [
        { action: 'scrollBy', direction: 'down', distance: 1, unit: 'pages' },
        { action: 'scrollBy', direction: 'down', distance: 1, unit: 'pages' },
        { action: 'scrollBy', direction: 'down', distance: 1, unit: 'pages' },
        {
          action: 'extract',
          target: { selector: 'main', visible: true },
          fields: {
            title: { type: 'text', selector: 'h1', visible: true },
            introduction: { type: 'text', selector: 'h1 + p', visible: true },
            first_section: { type: 'text', selector: 'h2', visible: true },
          },
        },
      ],
    };
    expect(() => assertMDNRule(valid)).not.toThrow();
    expect(() => assertMDNRule({
      ...valid,
      steps: valid.steps.slice(3),
    })).toThrow('reperform exactly three recorded downward page scrolls');
    expect(() => assertMDNRule({
      steps: [
        ...valid.steps.slice(0, 3),
        {
          action: 'extract',
          target: { selector: 'dl > dd', visible: true },
          fields: {
            title: { type: 'text', visible: true },
            introduction: { type: 'text', visible: true },
            first_section: { type: 'text', visible: true },
          },
        },
      ],
    })).toThrow('stable semantic main article');
    expect(() => assertMDNRule({
      ...valid,
      steps: [
        ...valid.steps.slice(0, 3),
        {
          ...valid.steps[3],
          fields: {
            ...(valid.steps[3] as { fields: object }).fields,
            title: { type: 'text', visible: true },
          },
        },
      ],
    })).toThrow('reviewed visible semantic selector');
    expect(() => assertMDNRule({
      ...valid,
      steps: [
        ...valid.steps.slice(0, 3),
        {
          ...valid.steps[3],
          fields: {
            title: { type: 'text', selector: 'h1', visible: true },
            introduction: { type: 'text', selector: '.layout__header', visible: true },
            first_section: { type: 'text', selector: 'dl', visible: true },
          },
        },
      ],
    })).toThrow('reviewed visible semantic selector');
    expect(() => assertMDNRule({
      ...valid,
      steps: [
        ...valid.steps.slice(0, 3),
        {
          action: 'waitForElementVisible',
          target: { selector: '#content' },
        },
        valid.steps[3],
      ],
    })).toThrow('readiness wait must target the stable semantic main article');
  });
});
