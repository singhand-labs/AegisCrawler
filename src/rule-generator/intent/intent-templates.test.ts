import { describe, it, expect } from 'vitest';
import {
  classifyIntent,
  listCollectionTemplate,
  searchPaginationTemplate,
  formSubmitTemplate,
  templates,
} from './intent-templates';
import type { Rule, Action } from '../types';
import type { ExtractAction, TypeAction } from '../../rule-engine/types/action';

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

const candidate = (label: string) => ({
  id: 'c1',
  label,
  description: '',
  confidence: 0.9,
});

describe('classifyIntent', () => {
  it('classifies list collection', () => {
    expect(classifyIntent(candidate('采集商品列表'))).toBe('list-collection');
    expect(classifyIntent(candidate('List Collection'))).toBe('custom');
  });

  it('classifies search pagination', () => {
    expect(classifyIntent(candidate('搜索并翻页'))).toBe('search-pagination');
    expect(classifyIntent(candidate('keyword search'))).toBe('search-pagination');
  });

  it('classifies form submit', () => {
    expect(classifyIntent(candidate('提交表单'))).toBe('form-submit');
    expect(classifyIntent(candidate('form submit'))).toBe('form-submit');
  });

  it('classifies custom for unknown labels', () => {
    expect(classifyIntent(candidate('其他目的'))).toBe('custom');
    expect(classifyIntent(candidate(''))).toBe('custom');
  });
});

describe('templates array', () => {
  it('contains all three built-in templates', () => {
    expect(templates.map((t) => t.type)).toEqual([
      'list-collection',
      'search-pagination',
      'form-submit',
    ]);
  });
});

describe('listCollectionTemplate', () => {
  it('adds extract step after last click', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'click', target: { selector: '.item' } },
    ]);
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    expect(result.steps).toHaveLength(3);
    expect(result.steps[1].action).toBe('click');
    expect(result.steps[2].action).toBe('extract');
    const extractStep = result.steps[2] as ExtractAction;
    expect(extractStep.name).toBe('itemData');
    expect(extractStep.target.selector).toBe('.item');
    expect(extractStep.multiple).toBe(true);
    expect(extractStep.fields).toHaveProperty('title');
    expect(extractStep.fields).toHaveProperty('price');
    expect(extractStep.fields).toHaveProperty('url');
    expect(extractStep.fields.url.attr).toBe('href');
  });

  it('does not mutate the original rule', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '.item' } },
    ]);
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    expect(rule.steps).toHaveLength(1);
    expect(result.steps).toHaveLength(2);
  });

  it('adds maxItems variable', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '.item' } },
    ]);
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    expect(result.variables).toEqual({ maxItems: 10 });
  });

  it('preserves existing variables', () => {
    const rule = makeRule(
      [{ action: 'click', target: { selector: '.item' } }],
      { existing: 'value' },
    );
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    expect(result.variables).toEqual({ existing: 'value', maxItems: 10 });
  });

  it('falls back to body target when click step lacks target', () => {
    const rule = makeRule([
      { action: 'click' } as Action,
    ]);
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    expect(result.steps).toHaveLength(2);
    const extractStep = result.steps[1] as ExtractAction;
    expect(extractStep.target.selector).toBe('body');
  });

  it('does nothing when there is no click step', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
    ]);
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    expect(result.steps).toHaveLength(1);
    expect(result.variables).toEqual({ maxItems: 10 });
  });

  it('uses the last click step', () => {
    const rule = makeRule([
      { action: 'click', target: { selector: '.first' } },
      { action: 'click', target: { selector: '.last' } },
    ]);
    const result = listCollectionTemplate.apply(rule, candidate('采集商品'));
    const extractStep = result.steps[2] as ExtractAction;
    expect(extractStep.target.selector).toBe('.last');
  });
});

describe('searchPaginationTemplate', () => {
  it('templatizes type action values', () => {
    const rule = makeRule([
      { action: 'type', target: { selector: '#search' }, value: 'phones' },
      { action: 'click', target: { selector: '#next' } },
    ]);
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect(result.variables).toEqual({ keyword: 'phones', pages: 1 });
    expect((result.steps[0] as TypeAction).value).toBe('{{keyword}}');
    expect((result.steps[1] as TypeAction).value).toBeUndefined();
  });

  it('skips empty type values', () => {
    const rule = makeRule([
      { action: 'type', target: { selector: '#search' }, value: '' },
    ]);
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect((result.steps[0] as TypeAction).value).toBe('');
  });

  it('does not mutate the original rule', () => {
    const rule = makeRule([
      { action: 'type', target: { selector: '#search' }, value: 'phones' },
    ]);
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect((rule.steps[0] as TypeAction).value).toBe('phones');
    expect((result.steps[0] as TypeAction).value).toBe('{{keyword}}');
  });

  it('adds wait and extract steps after submit-like action', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'type', target: { selector: '#search' }, value: 'phones', submit: true },
    ]);
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect(result.steps).toHaveLength(6);
    expect(result.steps[1].action).toBe('type');
    expect(result.steps[2].action).toBe('waitForTimeout');
    expect(result.steps[3].action).toBe('extract');
    expect(result.steps[4].action).toBe('sendResult');
    expect(result.steps[5].action).toBe('updateStatus');
    const extractStep = result.steps[3] as ExtractAction;
    expect(extractStep.name).toBe('searchResults');
    expect(extractStep.multiple).toBe(true);
    expect(extractStep.fields).toHaveProperty('title');
    expect(extractStep.fields).toHaveProperty('url');
    expect(extractStep.fields).toHaveProperty('summary');
  });

  it('adds extract step even when no submit-like action exists', () => {
    const rule = makeRule([{ action: 'navigate', url: 'https://example.com' }]);
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect(result.steps).toHaveLength(4);
    expect(result.steps[1].action).toBe('extract');
    expect(result.steps[2].action).toBe('sendResult');
    expect(result.steps[3].action).toBe('updateStatus');
  });

  it('preserves existing keyword variable and skips re-capturing', () => {
    const rule = makeRule(
      [{ action: 'type', target: { selector: '#search' }, value: 'phones' }],
      { keyword: 'existing' },
    );
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect(result.variables?.keyword).toBe('existing');
  });

  it('places extraction after navigate that follows submit', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'type', target: { selector: '#search' }, value: 'phones', submit: true },
      { action: 'navigate', url: 'https://example.com/results' },
    ]);
    const result = searchPaginationTemplate.apply(rule, candidate('搜索并翻页'));
    expect(result.steps[2].action).toBe('navigate');
    expect(result.steps[3].action).toBe('waitForTimeout');
    expect(result.steps[4].action).toBe('extract');
  });
});

describe('formSubmitTemplate', () => {
  it('adds formData variable', () => {
    const rule = makeRule([
      { action: 'type', target: { selector: '#name' }, value: 'John' },
    ]);
    const result = formSubmitTemplate.apply(rule, candidate('提交表单'));
    expect(result.variables).toEqual({ formData: {} });
    expect(result.steps).toHaveLength(1);
  });

  it('preserves existing variables', () => {
    const rule = makeRule(
      [{ action: 'click', target: { selector: '#submit' } }],
      { existing: 'value' },
    );
    const result = formSubmitTemplate.apply(rule, candidate('提交表单'));
    expect(result.variables).toEqual({ existing: 'value', formData: {} });
  });
});
