import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { BrowserContext, Page } from 'playwright';
import { convert } from '../../src/rule-generator';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { MODEL_EXTRACTION_ACTIONS, plainObject } from './model-contract';
import { readGoRawStringConstant } from './go-source-contract';

export const BAIDU_ENTRY_URL = 'https://www.baidu.com/';
export const BAIDU_TEST_KEYWORD = 'site:baidu.com 百度搜索帮助';

export interface BaiduResultRow extends Record<string, string> {
  title: string;
  summary: string;
}

export interface StructuredRequirementSpec {
  title: string;
  description: string;
  requiredInputs: Array<{ name: string; type: string; description: string }>;
  optionalInputs: Array<{ name: string; type: string; description: string }>;
  outputFields: Array<{ name: string; type: string; description: string }>;
  sampleOutput: Record<string, unknown>;
}

export const BAIDU_SEARCH_REQUIREMENT: StructuredRequirementSpec = {
  title: '采集百度普通网页搜索结果',
  description: [
    '打开百度首页，使用必填 keyword 输入执行搜索。',
    '采集第一页左侧主结果区中所有同时具有可见标题和可见摘要的普通网页结果。',
    '排除标记为广告的结果、AI 回答、知识卡片、图片或视频模块、相关搜索和右侧栏。',
    '只能采集用户可见的渲染内容；不得读取隐藏、脚本或模板数据。',
    '搜索提交后必须等待普通结果行实际可见；固定等待时长不能作为导航或结果就绪条件。',
  ].join(''),
  requiredInputs: [{ name: 'keyword', type: 'string', description: '百度搜索关键词' }],
  optionalInputs: [],
  outputFields: [
    { name: 'title', type: 'string', description: '结果卡片的可见标题' },
    { name: 'summary', type: 'string', description: '结果卡片的可见摘要' },
  ],
  sampleOutput: {
    title: '百度搜索帮助中心',
    summary: '介绍百度网页搜索的使用方法。',
  },
};

/**
 * Independent, visible-DOM oracle for the ordinary result cards in Baidu's
 * left result column. This function is deliberately self-contained so
 * Playwright can serialize it into the target page without any page bridge.
 */
export function collectVisibleBaiduResults(): BaiduResultRow[] {
  const normalize = (value: string | null | undefined): string =>
    String(value ?? '').replace(/\s+/g, ' ').trim();
  const elementText = (element: Element | null): string => {
    if (!element) return '';
    return normalize((element as HTMLElement).innerText || element.textContent);
  };
  const isVisible = (element: Element): boolean => {
    let current: Element | null = element;
    while (current) {
      if (current.hasAttribute('hidden') || current.getAttribute('aria-hidden') === 'true') return false;
      const style = getComputedStyle(current);
      if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse') return false;
      current = current.parentElement;
    }
    return true;
  };
  const root = document.querySelector('#content_left');
  if (!root || !isVisible(root)) return [];
  const rows: BaiduResultRow[] = [];
  const seen = new Set<string>();
  for (const card of Array.from(root.children)) {
    if (!(card instanceof Element) || !isVisible(card)) continue;
    const classes = card.className;
    const classText = typeof classes === 'string' ? classes : '';
    if (/\bresult-op\b/.test(classText)) continue;
    const titleElement = card.querySelector('h3 a, h3');
    if (!titleElement || !isVisible(titleElement)) continue;
    const title = elementText(titleElement);
    if (!title) continue;
    const visibleAdLabel = Array.from(card.querySelectorAll('*'))
      .some((element) => isVisible(element) && elementText(element) === '广告');
    if (visibleAdLabel) continue;
    const summaryElement = card.querySelector([
      '.c-abstract',
      '[class*="c-abstract"]',
      '[class*="content-right"]',
      '[data-module="abstract"]',
    ].join(', '));
    if (!summaryElement || !isVisible(summaryElement)) continue;
    const summary = elementText(summaryElement);
    if (!summary || summary === title) continue;
    const key = `${title}\u0000${summary}`;
    if (seen.has(key)) continue;
    seen.add(key);
    rows.push({ title, summary });
  }
  return rows;
}

export function baiduEnvironmentBlockForURL(rawURL: string): string {
  let url: URL;
  try {
    url = new URL(rawURL);
  } catch {
    return 'the browser is not on a valid HTTP(S) Baidu page';
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    return `unsupported ${url.protocol} page`;
  }
  if (!/(^|\.)baidu\.com$/i.test(url.hostname)) {
    return `unexpected cross-domain page at ${url.hostname}`;
  }
  if (/^(wappass|passport)\.baidu\.com$/i.test(url.hostname)) {
    return `human authentication or verification page at ${url.hostname}${url.pathname}`;
  }
  return '';
}

export function baiduSearchQueryBlockForURL(
  rawURL: string,
  expectedKeyword = BAIDU_TEST_KEYWORD,
): string {
  const environmentBlock = baiduEnvironmentBlockForURL(rawURL);
  if (environmentBlock) return environmentBlock;
  const url = new URL(rawURL);
  if (url.pathname !== '/s') return 'the browser is not on a Baidu result page';
  if (url.searchParams.get('wd') !== expectedKeyword) {
    return 'the result page was produced by an unexpected search keyword';
  }
  return '';
}

export async function collectBaiduPageOracle(
  page: Page,
  expectedKeyword = BAIDU_TEST_KEYWORD,
  signal?: AbortSignal,
): Promise<BaiduResultRow[]> {
  const deadline = Date.now() + 30_000;
  for (;;) {
    assert(!signal?.aborted, 'Baidu replay oracle capture was aborted');
    const urlBlock = baiduSearchQueryBlockForURL(page.url(), expectedKeyword);
    assert(!urlBlock, `Baidu environment blocked: ${urlBlock}`);
    const state = await page.evaluate(() => {
      const marker = document.querySelector([
        '#verify',
        '[class*="captcha"]',
        '[id*="captcha"]',
        'iframe[src*="captcha"]',
        'iframe[src*="verify"]',
      ].join(', '));
      const bodyText = String(document.body?.innerText ?? '').replace(/\s+/g, ' ');
      return {
        hasResults: document.querySelector('#content_left') !== null,
        blockReason: marker
          ? 'CAPTCHA or verification marker is present'
          : (/安全验证|请输入验证码|完成验证/.test(bodyText)
            ? 'Baidu safety verification is present'
            : ''),
      };
    }).catch(() => ({ hasResults: false, blockReason: '' }));
    assert(!state.blockReason, `Baidu environment blocked: ${state.blockReason}`);
    if (state.hasResults) break;
    if (Date.now() >= deadline) {
      throw new Error('Baidu environment blocked: the ordinary result list did not become available');
    }
    await page.waitForTimeout(250);
  }
  const rows = await page.evaluate(collectVisibleBaiduResults);
  assert(rows.length >= 3,
    `Baidu oracle found ${rows.length} ordinary visible result rows; at least 3 are required`);
  return rows;
}

/**
 * Observe the exact extension-owned replay page and collect its visible-DOM
 * oracle. The caller starts this before replay, so polling can see the fixed
 * query page while execution is active even if it is unavailable afterward.
 * Pages already present when capture starts are never eligible.
 */
export async function collectBaiduReplayExecutionOracle(
  context: Pick<BrowserContext, 'pages'>,
  expectedKeyword = BAIDU_TEST_KEYWORD,
  options: { timeoutMs?: number; pollIntervalMs?: number; signal?: AbortSignal } = {},
): Promise<BaiduResultRow[]> {
  const timeoutMs = options.timeoutMs ?? 90_000;
  const pollIntervalMs = options.pollIntervalMs ?? 100;
  const deadline = Date.now() + timeoutMs;
  const preexistingPages = new Set(context.pages());
  for (;;) {
    if (options.signal?.aborted) {
      throw new Error('Baidu replay oracle capture was aborted before the result page became available');
    }
    const page = context.pages().find((candidate) =>
      !preexistingPages.has(candidate)
      && baiduSearchQueryBlockForURL(candidate.url(), expectedKeyword) === '');
    if (page) return collectBaiduPageOracle(page, expectedKeyword, options.signal);
    if (Date.now() >= deadline) {
      throw new Error(
        'Baidu replay oracle could not observe the exact result page during execution',
      );
    }
    await new Promise<void>((resolve, reject) => {
      let settled = false;
      const onAbort = (): void => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        options.signal?.removeEventListener('abort', onAbort);
        reject(new Error('Baidu replay oracle capture was aborted before the result page became available'));
      };
      const timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        options.signal?.removeEventListener('abort', onAbort);
        resolve();
      }, pollIntervalMs);
      options.signal?.addEventListener('abort', onAbort, { once: true });
      if (options.signal?.aborted) onAbort();
    });
  }
}

function canonicalRows(rows: unknown[]): BaiduResultRow[] {
  return rows.map((value, index) => {
    assert(plainObject(value), `Baidu row ${index} must be an object`);
    assert.deepEqual(Object.keys(value).sort(), ['summary', 'title'],
      `Baidu row ${index} must contain exactly summary and title`);
    const title = String(value.title ?? '').replace(/\s+/g, ' ').trim();
    const summary = String(value.summary ?? '').replace(/\s+/g, ' ').trim();
    assert(title, `Baidu row ${index} has an empty title`);
    assert(summary, `Baidu row ${index} has an empty summary`);
    return { title, summary };
  });
}

export function baiduRowsHash(rows: readonly BaiduResultRow[]): string {
  return createHash('sha256').update(JSON.stringify(canonicalRows([...rows]))).digest('hex');
}

export function assertBaiduRowsEqual(
  rows: unknown[],
  outputSchema: unknown,
  expected: readonly BaiduResultRow[],
  phase: string,
): void {
  assert(plainObject(outputSchema), `${phase} Baidu output schema must be an object`);
  const properties = plainObject(outputSchema.properties) ? outputSchema.properties : {};
  assert.deepEqual(Object.keys(properties).sort(), ['summary', 'title'],
    `${phase} Baidu output schema must contain only summary and title`);
  assert.deepEqual(
    (Array.isArray(outputSchema.required) ? outputSchema.required.map(String) : []).sort(),
    ['summary', 'title'],
    `${phase} Baidu output fields must both be required`,
  );
  assert.equal(plainObject(properties.title) ? properties.title.type : undefined, 'string');
  assert.equal(plainObject(properties.summary) ? properties.summary.type : undefined, 'string');
  const actual = canonicalRows(rows);
  assert.deepEqual(actual, [...expected],
    `${phase} Baidu results must exactly match the contemporaneous visible-DOM oracle`);
}

export function assertBaiduRequirement(requirement: unknown): void {
  assert(plainObject(requirement), 'normalized Baidu requirement must be an object');
  assert(Array.isArray(requirement.requiredInputs), 'Baidu requiredInputs must be an array');
  const required = requirement.requiredInputs.filter(plainObject);
  assert.deepEqual(required.map((input) => [input.name, input.type]), [['keyword', 'string']],
    'Baidu requirement must declare keyword as its only required input');
  assert.deepEqual(requirement.optionalInputs, [], 'Baidu requirement must not declare optional inputs');
  assert(Array.isArray(requirement.outputFields), 'Baidu outputFields must be an array');
  const outputs = requirement.outputFields.filter(plainObject);
  assert.deepEqual(outputs.map((field) => [field.name, field.type]).sort(), [
    ['summary', 'string'],
    ['title', 'string'],
  ], 'Baidu requirement must preserve title and summary string fields');
  assert(
    typeof requirement.description === 'string'
    && requirement.description.includes('固定等待时长不能作为导航或结果就绪条件'),
    'Baidu requirement must preserve the fixed elapsed time readiness prohibition',
  );
}

function actionObjects(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) return value.flatMap(actionObjects);
  if (!plainObject(value)) return [];
  return [
    ...(typeof value.action === 'string' ? [value] : []),
    ...Object.values(value).flatMap(actionObjects),
  ];
}

function targetIdentity(target: unknown, selectors: Record<string, unknown>): string {
  if (!plainObject(target)) return '';
  const ref = typeof target.$ref === 'string' ? target.$ref : '';
  const alias = ref ? selectors[ref] : undefined;
  if (ref && !plainObject(alias)) return '';
  const resolved = plainObject(alias) ? { ...alias, ...target } : { ...target };
  delete resolved.$ref;
  delete resolved.visible;
  delete resolved.timeout;
  delete resolved.multiple;
  const canonicalize = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(canonicalize);
    if (!plainObject(value)) return value;
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, child]) => [key, canonicalize(child)]),
    );
  };
  return JSON.stringify(canonicalize(resolved));
}

function assertRepeatedExtractionReadiness(
  steps: unknown,
  selectors: Record<string, unknown>,
  path: string,
): void {
  if (!Array.isArray(steps)) return;
  for (let index = 0; index < steps.length; index += 1) {
    const rawStep: unknown = steps[index];
    if (!plainObject(rawStep)) continue;
    const step: Record<string, unknown> = rawStep;
    if (step.action === 'extract' && step.multiple === true) {
      let readinessIndex = index - 1;
      while (readinessIndex >= 0) {
        const candidate = steps[readinessIndex];
        if (!plainObject(candidate) || candidate.action !== 'waitForTimeout' || candidate.condition != null) {
          break;
        }
        readinessIndex -= 1;
      }
      const rawPrevious: unknown = readinessIndex >= 0 ? steps[readinessIndex] : undefined;
      const previous: Record<string, unknown> = plainObject(rawPrevious) ? rawPrevious : {};
      assert(
        previous.action === 'waitForElementVisible'
        && targetIdentity(previous.target, selectors) !== ''
        && targetIdentity(previous.target, selectors) === targetIdentity(step.target, selectors),
        `${path}[${index}] repeated extraction must have observable visible-element readiness `
        + 'for its exact target; fixed elapsed time is not readiness',
      );
    }
    for (const branch of ['steps', 'then', 'else', 'default', 'onOpen']) {
      assertRepeatedExtractionReadiness(step[branch], selectors, `${path}[${index}].${branch}`);
    }
    if (Array.isArray(step.cases)) {
      step.cases.forEach((entry, caseIndex) => {
        if (plainObject(entry)) {
          assertRepeatedExtractionReadiness(
            entry.steps,
            selectors,
            `${path}[${index}].cases[${caseIndex}].steps`,
          );
        }
      });
    }
  }
}

export interface BaiduRuleAssertionOptions {
  requireExactRepeatedReadiness?: boolean;
}

export function assertBaiduRule(
  rule: unknown,
  options: BaiduRuleAssertionOptions = {},
): void {
  assert(plainObject(rule), 'approved Baidu rule must be an object');
  const serialized = JSON.stringify(rule);
  assert(JSON.stringify(rule.domain).includes('baidu.com'),
    'approved Baidu rule domain must remain scoped to baidu.com');
  assert(serialized.includes('{{keyword}}'), 'approved Baidu rule must use the declared keyword task input');
  assert(!/evaluate|executeJavascript|solveCaptcha/.test(serialized),
    'approved Baidu rule must not contain arbitrary script or CAPTCHA actions');
  const actions = actionObjects([rule.steps, rule.hooks]);
  const selectors = plainObject(rule.selectors) ? rule.selectors : {};
  if (options.requireExactRepeatedReadiness !== false) {
    assertRepeatedExtractionReadiness(rule.steps, selectors, 'steps');
    if (plainObject(rule.hooks)) {
      for (const [name, steps] of Object.entries(rule.hooks)) {
        assertRepeatedExtractionReadiness(steps, selectors, `hooks.${name}`);
      }
    }
  }
  const extractions = actions.filter((action) => MODEL_EXTRACTION_ACTIONS.has(String(action.action)));
  assert(extractions.length > 0, 'approved Baidu rule must contain extraction actions');
  for (const extraction of extractions) {
    if (!plainObject(extraction.target)) continue;
    const target = extraction.target;
    const ref = typeof target.$ref === 'string' ? target.$ref : '';
    const resolved = ref && plainObject(selectors[ref]) ? { ...selectors[ref], ...target } : target;
    assert.equal(resolved.visible, true,
      `Baidu ${String(extraction.action)} target must explicitly require visible content`);
    assert(!/noscript|\[hidden\]|aria-hidden|script|template/i.test(JSON.stringify(resolved)),
      `Baidu ${String(extraction.action)} target must not address hidden or script content`);
  }
}

export interface BaiduRecordingForecast {
  payloadBytes: number;
  promptBytes: number;
  estimatedRecordingTokens: number;
  estimatedGenerationInputTokens: number;
  forecastLimit: number;
  withinForecast: boolean;
}

/**
 * Conservatively reconstruct the one-shot generation prompt from the current
 * Go system prompt, structured requirement, deterministic recording baseline,
 * complete recording, and the selector catalog's maximum 32 KiB prompt
 * allowance. The 3-bytes/token estimate mirrors the server's conservative
 * workflow chunk budget. The caller supplies the token ceiling calculated
 * from the selected model's real-site dollar budget.
 */
export function forecastBaiduGeneration(
  recording: PageAgentRecording,
  forecastLimit: number,
): BaiduRecordingForecast {
  const workflowSource = fs.readFileSync(
    path.resolve(__dirname, '..', '..', 'server', 'internal', 'llm', 'dsl', 'workflow.go'),
    'utf8',
  );
  const generationSystemPrompt = readGoRawStringConstant(workflowSource, 'generationSystemPrompt');
  const baseline = convert(recording);
  const recordingJSON = JSON.stringify(recording);
  const userPrompt = [
    'Generate the complete provisional provider rule and copy catalogHash to selectorCatalogHash.',
    '',
    'Confirmed requirement:',
    JSON.stringify(BAIDU_SEARCH_REQUIREMENT),
    '',
    'Baseline provider rule:',
    JSON.stringify(baseline),
    '',
    'Deterministic page-text-free selector candidate catalog:',
    '{"version":"selector-catalog-v5","catalogHash":"<forecast>","candidates":[]}',
    '',
    'Complete sanitized recording:',
    recordingJSON,
  ].join('\n');
  const payloadBytes = Buffer.byteLength(JSON.stringify(recording), 'utf8');
  const fullPrompt = `${generationSystemPrompt}\n${userPrompt}`;
  const promptBytes = Buffer.byteLength(fullPrompt, 'utf8') + 32 * 1024;
  const estimatedRecordingTokens = Math.ceil(payloadBytes / 3);
  const estimatedGenerationInputTokens = Math.ceil(promptBytes / 3);
  return {
    payloadBytes,
    promptBytes,
    estimatedRecordingTokens,
    estimatedGenerationInputTokens,
    forecastLimit,
    withinForecast: estimatedGenerationInputTokens <= forecastLimit,
  };
}
