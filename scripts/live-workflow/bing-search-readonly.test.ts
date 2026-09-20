import { describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { BING_READONLY_QUERY, BING_READONLY_REQUIREMENT, assertBingReadonlyRecording, assertBingReadonlyRequirement, assertBingReadonlyRows, assertBingReadonlyRule } from './bing-search-readonly';

function recording(events: PageAgentRecording['events']): PageAgentRecording {
  const resultURL = `https://www.bing.com/search?q=${encodeURIComponent(BING_READONLY_QUERY)}`;
  return {
    version: '2.0.0',
    meta: {
      startUrl: 'https://www.bing.com/', title: 'Bing',
      recordedAt: new Date(0).toISOString(), domain: 'www.bing.com',
      semanticDomVersion: '1', sanitizationVersion: 'extension-v1',
    },
    events,
    snapshots: [{
      timestamp: 3, url: resultURL, selectorMap: {}, phase: 'final',
      capture: { status: 'complete', frames: [], nodeCount: 1, redactionCount: 0, removedNodeCount: 0 },
      domTree: { type: 'element', tagName: 'main', children: [] },
    }],
    termination: { reason: 'user', message: 'done', timestamp: 4, complete: true },
  };
}

describe('Bing read-only search contract', () => {
  it('locks the reviewed query', () => expect(BING_READONLY_QUERY).toBe('how do ocean tides work'));
  it('declares semantic inputs', () => expect(() => assertBingReadonlyRequirement(BING_READONLY_REQUIREMENT)).not.toThrow());

  it('accepts a visible citation containing the selected host without requiring exact host text', () => {
    const selected = { title: 'How Ocean Tides Work', host: 'ocean.example.org' };
    expect(() => assertBingReadonlyRows([
      { title: selected.title, website: 'ocean.example.org › science › tides' },
    ], {}, selected, 'replay')).not.toThrow();
    expect(() => assertBingReadonlyRows([
      { title: selected.title, website: 'different.example.org › tides' },
    ], {}, selected, 'replay')).toThrow(/citation/);
    expect(() => assertBingReadonlyRows([
      { title: selected.title, website: 'evilocean.example.org › tides' },
    ], {}, selected, 'replay')).toThrow(/citation/);
  });

  it('requires exact row-local title selection and rejects host/citation equality', () => {
    const base = {
      domain: 'www.bing.com',
      steps: [
        { action: 'type', value: '{{keyword}}' },
        { action: 'waitForElementVisible', target: { selector: '#b_results > .b_algo' } },
        { action: 'extract', name: 'items', multiple: true, fields: {} },
      ],
    };
    const selection = [
      { action: 'filter', from: 'extracted.items', name: 'title_matches', criteria: { field: 'title', op: 'eq', value: '{{target_title}}' } },
      { action: 'loop', type: 'forEach', items: '{{extracted.title_matches}}', as: 'item', steps: [
        { action: 'sendResult', payload: { title: '{{loopItem.title}}', website: '{{loopItem.website}}' } },
      ] },
    ];
    expect(() => assertBingReadonlyRule({ ...base, steps: [...base.steps, ...selection] })).not.toThrow();
    expect(() => assertBingReadonlyRule({
      ...base,
      steps: [...base.steps, {
        action: 'loop', type: 'forEach', items: '{{extracted.items}}', as: 'item', steps: [{
          action: 'if', condition: { type: 'elementNotExists', target: { text: '{{target_title}}' } }, else: [{
            action: 'if', condition: { type: 'elementNotExists', target: { text: '{{target_host}}' } }, else: [{
              action: 'sendResult', payload: { title: '{{loopItem.title}}', website: '{{loopItem.website}}' },
            }],
          }],
        }],
      }],
    })).toThrow(/filter extracted title/);
    expect(() => assertBingReadonlyRule({ ...base, steps: [...base.steps, ...selection.slice(0, 1),
      { action: 'filter', from: 'extracted.title_matches', name: 'selected', criteria: { field: 'website', op: 'eq', value: '{{target_host}}' } },
      { ...selection[1], items: '{{extracted.selected}}' },
    ] })).toThrow(/visible citation/);
  });

  it('accepts an exact-query results navigation when client-side Enter emits no form event', () => {
    const resultURL = `https://www.bing.com/search?q=${encodeURIComponent(BING_READONLY_QUERY)}`;
    expect(() => assertBingReadonlyRecording(recording([
      { type: 'inputText', index: 7, text: BING_READONLY_QUERY, timestamp: 1 },
      { type: 'navigate', url: resultURL, timestamp: 2 },
      { type: 'scroll', index: 259, direction: 'down', amount: 320, unit: 'pixels', timestamp: 3 },
    ]))).not.toThrow();
  });

  it('rejects navigation outside the exact Bing results boundary even before returning', () => {
    const resultURL = `https://www.bing.com/search?q=${encodeURIComponent(BING_READONLY_QUERY)}`;
    expect(() => assertBingReadonlyRecording(recording([
      { type: 'inputText', index: 7, text: BING_READONLY_QUERY, timestamp: 1 },
      { type: 'navigate', url: 'https://example.com/result', timestamp: 1.5 },
      { type: 'navigate', url: resultURL, timestamp: 2 },
      { type: 'scroll', index: 259, direction: 'down', amount: 320, unit: 'pixels', timestamp: 3 },
    ]))).toThrow(/exact results boundary/);
  });
});
