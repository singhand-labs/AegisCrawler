import { describe, it, expect, vi } from 'vitest';
import { PageAgentRecorder } from './PageAgentRecorder';

function makeMockController() {
  return {
    clickElement: vi.fn(async (index: number) => ({ success: true })),
    inputText: vi.fn(async (index: number, text: string) => ({ success: true })),
    selectOption: vi.fn(async (index: number, optionText: string) => ({ success: true })),
    scroll: vi.fn(async (options: any) => ({ success: true })),
    scrollHorizontally: vi.fn(async (options: any) => ({ success: true })),
    executeJavascript: vi.fn(async (script: string) => ({ success: true })),
  };
}

describe('PageAgentRecorder', () => {
  it('records click events', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.clickElement(5);
    const recording = recorder.getRecording();
    expect(recording.events).toHaveLength(1);
    expect(recording.events[0]).toMatchObject({ type: 'click', index: 5 });
  });

  it('records inputText events', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.inputText(3, 'hello');
    const recording = recorder.getRecording();
    expect(recording.events[0]).toMatchObject({ type: 'inputText', index: 3, text: 'hello' });
  });

  it('records selectOption events', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.selectOption(4, 'Option B');
    const recording = recorder.getRecording();
    expect(recording.events).toHaveLength(1);
    expect(recording.events[0]).toMatchObject({ type: 'selectOption', index: 4, optionText: 'Option B' });
  });

  it('records scroll events', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.scroll({ down: true, numPages: 1 });
    const recording = recorder.getRecording();
    expect(recording.events[0]).toMatchObject({ type: 'scroll', direction: 'down', amount: 1, unit: 'pages' });
  });

  it('records upward pixel scrolling with a container index', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });

    await controller.scroll({ down: false, numPages: 1, pixels: 240, index: 9 });

    expect(recorder.getRecording().events[0]).toMatchObject({
      type: 'scroll',
      direction: 'up',
      amount: 240,
      unit: 'pixels',
      index: 9,
    });
  });

  it('records scrollHorizontally events', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.scrollHorizontally({ right: true, pixels: 120 });
    const recording = recorder.getRecording();
    expect(recording.events).toHaveLength(1);
    expect(recording.events[0]).toMatchObject({
      type: 'scroll',
      direction: 'right',
      amount: 120,
      unit: 'pixels',
    });
  });

  it('records leftward horizontal scrolling with a container index', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });

    await controller.scrollHorizontally({ right: false, pixels: 80, index: 4 });

    expect(recorder.getRecording().events[0]).toMatchObject({
      type: 'scroll',
      direction: 'left',
      amount: 80,
      index: 4,
    });
  });

  it('records executeJavascript events', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.executeJavascript('return document.title');
    const recording = recorder.getRecording();
    expect(recording.events).toHaveLength(1);
    expect(recording.events[0]).toMatchObject({ type: 'executeJavascript', script: 'return document.title' });
  });

  it('does not record an event when the action rejects', async () => {
    const controller = makeMockController();
    controller.clickElement = vi.fn(async () => {
      throw new Error('action failed');
    });
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });

    await expect(controller.clickElement(5)).rejects.toThrow('action failed');

    expect(recorder.getRecording().events).toHaveLength(0);
  });

  it('captures a snapshot before each event when enabled', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
    await controller.clickElement(7);

    const recording = recorder.getRecording();
    expect(recording.events).toHaveLength(1);
    expect(recording.snapshots).toHaveLength(1);
    expect(recording.snapshots[0]).toMatchObject({
      url: expect.any(String),
      selectorMap: expect.any(Object),
      timestamp: expect.any(Number),
    });
  });

  it('ignores snapshot errors and still runs the action', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const controller = makeMockController();
    Object.defineProperty(controller, 'selectorMap', {
      get: () => {
        throw new Error('snapshot boom');
      },
    });
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });

    await expect(controller.clickElement(2)).resolves.toEqual({ success: true });

    expect(recorder.getRecording().events).toHaveLength(1);
    expect(recorder.getRecording().snapshots).toHaveLength(0);
    expect(warnSpy).toHaveBeenCalled();

    warnSpy.mockRestore();
  });

  it('returns valid JSON from toJSON()', async () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.inputText(1, 'world');

    const json = recorder.toJSON();
    expect(() => JSON.parse(json)).not.toThrow();
    const parsed = JSON.parse(json);
    expect(parsed.version).toBe('1.0.0');
    expect(parsed.events).toHaveLength(1);
    expect(parsed.events[0]).toMatchObject({ type: 'inputText', index: 1, text: 'world' });
  });

  it('restores original controller methods via detach()', async () => {
    const controller = makeMockController();
    const clickMock = controller.clickElement;
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    await controller.clickElement(1);
    expect(recorder.getRecording().events).toHaveLength(1);

    recorder.detach();
    await controller.clickElement(2);

    expect(recorder.getRecording().events).toHaveLength(1);
    expect(clickMock).toHaveBeenCalledTimes(2);
  });

  it('throws when attaching a second recorder to the same controller', () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    expect(() => new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false })).toThrow(
      'already attached',
    );
    recorder.detach();
  });

  it('allows re-attachment after detach', () => {
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false });
    recorder.detach();
    expect(() => new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: false })).not.toThrow();
  });

  it('captures mixed selectorMap node shapes', async () => {
    const controller = makeMockController();
    const byId = document.createElement('button');
    byId.id = 'btn';
    byId.textContent = 'Click me';

    const byTestId = document.createElement('input');
    byTestId.setAttribute('data-testid', 'search');

    const byClass = document.createElement('div');
    byClass.classList.add('foo', 'bar');

    const plain = document.createElement('span');

    (controller as any).selectorMap = new Map([
      [1, { ref: byId }],
      [2, { tagName: 'a', text: 'link', boundingRect: { x: 1, y: 2, width: 3, height: 4 } }],
      [3, { tagName: 'span', text: 'plain' }],
      [4, { ref: byTestId }],
      [5, { ref: byClass }],
      [6, { ref: plain }],
    ]);

    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
    await controller.clickElement(1);

    const snapshot = recorder.getRecording().snapshots[0];
    expect(snapshot.selectorMap[1]).toMatchObject({
      index: 1,
      tagName: 'button',
      selector: '#btn',
      text: 'Click me',
    });
    expect(snapshot.selectorMap[2]).toMatchObject({
      index: 2,
      tagName: 'a',
      selector: '*',
      text: 'link',
      boundingRect: { x: 1, y: 2, width: 3, height: 4 },
    });
    expect(snapshot.selectorMap[3]).toMatchObject({
      index: 3,
      tagName: 'span',
      selector: '*',
      text: 'plain',
      boundingRect: { x: 0, y: 0, width: 0, height: 0 },
    });
    expect(snapshot.selectorMap[4].selector).toBe('[data-testid="search"]');
    expect(snapshot.selectorMap[5].selector).toBe('div.foo.bar');
    expect(snapshot.selectorMap[6].selector).toBe('span');
  });

  it('captures effective role via the implicit map when no explicit role is set (R-implicit)', async () => {
    const controller = makeMockController();
    const button = document.createElement('button');
    button.id = 'save';
    // No explicit role attribute — effective role must come from the tag map.
    const link = document.createElement('a');
    link.id = 'home';
    const checkbox = document.createElement('input');
    checkbox.setAttribute('type', 'checkbox');
    const span = document.createElement('span');
    span.id = 'custom';

    (controller as any).selectorMap = new Map([
      [1, { ref: button }],
      [2, { ref: link }],
      [3, { ref: checkbox }],
      [4, { ref: span }],
    ]);

    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
    await controller.clickElement(1);

    const selectorMap = recorder.getRecording().snapshots[0].selectorMap;
    expect(selectorMap[1].role).toBe('button'); // implicit from <button>
    expect(selectorMap[2].role).toBe('link'); // implicit from <a>
    expect(selectorMap[3].role).toBe('checkbox'); // implicit from input[type=checkbox]
    expect(selectorMap[4].role).toBeUndefined(); // span has no mapping
  });

  it('prefers an explicit role attribute over the implicit map', async () => {
    const controller = makeMockController();
    const button = document.createElement('button');
    button.id = 'navbtn';
    button.setAttribute('role', 'navigation'); // explicit overrides implicit 'button'

    (controller as any).selectorMap = new Map([[1, { ref: button }]]);

    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
    await controller.clickElement(1);

    expect(recorder.getRecording().snapshots[0].selectorMap[1].role).toBe('navigation');
  });

  it('refreshes state via updateTree and getBrowserState when available', async () => {
    const controller = makeMockController();
    const input = document.createElement('input');
    input.id = 'q';
    (controller as any).updateTree = vi.fn(async () => {});
    (controller as any).getBrowserState = vi.fn(async () => ({
      selectorMap: new Map([
        [5, { ref: input, boundingRect: { x: 0, y: 0, width: 10, height: 10 } }],
      ]),
    }));

    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
    await controller.clickElement(1);

    expect((controller as any).updateTree).toHaveBeenCalled();
    expect((controller as any).getBrowserState).toHaveBeenCalled();
    expect(recorder.getRecording().snapshots[0].selectorMap[5]).toMatchObject({
      index: 5,
      tagName: 'input',
      selector: '#q',
    });
  });

  it('reads meta and snapshot url from window/document when available', async () => {
    document.title = 'Recorder Test';
    const controller = makeMockController();
    const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
    await controller.clickElement(1);

    expect(recorder.getRecording().meta.title).toBe('Recorder Test');
    expect(recorder.getRecording().meta.startUrl).toBe(window.location.href);
    expect(recorder.getRecording().meta.domain).toBe(window.location.hostname);
    expect(recorder.getRecording().snapshots[0].url).toBe(window.location.href);
  });

  it('records safely when browser globals are unavailable', async () => {
    const savedWindow = globalThis.window;
    const savedDocument = globalThis.document;
    vi.stubGlobal('window', undefined);
    vi.stubGlobal('document', undefined);

    try {
      const controller = makeMockController();
      const recorder = new PageAgentRecorder(controller, { captureSnapshotBeforeEachEvent: true });
      await controller.clickElement(1);

      expect(recorder.getRecording().meta).toMatchObject({ startUrl: '', title: '', domain: '' });
      expect(recorder.getRecording().snapshots[0].url).toBe('');
    } finally {
      vi.stubGlobal('window', savedWindow);
      vi.stubGlobal('document', savedDocument);
    }
  });
});
