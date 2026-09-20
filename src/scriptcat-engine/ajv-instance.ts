import Ajv, { type ValidateFunction } from 'ajv';

export const sharedAjv = new Ajv({ allErrors: true, verbose: true });
export const sharedSchemaCache = new WeakMap<object, ValidateFunction>();
