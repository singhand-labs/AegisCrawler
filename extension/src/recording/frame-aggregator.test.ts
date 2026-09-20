import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  mergeFrameTreeIntoDom,
  buildFrameTree,
  collectFrameReports,
  aggregateFrameDom,
  captureLocalDom,
  requestAggregateFromBackground,
  type FrameTreeNode,
} from './frame-aggregator';
import type { DomNode } from '../../../src/rule-generator/types';

function el(tag: string, children?: DomNode[], attrs?: Record<string, string>): DomNode {
  return {
    type: 'element',
    tagName: tag,
    attributes: attrs ? Object.entries(attrs).map(([name, value]) => ({ name, value })) : undefined,
    children,
  };
}

function iframe(src: string): DomNode {
  return { type: 'element', tagName: 'iframe', attributes: [{ name: 'src', value: src }], framePlaceholder: true };
}

function text(value: string): DomNode {
  return { type: 'text', text: value };
}

function frameNode(frameId: number, domTree: DomNode, children: FrameTreeNode[] = [], url = 'about:blank'): FrameTreeNode {
  return { frameId, url, domTree, children };
}

function htmlTree(bodyChildren: DomNode[]): DomNode {
  return el('html', [el('head'), el('body', bodyChildren)]);
}

describe('buildFrameTree', () => {
  it('builds a tree from flat frame list', () => {
    const frames = [
      { frameId: 0, parentFrameId: -1, url: 'http://top' },
      { frameId: 10, parentFrameId: 0, url: 'http://child' },
      { frameId: 20, parentFrameId: 10, url: 'http://grandchild' },
    ];
    const tree = buildFrameTree(frames);
    expect(tree.frameId).toBe(0);
    expect(tree.children).toHaveLength(1);
    expect(tree.children[0].frameId).toBe(10);
    expect(tree.children[0].children[0].frameId).toBe(20);
  });

  it('throws when the frame list has no root frame', () => {
    const frames = [
      { frameId: 1, parentFrameId: 0, url: 'http://orphan' },
      { frameId: 2, parentFrameId: 1, url: 'http://orphan-child' },
    ];
    expect(() => buildFrameTree(frames)).toThrow('Frame list has no root frame');
  });

  it('sanitizes credential-like values from frame URLs', () => {
    const tree = buildFrameTree([
      { frameId: 0, parentFrameId: -1, url: 'https://example.com/?token=top-secret' },
      { frameId: 1, parentFrameId: 0, url: 'https://other.example/?api_key=child-secret' },
    ]);

    expect(JSON.stringify(tree)).not.toContain('top-secret');
    expect(JSON.stringify(tree)).not.toContain('child-secret');
    expect(tree.url).toContain('[REDACTED]');
    expect(tree.children[0].url).toContain('[REDACTED]');
  });
});

describe('collectFrameReports', () => {
  it('returns an empty report for a missing frame tree', () => {
    expect(collectFrameReports(null)).toEqual([]);
  });

  it('uses stable defaults when optional frame fields are absent', () => {
    const tree = frameNode(0, htmlTree([]));
    expect(collectFrameReports(tree)).toEqual([{
      frameId: 0,
      parentFrameId: -1,
      url: 'about:blank',
      status: 'captured',
      error: undefined,
    }]);
  });

  it('reports recursively captured and unavailable frames with stable errors', () => {
    const tree: FrameTreeNode = {
      frameId: 0,
      parentFrameId: -1,
      url: 'https://example.com/',
      domTree: htmlTree([]),
      status: 'captured',
      children: [{
        frameId: 1,
        parentFrameId: 0,
        url: 'https://other.example/',
        domTree: htmlTree([]),
        status: 'captured',
        children: [{
          frameId: 2,
          parentFrameId: 1,
          url: '[REMOVED_URL]',
          domTree: htmlTree([]),
          status: 'unavailable',
          error: 'content-script-unavailable',
          children: [],
        }],
      }],
    };

    expect(collectFrameReports(tree)).toEqual([
      { frameId: 0, parentFrameId: -1, url: 'https://example.com/', status: 'captured', error: undefined },
      { frameId: 1, parentFrameId: 0, url: 'https://other.example/', status: 'captured', error: undefined },
      { frameId: 2, parentFrameId: 1, url: '[REMOVED_URL]', status: 'unavailable', error: 'content-script-unavailable' },
    ]);
  });
});

describe('mergeFrameTreeIntoDom', () => {
  it('merges a single child frame into an iframe placeholder', () => {
    const dom = htmlTree([iframe('http://child')]);
    const childDom = htmlTree([el('button', [text('Inside frame')])]);
    const frameTree = frameNode(0, dom, [frameNode(10, childDom, [], 'http://child')]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const body = merged.children![1];
    const iframeNode = body.children![0];
    expect(iframeNode.children).toHaveLength(1);
    expect(iframeNode.children![0].tagName).toBe('html');
    const innerBody = iframeNode.children![0].children![1];
    expect(innerBody.children![0].tagName).toBe('button');
  });

  it('keeps unmatched iframes as placeholders', () => {
    const dom = htmlTree([iframe('http://child')]);
    const frameTree = frameNode(0, dom, []);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const body = merged.children![1];
    const iframeNode = body.children![0];
    expect(iframeNode.children).toBeUndefined();
    expect(iframeNode.framePlaceholder).toBe(true);
  });

  it('matches multiple iframes by DFS order', () => {
    const dom = htmlTree([iframe('http://a'), el('div', [iframe('http://b')])]);
    const frameA = htmlTree([el('a', [text('A')])]);
    const frameB = htmlTree([el('a', [text('B')])]);
    const frameTree = frameNode(0, dom, [
      frameNode(1, frameA, [], 'http://a'),
      frameNode(2, frameB, [], 'http://b'),
    ]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const body = merged.children![1];
    const first = body.children![0];
    const second = body.children![1].children![0];
    expect(first.children![0].children![1].children![0].tagName).toBe('a');
    expect(second.children![0].children![1].children![0].tagName).toBe('a');
  });

  it('recursively merges nested iframe placeholders inside child frames', () => {
    const dom = htmlTree([iframe('http://child')]);
    const childDom = htmlTree([el('div', [iframe('http://grandchild')])]);
    const grandchildDom = htmlTree([el('button', [text('Deep')])]);

    const frameTree = frameNode(0, dom, [
      frameNode(10, childDom, [frameNode(20, grandchildDom, [], 'http://grandchild')], 'http://child'),
    ]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const outerIframe = merged.children![1].children![0];
    expect(outerIframe.children![0].tagName).toBe('html');

    const innerIframe = outerIframe.children![0].children![1].children![0].children![0];
    expect(innerIframe.tagName).toBe('iframe');
    expect(innerIframe.children![0].children![1].children![0].tagName).toBe('button');
    expect(outerIframe.framePlaceholder).toBeUndefined();
    expect(innerIframe.framePlaceholder).toBeUndefined();
  });

  it('classifies same-origin and cross-origin placeholders', () => {
    const dom = htmlTree([
      iframe('https://example.com/page'),
      iframe('https://other.com/page'),
    ]);
    const sameOriginDom = htmlTree([el('p')]);
    const crossOriginDom = htmlTree([el('span')]);
    const frameTree = frameNode(
      0,
      dom,
      [
        frameNode(1, sameOriginDom, [], 'https://example.com/page/child'),
        frameNode(2, crossOriginDom, [], 'https://other.com/page/child'),
      ],
      'https://example.com/',
    );

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const body = merged.children![1];
    expect(body.children![0].frameOrigin).toBe('same-origin');
    expect(body.children![1].frameOrigin).toBe('cross-origin');
  });

  it('treats a malformed iframe src as unmatched placeholder', () => {
    const dom = htmlTree([iframe('http://[::1')]);
    const childDom = htmlTree([el('p')]);
    const frameTree = frameNode(0, dom, [frameNode(1, childDom, [], 'https://example.com/')]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const iframeNode = merged.children![1].children![0];
    expect(iframeNode.children).toBeUndefined();
    expect(iframeNode.framePlaceholder).toBe(true);
    expect(iframeNode.frameMatched).toBe(false);
  });

  it('falls back to URL scan when placeholder index cannot be determined', () => {
    const dom = htmlTree([iframe('https://example.com/frame')]);
    const childDom = htmlTree([el('p')]);
    const frameTree: FrameTreeNode = {
      frameId: 0,
      url: 'https://example.com/',
      domTree: htmlTree([]),
      children: [frameNode(1, childDom, [], 'https://example.com/frame')],
    };

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const iframeNode = merged.children![1].children![0];
    expect(iframeNode.children).toHaveLength(1);
    expect(iframeNode.frameMatched).toBe(true);
  });

  it('clones nodes without optional fields without adding them', () => {
    const dom: DomNode = {
      type: 'element',
      tagName: 'html',
      children: [{ type: 'text', text: 'hello' }],
    };
    const frameTree = frameNode(0, dom, []);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    expect(merged.attributes).toBeUndefined();
    expect(merged.frameOrigin).toBeUndefined();
    expect(merged.frameSrc).toBeUndefined();
    expect(merged.framePlaceholder).toBeUndefined();
    expect(merged.children![0].children).toBeUndefined();
  });

  it('preserves explicit non-rendered evidence while merging frame trees', () => {
    const hiddenChild = el('section', [text('hidden')]);
    hiddenChild.rendered = false;
    hiddenChild.sanitization = {
      markupAltered: true,
      contentOmitted: true,
      alteredAttributes: ['class'],
    };
    hiddenChild.children![0].rendered = false;
    const dom = htmlTree([hiddenChild, iframe('http://child')]);
    const hiddenFrameRoot = htmlTree([el('span', [text('inside')])]);
    hiddenFrameRoot.rendered = false;
    const frameTree = frameNode(0, dom, [frameNode(10, hiddenFrameRoot, [], 'http://child')]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);

    const body = merged.children![1];
    expect(body.children![0].rendered).toBe(false);
    expect(body.children![0].sanitization).toEqual({
      markupAltered: true,
      contentOmitted: true,
      alteredAttributes: ['class'],
    });
    expect(body.children![0].sanitization).not.toBe(hiddenChild.sanitization);
    expect(body.children![0].sanitization!.alteredAttributes)
      .not.toBe(hiddenChild.sanitization!.alteredAttributes);
    expect(body.children![0].children![0].rendered).toBe(false);
    expect(body.children![1].children![0].rendered).toBe(false);
  });

  it('skips merging when iframe src and frame URL do not match', () => {
    const dom = htmlTree([iframe('https://expected.com/')]);
    const childDom = htmlTree([el('button', [text('Wrong frame')])]);
    const frameTree = frameNode(0, dom, [frameNode(10, childDom, [], 'https://actual.com/')]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const iframeNode = merged.children![1].children![0];
    expect(iframeNode.children).toBeUndefined();
    expect(iframeNode.framePlaceholder).toBe(true);
    expect(iframeNode.frameMatched).toBe(false);
  });

  it('falls back to a later sibling frame when the index candidate mismatches but another frame matches', () => {
    const dom = htmlTree([iframe('https://b.com/')]);
    const frameA = htmlTree([el('p', [text('A')])]);
    const frameB = htmlTree([el('button', [text('B')])]);
    const frameTree = frameNode(0, dom, [
      frameNode(1, frameA, [], 'https://a.com/'),
      frameNode(2, frameB, [], 'https://b.com/'),
    ]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const iframeNode = merged.children![1].children![0];
    expect(iframeNode.children).toHaveLength(1);
    const innerBody = iframeNode.children![0].children![1];
    expect(innerBody.children![0].tagName).toBe('button');
    expect(iframeNode.framePlaceholder).toBeUndefined();
    expect(iframeNode.frameMatched).toBe(true);
  });

  it('keeps placeholder when no sibling frame matches at all', () => {
    const dom = htmlTree([iframe('https://x.com/')]);
    const frameTree = frameNode(0, dom, [
      frameNode(1, htmlTree([el('p')]), [], 'https://a.com/'),
      frameNode(2, htmlTree([el('p')]), [], 'https://b.com/'),
    ]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const iframeNode = merged.children![1].children![0];
    expect(iframeNode.children).toBeUndefined();
    expect(iframeNode.frameMatched).toBe(false);
  });

  it('prefers an exact path match over a prefix match', () => {
    const dom = htmlTree([iframe('https://example.com/page')]);
    const exactDom = htmlTree([el('button', [text('Exact')])]);
    const prefixDom = htmlTree([el('p', [text('Prefix')])]);
    const frameTree = frameNode(0, dom, [
      frameNode(1, prefixDom, [], 'https://example.com/'),
      frameNode(2, exactDom, [], 'https://example.com/page'),
    ]);

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    const iframeNode = merged.children![1].children![0];
    expect(iframeNode.frameMatched).toBe(true);
    const innerBody = iframeNode.children![0].children![1];
    expect(innerBody.children![0].tagName).toBe('button');
  });

  it('matches an iframe whose path extends the captured frame path', () => {
    const dom = htmlTree([iframe('https://example.com/frame/child')]);
    const childDom = htmlTree([el('p', [text('prefix match')])]);
    const frameTree = frameNode(0, dom, [
      frameNode(1, childDom, [], 'https://example.com/frame'),
    ], 'https://example.com/');

    const merged = mergeFrameTreeIntoDom(dom, frameTree);
    expect(merged.children![1].children![0].frameMatched).toBe(true);
  });
});

describe('aggregateFrameDom', () => {
  let chromeStub: {
    webNavigation: { getAllFrames: ReturnType<typeof vi.fn> };
    tabs: { sendMessage: ReturnType<typeof vi.fn> };
  };

  beforeEach(() => {
    chromeStub = {
      webNavigation: { getAllFrames: vi.fn() },
      tabs: { sendMessage: vi.fn() },
    };
    vi.stubGlobal('chrome', chromeStub);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns null when chrome APIs are missing', async () => {
    vi.unstubAllGlobals();
    const result = await aggregateFrameDom(42);
    expect(result).toBeNull();
  });

  it('returns null when getAllFrames returns an empty list', async () => {
    chromeStub.webNavigation.getAllFrames.mockResolvedValue([]);
    const result = await aggregateFrameDom(42);
    expect(result).toBeNull();
  });

  it('builds a frame tree and captures DOM from each frame', async () => {
    chromeStub.webNavigation.getAllFrames.mockResolvedValue([
      { frameId: 0, parentFrameId: -1, url: 'https://top' },
      { frameId: 10, parentFrameId: 0, url: 'https://child' },
    ]);

    const childCapture = { domTree: htmlTree([el('p', [text('child content')])]) };
    chromeStub.tabs.sendMessage.mockImplementation((_tabId: number, message: unknown, options?: { frameId?: number }) => {
      if (options?.frameId === 0) {
        return Promise.resolve({ domTree: htmlTree([el('h1', [text('top content')])]) });
      }
      if (options?.frameId === 10) {
        return Promise.resolve(childCapture);
      }
      return Promise.reject(new Error('unexpected frame'));
    });

    const tree = await aggregateFrameDom(7, { maxDepth: 15, maxNodes: 100, maxTextLength: 50 });
    expect(tree).not.toBeNull();
    expect(tree!.frameId).toBe(0);
    expect(tree!.children).toHaveLength(1);
    expect(tree!.children[0].frameId).toBe(10);
    expect(tree!.children[0].domTree.children![1].children![0].tagName).toBe('p');

    expect(chromeStub.tabs.sendMessage).toHaveBeenCalledWith(
      7,
      { action: 'CAPTURE_DOM', options: { maxDepth: 15, maxNodes: 100, maxTextLength: 50 } },
      { frameId: 0 },
    );
    expect(chromeStub.tabs.sendMessage).toHaveBeenCalledWith(
      7,
      { action: 'CAPTURE_DOM', options: { maxDepth: 15, maxNodes: 100, maxTextLength: 50 } },
      { frameId: 10 },
    );
  });

  it('records a capture error node when sendMessage rejects', async () => {
    chromeStub.webNavigation.getAllFrames.mockResolvedValue([
      { frameId: 0, parentFrameId: -1, url: 'https://top' },
      { frameId: 10, parentFrameId: 0, url: 'https://child' },
    ]);

    chromeStub.tabs.sendMessage.mockImplementation((_tabId: number, _message: unknown, options?: { frameId?: number }) => {
      if (options?.frameId === 10) {
        return Promise.reject(new Error('frame unreachable'));
      }
      return Promise.resolve({ domTree: htmlTree([]) });
    });

    const tree = await aggregateFrameDom(1);
    expect(tree).not.toBeNull();
    const child = tree!.children[0];
    const errorAttr = child.domTree.attributes?.find((a) => a.name === 'data-capture-error');
    expect(errorAttr).toBeDefined();
    expect(errorAttr!.value).toBe('capture-failed');
    expect(child.status).toBe('error');
    expect(child.error).toBe('capture-failed');
  });

  it.each([
    ['Could not establish connection. Receiving end does not exist.', 'unavailable', 'content-script-unavailable'],
    ['frame capture timeout', 'error', 'capture-timeout'],
  ])('classifies frame capture failure %s', async (message, status, code) => {
    chromeStub.webNavigation.getAllFrames.mockResolvedValue([
      { frameId: 0, parentFrameId: -1, url: 'https://top' },
    ]);
    chromeStub.tabs.sendMessage.mockRejectedValueOnce(message);

    const tree = await aggregateFrameDom(1);

    expect(tree).toMatchObject({ status, error: code });
    expect(tree!.domTree.attributes).toContainEqual({ name: 'data-capture-error', value: code });
  });

  it('records an empty-capture error node when a frame returns no domTree', async () => {
    chromeStub.webNavigation.getAllFrames.mockResolvedValue([
      { frameId: 0, parentFrameId: -1, url: 'https://top' },
      { frameId: 10, parentFrameId: 0, url: 'https://child' },
    ]);

    chromeStub.tabs.sendMessage.mockImplementation((_tabId: number, _message: unknown, options?: { frameId?: number }) => {
      if (options?.frameId === 10) {
        return Promise.resolve({});
      }
      return Promise.resolve({ domTree: htmlTree([]) });
    });

    const tree = await aggregateFrameDom(1);
    expect(tree).not.toBeNull();
    const child = tree!.children[0];
    const errorAttr = child.domTree.attributes?.find((a) => a.name === 'data-capture-error');
    expect(errorAttr).toBeDefined();
    expect(errorAttr!.value).toBe('empty-capture');
  });
});

describe('captureLocalDom', () => {
  it('returns a serialized DOM tree for the current document', () => {
    const tree = captureLocalDom();
    expect(tree).not.toBeNull();
    expect(tree!.tagName).toBe('html');
    expect(tree!.type).toBe('element');
  });

  it('accepts custom options', () => {
    const tree = captureLocalDom({ maxDepth: 5, maxNodes: 50, maxTextLength: 10 });
    expect(tree).not.toBeNull();
    expect(tree!.tagName).toBe('html');
  });
});

describe('requestAggregateFromBackground', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns null when chrome runtime is unavailable', async () => {
    expect(await requestAggregateFromBackground()).toBeNull();
  });

  it('returns the frameTree from a successful AGGREGATE_DOM response', async () => {
    const frameTree: FrameTreeNode = {
      frameId: 0,
      url: 'https://example.com',
      domTree: { type: 'element', tagName: 'html' },
      children: [],
    };
    const sendMessage = vi.fn().mockResolvedValue({ frameTree });
    vi.stubGlobal('chrome', { runtime: { sendMessage } });

    const result = await requestAggregateFromBackground({ maxDepth: 5, maxNodes: 50, maxTextLength: 10 });

    expect(sendMessage).toHaveBeenCalledWith({
      action: 'AGGREGATE_DOM',
      options: { maxDepth: 5, maxNodes: 50, maxTextLength: 10 },
    });
    expect(result).toEqual(frameTree);
  });
});
