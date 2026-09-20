import { describe, it, expect } from 'vitest';
import { redactString, hasSensitiveContent } from './client-redact';

describe('client-redact', () => {
  it('redacts email addresses', () => {
    expect(redactString('contact user@example.com for info')).toBe('contact [REDACTED] for info');
    expect(hasSensitiveContent('user@example.com')).toBe(true);
  });

  it('redacts 16-19 digit card numbers', () => {
    expect(redactString('card 4111111111111111 done')).toBe('card [REDACTED] done');
    expect(redactString('card 4111111111111111111 done')).toBe('card [REDACTED] done');
    // 15-digit numbers (e.g. Amex-ish) should NOT be redacted by the card pattern.
    expect(redactString('ref 123456789012345 end')).toBe('ref 123456789012345 end');
  });

  it('redacts credential key-value pairs', () => {
    expect(redactString('password=hunter2')).toBe('[REDACTED]');
    expect(redactString('api_key: abc123')).toBe('[REDACTED]');
    expect(redactString('token: "secret-value"')).toBe('[REDACTED]');
    expect(redactString('Authorization: Bearer xyz')).toBe('[REDACTED]');
  });

  it('redacts cookie / set-cookie header assignments', () => {
    expect(redactString('cookie: session=abc; Path=/')).toBe('[REDACTED]');
    expect(redactString('set-cookie: id=xyz')).toBe('[REDACTED]');
  });

  it('leaves clean strings unchanged', () => {
    const clean = 'Collect product titles and prices from example.com listings.';
    expect(redactString(clean)).toBe(clean);
    expect(hasSensitiveContent(clean)).toBe(false);
  });

  it('handles strings with multiple sensitive matches', () => {
    const input = 'email a@b.com and password=pw123 card 4111111111111111';
    const out = redactString(input);
    expect(out).not.toContain('a@b.com');
    expect(out).not.toContain('pw123');
    expect(out).not.toContain('4111111111111111');
    expect(hasSensitiveContent(input)).toBe(true);
  });
});
