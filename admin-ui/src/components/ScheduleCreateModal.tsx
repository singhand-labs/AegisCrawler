import { useEffect, useMemo, useState } from 'react';
import {
  Modal, Form, Input, Select, Switch, Typography, message,
} from 'antd';
import type { Rule, RuleVersion } from '../api/types';
import {
  createSchedule, listRuleVersions, previewSchedule, type CreateScheduleRequest,
} from '../api/client';

const { Text } = Typography;

const priorityOptions = [
  { value: 'low', label: '低' },
  { value: 'normal', label: '普通' },
  { value: 'high', label: '高' },
];

const catchupOptions = [
  { value: 'skip', label: '跳过' },
  { value: 'run_once', label: '补跑一次' },
];

interface ScheduleCreateModalProps {
  open: boolean;
  rules: Rule[];
  onClose: () => void;
  onCreated: () => void;
}

/**
 * Schedule creation form backed by POST /admin/schedules. Rule version options
 * come from the selected rule's immutable versions and only approved versions
 * are selectable — the server rejects schedules for unapproved versions.
 */
export default function ScheduleCreateModal({ open, rules, onClose, onCreated }: ScheduleCreateModalProps) {
  const [form] = Form.useForm();
  const [submitting, setSubmitting] = useState(false);
  const [versions, setVersions] = useState<RuleVersion[]>([]);
  const [versionsLoading, setVersionsLoading] = useState(false);
  const [preview, setPreview] = useState<string[]>([]);
  const [previewError, setPreviewError] = useState('');

  const approvedRules = useMemo(
    () => rules.filter((rule) => rule.approvalStatus === 'approved'),
    [rules],
  );

  const ruleId = Form.useWatch('ruleId', form);
  const expression = Form.useWatch('expression', form);
  const timezone = Form.useWatch('timezone', form);
  const scheduleType = Form.useWatch('type', form);

  useEffect(() => {
    if (!open) {
      form.resetFields();
      setVersions([]);
      setPreview([]);
      setPreviewError('');
    }
  }, [open, form]);

  useEffect(() => {
    let cancelled = false;
    if (!ruleId) {
      setVersions([]);
      return undefined;
    }
    setVersionsLoading(true);
    listRuleVersions(ruleId)
      .then((resp) => {
        if (cancelled) return;
        const approved = resp.ruleVersions.filter((v) => v.status === 'approved');
        setVersions(approved);
        const latest = approved[0];
        if (latest) {
          form.setFieldValue('ruleVersion', latest.versionLabel);
        } else {
          form.setFieldValue('ruleVersion', undefined);
        }
      })
      .catch(() => {
        if (!cancelled) setVersions([]);
      })
      .finally(() => {
        if (!cancelled) setVersionsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [ruleId, form]);

  useEffect(() => {
    let cancelled = false;
    if (scheduleType !== 'cron' || !expression || !timezone) {
      setPreview([]);
      setPreviewError('');
      return undefined;
    }
    const timer = setTimeout(() => {
      previewSchedule(expression, 3, timezone)
        .then((resp) => {
          if (cancelled) return;
          setPreview(resp.runs || []);
          setPreviewError('');
        })
        .catch((err) => {
          if (cancelled) return;
          setPreview([]);
          setPreviewError(err instanceof Error ? err.message : '表达式预览失败');
        });
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [expression, timezone, scheduleType]);

  const handleOk = async () => {
    let values;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    let variables: Record<string, unknown> = {};
    const rawVariables = String(values.variablesJson ?? '').trim();
    if (rawVariables && rawVariables !== '{}') {
      try {
        const parsed = JSON.parse(rawVariables);
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          throw new Error('变量必须是 JSON 对象');
        }
        variables = parsed as Record<string, unknown>;
      } catch (err) {
        message.error(err instanceof Error ? err.message : '变量 JSON 无效');
        return;
      }
    }
    setSubmitting(true);
    try {
      const request: CreateScheduleRequest = {
        name: values.name,
        ruleId: values.ruleId,
        ruleVersion: values.ruleVersion,
        type: values.type,
        expression: values.expression,
        timezone: values.timezone,
        browserProfileId: values.browserProfileId || undefined,
        variables,
        priority: values.priority,
        catchup: values.catchup,
        enabled: values.enabled ?? true,
      };
      await createSchedule(request);
      message.success('调度计划已创建');
      onCreated();
      onClose();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '创建调度计划失败');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open={open}
      title="新建调度计划"
      okText="创建"
      cancelText="取消"
      confirmLoading={submitting}
      onOk={handleOk}
      onCancel={onClose}
      destroyOnClose
      width={640}
    >
      <Form
        form={form}
        layout="vertical"
        initialValues={{
          type: 'cron',
          timezone: 'Asia/Shanghai',
          priority: 'normal',
          catchup: 'skip',
          enabled: true,
          variablesJson: '{}',
        }}
      >
        <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入调度名称' }]}>
          <Input placeholder="例如：公告采集-每5分钟" maxLength={120} />
        </Form.Item>
        <Form.Item name="ruleId" label="规则" rules={[{ required: true, message: '请选择规则' }]}>
          <Select
            placeholder="选择已批准的规则"
            showSearch
            optionFilterProp="children"
            notFoundContent={rules.length > 0 && approvedRules.length === 0 ? '没有已批准的规则' : undefined}
          >
            {approvedRules.map((rule) => (
              <Select.Option key={rule.id} value={rule.id}>
                {rule.name} ({rule.id})
              </Select.Option>
            ))}
          </Select>
        </Form.Item>
        <Form.Item
          name="ruleVersion"
          label="Immutable version"
          rules={[{ required: true, message: '请选择已批准的规则版本' }]}
          extra="仅已批准（approved）的不可变版本可发布任务"
        >
          <Select
            placeholder={ruleId ? (versionsLoading ? '加载版本中…' : '选择版本') : '请先选择规则'}
            disabled={!ruleId}
            loading={versionsLoading}
            notFoundContent={ruleId && !versionsLoading ? '该规则没有已批准版本' : undefined}
          >
            {versions.map((version) => (
              <Select.Option key={version.versionLabel} value={version.versionLabel}>
                v{version.version} ({version.versionLabel}) · {version.status}
              </Select.Option>
            ))}
          </Select>
        </Form.Item>
        <Form.Item name="type" label="类型" rules={[{ required: true }]}>
          <Select>
            <Select.Option value="cron">定时（cron）</Select.Option>
            <Select.Option value="once">单次</Select.Option>
          </Select>
        </Form.Item>
        <Form.Item
          name="expression"
          label={scheduleType === 'once' ? '执行时间' : 'Cron 表达式'}
          rules={[{ required: true, message: scheduleType === 'once' ? '请输入执行时间' : '请输入 cron 表达式' }]}
          extra={scheduleType === 'cron' ? '例如 */5 * * * * 表示每 5 分钟' : 'RFC3339 时间，例如 2026-01-02T15:04:05Z'}
        >
          <Input placeholder={scheduleType === 'once' ? '2026-01-02T15:04:05Z' : '*/5 * * * *'} />
        </Form.Item>
        {scheduleType === 'cron' && (preview.length > 0 || previewError) && (
          <Form.Item label="接下来 3 次执行时间" style={{ marginBottom: 12 }}>
            {previewError
              ? <Text type="danger">{previewError}</Text>
              : (
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {preview.map((run) => <li key={run}><Text type="secondary">{run}</Text></li>)}
                </ul>
              )}
          </Form.Item>
        )}
        <Form.Item name="timezone" label="Timezone">
          <Input placeholder="Asia/Shanghai" />
        </Form.Item>
        <Form.Item name="browserProfileId" label="Browser profile" extra="任务将只派发给匹配该 profile 的 worker">
          <Input placeholder="例如 current-chrome-profile" />
        </Form.Item>
        <Form.Item name="variablesJson" label="固定任务变量（JSON 对象）" extra="将按规则的 input schema 校验">
          <Input.TextArea rows={3} placeholder="{}" />
        </Form.Item>
        <Form.Item name="priority" label="优先级" style={{ display: 'inline-block', width: '48%', marginRight: '4%' }}>
          <Select options={priorityOptions} />
        </Form.Item>
        <Form.Item name="catchup" label="漏跑策略" style={{ display: 'inline-block', width: '48%' }}>
          <Select options={catchupOptions} />
        </Form.Item>
        <Form.Item name="enabled" label="创建后立即启用" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>
    </Modal>
  );
}
