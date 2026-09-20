import { describe, it, expect } from 'vitest';
import { formatTime, formatDate } from './time';

describe('time utils', () => {
  it('formats ISO timestamp to local date time', () => {
    const result = formatTime('2026-07-08T12:34:56Z');
    expect(result).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
  });

  it('returns dash for empty value', () => {
    expect(formatTime('')).toBe('-');
    expect(formatTime(null)).toBe('-');
    expect(formatDate(undefined)).toBe('-');
  });
});
