import assert from 'node:assert/strict';
import {
  assertExactModelRows,
  assertInputFreeModelRequirement,
  assertVisibleModelExtractionRule,
} from './model-contract';

export const CONFIGURATOR_MODEL_REQUIREMENT = [
  'Collect every available MiniPad Air model by traversing the visible interactive configurator, which shows one model at a time and provides a Next model control.',
  'Return exactly these string output fields: size, storage, connectivity, and price.',
  'Treat the Sky Blue and Violet colors of the same size/storage/connectivity model as one model,',
  'and return exactly one row for each size/storage/connectivity combination.',
  'This task has no user-supplied inputs; the size, color, storage, and connectivity controls are page data.',
  'Do not extract from hidden or noscript fallback content.',
].join(' ');

const EXPECTED_FIELDS = ['connectivity', 'price', 'size', 'storage'];
const STRING_FIELD_TYPES = Object.fromEntries(EXPECTED_FIELDS.map((field) => [field, 'string']));

export interface ConfiguratorExpectedModel extends Record<string, unknown> {
  size: string;
  storage: string;
  connectivity: string;
  price: string;
}

export const CONFIGURATOR_EXPECTED_MODELS: readonly ConfiguratorExpectedModel[] = [
  { size: '11-inch', storage: '128GB', connectivity: 'Wi-Fi', price: '$599.00' },
  { size: '11-inch', storage: '128GB', connectivity: 'Wi-Fi + Cellular', price: '$749.00' },
  { size: '11-inch', storage: '256GB', connectivity: 'Wi-Fi', price: '$699.00' },
  { size: '11-inch', storage: '256GB', connectivity: 'Wi-Fi + Cellular', price: '$849.00' },
  { size: '13-inch', storage: '128GB', connectivity: 'Wi-Fi', price: '$799.00' },
  { size: '13-inch', storage: '128GB', connectivity: 'Wi-Fi + Cellular', price: '$949.00' },
  { size: '13-inch', storage: '256GB', connectivity: 'Wi-Fi', price: '$899.00' },
  { size: '13-inch', storage: '256GB', connectivity: 'Wi-Fi + Cellular', price: '$1,049.00' },
];

function normalizeSize(value: unknown): string {
  const match = String(value).match(/\b(11|13)\b/);
  return match?.[1] ?? String(value).toLowerCase().replace(/\s+/g, ' ').trim();
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

function combinationKey(row: Record<string, unknown>): string {
  return [
    normalizeSize(row.size),
    normalizeStorage(row.storage),
    normalizeConnectivity(row.connectivity),
  ].join('|');
}

function priceAmount(value: unknown): number | undefined {
  const match = String(value).match(/[\d,]+(?:\.\d{1,2})?/);
  if (!match) return undefined;
  const parsed = Number(match[0].replaceAll(',', ''));
  return Number.isFinite(parsed) ? parsed : undefined;
}

const EXPECTED_PRICES = new Map(
  CONFIGURATOR_EXPECTED_MODELS.map((model) => [combinationKey(model), priceAmount(model.price)]),
);

/** Exact, price-aware oracle for the compact Apple-like fixture. */
export function assertConfiguratorModelRows(rows: unknown[], outputSchema: unknown): void {
  assertExactModelRows(rows, outputSchema, {
    label: 'configurator model',
    fields: EXPECTED_FIELDS,
    expectedCombinationKeys: CONFIGURATOR_EXPECTED_MODELS.map(combinationKey),
    expectedFieldTypes: STRING_FIELD_TYPES,
    combinationKey,
    validateRow: (row, index) => {
      const key = combinationKey(row);
      assert.equal(priceAmount(row.price), EXPECTED_PRICES.get(key),
        `configurator model row ${index} has the wrong displayed price for ${key}`);
    },
  });
}

export function assertConfiguratorModelRequirement(requirement: unknown): void {
  assertInputFreeModelRequirement(
    requirement,
    'configurator model',
    EXPECTED_FIELDS,
    STRING_FIELD_TYPES,
  );
}

export function assertConfiguratorVisibleExtractionRule(rule: unknown): void {
  assertVisibleModelExtractionRule(rule, 'configurator');
}
