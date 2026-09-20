/**
 * Derives task input values from an approved rule version's declared input
 * schema for the Stage 10 semantic fixture. Live LLM providers are free to
 * structure a collection requirement with required inputs, so the staging
 * acceptance harness must supply values like a real operator instead of
 * assuming every approved rule accepts empty variables.
 */

export const STAGE10_RESULT_TEXT = 'semantic action complete';
export const STAGE10_BUTTON_ID = 'semantic-action';
export const STAGE10_RESULT_ID = 'semantic-result';

type SchemaObject = Record<string, any>;

function asObject(value: unknown): SchemaObject {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as SchemaObject
    : {};
}

function requiredNames(schema: SchemaObject): Set<string> {
  const list = Array.isArray(schema.required) ? schema.required : [];
  return new Set(list.filter((name): name is string => typeof name === 'string'));
}

function fixtureString(name: string, property: SchemaObject): string {
  const description = typeof property.description === 'string' ? property.description : '';
  const label = `${name} ${description}`.toLowerCase();
  const mentions = (...words: string[]): boolean => words.some((word) => label.includes(word));
  if (mentions('selector') && mentions('button', 'action')) return `#${STAGE10_BUTTON_ID}`;
  if (mentions('selector') && mentions('result', 'output')) return `#${STAGE10_RESULT_ID}`;
  if (mentions('expected', 'text', 'value')) return STAGE10_RESULT_TEXT;
  if (mentions('button', 'action')) return STAGE10_BUTTON_ID;
  if (mentions('result', 'output')) return STAGE10_RESULT_ID;
  return `stage10-${name}`;
}

function numberValue(property: SchemaObject): number {
  if (typeof property.minimum === 'number') return property.minimum;
  if (typeof property.maximum === 'number' && property.maximum < 1) return property.maximum;
  return 1;
}

function synthesizeValue(name: string, property: SchemaObject): unknown {
  if (Array.isArray(property.enum) && property.enum.length > 0) return property.enum[0];
  switch (property.type) {
    case 'string': {
      const value = fixtureString(name, property);
      const minLength = typeof property.minLength === 'number' ? property.minLength : 0;
      return value.padEnd(minLength, 'x');
    }
    case 'integer':
    case 'number':
      return numberValue(property);
    case 'boolean':
      return true;
    case 'array': {
      const minItems = typeof property.minItems === 'number' ? Math.max(0, Math.floor(property.minItems)) : 0;
      const item = synthesizeValue(`${name}Item`, asObject(property.items));
      return Array.from({ length: minItems }, () => item);
    }
    case 'object':
      return buildRequiredInputs(property);
    default:
      return fixtureString(name, property);
  }
}

function buildRequiredInputs(schema: SchemaObject): Record<string, unknown> {
  const properties = asObject(schema.properties);
  const required = requiredNames(schema);
  const inputs: Record<string, unknown> = {};
  for (const [name, rawProperty] of Object.entries(properties)) {
    if (!required.has(name)) continue;
    const property = asObject(rawProperty);
    if ('default' in property) continue;
    if (property['x-secret'] === true) {
      throw new Error(`staging harness cannot synthesize required secret input "${name}"`);
    }
    inputs[name] = synthesizeValue(name, property);
  }
  return inputs;
}

/**
 * Returns the minimal input snapshot a task or schedule needs for the given
 * rule-version input schema: only required properties without declared
 * defaults, since the server applies defaults and rejects undeclared keys.
 */
export function buildTaskInputs(inputSchema: unknown): Record<string, unknown> {
  return buildRequiredInputs(asObject(inputSchema));
}
