import { render, screen, fireEvent, cleanup } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, afterEach } from 'vitest';
import RuleEditorToolbar from './RuleEditorToolbar';

afterEach(() => {
  cleanup();
});

describe('RuleEditorToolbar', () => {
  it('disables insert before/after and keeps append enabled when no action is selected', async () => {
    const onAdd = vi.fn();
    const user = userEvent.setup();
    render(<RuleEditorToolbar onAddAction={onAdd} />);

    await user.click(screen.getByRole('button', { name: /新增动作/ }));
    const items = screen.getAllByRole('menuitem');
    expect(items).toHaveLength(3);
    expect(items[0]).toHaveTextContent('在选中前插入');
    expect(items[1]).toHaveTextContent('在选中后插入');
    expect(items[2]).toHaveTextContent('追加到末尾');

    expect(items[0]).toHaveAttribute('aria-disabled', 'true');
    expect(items[1]).toHaveAttribute('aria-disabled', 'true');
    expect(items[2]).not.toHaveAttribute('aria-disabled', 'true');

    await user.click(items[2]);
    expect(onAdd).toHaveBeenCalledTimes(1);
    expect(onAdd).toHaveBeenCalledWith('append');
  });

  it('enables insert before/after when an action is selected', async () => {
    const onAdd = vi.fn();
    const user = userEvent.setup();
    render(<RuleEditorToolbar selectedPath={['steps', 0]} onAddAction={onAdd} />);

    await user.click(screen.getByRole('button', { name: /新增动作/ }));
    const items = screen.getAllByRole('menuitem');
    expect(items[0]).not.toHaveAttribute('aria-disabled', 'true');
    expect(items[1]).not.toHaveAttribute('aria-disabled', 'true');

    await user.click(items[0]);
    expect(onAdd).toHaveBeenCalledWith('before');
  });

  it('enables delete, move and clone buttons when an action is selected', () => {
    render(<RuleEditorToolbar selectedPath={['steps', 1]} siblingCount={3} />);

    expect(screen.getByRole('button', { name: '删除选中动作' })).not.toBeDisabled();
    expect(screen.getByRole('button', { name: '上移' })).not.toBeDisabled();
    expect(screen.getByRole('button', { name: '下移' })).not.toBeDisabled();
    expect(screen.getByRole('button', { name: '克隆选中动作' })).not.toBeDisabled();
  });

  it('disables move up at the first action in an array', () => {
    render(<RuleEditorToolbar selectedPath={['steps', 0]} siblingCount={3} />);

    expect(screen.getByRole('button', { name: '上移' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '下移' })).not.toBeDisabled();
  });

  it('disables move down at the last action in an array', () => {
    render(<RuleEditorToolbar selectedPath={['steps', 2]} siblingCount={3} />);

    expect(screen.getByRole('button', { name: '上移' })).not.toBeDisabled();
    expect(screen.getByRole('button', { name: '下移' })).toBeDisabled();
  });

  it('calls onSave when the save button is clicked', () => {
    const onSave = vi.fn();
    render(<RuleEditorToolbar isDirty onSave={onSave} />);

    fireEvent.click(screen.getByRole('button', { name: '保存规则' }));
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it('disables save when not dirty or while saving', () => {
    const onSave = vi.fn();
    const { rerender } = render(<RuleEditorToolbar onSave={onSave} />);
    expect(screen.getByRole('button', { name: '保存规则' })).toBeDisabled();

    rerender(<RuleEditorToolbar isDirty isSaving onSave={onSave} />);
    expect(screen.getByRole('button', { name: '保存中…' })).toBeDisabled();
  });

  it('shows unsaved changes text when dirty', () => {
    const { rerender } = render(<RuleEditorToolbar />);
    expect(screen.queryByText('有未保存的修改')).not.toBeInTheDocument();

    rerender(<RuleEditorToolbar isDirty />);
    expect(screen.getByText('有未保存的修改')).toBeInTheDocument();
  });

  it('triggers save, delete and undo via keyboard shortcuts', async () => {
    const onSave = vi.fn();
    const onDelete = vi.fn();
    const onUndo = vi.fn();
    const user = userEvent.setup();
    render(
      <RuleEditorToolbar
        selectedPath={['steps', 1]}
        isDirty
        canUndo
        onSave={onSave}
        onDeleteAction={onDelete}
        onUndo={onUndo}
      />,
    );

    await user.keyboard('{Control>}s{/Control}');
    expect(onSave).toHaveBeenCalledTimes(1);

    await user.keyboard('{Delete}');
    expect(onDelete).toHaveBeenCalledTimes(1);

    await user.keyboard('{Control>}z{/Control}');
    expect(onUndo).toHaveBeenCalledTimes(1);
  });

  it('triggers save and undo via Cmd+S / Cmd+Z keyboard shortcuts', async () => {
    const onSave = vi.fn();
    const onUndo = vi.fn();
    const user = userEvent.setup();
    render(
      <RuleEditorToolbar
        selectedPath={['steps', 1]}
        isDirty
        canUndo
        onSave={onSave}
        onUndo={onUndo}
      />,
    );

    await user.keyboard('{Meta>}s{/Meta}');
    expect(onSave).toHaveBeenCalledTimes(1);

    await user.keyboard('{Meta>}z{/Meta}');
    expect(onUndo).toHaveBeenCalledTimes(1);
  });

  it('does not trigger callbacks via keyboard shortcuts when disabled or preconditions are not met', async () => {
    const onSave = vi.fn();
    const onDelete = vi.fn();
    const onUndo = vi.fn();
    const user = userEvent.setup();
    render(
      <RuleEditorToolbar
        disabled
        isDirty
        canUndo
        onSave={onSave}
        onDeleteAction={onDelete}
        onUndo={onUndo}
      />,
    );

    await user.keyboard('{Control>}s{/Control}');
    await user.keyboard('{Delete}');
    await user.keyboard('{Control>}z{/Control}');

    expect(onSave).not.toHaveBeenCalled();
    expect(onDelete).not.toHaveBeenCalled();
    expect(onUndo).not.toHaveBeenCalled();
  });
});
