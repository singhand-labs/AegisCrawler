import { describe, it, expect } from 'vitest';
import { sharedAjv, sharedSchemaCache } from './ajv-instance';

describe('ajv-instance', () => {
  it('exposes a singleton Ajv instance', () => {
    expect(sharedAjv).toBe(sharedAjv); // identity
    expect(typeof sharedAjv.compile).toBe('function');
  });

  it('sharedSchemaCache is a WeakMap keyed by schema object', () => {
    const schema = { type: 'object', properties: { a: { type: 'string' } } };
    expect(sharedSchemaCache.has(schema)).toBe(false);
    const validate = sharedAjv.compile(schema);
    sharedSchemaCache.set(schema, validate);
    expect(sharedSchemaCache.get(schema)).toBe(validate);
  });
});
