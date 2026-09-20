import { describe, expect, it } from 'vitest';
import { buildFlowData, countNodes, getActionCategory, getActionSummary } from './ruleToGraph';
import type { FlowNode } from './ruleToGraph';
import { getActionByPath } from './ruleEdit';
import type { Action, Rule } from '../types/rule';

function assertPathsResolveToActions(rule: Rule, data: { nodes: FlowNode[] }): void {
  for (const node of data.nodes) {
    if (node.id === '__start__' || node.id === '__end__') {
      expect(node.data.path).toEqual([]);
    } else {
      const resolved = getActionByPath(rule, node.data.path);
      expect(resolved).toBe(node.data.action);
    }
  }
}

function makeRule(steps: Action[], hooks?: Rule['hooks']): Rule {
  return {
    id: 'test-rule',
    version: '1.0.0',
    name: 'Test Rule',
    domain: 'example.com',
    urlPattern: undefined,
    enabled: true,
    priority: 'normal',
    entry: 'https://example.com',
    variables: {},
    selectors: {},
    humanize: {},
    steps,
    output: {},
    sendPolicy: {},
    hooks: hooks ?? {},
    tags: {},
    owner: 'tester',
    approvalStatus: 'approved',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

describe('getActionCategory', () => {
  it('classifies common actions', () => {
    expect(getActionCategory('click')).toBe('interaction');
    expect(getActionCategory('extract')).toBe('extract');
    expect(getActionCategory('navigate')).toBe('navigation');
    expect(getActionCategory('if')).toBe('flow');
    expect(getActionCategory('sendResult')).toBe('output');
  });

  it('returns unknown for unrecognized actions', () => {
    expect(getActionCategory('futureAction')).toBe('unknown');
  });
});

describe('getActionSummary', () => {
  it('summarizes a navigate action', () => {
    const a: Action = { action: 'navigate', url: 'https://example.com/list' };
    const s = getActionSummary(a);
    expect(s.label).toBe('页面跳转');
    expect(s.summary).toContain('example.com');
    expect(s.category).toBe('navigation');
  });

  it('summarizes an extract action', () => {
    const a: Action = { action: 'extract', name: 'items', target: { selector: '.item' }, fields: {} };
    const s = getActionSummary(a);
    expect(s.label).toBe('提取数据');
    expect(s.summary).toContain('items');
    expect(s.summary).toContain('.item');
  });

  it('summarizes an if action with condition', () => {
    const a: Action = { action: 'if', condition: { type: 'elementExists', target: { selector: '.btn' } } };
    const s = getActionSummary(a);
    expect(s.label).toBe('条件判断');
    expect(s.summary).toContain('元素存在');
  });

  it('summarizes a loop action', () => {
    const a: Action = { action: 'loop', type: 'fixedCount', count: 5, steps: [] };
    const s = getActionSummary(a);
    expect(s.label).toBe('循环');
    expect(s.summary).toContain('fixedCount');
    expect(s.summary).toContain('5');
  });

  it('summarizes targeted page-unit scrolling', () => {
    const a: Action = {
      action: 'scrollBy',
      target: { selector: '#results' },
      direction: 'down',
      distance: 2,
      unit: 'pages',
    };
    const s = getActionSummary(a);
    expect(s.summary).toBe('#results down 2页');
  });
});

describe('buildFlowData', () => {
  it('renders a simple linear sequence', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      { action: 'click', target: { selector: '#btn' } },
      { action: 'extract', name: 'title', target: { selector: 'h1' }, fields: {} },
    ]);
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const { nodes, edges } = countNodes(data);
    // start + end + 3 actions
    expect(nodes).toBe(5);
    expect(edges).toBe(4);
    const labels = data.nodes.map((n) => n.data.label);
    expect(labels).toContain('开始');
    expect(labels).toContain('结束');
    expect(labels).toContain('页面跳转');
    expect(labels).toContain('点击');
    expect(labels).toContain('提取数据');
  });

  it('renders an if branch with then/else', () => {
    const rule = makeRule([
      { action: 'navigate', url: 'https://example.com' },
      {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '.vip' } },
        then: [{ action: 'click', target: { selector: '.vip' } }],
        else: [{ action: 'sendLog', level: 'warn', message: 'not vip' }],
      },
      { action: 'extract', name: 'result', target: { selector: '.result' }, fields: {} },
    ]);
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const ifCombo = data.combos.find((c) => c.data.type === 'if');
    expect(ifCombo).toBeDefined();
    const yesEdge = data.edges.find((e) => e.data?.label === '是');
    const noEdge = data.edges.find((e) => e.data?.label === '否');
    expect(yesEdge).toBeDefined();
    expect(noEdge).toBeDefined();
    // branches converge to the extract node after the if block
    const extractNode = data.nodes.find((n) => n.data.label === '提取数据');
    expect(extractNode).toBeDefined();
    const intoExtract = data.edges.filter((e) => e.target === extractNode?.id);
    expect(intoExtract.length).toBeGreaterThanOrEqual(2);
  });

  it('renders a switch with cases', () => {
    const rule = makeRule([
      {
        action: 'switch',
        expression: '{{status}}',
        cases: [
          { value: 'ok', steps: [{ action: 'sendResult' }] },
          { value: 'err', steps: [{ action: 'sendLog', level: 'error', message: 'fail' }] },
        ],
        default: [{ action: 'sendLog', level: 'info', message: 'unknown' }],
      },
    ]);
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const switchCombo = data.combos.find((c) => c.data.type === 'switch');
    expect(switchCombo).toBeDefined();
    const caseEdges = data.edges.filter((e) => e.data?.label === 'ok' || e.data?.label === 'err');
    expect(caseEdges.length).toBe(2);
    const defaultEdge = data.edges.find((e) => e.data?.label === '默认');
    expect(defaultEdge).toBeDefined();
  });

  it('renders a loop with a back edge', () => {
    const rule = makeRule([
      {
        action: 'loop',
        type: 'forEach',
        items: '{{links}}',
        as: 'link',
        steps: [{ action: 'navigate', url: '{{link.url}}' }, { action: 'extract', name: 'item', target: { selector: 'h1' }, fields: {} }],
      },
    ]);
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const loopCombo = data.combos.find((c) => c.data.type === 'loop');
    expect(loopCombo).toBeDefined();
    const backEdge = data.edges.find((e) => e.data?.label === '继续循环');
    expect(backEdge).toBeDefined();
  });

  it('renders parallel branches with fork and join', () => {
    const rule = makeRule([
      {
        action: 'parallel',
        steps: [
          { action: 'extract', name: 'a', target: { selector: '.a' }, fields: {} },
          { action: 'extract', name: 'b', target: { selector: '.b' }, fields: {} },
        ],
      },
    ]);
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const fork = data.nodes.find((n) => n.data.label === '并行执行');
    const join = data.nodes.find((n) => n.data.label === '合并');
    expect(fork).toBeDefined();
    expect(join).toBeDefined();
    const outFromFork = data.edges.filter((e) => e.source === fork?.id);
    expect(outFromFork.length).toBe(2);
    const intoJoin = data.edges.filter((e) => e.target === join?.id);
    expect(intoJoin.length).toBe(2);
  });

  it('renders hooks as separate lanes', () => {
    const rule = makeRule(
      [{ action: 'navigate', url: 'https://example.com' }],
      {
        beforeAll: [{ action: 'setViewport', width: 1920, height: 1080 }],
        onError: [{ action: 'sendLog', level: 'error', message: 'oops' }],
      },
    );
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const hookCombos = data.combos.filter((c) => c.data.category === 'hook');
    expect(hookCombos.length).toBe(2);
    expect(hookCombos.some((c) => c.data.label === '前置钩子')).toBe(true);
    expect(hookCombos.some((c) => c.data.label === '异常处理')).toBe(true);
  });

  it('handles unknown actions gracefully', () => {
    const rule = makeRule([{ action: 'futureAction', customField: 123 } as Action]);
    expect(() => buildFlowData(rule)).not.toThrow();
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const node = data.nodes.find((n) => n.data.label === 'futureAction');
    expect(node).toBeDefined();
    expect(node?.data.category).toBe('unknown');
  });

  it('connects start directly to end when there are no steps', () => {
    const rule = makeRule([]);
    const data = buildFlowData(rule);
    assertPathsResolveToActions(rule, data);
    const startEnd = data.edges.find((e) => e.source === '__start__' && e.target === '__end__');
    expect(startEnd).toBeDefined();
  });
});
