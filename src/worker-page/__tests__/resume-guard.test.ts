/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { validateResume, type WorkerRunState } from '../resume-guard';
import { assertEntryUrlAllowed, isNavigationAllowed, normalizeRunUrl } from '../url-policy';

const runningState: WorkerRunState = {
  taskId: 'task-1',
  ruleId: 'rule-1',
  lastCompletedStepPath: [{ kind: 'top', childIdx: 1 }],
  expectedUrl: 'https://shop.example.com/list',
  expectedUrlPolicy: 'expected-path',
  status: 'running',
};

const ctx = {
  lastCompletedStepPath: [{ kind: 'top' as const, childIdx: 1 }],
  extracted: { items: ['a'] },
  evaluated: {},
  captured: {},
};

describe('validateResume', () => {
  it('accepts a matching expected-path resume', () => {
    expect(validateResume(runningState, 'task-1', 'https://shop.example.com/list?ref=1#x', ctx))
      .toEqual({ ok: true });
  });

  it('accepts a child path of the expected path', () => {
    expect(validateResume(runningState, 'task-1', 'https://shop.example.com/list/page/2', ctx))
      .toEqual({ ok: true });
  });

  it('rejects when no running state exists', () => {
    expect(validateResume(null, 'task-1', 'https://shop.example.com/list', ctx)).toMatchObject({ ok: false });
    expect(validateResume({ ...runningState, status: 'done' }, 'task-1', 'https://shop.example.com/list', ctx))
      .toMatchObject({ ok: false });
  });

  it('rejects a task mismatch', () => {
    expect(validateResume(runningState, 'task-2', 'https://shop.example.com/list', ctx))
      .toMatchObject({ ok: false, reason: expect.stringContaining('does not match') });
  });

  it('rejects an empty expectedUrl (first-step anomaly)', () => {
    expect(validateResume({ ...runningState, expectedUrl: '' }, 'task-1', 'https://shop.example.com/list', ctx))
      .toMatchObject({ ok: false, reason: expect.stringContaining('expectedUrl empty') });
  });

  it('rejects a path change under expected-path policy', () => {
    expect(validateResume(runningState, 'task-1', 'https://shop.example.com/other', ctx))
      .toMatchObject({ ok: false, reason: expect.stringContaining('URL mismatch') });
  });

  it('accepts a path change under same-origin policy', () => {
    const state = { ...runningState, expectedUrlPolicy: 'same-origin' as const };
    expect(validateResume(state, 'task-1', 'https://shop.example.com/elsewhere?x=1', ctx)).toEqual({ ok: true });
  });

  it('rejects an origin change even under same-origin policy', () => {
    const state = { ...runningState, expectedUrlPolicy: 'same-origin' as const };
    expect(validateResume(state, 'task-1', 'https://evil.example.com/list', ctx))
      .toMatchObject({ ok: false, reason: expect.stringContaining('URL mismatch') });
    expect(validateResume(state, 'task-1', 'http://shop.example.com/list', ctx))
      .toMatchObject({ ok: false, reason: expect.stringContaining('URL mismatch') });
  });

  it('rejects stale host context (monotonicity)', () => {
    const staleCtx = { ...ctx, lastCompletedStepPath: [{ kind: 'top' as const, childIdx: 0 }] };
    const verdict = validateResume(runningState, 'task-1', 'https://shop.example.com/list', staleCtx);
    expect(verdict).toMatchObject({ ok: false, reason: expect.stringContaining('stale') });
  });

  it('rejects an unparseable current URL', () => {
    const verdict = validateResume(runningState, 'task-1', 'not a url', ctx);
    expect(verdict).toMatchObject({ ok: false, reason: expect.stringContaining('parse failed') });
  });
});

describe('url-policy', () => {
  const rule = { domain: 'example.com' };

  it('validates entry URLs against rule.domain', () => {
    expect(assertEntryUrlAllowed('https://example.com/a', rule).hostname).toBe('example.com');
    expect(assertEntryUrlAllowed('https://shop.example.com/', rule).hostname).toBe('shop.example.com');
    expect(() => assertEntryUrlAllowed('not a url', rule)).toThrow(/valid URL/);
    expect(() => assertEntryUrlAllowed('ftp://example.com/', rule)).toThrow(/protocol/);
    expect(() => assertEntryUrlAllowed('https://evil.com/', rule)).toThrow(/rule\.domain/);
    expect(() => assertEntryUrlAllowed('https://notexample.com/', rule)).toThrow(/rule\.domain/);
  });

  it('checks navigation targets against rule.domain', () => {
    expect(isNavigationAllowed('https://example.com/x', rule)).toBe(true);
    expect(isNavigationAllowed('https://sub.example.com/x', rule)).toBe(true);
    expect(isNavigationAllowed('https://example.com.evil.com/', rule)).toBe(false);
    expect(isNavigationAllowed('about:blank', rule)).toBe(false);
    expect(isNavigationAllowed('https://other.org/', { domain: ['example.com', 'other.org'] })).toBe(true);
  });

  it('strips query and fragment when normalizing run URLs', () => {
    expect(normalizeRunUrl('https://example.com/a?token=secret#frag')).toBe('https://example.com/a');
  });
});
