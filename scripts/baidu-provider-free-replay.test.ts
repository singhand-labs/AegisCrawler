import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import type { Rule } from '../src/scriptcat-engine/types';
import {
  assertProviderFreeBaiduRule,
  loadProviderFreeBaiduArtifact,
} from './live-workflow/baidu-provider-free-artifact';

const temporaryRoots: string[] = [];

afterEach(() => {
  for (const root of temporaryRoots.splice(0)) fs.rmSync(root, { recursive: true, force: true });
});

function replayRule(): Rule {
  return {
    id: 'preserved-baidu',
    version: '1.0.0',
    name: 'Preserved Baidu replay',
    domain: 'www.baidu.com',
    entry: 'https://www.baidu.com/',
    enabled: false,
    variables: { keyword: '' },
    steps: [
      { action: 'type', target: { selector: '#chat-textarea' }, value: '{{keyword}}' },
      { action: 'click', target: { selector: '#chat-submit-button' } },
      { action: 'waitForElementVisible', target: { selector: '#content_left' } },
      {
        action: 'extract',
        name: 'items',
        target: { selector: '#content_left > .result', visible: true },
        multiple: true,
        onEmpty: 'fail',
        fields: {
          title: { selector: '.t', type: 'text', visible: true },
          summary: { selector: '[role="text"]', type: 'text', visible: true },
        },
      },
      {
        action: 'loop',
        type: 'forEach',
        items: '{{extracted.items}}',
        as: 'item',
        steps: [{
          action: 'sendResult',
          payload: { title: '{{loopItem.title}}', summary: '{{loopItem.summary}}' },
        }],
      },
    ],
  } as Rule;
}

function writeArtifact(rule = replayRule()): { artifact: string } {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-baidu-provider-free-test-'));
  temporaryRoots.push(root);
  const artifact = path.join(root, 'dsl-workflow.json');
  const bytes = Buffer.from(JSON.stringify({
    status: 'failed',
    provisionalHash: 'a'.repeat(64),
    errorCode: 'REPLAY_FAILED',
    errorMessage: 'post-navigation domain mismatch: wappass.baidu.com',
    provisionalRule: rule,
  }));
  fs.writeFileSync(artifact, bytes);
  return { artifact };
}

describe('provider-free Baidu replay artifact', () => {
  it('preserves the reviewed legacy parent-readiness rule contract', () => {
    expect(() => assertProviderFreeBaiduRule(replayRule())).not.toThrow();
    const missingReadiness = replayRule();
    missingReadiness.steps = missingReadiness.steps.filter((step) =>
      (step as { action?: string }).action !== 'waitForElementVisible');
    expect(() => assertProviderFreeBaiduRule(missingReadiness))
      .toThrow(/reviewed #content_left parent readiness/);
  });

  it('rejects an unreviewed artifact even when its caller knows the exact hash', () => {
    const fixture = writeArtifact();
    expect(() => loadProviderFreeBaiduArtifact(fixture.artifact))
      .toThrow(/not the independently reviewed workflow/);
  });

  it('rejects changed bytes, unrelated failures, and symlinks', () => {
    const fixture = writeArtifact();
    expect(() => loadProviderFreeBaiduArtifact(fixture.artifact))
      .toThrow(/not the independently reviewed workflow/);

    const unrelated = JSON.parse(fs.readFileSync(fixture.artifact, 'utf8'));
    unrelated.errorCode = 'INVALID_DSL';
    fs.writeFileSync(fixture.artifact, JSON.stringify(unrelated));
    expect(() => loadProviderFreeBaiduArtifact(fixture.artifact))
      .toThrow(/not the independently reviewed workflow/);

    const link = path.join(path.dirname(fixture.artifact), 'workflow-link.json');
    fs.symlinkSync(fixture.artifact, link);
    expect(() => loadProviderFreeBaiduArtifact(link)).toThrow(/non-symlink/);
  });

  it('rejects actions outside the reviewed replay-only set', () => {
    const rule = replayRule();
    rule.steps.push({ action: 'evaluate', script: 'return document.cookie' } as any);
    expect(() => assertProviderFreeBaiduRule(rule)).toThrow(/arbitrary script|forbids action/);
  });
});
