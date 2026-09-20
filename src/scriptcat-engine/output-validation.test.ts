import { describe, it, expect, vi } from 'vitest';
import { validateOutput } from './output-validation';

function makeEnv() {
  return {
    env: {
      transport: {
        sendLog: vi.fn(async () => {}),
      },
    } as any,
  };
}

describe('validateOutput helper', () => {
  const schema = {
    type: 'object',
    required: ['products'],
    properties: {
      products: { type: 'array' },
    },
  };

  it('returns ok=true when data matches schema', async () => {
    const { env } = makeEnv();
    const result = await validateOutput({ products: [] }, schema, 'fail', env);
    expect(result.ok).toBe(true);
  });

  it('returns ok=false with errors when data fails + onInvalid=fail', async () => {
    const { env } = makeEnv();
    const result = await validateOutput({}, schema, 'fail', env);
    expect(result.ok).toBe(false);
    expect(result.errors).toMatch(/products/);
    expect(env.transport.sendLog).not.toHaveBeenCalled();
  });

  it('returns ok=true + logs warn when data fails + onInvalid=warn', async () => {
    const { env } = makeEnv();
    const result = await validateOutput({}, schema, 'warn', env);
    expect(result.ok).toBe(true);
    expect(result.errors).toMatch(/products/);
    expect(env.transport.sendLog).toHaveBeenCalledWith(
      'warn',
      'output schema validation failed',
      expect.objectContaining({ errors: expect.stringMatching(/products/) }),
    );
  });

  it('compiles server-style contracts declaring draft 2020-12', async () => {
    const { env } = makeEnv();
    // Mirrors the output contract emitted by server/internal/rule/contracts.go.
    const serverSchema = {
      $schema: 'https://json-schema.org/draft/2020-12/schema',
      type: 'object',
      required: ['products'],
      additionalProperties: false,
      properties: { products: { type: 'array' } },
    };
    const ok = await validateOutput({ products: [] }, serverSchema, 'fail', env);
    expect(ok.ok).toBe(true);
    const bad = await validateOutput({ extra: true }, serverSchema, 'fail', env);
    expect(bad.ok).toBe(false);
  });

  it('validates arrays of row objects against nested contracts', async () => {
    const { env } = makeEnv();
    // Shape of one collected row batch under the server replay contract.
    const rowSchema = {
      $schema: 'https://json-schema.org/draft/2020-12/schema',
      type: 'object',
      required: ['products'],
      additionalProperties: false,
      properties: {
        products: {
          type: 'array',
          minItems: 1,
          items: {
            type: 'object',
            required: ['name', 'price'],
            additionalProperties: false,
            properties: {
              name: { type: 'string', minLength: 1 },
              price: { type: 'string', pattern: '^\\$[\\d,]+(\\.\\d{2})?$' },
              category: { type: 'string', enum: ['Phones', 'Laptops'] },
            },
          },
        },
      },
    };
    const good = await validateOutput(
      { products: [{ name: 'iPhone 15 Pro', price: '$999.00', category: 'Phones' }] },
      rowSchema, 'fail', env,
    );
    expect(good.ok).toBe(true);
    const badPrice = await validateOutput(
      { products: [{ name: 'iPhone 15 Pro', price: '999 USD' }] },
      rowSchema, 'fail', env,
    );
    expect(badPrice.ok).toBe(false);
    expect(badPrice.errors).toMatch(/products\/0\/price/);
    const badEnum = await validateOutput(
      { products: [{ name: 'Pixel 8', price: '$699.00', category: 'Tablets' }] },
      rowSchema, 'fail', env,
    );
    expect(badEnum.ok).toBe(false);
    expect(badEnum.errors).toMatch(/category/);
  });

  it('supports type unions and anyOf combinators', async () => {
    const { env } = makeEnv();
    const unionSchema = { type: 'object', properties: { v: { type: ['string', 'null'] } } };
    expect((await validateOutput({ v: null }, unionSchema, 'fail', env)).ok).toBe(true);
    expect((await validateOutput({ v: 3 }, unionSchema, 'fail', env)).ok).toBe(false);
    const anyOfSchema = { type: 'object', properties: { v: { anyOf: [{ type: 'string' }, { type: 'integer' }] } } };
    expect((await validateOutput({ v: 2 }, anyOfSchema, 'fail', env)).ok).toBe(true);
    expect((await validateOutput({ v: 2.5 }, anyOfSchema, 'fail', env)).ok).toBe(false);
  });

  it('rejects unsupported keywords loudly instead of skipping validation', async () => {
    const { env } = makeEnv();
    await expect(validateOutput({}, { $ref: '#/definitions/row' }, 'fail', env)).rejects.toThrow(/not supported/);
    await expect(validateOutput({ a: 1 }, { properties: { a: { contains: {} } } }, 'fail', env)).rejects.toThrow(/not supported/);
  });
});

import { runRule } from './executor';

function makeRunEnv() {
  const sent: any[] = [];
  return {
    env: {
      signal: undefined,
      getUrl: () => 'https://example.com/',
      getTitle: () => 'T',
      sleep: vi.fn(async () => {}),
      evaluate: vi.fn(async () => undefined),
      findElement: vi.fn(async () => null),
      findElements: vi.fn(async () => []),
      now: () => 0,
      screenshot: vi.fn(async () => null),
      saveSnapshot: vi.fn(async () => null),
      transport: {
        fetchRule: vi.fn(async () => null),
        sendResult: vi.fn(async (p: any) => { sent.push(p); }),
        sendLog: vi.fn(async (level: string, msg: string) => { sent.push({ __log: true, level, msg }); }),
        sendStatus: vi.fn(async () => {}),
        sendHeartbeat: vi.fn(async () => ({ cancelRequested: false })),
        sendSnapshot: vi.fn(async () => {}),
      },
    } as any,
    sent,
  };
}

describe('runRule rule.output integration', () => {
  const schema = { type: 'object', required: ['products'], properties: { products: { type: 'array' } } };

  it('passes when extracted matches schema', async () => {
    const { env } = makeRunEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      output: schema,
    };
    const result = await runRule({
      rule, taskId: 't', workerId: 'w', env,
      initialContext: { extracted: { products: [{ name: 'x' }] } },
    });
    expect(result.status).toBe('success');
    expect(result.error).toBeUndefined();
  });

  it('fails with ValidationError by default when schema mismatches', async () => {
    const { env } = makeRunEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      output: schema,
    };
    const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(result.status).toBe('failure');
    expect(result.error?.type).toBe('ValidationError');
    expect(result.error?.message).toMatch(/products/);
  });

  it('warns and continues when outputOnInvalid=warn', async () => {
    const { env, sent } = makeRunEnv();
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      output: schema,
      outputOnInvalid: 'warn',
    };
    const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(result.status).toBe('success');
    expect(sent.find((s) => s?.__log && s.level === 'warn')).toBeDefined();
  });

  it('no rule.output → no validation', async () => {
    const { env } = makeRunEnv();
    const rule: any = { id: 'r', version: '1', steps: [] };
    const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(result.status).toBe('success');
    expect(result.error).toBeUndefined();
  });

  it('malformed schema routes through ConfigError', async () => {
    const { env } = makeRunEnv();
    // Ajv.compile throws on a non-object schema. If this schema happens to
    // compile, swap for `'not-a-schema'` (string) which definitely throws.
    const rule: any = {
      id: 'r', version: '1',
      steps: [],
      output: 'not-a-schema' as any,
    };
    const result = await runRule({ rule, taskId: 't', workerId: 'w', env });
    expect(result.status).toBe('failure');
    expect(result.error?.type).toBe('ConfigError');
  });
});
