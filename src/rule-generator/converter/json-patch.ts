/** RFC 6902 subset (add / replace / remove with rooted JSON pointers).
 *  Shared by the extension's snapshot-delta emitter and the recording
 *  consumers that expand reference/delta snapshots client-side — one
 *  implementation keeps both sides of the wire agreeing with the server's
 *  Go applier. */

export interface JsonPatchOp {
  op: 'add' | 'replace' | 'remove';
  path: string;
  value?: unknown;
}

function isContainer(value: unknown): value is Record<string, unknown> | unknown[] {
  return typeof value === 'object' && value !== null;
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
 *  paths, valueless add/replace, out-of-bounds indices — so a malformed
 *  patch fails closed. */
export function applyJsonPatch(root: unknown, ops: JsonPatchOp[]): void {
  for (const op of ops) {
    if (op.op !== 'add' && op.op !== 'replace' && op.op !== 'remove') {
      throw new Error(`unsupported patch op ${String((op as { op?: unknown }).op)}`);
    }
    if ((op.op === 'add' || op.op === 'replace') && op.value === undefined) {
      throw new Error(`patch op ${op.op} ${op.path} requires a value`);
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
