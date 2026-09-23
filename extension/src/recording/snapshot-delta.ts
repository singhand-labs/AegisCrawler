import type { DomSnapshot, SnapshotPatchOp } from '../../../src/rule-generator';

export type { SnapshotPatchOp };

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
 *  additions ascending so a straightforward applier reproduces the target. */
export function diffJson(
  base: unknown,
  target: unknown,
  pointer: string,
  ops: SnapshotPatchOp[],
): void {
  if (base === target) return;
  if (typeof base !== typeof target || !isContainer(base) || !isContainer(target)
    || Array.isArray(base) !== Array.isArray(target)) {
    if (base !== target) ops.push({ op: 'replace', path: pointer, value: target });
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
      ops.push({ op: 'add', path: `${pointer}/${index}`, value: target[index] });
    }
    return;
  }
  const baseRecord = base as Record<string, unknown>;
  const targetRecord = target as Record<string, unknown>;
  for (const key of Object.keys(baseRecord)) {
    if (!(key in targetRecord)) ops.push({ op: 'remove', path: `${pointer}/${escapeToken(key)}` });
  }
  for (const key of Object.keys(targetRecord)) {
    const token = escapeToken(key);
    if (!(key in baseRecord)) {
      ops.push({ op: 'add', path: `${pointer}/${token}`, value: targetRecord[key] });
    } else {
      diffJson(baseRecord[key], targetRecord[key], `${pointer}/${token}`, ops);
    }
  }
}

function parsePointer(pointer: string): string[] {
  if (!pointer.startsWith('/') || pointer.length < 1) {
    throw new Error(`patch path must be a rooted JSON pointer: ${pointer}`);
  }
  return pointer.slice(1).split('/').map((token) => token.replace(/~1/g, '/').replace(/~0/g, '~'));
}

function arrayIndex(token: string, length: number, allowAppend: boolean): number {
  if (!/^(0|[1-9][0-9]*)$/.test(token)) {
    throw new Error(`patch path array index must be a non-negative integer, got "${token}"`);
  }
  const index = Number(token);
  const max = allowAppend ? length : length - 1;
  if (index > max) {
    throw new Error(`patch path array index ${index} out of bounds (length ${length})`);
  }
  return index;
}

/** Applies an RFC 6902 subset patch to a JSON tree in place. Throws on
 *  anything the emitter never produces — unknown ops, unrooted or missing
 *  paths, out-of-bounds indices — so a malformed patch fails closed. */
export function applyPatch(root: unknown, ops: SnapshotPatchOp[]): void {
  for (const op of ops) {
    if (op.op !== 'add' && op.op !== 'replace' && op.op !== 'remove') {
      throw new Error(`unsupported patch op ${String((op as { op?: unknown }).op)}`);
    }
    const tokens = parsePointer(op.path);
    const last = tokens.pop();
    if (last === undefined || last === '') {
      throw new Error(`patch path must not address the document root: ${op.path}`);
    }
    let container: unknown = root;
    for (const token of tokens) {
      if (Array.isArray(container)) {
        container = container[arrayIndex(token, container.length, false)];
      } else if (isContainer(container)) {
        const record = container as Record<string, unknown>;
        if (!(token in record)) throw new Error(`patch path ${op.path} does not exist`);
        container = record[token];
      } else {
        throw new Error(`patch path ${op.path} traverses a non-container`);
      }
    }
    if (Array.isArray(container)) {
      if (op.op === 'add') {
        const index = arrayIndex(last, container.length, true);
        container.splice(index, 0, op.value);
      } else if (op.op === 'replace') {
        container[arrayIndex(last, container.length, false)] = op.value;
      } else {
        container.splice(arrayIndex(last, container.length, false), 1);
      }
    } else if (isContainer(container)) {
      const record = container as Record<string, unknown>;
      if (op.op === 'add') {
        record[last] = op.value;
      } else if (op.op === 'replace') {
        if (!(last in record)) throw new Error(`patch path ${op.path} does not exist`);
        record[last] = op.value;
      } else if (!(last in record)) {
        throw new Error(`patch path ${op.path} does not exist`);
      } else {
        delete record[last];
      }
    } else {
      throw new Error(`patch path ${op.path} addresses a non-container`);
    }
  }
}

/** Diff `candidate` against the base content and return a delta when it is
 *  genuinely worthwhile: bounded op count and clearly smaller than storing
 *  the full payload again. Returns null otherwise (caller stores a full
 *  snapshot, which also becomes the new delta base). */
export function buildSnapshotDelta(
  base: { sequence: number; content: SnapshotContent },
  candidate: SnapshotContent,
): { base: number; patch: SnapshotPatchOp[] } | null {
  const ops: SnapshotPatchOp[] = [];
  diffJson(base.content, candidate, '', ops);
  if (ops.length === 0 || ops.length > MAX_DELTA_PATCH_OPS) return null;
  const patchBytes = JSON.stringify(ops).length;
  const contentBytes = JSON.stringify(candidate).length;
  if (patchBytes >= contentBytes * MAX_DELTA_SIZE_RATIO) return null;
  return { base: base.sequence, patch: ops };
}
