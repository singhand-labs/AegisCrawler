import type { Environment } from './types';

/**
 * Validate ctx.extracted against rule.output schema.
 * - onInvalid='fail' (default): returns ok=false; caller sets status='failure'.
 * - onInvalid='warn': logs warn and returns ok=true.
 *
 * The validator is interpreter-based and never uses eval/new Function, so it
 * runs under Manifest V3 extension CSP and strict page CSP where Ajv's code
 * generation is blocked (replay runner, in-page worker). It covers the JSON
 * Schema subset the server contracts use (server/internal/rule/contracts.go):
 * type/properties/required/additionalProperties/items plus common scalar
 * constraints and combinators. Keywords outside that subset throw, which the
 * caller routes through markFatalError as ConfigError (loud, never silently
 * skipped). "$schema" declarations (e.g. draft 2020-12 from the server) are
 * accepted and ignored: supported constructs share semantics across drafts.
 */
export async function validateOutput(
  data: any,
  schema: object,
  onInvalid: 'warn' | 'fail',
  env: Environment,
): Promise<{ ok: boolean; errors?: string }> {
  const errors = validateNode(data, checkedSchema(schema, 'output schema'), 'data');
  if (errors.length === 0) return { ok: true };
  const text = errors.join(', ');
  if (onInvalid === 'warn') {
    await env.transport.sendLog('warn', 'output schema validation failed', { errors: text });
    return { ok: true, errors: text };
  }
  return { ok: false, errors: text };
}

const ANNOTATION_KEYWORDS = new Set([
  '$schema', '$id', '$comment', 'title', 'description', 'default', 'examples',
  'format', 'readOnly', 'writeOnly', 'deprecated',
]);

const SUPPORTED_KEYWORDS = new Set([
  ...ANNOTATION_KEYWORDS,
  'type', 'enum', 'const',
  'properties', 'required', 'additionalProperties', 'minProperties', 'maxProperties',
  'items', 'minItems', 'maxItems', 'uniqueItems',
  'minLength', 'maxLength', 'pattern',
  'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf',
  'anyOf', 'oneOf', 'allOf', 'not',
]);

type SchemaMap = Record<string, unknown>;

function checkedSchema(schema: unknown, label: string): SchemaMap {
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) {
    throw new Error(`${label} must be a JSON object`);
  }
  for (const key of Object.keys(schema as SchemaMap)) {
    if (!SUPPORTED_KEYWORDS.has(key)) {
      throw new Error(`${label} keyword "${key}" is not supported by the browser-safe output validator`);
    }
  }
  return schema as SchemaMap;
}

function typeOf(data: any): string {
  if (data === null) return 'null';
  if (Array.isArray(data)) return 'array';
  if (typeof data === 'number' && Number.isInteger(data)) return 'integer';
  return typeof data;
}

function matchesType(data: any, type: string): boolean {
  if (type === 'number') return typeof data === 'number' && Number.isFinite(data);
  if (type === 'integer') return typeof data === 'number' && Number.isInteger(data);
  if (type === 'object') return data !== null && typeof data === 'object' && !Array.isArray(data);
  return typeOf(data) === type;
}

function deepEqual(a: any, b: any): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b || a === null || b === null) return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length
      && a.every((value, index) => deepEqual(value, b[index]));
  }
  if (typeof a === 'object') {
    const aKeys = Object.keys(a);
    const bKeys = Object.keys(b);
    return aKeys.length === bKeys.length && aKeys.every((key) => deepEqual(a[key], b[key]));
  }
  return false;
}

function validateNode(data: any, schema: SchemaMap, path: string): string[] {
  const errors: string[] = [];
  const fail = (message: string): void => { errors.push(`${path} ${message}`); };

  if (schema.const !== undefined && !deepEqual(data, schema.const)) fail(`must be equal to constant`);
  if (Array.isArray(schema.enum) && !schema.enum.some((value) => deepEqual(data, value))) fail('must be equal to one of the allowed values');

  if (schema.type !== undefined) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.every((entry) => typeof entry === 'string')) throw new Error('output schema "type" must be a string or an array of strings');
    if (!(types as string[]).some((type) => matchesType(data, type))) fail(`must be ${(types as string[]).join(' or ')}`);
  }

  if (data !== null && typeof data === 'object' && !Array.isArray(data)) {
    const required = schema.required;
    if (required !== undefined) {
      if (!Array.isArray(required)) throw new Error('output schema "required" must be an array');
      for (const key of required) if (!(key in data)) fail(`must have required property '${String(key)}'`);
    }
    const properties = schema.properties;
    if (properties !== undefined) {
      if (typeof properties !== 'object' || properties === null || Array.isArray(properties)) {
        throw new Error('output schema "properties" must be an object');
      }
      for (const [key, value] of Object.entries(data)) {
        const child = (properties as SchemaMap)[key];
        if (child !== undefined) errors.push(...validateNode(value, checkedSchema(child, 'output schema "properties"'), `${path}/${key}`));
      }
    }
    if (schema.additionalProperties !== undefined) {
      const declared = new Set(Object.keys((schema.properties as SchemaMap | undefined) ?? {}));
      const extras = Object.keys(data).filter((key) => !declared.has(key));
      if (schema.additionalProperties === false && extras.length > 0) {
        fail(`must NOT have additional properties (${extras.join(', ')})`);
      } else if (typeof schema.additionalProperties === 'object' && schema.additionalProperties !== null) {
        const child = checkedSchema(schema.additionalProperties, 'output schema "additionalProperties"');
        for (const key of extras) errors.push(...validateNode(data[key], child, `${path}/${key}`));
      }
    }
    if (typeof schema.minProperties === 'number' && Object.keys(data).length < schema.minProperties) {
      fail(`must have at least ${schema.minProperties} properties`);
    }
    if (typeof schema.maxProperties === 'number' && Object.keys(data).length > schema.maxProperties) {
      fail(`must have at most ${schema.maxProperties} properties`);
    }
  }

  if (Array.isArray(data)) {
    if (schema.items !== undefined) {
      const child = checkedSchema(schema.items, 'output schema "items"');
      data.forEach((value, index) => errors.push(...validateNode(value, child, `${path}/${index}`)));
    }
    if (typeof schema.minItems === 'number' && data.length < schema.minItems) fail(`must have at least ${schema.minItems} items`);
    if (typeof schema.maxItems === 'number' && data.length > schema.maxItems) fail(`must have at most ${schema.maxItems} items`);
    if (schema.uniqueItems === true) {
      for (let i = 0; i < data.length; i += 1) {
        for (let j = i + 1; j < data.length; j += 1) {
          if (deepEqual(data[i], data[j])) { fail(`must not have duplicate items (${i} and ${j})`); i = data.length; break; }
        }
      }
    }
  }

  if (typeof data === 'string') {
    if (typeof schema.minLength === 'number' && data.length < schema.minLength) fail(`must be at least ${schema.minLength} characters`);
    if (typeof schema.maxLength === 'number' && data.length > schema.maxLength) fail(`must be at most ${schema.maxLength} characters`);
    if (typeof schema.pattern === 'string' && !(new RegExp(schema.pattern).test(data))) fail(`must match pattern ${schema.pattern}`);
  }

  if (typeof data === 'number') {
    if (typeof schema.minimum === 'number') {
      const exclusive = schema.exclusiveMinimum;
      if (exclusive === true ? data <= schema.minimum : data < schema.minimum) fail(`must be >= ${schema.minimum}`);
    }
    if (typeof schema.maximum === 'number') {
      const exclusive = schema.exclusiveMaximum;
      if (exclusive === true ? data >= schema.maximum : data > schema.maximum) fail(`must be <= ${schema.maximum}`);
    }
    if (typeof schema.exclusiveMinimum === 'number' && data <= schema.exclusiveMinimum) fail(`must be > ${schema.exclusiveMinimum}`);
    if (typeof schema.exclusiveMaximum === 'number' && data >= schema.exclusiveMaximum) fail(`must be < ${schema.exclusiveMaximum}`);
    if (typeof schema.multipleOf === 'number') {
      // M-9: use epsilon comparison instead of strict modulo to avoid floating-
      // point precision errors (e.g., 0.3 % 0.1 = 0.0999... in JS).
      const quotient = data / schema.multipleOf;
      if (Math.abs(quotient - Math.round(quotient)) > 1e-9) fail(`must be a multiple of ${schema.multipleOf}`);
    }
  }

  if (Array.isArray(schema.anyOf) && !schema.anyOf.some((child) => validateNode(data, checkedSchema(child, 'output schema "anyOf"'), path).length === 0)) {
    fail('must match at least one anyOf schema');
  }
  if (Array.isArray(schema.allOf)) {
    schema.allOf.forEach((child) => errors.push(...validateNode(data, checkedSchema(child, 'output schema "allOf"'), path)));
  }
  if (Array.isArray(schema.oneOf)) {
    const matches = schema.oneOf.filter((child) => validateNode(data, checkedSchema(child, 'output schema "oneOf"'), path).length === 0).length;
    if (matches !== 1) fail(`must match exactly one oneOf schema (matched ${matches})`);
  }
  if (schema.not !== undefined && validateNode(data, checkedSchema(schema.not, 'output schema "not"'), path).length === 0) {
    fail('must not match the "not" schema');
  }

  return errors;
}
