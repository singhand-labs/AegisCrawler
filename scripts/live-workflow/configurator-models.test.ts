import { describe, expect, it } from 'vitest';
import {
  CONFIGURATOR_EXPECTED_MODELS,
  CONFIGURATOR_MODEL_REQUIREMENT,
  assertConfiguratorModelRows,
  assertConfiguratorModelRequirement,
  assertConfiguratorVisibleExtractionRule,
} from './configurator-models';

const outputSchema = {
  type: 'object',
  properties: {
    size: { type: 'string' },
    storage: { type: 'string' },
    connectivity: { type: 'string' },
    price: { type: 'string' },
  },
  required: ['size', 'storage', 'connectivity', 'price'],
  additionalProperties: false,
};

describe('compact configurator live-workflow contract', () => {
  it('encodes the Apple-class requirement without turning controls into inputs', () => {
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('visible interactive configurator');
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('one model at a time');
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('Next model control');
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('Treat the Sky Blue and Violet colors');
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('exactly one row');
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('no user-supplied inputs');
    expect(CONFIGURATOR_MODEL_REQUIREMENT).toContain('Do not extract from hidden or noscript');
  });

  it('accepts only an input-free normalized string-field requirement', () => {
    const requirement = {
      requiredInputs: [],
      optionalInputs: [],
      outputFields: outputSchema.required.map((name) => ({ name, type: 'string' })),
    };
    expect(() => assertConfiguratorModelRequirement(requirement)).not.toThrow();
    expect(() => assertConfiguratorModelRequirement({
      ...requirement,
      requiredInputs: [{ name: 'storage_options', type: 'array' }],
    })).toThrow('must not require task inputs');
    expect(() => assertConfiguratorModelRequirement({
      ...requirement,
      outputFields: requirement.outputFields.map((field) =>
        field.name === 'price' ? { ...field, type: 'number' } : field),
    })).toThrow('field price must have type string');
  });

  it('accepts the exact eight-model product with fixture prices', () => {
    expect(() => assertConfiguratorModelRows(
      CONFIGURATOR_EXPECTED_MODELS.map((model) => ({ ...model })),
      outputSchema,
    )).not.toThrow();
  });

  it('rejects duplicate colors, missing combinations, and wrong prices', () => {
    const duplicate = CONFIGURATOR_EXPECTED_MODELS.map((model) => ({ ...model }));
    duplicate[7] = { ...duplicate[0] };
    expect(() => assertConfiguratorModelRows(duplicate, outputSchema))
      .toThrow('every expected model combination exactly once');

    const wrongPrice = CONFIGURATOR_EXPECTED_MODELS.map((model) => ({ ...model }));
    wrongPrice[0].price = '$1.00';
    expect(() => assertConfiguratorModelRows(wrongPrice, outputSchema)).toThrow('wrong displayed price');

    const colorRows = CONFIGURATOR_EXPECTED_MODELS.map((model) => ({ ...model, color: 'Sky Blue' }));
    expect(() => assertConfiguratorModelRows(colorRows, outputSchema))
      .toThrow('must contain exactly the requested fields');
  });

  it('requires visible extraction and rejects the fallback through aliases', () => {
    expect(() => assertConfiguratorVisibleExtractionRule({
      selectors: { models: { selector: '#current-model .model-card', visible: true } },
      steps: [{ action: 'extract', target: { $ref: 'models' } }],
    })).not.toThrow();
    expect(() => assertConfiguratorVisibleExtractionRule({
      selectors: { models: { selector: '.product-selection-area.noscript .item', visible: true } },
      steps: [{ action: 'extract', target: { $ref: 'models' } }],
    })).toThrow('must not address hidden or noscript');
    expect(() => assertConfiguratorVisibleExtractionRule({
      steps: [{ action: 'extract', target: { selector: '.model-card' } }],
    })).toThrow('must explicitly require visible content');
  });
});
