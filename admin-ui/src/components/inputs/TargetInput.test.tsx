import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import TargetInput from './TargetInput';

describe('TargetInput', () => {
  it('renders default selector value', () => {
    render(<TargetInput value={{ selector: '.item' }} onChange={vi.fn()} />);
    expect(screen.getByDisplayValue('.item')).toBeInTheDocument();
  });

  it('updates field through onChange', () => {
    const onChange = vi.fn();
    render(<TargetInput value={{ selector: '.item' }} onChange={onChange} />);
    const input = screen.getByDisplayValue('.item');
    fireEvent.change(input, { target: { value: '.title' } });
    expect(onChange).toHaveBeenCalledWith({ selector: '.title' });
  });

  it('preserves unrelated fields when switching modes', () => {
    const onChange = vi.fn();
    render(
      <TargetInput
        value={{ selector: '.item', frame: 'my-frame', timeout: 5000 }}
        onChange={onChange}
      />
    );
    fireEvent.click(screen.getByText('XPath'));
    expect(onChange).toHaveBeenCalledWith({
      xpath: '',
      frame: 'my-frame',
      timeout: 5000,
    });
  });

  it('clears value', () => {
    const onChange = vi.fn();
    render(<TargetInput value={{ selector: '.item' }} onChange={onChange} />);
    fireEvent.click(screen.getByText('清空'));
    expect(onChange).toHaveBeenCalledWith(undefined);
  });
});
