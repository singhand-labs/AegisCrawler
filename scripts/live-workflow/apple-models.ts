import assert from 'node:assert/strict';
import {
  assertExactModelRows,
  assertInputFreeModelRequirement,
  assertVisibleModelExtractionRule,
} from './model-contract';

export const APPLE_MODEL_REQUIREMENT = [
  'Collect every available iPad Air model from the visible interactive configurator.',
  'Return exactly these string output fields: size, storage, connectivity, and price.',
  'Treat different colors of the same size/storage/connectivity model as one model,',
  'and return exactly one row for each size/storage/connectivity combination.',
  'This task has no user-supplied inputs; size, storage, and connectivity choices are page data.',
  'Do not extract from hidden or noscript fallback content.',
].join(' ');

export const APPLE_MODEL_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    size: { type: 'string' },
    storage: { type: 'string' },
    connectivity: { type: 'string' },
    price: { type: 'string' },
  },
  required: ['size', 'storage', 'connectivity', 'price'],
  additionalProperties: false,
} as const;

const EXPECTED_FIELDS = Object.keys(APPLE_MODEL_OUTPUT_SCHEMA.properties).sort();
const EXPECTED_FIELD_TYPES = Object.fromEntries(
  Object.entries(APPLE_MODEL_OUTPUT_SCHEMA.properties)
    .map(([field, schema]) => [field, schema.type]),
);
const EXPECTED_SIZES = ['11', '13'];
const EXPECTED_STORAGE = ['128GB', '256GB', '512GB', '1TB'];
const EXPECTED_CONNECTIVITY = ['cellular', 'wifi'];
const DISPLAYED_PRICE_PATTERN = /^\s*\$[\d,]+(?:\.\d{2})?\s*$/;

function normalizeSize(value: unknown): string {
  const text = String(value).toLowerCase();
  if (/\b11\b/.test(text)) return '11';
  if (/\b13\b/.test(text)) return '13';
  return text.replace(/\s+/g, ' ').trim();
}

function normalizeStorage(value: unknown): string {
  return String(value).toUpperCase().replace(/\s+/g, '');
}

function normalizeConnectivity(value: unknown): string {
  const text = String(value).toLowerCase();
  if (text.includes('cellular')) return 'cellular';
  if (/wi[\s-]?fi/.test(text)) return 'wifi';
  return text.replace(/\s+/g, ' ').trim();
}

/** Scenario-specific semantic contract for the current Apple configurator. */
export function assertAppleModelRows(rows: unknown[], outputSchema: unknown): void {
  const expected: string[] = [];
  for (const size of EXPECTED_SIZES) {
    for (const storage of EXPECTED_STORAGE) {
      for (const connectivity of EXPECTED_CONNECTIVITY) {
        expected.push(`${size}|${storage}|${connectivity}`);
      }
    }
  }
  assertExactModelRows(rows, outputSchema, {
    label: 'Apple model',
    fields: EXPECTED_FIELDS,
    expectedFieldTypes: EXPECTED_FIELD_TYPES,
    expectedCombinationKeys: expected,
    combinationKey: (row) => [
      normalizeSize(row.size),
      normalizeStorage(row.storage),
      normalizeConnectivity(row.connectivity),
    ].join('|'),
    validateRow: (row, index) => {
      assert(row.price !== null && row.price !== undefined && row.price !== '',
        `Apple model row ${index} must include a displayed price`);
      assert(DISPLAYED_PRICE_PATTERN.test(String(row.price)),
        `Apple model row ${index} price must be formatted as a displayed price`);
    },
  });
}

/** Fail before DSL generation if normalization turns page elements into task inputs. */
export function assertAppleModelRequirement(requirement: unknown): void {
  assertInputFreeModelRequirement(requirement, 'Apple model', EXPECTED_FIELDS, EXPECTED_FIELD_TYPES);
}

/** The qualification must never pass by querying Apple's hidden fallback matrix. */
export function assertAppleVisibleExtractionRule(rule: unknown): void {
  assertVisibleModelExtractionRule(rule, 'Apple');
}
