import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ContentRecorder, RecorderOptions } from '../content';
import type { PageAgentRecording, DomSnapshot, PageMark } from '../../../src/rule-generator/types';

type PageAgentRecordingType = PageAgentRecording;

function pageMark(overrides: Partial<PageMark> = {}): PageMark {
  return {
    id: 'mark-1',
    timestamp: 1,
    url: window.location.href,
    role: 'field',
    note: 'price field',
    actionIndex: 0,
    snapshotSequence: 0,
    state: window.location.href,
    element: {
      index: 1,
      tagName: 'span',
      selector: '.price',
      stableSelector: '.price',
      text: '$10',
      boundingRect: { x: 1, y: 2, width: 30, height: 12 },
    },
    ...overrides,
  };
}

async function flushPromises(): Promise<void> {
  for (let i = 0; i < 50; i++) {
    await Promise.resolve();
  }
}


/** Follow a snapshot's ref pointer to its content snapshot (v2 reference
 *  snapshots store identical content once; consumers resolve by sequence). */
function resolveSnapshot(recording: { snapshots: Array<{ sequence?: number; ref?: number; domTree?: unknown }> }, snapshot: { sequence?: number; ref?: number; domTree?: unknown }) {
  if (snapshot?.ref === undefined) return snapshot;
  const target = recording.snapshots.find((item) => item.sequence === snapshot.ref);
  if (!target) throw new Error(`snapshot ref ${snapshot.ref} cannot be resolved`);
  return target;
}

describe('ContentRecorder', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    vi.useFakeTimers();
    // Provide an isolated sessionStorage so persistence across tests does not
    // leak between recorder instances.
    const store: Record<string, string> = {};
    Object.defineProperty(window, 'sessionStorage', {
      value: {
        getItem: (key: string) => store[key] ?? null,
        setItem: (key: string, value: string) => {
          store[key] = value;
        },
        removeItem: (key: string) => {
          delete store[key];
        },
        clear: () => {
          Object.keys(store).forEach((key) => delete store[key]);
        },
      },
      configurable: true,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllTimers();
    delete (window as unknown as Record<string, unknown>).__pageAgentController;
    delete (window as unknown as Record<string, unknown>).PageController;
    delete (window as unknown as Record<string, unknown>).__openCrawlerRecorder;
    delete (globalThis as Record<string, unknown>).chrome;
    try {
      sessionStorage.removeItem('__ocRecordingState');
    } catch {
      // ignore
    }
  });

  describe('lifecycle', () => {
    it('starts and stops and returns a valid PageAgentRecording', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      expect(recorder.isRecording()).toBe(false);

      recorder.start();
      expect(recorder.isRecording()).toBe(true);

      const recording = recorder.stop();
      expect(recorder.isRecording()).toBe(false);
      expect(recording.version).toBe('1.0.0');
      expect(recording.meta.startUrl).toBe(window.location.href);
      expect(recording.meta.title).toBe(document.title);
      expect(recording.events).toEqual([]);
      expect(recording.snapshots).toEqual([]);
    });

    it('returns an empty recording when stopped without starting', () => {
      const recorder = new ContentRecorder();
      const recording = recorder.stop();
      expect(recording.version).toBe('1.0.0');
      expect(recording.events).toEqual([]);
      expect(recording.snapshots).toEqual([]);
    });

    it('ignores a second start() while already recording', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      recorder.start();
      const recording = recorder.stop();
      expect(recording.events).toEqual([]);
    });

    it('caps recording size by dropping older domTrees', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      const recording = (recorder as unknown as { recording: PageAgentRecordingType }).recording;
      recording.snapshots = [
        { timestamp: 1, url: 'http://a', selectorMap: {}, domTree: { type: 'element', tagName: 'html', text: 'a'.repeat(5000) } },
        { timestamp: 2, url: 'http://a', selectorMap: {}, domTree: { type: 'element', tagName: 'html' } },
      ];
      const capped = (recorder as unknown as { capRecordingSize: (r: PageAgentRecordingType, max: number) => PageAgentRecordingType }).capRecordingSize(recording, 1000);
      expect(capped.snapshots[0].domTree).toBeUndefined();
      expect(capped.snapshots[1].domTree).toBeDefined();
    });
  });

  describe('page marks', () => {
    it('stores, replaces, and removes v2 page marks', () => {
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', captureSnapshotBeforeEachEvent: false });
      recorder.start();

      recorder.addPageMark(pageMark({ note: 'old' }));
      recorder.addPageMark(pageMark({ note: 'new' }));
      expect((recorder as unknown as { recording: PageAgentRecordingType }).recording.marks).toHaveLength(1);
      expect((recorder as unknown as { recording: PageAgentRecordingType }).recording.marks?.[0].note).toBe('new');

      expect(recorder.removePageMark('mark-1')).toBe(true);
      expect((recorder as unknown as { recording: PageAgentRecordingType }).recording.marks).toEqual([]);
      expect(recorder.removePageMark('missing')).toBe(false);
    });

    it('rejects over-limit marks and over-long notes', () => {
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        captureSnapshotBeforeEachEvent: false,
        maxMarks: 1,
      });
      recorder.start();

      expect(() => recorder.addPageMark(pageMark({ note: 'x'.repeat(201) }))).toThrow(/note/i);
      recorder.addPageMark(pageMark({ id: 'm1' }));
      expect(() => recorder.addPageMark(pageMark({ id: 'm2' }))).toThrow(/maximum mark count/i);
    });

    it('keeps marks in the returned recording when proxy mode is active', async () => {
      const controller = {
        clickElement: vi.fn(async (_index: number) => ({ success: true })),
        inputText: vi.fn(async (_index: number, _text: string) => ({ success: true })),
        selectOption: vi.fn(async (_index: number, _optionText: string) => ({ success: true })),
        scroll: vi.fn(async (_options: unknown) => ({ success: true })),
        scrollHorizontally: vi.fn(async (_options: unknown) => ({ success: true })),
        executeJavascript: vi.fn(async (_script: string) => ({ success: true })),
      };
      (window as unknown as Record<string, unknown>).PageController = controller;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      recorder.addPageMark(pageMark());
      await controller.clickElement(1);
      const recording = recorder.stop();

      expect(recording.marks).toEqual([pageMark()]);
      expect(recording.events[0]).toMatchObject({ type: 'click', index: 1 });
    });

    it('stops with a size-limit when a mark pushes the recording past the byte budget', async () => {
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        captureSnapshotBeforeEachEvent: false,
        maxRecordingBytes: 250,
      });
      recorder.start();

      recorder.addPageMark(pageMark({ note: 'compact' }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.termination?.reason).toBe('size-limit');
    });
  });

  describe('PageController proxy mode', () => {
    function makeMockController() {
      return {
        clickElement: vi.fn(async (_index: number) => ({ success: true })),
        inputText: vi.fn(async (_index: number, _text: string) => ({ success: true })),
        selectOption: vi.fn(async (_index: number, _optionText: string) => ({ success: true })),
        scroll: vi.fn(async (_options: unknown) => ({ success: true })),
        scrollHorizontally: vi.fn(async (_options: unknown) => ({ success: true })),
        executeJavascript: vi.fn(async (_script: string) => ({ success: true })),
      };
    }

    it('records clickElement as a click event', async () => {
      const controller = makeMockController();
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await controller.clickElement(5);

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'click', index: 5 });
    });

    it('records inputText events', async () => {
      const controller = makeMockController();
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await controller.inputText(3, 'hello');

      const recording = recorder.stop();
      expect(recording.events[0]).toMatchObject({ type: 'inputText', index: 3, text: 'hello' });
    });

    it('records selectOption events', async () => {
      const controller = makeMockController();
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await controller.selectOption(4, 'Option B');

      const recording = recorder.stop();
      expect(recording.events[0]).toMatchObject({ type: 'selectOption', index: 4, optionText: 'Option B' });
    });

    it('records scroll events from the controller', async () => {
      const controller = makeMockController();
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await controller.scroll({ down: true, numPages: 2 });

      const recording = recorder.stop();
      expect(recording.events[0]).toMatchObject({
        type: 'scroll',
        direction: 'down',
        amount: 2,
        unit: 'pages',
      });
    });

    it('captures snapshots before each event when enabled', async () => {
      const controller = makeMockController();
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      await controller.clickElement(1);

      const recording = recorder.stop();
      expect(recording.snapshots).toHaveLength(1);
      expect(recording.snapshots[0]).toMatchObject({
        url: expect.any(String),
        timestamp: expect.any(Number),
        selectorMap: expect.any(Object),
      });
    });

    it('restores controller methods on stop', async () => {
      const controller = makeMockController();
      const originalClick = controller.clickElement;
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await controller.clickElement(1);
      recorder.stop();
      await controller.clickElement(2);

      expect(originalClick).toHaveBeenCalledTimes(2);
    });

    it('rejects a controller missing a required method and falls back to DOM mode', () => {
      const controller = makeMockController();
      delete (controller as unknown as Record<string, unknown>).executeJavascript;
      (window as unknown as Record<string, unknown>).PageController = controller;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      const recording = recorder.stop();
      expect(recording.events).toEqual([]);
    });

    it('falls back to window.__pageAgentController when window.PageController is incomplete', async () => {
      const incomplete = makeMockController();
      delete (incomplete as unknown as Record<string, unknown>).scrollHorizontally;
      const fallback = makeMockController();

      (window as unknown as Record<string, unknown>).PageController = incomplete;
      (window as unknown as Record<string, unknown>).__pageAgentController = fallback;

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await (fallback as unknown as { scroll: typeof fallback.scroll }).scroll({ down: true, numPages: 1 });

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'scroll', direction: 'down' });
    });
  });

  describe('DOM event fallback mode', () => {
    it('records click events with an index and snapshot', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const btn = document.getElementById('btn')!;
      btn.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      const event = recording.events[0];
      expect(event.type).toBe('click');
      expect(event).toHaveProperty('index');
      expect(recording.snapshots).toHaveLength(1);
      const snapshot = recording.snapshots[0];
      expect(snapshot.selectorMap[(event as { index: number }).index]).toMatchObject({
        tagName: 'button',
        selector: '#btn',
      });
    });

    it('records a nested link child click against the interactable anchor', () => {
      document.body.innerHTML = '<a id="next" href="/page/2/">Next <span id="arrow" aria-hidden="true">→</span></a>';
      document.getElementById('next')!.addEventListener('click', (event) => event.preventDefault());
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      document.getElementById('arrow')!.dispatchEvent(new MouseEvent('click', {
        bubbles: true,
        cancelable: true,
        composed: true,
      }));

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'click', index: expect.any(Number) });
      expect(recording.snapshots).toHaveLength(1);
      const event = recording.events[0] as { index: number };
      expect(recording.snapshots[0].selectorMap[event.index]).toMatchObject({
        tagName: 'a',
        selector: '#next',
      });
    });

    it('keeps same-selector pagination links at distinct recording indexes', () => {
      document.body.innerHTML = `<ul class="pagination">
        <li><a href="?page_num=1"> 1 </a></li>
        <li><a href="?page_num=1" aria-label="Next"> » </a></li>
      </ul>`;
      document.querySelectorAll('a').forEach((link) => link.addEventListener('click', (event) => event.preventDefault()));
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', captureSnapshotBeforeEachEvent: true });
      recorder.start();

      (document.querySelector('a') as HTMLAnchorElement).click();
      const recording = recorder.stop();
      const event = recording.events[0] as { index: number };
      const beforeAction = recording.snapshots.find((snapshot) => snapshot.phase === 'before-action');

      expect(beforeAction?.selectorMap[event.index]).toMatchObject({
        selector: 'li > a',
        text: '1',
      });
      expect(Object.values(beforeAction?.selectorMap ?? {})).toEqual(expect.arrayContaining([
        expect.objectContaining({ selector: 'li > a', text: '1' }),
        expect.objectContaining({ selector: 'li > a', ariaLabel: 'Next' }),
      ]));
    });

    it('captures a full domTree in the snapshot', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      const btn = document.getElementById('btn')!;
      btn.click();
      await flushPromises();
      const recording = recorder.stop();
      expect(recording.snapshots).toHaveLength(1);
      const snapshot = recording.snapshots[0];
      expect(snapshot.domTree).toBeDefined();
      expect(snapshot.domTree!.tagName).toBe('html');
      const body = snapshot.domTree!.children?.find((c) => c.tagName === 'body');
      expect(body).toBeDefined();
      expect(body!.children?.some((c) => c.tagName === 'button')).toBe(true);
    });

    it('records inputText events after debounce', async () => {
      document.body.innerHTML = '<input id="search" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const input = document.getElementById('search') as HTMLInputElement;
      input.value = 'hello';
      input.dispatchEvent(new Event('input', { bubbles: true }));

      vi.advanceTimersByTime(100);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'inputText', text: 'hello' });
    });

    it('records selectOption events on change', async () => {
      document.body.innerHTML = `
        <select id="sel">
          <option value="a">Alpha</option>
          <option value="b" selected>Bravo</option>
        </select>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const select = document.getElementById('sel') as HTMLSelectElement;
      select.selectedIndex = 0;
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'selectOption', optionText: 'Alpha' });
    });

    it('ignores change events on non-select elements', async () => {
      document.body.innerHTML = '<input id="inp" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await (recorder as unknown as { handleChange: (e: Event) => Promise<void> }).handleChange({
        target: document.getElementById('inp'),
      } as unknown as Event);
      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('debounces rapid input events into a single record', async () => {
      document.body.innerHTML = '<input id="search" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('search') as HTMLInputElement;
      input.value = 'a';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.value = 'ab';
      input.dispatchEvent(new Event('input', { bubbles: true }));

      vi.advanceTimersByTime(100);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'inputText', text: 'ab' });
    });

    it('records scroll events debounced to 250ms', async () => {
      document.body.innerHTML =
        '<div id="scroller" style="height:100px;overflow:auto;"><div style="height:500px;"></div></div>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const scroller = document.getElementById('scroller') as HTMLElement;
      scroller.scrollTop = 50;
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));

      vi.advanceTimersByTime(250);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events.length).toBeGreaterThan(0);
      expect(recording.events[0].type).toBe('scroll');
    });

    it('records Enter keydown as submitForm when input is not in a form', async () => {
      document.body.innerHTML = '<input id="q" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'submitForm', index: expect.any(Number) });
    });

    it('flushes final inputText before Enter submission without a form', async () => {
      document.body.innerHTML = '<input id="q" type="search" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.value = 'how do solar eclipses happen';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(2);
      expect(recording.events[0]).toMatchObject({
        type: 'inputText', index: expect.any(Number), text: 'how do solar eclipses happen',
      });
      expect(recording.events[1]).toMatchObject({ type: 'submitForm', index: expect.any(Number) });
    });

    it('keeps Enter on a form-less non-text input as click semantics', async () => {
      document.body.innerHTML = '<input id="choice" type="checkbox" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('choice') as HTMLInputElement;
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'click', index: expect.any(Number) });
    });

    it('records Enter keydown inside a form as submitForm', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'submitForm', index: expect.any(Number) });
    });

    it('records form submit events as submitForm', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /><button id="s" type="submit">Go</button></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const form = document.getElementById('f') as HTMLFormElement;
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'submitForm', index: expect.any(Number), submitterIndex: undefined });
    });

    it('flushes pending inputText before submitForm when Enter is pressed', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.value = 'hello';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(2);
      expect(recording.events[0]).toMatchObject({ type: 'inputText', text: 'hello' });
      expect(recording.events[1]).toMatchObject({ type: 'submitForm' });
    });

    it('flushes pending inputText before submitForm on explicit form submit', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.value = 'world';
      input.dispatchEvent(new Event('input', { bubbles: true }));

      const form = document.getElementById('f') as HTMLFormElement;
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(2);
      expect(recording.events[0]).toMatchObject({ type: 'inputText', text: 'world' });
      expect(recording.events[1]).toMatchObject({ type: 'submitForm' });
    });

    it('does not duplicate submitForm when Enter keydown and submit event both fire', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.value = 'hello';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));

      const form = document.getElementById('f') as HTMLFormElement;
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await flushPromises();

      const recording = recorder.stop();
      const submitForms = recording.events.filter((e) => e.type === 'submitForm');
      expect(submitForms).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'inputText', text: 'hello' });
      expect(recording.events[1]).toMatchObject({ type: 'submitForm' });
    });

    it('does not duplicate submitForm when clicking a submit button triggers submit', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /><button id="s" type="submit">Go</button></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const button = document.getElementById('s') as HTMLButtonElement;
      button.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'click', index: expect.any(Number) });
    });

    it('records Enter on a button as click, not submitForm', async () => {
      document.body.innerHTML = '<button id="b">Go</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const button = document.getElementById('b') as HTMLButtonElement;
      button.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0].type).toBe('click');
    });

    it('does not record synthetic click on default submit button when Enter is pressed', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /><button id="s" type="submit">Go</button></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.value = 'hello';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));

      // Simulate the browser's synthetic click on the default submit button.
      const button = document.getElementById('s') as HTMLButtonElement;
      button.dispatchEvent(new Event('click', { bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      const clicks = recording.events.filter((e) => e.type === 'click');
      expect(clicks).toHaveLength(0);
      expect(recording.events).toHaveLength(2);
      expect(recording.events[0]).toMatchObject({ type: 'inputText', text: 'hello' });
      expect(recording.events[1]).toMatchObject({ type: 'submitForm' });
    });

    it('ignores submit events when not recording', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false, disableCrossPagePersistence: true });
      recorder.start();
      recorder.stop();

      const form = document.getElementById('f') as HTMLFormElement;
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('ignores direct handleSubmit calls when not recording', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      recorder.stop();

      const form = document.getElementById('f') as HTMLFormElement;
      await (recorder as unknown as { handleSubmit: (e: Event) => void }).handleSubmit({
        target: form,
      } as unknown as Event);
      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('does not duplicate submitForm when clicking an image submit input triggers submit', async () => {
      document.body.innerHTML = '<form id="f"><input id="q" /><input id="s" type="image" src="go.png" /></form>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const inputSubmit = document.getElementById('s') as HTMLInputElement;
      inputSubmit.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'click', index: expect.any(Number) });
    });

    it('drops queued events when recording is stopped before flush', async () => {
      document.body.innerHTML = '<button id="b">Go</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false, disableCrossPagePersistence: true });
      recorder.start();

      const button = document.getElementById('b') as HTMLButtonElement;
      button.click();
      recorder.stop();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('ignores non-Enter keydown events', async () => {
      document.body.innerHTML = '<input id="q" />';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('q') as HTMLInputElement;
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('ignores Enter keydown with no target', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      await (recorder as unknown as { handleKeydown: (e: KeyboardEvent) => Promise<void> }).handleKeydown({
        target: null,
        key: 'Enter',
      } as unknown as KeyboardEvent);
      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('does not record scroll when the position has not changed', async () => {
      document.body.innerHTML =
        '<div id="scroller" style="height:100px;overflow:auto;"><div style="height:500px;"></div></div>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const scroller = document.getElementById('scroller') as HTMLElement;
      Object.defineProperty(scroller, 'scrollHeight', { value: 500, configurable: true });
      scroller.scrollTop = 50;
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));
      vi.advanceTimersByTime(250);
      await flushPromises();

      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));
      vi.advanceTimersByTime(250);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
    });

    it('records horizontal scroll events', async () => {
      const originalScrollX = Object.getOwnPropertyDescriptor(window, 'scrollX');
      Object.defineProperty(window, 'scrollX', { value: 50, configurable: true });

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      document.dispatchEvent(new Event('scroll', { bubbles: true }));
      vi.advanceTimersByTime(250);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'scroll', direction: 'right', unit: 'pixels' });

      if (originalScrollX) {
        Object.defineProperty(window, 'scrollX', originalScrollX);
      } else {
        delete (window as unknown as Record<string, unknown>).scrollX;
      }
    });

    it('restarts the scroll debounce timer on rapid scroll events', async () => {
      document.body.innerHTML =
        '<div id="scroller" style="height:100px;overflow:auto;"><div style="height:500px;"></div></div>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const scroller = document.getElementById('scroller') as HTMLElement;
      Object.defineProperty(scroller, 'scrollHeight', { value: 500, configurable: true });
      scroller.scrollTop = 10;
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));
      scroller.scrollTop = 20;
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));
      vi.advanceTimersByTime(250);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
    });

    it('ignores scroll events when not recording', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      recorder.stop();
      await (recorder as unknown as { handleScroll: (e: Event) => void }).handleScroll({
        target: document.documentElement,
      } as unknown as Event);
    });

    it('records navigate events on hashchange', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      window.history.replaceState(null, '', '#recorded-navigation');
      window.dispatchEvent(new HashChangeEvent('hashchange'));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'navigate', url: window.location.href });
      window.history.replaceState(null, '', window.location.pathname);
    });

    it('ignores a late pageshow for the page where recording started', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      window.dispatchEvent(new Event('pageshow', { bubbles: false }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('deduplicates repeated navigate events for the same URL', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      window.history.replaceState(null, '', '#deduplicated-navigation');
      window.dispatchEvent(new HashChangeEvent('hashchange'));
      window.dispatchEvent(new Event('pageshow', { bubbles: false }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0]).toMatchObject({ type: 'navigate', url: window.location.href });
      window.history.replaceState(null, '', window.location.pathname);
    });

    it('does not capture snapshots when captureSnapshotBeforeEachEvent is false', async () => {
      document.body.innerHTML = `
        <button id="btn">Click</button>
        <input id="inp" />
        <select id="sel"><option>A</option><option selected>B</option></select>
        <div id="scroller" style="height:100px;overflow:auto;"><div style="height:500px;"></div></div>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      (document.getElementById('btn') as HTMLElement).click();

      const input = document.getElementById('inp') as HTMLInputElement;
      input.value = 'hello';
      input.dispatchEvent(new Event('input', { bubbles: true }));

      const select = document.getElementById('sel') as HTMLSelectElement;
      select.selectedIndex = 0;
      select.dispatchEvent(new Event('change', { bubbles: true }));

      const scroller = document.getElementById('scroller') as HTMLElement;
      scroller.scrollTop = 50;
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));

      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      window.dispatchEvent(new HashChangeEvent('hashchange'));

      vi.advanceTimersByTime(250);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events.length).toBeGreaterThan(0);
      expect(recording.snapshots).toHaveLength(0);
    });

    it('includes the root element in snapshots so window scroll events map to a valid index', async () => {
      const originalScrollY = Object.getOwnPropertyDescriptor(window, 'scrollY');
      const originalScrollX = Object.getOwnPropertyDescriptor(window, 'scrollX');
      Object.defineProperty(window, 'scrollY', { value: 100, configurable: true });
      Object.defineProperty(window, 'scrollX', { value: 0, configurable: true });

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      document.dispatchEvent(new Event('scroll', { bubbles: true }));
      vi.advanceTimersByTime(250);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      const event = recording.events[0] as { type: string; index: number };
      expect(event.type).toBe('scroll');
      expect(recording.snapshots).toHaveLength(1);
      const snapshot = recording.snapshots[0];
      expect(snapshot.selectorMap[event.index]).toBeDefined();
      expect(snapshot.selectorMap[event.index].tagName).toBe('html');

      if (originalScrollY) {
        Object.defineProperty(window, 'scrollY', originalScrollY);
      } else {
        delete (window as unknown as Record<string, unknown>).scrollY;
      }
      if (originalScrollX) {
        Object.defineProperty(window, 'scrollX', originalScrollX);
      } else {
        delete (window as unknown as Record<string, unknown>).scrollX;
      }
    });

    it('uses stable selectors for id, data-testid, classes and nth-of-type', async () => {
      document.body.innerHTML = `
        <button id="by-id">A</button>
        <button data-testid="by-testid">B</button>
        <button class="stable cls">C</button>
        <div><span></span><span role="button">D</span></div>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const byId = document.getElementById('by-id')!;
      const byTestId = document.querySelector('[data-testid="by-testid"]') as HTMLElement;
      const byClass = document.querySelector('.stable') as HTMLElement;
      const spans = Array.from(document.querySelectorAll('span'));

      byId.click();
      byTestId.click();
      byClass.click();
      spans[1].click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(4);
    });

    it('records clicks on scrollable elements', async () => {
      document.body.innerHTML =
        '<div id="scroller" style="height:100px;overflow:auto;"><div style="height:500px;"></div></div>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const scroller = document.getElementById('scroller') as HTMLElement;
      Object.defineProperty(scroller, 'scrollHeight', { value: 500, configurable: true });
      scroller.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(recording.events[0].type).toBe('click');
      expect(recording.snapshots).toHaveLength(1);
    });

    it('aggregates sub-frame DOM into the snapshot in the top frame', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const frameTree = {
        frameId: 0,
        url: window.location.href,
        domTree: { type: 'element' as const, tagName: 'html' },
        children: [],
      };
      const sendMessage = vi.fn().mockResolvedValue({ frameTree });
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      document.getElementById('btn')!.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.snapshots).toHaveLength(1);
      expect(recording.snapshots[0].domTree).toBeDefined();
      expect(sendMessage).toHaveBeenCalledWith({
        action: 'AGGREGATE_DOM',
        options: { maxDepth: 30, maxNodes: 2000, maxTextLength: 200 },
      });
    });

    it('falls back to local DOM when frame aggregation fails', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      (globalThis as Record<string, unknown>).chrome = {
        runtime: { sendMessage: vi.fn().mockRejectedValue(new Error('host disconnected')) },
      };

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      document.getElementById('btn')!.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.snapshots[0].domTree).toBeDefined();
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining('frame aggregation failed'));
      warnSpy.mockRestore();
    });

    it('falls back to local DOM when frame aggregation times out', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      (globalThis as Record<string, unknown>).chrome = {
        runtime: { sendMessage: vi.fn(() => new Promise(() => {})) },
      };

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      document.getElementById('btn')!.click();
      await flushPromises();

      vi.advanceTimersByTime(3000);
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.snapshots[0].domTree).toBeDefined();
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining('frame aggregation failed'));
      warnSpy.mockRestore();
    });

  });

  describe('event serialization', () => {
    it('preserves click order when the second snapshot resolves first', async () => {
      document.body.innerHTML = '<button id="a">A</button><button id="b">B</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      const deferreds: Array<{ resolve: () => void; promise: Promise<void> }> = [];
      vi.spyOn(recorder as unknown as { captureSnapshot: () => Promise<DomSnapshot> }, 'captureSnapshot').mockImplementation(
        () => {
          let resolveFn: () => void;
          const promise = new Promise<void>((resolve) => {
            resolveFn = resolve;
          });
          deferreds.push({ resolve: resolveFn!, promise });
          return promise.then(() => ({
            timestamp: Date.now(),
            url: window.location.href,
            selectorMap: {},
          })) as Promise<DomSnapshot>;
        },
      );

      const btnA = document.getElementById('a')!;
      const btnB = document.getElementById('b')!;

      vi.setSystemTime(new Date(1000));
      btnA.click();
      vi.setSystemTime(new Date(2000));
      btnB.click();

      // Resolve the second snapshot before the first to simulate out-of-order aggregation.
      deferreds[1].resolve();
      await flushPromises();
      deferreds[0].resolve();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(2);
      expect(recording.events[0].type).toBe('click');
      expect(recording.events[1].type).toBe('click');
      expect((recording.events[0] as { index: number }).index).toBeLessThan(
        (recording.events[1] as { index: number }).index,
      );
      expect(recording.events[0].timestamp).toBe(1000);
      expect(recording.events[1].timestamp).toBe(2000);
    });

    it('continues recording after a snapshot failure', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();

      let calls = 0;
      vi.spyOn(recorder as unknown as { captureSnapshot: () => Promise<DomSnapshot> }, 'captureSnapshot').mockImplementation(
        () => {
          calls++;
          if (calls === 1) {
            return Promise.reject(new Error('snapshot failed'));
          }
          return Promise.resolve({ timestamp: Date.now(), url: window.location.href, selectorMap: {} });
        },
      );

      const btn = document.getElementById('btn') as HTMLButtonElement;
      btn.click();
      btn.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(1);
      expect(warnSpy).toHaveBeenCalledWith('[ContentRecorder] event task failed:', expect.any(Error));
      warnSpy.mockRestore();
    });
  });

  describe('sub-frame guard', () => {
    it('uses only the local DOM for snapshots in a sub-frame', async () => {
      const originalTop = window.top;
      Object.defineProperty(window, 'top', { value: { self: {} }, configurable: true });
      vi.resetModules();
      try {
        const mod = await import('../content');
        const recorder = new mod.ContentRecorder({ captureSnapshotBeforeEachEvent: true });
        const recording: PageAgentRecording = {
          version: '1.0.0',
          meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
          events: [],
          snapshots: [],
        };
        (recorder as unknown as { recording: PageAgentRecording }).recording = recording;
        const snapshot = await (recorder as unknown as { captureSnapshot: () => Promise<DomSnapshot> }).captureSnapshot();
        expect(snapshot.domTree).toBeDefined();
        expect(recording.snapshots).toHaveLength(1);
      } finally {
        Object.defineProperty(window, 'top', { value: originalTop, configurable: true });
      }
    });
  });

  describe('Chrome messaging', () => {
    it('exports, imports, reconciles, and discards an in-progress v2 handoff', async () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
          sendMessage: vi.fn().mockResolvedValue({ success: true }),
        },
      };
      document.body.innerHTML = '<button id="recorded">Record</button>';
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', captureSnapshotBeforeEachEvent: false });
      recorder.attachChromeMessaging();
      const sendResponse = vi.fn();

      listeners[0]({
        action: 'START_RECORDING',
        payload: { protocolVersion: '2.0.0', captureSnapshotBeforeEachEvent: false },
      }, {}, sendResponse);
      await flushPromises();
      document.getElementById('recorded')!.click();
      await flushPromises();

      listeners[0]({ action: 'EXPORT_RECORDING_HANDOFF' }, {}, sendResponse);
      await flushPromises();
      const exported = sendResponse.mock.calls.at(-1)?.[0] as {
        success: boolean;
        state: { lastRecordedUrl: string; recording: PageAgentRecording };
      };
      expect(exported.success).toBe(true);
      expect(exported.state.recording.events).toHaveLength(1);

      exported.state.lastRecordedUrl = 'https://source.example/results';
      listeners[0]({ action: 'IMPORT_RECORDING_HANDOFF', payload: exported.state }, {}, sendResponse);
      await flushPromises();
      const imported = sendResponse.mock.calls.at(-1)?.[0] as {
        success: boolean;
        state: { recording: PageAgentRecording; lastRecordedUrl: string };
      };
      expect(imported.success).toBe(true);
      expect(imported.state.recording.events.map((event) => event.type)).toEqual(['click', 'navigate']);
      expect(imported.state.lastRecordedUrl).toBe(window.location.href);

      listeners[0]({ action: 'DISCARD_RECORDING_HANDOFF' }, {}, sendResponse);
      expect(recorder.isRecording()).toBe(false);
      expect(sendResponse).toHaveBeenLastCalledWith({ success: true, active: false });
    });

    it('starts recording on START_RECORDING and returns recording on STOP_RECORDING', async () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
        },
      };

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.attachChromeMessaging();

      expect(listeners).toHaveLength(1);

      const sendResponse = vi.fn();
      const handledStart = listeners[0]({ action: 'START_RECORDING' }, {}, sendResponse);
      expect(handledStart).toBe(true);
      expect(recorder.isRecording()).toBe(true);
      expect(sendResponse).toHaveBeenCalledWith({ success: true });

      const handledStop = listeners[0]({ action: 'STOP_RECORDING' }, {}, sendResponse);
      expect(handledStop).toBe(true);
      await flushPromises();
      expect(recorder.isRecording()).toBe(false);
      const lastCall = sendResponse.mock.calls[sendResponse.mock.calls.length - 1][0] as {
        recording: { version: string; events: unknown[] };
      };
      expect(lastCall.recording.version).toBe('1.0.0');
      expect(lastCall.recording.events).toEqual([]);
    });

    it('returns a valid recording when final snapshot capture fails during STOP_RECORDING', async () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
        },
      };

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.attachChromeMessaging();

      const sendResponse = vi.fn();
      listeners[0]({ action: 'START_RECORDING' }, {}, sendResponse);
      expect(recorder.isRecording()).toBe(true);

      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      vi.spyOn(recorder as unknown as { captureSnapshot: () => Promise<DomSnapshot> }, 'captureSnapshot').mockRejectedValue(
        new Error('snapshot failed'),
      );

      const handledStop = listeners[0]({ action: 'STOP_RECORDING' }, {}, sendResponse);
      expect(handledStop).toBe(true);

      await flushPromises();

      expect(recorder.isRecording()).toBe(false);
      expect(warnSpy).toHaveBeenCalledWith('[ContentRecorder] final snapshot failed:', expect.any(Error));
      const lastCall = sendResponse.mock.calls[sendResponse.mock.calls.length - 1][0] as {
        recording: { version: string; events: unknown[]; snapshots: unknown[] };
      };
      expect(lastCall.recording.version).toBe('1.0.0');
      expect(lastCall.recording.events).toEqual([]);
      expect(lastCall.recording.snapshots).toEqual([]);

      warnSpy.mockRestore();
    });

    it('drains in-flight events before capturing the final STOP_RECORDING snapshot', async () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
          sendMessage: vi.fn().mockResolvedValue(null),
        },
      };

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.attachChromeMessaging();

      const sendResponse = vi.fn();
      listeners[0]({ action: 'START_RECORDING' }, {}, sendResponse);

      document.body.innerHTML = '<button id="btn">Click me</button>';
      const btn = document.getElementById('btn') as HTMLButtonElement;

      vi.setSystemTime(new Date(1000));
      btn.click();

      vi.setSystemTime(new Date(2000));
      const handledStop = listeners[0]({ action: 'STOP_RECORDING' }, {}, sendResponse);
      expect(handledStop).toBe(true);

      await flushPromises();

      const lastCall = sendResponse.mock.calls[sendResponse.mock.calls.length - 1][0] as {
        recording: PageAgentRecordingType;
      };
      const recording = lastCall.recording;

      expect(recording.events).toHaveLength(1);
      expect(recording.events[0].type).toBe('click');
      expect(recording.snapshots).toHaveLength(2);

      const eventIndex = (recording.events[0] as { index: number }).index;
      expect(recording.snapshots[0].selectorMap[eventIndex]).toMatchObject({
        tagName: 'button',
        selector: '#btn',
      });
      expect(recording.events[0].timestamp).toBe(1000);
      expect(recording.snapshots[0].timestamp).toBe(1000);
      expect(recording.snapshots[1].timestamp).toBe(2000);
      expect(recording.snapshots[0].timestamp).toBeLessThan(recording.snapshots[1].timestamp);
    });

    it('waits for the shared v2 checkpoint restore before reporting recording readiness', async () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      let release!: (value: unknown) => void;
      const delayed = new Promise<unknown>((resolve) => {
        release = resolve;
      });
      const sendMessage = vi.fn(() => delayed);
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
          sendMessage,
        },
      };
      const checkpoint = {
        version: 1,
        recordingFlag: true,
        lastRecordedUrl: 'https://previous.example/entry',
        recording: {
          version: '2.0.0',
          meta: {
            startUrl: window.location.href,
            title: 'restored before next action',
            recordedAt: new Date().toISOString(),
            domain: window.location.hostname,
            semanticDomVersion: '1',
            sanitizationVersion: 'extension-v2',
          },
          limits: { maxActions: 10, maxDurationMs: 60_000, maxBytes: 100_000, warningThreshold: 0.8 },
          warnings: [],
          events: [{ type: 'click', index: 4, timestamp: 2 }],
          snapshots: [{
            timestamp: 1,
            url: window.location.href,
            selectorMap: {},
            domTree: { type: 'element' as const, tagName: 'html' },
            phase: 'initial' as const,
            sequence: 0,
            capture: {
              status: 'complete' as const,
              nodeCount: 1,
              redactionCount: 0,
              removedNodeCount: 0,
              frames: [],
            },
          }],
        } as PageAgentRecording,
      };

      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.attachChromeMessaging();
      const bootstrapRestore = recorder.resumeFromBackgroundIfNeeded();
      const sendResponse = vi.fn();
      const handled = listeners[0]({ action: 'ENSURE_RECORDING_READY' }, {}, sendResponse);
      expect(handled).toBe(true);
      await flushPromises();
      expect(sendResponse).not.toHaveBeenCalled();
      expect(sendMessage).toHaveBeenCalledTimes(1);

      release({ active: true, state: checkpoint });
      await expect(bootstrapRestore).resolves.toBe(true);
      await flushPromises();
      expect(sendResponse).toHaveBeenCalledWith({
        success: true,
        active: true,
        protocolVersion: '2.0.0',
      });
      expect(recorder.isRecording()).toBe(true);
      const recording = await recorder.stopAsync();
      expect(recording.events).toEqual([
        { type: 'click', index: 4, timestamp: 2 },
        expect.objectContaining({ type: 'navigate', url: window.location.href }),
      ]);
    });

    it('accepts options from START_RECORDING payload', () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
        },
      };

      const recorder = new ContentRecorder();
      recorder.attachChromeMessaging();

      const options: RecorderOptions = { captureSnapshotBeforeEachEvent: false };
      listeners[0]({ action: 'START_RECORDING', payload: options }, {}, vi.fn());
      expect(recorder.isRecording()).toBe(true);
    });

    it('ignores unknown actions', () => {
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
        },
      };

      const recorder = new ContentRecorder();
      recorder.attachChromeMessaging();

      const sendResponse = vi.fn();
      const handled = listeners[0]({ action: 'UNKNOWN' }, {}, sendResponse);
      expect(handled).toBe(false);
      expect(sendResponse).not.toHaveBeenCalled();
    });

    it('responds to CAPTURE_DOM with a serialized local DOM', () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const listeners: Array<(message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean> = [];
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn((fn) => listeners.push(fn)),
            removeListener: vi.fn(),
          },
        },
      };

      const recorder = new ContentRecorder();
      recorder.attachChromeMessaging();

      const sendResponse = vi.fn();
      const handled = listeners[0]({ action: 'CAPTURE_DOM' }, {}, sendResponse);
      expect(handled).toBe(true);
      const response = sendResponse.mock.calls[0][0] as { domTree?: { tagName?: string } };
      expect(response.domTree).toBeDefined();
      expect(response.domTree!.tagName).toBe('html');
    });

    it('does not start DOM listeners inside a sub-frame', async () => {
      const originalTop = window.top;
      Object.defineProperty(window, 'top', { value: { self: {} }, configurable: true });
      vi.resetModules();
      try {
        const mod = await import('../content');
        const recorder = new mod.ContentRecorder({ captureSnapshotBeforeEachEvent: false });
        recorder.start();
        expect(recorder.isRecording()).toBe(false);
      } finally {
        Object.defineProperty(window, 'top', { value: originalTop, configurable: true });
      }
    });

    it('warns when bootstrap chrome messaging attachment throws', async () => {
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn(() => {
              throw new Error('attach failed');
            }),
            removeListener: vi.fn(),
          },
        },
      };
      vi.resetModules();
      await import('../content');
      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining('failed to attach chrome messaging'),
        expect.any(Error),
      );
      warnSpy.mockRestore();
    });
  });

  describe('selector escaping', () => {
    function inferSelector(recorder: ContentRecorder, el: Element): string {
      return (recorder as unknown as { inferSelector: (element: Element) => string }).inferSelector(el);
    }

    it('escapes IDs containing dots, colons and spaces', () => {
      document.body.innerHTML = `
        <button id="a.b">A</button>
        <button id="a:b">B</button>
        <button id="a b">C</button>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const buttons = Array.from(document.querySelectorAll('button'));
      expect(inferSelector(recorder, buttons[0])).toBe('#a\\2E b');
      expect(inferSelector(recorder, buttons[1])).toBe('#a\\3A b');
      expect(inferSelector(recorder, buttons[2])).toBe('#a\\20 b');
    });

    it('escapes data-testid values containing quotes and brackets', () => {
      document.body.innerHTML = `
        <button data-testid='a"b'>A</button>
        <button data-testid="a[b]">B</button>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const buttons = Array.from(document.querySelectorAll('button'));
      expect(inferSelector(recorder, buttons[0])).toBe('[data-testid="a\\22 b"]');
      expect(inferSelector(recorder, buttons[1])).toBe('[data-testid="a\\5B b\\5D "]');
    });

    it('escapes class names containing special characters', () => {
      document.body.innerHTML = '<button class="a.b c:d">A</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const button = document.querySelector('button')!;
      expect(inferSelector(recorder, button)).toBe('button.a\\2E b.c\\3A d');
    });

    it('produces valid selectors that querySelector can resolve', () => {
      document.body.innerHTML = `
        <button id="weird.id">A</button>
        <button data-testid="weird:test">B</button>
        <button class="weird class">C</button>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const buttons = Array.from(document.querySelectorAll('button'));
      for (const button of buttons) {
        const selector = inferSelector(recorder, button);
        expect(document.querySelector(selector)).toBe(button);
      }
    });
  });

  describe('resource limits', () => {
    it('stops recording and warns when maxEvents is reached', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false, maxEvents: 3 });
      recorder.start();

      const btn = document.getElementById('btn') as HTMLButtonElement;
      btn.click();
      btn.click();
      await flushPromises();
      expect(recorder.isRecording()).toBe(true);

      btn.click();
      await flushPromises();
      expect(recorder.isRecording()).toBe(false);

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(3);
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining('maxEvents (3) reached'));
      warnSpy.mockRestore();
    });

    it('drops oldest snapshots FIFO when maxSnapshots is exceeded', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true, maxSnapshots: 2 });
      recorder.start();

      const btn = document.getElementById('btn') as HTMLButtonElement;
      btn.click();
      await flushPromises();
      vi.advanceTimersByTime(1);
      btn.click();
      await flushPromises();
      vi.advanceTimersByTime(1);
      btn.click();
      await flushPromises();

      const recording = recorder.stop();
      expect(recording.snapshots).toHaveLength(2);
      expect(recording.snapshots[0].timestamp).toBeLessThan(recording.snapshots[1].timestamp);
    });

    it('removes selector index entries that exceed selectorIndexTTLMs', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true, selectorIndexTTLMs: 100 });
      recorder.start();

      const btn = document.getElementById('btn') as HTMLButtonElement;
      btn.click();
      await flushPromises();

      const recorderInternals = recorder as unknown as { selectorToIndex: Map<string, unknown> };
      expect(recorderInternals.selectorToIndex.size).toBeGreaterThan(0);

      vi.advanceTimersByTime(2000);

      expect(recorderInternals.selectorToIndex.size).toBe(0);

      btn.click();
      await flushPromises();
      const recording = recorder.stop();
      expect(recording.events).toHaveLength(2);
    });
  });

  describe('internal helpers and edge cases', () => {
    afterEach(() => {
      (globalThis as Record<string, unknown>).CSS = (globalThis as Record<string, unknown>).__originalCSS;
    });

    beforeAll(() => {
      (globalThis as Record<string, unknown>).__originalCSS = (globalThis as Record<string, unknown>).CSS;
    });

    afterAll(() => {
      delete (globalThis as Record<string, unknown>).__originalCSS;
    });

    it('uses CSS.escape when available', () => {
      (globalThis as Record<string, unknown>).CSS = { escape: (value: string) => `[escaped:${value}]` };
      document.body.innerHTML = '<button id="a.b">A</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const infer = (recorder as unknown as { inferSelector: (el: Element) => string }).inferSelector;
      expect(infer(document.querySelector('button')!)).toBe('#[escaped:a.b]');
    });

    it('falls back to manual CSS escaping when CSS.escape is unavailable', () => {
      (globalThis as Record<string, unknown>).CSS = undefined;
      document.body.innerHTML = '<button>A</button><button>B</button><button>C</button><button>D</button><button>E</button><button>F</button>';
      const buttons = Array.from(document.querySelectorAll('button'));
      buttons[0].setAttribute('id', 'a.b');
      buttons[1].setAttribute('id', '1bad');
      buttons[2].setAttribute('id', '-');
      buttons[3].setAttribute('id', '-2x');
      buttons[4].setAttribute('data-testid', '\x00bad');
      buttons[5].setAttribute('data-testid', '\x01bad');

      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const infer = (recorder as unknown as { inferSelector: (el: Element) => string }).inferSelector;
      expect(infer(buttons[0])).toBe('#a\\2E b');
      expect(infer(buttons[1])).toBe('#\\31 bad');
      expect(infer(buttons[2])).toBe('#\\2D ');
      expect(infer(buttons[3])).toBe('#-\\32 x');
      expect(infer(buttons[4])).toBe('[data-testid="\uFFFDbad"]');
      expect(infer(buttons[5])).toBe('[data-testid="\\01 bad"]');
    });

    it('does not start recording on restricted URLs', () => {
      const originalLocation = window.location;
      Object.defineProperty(window, 'location', { value: { href: 'chrome://settings' }, configurable: true });
      try {
        const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
        recorder.start();
        expect(recorder.isRecording()).toBe(false);
      } finally {
        Object.defineProperty(window, 'location', { value: originalLocation, configurable: true });
      }
    });

    it('caps recording size by keeping the last snapshot when sufficient', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      const recording = (recorder as unknown as { recording: PageAgentRecordingType }).recording;
      recording.snapshots = [
        { timestamp: 1, url: 'http://a', selectorMap: {}, domTree: { type: 'element', tagName: 'html', text: 'a'.repeat(5000) } },
        { timestamp: 2, url: 'http://a', selectorMap: {}, domTree: { type: 'element', tagName: 'html' } },
      ];
      const capped = (recorder as unknown as { capRecordingSize: (r: PageAgentRecordingType, max: number) => PageAgentRecordingType }).capRecordingSize(recording, 3000);
      expect(capped.snapshots[0].domTree).toBeUndefined();
      expect(capped.snapshots[1].domTree).toBeDefined();
    });

    it('drops all domTrees as a last resort when capping size', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      const recording = (recorder as unknown as { recording: PageAgentRecordingType }).recording;
      recording.snapshots = [
        { timestamp: 1, url: 'http://a', selectorMap: {}, domTree: { type: 'element', tagName: 'html', text: 'a'.repeat(5000) } },
        { timestamp: 2, url: 'http://a', selectorMap: {}, domTree: { type: 'element', tagName: 'html', text: 'b'.repeat(5000) } },
      ];
      const capped = (recorder as unknown as { capRecordingSize: (r: PageAgentRecordingType, max: number) => PageAgentRecordingType }).capRecordingSize(recording, 1000);
      expect(capped.snapshots[0].domTree).toBeUndefined();
      expect(capped.snapshots[1].domTree).toBeUndefined();
    });

    it('clears pending input and scroll timeouts on stop', () => {
      document.body.innerHTML = `
        <input id="inp" />
        <div id="scroller" style="height:100px;overflow:auto;"><div style="height:500px;"></div></div>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();

      const input = document.getElementById('inp') as HTMLInputElement;
      input.value = 'x';
      input.dispatchEvent(new Event('input', { bubbles: true }));

      const scroller = document.getElementById('scroller') as HTMLElement;
      Object.defineProperty(scroller, 'scrollHeight', { value: 500, configurable: true });
      scroller.scrollTop = 10;
      scroller.dispatchEvent(new Event('scroll', { bubbles: true }));

      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('does not start selector cleanup when TTL is zero or negative', () => {
      const recorder = new ContentRecorder({ selectorIndexTTLMs: 0 });
      recorder.start();
      expect((recorder as unknown as { selectorCleanupInterval: unknown }).selectorCleanupInterval).toBeNull();
      recorder.stop();
    });

    it('keeps selector index entries that have not expired', async () => {
      document.body.innerHTML = '<button id="btn">Click me</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true, selectorIndexTTLMs: 1000 });
      recorder.start();

      const btn = document.getElementById('btn') as HTMLButtonElement;
      btn.click();
      await flushPromises();

      vi.advanceTimersByTime(100);
      const internals = recorder as unknown as { selectorToIndex: Map<string, unknown> };
      expect(internals.selectorToIndex.size).toBeGreaterThan(0);
      recorder.stop();
    });

    it('classifies element visibility correctly', () => {
      document.body.innerHTML = '<button id="visible">V</button><button id="hidden" hidden>H</button>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const isVisible = (recorder as unknown as { isVisible: (el: Element) => boolean }).isVisible;
      expect(isVisible(document.getElementById('visible')!)).toBe(true);
      expect(isVisible(document.getElementById('hidden')!)).toBe(false);

      const detached = document.createElement('button');
      expect(isVisible(detached)).toBe(false);
    });

    it('classifies scrollability correctly', () => {
      document.body.innerHTML = '<div id="scrollable" style="overflow:auto;height:10px;">x</div><div id="plain">y</div>';
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const isScrollable = (recorder as unknown as { isScrollable: (el: Element) => boolean }).isScrollable;

      const scrollable = document.getElementById('scrollable') as HTMLElement;
      Object.defineProperty(scrollable, 'scrollHeight', { value: 100, configurable: true });
      expect(isScrollable(scrollable)).toBe(true);
      expect(isScrollable(document.getElementById('plain')!)).toBe(false);
      expect(isScrollable(document.documentElement)).toBe(false);
    });

    it('classifies interactability correctly', () => {
      document.body.innerHTML = `
        <button id="btn">A</button>
        <div id="onclick" onclick="void 0">B</div>
        <div id="editable" contenteditable="true">C</div>
        <div id="none">D</div>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const isInteractable = (recorder as unknown as { isInteractable: (el: Element) => boolean }).isInteractable;
      expect(isInteractable.call(recorder, document.getElementById('btn')!)).toBe(true);
      expect(isInteractable.call(recorder, document.getElementById('onclick')!)).toBe(true);
      expect(isInteractable.call(recorder, document.getElementById('editable')!)).toBe(true);
      expect(isInteractable.call(recorder, document.getElementById('none')!)).toBe(false);
    });

    it('detects display:none and visibility:hidden elements as not visible', () => {
      document.body.innerHTML = `
        <div id="display-none" style="display:none;">A</div>
        <div id="visibility-hidden" style="visibility:hidden;">B</div>
      `;
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const isVisible = (recorder as unknown as { isVisible: (el: Element) => boolean }).isVisible;
      expect(isVisible(document.getElementById('display-none')!)).toBe(false);
      expect(isVisible(document.getElementById('visibility-hidden')!)).toBe(false);
    });

    it('ignores clicks with no target', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      await (recorder as unknown as { handleClick: (e: Event) => Promise<void> }).handleClick({
        target: null,
      } as unknown as Event);
      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('short-circuits event handlers after recording has stopped', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: true });
      recorder.start();
      recorder.stop();
      const internals = recorder as unknown as {
        handleClick: (e: Event) => Promise<void>;
        handleInput: (e: Event) => void;
        handleChange: (e: Event) => Promise<void>;
        handleKeydown: (e: KeyboardEvent) => Promise<void>;
        handleNavigate: () => Promise<void>;
      };
      await internals.handleClick({ target: document.createElement('button') } as unknown as Event);
      internals.handleInput({ target: document.createElement('input') } as unknown as Event);
      await internals.handleChange({ target: document.createElement('select') } as unknown as Event);
      await internals.handleKeydown({ target: null, key: 'Enter' } as unknown as KeyboardEvent);
      await internals.handleNavigate();
    });

    it('ignores input events on select elements', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      const internals = recorder as unknown as { handleInput: (e: Event) => void };
      internals.handleInput({ target: document.createElement('select') } as unknown as Event);
      expect(recorder.stop().events).toHaveLength(0);
    });

    it('detaches the chrome messaging listener', () => {
      const removeListener = vi.fn();
      (globalThis as Record<string, unknown>).chrome = {
        runtime: {
          onMessage: {
            addListener: vi.fn(),
            removeListener,
          },
        },
      };
      const recorder = new ContentRecorder();
      recorder.attachChromeMessaging();
      recorder.detachChromeMessaging();
      expect(removeListener).toHaveBeenCalledTimes(1);
    });

    it('withTimeout resolves when the main promise wins', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const withTimeout = (recorder as unknown as { withTimeout: <T>(p: Promise<T>, ms: number, msg: string) => Promise<T> }).withTimeout;
      const result = await withTimeout(Promise.resolve('ok'), 1000, 'should not fire');
      expect(result).toBe('ok');
    });

    it('withTimeout rejects with the timeout message when the timer fires first', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const withTimeout = (recorder as unknown as { withTimeout: <T>(p: Promise<T>, ms: number, msg: string) => Promise<T> }).withTimeout;
      const promise = withTimeout(new Promise(() => {}), 50, 'timed out');
      vi.advanceTimersByTime(50);
      await expect(promise).rejects.toThrow('timed out');
    });

    it('withTimeout does not re-throw the main promise after the timeout has fired', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      const withTimeout = (recorder as unknown as { withTimeout: <T>(p: Promise<T>, ms: number, msg: string) => Promise<T> }).withTimeout;

      let rejectMain: (reason: Error) => void;
      const main = new Promise<void>((_, reject) => {
        rejectMain = reject;
      });

      const promise = withTimeout(main, 50, 'timeout won');
      vi.advanceTimersByTime(50);
      await expect(promise).rejects.toThrow('timeout won');

      // Rejecting the main promise after the timeout must not produce an unhandled rejection.
      rejectMain!(new Error('late rejection'));
      await flushPromises();
    });
  });

  describe('semantic recording v2', () => {
    it('captures initial, pre-action, and final snapshots without exposing sensitive values', async () => {
      document.body.innerHTML = `
        <input id="password" type="password" value="plain-secret">
        <p>alice@example.com token=live-secret</p>
        <script>window.steal = 'secret'</script>
        <button id="collect">Collect</button>
      `;
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        maxEvents: 10,
        maxRecordingBytes: 1024 * 1024,
      });

      recorder.start();
      await flushPromises();
      document.getElementById('collect')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const recording = await recorder.stopAsync();

      expect(recording.version).toBe('2.0.0');
      expect(recording.meta.sanitizationVersion).toBe('extension-v2');
      expect(recording.snapshots.map((snapshot) => snapshot.phase)).toEqual([
        'initial',
        'before-action',
        'final',
      ]);
      expect(recording.snapshots.map((snapshot) => snapshot.sequence)).toEqual([0, 1, 2]);
      expect(recording.snapshots[1].actionIndex).toBe(0);
      expect(recording.termination).toMatchObject({ reason: 'user', complete: true });
      const serialized = JSON.stringify(recording);
      expect(serialized).not.toContain('plain-secret');
      expect(serialized).not.toContain('alice@example.com');
      expect(serialized).not.toContain('live-secret');
      expect(serialized).not.toContain('window.steal');
      expect(serialized).toContain('[REDACTED]');
      expect(serialized).toContain('"contentOmitted":true');
    });

    it('serializes the pre-action click DOM before page handlers mutate it', async () => {
      document.body.innerHTML = '<p id="state">before click</p><button id="mutate">Mutate</button>';
      document.getElementById('mutate')!.addEventListener('click', () => {
        document.getElementById('state')!.textContent = 'after click';
      });
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();

      document.getElementById('mutate')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const recording = await recorder.stopAsync();
      const initial = recording.snapshots.find((snapshot) => snapshot.phase === 'initial');
      const beforeAction = recording.snapshots.find((snapshot) => snapshot.phase === 'before-action');
      const final = recording.snapshots.find((snapshot) => snapshot.phase === 'final');

      // The page is identical between initial and the pre-action capture, so
      // the before-action snapshot is stored as a reference — resolving it
      // must still yield the provably pre-mutation DOM.
      expect(beforeAction?.ref).toBe(initial?.sequence);
      expect(beforeAction?.actionIndex).toBe(0);
      const resolved = resolveSnapshot(recording, beforeAction!);
      expect(JSON.stringify(resolved?.domTree)).toContain('before click');
      expect(JSON.stringify(resolved?.domTree)).not.toContain('after click');
      expect(final?.ref).toBeUndefined();
      expect(JSON.stringify(final?.domTree)).toContain('after click');
    });

    it('records the exact root frame audit on a synchronous navigation snapshot', async () => {
      document.body.innerHTML = '<a id="next" href="/next">Next</a>';
      document.getElementById('next')!.addEventListener('click', (event) => event.preventDefault());
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();

      document.getElementById('next')!.dispatchEvent(new MouseEvent('click', {
        bubbles: true,
        cancelable: true,
      }));
      const recording = await recorder.stopAsync();
      const beforeAction = recording.snapshots.find((snapshot) => snapshot.phase === 'before-action');

      expect(beforeAction?.capture?.frames).toEqual([{
        frameId: 0,
        parentFrameId: -1,
        url: window.location.href,
        status: 'captured',
      }]);
    });

    it('links rapid actions to distinct event indexes', async () => {
      document.body.innerHTML = '<button id="first">First</button><button id="second">Second</button>';
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();

      document.getElementById('first')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      document.getElementById('second')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const recording = await recorder.stopAsync();

      const beforeActions = recording.snapshots.filter((snapshot) => snapshot.phase === 'before-action');
      expect(recording.events).toHaveLength(2);
      expect(beforeActions.map((snapshot) => snapshot.actionIndex)).toEqual([0, 1]);
      expect(recording.snapshots.map((snapshot) => snapshot.sequence)).toEqual([0, 1, 2, 3]);
    });

    it('collapses identical adjacent snapshots into references and keeps per-action identity', async () => {
      document.body.innerHTML = '<ul>' + '<li>item</li>'.repeat(40) + '</ul><button id="one">One</button><button id="two">Two</button>';
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();

      document.getElementById('one')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      document.getElementById('two')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const recording = await recorder.stopAsync();

      // initial + two pre-action snapshots are identical page states:
      // [full, ref, ref], then the final snapshot is also identical -> ref.
      expect(recording.snapshots).toHaveLength(4);
      const [initial, refA, refB, finalRef] = recording.snapshots;
      expect(initial.ref).toBeUndefined();
      expect(initial.domTree).toBeDefined();
      expect(refA.ref).toBe(initial.sequence);
      expect(refA.phase).toBe('before-action');
      expect(refA.actionIndex).toBe(0);
      expect(refA.domTree).toBeUndefined();
      expect(refB.ref).toBe(initial.sequence);
      expect(refB.actionIndex).toBe(1);
      expect(finalRef.ref).toBe(initial.sequence);
      expect(finalRef.phase).toBe('final');
      // Reference snapshots are placeholders: the payload is stored once.
      // (Real full snapshots are ~1MB+ where this is a >99% cut; the jsdom
      // fixture page is only ~6KB, so the placeholder's own fixed fields
      // dominate — bound accordingly.)
      const bytes = (value: unknown) => new TextEncoder().encode(JSON.stringify(value ?? null)).length;
      expect(bytes(refA)).toBeLessThan(bytes(initial) / 40);
      const totalBytes = recording.snapshots.reduce((sum, snapshot) => sum + bytes(snapshot), 0);
      expect(totalBytes).toBeLessThan(bytes(initial) * 2.5);
    });

    it('keeps a full snapshot when the page content changes between actions', async () => {
      document.body.innerHTML = '<p id="state">v1</p><button id="mut">Mutate</button>';
      document.getElementById('mut')!.addEventListener('click', () => {
        document.getElementById('state')!.textContent = 'v2';
      });
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();

      document.getElementById('mut')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const recording = await recorder.stopAsync();

      // initial vs pre-action are identical (capture precedes the handler) —
      // the pre-action snapshot references it — but the mutated final page
      // must stay a full snapshot or the mutation evidence is lost.
      const initial = recording.snapshots.find((snapshot) => snapshot.phase === 'initial');
      const beforeAction = recording.snapshots.find((snapshot) => snapshot.phase === 'before-action');
      const final = recording.snapshots.find((snapshot) => snapshot.phase === 'final');
      expect(beforeAction?.ref).toBe(initial?.sequence);
      expect(final?.ref).toBeUndefined();
      expect(JSON.stringify(final?.domTree)).toContain('v2');
    });

    it('seeds the dedup baseline from a restored recording containing references', async () => {
      document.body.innerHTML = '<button id="go">Go</button>';
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();
      document.getElementById('go')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const first = await recorder.stopAsync();
      // stop() reset the recorder; restart from the finished recording the
      // way the resume path does and capture once more on the same page.
      const resumed = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      const internals = resumed as unknown as {
        recording: typeof first;
        recordingFlag: boolean;
        lastRecordedUrl: string;
        wrapRecordingArrays(recording: typeof first): void;
      };
      internals.recording = JSON.parse(JSON.stringify(first));
      internals.recordingFlag = true;
      internals.lastRecordedUrl = first.meta.startUrl;
      // Resume wraps this.recording's own arrays (never a throwaway copy) and
      // re-seeds the dedup baseline from the stored snapshots.
      internals.wrapRecordingArrays(internals.recording);
      // Push a fresh capture of the same page through the wrapped push: all
      // content fields (url, selectorMap, domTree, capture) copied from the
      // initial snapshot, only positional fields differ.
      const snapshot = {
        ...JSON.parse(JSON.stringify(first.snapshots[0])),
        timestamp: Date.now(),
        phase: 'final' as const,
        sequence: 99,
      };
      internals.recording.snapshots.push(snapshot);
      const last = internals.recording.snapshots[internals.recording.snapshots.length - 1];
      // Same page content as the referenced baseline -> the restored push
      // must collapse to a reference, proving the baseline was re-seeded.
      expect(last.ref).toBe(first.snapshots[0].sequence);
      expect(last.domTree).toBeUndefined();
    });

    it('marks an iframe snapshot partial when frame aggregation is unavailable', async () => {
      document.body.innerHTML = '<iframe src="https://other.example/frame"></iframe>';
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      const recording = await recorder.stopAsync();
      const initial = recording.snapshots.find((snapshot) => snapshot.phase === 'initial');

      expect(initial?.capture?.status).toBe('partial');
      expect(JSON.stringify(initial?.domTree)).toContain('framePlaceholder');
      expect(JSON.stringify(initial?.domTree)).toContain('unavailable');
    });

    it('supports synchronous v2 stop and preserves negotiated default limits', async () => {
      document.body.innerHTML = '<p>sync stop</p>';
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      const recording = recorder.stop();

      expect(recording.limits).toEqual({
        maxActions: 500,
        maxDurationMs: 7_200_000,
        maxBytes: 20 * 1024 * 1024,
        warningThreshold: 0.8,
      });
      expect(recording.snapshots.at(-1)?.phase).toBe('final');
      expect(recording.termination).toMatchObject({ reason: 'user', complete: true });
    });

    it('returns safe recordings when async stop has not started or is legacy', async () => {
      const neverStarted = new ContentRecorder({ protocolVersion: '2.0.0' });
      await expect(neverStarted.stopAsync()).resolves.toMatchObject({ version: '2.0.0', events: [] });

      const legacy = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      legacy.start();
      await expect(legacy.stopAsync()).resolves.toMatchObject({ version: '1.0.0' });
    });

    it('coalesces concurrent async stop requests', async () => {
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      let release!: () => void;
      const deferred = new Promise<void>((resolve) => { release = resolve; });
      (recorder as unknown as { eventQueue: Promise<void> }).eventQueue = deferred;

      const first = recorder.stopAsync('user', 'first stop');
      const second = recorder.stopAsync('size-limit', 'second stop');
      release();
      const [a, b] = await Promise.all([first, second]);

      expect(a).toEqual(b);
      expect(a.termination?.message).toBe('first stop');
    });

    it('marks final capture failures incomplete for Error and non-Error causes', async () => {
      for (const failure of [new Error('final failed'), 'final failed']) {
        const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
        recorder.start();
        await flushPromises();
        vi.spyOn(
          recorder as unknown as { captureSnapshot: () => Promise<DomSnapshot> },
          'captureSnapshot',
        ).mockRejectedValueOnce(failure);

        const recording = await recorder.stopAsync();
        expect(recording.termination).toMatchObject({ reason: 'capture-error', complete: false });
        expect(recording.termination?.message).toBe(
          failure instanceof Error ? 'final failed' : 'final snapshot capture failed',
        );
      }
    });

    it('handles v2 checkpoints and status delivery without leaking runtime failures', async () => {
      const sendMessage = vi.fn().mockRejectedValue(new Error('worker restarted'));
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        maxRecordingBytes: 100_000,
        warningThreshold: 0.001,
      });
      recorder.start();
      await flushPromises();

      expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({ action: 'RECORDING_CHECKPOINT' }));
      expect(sendMessage).toHaveBeenCalledWith(expect.objectContaining({ action: 'RECORDING_STATUS' }));
      const recording = await recorder.stopAsync();
      expect(recording.version).toBe('2.0.0');

      const internals = recorder as unknown as {
        checkpointToBackground: () => void;
        notifyRecordingStatus: (status: 'warning', message: string) => void;
        recordingByteLength: () => number;
      };
      internals.checkpointToBackground();
      internals.notifyRecordingStatus('warning', 'after stop');
      expect(internals.recordingByteLength()).toBe(0);
    });

    it('resumes v2 checkpoints from the background with default selector state', async () => {
      const checkpoint = {
        version: 1,
        recordingFlag: true,
        recording: {
          version: '2.0.0',
          meta: {
            startUrl: window.location.href,
            title: 'resume',
            recordedAt: new Date().toISOString(),
            domain: window.location.hostname,
            semanticDomVersion: '1',
            sanitizationVersion: 'extension-v1',
          },
          limits: { maxActions: 10, maxDurationMs: 1000, maxBytes: 100000, warningThreshold: 0.8 },
          warnings: [],
          events: [],
          snapshots: [{
            timestamp: 1,
            url: window.location.href,
            selectorMap: {},
            domTree: { type: 'element', tagName: 'html' },
            phase: 'initial',
            sequence: 3,
            capture: { status: 'complete', nodeCount: 1, redactionCount: 0, removedNodeCount: 0, frames: [] },
          }],
        } as PageAgentRecording,
      };
      const sendMessage = vi.fn()
        .mockResolvedValueOnce({ active: false })
        .mockResolvedValueOnce({ active: true })
        .mockResolvedValueOnce({ active: true, state: checkpoint });
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });

      await expect(recorder.resumeFromBackgroundIfNeeded()).resolves.toBe(false);
      await expect(recorder.resumeFromBackgroundIfNeeded()).resolves.toBe(false);
      await expect(recorder.resumeFromBackgroundIfNeeded()).resolves.toBe(true);
      expect(recorder.isRecording()).toBe(true);
      expect((recorder as unknown as { nextIndex: number; snapshotSequence: number }).nextIndex).toBe(1);
      expect((recorder as unknown as { snapshotSequence: number }).snapshotSequence).toBe(4);
      await recorder.stopAsync();
    });

    it('awaits one shared background restore before stop and leaves no ghost recorder', async () => {
      let release!: (value: unknown) => void;
      const delayed = new Promise<unknown>((resolve) => {
        release = resolve;
      });
      const sendMessage = vi.fn(() => delayed);
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      const checkpoint = {
        version: 1,
        recordingFlag: true,
        recording: {
          version: '2.0.0',
          meta: {
            startUrl: window.location.href,
            title: 'restored before stop',
            recordedAt: new Date().toISOString(),
            domain: window.location.hostname,
            semanticDomVersion: '1',
            sanitizationVersion: 'extension-v2',
          },
          limits: { maxActions: 10, maxDurationMs: 60_000, maxBytes: 100_000, warningThreshold: 0.8 },
          warnings: [],
          events: [{ type: 'click', index: 7, timestamp: 2 }],
          snapshots: [{
            timestamp: 1,
            url: window.location.href,
            selectorMap: {},
            domTree: { type: 'element' as const, tagName: 'html' },
            phase: 'initial' as const,
            sequence: 0,
            capture: {
              status: 'complete' as const,
              nodeCount: 1,
              redactionCount: 0,
              removedNodeCount: 0,
              frames: [],
            },
          }],
        } as PageAgentRecording,
      };

      const bootstrapRestore = recorder.resumeFromBackgroundIfNeeded();
      let stopSettled = false;
      const stopping = recorder.stopAsync().then((recording) => {
        stopSettled = true;
        return recording;
      });
      await flushPromises();
      expect(stopSettled).toBe(false);
      expect(sendMessage).toHaveBeenCalledTimes(1);

      release({ active: true, state: checkpoint });
      await expect(bootstrapRestore).resolves.toBe(true);
      const stopped = await stopping;

      expect(stopped.version).toBe('2.0.0');
      expect(stopped.events).toEqual(checkpoint.recording.events);
      expect(stopped.termination).toMatchObject({ complete: true });
      expect(recorder.isRecording()).toBe(false);
      await flushPromises();
      expect(recorder.isRecording()).toBe(false);
    });

    it('returns false when background resume messaging is missing or rejects', async () => {
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      await expect(recorder.resumeFromBackgroundIfNeeded()).resolves.toBe(false);

      (globalThis as Record<string, unknown>).chrome = {
        runtime: { sendMessage: vi.fn().mockRejectedValue('offline') },
      };
      await expect(recorder.resumeFromBackgroundIfNeeded()).resolves.toBe(false);
    });

    it('covers limit guards without scheduling duplicate stops', async () => {
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      const internals = recorder as unknown as {
        evaluateLimits: () => void;
        requestLimitStop: (reason: 'size-limit', message: string) => void;
        recording: PageAgentRecording | null;
        stopping: boolean;
        limitStopRequested: boolean;
      };
      internals.evaluateLimits();
      recorder.start();
      await flushPromises();
      internals.stopping = true;
      internals.evaluateLimits();
      internals.stopping = false;
      const limits = internals.recording!.limits!;
      internals.recording!.limits = undefined;
      internals.evaluateLimits();
      internals.recording!.limits = { ...limits, maxActions: 0, maxDurationMs: 0, maxBytes: 0 };
      internals.evaluateLimits();
      internals.limitStopRequested = true;
      internals.requestLimitStop('size-limit', 'duplicate');
      internals.limitStopRequested = false;
      await recorder.stopAsync();
      internals.requestLimitStop('size-limit', 'after stop');
    });

    it('aggregates v2 frame reports and serialization counts', async () => {
      document.body.innerHTML = '<iframe src="https://other.example/frame"></iframe>';
      const frameTree = {
        frameId: 0,
        parentFrameId: -1,
        url: window.location.href,
        status: 'captured',
        domTree: { type: 'element', tagName: 'html' },
        serialization: { nodeCount: 1, redactionCount: 0, removedNodeCount: 0, truncated: false },
        children: [{
          frameId: 1,
          parentFrameId: 0,
          url: 'https://other.example/frame',
          status: 'error',
          error: 'capture-failed',
          domTree: { type: 'element', tagName: 'html' },
          serialization: { nodeCount: 3, redactionCount: 2, removedNodeCount: 1, truncated: true },
          children: [{
            frameId: 2,
            parentFrameId: 1,
            url: 'https://other.example/nested',
            status: 'captured',
            domTree: { type: 'element', tagName: 'html' },
            children: [],
          }],
        }],
      };
      (globalThis as Record<string, unknown>).chrome = {
        runtime: { sendMessage: vi.fn().mockResolvedValue({ frameTree }) },
      };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      const recording = await recorder.stopAsync();
      const initial = recording.snapshots[0];

      expect(initial.capture).toMatchObject({
        status: 'partial',
        redactionCount: expect.any(Number),
        removedNodeCount: expect.any(Number),
      });
      expect(initial.capture!.redactionCount).toBeGreaterThanOrEqual(2);
      expect(initial.capture!.removedNodeCount).toBeGreaterThanOrEqual(1);
      expect(initial.capture!.frames).toHaveLength(3);
    });

    it('redacts password input events before checkpointing', async () => {
      document.body.innerHTML = '<input id="password" type="password">';
      const input = document.getElementById('password') as HTMLInputElement;
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
      recorder.start();
      await flushPromises();

      input.value = 'typed-secret';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      vi.advanceTimersByTime(100);
      await flushPromises();
      const recording = await recorder.stopAsync();

      expect(recording.events).toContainEqual(expect.objectContaining({ type: 'inputText', text: '[REDACTED]' }));
      expect(JSON.stringify(recording)).not.toContain('typed-secret');
    });

    it('stops cleanly at the action limit and never drops older snapshots', async () => {
      document.body.innerHTML = '<button id="one">One</button><button id="two">Two</button>';
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        maxEvents: 2,
        maxSnapshots: 2,
        warningThreshold: 0.5,
      });
      recorder.start();
      await flushPromises();

      document.getElementById('one')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      document.getElementById('two')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await flushPromises();
      const recording = await recorder.stopAsync();

      expect(recording.events).toHaveLength(2);
      expect(recording.snapshots).toHaveLength(4);
      expect(recording.snapshots.map((snapshot) => snapshot.phase)).toEqual([
        'initial',
        'before-action',
        'before-action',
        'final',
      ]);
      expect(recording.termination?.reason).toBe('action-limit');
      expect(recording.warnings?.some((warning) => warning.kind === 'actions')).toBe(true);
    });

    it('stops cleanly at the negotiated duration limit', async () => {
      document.body.innerHTML = '<p>duration fixture</p>';
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        maxDurationMs: 50,
        maxEvents: 10,
        warningThreshold: 0.5,
      });
      recorder.start();
      await flushPromises();

      vi.advanceTimersByTime(50);
      await flushPromises();
      const recording = await recorder.stopAsync();

      expect(recording.termination).toMatchObject({ reason: 'duration-limit', complete: true });
      expect(recording.snapshots.at(-1)?.phase).toBe('final');
    });

    it('stops cleanly at the negotiated serialized-size limit', async () => {
      document.body.textContent = 'large semantic value '.repeat(200);
      const recorder = new ContentRecorder({
        protocolVersion: '2.0.0',
        maxRecordingBytes: 512,
        maxEvents: 10,
        warningThreshold: 0.5,
      });
      recorder.start();
      await flushPromises();
      const recording = await recorder.stopAsync();

      expect(recording.termination).toMatchObject({ reason: 'size-limit', complete: true });
      expect(recording.warnings?.some((warning) => warning.kind === 'size')).toBe(true);
      expect(recording.snapshots.map((snapshot) => snapshot.phase)).toEqual(['initial', 'final']);
    });
  });

  describe('D-3: checkpoint throttling & .local fallback', () => {
    it('checkpointToBackground throttles to ≥5s between sends', async () => {
      const sendMessage = vi.fn().mockResolvedValue({ success: true });
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      // start() triggers an initial persistState() → checkpointToBackground().
      const initialCalls = sendMessage.mock.calls.filter(
        (call) => (call[0] as { action?: string }).action === 'RECORDING_CHECKPOINT',
      ).length;
      expect(initialCalls).toBeGreaterThanOrEqual(1);

      const internals = recorder as unknown as { checkpointToBackground: (force?: boolean) => void };
      // Drive two back-to-back unforced checkpoints — both should be throttled.
      internals.checkpointToBackground();
      internals.checkpointToBackground();
      expect(
        sendMessage.mock.calls.filter((c) => (c[0] as { action?: string }).action === 'RECORDING_CHECKPOINT').length,
      ).toBe(initialCalls); // no new calls

      // Advance past the 5s throttle window; next call should go through.
      vi.advanceTimersByTime(5001);
      internals.checkpointToBackground();
      expect(
        sendMessage.mock.calls.filter((c) => (c[0] as { action?: string }).action === 'RECORDING_CHECKPOINT').length,
      ).toBe(initialCalls + 1);

      await recorder.stopAsync();
    });

    it('checkpointToBackground(force=true) bypasses the throttle', async () => {
      const sendMessage = vi.fn().mockResolvedValue({ success: true });
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      const initialCalls = sendMessage.mock.calls.filter(
        (call) => (call[0] as { action?: string }).action === 'RECORDING_CHECKPOINT',
      ).length;

      const internals = recorder as unknown as { checkpointToBackground: (force?: boolean) => void };
      // Immediately after the initial checkpoint, force should bypass throttle.
      internals.checkpointToBackground(true);
      expect(
        sendMessage.mock.calls.filter((c) => (c[0] as { action?: string }).action === 'RECORDING_CHECKPOINT').length,
      ).toBe(initialCalls + 1);

      await recorder.stopAsync();
    });

    it('skips the doomed sessionStorage write for oversized v2 state but still checkpoints', async () => {
      const sendMessage = vi.fn().mockResolvedValue({ success: true });
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      recorder.start();
      await flushPromises();
      // A small v2 recording still uses the sessionStorage fast path.
      expect(sessionStorage.getItem('__ocRecordingState')).toBeTruthy();

      const checkpointCalls = () => sendMessage.mock.calls.filter(
        (c) => (c[0] as { action?: string }).action === 'RECORDING_CHECKPOINT',
      ).length;
      const before = checkpointCalls();
      const internals = recorder as unknown as {
        persistState: (force?: boolean) => void;
        currentByteLength: number;
      };
      // Past the ~4MB sessionStorage budget the write can never succeed
      // (quota), so persistState must skip serializing tens of megabytes on
      // the main thread and rely on the durable background checkpoint.
      internals.currentByteLength = 5 * 1024 * 1024;
      internals.persistState(true);
      expect(sessionStorage.getItem('__ocRecordingState')).toBeNull();
      expect(checkpointCalls()).toBe(before + 1);

      await recorder.stopAsync();
    });

    it('resumeFromBackgroundIfNeeded falls back to chrome.storage.local when SW has no in-memory session', async () => {
      const v2Recording = {
        version: '2.0.0',
        meta: {
          startUrl: window.location.href,
          title: 'fallback',
          recordedAt: '2026-08-04T00:00:00.000Z',
          domain: window.location.hostname,
          semanticDomVersion: '1',
          sanitizationVersion: 'extension-v1',
        },
        limits: { maxActions: 10, maxDurationMs: 60_000, maxBytes: 100_000, warningThreshold: 0.8 },
        warnings: [],
        events: [],
        snapshots: [{
          timestamp: 1,
          url: window.location.href,
          selectorMap: {},
          domTree: { type: 'element', tagName: 'html' },
          phase: 'initial',
          sequence: 0,
          capture: { status: 'complete', nodeCount: 1, redactionCount: 0, removedNodeCount: 0, frames: [] },
        }],
      } as PageAgentRecording;
      const localCheckpoint = {
        snapshot: v2Recording,
        options: { protocolVersion: '2.0.0' },
        selectorToIndex: [['#x', { index: 5, lastUsedAt: 9 }]],
        nextIndex: 6,
        lastRecordedUrl: 'https://example.com/page',
        savedAt: Date.now(),
        sessionId: 12345,
      };
      const sendMessage = vi.fn().mockResolvedValue({ active: false, localCheckpoint });
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });

      const result = await recorder.resumeFromBackgroundIfNeeded();
      expect(result).toBe(true);
      expect(recorder.isRecording()).toBe(true);
      const internals = recorder as unknown as { nextIndex: number; lastRecordedUrl: string | null; selectorToIndex: Map<string, { index: number }> };
      // nextIndex should reflect the checkpoint's selectorToIndex (highest index + 1)
      // or the explicit nextIndex, whichever is greater. The checkpoint has
      // nextIndex: 6 and selectorToIndex entry with index 5, so nextIndex = 6.
      expect(internals.nextIndex).toBeGreaterThanOrEqual(6);
      expect(internals.selectorToIndex.size).toBeGreaterThanOrEqual(1);
      expect(internals.lastRecordedUrl).toBe('https://example.com/page');

      await recorder.stopAsync();
    });

    it('discards stale .local checkpoint (sessionId mismatch)', async () => {
      // The content script has startedAtMs set from a prior restore (e.g.,
      // from sessionStorage in a previous page load). If the .local checkpoint
      // was written by a *different* session (sessionId mismatch), the content
      // script must NOT restore from it and must send CLEAR_RECORDING_CHECKPOINT
      // so the SW removes the stale key.
      const v2Recording = {
        version: '2.0.0',
        meta: {
          startUrl: window.location.href,
          title: 'stale',
          recordedAt: '2026-08-04T00:00:00.000Z',
          domain: window.location.hostname,
          semanticDomVersion: '1',
          sanitizationVersion: 'extension-v1',
        },
        limits: { maxActions: 10, maxDurationMs: 60_000, maxBytes: 100_000, warningThreshold: 0.8 },
        warnings: [],
        events: [],
        snapshots: [{
          timestamp: 1,
          url: window.location.href,
          selectorMap: {},
          domTree: { type: 'element', tagName: 'html' },
          phase: 'initial',
          sequence: 0,
          capture: { status: 'complete', nodeCount: 1, redactionCount: 0, removedNodeCount: 0, frames: [] },
        }],
      } as PageAgentRecording;
      // Stale checkpoint from a prior session — sessionId doesn't match the
      // current realm's startedAt.
      const localCheckpoint = {
        snapshot: v2Recording,
        options: { protocolVersion: '2.0.0' },
        selectorToIndex: [],
        nextIndex: 99,
        lastRecordedUrl: null,
        savedAt: 1,
        sessionId: 99999, // mismatched
      };
      const sendMessage = vi.fn().mockResolvedValue(undefined);
      (globalThis as Record<string, unknown>).chrome = { runtime: { sendMessage } };
      const recorder = new ContentRecorder({ protocolVersion: '2.0.0' });
      // Seed startedAtMs from a prior session without setting recordingFlag
      // (simulates the content script knowing its session epoch but not yet
      // having restored a recording).
      const internals = recorder as unknown as {
        startedAtMs: number;
        restoreFromLocalCheckpoint: (chk: unknown) => boolean;
      };
      internals.startedAtMs = Date.parse('2026-08-04T12:00:00.000Z');

      expect(internals.restoreFromLocalCheckpoint(localCheckpoint)).toBe(false);
      // CLEAR_RECORDING_CHECKPOINT should have been sent to clean up the stale key.
      const clearCall = sendMessage.mock.calls.find(
        (c) => (c[0] as { action?: string }).action === 'CLEAR_RECORDING_CHECKPOINT',
      );
      expect(clearCall).toBeDefined();
    });
  });

  describe('H-6: synthetic event rejection', () => {
    it('rejects synthetic click events when enforceIsTrusted is enabled', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false, enforceIsTrusted: true });
      recorder.start();
      document.body.innerHTML = '<button id="btn">Click</button>';
      const btn = document.getElementById('btn')!;
      // jsdom events have isTrusted=false — the recorder should reject them.
      btn.click();
      const recording = recorder.stop();
      expect(recording.events).toHaveLength(0);
    });

    it('accepts events when enforceIsTrusted is not set (backward compat)', async () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false });
      recorder.start();
      document.body.innerHTML = '<button id="btn">Click</button>';
      const btn = document.getElementById('btn')!;
      btn.click();
      await flushPromises();
      const recording = recorder.stop();
      expect(recording.events.length).toBeGreaterThanOrEqual(1);
    });
  });

  describe('H-5: disconnected element flush', () => {
    it('flushScrollTimeouts skips elements removed from the DOM', () => {
      const recorder = new ContentRecorder({ captureSnapshotBeforeEachEvent: false }) as unknown as {
        start(): void;
        stop(): PageAgentRecording;
        recordingFlag: boolean;
        recording: PageAgentRecording | null;
        scrollTimeouts: Map<Element, ReturnType<typeof setTimeout>>;
        scrollState: WeakMap<Element, { top: number; left: number }>;
        recordScroll(el: Element, sync?: boolean): void;
        getElementIndex(el: Element): number;
        flushScrollTimeouts(): void;
      };
      recorder.start();

      // Create a scrollable element, simulate scroll, then remove it.
      document.body.innerHTML = '<div id="scrollable" style="overflow:scroll;height:100px"><div style="height:500px"></div></div>';
      const el = document.getElementById('scrollable')!;
      recorder.scrollState.set(el, { top: 0, left: 0 });
      recorder.scrollTimeouts.set(el, setTimeout(() => undefined, 9999));

      // Remove the element from the DOM.
      el.remove();

      const eventsBefore = recorder.recording!.events.length;
      recorder.flushScrollTimeouts();
      // No new events should be recorded for the detached element.
      expect(recorder.recording!.events.length).toBe(eventsBefore);
      // The Map entry should be cleaned up regardless.
      expect(recorder.scrollTimeouts.size).toBe(0);
      recorder.stop();
    });
  });
});
