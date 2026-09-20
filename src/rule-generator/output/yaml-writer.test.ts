/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { writeYaml } from './yaml-writer';
import type { Rule } from '../types';

describe('yaml-writer', () => {
  it('writes a minimal rule', () => {
    const rule: Rule = {
      id: 'test',
      version: '1.0.0',
      name: 'Test',
      domain: 'example.com',
      enabled: true,
      steps: [{ action: 'navigate', url: 'https://example.com/' }],
    };
    const out = writeYaml(rule);
    expect(out).toContain('id: test');
    expect(out).toContain('action: navigate');
  });
});
