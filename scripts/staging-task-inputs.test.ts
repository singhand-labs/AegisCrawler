import { describe, expect, it } from 'vitest';
import { buildTaskInputs, STAGE10_RESULT_TEXT } from './staging-task-inputs';

describe('buildTaskInputs', () => {
  it('returns no inputs when the schema is missing or declares none', () => {
    expect(buildTaskInputs(undefined)).toEqual({});
    expect(buildTaskInputs({})).toEqual({});
    expect(buildTaskInputs({ type: 'object', properties: {}, required: [] })).toEqual({});
  });

  it('omits optional inputs and required inputs with declared defaults', () => {
    const schema = {
      type: 'object',
      properties: {
        optionalNote: { type: 'string' },
        expectedText: { type: 'string', default: STAGE10_RESULT_TEXT },
      },
      required: ['expectedText'],
      additionalProperties: false,
    };
    expect(buildTaskInputs(schema)).toEqual({});
  });

  it('synthesizes fixture-aware values for required strings', () => {
    const schema = {
      type: 'object',
      properties: {
        actionId: { type: 'string' },
        buttonSelector: { type: 'string' },
        expectedText: { type: 'string' },
        custom: { type: 'string' },
      },
      required: ['actionId', 'buttonSelector', 'expectedText', 'custom'],
      additionalProperties: false,
    };
    expect(buildTaskInputs(schema)).toEqual({
      actionId: 'semantic-action',
      buttonSelector: '#semantic-action',
      expectedText: STAGE10_RESULT_TEXT,
      custom: 'stage10-custom',
    });
  });

  it('honours enums, numeric bounds, and string minimum length', () => {
    const schema = {
      type: 'object',
      properties: {
        mode: { type: 'string', enum: ['fast', 'slow'] },
        count: { type: 'integer', minimum: 3 },
        capped: { type: 'number', maximum: 0.5 },
        plain: { type: 'number' },
        flag: { type: 'boolean' },
        code: { type: 'string', minLength: 30 },
      },
      required: ['mode', 'count', 'capped', 'plain', 'flag', 'code'],
    };
    const inputs = buildTaskInputs(schema);
    expect(inputs.mode).toBe('fast');
    expect(inputs.count).toBe(3);
    expect(inputs.capped).toBe(0.5);
    expect(inputs.plain).toBe(1);
    expect(inputs.flag).toBe(true);
    expect(typeof inputs.code).toBe('string');
    expect((inputs.code as string).length).toBeGreaterThanOrEqual(30);
  });

  it('fills arrays to minItems and builds nested required objects', () => {
    const schema = {
      type: 'object',
      properties: {
        tags: { type: 'array', minItems: 2, items: { type: 'string' } },
        nested: {
          type: 'object',
          properties: {
            needed: { type: 'integer' },
            skipped: { type: 'string' },
          },
          required: ['needed'],
        },
      },
      required: ['tags', 'nested'],
    };
    expect(buildTaskInputs(schema)).toEqual({
      tags: ['stage10-tagsItem', 'stage10-tagsItem'],
      nested: { needed: 1 },
    });
  });

  it('rejects required secret inputs', () => {
    const schema = {
      type: 'object',
      properties: { password: { type: 'string', 'x-secret': true } },
      required: ['password'],
    };
    expect(() => buildTaskInputs(schema)).toThrow('required secret input "password"');
  });
});
