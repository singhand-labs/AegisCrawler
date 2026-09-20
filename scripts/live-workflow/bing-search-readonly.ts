import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { plainObject } from './model-contract';
import { BING_ENTRY_URL, type BingSelectedResult, bingPageBlock, humanClick, isSelectableBingDestination, scrollLikeHuman, submitBingQuery, typeQueryLikeHuman, waitForBingChoice } from './bing-search';

export const BING_READONLY_QUERY = 'how do ocean tides work';
export const BING_READONLY_REQUIREMENT = {
  title: 'Report a visible Bing result without opening it',
  description: 'Search Bing with keyword, locate the uniquely visible organic HTTPS result identified by target_title without rank matching, and return its title and visible citation without leaving Bing.',
  requiredInputs: [
    { name: 'keyword', type: 'string', description: 'Exact reviewed Bing query' },
    { name: 'target_title', type: 'string', description: 'Exact visible result title' },
  ], optionalInputs: [], outputFields: [
    { name: 'title', type: 'string', description: 'Visible result level-two heading' },
    { name: 'website', type: 'string', description: 'Visible result citation' },
  ], sampleOutput: { title: 'Tides', website: 'example.org › science › tides' },
};

export async function bingSearchReadonlyDemo(page: Page, ready?: (page?: Page) => Promise<void>, onSelected?: (value: BingSelectedResult) => void): Promise<void> {
  await page.waitForLoadState('load', { timeout: 60_000 });
  const search = page.locator('#sb_form_q'); await search.waitFor({ state: 'visible', timeout: 30_000 });
  await humanClick(page, search); await typeQueryLikeHuman(search, page, BING_READONLY_QUERY);
  await submitBingQuery(page, search, BING_READONLY_QUERY); await ready?.(page);
  const choices = await waitForBingChoice(page, BING_READONLY_QUERY);
  const links = page.locator('#b_results h2 a[href]'); let selected: BingSelectedResult | undefined;
  const visible = await links.evaluateAll((nodes) => nodes.filter((node) => {
    const rect = node.getBoundingClientRect(); const style = window.getComputedStyle(node);
    return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
  }).map((node) => ({ href: node.getAttribute('href') ?? '', title: (node.textContent ?? '').replace(/\s+/g, ' ').trim() })));
  for (const candidate of visible) {
    if (!candidate.href || visible.filter((item) => item.title === candidate.title).length !== 1) continue;
    if (isSelectableBingDestination(candidate.href, candidate.title, choices, page.url())) {
      selected = { title: candidate.title, host: new URL(candidate.href, page.url()).hostname }; break;
    }
  }
  assert(selected, 'Bing results contain no visible organic HTTPS identity'); onSelected?.(selected);
  await scrollLikeHuman(page); assert.equal(bingPageBlock(page.url(), BING_READONLY_QUERY), '', 'Bing read-only journey left the results page');
}

export function assertBingReadonlyRecording(recording: PageAgentRecording): void {
  assert.equal(recording.termination?.complete, true); assert(recording.snapshots.every((s) => s.capture?.status === 'complete'));
  const events = [...recording.events].sort((a, b) => a.timestamp - b.timestamp);
  const nav = events.findIndex((e) => e.type === 'navigate' && bingPageBlock(e.url, BING_READONLY_QUERY) === ''); assert(nav >= 0, 'Bing read-only recording omitted exact results navigation');
  assert(events.slice(0, nav).some((e) => e.type === 'inputText' && e.text.replace(/\s+/g, ' ').trim() === BING_READONLY_QUERY), 'Bing read-only recording omitted exact input');
  assert(events.slice(nav + 1).some((e) => e.type === 'scroll'), 'Bing read-only recording omitted result scrolling');
  assert(!events.some((e) => e.type === 'navigate' && bingPageBlock(e.url, BING_READONLY_QUERY) !== ''), 'Bing read-only recording left the exact results boundary');
}
export function assertBingReadonlyRequirement(value: unknown): void { assert(plainObject(value)); assert.deepEqual((Array.isArray(value.requiredInputs) ? value.requiredInputs : []).map((v: any) => [v.name, v.type]), [['keyword', 'string'], ['target_title', 'string']]); }
export function assertBingReadonlyRule(rule: unknown): void {
  assert(plainObject(rule) && Array.isArray(rule.steps), 'Bing read-only rule steps are missing');
  const serialized = JSON.stringify(rule);
  assert(serialized.includes('{{keyword}}'), 'Bing read-only rule must bind the keyword input');
  assert(JSON.stringify(rule.domain).includes('bing.com'), 'Bing read-only rule must remain scoped to Bing');
  assert(serialized.includes('waitForElementVisible'), 'Bing read-only rule must use observable result readiness');
  assert(!/"index"\s*:/.test(serialized), 'Bing read-only rule must not bind the selected result by position');
  assert(!/solveCaptcha|executeJavascript/.test(serialized), 'Bing read-only rule contains unsafe actions');
  const filters = rule.steps.filter((step): step is Record<string, unknown> =>
    plainObject(step) && step.action === 'filter' && plainObject(step.criteria));
  const titleFilter = filters.find((step) =>
    (step.criteria as Record<string, unknown>).field === 'title'
    && (step.criteria as Record<string, unknown>).op === 'eq'
    && (step.criteria as Record<string, unknown>).value === '{{target_title}}');
  assert(titleFilter, 'Bing read-only rule must filter extracted title by target_title');
  assert(!filters.some((step) => (step.criteria as Record<string, unknown>).field === 'website'),
    'Bing read-only rule must not compare a visible citation with a destination host');
  const selected = `{{extracted.${String(titleFilter.name)}}}`;
  assert(rule.steps.some((step) => plainObject(step) && step.action === 'loop'
    && step.type === 'forEach' && step.items === selected
    && JSON.stringify(step.steps).includes('{{loopItem.title}}')
    && JSON.stringify(step.steps).includes('{{loopItem.website}}')),
  'Bing read-only rule must emit only the exact-title-filtered rows');
}
export function assertBingReadonlyRows(rows: unknown[], schema: unknown, expected: BingSelectedResult, phase: string): void {
  assert(plainObject(schema), `${phase} Bing output schema is missing`);
  assert.equal(rows.length, 1, `${phase} must return exactly the recorded Bing selection`);
  const row = rows[0]; assert(plainObject(row), `${phase} Bing row must be an object`);
  const title = String(row.title ?? '').replace(/\s+/g, ' ').trim();
  const citation = String(row.website ?? '').replace(/\s+/g, ' ').trim().toLowerCase();
  const escapedHost = expected.host.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  assert.equal(title, expected.title.replace(/\s+/g, ' ').trim(), `${phase} must return the uniquely selected title`);
  assert(new RegExp(`(^|[^a-z0-9.-])${escapedHost}(?=$|[^a-z0-9.-])`).test(citation),
    `${phase} visible citation must identify the selected HTTPS host`);
}
