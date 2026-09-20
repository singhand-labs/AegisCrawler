import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import ActionEditorPanel from './ActionEditorPanel';
import type { Action } from '@/types/rule';

describe('ActionEditorPanel', () => {
  it('shows generic precondition editor for actions without a native condition', () => {
    const action: Action = {
      action: 'click',
      target: { selector: '.btn' },
    };
    render(<ActionEditorPanel action={action} onChange={vi.fn()} />);

    expect(screen.getByText('前置条件')).toBeInTheDocument();
    expect(screen.queryByText('前置条件已合并')).not.toBeInTheDocument();
  });

  it('merges native condition with precondition for if actions', () => {
    const action: Action = {
      action: 'if',
      condition: { type: 'elementExists', target: { selector: '.x' } },
      then: [{ action: 'click', target: { selector: '.y' } }],
    };
    render(<ActionEditorPanel action={action} onChange={vi.fn()} />);

    expect(screen.getByText('前置条件已合并')).toBeInTheDocument();
    expect(screen.queryByText('前置条件')).not.toBeInTheDocument();
    expect(screen.getByText('分支条件')).toBeInTheDocument();
  });

  it('refreshes initial action when parent passes a value-equal but updated action', () => {
    const action: Action = {
      id: 'a',
      action: 'click',
      target: { selector: '.btn' },
    };
    const onChange = vi.fn();
    const { rerender } = render(<ActionEditorPanel action={action} onChange={onChange} />);

    const targetInput = screen.getByDisplayValue('.btn');
    fireEvent.change(targetInput, { target: { value: '.new' } });

    const updatedAction: Action = {
      id: 'a',
      action: 'click',
      target: { selector: '.updated' },
    };
    rerender(<ActionEditorPanel action={updatedAction} onChange={onChange} />);

    fireEvent.click(screen.getByRole('button', { name: '重置' }));

    expect(screen.getByDisplayValue('.updated')).toBeInTheDocument();
    expect(screen.queryByDisplayValue('.btn')).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue('.new')).not.toBeInTheDocument();
  });

  it('renders click action and updates target before applying', () => {
    const action: Action = {
      action: 'click',
      target: { selector: '.btn' },
    };
    const onChange = vi.fn();
    render(<ActionEditorPanel action={action} onChange={onChange} />);

    const targetInput = screen.getByDisplayValue('.btn');
    fireEvent.change(targetInput, { target: { value: '.submit' } });

    fireEvent.click(screen.getByRole('button', { name: '应用' }));

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        action: 'click',
        target: { selector: '.submit' },
      }),
    );
  });

  it('renders type action and updates value before applying', () => {
    const action: Action = {
      action: 'type',
      target: { selector: '#input' },
      value: 'hello',
    };
    const onChange = vi.fn();
    render(<ActionEditorPanel action={action} onChange={onChange} />);

    const valueInput = screen.getByDisplayValue('hello');
    fireEvent.change(valueInput, { target: { value: 'world' } });

    fireEvent.click(screen.getByRole('button', { name: '应用' }));

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        action: 'type',
        value: 'world',
      }),
    );
  });

  it('renders if container with hint and no child-step editor', () => {
    const action: Action = {
      action: 'if',
      condition: { type: 'elementExists', target: { selector: '.x' } },
      then: [{ action: 'click', target: { selector: '.y' } }],
    };
    render(<ActionEditorPanel action={action} onChange={vi.fn()} />);

    expect(screen.getByText('子步骤请在流程图中编辑')).toBeInTheDocument();
    expect(screen.queryByDisplayValue('.y')).not.toBeInTheDocument();
  });

  it('JSON fallback rejects invalid JSON and accepts valid JSON', () => {
    const action: Action = {
      action: 'abort',
      reason: 'stop',
    };
    const onChange = vi.fn();
    render(<ActionEditorPanel action={action} onChange={onChange} />);

    const jsonInput = screen.getByDisplayValue(/"reason": "stop"/);
    fireEvent.change(jsonInput, { target: { value: '{invalid' } });

    expect(screen.getByText(/JSON 格式错误/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '应用' })).toBeDisabled();

    fireEvent.change(jsonInput, {
      target: { value: '{"action":"abort","reason":"halt"}' },
    });

    expect(screen.queryByText(/JSON 格式错误/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '应用' })).not.toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: '应用' }));

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith({
      action: 'abort',
      reason: 'halt',
    });
  });

  it('resets to initial action when reset is clicked', () => {
    const action: Action = {
      action: 'click',
      target: { selector: '.btn' },
    };
    const onChange = vi.fn();
    render(<ActionEditorPanel action={action} onChange={onChange} />);

    const targetInput = screen.getByDisplayValue('.btn');
    fireEvent.change(targetInput, { target: { value: '.new' } });

    fireEvent.click(screen.getByRole('button', { name: '重置' }));

    expect(screen.getByDisplayValue('.btn')).toBeInTheDocument();
    expect(screen.queryByDisplayValue('.new')).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });

  it('preserves undefined-valued keys across JSON mode round-trip (M-8)', () => {
    // M-8: JSON.stringify strips undefined-valued keys. When the user toggles
    // form → JSON → form without editing, the parsed draft loses those keys,
    // drifting from the original action's key set.
    const action: Action = {
      id: 'a',
      action: 'click',
      target: { selector: '.btn' },
      description: undefined,
    };
    const onChange = vi.fn();
    render(<ActionEditorPanel action={action} onChange={onChange} />);

    // Toggle to JSON mode.
    fireEvent.click(screen.getByRole('button', { name: 'JSON 编辑' }));
    // Toggle back to form mode without editing the JSON.
    fireEvent.click(screen.getByRole('button', { name: '表单编辑' }));

    // Apply and verify the key set matches the original.
    fireEvent.click(screen.getByRole('button', { name: /应用/ }));
    expect(onChange).toHaveBeenCalledTimes(1);
    const applied = onChange.mock.calls[0][0] as Record<string, unknown>;
    expect(Object.keys(applied).sort()).toEqual(Object.keys(action).sort());
  });
});
