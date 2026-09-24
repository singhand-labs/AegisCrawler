import type { DomSnapshot, PageAgentRecording } from '../types';
import { applyJsonPatch, type JsonPatchOp } from './json-patch';

/**
 * Client-side mirror of the server's ingest-time expansion: resolves
 * reference snapshots (identical content → `ref`) and snapshot deltas
 * (near-identical content → `base` + `patch`) in place so consumers that
 * never touch the server — the extension's rule/DSL generation and intent
 * prediction — see full snapshots exactly like every server-side consumer.
 *
 * Semantics match server/internal/recording ExpandSnapshotReferences:
 * a valid reference names the sequence of an EARLIER snapshot that carries
 * content; a valid delta names an earlier content snapshot and carries a
 * bounded RFC 6902 subset patch, which is applied to a deep copy of the
 * base content; an expanded delta itself becomes resolvable content, so a
 * later reference may name a delta's sequence. Anything malformed throws
 * (fail closed) instead of silently dropping per-action evidence.
 */
export function expandSnapshotReferences(recording: PageAgentRecording): void {
  if (!recording || recording.version !== '2.0.0' || !Array.isArray(recording.snapshots)) return;
  const content = new Map<number, DomSnapshot>();
  for (let index = 0; index < recording.snapshots.length; index += 1) {
    const snapshot = recording.snapshots[index];
    const isRef = snapshot?.ref !== undefined;
    const isDelta = snapshot?.patch !== undefined;
    if (!snapshot || (!isRef && !isDelta)) {
      if (snapshot?.domTree !== undefined && snapshot.sequence !== undefined) {
        content.set(snapshot.sequence, snapshot);
      }
      continue;
    }
    if (isRef && isDelta) {
      throw new Error(`snapshots[${index}] carries both ref and patch`);
    }
    const sequence = requireSequence(snapshot.sequence, `snapshots[${index}]`);
    if (isDelta) {
      expandDelta(content, snapshot, index);
      content.set(sequence, snapshot);
      continue;
    }
    const ref = requireSequence(snapshot.ref, `snapshots[${index}].ref`);
    const target = content.get(ref);
    if (!target) {
      throw new Error(`snapshots[${index}].ref ${ref} does not name an earlier content snapshot`);
    }
    for (const field of ['domTree', 'selectorMap', 'capture'] as const) {
      if (target[field] !== undefined) {
        (snapshot as unknown as Record<string, unknown>)[field] = deepClone(target[field]);
      }
    }
    delete snapshot.ref;
  }
}

function expandDelta(content: Map<number, DomSnapshot>, snapshot: DomSnapshot, index: number): void {
  const base = requireSequence(snapshot.base, `snapshots[${index}].base`);
  const target = content.get(base);
  if (!target) {
    throw new Error(`snapshots[${index}].base ${base} does not name an earlier content snapshot`);
  }
  const patched: Record<string, unknown> = {};
  for (const field of ['selectorMap', 'domTree', 'capture'] as const) {
    if (target[field] !== undefined) patched[field] = deepClone(target[field]);
  }
  applyJsonPatch(patched, snapshot.patch as JsonPatchOp[]);
  for (const [field, value] of Object.entries(patched)) {
    (snapshot as unknown as Record<string, unknown>)[field] = value;
  }
  delete snapshot.base;
  delete snapshot.patch;
}

function requireSequence(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) {
    throw new Error(`${label} must be a non-negative integer sequence number`);
  }
  return value;
}

function deepClone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}
