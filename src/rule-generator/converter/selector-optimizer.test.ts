/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { optimizeSelector } from './selector-optimizer';
import type { DomElementInfo } from '../types';

function info(partial: Partial<DomElementInfo>): DomElementInfo {
  return {
    index: 1,
    tagName: 'button',
    selector: '.btn',
    boundingRect: { x: 0, y: 0, width: 10, height: 10 },
    ...partial,
  };
}

describe('selector-optimizer', () => {
  it('prefers id', () => {
    const result = optimizeSelector(info({ selector: 'button#search-btn.btn' }), { allSelectors: [] });
    expect(result).toBe('#search-btn');
  });

  it('prefers data-testid', () => {
    const result = optimizeSelector(info({ selector: 'button[data-testid="submit"]' }), { allSelectors: [] });
    expect(result).toBe('[data-testid="submit"]');
  });

  it('falls back to data-qa when data-testid is absent', () => {
    const result = optimizeSelector(info({ selector: 'button[data-qa="search-btn"]' }), { allSelectors: [] });
    expect(result).toBe('[data-qa="search-btn"]');
  });

  it('falls back to data-automation-id when data-testid and data-qa are absent', () => {
    const result = optimizeSelector(info({ selector: 'button[data-automation-id="submit-btn"]' }), { allSelectors: [] });
    expect(result).toBe('[data-automation-id="submit-btn"]');
  });

  it('prefers name attribute', () => {
    const result = optimizeSelector(info({ tagName: 'input', selector: 'input', name: 'q' }), { allSelectors: [] });
    expect(result).toBe('input[name="q"]');
  });

  it('uses aria-label', () => {
    const result = optimizeSelector(info({ selector: 'button', ariaLabel: 'Close dialog' }), { allSelectors: [] });
    expect(result).toBe('[aria-label="Close dialog"]');
  });

  it('uses unique class combination', () => {
    const result = optimizeSelector(info({ selector: 'button.btn.primary' }), { allSelectors: ['button.btn.primary'] });
    expect(result).toBe('.btn.primary');
  });

  it('does not confuse similar class names when checking uniqueness', () => {
    const result = optimizeSelector(info({ selector: 'button.btn.primary' }), {
      allSelectors: ['button.btn.primary', 'button.btn.primary-extra'],
    });
    expect(result).toBe('.btn.primary');
  });

  it('falls back to next strategy when class combination is not unique', () => {
    const result = optimizeSelector(info({ selector: 'button.btn.primary' }), {
      allSelectors: ['button.btn.primary', 'a.btn.primary'],
    });
    expect(result).toBe('button.btn.primary');
  });

  it('uses placeholder for inputs', () => {
    const result = optimizeSelector(info({ tagName: 'input', selector: 'input', placeholder: 'Search...' }), {
      allSelectors: [],
    });
    expect(result).toBe('input[placeholder="Search..."]');
  });

  it('does not treat hash in attribute values as an id', () => {
    const result = optimizeSelector(info({ selector: 'a[href="#section"]' }), { allSelectors: [] });
    expect(result).toBe('a[href="#section"]');
  });

  it('falls back to original selector', () => {
    const result = optimizeSelector(info({ selector: 'div > span:nth-child(2)' }), { allSelectors: [] });
    expect(result).toBe('div > span:nth-child(2)');
  });
});
