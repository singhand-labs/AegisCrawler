import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useState as useReactState } from 'react';
import {
  Table, Button, Select, Space, Card, Typography, message, Popconfirm, Switch,
} from 'antd';
import { ReloadOutlined, PlayCircleOutlined, PlusOutlined } from '@ant-design/icons';
import {
  listSchedules, listRules, deleteSchedule, updateSchedule, triggerSchedule,
} from '../api/client';
import type { Schedule, Rule } from '../api/types';
import { PriorityTag } from '../components/StatusBadge';
import ScheduleCreateModal from '../components/ScheduleCreateModal';
import { formatTime } from '../utils/time';

const { Title } = Typography;
const { Option } = Select;

const typeMap: Record<string, string> = {
  once: '单次',
  cron: '定时',
};

const catchupMap: Record<string, string> = {
  skip: '跳过',
  run_once: '补跑一次',
};

export default function ScheduleList() {
  const navigate = useNavigate();
  const [schedules, setSchedules] = useState<Schedule[]>([]);
  const [rules, setRules] = useState<Rule[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState({ ruleId: '', enabled: '' });
  const [pagination, setPagination] = useState({ current: 1, pageSize: 20 });
  const [createOpen, setCreateOpen] = useReactState(false);

  const fetchSchedules = async (page = pagination.current, size = pagination.pageSize) => {
    setLoading(true);
    try {
      const resp = await listSchedules({
        rule_id: filters.ruleId,
        enabled: filters.enabled,
        limit: size,
        offset: (page - 1) * size,
      });
      setSchedules(resp.schedules);
      setTotal(resp.total);
    } catch (err) {
      message.error(err instanceof Error ? err.message : '加载调度计划失败');
    } finally {
      setLoading(false);
    }
  };

  const fetchRules = async () => {
    try {
      // listRules rejects limit > 500; page through approved rules so the
      // create-form picker sees every candidate even in busy workspaces.
      const collected: Rule[] = [];
      for (let offset = 0; offset < 5000; offset += 500) {
        const resp = await listRules({ limit: 500, offset });
        collected.push(...resp.rules);
        if (collected.length >= resp.total || resp.rules.length === 0) break;
      }
      setRules(collected);
    } catch (err) {
      message.error(err instanceof Error ? err.message : '加载规则失败');
    }
  };

  useEffect(() => {
    fetchSchedules(1, pagination.pageSize);
    fetchRules();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters, pagination.pageSize]);

  const handleTableChange = (p: { current?: number; pageSize?: number }) => {
    const current = p.current || 1;
    const pageSize = p.pageSize || 20;
    setPagination({ current, pageSize });
    fetchSchedules(current, pageSize);
  };

  const handleToggleEnabled = async (schedule: Schedule) => {
    try {
      await updateSchedule(schedule.id, { enabled: !schedule.enabled });
      message.success('状态已更新');
      fetchSchedules();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '更新失败');
    }
  };

  const handleTrigger = async (schedule: Schedule) => {
    try {
      const resp = await triggerSchedule(schedule.id);
      message.success(`已触发任务 ${resp.taskId.slice(0, 12)}...`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : '触发失败');
    }
  };

  const handleDelete = async (schedule: Schedule) => {
    try {
      await deleteSchedule(schedule.id);
      message.success('调度计划已删除');
      fetchSchedules();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '删除失败');
    }
  };

  const getRuleName = (ruleId: string) => {
    const rule = rules.find((r) => r.id === ruleId);
    return rule ? `${rule.name} (${ruleId})` : ruleId;
  };

  const columns = [
    { title: '名称', dataIndex: 'name', key: 'name' },
    {
      title: '规则',
      dataIndex: 'ruleId',
      key: 'ruleId',
      render: (v: string) => (
        <Button type="link" onClick={() => navigate(`/rules/${v}`)}>
          {getRuleName(v)}
        </Button>
      ),
    },
    {
      title: 'Immutable version',
      key: 'ruleVersionNumber',
      render: (_: unknown, record: Schedule) => record.ruleVersionNumber
        ? `v${record.ruleVersionNumber} (${record.ruleVersion})`
        : record.ruleVersion,
    },
    { title: '类型', dataIndex: 'type', key: 'type', render: (v: string) => typeMap[v] || v },
    { title: '表达式', dataIndex: 'expression', key: 'expression' },
    { title: 'Timezone', dataIndex: 'timezone', key: 'timezone', render: (v: string) => v || 'UTC' },
    { title: 'Browser profile', dataIndex: 'browserProfileId', key: 'browserProfileId', render: (v: string) => v || '-' },
    { title: '下次执行时间', dataIndex: 'nextRunAt', key: 'nextRunAt', render: formatTime },
    {
      title: '启用状态',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (v: boolean, record: Schedule) => (
        <Switch checked={v} onChange={() => handleToggleEnabled(record)} />
      ),
    },
    { title: '优先级', dataIndex: 'priority', key: 'priority', render: (v: string) => <PriorityTag priority={v} /> },
    { title: '漏跑策略', dataIndex: 'catchup', key: 'catchup', render: (v: string) => catchupMap[v] || v },
    { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime },
    {
      title: '操作',
      key: 'action',
      width: 220,
      render: (_: unknown, record: Schedule) => (
        <Space size="small">
          <Button type="link" size="small" icon={<PlayCircleOutlined />} onClick={() => handleTrigger(record)}>
            手动触发
          </Button>
          <Popconfirm
            title="删除调度计划"
            description={`确定删除调度计划 "${record.name}" 吗？`}
            onConfirm={() => handleDelete(record)}
            okText="确认"
            cancelText="取消"
          >
            <Button type="link" size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>调度计划</Title>
      <Card style={{ marginBottom: 24 }}>
        <Space wrap style={{ marginBottom: 16 }}>
          <Select
            placeholder="按规则筛选"
            aria-label="按规则筛选"
            allowClear
            showSearch
            optionFilterProp="children"
            style={{ width: 240 }}
            value={filters.ruleId || undefined}
            onChange={(v) => setFilters((f) => ({ ...f, ruleId: v || '' }))}
          >
            {rules.map((rule) => (
              <Option key={rule.id} value={rule.id}>
                {rule.name} ({rule.id})
              </Option>
            ))}
          </Select>
          <Select
            placeholder="启用状态"
            aria-label="启用状态"
            allowClear
            style={{ width: 140 }}
            value={filters.enabled || undefined}
            onChange={(v) => setFilters((f) => ({ ...f, enabled: v || '' }))}
          >
            <Option value="true">已启用</Option>
            <Option value="false">已停用</Option>
          </Select>
          <Button icon={<ReloadOutlined />} onClick={() => fetchSchedules()}>
            刷新
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
            新建调度
          </Button>
        </Space>
      </Card>

      <Table
        dataSource={schedules}
        columns={columns}
        rowKey="id"
        loading={loading}
        pagination={{
          current: pagination.current,
          pageSize: pagination.pageSize,
          total,
          showSizeChanger: true,
          showTotal: (t) => `共 ${t} 条`,
        }}
        onChange={handleTableChange}
        expandable={{
          expandedRowRender: (schedule) => (
            <Space direction="vertical" style={{ width: '100%' }}>
              <strong>Fixed task inputs</strong>
              <pre className="code-preview">{JSON.stringify(schedule.variables, null, 2)}</pre>
              <strong>Input schema</strong>
              <pre className="code-preview">{JSON.stringify(schedule.inputSchema, null, 2)}</pre>
            </Space>
          ),
          rowExpandable: (schedule) => Object.keys(schedule.variables ?? {}).length > 0 || Object.keys(schedule.inputSchema ?? {}).length > 0,
        }}
        scroll={{ x: 1600 }}
      />

      <ScheduleCreateModal
        open={createOpen}
        rules={rules}
        onClose={() => setCreateOpen(false)}
        onCreated={() => fetchSchedules(1, pagination.pageSize)}
      />
    </div>
  );
}
