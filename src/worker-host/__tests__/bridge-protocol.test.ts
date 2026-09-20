/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import {
  BridgeValidationError,
  MAX_BRIDGE_PAYLOAD_BYTES,
  validateBridgeCall,
} from '../bridge-protocol';

describe('validateBridgeCall', () => {
  it('rejects unknown bridge methods', () => {
    expect(() => validateBridgeCall('stealCredentials', {})).toThrow(BridgeValidationError);
    expect(() => validateBridgeCall('fetchRule', {})).toThrow(/unknown bridge method/);
    expect(() => validateBridgeCall(42, {})).toThrow(BridgeValidationError);
    expect(() => validateBridgeCall('__proto__', {})).toThrow(BridgeValidationError);
  });

  it('rejects payloads beyond the byte budget', () => {
    const big = { payload: 'x'.repeat(MAX_BRIDGE_PAYLOAD_BYTES) };
    expect(() => validateBridgeCall('sendResult', big)).toThrow(/byte budget/);
  });

  it('accepts and sanitizes sendResult', () => {
    const call = validateBridgeCall('sendResult', { payload: [{ a: 1 }], immediate: true, evil: 'dropped' });
    expect(call).toEqual({ method: 'sendResult', payload: { payload: [{ a: 1 }], immediate: true } });
  });

  it('validates sendLog shapes', () => {
    expect(validateBridgeCall('sendLog', { level: 'info', message: 'hello', extra: { k: 1 } }))
      .toEqual({ method: 'sendLog', payload: { level: 'info', message: 'hello', extra: { k: 1 } } });
    expect(() => validateBridgeCall('sendLog', { level: 'info' })).toThrow(/message/);
    expect(() => validateBridgeCall('sendLog', { level: 1, message: 'x' })).toThrow(/level/);
    expect(() => validateBridgeCall('sendLog', 'not-an-object')).toThrow(BridgeValidationError);
  });

  it('validates sendStatus shapes', () => {
    expect(validateBridgeCall('sendStatus', { status: 'running' }))
      .toEqual({ method: 'sendStatus', payload: { status: 'running', message: undefined } });
    expect(validateBridgeCall('sendStatus', { status: 'running', message: 'm' }))
      .toEqual({ method: 'sendStatus', payload: { status: 'running', message: 'm' } });
    // The executor legitimately completes with an empty message.
    expect(validateBridgeCall('sendStatus', { status: 'success', message: '' }))
      .toEqual({ method: 'sendStatus', payload: { status: 'success', message: '' } });
    expect(() => validateBridgeCall('sendStatus', { status: '' })).toThrow(BridgeValidationError);
  });

  it('validates sendSnapshot shapes', () => {
    expect(validateBridgeCall('sendSnapshot', { name: 'n', type: 'dom', data: '{}' }))
      .toEqual({ method: 'sendSnapshot', payload: { name: 'n', type: 'dom', data: '{}' } });
    expect(() => validateBridgeCall('sendSnapshot', { name: 'n', type: 'raw', data: '{}' }))
      .toThrow(/type/);
    expect(() => validateBridgeCall('sendSnapshot', { name: 'n', type: 'dom', data: 42 }))
      .toThrow(/data/);
  });

  it('validates sendHeartbeat shapes', () => {
    expect(validateBridgeCall('sendHeartbeat', { payload: { url: 'https://example.com' } }))
      .toEqual({ method: 'sendHeartbeat', payload: { payload: { url: 'https://example.com' } } });
    expect(validateBridgeCall('sendHeartbeat', {})).toEqual({ method: 'sendHeartbeat', payload: { payload: undefined } });
    expect(() => validateBridgeCall('sendHeartbeat', { payload: 'url' })).toThrow(BridgeValidationError);
  });

  it('validates requestHuman shapes', () => {
    const valid = {
      type: 'confirmation',
      prompt: 'approve',
      timeoutMs: 30_000,
      checkpoint: { stepId: 's1', url: 'https://example.com/page' },
    };
    const call = validateBridgeCall('requestHuman', { ...valid, extra: 'dropped' });
    expect(call).toEqual({ method: 'requestHuman', payload: valid });
    expect(() => validateBridgeCall('requestHuman', { ...valid, type: 'anything' })).toThrow(/type/);
    expect(() => validateBridgeCall('requestHuman', { ...valid, timeoutMs: -5 })).toThrow(/timeoutMs/);
    expect(() => validateBridgeCall('requestHuman', { ...valid, checkpoint: { stepId: 's1' } })).toThrow(/url/);
  });

  it('validates checkpointContext shapes', () => {
    const valid = {
      taskId: 'task-1',
      lastCompletedStepPath: [{ kind: 'top', childIdx: 0 }],
      extracted: { a: 1 },
      evaluated: {},
      captured: {},
    };
    expect(validateBridgeCall('checkpointContext', valid)).toEqual({ method: 'checkpointContext', payload: valid });
    expect(() => validateBridgeCall('checkpointContext', { ...valid, taskId: '' })).toThrow(/taskId/);
    expect(() => validateBridgeCall('checkpointContext', { ...valid, lastCompletedStepPath: 'path' }))
      .toThrow(/array/);
    expect(() => validateBridgeCall('checkpointContext', { ...valid, lastCompletedStepPath: [{ kind: 'top' }] }))
      .toThrow(/frame/);
  });

  it('validates complete shapes', () => {
    expect(validateBridgeCall('complete', { status: 'success', message: 'ok', partialData: { a: [1] } }))
      .toEqual({
        method: 'complete',
        payload: { status: 'success', message: 'ok', error: undefined, partialData: { a: [1] } },
      });
    expect(validateBridgeCall('complete', { status: 'failure', error: { type: 'E', message: 'm', extra: 1 } }))
      .toEqual({
        method: 'complete',
        payload: { status: 'failure', message: undefined, error: { type: 'E', message: 'm' }, partialData: undefined },
      });
    expect(() => validateBridgeCall('complete', { status: 'done' })).toThrow(/status/);
  });
});
