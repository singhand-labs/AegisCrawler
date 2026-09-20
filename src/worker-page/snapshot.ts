/**
 * Bounded snapshot sanitizer for the worker page runtime. Adapts the
 * safeReplaySnapshot idea from extension/src/intent/replay-transport.ts:
 * BrowserEnvironment snapshots contain literal outerHTML, which must never
 * cross the host boundary unbounded. The DOM is re-serialized with strict
 * node/depth/text budgets and unsafe or non-semantic subtrees are dropped.
 */

import type { Snapshot } from '../scriptcat-engine/types';

export const MAX_WORKER_SNAPSHOT_BYTES = 384 * 1024;
export const MAX_WORKER_SNAPSHOT_NODES = 1500;
export const MAX_WORKER_SNAPSHOT_DEPTH = 40;
export const MAX_WORKER_SNAPSHOT_TEXT_LENGTH = 1000;

const SKIP_TAGS = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'IFRAME', 'OBJECT', 'EMBED', 'SVG', 'CANVAS']);

interface SerializeReport {
  nodes: number;
  truncated: boolean;
  maxDepthReached: boolean;
}

function jsonBytes(value: unknown): number {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    return Number.POSITIVE_INFINITY;
  }
}

function serializeNode(node: Node, depth: number, report: SerializeReport): unknown {
  if (report.nodes >= MAX_WORKER_SNAPSHOT_NODES) {
    report.truncated = true;
    return null;
  }
  if (node.nodeType === 3 /* TEXT_NODE */) {
    const raw = (node.textContent ?? '').replace(/\s+/g, ' ').trim();
    if (!raw) return null;
    report.nodes += 1;
    return {
      text: raw.length > MAX_WORKER_SNAPSHOT_TEXT_LENGTH
        ? `${raw.slice(0, MAX_WORKER_SNAPSHOT_TEXT_LENGTH)}…`
        : raw,
    };
  }
  if (node.nodeType !== 1 /* ELEMENT_NODE */) return null;
  const el = node as Element;
  if (SKIP_TAGS.has(el.tagName)) return null;
  if (depth >= MAX_WORKER_SNAPSHOT_DEPTH) {
    report.maxDepthReached = true;
    return null;
  }
  report.nodes += 1;
  const children: unknown[] = [];
  for (const child of Array.from(el.childNodes)) {
    const serialized = serializeNode(child, depth + 1, report);
    if (serialized !== null) children.push(serialized);
    if (report.nodes >= MAX_WORKER_SNAPSHOT_NODES) break;
  }
  const out: Record<string, unknown> = { tag: el.tagName.toLowerCase() };
  const id = el.getAttribute('id');
  if (id) out.id = id;
  const className = (el.getAttribute('class') ?? '').trim();
  if (className) out.class = className.slice(0, 200);
  const ariaLabel = el.getAttribute('aria-label');
  if (ariaLabel) out.ariaLabel = ariaLabel.slice(0, 200);
  if (children.length > 0) out.children = children;
  return out;
}

/**
 * Replace a raw BrowserEnvironment snapshot with a bounded semantic
 * re-serialization. Always returns a well-formed snapshot; serialization
 * failure degrades to an explicit omission marker instead of raw page data.
 */
export function sanitizeWorkerSnapshot(snapshot: Snapshot, root: Element): Snapshot {
  try {
    const report: SerializeReport = { nodes: 0, truncated: false, maxDepthReached: false };
    const domTree = serializeNode(root, 0, report);
    const artifact = {
      format: 'semantic-dom-v1',
      originalType: snapshot.type,
      domTree,
      capture: report,
    };
    const originalBytes = jsonBytes(artifact);
    const data = originalBytes <= MAX_WORKER_SNAPSHOT_BYTES
      ? JSON.stringify(artifact)
      : JSON.stringify({
          format: 'semantic-dom-v1',
          originalType: snapshot.type,
          domOmitted: true,
          originalBytes,
          capture: { ...report, truncated: true },
          reason: 'sanitized worker snapshot exceeded the browser artifact budget',
        });
    return { name: snapshot.name, type: 'dom', data };
  } catch {
    return {
      name: snapshot.name,
      type: 'dom',
      data: JSON.stringify({
        format: 'semantic-dom-v1',
        originalType: snapshot.type,
        domOmitted: true,
        reason: 'sanitized worker snapshot serialization failed',
      }),
    };
  }
}
