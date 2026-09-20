import { Drawer, Descriptions, Collapse, Tag, Typography } from 'antd';
import type { FlowNode } from '../utils/ruleToGraph';
import { getActionSummary } from '../utils/ruleToGraph';

interface RuleStepInspectorProps {
  open: boolean;
  node: FlowNode | null;
  onClose: () => void;
}

const { Text } = Typography;

export default function RuleStepInspector({ open, node, onClose }: RuleStepInspectorProps) {
  if (!node) return null;

  const { label, summary, category } = getActionSummary(node.data.action);
  const action = node.data.action;

  return (
    <Drawer title="步骤详情" placement="right" width={480} onClose={onClose} open={open}>
      <Descriptions bordered column={1} size="small" layout="vertical">
        <Descriptions.Item label="动作名称">
          <Text strong>{label}</Text>
        </Descriptions.Item>
        <Descriptions.Item label="动作类型">
          <Tag>{action.action}</Tag>
        </Descriptions.Item>
        <Descriptions.Item label="分类">
          <Tag color="blue">{category}</Tag>
        </Descriptions.Item>
        {summary && (
          <Descriptions.Item label="一句话摘要">
            <Text type="secondary">{summary}</Text>
          </Descriptions.Item>
        )}
        {node.data.originalId && (
          <Descriptions.Item label="步骤 ID">{node.data.originalId}</Descriptions.Item>
        )}
        {action.description && (
          <Descriptions.Item label="说明">{action.description}</Descriptions.Item>
        )}
        {action.timeout && (
          <Descriptions.Item label="超时">{action.timeout} ms</Descriptions.Item>
        )}
        {action.critical && (
          <Descriptions.Item label="关键步骤">
            <Tag color="red">是</Tag>
          </Descriptions.Item>
        )}
        {action.checkpoint && (
          <Descriptions.Item label="检查点">
            <Tag color="green">是</Tag>
          </Descriptions.Item>
        )}
      </Descriptions>

      <Collapse ghost style={{ marginTop: 16 }}>
        <Collapse.Panel header="原始配置 JSON" key="raw">
          <pre className="code-preview" style={{ maxHeight: 360, overflow: 'auto' }}>
            {JSON.stringify(action, null, 2)}
          </pre>
        </Collapse.Panel>
      </Collapse>
    </Drawer>
  );
}
