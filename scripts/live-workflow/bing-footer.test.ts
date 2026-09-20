import { describe, expect, it } from 'vitest';
import { BING_FOOTER_REQUIREMENT, assertBingFooterRequirement, assertBingFooterRows, assertBingFooterRule, footerDestination } from './bing-footer';
describe('Bing footer roundtrip contract', () => {
  it('accepts only HTTPS destinations', () => { expect(footerDestination('https://www.microsoft.com/legal')).toBe('www.microsoft.com'); expect(footerDestination('http://example.com')).toBeUndefined(); });
  it('binds exact inputs and row identity', () => { expect(() => assertBingFooterRequirement(BING_FOOTER_REQUIREMENT)).not.toThrow(); expect(() => assertBingFooterRows([{ text: 'Legal', website: 'www.microsoft.com' }], {}, { text: 'Legal', host: 'www.microsoft.com' }, 'replay')).not.toThrow(); });
  it('rejects positional or unsafe rules', () => { expect(() => assertBingFooterRule({ domain: ['bing.com'], steps: [{ action: 'waitForElementVisible', target: { text: '{{target_text}}' } }, { value: '{{target_host}}' }] })).not.toThrow(); expect(() => assertBingFooterRule({ steps: [{ index: 0, value: '{{target_text}}{{target_host}}' }] })).toThrow(); });
});
