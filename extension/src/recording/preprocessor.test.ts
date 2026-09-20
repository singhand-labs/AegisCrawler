import { describe, it, expect } from 'vitest';
import { preprocess } from './preprocessor';
import type { PageAgentRecording, DomSnapshot } from '../../../src/rule-generator/types';

function makeRecording(events: any[], snapshots: DomSnapshot[] = []): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: { startUrl: 'http://example.com', title: 'test', recordedAt: new Date().toISOString(), domain: 'example.com' },
    events: events as any,
    snapshots,
  };
}

function makeSnapshot(nodes: Record<number, { text?: string }>): DomSnapshot {
  const selectorMap: DomSnapshot['selectorMap'] = {};
  for (const [idx, data] of Object.entries(nodes)) {
    selectorMap[Number(idx)] = {
      index: Number(idx),
      tagName: 'div',
      selector: `#node-${idx}`,
      text: data.text,
      boundingRect: { x: 0, y: 0, width: 1, height: 1 },
    };
  }
  return { timestamp: 0, url: 'http://example.com', selectorMap };
}

describe('preprocess', () => {
  it('deduplicates rapid clicks on same index', () => {
    const recording = makeRecording([
      { type: 'click', index: 1, timestamp: 100 },
      { type: 'click', index: 1, timestamp: 150 },
      { type: 'click', index: 2, timestamp: 1000 },
    ]);
    const out = preprocess(recording);
    expect(out.events).toHaveLength(2);
  });

  it('merges consecutive scroll events with the same direction', () => {
    const recording = makeRecording([
      { type: 'scroll', direction: 'down', amount: 100, unit: 'pixels', timestamp: 100 },
      { type: 'scroll', direction: 'down', amount: 200, unit: 'pixels', timestamp: 200 },
      { type: 'scroll', direction: 'down', amount: 50, unit: 'pixels', timestamp: 300 },
    ]);
    const out = preprocess(recording);
    expect(out.events).toHaveLength(1);
    expect(out.events[0]).toMatchObject({ type: 'scroll', direction: 'down', amount: 350 });
  });

  it('does not merge scroll events with different directions', () => {
    const recording = makeRecording([
      { type: 'scroll', direction: 'down', amount: 100, unit: 'pixels', timestamp: 100 },
      { type: 'scroll', direction: 'up', amount: 50, unit: 'pixels', timestamp: 200 },
    ]);
    const out = preprocess(recording);
    expect(out.events).toHaveLength(2);
    expect(out.events[0]).toMatchObject({ type: 'scroll', direction: 'down', amount: 100 });
    expect(out.events[1]).toMatchObject({ type: 'scroll', direction: 'up', amount: 50 });
  });

  it('dehydrates snapshots to at most maxSnapshotNodes', () => {
    const nodes: Record<number, { text?: string }> = {};
    for (let i = 0; i < 10; i++) {
      nodes[i] = {};
    }
    const recording = makeRecording([], [makeSnapshot(nodes)]);
    const out = preprocess(recording, { maxSnapshotNodes: 3 });
    expect(Object.keys(out.domSnapshots[0].selectorMap)).toHaveLength(3);
  });

  it('truncates snapshot text to maxTextLength', () => {
    const recording = makeRecording([], [makeSnapshot({ 0: { text: 'a'.repeat(100) } })]);
    const out = preprocess(recording, { maxTextLength: 20 });
    expect(out.domSnapshots[0].selectorMap[0].text).toHaveLength(20);
    expect(out.domSnapshots[0].selectorMap[0].text).toBe('a'.repeat(20));
  });

  it('prunes domTree nodes and depth', () => {
    const snapshot: DomSnapshot = {
      timestamp: 0,
      url: 'http://example.com',
      selectorMap: {},
      domTree: {
        type: 'element',
        tagName: 'html',
        children: [
          {
            type: 'element',
            tagName: 'body',
            children: [
              { type: 'element', tagName: 'div', children: [{ type: 'element', tagName: 'span' }] },
              { type: 'element', tagName: 'p' },
            ],
          },
        ],
      },
    };
    const recording = makeRecording([], [snapshot]);
    const out = preprocess(recording, { maxDomTreeDepth: 1, maxDomTreeNodes: 3 });
    expect(out.domSnapshots[0].domTree).toBeDefined();
    expect(out.domSnapshots[0].domTree!.children).toBeDefined();
    expect(out.domSnapshots[0].domTree!.children).toHaveLength(1);
  });

  it('truncates domTree text', () => {
    const snapshot: DomSnapshot = {
      timestamp: 0,
      url: 'http://example.com',
      selectorMap: {},
      domTree: {
        type: 'element',
        tagName: 'p',
        text: 'a'.repeat(100),
      },
    };
    const recording = makeRecording([], [snapshot]);
    const out = preprocess(recording, { maxDomTreeTextLength: 10 });
    expect(out.domSnapshots[0].domTree!.text).toHaveLength(10);
    expect(out.domSnapshots[0].domTree!.sanitization).toMatchObject({
      markupAltered: true,
      contentOmitted: true,
    });
  });

  it('preserves iframe placeholder fields when pruning domTree', () => {
    const snapshot: DomSnapshot = {
      timestamp: 0,
      url: 'http://example.com',
      selectorMap: {},
      domTree: {
        type: 'element',
        tagName: 'body',
        children: [
          {
            type: 'element',
            tagName: 'iframe',
            framePlaceholder: true,
            frameOrigin: 'cross-origin',
            frameSrc: 'https://example.com/frame',
            frameMatched: false,
          },
        ],
      },
    };
    const recording = makeRecording([], [snapshot]);
    const out = preprocess(recording);
    const iframe = out.domSnapshots[0].domTree!.children![0];
    expect(iframe.framePlaceholder).toBe(true);
    expect(iframe.frameOrigin).toBe('cross-origin');
    expect(iframe.frameSrc).toBe('https://example.com/frame');
    expect(iframe.frameMatched).toBe(false);
  });

  it('preserves explicit non-rendered evidence when pruning domTree', () => {
    const snapshot: DomSnapshot = {
      timestamp: 0,
      url: 'http://example.com',
      selectorMap: {},
      domTree: {
        type: 'element',
        tagName: 'body',
        children: [
          {
            type: 'element',
            tagName: 'section',
            rendered: false,
            sanitization: {
              markupAltered: true,
              alteredAttributes: ['class'],
            },
            children: [{ type: 'element', tagName: 'span', rendered: false }],
          },
        ],
      },
    };
    const recording = makeRecording([], [snapshot]);

    const out = preprocess(recording);

    const section = out.domSnapshots[0].domTree!.children![0];
    expect(section.rendered).toBe(false);
    expect(section.sanitization).toEqual({
      markupAltered: true,
      alteredAttributes: ['class'],
    });
    expect(section.children![0].rendered).toBe(false);
  });

  it('drops the domTree entirely when maxDomTreeNodes is zero', () => {
    const snapshot: DomSnapshot = {
      timestamp: 0,
      url: 'http://example.com',
      selectorMap: {},
      domTree: { type: 'element', tagName: 'html' },
    };
    const recording = makeRecording([], [snapshot]);
    const out = preprocess(recording, { maxDomTreeNodes: 0 });
    expect(out.domSnapshots[0].domTree).toBeUndefined();
  });

  it('stops pruning children once the node budget is exhausted', () => {
    const snapshot: DomSnapshot = {
      timestamp: 0,
      url: 'http://example.com',
      selectorMap: {},
      domTree: {
        type: 'element',
        tagName: 'html',
        children: [{ type: 'element', tagName: 'head' }, { type: 'element', tagName: 'body' }],
      },
    };
    const recording = makeRecording([], [snapshot]);
    const out = preprocess(recording, { maxDomTreeNodes: 1 });
    expect(out.domSnapshots[0].domTree).toBeDefined();
    expect(out.domSnapshots[0].domTree!.children).toBeUndefined();
  });

  it('merges scroll amounts even when the first amount is missing', () => {
    const recording = makeRecording([
      { type: 'scroll', direction: 'down', timestamp: 100 },
      { type: 'scroll', direction: 'down', amount: 50, unit: 'pixels', timestamp: 200 },
    ]);
    const out = preprocess(recording);
    expect(out.events).toHaveLength(1);
    expect(out.events[0]).toMatchObject({ type: 'scroll', direction: 'down', amount: 50 });
  });
});
