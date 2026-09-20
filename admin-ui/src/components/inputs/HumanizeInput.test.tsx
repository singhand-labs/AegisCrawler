import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import HumanizeInput from './HumanizeInput';

describe('HumanizeInput', () => {
  it('renders default preDelay range', () => {
    render(<HumanizeInput value={{ preDelay: [100, 200] }} onChange={vi.fn()} />);
    expect(screen.getByDisplayValue('100')).toBeInTheDocument();
    expect(screen.getByDisplayValue('200')).toBeInTheDocument();
  });

  it('updates range through onChange', () => {
    const onChange = vi.fn();
    render(<HumanizeInput value={{ preDelay: [100, 200] }} onChange={onChange} />);
    const [minInput] = screen.getAllByPlaceholderText('最小值');
    fireEvent.change(minInput, { target: { value: '150' } });
    expect(onChange).toHaveBeenCalledWith({ preDelay: [150, 200] });
  });

  it('clears value', () => {
    const onChange = vi.fn();
    render(<HumanizeInput value={{ preDelay: [100, 200] }} onChange={onChange} />);
    fireEvent.click(screen.getByText('清空'));
    expect(onChange).toHaveBeenCalledWith(undefined);
  });
});
