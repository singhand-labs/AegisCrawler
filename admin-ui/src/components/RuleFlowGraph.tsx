import { useEffect, useRef, useState } from 'react';
import { Alert, Spin } from 'antd';
import type { Graph } from '@antv/g6';
import type { ActionPath } from '../utils/ruleEdit';
import type { FlowData, FlowNode } from '../utils/ruleToGraph';

interface RuleFlowGraphProps {
  data: FlowData;
  selectedPath?: ActionPath;
  onSelectNode?: (node: FlowNode) => void;
  onEditNode?: (node: FlowNode) => void;
  onError?: (err: Error) => void;
}

const NODE_COLORS: Record<string, string> = {
  start: '#52c41a',
  end: '#f5222d',
  interaction: '#1890ff',
  input: '#722ed1',
  scroll: '#13c2c2',
  navigation: '#595959',
  wait: '#faad14',
  extract: '#52c41a',
  transform: '#eb2f96',
  ops: '#fa8c16',
  flow: '#f5222d',
  page: '#2f4554',
  browser: '#096dd9',
  auth: '#cf1322',
  output: '#531dab',
  hook: '#87e8de',
  unknown: '#8c8c8c',
};

const COMBO_COLORS: Record<string, string> = {
  if: '#fff2f0',
  switch: '#fff2f0',
  loop: '#fff7e6',
  group: '#f6ffed',
  retry: '#f6ffed',
  parallel: '#e6fffb',
  requestHuman: '#f9f0ff',
  cleanup: '#f5f5f5',
  hook: '#e6fffb',
  branch: '#ffffff',
};

function pathEquals(a?: ActionPath, b?: ActionPath): boolean {
  if (!a || !b) return false;
  if (a.length !== b.length) return false;
  return a.every((v, i) => v === b[i]);
}

function toG6Data(data: FlowData) {
  return {
    nodes: data.nodes.map((n) => ({
      id: n.id,
      combo: n.combo,
      label: n.data.label,
      summary: n.data.summary,
      category: n.data.category,
      action: n.data.action,
      originalId: n.data.originalId,
      path: n.data.path,
    })),
    edges: data.edges.map((e) => ({
      id: e.id,
      source: e.source,
      target: e.target,
      label: e.data?.label,
      dashed: e.data?.dashed,
    })),
    combos: data.combos.map((c) => ({
      id: c.id,
      combo: c.combo,
      label: c.data.label,
      category: c.data.category,
      type: c.data.type,
      summary: c.data.summary,
    })),
  };
}

export default function RuleFlowGraph({ data, selectedPath, onSelectNode, onEditNode, onError }: RuleFlowGraphProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const graphRef = useRef<Graph | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [graphReady, setGraphReady] = useState(false);

  const onSelectNodeRef = useRef(onSelectNode);
  const onEditNodeRef = useRef(onEditNode);
  const onErrorRef = useRef(onError);

  useEffect(() => {
    onSelectNodeRef.current = onSelectNode;
  }, [onSelectNode]);

  useEffect(() => {
    onEditNodeRef.current = onEditNode;
  }, [onEditNode]);

  useEffect(() => {
    onErrorRef.current = onError;
  }, [onError]);

  useEffect(() => {
    let mounted = true;
    let resizeObserver: ResizeObserver | null = null;

    async function init() {
      try {
        const g6 = await import('@antv/g6');
        if (!mounted || !containerRef.current) return;

        const { Graph } = g6;
        const g6Data = toG6Data(data);

        const graph = new Graph({
          container: containerRef.current,
          autoFit: 'view',
          padding: 16,
          data: g6Data as any,
          node: {
            type: 'rect',
            style: {
              size: [150, 44],
              radius: 6,
              fill: (d: any) => NODE_COLORS[d.category] || '#ffffff',
              fillOpacity: 0.12,
              stroke: (d: any) => NODE_COLORS[d.category] || '#d9d9d9',
              lineWidth: 1.5,
              labelText: (d: any) => d.label,
              labelFill: '#262626',
              labelFontSize: 12,
              labelWordWrap: true,
              labelMaxWidth: 130,
              labelTextAlign: 'center',
              cursor: 'pointer',
            },
            state: {
              selected: {
                lineWidth: 3,
                stroke: '#262626',
                fillOpacity: 0.3,
              },
            },
          },
          edge: {
            type: 'line',
            style: {
              stroke: '#8c8c8c',
              lineWidth: 1.2,
              endArrow: true,
              endArrowSize: 8,
              labelText: (d: any) => d.label || '',
              labelFill: '#595959',
              labelFontSize: 11,
              labelBackground: true,
              labelBackgroundFill: '#ffffff',
              lineDash: (d: any) => (d.dashed ? [4, 4] : []),
            },
          },
          combo: {
            type: 'rect',
            style: {
              radius: 8,
              lineWidth: 1,
              stroke: '#bfbfbf',
              fill: (d: any) => COMBO_COLORS[d.type] || '#fafafa',
              labelText: (d: any) => d.label,
              labelFontSize: 12,
              labelFill: '#595959',
              labelPosition: 'top',
              labelOffsetY: 8,
            },
          },
          layout: {
            type: 'antv-dagre',
            ranksep: 60,
            nodesep: 24,
            sortByCombo: true,
          },
          behaviors: ['drag-element', 'drag-canvas', 'zoom-canvas', 'collapse-expand'],
        });

        function g6NodeToFlowNode(g6Node: any): FlowNode | null {
          if (!g6Node || !g6Node.id) return null;
          return {
            id: g6Node.id,
            combo: g6Node.combo,
            data: {
              label: g6Node.label ?? '',
              summary: g6Node.summary ?? '',
              category: g6Node.category ?? 'unknown',
              action: g6Node.action,
              originalId: g6Node.originalId,
              path: g6Node.path ?? [],
            },
          };
        }

        graph.on('node:click', (e: any) => {
          const id = e.target?.id;
          if (id && onSelectNodeRef.current) {
            try {
              const nodeData = graph.getNodeData(id);
              const node = g6NodeToFlowNode(nodeData);
              if (node) onSelectNodeRef.current(node);
            } catch {
              // ignore
            }
          }
        });

        graph.on('node:dblclick', (e: any) => {
          const id = e.target?.id;
          if (id && onEditNodeRef.current) {
            try {
              const nodeData = graph.getNodeData(id);
              const node = g6NodeToFlowNode(nodeData);
              if (node) onEditNodeRef.current(node);
            } catch {
              // ignore
            }
          }
        });

        graphRef.current = graph;

        if (containerRef.current) {
          const resize = () => {
            if (!containerRef.current || !graphRef.current) return;
            const { clientWidth, clientHeight } = containerRef.current;
            graphRef.current.setSize(clientWidth, clientHeight);
          };
          resizeObserver = new ResizeObserver(resize);
          resizeObserver.observe(containerRef.current);
          resize();
        }

        await graph.render();
        setGraphReady(true);
        setLoading(false);
      } catch (err) {
        const e = err instanceof Error ? err : new Error(String(err));
        setError(e);
        setLoading(false);
        onErrorRef.current?.(e);
      }
    }

    init();

    return () => {
      mounted = false;
      if (resizeObserver && containerRef.current) {
        resizeObserver.disconnect();
      }
      if (graphRef.current) {
        try {
          graphRef.current.destroy();
        } catch {
          // ignore
        }
        graphRef.current = null;
      }
      setGraphReady(false);
    };
  }, [data]);

  useEffect(() => {
    const graph = graphRef.current;
    if (!graph || !graphReady) return;

    data.nodes.forEach((n) => {
      const state = pathEquals(n.data.path, selectedPath) ? 'selected' : [];
      graph.setElementState(n.id, state).catch(() => {
        // ignore
      });
    });
  }, [data.nodes, selectedPath, graphReady]);

  if (error) {
    return (
      <Alert
        type="warning"
        message="流程图渲染失败"
        description={error.message}
        showIcon
      />
    );
  }

  return (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      {loading && (
        <div style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1 }}>
          <Spin />
        </div>
      )}
      <div ref={containerRef} style={{ width: '100%', height: '100%', minHeight: 480 }} />
    </div>
  );
}
