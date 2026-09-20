/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { convert, convertToYaml } from './index';
import type { PageAgentRecording } from './index';

const minimalRecording: PageAgentRecording = {
  version: '1.0.0',
  meta: {
    startUrl: 'https://example.com/',
    title: 'Test',
    recordedAt: '2026-07-04T10:00:00Z',
    domain: 'example.com',
  },
  events: [],
  snapshots: [],
};

describe('rule-generator entrypoint', () => {
  it('convert() returns a Rule from a recording', () => {
    const rule = convert(minimalRecording);
    expect(rule.version).toBe('1.0.0');
    expect(rule.name).toBe('录制-Test');
    expect(rule.domain).toBe('example.com');
    expect(rule.steps).toEqual([]);
    expect(rule.variables).toEqual({});
  });

  it('convertToYaml() returns YAML output', () => {
    const yaml = convertToYaml(minimalRecording);
    expect(yaml).toContain('id:');
    expect(yaml).toContain('name: 录制-Test');
    expect(yaml).toContain('domain: example.com');
  });
});
