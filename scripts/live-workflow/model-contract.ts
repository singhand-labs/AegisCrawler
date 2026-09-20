import assert from 'node:assert/strict';

export const MODEL_EXTRACTION_ACTIONS = new Set([
  'extract', 'extractAttribute', 'extractHtml', 'extractJson',
  'extractPageInfo', 'extractTable', 'extractText',
]);

const HIDDEN_TARGET_PATTERN = /noscript|\[hidden\]|type\s*=\s*["']?hidden|aria-hidden\s*=\s*["']?true|display\s*:\s*none|visibility\s*:\s*hidden/i;

export interface ExactModelRowsContract {
  label: string;
  fields: readonly string[];
  expectedCombinationKeys: readonly string[];
  combinationKey: (row: Record<string, unknown>) => string;
  expectedFieldTypes?: Readonly<Record<string, string>>;
  validateRow?: (row: Record<string, unknown>, index: number) => void;
}

export function plainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/**
 * Shared semantic oracle for the Apple qualification and its compact local
 * analogue. It rejects plausible-looking subsets, duplicate color variants,
 * extra fields, and schema drift instead of accepting a broad row-count band.
 */
export function assertExactModelRows(
  rows: unknown[],
  outputSchema: unknown,
  contract: ExactModelRowsContract,
): void {
  const expectedFields = [...contract.fields].sort();
  const expectedKeys = [...contract.expectedCombinationKeys].sort();
  assert.equal(rows.length, expectedKeys.length,
    `${contract.label} qualification must return exactly ${expectedKeys.length} rows`);
  assert(plainObject(outputSchema), `${contract.label} output schema must be an object`);
  const properties = plainObject(outputSchema.properties) ? outputSchema.properties : {};
  assert.deepEqual(Object.keys(properties).sort(), expectedFields,
    `${contract.label} output schema must contain only ${expectedFields.join(', ')}`);
  assert.deepEqual(
    (Array.isArray(outputSchema.required) ? outputSchema.required.map(String) : []).sort(),
    expectedFields,
    `every ${contract.label} output field must be required`,
  );
  for (const [field, expectedType] of Object.entries(contract.expectedFieldTypes ?? {})) {
    const property = plainObject(properties[field]) ? properties[field] : {};
    assert.equal(property.type, expectedType,
      `${contract.label} output field ${field} must have type ${expectedType}`);
  }

  const actualKeys: string[] = [];
  for (const [index, value] of rows.entries()) {
    assert(plainObject(value), `${contract.label} row ${index} must be an object`);
    assert.deepEqual(Object.keys(value).sort(), expectedFields,
      `${contract.label} row ${index} must contain exactly the requested fields`);
    contract.validateRow?.(value, index);
    actualKeys.push(contract.combinationKey(value));
  }
  assert.deepEqual(actualKeys.sort(), expectedKeys,
    `${contract.label} results must contain every expected model combination exactly once`);
}

/** Page controls and discovered option values must not become task inputs. */
export function assertInputFreeModelRequirement(
  requirement: unknown,
  label: string,
  fields: readonly string[],
  expectedFieldTypes?: Readonly<Record<string, string>>,
): void {
  assert(plainObject(requirement), `normalized ${label} requirement must be an object`);
  assert.deepEqual(requirement.requiredInputs, [], `${label} collection must not require task inputs`);
  assert.deepEqual(requirement.optionalInputs, [], `${label} collection must not declare optional task inputs`);
  assert(Array.isArray(requirement.outputFields), `normalized ${label} outputFields must be an array`);
  const outputFields = requirement.outputFields.filter(plainObject);
  assert.deepEqual(outputFields.map((field) => String(field.name)).sort(), [...fields].sort(),
    `normalized ${label} requirement must preserve the requested fields`);
  for (const [fieldName, expectedType] of Object.entries(expectedFieldTypes ?? {})) {
    const field = outputFields.find((candidate) => candidate.name === fieldName);
    assert.equal(field?.type, expectedType,
      `normalized ${label} field ${fieldName} must have type ${expectedType}`);
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

function resolvedTarget(
  target: Record<string, unknown>,
  selectors: Record<string, unknown>,
): Record<string, unknown> {
  const ref = typeof target.$ref === 'string' ? target.$ref.trim() : '';
  if (!ref || !plainObject(selectors[ref])) return target;
  return { ...(selectors[ref] as Record<string, unknown>), ...target };
}

/** A visible-only requirement must be backed by a visible target at runtime. */
export function assertVisibleModelExtractionRule(rule: unknown, label: string): void {
  assert(plainObject(rule), `approved ${label} rule must be an object`);
  const selectors = plainObject(rule.selectors) ? rule.selectors : {};
  const extractions = actionObjects([rule.steps, rule.hooks])
    .filter((step) => MODEL_EXTRACTION_ACTIONS.has(String(step.action)));
  const targeted = extractions.filter((step) => plainObject(step.target));
  assert(targeted.length > 0, `approved ${label} rule must contain a target-based extraction action`);
  for (const step of targeted) {
    const target = step.target as Record<string, unknown>;
    const resolved = resolvedTarget(target, selectors);
    assert.equal(resolved.visible, true,
      `${label} ${String(step.action)} target must explicitly require visible content`);
    assert(!HIDDEN_TARGET_PATTERN.test(`${JSON.stringify(target)} ${JSON.stringify(resolved)}`),
      `${label} ${String(step.action)} target must not address hidden or noscript content`);
  }
}
