import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Rule } from '../../src/scriptcat-engine/types';
import { BAIDU_ENTRY_URL, assertBaiduRule } from './baidu-search';

const MAX_ARTIFACT_BYTES = 1024 * 1024;
export const REVIEWED_BAIDU_REPLAY_ARTIFACT_SHA256 = 'd21d858c546f397907ba2f9257497c2b0adc3f031a0469271413b18b0275c984';
const REVIEWED_PROVISIONAL_HASH = 'a19e4b40eed19138be6a9fa68eaaf34ce45ed7b470bc4538f444d707dd5373c4';
const REVIEWED_RULE_ID = 'ext-1785119677145';
const ALLOWED_ACTIONS = new Set([
  'type',
  'click',
  'waitForElementVisible',
  'extract',
  'loop',
  'sendResult',
]);

interface PreservedBaiduWorkflow {
  status: string;
  provisionalHash: string;
  errorCode?: string;
  errorMessage?: string;
  provisionalRule: Rule;
}

export interface ProviderFreeBaiduArtifact {
  artifactPath: string;
  artifactSha256: string;
  workflow: PreservedBaiduWorkflow;
  rule: Rule;
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function actionObjects(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) return value.flatMap(actionObjects);
  if (!plainObject(value)) return [];
  return [
    ...(typeof value.action === 'string' ? [value] : []),
    ...Object.values(value).flatMap(actionObjects),
  ];
}

export function assertProviderFreeBaiduRule(rule: Rule): void {
  assertBaiduRule(rule, { requireExactRepeatedReadiness: false });
  assert.equal(rule.entry, BAIDU_ENTRY_URL, 'provider-free Baidu replay requires the exact Baidu entry URL');
  assert.equal(rule.domain, 'www.baidu.com', 'provider-free Baidu replay requires the exact Baidu domain');

  const actions = actionObjects([rule.steps, rule.hooks]);
  assert(actions.length > 0, 'provider-free Baidu replay rule contains no actions');
  for (const action of actions) {
    const name = String(action.action);
    assert(ALLOWED_ACTIONS.has(name), `provider-free Baidu replay forbids action ${name}`);
  }

  const extraction = actions.find((action) => action.action === 'extract');
  assert(extraction && extraction.multiple === true, 'provider-free Baidu replay requires one repeated extraction');
  assert(plainObject(extraction.fields), 'provider-free Baidu replay extraction fields are missing');
  assert.deepEqual(Object.keys(extraction.fields).sort(), ['summary', 'title'],
    'provider-free Baidu replay extraction must contain exactly summary and title');
  const topLevelSteps = Array.isArray(rule.steps) ? rule.steps.filter(plainObject) : [];
  const extractionIndex = topLevelSteps.findIndex((action) => action.action === 'extract');
  const parentReadinessIndex = topLevelSteps.findIndex((action) =>
    action.action === 'waitForElementVisible'
    && plainObject(action.target)
    && action.target.selector === '#content_left');
  assert(
    parentReadinessIndex >= 0 && extractionIndex > parentReadinessIndex,
    'provider-free Baidu replay requires the reviewed #content_left parent readiness before extraction',
  );

  const resultActions = actions.filter((action) => action.action === 'sendResult');
  assert.equal(resultActions.length, 1, 'provider-free Baidu replay requires exactly one sendResult action');
  assert(plainObject(resultActions[0].payload), 'provider-free Baidu replay sendResult payload is missing');
  assert.deepEqual(Object.keys(resultActions[0].payload).sort(), ['summary', 'title'],
    'provider-free Baidu replay sendResult must contain exactly summary and title');
}

export function loadProviderFreeBaiduArtifact(
  artifactPathValue: string,
): ProviderFreeBaiduArtifact {
  const artifactPath = path.resolve(artifactPathValue);
  const stat = fs.lstatSync(artifactPath);
  assert(stat.isFile() && !stat.isSymbolicLink(), 'provider-free replay artifact must be a regular non-symlink file');
  assert(stat.size > 0 && stat.size <= MAX_ARTIFACT_BYTES,
    `provider-free replay artifact must be between 1 and ${MAX_ARTIFACT_BYTES} bytes`);
  const bytes = fs.readFileSync(artifactPath);
  const artifactSha256 = createHash('sha256').update(bytes).digest('hex');
  assert.equal(artifactSha256, REVIEWED_BAIDU_REPLAY_ARTIFACT_SHA256,
    'provider-free replay artifact is not the independently reviewed workflow');

  const parsed = JSON.parse(bytes.toString('utf8')) as unknown;
  assert(plainObject(parsed), 'provider-free replay artifact must contain one workflow object');
  assert.equal(parsed.status, 'failed', 'provider-free replay requires a failed preserved workflow');
  assert.equal(parsed.errorCode, 'REPLAY_FAILED', 'provider-free replay requires a replay-only failure');
  assert.equal(parsed.errorMessage, 'post-navigation domain mismatch: wappass.baidu.com',
    'provider-free replay artifact failure must be the reviewed Baidu verification redirect');
  assert(typeof parsed.provisionalHash === 'string' && /^[0-9a-f]{64}$/.test(parsed.provisionalHash),
    'provider-free replay artifact has no canonical provisional hash');
  assert(plainObject(parsed.provisionalRule), 'provider-free replay artifact has no provisional rule');
  const workflow = parsed as unknown as PreservedBaiduWorkflow;
  assert.equal(workflow.provisionalHash, REVIEWED_PROVISIONAL_HASH,
    'provider-free replay provisional hash does not match the reviewed workflow');
  assert.equal(workflow.provisionalRule.id, REVIEWED_RULE_ID,
    'provider-free replay rule ID does not match the reviewed workflow');
  assertProviderFreeBaiduRule(workflow.provisionalRule);
  return { artifactPath, artifactSha256, workflow, rule: workflow.provisionalRule };
}
