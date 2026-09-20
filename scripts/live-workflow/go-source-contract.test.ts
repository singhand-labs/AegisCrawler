import { describe, expect, it } from 'vitest';
import { readGoRawStringConstant } from './go-source-contract';

describe('Go source contract reader', () => {
  it('is independent of neighboring declarations and whitespace', () => {
    expect(readGoRawStringConstant('const prompt=`one\ntwo`\nvar next = 1', 'prompt'))
      .toBe('one\ntwo');
    expect(readGoRawStringConstant('const prompt = `value`\n\nconst renamed = true', 'prompt'))
      .toBe('value');
  });

  it('rejects missing, unterminated, and unsafe names', () => {
    expect(() => readGoRawStringConstant('const other = `x`', 'prompt')).toThrow(/could not find/);
    expect(() => readGoRawStringConstant('const prompt = `x', 'prompt')).toThrow(/unterminated/);
    expect(() => readGoRawStringConstant('const prompt = `x`', 'prompt|other')).toThrow(/identifier/);
  });
});
