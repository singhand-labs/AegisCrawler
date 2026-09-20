/// <reference types="vitest/globals" />
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { run } from './generate-rule';

describe('generate-rule CLI', () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'generate-rule-test-'));
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  it('generates a YAML rule from a recording', () => {
    const inputPath = path.resolve('examples/sample-recording.json');
    const outputPath = path.join(tmpDir, 'generated.rule.yaml');

    const code = run([inputPath, outputPath]);

    expect(code).toBe(0);
    expect(fs.existsSync(outputPath)).toBe(true);

    const yaml = fs.readFileSync(outputPath, 'utf-8');
    expect(yaml).toMatch(/^id: recorded-\d+/m);
    expect(yaml).toContain('selectors:');
    expect(yaml).toContain('steps:');
    expect(yaml).toContain('variables:');
  });

  it('throws when the input file does not exist', () => {
    const inputPath = path.join(tmpDir, 'missing.json');
    const outputPath = path.join(tmpDir, 'out.yaml');

    expect(() => run([inputPath, outputPath])).toThrow(/not found/);
  });
});
