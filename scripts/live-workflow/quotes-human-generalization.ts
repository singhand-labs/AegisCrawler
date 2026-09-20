import assert from 'node:assert/strict';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { assertVisibleModelExtractionRule, plainObject } from './model-contract';
import {
  QUOTES_BY_TAG_ORIGIN,
  type QuoteRow,
  type QuotesContract,
  validateQuotesTagPageURL,
} from './quotes-by-tag';

export const QUOTES_HUMAN_CHOOSE_TAG = 'reading';
export const QUOTES_HUMAN_CHOOSE_ENTRY_URL = `${QUOTES_BY_TAG_ORIGIN}/`;

export const QUOTES_HUMAN_INPUT_REPLAY_TAG = 'humor';
export const QUOTES_HUMAN_INPUT_TASK_TAG = 'friendship';
export const QUOTES_HUMAN_INPUT_ENTRY_URL =
  `${QUOTES_BY_TAG_ORIGIN}/tag/${QUOTES_HUMAN_INPUT_REPLAY_TAG}/`;
export const QUOTES_HUMAN_INPUT_TASK_PAGES = 1;

export const QUOTES_HUMAN_INPUT_CONTRACT: QuotesContract = {
  inputName: 'label',
  replayTag: QUOTES_HUMAN_INPUT_REPLAY_TAG,
  taskTag: QUOTES_HUMAN_INPUT_TASK_TAG,
  outputMap: {
    quotation: 'quote',
    writer: 'author',
    writer_url: 'author_url',
  },
};

export const QUOTES_HUMAN_INPUT_TEXT = [
  'Collect every visible quotation from the Quotes to Scrape tag identified by the required string input named label.',
  'The label must match ^[a-z0-9]+(?:-[a-z0-9]+)*$ and have maximum length 64.',
  'Navigate to the canonical /tag/{{label}}/ route.',
  'Use a fixedCount pagination loop with a positive count of at most 10, stopping when no visible Next link remains.',
  'Return exactly quotation, writer, and writer_url as strings, where writer_url is the resolved same-origin author detail URL.',
].join(' ');

export const QUOTES_HUMAN_INPUT_REQUIREMENT = {
  title: 'Collect every quotation for a label',
  description: [
    'Navigate to the public Quotes to Scrape tag page using the required label input.',
    'Use a fixedCount pagination loop with a positive count of at most 10 to collect every visible quotation in page order across all pages.',
    'After each page, stop the loop when no visible Next link remains; otherwise click Next and continue.',
    'Return exactly quotation, writer, and writer_url as strings.',
  ].join(' '),
  requiredInputs: [{
    name: 'label',
    type: 'string',
    description: 'Lowercase quote tag label',
    constraints: { pattern: '^[a-z0-9]+(?:-[a-z0-9]+)*$', maxLength: 64 },
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'quotation', type: 'string', description: 'Visible quotation text' },
    { name: 'writer', type: 'string', description: 'Visible writer name' },
    {
      name: 'writer_url',
      type: 'string',
      description: 'Resolved same-origin writer detail URL',
    },
  ],
  sampleOutput: {
    quotation: 'A visible quotation.',
    writer: 'Example Writer',
    writer_url: `${QUOTES_BY_TAG_ORIGIN}/author/Example-Writer/`,
  },
};

function assertRecordingEnvelope(recording: PageAgentRecording, label: string): void {
  assert.equal(recording.version, '2.0.0', `${label} recording must use recording v2`);
  assert.equal(recording.meta.sanitizationVersion, 'extension-v2',
    `${label} recording must retain extension-v2 sanitization provenance`);
  assert.equal(recording.termination?.complete, true, `${label} recording must terminate completely`);
  assert(recording.events.length >= 1 && recording.events.length <= 12,
    `${label} recording must contain between 1 and 12 human events`);
  assert(!recording.events.some((event) => event.type === 'executeJavascript'),
    `${label} recording must not contain executeJavascript`);
  assert.equal(recording.snapshots.length, recording.events.length + 2,
    `${label} recording must retain initial, pre-action, and final snapshots`);
  recording.snapshots.forEach((snapshot, index) => {
    assert.equal(snapshot.capture?.status, 'complete', `${label} snapshot ${index} must be complete`);
    assert(!(snapshot.capture?.frames ?? []).some((frame) => frame.status !== 'captured'),
      `${label} snapshot ${index} contains an unavailable frame`);
    assert.equal(new URL(snapshot.url).origin, QUOTES_BY_TAG_ORIGIN,
      `${label} snapshot ${index} left the exact quotes origin`);
  });
}

export function assertQuotesHumanChooseRecording(recording: PageAgentRecording): void {
  const label = 'human-choose';
  assertRecordingEnvelope(recording, label);
  assert(recording.events.some((event) => event.type === 'click'),
    'human-choose recording must contain the visible reading-tag click');
  const pages = recording.snapshots.map((snapshot, index) => {
    const url = new URL(snapshot.url);
    if (url.pathname === '/' && !url.search && !url.hash) return 0;
    const parsed = validateQuotesTagPageURL(snapshot.url, QUOTES_HUMAN_CHOOSE_TAG);
    assert.equal(parsed.page, 1, `human-choose snapshot ${index} must remain on reading page 1`);
    return 1;
  });
  assert.equal(pages[0], 0, 'human-choose recording must start on the quotes homepage');
  assert.equal(pages.at(-1), 1, 'human-choose recording must finish on reading page 1');
  assert(pages.includes(0) && pages.includes(1),
    'human-choose recording must observe both the homepage and reading page');
  assert(pages.every((page, index) => index === 0 || page >= pages[index - 1]),
    'human-choose recording states must advance monotonically');
}

export function assertQuotesHumanInputRecording(recording: PageAgentRecording): void {
  const label = 'human-input';
  assertRecordingEnvelope(recording, label);
  assert(recording.events.some((event) => event.type === 'click'),
    'human-input recording must contain the visible Next click');
  const pages = recording.snapshots.map((snapshot) =>
    validateQuotesTagPageURL(snapshot.url, QUOTES_HUMAN_INPUT_REPLAY_TAG).page);
  assert.equal(pages[0], 1, 'human-input recording must start directly on humor page 1');
  assert.equal(pages.at(-1), 2, 'human-input recording must finish on humor page 2');
  assert(pages.includes(1) && pages.includes(2),
    'human-input recording must observe both humor pages');
  assert(pages.every((page, index) => index === 0 || page >= pages[index - 1]),
    'human-input recording states must advance monotonically');
}

function classifyOutputField(field: Record<string, unknown>): keyof QuoteRow | undefined {
  const name = String(field.name ?? '').trim().toLowerCase();
  const description = String(field.description ?? '').trim().toLowerCase();
  const text = `${name} ${description}`;
  if (/tag|category|topic|label/.test(text)) return undefined;
  if (/url|link|profile/.test(text) && /author|writer/.test(text)) return 'author_url';
  if (/author|writer/.test(text)) return 'author';
  if (/quote|quotation/.test(text) || name === 'text') return 'quote';
  return undefined;
}

export function deriveHumanChosenQuotesContract(requirement: unknown): QuotesContract {
  assert(plainObject(requirement), 'human-chosen requirement must be an object');
  assert(Array.isArray(requirement.requiredInputs) && requirement.requiredInputs.length === 0,
    'human must choose a fixed-page quote candidate with no required inputs');
  assert(Array.isArray(requirement.optionalInputs) && requirement.optionalInputs.length === 0,
    'human must choose a fixed-page quote candidate with no optional inputs');
  assert(Array.isArray(requirement.outputFields)
    && requirement.outputFields.length >= 2
    && requirement.outputFields.length <= 3,
  'human must choose a candidate with two or three quote-row output fields');

  const outputMap: Record<string, keyof QuoteRow> = {};
  const used = new Set<keyof QuoteRow>();
  for (const rawField of requirement.outputFields) {
    assert(plainObject(rawField), 'human-chosen output field must be an object');
    const name = String(rawField.name ?? '').trim();
    assert(name && rawField.type === 'string',
      'human-chosen quote output fields must have exact non-empty names and string types');
    const source = classifyOutputField(rawField);
    assert(source, `human-chosen output field "${name}" is not quote, author, or author URL`);
    assert(!used.has(source), `human-chosen candidate maps more than one field to ${source}`);
    used.add(source);
    outputMap[name] = source;
  }
  assert(used.has('quote') && used.has('author'),
    'human-chosen candidate must include both quotation text and author');

  return {
    inputName: '',
    replayTag: QUOTES_HUMAN_CHOOSE_TAG,
    taskTag: QUOTES_HUMAN_CHOOSE_TAG,
    outputMap,
  };
}

export function isHumanChosenQuotesRequirement(requirement: unknown): boolean {
  try {
    deriveHumanChosenQuotesContract(requirement);
    return true;
  } catch {
    return false;
  }
}

function actionObjects(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) return value.flatMap(actionObjects);
  if (!plainObject(value)) return [];
  return [
    ...(typeof value.action === 'string' ? [value] : []),
    ...Object.values(value).flatMap(actionObjects),
  ];
}

export function assertHumanChosenQuotesRule(
  rule: unknown,
  contract: QuotesContract,
): void {
  assert(plainObject(rule), 'human-chosen quotes rule must be an object');
  const domains = Array.isArray(rule.domain) ? rule.domain : [rule.domain];
  assert.deepEqual(domains, ['quotes.toscrape.com'],
    'human-chosen quotes rule domain must be exactly quotes.toscrape.com');
  const entry = new URL(String(rule.entry));
  assert.equal(entry.origin, QUOTES_BY_TAG_ORIGIN,
    'human-chosen quotes entry must remain on the exact HTTPS origin');
  assert(!/evaluate|executeJavascript|solveCaptcha/.test(JSON.stringify(rule)),
    'human-chosen quotes rule must not contain arbitrary script or CAPTCHA actions');
  assertVisibleModelExtractionRule(rule, 'human-chosen quotes');

  const actions = actionObjects([rule.steps, rule.hooks]);
  for (const navigation of actions.filter((action) => action.action === 'navigate')) {
    const target = new URL(String(navigation.url ?? ''), QUOTES_BY_TAG_ORIGIN);
    assert.equal(target.origin, QUOTES_BY_TAG_ORIGIN,
      'human-chosen quotes navigation must remain on the exact HTTPS origin');
  }
  const results = actions.filter((action) => action.action === 'sendResult');
  assert(results.length > 0, 'human-chosen quotes rule must send quote rows');
  const expected = Object.keys(contract.outputMap).sort();
  for (const action of results) {
    assert(plainObject(action.payload), 'human-chosen sendResult payload must be an object');
    assert.deepEqual(Object.keys(action.payload).sort(), expected,
      `human-chosen sendResult must contain exactly ${expected.join(', ')}`);
  }
}
