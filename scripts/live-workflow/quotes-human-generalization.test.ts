// @vitest-environment node
import { describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import {
  QUOTES_HUMAN_INPUT_CONTRACT,
  QUOTES_HUMAN_INPUT_TEXT,
  assertQuotesHumanChooseRecording,
  assertQuotesHumanInputRecording,
  deriveHumanChosenQuotesContract,
  isHumanChosenQuotesRequirement,
} from './quotes-human-generalization';

function recording(urls: string[], events: PageAgentRecording['events']): PageAgentRecording {
  const snapshots = urls.map((url, sequence) => ({
    timestamp: sequence + 1,
    url,
    selectorMap: {},
    phase: sequence === 0
      ? 'initial' as const
      : sequence === urls.length - 1 ? 'final' as const : 'before-action' as const,
    sequence,
    ...(sequence > 0 && sequence < urls.length - 1 ? { actionIndex: sequence - 1 } : {}),
    domTree: { type: 'element' as const, tagName: 'html', children: [] },
    capture: {
      status: 'complete' as const,
      nodeCount: 1,
      redactionCount: 0,
      removedNodeCount: 0,
      frames: [],
    },
  }));
  return {
    version: '2.0.0',
    meta: {
      title: 'Quotes',
      domain: 'quotes.toscrape.com',
      startUrl: urls[0],
      recordedAt: new Date().toISOString(),
      endedAt: new Date().toISOString(),
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v2',
    },
    limits: {
      maxActions: 500,
      maxDurationMs: 7_200_000,
      maxBytes: 64 * 1024 * 1024,
      warningThreshold: 0.8,
    },
    events,
    snapshots,
    termination: { reason: 'user', message: 'done', complete: true, timestamp: urls.length + 1 },
  };
}

describe('human-operation quote generalization contracts', () => {
  it('accepts a homepage-to-one-page human-choice recording', () => {
    const value = recording([
      'https://quotes.toscrape.com/',
      'https://quotes.toscrape.com/',
      'https://quotes.toscrape.com/tag/reading/',
    ], [{ type: 'click', timestamp: 2, index: 1 }]);
    expect(() => assertQuotesHumanChooseRecording(value)).not.toThrow();
    value.snapshots[2].url = 'https://quotes.toscrape.com/tag/reading/page/2/';
    expect(() => assertQuotesHumanChooseRecording(value)).toThrow(/page 1/);
  });

  it('accepts a direct two-page human-input recording', () => {
    const value = recording([
      'https://quotes.toscrape.com/tag/humor/',
      'https://quotes.toscrape.com/tag/humor/',
      'https://quotes.toscrape.com/tag/humor/page/2/',
    ], [{ type: 'click', timestamp: 2, index: 1 }]);
    expect(() => assertQuotesHumanInputRecording(value)).not.toThrow();
    value.snapshots[0].url = 'https://quotes.toscrape.com/';
    expect(() => assertQuotesHumanInputRecording(value)).toThrow();
  });

  it('derives only a semantically complete human-chosen quote-row contract', () => {
    const contract = deriveHumanChosenQuotesContract({
      requiredInputs: [],
      optionalInputs: [],
      outputFields: [
        { name: 'text', type: 'string', description: 'Quotation text' },
        { name: 'writer', type: 'string', description: 'Author name' },
        { name: 'profile', type: 'string', description: 'Author profile URL' },
      ],
    });
    expect(contract.outputMap).toEqual({
      text: 'quote',
      writer: 'author',
      profile: 'author_url',
    });
    expect(() => deriveHumanChosenQuotesContract({
      requiredInputs: [],
      optionalInputs: [],
      outputFields: [
        { name: 'tags', type: 'string', description: 'Quote tags' },
        { name: 'writer', type: 'string', description: 'Author name' },
      ],
    })).toThrow(/not quote, author, or author URL/);
    expect(isHumanChosenQuotesRequirement({
      requiredInputs: [],
      optionalInputs: [],
      outputFields: [
        { name: 'quote', type: 'string', description: 'Quotation text' },
        { name: 'author', type: 'string', description: 'Author name' },
      ],
    })).toBe(true);
    expect(isHumanChosenQuotesRequirement({
      requiredInputs: [],
      optionalInputs: [],
      outputFields: [{ name: 'quotes', type: 'array', description: 'Quote records' }],
    })).toBe(false);
  });

  it('pins the exact human-entered requirement and renamed contract', () => {
    expect(QUOTES_HUMAN_INPUT_TEXT).toContain('required string input named label');
    expect(QUOTES_HUMAN_INPUT_TEXT).toContain('maximum length 64');
    expect(QUOTES_HUMAN_INPUT_TEXT).toContain('exactly quotation, writer, and writer_url');
    expect(QUOTES_HUMAN_INPUT_CONTRACT.outputMap).toEqual({
      quotation: 'quote',
      writer: 'author',
      writer_url: 'author_url',
    });
  });
});
