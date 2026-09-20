import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import {
  RFC_EDITOR_ENTRY_URL, RFC_EDITOR_RECORDED_SECTION, RFC_EDITOR_REQUIREMENT,
  assertRFCEditorRecording, assertRFCEditorRequirement, assertRFCEditorRule,
  assertRFCEditorSection, assertRFCEditorURL, bindRFCEditorSection,
  canonicalRFCEditorRows, collectVisibleRFCEditorSection, parseRFCEditorSectionText,
  rfcEditorEnvironmentBlock, rfcEditorTOCLabelIndex,
} from './rfc-editor-section-readonly';

const original = { document: globalThis.document, getComputedStyle: globalThis.getComputedStyle, CSS: globalThis.CSS };
afterEach(() => Object.assign(globalThis, original));

function installDOM(body: string): void {
  const dom = new JSDOM(`<!doctype html><body>${body}</body>`, { url: `${RFC_EDITOR_ENTRY_URL}#section-3` });
  Object.assign(globalThis, {
    document: dom.window.document,
    getComputedStyle: dom.window.getComputedStyle.bind(dom.window),
    CSS: { escape: (value: string) => value },
  });
  for (const element of Array.from(dom.window.document.querySelectorAll('*'))) {
    Object.defineProperty(element, 'getBoundingClientRect', { configurable: true, value: () => ({ width: 100, height: 20 }) });
  }
}

describe('RFC Editor read-only section contract', () => {
  it('allows only the reviewed document and canonical sections', () => {
    expect(assertRFCEditorURL(`${RFC_EDITOR_ENTRY_URL}#section-3`, '3').hash).toBe('#section-3');
    for (const value of ['02', '3.0', '2/3', '5']) expect(() => assertRFCEditorSection(value)).toThrow();
    for (const url of ['http://www.rfc-editor.org/rfc/rfc2606.html#section-3', 'https://rfc-editor.org/rfc/rfc2606.html#section-3',
      'https://www.rfc-editor.org/rfc/rfc2606.txt#section-3', 'https://www.rfc-editor.org/rfc/rfc2606.html?q=x#section-3',
      'https://www.rfc-editor.org/rfc/rfc2606.html#section-3%2F4']) expect(() => assertRFCEditorURL(url, '3')).toThrow();
  });

  it('detects challenge, consent, traffic, and access boundaries', () => {
    for (const text of ['CAPTCHA', 'Verify you are human', 'Consent required', 'Unusual traffic', 'Access denied']) {
      expect(rfcEditorEnvironmentBlock(text)).toBeTruthy();
    }
    expect(rfcEditorEnvironmentBlock('Reserved Top Level DNS Names')).toBeUndefined();
  });

  it('selects the canonical section label and rejects its page-number alias', () => {
    const href = `${RFC_EDITOR_ENTRY_URL}#section-3`;
    expect(rfcEditorTOCLabelIndex([
      { href, text: '3', beforeTarget: true, insideTarget: false },
      { href, text: '3', beforeTarget: false, insideTarget: true },
    ], '3')).toBe(0);
    expect(rfcEditorTOCLabelIndex([
      { href, text: '3.', beforeTarget: true, insideTarget: false },
      { href, text: '2', beforeTarget: true, insideTarget: false },
    ], '3')).toBe(0);
    expect(() => rfcEditorTOCLabelIndex([
      { href, text: '3', beforeTarget: false, insideTarget: true },
    ], '3')).toThrow(/eligible.*0/);
    expect(() => rfcEditorTOCLabelIndex([
      { href, text: '3', beforeTarget: true, insideTarget: false },
      { href, text: ' 3. ', beforeTarget: true, insideTarget: false },
    ], '3')).toThrow(/eligible.*2/);
    expect(() => rfcEditorTOCLabelIndex([
      { href: 'https://example.org/#section-3', text: '3', beforeTarget: true, insideTarget: false },
    ], '3')).toThrow(/exactHref.*0/);
    expect(() => rfcEditorTOCLabelIndex([
      { href, text: '3', beforeTarget: true, insideTarget: false },
    ], '3', 0)).toThrow(/targetCount.*0/);
  });

  it('extracts one bounded preformatted section and rejects hostile fixtures', () => {
    installDOM(`<pre><a id="section-3">3</a>. Reserved Example Second Level DNS Names

   First example paragraph wraps across
   two lines.

   Second paragraph.

<a id="section-4">4</a>. IANA Considerations

   Next section prose.</pre>`);
    expect(collectVisibleRFCEditorSection('3')).toEqual([{ section_number: '3', heading: 'Reserved Example Second Level DNS Names', first_paragraph: 'First example paragraph wraps across two lines.' }]);
    const serialized = (Function(
      `"use strict"; return (${collectVisibleRFCEditorSection.toString()});`,
    )() as unknown) as typeof collectVisibleRFCEditorSection;
    expect(serialized('3')).toEqual([{ section_number: '3', heading: 'Reserved Example Second Level DNS Names', first_paragraph: 'First example paragraph wraps across two lines.' }]);
    installDOM('<pre hidden><a id="section-3">3</a>. Hidden\n\nSecret\n\n<a id="section-4">4</a>. Next</pre>');
    expect(collectVisibleRFCEditorSection('3')).toEqual([]);
    installDOM('<pre><a id="section-3">3</a>. One\n\nText\n<a id="section-3">3</a>. Two\n\nText\n<a id="section-4">4</a>. Next</pre>');
    expect(collectVisibleRFCEditorSection('3')).toEqual([]);
    installDOM('<pre><a id="section-3">3</a>. Empty\n\n   \n\n<a id="section-4">4</a>. Next</pre>');
    expect(collectVisibleRFCEditorSection('3')).toEqual([]);
    installDOM('<pre><a id="section-3">3</a>. Missing boundary\n\nText</pre>');
    expect(collectVisibleRFCEditorSection('3')).toEqual([]);
    expect(parseRFCEditorSectionText('3 Reserved punctuation\n\nText', '3')).toEqual([]);
    expect(parseRFCEditorSectionText('3. Heading\n\n', '3')).toEqual([]);
  });

  it('locks requirement, input, row, and generated-rule contracts', () => {
    expect(() => assertRFCEditorRequirement(RFC_EDITOR_REQUIREMENT)).not.toThrow();
    expect(bindRFCEditorSection({ section_number: '3' }, '2')).toEqual({ section_number: '2' });
    expect(() => bindRFCEditorSection({ section_number: '3', extra: 'x' }, '2')).toThrow();
    expect(canonicalRFCEditorRows([{ section_number: '3' }], '3', 'test')).toHaveLength(1);
    expect(() => canonicalRFCEditorRows([{ section_number: '3', heading: 'Heading' }], '3', 'test')).toThrow(/schema drift/);
    const rule = { domain: 'www.rfc-editor.org', entry: RFC_EDITOR_ENTRY_URL, steps: [
      { action: 'navigate', url: `${RFC_EDITOR_ENTRY_URL}#section-{{section_number}}` },
      ...['2', '3', '4'].map((section) => ({
        action: 'extractText', name: 'section_number',
        target: { selector: `#section-${section}`, visible: true },
        condition: { type: 'urlMatches', pattern: `#section-${section}$` },
      })),
      { action: 'sendResult', payload: {} },
    ] };
    expect(() => assertRFCEditorRule(rule)).not.toThrow();
    expect(() => assertRFCEditorRule({ ...rule, steps: [
      rule.steps[0],
      { action: 'extractText', name: 'section_number', target: { selector: '#section-3', visible: true } },
      rule.steps.at(-1),
    ] })).toThrow(/one guarded.*per reviewed section/);
    expect(() => assertRFCEditorRule({ ...rule, steps: [
      ...rule.steps.slice(0, 2),
      { ...rule.steps[2], condition: { type: 'urlMatches', pattern: '#section-4$' } },
      ...rule.steps.slice(3),
    ] })).toThrow(/guard must match/);
    expect(() => assertRFCEditorRule({ ...rule, steps: [...rule.steps, { action: 'extract', index: 0 }] })).toThrow();
  });

  it('requires complete one-click fragment-navigation recording with scrolling', () => {
    const value = { version: '2.0.0', meta: { startUrl: RFC_EDITOR_ENTRY_URL }, termination: { complete: true },
      snapshots: [{ capture: { status: 'complete' } }], events: [
        { type: 'click', timestamp: 1, index: 1 },
        { type: 'navigate', timestamp: 2, url: `${RFC_EDITOR_ENTRY_URL}#section-${RFC_EDITOR_RECORDED_SECTION}` },
        { type: 'scroll', timestamp: 3, x: 0, y: 260 },
      ] } as unknown as PageAgentRecording;
    expect(() => assertRFCEditorRecording(value)).not.toThrow();
    expect(() => assertRFCEditorRecording({ ...value, events: value.events.filter((event) => event.type !== 'scroll') })).toThrow(/scroll/);
    expect(() => assertRFCEditorRecording({ ...value, events: [...value.events, { type: 'navigate', timestamp: 4, url: 'https://example.org/' } as never] })).toThrow();
  });
});
