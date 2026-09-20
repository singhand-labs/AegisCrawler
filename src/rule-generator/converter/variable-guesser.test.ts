/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { guessVariables } from './variable-guesser';
import type { PageAgentRecording } from '../types';

function makeRecording(url: string, texts: string[] = []): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: { startUrl: url, title: 'T', recordedAt: '2026-07-04T10:00:00Z', domain: 'example.com' },
    events: texts.map((text, i) => ({ type: 'inputText', index: i, text, timestamp: i + 1 })),
    snapshots: [],
  };
}

describe('variable-guesser', () => {
  it('extracts keyword from URL query', () => {
    const result = guessVariables(makeRecording('https://example.com/search?q=手机'));
    expect(result.variables).toEqual({ keyword: '手机' });
    expect(result.templatize('手机')).toBe('{{keyword}}');
  });

  it('extracts common input text as keyword', () => {
    const result = guessVariables(makeRecording('https://example.com/search', ['手机', '手机']));
    expect(result.variables.keyword).toBe('手机');
  });

  it('extracts a normalized multi-word search phrase as one keyword', () => {
    const result = guessVariables(makeRecording('https://example.com/search', ['New York']));
    expect(result.variables).toEqual({ keyword: 'New York' });
    expect(result.templatize('https://example.com/search?q=New York')).toBe(
      'https://example.com/search?q={{keyword}}',
    );
  });

  it('rejects unnormalized or prose-like spaced input', () => {
    expect(guessVariables(makeRecording('https://example.com/search', ['New  York'])).variables)
      .toEqual({});
    expect(guessVariables(makeRecording('https://example.com/search', [' New York'])).variables)
      .toEqual({});
    expect(guessVariables(makeRecording('https://example.com/search', ['New York!'])).variables)
      .toEqual({});
  });

  it('templatizes URL with variable', () => {
    const result = guessVariables(makeRecording('https://example.com/search?q=手机'));
    expect(result.templatize('https://example.com/search?q=手机')).toBe('https://example.com/search?q={{keyword}}');
  });

  it('returns empty variables for an invalid start URL', () => {
    const result = guessVariables(makeRecording('not-a-valid-url'));
    expect(result.variables).toEqual({});
    expect(result.templatize('anything')).toBe('anything');
  });

  it('ignores input text that does not look like a variable', () => {
    const result = guessVariables(makeRecording('https://example.com/search', ['hello world!', 'hello world!']));
    expect(result.variables).toEqual({});
  });

  it('ignores overly long input text', () => {
    const long = 'a'.repeat(51);
    const result = guessVariables(makeRecording('https://example.com/search', [long, long]));
    expect(result.variables).toEqual({});
  });

  it('uses the URL query key name when it is not "q"', () => {
    const result = guessVariables(makeRecording('https://example.com/items?category=phones'));
    expect(result.variables).toEqual({ category: 'phones' });
    expect(result.templatize('https://example.com/items?category=phones')).toBe(
      'https://example.com/items?category={{category}}',
    );
  });

  it('templatizes multiple variables without collision', () => {
    const result = guessVariables(
      makeRecording('https://example.com/search?category=phones&q=手机', ['手机', '手机']),
    );
    expect(result.variables).toEqual({ category: 'phones', keyword: '手机' });
    expect(result.templatize('category=phones q=手机')).toBe('category={{category}} q={{keyword}}');
  });

  it('gives input-text keyword precedence over URL keyword when names collide', () => {
    const result = guessVariables(makeRecording('https://example.com/search?q=phone', ['手机', '手机']));
    expect(result.variables).toEqual({ keyword: '手机' });
    expect(result.templatize('phone手机')).toBe('phone{{keyword}}');
  });

  it('ignores empty URL query values', () => {
    const result = guessVariables(makeRecording('https://example.com/search?q='));
    expect(result.variables).toEqual({});
  });

  it('breaks ties in most common input by first occurrence', () => {
    const result = guessVariables(makeRecording('https://example.com/search', ['alpha', 'beta']));
    expect(result.variables.keyword).toBe('alpha');
  });

  it('prefers Baidu wd over non-search params like ie', () => {
    const result = guessVariables(
      makeRecording('https://www.baidu.com/s?ie=utf-8&f=8&wd=opencrawler&tn=baidu'),
    );
    expect(result.variables).toEqual({ keyword: 'opencrawler' });
    expect(result.templatize('wd=opencrawler')).toBe('wd={{keyword}}');
  });

  it('prefers search-related params in any order and skips short flags', () => {
    const result = guessVariables(makeRecording('https://example.com/s?foo=bar&q=phones&page=2'));
    // keyword is detected from the search param; foo=bar is a meaningful business variable;
    // page=2 is skipped because the value is too short to be a user variable.
    expect(result.variables).toEqual({ keyword: 'phones', foo: 'bar' });
  });

  it('does not extract sensitive URL params as variables (M-2)', () => {
    // M-2: session tokens, API keys, and OAuth params in the recording URL
    // must not be extracted as rule variables — they would leak real session
    // tokens into the generated rule file.
    const result = guessVariables(makeRecording('https://example.com/search?q=hello&sid=abc123&token=xyz456&access_token=eyJhbG'));
    expect(result.variables.sid).toBeUndefined();
    expect(result.variables.token).toBeUndefined();
    expect(result.variables.access_token).toBeUndefined();
    // Non-sensitive param still extracted.
    expect(result.variables.keyword).toBe('hello');
  });
});
