import { describe, it, expect } from 'vitest';
import { validateRule } from './ruleValidate';
import type { Rule, Action } from '../types/rule';

function makeRule(overrides: Partial<Rule> = {}): Rule {
  return {
    id: 'rule-1',
    version: '1.0.0',
    name: '测试规则',
    domain: 'example.com',
    urlPattern: undefined,
    enabled: true,
    priority: 'normal',
    entry: 'https://example.com',
    variables: {},
    selectors: {},
    humanize: {},
    steps: [
      { action: 'navigate', url: 'https://example.com/list' },
      { action: 'click', target: { selector: '#btn' } },
      { action: 'type', target: { selector: '#input' }, value: 'hello' },
    ],
    output: {},
    sendPolicy: {},
    hooks: {},
    tags: {},
    owner: 'alice',
    approvalStatus: 'pending',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    ...overrides,
  };
}

describe('validateRule', () => {
  it('passes for a valid rule', () => {
    const result = validateRule(makeRule());
    expect(result.valid).toBe(true);
    expect(result.errors).toEqual([]);
  });

  it('fails when name, entry or steps are missing/invalid', () => {
    const result = validateRule(
      makeRule({ name: '', entry: 'ftp://example.com', steps: [] }),
    );
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('规则名称不能为空');
    expect(result.errors).toContain('入口 URL 不合法或必须以 http(s):// 开头');
    expect(result.errors).toContain('steps 不能为空');
  });

  it('fails when an action is missing the action field', () => {
    const steps = [{ target: { selector: '#x' } }] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0] 缺少动作类型');
  });

  it('fails when a target-required action lacks a target', () => {
    const steps = [
      { action: 'click' },
      { action: 'extractText' },
      { action: 'waitFor' },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0] 缺少目标元素');
    expect(result.errors).toContain('steps[1] 缺少目标元素');
    expect(result.errors).toContain('steps[2] 缺少目标元素');
  });

  it('fails when a container action has empty child steps', () => {
    const steps = [
      { action: 'group', steps: [] },
      { action: 'if', condition: { type: 'elementExists' }, then: [], else: [] },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0] 子步骤数组必须存在且非空');
    expect(result.errors).toContain('steps[1].then 子步骤数组必须存在且非空');
    expect(result.errors).toContain('steps[1].else 子步骤数组必须存在且非空');
  });

  it('fails when if action lacks condition', () => {
    const steps = [
      { action: 'if', then: [{ action: 'navigate', url: 'https://a.com' }] },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0] 缺少 condition');
  });

  it('fails when loop count type lacks a positive count', () => {
    const steps = [
      { action: 'loop', type: 'count', steps: [{ action: 'click', target: { selector: '#x' } }] },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0].count 必须为正整数');
  });

  it('reports nested path errors correctly', () => {
    const steps = [
      {
        action: 'if',
        condition: { type: 'elementExists' },
        then: [
          {
            action: 'loop',
            type: 'count',
            count: 2,
            steps: [
              {
                action: 'group',
                steps: [{ action: 'type', target: { selector: '#i' }, value: '' }],
              },
            ],
          },
        ],
        else: [],
      },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0].then[0].steps[0].steps[0] 缺少输入值');
    expect(result.errors).toContain('steps[0].else 子步骤数组必须存在且非空');
  });

  it('fails when value-required action lacks a non-empty value', () => {
    const steps = [
      { action: 'type', target: { selector: '#i' }, value: '' },
      { action: 'paste', target: { selector: '#p' } },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0] 缺少输入值');
    expect(result.errors).toContain('steps[1] 缺少输入值');
  });

  it('fails when url-required action has an invalid url', () => {
    const steps = [
      { action: 'navigate', url: '/relative' },
      { action: 'openTab', url: '' },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0] URL 不合法或必须以 http(s):// 开头');
    expect(result.errors).toContain('steps[1] URL 不合法或必须以 http(s):// 开头');
  });

  it('fails when ms is missing or negative for waitForTimeout/sleep', () => {
    const steps = [
      { action: 'waitForTimeout' },
      { action: 'sleep', ms: -1 },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0].ms 必须为非负数');
    expect(result.errors).toContain('steps[1].ms 必须为非负数');
  });

  it('fails when timeout is negative', () => {
    const steps = [{ action: 'click', target: { selector: '#x' }, timeout: -5 }] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0].timeout 必须为非负数');
  });

  it('fails when priority is invalid', () => {
    const result = validateRule(makeRule({ priority: 'urgent' as Rule['priority'] }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('priority 必须是 low/normal/high 之一');
  });

  it('accepts empty priority to support legacy rules', () => {
    const result = validateRule(makeRule({ priority: '' as Rule['priority'] }));
    expect(result.valid).toBe(true);
  });

  it('validates actions inside hooks', () => {
    const rule = makeRule({
      hooks: {
        beforeAll: [{ action: 'click' }] as Action[],
      },
    });
    const result = validateRule(rule);
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('hooks.beforeAll[0] 缺少目标元素');
  });

  it('validates switch cases and default', () => {
    const steps = [
      {
        action: 'switch',
        cases: [
          { value: 'a', steps: [{ action: 'click', target: { selector: '#a' } }] },
          { value: undefined as unknown as string, steps: [{ action: 'click', target: { selector: '#b' } }] },
          { value: 'c', steps: [] },
        ],
        default: [{ action: 'navigate', url: 'not-a-url' }],
      },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps[0].cases[1] 缺少 value');
    expect(result.errors).toContain('steps[0].cases[2].steps 必须存在且非空');
    expect(result.errors).toContain('steps[0].default[0] URL 不合法或必须以 http(s):// 开头');
  });

  it('fails when steps is not an array', () => {
    const result = validateRule(makeRule({ steps: 'oops' as unknown as Action[] }));
    expect(result.valid).toBe(false);
    expect(result.errors).toContain('steps 必须是数组');
  });

  it('accepts a target defined by any supported locator', () => {
    const steps = [
      { action: 'click', target: { xpath: '//button' } },
      { action: 'click', target: { text: 'OK' } },
      { action: 'click', target: { ariaLabel: 'submit' } },
      { action: 'click', target: { role: 'button' } },
      { action: 'click', target: { position: { x: 1, y: 2 } } },
      { action: 'click', target: { $ref: 'ref1' } },
    ] as Action[];
    const result = validateRule(makeRule({ steps }));
    expect(result.valid).toBe(true);
  });
});
