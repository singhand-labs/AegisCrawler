import type { JSONSchema, Result } from '../api/types';

export interface ResultRow {
  key: string;
  attemptId: string;
  sequence: number;
  createdAt: string;
  data: Record<string, unknown>;
}

function asRow(value: unknown): Record<string, unknown> {
  if (value !== null && !Array.isArray(value) && typeof value === 'object') {
    return value as Record<string, unknown>;
  }
  return { value };
}

export function resultRows(batches: Result[]): ResultRow[] {
  return batches.flatMap((batch) => {
    const values = Array.isArray(batch.payload) ? batch.payload : [batch.payload];
    return values.map((value, index) => ({
      key: `${batch.id}:${index}`,
      attemptId: batch.attemptId,
      sequence: batch.sequence,
      createdAt: batch.createdAt,
      data: asRow(value),
    }));
  });
}

export function resultFields(schema: JSONSchema | undefined, rows: ResultRow[]): string[] {
  const declared = Object.keys(schema?.properties ?? {});
  if (declared.length > 0) return declared;
  return Array.from(new Set(rows.flatMap((row) => Object.keys(row.data))));
}

function csvCell(value: unknown): string {
  let text = value === null || value === undefined
    ? ''
    : typeof value === 'object' ? JSON.stringify(value) : String(value);
  // Prevent spreadsheet formula execution while preserving the displayed value.
  if (/^[=+\-@]/.test(text)) text = `'${text}`;
  return `"${text.replace(/"/g, '""')}"`;
}

export function resultsToCSV(fields: string[], rows: ResultRow[]): string {
  const header = fields.map(csvCell).join(',');
  const body = rows.map((row) => fields.map((field) => csvCell(row.data[field])).join(','));
  return [header, ...body].join('\r\n');
}

export function resultsToJSON(rows: ResultRow[]): string {
  return JSON.stringify(rows.map((row) => row.data), null, 2);
}
