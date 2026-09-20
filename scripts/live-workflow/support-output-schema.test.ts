import { describe, expect, it } from 'vitest';
import { assertRowsConformToOutputSchema } from './output-schema';

describe('live workflow generic output-schema conformance', () => {
  const schema = {
    type: 'object',
    properties: {
      title: { type: 'string' },
      price: { type: 'string' },
    },
    required: ['title', 'price'],
    additionalProperties: false,
  };

  it('does not impose a site-specific currency on generic price fields', () => {
    expect(() => assertRowsConformToOutputSchema(
      [{ title: 'Book', price: '£51.77' }],
      schema,
      [1, 1],
    )).not.toThrow();
    expect(() => assertRowsConformToOutputSchema(
      [{ title: 'Product', price: '$51.77' }],
      schema,
      [1, 1],
    )).not.toThrow();
  });

  it('continues to enforce row bands, declared fields, and runtime types', () => {
    expect(() => assertRowsConformToOutputSchema([], schema, [1, 1]))
      .toThrow(/row count/);
    expect(() => assertRowsConformToOutputSchema(
      [{ title: 'Book', price: 51.77 }],
      schema,
      [1, 1],
    )).toThrow(/declared type string/);
    expect(() => assertRowsConformToOutputSchema(
      [{ title: 'Book', price: '£51.77', hidden: true }],
      schema,
      [1, 1],
    )).toThrow(/undeclared key/);
  });
});
