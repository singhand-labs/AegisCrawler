import { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Checkbox, List, Space, Tag, Typography } from 'antd';
import type { PatchOperation } from '../api/types';

const { Text } = Typography;

interface Props {
  baseline: object;
  patch?: PatchOperation[];
  onChange?: (selected: PatchOperation[], preview: object) => void;
}

function decodePointerToken(token: string): string {
  return token.replace(/~1/g, '/').replace(/~0/g, '~');
}

function parsePath(path: string): string[] {
  if (path === '') return [];
  const parts = path.split('/');
  if (parts[0] === '') parts.shift();
  return parts.map(decodePointerToken);
}

function getParentAndKey(
  root: Record<string, unknown> | unknown[],
  path: string[],
): { parent: Record<string, unknown> | unknown[]; key: string | number } | null {
  if (path.length === 0) return null;
  let current: unknown = root;
  for (let i = 0; i < path.length - 1; i++) {
    const key = path[i];
    if (Array.isArray(current)) {
      const index = Number(key);
      if (Number.isNaN(index) || index < 0 || index >= current.length) return null;
      current = current[index];
    } else if (current && typeof current === 'object') {
      current = (current as Record<string, unknown>)[key];
    } else {
      return null;
    }
  }
  const last = path[path.length - 1];
  if (Array.isArray(current)) {
    if (last === '-') {
      return { parent: current, key: current.length };
    }
    const index = Number(last);
    if (Number.isNaN(index)) return null;
    return { parent: current, key: index };
  }
  if (current && typeof current === 'object') {
    return { parent: current as Record<string, unknown>, key: last };
  }
  return null;
}

export function applyPatchOperations(baseline: object, patch: PatchOperation[]): object {
  const result = JSON.parse(JSON.stringify(baseline)) as Record<string, unknown>;
  for (const op of patch) {
    const path = parsePath(op.path);
    const target = getParentAndKey(result, path);
    if (!target) continue;
    const { parent, key } = target;
    switch (op.op) {
      case 'add':
      case 'replace': {
        if (Array.isArray(parent)) {
          const index = Number(key);
          if (Number.isNaN(index)) continue;
          if (op.op === 'add') {
            // C-1: RFC 6902 'add' to an array index INSERTS at that index,
            // shifting subsequent elements right. Previously this shared the
            // 'replace' path which overwrote the element, silently destroying it.
            if (index >= 0 && index <= parent.length) {
              parent.splice(index, 0, op.value);
            }
          } else if (index >= 0 && index < parent.length) {
            parent[index] = op.value;
          }
        } else {
          parent[key] = op.value;
        }
        break;
      }
      case 'remove': {
        if (Array.isArray(parent)) {
          const index = Number(key);
          if (!Number.isNaN(index) && index >= 0 && index < parent.length) {
            parent.splice(index, 1);
          }
        } else {
          delete parent[key];
        }
        break;
      }
      default:
        break;
    }
  }
  return result;
}

// M-5: stable default so the useEffect dependency doesn't change on every
// render (a literal [] default creates a new reference each render, causing
// an infinite effect → setState → re-render loop).
const DEFAULT_PATCH: PatchOperation[] = [];

export default function PatchEditor({ baseline, patch = DEFAULT_PATCH, onChange }: Props) {
  const [selectedIds, setSelectedIds] = useState<Set<number>>(() => new Set(patch.map((_, i) => i)));

  // M-5: reset selection when a new patch arrives. The useState initializer
  // only runs on mount; without this, a shorter replacement patch inherits
  // stale indices from the old patch (wrong count display, potentially wrong
  // operations selected when indices overlap).
  useEffect(() => {
    setSelectedIds(new Set(patch.map((_, i) => i)));
  }, [patch]);

  const selectedOps = useMemo(
    () => patch.filter((_, i) => selectedIds.has(i)),
    [patch, selectedIds],
  );

  const preview = useMemo(
    () => applyPatchOperations(baseline, selectedOps),
    [baseline, selectedOps],
  );

  const toggle = (index: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  };

  const selectAll = () => setSelectedIds(new Set(patch.map((_, i) => i)));
  const selectNone = () => setSelectedIds(new Set());

  const handleApply = () => {
    onChange?.(selectedOps, preview);
  };

  if (patch.length === 0) {
    return <Alert type="info" message="无 patch 数据" description="本次增强未返回可编辑的 patch 操作列表。" showIcon />;
  }

  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Space>
        <Button size="small" onClick={selectAll}>
          全部启用
        </Button>
        <Button size="small" onClick={selectNone}>
          全部禁用
        </Button>
        <Text type="secondary">已启用 {selectedIds.size} / {patch.length} 项</Text>
      </Space>
      <List
        size="small"
        bordered
        dataSource={patch}
        renderItem={(op, index) => (
          <List.Item
            actions={[
              <Checkbox
                key="toggle"
                checked={selectedIds.has(index)}
                onChange={() => toggle(index)}
              >
                启用
              </Checkbox>,
            ]}
          >
            <Space>
              <Tag color="blue">{op.op}</Tag>
              <Text code>{op.path}</Text>
              {op.value !== undefined && (
                <Text type="secondary" ellipsis style={{ maxWidth: 240 }}>
                  {JSON.stringify(op.value)}
                </Text>
              )}
            </Space>
          </List.Item>
        )}
      />
      <Button type="primary" onClick={handleApply}>
        应用选中项
      </Button>
    </Space>
  );
}
