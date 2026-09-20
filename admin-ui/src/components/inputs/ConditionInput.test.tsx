import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import ConditionInput from './ConditionInput';

describe('ConditionInput', () => {
  it('renders default text condition', () => {
    render(<ConditionInput value={{ type: 'textContains', text: 'hello' }} onChange={vi.fn()} />);
    expect(screen.getByDisplayValue('hello')).toBeInTheDocument();
  });

  it('updates text field through onChange', () => {
    const onChange = vi.fn();
    render(<ConditionInput value={{ type: 'textContains', text: 'hello' }} onChange={onChange} />);
    const input = screen.getByDisplayValue('hello');
    fireEvent.change(input, { target: { value: 'world' } });
    expect(onChange).toHaveBeenCalledWith({ type: 'textContains', text: 'world' });
  });

  it('clears value', () => {
    const onChange = vi.fn();
    render(<ConditionInput value={{ type: 'textContains', text: 'hello' }} onChange={onChange} />);
    fireEvent.click(screen.getByText('清空'));
    expect(onChange).toHaveBeenCalledWith(undefined);
  });
});
