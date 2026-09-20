/**
 * Bridge protocol — the host-side validation boundary for the single
 * page→host channel `window.__ocWorkerBridge(method, payload)`.
 *
 * The page realm is untrusted: it runs approved DSL plus arbitrary target-site
 * JavaScript. Every call is validated here before touching the task transport:
 *   - the method must be one of the eight whitelisted names;
 *   - the total serialized payload must fit the byte budget;
 *   - each method's payload must match its declared shape, and only known
 *     fields are forwarded (sanitized copies, never the caller's object).
 *
 * Unknown methods and malformed payloads throw BridgeValidationError, which
 * Playwright surfaces to the page as a rejected promise — the page learns
 * nothing beyond "rejected".
 */

import type { HumanInterventionRequest, Snapshot } from '../scriptcat-engine/types';
import type { StepPath } from '../scriptcat-engine/step-path';

export const MAX_BRIDGE_PAYLOAD_BYTES = 1024 * 1024;
const MAX_BRIDGE_STRING = 64 * 1024;

export type BridgeMethod =
  | 'sendResult'
  | 'sendLog'
  | 'sendStatus'
  | 'sendSnapshot'
  | 'sendHeartbeat'
  | 'requestHuman'
  | 'checkpointContext'
  | 'complete';

export interface BridgeCompletePayload {
  status: 'success' | 'failure' | 'cancelled';
  message?: string;
  error?: { type?: string; message?: string };
  partialData?: unknown;
}

export interface BridgeCheckpointContext {
  taskId: string;
  lastCompletedStepPath: StepPath;
  extracted: Record<string, any>;
  evaluated: Record<string, any>;
  captured: Record<string, any>;
}

export type ValidatedBridgeCall =
  | { method: 'sendResult'; payload: { payload: unknown; immediate: boolean } }
  | { method: 'sendLog'; payload: { level: string; message: string; extra?: Record<string, any> } }
  | { method: 'sendStatus'; payload: { status: string; message?: string } }
  | { method: 'sendSnapshot'; payload: Snapshot }
  | { method: 'sendHeartbeat'; payload: { payload?: Record<string, any> } }
  | { method: 'requestHuman'; payload: HumanInterventionRequest }
  | { method: 'checkpointContext'; payload: BridgeCheckpointContext }
  | { method: 'complete'; payload: BridgeCompletePayload };

export class BridgeValidationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BridgeValidationError';
  }
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function requireString(value: unknown, field: string, options?: { nonEmpty?: boolean }): string {
  if (typeof value !== 'string') {
    throw new BridgeValidationError(`${field} must be a string`);
  }
  if (options?.nonEmpty && value.length === 0) {
    throw new BridgeValidationError(`${field} must be a non-empty string`);
  }
  if (value.length > MAX_BRIDGE_STRING) {
    throw new BridgeValidationError(`${field} exceeds the string budget`);
  }
  return value;
}

function optionalString(value: unknown, field: string): string | undefined {
  if (value === undefined || value === null) return undefined;
  return requireString(value, field);
}

function requireStepPath(value: unknown, field: string): StepPath {
  if (!Array.isArray(value)) {
    throw new BridgeValidationError(`${field} must be an array`);
  }
  for (const frame of value) {
    if (!isRecord(frame) || typeof frame.kind !== 'string' || typeof frame.childIdx !== 'number') {
      throw new BridgeValidationError(`${field} contains a malformed frame`);
    }
  }
  return value as StepPath;
}

function requireRecord(value: unknown, field: string): Record<string, any> {
  if (!isRecord(value)) {
    throw new BridgeValidationError(`${field} must be an object`);
  }
  return value;
}

function validateSendResult(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'sendResult payload');
  return { method: 'sendResult', payload: { payload: p.payload, immediate: p.immediate === true } };
}

function validateSendLog(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'sendLog payload');
  const level = requireString(p.level, 'sendLog.level', { nonEmpty: true });
  const message = requireString(p.message, 'sendLog.message');
  const extra = p.extra === undefined || p.extra === null ? undefined : requireRecord(p.extra, 'sendLog.extra');
  return { method: 'sendLog', payload: { level, message, extra } };
}

function validateSendStatus(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'sendStatus payload');
  const status = requireString(p.status, 'sendStatus.status', { nonEmpty: true });
  const message = optionalString(p.message, 'sendStatus.message');
  return { method: 'sendStatus', payload: { status, message } };
}

function validateSendSnapshot(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'sendSnapshot payload');
  const name = requireString(p.name, 'sendSnapshot.name', { nonEmpty: true });
  if (p.type !== 'html' && p.type !== 'dom' && p.type !== 'screenshot') {
    throw new BridgeValidationError('sendSnapshot.type must be html, dom, or screenshot');
  }
  const data = requireString(p.data, 'sendSnapshot.data');
  return { method: 'sendSnapshot', payload: { name, type: p.type, data } };
}

function validateSendHeartbeat(raw: unknown): ValidatedBridgeCall {
  const p = raw === undefined || raw === null ? {} : requireRecord(raw, 'sendHeartbeat payload');
  const inner = p.payload === undefined || p.payload === null ? undefined : requireRecord(p.payload, 'sendHeartbeat.payload');
  return { method: 'sendHeartbeat', payload: { payload: inner } };
}

const HUMAN_TYPES = new Set(['captcha', '2fa', 'confirmation', 'generic']);

function validateRequestHuman(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'requestHuman payload');
  if (!HUMAN_TYPES.has(p.type)) {
    throw new BridgeValidationError('requestHuman.type is not a supported intervention type');
  }
  const prompt = requireString(p.prompt, 'requestHuman.prompt', { nonEmpty: true });
  if (typeof p.timeoutMs !== 'number' || !Number.isFinite(p.timeoutMs) || p.timeoutMs <= 0) {
    throw new BridgeValidationError('requestHuman.timeoutMs must be a positive number');
  }
  const checkpoint = requireRecord(p.checkpoint, 'requestHuman.checkpoint');
  const stepId = requireString(checkpoint.stepId, 'requestHuman.checkpoint.stepId', { nonEmpty: true });
  const url = requireString(checkpoint.url, 'requestHuman.checkpoint.url', { nonEmpty: true });
  return {
    method: 'requestHuman',
    payload: {
      type: p.type,
      prompt,
      timeoutMs: p.timeoutMs,
      checkpoint: { stepId, url },
    },
  };
}

function validateCheckpointContext(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'checkpointContext payload');
  const taskId = requireString(p.taskId, 'checkpointContext.taskId', { nonEmpty: true });
  const lastCompletedStepPath = requireStepPath(p.lastCompletedStepPath, 'checkpointContext.lastCompletedStepPath');
  return {
    method: 'checkpointContext',
    payload: {
      taskId,
      lastCompletedStepPath,
      extracted: requireRecord(p.extracted ?? {}, 'checkpointContext.extracted'),
      evaluated: requireRecord(p.evaluated ?? {}, 'checkpointContext.evaluated'),
      captured: requireRecord(p.captured ?? {}, 'checkpointContext.captured'),
    },
  };
}

function validateComplete(raw: unknown): ValidatedBridgeCall {
  const p = requireRecord(raw, 'complete payload');
  if (p.status !== 'success' && p.status !== 'failure' && p.status !== 'cancelled') {
    throw new BridgeValidationError('complete.status must be success, failure, or cancelled');
  }
  let error: BridgeCompletePayload['error'];
  if (p.error !== undefined && p.error !== null) {
    const e = requireRecord(p.error, 'complete.error');
    error = {
      type: optionalString(e.type, 'complete.error.type'),
      message: optionalString(e.message, 'complete.error.message'),
    };
  }
  return {
    method: 'complete',
    payload: {
      status: p.status,
      message: optionalString(p.message, 'complete.message'),
      error,
      partialData: p.partialData,
    },
  };
}

const VALIDATORS: Record<BridgeMethod, (raw: unknown) => ValidatedBridgeCall> = {
  sendResult: validateSendResult,
  sendLog: validateSendLog,
  sendStatus: validateSendStatus,
  sendSnapshot: validateSendSnapshot,
  sendHeartbeat: validateSendHeartbeat,
  requestHuman: validateRequestHuman,
  checkpointContext: validateCheckpointContext,
  complete: validateComplete,
};

export const BRIDGE_METHODS = Object.keys(VALIDATORS) as BridgeMethod[];

/** Validate one raw bridge call; returns a sanitized copy to forward. */
export function validateBridgeCall(method: unknown, rawPayload: unknown): ValidatedBridgeCall {
  // hasOwnProperty: a bare `in` check would admit '__proto__' and other
  // Object.prototype members as bridge "methods".
  if (typeof method !== 'string' || !Object.prototype.hasOwnProperty.call(VALIDATORS, method)) {
    throw new BridgeValidationError(`unknown bridge method: ${String(method)}`);
  }
  let size = 0;
  try {
    size = JSON.stringify(rawPayload)?.length ?? 0;
  } catch {
    throw new BridgeValidationError('bridge payload is not serializable');
  }
  if (size > MAX_BRIDGE_PAYLOAD_BYTES) {
    throw new BridgeValidationError(`bridge payload exceeds the ${MAX_BRIDGE_PAYLOAD_BYTES}-byte budget`);
  }
  return VALIDATORS[method as BridgeMethod](rawPayload);
}
