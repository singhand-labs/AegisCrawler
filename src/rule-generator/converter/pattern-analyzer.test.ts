/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { analyzePatterns } from './pattern-analyzer';
import type { PageAgentEvent } from '../types';

describe('pattern-analyzer', () => {
  it('groups consecutive field extracts', () => {
    const events: PageAgentEvent[] = [
      { type: 'navigate', url: 'https://example.com/', timestamp: 1 },
      { type: 'extract', index: 1, fieldName: 'title', mode: 'text', annotation: 'field', timestamp: 2 },
      { type: 'extract', index: 1, fieldName: 'price', mode: 'text', annotation: 'field', timestamp: 3 },
      { type: 'click', index: 2, timestamp: 4 },
    ];
    const patterns = analyzePatterns(events);
    expect(patterns.extractGroups).toHaveLength(1);
    expect(patterns.extractGroups[0].type).toBe('field');
    expect(patterns.extractGroups[0].events).toHaveLength(2);
  });

  it('groups consecutive listItem extracts', () => {
    const events: PageAgentEvent[] = [
      { type: 'extract', index: 1, fieldName: 'title', mode: 'text', annotation: 'listItem', timestamp: 1 },
      { type: 'extract', index: 1, fieldName: 'price', mode: 'text', annotation: 'listItem', timestamp: 2 },
    ];
    const patterns = analyzePatterns(events);
    expect(patterns.extractGroups).toHaveLength(1);
    expect(patterns.extractGroups[0].type).toBe('listItem');
    expect(patterns.extractGroups[0].events).toHaveLength(2);
  });

  it('detects nextPage click', () => {
    const events: PageAgentEvent[] = [
      { type: 'click', index: 1, annotation: 'nextPage', timestamp: 1 },
    ];
    const patterns = analyzePatterns(events);
    expect(patterns.pagination).toHaveLength(1);
    expect(patterns.pagination[0].eventIndex).toBe(0);
  });

  it('detects login and captcha annotations', () => {
    const events: PageAgentEvent[] = [
      { type: 'click', index: 1, annotation: 'login', timestamp: 1 },
      { type: 'click', index: 2, annotation: 'captcha', timestamp: 2 },
    ];
    const patterns = analyzePatterns(events);
    expect(patterns.auth).toHaveLength(2);
    expect(patterns.auth[0].type).toBe('login');
    expect(patterns.auth[1].type).toBe('captcha');
  });

  it('ignores clicks with unknown annotations', () => {
    const events: PageAgentEvent[] = [
      { type: 'click', index: 1, annotation: 'unknown' as any, timestamp: 1 },
    ];
    const patterns = analyzePatterns(events);
    expect(patterns.pagination).toHaveLength(0);
    expect(patterns.auth).toHaveLength(0);
  });

  it('breaks extract group when annotation changes', () => {
    const events: PageAgentEvent[] = [
      { type: 'extract', index: 1, fieldName: 'title', mode: 'text', annotation: 'field', timestamp: 1 },
      { type: 'extract', index: 1, fieldName: 'price', mode: 'text', annotation: 'listItem', timestamp: 2 },
    ];
    const patterns = analyzePatterns(events);
    expect(patterns.extractGroups).toHaveLength(2);
    expect(patterns.extractGroups[0].events).toHaveLength(1);
    expect(patterns.extractGroups[1].events).toHaveLength(1);
  });

});
