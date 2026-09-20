import { describe, expect, it } from 'vitest';
import {
  APPLE_MODEL_REQUIREMENT,
  APPLE_MODEL_OUTPUT_SCHEMA,
  assertAppleModelRows,
  assertAppleModelRequirement,
  assertAppleVisibleExtractionRule,
} from './apple-models';

const outputSchema = APPLE_MODEL_OUTPUT_SCHEMA;

function completeRows(): Record<string, unknown>[] {
  const rows: Record<string, unknown>[] = [];
  for (const size of ['11-inch model', '13-inch model']) {
    for (const storage of ['128 GB', '256GB', '512 GB', '1 TB']) {
      for (const connectivity of ['Wi-Fi', 'Wi-Fi + Cellular']) {
        rows.push({ size, storage, connectivity, price: '$749.00' });
      }
    }
  }
  return rows;
}

describe('Apple live-workflow model contract', () => {
  it('asks for visible, color-deduplicated model combinations', () => {
    expect(APPLE_MODEL_REQUIREMENT).toContain('visible interactive configurator');
    expect(APPLE_MODEL_REQUIREMENT).toContain('exactly these string output fields');
    expect(APPLE_MODEL_REQUIREMENT).toContain('Treat different colors');
    expect(APPLE_MODEL_REQUIREMENT).toContain('exactly one row');
    expect(APPLE_MODEL_REQUIREMENT).toContain('no user-supplied inputs');
    expect(APPLE_MODEL_REQUIREMENT).toContain('Do not extract from hidden or noscript');
  });

  it('accepts only an input-free normalized requirement with the requested fields', () => {
    const requirement = {
      requiredInputs: [],
      optionalInputs: [],
      outputFields: outputSchema.required.map((name) => ({ name, type: 'string' })),
    };
    expect(() => assertAppleModelRequirement(requirement)).not.toThrow();
    expect(() => assertAppleModelRequirement({
      ...requirement,
      requiredInputs: [{ name: 'size_elements', type: 'array' }],
    })).toThrow('must not require task inputs');
  });

  it('accepts the complete 16-model Cartesian product', () => {
    expect(() => assertAppleModelRows(completeRows(), outputSchema)).not.toThrow();
  });

  it('rejects a duplicate model in place of a missing combination', () => {
    const rows = completeRows();
    rows[15] = { ...rows[0] };
    expect(() => assertAppleModelRows(rows, outputSchema)).toThrow('every expected model combination exactly once');
  });

  it('rejects color or other unrequested output fields', () => {
    const rows = completeRows().map((row) => ({ ...row, color: 'Blue' }));
    const schema = {
      ...outputSchema,
      properties: { ...outputSchema.properties, color: { type: 'string' } },
      required: [...outputSchema.required, 'color'],
    };
    expect(() => assertAppleModelRows(rows, schema)).toThrow('must contain only');
  });

  it('rejects missing, unformatted, or non-string prices', () => {
    const missing = completeRows();
    delete missing[0].price;
    expect(() => assertAppleModelRows(missing, outputSchema)).toThrow();

    for (const price of ['', '749.00', 749]) {
      const rows = completeRows();
      rows[0] = { ...rows[0], price };
      expect(() => assertAppleModelRows(rows, outputSchema)).toThrow();
    }
  });

  it('accepts visible extraction targets and rejects the hidden fallback', () => {
    expect(() => assertAppleVisibleExtractionRule({
      steps: [{ action: 'extract', target: { selector: '.rf-bfe-selectionarea .price', visible: true } }],
    })).not.toThrow();
    expect(() => assertAppleVisibleExtractionRule({
      steps: [{ action: 'extract', target: { selector: '.product-selection-area.noscript', visible: true } }],
    })).toThrow('must not address hidden or noscript');
    expect(() => assertAppleVisibleExtractionRule({
      steps: [{ action: 'extractText', target: { selector: '.current-price' } }],
    })).toThrow('must explicitly require visible content');
  });

  it('resolves visible extraction metadata from selector aliases', () => {
    expect(() => assertAppleVisibleExtractionRule({
      selectors: { cards: { selector: '.rf-bfe-selectionarea .item', visible: true } },
      steps: [{ action: 'extract', target: { $ref: 'cards' } }],
    })).not.toThrow();
    expect(() => assertAppleVisibleExtractionRule({
      selectors: { cards: { selector: '.product-selection-area.noscript .item', visible: true } },
      steps: [{ action: 'extract', target: { $ref: 'cards' } }],
    })).toThrow('must not address hidden or noscript');
  });
});
