import type { DomNode, FrameCaptureReport } from '../../../src/rule-generator/types';
import {
  sanitizeRecordingUrl,
  serializeDomWithReport,
  type SerializationReport,
} from './dom-serializer';

export interface FrameInfo {
  frameId: number;
  parentFrameId: number;
  url: string;
}

export interface FrameTreeNode {
  frameId: number;
  parentFrameId?: number;
  url: string;
  domTree: DomNode;
  children: FrameTreeNode[];
  status?: 'captured' | 'unavailable' | 'error';
  error?: string;
  serialization?: SerializationReport;
}

export interface AggregateOptions {
  maxNodes?: number;
  maxDepth?: number;
  maxTextLength?: number;
  strictLimits?: boolean;
}

export function buildFrameTree(frames: FrameInfo[]): FrameTreeNode {
  const root = frames.find((f) => f.parentFrameId === -1 || f.parentFrameId === f.frameId);
  if (!root) {
    throw new Error('Frame list has no root frame');
  }

  function build(parentFrameId: number): FrameTreeNode[] {
    return frames
      .filter((f) => f.parentFrameId === parentFrameId && f.frameId !== parentFrameId)
      .map((f) => ({
        frameId: f.frameId,
        parentFrameId: f.parentFrameId,
        url: sanitizeRecordingUrl(f.url),
        domTree: { type: 'element', tagName: 'html' },
        children: build(f.frameId),
        status: 'unavailable' as const,
      }));
  }

  return {
    frameId: root.frameId,
    parentFrameId: root.parentFrameId,
    url: sanitizeRecordingUrl(root.url),
    domTree: { type: 'element', tagName: 'html' },
    children: build(root.frameId),
    status: 'unavailable',
  };
}

export function mergeFrameTreeIntoDom(domTree: DomNode, frameTree: FrameTreeNode): DomNode {
  const copy = cloneDomNode(domTree);
  mergeFrameIntoNode(copy, frameTree, new Set());
  return copy;
}

function mergeFrameIntoNode(node: DomNode, frameTree: FrameTreeNode, usedFrames: Set<number>): void {
  if (node.tagName === 'iframe' && node.framePlaceholder) {
    const matched = findMatchingFrame(node, frameTree, usedFrames);
    if (matched) {
      usedFrames.add(matched.frameId);
      node.frameOrigin = inferFrameOrigin(node, frameTree.url);
      const matchedStatus = matched.status ?? 'captured';
      node.frameStatus = matchedStatus;
      node.frameError = matched.error;
      node.frameMatched = matchedStatus === 'captured';
      if (matchedStatus === 'captured') {
        node.children = [mergeFrameTreeIntoDom(matched.domTree, matched)];
      }
      delete node.framePlaceholder;
      return;
    }
    node.frameMatched = false;
    node.frameStatus = 'unavailable';
    node.frameError = 'frame-not-matched';
    return;
  }

  if (node.children) {
    for (const child of node.children) {
      mergeFrameIntoNode(child, frameTree, usedFrames);
    }
  }
}

function findMatchingFrame(placeholder: DomNode, frameTree: FrameTreeNode, usedFrames: Set<number>): FrameTreeNode | null {
  let best: FrameTreeNode | null = null;
  let bestScore = 0;
  for (const candidate of frameTree.children) {
    if (usedFrames.has(candidate.frameId)) {
      continue;
    }
    const score = scoreIframeMatch(placeholder, candidate, frameTree.url);
    if (score > bestScore) {
      bestScore = score;
      best = candidate;
    }
  }
  // Require a minimum confidence before matching; otherwise leave the
  // placeholder unmatched so callers can degrade gracefully.
  return bestScore >= 20 ? best : null;
}

function scoreIframeMatch(placeholder: DomNode, frame: FrameTreeNode, baseUrl: string): number {
  const srcAttr = placeholder.attributes?.find((a) => a.name === 'src')?.value || '';
  try {
    const placeholderUrl = new URL(srcAttr, baseUrl);
    const frameUrl = new URL(frame.url);
    if (placeholderUrl.origin !== frameUrl.origin) {
      return -1;
    }
    const p = placeholderUrl.pathname;
    const f = frameUrl.pathname;
    if (p === f) {
      return 100;
    }
    if (p.startsWith(f + '/')) {
      return 50;
    }
    if (f.startsWith(p + '/')) {
      return 25;
    }
    return 0;
  } catch {
    return 0;
  }
}

function inferFrameOrigin(placeholder: DomNode, parentFrameUrl: string): 'same-origin' | 'cross-origin' {
  const srcAttr = placeholder.attributes?.find((a) => a.name === 'src')?.value || '';
  try {
    const placeholderOrigin = new URL(srcAttr, parentFrameUrl).origin;
    const parentOrigin = new URL(parentFrameUrl).origin;
    return placeholderOrigin === parentOrigin ? 'same-origin' : 'cross-origin';
  } catch {
    return 'cross-origin';
  }
}

function cloneDomNode(node: DomNode): DomNode {
  const copy: DomNode = {
    type: node.type,
    tagName: node.tagName,
    attributes: node.attributes ? [...node.attributes] : undefined,
    text: node.text,
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
    frameStatus: node.frameStatus,
    frameError: node.frameError,
  };
  if (node.children) {
    copy.children = node.children.map(cloneDomNode);
  }
  return copy;
}

function errorNode(code: string): DomNode {
  return {
    type: 'element',
    tagName: 'html',
    attributes: [
      { name: 'data-capture-status', value: 'unavailable' },
      { name: 'data-capture-error', value: code },
    ],
  };
}

function classifyFrameError(error: unknown): { status: 'unavailable' | 'error'; code: string } {
  const message = error instanceof Error ? error.message : String(error);
  if (/receiving end|could not establish|no tab with id|frame.*not found/i.test(message)) {
    return { status: 'unavailable', code: 'content-script-unavailable' };
  }
  if (/timeout/i.test(message)) return { status: 'error', code: 'capture-timeout' };
  return { status: 'error', code: 'capture-failed' };
}

export function collectFrameReports(frameTree: FrameTreeNode | null): FrameCaptureReport[] {
  if (!frameTree) return [];
  const reports: FrameCaptureReport[] = [];
  const visit = (node: FrameTreeNode) => {
    reports.push({
      frameId: node.frameId,
      parentFrameId: node.parentFrameId ?? -1,
      url: node.url,
      status: node.status ?? 'captured',
      error: node.error,
    });
    node.children.forEach(visit);
  };
  visit(frameTree);
  return reports;
}

export async function aggregateFrameDom(
  tabId: number,
  options: AggregateOptions = {},
): Promise<FrameTreeNode | null> {
  const chromeApi = (globalThis as Record<string, unknown>).chrome as
    | {
        webNavigation?: { getAllFrames: (d: { tabId: number }) => Promise<FrameInfo[]> };
        tabs?: { sendMessage: (tabId: number, message: unknown, options?: { frameId?: number }) => Promise<unknown> };
      }
    | undefined;

  if (!chromeApi?.webNavigation?.getAllFrames || !chromeApi?.tabs?.sendMessage) {
    return null;
  }

  const frames = await chromeApi.webNavigation.getAllFrames({ tabId });
  if (!frames || frames.length === 0) {
    return null;
  }

  const tree = buildFrameTree(frames);

  async function capture(node: FrameTreeNode): Promise<void> {
    try {
      const response = (await chromeApi!.tabs!.sendMessage(
        tabId,
        { action: 'CAPTURE_DOM', options },
        { frameId: node.frameId },
      )) as { domTree?: DomNode; serialization?: SerializationReport };
      if (response?.domTree) {
        node.domTree = response.domTree;
        node.serialization = response.serialization;
        node.status = 'captured';
      } else {
        node.domTree = errorNode('empty-capture');
        node.status = 'error';
        node.error = 'empty-capture';
      }
    } catch (err) {
      const failure = classifyFrameError(err);
      node.domTree = errorNode(failure.code);
      node.status = failure.status;
      node.error = failure.code;
    }
    await Promise.all(node.children.map(capture));
  }

  await capture(tree);
  return tree;
}

export function captureLocalDom(options: AggregateOptions = {}): DomNode | null {
  return captureLocalDomWithReport(options).domTree;
}

export function captureLocalDomWithReport(options: AggregateOptions = {}) {
  return serializeDomWithReport(document.documentElement, options);
}

export async function requestAggregateFromBackground(options: AggregateOptions = {}): Promise<FrameTreeNode | null> {
  const chromeRuntime = (globalThis as Record<string, unknown>).chrome as
    | { runtime?: { sendMessage: (m: unknown) => Promise<unknown> } }
    | undefined;
  if (!chromeRuntime?.runtime?.sendMessage) {
    return null;
  }
  const response = (await chromeRuntime.runtime.sendMessage({ action: 'AGGREGATE_DOM', options })) as {
    frameTree?: FrameTreeNode;
  };
  return response?.frameTree ?? null;
}
