import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert, Button, Card, DatePicker, Form, Input, InputNumber, List, Radio, Select, Space, Spin, Switch, Typography, message,
} from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { createSchedule, createTask, listRules, listRuleVersions, previewSchedule } from '../api/client';
import type {
  CatchupMode, JSONSchema, Priority, Rule, RuleVersion, RuleVersionContract,
} from '../api/types';
import SchemaInputForm, {
  normalizeSchemaInputs, requiredSecretInputs, schemaFormDefaults,
} from '../components/SchemaInputForm';
import { formatTime, toRFC3339 } from '../utils/time';

const { Title, Text } = Typography;
const { Option } = Select;

type Mode = 'once' | 'scheduled';

const cronPresets = [
  { label: '每分钟', value: '* * * * *' },
  { label: '每5分钟', value: '*/5 * * * *' },
  { label: '每15分钟', value: '*/15 * * * *' },
  { label: '每小时整点', value: '0 * * * *' },
  { label: '每天0点', value: '0 0 * * *' },
  { label: '每周一0点', value: '0 0 * * 1' },
  { label: '每月1日0点', value: '0 0 1 * *' },
  { label: '自定义', value: '' },
];

interface CommonFormValues {
  ruleId: string;
  ruleVersionNumber?: number;
  priority: Priority;
  maxRetries: number;
  variables?: Record<string, unknown>;
  browserProfileId?: string;
}

interface OnceFormValues extends CommonFormValues {
  scheduledAt?: import('dayjs').Dayjs;
}

interface ScheduledFormValues extends CommonFormValues {
  name: string;
  expression: string;
  timezone: string;
  catchup: CatchupMode;
  enabled: boolean;
}

function legacyInputSchema(rule?: Rule): JSONSchema | undefined {
  if (!rule) return undefined;
  const properties: Record<string, JSONSchema> = {};
  Object.entries(rule.variables ?? {}).forEach(([name, value]) => {
    const type = Array.isArray(value) ? 'array' : value === null ? 'null' : typeof value;
    const supported = ['string', 'number', 'boolean', 'object', 'array', 'null'].includes(type) ? type : 'string';
    properties[name] = { type: supported as JSONSchema['type'], default: value };
  });
  return { type: 'object', properties, required: [], additionalProperties: false };
}

export default function TaskCreate() {
  const navigate = useNavigate();
  const [mode, setMode] = useState<Mode>('once');
  const [rules, setRules] = useState<Rule[]>([]);
  const [selectedRuleId, setSelectedRuleId] = useState('');
  const [versions, setVersions] = useState<RuleVersion[]>([]);
  const [contracts, setContracts] = useState<RuleVersionContract[]>([]);
  const [selectedVersionNumber, setSelectedVersionNumber] = useState<number>();
  const [isVersionedRule, setIsVersionedRule] = useState(false);
  const [versionLoading, setVersionLoading] = useState(false);
  const [versionNotice, setVersionNotice] = useState('');
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [onceForm] = Form.useForm<OnceFormValues>();
  const [scheduledForm] = Form.useForm<ScheduledFormValues>();
  const [cronPreview, setCronPreview] = useState<{ expression: string; timezone: string; runs: string[] } | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const [scheduledExpression, setScheduledExpression] = useState('');
  const [scheduledTimezone, setScheduledTimezone] = useState('UTC');
  const versionRequest = useRef(0);

  const approvedRules = rules.filter((rule) => rule.approvalStatus === 'approved');
  const selectedRule = rules.find((rule) => rule.id === selectedRuleId);
  const selectedContract = contracts.find((contract) => contract.version === selectedVersionNumber);
  const selectedSchema = selectedContract?.inputSchema ?? legacyInputSchema(selectedRule);
  const blockedSecrets = requiredSecretInputs(selectedSchema);
  const noApprovedVersion = isVersionedRule && versions.length === 0;
  const activeForm = mode === 'once' ? onceForm : scheduledForm;

  useEffect(() => {
    const fetchRules = async () => {
      setLoading(true);
      try {
        const response = await listRules({ limit: 1000 });
        setRules(response.rules);
      } catch (error) {
        message.error(error instanceof Error ? error.message : '加载规则失败');
      } finally {
        setLoading(false);
      }
    };
    void fetchRules();
  }, []);

  useEffect(() => {
    if (mode !== 'scheduled' || !scheduledExpression.trim()) {
      setCronPreview(null);
      setPreviewError('');
      return;
    }
    setPreviewLoading(true);
    setPreviewError('');
    const timer = window.setTimeout(() => {
      previewSchedule(scheduledExpression, 5, scheduledTimezone || 'UTC')
        .then(setCronPreview)
        .catch((error) => {
          setCronPreview(null);
          setPreviewError(error instanceof Error ? error.message : '预览失败');
        })
        .finally(() => setPreviewLoading(false));
    }, 300);
    return () => window.clearTimeout(timer);
  }, [mode, scheduledExpression, scheduledTimezone]);

  const applyVersion = (
    versionNumber: number | undefined,
    availableContracts = contracts,
    schemaOverride?: JSONSchema,
  ) => {
    setSelectedVersionNumber(versionNumber);
    const contract = availableContracts.find((item) => item.version === versionNumber);
    const schema = contract?.inputSchema ?? schemaOverride;
    activeForm.setFieldValue('ruleVersionNumber', versionNumber);
    activeForm.setFieldValue('variables', schemaFormDefaults(schema));
    activeForm.setFieldValue('browserProfileId', contract?.browserProfileId || undefined);
  };

  const handleRuleChange = async (ruleId: string) => {
    const requestNumber = ++versionRequest.current;
    const rule = rules.find((item) => item.id === ruleId);
    setSelectedRuleId(ruleId);
    setVersions([]);
    setContracts([]);
    setSelectedVersionNumber(undefined);
    setIsVersionedRule(false);
    setVersionNotice('');
    activeForm.setFieldsValue({ ruleVersionNumber: undefined, variables: {}, browserProfileId: undefined });
    if (!rule) return;

    setVersionLoading(true);
    try {
      const response = await listRuleVersions(ruleId);
      if (requestNumber !== versionRequest.current) return;
      const approved = response.ruleVersions.filter((version) => version.status === 'approved');
      setVersions(approved);
      setContracts(response.contracts ?? []);
      setIsVersionedRule(response.ruleVersions.length > 0);
      if (approved.length > 0) {
        applyVersion(approved[0].version, response.contracts ?? []);
      } else if (response.ruleVersions.length > 0) {
        setVersionNotice('This rule has immutable versions, but none is approved for execution.');
      } else {
        const schema = legacyInputSchema(rule);
        setVersionNotice('This legacy rule has no immutable version. Its catalog defaults will be used.');
        applyVersion(undefined, [], schema);
      }
    } catch {
      if (requestNumber !== versionRequest.current) return;
      const schema = legacyInputSchema(rule);
      setVersionNotice('Immutable version data is unavailable. Using the legacy catalog contract.');
      applyVersion(undefined, [], schema);
    } finally {
      if (requestNumber === versionRequest.current) setVersionLoading(false);
    }
  };

  const taskBinding = (values: CommonFormValues) => {
    const version = versions.find((item) => item.version === values.ruleVersionNumber);
    return {
      ruleId: values.ruleId,
      ruleVersion: version ? undefined : selectedRule?.version,
      ruleVersionNumber: version?.version,
      variables: normalizeSchemaInputs(selectedSchema, values.variables),
      browserProfileId: values.browserProfileId?.trim() || undefined,
      priority: values.priority,
      maxRetries: values.maxRetries,
    };
  };

  const handleCreateOnce = async (values: OnceFormValues) => {
    if (!selectedRule || noApprovedVersion || blockedSecrets.length > 0) return;
    setSubmitting(true);
    try {
      await createTask({
        ...taskBinding(values),
        scheduledAt: values.scheduledAt ? toRFC3339(values.scheduledAt) : undefined,
      });
      message.success('创建成功');
      navigate('/tasks');
    } catch (error) {
      message.error(error instanceof Error ? error.message : '创建失败');
    } finally {
      setSubmitting(false);
    }
  };

  const handleCreateScheduled = async (values: ScheduledFormValues) => {
    if (!selectedRule || noApprovedVersion || blockedSecrets.length > 0) return;
    setSubmitting(true);
    try {
      await createSchedule({
        ...taskBinding(values),
        name: values.name,
        type: 'cron',
        expression: values.expression,
        timezone: values.timezone,
        enabled: values.enabled,
        catchup: values.catchup,
      });
      message.success('创建成功');
      navigate('/schedules');
    } catch (error) {
      message.error(error instanceof Error ? error.message : '创建失败');
    } finally {
      setSubmitting(false);
    }
  };

  const versionSummary = useMemo(() => {
    if (!selectedVersionNumber) return null;
    const version = versions.find((item) => item.version === selectedVersionNumber);
    return version ? `v${version.version} · ${version.versionLabel} · ${version.contentHash.slice(0, 12)}` : null;
  }, [selectedVersionNumber, versions]);

  const commonFields = () => (
    <>
      <Form.Item name="ruleId" label="选择规则模板" rules={[{ required: true, message: '请选择规则模板' }]}>
        <Select
          placeholder="请选择规则"
          loading={loading}
          showSearch
          optionFilterProp="children"
          onChange={(value) => void handleRuleChange(value)}
          filterOption={(input, option) => String(option?.children).toLowerCase().includes(input.toLowerCase())}
        >
          {approvedRules.map((rule) => <Option key={rule.id} value={rule.id}>{rule.name} ({rule.id})</Option>)}
        </Select>
      </Form.Item>

      {versionLoading && <Spin size="small" />}
      {selectedRuleId && versions.length > 0 && (
        <Form.Item
          name="ruleVersionNumber"
          label="Immutable rule version"
          rules={[{ required: true, message: 'Select an approved rule version' }]}
          extra={versionSummary ? <Text type="secondary">{versionSummary}</Text> : undefined}
        >
          <Select onChange={(value) => applyVersion(value)}>
            {versions.map((version) => (
              <Option key={version.version} value={version.version}>v{version.version} ({version.versionLabel})</Option>
            ))}
          </Select>
        </Form.Item>
      )}
      {versionNotice && <Alert showIcon type={noApprovedVersion ? 'error' : 'warning'} message={versionNotice} style={{ marginBottom: 24 }} />}

      {selectedRuleId && (
        <Card size="small" title="Task inputs" style={{ marginBottom: 24 }}>
          <SchemaInputForm schema={selectedSchema} />
        </Card>
      )}
      {blockedSecrets.length > 0 && (
        <Alert
          type="error"
          showIcon
          message="Required secret references are not configured"
          description={`Cannot create this task until approved secret references are available for: ${blockedSecrets.join(', ')}`}
          style={{ marginBottom: 24 }}
        />
      )}

      {selectedRuleId && (
        <Form.Item
          name="browserProfileId"
          label="Browser profile ID（可选）"
          extra="Leave empty to use the immutable rule version's profile. This is a reference only; credentials are never exposed."
        >
          <Input placeholder={selectedContract?.browserProfileId || 'default profile'} />
        </Form.Item>
      )}

      <Form.Item name="priority" label="优先级" rules={[{ required: true, message: '请选择优先级' }]}>
        <Select><Option value="high">高</Option><Option value="normal">普通</Option><Option value="low">低</Option></Select>
      </Form.Item>
      <Form.Item name="maxRetries" label="最大重试次数" rules={[{ required: true, message: '请输入最大重试次数' }]}>
        <InputNumber min={0} max={10} style={{ width: '100%' }} />
      </Form.Item>
    </>
  );

  const submitDisabled = versionLoading || noApprovedVersion || blockedSecrets.length > 0;

  return (
    <div>
      <Title level={3}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/tasks')} style={{ marginRight: 12 }}>返回</Button>
        创建任务
      </Title>
      <Card>
        <Radio.Group
          value={mode}
          onChange={(event) => {
            versionRequest.current += 1;
            setMode(event.target.value as Mode);
            setVersionLoading(false);
            setSelectedRuleId('');
            setVersions([]);
            setContracts([]);
            setSelectedVersionNumber(undefined);
            setVersionNotice('');
            setIsVersionedRule(false);
          }}
          style={{ marginBottom: 24 }}
        >
          <Radio.Button value="once">单次执行</Radio.Button>
          <Radio.Button value="scheduled">定时执行</Radio.Button>
        </Radio.Group>

        {mode === 'once' ? (
          <Form
            key="once"
            form={onceForm}
            layout="vertical"
            initialValues={{ priority: 'normal', maxRetries: 3, variables: {} }}
            onFinish={handleCreateOnce}
            style={{ maxWidth: 680 }}
          >
            {commonFields()}
            <Form.Item name="scheduledAt" label="执行时间（可选，留空立即执行）">
              <DatePicker showTime style={{ width: '100%' }} placeholder="选择执行时间" />
            </Form.Item>
            <Form.Item><Space><Button type="primary" htmlType="submit" loading={submitting} disabled={submitDisabled}>创建任务</Button><Button onClick={() => navigate('/tasks')}>取消</Button></Space></Form.Item>
          </Form>
        ) : (
          <Form
            key="scheduled"
            form={scheduledForm}
            layout="vertical"
            initialValues={{ priority: 'normal', maxRetries: 3, catchup: 'skip', enabled: true, timezone: 'UTC', variables: {} }}
            onFinish={handleCreateScheduled}
            onValuesChange={(_changed, values) => {
              setScheduledExpression(values.expression ?? '');
              setScheduledTimezone(values.timezone ?? 'UTC');
            }}
            style={{ maxWidth: 680 }}
          >
            {commonFields()}
            <Form.Item name="name" label="计划名称" rules={[{ required: true, message: '请输入计划名称' }]}><Input /></Form.Item>
            <Form.Item label="常用 Cron 预设">
              <Select
                aria-label="常用 Cron 预设"
                placeholder="选择预设或自定义"
                allowClear
                value={cronPresets.some((preset) => preset.value === scheduledExpression) ? scheduledExpression : undefined}
                onChange={(value) => scheduledForm.setFieldValue('expression', value ?? '')}
              >
                {cronPresets.map((preset) => <Option key={preset.value} value={preset.value}>{preset.label}</Option>)}
              </Select>
            </Form.Item>
            <Form.Item name="expression" label="Cron 表达式" rules={[{ required: true, message: '请输入 Cron 表达式' }]}><Input placeholder="*/5 * * * *" /></Form.Item>
            <Form.Item name="timezone" label="IANA timezone" rules={[{ required: true, message: 'Enter an IANA timezone' }]}><Input placeholder="UTC" /></Form.Item>
            {previewLoading && <Form.Item><Spin size="small" /> 正在计算下次执行时间…</Form.Item>}
            {previewError && <Form.Item><Alert message={previewError} type="error" showIcon /></Form.Item>}
            {cronPreview && !previewLoading && !previewError && (
              <Form.Item label={`下次执行时间 (${cronPreview.timezone})`}>
                <List size="small" bordered dataSource={cronPreview.runs} renderItem={(run) => <List.Item>{formatTime(run)}</List.Item>} />
              </Form.Item>
            )}
            <Form.Item name="catchup" label="漏跑策略" rules={[{ required: true, message: '请选择漏跑策略' }]}>
              <Select><Option value="skip">跳过</Option><Option value="run_once">补跑一次</Option></Select>
            </Form.Item>
            <Form.Item name="enabled" label="启用状态" valuePropName="checked"><Switch checkedChildren="启用" unCheckedChildren="禁用" /></Form.Item>
            <Form.Item><Space><Button type="primary" htmlType="submit" loading={submitting} disabled={submitDisabled}>创建调度计划</Button><Button onClick={() => navigate('/schedules')}>取消</Button></Space></Form.Item>
          </Form>
        )}
      </Card>
    </div>
  );
}
