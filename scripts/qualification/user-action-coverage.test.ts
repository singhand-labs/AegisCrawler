import { describe, expect, it } from 'vitest';
import { USER_ACTION_COVERAGE, buildCoverageReport } from './user-action-coverage';

describe('whole-product user-action coverage', () => {
  it('classifies every entry exactly once with evidence', () => {
    const report = buildCoverageReport();
    expect(report.schema).toBe('aegiscrawler.user-action-coverage.v1');
    expect(new Set(report.entries.map((entry) => entry.id)).size).toBe(report.entries.length);
    expect(report.entries.every((entry) => entry.evidence.length > 0)).toBe(true);
  });

  it('keeps restricted public behavior out of public coverage', () => {
    const restricted = USER_ACTION_COVERAGE.filter((entry) =>
      ['action:solveCaptcha', 'action:uploadFile', 'action:handleDownload',
        'action:setCookie', 'action:evaluate'].includes(entry.id));
    expect(restricted).toHaveLength(5);
    expect(restricted.every((entry) => entry.classification !== 'covered-public')).toBe(true);
  });

  it('covers every product surface', () => {
    expect(new Set(USER_ACTION_COVERAGE.map((entry) => entry.surface))).toEqual(new Set([
      'browser-action', 'extension', 'admin', 'mcp', 'lifecycle',
    ]));
  });
});
