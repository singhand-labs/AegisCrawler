import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import StatusBadge, { PriorityTag } from './StatusBadge';

describe('StatusBadge', () => {
  it.each([
    ['done', '完成'],
    ['failed', '失败'],
    ['pending', '待执行'],
    ['dead_letter', '死信'],
    ['approved', '已通过'],
  ])('renders %s as "%s"', (status, text) => {
    render(<StatusBadge status={status} />);
    expect(screen.getByText(text)).toBeInTheDocument();
  });
});

describe('PriorityTag', () => {
  it('renders high priority', () => {
    render(<PriorityTag priority="high" />);
    expect(screen.getByText('高')).toBeInTheDocument();
  });
});
