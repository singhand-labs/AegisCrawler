import { Badge, Tag } from 'antd';
import type { TaskStatus, RuleApprovalStatus } from '../api/types';

interface StatusBadgeProps {
  status: TaskStatus | RuleApprovalStatus | string;
}

const taskStatusMap: Record<string, { color: string; text: string }> = {
  pending: { color: 'default', text: '待执行' },
  leased: { color: 'processing', text: '已领取' },
  running: { color: 'processing', text: '执行中' },
  done: { color: 'success', text: '完成' },
  failed: { color: 'error', text: '失败' },
  cancelled: { color: 'warning', text: '已取消' },
  waiting_for_human: { color: 'purple', text: '等待人工' },
  dead_letter: { color: 'red', text: '死信' },
};

const approvalStatusMap: Record<string, { color: string; text: string }> = {
  pending: { color: 'default', text: '待审核' },
  approved: { color: 'success', text: '已通过' },
  rejected: { color: 'error', text: '已拒绝' },
};

export default function StatusBadge({ status }: StatusBadgeProps) {
  const lower = status.toLowerCase();
  const mapped = taskStatusMap[lower] || approvalStatusMap[lower] || { color: 'default', text: status };
  return <Badge status={mapped.color as never} text={mapped.text} />;
}

export function PriorityTag({ priority }: { priority: string }) {
  const color = priority === 'high' ? 'red' : priority === 'low' ? 'blue' : 'green';
  const text = priority === 'high' ? '高' : priority === 'low' ? '低' : '普通';
  return <Tag color={color}>{text}</Tag>;
}
