import { useEffect, useRef, useState } from 'react';
import {
  Table, Input, DatePicker, Select, Space, Card, Typography, Button, message,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { listAuditLogs } from '../api/client';
import type { AuditLog } from '../api/types';
import { formatTime, toRFC3339 } from '../utils/time';
import { useDebouncedValue } from '../utils/useDebouncedValue';

const { Title } = Typography;
const { RangePicker } = DatePicker;
const { Option } = Select;

const actionOptions = [
  'create_rule', 'update_rule', 'delete_rule', 'approve_rule', 'reject_rule',
  'create_task', 'cancel_task', 'retry_task',
  'create_mcp_token', 'revoke_mcp_token',
  'mcp.list_rules', 'mcp.get_rule', 'mcp.create_task', 'mcp.get_task',
  'mcp.get_task_results', 'mcp.cancel_task', 'mcp.retry_task',
  'mcp.list_schedules', 'mcp.create_schedule', 'mcp.update_schedule', 'mcp.delete_schedule',
];

export default function AuditLogs() {
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState({
    actor: '',
    action: '',
    resourceType: '',
    resourceId: '',
    dates: null as [dayjs.Dayjs, dayjs.Dayjs] | null,
  });
  const [pagination, setPagination] = useState({ current: 1, pageSize: 20 });
  // M-1: debounce text filter inputs so rapid typing doesn't fire one API
  // request per keystroke.
  const debouncedFilters = useDebouncedValue(filters, 300);
  const fetchSeqRef = useRef(0);

  const fetchLogs = async (page = pagination.current, size = pagination.pageSize, activeFilters = filters) => {
    const seq = ++fetchSeqRef.current;
    setLoading(true);
    try {
      const resp = await listAuditLogs({
        actor: activeFilters.actor,
        action: activeFilters.action,
        resource_type: activeFilters.resourceType,
        resource_id: activeFilters.resourceId,
        created_after: activeFilters.dates ? toRFC3339(activeFilters.dates[0]) : undefined,
        created_before: activeFilters.dates ? toRFC3339(activeFilters.dates[1]) : undefined,
        limit: size,
        offset: (page - 1) * size,
      });
      if (seq !== fetchSeqRef.current) return; // stale response — ignore
      setLogs(resp.logs);
      setTotal(resp.total);
    } catch (err) {
      // H-2: surface network/API errors instead of silently showing an empty table.
      if (seq === fetchSeqRef.current) {
        message.error(err instanceof Error ? err.message : '加载审计日志失败');
      }
    } finally {
      if (seq === fetchSeqRef.current) setLoading(false);
    }
  };

  useEffect(() => {
    fetchLogs(1, pagination.pageSize, debouncedFilters);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedFilters, pagination.pageSize]);

  const handleTableChange = (p: { current?: number; pageSize?: number }) => {
    const current = p.current || 1;
    const pageSize = p.pageSize || 20;
    setPagination({ current, pageSize });
    fetchLogs(current, pageSize);
  };

  const columns = [
    { title: '时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime, width: 180 },
    { title: '操作人', dataIndex: 'actor', key: 'actor' },
    { title: '动作', dataIndex: 'action', key: 'action' },
    { title: '资源类型', dataIndex: 'resourceType', key: 'resourceType' },
    { title: '资源 ID', dataIndex: 'resourceId', key: 'resourceId', ellipsis: true },
    {
      title: '详情',
      dataIndex: 'payload',
      key: 'payload',
      render: (v: Record<string, unknown>) => <pre className="code-preview" style={{ maxHeight: 120 }}>{JSON.stringify(v, null, 2)}</pre>,
    },
  ];

  return (
    <div>
      <Title level={3}>审计日志</Title>
      <Card style={{ marginBottom: 24 }}>
        <Space wrap style={{ marginBottom: 16 }}>
          <Input
            placeholder="操作人"
            value={filters.actor}
            onChange={(e) => setFilters((f) => ({ ...f, actor: e.target.value }))}
            style={{ width: 160 }}
            allowClear
          />
          <Select
            placeholder="动作"
            allowClear
            style={{ width: 200 }}
            value={filters.action || undefined}
            onChange={(v) => setFilters((f) => ({ ...f, action: v || '' }))}
          >
            {actionOptions.map((a) => (
              <Option key={a} value={a}>
                {a}
              </Option>
            ))}
          </Select>
          <Input
            placeholder="资源类型"
            value={filters.resourceType}
            onChange={(e) => setFilters((f) => ({ ...f, resourceType: e.target.value }))}
            style={{ width: 160 }}
            allowClear
          />
          <Input
            placeholder="资源 ID"
            value={filters.resourceId}
            onChange={(e) => setFilters((f) => ({ ...f, resourceId: e.target.value }))}
            style={{ width: 200 }}
            allowClear
          />
          <RangePicker
            showTime
            value={filters.dates}
            onChange={(dates) => setFilters((f) => ({ ...f, dates: dates as [dayjs.Dayjs, dayjs.Dayjs] | null }))}
          />
          <Button icon={<ReloadOutlined />} onClick={() => fetchLogs()}>
            刷新
          </Button>
        </Space>
      </Card>

      <Table
        dataSource={logs}
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
        scroll={{ x: 1200 }}
      />
    </div>
  );
}
