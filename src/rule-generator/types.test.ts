/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import Ajv from 'ajv';
import addFormats from 'ajv-formats';
import recordingSchema from './schema/page-agent-recording.schema.json';

describe('PageAgentRecording schema', () => {
  it('validates a minimal recording', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    const valid = validate({
      version: '1.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Test',
        recordedAt: '2026-07-04T10:00:00Z',
        domain: 'example.com',
      },
      events: [{ type: 'navigate', url: 'https://example.com/', timestamp: 1 }],
      snapshots: [
        {
          timestamp: 1,
          url: 'https://example.com/',
          selectorMap: {
            '1': { index: 1, tagName: 'button', selector: '#btn', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
          },
        },
      ],
    });
    expect(valid).toBe(true);
  });

  it('rejects recording without required meta', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    const valid = validate({
      version: '1.0.0',
      meta: { startUrl: 'https://example.com/' },
      events: [],
      snapshots: [],
    });
    expect(valid).toBe(false);
  });

  it('validates a completed semantic recording v2 contract', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    const valid = validate({
      version: '2.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Semantic recording',
        recordedAt: '2026-07-16T08:00:00Z',
        endedAt: '2026-07-16T08:01:00Z',
        domain: 'example.com',
        semanticDomVersion: '1',
        sanitizationVersion: 'extension-v2',
      },
      limits: { maxActions: 500, maxDurationMs: 7200000, maxBytes: 26214400, warningThreshold: 0.8 },
      warnings: [],
      termination: { reason: 'user', message: 'done', timestamp: 2, complete: true },
      events: [{ type: 'click', index: 1, timestamp: 1 }],
      snapshots: [
        {
          timestamp: 0,
          url: 'https://example.com/',
          phase: 'initial',
          sequence: 0,
          selectorMap: {},
          domTree: {
            type: 'element',
            tagName: 'html',
            children: [{
              type: 'element',
              tagName: 'section',
              rendered: false,
              sanitization: {
                markupAltered: true,
                contentOmitted: true,
                alteredAttributes: ['class'],
              },
            }],
          },
          capture: { status: 'complete', nodeCount: 2, redactionCount: 1, removedNodeCount: 0, frames: [] },
        },
        {
          timestamp: 2,
          url: 'https://example.com/',
          phase: 'final',
          sequence: 1,
          selectorMap: {},
          domTree: { type: 'element', tagName: 'html' },
          capture: { status: 'complete', nodeCount: 2, redactionCount: 1, removedNodeCount: 0, frames: [] },
        },
      ],
    });
    expect(validate.errors).toBeNull();
    expect(valid).toBe(true);
  });

  it('accepts v2 reference snapshots and rejects snapshots that are neither full nor references', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    const base = {
      version: '2.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Dedup references',
        recordedAt: '2026-09-22T08:00:00Z',
        endedAt: '2026-09-22T08:01:00Z',
        domain: 'example.com',
        semanticDomVersion: '1',
        sanitizationVersion: 'extension-v2',
      },
      limits: { maxActions: 500, maxDurationMs: 7200000, maxBytes: 26214400, warningThreshold: 0.8 },
      warnings: [],
      termination: { reason: 'user', message: 'done', timestamp: 2, complete: true },
      events: [{ type: 'click', index: 1, timestamp: 1 }],
    };
    const contentSnapshot = {
      timestamp: 0,
      url: 'https://example.com/',
      phase: 'initial',
      sequence: 0,
      selectorMap: {},
      domTree: { type: 'element', tagName: 'html' },
      capture: { status: 'complete', nodeCount: 2, redactionCount: 0, removedNodeCount: 0, frames: [] },
    };
    const reference = { timestamp: 1, url: 'https://example.com/', selectorMap: {}, phase: 'before-action', sequence: 1, actionIndex: 0, ref: 0 };

    // A v2 snapshot may replace its duplicated content with a back-reference
    // to an earlier full snapshot's sequence.
    expect(validate({ ...base, snapshots: [contentSnapshot, reference, { ...contentSnapshot, phase: 'final', sequence: 2 }] })).toBe(true);
    expect(validate({ ...base, snapshots: [contentSnapshot, { ...reference, ref: -1 }] })).toBe(false);
    // Neither a full snapshot (no domTree/capture) nor a reference (no ref):
    // not a valid variant.
    expect(validate({
      ...base,
      snapshots: [contentSnapshot, { timestamp: 1, url: 'https://example.com/', phase: 'final', sequence: 1 }],
    })).toBe(false);
  });

  it('rejects incomplete v2 recordings without limits, termination, and two snapshots', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    expect(validate({
      version: '2.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Incomplete',
        recordedAt: '2026-07-16T08:00:00Z',
        domain: 'example.com',
      },
      events: [],
      snapshots: [],
    })).toBe(false);
  });

  it('accepts submitForm events and rejects v2 snapshots without semantic DOM data', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    expect(validate({
      version: '1.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Form',
        recordedAt: '2026-07-16T08:00:00Z',
        domain: 'example.com',
      },
      events: [{ type: 'submitForm', index: 1, submitterIndex: 2, timestamp: 1 }],
      snapshots: [],
    })).toBe(true);

    expect(validate({
      version: '2.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Missing DOM',
        recordedAt: '2026-07-16T08:00:00Z',
        endedAt: '2026-07-16T08:01:00Z',
        domain: 'example.com',
        semanticDomVersion: '1',
        sanitizationVersion: 'extension-v1',
      },
      limits: { maxActions: 500, maxDurationMs: 7200000, maxBytes: 26214400, warningThreshold: 0.8 },
      warnings: [],
      termination: { reason: 'user', message: 'done', timestamp: 2, complete: true },
      events: [],
      snapshots: [
        { timestamp: 1, url: 'https://example.com/', selectorMap: {}, phase: 'initial', sequence: 0 },
        { timestamp: 2, url: 'https://example.com/', selectorMap: {}, phase: 'final', sequence: 1 },
      ],
    })).toBe(false);
  });

  it('validates bounded page marks on recordings', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    const base = {
      version: '1.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Marked recording',
        recordedAt: '2026-09-21T08:00:00Z',
        domain: 'example.com',
      },
      events: [],
      snapshots: [],
    };
    const mark = {
      id: 'mark-1',
      timestamp: 1,
      url: 'https://example.com/',
      role: 'field',
      note: '商品标题字段',
      element: {
        index: 1,
        tagName: 'span',
        selector: '.product-title',
        stableSelector: '.product-title',
        text: 'Example',
        boundingRect: { x: 0, y: 0, width: 10, height: 10 },
      },
      actionIndex: 0,
      snapshotSequence: 0,
      state: 'https://example.com/',
      canonicalId: 'canonical-mark-1',
    };

    expect(validate({ ...base, marks: [mark] })).toBe(true);
    expect(validate({ ...base, marks: [{ ...mark, role: 'ads' }] })).toBe(false);
    expect(validate({ ...base, marks: [{ ...mark, note: 'x'.repeat(201) }] })).toBe(false);
    expect(validate({ ...base, marks: Array.from({ length: 25 }, (_, index) => ({ ...mark, id: `mark-${index}` })) })).toBe(false);
  });

  it('rejects malformed or unbounded DOM sanitization provenance', () => {
    const ajv = new Ajv();
    addFormats(ajv);
    const validate = ajv.compile(recordingSchema);
    const recordingWith = (sanitization: unknown) => ({
      version: '1.0.0',
      meta: {
        startUrl: 'https://example.com/',
        title: 'Malformed provenance',
        recordedAt: '2026-07-16T08:00:00Z',
        domain: 'example.com',
      },
      events: [],
      snapshots: [{
        timestamp: 1,
        url: 'https://example.com/',
        selectorMap: {},
        domTree: { type: 'element', tagName: 'main', sanitization },
      }],
    });

    for (const sanitization of [
      {},
      { markupAltered: false },
      { alteredAttributes: [] },
      { alteredAttributes: Array.from({ length: 65 }, (_, index) => `data-${index}`) },
      { markupAltered: true, unknown: true },
    ]) {
      expect(validate(recordingWith(sanitization)), JSON.stringify(sanitization)).toBe(false);
    }

    const textRendered = recordingWith({ markupAltered: true });
    Object.assign(textRendered.snapshots[0], {
      domTree: {
        type: 'text',
        text: 'hidden text',
        rendered: false,
        sanitization: { markupAltered: true },
      },
    });
    expect(validate(textRendered)).toBe(false);
  });
});
