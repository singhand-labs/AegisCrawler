import { useEffect, useState } from 'react';

/**
 * M-1: debounce a value so rapidly-changing state (e.g., text filter input
 * firing on every keystroke) doesn't trigger an API request per keystroke.
 * The returned value only updates after `delayMs` of no changes, coalescing
 * a burst of updates into a single emission.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}
