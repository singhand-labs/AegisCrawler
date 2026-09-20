import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Table, Button, Input, Select, Space, Modal, Form, Switch, message, Card, Typography, Row, Col, Tag,
} from 'antd';
import { PlusOutlined, SearchOutlined } from '@ant-design/icons';
import {
  listRules, createRule, updateRule, deleteRule, approveRule, rejectRule,
} from '../api/client';
import type { Rule } from '../api/types';
import StatusBadge, { PriorityTag } from '../components/StatusBadge';
import ConfirmModal from '../components/ConfirmModal';
import { formatTime } from '../utils/time';

const { Title } = Typography;
const { Option } = Select;
const { TextArea } = Input;

export default function Rules() {
  const navigate = useNavigate();
  const [rules, setRules] = useState<Rule[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState({ keyword: '', approvalStatus: '', enabled: '' });
  const [pagination, setPagination] = useState({ current: 1, pageSize: 20 });
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<Rule | null>(null);
  // M-4: double-submit guard — antd Form provides some protection, but an
  // explicit saving state also disables the submit button visually.
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const [confirm, setConfirm] = useState<{ open: boolean; title: string; content: string; onOk: () => void } | null>(null);

  const fetchRules = async (page = pagination.current, size = pagination.pageSize) => {
    setLoading(true);
    try {
      const resp = await listRules({
        approval_status: filters.approvalStatus,
        limit: size,
        offset: (page - 1) * size,
      });
      let rows = resp.rules;
      if (filters.keyword) {
        const kw = filters.keyword.toLowerCase();
        rows = rows.filter(
          (r) =>
            r.id.toLowerCase().includes(kw) ||
            r.name.toLowerCase().includes(kw) ||
            String(r.domain).toLowerCase().includes(kw)
        );
      }
      if (filters.enabled !== '') {
        const enabled = filters.enabled === 'true';
        rows = rows.filter((r) => r.enabled === enabled);
      }
      setRules(rows);
      setTotal(resp.total);
    } catch (err) {
      // H-2: surface network/API errors instead of silently showing an empty table.
      message.error(err instanceof Error ? err.message : '加载规则列表失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchRules(1, pagination.pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters, pagination.pageSize]);

  const handleTableChange = (p: { current?: number; pageSize?: number }) => {
    const current = p.current || 1;
    const pageSize = p.pageSize || 20;
    setPagination({ current, pageSize });
    fetchRules(current, pageSize);
  };

  const openCreate = () => {
    setEditingRule(null);
    form.resetFields();
    setIsModalOpen(true);
  };

  const openEdit = (rule: Rule) => {
    setEditingRule(rule);
    form.setFieldsValue({
      content: JSON.stringify(rule, null, 2),
    });
    setIsModalOpen(true);
  };

  const handleSave = async (values: { content: string }) => {
    if (saving) return;
    setSaving(true);
    try {
      const parsed = JSON.parse(values.content) as Rule;
      if (editingRule) {
        await updateRule(editingRule.id, parsed);
        message.success('规则已更新');
      } else {
        await createRule(parsed);
        message.success('规则已创建');
      }
      setIsModalOpen(false);
      fetchRules();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '保存失败，请检查 JSON 格式');
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = (rule: Rule) => {
    setConfirm({
      open: true,
      title: '删除规则',
      content: `确定删除规则 "${rule.name}"（${rule.id}）吗？相关任务和结果也会被删除。`,
      onOk: async () => {
        try {
          await deleteRule(rule.id);
          message.success('规则已删除');
          fetchRules();
        } catch (err) {
          message.error(err instanceof Error ? err.message : '删除失败');
        } finally {
          setConfirm(null);
        }
      },
    });
  };

  const toggleEnabled = async (rule: Rule) => {
    try {
      await updateRule(rule.id, { enabled: !rule.enabled });
      message.success('状态已更新');
      fetchRules();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '更新失败');
    }
  };

  const handleApprove = async (rule: Rule) => {
    try {
      await approveRule(rule.id);
      message.success('规则已审核通过');
      fetchRules();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '操作失败');
    }
  };

  const handleReject = async (rule: Rule) => {
    try {
      await rejectRule(rule.id);
      message.success('规则已拒绝');
      fetchRules();
    } catch (err) {
      message.error(err instanceof Error ? err.message : '操作失败');
    }
  };

  const columns = [
    { title: '规则 ID', dataIndex: 'id', key: 'id', ellipsis: true },
    { title: '名称', dataIndex: 'name', key: 'name' },
    {
      title: '域名',
      dataIndex: 'domain',
      key: 'domain',
      render: (v: unknown) => (typeof v === 'string' ? v : JSON.stringify(v)),
      ellipsis: true,
    },
    { title: '优先级', dataIndex: 'priority', key: 'priority', render: (v: string) => <PriorityTag priority={v} /> },
    {
      title: '启用',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (v: boolean, record: Rule) => <Switch checked={v} onChange={() => toggleEnabled(record)} />,
    },
    { title: '审批状态', dataIndex: 'approvalStatus', key: 'approvalStatus', render: (v: string) => <StatusBadge status={v} /> },
    {
      title: '来源',
      dataIndex: 'source',
      key: 'source',
      render: (v: string) => <Tag color="blue">{v || 'pageagent'}</Tag>,
    },
    { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime },
    {
      title: '操作',
      key: 'action',
      width: 220,
      render: (_: unknown, record: Rule) => (
        <Space size="small">
          <Button type="link" size="small" onClick={() => navigate(`/rules/${record.id}`)}>
            查看
          </Button>
          <Button type="link" size="small" onClick={() => openEdit(record)}>
            编辑
          </Button>
          {record.approvalStatus === 'pending' && (
            <>
              <Button type="link" size="small" onClick={() => handleApprove(record)}>
                通过
              </Button>
              <Button type="link" size="small" danger onClick={() => handleReject(record)}>
                拒绝
              </Button>
            </>
          )}
          <Button type="link" size="small" danger onClick={() => handleDelete(record)}>
            删除
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}>规则管理</Title>
      <Card style={{ marginBottom: 24 }}>
        <Row gutter={16} align="middle">
          <Col span={8}>
            <Input
              placeholder="搜索规则 ID / 名称 / 域名"
              prefix={<SearchOutlined />}
              value={filters.keyword}
              onChange={(e) => setFilters((f) => ({ ...f, keyword: e.target.value }))}
              allowClear
            />
          </Col>
          <Col span={4}>
            <Select
              placeholder="审批状态"
              allowClear
              style={{ width: '100%' }}
              value={filters.approvalStatus || undefined}
              onChange={(v) => setFilters((f) => ({ ...f, approvalStatus: v || '' }))}
            >
              <Option value="pending">待审核</Option>
              <Option value="approved">已通过</Option>
              <Option value="rejected">已拒绝</Option>
            </Select>
          </Col>
          <Col span={4}>
            <Select
              placeholder="启用状态"
              allowClear
              style={{ width: '100%' }}
              value={filters.enabled || undefined}
              onChange={(v) => setFilters((f) => ({ ...f, enabled: v || '' }))}
            >
              <Option value="true">已启用</Option>
              <Option value="false">已停用</Option>
            </Select>
          </Col>
          <Col span={8} style={{ textAlign: 'right' }}>
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              新建规则
            </Button>
          </Col>
        </Row>
      </Card>

      <Table
        dataSource={rules}
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
        scroll={{ x: 1000 }}
      />

      <Modal
        title={editingRule ? '编辑规则' : '新建规则'}
        open={isModalOpen}
        onCancel={() => setIsModalOpen(false)}
        footer={null}
        width={720}
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={handleSave}>
          <Form.Item
            name="content"
            label="规则内容（JSON）"
            initialValue={JSON.stringify(
              {
                id: 'example-rule',
                version: '1.0.0',
                name: '示例规则',
                domain: 'example.com',
                enabled: true,
                priority: 'normal',
                entry: 'https://example.com',
                approvalStatus: 'pending',
                variables: {},
                selectors: {},
                humanize: {},
                steps: [],
              },
              null,
              2
            )}
            rules={[{ required: true, message: '请输入规则内容' }]}
          >
            <TextArea rows={16} placeholder="粘贴规则 JSON" />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button type="primary" htmlType="submit" loading={saving}>
                保存
              </Button>
              <Button onClick={() => setIsModalOpen(false)}>取消</Button>
            </Space>
          </Form.Item>
        </Form>
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
