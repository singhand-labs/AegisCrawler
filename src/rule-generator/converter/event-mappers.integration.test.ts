/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { mapEvent } from './event-mappers';
import { SelectorAliasRegistry, resolveTarget } from './selector-resolver';
import type { PageAgentEvent, PageAgentRecording } from '../types';

function makeRecording(): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: {
      startUrl: 'https://example.com/',
      title: 'Example',
      recordedAt: new Date().toISOString(),
      domain: 'example.com',
    },
    events: [],
    snapshots: [
      {
        timestamp: 0,
        url: 'https://example.com/',
        selectorMap: {
          1: {
            index: 1,
            tagName: 'button',
            selector: 'button.submit',
            text: 'Submit',
            boundingRect: { x: 10, y: 20, width: 100, height: 30 },
          },
          2: {
            index: 2,
            tagName: 'h1',
            selector: 'h1.title',
            text: 'Welcome',
            boundingRect: { x: 10, y: 60, width: 300, height: 40 },
          },
        },
      },
    ],
  };
}

function makeContext(recording: PageAgentRecording) {
  const registry = new SelectorAliasRegistry();
  return {
    registry,
    resolve: (index: number, timestamp: number) => resolveTarget(recording, index, timestamp, registry),
  };
}

describe('event-mappers integration', () => {
  it('maps click and extract events using real selector resolution and alias registry', () => {
    const recording = makeRecording();
    const ctx = makeContext(recording);

    const clickEvent: PageAgentEvent = { type: 'click', index: 1, timestamp: 100 };
    const extractEvent: PageAgentEvent = { type: 'extract', index: 2, fieldName: 'heading', mode: 'text', timestamp: 100 };

    const clickActions = mapEvent(clickEvent, ctx);
    const extractActions = mapEvent(extractEvent, ctx);

    expect(clickActions).toEqual([{ action: 'click', target: { $ref: 'el1' } }]);
    expect(extractActions).toEqual([{ action: 'extractText', name: 'heading', target: { $ref: 'el2' } }]);

    expect(ctx.registry.getAliases()).toEqual({
      el1: { selector: '.submit', text: 'Submit' },
      el2: { selector: '.title', text: 'Welcome' },
    });
  });
});
