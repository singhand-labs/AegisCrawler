import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { assertVisibleModelExtractionRule, plainObject } from './model-contract';

export const RFC_EDITOR_ENTRY_URL = 'https://www.rfc-editor.org/rfc/rfc2606.html';
export const RFC_EDITOR_RECORDED_SECTION = '3';
export const RFC_EDITOR_REPLAY_SECTION = '2';
export const RFC_EDITOR_TASK_SECTION = '4';
export const RFC_EDITOR_COST_BUDGET_USD = 1.5;
const RFC_EDITOR_SECTIONS = new Set(['2', '3', '4']);
export const RFC_EDITOR_FIELDS = ['section_number'] as const;

export interface RFCEditorRow extends Record<string, string> {
  section_number: string;
}

export interface RFCEditorSectionEvidence extends RFCEditorRow {
  heading: string;
  first_paragraph: string;
}

export const RFC_EDITOR_REQUIREMENT = {
  title: 'Report one reviewed section from RFC 2606',
  description: [
    'Open the immutable RFC 2606 HTML document and navigate to the same-document fragment section-section_number.',
    'Locate the unique visible semantic section for section_number without rank or positional targeting. Because extraction targets are evidence-bound, use one URL-guarded extraction for each reviewed section 2, 3, and 4; each guard and target must refer to the same section.',
    'Return exactly its canonical visible section number. The independent oracle validates the adjacent heading and first non-empty prose paragraph, which are text-node context rather than selectable output fields.',
    'Never open an external link, download a document, or extract hidden navigation content.',
  ].join(' '),
  requiredInputs: [{
    name: 'section_number', type: 'string', description: 'Reviewed RFC 2606 section number',
    constraints: { pattern: '^[234]$', maxLength: 1 },
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'section_number', type: 'string', description: 'Canonical visible section number' },
  ],
  sampleOutput: { section_number: '3' },
};

function norm(value: unknown): string { return String(value ?? '').replace(/\s+/g, ' ').trim(); }

export function assertRFCEditorSection(value: string): string {
  assert.equal(value, value.trim(), 'RFC Editor section must not contain surrounding whitespace');
  assert(RFC_EDITOR_SECTIONS.has(value), 'RFC Editor section must be exactly 2, 3, or 4');
  return value;
}

export function assertRFCEditorURL(raw: string, expectedSection?: string): URL {
  const url = new URL(raw);
  assert.equal(url.protocol, 'https:', 'RFC Editor URL must use HTTPS');
  assert.equal(url.hostname, 'www.rfc-editor.org', 'RFC Editor URL must stay on the reviewed host');
  assert.equal(url.port, '', 'RFC Editor URL must not use a non-default port');
  assert(!url.username && !url.password && !url.search, 'RFC Editor URL must not contain credentials or a query');
  assert.equal(url.pathname, '/rfc/rfc2606.html', 'RFC Editor URL must stay on RFC 2606 HTML');
  if (expectedSection !== undefined) {
    assertRFCEditorSection(expectedSection);
    assert.equal(url.hash, `#section-${expectedSection}`, 'RFC Editor URL fragment does not match the reviewed section');
  } else {
    assert(url.hash === '' || /^#section-[234]$/.test(url.hash), 'RFC Editor URL has an unreviewed fragment');
  }
  return url;
}

export function rfcEditorEnvironmentBlock(bodyText: string, title = ''): string | undefined {
  const sample = `${title}\n${bodyText}`.toLowerCase();
  return ['captcha', 'verify you are human', 'verification required', 'access denied',
    'consent required', 'unusual traffic'].find((phrase) => sample.includes(phrase));
}

export function rfcEditorTOCLabelIndex(
  candidates: readonly { href: string; text: string; beforeTarget: boolean; insideTarget: boolean }[],
  sectionNumber: string,
  targetCount = 1,
): number {
  assertRFCEditorSection(sectionNumber);
  const expectedHref = `${RFC_EDITOR_ENTRY_URL}#section-${sectionNumber}`;
  const exactHref = candidates.filter((candidate) => candidate.href === expectedHref);
  const canonical = exactHref.filter((candidate) => {
    const label = norm(candidate.text);
    return label === sectionNumber || label === `${sectionNumber}.`;
  });
  const matches = candidates
    .map((candidate, index) => ({ candidate, index }))
    .filter(({ candidate }) => {
      if (candidate.href !== expectedHref) return false;
      const label = norm(candidate.text);
      return (label === sectionNumber || label === `${sectionNumber}.`)
        && candidate.beforeTarget && !candidate.insideTarget;
    });
  const diagnostics = {
    candidates: candidates.length,
    exactHref: exactHref.length,
    canonical: canonical.length,
    beforeTarget: canonical.filter((candidate) => candidate.beforeTarget).length,
    insideTarget: canonical.filter((candidate) => candidate.insideTarget).length,
    eligible: matches.length,
    targetCount,
  };
  assert(targetCount === 1 && matches.length === 1,
    `RFC Editor must expose one canonical pre-section TOC link; topology=${JSON.stringify(diagnostics)}`);
  return matches[0].index;
}

export function parseRFCEditorSectionText(raw: string, sectionNumber: string): RFCEditorSectionEvidence[] {
  if (!/^[234]$/.test(sectionNumber)) return [];
  const lines = raw.replace(/\r/g, '').split('\n').map((line) => line.replace(/\s+$/g, ''));
  const headingIndex = lines.findIndex((line) => line.trim() !== '');
  if (headingIndex < 0) return [];
  const headingMatch = lines[headingIndex].trim().match(new RegExp(`^${sectionNumber}\\s*\\.\\s+(.+)$`));
  if (!headingMatch) return [];
  let paragraphStart = headingIndex + 1;
  while (paragraphStart < lines.length && lines[paragraphStart].trim() === '') paragraphStart += 1;
  const paragraphLines: string[] = [];
  for (let index = paragraphStart; index < lines.length; index += 1) {
    if (lines[index].trim() === '') break;
    paragraphLines.push(lines[index].trim());
  }
  const heading = norm(headingMatch[1]);
  const first_paragraph = norm(paragraphLines.join(' '));
  if (!heading || !first_paragraph) return [];
  return [{ section_number: sectionNumber, heading, first_paragraph }];
}

/** Self-contained visible-DOM oracle for Playwright evaluation. */
export function collectVisibleRFCEditorSection(sectionNumber: string): RFCEditorSectionEvidence[] {
  const clean = (value: unknown): string => String(value ?? '').replace(/\s+/g, ' ').trim();
  const parse = (raw: string): RFCEditorSectionEvidence[] => {
    const lines = raw.replace(/\r/g, '').split('\n').map((line) => line.replace(/\s+$/g, ''));
    const headingIndex = lines.findIndex((line) => line.trim() !== '');
    if (headingIndex < 0) return [];
    const headingMatch = lines[headingIndex].trim().match(
      new RegExp(`^${sectionNumber}\\s*\\.\\s+(.+)$`),
    );
    if (!headingMatch) return [];
    let paragraphStart = headingIndex + 1;
    while (paragraphStart < lines.length && lines[paragraphStart].trim() === '') paragraphStart += 1;
    const paragraphLines: string[] = [];
    for (let index = paragraphStart; index < lines.length; index += 1) {
      if (lines[index].trim() === '') break;
      paragraphLines.push(lines[index].trim());
    }
    const heading = clean(headingMatch[1]);
    const first_paragraph = clean(paragraphLines.join(' '));
    if (!heading || !first_paragraph) return [];
    return [{ section_number: sectionNumber, heading, first_paragraph }];
  };
  const visible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      const style = getComputedStyle(current);
      if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true'
        || style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return false;
      current = current.parentElement;
    }
    const rect = element.getBoundingClientRect(); return rect.width > 0 && rect.height > 0;
  };
  if (!/^[234]$/.test(sectionNumber)) return [];
  const targets = Array.from(document.querySelectorAll(`#section-${CSS.escape(sectionNumber)}`));
  if (targets.length !== 1 || !visible(targets[0])) return [];
  const target = targets[0];
  const next = Array.from(document.querySelectorAll('[id^="section-"]')).find((candidate) =>
    candidate !== target
      && /^section-\d+$/.test(candidate.id)
      && Boolean(target.compareDocumentPosition(candidate) & Node.DOCUMENT_POSITION_FOLLOWING));
  if (!next || !visible(next)) return [];
  const range = document.createRange();
  range.setStartBefore(target);
  range.setEndBefore(next);
  return parse(range.toString());
}

export function canonicalRFCEditorRows(values: readonly unknown[], section: string, label: string): RFCEditorRow[] {
  assertRFCEditorSection(section);
  assert.equal(values.length, 1, `${label} must contain exactly one row`);
  const value = values[0]; assert(plainObject(value), `${label} row must be an object`);
  assert.deepEqual(Object.keys(value).sort(), [...RFC_EDITOR_FIELDS], `${label} row has schema drift`);
  const row = { section_number: norm(value.section_number) };
  assert.equal(row.section_number, section, `${label} returned the wrong section`);
  return [row];
}

export function assertRFCEditorRowsEqual(rows: unknown[], schema: unknown, expected: RFCEditorRow[], section: string, label: string): void {
  assert(plainObject(schema) && plainObject(schema.properties), `${label} output schema is missing`);
  assert.deepEqual(Object.keys(schema.properties).sort(), [...RFC_EDITOR_FIELDS], `${label} output schema drifted`);
  assert.deepEqual(canonicalRFCEditorRows(rows, section, label), canonicalRFCEditorRows(expected, section, `${label} oracle`));
}

export function bindRFCEditorSection(inputs: Record<string, unknown>, section: string): Record<string, unknown> {
  assertRFCEditorSection(section);
  assert.deepEqual(Object.keys(inputs), ['section_number'], 'RFC Editor inputs must contain only section_number');
  return { section_number: section };
}

export async function collectRFCEditorOracle(page: Page, section: string): Promise<RFCEditorRow[]> {
  assertRFCEditorURL(page.url(), section);
  const boundary = rfcEditorEnvironmentBlock(await page.locator('body').innerText(), await page.title());
  assert(!boundary, `RFC Editor environment blocked: ${boundary}`);
  const evidence = await page.evaluate(collectVisibleRFCEditorSection, section);
  assert.equal(evidence.length, 1, `RFC Editor section ${section} context is not uniquely visible`);
  assert(evidence[0].heading && evidence[0].first_paragraph,
    `RFC Editor section ${section} context is incomplete`);
  return canonicalRFCEditorRows(
    [{ section_number: evidence[0].section_number }], section, `RFC Editor section ${section} oracle`,
  );
}

export async function rfcEditorDemo(page: Page, ensureRecordingReady: () => Promise<void> = async () => undefined): Promise<void> {
  assertRFCEditorURL(page.url());
  const boundary = rfcEditorEnvironmentBlock(await page.locator('body').innerText(), await page.title());
  assert(!boundary, `RFC Editor environment blocked: ${boundary}`);
  const links = page.locator(`a[href="#section-${RFC_EDITOR_RECORDED_SECTION}"]:visible`);
  const topology = await links.evaluateAll((elements, sectionNumber) => {
    const targets = Array.from(document.querySelectorAll(`#section-${CSS.escape(sectionNumber)}`));
    const target = targets.length === 1 ? targets[0] : undefined;
    return {
      targetCount: targets.length,
      candidates: elements.map((element) => ({
        href: (element as HTMLAnchorElement).href,
        text: element.textContent ?? '',
        beforeTarget: Boolean(target
          && (element.compareDocumentPosition(target) & Node.DOCUMENT_POSITION_FOLLOWING)),
        insideTarget: Boolean(target?.contains(element)),
      })),
    };
  }, RFC_EDITOR_RECORDED_SECTION);
  const tocLabel = links.nth(rfcEditorTOCLabelIndex(
    topology.candidates, RFC_EDITOR_RECORDED_SECTION, topology.targetCount,
  ));
  await Promise.all([
    page.waitForURL((url) => url.hash === `#section-${RFC_EDITOR_RECORDED_SECTION}`, { timeout: 30_000 }),
    tocLabel.click(),
  ]);
  assertRFCEditorURL(page.url(), RFC_EDITOR_RECORDED_SECTION);
  await ensureRecordingReady();
  assert.equal((await page.evaluate(collectVisibleRFCEditorSection, RFC_EDITOR_RECORDED_SECTION)).length, 1,
    'RFC Editor recorded section is not uniquely visible');
  await page.mouse.wheel(0, 260); await page.waitForTimeout(700);
}

export function assertRFCEditorRecording(recording: PageAgentRecording): void {
  assert.equal(recording.version, '2.0.0');
  assert.equal(recording.meta.startUrl, RFC_EDITOR_ENTRY_URL);
  assert.equal(recording.termination?.complete, true);
  assert(recording.snapshots.every((snapshot) => snapshot.capture?.status === 'complete'));
  assert.equal(recording.events.filter((event) => event.type === 'click').length, 1, 'RFC Editor recording must contain one TOC click');
  assert(recording.events.some((event) => event.type === 'navigate' && (() => {
    try { assertRFCEditorURL(event.url, RFC_EDITOR_RECORDED_SECTION); return true; } catch { return false; }
  })()), 'RFC Editor recording omitted the committed section fragment');
  assert(recording.events.some((event) => event.type === 'scroll'), 'RFC Editor recording omitted bounded section scrolling');
  for (const event of recording.events) {
    if (event.type === 'navigate') assertRFCEditorURL(event.url);
    assert(event.type !== 'executeJavascript', 'RFC Editor recording contains script execution');
  }
}

export function assertRFCEditorRequirement(value: unknown): void {
  assert(plainObject(value));
  assert.deepEqual((Array.isArray(value.requiredInputs) ? value.requiredInputs : []).filter(plainObject).map((input) => [input.name, input.type]), [['section_number', 'string']]);
  assert.deepEqual(value.optionalInputs, []);
  assert.deepEqual((Array.isArray(value.outputFields) ? value.outputFields : []).filter(plainObject).map((field) => field.name).sort(), [...RFC_EDITOR_FIELDS]);
}

export function assertRFCEditorRule(rule: unknown): void {
  assert(plainObject(rule), 'RFC Editor rule must be an object');
  assert.deepEqual(Array.isArray(rule.domain) ? rule.domain : [rule.domain], ['www.rfc-editor.org']);
  assert.equal(rule.entry, RFC_EDITOR_ENTRY_URL);
  const serialized = JSON.stringify(rule);
  assert(serialized.includes('{{section_number}}'), 'RFC Editor rule must bind section_number');
  assert(serialized.includes('#section-'), 'RFC Editor rule must use same-document section navigation');
  assert(!/"index"\s*:|executeJavascript|solveCaptcha|download|upload/i.test(serialized), 'RFC Editor rule contains unsafe or positional behavior');
  assertVisibleModelExtractionRule(rule, 'RFC Editor');

  const actions = (value: unknown): Record<string, unknown>[] => {
    if (Array.isArray(value)) return value.flatMap(actions);
    if (!plainObject(value)) return [];
    return [
      ...(typeof value.action === 'string' ? [value] : []),
      ...Object.values(value).flatMap(actions),
    ];
  };
  // Two semantically equivalent guarded shapes are accepted: the historical
  // extractText form (target selector + URL guard) and the structured extract
  // form (exactly one field named section_number + the same URL guard). Both
  // enforce one guarded extraction per reviewed section.
  const isGuardedSectionExtraction = (step: Record<string, unknown>): boolean => {
    const condition = step.condition;
    if (!plainObject(condition) || condition.type !== 'urlMatches') return false;
    if (step.action === 'extractText') return step.name === 'section_number';
    if (step.action !== 'extract') return false;
    // Structured form: the extraction name is free; exactly one field named
    // section_number carries the reviewed value.
    const fields = step.fields;
    return plainObject(fields) && Object.keys(fields).length === 1
      && 'section_number' in fields;
  };
  const sectionExtractions = actions([rule.steps, rule.hooks]).filter(isGuardedSectionExtraction);
  assert.equal(sectionExtractions.length, RFC_EDITOR_SECTIONS.size,
    'RFC Editor rule must contain one guarded section_number extraction per reviewed section');
  const guardedSections = sectionExtractions.map((step) => {
    const condition = step.condition;
    assert(plainObject(condition) && condition.type === 'urlMatches',
      'RFC Editor section_number extraction must use a URL-only guard');
    const patternMatch = String(condition.pattern ?? '').match(/^#section-([234])\$$/);
    assert(patternMatch, 'RFC Editor extraction URL guard must match its authorized section');
    if (step.action === 'extractText') {
      const target = step.target;
      assert(plainObject(target), 'RFC Editor section_number extraction must have a target');
      const selectorMatch = String(target.selector ?? '').match(/^#section-([234])$/);
      assert(selectorMatch && selectorMatch[1] === patternMatch![1],
        'RFC Editor extraction URL guard must match its authorized section selector');
    }
    return patternMatch![1];
  });
  assert.deepEqual(guardedSections.sort(), [...RFC_EDITOR_SECTIONS].sort(),
    'RFC Editor extraction branches must cover each reviewed section exactly once');
}
