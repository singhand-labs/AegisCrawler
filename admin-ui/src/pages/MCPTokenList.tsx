import { useEffect, useState } from 'react';
import {
  Alert, Button, Card, Checkbox, DatePicker, Form, Input, Modal, Popconfirm,
  Space, Table, Tag, Typography, message,
} from 'antd';
import { KeyOutlined, PlusOutlined, ReloadOutlined, StopOutlined } from '@ant-design/icons';
import type { Dayjs } from 'dayjs';
import { createMCPToken, listMCPTokens, revokeMCPToken } from '../api/client';
import type { MCPPermission, MCPToken } from '../api/types';
import { formatTime } from '../utils/time';

const { Title, Paragraph, Text } = Typography;

interface TokenFormValues {
  name: string;
  permissions: MCPPermission[];
  expiresAt?: Dayjs;
}

function tokenStatus(token: MCPToken): { color: string; label: string } {
  if (token.revokedAt) return { color: 'default', label: '已撤销' };
  if (token.expiresAt && new Date(token.expiresAt).getTime() <= Date.now()) return { color: 'red', label: '已过期' };
  return { color: 'green', label: '有效' };
}

export default function MCPTokenList() {
  const [tokens, setTokens] = useState<MCPToken[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [createdToken, setCreatedToken] = useState<MCPToken | null>(null);
  const [form] = Form.useForm<TokenFormValues>();

  const fetchTokens = async () => {
    setLoading(true);
    try {
      const response = await listMCPTokens();
      setTokens(response.tokens);
    } catch (error) {
      message.error(error instanceof Error ? error.message : '加载 MCP 令牌失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void fetchTokens();
  }, []);

  const handleCreate = async () => {
    const values = await form.validateFields();
    setCreating(true);
    try {
      const token = await createMCPToken({
        name: values.name.trim(),
        permissions: values.permissions,
        expiresAt: values.expiresAt?.toISOString(),
      });
      setCreateOpen(false);
      form.resetFields();
      setCreatedToken(token);
      await fetchTokens();
    } catch (error) {
      message.error(error instanceof Error ? error.message : '创建 MCP 令牌失败');
    } finally {
      setCreating(false);
    }
  };

  const handleRevoke = async (id: string) => {
    try {
      await revokeMCPToken(id);
      message.success('MCP 令牌已撤销');
      await fetchTokens();
    } catch (error) {
      message.error(error instanceof Error ? error.message : '撤销 MCP 令牌失败');
    }
  };

  const columns = [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '令牌前缀', dataIndex: 'tokenPrefix', key: 'tokenPrefix', render: (value: string) => <Text code>{value}…</Text> },
    {
      title: '权限', dataIndex: 'permissions', key: 'permissions',
      render: (permissions: MCPPermission[]) => <Space>{permissions.map((permission) => <Tag key={permission}>{permission}</Tag>)}</Space>,
    },
    {
      title: '状态', key: 'status', render: (_: unknown, token: MCPToken) => {
        const status = tokenStatus(token);
        return <Tag color={status.color}>{status.label}</Tag>;
      },
    },
    { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime },
    { title: '最近使用', dataIndex: 'lastUsedAt', key: 'lastUsedAt', render: (value?: string) => (value ? formatTime(value) : '—') },
    { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', render: (value?: string) => (value ? formatTime(value) : '永不过期') },
    {
      title: '操作', key: 'actions', render: (_: unknown, token: MCPToken) => (
        <Popconfirm title="确认撤销此令牌？" description="撤销后无法恢复，客户端的后续请求会立即失败。" onConfirm={() => handleRevoke(token.id)} disabled={Boolean(token.revokedAt)}>
          <Button danger size="small" icon={<StopOutlined />} disabled={Boolean(token.revokedAt)}>撤销</Button>
        </Popconfirm>
      ),
    },
  ];

  return (
    <div>
      <Title level={3}><KeyOutlined /> MCP 访问令牌</Title>
      <Alert
        type="info"
        showIcon
        message="令牌绑定当前工作区"
        description="为每个 MCP 客户端创建独立令牌并授予最小权限。服务器只保存令牌哈希；明文只在创建后显示一次。"
        style={{ marginBottom: 16 }}
      />
      <Card>
        <Space style={{ marginBottom: 16 }}>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>创建令牌</Button>
          <Button icon={<ReloadOutlined />} onClick={() => void fetchTokens()}>刷新</Button>
        </Space>
        <Table dataSource={tokens} columns={columns} rowKey="id" loading={loading} pagination={false} scroll={{ x: 1100 }} />
      </Card>

      <Modal title="创建 MCP 令牌" open={createOpen} onOk={() => void handleCreate()} confirmLoading={creating} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form form={form} layout="vertical" initialValues={{ permissions: ['read'] }}>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入令牌名称' }, { max: 100 }]}>
            <Input placeholder="例如：数据分析自动化" />
          </Form.Item>
          <Form.Item name="permissions" label="权限" rules={[{ required: true, message: '至少选择一项权限' }]}>
            <Checkbox.Group options={[{ label: '读取', value: 'read' }, { label: '写入', value: 'write' }]} />
          </Form.Item>
          <Form.Item name="expiresAt" label="过期时间（可选）">
            <DatePicker showTime style={{ width: '100%' }} disabledDate={(date) => date.endOf('day').isBefore()} />
          </Form.Item>
          <Paragraph type="secondary">写入权限不会自动包含读取权限。需要完整任务生命周期的客户端应同时选择两项。</Paragraph>
        </Form>
      </Modal>

      <Modal title="立即保存令牌" open={Boolean(createdToken)} onOk={() => setCreatedToken(null)} onCancel={() => setCreatedToken(null)} cancelButtonProps={{ style: { display: 'none' } }}>
        <Alert type="warning" showIcon message="此明文令牌不会再次显示" style={{ marginBottom: 16 }} />
        <Paragraph copyable={{ text: createdToken?.token }} code style={{ wordBreak: 'break-all' }}>{createdToken?.token}</Paragraph>
      </Modal>
    </div>
  );
}
