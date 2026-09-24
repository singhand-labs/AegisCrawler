import { describe, it, expect } from 'vitest';
import { expandSnapshotReferences } from './snapshot-refs';
import { convert } from './PageAgentToDslConverter';
import type { PageAgentRecording, DomSnapshot } from '../types';

function fullSnapshot(overrides: Partial<DomSnapshot> = {}): DomSnapshot {
  return {
    timestamp: 0,
    url: 'https://example.com/',
    selectorMap: {},
    phase: 'initial',
    sequence: 0,
    domTree: { type: 'element', tagName: 'html', children: [{ type: 'text', text: 'Example' }] },
    capture: { status: 'complete', nodeCount: 2, redactionCount: 0, removedNodeCount: 0, frames: [] },
    ...overrides,
  };
}

function compressedRecording(): PageAgentRecording {
  const initial = fullSnapshot({
    selectorMap: {
      '1': { index: 1, tagName: 'a', selector: 'a.next', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
    },
  });
  // A delta whose patch registers a NEW element (index 2) in the selector
  // map — exactly the case that broke client-side element resolution.
  const delta = fullSnapshot({
    timestamp: 10,
    phase: 'before-action',
    sequence: 1,
    actionIndex: 0,
    selectorMap: {},
    base: 0,
    patch: [
      { op: 'add', path: '/selectorMap/2', value: { index: 2, tagName: 'button', selector: 'button.go', boundingRect: { x: 5, y: 5, width: 8, height: 8 } } },
      { op: 'replace', path: '/domTree/children/0/text', value: 'Mutated' },
    ],
  });
  // A later identical capture referencing the DELTA's sequence.
  const ref = fullSnapshot({
    timestamp: 20,
    phase: 'final',
    sequence: 2,
    selectorMap: {},
    ref: 1,
  });
  return {
    version: '2.0.0',
    meta: {
      startUrl: 'https://example.com/',
      title: 'Example',
      recordedAt: '2026-09-24T08:00:00Z',
      endedAt: '2026-09-24T08:01:00Z',
      domain: 'example.com',
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v2',
    },
    limits: { maxActions: 500, maxDurationMs: 7200000, maxBytes: 26214400, warningThreshold: 0.8 },
    warnings: [],
    termination: { reason: 'user', message: 'done', timestamp: 25, complete: true },
    events: [{ type: 'click', index: 2, timestamp: 10 }],
    snapshots: [initial, delta, ref],
  };
}

describe('expandSnapshotReferences (client-side)', () => {
  it('expands deltas, allows refs to name expanded deltas, and keeps positional identity', () => {
    const recording = compressedRecording();
    expandSnapshotReferences(recording);
    const [initial, delta, ref] = recording.snapshots;

    expect(delta.patch).toBeUndefined();
    expect(delta.base).toBeUndefined();
    expect(delta.domTree).toBeDefined();
    expect((delta.domTree as { children: Array<{ text?: string }> }).children[0].text).toBe('Mutated');
    expect(delta.selectorMap?.['2']?.selector).toBe('button.go');
    expect(delta.phase).toBe('before-action');
    expect(delta.timestamp).toBe(10);

    // The base snapshot itself stays untouched.
    expect((initial.domTree as { children: Array<{ text?: string }> }).children[0].text).toBe('Example');
    expect(initial.selectorMap?.['2']).toBeUndefined();

    // The ref resolved to the expanded delta's content.
    expect(ref.ref).toBeUndefined();
    expect(JSON.stringify(ref.domTree)).toBe(JSON.stringify(delta.domTree));
    expect(ref.selectorMap?.['2']?.selector).toBe('button.go');
  });

  it('lets the rule converter resolve an element whose index only exists in a delta patch', () => {
    // This is the exact regression: without expansion, convert() threw
    // "Element not found in recording snapshots: index=2".
    const recording = compressedRecording();
    expandSnapshotReferences(recording);
    const rule = convert(recording, { ruleIdPrefix: 'ext' });
    // The click resolved to a real target (the optimizer may rewrite the
    // raw selector string, e.g. button.go → .go).
    const step = rule.steps.find((s) => s.action === 'click') as { target: { $ref?: string } };
    expect(step).toBeDefined();
    expect(step.target?.$ref).toBeDefined();
    const alias = rule.selectors?.[step.target.$ref as string] as { selector?: string } | undefined;
    expect(alias?.selector).toBeTruthy();
    expect(alias?.selector).toContain('go');
  });

  it('fails closed on malformed pointers, patches, and unknown bases', () => {
    const cases: Array<{ name: string; mutate: (rec: PageAgentRecording) => void }> = [
      { name: 'unknown ref', mutate: (r) => { r.snapshots[2].ref = 99; } },
      { name: 'forward ref', mutate: (r) => { r.snapshots[0].ref = 2; } },
      { name: 'negative ref', mutate: (r) => { r.snapshots[2].ref = -1; } },
      { name: 'ref and patch together', mutate: (r) => { (r.snapshots[1] as DomSnapshot & { ref?: number }).ref = 0; } },
      { name: 'unknown base', mutate: (r) => { r.snapshots[1].base = 42; } },
      { name: 'bad patch path', mutate: (r) => { r.snapshots[1].patch = [{ op: 'replace', path: 'domTree', value: 1 }]; } },
      { name: 'valueless op', mutate: (r) => { r.snapshots[1].patch = [{ op: 'replace', path: '/domTree' }]; } },
      { name: 'missing path', mutate: (r) => { r.snapshots[1].patch = [{ op: 'replace', path: '/domTree/missing', value: 1 }]; } },
    ];
    for (const { name, mutate } of cases) {
      const recording = compressedRecording();
      mutate(recording);
      expect(() => expandSnapshotReferences(recording), name).toThrow();
    }
  });

  it('is a no-op for legacy v1 recordings and snapshot-less payloads', () => {
    const legacy = { version: '1.0.0', snapshots: [{ timestamp: 1, url: 'https://example.com/', selectorMap: {} }] } as unknown as PageAgentRecording;
    expect(() => expandSnapshotReferences(legacy)).not.toThrow();
  });
});
