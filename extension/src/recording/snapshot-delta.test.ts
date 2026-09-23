import { describe, it, expect } from 'vitest';
import {
  applyPatch,
  buildSnapshotDelta,
  contentOf,
  diffJson,
  type SnapshotContent,
  type SnapshotPatchOp,
} from './snapshot-delta';
import type { DomSnapshot } from '../../../src/rule-generator/types';

function deepClone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

/** Property-style check: applying the diff of base→target to base must
 *  reproduce target exactly, for arbitrarily gnarly JSON trees. */
function expectRoundTrip(base: unknown, target: unknown): SnapshotPatchOp[] {
  const ops: SnapshotPatchOp[] = [];
  diffJson(base, target, '', ops);
  const patched = deepClone(base);
  applyPatch(patched, ops);
  expect(patched).toEqual(target);
  return ops;
}

describe('snapshot-delta', () => {
  it('round-trips nested replaces, key adds/removes, and escapes pointer tokens', () => {
    const base = {
      selectorMap: { '1': { index: 1, selector: 'a/x', boundingRect: { x: 0, y: 0 } } },
      domTree: { type: 'element', tagName: 'html', 'weird/key': 1, 'til~de': 2, children: [{ type: 'text', text: 'a' }] },
      capture: { status: 'complete', nodeCount: 3 },
    };
    const target = deepClone(base) as typeof base & Record<string, unknown> & {
      selectorMap: Record<string, { index: number; selector: string; boundingRect: { x: number; y: number } }>;
    };
    target.selectorMap['1'].boundingRect = { x: 5, y: 700 };
    target.selectorMap['2'] = { index: 2, selector: 'button', boundingRect: { x: 1, y: 1 } };
    delete (target.domTree as Record<string, unknown>)['weird/key'];
    (target.domTree as Record<string, unknown>)['new~key/with~escapes'] = true;
    target.domTree.children.push({ type: 'text', text: 'b' });
    target.capture.nodeCount = 4;

    const ops = expectRoundTrip(base, target);
    expect(ops.length).toBeGreaterThan(0);
    expect(ops.length).toBeLessThan(10);
  });

  it('round-trips array shrinks and growth in the middle of a tree', () => {
    const base = { domTree: { children: [1, 2, 3, 4, 5].map((n) => ({ n })) } };
    const shrunk = { domTree: { children: [1, 3, 5].map((n) => ({ n })) } };
    expectRoundTrip(base, shrunk);
    const grown = { domTree: { children: [0, 1, 2, 3, 4, 5, 6, 9].map((n) => ({ n })) } };
    expectRoundTrip(base, grown);
    const flipped = { domTree: { children: [1, 2, 3, 4, 5].map((n) => ({ n, rendered: n % 2 === 0 })) } };
    const ops = expectRoundTrip(base, flipped);
    // Carousel-style flips stay small: one op per flipped node.
    expect(ops.length).toBe(5);
  });

  it('type changes round-trip with a handful of field-level ops', () => {
    const base = { domTree: { type: 'element', children: ['a', 'b', 'c'] } };
    const target = { domTree: { type: 'text', text: 'now text' } };
    const ops = expectRoundTrip(base, target);
    expect(ops.length).toBeLessThanOrEqual(4);
  });

  it('buildSnapshotDelta emits a bounded patch for carousel drift and rejects big changes', () => {
    const fullSnapshot: DomSnapshot = {
      timestamp: 1,
      url: 'https://example.com/',
      selectorMap: { '1': { index: 1, tagName: 'a', selector: 'a.item', boundingRect: { x: 0, y: 0, width: 1, height: 1 } } },
      phase: 'initial',
      sequence: 0,
      domTree: {
        type: 'element', tagName: 'html',
        children: [{ type: 'text', text: 'x'.repeat(200 * 1024) }],
      },
      capture: { status: 'complete', nodeCount: 10, redactionCount: 0, removedNodeCount: 0, frames: [] },
    };
    const base = { sequence: 0, content: contentOf(fullSnapshot) };

    // Small drift: one rendered flip deep in the tree.
    const drifted = deepClone(base.content);
    (drifted.domTree as { children: Array<{ rendered?: boolean }> }).children[0].rendered = true;
    const delta = buildSnapshotDelta(base, drifted);
    expect(delta).not.toBeNull();
    expect(delta!.base).toBe(0);
    expect(delta!.patch.length).toBe(1);
    expect(JSON.stringify(delta!.patch).length).toBeLessThan(200);

    // Applying the delta to the base reproduces the drifted content.
    const patched = deepClone(base.content);
    applyPatch(patched, delta!.patch);
    expect(patched).toEqual(drifted);

    // A rewrite of the whole payload is not worth a delta.
    const rewritten: SnapshotContent = {
      selectorMap: {},
      domTree: { type: 'element', tagName: 'html', children: [{ type: 'text', text: 'y'.repeat(200 * 1024) }] },
      capture: null,
    };
    expect(buildSnapshotDelta(base, rewritten)).toBeNull();

    // Identical content is the reference-snapshot case, not a delta.
    expect(buildSnapshotDelta(base, deepClone(base.content))).toBeNull();
  });

  it('applyPatch fails closed on malformed operations', () => {
    const root = { domTree: { children: [{ n: 1 }] } };
    expect(() => applyPatch(root, [{ op: 'replace' as const, path: 'domTree', value: 1 }])).toThrow();
    expect(() => applyPatch(root, [{ op: 'replace' as const, path: '/', value: 1 }])).toThrow();
    expect(() => applyPatch(root, [{ op: 'replace' as const, path: '/domTree/missing', value: 1 }])).toThrow();
    expect(() => applyPatch(root, [{ op: 'remove' as const, path: '/domTree/children/9' }])).toThrow();
    expect(() => applyPatch(root, [{ op: 'add' as const, path: '/domTree/children/-', value: 1 }])).toThrow();
    expect(() => applyPatch(root, [{ op: 'add' as const, path: '/domTree/children/2', value: 1 }])).toThrow();
    expect(() => applyPatch(root, [{ op: 'copy' as unknown as 'add', path: '/domTree', value: 1 }])).toThrow(/unsupported/);
    expect(() => applyPatch(root, [{ op: 'replace' as const, path: '/domTree/children/0/n' }])).toThrow(/requires a value/);
  });

  it('treats undefined-valued keys as absent so no op loses its value on the wire', () => {
    // Regression shape from live baidu recordings: the serializer leaves
    // optional sanitization fields explicitly undefined on some nodes, and
    // JSON.stringify drops them — a `value: undefined` replace would arrive
    // at the server as a valueless op and fail validation.
    const base = {
      domTree: {
        type: 'element', tagName: 'section',
        sanitization: { markupAltered: true, contentOmitted: true, alteredAttributes: ['class'] },
      },
      selectorMap: {},
      capture: { nodeCount: 3 },
    };
    const target = {
      domTree: {
        type: 'element', tagName: 'section',
        // Key present in the TS object but undefined — JSON-invisible.
        sanitization: { markupAltered: true, contentOmitted: true, alteredAttributes: undefined },
      },
      selectorMap: {},
      capture: { nodeCount: 3, extra: undefined },
    };

    const ops: SnapshotPatchOp[] = [];
    diffJson(base, target, '', ops);
    // The dropped attribute becomes a remove; the undefined-valued new key
    // on capture never materializes.
    expect(ops).toEqual([{ op: 'remove', path: '/domTree/sanitization/alteredAttributes' }]);

    // The emitted patch survives its own JSON round-trip unchanged.
    const wire = JSON.parse(JSON.stringify(ops)) as SnapshotPatchOp[];
    expect(wire).toEqual(ops);
    for (const op of wire) {
      if (op.op !== 'remove') expect(op.value).not.toBeUndefined();
    }

    // Applying it to the base reproduces the target's JSON form exactly.
    const patched = deepClone(base);
    applyPatch(patched, wire);
    expect(JSON.parse(JSON.stringify(patched))).toEqual(JSON.parse(JSON.stringify(target)));

    // And buildSnapshotDelta never emits a patch that mutates on the wire.
    const delta = buildSnapshotDelta({ sequence: 4, content: base }, target);
    expect(delta).not.toBeNull();
    expect(JSON.parse(JSON.stringify(delta!.patch))).toEqual(delta!.patch);
  });

  it('maps undefined array elements to the null the wire would carry', () => {
    const base = { domTree: { children: [{ a: 1 }] } };
    const target = { domTree: { children: [{ a: 1 }, { b: undefined }, undefined] } };
    const ops: SnapshotPatchOp[] = [];
    diffJson(base, target, '', ops);
    const wire = JSON.parse(JSON.stringify(ops)) as SnapshotPatchOp[];
    const patched = deepClone(base);
    applyPatch(patched, wire);
    expect(JSON.parse(JSON.stringify(patched))).toEqual(JSON.parse(JSON.stringify(target)));
  });
});
