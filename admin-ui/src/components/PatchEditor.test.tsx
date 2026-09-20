import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import PatchEditor from './PatchEditor';
import type { PatchOperation } from '../api/types';

const baseline = { name: 'baseline', steps: [{ action: 'navigate' }] };
const patch: PatchOperation[] = [
  { op: 'replace', path: '/name', value: 'enhanced' },
  { op: 'add', path: '/steps/-', value: { action: 'click' } },
];

describe('PatchEditor', () => {
  it('renders patch ops and calls onChange with selected preview', () => {
    const onChange = vi.fn();
    render(<PatchEditor baseline={baseline} patch={patch} onChange={onChange} />);

    expect(screen.getByText('/name')).toBeInTheDocument();
    expect(screen.getByText('/steps/-')).toBeInTheDocument();
    expect(screen.getByText('应用选中项')).toBeInTheDocument();

    fireEvent.click(screen.getByText('应用选中项'));
    expect(onChange).toHaveBeenCalledTimes(1);
    const [selected, preview] = onChange.mock.calls[0];
    expect(selected).toHaveLength(2);
    expect((preview as { name: string }).name).toBe('enhanced');
    expect((preview as { steps: unknown[] }).steps).toHaveLength(2);
  });

  it('add to array index inserts (shifts right), not replaces (C-1)', () => {
    // C-1 regression: RFC 6902 'add' to /steps/1 must INSERT at index 1,
    // shifting the existing element to index 2. Previously the code shared
    // the 'replace' path which overwrote the element, silently destroying it.
    const onChange = vi.fn();
    const multiStepBaseline = { name: 'baseline', steps: [{ action: 'navigate' }, { action: 'click' }, { action: 'extract' }] };
    const insertPatch: PatchOperation[] = [
      { op: 'add', path: '/steps/1', value: { action: 'extractText' } },
    ];
    render(<PatchEditor baseline={multiStepBaseline} patch={insertPatch} onChange={onChange} />);
    fireEvent.click(screen.getByText('应用选中项'));
    const [, preview] = onChange.mock.calls[0];
    const steps = (preview as { steps: Array<{ action: string }> }).steps;
    // Insert at index 1 → length grows from 3 to 4; original elements survive.
    expect(steps).toHaveLength(4);
    expect(steps[0].action).toBe('navigate');
    expect(steps[1].action).toBe('extractText');  // newly inserted
    expect(steps[2].action).toBe('click');          // shifted right, not destroyed
    expect(steps[3].action).toBe('extract');        // shifted right
  });

  it('disables an operation and recomputes preview', () => {
    const onChange = vi.fn();
    render(<PatchEditor baseline={baseline} patch={patch} onChange={onChange} />);

    const checkboxes = screen.getAllByRole('checkbox');
    expect(checkboxes).toHaveLength(2);
    fireEvent.click(checkboxes[1]);

    fireEvent.click(screen.getByText('应用选中项'));
    const [selected, preview] = onChange.mock.calls[0];
    expect(selected).toHaveLength(1);
    expect(selected[0].path).toBe('/name');
    expect((preview as { steps: unknown[] }).steps).toHaveLength(1);
  });

  it('supports select all and select none', () => {
    render(<PatchEditor baseline={baseline} patch={patch} />);
    const checkboxes = screen.getAllByRole('checkbox');
    expect(checkboxes.every((c) => (c as HTMLInputElement).checked)).toBe(true);

    fireEvent.click(screen.getByText('全部禁用'));
    expect(screen.getAllByRole('checkbox').every((c) => !(c as HTMLInputElement).checked)).toBe(true);

    fireEvent.click(screen.getByText('全部启用'));
    expect(screen.getAllByRole('checkbox').every((c) => (c as HTMLInputElement).checked)).toBe(true);
  });

  it('shows empty state when patch is missing', () => {
    render(<PatchEditor baseline={baseline} />);
    expect(screen.getByText('无 patch 数据')).toBeInTheDocument();
  });

  it('resets selection when the patch prop changes (M-5)', () => {
    // First render: 3 operations, all selected by default.
    const patch3: PatchOperation[] = [
      { op: 'replace', path: '/name', value: 'a' },
      { op: 'add', path: '/steps/-', value: { action: 'click' } },
      { op: 'remove', path: '/steps/0' },
    ];
    const { rerender } = render(<PatchEditor baseline={baseline} patch={patch3} />);
    expect(screen.getByText('已启用 3 / 3 项')).toBeInTheDocument();

    // New enhancement returns a shorter patch — selection must reset,
    // not retain stale indices from the old patch.
    const patch2: PatchOperation[] = [
      { op: 'replace', path: '/name', value: 'b' },
      { op: 'add', path: '/steps/-', value: { action: 'extract' } },
    ];
    rerender(<PatchEditor baseline={baseline} patch={patch2} />);
    expect(screen.getByText('已启用 2 / 2 项')).toBeInTheDocument();
  });
});
