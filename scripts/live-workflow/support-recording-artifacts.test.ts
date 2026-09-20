// @vitest-environment node

import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { sanitizeRecordingEventTrace, writeRecordingEventTraceArtifact } from './support';

describe('live-workflow recording failure artifacts', () => {
  it('retains bounded structural event evidence without URL queries or hashes', () => {
    const recording = {
      events: [
        { type: 'inputText', index: 4, text: '  safe   query  ', submit: true, timestamp: 1 },
        { type: 'submitForm', index: 4, submitterIndex: 8, timestamp: 2 },
        { type: 'navigate', url: 'https://example.com/search?q=secret#private', timestamp: 3 },
      ],
    } as PageAgentRecording;

    expect(sanitizeRecordingEventTrace(recording)).toEqual({
      version: 1,
      eventCount: 3,
      retainedEventCount: 3,
      eventsTruncated: false,
      events: [
        {
          order: 0, type: 'inputText', timestamp: 1, index: 4,
          text: 'safe query', textLength: 10, textTruncated: false, submit: true,
        },
        { order: 1, type: 'submitForm', timestamp: 2, index: 4, submitterIndex: 8 },
        {
          order: 2, type: 'navigate', timestamp: 3,
          url: { origin: 'https://example.com', pathname: '/search' },
        },
      ],
    });
    expect(JSON.stringify(sanitizeRecordingEventTrace(recording))).not.toContain('secret');
    expect(JSON.stringify(sanitizeRecordingEventTrace(recording))).not.toContain('private');
  });

  it('caps event count and normalized input evidence', () => {
    const events = Array.from({ length: 257 }, (_, index) => ({
      type: 'inputText' as const,
      index,
      text: ` ${'x'.repeat(200)} `,
      timestamp: index,
    }));
    const trace = sanitizeRecordingEventTrace({ events } as PageAgentRecording) as {
      retainedEventCount: number;
      eventsTruncated: boolean;
      events: Array<{ text: string; textLength: number; textTruncated: boolean }>;
    };

    expect(trace.retainedEventCount).toBe(256);
    expect(trace.eventsTruncated).toBe(true);
    expect(trace.events[0]).toMatchObject({ textLength: 200, textTruncated: true });
    expect(trace.events[0].text).toHaveLength(160);
  });

  it('writes the production failure artifact as parseable sanitized JSON', () => {
    const artifactsDir = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-recording-trace-test-'));
    try {
      const recording = {
        events: [
          { type: 'inputText', index: 2, text: 'recorded query', timestamp: 10 },
          { type: 'submitForm', index: 2, timestamp: 11 },
          { type: 'navigate', url: 'https://example.com/search?q=private', timestamp: 12 },
        ],
      } as PageAgentRecording;

      const artifact = writeRecordingEventTraceArtifact(artifactsDir, recording);
      expect(artifact).toBe(path.join(artifactsDir, 'recording-events.json'));
      const raw = fs.readFileSync(artifact, 'utf8');
      expect(JSON.parse(raw)).toEqual(sanitizeRecordingEventTrace(recording));
      expect(raw).not.toContain('private');
    } finally {
      fs.rmSync(artifactsDir, { recursive: true, force: true });
    }
  });
});
