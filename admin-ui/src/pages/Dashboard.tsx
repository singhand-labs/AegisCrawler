import { useEffect, useState } from 'react';
import { Row, Col, Card, Statistic, Table, Typography, Button, Tag } from 'antd';
import { useNavigate } from 'react-router-dom';
import { listRules, listTasks } from '../api/client';
import type { Task, Rule } from '../api/types';
import StatusBadge, { PriorityTag } from '../components/StatusBadge';
import { formatTime } from '../utils/time';

const { Title } = Typography;

export default function Dashboard() {
  const navigate = useNavigate();
  const [rules, setRules] = useState<Rule[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    async function load() {
      setLoading(true);
      try {
        const [rulesResp, tasksResp] = await Promise.all([
          listRules({ limit: 1000 }),
          listTasks({ limit: 10 }),
        ]);
        setRules(rulesResp.rules);
        setTasks(tasksResp.tasks);
      } finally {
        setLoading(false);
      }
    }
    load();
  }, []);

  const enabledCount = rules.filter((r) => r.enabled).length;
  const pendingApproval = rules.filter((r) => r.approvalStatus === 'pending').length;
  const statusCounts = tasks.reduce(
    (acc, t) => {
      acc[t.status] = (acc[t.status] || 0) + 1;
      return acc;
    },
    {} as Record<string, number>
  );

  const taskColumns = [
    { title: '任务 ID', dataIndex: 'id', key: 'id', ellipsis: true },
    {
      title: '规则',
      dataIndex: 'ruleId',
      key: 'ruleId',
      render: (v: string, record: Task) => (
        <Button type="link" onClick={() => navigate(`/rules/${record.ruleId}`)}>
          {v}
        </Button>
      ),
    },
    { title: '状态', dataIndex: 'status', key: 'status', render: (v: string) => <StatusBadge status={v} /> },
    { title: '优先级', dataIndex: 'priority', key: 'priority', render: (v: string) => <PriorityTag priority={v} /> },
    { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: Task) => (
        <Button type="link" onClick={() => navigate(`/tasks/${record.id}`)}>
          查看
        </Button>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>概览</Title>
      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={6}>
          <Card loading={loading} className="stat-card">
            <Statistic title="规则总数" value={rules.length} />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading} className="stat-card">
            <Statistic title="已启用规则" value={enabledCount} />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading} className="stat-card">
            <Statistic title="待审核规则" value={pendingApproval} />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading} className="stat-card">
            <Statistic
              title="任务状态分布"
              value={Object.keys(statusCounts).length}
              suffix={`类 / ${tasks.length} 条`}
            />
          </Card>
        </Col>
      </Row>

      <Card title="最近任务" loading={loading} extra={<Button onClick={() => navigate('/tasks')}>查看全部</Button>}>
        <Table
          dataSource={tasks}
          columns={taskColumns}
          rowKey="id"
          pagination={false}
          size="middle"
          locale={{ emptyText: '暂无任务' }}
        />
      </Card>

      <Row gutter={16} style={{ marginTop: 24 }}>
        {Object.entries(statusCounts).map(([status, count]) => (
          <Col key={status}>
            <Tag color="blue">
              <StatusBadge status={status} /> × {count}
            </Tag>
          </Col>
        ))}
      </Row>
    </div>
  );
}
