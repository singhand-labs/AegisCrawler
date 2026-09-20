import { describe, it, expect } from 'vitest';
import { enhanceWithIntent } from './enhance';
import type { Rule, Action } from '../types';
import type { TypeAction } from '../../rule-engine/types/action';

function makeRule(steps: Action[] = [], variables: Record<string, any> = {}): Rule {
  return {
    id: 'r1',
    version: '1.0.0',
    name: 'test',
    domain: 'example.com',
    enabled: true,
    entry: 'https://example.com',
    variables,
    selectors: {},
    steps,
  };
}

describe('enhanceWithIntent', () => {
  it('returns enhanced rule for list collection', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '.item' } },
    ]);
    const result = enhanceWithIntent(rule, {
      id: 'c1',
      label: '采集商品列表',
      description: '',
      confidence: 0.9,
    });
    expect(result.steps).toHaveLength(2);
    expect(result.steps[1].action).toBe('extract');
    expect(result.variables).toBeDefined();
    expect(result.variables!.maxItems).toBe(10);
  });

  it('returns enhanced rule for search pagination', () => {
    const rule = makeRule([
      { action: 'type', target: { selector: '#search' }, value: 'phones' },
    ]);
    const result = enhanceWithIntent(rule, {
      id: 'c2',
      label: '搜索并翻页',
      description: '',
      confidence: 0.9,
    });
    expect((result.steps[0] as TypeAction).value).toBe('{{keyword}}');
    expect(result.variables).toBeDefined();
    expect(result.variables!.keyword).toBe('phones');
    expect(result.variables!.pages).toBe(1);
  });

  it('returns enhanced rule for form submit', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '#submit' } },
    ]);
    const result = enhanceWithIntent(rule, {
      id: 'c3',
      label: '提交表单',
      description: '',
      confidence: 0.9,
    });
    expect(result.variables).toBeDefined();
    expect(result.variables!.formData).toEqual({});
    expect(result.steps).toHaveLength(1);
  });

  it('returns original rule for custom intent', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '.item' } },
    ]);
    const result = enhanceWithIntent(rule, {
      id: 'custom',
      label: '其他目的',
      description: '',
      confidence: 0,
    });
    expect(result).toEqual(rule);
    expect(result.steps).toHaveLength(1);
  });

  it('does not mutate the original rule', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '.item' } },
    ]);
    const result = enhanceWithIntent(rule, {
      id: 'c1',
      label: '采集商品列表',
      description: '',
      confidence: 0.9,
    });
    expect(rule.steps).toHaveLength(1);
    expect(result.steps).toHaveLength(2);
  });
});
