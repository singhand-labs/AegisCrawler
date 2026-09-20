import { describe, expect, it } from 'vitest';
import type { Result } from '../api/types';
import { resultFields, resultRows, resultsToCSV, resultsToJSON } from './resultData';

const batch = (payload: unknown): Result => ({
  id: 'result-1', workspaceId: 'default', taskId: 'task-1', workerId: 'worker-1', attemptId: 'attempt-1',
  idempotencyKey: 'key-1', sequence: 1, kind: 'batch', payload, payloadHash: 'hash', valid: true,
  immediate: false, createdAt: '2026-01-01T00:00:00Z',
});

describe('result data helpers', () => {
  it('expands ordered batches into data rows and honors schema field order', () => {
    const rows = resultRows([batch([{ name: 'A', price: 10 }, { name: 'B', price: 20 }])]);
    expect(rows).toHaveLength(2);
    expect(rows[1]).toMatchObject({ attemptId: 'attempt-1', sequence: 1, data: { name: 'B', price: 20 } });
    expect(resultFields({ type: 'object', properties: { price: { type: 'number' }, name: { type: 'string' } } }, rows)).toEqual(['price', 'name']);
  });

  it('exports valid rows as JSON and formula-safe CSV', () => {
    const rows = resultRows([batch([{ name: '=cmd', note: 'a"b' }])]);
    expect(resultsToCSV(['name', 'note'], rows)).toBe('"name","note"\r\n"\'=cmd","a""b"');
    expect(resultsToJSON(rows)).toContain('"name": "=cmd"');
  });
});
