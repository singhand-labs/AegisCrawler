import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { convert } from '../src/rule-generator/converter/PageAgentToDslConverter';
import type { PageAgentRecording } from '../src/rule-generator/types';
import { BING_READONLY_QUERY, assertBingReadonlyRule } from './live-workflow/bing-search-readonly';

const ITERATIONS = 250;
const SEARCH_ARIA_LABEL = 'Enter your search here - Search suggestions will show as you type';

function recording(): PageAgentRecording {
  const prefixes = Array.from({ length: BING_READONLY_QUERY.length }, (_, index) =>
    BING_READONLY_QUERY.slice(0, index + 1));
  const events: PageAgentRecording['events'] = [
    { type: 'click', index: 7, timestamp: 1 },
    ...prefixes.map((text, index) => ({
      type: 'inputText' as const, index: 7, text, timestamp: index + 2,
    })),
  ];
  const snapshots: PageAgentRecording['snapshots'] = [
    {
      timestamp: 1, url: 'https://www.bing.com/?cc=us&setlang=en-US', phase: 'before-action',
      selectorMap: { 7: {
        index: 7, tagName: 'input', selector: '#sb_form_q', role: 'combobox',
        ariaLabel: SEARCH_ARIA_LABEL, boundingRect: { x: 10, y: 10, width: 400, height: 40 },
      } },
    },
    ...prefixes.map((text, index) => ({
      timestamp: index + 2,
      url: 'https://www.bing.com/?cc=us&setlang=en-US',
      phase: 'before-action' as const,
      selectorMap: { 7: {
        index: 7, tagName: 'input', selector: '#sb_form_q', role: 'combobox',
        ariaLabel: SEARCH_ARIA_LABEL, text,
        boundingRect: { x: 10, y: 10, width: 400, height: 40 },
      } },
    })),
  ];
  return {
    version: '1.0.0',
    meta: {
      startUrl: 'https://www.bing.com/?cc=us&setlang=en-US',
      title: 'Search - Microsoft Bing', recordedAt: '2026-08-24T00:00:00.000Z',
      domain: 'www.bing.com',
    },
    events,
    snapshots,
  };
}

function selectedRule(): Record<string, unknown> {
  return {
    domain: 'www.bing.com',
    steps: [
      { action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}' },
      { action: 'waitForElementVisible', target: { selector: '#b_results > .b_algo' } },
      { action: 'extract', name: 'items', multiple: true, fields: {} },
      { action: 'filter', from: 'extracted.items', name: 'title_matches', criteria: { field: 'title', op: 'eq', value: '{{target_title}}' } },
      { action: 'loop', type: 'forEach', items: '{{extracted.title_matches}}', as: 'item', steps: [
        { action: 'sendResult', payload: { title: '{{loopItem.title}}', website: '{{loopItem.website}}' } },
      ] },
    ],
  };
}

function capturedInvalidRule(): Record<string, unknown> {
  return {
    domain: 'www.bing.com',
    steps: [
      { action: 'type', target: { $ref: 'el1' }, value: '{{keyword}}' },
      { action: 'waitForElementVisible', target: { selector: '#b_results > .b_algo' } },
      { action: 'extract', name: 'items', multiple: true, fields: {} },
      { action: 'loop', type: 'forEach', items: '{{extracted.items}}', as: 'item', steps: [{
        action: 'if', condition: { type: 'elementNotExists', target: { text: '{{target_title}}' } }, else: [{
          action: 'if', condition: { type: 'elementNotExists', target: { text: '{{target_host}}' } }, else: [{
            action: 'sendResult', payload: { title: '{{loopItem.title}}', website: '{{loopItem.website}}' },
          }],
        }],
      }] },
    ],
  };
}

function runIteration(): string {
  const converted = convert(recording(), { optimizeSelectors: false, ruleIdPrefix: 'bing-offline-soak' });
  assert.deepEqual(converted.selectors, {
    el1: { selector: '#sb_form_q', ariaLabel: SEARCH_ARIA_LABEL, role: 'combobox' },
  });
  assert.equal(converted.steps.filter((step) => step.action === 'type').length, 1);
  assert.doesNotThrow(() => assertBingReadonlyRule(selectedRule()));
  assert.throws(() => assertBingReadonlyRule(capturedInvalidRule()), /filter extracted title/);
  return createHash('sha256').update(JSON.stringify({
    selectors: converted.selectors,
    steps: converted.steps,
    selectedRule: selectedRule(),
  })).digest('hex');
}

const digests = new Set<string>();
for (let iteration = 0; iteration < ITERATIONS; iteration += 1) digests.add(runIteration());
assert.equal(digests.size, 1, 'offline Bing soak produced nondeterministic normalized output');
console.log(`[bing-offline-soak] ${ITERATIONS}/${ITERATIONS} deterministic iterations passed; digest=${[...digests][0]}`);
