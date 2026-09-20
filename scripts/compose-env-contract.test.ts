import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { describe, expect, it } from 'vitest';

function requiredComposeVariables(source: string): string[] {
  return Array.from(
    source.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*):\?[^}]*\}/g),
    (match) => match[1],
  );
}

function parseExampleValue(source: string): string {
  const value = source.trimStart();
  const quote = value[0];
  if (quote === '"' || quote === "'") {
    const closingQuote = value.indexOf(quote, 1);
    if (closingQuote >= 0) {
      const trailing = value.slice(closingQuote + 1).trim();
      if (trailing === '' || trailing.startsWith('#')) {
        return value.slice(1, closingQuote);
      }
    }
  }

  const inlineComment = source.match(/(^|\s)#/);
  const withoutComment =
    inlineComment?.index === undefined
      ? source
      : source.slice(0, inlineComment.index);
  return withoutComment.trim();
}

function parseExampleEnvironment(source: string): Map<string, string> {
  const values = new Map<string, string>();
  for (const match of source.matchAll(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/gm)) {
    values.set(match[1], parseExampleValue(match[2]));
  }
  return values;
}

describe('Compose example environment contract', () => {
  it.each([
    'REQUIRED=""',
    "REQUIRED=''",
    'REQUIRED="" # documented empty',
    'REQUIRED= # documented empty',
  ])(
    'treats a quoted empty value as empty: %s',
    (line) => {
      expect(parseExampleEnvironment(line).get('REQUIRED')).toBe('');
    },
  );

  it('supplies every required Compose variable with rollback-safe LLM defaults', () => {
    const root = process.cwd();
    const compose = readFileSync(join(root, 'docker-compose.yml'), 'utf8');
    const example = parseExampleEnvironment(
      readFileSync(join(root, '.env.example'), 'utf8'),
    );

    for (const variable of requiredComposeVariables(compose)) {
      expect(example.has(variable), `${variable} is missing from .env.example`).toBe(true);
      expect(example.get(variable), `${variable} is empty in .env.example`).not.toBe('');
    }
    expect(example.get('LLM_ENABLED')).toBe('false');
    expect(example.get('LLM_POLICY_MODE')).toBe('legacy');
  });

  it('passes both enforced route output-cap dialects through Compose', () => {
    const compose = readFileSync(join(process.cwd(), 'docker-compose.yml'), 'utf8');
    for (const variable of [
      'LLM_PRIMARY_OUTPUT_CAP_DIALECT',
      'LLM_FALLBACK_OUTPUT_CAP_DIALECT',
    ]) {
      expect(compose).toContain(`- ${variable}=\${${variable}:-}`);
    }
  });
});
