/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { convert } from './PageAgentToDslConverter';
import type { PageAgentRecording } from '../types';

function makeRecording(): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: {
      startUrl: 'https://mall.example.com/search?q=手机',
      title: '商品搜索',
      recordedAt: '2026-07-04T10:00:00Z',
      domain: 'mall.example.com',
    },
    events: [
      { type: 'navigate', url: 'https://mall.example.com/search?q=手机', timestamp: 1 },
      { type: 'inputText', index: 5, text: '手机', timestamp: 2 },
      { type: 'click', index: 12, timestamp: 3 },
      { type: 'scroll', direction: 'down', amount: 1, unit: 'pages', timestamp: 4 },
      { type: 'extract', index: 20, fieldName: 'title', mode: 'text', timestamp: 5 },
    ],
    snapshots: [
      {
        timestamp: 1,
        url: 'https://mall.example.com/search?q=手机',
        selectorMap: {
          5: { index: 5, tagName: 'input', selector: '#search-input', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
          12: { index: 12, tagName: 'button', selector: '.search-btn', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
          20: { index: 20, tagName: 'h3', selector: '.product-title', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
        },
      },
    ],
  };
}

describe('PageAgentToDslConverter', () => {
  it('converts a full recording to a Rule', () => {
    const rule = convert(makeRecording());
    expect(rule.id).toMatch(/^recorded-mall.example.com-/);
    expect(rule.name).toBe('录制-商品搜索');
    expect(rule.entry).toBe('https://mall.example.com/search?q=手机');
    expect(rule.variables).toEqual({ keyword: '手机' });
    expect(rule.selectors).toEqual({
      el1: { selector: '#search-input', position: { x: 5, y: 5 } },
      el2: { selector: '.search-btn', position: { x: 5, y: 5 } },
      el3: { selector: '.product-title', position: { x: 5, y: 5 } },
    });
    expect(rule.steps).toHaveLength(5);
    expect(rule.steps[0]).toEqual({ action: 'navigate', url: 'https://mall.example.com/search?q={{keyword}}', waitUntil: 'load' });
    expect(rule.steps[1]).toEqual({ action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}' });
    expect(rule.steps[3]).toEqual({ action: 'scrollBy', direction: 'down', distance: 1, unit: 'pages' });
    expect(rule.steps[4]).toEqual({ action: 'extractText', name: 'title', target: { $ref: 'el3' } });
  });

  it('preserves indexed container scroll targets', () => {
    const recording = makeRecording();
    recording.events = [{
      type: 'scroll',
      direction: 'right',
      amount: 240,
      unit: 'pixels',
      index: 30,
      timestamp: 2,
    }];
    recording.snapshots[0].selectorMap[30] = {
      index: 30,
      tagName: 'div',
      selector: '#horizontal-results',
      boundingRect: { x: 0, y: 0, width: 300, height: 100 },
    };

    const rule = convert(recording);
    expect(rule.selectors).toEqual({ el1: { selector: '#horizontal-results', position: { x: 150, y: 50 } } });
    expect(rule.steps).toEqual([{
      action: 'scrollBy',
      direction: 'right',
      distance: 240,
      target: { $ref: 'el1' },
    }]);
  });

  it('merges submitForm into preceding inputText with the same index', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/search', timestamp: 1 },
      { type: 'inputText', index: 5, text: '手机', timestamp: 2 },
      { type: 'submitForm', index: 5, timestamp: 3 },
    ];
    const rule = convert(recording);
    expect(rule.steps).toHaveLength(2);
    expect(rule.steps[0]).toEqual({ action: 'navigate', url: 'https://mall.example.com/search', waitUntil: 'load' });
    expect(rule.steps[1]).toEqual({ action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}', submit: true });
  });

  it('coalesces slow incremental typing before variable inference and submission', () => {
    const recording = makeRecording();
    recording.meta.startUrl = 'https://www.scrapethissite.com/pages/forms/';
    recording.meta.domain = 'www.scrapethissite.com';
    recording.events = [
      { type: 'click', index: 5, timestamp: 1 },
      { type: 'inputText', index: 5, text: 'N', timestamp: 2 },
      { type: 'inputText', index: 5, text: 'Ne', timestamp: 3 },
      { type: 'inputText', index: 5, text: 'New', timestamp: 4 },
      { type: 'inputText', index: 5, text: 'New ', timestamp: 5 },
      { type: 'inputText', index: 5, text: 'New Y', timestamp: 6 },
      { type: 'inputText', index: 5, text: 'New York', timestamp: 7 },
      { type: 'submitForm', index: 5, timestamp: 8 },
      {
        type: 'navigate',
        url: 'https://www.scrapethissite.com/pages/forms/?q=New+York',
        timestamp: 9,
      },
    ];
    recording.snapshots[0].url = recording.meta.startUrl;

    const rule = convert(recording);

    expect(rule.variables).toEqual({ keyword: 'New York' });
    expect(rule.steps).toEqual([
      { action: 'click', target: { $ref: 'el1' } },
      { action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}', submit: true },
      {
        action: 'navigate',
        url: 'https://www.scrapethissite.com/pages/forms/?q={{keyword}}',
        waitUntil: 'load',
      },
    ]);
  });

  it('preserves edits, distinct controls, and intervening action boundaries', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'inputText', index: 5, text: 'New', timestamp: 1 },
      { type: 'inputText', index: 5, text: 'Net', timestamp: 2 },
      { type: 'inputText', index: 12, text: 'Network', timestamp: 3 },
      { type: 'click', index: 12, timestamp: 4 },
      { type: 'inputText', index: 5, text: 'Network', timestamp: 5 },
    ];

    const rule = convert(recording, { guessVariables: false });

    expect(rule.steps).toEqual([
      { action: 'type', target: { $ref: 'el1' }, value: 'New' },
      { action: 'type', target: { $ref: 'el1' }, value: 'Net' },
      { action: 'type', target: { $ref: 'el2' }, value: 'Network' },
      { action: 'click', target: { $ref: 'el2' } },
      { action: 'type', target: { $ref: 'el1' }, value: 'Network' },
    ]);
  });

  it('keeps standalone submitForm as pressKey when no preceding inputText matches', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/search', timestamp: 1 },
      { type: 'submitForm', index: 5, timestamp: 2 },
    ];
    const rule = convert(recording);
    expect(rule.steps).toHaveLength(2);
    expect(rule.steps[0]).toEqual({ action: 'navigate', url: 'https://mall.example.com/search', waitUntil: 'load' });
    expect(rule.steps[1]).toEqual({ action: 'pressKey', keys: ['Enter'] });
  });

  it('keeps submitForm when preceding inputText has a different index', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/search', timestamp: 1 },
      { type: 'inputText', index: 5, text: '手机', timestamp: 2 },
      { type: 'submitForm', index: 12, timestamp: 3 },
    ];
    const rule = convert(recording);
    expect(rule.steps).toHaveLength(3);
    expect(rule.steps[1]).toEqual({ action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}' });
    expect(rule.steps[2]).toEqual({ action: 'pressKey', keys: ['Enter'] });
  });

  it('templatizes select option values', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/search', timestamp: 1 },
      { type: 'selectOption', index: 5, optionText: '手机', timestamp: 2 },
    ];
    const rule = convert(recording);
    expect(rule.steps).toHaveLength(2);
    expect(rule.steps[1]).toEqual({ action: 'select', target: { $ref: 'el1' }, value: '{{keyword}}', by: 'text' });
  });

  it('uses ruleIdPrefix when provided', () => {
    const rule = convert(makeRecording(), { ruleIdPrefix: 'custom' });
    expect(rule.id).toMatch(/^custom-\d+$/);
  });

  it('derives an ordered deduplicated domain set from recorded HTTPS pages', () => {
    const recording = makeRecording();
    recording.events.push(
      { type: 'navigate', url: 'https://Science.NASA.gov/eclipses', timestamp: 6 },
      { type: 'navigate', url: 'https://mall.example.com/search?q=back', timestamp: 7 },
    );
    recording.snapshots.push({
      timestamp: 6,
      url: 'https://science.nasa.gov/eclipses',
      selectorMap: {},
    });

    expect(convert(recording).domain).toEqual(['mall.example.com', 'science.nasa.gov']);
  });

  it('does not derive domains from unsafe or malformed recording URLs', () => {
    const recording = makeRecording();
    recording.events.push(
      { type: 'navigate', url: 'http://insecure.example/path', timestamp: 6 },
      { type: 'navigate', url: 'https://user:secret@credential.example/path', timestamp: 7 },
      { type: 'navigate', url: 'not a url', timestamp: 8 },
    );

    expect(convert(recording).domain).toBe('mall.example.com');
  });

  it('retains the owned-fixture loopback HTTP exception', () => {
    const recording = makeRecording();
    recording.meta.startUrl = 'http://127.0.0.1:43123/fixture';
    recording.events = [{ type: 'navigate', url: recording.meta.startUrl, timestamp: 1 }];
    recording.snapshots = [{ timestamp: 1, url: recording.meta.startUrl, selectorMap: {} }];

    expect(convert(recording).domain).toBe('127.0.0.1');
  });

  it('derives entry from the earliest safe recorded page when metadata is transient', () => {
    const recording = makeRecording();
    recording.meta.startUrl = 'about:blank';
    recording.events = [
      { type: 'navigate', url: 'chrome-extension://example/popup.html', timestamp: 1 },
      { type: 'navigate', url: 'https://www.bing.com/', timestamp: 3 },
      { type: 'navigate', url: 'https://science.nasa.gov/eclipses/', timestamp: 5 },
    ];
    recording.snapshots = [
      { timestamp: 2, url: 'https://www.bing.com/', selectorMap: {} },
      { timestamp: 4, url: 'https://www.bing.com/search?q=eclipse', selectorMap: {} },
    ];

    expect(convert(recording).entry).toBe('https://www.bing.com/');
  });

  it('keeps a query-bearing recovered entry literal when its value is inferred as a variable', () => {
    const recording = makeRecording();
    recording.meta.startUrl = 'about:blank';
    recording.events = [
      { type: 'inputText', index: 1, text: 'solar-eclipses', timestamp: 1 },
      {
        type: 'navigate',
        url: 'https://www.bing.com/search?q=solar-eclipses',
        timestamp: 2,
      },
    ];
    recording.snapshots = [{
      timestamp: 1,
      url: 'https://www.bing.com/search?q=solar-eclipses',
      selectorMap: {
        1: {
          index: 1,
          tagName: 'input',
          selector: '#search-input',
          boundingRect: { x: 0, y: 0, width: 10, height: 10 },
        },
      },
    }];

    const rule = convert(recording);

    expect(rule.variables?.keyword).toBe('solar-eclipses');
    expect(rule.entry).toBe('https://www.bing.com/search?q=solar-eclipses');
  });

  it('does not choose credential-bearing or non-HTTP fallback entries', () => {
    const recording = makeRecording();
    recording.meta.startUrl = 'about:blank';
    recording.events = [
      { type: 'navigate', url: 'file:///private/data', timestamp: 1 },
      { type: 'navigate', url: 'https://user:secret@example.com/private', timestamp: 2 },
    ];
    recording.snapshots = [{ timestamp: 3, url: 'chrome-extension://example/popup.html', selectorMap: {} }];

    expect(convert(recording).entry).toBe('about:blank');
  });

  it('skips variable guessing when guessVariables is false', () => {
    const rule = convert(makeRecording(), { guessVariables: false });
    expect(rule.variables).toEqual({});
    expect(rule.steps[1]).toEqual({ action: 'type', target: { $ref: 'el1' }, value: '手机' });
  });

  it('uses annotated converter when events have annotations', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'click', index: 12, annotation: 'nextPage', timestamp: 1 },
      { type: 'extract', index: 20, fieldName: 'title', mode: 'text', annotation: 'listItem', timestamp: 2 },
    ];
    const rule = convert(recording);
    expect(rule.steps.some((s) => s.action === 'loop')).toBe(true);
  });

  it('converts a Baidu-style search recording with result page navigation', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: {
        startUrl: 'https://www.baidu.com/',
        title: '百度一下，你就知道',
        recordedAt: '2026-07-04T10:00:00Z',
        domain: 'www.baidu.com',
      },
      events: [
        { type: 'navigate', url: 'https://www.baidu.com/', timestamp: 1 },
        { type: 'inputText', index: 1, text: 'opencrawler', timestamp: 2 },
        { type: 'submitForm', index: 1, timestamp: 3 },
        { type: 'navigate', url: 'https://www.baidu.com/s?wd=opencrawler', timestamp: 4 },
        { type: 'scroll', direction: 'down', amount: 1, unit: 'pages', timestamp: 5 },
        { type: 'extract', index: 2, fieldName: 'title', mode: 'text', timestamp: 6 },
      ],
      snapshots: [
        {
          timestamp: 1,
          url: 'https://www.baidu.com/',
          selectorMap: {
            1: { index: 1, tagName: 'input', selector: '#kw', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
          },
        },
        {
          timestamp: 4,
          url: 'https://www.baidu.com/s?wd=opencrawler',
          selectorMap: {
            2: { index: 2, tagName: 'h3', selector: '.result h3', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
          },
        },
      ],
    };
    const rule = convert(recording);
    expect(rule.entry).toBe('https://www.baidu.com/');
    expect(rule.variables).toEqual({ keyword: 'opencrawler' });
    expect(rule.steps).toEqual([
      { action: 'navigate', url: 'https://www.baidu.com/', waitUntil: 'load' },
      { action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}', submit: true },
      { action: 'navigate', url: 'https://www.baidu.com/s?wd={{keyword}}', waitUntil: 'load' },
      { action: 'scrollBy', direction: 'down', distance: 1, unit: 'pages' },
      { action: 'extractText', name: 'title', target: { $ref: 'el2' } },
    ]);
  });
});
