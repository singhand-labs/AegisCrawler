import { describe, expect, it } from 'vitest';
import { BoundedRedactor } from './bounded-redaction';

describe('BoundedRedactor', () => {
  it('redacts secrets split across arbitrary stream chunks', () => {
    const redactor = new BoundedRedactor(200, ['sk-live-secret', 'forbidden-value']);
    for (const chunk of ['prefix sk-', 'live-', 'secret and forbidden', '-value suffix']) {
      redactor.append(chunk);
    }

    const rendered = redactor.render();
    expect(rendered).toBe('prefix [REDACTED] and [REDACTED] suffix');
    expect(rendered).not.toContain('sk-live-secret');
    expect(rendered).not.toContain('forbidden-value');
  });

  it('handles overlapping secret prefixes without retaining unbounded input', () => {
    const redactor = new BoundedRedactor(32, ['aaaaaaaa', 'aaaa']);
    for (const chunk of ['aa', 'aaaaa', 'aaaa', ' tail']) redactor.append(chunk);

    const rendered = redactor.render();
    expect(rendered.length).toBeLessThanOrEqual(32);
    expect(rendered).not.toContain('aaaaaaaa');
    expect(rendered).not.toContain('aaaa');
  });

  it('bounds the sanitized output after repeated secret contraction', () => {
    const redactor = new BoundedRedactor(24, ['a-very-long-sensitive-value']);
    for (let index = 0; index < 20; index += 1) {
      redactor.append(`line-${index}:a-very-long-sensitive-value\n`);
    }

    const rendered = redactor.render();
    expect(rendered.length).toBeLessThanOrEqual(24);
    expect(rendered).not.toContain('a-very-long-sensitive-value');
  });

  it('rejects an invalid output bound', () => {
    expect(() => new BoundedRedactor(0, ['secret'])).toThrow(/positive integer/);
  });
});
