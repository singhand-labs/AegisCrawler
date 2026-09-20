import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Form } from 'antd';
import SchemaInputForm, {
  normalizeSchemaInputs, requiredSecretInputs, schemaFormDefaults,
} from './SchemaInputForm';
import type { JSONSchema } from '../api/types';

const schema: JSONSchema = {
  type: 'object',
  required: ['query', 'credential'],
  properties: {
    query: { type: 'string', description: 'Search phrase', minLength: 2, default: 'books' },
    limit: { type: 'integer', minimum: 1, maximum: 100, default: 10 },
    enabled: { type: 'boolean', default: true },
    filters: { type: 'object', default: { category: 'new' } },
    credential: { type: 'string', 'x-secret': true },
  },
};

describe('SchemaInputForm', () => {
  it('renders typed inputs and explains secret handling', () => {
    render(<Form><SchemaInputForm schema={schema} /></Form>);

    expect(screen.getByLabelText('query')).toBeInTheDocument();
    expect(screen.getByLabelText('limit')).toHaveAttribute('role', 'spinbutton');
    expect(screen.getByRole('combobox', { name: 'enabled' })).toBeInTheDocument();
    expect(screen.getByText('Secret values cannot be supplied as task variables. Use an approved secret reference.')).toBeInTheDocument();
  });

  it('builds form defaults without exposing secrets', () => {
    expect(schemaFormDefaults(schema)).toEqual({
      query: 'books', limit: 10, enabled: true, filters: '{\n  "category": "new"\n}',
    });
  });

  it('normalizes structured values and omits secret fields', () => {
    expect(normalizeSchemaInputs(schema, {
      query: 'music', limit: 5, enabled: false, filters: '{"category":"used"}', credential: 'do-not-send',
    })).toEqual({ query: 'music', limit: 5, enabled: false, filters: { category: 'used' } });
    expect(requiredSecretInputs(schema)).toEqual(['credential']);
  });
});
