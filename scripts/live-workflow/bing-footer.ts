import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { plainObject } from './model-contract';
import { BING_ENTRY_URL, BING_READING_PAUSE_MS, findBingBoundary } from './bing-search';

export const BING_FOOTER_LABEL = 'Legal';
export interface BingFooterSelection { text: string; host: string }
export const BING_FOOTER_REQUIREMENT = {
  title: 'Open and report a reviewed Bing footer link',
  description: 'Locate the visible Bing footer link identified by target_text and target_host without positional matching, open it, and return exactly its visible text and destination host.',
  requiredInputs: [
    { name: 'target_text', type: 'string', description: 'Exact visible footer-link text' },
    { name: 'target_host', type: 'string', description: 'HTTPS destination host' },
  ],
  optionalInputs: [],
  outputFields: [
    { name: 'text', type: 'string', description: 'Visible footer-link text' },
    { name: 'website', type: 'string', description: 'Destination host' },
  ],
  sampleOutput: { text: BING_FOOTER_LABEL, website: 'www.microsoft.com' },
};

const clean = (value: string | null | undefined) => String(value ?? '').replace(/\s+/g, ' ').trim();
export function footerDestination(raw: string): string | undefined {
  try { const url = new URL(raw, BING_ENTRY_URL); return url.protocol === 'https:' ? url.hostname : undefined; } catch { return undefined; }
}

export async function bingFooterDemo(page: Page, ready?: (page?: Page) => Promise<void>, selected?: (v: BingFooterSelection) => void): Promise<void> {
  await page.waitForLoadState('load', { timeout: 60_000 });
  const boundary = findBingBoundary(await page.locator('body').innerText(), await page.title());
  if (boundary) throw new Error(`Bing environment blocked: ${boundary}`);
  const link = page.getByRole('link', { name: BING_FOOTER_LABEL, exact: true }).filter({ visible: true }).first();
  await link.waitFor({ state: 'visible', timeout: 30_000 });
  await link.scrollIntoViewIfNeeded();
  const href = await link.getAttribute('href');
  const host = footerDestination(href ?? '');
  assert(host, 'Bing Legal footer link must have an HTTPS destination');
  const text = clean(await link.innerText());
  assert.equal(text, BING_FOOTER_LABEL, 'Bing footer label changed');
  selected?.({ text, host });
  const before = new Set(page.context().pages());
  // Bing's lazily-growing DISCOVER feed keeps the footer perpetually outside
  // the reachable viewport (Playwright scrolls, the feed grows, the element
  // recedes — pointer clicks time out). Activate the reviewed link with a
  // trusted keyboard Enter after focusing it; the browser-generated
  // navigation is exactly what the recorder captures.
  await link.focus();
  await page.keyboard.press('Enter');
  // Bing varies between a same-tab navigation and a target=_blank popup for
  // this footer link; accept either. The same-tab navigation commits
  // asynchronously (sampling the URL right after Enter can still observe the
  // pre-navigation bing.com document), and the destination's full "load" can
  // hang on third-party telemetry, so domcontentloaded is the readiness
  // boundary throughout.
  const navigationDeadline = Date.now() + 60_000;
  await Promise.race([
    page.waitForURL((url) => url.protocol === 'https:' && url.hostname !== 'www.bing.com',
      // 'commit': the predicate already validates the destination URL; the
      // destination's domcontentloaded can itself hang on third-party
      // telemetry, so waiting for any lifecycle event past the commit is
      // another variance trap.
      { timeout: 60_000, waitUntil: 'commit' }),
    (async () => {
      for (;;) {
        const popup = page.context().pages().find((candidate) => !before.has(candidate));
        if (popup) return; // body-visible below is the readiness boundary.
        if (Date.now() > navigationDeadline) {
          throw new Error('Bing footer link neither navigated nor opened a destination tab');
        }
        await page.waitForTimeout(250);
      }
    })(),
  ]);
  await page.waitForTimeout(BING_READING_PAUSE_MS);
  const popup = page.context().pages().find((candidate) => !before.has(candidate));
  const destination = popup ?? page;
  // No lifecycle wait here: the destination's domcontentloaded can also hang
  // on third-party telemetry; the body-visible wait below is the boundary.
  await ready?.(destination);
  assert.equal(new URL(destination.url()).protocol, 'https:', 'Bing footer destination must remain HTTPS');
  assert.notEqual(new URL(destination.url()).hostname, 'www.bing.com', 'Bing footer link did not leave Bing');
  await destination.locator('body').waitFor({ state: 'visible', timeout: 30_000 });
  await destination.mouse.wheel(0, 220); await destination.waitForTimeout(BING_READING_PAUSE_MS);
  if (popup) { await page.bringToFront(); await popup.close(); } else { await page.goBack({ waitUntil: 'domcontentloaded', timeout: 60_000 }); }
  await ready?.(page);
  await page.getByRole('link', { name: BING_FOOTER_LABEL, exact: true }).waitFor({ state: 'visible', timeout: 30_000 });
}

export function assertBingFooterRecording(recording: PageAgentRecording): void {
  assert.equal(recording.termination?.complete, true, 'Bing footer recording must be complete');
  assert(recording.snapshots.every((s) => s.capture?.status === 'complete'), 'Bing footer snapshots must be complete');
  const destination = recording.events.find((e) => e.type === 'navigate'
    && footerDestination(e.url) && new URL(e.url).hostname !== 'www.bing.com');
  assert(destination?.type === 'navigate', 'Bing footer recording omitted HTTPS destination');
  assert(recording.snapshots.some((snapshot) => {
    try { return snapshot.timestamp > destination.timestamp && new URL(snapshot.url).hostname === 'www.bing.com'; } catch { return false; }
  }), 'Bing footer context was not restored after the destination');
}

export function assertBingFooterRequirement(value: unknown): void {
  assert(plainObject(value));
  const inputs = Array.isArray(value.requiredInputs) ? value.requiredInputs : [];
  assert.deepEqual(inputs.map((v: any) => [v.name, v.type]), [['target_text', 'string'], ['target_host', 'string']]);
}
export function assertBingFooterRule(rule: unknown): void {
  const raw = JSON.stringify(rule); assert(raw.includes('{{target_text}}')); assert(raw.includes('{{target_host}}'));
  assert(raw.includes('bing.com'), 'Bing footer rule must remain scoped to Bing');
  assert(raw.includes('waitForElementVisible'), 'Bing footer rule must use observable readiness');
  assert(!/"index"\s*:|solveCaptcha|executeJavascript/.test(raw), 'Bing footer rule contains unsafe/positional actions');
}
export function assertBingFooterRows(rows: unknown[], schema: unknown, expected: BingFooterSelection, phase: string): void {
  assert(plainObject(schema), `${phase} output schema missing`); assert.equal(rows.length, 1);
  assert.deepEqual(rows.map((row: any) => ({ text: clean(row.text), host: clean(row.website).toLowerCase() })), [{ text: expected.text, host: expected.host }]);
}
