import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import RuleJsonDiff from './RuleJsonDiff';

describe('RuleJsonDiff', () => {
  it('highlights added and removed lines', () => {
    const baseline = { name: 'baseline', steps: [{ action: 'navigate' }] };
    const enhanced = { name: 'enhanced', steps: [{ action: 'navigate' }, { action: 'click' }] };
    render(<RuleJsonDiff baseline={baseline} enhanced={enhanced} />);

    const add = document.querySelector('.diff-add');
    const remove = document.querySelector('.diff-remove');
    expect(add).toBeInTheDocument();
    expect(remove).toBeInTheDocument();
    expect(add).toHaveTextContent('enhanced');
    expect(remove).toHaveTextContent('baseline');
    expect(screen.getByText(/navigate/)).toBeInTheDocument();
  });

  it('renders identical objects as neutral', () => {
    const obj = { name: 'same' };
    render(<RuleJsonDiff baseline={obj} enhanced={obj} />);
    expect(document.querySelector('.diff-neutral')).toBeInTheDocument();
    expect(document.querySelector('.diff-add')).not.toBeInTheDocument();
    expect(document.querySelector('.diff-remove')).not.toBeInTheDocument();
  });
});
