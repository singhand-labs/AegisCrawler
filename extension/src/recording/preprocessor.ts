import type { PageAgentRecording, PageAgentEvent, ScrollEvent, DomSnapshot, DomElementInfo, DomNode, RecordingMeta } from '../../../src/rule-generator/types';

export interface PreprocessedRecording {
  version: string;
  meta: RecordingMeta;
  events: PageAgentEvent[];
  domSnapshots: DomSnapshot[];
}

export interface PreprocessOptions {
  dedupeClickRadiusMs?: number;
  keepSnapshotsPerEvent?: number;
  maxSnapshotNodes?: number;
  maxTextLength?: number;
  maxDomTreeNodes?: number;
  maxDomTreeDepth?: number;
  maxDomTreeTextLength?: number;
}

interface PruneOptions {
  maxNodes: number;
  maxDepth: number;
  maxTextLength: number;
}

export function preprocess(recording: PageAgentRecording, options: PreprocessOptions = {}): PreprocessedRecording {
  const deduped = dedupeEvents(recording.events, options.dedupeClickRadiusMs ?? 500);
  const snapshots = recording.snapshots.map((s) => dehydrateSnapshot(s, options));
  return {
    version: recording.version,
    meta: recording.meta,
    events: deduped,
    domSnapshots: snapshots,
  };
}

function dedupeEvents(events: PageAgentEvent[], radiusMs: number): PageAgentEvent[] {
  const out: PageAgentEvent[] = [];
  let last: PageAgentEvent | null = null;
  for (const e of events) {
    if (
      last &&
      e.type === 'click' &&
      last.type === 'click' &&
      e.index === last.index &&
      e.timestamp - last.timestamp < radiusMs
    ) {
      continue;
    }
    if (last && e.type === 'scroll' && last.type === 'scroll' && e.direction === last.direction) {
      // `amount` is typed as required, but we coerce defensively because
      // runtime events from older recordings may omit it.
      const lastAmount = (last as { amount?: number }).amount ?? 0;
      const currentAmount = (e as { amount?: number }).amount ?? 0;
      const merged: ScrollEvent = { ...last, amount: lastAmount + currentAmount };
      out[out.length - 1] = merged;
      last = merged;
      continue;
    }
    out.push(e);
    last = e;
  }
  return out;
}

function dehydrateSnapshot(snapshot: DomSnapshot, options: PreprocessOptions): DomSnapshot {
  const maxNodes = options.maxSnapshotNodes ?? 500;
  const maxText = options.maxTextLength ?? 200;
  const maxDomTreeNodes = options.maxDomTreeNodes ?? 1000;
  const maxDomTreeDepth = options.maxDomTreeDepth ?? 20;
  const maxDomTreeText = options.maxDomTreeTextLength ?? 100;

  const entries = Object.entries(snapshot.selectorMap).slice(0, maxNodes);
  const selectorMap: Record<number, DomElementInfo> = {};
  for (const [idx, info] of entries) {
    selectorMap[Number(idx)] = {
      index: info.index,
      tagName: info.tagName,
      selector: info.selector,
      stableSelector: info.stableSelector,
      text: info.text ? info.text.slice(0, maxText) : info.text,
      ariaLabel: info.ariaLabel,
      role: info.role,
      placeholder: info.placeholder,
      name: info.name,
      boundingRect: info.boundingRect,
    };
  }

  const domTree = snapshot.domTree
    ? pruneDomTree(snapshot.domTree, {
        maxNodes: maxDomTreeNodes,
        maxDepth: maxDomTreeDepth,
        maxTextLength: maxDomTreeText,
      })
    : undefined;

  return { timestamp: snapshot.timestamp, url: snapshot.url, selectorMap, domTree };
}

function pruneDomTree(
  node: DomNode,
  options: PruneOptions,
  depth = 0,
  state = { remaining: options.maxNodes },
): DomNode | undefined {
  if (state.remaining <= 0) return undefined;
  state.remaining--;

  const copy: DomNode = {
    type: node.type,
    tagName: node.tagName,
    attributes: node.attributes,
    text: node.text ? node.text.slice(0, options.maxTextLength) : node.text,
    rendered: node.rendered,
    sanitization: node.sanitization
      ? {
          ...node.sanitization,
          alteredAttributes: node.sanitization.alteredAttributes
            ? [...node.sanitization.alteredAttributes]
            : undefined,
        }
      : undefined,
    frameOrigin: node.frameOrigin,
    frameSrc: node.frameSrc,
    framePlaceholder: node.framePlaceholder,
    frameMatched: node.frameMatched,
  };
  const markContentOmitted = () => {
    copy.sanitization = {
      ...copy.sanitization,
      markupAltered: true,
      contentOmitted: true,
      alteredAttributes: copy.sanitization?.alteredAttributes
        ? [...copy.sanitization.alteredAttributes]
        : undefined,
    };
  };
  if (node.text && node.text.length > options.maxTextLength) {
    markContentOmitted();
  }

  if (depth < options.maxDepth && node.children && node.children.length > 0) {
    const children: DomNode[] = [];
    for (const child of node.children) {
      const pruned = pruneDomTree(child, options, depth + 1, state);
      if (pruned) children.push(pruned);
      else markContentOmitted();
      if (state.remaining <= 0) break;
    }
    if (children.length > 0) copy.children = children;
    if (children.length < node.children.length) markContentOmitted();
  } else if (node.children && node.children.length > 0) {
    markContentOmitted();
  }

  return copy;
}
