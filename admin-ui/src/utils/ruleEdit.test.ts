import { describe, expect, it } from 'vitest';
import {
  ActionPath,
  appendActionToContainer,
  cloneRule,
  getActionByPath,
  getParentPath,
  insertActionByPath,
  moveActionByPath,
  pathToString,
  removeActionByPath,
  stringToPath,
  updateActionByPath,
} from './ruleEdit';
import type { Rule } from '../types/rule';

function makeRule(): Rule {
  return {
    id: 'r1',
    version: '1.0.0',
    name: 'Rule',
    domain: 'example.com',
    urlPattern: undefined,
    enabled: true,
    priority: 'normal',
    entry: 'https://example.com',
    variables: {},
    selectors: {},
    humanize: {},
    steps: [
      { action: 'navigate', url: 'https://example.com' },
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '.x' } },
        then: [{ action: 'click', target: { selector: '.x' } }],
        else: [{ action: 'sendLog', level: 'warn', message: 'miss' }],
      },
      { action: 'extract', name: 'items', target: { selector: '.item' }, fields: {} },
    ],
    output: {},
    sendPolicy: {},
    hooks: {},
    tags: {},
    owner: 'x',
    approvalStatus: 'approved',
    createdAt: '',
    updatedAt: '',
  };
}

describe('cloneRule', () => {
  it('returns a deep clone', () => {
    const rule = makeRule();
    const cloned = cloneRule(rule);
    expect(cloned).toEqual(rule);
    expect(cloned).not.toBe(rule);
    cloned.steps[0].url = 'changed';
    expect(rule.steps[0].url).toBe('https://example.com');
  });
});

describe('getActionByPath', () => {
  it('gets top-level action', () => {
    const rule = makeRule();
    const action = getActionByPath(rule, ['steps', 0]);
    expect(action?.action).toBe('navigate');
  });

  it('gets nested then action', () => {
    const rule = makeRule();
    const action = getActionByPath(rule, ['steps', 1, 'then', 0]);
    expect(action?.action).toBe('click');
  });

  it('returns undefined for missing path', () => {
    const rule = makeRule();
    expect(getActionByPath(rule, ['steps', 99])).toBeUndefined();
  });
});

describe('updateActionByPath', () => {
  it('updates a top-level action', () => {
    const rule = makeRule();
    const next = updateActionByPath(rule, ['steps', 0], { action: 'navigate', url: 'https://new.com' });
    expect(next.steps[0].url).toBe('https://new.com');
    expect(rule.steps[0].url).toBe('https://example.com');
  });

  it('updates a nested action', () => {
    const rule = makeRule();
    const next = updateActionByPath(rule, ['steps', 1, 'then', 0], { action: 'click', target: { selector: '.new' } });
    expect((next.steps[1] as any).then[0].target.selector).toBe('.new');
  });
});

describe('removeActionByPath', () => {
  it('removes a top-level action', () => {
    const rule = makeRule();
    const next = removeActionByPath(rule, ['steps', 0]);
    expect(next.steps.length).toBe(2);
    expect(next.steps[0].action).toBe('if');
  });

  it('removes a nested action', () => {
    const rule = makeRule();
    const next = removeActionByPath(rule, ['steps', 1, 'then', 0]);
    expect((next.steps[1] as any).then.length).toBe(0);
  });
});

describe('insertActionByPath', () => {
  it('inserts before a top-level action', () => {
    const rule = makeRule();
    const next = insertActionByPath(rule, ['steps', 1], 'before', { action: 'sleep', ms: 1000 });
    expect(next.steps.length).toBe(4);
    expect(next.steps[1].action).toBe('sleep');
    expect(next.steps[2].action).toBe('if');
  });

  it('inserts after a top-level action', () => {
    const rule = makeRule();
    const next = insertActionByPath(rule, ['steps', 0], 'after', { action: 'sleep', ms: 1000 });
    expect(next.steps[1].action).toBe('sleep');
    expect(next.steps[2].action).toBe('if');
  });
});

describe('moveActionByPath', () => {
  it('moves an action up', () => {
    const rule = makeRule();
    const next = moveActionByPath(rule, ['steps', 2], 'up');
    expect(next.steps[1].action).toBe('extract');
    expect(next.steps[2].action).toBe('if');
  });

  it('moves an action down', () => {
    const rule = makeRule();
    const next = moveActionByPath(rule, ['steps', 0], 'down');
    expect(next.steps[0].action).toBe('if');
    expect(next.steps[1].action).toBe('navigate');
  });

  it('does nothing when moving out of bounds', () => {
    const rule = makeRule();
    const next = moveActionByPath(rule, ['steps', 0], 'up');
    expect(next.steps[0].action).toBe('navigate');
  });
});

describe('appendActionToContainer', () => {
  it('appends to loop steps', () => {
    const rule = makeRule();
    (rule.steps[1] as any).then = [{ action: 'loop', type: 'fixedCount', count: 3, steps: [{ action: 'click', target: { selector: '.a' } }] }];
    const next = appendActionToContainer(rule, ['steps', 1, 'then', 0], { action: 'extract', name: 'x', target: { selector: '.b' }, fields: {} });
    const loop = (next.steps[1] as any).then[0];
    expect(loop.steps.length).toBe(2);
    expect(loop.steps[1].action).toBe('extract');
  });

  it('appends to if then branch', () => {
    const rule = makeRule();
    const next = appendActionToContainer(rule, ['steps', 1], { action: 'sendResult' });
    expect((next.steps[1] as any).then.length).toBe(2);
    expect((next.steps[1] as any).then[1].action).toBe('sendResult');
  });
});

describe('path helpers', () => {
  it('getParentPath returns parent', () => {
    expect(getParentPath(['steps', 2, 'then', 0])).toEqual(['steps', 2, 'then']);
    expect(getParentPath(['steps', 0])).toEqual(['steps']);
    expect(getParentPath([])).toBeNull();
  });

  it('pathToString and stringToPath roundtrip', () => {
    const path: ActionPath = ['steps', 1, 'then', 0];
    const str = pathToString(path);
    expect(str).toBe('steps[1].then[0]');
    expect(stringToPath(str)).toEqual(path);
  });
});
