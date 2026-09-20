import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert, Button, Card, Descriptions, Empty, Pagination, Radio, Space, Spin, Table, Tabs, Tag, Typography, message,
} from 'antd';
import { DownloadOutlined, ReloadOutlined, ArrowLeftOutlined } from '@ant-design/icons';
import {
  cancelTask, decideHumanIntervention, getTask, getTaskLogs, getTaskResults,
  listTaskHumanInterventions, retryTask,
} from '../api/client';
import type { HumanIntervention, LogEntry, Result, Task, TaskResultsResponse } from '../api/types';
import ConfirmModal from '../components/ConfirmModal';
import StatusBadge, { PriorityTag } from '../components/StatusBadge';
import { formatTime } from '../utils/time';
import {
  resultFields, resultRows, resultsToCSV, resultsToJSON, type ResultRow,
} from '../utils/resultData';

const { Title, Text } = Typography;

function displayValue(value: unknown): string {
  if (value === null || value === undefined) return '-';
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

function downloadText(filename: string, content: string, type: string) {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}

// M-3: cap export loop iterations. 200 × 500 rows/batch = 100k rows max —
// prevents multi-million-row datasets from occupying the export slot for hours.
const MAX_EXPORT_BATCHES = 200;

export default function TaskDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [task, setTask] = useState<Task | null>(null);
  const [results, setResults] = useState<TaskResultsResponse | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [humanInterventions, setHumanInterventions] = useState<HumanIntervention[]>([]);
  const [loading, setLoading] = useState(true);
  const [resultLoading, setResultLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  // M-3: track mount status so the export loop can stop when the component
  // unmounts mid-export (no further fetches, no setState on unmounted).
  const isMountedRef = useRef(true);
  const [resultView, setResultView] = useState<'table' | 'json'>('table');
  const [pagination, setPagination] = useState({ current: 1, pageSize: 20 });
  const [confirm, setConfirm] = useState<{ open: boolean; title: string; content: string; onOk: () => void } | null>(null);

  const loadResults = async (current: number, pageSize: number) => {
    if (!id) return;
    setResultLoading(true);
    try {
      const response = await getTaskResults(id, {
        limit: pageSize,
        offset: (current - 1) * pageSize,
        include_invalid: true,
      });
      setResults(response);
      setPagination({ current, pageSize });
    } catch (error) {
      message.error(error instanceof Error ? error.message : '加载采集结果失败');
    } finally {
      setResultLoading(false);
    }
  };

  const fetchAll = async (isStale?: () => boolean) => {
    if (!id) return;
    setLoading(true);
    try {
      const [taskResponse, resultResponse, logResponse, interventionResponse] = await Promise.all([
        getTask(id),
        getTaskResults(id, {
          limit: pagination.pageSize,
          offset: (pagination.current - 1) * pagination.pageSize,
          include_invalid: true,
        }),
        getTaskLogs(id),
        listTaskHumanInterventions(id),
      ]);
      if (isStale?.()) return;
      setTask(taskResponse);
      setResults(resultResponse);
      setLogs(logResponse);
      setHumanInterventions(interventionResponse.interventions);
    } catch (error) {
      if (!isStale?.()) {
        message.error(error instanceof Error ? error.message : '加载任务详情失败');
      }
    } finally {
      if (!isStale?.()) setLoading(false);
    }
  };

  useEffect(() => {
    // M-2: guard against out-of-order responses when the user navigates
    // between task detail pages. Without this, task A's in-flight fetch
    // resolving after task B's would overwrite B's data with A's.
    let cancelled = false;
    void fetchAll(() => cancelled);
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const rows = useMemo(() => resultRows(results?.page.batches ?? []), [results]);
  const fields = useMemo(() => resultFields(results?.outputSchema, rows), [results, rows]);
  const pendingIntervention = humanInterventions.find((item) => item.status === 'pending');

  useEffect(() => {
    // M-3: flip the mount flag on unmount so the export loop stops.
    return () => { isMountedRef.current = false; };
  }, []);

  const exportAll = async (format: 'csv' | 'json') => {
    if (!id) return;
    setExporting(true);
    let truncated = false;
    try {
      const batches: Result[] = [];
      let offset = 0;
      let total = 0;
      let iterations = 0;
      do {
        // M-3: cap iterations so a multi-million-row dataset cannot loop
        // indefinitely (each request has a 30s timeout, but hundreds of
        // sequential requests would still occupy the slot for hours).
        if (iterations >= MAX_EXPORT_BATCHES) {
          truncated = true;
          break;
        }
        // M-3: stop when the component unmounts mid-export.
        if (!isMountedRef.current) return;
        const response = await getTaskResults(id, { limit: 500, offset, include_invalid: false });
        batches.push(...response.page.batches);
        total = response.page.total;
        offset += response.page.batches.length;
        iterations++;
        if (response.page.batches.length === 0) break;
      } while (offset < total);
      if (!isMountedRef.current) return;
      const allRows = resultRows(batches);
      const allFields = resultFields(results?.outputSchema, allRows);
      if (format === 'csv') {
        downloadText(`task-${id}-results.csv`, resultsToCSV(allFields, allRows), 'text/csv;charset=utf-8');
      } else {
        downloadText(`task-${id}-results.json`, resultsToJSON(allRows), 'application/json;charset=utf-8');
      }
      if (truncated) {
        message.warning(`结果数量超过导出上限，仅导出前 ${batches.length} 条（共 ${total} 条）`);
      }
    } catch (error) {
      if (isMountedRef.current) {
        message.error(error instanceof Error ? error.message : '导出失败');
      }
    } finally {
      if (isMountedRef.current) setExporting(false);
    }
  };

  if (loading || !task) {
    return <div style={{ textAlign: 'center', padding: 64 }}><Spin size="large" /></div>;
  }

  const tableColumns = [
    { title: 'Attempt', key: 'attemptId', width: 150, render: (_: unknown, row: ResultRow) => row.attemptId || '-' },
    { title: 'Sequence', key: 'sequence', width: 100, render: (_: unknown, row: ResultRow) => row.sequence },
    ...fields.map((field) => ({
      title: field,
      key: field,
      render: (_: unknown, row: ResultRow) => displayValue(row.data[field]),
    })),
    { title: 'Received', key: 'createdAt', width: 180, render: (_: unknown, row: ResultRow) => formatTime(row.createdAt) },
  ];

  const logColumns = [
    { title: '时间', dataIndex: 'createdAt', key: 'createdAt', render: formatTime, width: 180 },
    { title: 'Worker', dataIndex: 'workerId', key: 'workerId', width: 140 },
    { title: '级别', dataIndex: 'level', key: 'level', render: (value: string) => <Tag color={value === 'error' ? 'red' : value === 'warn' ? 'orange' : 'blue'}>{value}</Tag> },
    { title: '消息', dataIndex: 'message', key: 'message' },
  ];

  const resultContent = results?.page.total === 0 ? <Empty description="暂无有效结果" /> : (
    <>
      <Space wrap style={{ marginBottom: 16 }}>
        <Radio.Group value={resultView} onChange={(event) => setResultView(event.target.value)}>
          <Radio.Button value="table">Table</Radio.Button>
          <Radio.Button value="json">JSON</Radio.Button>
        </Radio.Group>
        <Button icon={<DownloadOutlined />} loading={exporting} onClick={() => void exportAll('csv')}>Export CSV</Button>
        <Button icon={<DownloadOutlined />} loading={exporting} onClick={() => void exportAll('json')}>Export JSON</Button>
        <Text type="secondary">{results?.page.total ?? 0} valid batches</Text>
      </Space>
      {resultView === 'table' ? (
        <Table<ResultRow>
          dataSource={rows}
          columns={tableColumns}
          rowKey="key"
          loading={resultLoading}
          pagination={false}
          scroll={{ x: 'max-content' }}
        />
      ) : (
        <pre className="code-preview">{resultsToJSON(rows)}</pre>
      )}
      <Pagination
        current={pagination.current}
        pageSize={pagination.pageSize}
        total={results?.page.total ?? 0}
        showSizeChanger
        showTotal={(total) => `${total} valid batches`}
        onChange={(current, pageSize) => void loadResults(current, pageSize)}
        style={{ marginTop: 16 }}
      />
    </>
  );

  return (
    <div>
      <Title level={3}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/tasks')} style={{ marginRight: 12 }}>返回</Button>
        任务详情
        <Button icon={<ReloadOutlined />} onClick={() => void fetchAll()} style={{ marginLeft: 16 }}>刷新</Button>
      </Title>

      <Card
        extra={(
          <>
            {['pending', 'leased', 'running', 'waiting_for_human'].includes(task.status) && (
              <Button
                danger
                onClick={() => setConfirm({
                  open: true, title: '取消任务', content: `确定取消任务 ${task.id} 吗？`,
                  // M-9: surface API failures; previously the rejection was
                  // unhandled and the modal stayed open with no feedback.
                  onOk: async () => {
                    try {
                      await cancelTask(task.id);
                      await fetchAll();
                    } catch (err) {
                      message.error(err instanceof Error ? err.message : '取消失败');
                    } finally {
                      setConfirm(null);
                    }
                  },
                })}
                style={{ marginRight: 8 }}
              >取消</Button>
            )}
            {['failed', 'cancelled', 'dead_letter'].includes(task.status) && (
              <Button
                type="primary"
                onClick={() => setConfirm({
                  open: true, title: '重试任务', content: `确定重试任务 ${task.id} 吗？`,
                  // M-9: surface API failures.
                  onOk: async () => {
                    try {
                      await retryTask(task.id);
                      await fetchAll();
                    } catch (err) {
                      message.error(err instanceof Error ? err.message : '重试失败');
                    } finally {
                      setConfirm(null);
                    }
                  },
                })}
              >重试</Button>
            )}
          </>
        )}
      >
        <Descriptions bordered column={2}>
          <Descriptions.Item label="任务 ID">{task.id}</Descriptions.Item>
          <Descriptions.Item label="规则 ID"><Button type="link" onClick={() => navigate(`/rules/${task.ruleId}`)}>{task.ruleId}</Button></Descriptions.Item>
          <Descriptions.Item label="Immutable version">v{task.ruleVersionNumber || '-'} ({task.ruleVersion})</Descriptions.Item>
          <Descriptions.Item label="状态"><StatusBadge status={task.status} /></Descriptions.Item>
          <Descriptions.Item label="Current attempt">{task.currentAttemptId || '-'}</Descriptions.Item>
          <Descriptions.Item label="Browser profile">{task.browserProfileId || '-'}</Descriptions.Item>
          <Descriptions.Item label="优先级"><PriorityTag priority={task.priority} /></Descriptions.Item>
          <Descriptions.Item label="重试次数">{task.retryCount} / {task.maxRetries}</Descriptions.Item>
          <Descriptions.Item label="Worker">{task.workerId || '-'}</Descriptions.Item>
          <Descriptions.Item label="租约到期">{formatTime(task.leaseUntil)}</Descriptions.Item>
          <Descriptions.Item label="创建时间">{formatTime(task.createdAt)}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatTime(task.updatedAt)}</Descriptions.Item>
          <Descriptions.Item label="计划时间">{formatTime(task.scheduledAt)}</Descriptions.Item>
          <Descriptions.Item label="完成时间">{formatTime(task.completedAt)}</Descriptions.Item>
          {task.errorMessage && <Descriptions.Item label="错误信息" span={2}><Tag color="red">{task.errorType || 'error'}</Tag> {task.errorMessage}</Descriptions.Item>}
        </Descriptions>
      </Card>

      {pendingIntervention && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 24 }}
          message="Operator decision required"
          description={(
            <Space direction="vertical" style={{ width: '100%' }}>
              <Text>{pendingIntervention.prompt}</Text>
              <Descriptions bordered size="small" column={2}>
                <Descriptions.Item label="Type">{pendingIntervention.type}</Descriptions.Item>
                <Descriptions.Item label="Approved action">{pendingIntervention.requestedAction}</Descriptions.Item>
                <Descriptions.Item label="Target origin">{pendingIntervention.targetOrigin}</Descriptions.Item>
                <Descriptions.Item label="Browser profile">{pendingIntervention.browserProfileId || '-'}</Descriptions.Item>
                <Descriptions.Item label="Attempt">{pendingIntervention.attemptId}</Descriptions.Item>
                <Descriptions.Item label="Checkpoint">{pendingIntervention.checkpointId}</Descriptions.Item>
                <Descriptions.Item label="Expires">{formatTime(pendingIntervention.expiresAt)}</Descriptions.Item>
              </Descriptions>
              <Space>
                <Button
                  type="primary"
                  onClick={() => setConfirm({
                    open: true,
                    title: 'Approve checkpoint resume',
                    content: `Approve only after completing ${pendingIntervention.requestedAction} at ${pendingIntervention.targetOrigin}.`,
                    // M-9: surface API failures.
                    onOk: async () => {
                      try {
                        await decideHumanIntervention(task.id, pendingIntervention.id, 'approved', pendingIntervention.checkpointId);
                        await fetchAll();
                      } catch (err) {
                        message.error(err instanceof Error ? err.message : '审批失败');
                      } finally {
                        setConfirm(null);
                      }
                    },
                  })}
                >Approve and resume</Button>
                <Button
                  danger
                  onClick={() => setConfirm({
                    open: true,
                    title: 'Reject human intervention',
                    content: 'Rejecting fails this execution attempt and cannot be undone.',
                    // M-9: surface API failures.
                    onOk: async () => {
                      try {
                        await decideHumanIntervention(task.id, pendingIntervention.id, 'rejected', pendingIntervention.checkpointId);
                        await fetchAll();
                      } catch (err) {
                        message.error(err instanceof Error ? err.message : '拒绝失败');
                      } finally {
                        setConfirm(null);
                      }
                    },
                  })}
                >Reject</Button>
              </Space>
            </Space>
          )}
        />
      )}

      <Tabs
        defaultActiveKey="results"
        style={{ marginTop: 24 }}
        items={[
          { key: 'results', label: '采集结果', children: resultContent },
          {
            key: 'invalid',
            label: `Invalid diagnostics (${results?.page.invalidBatches?.length ?? 0})`,
            children: results?.page.invalidBatches?.length ? (
              <Space direction="vertical" style={{ width: '100%' }}>
                {results.page.invalidBatches.map((result) => (
                  <Alert
                    key={result.id}
                    type="error"
                    showIcon
                    message={`Attempt ${result.attemptId || '-'} · sequence ${result.sequence}`}
                    description={<><div>{result.validationError || 'Output schema validation failed'}</div><pre className="code-preview">{JSON.stringify(result.payload, null, 2)}</pre></>}
                  />
                ))}
              </Space>
            ) : <Empty description="No invalid result payloads" />,
          },
          {
            key: 'summary', label: 'Execution summary',
            children: results?.page.summary
              ? <Card title={`Attempt ${results.page.summary.attemptId}`} extra={formatTime(results.page.summary.createdAt)}><pre className="code-preview">{JSON.stringify(results.page.summary.payload, null, 2)}</pre></Card>
              : <Empty description="No final execution summary" />,
          },
          { key: 'logs', label: '执行日志', children: <Table dataSource={logs} columns={logColumns} rowKey="id" pagination={{ pageSize: 20 }} locale={{ emptyText: '暂无日志' }} /> },
          {
            key: 'human-interventions', label: `Operator decisions (${humanInterventions.length})`,
            children: humanInterventions.length ? (
              <Space direction="vertical" style={{ width: '100%' }}>
                {humanInterventions.map((item) => (
                  <Card key={item.id} size="small" title={`${item.type} · ${item.status}`} extra={formatTime(item.createdAt)}>
                    <Descriptions size="small" column={2}>
                      <Descriptions.Item label="Attempt">{item.attemptId}</Descriptions.Item>
                      <Descriptions.Item label="Checkpoint">{item.checkpointId}</Descriptions.Item>
                      <Descriptions.Item label="Target">{item.targetOrigin}</Descriptions.Item>
                      <Descriptions.Item label="Action">{item.requestedAction}</Descriptions.Item>
                      <Descriptions.Item label="Decision by">{item.decidedBy || '-'}</Descriptions.Item>
                      <Descriptions.Item label="Decision at">{formatTime(item.decidedAt)}</Descriptions.Item>
                    </Descriptions>
                  </Card>
                ))}
              </Space>
            ) : <Empty description="No operator decisions" />,
          },
          {
            key: 'contract', label: 'Inputs and schemas',
            children: (
              <Space direction="vertical" size="large" style={{ width: '100%' }}>
                <Card size="small" title="Immutable task inputs"><pre className="code-preview">{JSON.stringify(task.variables, null, 2)}</pre></Card>
                <Card size="small" title="Input schema"><pre className="code-preview">{JSON.stringify(task.inputSchema, null, 2)}</pre></Card>
                <Card size="small" title="Output schema"><pre className="code-preview">{JSON.stringify(results?.outputSchema ?? task.outputSchema, null, 2)}</pre></Card>
              </Space>
            ),
          },
        ]}
      />

      {confirm && <ConfirmModal title={confirm.title} content={confirm.content} open={confirm.open} onConfirm={confirm.onOk} onCancel={() => setConfirm(null)} />}
    </div>
  );
}
