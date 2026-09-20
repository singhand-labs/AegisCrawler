/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { convert } from './PageAgentToDslConverter';
import type { PageAgentRecording } from '../types';

function makeRecording(): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: {
      startUrl: 'https://mall.example.com/list',
      title: '商品列表',
      recordedAt: '2026-07-04T10:00:00Z',
      domain: 'mall.example.com',
    },
    events: [
      { type: 'navigate', url: 'https://mall.example.com/list', timestamp: 1 },
      { type: 'extract', index: 10, fieldName: 'title', mode: 'text', annotation: 'listItem', timestamp: 2 },
      { type: 'extract', index: 10, fieldName: 'price', mode: 'text', annotation: 'listItem', timestamp: 3 },
      { type: 'click', index: 20, annotation: 'nextPage', timestamp: 4 },
    ],
    snapshots: [
      {
        timestamp: 1,
        url: 'https://mall.example.com/list',
        selectorMap: {
          10: { index: 10, tagName: 'div', selector: '.product-card', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
          20: { index: 20, tagName: 'a', selector: '.next-page', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
        },
      },
    ],
  };
}

describe('annotated conversion', () => {
  it('generates a loop with multiple extract for listItem annotations', () => {
    const rule = convert(makeRecording());
    expect(rule.steps).toHaveLength(2); // navigate + loop
    expect(rule.steps[0]).toMatchObject({ action: 'navigate' });

    const loop = rule.steps[1] as any;
    expect(loop.action).toBe('loop');
    expect(loop.type).toBe('whileElementExists');
    expect(loop.steps).toHaveLength(3); // extract + click next + wait

    const extract = loop.steps[0];
    expect(extract.action).toBe('extract');
    expect(extract.multiple).toBe(true);
    expect(extract.fields).toHaveProperty('title');
    expect(extract.fields).toHaveProperty('price');
  });

  it('generates field extraction without loop', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/detail', timestamp: 1 },
      { type: 'extract', index: 10, fieldName: 'title', mode: 'text', annotation: 'field', timestamp: 2 },
      { type: 'extract', index: 10, fieldName: 'sku', mode: 'text', annotation: 'field', timestamp: 3 },
    ];
    const rule = convert(recording);
    expect(rule.steps).toHaveLength(2);
    const extract = rule.steps[1] as any;
    expect(extract.action).toBe('extract');
    expect(extract.multiple).toBe(false);
    expect(extract.fields).toHaveProperty('title');
    expect(extract.fields).toHaveProperty('sku');
  });

  it('maps html and attribute extraction modes', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'extract', index: 10, fieldName: 'markup', mode: 'html', annotation: 'field', timestamp: 1 },
      { type: 'extract', index: 10, fieldName: 'link', mode: 'attribute', attribute: 'data-url', annotation: 'field', timestamp: 2 },
      { type: 'extract', index: 10, fieldName: 'fallbackLink', mode: 'attribute', annotation: 'field', timestamp: 3 },
    ];

    const rule = convert(recording);
    const extract = rule.steps[0] as any;

    expect(extract.fields).toEqual({
      markup: { type: 'html' },
      link: { type: 'attr', attr: 'data-url' },
      fallbackLink: { type: 'attr', attr: 'href' },
    });
  });

  it('preserves ordinary actions after an extract group', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'extract', index: 10, fieldName: 'title', mode: 'text', annotation: 'field', timestamp: 1 },
      { type: 'scroll', direction: 'down', amount: 2, unit: 'pages', timestamp: 2 },
    ];

    const rule = convert(recording);

    expect(rule.steps).toHaveLength(2);
    expect(rule.steps[0]).toMatchObject({ action: 'extract', name: 'fields' });
    expect(rule.steps[1]).toMatchObject({ action: 'scrollBy', direction: 'down', distance: 2, unit: 'pages' });
  });

  it('generates login requestHuman branch', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/login', timestamp: 1 },
      { type: 'click', index: 10, annotation: 'login', timestamp: 2 },
    ];
    const rule = convert(recording);
    const branch = rule.steps[1] as any;
    expect(branch.action).toBe('if');
    expect(branch.condition.type).toBe('elementExists');
    expect(branch.then[0].action).toBe('requestHuman');
  });

  it('generates captcha solveCaptcha branch', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/login', timestamp: 1 },
      { type: 'click', index: 10, annotation: 'captcha', timestamp: 2 },
    ];
    const rule = convert(recording);
    const branch = rule.steps[1] as any;
    expect(branch.action).toBe('if');
    expect(branch.then[0].action).toBe('solveCaptcha');
  });

  it('falls back to linear mapping when no annotations', () => {
    const recording = makeRecording();
    recording.events = [
      { type: 'navigate', url: 'https://mall.example.com/list', timestamp: 1 },
      { type: 'click', index: 20, timestamp: 2 },
    ];
    const rule = convert(recording);
    expect(rule.steps).toHaveLength(2);
    expect(rule.steps[0].action).toBe('navigate');
    expect(rule.steps[1].action).toBe('click');
  });
});
