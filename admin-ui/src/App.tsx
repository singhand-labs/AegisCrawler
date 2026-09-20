import { Routes, Route, Navigate } from 'react-router-dom';
import { Spin } from 'antd';
import { useAuth } from './contexts/AuthContext';
import AdminLayout from './components/Layout';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Rules from './pages/Rules';
import RuleDetail from './pages/RuleDetail';
import Tasks from './pages/Tasks';
import TaskCreate from './pages/TaskCreate';
import TaskDetail from './pages/TaskDetail';
import ScheduleList from './pages/ScheduleList';
import AuditLogs from './pages/AuditLogs';
import MCPTokenList from './pages/MCPTokenList';
import ErrorBoundary from './components/ErrorBoundary';

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { isAuthenticated, isLoading } = useAuth();
  if (isLoading) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100vh' }}>
        <Spin size="large" tip="正在校验登录状态..." />
      </div>
    );
  }
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }
  return <>{children}</>;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="/"
        element={
          <RequireAuth>
            <AdminLayout />
          </RequireAuth>
        }
      >
        <Route index element={<Navigate to="/dashboard" replace />} />
        {/* H-5: wrap ALL routes in ErrorBoundary so a render-time crash in
            any page shows a recoverable error instead of taking down the
            entire AdminLayout (navigation + all routes). */}
        <Route path="dashboard" element={<ErrorBoundary><Dashboard /></ErrorBoundary>} />
        <Route path="rules" element={<ErrorBoundary><Rules /></ErrorBoundary>} />
        <Route
          path="rules/:id"
          element={
            <ErrorBoundary>
              <RuleDetail />
            </ErrorBoundary>
          }
        />
        <Route path="tasks" element={<ErrorBoundary><Tasks /></ErrorBoundary>} />
        <Route path="tasks/create" element={<ErrorBoundary><TaskCreate /></ErrorBoundary>} />
        <Route path="tasks/:id" element={<ErrorBoundary><TaskDetail /></ErrorBoundary>} />
        <Route path="schedules" element={<ErrorBoundary><ScheduleList /></ErrorBoundary>} />
        <Route path="audit-logs" element={<ErrorBoundary><AuditLogs /></ErrorBoundary>} />
        <Route path="mcp-tokens" element={<ErrorBoundary><MCPTokenList /></ErrorBoundary>} />
      </Route>
    </Routes>
  );
}
