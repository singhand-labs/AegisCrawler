/// <reference types="vitest/globals" />
import { describe, it, expect, vi } from 'vitest';
import { mapEvent } from './event-mappers';
import { SelectorAliasRegistry } from './selector-resolver';
import type { PageAgentEvent } from '../types';

function ctx() {
  const registry = new SelectorAliasRegistry();
  return {
    registry,
    resolve: (index: number, timestamp: number) => ({ $ref: `el${index}` }),
  };
}

describe('event-mappers', () => {
  it('maps navigate', () => {
    const event: PageAgentEvent = { type: 'navigate', url: 'https://example.com/', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'navigate', url: 'https://example.com/', waitUntil: 'load' }]);
  });

  it('maps click', () => {
    const event: PageAgentEvent = { type: 'click', index: 5, timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'click', target: { $ref: 'el5' } }]);
  });

  it('maps inputText', () => {
    const event: PageAgentEvent = { type: 'inputText', index: 3, text: 'hello', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'type', target: { $ref: 'el3' }, value: 'hello' }]);
  });

  it('maps inputText with submit flag', () => {
    const event: PageAgentEvent = { type: 'inputText', index: 3, text: 'hello', submit: true, timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'type', target: { $ref: 'el3' }, value: 'hello', submit: true }]);
  });

  it('maps submitForm without submitter to pressKey Enter', () => {
    const event: PageAgentEvent = { type: 'submitForm', index: 3, timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'pressKey', keys: ['Enter'] }]);
  });

  it('maps submitForm with submitter to click on submitter', () => {
    const event: PageAgentEvent = { type: 'submitForm', index: 3, submitterIndex: 5, timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'click', target: { $ref: 'el5' } }]);
  });

  it('maps selectOption', () => {
    const event: PageAgentEvent = { type: 'selectOption', index: 4, optionText: 'Option A', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'select', target: { $ref: 'el4' }, value: 'Option A', by: 'text' }]);
  });

  it('preserves page scroll direction and distance', () => {
    const event: PageAgentEvent = { type: 'scroll', direction: 'down', amount: 1, unit: 'pages', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{
      action: 'scrollBy',
      direction: 'down',
      distance: 1,
      unit: 'pages',
    }]);
  });

  it('maps scroll pixels to scrollBy', () => {
    const event: PageAgentEvent = { type: 'scroll', direction: 'down', amount: 300, unit: 'pixels', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'scrollBy', direction: 'down', distance: 300 }]);
  });

  it('preserves an indexed pixel scroll target', () => {
    const event: PageAgentEvent = {
      type: 'scroll',
      direction: 'right',
      amount: 120,
      unit: 'pixels',
      index: 7,
      timestamp: 1,
    };
    expect(mapEvent(event, ctx())).toEqual([{
      action: 'scrollBy',
      direction: 'right',
      distance: 120,
      target: { $ref: 'el7' },
    }]);
  });

  it('skips executeJavascript events for safety (M-1)', () => {
    // M-1: recorded JS scripts are not auto-converted to evaluate actions.
    // A crafted recording could inject arbitrary code into the rule, which
    // executes in worker-host mode with allowEvaluate=true.
    const event: PageAgentEvent = { type: 'executeJavascript', script: 'return document.title;', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([]);
  });

  it('maps wait', () => {
    const event: PageAgentEvent = { type: 'wait', duration: 500, timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'waitForTimeout', ms: 500 }]);
  });

  it('maps extract text', () => {
    const event: PageAgentEvent = { type: 'extract', index: 7, fieldName: 'title', mode: 'text', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'extractText', name: 'title', target: { $ref: 'el7' } }]);
  });

  it('maps extract html', () => {
    const event: PageAgentEvent = { type: 'extract', index: 8, fieldName: 'body', mode: 'html', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'extractHtml', name: 'body', target: { $ref: 'el8' } }]);
  });

  it('maps extract attribute with explicit attribute', () => {
    const event: PageAgentEvent = { type: 'extract', index: 9, fieldName: 'href', mode: 'attribute', attribute: 'href', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'extractAttribute', name: 'href', target: { $ref: 'el9' }, attr: 'href' }]);
  });

  it('maps extract attribute without explicit attribute to empty string', () => {
    const event: PageAgentEvent = { type: 'extract', index: 10, fieldName: 'data', mode: 'attribute', timestamp: 1 };
    expect(mapEvent(event, ctx())).toEqual([{ action: 'extractAttribute', name: 'data', target: { $ref: 'el10' }, attr: '' }]);
  });

  it('warns and returns empty array for unknown event type', () => {
    const event = { type: 'unknown', timestamp: 1 } as unknown as PageAgentEvent;
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    expect(mapEvent(event, ctx())).toEqual([]);
    expect(warnSpy).toHaveBeenCalledTimes(1);
    expect(warnSpy.mock.calls[0][0]).toContain('unknown event type');
    expect(warnSpy.mock.calls[0][0]).toContain('unknown');
    warnSpy.mockRestore();
  });

  it('invokes onUnknownEvent hook with type, event, and rule', () => {
    const event = { type: 'mysteryGesture', timestamp: 1 } as unknown as PageAgentEvent;
    const onUnknownEvent = vi.fn();
    const rule = { id: 'r1' } as any;
    expect(mapEvent(event, { ...ctx(), onUnknownEvent, rule })).toEqual([]);
    expect(onUnknownEvent).toHaveBeenCalledTimes(1);
    const [info] = onUnknownEvent.mock.calls[0];
    expect(info).toMatchObject({ type: 'mysteryGesture', event, rule });
  });

  it('does not throw when onUnknownEvent is absent (forward-compat)', () => {
    const event = { type: 'neverSeen', timestamp: 1 } as unknown as PageAgentEvent;
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    expect(() => mapEvent(event, ctx())).not.toThrow();
    warnSpy.mockRestore();
  });
});
