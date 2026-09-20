import * as fs from 'node:fs';
import * as path from 'node:path';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { runRule } from '../../src/scriptcat-engine/executor';
import { findElement, findElements } from '../../src/scriptcat-engine/selectors';
import type { Environment, Rule, Transport } from '../../src/scriptcat-engine/types';
import {
  BAIDU_SEARCH_REQUIREMENT,
  BAIDU_TEST_KEYWORD,
  assertBaiduRequirement,
  assertBaiduRowsEqual,
  assertBaiduRule,
  baiduEnvironmentBlockForURL,
  baiduRowsHash,
  baiduSearchQueryBlockForURL,
  collectBaiduReplayExecutionOracle,
  collectVisibleBaiduResults,
  forecastBaiduGeneration,
} from './baidu-search';
import type { Page } from 'playwright';
import type { PageAgentRecording } from '../../src/rule-generator/types';

interface HistoricalDSL {
  schema: string;
  scenario: string;
  providerAdapter: string;
  model: string;
  promptVersion: string;
  artifactSha256: string;
  provisionalHash: string;
  observedReplayFailure: string;
  rule: Rule;
}

function loadHistoricalBaiduDSL(): HistoricalDSL {
  return JSON.parse(fs.readFileSync(
    path.resolve(__dirname, '..', 'fixtures', 'history', 'baidu-qwen3.6-flash-2026-07-23.json'),
    'utf8',
  )) as HistoricalDSL;
}

function recordingWithText(text: string): PageAgentRecording {
  return {
    version: '2.0.0',
    meta: {
      startUrl: 'https://www.baidu.com/',
      title: '百度一下',
      recordedAt: new Date(0).toISOString(),
      domain: 'www.baidu.com',
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v1',
    },
    events: [],
    snapshots: [{
      timestamp: 0,
      url: 'https://www.baidu.com/',
      selectorMap: {},
      phase: 'initial',
      domTree: { type: 'element', tagName: 'main', children: [{ type: 'text', text }] },
    }],
    termination: { reason: 'user', message: 'done', timestamp: 1, complete: true },
  };
}

describe('Baidu live-search qualification contract', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <main id="content_left">
        <div class="result c-container">
          <h3><a>Ordinary one</a></h3><div class="c-abstract">First visible summary</div>
        </div>
        <div class="result c-container" hidden>
          <h3><a>Hidden</a></h3><div class="c-abstract">Hidden summary</div>
        </div>
        <div class="result c-container">
          <span>广告</span><h3><a>Promoted</a></h3><div class="c-abstract">Ad summary</div>
        </div>
        <div class="result-op c-container">
          <h3><a>Knowledge card</a></h3><div class="c-abstract">Rich summary</div>
        </div>
        <div class="result c-container">
          <h3><a>Ordinary two</a></h3><div class="content-right_hash">Second visible summary</div>
        </div>
        <div class="result c-container">
          <h3><a>No summary</a></h3>
        </div>
      </main>`;
  });

  it('collects only unique visible ordinary cards with title and summary', () => {
    expect(collectVisibleBaiduResults()).toEqual([
      { title: 'Ordinary one', summary: 'First visible summary' },
      { title: 'Ordinary two', summary: 'Second visible summary' },
    ]);
  });

  it('enforces exact ordered equality against a contemporaneous oracle', () => {
    const expected = [
      { title: 'Ordinary one', summary: 'First visible summary' },
      { title: 'Ordinary two', summary: 'Second visible summary' },
    ];
    const schema = {
      type: 'object',
      properties: { title: { type: 'string' }, summary: { type: 'string' } },
      required: ['title', 'summary'],
      additionalProperties: false,
    };
    expect(() => assertBaiduRowsEqual(expected, schema, expected, 'replay')).not.toThrow();
    expect(() => assertBaiduRowsEqual(expected.slice(1), schema, expected, 'replay'))
      .toThrow(/exactly match/);
    expect(baiduRowsHash([{ summary: ' First   visible summary ', title: ' Ordinary one ' }]))
      .toBe(baiduRowsHash([{ title: 'Ordinary one', summary: 'First visible summary' }]));
  });

  it('requires the structured keyword contract and visible keyword-bound rule', () => {
    expect(() => assertBaiduRequirement(BAIDU_SEARCH_REQUIREMENT)).not.toThrow();
    expect(() => assertBaiduRule({
      domain: ['baidu.com'],
      selectors: { results: { selector: '#content_left .result', visible: true } },
      steps: [
        { action: 'type', target: { selector: '#kw' }, value: '{{keyword}}' },
        { action: 'waitForElementVisible', target: { $ref: 'results', visible: true } },
        { action: 'extract', target: { $ref: 'results' }, multiple: true, fields: {} },
      ],
    })).not.toThrow();
    expect(() => assertBaiduRule({
      domain: 'www.baidu.com',
      steps: [
        {
          action: 'navigate',
          url: 'https://www.baidu.com/s?wd={{keyword}}',
          waitUntil: 'domcontentloaded',
        },
        {
          action: 'waitForElementVisible',
          target: { selector: '#content_left > .result', visible: true },
        },
        {
          action: 'extract',
          target: { selector: '#content_left > .result', visible: true },
          multiple: true,
          fields: {},
        },
      ],
    })).not.toThrow();
    expect(() => assertBaiduRule({
      domain: ['baidu.com'],
      selectors: { results: { selector: '#content_left .result', visible: true } },
      steps: [
        { action: 'type', target: { selector: '#kw' }, value: '{{keyword}}' },
        { action: 'pressKey', keys: ['Enter'] },
        { action: 'waitForTimeout', ms: 2000 },
        { action: 'extract', target: { $ref: 'results' }, multiple: true, fields: {} },
      ],
    })).toThrow(/observable visible-element readiness/);
    expect(() => assertBaiduRule({
      domain: ['baidu.com'],
      steps: [{ action: 'type', target: { selector: '#kw' }, value: 'hard-coded' }],
    })).toThrow(/keyword task input/);
    expect(() => assertBaiduRequirement({
      ...BAIDU_SEARCH_REQUIREMENT,
      description: '采集百度普通结果，但丢失导航就绪约束。',
    })).toThrow(/fixed elapsed time/i);
  });

  it('accepts equivalent inline and alias readiness targets', () => {
    expect(() => assertBaiduRule({
      domain: ['baidu.com'],
      selectors: { results: { selector: '#content_left > .result', visible: true } },
      steps: [
        { action: 'type', target: { selector: '#kw' }, value: '{{keyword}}' },
        {
          action: 'waitForElementVisible',
          target: { selector: '#content_left > .result', visible: true },
        },
        { action: 'extract', target: { $ref: 'results' }, multiple: true, fields: {} },
      ],
    })).not.toThrow();
  });

  it('checks readiness inside onOpen action branches', () => {
    expect(() => assertBaiduRule({
      domain: ['baidu.com'],
      selectors: {
        results: { selector: '#content_left > .result', visible: true },
        fallback: { selector: '#fallback > .result', visible: true },
      },
      steps: [
        { action: 'type', target: { selector: '#kw' }, value: '{{keyword}}' },
        { action: 'waitForElementVisible', target: { $ref: 'results', visible: true } },
        { action: 'extract', target: { $ref: 'results' }, multiple: true, fields: {} },
        {
          action: 'circuitBreaker',
          onOpen: [
            { action: 'extract', target: { $ref: 'fallback' }, multiple: true, fields: {} },
          ],
        },
      ],
    })).toThrow(/onOpen.*observable visible-element readiness/);
  });

  it('waits on delayed visible results instead of trusting the failed fixed-time shape', async () => {
    const fixedWaitRule = {
      id: 'baidu-readiness-regression',
      version: '1',
      name: 'Baidu readiness regression',
      domain: ['baidu.com'],
      entry: 'https://www.baidu.com/',
      selectors: {
        results: { selector: '#content_left > .result', visible: true },
      },
      steps: [
        { action: 'type', target: { selector: '#kw' }, value: '{{keyword}}' },
        { action: 'pressKey', keys: ['Enter'] },
        { action: 'waitForTimeout', ms: 2000 },
        {
          action: 'extract',
          name: 'items',
          target: { $ref: 'results' },
          multiple: true,
          onEmpty: 'fail',
          fields: {
            title: { type: 'text', selector: '.t', visible: true },
            summary: { type: 'text', selector: '[role="text"]', visible: true },
          },
        },
        {
          action: 'loop',
          type: 'forEach',
          items: '{{extracted.items}}',
          as: 'item',
          steps: [{
            action: 'sendResult',
            payload: {
              title: '{{loopItem.title}}',
              summary: '{{loopItem.summary}}',
            },
            immediate: true,
          }],
        },
      ],
    } as unknown as Rule;
    const visibleWaitRule = {
      ...fixedWaitRule,
      steps: [
        ...fixedWaitRule.steps.slice(0, 2),
        { action: 'waitForElementVisible', target: { $ref: 'results', visible: true } },
        ...fixedWaitRule.steps.slice(2),
      ],
    } as unknown as Rule;

    const runWithDelayedResults = async (rule: Rule) => {
      document.body.innerHTML = '<input id="kw">';
      let clock = 0;
      const readyAt = 8000;
      const rows: unknown[] = [];
      const dateNow = vi.spyOn(Date, 'now').mockImplementation(() => clock);
      const originalRect = HTMLElement.prototype.getBoundingClientRect;
      HTMLElement.prototype.getBoundingClientRect = () => ({
        x: 0, y: 0, top: 0, right: 100, bottom: 20, left: 0,
        width: 100, height: 20, toJSON: () => ({}),
      } as DOMRect);
      const reveal = () => {
        if (clock < readyAt || document.querySelector('#content_left')) return;
        document.body.insertAdjacentHTML('beforeend', `
          <main id="content_left">
            <div class="result">
              <h3><a class="t">Ordinary one</a></h3>
              <div role="text">First visible summary</div>
            </div>
          </main>
        `);
      };
      const targetSelector = (target: unknown): string => (
        typeof target === 'object' && target !== null
        && typeof (target as Record<string, unknown>).selector === 'string'
          ? String((target as Record<string, unknown>).selector)
          : ''
      );
      const transport: Transport = {
        fetchRule: async () => null,
        sendResult: async (payload) => {
          if (payload?.__final !== true) rows.push(payload);
        },
        sendLog: async () => undefined,
        sendHeartbeat: async () => ({ cancelRequested: false }),
        sendStatus: async () => undefined,
        sendSnapshot: async () => undefined,
      };
      const env: Environment = {
        findElement: async (target, timeout = 5000) => {
          const existing = document.querySelector(targetSelector(target));
          if (existing) return existing;
          clock += timeout;
          reveal();
          return document.querySelector(targetSelector(target));
        },
        findElements: async (target, timeout = 5000) => {
          const existing = Array.from(document.querySelectorAll(targetSelector(target)));
          if (existing.length > 0) return existing;
          clock += timeout;
          reveal();
          return Array.from(document.querySelectorAll(targetSelector(target)));
        },
        sleep: async (ms) => {
          clock += ms;
          reveal();
        },
        now: () => clock,
        transport,
        getUrl: () => 'https://www.baidu.com/s',
        getTitle: () => '百度搜索',
        evaluate: async () => undefined,
        screenshot: async () => null,
        saveSnapshot: async () => null,
      };
      try {
        const result = await runRule({
          rule,
          taskId: 'baidu-readiness-regression',
          workerId: 'test-worker',
          variables: { inputs: { keyword: BAIDU_TEST_KEYWORD }, keyword: BAIDU_TEST_KEYWORD },
          env,
        });
        return { result, rows };
      } finally {
        dateNow.mockRestore();
        HTMLElement.prototype.getBoundingClientRect = originalRect;
      }
    };

    const fixed = await runWithDelayedResults(fixedWaitRule);
    expect(fixed.result.status).toBe('failure');
    expect(fixed.result.error?.message).toContain('ElementNotFound');
    expect(fixed.rows).toEqual([]);

    const observable = await runWithDelayedResults(visibleWaitRule);
    expect(observable.result.status).toBe('success');
    expect(observable.rows).toEqual([
      { title: 'Ordinary one', summary: 'First visible summary' },
    ]);
  });

  it('forecasts conservatively and fails the preflight gate for oversized recordings', () => {
    const small = forecastBaiduGeneration(recordingWithText('small'), 130_000);
    expect(small.withinForecast).toBe(true);
    expect(small.promptBytes).toBeGreaterThan(small.payloadBytes);
    const large = forecastBaiduGeneration(recordingWithText('x'.repeat(390_000)), 130_000);
    expect(large.estimatedGenerationInputTokens).toBeGreaterThan(130_000);
    expect(large.withinForecast).toBe(false);
  });

  it('classifies Baidu authentication and cross-domain redirects without query leakage', () => {
    expect(baiduEnvironmentBlockForURL('https://www.baidu.com/s?wd=secret')).toBe('');
    expect(baiduEnvironmentBlockForURL('https://wappass.baidu.com/static/captcha?token=secret'))
      .toBe('human authentication or verification page at wappass.baidu.com/static/captcha');
    expect(baiduEnvironmentBlockForURL('https://example.com/login?token=secret'))
      .toBe('unexpected cross-domain page at example.com');
  });

  it('rejects stale Baidu result pages that were produced by a different replay input', () => {
    expect(baiduSearchQueryBlockForURL(
      `https://www.baidu.com/s?wd=${encodeURIComponent(BAIDU_TEST_KEYWORD)}`,
    )).toBe('');
    expect(baiduSearchQueryBlockForURL('https://www.baidu.com/s?wd=stale-profile-query'))
      .toBe('the result page was produced by an unexpected search keyword');
    expect(baiduSearchQueryBlockForURL('https://www.baidu.com/'))
      .toBe('the browser is not on a Baidu result page');
  });

  it('waits for and collects replay evidence from the exact execution page', async () => {
    const expected = [
      { title: 'Ordinary one', summary: 'First visible summary' },
      { title: 'Ordinary two', summary: 'Second visible summary' },
      { title: 'Ordinary three', summary: 'Third visible summary' },
    ];
    const page = {
      evaluate: vi.fn()
        .mockResolvedValueOnce({ hasResults: true, blockReason: '' })
        .mockResolvedValueOnce(expected),
      url: vi.fn(() => `https://www.baidu.com/s?wd=${encodeURIComponent(BAIDU_TEST_KEYWORD)}`),
      waitForTimeout: vi.fn(async () => undefined),
    } as unknown as Page;
    const stalePage = {
      evaluate: vi.fn(),
      url: vi.fn(() => `https://www.baidu.com/s?wd=${encodeURIComponent(BAIDU_TEST_KEYWORD)}`),
    } as unknown as Page;
    const context = {
      pages: vi.fn()
        .mockReturnValueOnce([stalePage])
        .mockReturnValueOnce([stalePage])
        .mockReturnValue([stalePage, page]),
    };

    await expect(collectBaiduReplayExecutionOracle(
      context,
      BAIDU_TEST_KEYWORD,
      { timeoutMs: 100, pollIntervalMs: 1 },
    )).resolves.toEqual(expected);
    expect(context.pages).toHaveBeenCalledTimes(3);
    expect(stalePage.evaluate).not.toHaveBeenCalled();
    expect(page.evaluate).toHaveBeenCalledTimes(2);
  });

  it('fails closed if the execution page redirects to verification before collection', async () => {
    const page = {
      evaluate: vi.fn(),
      url: vi.fn()
        .mockReturnValueOnce(`https://www.baidu.com/s?wd=${encodeURIComponent(BAIDU_TEST_KEYWORD)}`)
        .mockReturnValue('https://wappass.baidu.com/static/captcha'),
      waitForTimeout: vi.fn(async () => undefined),
    } as unknown as Page;

    await expect(collectBaiduReplayExecutionOracle({
      pages: vi.fn()
        .mockReturnValueOnce([])
        .mockReturnValue([page]),
    })).rejects.toThrow(/human authentication or verification page/);
    expect(page.evaluate).not.toHaveBeenCalled();
  });

  it('fails closed without issuing a second request when the execution page never appears', async () => {
    const context = {
      pages: vi.fn(() => [{
        url: vi.fn(() => 'https://www.baidu.com/s?wd=stale-profile-query'),
      } as unknown as Page]),
    };

    await expect(collectBaiduReplayExecutionOracle(
      context,
      BAIDU_TEST_KEYWORD,
      { timeoutMs: 0, pollIntervalMs: 1 },
    )).rejects.toThrow(/could not observe the exact result page during execution/);
    expect(context.pages).toHaveBeenCalledTimes(2);
  });

  it('stops a pending execution-page capture when the replay phase aborts', async () => {
    const controller = new AbortController();
    const context = { pages: vi.fn(() => [] as Page[]) };
    const capture = collectBaiduReplayExecutionOracle(
      context,
      BAIDU_TEST_KEYWORD,
      { timeoutMs: 10_000, pollIntervalMs: 10_000, signal: controller.signal },
    );

    controller.abort();
    await expect(capture).rejects.toThrow(/capture was aborted/);
    expect(context.pages).toHaveBeenCalledTimes(2);
  });

  it('rejects insufficient rows from the replay execution page', async () => {
    const page = {
      evaluate: vi.fn()
        .mockResolvedValueOnce({ hasResults: true, blockReason: '' })
        .mockResolvedValueOnce([
          { title: 'Ordinary one', summary: 'First visible summary' },
          { title: 'Ordinary two', summary: 'Second visible summary' },
        ]),
      url: vi.fn(() => `https://www.baidu.com/s?wd=${encodeURIComponent(BAIDU_TEST_KEYWORD)}`),
      waitForTimeout: vi.fn(async () => undefined),
    } as unknown as Page;

    await expect(collectBaiduReplayExecutionOracle({
      pages: vi.fn()
        .mockReturnValueOnce([])
        .mockReturnValue([page]),
    })).rejects.toThrow(/at least 3 are required/);
  });

  it('replays the historical Qwen DSL and rejects its empty summary rows', async () => {
    const history = loadHistoricalBaiduDSL();
    expect(history).toMatchObject({
      schema: 'aegiscrawler.live-dsl-history.v1',
      scenario: 'baidu-search',
      providerAdapter: 'openai',
      model: 'qwen3.6-flash',
      promptVersion: 'dsl-workflow-v22',
      artifactSha256: 'eab0b8ca71337283c90b133a1d25cce1aaf6179e25bb8b8ac9abd0d1db2a0352',
      provisionalHash: '4f67adfa329c42e23afe10e2b7db396b59d028a2384c1b55222e5101fc5a6dab',
      observedReplayFailure: 'summary selector .c-row emitted an empty string',
    });
    expect(() => assertBaiduRule(history.rule)).toThrow(/keyword task input/);

    document.body.innerHTML = `
      <textarea id="chat-textarea"></textarea>
      <button id="chat-submit-button" type="button">百度一下</button>
      <main id="results-root"></main>
    `;
    const input = document.querySelector<HTMLTextAreaElement>('#chat-textarea');
    const resultsRoot = document.querySelector<HTMLElement>('#results-root');
    document.querySelector('#chat-submit-button')?.addEventListener('click', () => {
      expect(input?.value).toBe(BAIDU_TEST_KEYWORD);
      if (resultsRoot) {
        resultsRoot.outerHTML = `
          <main id="content_left">
            <div class="result c-container">
              <h3><a class="t">Ordinary one</a></h3>
              <div class="c-row"></div>
              <div class="c-abstract" role="text">First visible summary</div>
            </div>
            <div class="result c-container">
              <h3><a class="t">Ordinary two</a></h3>
              <div class="c-row"></div>
              <div class="c-abstract" role="text">Second visible summary</div>
            </div>
          </main>
        `;
      }
    });

    const originalRect = HTMLElement.prototype.getBoundingClientRect;
    const originalScrollBy = window.scrollBy;
    HTMLElement.prototype.getBoundingClientRect = () => ({
      x: 0, y: 0, top: 0, right: 100, bottom: 20, left: 0,
      width: 100, height: 20, toJSON: () => ({}),
    } as DOMRect);
    window.scrollBy = vi.fn();
    const rows: unknown[] = [];
    const transport: Transport = {
      fetchRule: async () => null,
      sendResult: async (payload) => {
        if (payload?.__final !== true) rows.push(payload);
      },
      sendLog: async () => undefined,
      sendHeartbeat: async () => ({ cancelRequested: false }),
      sendStatus: async () => undefined,
      sendSnapshot: async () => undefined,
    };
    const env: Environment = {
      findElement: (target, timeout) => findElement(target, timeout),
      findElements: (target, timeout) => findElements(target, timeout),
      sleep: async () => undefined,
      now: () => Date.now(),
      transport,
      getUrl: () => 'https://www.baidu.com/s',
      getTitle: () => '百度搜索',
      evaluate: async () => undefined,
      screenshot: async () => null,
      saveSnapshot: async () => null,
    };

    try {
      const result = await runRule({
        rule: history.rule,
        taskId: 'historical-baidu-replay',
        workerId: 'history-test',
        variables: { inputs: { keyword: BAIDU_TEST_KEYWORD } },
        env,
      });
      expect(result.status).toBe('success');
      expect(rows).toEqual([
        { title: 'Ordinary one', summary: '' },
        { title: 'Ordinary two', summary: '' },
      ]);
      const oracle = collectVisibleBaiduResults();
      expect(oracle).toEqual([
        { title: 'Ordinary one', summary: 'First visible summary' },
        { title: 'Ordinary two', summary: 'Second visible summary' },
      ]);
      expect(() => assertBaiduRowsEqual(rows, {
        type: 'object',
        properties: { title: { type: 'string' }, summary: { type: 'string' } },
        required: ['title', 'summary'],
        additionalProperties: false,
      }, oracle, 'historical replay')).toThrow(/empty summary/);
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalRect;
      window.scrollBy = originalScrollBy;
      document.body.textContent = '';
    }
  });
});
