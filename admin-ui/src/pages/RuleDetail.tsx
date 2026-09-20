import React, { useEffect, useMemo, useState, Suspense, useCallback, useRef } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  Card,
  Descriptions,
  Button,
  Spin,
  message,
  Tag,
  Typography,
  Tabs,
  Alert,
  Space,
  Modal,
  Drawer,
  Input,
} from 'antd';
import { ArrowLeftOutlined, UnorderedListOutlined, RobotOutlined } from '@ant-design/icons';
import { getRule, updateRule, approveRule, rejectRule, deleteRule, getRuleEnhancement, acceptEnhancement, rejectEnhancement, enhanceRule, getEnhancementJob } from '../api/client';
import type { Rule, Action, ActionType } from '../types/rule';
import type { RuleEnhancement, LLMJob } from '../api/types';
import StatusBadge, { PriorityTag } from '../components/StatusBadge';
import ConfirmModal from '../components/ConfirmModal';
import RuleFlowList from '../components/RuleFlowList';
import RuleEnhanceDiff from '../components/RuleEnhanceDiff';
import RuleEditorToolbar from '../components/RuleEditorToolbar';
import ActionEditorPanel from '../components/ActionEditorPanel';
import { formatTime } from '../utils/time';
import { buildFlowData, type FlowData, type FlowNode } from '../utils/ruleToGraph';
import {
  cloneRule,
  cloneAction,
  getActionByPath,
  getValueByPath,
  updateActionByPath,
  removeActionByPath,
  insertActionByPath,
  moveActionByPath,
  type ActionPath,
} from '../utils/ruleEdit';
import { validateRule } from '../utils/ruleValidate';

const { Title } = Typography;

const RuleFlowGraph = React.lazy(() => import('../components/RuleFlowGraph'));

const ACTION_TYPE_OPTIONS: { type: ActionType; label: string }[] = [
  { type: 'click', label: '点击' },
  { type: 'type', label: '输入文本' },
  { type: 'navigate', label: '页面跳转' },
  { type: 'waitFor', label: '等待元素' },
  { type: 'extract', label: '提取数据' },
  { type: 'evaluate', label: '执行脚本' },
  { type: 'if', label: '条件判断' },
  { type: 'loop', label: '循环' },
  { type: 'group', label: '步骤组' },
];

function createDefaultAction(type: ActionType): Action {
  switch (type) {
    case 'click':
      return { action: 'click', target: { selector: '' } };
    case 'type':
      return { action: 'type', target: { selector: '' }, value: '' };
    case 'navigate':
      return { action: 'navigate', url: '' };
    case 'waitFor':
      return { action: 'waitFor', target: { selector: '' } };
    case 'extract':
      return { action: 'extract', target: { selector: '' }, name: '' };
    case 'evaluate':
      return { action: 'evaluate', script: '' };
    case 'if':
      return {
        action: 'if',
        condition: { type: 'elementExists', target: { selector: '' } },
        then: [],
        else: [],
      };
    case 'loop':
      return { action: 'loop', type: 'count', count: 1, steps: [] };
    case 'group':
      return { action: 'group', steps: [] };
    default:
      return { action: type };
  }
}

function getSiblingCount(rule: Rule, path: ActionPath): number | undefined {
  if (path.length === 0) return undefined;
  const parentPath = path.slice(0, -1);
  const array = getValueByPath<Action[]>(rule, parentPath);
  if (!Array.isArray(array)) return undefined;
  return array.length;
}

function computeNewPath(
  selectedPath: ActionPath,
  position: 'before' | 'after' | 'append',
  parentArrayLength: number,
): ActionPath {
  const parentPath = selectedPath.slice(0, -1);
  const index = selectedPath[selectedPath.length - 1] as number;
  if (position === 'before') return [...parentPath, index];
  if (position === 'after') return [...parentPath, index + 1];
  return [...parentPath, parentArrayLength];
}

// NOTE: JSON.stringify is used here for simplicity. It is sufficient for comparing
// serializable rule objects in the admin UI, but it ignores key ordering and cannot
// compare functions, undefined values, or circular structures. Consider replacing
// with a dedicated deep-equal library (e.g. fast-deep-equal) if needs grow.
function isDeepEqual(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function MetadataCard({ rule }: { rule: Rule }) {
  return (
    <Card title="规则元数据">
      <Descriptions bordered column={2}>
        <Descriptions.Item label="规则 ID">{rule.id}</Descriptions.Item>
        <Descriptions.Item label="版本">{rule.version}</Descriptions.Item>
        <Descriptions.Item label="名称">{rule.name}</Descriptions.Item>
        <Descriptions.Item label="域名">{typeof rule.domain === 'string' ? rule.domain : JSON.stringify(rule.domain)}</Descriptions.Item>
        <Descriptions.Item label="入口 URL">{rule.entry}</Descriptions.Item>
        <Descriptions.Item label="优先级"><PriorityTag priority={rule.priority} /></Descriptions.Item>
        <Descriptions.Item label="启用状态"><Tag color={rule.enabled ? 'green' : 'default'}>{rule.enabled ? '已启用' : '已停用'}</Tag></Descriptions.Item>
        <Descriptions.Item label="审批状态"><StatusBadge status={rule.approvalStatus} /></Descriptions.Item>
        <Descriptions.Item label="来源"><Tag color="blue">{rule.source || 'pageagent'}</Tag></Descriptions.Item>
        <Descriptions.Item label="创建时间">{formatTime(rule.createdAt)}</Descriptions.Item>
        <Descriptions.Item label="更新时间">{formatTime(rule.updatedAt)}</Descriptions.Item>
      </Descriptions>
    </Card>
  );
}

export default function RuleDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [rule, setRule] = useState<Rule | null>(null);
  const [draftRule, setDraftRule] = useState<Rule | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState('flow');
  const [flowData, setFlowData] = useState<FlowData | null>(null);
  const [graphError, setGraphError] = useState<Error | null>(null);
  const [forceList, setForceList] = useState(false);
  const [selectedPath, setSelectedPath] = useState<ActionPath | undefined>(undefined);
  const [editorOpen, setEditorOpen] = useState(false);
  const [actionTypeSelectorOpen, setActionTypeSelectorOpen] = useState(false);
  const [pendingInsertPosition, setPendingInsertPosition] = useState<'before' | 'after' | 'append'>('after');
  const [isSaving, setIsSaving] = useState(false);
  const [confirm, setConfirm] = useState<{ open: boolean; title: string; content: string; onOk: () => void } | null>(null);
  const [enhancement, setEnhancement] = useState<RuleEnhancement | null>(null);
  const [enhancementLoading, setEnhancementLoading] = useState(false);
  const [job, setJob] = useState<LLMJob | null>(null);
  const [pollingJobId, setPollingJobId] = useState<string | null>(null);
  const [enhanceModalOpen, setEnhanceModalOpen] = useState(false);
  const [userHint, setUserHint] = useState('');
  const [isEnhancing, setIsEnhancing] = useState(false);
  const pollRef = useRef<number | null>(null);

  const loadEnhancement = useCallback(async (ruleId: string) => {
    try {
      const e = await getRuleEnhancement(ruleId);
      setEnhancement(e.status === 'pending' ? e : null);
    } catch {
      setEnhancement(null);
    }
  }, []);

  const fetchRule = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError(null);
    try {
      const r = (await getRule(id)) as Rule;
      setRule(r);
      setDraftRule(cloneRule(r));
      setSelectedPath(undefined);
      setEditorOpen(false);
      if (r.approvalStatus === 'pending') {
        await loadEnhancement(r.id);
      } else {
        setEnhancement(null);
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : '加载规则失败';
      setError(msg);
      message.error(msg);
    } finally {
      setLoading(false);
    }
  }, [id, loadEnhancement]);

  useEffect(() => {
    fetchRule();
  }, [fetchRule]);

  useEffect(() => {
    if (!draftRule) return;
    try {
      setFlowData(buildFlowData(draftRule));
      setGraphError(null);
    } catch (err) {
      const e = err instanceof Error ? err : new Error(String(err));
      setGraphError(e);
      setFlowData(null);
    }
  }, [draftRule]);

  useEffect(() => {
    if (!pollingJobId) return;

    let cancelled = false;
    const poll = async () => {
      try {
        const j = await getEnhancementJob(pollingJobId);
        if (cancelled) return;
        setJob(j);
        if (j.status === 'completed' || j.status === 'failed') {
          setPollingJobId(null);
          if (j.status === 'completed') {
            await loadEnhancement(j.ruleId);
            message.success('增强完成');
          } else {
            message.error(`增强失败：${j.resultError || '未知错误'}`);
          }
        }
      } catch (err) {
        if (cancelled) return;
        message.error(err instanceof Error ? err.message : '轮询任务状态失败');
        setPollingJobId(null);
      }
    };

    poll();
    const interval = window.setInterval(poll, 1500);
    pollRef.current = interval;
    return () => {
      cancelled = true;
      window.clearInterval(interval);
      pollRef.current = null;
    };
  }, [pollingJobId, loadEnhancement]);

  const isDirty = useMemo(() => {
    if (!rule || !draftRule) return false;
    return !isDeepEqual(rule, draftRule);
  }, [rule, draftRule]);

  const siblingCount = useMemo(() => {
    if (!draftRule || !selectedPath) return undefined;
    return getSiblingCount(draftRule, selectedPath);
  }, [draftRule, selectedPath]);

  const selectedAction = useMemo(() => {
    if (!draftRule || !selectedPath) return undefined;
    return getActionByPath(draftRule, selectedPath);
  }, [draftRule, selectedPath]);

  useEffect(() => {
    if (editorOpen && !selectedAction) {
      setEditorOpen(false);
    }
  }, [editorOpen, selectedAction]);

  // Warn before closing/reloading the page when there are unsaved changes.
  useEffect(() => {
    const handler = (e: BeforeUnloadEvent) => {
      if (isDirty) {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [isDirty]);

  const toggleEnabled = async () => {
    if (!rule) return;
    // H-3: guard against discarding unsaved draft edits — fetchRule() below
    // unconditionally replaces draftRule, silently losing user changes.
    if (isDirty) {
      message.warning('有未保存的修改，请先保存或撤销后再切换启用状态');
      return;
    }
    try {
      await updateRule(rule.id, { enabled: !rule.enabled });
      message.success('状态已更新');
      fetchRule();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '更新失败');
    }
  };

  // H-3: approve/reject also refetch (discarding drafts) and previously had
  // no error handling on the fire-and-forget promise chain.
  const handleApprove = async () => {
    if (!rule) return;
    if (isDirty) {
      message.warning('有未保存的修改，请先保存或撤销后再审批');
      return;
    }
    try {
      await approveRule(rule.id);
      fetchRule();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '审批失败');
    }
  };

  const handleReject = async () => {
    if (!rule) return;
    if (isDirty) {
      message.warning('有未保存的修改，请先保存或撤销后再拒绝');
      return;
    }
    try {
      await rejectRule(rule.id);
      fetchRule();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '拒绝失败');
    }
  };

  const handleStartEnhance = () => {
    setUserHint('');
    setEnhanceModalOpen(true);
  };

  const handleEnhance = async () => {
    if (!rule) return;
    setIsEnhancing(true);
    try {
      const resp = await enhanceRule({
        recording: {},
        baselineRule: rule,
        userHint,
      });
      setEnhanceModalOpen(false);
      setPollingJobId(resp.jobId);
      message.info('已提交 AI 增强任务，正在处理…');
    } catch (err) {
      message.error(err instanceof Error ? err.message : '提交增强失败');
    } finally {
      setIsEnhancing(false);
    }
  };

  const handleAcceptEnhancement = async () => {
    if (!rule) return;
    setEnhancementLoading(true);
    try {
      await acceptEnhancement(rule.id);
      message.success('已接受增强');
      setJob(null);
      fetchRule();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '接受失败');
    } finally {
      setEnhancementLoading(false);
    }
  };

  const handleRejectEnhancement = async () => {
    if (!rule) return;
    setEnhancementLoading(true);
    try {
      await rejectEnhancement(rule.id);
      message.success('已拒绝并删除');
      setJob(null);
      navigate('/rules');
    } catch (err) {
      message.error(err instanceof Error ? err.message : '拒绝失败');
    } finally {
      setEnhancementLoading(false);
    }
  };

  const handleSelectNode = useCallback((node: FlowNode) => {
    if (node.data.path.length === 0) {
      setSelectedPath(undefined);
      return;
    }
    setSelectedPath(node.data.path);
  }, []);

  const handleEditNode = useCallback((node: FlowNode) => {
    if (node.data.path.length === 0) {
      setSelectedPath(undefined);
      setEditorOpen(false);
      return;
    }
    setSelectedPath(node.data.path);
    setEditorOpen(true);
  }, []);

  const handleSelectAction = useCallback((path: ActionPath) => {
    setSelectedPath(path);
  }, []);

  const handleAddAction = useCallback((position: 'before' | 'after' | 'append') => {
    setPendingInsertPosition(position);
    setActionTypeSelectorOpen(true);
  }, []);

  const handleSelectActionType = useCallback((type: ActionType) => {
    if (!draftRule) return;
    setActionTypeSelectorOpen(false);
    const defaultAction = createDefaultAction(type);
    let nextRule: Rule;
    let newPath: ActionPath;

    if (pendingInsertPosition === 'append' || !selectedPath) {
      const currentSteps = draftRule.steps ?? [];
      nextRule = { ...draftRule, steps: [...currentSteps, defaultAction] };
      newPath = ['steps', currentSteps.length];
    } else {
      const parentPath = selectedPath.slice(0, -1);
      const array = getValueByPath<Action[]>(draftRule, parentPath);
      newPath = computeNewPath(selectedPath, pendingInsertPosition, array?.length ?? 0);
      nextRule = insertActionByPath(draftRule, selectedPath, pendingInsertPosition, defaultAction);
    }

    setDraftRule(nextRule);
    setSelectedPath(newPath);
    setEditorOpen(true);
  }, [draftRule, pendingInsertPosition, selectedPath]);

  const handleDeleteAction = useCallback(() => {
    if (!draftRule || !selectedPath) return;
    setDraftRule(removeActionByPath(draftRule, selectedPath));
    setSelectedPath(undefined);
    setEditorOpen(false);
  }, [draftRule, selectedPath]);

  const handleMoveAction = useCallback((direction: 'up' | 'down') => {
    if (!draftRule || !selectedPath) return;
    const parentPath = selectedPath.slice(0, -1);
    const index = selectedPath[selectedPath.length - 1] as number;
    const array = getValueByPath<Action[]>(draftRule, parentPath);
    if (!Array.isArray(array)) return;
    const newIndex = direction === 'up' ? index - 1 : index + 1;
    if (newIndex < 0 || newIndex >= array.length) return;
    setDraftRule(moveActionByPath(draftRule, selectedPath, direction));
    setSelectedPath([...parentPath, newIndex]);
  }, [draftRule, selectedPath]);

  const handleCloneAction = useCallback(() => {
    if (!draftRule || !selectedPath) return;
    const action = getActionByPath(draftRule, selectedPath);
    if (!action) return;
    const clone = cloneAction(action, {
      id: undefined,
      description: action.description ? `${action.description}（副本）` : action.description,
    });
    const parentPath = selectedPath.slice(0, -1);
    const array = getValueByPath<Action[]>(draftRule, parentPath);
    const newPath = computeNewPath(selectedPath, 'after', array?.length ?? 0);
    setDraftRule(insertActionByPath(draftRule, selectedPath, 'after', clone));
    setSelectedPath(newPath);
    setEditorOpen(true);
  }, [draftRule, selectedPath]);

  const handleUndo = useCallback(() => {
    if (!rule) return;
    setDraftRule(cloneRule(rule));
    setSelectedPath(undefined);
    setEditorOpen(false);
  }, [rule]);

  const handleActionChange = useCallback((action: Action) => {
    if (!draftRule || !selectedPath) return;
    setDraftRule(updateActionByPath(draftRule, selectedPath, action));
    setEditorOpen(false);
  }, [draftRule, selectedPath]);

  const handleSave = useCallback(async () => {
    if (!draftRule || !rule) return;
    // Normalize legacy fields before validation and save.
    const ruleToSave: Rule = {
      ...draftRule,
      priority: draftRule.priority || 'normal',
    };
    const validation = validateRule(ruleToSave);
    if (!validation.valid) {
      message.error(`校验失败：${validation.errors.join('；')}`);
      return;
    }
    setIsSaving(true);
    try {
      const updated = (await updateRule(rule.id, ruleToSave)) as Rule;
      setRule(updated);
      setDraftRule(cloneRule(updated));
      setSelectedPath(undefined);
      setEditorOpen(false);
      message.success('保存成功');
    } catch (err) {
      message.error(err instanceof Error ? err.message : '保存失败');
    } finally {
      setIsSaving(false);
    }
  }, [draftRule, rule]);

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 64 }}>
        <Spin size="large" />
      </div>
    );
  }

  if (error || !rule || !draftRule) {
    return (
      <div>
        <Title level={3}>
          <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/rules')} style={{ marginRight: 12 }}>
            返回
          </Button>
          规则详情
        </Title>
        <Alert type="error" message={error || '加载规则失败'} showIcon />
      </div>
    );
  }

  const tabItems = [
    {
      key: 'flow',
      label: '流程图',
      children: (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16, height: 640 }}>
          <RuleEditorToolbar
            selectedPath={selectedPath}
            siblingCount={siblingCount}
            isDirty={isDirty}
            isSaving={isSaving}
            canUndo={isDirty}
            onAddAction={handleAddAction}
            onDeleteAction={handleDeleteAction}
            onMoveAction={handleMoveAction}
            onCloneAction={handleCloneAction}
            onUndo={handleUndo}
            onSave={handleSave}
          />
          <div style={{ display: 'flex', gap: 16, flex: 1, minHeight: 0 }}>
            <Card
              style={{ flex: 1, overflow: 'hidden' }}
              title={
                <Space>
                  <span>操作过程</span>
                  <Button
                    size="small"
                    icon={<UnorderedListOutlined />}
                    onClick={() => setForceList((v) => !v)}
                  >
                    {forceList || graphError ? '切换为 DAG' : '切换为列表'}
                  </Button>
                </Space>
              }
            >
              {(forceList || graphError) ? (
                <RuleFlowList
                  rule={draftRule}
                  selectedPath={selectedPath}
                  onSelectAction={handleSelectAction}
                />
              ) : (
                <Suspense fallback={(
                  <div style={{ position: 'relative', minHeight: 200 }}>
                    <Spin tip="加载图引擎…" fullscreen />
                  </div>
                )}>
                  {flowData && (
                    <RuleFlowGraph
                      data={flowData}
                      selectedPath={selectedPath}
                      onSelectNode={handleSelectNode}
                      onEditNode={handleEditNode}
                      onError={(err) => setGraphError(err)}
                    />
                  )}
                </Suspense>
              )}
            </Card>
          </div>
        </div>
      ),
    },
    {
      key: 'metadata',
      label: '概览',
      children: <MetadataCard rule={rule} />,
    },
    {
      key: 'json',
      label: '原始 JSON',
      children: (
        <Card title="原始规则 JSON">
          <pre className="code-preview">{JSON.stringify(draftRule, null, 2)}</pre>
        </Card>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/rules')} style={{ marginRight: 12 }}>
          返回
        </Button>
        规则详情
      </Title>
      {(enhancement || job) && (
        <RuleEnhanceDiff
          enhancement={enhancement ?? {
            id: job?.id ?? '',
            ruleId: job?.ruleId ?? '',
            baseline: job?.baseline ?? rule,
            enhanced: job?.resultRule ?? rule,
            patch: job?.patch ?? [],
            userHint: job?.userHint ?? '',
            provider: job?.provider ?? '',
            model: job?.model ?? '',
            inputTokens: job?.inputTokens ?? 0,
            outputTokens: job?.outputTokens ?? 0,
            suggestions: job?.suggestions ?? [],
            safetyFlags: job?.safetyFlags ?? [],
            status: 'pending',
            resultError: job?.resultError ?? '',
            createdAt: job?.createdAt ?? '',
          }}
          job={job}
          onAccept={handleAcceptEnhancement}
          onReject={handleRejectEnhancement}
          loading={enhancementLoading}
        />
      )}
      <Card
        title={rule.name}
        extra={
          <>
            <Button onClick={toggleEnabled} style={{ marginRight: 8 }}>
              {rule.enabled ? '停用' : '启用'}
            </Button>
            {rule.approvalStatus === 'approved' && !enhancement && !job && (
              <Button icon={<RobotOutlined />} type="primary" onClick={handleStartEnhance} style={{ marginRight: 8 }}>
                AI 增强
              </Button>
            )}
            {rule.approvalStatus === 'pending' && !enhancement && !job && (
              <>
                <Button type="primary" onClick={handleApprove} style={{ marginRight: 8 }}>
                  通过
                </Button>
                <Button danger onClick={handleReject} style={{ marginRight: 8 }}>
                  拒绝
                </Button>
              </>
            )}
            <Button
              danger
              onClick={() =>
                setConfirm({
                  open: true,
                  title: '删除规则',
                  content: `确定删除规则 "${rule.name}" 吗？`,
                  onOk: async () => {
                    await deleteRule(rule.id);
                    navigate('/rules');
                  },
                })}
            >
              删除
            </Button>
          </>
        }
      >
        <Tabs activeKey={activeTab} onChange={setActiveTab} items={tabItems} />
      </Card>

      <Drawer
        title="编辑动作"
        placement="right"
        width={560}
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        destroyOnClose
      >
        {selectedAction ? (
          <ActionEditorPanel
            action={selectedAction}
            onChange={handleActionChange}
            onCancel={() => setEditorOpen(false)}
            disabled={isSaving}
          />
        ) : (
          <Alert message="未选择动作" type="warning" showIcon />
        )}
      </Drawer>

      <Modal
        title="选择动作类型"
        open={actionTypeSelectorOpen}
        onCancel={() => setActionTypeSelectorOpen(false)}
        footer={null}
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          {ACTION_TYPE_OPTIONS.map((opt) => (
            <Button
              key={String(opt.type)}
              data-testid={`action-type-${String(opt.type)}`}
              style={{ width: '100%', justifyContent: 'flex-start' }}
              onClick={() => handleSelectActionType(opt.type)}
            >
              {opt.label}
            </Button>
          ))}
        </Space>
      </Modal>

      <Modal
        title="AI 规则增强"
        open={enhanceModalOpen}
        onOk={handleEnhance}
        onCancel={() => setEnhanceModalOpen(false)}
        confirmLoading={isEnhancing}
        okText="提交"
        cancelText="取消"
        okButtonProps={{ 'data-testid': 'enhance-submit' as string }}
      >
        <p>输入用户意图或改进建议，AI 将基于当前规则生成增强版本。</p>
        <Input.TextArea
          rows={4}
          value={userHint}
          onChange={(e) => setUserHint(e.target.value)}
          placeholder="例如：抓取商品列表中的价格字段，并翻页 3 次"
        />
      </Modal>

      {confirm && (
        <ConfirmModal
          title={confirm.title}
          content={confirm.content}
          open={confirm.open}
          onConfirm={confirm.onOk}
          onCancel={() => setConfirm(null)}
        />
      )}
    </div>
  );
}
