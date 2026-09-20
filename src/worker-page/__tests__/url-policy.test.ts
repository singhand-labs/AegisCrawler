import { describe, it, expect } from 'vitest';
import { hostMatchesDomain, assertEntryUrlAllowed } from '../url-policy';

describe('url-policy', () => {
  describe('hostMatchesDomain', () => {
    it('matches exact host', () => {
      expect(hostMatchesDomain('example.com', ['example.com'])).toBe(true);
    });

    it('matches subdomain', () => {
      expect(hostMatchesDomain('www.example.com', ['example.com'])).toBe(true);
    });

    it('rejects unrelated host', () => {
      expect(hostMatchesDomain('other.com', ['example.com'])).toBe(false);
    });
  });

  describe('assertEntryUrlAllowed', () => {
    it('accepts http(s) URL with matching domain', () => {
      const url = assertEntryUrlAllowed('https://example.com/page', { domain: 'example.com' });
      expect(url.hostname).toBe('example.com');
    });

    it('rejects non-http scheme', () => {
      expect(() => assertEntryUrlAllowed('javascript:alert(1)', { domain: 'example.com' })).toThrow();
    });

    it('rejects mismatched domain', () => {
      expect(() => assertEntryUrlAllowed('https://evil.com/', { domain: 'example.com' })).toThrow();
    });

    it('rejects overly-broad rule domain like a bare TLD (M-1)', () => {
      // M-1: rule.domain='com' would match every .com host via suffix check.
      // Mirrors background.ts H-5 / replay-runner.ts L-1 / scriptcat-engine L-1.
      expect(() => assertEntryUrlAllowed('https://attacker.com/', { domain: 'com' })).toThrow(/overly-broad|过宽/i);
    });

    it('accepts the loopback domain entry localhost', () => {
      // '*.localhost' resolves to loopback in Chromium, so it adds no
      // public-site replay surface (canonical-alias fixtures rely on it).
      expect(() => assertEntryUrlAllowed('http://localhost:8080/entry', { domain: ['127.0.0.1', 'localhost'] })).not.toThrow();
    });

    it('rejects dot-prefixed domain entries', () => {
      expect(() => assertEntryUrlAllowed('https://example.com/', { domain: '.com' })).toThrow();
    });
  });
});
