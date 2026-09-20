/// <reference types="vitest/globals" />
import { preflightRule } from './rule-preflight';
import type { Rule } from './types';

function baseRule(steps: any[], hooks?: any): Rule {
  const r: Rule = {
    id: 'test',
    version: '1.0.0',
    name: 'Test',
    domain: 'example.com',
    enabled: true,
    steps,
  } as Rule;
  if (hooks) (r as any).hooks = hooks;
  return r;
}

describe('rule-preflight - Phase 3 relaxation (navigation inside supported containers)', () => {
  it('allows navigate inside if.then', () => {
    const r = baseRule([
      { action: 'if', condition: { type: 'elementExists', target: { selector: '#x' } }, then: [{ action: 'navigate', url: '/next' }] },
    ]);
    const errors = preflightRule(r);
    expect(errors).toEqual([]);
  });

  it('allows navigate inside if.else', () => {
    const r = baseRule([
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '#x' } },
        then: [{ action: 'extract', name: 'a', target: { selector: 'h1' }, fields: {} }],
        else: [{ action: 'navigate', url: '/alt' }],
      },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('allows click inside loop.fixedCount', () => {
    const r = baseRule([
      { action: 'loop', type: 'fixedCount', count: 3, steps: [{ action: 'click', target: { selector: '#next' } }] },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('allows click inside loop.forEach', () => {
    const r = baseRule([
      { action: 'loop', type: 'forEach', items: '{{items}}', as: 'item', steps: [{ action: 'click', target: { selector: '#next' } }] },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('allows type+submit inside switch.cases', () => {
    const r = baseRule([
      {
        action: 'switch',
        expression: 'a',
        cases: [{ value: 'a', steps: [{ action: 'type', target: { selector: '#q' }, value: 'x', submit: true }] }],
      },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('allows navigate inside switch.default', () => {
    const r = baseRule([
      {
        action: 'switch',
        expression: 'a',
        cases: [],
        default: [{ action: 'navigate', url: '/fallback' }],
      },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('allows navigate inside group.steps', () => {
    const r = baseRule([{ action: 'group', steps: [{ action: 'navigate', url: '/p' }] }]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('still flags static cross-origin navigate inside an allowed container', () => {
    const r = baseRule([
      { action: 'if', condition: { type: 'elementExists', target: { selector: '#x' } }, then: [{ action: 'navigate', url: 'https://other.com/next' }] },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'static_cross_origin_navigate')).toBe(true);
  });

  it('allows deeply nested navigation inside loop→if→switch', () => {
    const r = baseRule([
      {
        action: 'loop',
        type: 'fixedCount',
        count: 2,
        steps: [
          {
            action: 'if',
            condition: { type: 'elementExists', target: { selector: '#x' } },
            then: [
              {
                action: 'switch',
                expression: 'a',
                cases: [{ value: 'a', steps: [{ action: 'click', target: { selector: '#deep' } }] }],
              },
            ],
          },
        ],
      },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('allows non-navigating actions inside flow-control', () => {
    const r = baseRule([
      { action: 'loop', type: 'fixedCount', count: 2, steps: [{ action: 'extract', name: 'x', target: { selector: '.c' }, fields: {} }] },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });
});

describe('rule-preflight - retained rejections', () => {
  it.each(['goBack', 'goForward'] as const)('rejects %s because its destination is not prevalidatable', (action) => {
    const errors = preflightRule(baseRule([{ action }]));
    expect(errors).toContainEqual(expect.objectContaining({
      code: 'history_navigation_unsupported',
      path: 'steps[0]',
    }));
  });

  it('rejects navigation inside loop.whileElementExists', () => {
    const r = baseRule([
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '.item' },
        steps: [{ action: 'click', target: { selector: '#next' } }],
      },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'while_element_exists_navigation_unsupported')).toBe(true);
  });

  it('rejects navigation nested inside loop.whileElementExists (in an inner if)', () => {
    const r = baseRule([
      {
        action: 'loop',
        type: 'whileElementExists',
        target: { selector: '.item' },
        steps: [
          {
            action: 'if',
            condition: { type: 'elementExists', target: { selector: '#x' } },
            then: [{ action: 'click', target: { selector: '#deep' } }],
          },
        ],
      },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'while_element_exists_navigation_unsupported')).toBe(true);
  });

  it('rejects navigation inside retry.steps', () => {
    const r = baseRule([
      { action: 'retry', config: { maxAttempts: 2 }, steps: [{ action: 'reload' }] },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'retry_navigation_unsupported')).toBe(true);
  });

  it('still allows top-level navigate', () => {
    const r = baseRule([{ action: 'navigate', url: '/next' }]);
    expect(preflightRule(r).filter((e) => e.code.startsWith('while_') || e.code === 'retry_navigation_unsupported' || e.code === 'hook_navigation')).toEqual([]);
  });
});

describe('rule-preflight - hook_navigation', () => {
  it('flags navigation at any depth in hooks', () => {
    const r = baseRule(
      [{ action: 'extract', name: 'x', target: { selector: 'h1' }, fields: {} }],
      { beforeAll: [{ action: 'navigate', url: '/setup' }] },
    );
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'hook_navigation' && e.path.startsWith('hooks.beforeAll'))).toBe(true);
  });

  it('flags navigation nested in a flow-control inside a hook (hook rule beats Phase 3 relaxation)', () => {
    const r = baseRule(
      [{ action: 'extract', name: 'x', target: { selector: 'h1' }, fields: {} }],
      {
        onError: [
          { action: 'if', condition: { type: 'elementExists', target: { selector: '#x' } }, then: [{ action: 'click', target: { selector: '#retry' } }] },
        ],
      },
    );
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'hook_navigation')).toBe(true);
  });
});

describe('rule-preflight - evaluate_disabled (jsTruthy condition)', () => {
  it('flags jsTruthy condition', () => {
    const r = baseRule([
      { action: 'click', target: { selector: '#x' }, condition: { type: 'jsTruthy', script: 'true' } },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'evaluate_disabled' && e.path.endsWith('.condition'))).toBe(true);
  });
});

describe('rule-preflight - evaluate_recorded_untrusted (action path)', () => {
  it('evaluate without trusted yields structured evaluate_recorded_untrusted error', () => {
    const r = baseRule([{ action: 'evaluate', script: 'return 1', name: 'ok' }]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'evaluate_recorded_untrusted')).toBe(true);
  });

  it('waitForFunction without trusted yields evaluate_recorded_untrusted', () => {
    const r = baseRule([{ action: 'waitForFunction', fn: '() => true' }]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'evaluate_recorded_untrusted')).toBe(true);
  });

  it('evaluate with trusted=true passes preflight', () => {
    const r = baseRule([{ action: 'evaluate', script: 'return 1', trusted: true }]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('waitForFunction with trusted=true passes preflight', () => {
    const r = baseRule([{ action: 'waitForFunction', script: 'return true', trusted: true }]);
    expect(preflightRule(r)).toEqual([]);
  });

  it('error message tells author to set trusted: true after reviewing', () => {
    const r = baseRule([{ action: 'evaluate', script: 'return 1' }]);
    const err = preflightRule(r).find((e) => e.code === 'evaluate_recorded_untrusted')!;
    expect(err).toBeDefined();
    expect(err.message).toMatch(/set `trusted: true`/);
    expect(err.message).toMatch(/after reviewing the script/);
  });

  it('error includes scriptPreview (first 200 chars)', () => {
    const longScript = 'return ' + 'x'.repeat(300);
    const r = baseRule([{ action: 'evaluate', script: longScript }]);
    const err = preflightRule(r).find((e) => e.code === 'evaluate_recorded_untrusted')!;
    expect(err).toBeDefined();
    expect(err.scriptPreview).toBeDefined();
    expect(err.scriptPreview!.length).toBeLessThanOrEqual(200);
    expect(err.scriptPreview).toBe(longScript.slice(0, 200));
  });

  it('scriptPreview undefined when script missing', () => {
    const r = baseRule([{ action: 'evaluate' }]);
    const err = preflightRule(r).find((e) => e.code === 'evaluate_recorded_untrusted')!;
    expect(err).toBeDefined();
    expect(err.scriptPreview).toBeUndefined();
  });

  it('flags evaluate inside flow-control block', () => {
    const r = baseRule([
      { action: 'loop', type: 'fixedCount', count: 2, steps: [{ action: 'evaluate', script: 'return 1' }] },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'evaluate_recorded_untrusted')).toBe(true);
  });

  it('trusted evaluate inside flow-control passes preflight (no evaluate_recorded_untrusted)', () => {
    const r = baseRule([
      { action: 'loop', type: 'fixedCount', count: 2, steps: [{ action: 'evaluate', script: 'return 1', trusted: true }] },
    ]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'evaluate_recorded_untrusted')).toBe(false);
  });
});

describe('rule-preflight - static_cross_origin_navigate', () => {
  it('flags literal cross-origin navigate URL', () => {
    const r = baseRule([{ action: 'navigate', url: 'https://other.com/page' }]);
    const errors = preflightRule(r);
    expect(errors.some((e) => e.code === 'static_cross_origin_navigate')).toBe(true);
  });

  it('allows same-origin literal navigate URL', () => {
    const r = baseRule([{ action: 'navigate', url: 'https://example.com/page' }]);
    const errors = preflightRule(r);
    expect(errors.filter((e) => e.code === 'static_cross_origin_navigate')).toEqual([]);
  });

  it('allows subdomain of rule.domain', () => {
    const r: Rule = { ...baseRule([{ action: 'navigate', url: 'https://sub.example.com/page' }]) };
    const errors = preflightRule(r);
    expect(errors.filter((e) => e.code === 'static_cross_origin_navigate')).toEqual([]);
  });

  it('allows multi-domain rule.domain array', () => {
    const r: Rule = {
      ...baseRule([{ action: 'navigate', url: 'https://api.example.org/x' }]),
      domain: ['example.com', 'example.org'],
    } as Rule;
    const errors = preflightRule(r);
    expect(errors.filter((e) => e.code === 'static_cross_origin_navigate')).toEqual([]);
  });

  it('skips interpolated URLs (runtime assertion handles them)', () => {
    const r = baseRule([{ action: 'navigate', url: 'https://{{host}}/page' }]);
    const errors = preflightRule(r);
    expect(errors.filter((e) => e.code === 'static_cross_origin_navigate')).toEqual([]);
  });

  it('skips relative URLs', () => {
    const r = baseRule([{ action: 'navigate', url: '/page' }]);
    const errors = preflightRule(r);
    expect(errors.filter((e) => e.code === 'static_cross_origin_navigate')).toEqual([]);
  });
});

describe('rule-preflight - clean rule', () => {
  it('passes with no errors for a clean same-origin rule', () => {
    const r = baseRule([
      { action: 'navigate', url: 'https://example.com/start' },
      { action: 'extract', name: 'title', target: { selector: 'h1' }, fields: { text: { type: 'text' } } },
      { action: 'click', target: { selector: '#go' } },
    ]);
    expect(preflightRule(r)).toEqual([]);
  });
});
