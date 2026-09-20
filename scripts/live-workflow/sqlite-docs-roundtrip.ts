import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { assertVisibleModelExtractionRule, plainObject } from './model-contract';

export const SQLITE_DOCS_ENTRY_URL = 'https://www.sqlite.org/docs.html';
export const SQLITE_LANGUAGE_URL = 'https://www.sqlite.org/lang.html';
export const SQLITE_LINK_TEXT = 'SQL Syntax';
export const SQLITE_HOST = 'www.sqlite.org';
export const SQLITE_DESTINATION_PATH = '/lang.html';
export const SQLITE_COST_BUDGET_USD = 1.5;
export const SQLITE_FIELDS = ['text'] as const;

export interface SQLiteDocsRow extends Record<string, string> { text: string }
export interface SQLiteDocumentContext { title: string; introduction: string }

export const SQLITE_DOCS_REQUIREMENT = {
  title: 'Report the reviewed SQLite SQL language document',
  description: [
    'From the SQLite documentation index, identify and follow the exact visible target_text link to the HTTPS target_host destination_path page, then restore the documentation index.',
    'Extract the repeated visible documentation-link cohort as objects with one text field, filter that collection once by text exactly equal to target_text, and emit exactly one result object whose text is the filtered visible value.',
    'Do not return a literal input value, a page-wide text target, a scalar collection, or more than one row.',
    'The independent oracle validates the destination heading and introduction.',
    'Never use search, positional targeting, downloads, external navigation, or hidden content.',
  ].join(' '),
  requiredInputs: [
    { name: 'target_text', type: 'string', description: 'Exact visible documentation link label' },
    { name: 'target_host', type: 'string', description: 'Reviewed SQLite host' },
    { name: 'destination_path', type: 'string', description: 'Reviewed language-document path' },
  ], optionalInputs: [], outputFields: [
    { name: 'text', type: 'string', description: 'Exact visible SQLite documentation link label' },
  ], sampleOutput: { text: 'SQL Syntax' },
};

const norm = (value: unknown): string => String(value ?? '').replace(/\s+/g, ' ').trim();

export function assertSQLiteURL(raw: string, path?: '/docs.html' | '/lang.html'): URL {
  const url = new URL(raw);
  assert.equal(url.protocol, 'https:'); assert.equal(url.hostname, SQLITE_HOST); assert.equal(url.port, '');
  assert(!url.username && !url.password && !url.search && !url.hash, 'SQLite URL must be canonical and credential-free');
  assert(['/docs.html', '/lang.html'].includes(url.pathname), 'SQLite URL path is outside the reviewed journey');
  if (path) assert.equal(url.pathname, path);
  return url;
}

export function sqliteEnvironmentBlock(text: string): string | undefined {
  const sample = text.toLowerCase();
  return ['captcha', 'verify you are human', 'verification required', 'access denied', 'consent required', 'unusual traffic']
    .find((phrase) => sample.includes(phrase));
}

export function sqliteLinkIndex(candidates: readonly { href: string; text: string; visible: boolean; download: boolean }[]): number {
  const matches = candidates.map((candidate, index) => ({ candidate, index })).filter(({ candidate }) =>
    candidate.href === SQLITE_LANGUAGE_URL && norm(candidate.text) === SQLITE_LINK_TEXT && candidate.visible && !candidate.download);
  assert.equal(matches.length, 1, 'SQLite index must expose exactly one eligible SQL Syntax link');
  return matches[0].index;
}

export function collectVisibleSQLiteDocument(): SQLiteDocumentContext[] {
  const clean = (value: unknown): string => String(value ?? '').replace(/\s+/g, ' ').trim();
  const visible = (element: Element): boolean => {
    const style = getComputedStyle(element); const rect = element.getBoundingClientRect();
    return !element.hasAttribute('hidden') && element.getAttribute('aria-hidden') !== 'true'
      && style.display !== 'none' && style.visibility !== 'hidden' && rect.width > 0 && rect.height > 0;
  };
  const headings = Array.from(document.querySelectorAll('h1')).filter(visible);
  if (headings.length !== 1) return [];
  let current = headings[0].nextElementSibling;
  while (current && (!visible(current) || current.tagName.toLowerCase() !== 'p')) current = current.nextElementSibling;
  const title = clean(headings[0].textContent); const introduction = clean(current?.textContent);
  return title && introduction ? [{ title, introduction }] : [];
}

export async function sqliteDocsDemo(page: Page, ready: () => Promise<void> = async () => undefined, capture: (context: SQLiteDocumentContext) => void = () => undefined): Promise<void> {
  assertSQLiteURL(page.url(), '/docs.html');
  assert(!sqliteEnvironmentBlock(await page.locator('body').innerText()));
  const links = page.locator('a');
  const candidates = await links.evaluateAll((elements) => elements.map((element) => ({
    href: (element as HTMLAnchorElement).href, text: element.textContent ?? '',
    visible: Boolean((element as HTMLElement).offsetWidth && (element as HTMLElement).offsetHeight),
    download: (element as HTMLAnchorElement).hasAttribute('download'),
  })));
  const index = sqliteLinkIndex(candidates);
  await Promise.all([page.waitForURL(SQLITE_LANGUAGE_URL, { timeout: 30_000 }), links.nth(index).click()]);
  await ready(); assertSQLiteURL(page.url(), '/lang.html');
  const rows = await page.evaluate(collectVisibleSQLiteDocument); assert.equal(rows.length, 1); capture(rows[0]);
  await page.mouse.wheel(0, 420); await page.waitForTimeout(700); await page.mouse.wheel(0, 420); await page.waitForTimeout(700);
  await Promise.all([page.waitForURL(SQLITE_DOCS_ENTRY_URL, { timeout: 30_000 }), page.goBack({ waitUntil: 'domcontentloaded' })]);
  await ready(); assertSQLiteURL(page.url(), '/docs.html');
}

export function assertSQLiteRecording(recording: PageAgentRecording): void {
  assert.equal(recording.version, '2.0.0'); assert.equal(recording.meta.startUrl, SQLITE_DOCS_ENTRY_URL);
  assert.equal(recording.termination?.complete, true); assert(recording.snapshots.every((s) => s.capture?.status === 'complete'));
  assert.equal(recording.events.filter((e) => e.type === 'click').length, 1);
  assert.equal(recording.events.filter((e) => e.type === 'scroll').length, 2);
  const navigations = recording.events.filter((e): e is Extract<typeof e, { type: 'navigate' }> => e.type === 'navigate');
  assert(navigations.some((e) => assertSQLiteURL(e.url).pathname === '/lang.html'));
  assert.equal(assertSQLiteURL(navigations.at(-1)?.url ?? '').pathname, '/docs.html');
  for (const event of recording.events) assert(!['inputText', 'submitForm', 'executeJavascript'].includes(event.type));
}

export function bindSQLiteInputs(inputs: Record<string, unknown>): Record<string, unknown> {
  assert.deepEqual(Object.keys(inputs).sort(), ['destination_path', 'target_host', 'target_text']);
  return { target_text: SQLITE_LINK_TEXT, target_host: SQLITE_HOST, destination_path: SQLITE_DESTINATION_PATH };
}

export function assertSQLiteRequirement(value: unknown): void {
  assert(plainObject(value));
  assert.deepEqual((value.requiredInputs as Array<Record<string, unknown>>).map((v) => v.name).sort(), ['destination_path', 'target_host', 'target_text']);
  assert.deepEqual((value.outputFields as Array<Record<string, unknown>>).map((v) => v.name).sort(), [...SQLITE_FIELDS]);
}

export function assertSQLiteRows(rows: unknown[], schema: unknown, label: string): void {
  assert.equal(rows.length, 1); assert(plainObject(rows[0])); assert(plainObject(schema) && plainObject(schema.properties));
  assert.deepEqual(Object.keys(rows[0]).sort(), [...SQLITE_FIELDS]); assert.deepEqual(Object.keys(schema.properties).sort(), [...SQLITE_FIELDS]);
  assert.deepEqual({ text: norm(rows[0].text) }, { text: SQLITE_LINK_TEXT }, `${label} semantic row drifted`);
}

export function assertSQLiteRule(rule: unknown): void {
  assert(plainObject(rule)); assert.deepEqual(Array.isArray(rule.domain) ? rule.domain : [rule.domain], [SQLITE_HOST]);
  assert.equal(rule.entry, SQLITE_DOCS_ENTRY_URL); const serialized = JSON.stringify(rule);
  for (const input of ['target_text', 'target_host', 'destination_path']) assert(serialized.includes(`{{${input}}}`));
  assert(serialized.includes('text'), 'SQLite rule must emit the visible link text');
  assert(!/"index"\s*:|"position"\s*:|inputText|submitForm|executeJavascript|download|upload/i.test(serialized));
  const actions = (value: unknown): Record<string, unknown>[] => {
    if (Array.isArray(value)) return value.flatMap(actions);
    if (!plainObject(value)) return [];
    return [...(typeof value.action === 'string' ? [value] : []), ...Object.values(value).flatMap(actions)];
  };
  const exactTextFilters = actions([rule.steps, rule.hooks]).filter((step) =>
    step.action === 'filter' && plainObject(step.criteria)
      && step.criteria.field === 'text' && step.criteria.op === 'eq'
      && step.criteria.value === '{{target_text}}');
  assert.equal(exactTextFilters.length, 1, 'SQLite rule must select the exact reviewed visible link text once');
  assertVisibleModelExtractionRule(rule, 'SQLite docs');
}
