import React, { useState } from 'react';
import {
  AimOutlined,
  BorderOutlined,
  BranchesOutlined,
  ClockCircleOutlined,
  CloudUploadOutlined,
  ControlOutlined,
  EditOutlined,
  GlobalOutlined,
  LinkOutlined,
  MedicineBoxOutlined,
  MoreOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SafetyOutlined,
  ScanOutlined,
  StopOutlined,
} from '@ant-design/icons';
import { Collapse, Space, Tag, Typography } from 'antd';
import type { Action, Rule } from '../types/rule';
import type { ActionPath } from '../utils/ruleEdit';
import { pathToString } from '../utils/ruleEdit';
import { getActionSummary } from '../utils/ruleToGraph';

const { Text } = Typography;

interface RuleFlowListProps {
  rule: Rule;
  selectedPath?: ActionPath;
  onSelectAction?: (path: ActionPath) => void;
}

const CATEGORY_ICONS: Record<string, React.ReactNode> = {
  start: <PlayCircleOutlined />,
  end: <StopOutlined />,
  interaction: <AimOutlined />,
  input: <EditOutlined />,
  scroll: <MoreOutlined rotate={90} />,
  navigation: <LinkOutlined />,
  wait: <ClockCircleOutlined />,
  extract: <ScanOutlined />,
  transform: <ReloadOutlined />,
  ops: <MedicineBoxOutlined />,
  flow: <BranchesOutlined />,
  page: <BorderOutlined />,
  browser: <GlobalOutlined />,
  auth: <SafetyOutlined />,
  output: <CloudUploadOutlined />,
  hook: <ControlOutlined />,
  unknown: <BorderOutlined />,
};

const CATEGORY_COLORS: Record<string, string> = {
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

function pathEquals(a?: ActionPath, b?: ActionPath): boolean {
  if (!a || !b) return false;
  if (a.length !== b.length) return false;
  return a.every((v, i) => v === b[i]);
}

function ActionIcon({ category }: { category: string }) {
  return <span style={{ color: CATEGORY_COLORS[category] || '#8c8c8c' }}>{CATEGORY_ICONS[category] || CATEGORY_ICONS.unknown}</span>;
}

interface Branch {
  key: string;
  label: string;
  pathSegment: ActionPath;
  actions: Action[];
  single?: boolean;
}

function getBranches(action: Action): Branch[] {
  switch (action.action) {
    case 'if':
      return [
        { key: 'then', label: '满足条件时执行', pathSegment: ['then'], actions: action.then ?? [] },
        { key: 'else', label: '不满足条件时执行', pathSegment: ['else'], actions: action.else ?? [] },
      ].filter((b) => b.actions.length > 0);
    case 'switch': {
      const branches = (action.cases ?? []).map((c, idx) => ({
        key: `case-${c.value ?? idx}`,
        label: `分支 ${c.value ?? idx}`,
        pathSegment: ['cases', idx, 'steps'],
        actions: c.steps ?? [],
      }));
      if (action.default?.length) {
        branches.push({ key: 'default', label: '默认', pathSegment: ['default'], actions: action.default });
      }
      return branches.filter((b) => b.actions.length > 0);
    }
    case 'loop':
    case 'group':
    case 'retry':
    case 'cleanup':
      return [
        {
          key: 'steps',
          label:
            action.action === 'loop'
              ? '循环体'
              : action.action === 'group'
                ? '组内步骤'
                : action.action === 'retry'
                  ? '重试体'
                  : '清理步骤',
          pathSegment: ['steps'],
          actions: action.steps ?? [],
        },
      ].filter((b) => b.actions.length > 0);
    case 'parallel':
      return (action.steps ?? []).map((s, idx) => ({
        key: `branch-${idx}`,
        label: '并行分支',
        pathSegment: ['steps', idx],
        actions: [s],
        single: true,
      }));
    case 'requestHuman':
      return [
        { key: 'then', label: '人工处理后执行', pathSegment: ['then'], actions: action.then ?? [] },
      ].filter((b) => b.actions.length > 0);
    default:
      return [];
  }
}

interface ActionCardProps {
  action: Action;
  path: ActionPath;
  depth: number;
  selectedPath?: ActionPath;
  onSelectAction?: (path: ActionPath) => void;
}

function ActionCard({ action, path, depth, selectedPath, onSelectAction }: ActionCardProps) {
  const { label, summary, category } = getActionSummary(action);
  const [expanded, setExpanded] = useState(depth < 2);

  const branches = getBranches(action);
  const hasChildren = branches.length > 0 && branches.some((b) => b.actions.length > 0);
  const isSelected = pathEquals(path, selectedPath);

  const content = (
    <div
      data-testid={`action-card-${pathToString(path)}`}
      onClick={() => onSelectAction?.(path)}
      style={{
        borderLeft: `${isSelected ? 6 : 4}px solid ${CATEGORY_COLORS[category] || '#8c8c8c'}`,
        backgroundColor: isSelected ? '#e6f7ff' : undefined,
        paddingLeft: 12,
        marginBottom: 8,
        cursor: 'pointer',
        borderRadius: 2,
      }}
    >
      <Space wrap>
        <ActionIcon category={category} />
        <Text strong>{label}</Text>
        {summary && (
          <Text type="secondary" ellipsis style={{ maxWidth: 360 }}>
            {summary}
          </Text>
        )}
        {action.id && <Tag>{action.id}</Tag>}
        {hasChildren && (
          <Tag
            style={{ cursor: 'pointer' }}
            onClick={(e) => {
              e.stopPropagation();
              setExpanded((v) => !v);
            }}
          >
            {expanded ? '收起' : '展开'}
          </Tag>
        )}
      </Space>
    </div>
  );

  if (!hasChildren) {
    return content;
  }

  const panels = branches.map((branch) => ({
    key: branch.key,
    label: branch.label,
    children: branch.single ? (
      <ActionCard
        action={branch.actions[0]}
        path={[...path, ...branch.pathSegment]}
        depth={depth + 1}
        selectedPath={selectedPath}
        onSelectAction={onSelectAction}
      />
    ) : (
      <NestedActions
        actions={branch.actions}
        basePath={[...path, ...branch.pathSegment]}
        depth={depth + 1}
        selectedPath={selectedPath}
        onSelectAction={onSelectAction}
      />
    ),
  }));

  return (
    <div>
      {content}
      <div style={{ marginLeft: 24 }} onClick={(e) => e.stopPropagation()}>
        <Collapse activeKey={expanded ? panels.map((p) => p.key) : []} onChange={() => setExpanded((v) => !v)} ghost items={panels} />
      </div>
    </div>
  );
}

interface NestedActionsProps {
  actions: Action[];
  basePath: ActionPath;
  depth: number;
  selectedPath?: ActionPath;
  onSelectAction?: (path: ActionPath) => void;
}

function NestedActions({ actions, basePath, depth, selectedPath, onSelectAction }: NestedActionsProps) {
  return (
    <div>
      {actions.map((action, idx) => {
        const path = [...basePath, idx];
        return (
          <ActionCard
            key={pathToString(path)}
            action={action}
            path={path}
            depth={depth}
            selectedPath={selectedPath}
            onSelectAction={onSelectAction}
          />
        );
      })}
    </div>
  );
}

interface HookSectionProps {
  name: string;
  hookKey: string;
  actions?: Action[];
  selectedPath?: ActionPath;
  onSelectAction?: (path: ActionPath) => void;
}

function HookSection({ name, hookKey, actions, selectedPath, onSelectAction }: HookSectionProps) {
  if (!actions || actions.length === 0) return null;
  return (
    <div style={{ marginBottom: 16 }}>
      <Tag color="cyan" style={{ marginBottom: 8 }}>
        {name}
      </Tag>
      <NestedActions
        actions={actions}
        basePath={['hooks', hookKey]}
        depth={0}
        selectedPath={selectedPath}
        onSelectAction={onSelectAction}
      />
    </div>
  );
}

export default function RuleFlowList({ rule, selectedPath, onSelectAction }: RuleFlowListProps) {
  const hooks = rule.hooks ?? {};
  return (
    <div className="rule-flow-list">
      <NestedActions
        actions={rule.steps ?? []}
        basePath={['steps']}
        depth={0}
        selectedPath={selectedPath}
        onSelectAction={onSelectAction}
      />
      {Object.keys(hooks).length > 0 && (
        <div style={{ marginTop: 24 }}>
          <Text strong style={{ display: 'block', marginBottom: 12 }}>
            生命周期钩子
          </Text>
          <HookSection name="前置钩子" hookKey="beforeAll" actions={hooks.beforeAll} selectedPath={selectedPath} onSelectAction={onSelectAction} />
          <HookSection name="后置钩子" hookKey="afterAll" actions={hooks.afterAll} selectedPath={selectedPath} onSelectAction={onSelectAction} />
          <HookSection name="异常处理" hookKey="onError" actions={hooks.onError} selectedPath={selectedPath} onSelectAction={onSelectAction} />
          <HookSection name="最终清理" hookKey="cleanup" actions={hooks.cleanup} selectedPath={selectedPath} onSelectAction={onSelectAction} />
        </div>
      )}
    </div>
  );
}
