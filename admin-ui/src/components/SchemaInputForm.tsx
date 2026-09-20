import { Alert, Form, Input, InputNumber, Select, Typography } from 'antd';
import type { Rule } from 'antd/es/form';
import type { JSONSchema } from '../api/types';

const { TextArea } = Input;

interface SchemaInputFormProps {
  schema?: JSONSchema;
  name?: string;
}

function parseStructuredValue(value: unknown, type: 'object' | 'array'): unknown {
  if (typeof value !== 'string') return value;
  if (value.trim() === '') return undefined;
  const parsed = JSON.parse(value) as unknown;
  if (type === 'array' ? !Array.isArray(parsed) : parsed === null || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(type === 'array' ? 'Enter a JSON array' : 'Enter a JSON object');
  }
  return parsed;
}

export function schemaFormDefaults(schema?: JSONSchema): Record<string, unknown> {
  const defaults: Record<string, unknown> = {};
  Object.entries(schema?.properties ?? {}).forEach(([name, property]) => {
    if (property['x-secret'] || property.default === undefined) return;
    defaults[name] = property.type === 'object' || property.type === 'array'
      ? JSON.stringify(property.default, null, 2)
      : property.default;
  });
  return defaults;
}

export function normalizeSchemaInputs(
  schema: JSONSchema | undefined,
  values: Record<string, unknown> | undefined,
): Record<string, unknown> {
  const normalized: Record<string, unknown> = {};
  Object.entries(schema?.properties ?? {}).forEach(([name, property]) => {
    if (property['x-secret']) return;
    let value = values?.[name];
    if (value === undefined || value === null) return;
    if (property.type === 'object' || property.type === 'array') {
      value = parseStructuredValue(value, property.type);
      if (value === undefined) return;
    }
    normalized[name] = value;
  });
  return normalized;
}

export function requiredSecretInputs(schema?: JSONSchema): string[] {
  const required = new Set(schema?.required ?? []);
  return Object.entries(schema?.properties ?? {})
    .filter(([name, property]) => required.has(name) && property['x-secret'])
    .map(([name]) => name);
}

function validationRules(name: string, property: JSONSchema, required: boolean): Rule[] {
  const rules: Rule[] = [];
  if (required) {
    rules.push({ required: true, message: `${name} is required` });
  }
  if (property.type === 'string') {
    if (property.minLength !== undefined) rules.push({ min: property.minLength });
    if (property.maxLength !== undefined) rules.push({ max: property.maxLength });
    if (property.pattern) rules.push({ pattern: new RegExp(property.pattern), message: `${name} has an invalid format` });
  }
  if (property.type === 'integer') {
    rules.push({
      validator: (_, value) => value === undefined || Number.isInteger(value)
        ? Promise.resolve()
        : Promise.reject(new Error(`${name} must be an integer`)),
    });
  }
  if (property.type === 'object' || property.type === 'array') {
    rules.push({
      validator: (_, value) => {
        if ((value === undefined || value === '') && !required) return Promise.resolve();
        try {
          parseStructuredValue(value, property.type as 'object' | 'array');
          return Promise.resolve();
        } catch (error) {
          return Promise.reject(error instanceof Error ? error : new Error('Enter valid JSON'));
        }
      },
    });
  }
  return rules;
}

function renderInput(name: string, property: JSONSchema) {
  if (property.enum) {
    return (
      <Select
        placeholder={property.description || `Select ${name}`}
        options={property.enum.map((value) => ({ value, label: typeof value === 'string' ? value : JSON.stringify(value) }))}
      />
    );
  }
  switch (property.type) {
    case 'number':
    case 'integer':
      return <InputNumber min={property.minimum} max={property.maximum} style={{ width: '100%' }} />;
    case 'boolean':
      return <Select options={[{ value: true, label: 'True' }, { value: false, label: 'False' }]} />;
    case 'object':
    case 'array':
      return <TextArea rows={4} placeholder={property.type === 'array' ? '[...]' : '{...}'} />;
    default:
      return <Input placeholder={property.description} />;
  }
}

export default function SchemaInputForm({ schema, name = 'variables' }: SchemaInputFormProps) {
  const properties = Object.entries(schema?.properties ?? {});
  const required = new Set(schema?.required ?? []);

  if (properties.length === 0) {
    return <Alert type="info" showIcon message="This rule version does not declare task inputs." />;
  }

  return (
    <>
      {properties.map(([propertyName, property]) => {
        const label = property.title || propertyName;
        if (property['x-secret']) {
          return (
            <Form.Item key={propertyName} label={`${label}${required.has(propertyName) ? ' (required)' : ''}`}>
              <Alert
                type={required.has(propertyName) ? 'warning' : 'info'}
                showIcon
                message="Secret input"
                description="Secret values cannot be supplied as task variables. Use an approved secret reference."
              />
            </Form.Item>
          );
        }
        return (
          <Form.Item
            key={propertyName}
            name={[name, propertyName]}
            label={label}
            extra={property.description ? <Typography.Text type="secondary">{property.description}</Typography.Text> : undefined}
            rules={validationRules(propertyName, property, required.has(propertyName))}
            valuePropName="value"
          >
            {renderInput(propertyName, property)}
          </Form.Item>
        );
      })}
    </>
  );
}
