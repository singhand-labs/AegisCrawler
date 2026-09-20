import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Table, Button, Input, Select, DatePicker, Space, Card, Typography, message,
} from 'antd';
import { ReloadOutlined, PlusOutlined, ScheduleOutlined } from '@ant-design/icons';
import { listTasks, cancelTask, retryTask } from '../api/client';
import type { Task } from '../api/types';
import StatusBadge, { PriorityTag } from '../components/StatusBadge';
import ConfirmModal from '../components/ConfirmModal';
import { formatTime, toRFC3339 } from '../utils/time';
import { useDebouncedValue } from '../utils/useDebouncedValue';
import dayjs from 'dayjs';

const { Title } = Typography;
const { Option } = Select;
const { RangePicker } = DatePicker;

const statusOptions = [
  { value: 'pending', label: '待执行' },
  { value: 'leased', label: '已领取' },
  { value: 'running', label: '执行中' },
  { value: 'done', label: '完成' },
  { value: 'failed', label: '失败' },
  { value: 'cancelled', label: '已取消' },
  { value: 'waiting_for_human', label: '等待人工' },
  { value: 'dead_letter', label: '死信' },
];

export default function Tasks() {
  const navigate = useNavigate();
  const [tasks, setTasks] = useState<Task[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState({
    status: '',
    ruleId: '',
    workerId: '',
    priority: '',
    dates: null as [dayjs.Dayjs, dayjs.Dayjs] | null,
  });
  const [pagination, setPagination] = useState({ current: 1, pageSize: 20 });
  const [confirm, setConfirm] = useState<{ open: boolean; title: string; content: string; onOk: () => void } | null>(null);
  // M-1: debounce text filter inputs so rapid typing doesn't fire one API
  // request per keystroke (server load + out-of-order response races).
  const debouncedFilters = useDebouncedValue(filters, 300);
  // M-1: race-condition guard — ignore stale responses when a newer request
  // has already been issued.
  const fetchSeqRef = useRef(0);

  const fetchTasks = async (page = pagination.current, size = pagination.pageSize, activeFilters = filters) => {
    const seq = ++fetchSeqRef.current;
    setLoading(true);
    try {
      const resp = await listTasks({
        status: activeFilters.status,
        rule_id: activeFilters.ruleId,
        worker_id: activeFilters.workerId,
        priority: activeFilters.priority,
        created_after: activeFilters.dates ? toRFC3339(activeFilters.dates[0]) : undefined,
        created_before: activeFilters.dates ? toRFC3339(activeFilters.dates[1]) : undefined,
        limit: size,
        offset: (page - 1) * size,
      });
      if (seq !== fetchSeqRef.current) return; // stale response — ignore
      setTasks(resp.tasks);
      setTotal(resp.total);
    } catch (err) {
      // H-2: surface network/API errors instead of silently showing an empty table.
      if (seq === fetchSeqRef.current) {
        message.error(err instanceof Error ? err.message : '加载任务列表失败');
      }
    } finally {
      if (seq === fetchSeqRef.current) setLoading(false);
    }
  };

  useEffect(() => {
    fetchTasks(1, pagination.pageSize, debouncedFilters);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedFilters, pagination.pageSize]);

  const handleTableChange = (p: { current?: number; pageSize?: number }) => {
    const current = p.current || 1;
    const pageSize = p.pageSize || 20;
    setPagination({ current, pageSize });
    fetchTasks(current, pageSize);
  };

  const handleCancel = (task: Task) => {
    setConfirm({
      open: true,
      title: '取消任务',
      content: `确定取消任务 ${task.id} 吗？`,
      onOk: async () => {
        try {
          await cancelTask(task.id);
          message.success('任务已取消');
          fetchTasks();
        } catch (err) {
          message.error(err instanceof Error ? err.message : '取消失败');
        } finally {
          setConfirm(null);
        }
      },
    });
  };

  const handleRetry = (task: Task) => {
    setConfirm({
      open: true,
      title: '重试任务',
      content: `确定重试任务 ${task.id} 吗？`,
      onOk: async () => {
        try {
          await retryTask(task.id);
          message.success('任务已重试');
          fetchTasks();
        } catch (err) {
          message.error(err instanceof Error ? err.message : '重试失败');
        } finally {
          setConfirm(null);
        }
      },
    });
  };

  const columns = [
    {
      title: '任务 ID',
      dataIndex: 'id',
      key: 'id',
      render: (v: string, record: Task) => (
        <Space>
          <Button type="link" onClick={() => navigate(`/tasks/${v}`)}>
            {v.slice(0, 12)}...
          </Button>
          {record.scheduleId && (
            <Button
              type="link"
              size="small"
              icon={<ScheduleOutlined />}
              title="来自调度计划"
              onClick={() => navigate('/schedules')}
            >
              调度
            </Button>
          )}
        </Space>
      ),
    },
    {
      title: '规则',
      dataIndex: 'ruleId',
      key: 'ruleId',
      render: (v: string) => (
        <Button type="link" onClick={() => navigate(`/rules/${v}`)}>
          {v}
        </Button>
      ),
    },
    { title: '状态', dataIndex: 'status', key: 'status', render: (v: string) => <StatusBadge status={v} /> },
    { title: '优先级', dataIndex: 'priority', key: 'priority', render: (v: string) => <PriorityTag priority={v} /> },
    { title: '重试', dataIndex: 'retryCount', key: 'retryCount', render: (v: number, record: Task) => `${v}/${record.maxRetries}` },
    { title: 'Worker', dataIndex: 'workerId', key: 'workerId', render: (v: string) => v || '-' },
    { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: Task) => (
        <Space size="small">
          <Button type="link" size="small" onClick={() => navigate(`/tasks/${record.id}`)}>
            详情
          </Button>
          {['pending', 'leased', 'running'].includes(record.status) && (
            <Button type="link" size="small" danger onClick={() => handleCancel(record)}>
              取消
            </Button>
          )}
          {['failed', 'cancelled', 'dead_letter'].includes(record.status) && (
            <Button type="link" size="small" onClick={() => handleRetry(record)}>
              重试
            </Button>
          )}
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>任务管理</Title>
      <Card style={{ marginBottom: 24 }}>
        <Space wrap style={{ marginBottom: 16 }}>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/tasks/create')}>
            创建任务
          </Button>
          <Input
            placeholder="规则 ID"
            value={filters.ruleId}
            onChange={(e) => setFilters((f) => ({ ...f, ruleId: e.target.value }))}
            style={{ width: 200 }}
            allowClear
          />
          <Input
            placeholder="Worker ID"
            value={filters.workerId}
            onChange={(e) => setFilters((f) => ({ ...f, workerId: e.target.value }))}
            style={{ width: 200 }}
            allowClear
          />
          <Select
            placeholder="任务状态"
            allowClear
            style={{ width: 160 }}
            value={filters.status || undefined}
            onChange={(v) => setFilters((f) => ({ ...f, status: v || '' }))}
          >
            {statusOptions.map((s) => (
              <Option key={s.value} value={s.value}>
                {s.label}
              </Option>
            ))}
          </Select>
          <Select
            placeholder="优先级"
            allowClear
            style={{ width: 120 }}
            value={filters.priority || undefined}
            onChange={(v) => setFilters((f) => ({ ...f, priority: v || '' }))}
          >
            <Option value="high">高</Option>
            <Option value="normal">普通</Option>
            <Option value="low">低</Option>
          </Select>
          <RangePicker
            showTime
            value={filters.dates}
            onChange={(dates) => setFilters((f) => ({ ...f, dates: dates as [dayjs.Dayjs, dayjs.Dayjs] | null }))}
          />
          <Button icon={<ReloadOutlined />} onClick={() => fetchTasks()}>
            刷新
          </Button>
        </Space>
      </Card>

      <Table
        dataSource={tasks}
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
        scroll={{ x: 1100 }}
      />

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
