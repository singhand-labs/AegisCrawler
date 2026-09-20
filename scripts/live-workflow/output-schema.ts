import assert from 'node:assert/strict';

function isPlainObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/** Light generic conformance check. Site-specific value formats belong to scenario oracles. */
export function assertRowsConformToOutputSchema(
  rows: any[],
  outputSchema: any,
  rowBand: [number, number],
): void {
  assert(rows.length >= rowBand[0] && rows.length <= rowBand[1],
    `collected row count ${rows.length} is outside the expected band ${rowBand[0]}..${rowBand[1]}`);
  const properties = isPlainObject(outputSchema?.properties) ? outputSchema.properties : {};
  const propertyNames = Object.keys(properties);
  assert(propertyNames.length > 0,
    `approved output schema declares no properties: ${JSON.stringify(outputSchema).slice(0, 300)}`);
  const required = Array.isArray(outputSchema?.required) ? outputSchema.required.map(String) : [];
  const typeMatches = (value: unknown, type: unknown): boolean => {
    switch (type) {
      case 'string': return typeof value === 'string';
      case 'number': return typeof value === 'number';
      case 'boolean': return typeof value === 'boolean';
      case 'array': return Array.isArray(value);
      case 'object': return isPlainObject(value);
      default: return true;
    }
  };
  rows.forEach((row, index) => {
    assert(isPlainObject(row), `row ${index} is not an object: ${JSON.stringify(row)?.slice(0, 200)}`);
    for (const key of Object.keys(row)) {
      assert(propertyNames.includes(key), `row ${index} carries undeclared key "${key}"`);
    }
    for (const name of required) {
      assert(name in row, `row ${index} is missing required field "${name}"`);
    }
    for (const [key, value] of Object.entries(row)) {
      assert(typeMatches(value, properties[key]?.type),
        `row ${index} field "${key}" does not match declared type ${String(properties[key]?.type)}`);
    }
  });
}
