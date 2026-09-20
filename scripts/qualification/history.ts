import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';
import {
  assertWindowsPrivatePath,
  secureWindowsPrivatePath,
} from './windows-private-storage';

export const QUALIFICATION_ARCHIVE_SCHEMA = 'aegiscrawler.qualification-history.v2' as const;
const QUALIFICATION_ENVELOPE_SCHEMA = 'aegiscrawler.qualification-history-envelope.v2' as const;
const ALGORITHM = 'aes-256-gcm' as const;
const MAX_ARCHIVE_BYTES = 64 * 1024 * 1024;
const MAX_ENVELOPE_BYTES = Math.ceil(MAX_ARCHIVE_BYTES * 4 / 3) + 64 * 1024;
const MAX_PROVIDER_CALLS = 2_048;
const MAX_PROVIDER_CONTENT_BYTES = 4 * 1024 * 1024;
const MAX_SIDECAR_BYTES = 1 * 1024 * 1024;
const MAX_SIDECAR_ENVELOPE_BYTES = Math.ceil(MAX_SIDECAR_BYTES * 4 / 3) + 16 * 1024;
const MAX_CLOCK_SKEW_MS = 30_000;
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const ARTIFACT_HASH_PATTERN = /^[0-9a-f]{64}$/;
const PROTOCOL_HASH_PATTERN = /^[A-Za-z0-9_-]{43}$/;
const SCENARIO_PATTERN = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/;
const PLAINTEXT_SECRET_PATTERNS = [
  /^(?:password|passwd|pwd|secret|token|session[-_ ]?(?:id|key|token)|access[-_ ]?(?:key|token)|refresh[-_ ]?token|api[-_ ]?(?:key|token)|client[-_ ]?secret|private[-_ ]?key|authorization|cookie|set-cookie)$/i,
  /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/i,
  /(?:authorization)\s*[:=]\s*(?:bearer|basic)\s+[^\s,;]+/i,
  /(?:cookie|set-cookie)\s*[:=]\s*[^\r\n]+/i,
  /(?:password|passwd|pwd|secret|token|session[_-]?(?:id|key|token)|access[_-]?(?:key|token)|refresh[_-]?token|api[_-]?(?:key|token)|client[_-]?secret|private[_-]?key)\s*[:=]\s*["']?[^\s"'<>]+/i,
  /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b/,
  /\bsk-[A-Za-z0-9_-]{16,}\b/,
  /\bAKIA[0-9A-Z]{16}\b/,
];
const REDACTION_MARKER_PATTERN = /^(?:\[\s*redacted\s*\]|<\s*redacted\s*>|\*+\s*redacted\s*\*+|redacted)$/i;
const REDACTION_MARKER_SEARCH_PATTERN = /(?:\[\s*redacted\s*\]|<\s*redacted\s*>|\*{2,}\s*redacted\s*\*{2,})/i;

export type QualificationJSON =
  | null
  | boolean
  | number
  | string
  | QualificationJSON[]
  | { [key: string]: QualificationJSON };

declare const qualificationArtifactHash: unique symbol;
declare const qualificationProtocolHash: unique symbol;

// Artifact hashes authenticate locally retained canonical JSON or content.
// Protocol hashes are the base64url selector-catalog identities copied by the
// provider. They intentionally cannot be assigned to each other.
export type QualificationArtifactHash = string & {
  readonly [qualificationArtifactHash]: 'qualification-artifact-hash';
};
export type QualificationProtocolHash = string & {
  readonly [qualificationProtocolHash]: 'qualification-protocol-hash';
};

export interface BoundQualificationArtifact {
  hash: QualificationArtifactHash;
  value: QualificationJSON;
}

export type QualificationArchiveKind = 'source-history' | 'fresh-canary';

export interface QualificationSourceArchiveReference {
  archiveId: string;
  payloadHash: QualificationArtifactHash;
  providerIRHash: QualificationArtifactHash;
  resolvedRuleHash: QualificationArtifactHash;
  validationHash: QualificationArtifactHash;
}

export type QualificationProviderCallKind =
  | 'provider_response'
  | 'provider_error'
  | 'cache_hit';

export interface QualificationProviderCall {
  callId: string;
  callIndex: number;
  callKind: QualificationProviderCallKind;
  providerAttempt: number;
  phase: string;
  chunkIndex?: number;
  chunkCount: number;
  provider: string;
  model: string;
  requestHash: QualificationArtifactHash;
  responseHash: QualificationArtifactHash;
  responseId: string | null;
  finishReason: string | null;
  httpStatus: number;
  errorCode: string | null;
  content: string;
  inputTokens: number;
  outputTokens: number;
  cacheHit: boolean;
  redacted: boolean;
  truncated: boolean;
  replayable: boolean;
}

export interface QualificationArchivePayload {
  schemaVersion: typeof QUALIFICATION_ARCHIVE_SCHEMA;
  archiveId: string;
  archiveKind: QualificationArchiveKind;
  sourceHistory: QualificationSourceArchiveReference | null;
  scenario: string;
  capturedAt: string;
  recordingHash: QualificationArtifactHash;
  requirement: BoundQualificationArtifact;
  baseline: BoundQualificationArtifact;
  provider: {
    name: string;
    model: string;
    promptVersion: string;
    selectorCatalogHash: QualificationProtocolHash;
    selectorCatalog: BoundQualificationArtifact;
  };
  attempt: {
    jobType: 'dsl';
    jobId: string;
    attemptNumber: number;
    reportId: string;
    reportHash: QualificationArtifactHash;
    report: BoundQualificationArtifact;
    outcome: 'succeeded' | 'failed';
    calls: QualificationProviderCall[];
    providerIRSourceCallId: string | null;
    providerIR: BoundQualificationArtifact;
    resolvedRule: BoundQualificationArtifact;
    validation: BoundQualificationArtifact;
  };
}

interface QualificationArchiveEnvelope {
  schemaVersion: typeof QUALIFICATION_ENVELOPE_SCHEMA;
  payloadSchemaVersion: typeof QUALIFICATION_ARCHIVE_SCHEMA;
  archiveId: string;
  scenario: string;
  createdAt: string;
  algorithm: typeof ALGORITHM;
  payloadHash: QualificationArtifactHash;
  iv: string;
  tag: string;
  ciphertext: string;
}

export interface QualificationHistoryPaths {
  root: string;
  scenarioDirectory: string;
  keyFile: string;
  artifact: string;
}

export interface LoadedQualificationArchive {
  payload: QualificationArchivePayload;
  payloadHash: QualificationArtifactHash;
  artifactPath: string;
}

interface QualificationSidecarEnvelope {
  schemaVersion: 'aegiscrawler.qualification-sidecar.v1';
  archiveId: string;
  scenario: string;
  name: string;
  purpose: string;
  createdAt: string;
  payloadHash: QualificationArtifactHash;
  algorithm: typeof ALGORITHM;
  iv: string;
  tag: string;
  ciphertext: string;
}

export interface QualificationArchiveOptions {
  /**
   * Tests may lower the bound to exercise fail-closed behavior cheaply.
   * Production callers can never raise the absolute 64 MiB limit.
   */
  maxArchiveBytes?: number;
}

function assertPlainRecord(value: unknown, label: string): asserts value is Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)
      || Object.getPrototypeOf(value) !== Object.prototype) {
    throw new Error(`${label} must be a JSON object`);
  }
}

function assertExactKeys(
  value: Record<string, unknown>,
  required: readonly string[],
  optional: readonly string[],
  label: string,
): void {
  const allowed = new Set([...required, ...optional]);
  for (const key of required) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) {
      throw new Error(`${label} is missing ${key}`);
    }
  }
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) throw new Error(`${label} contains unsupported field ${key}`);
  }
}

function assertBoundedString(value: unknown, label: string, maxBytes: number, allowEmpty = false): asserts value is string {
  if (typeof value !== 'string' || (!allowEmpty && value.length === 0)
      || Buffer.byteLength(value, 'utf8') > maxBytes) {
    throw new Error(`${label} must be ${allowEmpty ? 'a' : 'a non-empty'} bounded string`);
  }
}

function assertInteger(value: unknown, label: string, minimum = 0): asserts value is number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum) {
    throw new Error(`${label} must be an integer >= ${minimum}`);
  }
}

function assertBoolean(value: unknown, label: string): asserts value is boolean {
  if (typeof value !== 'boolean') throw new Error(`${label} must be boolean`);
}

function boundedNullableString(value: unknown, label: string, maxBytes: number): string | null {
  if (value === null) return null;
  assertBoundedString(value, label, maxBytes);
  return value;
}

function assertArtifactHash(value: unknown, label: string): asserts value is QualificationArtifactHash {
  if (typeof value !== 'string' || !ARTIFACT_HASH_PATTERN.test(value)) {
    throw new Error(`${label} must be a lowercase SHA-256 hash`);
  }
}

function assertProtocolHash(value: unknown, label: string): asserts value is QualificationProtocolHash {
  if (typeof value !== 'string' || !PROTOCOL_HASH_PATTERN.test(value)) {
    throw new Error(`${label} must be a canonical base64url SHA-256 hash`);
  }
  const decoded = Buffer.from(value, 'base64url');
  if (decoded.length !== 32 || decoded.toString('base64url') !== value) {
    throw new Error(`${label} must be a canonical base64url SHA-256 hash`);
  }
}

function assertUUID(value: unknown, label: string): asserts value is string {
  if (typeof value !== 'string' || !UUID_PATTERN.test(value)) {
    throw new Error(`${label} must be a canonical UUID`);
  }
}

function assertScenario(value: unknown): asserts value is string {
  if (typeof value !== 'string' || !SCENARIO_PATTERN.test(value)) {
    throw new Error('qualification scenario must be a lowercase slug');
  }
}

function assertTimestamp(value: unknown, label: string): asserts value is string {
  assertBoundedString(value, label, 64);
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString() !== value) {
    throw new Error(`${label} must be a canonical ISO-8601 timestamp`);
  }
}

function assertNotFuture(value: string, label: string): void {
  if (Date.parse(value) > Date.now() + MAX_CLOCK_SKEW_MS) {
    throw new Error(`${label} cannot be in the future`);
  }
}

function normalizeJSON(value: unknown, label: string, seen: Set<object>): QualificationJSON {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value;
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new Error(`${label} contains a non-finite number`);
    return value;
  }
  if (typeof value !== 'object') throw new Error(`${label} is not JSON-compatible`);
  if (seen.has(value)) throw new Error(`${label} contains a cycle`);
  seen.add(value);
  try {
    if (Array.isArray(value)) {
      return value.map((item, index) => normalizeJSON(item, `${label}[${index}]`, seen));
    }
    if (Object.getPrototypeOf(value) !== Object.prototype) {
      throw new Error(`${label} contains a non-plain object`);
    }
    const result: { [key: string]: QualificationJSON } = {};
    for (const key of Object.keys(value as Record<string, unknown>).sort()) {
      if (key.length === 0 || Buffer.byteLength(key, 'utf8') > 512) {
        throw new Error(`${label} contains an invalid object key`);
      }
      const normalized = normalizeJSON(
        (value as Record<string, unknown>)[key],
        `${label}.${key}`,
        seen,
      );
      Object.defineProperty(result, key, {
        value: normalized,
        enumerable: true,
        configurable: true,
        writable: true,
      });
    }
    return result;
  } finally {
    seen.delete(value);
  }
}

function credentialLikeKey(key: string): boolean {
  const normalized = key.toLowerCase().replace(/[^a-z0-9]/g, '');
  if ([
    'inputtokens', 'outputtokens', 'totaltokens', 'maxtokens',
    'prompttokens', 'completiontokens', 'cachedtokens', 'reasoningtokens',
    'tokenusage', 'tokencount',
  ].includes(normalized)) {
    return false;
  }
  return [
    'password', 'passwd', 'pwd', 'secret', 'token', 'apikey',
    'authorization', 'cookie', 'setcookie', 'sessionid', 'sessionkey',
    'sessiontoken', 'privatekey', 'clientsecret', 'accesskey',
    'accesstoken', 'refreshtoken', 'tokenvalue', 'passwordhash',
  ].some((marker) => normalized === marker
    || normalized.endsWith(marker));
}

function isRedactionMarker(value: QualificationJSON): boolean {
  return typeof value === 'string' && REDACTION_MARKER_PATTERN.test(value.trim());
}

function qualificationValueContainsRedaction(value: QualificationJSON): boolean {
  if (typeof value === 'string') {
    return isRedactionMarker(value) || REDACTION_MARKER_SEARCH_PATTERN.test(value);
  }
  if (Array.isArray(value)) return value.some(qualificationValueContainsRedaction);
  if (value !== null && typeof value === 'object') {
    return Object.values(value).some(qualificationValueContainsRedaction);
  }
  return false;
}

function qualificationValueContainsSecret(value: QualificationJSON): boolean {
  if (typeof value === 'string') {
    return !isRedactionMarker(value)
      && PLAINTEXT_SECRET_PATTERNS.some((pattern) => pattern.test(value));
  }
  if (Array.isArray(value)) return value.some(qualificationValueContainsSecret);
  if (value !== null && typeof value === 'object') {
    return Object.entries(value).some(([key, item]) =>
      (credentialLikeKey(key) && item !== null && !isRedactionMarker(item))
      || qualificationValueContainsSecret(item));
  }
  return false;
}

function qualificationTextContainsSecret(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      const parsed = normalizeJSON(JSON.parse(trimmed) as unknown, 'provider content', new Set());
      return qualificationValueContainsSecret(parsed);
    } catch {
      // Invalid provider JSON is still retained for diagnostics; use the
      // conservative plaintext patterns below rather than treating parse
      // failure itself as sensitive.
    }
  }
  return qualificationValueContainsSecret(value);
}

function qualificationTextContainsRedaction(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      return qualificationValueContainsRedaction(
        normalizeJSON(JSON.parse(trimmed) as unknown, 'provider content', new Set()),
      );
    } catch {
      // Fall through to the bounded plaintext marker scan.
    }
  }
  return REDACTION_MARKER_SEARCH_PATTERN.test(value);
}

export function canonicalQualificationJSON(value: unknown): string {
  return JSON.stringify(normalizeJSON(value, 'qualification artifact', new Set()));
}

export function qualificationSHA256(value: string | Buffer): QualificationArtifactHash {
  return crypto.createHash('sha256').update(value).digest('hex') as QualificationArtifactHash;
}

export function qualificationProtocolSHA256(value: string | Buffer): QualificationProtocolHash {
  return crypto.createHash('sha256').update(value).digest('base64url') as QualificationProtocolHash;
}

export function bindQualificationArtifact(value: unknown): BoundQualificationArtifact {
  const normalized = normalizeJSON(value, 'qualification artifact', new Set());
  return {
    hash: qualificationSHA256(JSON.stringify(normalized)),
    value: normalized,
  };
}

function validateBoundArtifact(value: unknown, label: string): BoundQualificationArtifact {
  assertPlainRecord(value, label);
  assertExactKeys(value, ['hash', 'value'], [], label);
  assertArtifactHash(value.hash, `${label}.hash`);
  const normalized = normalizeJSON(value.value, `${label}.value`, new Set());
  if (qualificationValueContainsSecret(normalized)) {
    throw new Error(`${label}.value contains credential-like or sensitive plaintext`);
  }
  const expected = qualificationSHA256(JSON.stringify(normalized));
  if (value.hash !== expected) throw new Error(`${label}.hash does not match its canonical value`);
  return { hash: value.hash, value: normalized };
}

function validateProviderCall(value: unknown, expectedIndex: number): QualificationProviderCall {
  const label = `qualification provider call ${expectedIndex}`;
  assertPlainRecord(value, label);
  assertExactKeys(value, [
    'callId', 'callIndex', 'callKind', 'providerAttempt', 'phase', 'chunkCount',
    'provider', 'model', 'requestHash', 'responseHash', 'content',
    'responseId', 'finishReason', 'httpStatus', 'errorCode',
    'inputTokens', 'outputTokens', 'cacheHit', 'redacted', 'truncated', 'replayable',
  ], ['chunkIndex'], label);
  assertBoundedString(value.callId, `${label}.callId`, 128);
  assertInteger(value.callIndex, `${label}.callIndex`, 1);
  if (value.callIndex !== expectedIndex) throw new Error('qualification provider calls must be contiguous and ordered');
  if (!['provider_response', 'provider_error', 'cache_hit'].includes(value.callKind as string)) {
    throw new Error(`${label}.callKind is unsupported`);
  }
  assertInteger(value.providerAttempt, `${label}.providerAttempt`);
  assertBoundedString(value.phase, `${label}.phase`, 64);
  if (!['analysis', 'synthesis', 'final'].includes(value.phase)) {
    throw new Error(`${label}.phase must be analysis, synthesis, or final`);
  }
  assertInteger(value.chunkCount, `${label}.chunkCount`);
  let chunkIndex: number | undefined;
  if (Object.prototype.hasOwnProperty.call(value, 'chunkIndex')) {
    assertInteger(value.chunkIndex, `${label}.chunkIndex`);
    chunkIndex = value.chunkIndex;
    if (value.chunkCount <= 0 || chunkIndex >= value.chunkCount) {
      throw new Error(`${label}.chunkIndex is outside chunkCount`);
    }
  }
  assertBoundedString(value.provider, `${label}.provider`, 128);
  assertBoundedString(value.model, `${label}.model`, 256);
  assertArtifactHash(value.requestHash, `${label}.requestHash`);
  assertArtifactHash(value.responseHash, `${label}.responseHash`);
  const responseId = boundedNullableString(value.responseId, `${label}.responseId`, 256);
  const finishReason = boundedNullableString(value.finishReason, `${label}.finishReason`, 256);
  assertInteger(value.httpStatus, `${label}.httpStatus`);
  if (value.httpStatus > 599) throw new Error(`${label}.httpStatus must be <= 599`);
  const errorCode = boundedNullableString(value.errorCode, `${label}.errorCode`, 256);
  assertBoundedString(value.content, `${label}.content`, MAX_PROVIDER_CONTENT_BYTES, true);
  if (qualificationTextContainsSecret(value.content)) {
    throw new Error(`${label}.content contains credential-like or sensitive plaintext`);
  }
  if (qualificationSHA256(value.content) !== value.responseHash) {
    throw new Error(`${label}.responseHash does not match content`);
  }
  assertInteger(value.inputTokens, `${label}.inputTokens`);
  assertInteger(value.outputTokens, `${label}.outputTokens`);
  assertBoolean(value.cacheHit, `${label}.cacheHit`);
  assertBoolean(value.redacted, `${label}.redacted`);
  assertBoolean(value.truncated, `${label}.truncated`);
  assertBoolean(value.replayable, `${label}.replayable`);
  const containsRedaction = qualificationTextContainsRedaction(value.content);
  if (containsRedaction && (!value.redacted || value.replayable)) {
    throw new Error(`${label} containing redaction markers must be marked redacted and non-replayable`);
  }
  const callKind = value.callKind as QualificationProviderCallKind;
  if (callKind === 'cache_hit') {
    if (!value.cacheHit || value.providerAttempt !== 0 || value.httpStatus !== 0
        || errorCode !== null || responseId === null || finishReason === null) {
      throw new Error(`${label} has invalid cache-hit provenance`);
    }
  } else if (value.cacheHit || value.providerAttempt <= 0) {
    throw new Error(`${label} has invalid physical provider provenance`);
  }
  if (callKind === 'provider_error') {
    if (errorCode === null || responseId !== null || finishReason !== null
        || (value.httpStatus !== 0 && value.httpStatus < 400)) {
      throw new Error(`${label} provider error has invalid terminal metadata`);
    }
  }
  if (callKind === 'provider_response') {
    if (errorCode !== null || value.httpStatus < 200 || value.httpStatus > 299
        || responseId === null || finishReason === null) {
      throw new Error(`${label} provider response requires 2xx status, response id, and finish reason`);
    }
  }
  if (value.replayable && (value.redacted || value.truncated)) {
    throw new Error(`${label} cannot be replayable after redaction or truncation`);
  }
  return {
    callId: value.callId,
    callIndex: value.callIndex,
    callKind,
    providerAttempt: value.providerAttempt,
    phase: value.phase,
    ...(chunkIndex === undefined ? {} : { chunkIndex }),
    chunkCount: value.chunkCount,
    provider: value.provider,
    model: value.model,
    requestHash: value.requestHash,
    responseHash: value.responseHash,
    responseId,
    finishReason,
    httpStatus: value.httpStatus,
    errorCode,
    content: value.content,
    inputTokens: value.inputTokens,
    outputTokens: value.outputTokens,
    cacheHit: value.cacheHit,
    redacted: value.redacted,
    truncated: value.truncated,
    replayable: value.replayable,
  };
}

function providerCallLineageKey(call: QualificationProviderCall): string {
  return `${call.phase}:${call.chunkIndex ?? 'none'}:${call.chunkCount}`;
}

function validateProviderCallLineage(
  calls: QualificationProviderCall[],
  outcome: 'succeeded' | 'failed',
): void {
  const seenLogicalCalls = new Set<string>();
  const logicalCalls: QualificationProviderCall[][] = [];
  for (const call of calls) {
    if (call.phase === 'analysis') {
      if (call.chunkIndex === undefined || call.chunkCount < 2) {
        throw new Error('qualification analysis calls require an indexed multi-chunk lineage');
      }
    } else if (call.phase === 'synthesis') {
      if (call.chunkIndex !== undefined || call.chunkCount < 2) {
        throw new Error('qualification synthesis call requires an unindexed multi-chunk lineage');
      }
    } else if (!((call.chunkCount === 0 && call.chunkIndex === undefined)
      || (call.chunkCount === 1 && call.chunkIndex === 0))) {
      throw new Error('qualification final call has invalid single-pass chunk lineage');
    }

    const key = providerCallLineageKey(call);
    const current = logicalCalls[logicalCalls.length - 1];
    if (!current || providerCallLineageKey(current[0]) !== key) {
      if (seenLogicalCalls.has(key)) {
        throw new Error('qualification logical provider calls must be unique and contiguous');
      }
      seenLogicalCalls.add(key);
      logicalCalls.push([call]);
    } else {
      current.push(call);
    }
  }

  for (const group of logicalCalls) {
    if (group[0].callKind === 'cache_hit') {
      if (group.length !== 1) {
        throw new Error('qualification cache-hit lineage must contain exactly one call');
      }
      continue;
    }
    group.forEach((call, index) => {
      if (call.callKind === 'cache_hit' || call.providerAttempt !== index + 1) {
        throw new Error('qualification physical provider attempts must be contiguous and ordered');
      }
      if (index < group.length - 1 && call.callKind !== 'provider_error') {
        throw new Error('qualification provider retries must follow a provider error');
      }
    });
  }

  const terminalGroups = logicalCalls.filter((group) =>
    group[0].phase === 'final' || group[0].phase === 'synthesis');
  if (terminalGroups.length > 1
      || (terminalGroups.length === 1
        && logicalCalls[logicalCalls.length - 1] !== terminalGroups[0])) {
    throw new Error('qualification attempt must contain at most one final ordered terminal phase');
  }
  const terminal = terminalGroups[0];
  if (terminal?.[0].phase === 'final' && logicalCalls.length !== 1) {
    throw new Error('qualification final phase cannot follow chunk-analysis calls');
  }
  if (terminal?.[0].phase === 'synthesis') {
    const chunkCount = terminal[0].chunkCount;
    const analyses = logicalCalls.slice(0, -1);
    if (analyses.length !== chunkCount
        || analyses.some((group, index) =>
          group[0].phase !== 'analysis'
          || group[0].chunkCount !== chunkCount
          || group[0].chunkIndex !== index)) {
      throw new Error('qualification chunk analyses must cover each index exactly once before synthesis');
    }
  }
  if (outcome === 'succeeded') {
    const last = calls[calls.length - 1];
    if (!terminal || !['provider_response', 'cache_hit'].includes(last.callKind)) {
      throw new Error('successful qualification attempt requires a completed terminal provider phase');
    }
  }
  const responseIds = calls
    .filter((call) => call.callKind === 'provider_response')
    .map((call) => call.responseId as string);
  if (new Set(responseIds).size !== responseIds.length) {
    throw new Error('qualification physical provider response ids must be unique');
  }
}

function validateSourceArchiveReference(
  value: unknown,
  label: string,
): QualificationSourceArchiveReference {
  assertPlainRecord(value, label);
  assertExactKeys(value, [
    'archiveId', 'payloadHash', 'providerIRHash', 'resolvedRuleHash', 'validationHash',
  ], [], label);
  assertUUID(value.archiveId, `${label}.archiveId`);
  assertArtifactHash(value.payloadHash, `${label}.payloadHash`);
  assertArtifactHash(value.providerIRHash, `${label}.providerIRHash`);
  assertArtifactHash(value.resolvedRuleHash, `${label}.resolvedRuleHash`);
  assertArtifactHash(value.validationHash, `${label}.validationHash`);
  return value as unknown as QualificationSourceArchiveReference;
}

export function validateQualificationArchivePayload(value: unknown): QualificationArchivePayload {
  assertPlainRecord(value, 'qualification archive payload');
  assertExactKeys(value, [
    'schemaVersion', 'archiveId', 'archiveKind', 'sourceHistory', 'scenario',
    'capturedAt', 'recordingHash',
    'requirement', 'baseline', 'provider', 'attempt',
  ], [], 'qualification archive payload');
  if (value.schemaVersion !== QUALIFICATION_ARCHIVE_SCHEMA) {
    throw new Error('unsupported qualification archive payload schema');
  }
  assertUUID(value.archiveId, 'qualification archive id');
  if (value.archiveKind !== 'source-history' && value.archiveKind !== 'fresh-canary') {
    throw new Error('qualification archive kind is unsupported');
  }
  let sourceHistory: QualificationSourceArchiveReference | null = null;
  if (value.archiveKind === 'source-history') {
    if (value.sourceHistory !== null) {
      throw new Error('source-history qualification archive cannot reference another archive');
    }
  } else {
    if (value.sourceHistory === null) {
      throw new Error('fresh-canary qualification archive requires an exact source-history reference');
    }
    sourceHistory = validateSourceArchiveReference(
      value.sourceHistory,
      'qualification source-history reference',
    );
    if (sourceHistory.archiveId === value.archiveId) {
      throw new Error('fresh-canary qualification archive must have a distinct identity');
    }
  }
  assertScenario(value.scenario);
  assertTimestamp(value.capturedAt, 'qualification archive capture time');
  assertArtifactHash(value.recordingHash, 'qualification recording hash');
  const requirement = validateBoundArtifact(value.requirement, 'qualification requirement');
  const baseline = validateBoundArtifact(value.baseline, 'qualification baseline');

  assertPlainRecord(value.provider, 'qualification provider');
  assertExactKeys(value.provider, [
    'name', 'model', 'promptVersion', 'selectorCatalogHash', 'selectorCatalog',
  ], [], 'qualification provider');
  assertBoundedString(value.provider.name, 'qualification provider name', 128);
  assertBoundedString(value.provider.model, 'qualification provider model', 256);
  assertBoundedString(value.provider.promptVersion, 'qualification prompt version', 128);
  assertProtocolHash(value.provider.selectorCatalogHash, 'qualification selector catalog hash');
  const selectorCatalog = validateBoundArtifact(
    value.provider.selectorCatalog,
    'qualification selector catalog',
  );
  if (selectorCatalog.value === null
      || typeof selectorCatalog.value !== 'object'
      || Array.isArray(selectorCatalog.value)
      || selectorCatalog.value.catalogHash !== value.provider.selectorCatalogHash) {
    throw new Error('qualification selector catalog value does not match its catalog hash');
  }

  assertPlainRecord(value.attempt, 'qualification attempt');
  assertExactKeys(value.attempt, [
    'jobType', 'jobId', 'attemptNumber', 'reportId', 'reportHash', 'report',
    'outcome', 'calls', 'providerIRSourceCallId', 'providerIR', 'resolvedRule',
    'validation',
  ], [], 'qualification attempt');
  if (value.attempt.jobType !== 'dsl') throw new Error('qualification attempt must be a DSL job');
  assertBoundedString(value.attempt.jobId, 'qualification job id', 128);
  assertInteger(value.attempt.attemptNumber, 'qualification attempt number', 1);
  assertBoundedString(value.attempt.reportId, 'qualification attempt report id', 128);
  assertArtifactHash(value.attempt.reportHash, 'qualification attempt report hash');
  const report = validateBoundArtifact(value.attempt.report, 'qualification attempt report');
  if (report.hash !== value.attempt.reportHash) {
    throw new Error('qualification attempt report hash does not bind the retained report');
  }
  if (value.attempt.outcome !== 'succeeded' && value.attempt.outcome !== 'failed') {
    throw new Error('qualification attempt outcome is unsupported');
  }
  if (!Array.isArray(value.attempt.calls)
      || value.attempt.calls.length === 0
      || value.attempt.calls.length > MAX_PROVIDER_CALLS) {
    throw new Error(`qualification attempt must contain 1-${MAX_PROVIDER_CALLS} provider calls`);
  }
  const calls = value.attempt.calls.map((call, index) => validateProviderCall(call, index + 1));
  if (new Set(calls.map((call) => call.callId)).size !== calls.length) {
    throw new Error('qualification provider call ids must be unique');
  }
  validateProviderCallLineage(calls, value.attempt.outcome);
  const providerIRSourceCallId = boundedNullableString(
    value.attempt.providerIRSourceCallId,
    'qualification provider IR source call id',
    128,
  );
  const providerIR = validateBoundArtifact(value.attempt.providerIR, 'qualification provider IR');
  if (providerIR.value !== null
      && (typeof providerIR.value !== 'object'
        || Array.isArray(providerIR.value)
        || providerIR.value.selectorCatalogHash !== value.provider.selectorCatalogHash)) {
    throw new Error('qualification provider IR does not bind the exact selector catalog');
  }
  const resolvedRule = validateBoundArtifact(value.attempt.resolvedRule, 'qualification resolved rule');
  const validation = validateBoundArtifact(value.attempt.validation, 'qualification validation report');
  let providerIRSourceResponseHash: QualificationArtifactHash | null = null;
  if (providerIR.value === null) {
    if (providerIRSourceCallId !== null) {
      throw new Error('null qualification provider IR cannot reference a source call');
    }
  } else {
    const sourceCall = calls.find((call) => call.callId === providerIRSourceCallId);
    const terminalCall = calls[calls.length - 1];
    if (!sourceCall || sourceCall !== terminalCall
        || !['final', 'synthesis'].includes(sourceCall.phase)
        || !['provider_response', 'cache_hit'].includes(sourceCall.callKind)
        || !sourceCall.replayable) {
      throw new Error('qualification provider IR must bind the exact replayable terminal response');
    }
    providerIRSourceResponseHash = sourceCall.responseHash;
  }
  assertPlainRecord(report.value, 'qualification attempt report value');
  const reportBindings = report.value.bindings;
  assertPlainRecord(reportBindings, 'qualification attempt report bindings');
  assertExactKeys(reportBindings, [
    'callIds', 'providerIRSourceCallId', 'providerIRSourceResponseHash',
    'providerIRHash', 'resolvedRuleHash', 'validationHash',
  ], [], 'qualification attempt report bindings');
  if (!Array.isArray(reportBindings.callIds)
      || reportBindings.callIds.length !== calls.length
      || reportBindings.callIds.some((callId, index) =>
        callId !== calls[index].callId)
      || reportBindings.providerIRSourceCallId !== providerIRSourceCallId
      || reportBindings.providerIRSourceResponseHash !== providerIRSourceResponseHash
      || reportBindings.providerIRHash !== providerIR.hash
      || reportBindings.resolvedRuleHash !== resolvedRule.hash
      || reportBindings.validationHash !== validation.hash) {
    throw new Error('qualification attempt report does not bind exact calls and provisional artifacts');
  }

  return {
    schemaVersion: QUALIFICATION_ARCHIVE_SCHEMA,
    archiveId: value.archiveId,
    archiveKind: value.archiveKind,
    sourceHistory,
    scenario: value.scenario,
    capturedAt: value.capturedAt,
    recordingHash: value.recordingHash,
    requirement,
    baseline,
    provider: {
      name: value.provider.name,
      model: value.provider.model,
      promptVersion: value.provider.promptVersion,
      selectorCatalogHash: value.provider.selectorCatalogHash,
      selectorCatalog,
    },
    attempt: {
      jobType: 'dsl',
      jobId: value.attempt.jobId,
      attemptNumber: value.attempt.attemptNumber,
      reportId: value.attempt.reportId,
      reportHash: value.attempt.reportHash,
      report,
      outcome: value.attempt.outcome,
      calls,
      providerIRSourceCallId,
      providerIR,
      resolvedRule,
      validation,
    },
  };
}

function effectiveArchiveLimit(options?: QualificationArchiveOptions): number {
  const requested = options?.maxArchiveBytes ?? MAX_ARCHIVE_BYTES;
  if (!Number.isSafeInteger(requested) || requested <= 0 || requested > MAX_ARCHIVE_BYTES) {
    throw new Error(`qualification archive limit must be between 1 and ${MAX_ARCHIVE_BYTES} bytes`);
  }
  return requested;
}

export function qualificationHistoryPaths(
  projectRoot: string,
  scenario: string,
  archiveId: string,
): QualificationHistoryPaths {
  assertScenario(scenario);
  assertUUID(archiveId, 'qualification archive id');
  const resolvedProjectRoot = path.resolve(projectRoot);
  const root = path.join(resolvedProjectRoot, '.env', 'qualification-history');
  const scenarioDirectory = path.join(root, scenario);
  const artifact = path.join(scenarioDirectory, `${archiveId}.enc.json`);
  if (path.relative(root, artifact).startsWith('..') || path.isAbsolute(path.relative(root, artifact))) {
    throw new Error('qualification archive path escapes its dedicated root');
  }
  return {
    root,
    scenarioDirectory,
    keyFile: path.join(root, 'qualification-history.key'),
    artifact,
  };
}

function assertNonSymlinkDirectory(directory: string, label: string, privateMode: boolean): void {
  const stat = fs.lstatSync(directory);
  if (!stat.isDirectory() || stat.isSymbolicLink()) {
    throw new Error(`${label} must be a non-symlink directory`);
  }
  if (privateMode && process.platform !== 'win32' && (stat.mode & 0o777) !== 0o700) {
    throw new Error(`${label} must have mode 0700`);
  }
  if (privateMode && process.getuid && stat.uid !== process.getuid()) {
    throw new Error(`${label} must be owned by the current user`);
  }
  if (privateMode) assertWindowsPrivatePath(directory, 'directory');
}

function fsyncDirectory(
  directory: string,
  label: string,
  privateMode: boolean,
): void {
  if (process.platform === 'win32') {
    // Windows does not expose O_DIRECTORY/O_NOFOLLOW through Node and opening a
    // directory for fsync fails with EPERM. Reject reparse points with lstat,
    // then verify that resolving the path did not change the directory identity.
    const before = fs.lstatSync(directory);
    if (!before.isDirectory() || before.isSymbolicLink()) {
      throw new Error(`${label} must be a non-symlink directory`);
    }
    const resolved = fs.statSync(fs.realpathSync.native(directory));
    const after = fs.lstatSync(directory);
    if (!after.isDirectory() || after.isSymbolicLink()
        || resolved.dev !== before.dev || resolved.ino !== before.ino
        || after.dev !== before.dev || after.ino !== before.ino) {
      throw new Error(`${label} changed while it was being synchronized`);
    }
    return;
  }
  if (typeof fs.constants.O_DIRECTORY !== 'number'
      || typeof fs.constants.O_NOFOLLOW !== 'number') {
    throw new Error('qualification storage requires no-follow directory descriptor support');
  }
  let descriptor: number;
  try {
    descriptor = fs.openSync(
      directory,
      fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW,
    );
  } catch (error) {
    if (['ELOOP', 'ENOTDIR'].includes((error as NodeJS.ErrnoException).code ?? '')) {
      throw new Error(`${label} must be a non-symlink directory`);
    }
    throw error;
  }
  try {
    const descriptorStat = fs.fstatSync(descriptor);
    if (!descriptorStat.isDirectory()) {
      throw new Error(`${label} must be a non-symlink directory`);
    }
    if (privateMode && (descriptorStat.mode & 0o777) !== 0o700) {
      throw new Error(`${label} must have mode 0700`);
    }
    if (privateMode && process.getuid && descriptorStat.uid !== process.getuid()) {
      throw new Error(`${label} must be owned by the current user`);
    }
    // Node does not expose openat(2). Re-resolve the pathname and compare its
    // current inode with the already opened no-follow descriptor so a swapped
    // ancestor cannot silently redirect the synchronization target.
    const resolvedStat = fs.statSync(fs.realpathSync.native(directory));
    if (resolvedStat.dev !== descriptorStat.dev || resolvedStat.ino !== descriptorStat.ino) {
      throw new Error(`${label} changed while it was being synchronized`);
    }
    fs.fsyncSync(descriptor);
  } finally {
    fs.closeSync(descriptor);
  }
}

function createDirectoryIfMissing(
  directory: string,
  mode: number,
  parentLabel: string,
  parentPrivateMode: boolean,
): void {
  let created = false;
  try {
    fs.mkdirSync(directory, { mode });
    created = true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error;
  }
  if (created) {
    secureWindowsPrivatePath(directory, 'directory');
    fsyncDirectory(path.dirname(directory), parentLabel, parentPrivateMode);
  }
}

function prepareHistoryDirectories(projectRoot: string, paths: QualificationHistoryPaths): void {
  const resolvedProjectRoot = path.resolve(projectRoot);
  assertNonSymlinkDirectory(resolvedProjectRoot, 'qualification project root', false);
  const environmentDirectory = path.join(resolvedProjectRoot, '.env');
  createDirectoryIfMissing(
    environmentDirectory,
    0o700,
    'qualification project root',
    false,
  );
  assertNonSymlinkDirectory(environmentDirectory, 'qualification .env directory', false);
  for (const [directory, label, parentLabel, parentPrivateMode] of [
    [
      paths.root,
      'qualification history directory',
      'qualification .env directory',
      false,
    ],
    [
      paths.scenarioDirectory,
      'qualification scenario directory',
      'qualification history directory',
      true,
    ],
  ] as const) {
    createDirectoryIfMissing(directory, 0o700, parentLabel, parentPrivateMode);
    assertNonSymlinkDirectory(directory, label, true);
  }
}

function assertExistingHistoryDirectories(projectRoot: string, paths: QualificationHistoryPaths): void {
  const resolvedProjectRoot = path.resolve(projectRoot);
  assertNonSymlinkDirectory(resolvedProjectRoot, 'qualification project root', false);
  assertNonSymlinkDirectory(path.join(resolvedProjectRoot, '.env'), 'qualification .env directory', false);
  assertNonSymlinkDirectory(paths.root, 'qualification history directory', true);
  assertNonSymlinkDirectory(paths.scenarioDirectory, 'qualification scenario directory', true);
}

function readPrivateRegularFile(file: string, label: string, maxBytes: number): Buffer {
  if (process.platform !== 'win32' && typeof fs.constants.O_NOFOLLOW !== 'number') {
    throw new Error('qualification storage requires no-follow file descriptor support');
  }
  const pathStat = fs.lstatSync(file);
  if (!pathStat.isFile() || pathStat.isSymbolicLink()) {
    throw new Error(`${label} must be a regular non-symlink file`);
  }
  let descriptor: number;
  try {
    descriptor = fs.openSync(
      file,
      fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0),
    );
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ELOOP') {
      throw new Error(`${label} must be a regular non-symlink file`);
    }
    throw error;
  }
  try {
    const before = fs.fstatSync(descriptor);
    if (!before.isFile()) throw new Error(`${label} must be a regular non-symlink file`);
    if (before.dev !== pathStat.dev || before.ino !== pathStat.ino) {
      throw new Error(`${label} changed while it was being opened`);
    }
    if (process.platform !== 'win32' && (before.mode & 0o777) !== 0o600) {
      throw new Error(`${label} must have mode 0600`);
    }
    assertWindowsPrivatePath(file, 'file');
    if (process.getuid && before.uid !== process.getuid()) {
      throw new Error(`${label} must be owned by the current user`);
    }
    if (before.size <= 0 || before.size > maxBytes) {
      throw new Error(`${label} has an invalid size`);
    }
    const content = fs.readFileSync(descriptor);
    const after = fs.fstatSync(descriptor);
    if (after.dev !== before.dev || after.ino !== before.ino
        || after.size !== before.size || content.length !== before.size) {
      throw new Error(`${label} changed while it was being read`);
    }
    return content;
  } finally {
    fs.closeSync(descriptor);
  }
}

function exclusivePrivateWrite(file: string, content: string): void {
  const temporary = path.join(
    path.dirname(file),
    `.${path.basename(file)}.tmp-${process.pid}-${crypto.randomUUID()}`,
  );
  let descriptor: number | undefined;
  try {
    descriptor = fs.openSync(temporary, 'wx', 0o600);
    fs.writeFileSync(descriptor, content, { encoding: 'utf8' });
    fs.fchmodSync(descriptor, 0o600);
    fs.fsyncSync(descriptor);
    fs.closeSync(descriptor);
    descriptor = undefined;
    secureWindowsPrivatePath(temporary, 'file');
    // link(2) supplies the no-replace atomicity that rename(2) lacks.
    fs.linkSync(temporary, file);
  } finally {
    if (descriptor !== undefined) fs.closeSync(descriptor);
    if (fs.existsSync(temporary)) fs.unlinkSync(temporary);
    fsyncDirectory(
      path.dirname(file),
      'qualification artifact directory',
      true,
    );
  }
}

function readOrCreateHistoryKey(paths: QualificationHistoryPaths): Buffer {
  try {
    exclusivePrivateWrite(paths.keyFile, crypto.randomBytes(32).toString('hex'));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error;
  }
  return readExistingHistoryKey(paths);
}

function readExistingHistoryKey(paths: QualificationHistoryPaths): Buffer {
  const encoded = readPrivateRegularFile(
    paths.keyFile,
    'qualification history key',
    128,
  ).toString('utf8');
  if (!/^[0-9a-f]{64}$/.test(encoded)) {
    throw new Error('qualification history key must be canonical 32-byte lowercase hex');
  }
  return Buffer.from(encoded, 'hex');
}

function archiveAAD(
  scenario: string,
  archiveId: string,
  createdAt: string,
  payloadHash: QualificationArtifactHash,
): Buffer {
  return Buffer.from(canonicalQualificationJSON({
    algorithm: ALGORITHM,
    archiveId,
    createdAt,
    payloadHash,
    payloadSchemaVersion: QUALIFICATION_ARCHIVE_SCHEMA,
    scenario,
    schemaVersion: QUALIFICATION_ENVELOPE_SCHEMA,
  }), 'utf8');
}

function decodeCanonicalBase64(
  value: unknown,
  label: string,
  maximumBytes: number,
  exactBytes?: number,
): Buffer {
  if (typeof value !== 'string' || value.length > Math.ceil(maximumBytes * 4 / 3) + 4) {
    throw new Error(`${label} must be bounded canonical base64`);
  }
  const decoded = Buffer.from(value, 'base64');
  if (decoded.toString('base64') !== value || decoded.length > maximumBytes
      || (exactBytes !== undefined && decoded.length !== exactBytes)) {
    throw new Error(`${label} must be bounded canonical base64`);
  }
  return decoded;
}

function validateEnvelope(
  value: unknown,
  scenario: string,
  archiveId: string,
  maxArchiveBytes: number,
): QualificationArchiveEnvelope {
  assertPlainRecord(value, 'qualification archive envelope');
  assertExactKeys(value, [
    'schemaVersion', 'payloadSchemaVersion', 'archiveId', 'scenario', 'createdAt',
    'algorithm', 'payloadHash', 'iv', 'tag', 'ciphertext',
  ], [], 'qualification archive envelope');
  if (value.schemaVersion !== QUALIFICATION_ENVELOPE_SCHEMA
      || value.payloadSchemaVersion !== QUALIFICATION_ARCHIVE_SCHEMA
      || value.algorithm !== ALGORITHM) {
    throw new Error('unsupported qualification archive envelope');
  }
  if (value.archiveId !== archiveId || value.scenario !== scenario) {
    throw new Error('qualification archive envelope identity does not match its path');
  }
  assertTimestamp(value.createdAt, 'qualification archive creation time');
  assertArtifactHash(value.payloadHash, 'qualification archive payload hash');
  decodeCanonicalBase64(value.iv, 'qualification archive IV', 12, 12);
  decodeCanonicalBase64(value.tag, 'qualification archive authentication tag', 16, 16);
  const ciphertext = decodeCanonicalBase64(
    value.ciphertext,
    'qualification archive ciphertext',
    maxArchiveBytes,
  );
  if (ciphertext.length === 0) throw new Error('qualification archive ciphertext must not be empty');
  return value as unknown as QualificationArchiveEnvelope;
}

export function saveQualificationArchive(
  projectRoot: string,
  source: QualificationArchivePayload,
  options?: QualificationArchiveOptions,
): LoadedQualificationArchive {
  const payload = validateQualificationArchivePayload(source);
  assertNotFuture(payload.capturedAt, 'qualification archive capture time');
  const maxArchiveBytes = effectiveArchiveLimit(options);
  const paths = qualificationHistoryPaths(projectRoot, payload.scenario, payload.archiveId);
  prepareHistoryDirectories(projectRoot, paths);
  if (payload.archiveKind === 'fresh-canary') {
    assertQualificationSourceHistory(projectRoot, payload);
  }
  if (fs.existsSync(paths.artifact)) {
    throw new Error('qualification archive already exists; immutable history cannot be overwritten');
  }
  const key = readOrCreateHistoryKey(paths);
  const plaintext = Buffer.from(canonicalQualificationJSON(payload), 'utf8');
  if (plaintext.length === 0 || plaintext.length > maxArchiveBytes) {
    throw new Error('qualification archive plaintext exceeds its bounded size');
  }
  const payloadHash = qualificationSHA256(plaintext);
  const createdAt = new Date().toISOString();
  const iv = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv(ALGORITHM, key, iv);
  cipher.setAAD(archiveAAD(payload.scenario, payload.archiveId, createdAt, payloadHash));
  const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  if (ciphertext.length === 0 || ciphertext.length > maxArchiveBytes) {
    throw new Error('qualification archive ciphertext exceeds its bounded size');
  }
  const envelope: QualificationArchiveEnvelope = {
    schemaVersion: QUALIFICATION_ENVELOPE_SCHEMA,
    payloadSchemaVersion: QUALIFICATION_ARCHIVE_SCHEMA,
    archiveId: payload.archiveId,
    scenario: payload.scenario,
    createdAt,
    algorithm: ALGORITHM,
    payloadHash,
    iv: iv.toString('base64'),
    tag: cipher.getAuthTag().toString('base64'),
    ciphertext: ciphertext.toString('base64'),
  };
  const serialized = JSON.stringify(envelope);
  if (Buffer.byteLength(serialized, 'utf8') > MAX_ENVELOPE_BYTES) {
    throw new Error('qualification archive envelope exceeds its bounded size');
  }
  try {
    exclusivePrivateWrite(paths.artifact, serialized);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'EEXIST') {
      throw new Error('qualification archive already exists; immutable history cannot be overwritten');
    }
    throw error;
  }
  return { payload, payloadHash, artifactPath: paths.artifact };
}

export function loadQualificationArchive(
  projectRoot: string,
  scenario: string,
  archiveId: string,
  options?: QualificationArchiveOptions,
): LoadedQualificationArchive {
  const maxArchiveBytes = effectiveArchiveLimit(options);
  const paths = qualificationHistoryPaths(projectRoot, scenario, archiveId);
  assertExistingHistoryDirectories(projectRoot, paths);
  const key = readExistingHistoryKey(paths);
  const serializedEnvelope = readPrivateRegularFile(
    paths.artifact,
    'qualification archive',
    MAX_ENVELOPE_BYTES,
  ).toString('utf8');
  let decoded: unknown;
  try {
    decoded = JSON.parse(serializedEnvelope) as unknown;
  } catch (error) {
    throw new Error(`qualification archive is not valid JSON: ${(error as Error).message}`);
  }
  const envelope = validateEnvelope(decoded, scenario, archiveId, maxArchiveBytes);
  const iv = decodeCanonicalBase64(envelope.iv, 'qualification archive IV', 12, 12);
  const tag = decodeCanonicalBase64(envelope.tag, 'qualification archive authentication tag', 16, 16);
  const ciphertext = decodeCanonicalBase64(
    envelope.ciphertext,
    'qualification archive ciphertext',
    maxArchiveBytes,
  );
  const decipher = crypto.createDecipheriv(ALGORITHM, key, iv);
  decipher.setAAD(archiveAAD(
    scenario,
    archiveId,
    envelope.createdAt,
    envelope.payloadHash,
  ));
  decipher.setAuthTag(tag);
  let plaintext: Buffer;
  try {
    plaintext = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
  } catch {
    throw new Error('qualification archive authentication failed');
  }
  if (plaintext.length === 0 || plaintext.length > maxArchiveBytes) {
    throw new Error('qualification archive plaintext exceeds its bounded size');
  }
  if (qualificationSHA256(plaintext) !== envelope.payloadHash) {
    throw new Error('qualification archive payload hash mismatch');
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(plaintext.toString('utf8')) as unknown;
  } catch {
    throw new Error('qualification archive plaintext is not valid JSON');
  }
  const payload = validateQualificationArchivePayload(parsed);
  if (payload.archiveId !== archiveId || payload.scenario !== scenario) {
    throw new Error('qualification archive payload identity does not match its authenticated path');
  }
  assertNotFuture(payload.capturedAt, 'qualification archive capture time');
  assertNotFuture(envelope.createdAt, 'qualification archive creation time');
  if (Date.parse(envelope.createdAt) < Date.parse(payload.capturedAt)) {
    throw new Error('qualification archive creation time predates its capture');
  }
  if (payload.archiveKind === 'fresh-canary') {
    assertQualificationSourceHistory(projectRoot, payload);
  }
  return { payload, payloadHash: envelope.payloadHash, artifactPath: paths.artifact };
}

function assertQualificationSourceHistory(
  projectRoot: string,
  payload: QualificationArchivePayload,
): void {
  const reference = payload.sourceHistory;
  if (payload.archiveKind !== 'fresh-canary' || reference === null) {
    throw new Error('fresh-canary qualification archive lacks source-history lineage');
  }
  const source = loadQualificationArchive(
    projectRoot,
    payload.scenario,
    reference.archiveId,
  );
  if (source.payload.archiveKind !== 'source-history'
      || source.payloadHash !== reference.payloadHash
      || source.payload.attempt.providerIR.hash !== reference.providerIRHash
      || source.payload.attempt.resolvedRule.hash !== reference.resolvedRuleHash
      || source.payload.attempt.validation.hash !== reference.validationHash) {
    throw new Error('fresh-canary qualification archive does not bind the exact source history');
  }
  if (Date.parse(payload.capturedAt) <= Date.parse(source.payload.capturedAt)) {
    throw new Error('fresh-canary qualification archive must be captured after its source history');
  }
}

export interface ExactQualificationCallExpectation {
  callIndex: number;
  provider: string;
  model: string;
  phase: string;
  requestHash: QualificationArtifactHash;
}

/**
 * Resolve one archived response for loopback only after exact request and
 * provider identity matching. There is deliberately no fuzzy prompt/model
 * fallback and no "closest" call selection.
 */
export function exactQualificationLoopbackCall(
  archive: QualificationArchivePayload,
  expected: ExactQualificationCallExpectation,
): QualificationProviderCall {
  const payload = validateQualificationArchivePayload(archive);
  assertInteger(expected.callIndex, 'expected qualification call index', 1);
  assertBoundedString(expected.provider, 'expected qualification provider', 128);
  assertBoundedString(expected.model, 'expected qualification model', 256);
  assertBoundedString(expected.phase, 'expected qualification phase', 64);
  assertArtifactHash(expected.requestHash, 'expected qualification request hash');
  const call = payload.attempt.calls[expected.callIndex - 1];
  if (!call
      || call.callIndex !== expected.callIndex
      || call.provider !== expected.provider
      || call.model !== expected.model
      || call.phase !== expected.phase
      || call.requestHash !== expected.requestHash) {
    throw new Error('qualification loopback requires an exact ordered request/provider/model match');
  }
  if (call.callKind !== 'provider_response'
      || call.cacheHit
      || !call.replayable
      || call.redacted
      || call.truncated) {
    throw new Error('qualification loopback call is not replayable');
  }
  return call;
}

export function newQualificationArchiveId(): string {
  return crypto.randomUUID();
}

function assertSidecarName(value: string): void {
  if (!/^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/.test(value)) {
    throw new Error('qualification sidecar name must be a lowercase slug');
  }
}

function sidecarPath(
  paths: QualificationHistoryPaths,
  archiveId: string,
  name: string,
): string {
  assertSidecarName(name);
  const file = path.join(paths.scenarioDirectory, `${archiveId}.${name}.json`);
  const relative = path.relative(paths.scenarioDirectory, file);
  if (relative.startsWith('..') || path.isAbsolute(relative)) {
    throw new Error('qualification sidecar path escapes its scenario directory');
  }
  return file;
}

function sidecarAAD(
  scenario: string,
  archiveId: string,
  name: string,
  purpose: string,
  createdAt: string,
  payloadHash: QualificationArtifactHash,
): Buffer {
  return Buffer.from(canonicalQualificationJSON({
    algorithm: ALGORITHM,
    archiveId,
    createdAt,
    name,
    payloadHash,
    purpose,
    scenario,
    schemaVersion: 'aegiscrawler.qualification-sidecar.v1',
  }), 'utf8');
}

/**
 * Store an encrypted qualification sidecar beside its encrypted archive.
 * The sidecar is immutable, private, and independently authenticated.
 */
export function saveQualificationSidecar(
  projectRoot: string,
  scenario: string,
  archiveId: string,
  name: string,
  purpose: string,
  source: unknown,
): string {
  assertBoundedString(purpose, 'qualification sidecar purpose', 128);
  const paths = qualificationHistoryPaths(projectRoot, scenario, archiveId);
  assertExistingHistoryDirectories(projectRoot, paths);
  // A sidecar is meaningless unless its referenced encrypted archive exists
  // and authenticates successfully.
  loadQualificationArchive(projectRoot, scenario, archiveId);
  const key = readExistingHistoryKey(paths);
  const payload = normalizeJSON(source, 'qualification sidecar payload', new Set());
  const payloadJSON = JSON.stringify(payload);
  const plaintext = Buffer.from(payloadJSON, 'utf8');
  if (plaintext.length === 0 || plaintext.length > MAX_SIDECAR_BYTES) {
    throw new Error('qualification sidecar exceeds its bounded size');
  }
  const payloadHash = qualificationSHA256(payloadJSON);
  const createdAt = new Date().toISOString();
  const iv = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv(ALGORITHM, key, iv);
  cipher.setAAD(sidecarAAD(
    scenario,
    archiveId,
    name,
    purpose,
    createdAt,
    payloadHash,
  ));
  const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const envelope: QualificationSidecarEnvelope = {
    schemaVersion: 'aegiscrawler.qualification-sidecar.v1',
    archiveId,
    scenario,
    name,
    purpose,
    createdAt,
    payloadHash,
    algorithm: ALGORITHM,
    iv: iv.toString('base64'),
    tag: cipher.getAuthTag().toString('base64'),
    ciphertext: ciphertext.toString('base64'),
  };
  const serialized = JSON.stringify(envelope);
  if (Buffer.byteLength(serialized, 'utf8') > MAX_SIDECAR_ENVELOPE_BYTES) {
    throw new Error('qualification sidecar exceeds its bounded size');
  }
  const file = sidecarPath(paths, archiveId, name);
  try {
    exclusivePrivateWrite(file, serialized);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'EEXIST') {
      throw new Error('qualification sidecar already exists; evidence cannot be overwritten');
    }
    throw error;
  }
  return file;
}

export function loadQualificationSidecar(
  projectRoot: string,
  scenario: string,
  archiveId: string,
  name: string,
  expectedPurpose: string,
): QualificationJSON {
  assertBoundedString(expectedPurpose, 'qualification sidecar purpose', 128);
  const paths = qualificationHistoryPaths(projectRoot, scenario, archiveId);
  assertExistingHistoryDirectories(projectRoot, paths);
  loadQualificationArchive(projectRoot, scenario, archiveId);
  const key = readExistingHistoryKey(paths);
  const file = sidecarPath(paths, archiveId, name);
  const serializedEnvelope = readPrivateRegularFile(
    file,
    'qualification sidecar',
    MAX_SIDECAR_ENVELOPE_BYTES,
  ).toString('utf8');
  let decoded: unknown;
  try {
    decoded = JSON.parse(serializedEnvelope) as unknown;
  } catch {
    throw new Error('qualification sidecar is not valid JSON');
  }
  assertPlainRecord(decoded, 'qualification sidecar');
  assertExactKeys(decoded, [
    'schemaVersion', 'archiveId', 'scenario', 'name', 'purpose', 'createdAt',
    'payloadHash', 'algorithm', 'iv', 'tag', 'ciphertext',
  ], [], 'qualification sidecar');
  if (decoded.schemaVersion !== 'aegiscrawler.qualification-sidecar.v1'
      || decoded.archiveId !== archiveId
      || decoded.scenario !== scenario
      || decoded.name !== name
      || decoded.purpose !== expectedPurpose
      || decoded.algorithm !== ALGORITHM) {
    throw new Error('qualification sidecar identity does not match its authenticated path');
  }
  assertTimestamp(decoded.createdAt, 'qualification sidecar creation time');
  assertNotFuture(decoded.createdAt, 'qualification sidecar creation time');
  assertArtifactHash(decoded.payloadHash, 'qualification sidecar payload hash');
  const iv = decodeCanonicalBase64(decoded.iv, 'qualification sidecar IV', 12, 12);
  const tag = decodeCanonicalBase64(decoded.tag, 'qualification sidecar authentication tag', 16, 16);
  const ciphertext = decodeCanonicalBase64(
    decoded.ciphertext,
    'qualification sidecar ciphertext',
    MAX_SIDECAR_BYTES,
  );
  if (ciphertext.length === 0) throw new Error('qualification sidecar ciphertext must not be empty');
  const decipher = crypto.createDecipheriv(ALGORITHM, key, iv);
  decipher.setAAD(sidecarAAD(
    scenario,
    archiveId,
    name,
    expectedPurpose,
    decoded.createdAt,
    decoded.payloadHash,
  ));
  decipher.setAuthTag(tag);
  let plaintext: Buffer;
  try {
    plaintext = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
  } catch {
    throw new Error('qualification sidecar authentication failed');
  }
  if (plaintext.length === 0 || plaintext.length > MAX_SIDECAR_BYTES) {
    throw new Error('qualification sidecar exceeds its bounded size');
  }
  const payloadHash = qualificationSHA256(plaintext);
  if (payloadHash !== decoded.payloadHash) {
    throw new Error('qualification sidecar payload hash mismatch');
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(plaintext.toString('utf8')) as unknown;
  } catch {
    throw new Error('qualification sidecar plaintext is not valid JSON');
  }
  return normalizeJSON(parsed, 'qualification sidecar payload', new Set());
}
