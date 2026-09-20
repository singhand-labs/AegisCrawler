import { useEffect, useMemo } from 'react';
import {
  Button,
  Dropdown,
  Space,
  Tooltip,
  Badge,
} from 'antd';
import type { MenuProps } from 'antd';
import {
  PlusOutlined,
  DeleteOutlined,
  ArrowUpOutlined,
  ArrowDownOutlined,
  CopyOutlined,
  UndoOutlined,
  SaveOutlined,
  DownOutlined,
} from '@ant-design/icons';
import type { ActionPath } from '../utils/ruleEdit';

export interface RuleEditorToolbarProps {
  selectedPath?: ActionPath;
  siblingCount?: number;
  isDirty?: boolean;
  isSaving?: boolean;
  canUndo?: boolean;
  onAddAction?: (position: 'before' | 'after' | 'append') => void;
  onDeleteAction?: () => void;
  onMoveAction?: (direction: 'up' | 'down') => void;
  onCloneAction?: () => void;
  onUndo?: () => void;
  onSave?: () => void;
  disabled?: boolean;
}

/**
 * 判断路径是否指向真实动作节点。
 * 在本项目的 DAG 中，虚拟开始/结束节点的 path 为 `[]`（空数组），
 * 因此只要 path 为空就认为不是真实动作。
 */
function isActionPath(path?: ActionPath): boolean {
  if (!path || path.length === 0) return false;
  return typeof path[path.length - 1] === 'number';
}

function getSelectedIndex(path?: ActionPath): number | undefined {
  if (!isActionPath(path)) return undefined;
  return path![path!.length - 1] as number;
}

export default function RuleEditorToolbar({
  selectedPath,
  siblingCount,
  isDirty = false,
  isSaving = false,
  canUndo = false,
  onAddAction,
  onDeleteAction,
  onMoveAction,
  onCloneAction,
  onUndo,
  onSave,
  disabled = false,
}: RuleEditorToolbarProps) {
  const hasActionSelected = isActionPath(selectedPath);
  const selectedIndex = getSelectedIndex(selectedPath);
  const hasSelection = selectedPath !== undefined && selectedPath.length > 0;

  const addMenuItems: MenuProps['items'] = useMemo(
    () => [
      {
        key: 'before',
        label: '在选中前插入',
        disabled: disabled || !hasSelection,
      },
      {
        key: 'after',
        label: '在选中后插入',
        disabled: disabled || !hasSelection,
      },
      {
        key: 'append',
        label: '追加到末尾',
        disabled,
      },
    ],
    [hasSelection, disabled],
  );

  const handleMenuClick: MenuProps['onClick'] = ({ key }) => {
    onAddAction?.(key as 'before' | 'after' | 'append');
  };

  const canDelete = !disabled && hasActionSelected;
  const canMoveUp =
    !disabled && hasActionSelected && selectedIndex !== undefined && selectedIndex > 0;
  const canMoveDown =
    !disabled &&
    hasActionSelected &&
    selectedIndex !== undefined &&
    (siblingCount === undefined || selectedIndex < siblingCount - 1);
  const canClone = !disabled && hasActionSelected;
  const canUndoBtn = !disabled && isDirty && canUndo && onUndo !== undefined;
  const canSave = !disabled && isDirty && !isSaving && onSave !== undefined;

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (disabled) return;

      const meta = e.ctrlKey || e.metaKey;

      if (meta && e.key.toLowerCase() === 's') {
        if (canSave) {
          e.preventDefault();
          onSave?.();
        }
        return;
      }

      if (e.key === 'Delete') {
        if (canDelete) {
          e.preventDefault();
          onDeleteAction?.();
        }
        return;
      }

      if (meta && e.key.toLowerCase() === 'z') {
        if (canUndoBtn) {
          e.preventDefault();
          onUndo?.();
        }
      }
    };

    document.addEventListener('keydown', handler);
    return () => document.removeEventListener('keydown', handler);
  }, [disabled, canSave, canDelete, canUndoBtn, onSave, onDeleteAction, onUndo]);

  return (
    <Space wrap>
      <Dropdown
        trigger={['click']}
        menu={{ items: addMenuItems, onClick: handleMenuClick }}
        getPopupContainer={(trigger) => trigger.parentElement as HTMLElement}
        disabled={disabled}
      >
        <Button icon={<PlusOutlined />} disabled={disabled} aria-label="新增动作">
          新增动作 <DownOutlined />
        </Button>
      </Dropdown>

      <Tooltip title="删除选中动作">
        <Button
          icon={<DeleteOutlined />}
          aria-label="删除选中动作"
          disabled={!canDelete}
          onClick={onDeleteAction}
        />
      </Tooltip>

      <Tooltip title="上移">
        <Button
          icon={<ArrowUpOutlined />}
          aria-label="上移"
          disabled={!canMoveUp}
          onClick={() => onMoveAction?.('up')}
        />
      </Tooltip>

      <Tooltip title="下移">
        <Button
          icon={<ArrowDownOutlined />}
          aria-label="下移"
          disabled={!canMoveDown}
          onClick={() => onMoveAction?.('down')}
        />
      </Tooltip>

      <Tooltip title="克隆选中动作">
        <Button
          icon={<CopyOutlined />}
          aria-label="克隆选中动作"
          disabled={!canClone}
          onClick={onCloneAction}
        />
      </Tooltip>

      <Tooltip title="撤销修改">
        <Button
          icon={<UndoOutlined />}
          aria-label="撤销修改"
          disabled={!canUndoBtn}
          onClick={onUndo}
        />
      </Tooltip>

      <Badge dot={isDirty && !isSaving} offset={[10, 0]}>
        <Button
          type="primary"
          icon={<SaveOutlined />}
          loading={isSaving}
          disabled={!canSave}
          onClick={onSave}
          aria-label={isSaving ? '保存中…' : '保存规则'}
        >
          {isSaving ? '保存中…' : '保存规则'}
        </Button>
      </Badge>

      {isDirty && !isSaving && (
        <span style={{ color: '#fa8c16' }}>有未保存的修改</span>
      )}
    </Space>
  );
}
