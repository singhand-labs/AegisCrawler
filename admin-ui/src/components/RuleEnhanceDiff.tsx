import { useEffect, useState } from 'react';
import { Card, Alert, Button, Space, List, Tabs, Tag, Typography, Spin } from 'antd';
import type { RuleEnhancement, SafetyFlag, LLMJob, LLMJobStatus, PatchOperation } from '../api/types';
import RuleFlowList from './RuleFlowList';
import RuleJsonDiff from './RuleJsonDiff';
import PatchEditor from './PatchEditor';

const { Title, Text } = Typography;

type DiffTab = 'enhanced' | 'baseline' | 'jsonDiff' | 'patchEdit';

interface Props {
  enhancement: RuleEnhancement;
  job?: LLMJob | null;
  onAccept: () => void;
  onReject: () => void;
  loading?: boolean;
}

const statusLabel: Record<LLMJobStatus, string> = {
  pending: '排队中',
  running: '增强中',
  completed: '已完成',
  failed: '失败',
};

const statusColor: Record<LLMJobStatus, string> = {
  pending: 'default',
  running: 'processing',
  completed: 'success',
  failed: 'error',
};

export default function RuleEnhanceDiff({ enhancement, job, onAccept, onReject, loading }: Props) {
  const [activeTab, setActiveTab] = useState<DiffTab>('enhanced');
  const [previewEnhanced, setPreviewEnhanced] = useState<object>(enhancement.enhanced);
  const [activePatch, setActivePatch] = useState<PatchOperation[]>(enhancement.patch ?? []);

  useEffect(() => {
    setPreviewEnhanced(enhancement.enhanced);
    setActivePatch(enhancement.patch ?? []);
  }, [enhancement.id, enhancement.enhanced, enhancement.patch]);

  const isPending = job?.status === 'pending' || job?.status === 'running';

  return (
    <Card
      title={
        <Space>
          <Title level={5} style={{ margin: 0 }}>AI 规则增强建议</Title>
          <Tag color="blue">{enhancement.provider}/{enhancement.model}</Tag>
          {job && <Tag color={statusColor[job.status]}>{statusLabel[job.status]}</Tag>}
        </Space>
      }
      extra={
        <Space>
          <Button danger onClick={onReject} loading={loading} disabled={isPending}>拒绝并删除</Button>
          <Button type="primary" onClick={onAccept} loading={loading} disabled={isPending}>接受增强</Button>
        </Space>
      }
    >
      {isPending && (
        <Alert
          message="AI 增强任务正在处理中"
          description={<Space><Spin size="small" />{job?.status === 'pending' ? '等待执行' : '正在调用 LLM 生成增强建议'}</Space>}
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
        />
      )}
      {(enhancement.resultError || job?.resultError) && (
        <Alert
          message="增强任务异常"
          description={enhancement.resultError || job?.resultError || '未知错误'}
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
        />
      )}
      {enhancement.userHint && (
        <Alert message={`用户意图：${enhancement.userHint}`} type="info" style={{ marginBottom: 16 }} />
      )}
      {enhancement.safetyFlags.length > 0 && (
        <Alert
          message="检测到高风险操作，请人工确认"
          description={
            <List
              size="small"
              dataSource={enhancement.safetyFlags}
              renderItem={(flag: SafetyFlag) => (
                <List.Item>
                  步骤 {flag.stepIndex} ({flag.action})：{flag.reason}
                </List.Item>
              )}
            />
          }
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
        />
      )}
      {enhancement.suggestions.length > 0 && (
        <Alert
          message="AI 建议"
          description={
            <List
              size="small"
              dataSource={enhancement.suggestions}
              renderItem={(item: string) => <List.Item>{item}</List.Item>}
            />
          }
          type="warning"
          style={{ marginBottom: 16 }}
        />
      )}
      <Tabs activeKey={activeTab} onChange={(k) => setActiveTab(k as DiffTab)}>
        <Tabs.TabPane tab="增强后流程" key="enhanced">
          <RuleFlowList
            rule={previewEnhanced as RuleEnhancement['enhanced']}
            selectedPath={undefined}
            onSelectAction={() => {}}
          />
        </Tabs.TabPane>
        <Tabs.TabPane tab="Baseline 流程" key="baseline">
          <RuleFlowList
            rule={enhancement.baseline}
            selectedPath={undefined}
            onSelectAction={() => {}}
          />
        </Tabs.TabPane>
        <Tabs.TabPane tab="JSON Diff" key="jsonDiff">
          <RuleJsonDiff baseline={enhancement.baseline} enhanced={previewEnhanced} />
        </Tabs.TabPane>
        <Tabs.TabPane tab="Patch 编辑" key="patchEdit">
          <PatchEditor
            baseline={enhancement.baseline}
            patch={enhancement.patch}
            onChange={(selected, preview) => {
              setActivePatch(selected);
              setPreviewEnhanced(preview);
            }}
          />
        </Tabs.TabPane>
      </Tabs>
      <Text type="secondary">
        输入 Token：{enhancement.inputTokens} / 输出 Token：{enhancement.outputTokens}
        {(enhancement.patch?.length ?? 0) > 0 && (
          <span data-testid="active-patch-count">
            {' '}· 已启用 {activePatch.length} / {enhancement.patch?.length ?? 0} 条 patch
          </span>
        )}
      </Text>
    </Card>
  );
}
