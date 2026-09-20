import { Layout, Menu, Button, Space, Typography } from 'antd';
import {
  DashboardOutlined,
  FileTextOutlined,
  PlayCircleOutlined,
  ScheduleOutlined,
  AuditOutlined,
  LogoutOutlined,
  KeyOutlined,
} from '@ant-design/icons';
import { Link, useLocation, Outlet } from 'react-router-dom';
import { useAuth } from '../contexts/AuthContext';

const { Header, Sider, Content } = Layout;
const { Title } = Typography;

const menuItems = [
  { key: '/admin/dashboard', icon: <DashboardOutlined />, label: <Link to="/dashboard">概览</Link> },
  { key: '/admin/rules', icon: <FileTextOutlined />, label: <Link to="/rules">规则管理</Link> },
  { key: '/admin/tasks', icon: <PlayCircleOutlined />, label: <Link to="/tasks">任务管理</Link> },
  { key: '/admin/schedules', icon: <ScheduleOutlined />, label: <Link to="/schedules">调度计划</Link> },
  { key: '/admin/audit-logs', icon: <AuditOutlined />, label: <Link to="/audit-logs">审计日志</Link> },
  { key: '/admin/mcp-tokens', icon: <KeyOutlined />, label: <Link to="/mcp-tokens">MCP 令牌</Link> },
];

export default function AdminLayout() {
  const location = useLocation();
  const { logout } = useAuth();

  return (
    <Layout className="admin-layout">
      <Header style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', background: '#001529' }}>
        <Title level={4} style={{ color: '#fff', margin: 0 }}>
          AegisCrawler 管理后台
        </Title>
        <Space>
          <Button type="link" icon={<LogoutOutlined />} onClick={logout} style={{ color: '#fff' }}>
            退出登录
          </Button>
        </Space>
      </Header>
      <Layout>
        <Sider width={200} theme="light">
          <Menu
            mode="inline"
            selectedKeys={[`/admin${location.pathname}`]}
            style={{ height: '100%', borderRight: 0 }}
            items={menuItems}
          />
        </Sider>
        <Content className="admin-content">
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
}
