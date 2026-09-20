import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { SQLITE_DOCS_ENTRY_URL, SQLITE_DOCS_REQUIREMENT, SQLITE_LANGUAGE_URL, assertSQLiteRecording, assertSQLiteRequirement, assertSQLiteRule, assertSQLiteURL, bindSQLiteInputs, collectVisibleSQLiteDocument, sqliteEnvironmentBlock, sqliteLinkIndex } from './sqlite-docs-roundtrip';

const original = { document: globalThis.document, getComputedStyle: globalThis.getComputedStyle };
afterEach(() => Object.assign(globalThis, original));

describe('SQLite docs roundtrip contract', () => {
  it('allows only canonical reviewed URLs', () => {
    expect(assertSQLiteURL(SQLITE_DOCS_ENTRY_URL).pathname).toBe('/docs.html');
    for (const url of ['http://www.sqlite.org/docs.html', 'https://sqlite.org/docs.html', 'https://www.sqlite.org/docs.html?q=x', 'https://www.sqlite.org/lang.html#x', 'https://www.sqlite.org/download.html']) expect(() => assertSQLiteURL(url)).toThrow();
  });
  it('selects one exact safe visible link', () => {
    expect(sqliteLinkIndex([{ href: SQLITE_LANGUAGE_URL, text: ' SQL Syntax ', visible: true, download: false }])).toBe(0);
    for (const candidate of [{ href: 'https://example.org/lang.html', text: 'SQL Syntax', visible: true, download: false }, { href: SQLITE_LANGUAGE_URL, text: 'SQL syntax', visible: true, download: false }, { href: SQLITE_LANGUAGE_URL, text: 'SQL Syntax', visible: false, download: false }, { href: SQLITE_LANGUAGE_URL, text: 'SQL Syntax', visible: true, download: true }]) expect(() => sqliteLinkIndex([candidate])).toThrow();
  });
  it('collects one visible heading and introduction and rejects hostile DOM', () => {
    const install = (body: string) => { const dom = new JSDOM(`<body>${body}</body>`); Object.assign(globalThis, { document: dom.window.document, getComputedStyle: dom.window.getComputedStyle.bind(dom.window) }); for (const el of Array.from(dom.window.document.querySelectorAll('*'))) Object.defineProperty(el, 'getBoundingClientRect', { value: () => ({ width: 100, height: 20 }) }); };
    install('<h1>SQL As Understood By SQLite</h1><p> SQLite understands most of SQL. </p>');
    expect(collectVisibleSQLiteDocument()).toEqual([{ title: 'SQL As Understood By SQLite', introduction: 'SQLite understands most of SQL.' }]);
    install('<h1>One</h1><h1>Two</h1><p>Text</p>'); expect(collectVisibleSQLiteDocument()).toEqual([]);
    install('<h1 hidden>Hidden</h1><p>Secret</p>'); expect(collectVisibleSQLiteDocument()).toEqual([]);
  });
  it('locks environment, requirement, inputs, and recording order', () => {
    expect(sqliteEnvironmentBlock('Verify you are human')).toBeTruthy(); expect(sqliteEnvironmentBlock('Documentation')).toBeUndefined();
    expect(() => assertSQLiteRequirement(SQLITE_DOCS_REQUIREMENT)).not.toThrow();
    expect(SQLITE_DOCS_REQUIREMENT.outputFields.map((field) => field.name)).toEqual(['text']);
    expect(SQLITE_DOCS_REQUIREMENT.description).toContain('filter that collection once');
    expect(SQLITE_DOCS_REQUIREMENT.description).toContain('exactly one result object');
    expect(bindSQLiteInputs({ target_text: '', target_host: '', destination_path: '' })).toEqual({ target_text: 'SQL Syntax', target_host: 'www.sqlite.org', destination_path: '/lang.html' });
    const recording = { version: '2.0.0', meta: { startUrl: SQLITE_DOCS_ENTRY_URL }, termination: { complete: true }, snapshots: [{ capture: { status: 'complete' } }], events: [{ type: 'click', timestamp: 1, index: 1 }, { type: 'navigate', timestamp: 2, url: SQLITE_LANGUAGE_URL }, { type: 'scroll', timestamp: 3, x: 0, y: 420 }, { type: 'scroll', timestamp: 4, x: 0, y: 840 }, { type: 'navigate', timestamp: 5, url: SQLITE_DOCS_ENTRY_URL }] } as unknown as PageAgentRecording;
    expect(() => assertSQLiteRecording(recording)).not.toThrow();
    expect(() => assertSQLiteRecording({ ...recording, events: recording.events.slice(0, -1) })).toThrow();
  });
  it('requires exact semantic text filtering without positional targeting', () => {
    const rule = { domain: 'www.sqlite.org', entry: SQLITE_DOCS_ENTRY_URL, steps: [
      { action: 'navigate', url: `https://{{target_host}}{{destination_path}}` },
      { action: 'extract', name: 'items', target: { selector: 'li', visible: true }, multiple: true, fields: { text: { selector: 'a', type: 'text', visible: true } } },
      { action: 'filter', from: 'extracted.items', name: 'selected', criteria: { field: 'text', op: 'eq', value: '{{target_text}}' } },
      { action: 'sendResult', payload: { text: '{{target_text}}' } },
    ] };
    expect(() => assertSQLiteRule(rule)).not.toThrow();
    expect(() => assertSQLiteRule({ ...rule, steps: rule.steps.filter((step) => step.action !== 'filter') })).toThrow(/exact reviewed/);
  });
});
