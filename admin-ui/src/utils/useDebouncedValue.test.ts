import { renderHook, act } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { useDebouncedValue } from './useDebouncedValue';

describe('useDebouncedValue', () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it('returns the initial value immediately', () => {
    const { result } = renderHook(() => useDebouncedValue('hello', 300));
    expect(result.current).toBe('hello');
  });

  it('does not update until the delay elapses', () => {
    const { result, rerender } = renderHook(({ value }) => useDebouncedValue(value, 300), {
      initialProps: { value: 'a' },
    });
    rerender({ value: 'ab' });
    expect(result.current).toBe('a'); // still old value
    act(() => { vi.advanceTimersByTime(299); });
    expect(result.current).toBe('a'); // 1ms before threshold
    act(() => { vi.advanceTimersByTime(1); });
    expect(result.current).toBe('ab'); // after 300ms
  });

  it('only fires once after rapid changes (M-1 debounce)', () => {
    const { result, rerender } = renderHook(({ value }) => useDebouncedValue(value, 300), {
      initialProps: { value: '' },
    });
    // Simulate rapid typing: a, ab, abc, abcd
    rerender({ value: 'a' });
    rerender({ value: 'ab' });
    rerender({ value: 'abc' });
    rerender({ value: 'abcd' });
    // No update yet
    expect(result.current).toBe('');
    // After one delay, only the final value propagates
    act(() => { vi.advanceTimersByTime(300); });
    expect(result.current).toBe('abcd');
  });

  it('resets the timer on each change', () => {
    const { result, rerender } = renderHook(({ value }) => useDebouncedValue(value, 300), {
      initialProps: { value: 'x' },
    });
    rerender({ value: 'xy' });
    act(() => { vi.advanceTimersByTime(200); }); // 200ms
    rerender({ value: 'xyz' });                  // reset timer
    act(() => { vi.advanceTimersByTime(200); }); // 200ms after reset
    expect(result.current).toBe('x'); // still old
    act(() => { vi.advanceTimersByTime(100); }); // 300ms after reset
    expect(result.current).toBe('xyz'); // now updated
  });
});
