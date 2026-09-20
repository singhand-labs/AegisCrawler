import { render, waitFor, cleanup } from '@testing-library/react';
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import RuleFlowGraph from './RuleFlowGraph';
import type { FlowData, FlowNode } from '../utils/ruleToGraph';
import type { ActionPath } from '../utils/ruleEdit';

const mockSetElementState = vi.fn().mockResolvedValue(undefined);
const mockRender = vi.fn().mockResolvedValue(undefined);
const mockSetSize = vi.fn();
const mockDestroy = vi.fn();
const mockOn = vi.fn();
const mockGetNodeData = vi.fn();

let clickHandler: ((e: any) => void) | null = null;
let dblClickHandler: ((e: any) => void) | null = null;

const MockGraph = vi.fn(({ data }: { data: FlowData }) => {
  return {
    on: (event: string, handler: (e: any) => void) => {
      mockOn(event, handler);
      if (event === 'node:click') clickHandler = handler;
      if (event === 'node:dblclick') dblClickHandler = handler;
    },
    render: mockRender,
    setElementState: mockSetElementState,
    getNodeData: mockGetNodeData,
    setSize: mockSetSize,
    destroy: mockDestroy,
    data,
  };
});

vi.mock('@antv/g6', () => ({
  Graph: MockGraph,
}));

function makeData(nodes: FlowData['nodes']): FlowData {
  return {
    nodes,
    edges: [],
    combos: [],
  };
}

function makeNode(id: string, path: ActionPath): FlowNode {
  return {
    id,
    data: {
      label: id,
      summary: '',
      category: 'interaction',
      action: { action: 'click', target: { selector: '#x' } } as any,
      path,
    },
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  clickHandler = null;
  dblClickHandler = null;
  globalThis.ResizeObserver = vi.fn().mockImplementation(() => ({
    observe: vi.fn(),
    disconnect: vi.fn(),
    unobserve: vi.fn(),
  }));
});

afterEach(() => {
  cleanup();
});

describe('RuleFlowGraph', () => {
  it('renders the graph container', async () => {
    render(<RuleFlowGraph data={makeData([makeNode('n1', ['steps', 0])])} />);
    await waitFor(() => expect(mockRender).toHaveBeenCalled());
    expect(document.querySelector('[style*="min-height: 480px"]')).toBeInTheDocument();
  });

  it('calls onSelectNode with FlowNode when a node is clicked', async () => {
    const onSelectNode = vi.fn();
    const node = makeNode('n1', ['steps', 0]);
    render(<RuleFlowGraph data={makeData([node])} onSelectNode={onSelectNode} />);
    await waitFor(() => expect(clickHandler).toBeTruthy());

    mockGetNodeData.mockReturnValue({
      id: 'n1',
      label: 'n1',
      summary: '',
      category: 'interaction',
      action: node.data.action,
      path: ['steps', 0],
    });

    clickHandler!({ target: { id: 'n1' } });

    expect(onSelectNode).toHaveBeenCalledTimes(1);
    const selected = onSelectNode.mock.calls[0][0] as FlowNode;
    expect(selected.id).toBe('n1');
    expect(selected.data.path).toEqual(['steps', 0]);
  });

  it('calls onEditNode when a node is double-clicked', async () => {
    const onEditNode = vi.fn();
    const node = makeNode('n2', ['steps', 1]);
    render(<RuleFlowGraph data={makeData([node])} onEditNode={onEditNode} />);
    await waitFor(() => expect(dblClickHandler).toBeTruthy());

    mockGetNodeData.mockReturnValue({
      id: 'n2',
      label: 'n2',
      summary: '',
      category: 'interaction',
      action: node.data.action,
      path: ['steps', 1],
    });

    dblClickHandler!({ target: { id: 'n2' } });

    expect(onEditNode).toHaveBeenCalledTimes(1);
    const edited = onEditNode.mock.calls[0][0] as FlowNode;
    expect(edited.id).toBe('n2');
    expect(edited.data.path).toEqual(['steps', 1]);
  });

  it('highlights the node matching selectedPath', async () => {
    const node = makeNode('n3', ['steps', 2]);
    const { rerender } = render(<RuleFlowGraph data={makeData([node])} />);
    await waitFor(() => expect(mockRender).toHaveBeenCalled());

    rerender(<RuleFlowGraph data={makeData([node])} selectedPath={['steps', 2]} />);

    await waitFor(() => expect(mockSetElementState).toHaveBeenCalledWith('n3', 'selected'));
  });

  it('clears highlight when selectedPath no longer matches', async () => {
    const node = makeNode('n4', ['steps', 3]);
    const { rerender } = render(<RuleFlowGraph data={makeData([node])} selectedPath={['steps', 3]} />);
    await waitFor(() => expect(mockRender).toHaveBeenCalled());

    rerender(<RuleFlowGraph data={makeData([node])} selectedPath={['steps', 4]} />);

    await waitFor(() => expect(mockSetElementState).toHaveBeenLastCalledWith('n4', []));
  });

  it('does not re-initialize the graph when callback props change', async () => {
    const node = makeNode('n5', ['steps', 4]);
    const data = makeData([node]);
    const onSelectNode = vi.fn();
    const { rerender } = render(<RuleFlowGraph data={data} onSelectNode={onSelectNode} />);
    await waitFor(() => expect(mockRender).toHaveBeenCalled());
    const initCount = MockGraph.mock.calls.length;

    rerender(<RuleFlowGraph data={data} onSelectNode={vi.fn()} />);

    expect(MockGraph).toHaveBeenCalledTimes(initCount);
    expect(mockDestroy).not.toHaveBeenCalled();
  });
});
