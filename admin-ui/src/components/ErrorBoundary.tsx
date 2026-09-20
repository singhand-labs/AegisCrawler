import React, { Component, type ReactNode } from 'react';
import { Button, Result } from 'antd';
import { useNavigate } from 'react-router-dom';

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error?: Error;
}

function ErrorActions({ onReset }: { onReset: () => void }) {
  const navigate = useNavigate();
  return (
    <div style={{ display: 'flex', gap: 12, justifyContent: 'center' }}>
      <Button type="primary" onClick={onReset}>
        重试
      </Button>
      <Button onClick={() => navigate('/rules')}>返回规则列表</Button>
    </div>
  );
}

export default class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error('ErrorBoundary caught error:', error, errorInfo);
  }

  handleReset = () => {
    this.setState({ hasError: false, error: undefined });
  };

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }
      return (
        <Result
          status="error"
          title="页面渲染出错"
          subTitle={this.state.error?.message || '发生未知错误，请重试或返回列表。'}
          extra={<ErrorActions onReset={this.handleReset} />}
        />
      );
    }
    return this.props.children;
  }
}
