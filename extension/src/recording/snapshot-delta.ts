import type { DomSnapshot, SnapshotPatchOp } from '../../../src/rule-generator';
import { applyJsonPatch, type JsonPatchOp } from '../../../src/rule-generator/converter/json-patch';

export type { SnapshotPatchOp, JsonPatchOp };

/** Snapshot content that deltas compare and patch. `url` stays positional —
 *  every snapshot carries its own — so only the heavy payload participates. */
export interface SnapshotContent {
  selectorMap: unknown;
  domTree: unknown;
  capture: unknown;
}

/** Emission caps: a delta past either bound falls back to a full snapshot. */
export const MAX_DELTA_PATCH_OPS = 2048;
/** A patch at or above this fraction of the full content buys nothing. */
export const MAX_DELTA_SIZE_RATIO = 0.5;

/** Normalized content of a stored snapshot (undefined fields canonicalized so
 *  diffs never see `undefined`, which JSON cannot represent). */
export function contentOf(snapshot: DomSnapshot): SnapshotContent {
  return {
    selectorMap: snapshot.selectorMap ?? {},
    domTree: snapshot.domTree ?? null,
    capture: snapshot.capture ?? null,
  };
}

function escapeToken(token: string): string {
  return token.replace(/~/g, '~0').replace(/\//g, '~1');
}

function isContainer(value: unknown): value is Record<string, unknown> | unknown[] {
  return typeof value === 'object' && value !== null;
}

/** Structural JSON diff producing an RFC 6902 subset (add / remove /
 *  replace). Arrays diff index-aligned: trailing removals go descending and
 *  additions ascending so a straightforward applier reproduces the target.
 *  Property semantics follow JSON, not the TS in-memory shape: a key whose
 *  value is `undefined` counts as absent (JSON.stringify drops it), so a
 *  node dropping an optional field diffs to a remove — never to a
 *  `value: undefined` replace, which would lose its value on the wire and
 *  fail server-side validation. */
export function diffJson(
  base: unknown,
  target: unknown,
  pointer: string,
  ops: JsonPatchOp[],
): void {
  if (base === target) return;
  if (typeof base !== typeof target || !isContainer(base) || !isContainer(target)
    || Array.isArray(base) !== Array.isArray(target)) {
    if (base !== target) ops.push({ op: 'replace', path: pointer, value: jsonSafe(target) });
    return;
  }
  if (Array.isArray(base) && Array.isArray(target)) {
    const shared = Math.min(base.length, target.length);
    for (let index = 0; index < shared; index += 1) {
      diffJson(base[index], target[index], `${pointer}/${index}`, ops);
    }
    for (let index = base.length - 1; index >= shared; index -= 1) {
      ops.push({ op: 'remove', path: `${pointer}/${index}` });
    }
    for (let index = shared; index < target.length; index += 1) {
      ops.push({ op: 'add', path: `${pointer}/${index}`, value: jsonSafe(target[index]) });
    }
    return;
  }
  const baseRecord = base as Record<string, unknown>;
  const targetRecord = target as Record<string, unknown>;
  const present = (record: Record<string, unknown>, key: string): boolean =>
    key in record && record[key] !== undefined;
  for (const key of Object.keys(baseRecord)) {
    if (!present(targetRecord, key) && present(baseRecord, key)) {
      ops.push({ op: 'remove', path: `${pointer}/${escapeToken(key)}` });
    }
  }
  for (const key of Object.keys(targetRecord)) {
    if (!present(targetRecord, key)) continue; // undefined === absent
    const token = escapeToken(key);
    if (!present(baseRecord, key)) {
      ops.push({ op: 'add', path: `${pointer}/${token}`, value: targetRecord[key] });
    } else {
      diffJson(baseRecord[key], targetRecord[key], `${pointer}/${token}`, ops);
    }
  }
}

/** JSON wire semantics for a leaf value: `undefined` cannot travel (object
 *  context drops the key, array context becomes null), so replace it with
 *  the null the serialized form would carry. */
function jsonSafe(value: unknown): unknown {
  return value === undefined ? null : value;
}

export { applyJsonPatch as applyPatch };

export function buildSnapshotDelta(
  base: { sequence: number; content: SnapshotContent },
  candidate: SnapshotContent,
): { base: number; patch: JsonPatchOp[] } | null {
  const ops: JsonPatchOp[] = [];
  diffJson(base.content, candidate, '', ops);
  if (ops.length === 0 || ops.length > MAX_DELTA_PATCH_OPS) return null;
  // Canonicalize to exactly the bytes the wire will carry: JSON.stringify
  // drops `undefined` values, and an add/replace that lost its value this
  // way would fail server-side validation. If any op is malformed after the
  // round-trip, fall back to a full snapshot rather than emit it.
  const patch = JSON.parse(JSON.stringify(ops)) as JsonPatchOp[];
  if (patch.some((op) => (op.op === 'add' || op.op === 'replace') && op.value === undefined)) {
    return null;
  }
  const patchBytes = JSON.stringify(patch).length;
  const contentBytes = JSON.stringify(candidate).length;
  if (patchBytes >= contentBytes * MAX_DELTA_SIZE_RATIO) return null;
  return { base: base.sequence, patch };
}
