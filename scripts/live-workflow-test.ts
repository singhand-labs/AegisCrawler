#!/usr/bin/env ts-node
/**
 * Opt-in LIVE-provider full-workflow suite (NON-CI). See docs/live-workflow-tests.md.
 *
 * Tiering:
 *   - hermetic staging e2e (npm run test:e2e:staging): free, fake provider, CI default.
 *   - this suite's fixture scenarios: opt-in, real paid provider against tiny
 *     local fixture pages. fixture-candidates + fixture-models are the default.
 *   - tier 3 public scenarios run ONLY when explicitly listed in
 *     AEGIS_LIVE_WORKFLOW_SCENARIOS; Apple historically consumed ~4-8M input
 *     tokens, while the quote scenarios are smaller generalization canaries.
 *
 * Environment:
 *   AEGIS_LIVE_ALLOW_PAID_LLM / AEGIS_LIVE_ALLOW_MUTATIONS   approval strings
 *     (validated by loadLocalLLMConfig; see scripts/local-live-llm-canary.ts)
 *   AEGIS_LOCAL_LLM_PROVIDER / AEGIS_LOCAL_LLM_BASE_URL / AEGIS_LOCAL_LLM_MODEL /
 *   AEGIS_LOCAL_LLM_API_KEY_FILE                             live provider config
 *   AEGIS_LIVE_WORKFLOW_SCENARIOS   comma list; default 'fixture-candidates,fixture-models'
 *   AEGIS_LIVE_WORKFLOW_HEADED=1    show the recording browser (default headless)
 *   AEGIS_LIVE_HUMAN_DEMO=1         pause for a real human demonstration (one headed scenario only)
 *   AEGIS_LIVE_HUMAN_REQUIREMENT=1  pause for real human requirement entry/selection
 *   AEGIS_LIVE_RECORD_ONLY=1        free Baidu preflight; model pricing only, no provider key loaded
 *   AEGIS_LIVE_SAVE_RECORDING=1      encrypt an approved recording (provider-free scripted Books or paid human site)
 *   AEGIS_LIVE_REUSE_RECORDING=1     reuse that recording without a human demonstration
 *   AEGIS_LIVE_PROFILE_WARMUP=1       optionally refresh Baidu verification in the same headed reuse context
 *   AEGIS_LIVE_LLM_MAX_INPUT_TOKENS server LLM_MAX_INPUT_TOKENS override (default 200000)
 *   AEGIS_LIVE_LLM_MAX_OUTPUT_TOKENS server LLM_MAX_OUTPUT_TOKENS override
 *   AEGIS_LIVE_LLM_REQUEST_TIMEOUT   provider request timeout below the 10-minute stall window (default 120s)
 *
 * The suite never prints or persists the LLM API key, never injects
 * credentials into page JavaScript, and uses no GM shims.
 */

import * as readline from 'node:readline/promises';
import { deepStrictEqual } from 'node:assert';
import { stdin, stdout } from 'node:process';
import type { BrowserContext, Page } from 'playwright';
import type { PageAgentRecording } from '../src/rule-generator/types';
import {
  LiveEnvironment,
  LiveWorkflowReport,
  LiveWorkflowScenario,
  assert,
  bootLiveEnvironment,
  captureLiveScenarioRecording,
  runScenario,
  shutdownLiveEnvironment,
  startShopFixtureServer,
} from './live-workflow/support';
import { loadLocalLLMConfig } from './local-live-llm-canary';
import {
  APPLE_MODEL_REQUIREMENT,
  assertAppleModelRows,
  assertAppleModelRequirement,
  assertAppleVisibleExtractionRule,
} from './live-workflow/apple-models';
import {
  CONFIGURATOR_MODEL_REQUIREMENT,
  assertConfiguratorModelRows,
  assertConfiguratorModelRequirement,
  assertConfiguratorVisibleExtractionRule,
} from './live-workflow/configurator-models';
import {
  MODEL_FIXTURE_INPUT_TOKEN_BUDGET,
} from './live-workflow/qualification';
import {
  LiveWorkflowScenarioName,
  isHumanRequirementScenario,
  isQuotesLiveScenario,
  parseScenarioSelection,
  validateHumanDemoConfiguration,
  validateHumanRequirementConfiguration,
  validateRecordOnlyConfiguration,
  validateReusableRecordingConfiguration,
} from './live-workflow/human-demonstration';
import {
  BAIDU_ENTRY_URL,
  BAIDU_SEARCH_REQUIREMENT,
  BAIDU_TEST_KEYWORD,
  BaiduResultRow,
  assertBaiduRequirement,
  assertBaiduRowsEqual,
  assertBaiduRule,
  baiduRowsHash,
  collectBaiduReplayExecutionOracle,
  collectBaiduPageOracle,
  forecastBaiduGeneration,
} from './live-workflow/baidu-search';
import {
  BING_COST_BUDGET_USD,
  BING_ENTRY_URL,
  BING_QUERY,
  BING_SEARCH_REQUIREMENT,
  type BingSelectedResult,
  assertBingRecording,
  assertBingRequirement,
  assertBingRows,
  assertBingRule,
  bingSearchDemo,
} from './live-workflow/bing-search';
import { BING_FOOTER_REQUIREMENT, type BingFooterSelection, assertBingFooterRecording, assertBingFooterRequirement, assertBingFooterRows, assertBingFooterRule, bingFooterDemo } from './live-workflow/bing-footer';
import { BING_READONLY_QUERY, BING_READONLY_REQUIREMENT, assertBingReadonlyRecording, assertBingReadonlyRequirement, assertBingReadonlyRows, assertBingReadonlyRule, bingSearchReadonlyDemo } from './live-workflow/bing-search-readonly';
import {
  BAIDU_QUALIFICATION_PROFILE,
  resolveDedicatedProfile,
  type DedicatedProfileConfig,
} from './live-workflow/dedicated-profile';
import {
  capRealSiteCostBudget,
  estimateContextWindowCostUpperBound,
  estimateRecordingInputTokens,
  resolveRealSiteCostBudget,
  type RealSiteCostBudget,
} from './live-workflow/real-site-cost';
import {
  MDN_COST_BUDGET_USD,
  MDN_ENTRY_URL,
  MDN_GUIDE_REQUIREMENT,
  type MDNGuideRow,
  assertMDNRecording,
  assertMDNRule,
  assertMDNRows,
  collectMDNGuideOracle,
  mdnGuideDemo,
  prepareMDNContext,
  prepareMDNPage,
} from './live-workflow/mdn-guide';
import {
  IANA_COST_BUDGET_USD,
  IANA_ENTRY_URL,
  IANA_LINK_ROUNDTRIP_REQUIREMENT,
  type IANAArticleRow,
  assertIANARoundtripRecording,
  assertIANARoundtripRule,
  assertIANARows,
  collectIANAArticleOracle,
  ianaLinkRoundtripDemo,
  prepareIANAContext,
  prepareIANAPage,
} from './live-workflow/iana-link-roundtrip';
import {
  BOOKS_CATEGORY_REQUIREMENT,
  BOOKS_COST_BUDGET_USD,
  BOOKS_DEEPSEEK_CONTEXT_TOKENS,
  BOOKS_ENTRY_URL,
  BOOKS_MAX_ESTIMATED_INPUT_TOKENS,
  BOOKS_MAX_OUTPUT_TOKENS,
  BOOKS_MISSING_CATEGORY,
  BOOKS_REPLAY_CATEGORY,
  BOOKS_SEQUENTIAL_ART_CATEGORY,
  BOOKS_TRAVEL_CATEGORY,
  type BookRow,
  type BooksCategoryOracle,
  assertBooksAcceptedRequestCostAuthorized,
  assertBooksExecutionPage,
  assertBooksQualificationEnvironment,
  assertBooksRecording,
  assertBooksRequirement,
  assertBooksRowsEqual,
  assertBooksRule,
  bindBooksCategoryInput,
  booksRowsHash,
  booksCategoryDemo,
  collectBooksCategoryOracle,
  forecastBooksDirectRequest,
} from './live-workflow/books-category-matrix';
import {
  HOCKEY_BOSTON_QUERY,
  HOCKEY_COST_BUDGET_USD,
  HOCKEY_DETROIT_QUERY,
  HOCKEY_ENTRY_URL,
  HOCKEY_REPLAY_QUERY,
  HOCKEY_REQUIREMENT,
  type HockeyOracle,
  type HockeyRow,
  assertHockeyExecutionPage,
  assertHockeyRecording,
  assertHockeyRequirement,
  assertHockeyRowsEqual,
  assertHockeyRule,
  bindHockeyInput,
  collectHockeyOracle,
  hockeyDemo,
  hockeyRowsHash,
} from './live-workflow/scrape-site-hockey';
import {
  RFC_EDITOR_COST_BUDGET_USD,
  RFC_EDITOR_ENTRY_URL,
  RFC_EDITOR_REPLAY_SECTION,
  RFC_EDITOR_TASK_SECTION,
  RFC_EDITOR_REQUIREMENT,
  type RFCEditorRow,
  assertRFCEditorRecording,
  assertRFCEditorRequirement,
  assertRFCEditorRowsEqual,
  assertRFCEditorRule,
  bindRFCEditorSection,
  collectRFCEditorOracle,
  rfcEditorDemo,
} from './live-workflow/rfc-editor-section-readonly';
import {
  DUCKDUCKGO_COST_BUDGET_USD,
  DUCKDUCKGO_ENTRY_URL,
  DUCKDUCKGO_QUERY,
  DUCKDUCKGO_REQUIREMENT,
  type DuckDuckGoSelectedResult,
  assertDuckDuckGoRecording,
  assertDuckDuckGoRequirement,
  assertDuckDuckGoRows,
  assertDuckDuckGoRule,
  assertDuckDuckGoSelectionVisible,
  duckDuckGoSearchReadonlyDemo,
} from './live-workflow/duckduckgo-search-readonly';
import {
  SQLITE_COST_BUDGET_USD, SQLITE_DOCS_ENTRY_URL, SQLITE_DOCS_REQUIREMENT,
  type SQLiteDocumentContext, assertSQLiteRecording, assertSQLiteRequirement,
  assertSQLiteRows, assertSQLiteRule, bindSQLiteInputs, sqliteDocsDemo,
} from './live-workflow/sqlite-docs-roundtrip';
import {
  loadReusableBaiduRecording,
  loadReusableBooksRecording,
  loadReusableQuotesRecording,
  reusableBaiduRecordingPaths,
  reusableBooksRecordingPaths,
  reusableQuotesRecordingPaths,
  saveReusableBaiduRecording,
  saveReusableBooksRecording,
  saveReusableQuotesRecording,
  type QuotesReusableRecordingScenario,
} from './live-workflow/reusable-recording';
import {
  QUOTES_BY_TAG_ENTRY_URL,
  QUOTES_BY_TAG_REQUIREMENT,
  QUOTES_REPLAY_TAG,
  QUOTES_TASK_TAG,
  QuoteRow,
  type QuotesContract,
  assertQuotesExecutionPage,
  assertQuotesContractRequirement,
  assertQuotesContractRowsEqual,
  assertQuotesContractRule,
  assertQuotesRecording,
  bindQuotesContractInput,
  collectQuotesPaginationOracle,
  quotesTagPageBlockForURL,
} from './live-workflow/quotes-by-tag';
import {
  QUOTES_ONE_PAGE_CONTRACT,
  QUOTES_ONE_PAGE_REQUIREMENT,
  QUOTES_ONE_PAGE_TASK_PAGES,
  QUOTES_PROJECTION_CONTRACT,
  QUOTES_PROJECTION_REQUIREMENT,
  QUOTES_PROJECTION_TASK_PAGES,
  QUOTES_RENAMED_CONTRACT,
  QUOTES_RENAMED_REQUIREMENT,
  QUOTES_RENAMED_TASK_PAGES,
} from './live-workflow/quotes-generalization';
import {
  QUOTES_HUMAN_CHOOSE_ENTRY_URL,
  QUOTES_HUMAN_CHOOSE_TAG,
  QUOTES_HUMAN_INPUT_CONTRACT,
  QUOTES_HUMAN_INPUT_ENTRY_URL,
  QUOTES_HUMAN_INPUT_REQUIREMENT,
  QUOTES_HUMAN_INPUT_TASK_PAGES,
  QUOTES_HUMAN_INPUT_TEXT,
  assertHumanChosenQuotesRule,
  assertQuotesHumanChooseRecording,
  assertQuotesHumanInputRecording,
  deriveHumanChosenQuotesContract,
  isHumanChosenQuotesRequirement,
} from './live-workflow/quotes-human-generalization';

const FIXTURE_INPUT_TOKEN_BUDGET = 300_000;
type ScenarioName = LiveWorkflowScenarioName;
type StructuredRequirementSpec = Extract<
  LiveWorkflowScenario['requirement'],
  { mode: 'structured' }
>['spec'];

function quotesRecordingScenario(name: ScenarioName): QuotesReusableRecordingScenario {
  if (name === 'quotes-human-choose-requirement'
    || name === 'quotes-human-input-requirement') {
    return name;
  }
  return 'quotes-by-tag';
}

const HUMAN_DEMO_INSTRUCTIONS: Record<ScenarioName, string[]> = {
  'fixture-candidates': [
    'Type iphone in the search box, click Search, then scroll down slightly.',
  ],
  'fixture-intent': [
    'Type iphone in the search box, click Search, then scroll down slightly.',
  ],
  'fixture-models': [
    'Select 11-inch model, Sky Blue, 128GB, and Wi-Fi.',
    'Wait for the current model card to appear, then click Next model once.',
  ],
  apple: [
    'Scroll the visible iPad Air configurator into view.',
    'Select the 11-inch model, Space Gray, and 256GB using visible controls only.',
  ],
  'mdn-css-grid': [
    'Read the visible CSS grid guide heading and introduction.',
    'Watch the foreground MDN tab while the pointer moves into the main article.',
    'Observe three paced downward wheel scrolls.',
    'Do not click sidebar or advertisement links.',
  ],
  'iana-link-roundtrip': [
    'Click the visible IANA-managed Reserved Domains link on the Example Domains page.',
    'Wait for the reserved-domains page to load, then use browser Back.',
    'Confirm the Example Domains page is visible again before extraction.',
  ],
  'baidu-search': [
    `Click the Baidu search box and type exactly: ${BAIDU_TEST_KEYWORD}`,
    'Click 百度一下 and wait until the left result list is stable.',
    'Scroll through the first result page without opening any result link.',
  ],
  'bing-search-full': [
    `Type and submit exactly: ${BING_QUERY}`,
    'Scroll through ordinary results, open one visible organic result, read it, then use Back.',
    'After returning to Bing, leave the visible ordinary result list on screen.',
  ],
  'bing-footer-link-roundtrip': [
    'Scroll to the visible Bing footer and open the exact Legal link.',
    'Read and scroll the HTTPS destination, then return to the Bing homepage.',
  ],
  'bing-search-readonly': [`Click the Bing search box and type exactly: ${BING_READONLY_QUERY}`, 'Press Enter, scroll ordinary results, and do not open any result.'],
  'books-category-matrix': [
    `Click the visible ${BOOKS_REPLAY_CATEGORY} category in the sidebar.`,
    'Wait for page 1, scroll through the visible book cards, then click Next.',
    'Wait for page 2, scroll through its visible book cards, and do not open a book.',
  ],
  'scrape-site-hockey': [
    `Search visibly for ${HOCKEY_REPLAY_QUERY} and submit exactly once.`,
    'Read and scroll every visible team row, then follow only the visible Next control.',
    'Stop after the terminal page; do not construct page URLs or open unrelated links.',
  ],
  'rfc-editor-section-readonly': [
    'Click the one visible RFC 2606 table-of-contents link for section 3.',
    'Read the visible section heading and first prose paragraph, then scroll down slightly.',
    'Do not open external links or download another RFC representation.',
  ],
  'duckduckgo-search-readonly': [
    `Click the DuckDuckGo search box and type exactly: ${DUCKDUCKGO_QUERY}`,
    'Press Enter exactly once, scroll ordinary results, and do not open any result.',
  ],
  'sqlite-docs-roundtrip': [
    'Click the exact visible SQL Syntax link on the SQLite documentation index.',
    'Read the language-page heading and introduction, then observe exactly two bounded scrolls.',
    'Use browser Back to restore the documentation index; do not search, download, or leave sqlite.org.',
  ],
  'quotes-by-tag': [
    `Click the visible ${QUOTES_REPLAY_TAG} tag in the Top Ten tags list.`,
    'Wait for the first tag page, scroll through its visible quote cards, then click Next.',
    'Wait for page 2, scroll through its visible quote cards, and do not open an author link.',
  ],
  'quotes-one-page': [
    `Click the visible ${QUOTES_REPLAY_TAG} tag in the Top Ten tags list.`,
    'Wait for the first tag page, scroll through its visible quote cards, then click Next.',
    'Wait for page 2, scroll through its visible quote cards, and do not open an author link.',
  ],
  'quotes-renamed-contract': [
    `Click the visible ${QUOTES_REPLAY_TAG} tag in the Top Ten tags list.`,
    'Wait for the first tag page, scroll through its visible quote cards, then click Next.',
    'Wait for page 2, scroll through its visible quote cards, and do not open an author link.',
  ],
  'quotes-output-projection': [
    `Click the visible ${QUOTES_REPLAY_TAG} tag in the Top Ten tags list.`,
    'Wait for the first tag page, scroll through its visible quote cards, then click Next.',
    'Wait for page 2, scroll through its visible quote cards, and do not open an author link.',
  ],
  'quotes-human-choose-requirement': [
    `Click the visible ${QUOTES_HUMAN_CHOOSE_TAG} tag in the Top Ten tags list.`,
    'On its single page, scroll through every visible quote card.',
    'Do not click an author, another tag, or any pagination control.',
  ],
  'quotes-human-input-requirement': [
    'The recording starts directly on the humor tag page; do not return to the homepage.',
    'Scroll through page 1, click the visible Next link, then wait for page 2.',
    'Scroll through every visible quote card on page 2 and do not open an author link.',
  ],
};

function humanDemo(scenario: ScenarioName): (page: Page) => Promise<void> {
  return async (page: Page) => {
    await page.bringToFront();
    console.log(`\n[live-wf:${scenario}] REAL HUMAN RECORDING IS ACTIVE`);
    for (const instruction of HUMAN_DEMO_INSTRUCTIONS[scenario]) console.log(`  - ${instruction}`);
    console.log('  - Do not use DevTools, scripts, or hidden/noscript content.');
    const prompt = readline.createInterface({ input: stdin, output: stdout });
    try {
      await prompt.question('[live-wf] Press ENTER here only after the visible human demonstration is complete: ');
    } finally {
      prompt.close();
    }
  };
}

function humanRequirement(
  scenario: ScenarioName,
): NonNullable<LiveWorkflowScenario['completeHumanRequirement']> {
  return async (intent, phase) => {
    await intent.bringToFront();
    console.log(`\n[live-wf:${scenario}] REAL HUMAN REQUIREMENT OPERATION IS ACTIVE`);
    if (phase === 'candidate-selection') {
      console.log('  - Review all three generated requirement cards in the visible extension wizard.');
      console.log('  - The terminal printed the candidate number(s) that passed the fail-closed semantic filter.');
      console.log('  - Select exactly one of those eligible fixed-page candidates with no inputs.');
      console.log('  - It must return quotation text and author as strings; an author URL string is optional.');
      console.log('  - Do not select a tags/metadata-only, array-valued, or author-directory candidate.');
      console.log('  - Do not click Next, Confirm, Generate DSL, or any other wizard button.');
    } else {
      console.log('  - Type or paste the following text exactly into the visible custom-requirement box:');
      console.log(`\n${QUOTES_HUMAN_INPUT_TEXT}\n`);
      console.log('  - Do not click the wizard primary button; the harness will verify the exact text first.');
    }
    const prompt = readline.createInterface({ input: stdin, output: stdout });
    try {
      await prompt.question('[live-wf] Press ENTER here only after the visible human requirement operation is complete: ');
    } finally {
      prompt.close();
    }
  };
}

/**
 * Scripted human demonstration on the shop fixture: type a query, submit the
 * client-side filter, then a small scroll — deliberately tiny so the
 * recording (and the paid LLM tokens it costs) stays small.
 */
async function shopDemo(page: Page): Promise<void> {
  await page.locator('#search-input').pressSequentially('iphone', { delay: 30 });
  await page.locator('#search-button').click();
  await page.locator('#result-count', { hasText: 'Showing 2 of 8' }).waitFor({ timeout: 15_000 });
  await page.mouse.wheel(0, 320);
  await page.waitForTimeout(500);
}

/**
 * Small Apple-like demonstration. The final click advances a one-model-at-a-
 * time visible configurator while a 16-row hidden fallback remains in the DOM.
 */
async function configuratorDemo(page: Page): Promise<void> {
  await page.getByLabel('11-inch model', { exact: true }).check();
  await page.getByLabel('Sky Blue', { exact: true }).check();
  await page.getByLabel('128GB', { exact: true }).check();
  await page.getByLabel('Wi-Fi', { exact: true }).check();
  await page.locator('#model-viewport:not([hidden]) .model-card').waitFor({ timeout: 15_000 });
  await page.getByRole('button', { name: 'Next model' }).click();
  await page.locator('#model-position', { hasText: 'Model 2 of 8' }).waitFor({ timeout: 15_000 });
  await page.waitForTimeout(300);
}

/** Real-site demonstration: load, scroll, then choose model, color, and storage. */
async function appleDemo(page: Page): Promise<void> {
  // Apple currently renders the interactive configurator under
  // .rf-bfe-selectionarea while retaining .product-selection-area for a
  // hidden noscript fallback. Keep the historical selector as a fallback,
  // but never let it win over a visible configurator.
  const area = page.locator('.rf-bfe-selectionarea:visible, .product-selection-area:visible').first();
  await area.waitFor({ state: 'visible', timeout: 60_000 });
  await area.scrollIntoViewIfNeeded();
  await page.mouse.wheel(0, 480);
  await page.waitForTimeout(800);
  await page.getByText('11-inch model', { exact: true }).first().click({ timeout: 30_000 });
  await area.locator('label.rf-bfe-product-dimension-colornav-label')
    .filter({ hasText: 'Space Gray' }).first().click({ timeout: 30_000 });
  const capacity = area.locator('[data-autom="dimensionCapacity256gb"]');
  await capacity.waitFor({ state: 'attached', timeout: 30_000 });
  await page.waitForFunction(() => {
    const input = document.querySelector<HTMLInputElement>('[data-autom="dimensionCapacity256gb"]');
    return input != null && !input.disabled;
  }, undefined, { timeout: 30_000 });
  await capacity.locator('xpath=following-sibling::label[1]').click({ timeout: 30_000 });
  await page.waitForTimeout(1_500);
}

/**
 * The fixture rule may legitimately declare a required search-like input
 * (PR #48 class of harness defect: the harness, not the product, must supply
 * it). buildTaskInputs synthesizes a placeholder string; rebind anything
 * search-shaped to the query the fixture actually contains.
 */
function bindShopSearchInputs(inputs: Record<string, unknown>): Record<string, unknown> {
  const bound: Record<string, unknown> = { ...inputs };
  for (const [key, value] of Object.entries(bound)) {
    if (typeof value === 'string' && /search|keyword|query|term|filter/i.test(key)) {
      bound[key] = 'iphone';
    }
  }
  return bound;
}

function createBaiduScenario(costBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  let replayOracle: BaiduResultRow[] | undefined;
  let taskOracle: BaiduResultRow[] | undefined;
  return {
    name: 'baidu-search',
    entryUrl: BAIDU_ENTRY_URL,
    browserProfileId: BAIDU_QUALIFICATION_PROFILE,
    demo: async () => undefined,
    requirement: { mode: 'structured', spec: BAIDU_SEARCH_REQUIREMENT },
    rowBand: [3, 20],
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    adjustTaskInputs: (inputs) => ({ ...inputs, keyword: BAIDU_TEST_KEYWORD }),
    assertRequirement: assertBaiduRequirement,
    assertProvisionalRule: assertBaiduRule,
    assertRule: assertBaiduRule,
    workerHeadless: false,
    captureReplayOracleDuringExecution: async (context, signal) => {
      replayOracle = await collectBaiduReplayExecutionOracle(
        context,
        BAIDU_TEST_KEYWORD,
        { signal },
      );
    },
    captureTaskOracle: async (page) => {
      taskOracle = await collectBaiduPageOracle(page);
    },
    assertReplayRows: (rows, schema) => {
      assert(replayOracle, 'Baidu replay oracle was not captured');
      assertBaiduRowsEqual(rows, schema, replayOracle, 'replay');
    },
    assertTaskRows: (rows, schema) => {
      assert(taskOracle, 'Baidu task oracle was not captured');
      assertBaiduRowsEqual(rows, schema, taskOracle, 'task');
    },
  };
}

function createBingScenario(reviewedBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const costBudget = reviewedBudget
    ? capRealSiteCostBudget(reviewedBudget, BING_COST_BUDGET_USD)
    : undefined;
  let selected: BingSelectedResult | undefined;
  return {
    name: 'bing-search-full',
    entryUrl: BING_ENTRY_URL,
    demo: (page, ensureRecordingReady) => bingSearchDemo(
      page,
      ensureRecordingReady,
      (choice) => { selected = choice; },
    ),
    requirement: { mode: 'structured', spec: BING_SEARCH_REQUIREMENT },
    rowBand: [1, 1],
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    expectedProviderCalls: 1,
    adjustTaskInputs: (inputs) => {
      assert(selected, 'Bing recording did not capture the selected result identity');
      return {
        ...inputs,
        keyword: BING_QUERY,
        target_title: selected.title,
        target_host: selected.host,
      };
    },
    assertRequirement: assertBingRequirement,
    assertRecording: assertBingRecording,
    assertProvisionalRule: assertBingRule,
    assertRule: assertBingRule,
    workerHeadless: false,
    assertReplayRows: (rows, schema) => {
      assert(selected, 'Bing recorded selection was not captured');
      assertBingRows(rows, schema, selected, 'replay');
    },
    assertTaskRows: (rows, schema) => {
      assert(selected, 'Bing recorded selection was not captured');
      assertBingRows(rows, schema, selected, 'task');
    },
  };
}

function createBingFooterScenario(reviewedBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const costBudget = reviewedBudget ? capRealSiteCostBudget(reviewedBudget, BING_COST_BUDGET_USD) : undefined;
  let selected: BingFooterSelection | undefined;
  return { name: 'bing-footer-link-roundtrip', entryUrl: BING_ENTRY_URL,
    demo: (page, ready) => bingFooterDemo(page, ready, (value) => { selected = value; }),
    requirement: { mode: 'structured', spec: BING_FOOTER_REQUIREMENT }, rowBand: [1, 1],
    inputTokenBudget: costBudget?.inputTokenLimit, realSiteCostBudget: costBudget, expectedProviderCalls: 1,
    adjustTaskInputs: (inputs) => ({ ...inputs, target_text: selected?.text, target_host: selected?.host }),
    assertRequirement: assertBingFooterRequirement, assertRecording: assertBingFooterRecording,
    assertProvisionalRule: assertBingFooterRule, assertRule: assertBingFooterRule, workerHeadless: false,
    assertReplayRows: (rows, schema) => { assert(selected, 'Bing footer selection missing'); assertBingFooterRows(rows, schema, selected, 'replay'); },
    assertTaskRows: (rows, schema) => { assert(selected, 'Bing footer selection missing'); assertBingFooterRows(rows, schema, selected, 'task'); },
  };
}
function createBingReadonlyScenario(reviewedBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const cost = reviewedBudget ? capRealSiteCostBudget(reviewedBudget, BING_COST_BUDGET_USD) : undefined; let selected: BingSelectedResult | undefined;
  return { name: 'bing-search-readonly', entryUrl: BING_ENTRY_URL, demo: (page, ready) => bingSearchReadonlyDemo(page, ready, (v) => { selected = v; }), requirement: { mode: 'structured', spec: BING_READONLY_REQUIREMENT }, rowBand: [1, 1], inputTokenBudget: cost?.inputTokenLimit, realSiteCostBudget: cost, expectedProviderCalls: 1, adjustTaskInputs: (inputs) => ({ ...inputs, keyword: BING_READONLY_QUERY, target_title: selected?.title }), assertRequirement: assertBingReadonlyRequirement, assertRecording: assertBingReadonlyRecording, assertProvisionalRule: assertBingReadonlyRule, assertRule: assertBingReadonlyRule, workerHeadless: false, assertReplayRows: (rows, schema) => { assert(selected, 'Bing selection missing'); assertBingReadonlyRows(rows, schema, selected, 'replay'); }, assertTaskRows: (rows, schema) => { assert(selected, 'Bing selection missing'); assertBingReadonlyRows(rows, schema, selected, 'task'); } };
}

function createBooksCategoryMatrixScenario(
  reviewedCostBudget?: RealSiteCostBudget,
): LiveWorkflowScenario {
  const costBudget = reviewedCostBudget
    ? capRealSiteCostBudget(reviewedCostBudget, BOOKS_COST_BUDGET_USD)
    : undefined;
  let replayOracle: BooksCategoryOracle | undefined;
  const taskOracles = new Map<string, readonly BookRow[]>();
  const successCase = (
    name: string,
    category: string,
    exactPages: number,
    exactRows: number,
  ): NonNullable<LiveWorkflowScenario['taskCases']>[number] => ({
    name,
    expectedStatus: 'done',
    rowBand: [exactRows, exactRows],
    bindInputs: (inputs) => bindBooksCategoryInput(inputs, category, `task case ${name}`),
    captureOracle: async (page) => {
      const oracle = await collectBooksCategoryOracle(page.context(), category, {
        exactPages,
        exactRows,
      });
      await assertBooksExecutionPage(page, oracle);
      taskOracles.set(name, oracle.rows);
      console.log(`[live-wf:books-category-matrix] ${name} oracle `
        + `${oracle.pages.length} page(s) / ${oracle.rows.length} row(s), sha256 ${booksRowsHash(oracle.rows)}`);
    },
    assertRows: (rows, schema) => {
      const expected = taskOracles.get(name);
      assert(expected, `Books task oracle was not captured for ${name}`);
      assertBooksRowsEqual(rows, schema, expected, `task case ${name}`);
    },
  });
  return {
    name: 'books-category-matrix',
    entryUrl: BOOKS_ENTRY_URL,
    demo: booksCategoryDemo,
    requirement: { mode: 'structured', spec: BOOKS_CATEGORY_REQUIREMENT },
    rowBand: [1, 100],
    inputTokenBudget: costBudget
      ? Math.min(costBudget.inputTokenLimit, BOOKS_MAX_ESTIMATED_INPUT_TOKENS)
      : BOOKS_MAX_ESTIMATED_INPUT_TOKENS,
    realSiteCostBudget: costBudget,
    expectedProviderCalls: 1,
    adjustReplayInputs: (inputs) => bindBooksCategoryInput(
      inputs,
      BOOKS_REPLAY_CATEGORY,
      'replay',
    ),
    taskCases: [
      successCase('travel-one-page', BOOKS_TRAVEL_CATEGORY, 1, 11),
      successCase('sequential-art-four-pages', BOOKS_SEQUENTIAL_ART_CATEGORY, 4, 75),
      {
        name: 'missing-category',
        expectedStatus: 'failed',
        bindInputs: (inputs) => bindBooksCategoryInput(
          inputs,
          BOOKS_MISSING_CATEGORY,
          'task case missing-category',
        ),
      },
    ],
    assertRecording: (recording) => {
      assertBooksRecording(recording);
      const forecast = forecastBooksDirectRequest(recording);
      assert(forecast.withinDirectEnvelope,
        `Books recording requires ${forecast.requiredDirectBytes} direct-request bytes, `
        + `over the ${forecast.availableDirectBytes}-byte one-call envelope`);
    },
    assertRequirement: assertBooksRequirement,
    assertRule: assertBooksRule,
    captureReplayOracle: async (context) => {
      const pages = context.pages().filter((candidate) => {
        try {
          const url = new URL(candidate.url());
          return url.hostname === 'books.toscrape.com'
            && url.pathname.startsWith('/catalogue/category/books/');
        } catch {
          return false;
        }
      });
      assert(pages.length === 1,
        `Books replay oracle expected one retained category page, found ${pages.length}`);
      replayOracle = await collectBooksCategoryOracle(context, BOOKS_REPLAY_CATEGORY, {
        exactPages: 2,
        exactRows: 32,
      });
      await assertBooksExecutionPage(pages[0], replayOracle);
      console.log(`[live-wf:books-category-matrix] replay oracle `
        + `${replayOracle.pages.length} page(s) / ${replayOracle.rows.length} row(s), `
        + `sha256 ${booksRowsHash(replayOracle.rows)}`);
    },
    assertReplayRows: (rows, schema) => {
      assert(replayOracle, 'Books replay oracle was not captured');
      assertBooksRowsEqual(rows, schema, replayOracle.rows, 'replay');
    },
  };
}

function createHockeyScenario(reviewedCostBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const costBudget = reviewedCostBudget
    ? capRealSiteCostBudget(reviewedCostBudget, HOCKEY_COST_BUDGET_USD)
    : undefined;
  let replayOracle: HockeyOracle | undefined;
  const taskOracles = new Map<string, { query: string; rows: readonly HockeyRow[] }>();
  const taskCase = (
    name: string,
    query: string,
  ): NonNullable<LiveWorkflowScenario['taskCases']>[number] => ({
    name,
    expectedStatus: 'done',
    rowBand: [1, 100],
    bindInputs: (inputs) => bindHockeyInput(inputs, query, `task case ${name}`),
    captureOracle: async (page) => {
      const oracle = await collectHockeyOracle(page.context(), query);
      await assertHockeyExecutionPage(page, oracle);
      taskOracles.set(name, { query, rows: oracle.rows });
      console.log(`[live-wf:scrape-site-hockey] ${name} oracle `
        + `${oracle.pages.length} page(s) / ${oracle.rows.length} row(s), sha256 ${hockeyRowsHash(oracle.rows, query)}`);
    },
    assertRows: (rows, schema) => {
      const oracle = taskOracles.get(name);
      assert(oracle, `Hockey task oracle was not captured for ${name}`);
      assertHockeyRowsEqual(rows, schema, oracle.rows, oracle.query, `task case ${name}`);
    },
  });
  return {
    name: 'scrape-site-hockey',
    entryUrl: HOCKEY_ENTRY_URL,
    navigationWaitUntil: 'domcontentloaded',
    demo: hockeyDemo,
    requirement: { mode: 'structured', spec: HOCKEY_REQUIREMENT },
    rowBand: [1, 100],
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    expectedProviderCalls: 1,
    adjustReplayInputs: (inputs) => bindHockeyInput(inputs, HOCKEY_REPLAY_QUERY, 'replay'),
    taskCases: [
      taskCase('boston-single-page', HOCKEY_BOSTON_QUERY),
      taskCase('detroit-single-page', HOCKEY_DETROIT_QUERY),
    ],
    assertRecording: assertHockeyRecording,
    assertRequirement: assertHockeyRequirement,
    assertProvisionalRule: assertHockeyRule,
    assertRule: assertHockeyRule,
    captureReplayOracle: async (context) => {
      replayOracle = await collectHockeyOracle(context, HOCKEY_REPLAY_QUERY);
      const retained = context.pages().find((page) => {
        try { return new URL(page.url()).hostname === 'www.scrapethissite.com'; } catch { return false; }
      });
      assert(retained, 'Hockey replay retained page was not available');
      await assertHockeyExecutionPage(retained, replayOracle);
      console.log(`[live-wf:scrape-site-hockey] replay oracle `
        + `${replayOracle.pages.length} page(s) / ${replayOracle.rows.length} row(s), `
        + `sha256 ${hockeyRowsHash(replayOracle.rows, HOCKEY_REPLAY_QUERY)}`);
    },
    assertReplayRows: (rows, schema) => {
      assert(replayOracle, 'Hockey replay oracle was not captured');
      assertHockeyRowsEqual(rows, schema, replayOracle.rows, HOCKEY_REPLAY_QUERY, 'replay');
    },
  };
}

function createRFCEditorScenario(reviewedCostBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const costBudget = reviewedCostBudget
    ? capRealSiteCostBudget(reviewedCostBudget, RFC_EDITOR_COST_BUDGET_USD)
    : undefined;
  let replayOracle: RFCEditorRow[] | undefined;
  let taskOracle: RFCEditorRow[] | undefined;
  return {
    name: 'rfc-editor-section-readonly',
    entryUrl: RFC_EDITOR_ENTRY_URL,
    navigationWaitUntil: 'domcontentloaded',
    demo: rfcEditorDemo,
    requirement: { mode: 'structured', spec: RFC_EDITOR_REQUIREMENT },
    rowBand: [1, 1],
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    expectedProviderCalls: 1,
    adjustReplayInputs: (inputs) => bindRFCEditorSection(inputs, RFC_EDITOR_REPLAY_SECTION),
    taskCases: [{
      name: 'unseen-section-4', expectedStatus: 'done', rowBand: [1, 1],
      bindInputs: (inputs) => bindRFCEditorSection(inputs, RFC_EDITOR_TASK_SECTION),
      captureOracle: async (page) => { taskOracle = await collectRFCEditorOracle(page, RFC_EDITOR_TASK_SECTION); },
      assertRows: (rows, schema) => {
        assert(taskOracle, 'RFC Editor task oracle was not captured');
        assertRFCEditorRowsEqual(rows, schema, taskOracle, RFC_EDITOR_TASK_SECTION, 'task');
      },
    }],
    assertRecording: assertRFCEditorRecording,
    assertRequirement: assertRFCEditorRequirement,
    assertProvisionalRule: assertRFCEditorRule,
    assertRule: assertRFCEditorRule,
    captureReplayOracle: async (context) => {
      const page = context.pages().find((candidate) => {
        try { return new URL(candidate.url()).hostname === 'www.rfc-editor.org'; } catch { return false; }
      });
      assert(page, 'RFC Editor replay page was not retained');
      replayOracle = await collectRFCEditorOracle(page, RFC_EDITOR_REPLAY_SECTION);
    },
    assertReplayRows: (rows, schema) => {
      assert(replayOracle, 'RFC Editor replay oracle was not captured');
      assertRFCEditorRowsEqual(rows, schema, replayOracle, RFC_EDITOR_REPLAY_SECTION, 'replay');
    },
  };
}

function createDuckDuckGoScenario(reviewedCostBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const costBudget = reviewedCostBudget
    ? capRealSiteCostBudget(reviewedCostBudget, DUCKDUCKGO_COST_BUDGET_USD)
    : undefined;
  let selected: DuckDuckGoSelectedResult | undefined;
  return {
    name: 'duckduckgo-search-readonly',
    entryUrl: DUCKDUCKGO_ENTRY_URL,
    navigationWaitUntil: 'domcontentloaded',
    demo: (page, ready) => duckDuckGoSearchReadonlyDemo(
      page, ready, (value) => { selected = value; },
    ),
    requirement: { mode: 'structured', spec: DUCKDUCKGO_REQUIREMENT },
    rowBand: [1, 1],
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    expectedProviderCalls: 1,
    adjustReplayInputs: (inputs) => {
      assert(selected, 'DuckDuckGo recording did not capture a visible result identity');
      return { ...inputs, keyword: DUCKDUCKGO_QUERY, target_title: selected.title, target_host: selected.host };
    },
    adjustTaskInputs: (inputs) => {
      assert(selected, 'DuckDuckGo recording did not capture a visible result identity');
      return { ...inputs, keyword: DUCKDUCKGO_QUERY, target_title: selected.title, target_host: selected.host };
    },
    assertRecording: assertDuckDuckGoRecording,
    assertRequirement: assertDuckDuckGoRequirement,
    assertProvisionalRule: assertDuckDuckGoRule,
    assertRule: assertDuckDuckGoRule,
    workerHeadless: false,
    captureReplayOracle: async (context) => {
      assert(selected, 'DuckDuckGo recording did not capture a visible result identity');
      const page = context.pages().find((candidate) => {
        try { return new URL(candidate.url()).hostname.endsWith('duckduckgo.com'); } catch { return false; }
      });
      assert(page, 'DuckDuckGo replay page was not available for the independent oracle');
      await assertDuckDuckGoSelectionVisible(page, selected, 'replay');
    },
    captureTaskOracle: async (page) => {
      assert(selected, 'DuckDuckGo recording did not capture a visible result identity');
      await assertDuckDuckGoSelectionVisible(page, selected, 'task');
    },
    assertReplayRows: (rows, schema) => {
      assert(selected, 'DuckDuckGo recording did not capture a visible result identity');
      assertDuckDuckGoRows(rows, schema, selected, 'replay');
    },
    assertTaskRows: (rows, schema) => {
      assert(selected, 'DuckDuckGo recording did not capture a visible result identity');
      assertDuckDuckGoRows(rows, schema, selected, 'task');
    },
  };
}

function createSQLiteDocsScenario(reviewedCostBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  const cost = reviewedCostBudget ? capRealSiteCostBudget(reviewedCostBudget, SQLITE_COST_BUDGET_USD) : undefined;
  let context: SQLiteDocumentContext | undefined;
  return {
    name: 'sqlite-docs-roundtrip', entryUrl: SQLITE_DOCS_ENTRY_URL, navigationWaitUntil: 'domcontentloaded',
    demo: (page, ready) => sqliteDocsDemo(page, ready, (value) => { context = value; }),
    requirement: { mode: 'structured', spec: SQLITE_DOCS_REQUIREMENT }, rowBand: [1, 1],
    inputTokenBudget: cost?.inputTokenLimit, realSiteCostBudget: cost, expectedProviderCalls: 1,
    adjustReplayInputs: (inputs) => bindSQLiteInputs(inputs),
    adjustTaskInputs: (inputs) => bindSQLiteInputs(inputs),
    assertRecording: assertSQLiteRecording, assertRequirement: assertSQLiteRequirement,
    assertProvisionalRule: assertSQLiteRule, assertRule: assertSQLiteRule,
    assertReplayRows: (rows, schema) => { assert(context?.title && context.introduction, 'SQLite destination context was not captured'); assertSQLiteRows(rows, schema, 'replay'); },
    assertTaskRows: (rows, schema) => { assert(context?.title && context.introduction, 'SQLite destination context was not captured'); assertSQLiteRows(rows, schema, 'task'); },
  };
}

function createQuotesScenario(
  name: ScenarioName,
  requirement: StructuredRequirementSpec,
  contract: QuotesContract,
  expectedTaskPages: number,
  costBudget?: RealSiteCostBudget,
  rowBand: [number, number] = [1, 50],
  options: {
    entryUrl?: string;
    requirementMode?: LiveWorkflowScenario['requirement'];
    assertRecording?: LiveWorkflowScenario['assertRecording'];
    completeHumanRequirement?: LiveWorkflowScenario['completeHumanRequirement'];
  } = {},
): LiveWorkflowScenario {
  let replayOracle: QuoteRow[] | undefined;
  let taskOracle: QuoteRow[] | undefined;
  return {
    name,
    entryUrl: options.entryUrl ?? QUOTES_BY_TAG_ENTRY_URL,
    demo: async () => undefined,
    requirement: options.requirementMode ?? { mode: 'structured', spec: requirement },
    rowBand,
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    adjustReplayInputs: (inputs) => bindQuotesContractInput(
      inputs, contract.inputName, contract.replayTag, 'replay',
    ),
    adjustTaskInputs: (inputs) => bindQuotesContractInput(
      inputs, contract.inputName, contract.taskTag, 'task',
    ),
    assertRecording: options.assertRecording ?? assertQuotesRecording,
    completeHumanRequirement: options.completeHumanRequirement,
    assertRequirement: (value) => assertQuotesContractRequirement(value, contract),
    assertRule: (value) => assertQuotesContractRule(value, contract),
    captureReplayOracle: async (context) => {
      const pages = context.pages().filter((candidate) =>
        !quotesTagPageBlockForURL(candidate.url(), contract.replayTag));
      assert(pages.length === 1,
        `quotes replay oracle expected one retained ${contract.replayTag} page, found ${pages.length}`);
      const page = pages[0];
      const oracle = await collectQuotesPaginationOracle(context, contract.replayTag);
      await assertQuotesExecutionPage(page, contract.replayTag, oracle.pages.length);
      replayOracle = oracle.rows;
    },
    captureTaskOracle: async (page) => {
      const oracle = await collectQuotesPaginationOracle(page.context(), contract.taskTag, {
        minPages: 1,
        minRows: 1,
      });
      assert(oracle.pages.length === expectedTaskPages,
        `quotes ${contract.taskTag} topology changed: expected ${expectedTaskPages} page(s)`);
      await assertQuotesExecutionPage(page, contract.taskTag, oracle.pages.length);
      taskOracle = oracle.rows;
    },
    assertReplayRows: (rows, schema) => {
      assert(replayOracle, 'quotes replay pagination oracle was not captured');
      assertQuotesContractRowsEqual(rows, schema, replayOracle, contract, 'replay');
    },
    assertTaskRows: (rows, schema) => {
      assert(taskOracle, 'quotes task pagination oracle was not captured');
      assertQuotesContractRowsEqual(rows, schema, taskOracle, contract, 'task');
    },
  };
}

function createQuotesByTagScenario(costBudget?: RealSiteCostBudget): LiveWorkflowScenario {
  return createQuotesScenario(
    'quotes-by-tag',
    QUOTES_BY_TAG_REQUIREMENT,
    {
      inputName: 'tag',
      replayTag: QUOTES_REPLAY_TAG,
      taskTag: QUOTES_TASK_TAG,
      outputMap: { quote: 'quote', author: 'author', author_url: 'author_url' },
    },
    2,
    costBudget,
    [11, 50],
  );
}

function createQuotesHumanChooseScenario(
  costBudget?: RealSiteCostBudget,
): LiveWorkflowScenario {
  let contract: QuotesContract | undefined;
  let replayOracle: QuoteRow[] | undefined;
  let taskOracle: QuoteRow[] | undefined;
  return {
    name: 'quotes-human-choose-requirement',
    entryUrl: QUOTES_HUMAN_CHOOSE_ENTRY_URL,
    demo: async () => undefined,
    requirement: { mode: 'candidates', pick: 'human' },
    rowBand: [1, 20],
    inputTokenBudget: costBudget?.inputTokenLimit,
    realSiteCostBudget: costBudget,
    assertRecording: assertQuotesHumanChooseRecording,
    completeHumanRequirement: humanRequirement('quotes-human-choose-requirement'),
    acceptHumanCandidate: isHumanChosenQuotesRequirement,
    assertRequirement: (value) => {
      contract = deriveHumanChosenQuotesContract(value);
    },
    assertRule: (value) => {
      assert(contract, 'human-chosen quotes contract was not retained');
      assertHumanChosenQuotesRule(value, contract);
    },
    captureReplayOracle: async (context) => {
      const pages = context.pages().filter((candidate) =>
        !quotesTagPageBlockForURL(candidate.url(), QUOTES_HUMAN_CHOOSE_TAG));
      assert(pages.length === 1,
        `human-chosen replay expected one retained ${QUOTES_HUMAN_CHOOSE_TAG} page, found ${pages.length}`);
      const oracle = await collectQuotesPaginationOracle(context, QUOTES_HUMAN_CHOOSE_TAG, {
        minPages: 1,
        minRows: 1,
      });
      assert(oracle.pages.length === 1,
        `human-chosen ${QUOTES_HUMAN_CHOOSE_TAG} topology changed from one page`);
      await assertQuotesExecutionPage(pages[0], QUOTES_HUMAN_CHOOSE_TAG, 1);
      replayOracle = oracle.rows;
    },
    captureTaskOracle: async (page) => {
      const oracle = await collectQuotesPaginationOracle(page.context(), QUOTES_HUMAN_CHOOSE_TAG, {
        minPages: 1,
        minRows: 1,
      });
      assert(oracle.pages.length === 1,
        `human-chosen task ${QUOTES_HUMAN_CHOOSE_TAG} topology changed from one page`);
      await assertQuotesExecutionPage(page, QUOTES_HUMAN_CHOOSE_TAG, 1);
      taskOracle = oracle.rows;
    },
    assertReplayRows: (rows, schema) => {
      assert(contract && replayOracle, 'human-chosen replay contract or oracle was not captured');
      assertQuotesContractRowsEqual(rows, schema, replayOracle, contract, 'replay');
    },
    assertTaskRows: (rows, schema) => {
      assert(contract && taskOracle, 'human-chosen task contract or oracle was not captured');
      assertQuotesContractRowsEqual(rows, schema, taskOracle, contract, 'task');
    },
  };
}

async function warmUpDedicatedBaiduProfile(
  env: Awaited<ReturnType<typeof bootLiveEnvironment>>,
  profile: DedicatedProfileConfig,
): Promise<void> {
  const page = await env.context.newPage();
  const prompt = readline.createInterface({ input: stdin, output: stdout });
  try {
    await page.goto(BAIDU_ENTRY_URL, { waitUntil: 'load', timeout: 90_000 });
    await page.bringToFront();
    console.log('\n[live-wf:baidu-search] DEDICATED PROFILE WARM-UP — RECORDING IS NOT ACTIVE');
    console.log(`  - Search exactly for: ${BAIDU_TEST_KEYWORD}`);
    console.log('  - Complete any ordinary human verification yourself; the harness will not bypass it.');
    console.log('  - Wait until at least three ordinary results are visible.');
    await prompt.question('[live-wf] Press ENTER only when the visible result list is ready: ');
    const oracle = await collectBaiduPageOracle(page);
    console.log(`[live-wf:baidu-search] dedicated profile ${profile.name} is ready (${oracle.length} visible result rows)`);
  } finally {
    prompt.close();
    await page.close().catch(() => undefined);
  }
}

function scenarioRegistry(
  shopUrl: string,
  configuratorUrl: string,
  realSiteCostBudget?: RealSiteCostBudget,
): Record<ScenarioName, LiveWorkflowScenario> {
  return {
    'fixture-candidates': {
      name: 'fixture-candidates',
      entryUrl: shopUrl,
      demo: shopDemo,
      requirement: { mode: 'candidates', pick: 'first' },
      rowBand: [1, 64],
      inputTokenBudget: FIXTURE_INPUT_TOKEN_BUDGET,
      adjustTaskInputs: bindShopSearchInputs,
    },
    'fixture-intent': {
      name: 'fixture-intent',
      entryUrl: shopUrl,
      demo: shopDemo,
      requirement: { mode: 'intent', customText: 'Collect the name and price of every visible product card' },
      rowBand: [1, 64],
      inputTokenBudget: FIXTURE_INPUT_TOKEN_BUDGET,
      adjustTaskInputs: bindShopSearchInputs,
    },
    'fixture-models': {
      name: 'fixture-models',
      entryUrl: configuratorUrl,
      demo: configuratorDemo,
      requirement: { mode: 'intent', customText: CONFIGURATOR_MODEL_REQUIREMENT },
      rowBand: [8, 8],
      inputTokenBudget: MODEL_FIXTURE_INPUT_TOKEN_BUDGET,
      assertRequirement: assertConfiguratorModelRequirement,
      assertRows: assertConfiguratorModelRows,
      assertRule: assertConfiguratorVisibleExtractionRule,
    },
    apple: {
      name: 'apple',
      entryUrl: 'https://www.apple.com/shop/buy-ipad/ipad-air',
      demo: appleDemo,
      requirement: { mode: 'intent', customText: APPLE_MODEL_REQUIREMENT },
      rowBand: [16, 16],
      inputTokenBudget: realSiteCostBudget?.inputTokenLimit,
      realSiteCostBudget,
      assertRequirement: assertAppleModelRequirement,
      assertRows: assertAppleModelRows,
      assertRule: assertAppleVisibleExtractionRule,
    },
    'mdn-css-grid': (() => {
      let replayOracle: MDNGuideRow | undefined;
      let taskOracle: MDNGuideRow | undefined;
      const costBudget = realSiteCostBudget
        ? capRealSiteCostBudget(realSiteCostBudget, MDN_COST_BUDGET_USD)
        : undefined;
      return {
        name: 'mdn-css-grid',
        entryUrl: MDN_ENTRY_URL,
        navigationWaitUntil: 'domcontentloaded',
        prepareContext: prepareMDNContext,
        preparePage: prepareMDNPage,
        demo: mdnGuideDemo,
        requirement: { mode: 'structured', spec: MDN_GUIDE_REQUIREMENT },
        rowBand: [1, 1],
        inputTokenBudget: costBudget?.inputTokenLimit,
        realSiteCostBudget: costBudget,
        expectedProviderCalls: 1,
        assertRecording: assertMDNRecording,
        assertProvisionalRule: assertMDNRule,
        assertRule: assertMDNRule,
        captureReplayOracle: async (context) => {
          const page = context.pages().find((candidate) =>
            candidate.url().startsWith(MDN_ENTRY_URL));
          assert(page, 'MDN replay page was not available for the independent oracle');
          replayOracle = await collectMDNGuideOracle(page);
        },
        captureTaskOracle: async (page) => {
          taskOracle = await collectMDNGuideOracle(page);
        },
        assertReplayRows: (rows, schema) => {
          assert(replayOracle, 'MDN replay oracle was not captured');
          assertMDNRows(rows, schema, replayOracle, 'replay');
        },
        assertTaskRows: (rows, schema) => {
          assert(taskOracle, 'MDN task oracle was not captured');
          assertMDNRows(rows, schema, taskOracle, 'task');
        },
      };
    })(),
    'iana-link-roundtrip': (() => {
      let replayOracle: IANAArticleRow | undefined;
      let taskOracle: IANAArticleRow | undefined;
      const costBudget = realSiteCostBudget
        ? capRealSiteCostBudget(realSiteCostBudget, IANA_COST_BUDGET_USD)
        : undefined;
      return {
        name: 'iana-link-roundtrip',
        entryUrl: IANA_ENTRY_URL,
        navigationWaitUntil: 'domcontentloaded',
        prepareContext: prepareIANAContext,
        preparePage: prepareIANAPage,
        demo: ianaLinkRoundtripDemo,
        requirement: { mode: 'structured', spec: IANA_LINK_ROUNDTRIP_REQUIREMENT },
        rowBand: [1, 1],
        inputTokenBudget: costBudget?.inputTokenLimit,
        realSiteCostBudget: costBudget,
        expectedProviderCalls: 1,
        assertRecording: assertIANARoundtripRecording,
        assertProvisionalRule: assertIANARoundtripRule,
        assertRule: assertIANARoundtripRule,
        captureReplayOracle: async (context: BrowserContext) => {
          const page = context.pages().find((candidate) =>
            candidate.url().startsWith(IANA_ENTRY_URL));
          assert(page, 'IANA replay page was not available for the independent oracle');
          replayOracle = await collectIANAArticleOracle(page);
        },
        captureTaskOracle: async (page: Page) => {
          taskOracle = await collectIANAArticleOracle(page);
        },
        assertReplayRows: (rows: unknown[], schema: unknown) => {
          assert(replayOracle, 'IANA replay oracle was not captured');
          assertIANARows(rows, schema, replayOracle, 'replay');
        },
        assertTaskRows: (rows: unknown[], schema: unknown) => {
          assert(taskOracle, 'IANA task oracle was not captured');
          assertIANARows(rows, schema, taskOracle, 'task');
        },
      };
    })(),
    'baidu-search': createBaiduScenario(realSiteCostBudget),
    'bing-search-full': createBingScenario(realSiteCostBudget),
    'bing-footer-link-roundtrip': createBingFooterScenario(realSiteCostBudget),
    'bing-search-readonly': createBingReadonlyScenario(realSiteCostBudget),
    'books-category-matrix': createBooksCategoryMatrixScenario(realSiteCostBudget),
    'scrape-site-hockey': createHockeyScenario(realSiteCostBudget),
    'rfc-editor-section-readonly': createRFCEditorScenario(realSiteCostBudget),
    'duckduckgo-search-readonly': createDuckDuckGoScenario(realSiteCostBudget),
    'sqlite-docs-roundtrip': createSQLiteDocsScenario(realSiteCostBudget),
    'quotes-by-tag': createQuotesByTagScenario(realSiteCostBudget),
    'quotes-one-page': createQuotesScenario(
      'quotes-one-page',
      QUOTES_ONE_PAGE_REQUIREMENT,
      QUOTES_ONE_PAGE_CONTRACT,
      QUOTES_ONE_PAGE_TASK_PAGES,
      realSiteCostBudget,
    ),
    'quotes-renamed-contract': createQuotesScenario(
      'quotes-renamed-contract',
      QUOTES_RENAMED_REQUIREMENT,
      QUOTES_RENAMED_CONTRACT,
      QUOTES_RENAMED_TASK_PAGES,
      realSiteCostBudget,
    ),
    'quotes-output-projection': createQuotesScenario(
      'quotes-output-projection',
      QUOTES_PROJECTION_REQUIREMENT,
      QUOTES_PROJECTION_CONTRACT,
      QUOTES_PROJECTION_TASK_PAGES,
      realSiteCostBudget,
    ),
    'quotes-human-choose-requirement': createQuotesHumanChooseScenario(realSiteCostBudget),
    'quotes-human-input-requirement': createQuotesScenario(
      'quotes-human-input-requirement',
      QUOTES_HUMAN_INPUT_REQUIREMENT,
      QUOTES_HUMAN_INPUT_CONTRACT,
      QUOTES_HUMAN_INPUT_TASK_PAGES,
      realSiteCostBudget,
      [1, 30],
      {
        entryUrl: QUOTES_HUMAN_INPUT_ENTRY_URL,
        requirementMode: {
          mode: 'intent',
          customText: QUOTES_HUMAN_INPUT_TEXT,
          humanInput: true,
        },
        assertRecording: assertQuotesHumanInputRecording,
        completeHumanRequirement: humanRequirement('quotes-human-input-requirement'),
      },
    ),
  };
}

async function runRecordOnlyPreflight(
  env: LiveEnvironment,
  scenario: LiveWorkflowScenario,
  saveRecording?: (recording: PageAgentRecording) => void,
): Promise<void> {
  const target = await env.context.newPage();
  try {
    const recording = await captureLiveScenarioRecording(env, scenario, target);
    assert(scenario.realSiteCostBudget, 'real-site preflight requires a model-dependent cost budget');
    if (scenario.name === 'books-category-matrix') {
      assertBooksRecording(recording);
      const oracle = await collectBooksCategoryOracle(
        env.context,
        BOOKS_REPLAY_CATEGORY,
        { exactPages: 2, exactRows: 32 },
      );
      await assertBooksExecutionPage(target, oracle);
      const metadata = await env.api.admin<{ recording?: {
        compressedBytes?: number;
        redactionCount?: number;
        removedFieldCount?: number;
        contentHash?: string;
      } }>('GET', `/api/v1/recordings/${encodeURIComponent(recording.meta.serverRecordingId ?? '')}`);
      const recordingInputFloor = estimateRecordingInputTokens(recording);
      const directRequest = forecastBooksDirectRequest(recording);
      assert(directRequest.withinDirectEnvelope,
        `Books recording requires ${directRequest.requiredDirectBytes} direct-request bytes, `
        + `over the ${directRequest.availableDirectBytes}-byte one-call envelope`);
      const cappedRequestCostUSD = estimateContextWindowCostUpperBound(
        scenario.realSiteCostBudget,
        BOOKS_DEEPSEEK_CONTEXT_TOKENS,
        BOOKS_MAX_OUTPUT_TOKENS,
      );
      assert(cappedRequestCostUSD <= scenario.realSiteCostBudget.budgetUSD,
        'Books capped request cost exceeds the reviewed scenario budget');
      assert(metadata.recording?.redactionCount === 0,
        'Books reusable preflight requires zero server redactions');
      assert(metadata.recording?.removedFieldCount === 0,
        'Books reusable preflight requires zero server-removed fields');
      saveRecording?.(recording);
      console.log(JSON.stringify({
        schema: 'aegiscrawler.books-recording-preflight.v1',
        status: 'passed',
        llmEnabled: false,
        scenario: scenario.name,
        recordingId: recording.meta.serverRecordingId,
        actions: recording.events.length,
        snapshots: recording.snapshots.length,
        compressedBytes: metadata.recording?.compressedBytes ?? 0,
        redactionCount: metadata.recording?.redactionCount ?? 0,
        removedFieldCount: metadata.recording?.removedFieldCount ?? 0,
        contentHash: metadata.recording?.contentHash ?? '',
        recordingInputFloor,
        directRequest,
        replayPages: oracle.pages.length,
        replayRows: oracle.rows.length,
        maxEstimatedInputTokens: BOOKS_MAX_ESTIMATED_INPUT_TOKENS,
        maxOutputTokens: BOOKS_MAX_OUTPUT_TOKENS,
        providerContextTokens: BOOKS_DEEPSEEK_CONTEXT_TOKENS,
        maxOneRequestCostUSD: cappedRequestCostUSD,
      }, null, 2));
      return;
    }
    const forecast = forecastBaiduGeneration(recording, scenario.realSiteCostBudget.inputTokenLimit);
    const incompleteSnapshots = recording.snapshots.filter((snapshot) =>
      snapshot.capture?.status !== 'complete');
    const unavailableFrames = recording.snapshots.flatMap((snapshot) =>
      snapshot.capture?.frames?.filter((frame) => frame.status !== 'captured') ?? []);
    const unsafeEvents = recording.events.filter((event) => event.type === 'executeJavascript');
    const metadata = await env.api.admin<{ recording?: {
      compressedBytes?: number;
      redactionCount?: number;
      removedFieldCount?: number;
      contentHash?: string;
    } }>('GET', `/api/v1/recordings/${encodeURIComponent(recording.meta.serverRecordingId ?? '')}`);
    let oracle: BaiduResultRow[];
    try {
      oracle = await collectBaiduPageOracle(target);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (!message.startsWith('Baidu environment blocked:')) throw error;
      console.log(JSON.stringify({
        schema: 'aegiscrawler.baidu-recording-preflight.v1',
        status: 'blocked',
        llmEnabled: false,
        scenario: scenario.name,
        recordingId: recording.meta.serverRecordingId,
        actions: recording.events.length,
        snapshots: recording.snapshots.length,
        compressedBytes: metadata.recording?.compressedBytes ?? 0,
        contentHash: metadata.recording?.contentHash ?? '',
        environmentBlock: message.slice('Baidu environment blocked:'.length).trim().slice(0, 200),
      }, null, 2));
      throw new Error(`record-only preflight blocked before LLM work: ${message}`);
    }
    const report = {
      schema: 'aegiscrawler.baidu-recording-preflight.v1',
      status: incompleteSnapshots.length === 0
        && unavailableFrames.length === 0
        && unsafeEvents.length === 0
        && recording.events.length <= 20
        && forecast.withinForecast ? 'passed' : 'failed',
      llmEnabled: false,
      scenario: scenario.name,
      recordingId: recording.meta.serverRecordingId,
      actions: recording.events.length,
      snapshots: recording.snapshots.length,
      incompleteSnapshots: incompleteSnapshots.length,
      unavailableFrames: unavailableFrames.length,
      unsafeEvents: unsafeEvents.length,
      compressedBytes: metadata.recording?.compressedBytes ?? 0,
      redactionCount: metadata.recording?.redactionCount ?? 0,
      removedFieldCount: metadata.recording?.removedFieldCount ?? 0,
      contentHash: metadata.recording?.contentHash ?? '',
      oracleRows: oracle.length,
      oracleHash: baiduRowsHash(oracle),
      forecast,
    };
    console.log(JSON.stringify(report, null, 2));
    assert(incompleteSnapshots.length === 0,
      `Baidu recording contains ${incompleteSnapshots.length} incomplete semantic snapshots`);
    assert(unavailableFrames.length === 0,
      `Baidu recording contains ${unavailableFrames.length} unavailable/error frame captures`);
    assert(unsafeEvents.length === 0, 'Baidu recording contains an unsafe executeJavascript event');
    assert(recording.events.length <= 20,
      `Baidu recording captured ${recording.events.length} actions, over the 20-action preflight cap`);
    assert(forecast.withinForecast,
      `Baidu generation forecast ${forecast.estimatedGenerationInputTokens} exceeds ${forecast.forecastLimit}`);
  } finally {
    await target.close().catch(() => undefined);
  }
}

async function main(): Promise<void> {
  const selection = parseScenarioSelection(process.env);
  const headed = process.env.AEGIS_LIVE_WORKFLOW_HEADED === '1';
  const humanDemoEnabled = process.env.AEGIS_LIVE_HUMAN_DEMO === '1';
  const humanRequirementEnabled = process.env.AEGIS_LIVE_HUMAN_REQUIREMENT === '1';
  const recordOnly = process.env.AEGIS_LIVE_RECORD_ONLY === '1';
  const saveRecording = process.env.AEGIS_LIVE_SAVE_RECORDING === '1';
  const reuseRecording = process.env.AEGIS_LIVE_REUSE_RECORDING === '1';
  if (selection.includes('books-category-matrix')) {
    assertBooksQualificationEnvironment(process.env, recordOnly);
  }
  validateReusableRecordingConfiguration({
    save: saveRecording,
    reuse: reuseRecording,
    humanDemo: humanDemoEnabled,
    recordOnly,
    warmup: process.env.AEGIS_LIVE_PROFILE_WARMUP === '1',
    headed,
    selection,
  });
  validateHumanDemoConfiguration(humanDemoEnabled, headed, selection, reuseRecording);
  validateHumanRequirementConfiguration(humanRequirementEnabled, headed, selection);
  validateRecordOnlyConfiguration(
    recordOnly,
    process.env.AEGIS_LIVE_ALLOW_RECORD_ONLY,
    humanDemoEnabled,
    selection,
  );
  const dedicatedProfile = resolveDedicatedProfile({
    env: process.env,
    headed,
    humanDemoEnabled,
    selection,
  });
  const includesRealSite = selection.some((name) =>
    name === 'apple'
    || name === 'mdn-css-grid'
    || name === 'iana-link-roundtrip'
    || name === 'baidu-search'
    || name === 'bing-search-full'
    || name === 'bing-footer-link-roundtrip'
    || name === 'bing-search-readonly'
    || name === 'books-category-matrix'
    || name === 'scrape-site-hockey'
    || name === 'rfc-editor-section-readonly'
    || name === 'duckduckgo-search-readonly'
    || name === 'sqlite-docs-roundtrip'
    || isQuotesLiveScenario(name));
  const realSiteCostBudget = includesRealSite ? resolveRealSiteCostBudget(process.env) : undefined;
  const booksCostBudget = selection.includes('books-category-matrix') && realSiteCostBudget
    ? capRealSiteCostBudget(realSiteCostBudget, BOOKS_COST_BUDGET_USD)
    : undefined;
  const booksAcceptedRequestCostUSD = booksCostBudget
    ? estimateContextWindowCostUpperBound(
      booksCostBudget,
      BOOKS_DEEPSEEK_CONTEXT_TOKENS,
      BOOKS_MAX_OUTPUT_TOKENS,
    )
    : undefined;
  if (booksAcceptedRequestCostUSD !== undefined) {
    assertBooksAcceptedRequestCostAuthorized(booksAcceptedRequestCostUSD);
  }
  const reusableRecordingPaths = (saveRecording || reuseRecording)
    ? selection[0] === 'books-category-matrix'
      ? reusableBooksRecordingPaths()
      : isQuotesLiveScenario(selection[0])
      ? reusableQuotesRecordingPaths(undefined, quotesRecordingScenario(selection[0]))
      : reusableBaiduRecordingPaths()
    : undefined;
  const restoredRecording = reuseRecording && reusableRecordingPaths
    ? isQuotesLiveScenario(selection[0])
      ? loadReusableQuotesRecording(reusableRecordingPaths, quotesRecordingScenario(selection[0]))
      : loadReusableBaiduRecording(reusableRecordingPaths)
    : undefined;
  // Paid configuration is never parsed or loaded during the free preflight.
  const llm = recordOnly ? undefined : loadLocalLLMConfig();
  if (llm) console.log(`[live-wf] provider ${llm.providerLabel}/${llm.model}`);
  else console.log('[live-wf] record-only preflight: LLM disabled and no provider credentials loaded');
  console.log(`[live-wf] scenarios: ${selection.join(', ')} (${headed ? 'headed' : 'headless'} recording browser)`);
  if (selection.includes('apple')) {
    console.warn('[live-wf] WARNING: the apple scenario runs against https://www.apple.com with a paid provider; '
      + 'real-page runs historically consumed ~4-8M input tokens. Proceeding because it was explicitly listed.');
  }
  if (selection.includes('baidu-search')) {
    console.warn('[live-wf] baidu-search is an explicit real-site scenario: one human search, one replay, and one task only; CAPTCHA is never bypassed.');
  }
  if (selection.includes('bing-search-full')) {
    console.warn('[live-wf] bing-search-full records through the extension and runs one paid generation, one replay, and one Worker task; autocomplete is not used.');
  }
  if (selection.includes('bing-footer-link-roundtrip')) console.warn('[live-wf] bing-footer-link-roundtrip performs one read-only Bing footer roundtrip; verification remains fail-closed.');
  if (selection.includes('books-category-matrix')) {
    console.warn('[live-wf] selected the explicit Books category matrix: one scripted recording, '
      + 'one exact replay, and three ordered task cases against one immutable rule.');
    assert(booksCostBudget && booksAcceptedRequestCostUSD !== undefined,
      'Books scenario requires reviewed model pricing');
    console.warn(`[live-wf] Books bounds: ${BOOKS_MAX_ESTIMATED_INPUT_TOKENS} server input-estimate / `
      + `${BOOKS_MAX_OUTPUT_TOKENS} hard output / ${BOOKS_DEEPSEEK_CONTEXT_TOKENS} provider-context tokens; `
      + `accepted-request ceiling `
      + `$${booksAcceptedRequestCostUSD.toFixed(6)}`);
  }
  if (selection.includes('scrape-site-hockey')) {
    console.warn('[live-wf] selected the explicit Scrape This Site hockey workflow: one scripted search/pagination recording, '
      + 'one exact replay, and two unseen task cases against one immutable rule.');
  }
  if (selection.includes('rfc-editor-section-readonly')) {
    console.warn('[live-wf] selected the explicit RFC Editor section workflow: one same-document TOC recording, '
      + 'one replay section, and one unseen Worker section against immutable RFC 2606.');
  }
  if (selection.includes('duckduckgo-search-readonly')) {
    console.warn('[live-wf] duckduckgo-search-readonly is an explicit read-only public-site scenario: '
      + 'one direct search, one replay, and one task only; results are never opened and verification is never bypassed.');
  }
  if (selection.some(isQuotesLiveScenario)) {
    console.warn('[live-wf] selected an explicit quotes.toscrape.com generalization scenario: '
      + 'replay and unseen task inputs must each match an independent complete pagination oracle.');
  }
  const displayedCostBudget = booksCostBudget ?? realSiteCostBudget;
  if (displayedCostBudget) {
    console.warn(`[live-wf] real-site cost guard: $${displayedCostBudget.budgetUSD.toFixed(2)} for `
      + `${displayedCostBudget.model} (${displayedCostBudget.pricingRegion}); conservative input ceiling `
      + `${displayedCostBudget.inputTokenLimit} tokens at $${displayedCostBudget.inputUSDPerMillion}/M`);
  }
  if (restoredRecording) {
    console.warn(`[live-wf] reusing encrypted ${selection[0]} recording (${restoredRecording.events.length} actions, `
      + `${restoredRecording.snapshots.length} snapshots); no human recording will run`);
  }

  const needsShop = selection.some((name) => name.startsWith('fixture-'));
  const shop = needsShop ? await startShopFixtureServer() : undefined;
  if (shop) {
    console.log(`[live-wf] shop fixture served at ${shop.url}`);
    console.log(`[live-wf] model fixture served at ${shop.configuratorUrl}`);
  }
  const registry = scenarioRegistry(shop?.url ?? '', shop?.configuratorUrl ?? '', realSiteCostBudget);
  if (humanDemoEnabled) {
    const scenarioName = selection[0];
    registry[scenarioName] = { ...registry[scenarioName], demo: humanDemo(scenarioName) };
    console.log(`[live-wf] scenario ${scenarioName} will use a real human demonstration`);
  }

  let env: LiveEnvironment | undefined;
  const reports: LiveWorkflowReport[] = [];
  try {
    env = await bootLiveEnvironment({ llm, headed, dedicatedProfile });
    if (dedicatedProfile?.warmup) await warmUpDedicatedBaiduProfile(env, dedicatedProfile);
    if (recordOnly) {
      await runRecordOnlyPreflight(
        env,
        registry[selection[0]],
        saveRecording && reusableRecordingPaths
          ? (recording) => {
            assert(selection[0] === 'books-category-matrix',
              'provider-free recording save is restricted to Books');
            saveReusableBooksRecording(reusableRecordingPaths, recording);
            const expected = structuredClone(recording);
            delete expected.meta.serverRecordingId;
            deepStrictEqual(
              loadReusableBooksRecording(reusableRecordingPaths),
              expected,
              'encrypted Books recording did not round-trip exactly after save',
            );
            console.log(`[live-wf:${selection[0]}] encrypted reusable recording saved at `
              + reusableRecordingPaths.artifact);
          }
          : undefined,
      );
      console.log(`[live-wf] free ${selection[0]} recording preflight passed; no LLM request was made`);
      return;
    }
    assert(llm, 'paid live workflow requires provider configuration');
    for (const name of selection) {
      const scenario = registry[name];
      console.log(`[live-wf] --- scenario ${scenario.name} starting ---`);
      const report = await runScenario(env, scenario, {
        llm,
        headed,
        reusableRecording: restoredRecording,
        saveRecording: saveRecording && reusableRecordingPaths
          ? (recording) => {
            if (isQuotesLiveScenario(name)) {
              saveReusableQuotesRecording(
                reusableRecordingPaths,
                recording,
                quotesRecordingScenario(name),
              );
            } else if (name === 'baidu-search') {
              saveReusableBaiduRecording(reusableRecordingPaths, recording);
            } else {
              throw new Error('paid Books qualification must not save a reusable recording');
            }
            console.log(`[live-wf:${name}] encrypted reusable recording saved at ${reusableRecordingPaths.artifact}`);
          }
          : undefined,
      });
      reports.push(report);
      console.log(`[live-wf] --- scenario ${scenario.name} ${report.status} ---`);
      console.log(JSON.stringify(report, null, 2));
    }
  } finally {
    if (env) await shutdownLiveEnvironment(env, recordOnly);
    if (shop) await shop.close();
  }

  const failed = reports.filter((report) => report.status !== 'passed');
  console.log(`[live-wf] ${reports.length - failed.length}/${reports.length} scenario(s) passed`);
  if (reports.length === 0) {
    console.error('[live-wf] no scenario ran');
    process.exit(1);
  }
  if (failed.length > 0) {
    console.error(`[live-wf] failed scenarios: ${failed.map((report) => report.scenario).join(', ')}`);
    process.exit(1);
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error('[live-wf] suite failed to boot:', error);
    process.exit(1);
  });
}
