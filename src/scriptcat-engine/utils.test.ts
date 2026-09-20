/// <reference types="vitest/globals" />
import {
  interpolate,
  resolveExpression,
  resolveSelectorAlias,
  parseRetry,
  randomBetween,
  toNumber,
  sleep,
} from './utils';

const baseCtx = {
  variables: { name: 'Alice', nested: { age: 30 } },
  extracted: { items: [{ id: 1 }, { id: 2 }] },
  evaluated: { score: 95 },
  captured: {},
  loopIndex: 2,
  loopItem: { title: 'B' },
};

describe('utils - interpolate', () => {
  it('interpolates template variables', () => {
    expect(interpolate('Hello {{variables.name}}', baseCtx)).toBe('Hello Alice');
  });

  it('interpolates extracted paths', () => {
    expect(interpolate('Count: {{extracted.items.length}}', baseCtx)).toBe('Count: 2');
  });

  it('interpolates loopIndex and loopItem', () => {
    expect(interpolate('idx={{loopIndex}} item={{loopItem.title}}', baseCtx)).toBe('idx=2 item=B');
  });

  it('returns null/undefined as-is', () => {
    expect(interpolate(null, baseCtx)).toBeNull();
    expect(interpolate(undefined, baseCtx)).toBeUndefined();
  });

  it('interpolates arrays recursively', () => {
    expect(interpolate(['{{variables.name}}', '{{evaluated.score}}'], baseCtx)).toEqual(['Alice', 95]);
  });

  it('interpolates objects recursively', () => {
    expect(interpolate({ greeting: 'Hi {{variables.name}}' }, baseCtx)).toEqual({ greeting: 'Hi Alice' });
  });

  it('returns non-string primitives unchanged', () => {
    expect(interpolate(42, baseCtx)).toBe(42);
    expect(interpolate(true, baseCtx)).toBe(true);
  });

  it('falls back to empty string when exact placeholder resolves to undefined', () => {
    expect(interpolate('{{variables.missing}}', baseCtx)).toBe('');
  });

  it('falls back to empty string when inline placeholder resolves to undefined', () => {
    expect(interpolate('val={{variables.missing}}', baseCtx)).toBe('val=');
  });
});

describe('utils - resolveExpression', () => {
  it('resolves variable paths', () => {
    expect(resolveExpression('variables.name', baseCtx)).toBe('Alice');
    expect(resolveExpression('variables.nested.age', baseCtx)).toBe(30);
  });

  it('resolves loop special variables', () => {
    expect(resolveExpression('loopIndex', baseCtx)).toBe(2);
    expect(resolveExpression('loopItem.title', baseCtx)).toBe('B');
  });

  it('falls back to variables/extracted/evaluated when no namespace', () => {
    expect(resolveExpression('name', baseCtx)).toBe('Alice');
    expect(resolveExpression('score', baseCtx)).toBe(95);
  });

  it('returns undefined for missing paths', () => {
    expect(resolveExpression('variables.missing.deep', baseCtx)).toBeUndefined();
  });

  it('falls back to bare name with subkey', () => {
    expect(resolveExpression('items', baseCtx)).toEqual([{ id: 1 }, { id: 2 }]);
  });

  it('resolves nested path on bare variable name', () => {
    const ctx = { variables: { myVar: { deep: 42 } }, extracted: {}, evaluated: {}, captured: {} };
    expect(resolveExpression('myVar.deep', ctx)).toBe(42);
  });
});

describe('utils - resolveSelectorAlias', () => {
  it('returns target unchanged when no $ref', () => {
    const t = { selector: '#a' };
    expect(resolveSelectorAlias(t)).toBe(t);
  });

  it('resolves $ref selector alias', () => {
    const selectors = { card: { selector: '.card' } };
    expect(resolveSelectorAlias({ $ref: 'card', index: 1 }, selectors)).toEqual({ selector: '.card', index: 1 });
  });

  it('throws when alias missing', () => {
    expect(() => resolveSelectorAlias({ $ref: 'missing' }, {})).toThrow('Selector alias not found: missing');
  });

  it('treats a blank alias reference as absent', () => {
    expect(resolveSelectorAlias({ $ref: '  ', selector: '#result' }, {})).toEqual({
      $ref: '  ',
      selector: '#result',
    });
  });
});

describe('utils - parseRetry', () => {
  it('uses default attempts when undefined', () => {
    expect(parseRetry(undefined, 3)).toEqual({ maxAttempts: 3 });
  });

  it('parses number as maxAttempts', () => {
    expect(parseRetry(5, 3)).toEqual({ maxAttempts: 5 });
  });

  it('preserves maxAttempts and other config fields', () => {
    expect(parseRetry({ delay: 100, backoff: 'linear', maxAttempts: 3 }, 2)).toEqual({ maxAttempts: 3, delay: 100, backoff: 'linear' });
  });

  it('uses default maxAttempts when config omits it', () => {
    expect(parseRetry({ delay: 100 } as any, 2)).toEqual({ maxAttempts: 2, delay: 100 });
  });
});

describe('utils - randomBetween', () => {
  it('returns value within range', () => {
    const v = randomBetween([10, 20]);
    expect(v).toBeGreaterThanOrEqual(10);
    expect(v).toBeLessThan(20);
  });
});

describe('utils - toNumber', () => {
  it('parses numbers from strings', () => {
    expect(toNumber('Price: $12.50')).toBe(12.5);
    expect(toNumber('42')).toBe(42);
    expect(toNumber('$1,049.00')).toBe(1049);
    expect(toNumber('USD 12,345')).toBe(12345);
    expect(toNumber('1\u202f249.50 USD')).toBe(1249.5);
  });

  it('returns null when no number found', () => {
    expect(toNumber('abc')).toBeNull();
  });

  it('returns number as-is', () => {
    expect(toNumber(7)).toBe(7);
  });

  it('returns null for non-string non-number', () => {
    expect(toNumber(null)).toBeNull();
    expect(toNumber({})).toBeNull();
  });
});

describe('utils - sleep', () => {
  it('resolves after ms', async () => {
    const start = Date.now();
    await sleep(30);
    expect(Date.now() - start).toBeGreaterThanOrEqual(25);
  });
});

describe('page namespace interpolation', () => {
  const ctx: any = {
    variables: {},
    extracted: {},
    evaluated: {},
    captured: {},
    page: { url: 'https://example.com/p', title: 'Hello' },
  };

  it('interpolates {{page.url}}', () => {
    expect(interpolate('current url: {{page.url}}', ctx)).toBe('current url: https://example.com/p');
  });

  it('interpolates {{page.title}}', () => {
    expect(interpolate('t: {{page.title}}', ctx)).toBe('t: Hello');
  });

  it('resolveExpression returns page.url value', () => {
    expect(resolveExpression('page.url', ctx)).toBe('https://example.com/p');
  });

  it('resolveExpression returns undefined when ctx.page absent', () => {
    const ctxNoPage: any = { variables: {}, extracted: {}, evaluated: {}, captured: {} };
    expect(resolveExpression('page.url', ctxNoPage)).toBeUndefined();
  });
});

describe('parseRetry maxCeiling', () => {
  it('returns parsed retry unchanged when maxCeiling is undefined', () => {
    expect(parseRetry(5, 0)).toEqual({ maxAttempts: 5 });
    expect(parseRetry(undefined, 3)).toEqual({ maxAttempts: 3 });
    expect(parseRetry({ maxAttempts: 10, delay: 100 }, 0)).toEqual({ maxAttempts: 10, delay: 100 });
  });

  it('clamps maxAttempts down to maxCeiling when retry exceeds', () => {
    expect(parseRetry(10, 0, 3)).toEqual({ maxAttempts: 3 });
    expect(parseRetry({ maxAttempts: 10, delay: 100 }, 0, 2)).toEqual({ maxAttempts: 2, delay: 100 });
  });

  it('does not inflate maxAttempts when retry is below maxCeiling', () => {
    expect(parseRetry(2, 0, 5)).toEqual({ maxAttempts: 2 });
  });

  it('clamps default fallback when maxCeiling is smaller', () => {
    expect(parseRetry(undefined, 5, 3)).toEqual({ maxAttempts: 3 });
  });

  it('preserves maxCeiling=0 semantics (disables retry entirely)', () => {
    expect(parseRetry(10, 5, 0)).toEqual({ maxAttempts: 0 });
  });
});
