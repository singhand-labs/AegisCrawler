import type {
  DomNode,
  DomAttribute,
  DomSanitizationEvidence,
} from '../../../src/rule-generator/types';

export const REDACTED_VALUE = '[REDACTED]';
export const REMOVED_URL_VALUE = '[REMOVED_URL]';

export interface SerializeOptions {
  maxDepth?: number;
  maxNodes?: number;
  maxTextLength?: number;
  allowedAttrs?: Set<string>;
  /** V2 uses strict limits so a capture stops instead of silently truncating. */
  strictLimits?: boolean;
}

export interface SerializationReport {
  nodeCount: number;
  redactionCount: number;
  removedNodeCount: number;
  truncated: boolean;
}

export interface SerializedDom {
  domTree: DomNode | null;
  report: SerializationReport;
}

export class SemanticDomLimitError extends Error {
  constructor(readonly limit: 'nodes' | 'depth' | 'text') {
    super(`semantic DOM ${limit} limit exceeded`);
    this.name = 'SemanticDomLimitError';
  }
}

const DEFAULT_ALLOWED_ATTRS = new Set([
  'id',
  'class',
  'data-testid',
  'data-qa',
  'data-automation-id',
  'role',
  'aria-label',
  'aria-labelledby',
  'aria-describedby',
  'aria-expanded',
  'aria-checked',
  'aria-selected',
  'aria-disabled',
  'name',
  'autocomplete',
  'placeholder',
  'type',
  'href',
  'src',
  'alt',
  'title',
  'for',
  'value',
  'checked',
  'selected',
  'disabled',
  'readonly',
  'required',
  'multiple',
  'hidden',
  'open',
  'contenteditable',
  'action',
  'method',
]);

const SKIP_TAGS = new Set(['script', 'style', 'noscript', 'link', 'meta', 'template']);
const URL_ATTRIBUTES = new Set(['href', 'src', 'action']);
const MAX_PROVENANCE_ATTRIBUTE_NAMES = 64;
const SENSITIVE_NAME = /(password|passwd|pwd|secret|token|api[-_]?key|auth(?:orization)?|cookie|credential|user[-_]?name|login|credit[-_]?card|card[-_]?number|cvv|cvc|ssn)/i;
const SENSITIVE_AUTOCOMPLETE = /(?:current|new)-password|one-time-code|cc-(?:number|csc)|username/i;
const SENSITIVE_TEXT_PATTERNS = [
  /(?:password|passwd|pwd|secret|token|api[-_]?key|authorization|cookie)\s*[:=]\s*["']?[^"'\s<>;&]+["']?/gi,
  /bearer\s+[a-z0-9._~+/=-]+/gi,
  /\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g,
  // M-11: match credit card numbers both as contiguous digits and with
  // spaces/dashes between 4-digit groups (e.g., 4111 1111 1111 1111).
  /\b\d{16,19}\b/g,
  /\b\d{4}[-\s]\d{4}[-\s]\d{4}[-\s]\d{1,4}\b/g,
  /\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/g,
];

interface State {
  remainingNodes: number;
  maxDepth: number;
  maxTextLength: number;
  allowedAttrs: Set<string>;
  strictLimits: boolean;
  report: SerializationReport;
}

export function serializeDom(root: Element | null, options: SerializeOptions = {}): DomNode | null {
  return serializeDomWithReport(root, options).domTree;
}

export function serializeDomWithReport(root: Element | null, options: SerializeOptions = {}): SerializedDom {
  const report: SerializationReport = {
    nodeCount: 0,
    redactionCount: 0,
    removedNodeCount: 0,
    truncated: false,
  };
  if (!root) return { domTree: null, report };
  const state: State = {
    remainingNodes: options.maxNodes ?? Number.POSITIVE_INFINITY,
    maxDepth: options.maxDepth ?? Number.POSITIVE_INFINITY,
    maxTextLength: options.maxTextLength ?? Number.POSITIVE_INFINITY,
    allowedAttrs: options.allowedAttrs ?? DEFAULT_ALLOWED_ATTRS,
    strictLimits: options.strictLimits ?? false,
    report,
  };
  return { domTree: serializeElement(root, 0, state, renderedAncestors(root)), report };
}

export function redactSensitiveText(value: string): { value: string; redacted: boolean } {
  let output = value;
  for (const pattern of SENSITIVE_TEXT_PATTERNS) {
    pattern.lastIndex = 0;
    output = output.replace(pattern, REDACTED_VALUE);
  }
  return { value: output, redacted: output !== value };
}

export function sanitizeRecordingUrl(value: string): string {
  const report: SerializationReport = { nodeCount: 0, redactionCount: 0, removedNodeCount: 0, truncated: false };
  const state: State = {
    remainingNodes: Number.POSITIVE_INFINITY,
    maxDepth: Number.POSITIVE_INFINITY,
    maxTextLength: Number.POSITIVE_INFINITY,
    allowedAttrs: DEFAULT_ALLOWED_ATTRS,
    strictLimits: false,
    report,
  };
  return sanitizeUrl(value, state);
}

export function isSensitiveElement(el: Element): boolean {
  const type = el.getAttribute('type')?.toLowerCase();
  if (type === 'password' || type === 'hidden') return true;
  const identity = [el.getAttribute('name'), el.getAttribute('id'), el.getAttribute('aria-label')]
    .filter(Boolean)
    .join(' ');
  if (SENSITIVE_NAME.test(identity)) return true;
  return SENSITIVE_AUTOCOMPLETE.test(el.getAttribute('autocomplete') ?? '');
}

/**
 * Evaluate one element's non-sensitive rendered-state evidence. Geometry is
 * deliberately excluded: off-screen elements and elements awaiting layout can
 * still be rendered, while live replay applies the stronger bounding-box
 * predicate.
 */
function isElementSelfRendered(el: Element): boolean {
  if (el.hasAttribute('hidden')) return false;
  if (el.getAttribute('aria-hidden')?.trim().toLowerCase() === 'true') return false;
  if (
    el.tagName.toLowerCase() === 'input'
    && el.getAttribute('type')?.trim().toLowerCase() === 'hidden'
  ) {
    return false;
  }
  const view = el.ownerDocument.defaultView;
  if (view && typeof view.getComputedStyle === 'function') {
    let style: CSSStyleDeclaration;
    try {
      style = view.getComputedStyle(el);
    } catch {
      return false;
    }
    if (
      style.display === 'none'
      || style.visibility === 'hidden'
      || style.visibility === 'collapse'
      || style.contentVisibility === 'hidden'
      || style.opacity === '0'
    ) {
      return false;
    }
  }
  return true;
}

function renderedAncestors(el: Element): boolean {
  let current: Element | null = el.parentElement;
  if (!current) {
    const root = el.getRootNode();
    current = root && 'host' in root ? ((root as ShadowRoot).host ?? null) : null;
  }
  while (current) {
    if (!isElementSelfRendered(current)) return false;
    if (current.parentElement) {
      current = current.parentElement;
      continue;
    }
    const root = current.getRootNode();
    current = root && 'host' in root ? ((root as ShadowRoot).host ?? null) : null;
  }
  return true;
}

function consumeNode(state: State): boolean {
  if (state.remainingNodes <= 0) {
    state.report.truncated = true;
    if (state.strictLimits) throw new SemanticDomLimitError('nodes');
    return false;
  }
  state.remainingNodes--;
  state.report.nodeCount++;
  return true;
}

function mergeSanitization(
  node: DomNode,
  evidence: DomSanitizationEvidence,
): void {
  const current = node.sanitization ?? {};
  if (evidence.markupAltered) current.markupAltered = true;
  if (evidence.contentOmitted) {
    current.markupAltered = true;
    current.contentOmitted = true;
  }
  if (evidence.alteredAttributes?.length) {
    current.markupAltered = true;
    const names = new Set([
      ...(current.alteredAttributes ?? []),
      ...evidence.alteredAttributes.map((name) => name.toLowerCase()),
    ]);
    current.alteredAttributes = names.has('*') || names.size > MAX_PROVENANCE_ATTRIBUTE_NAMES
      ? ['*']
      : Array.from(names).sort();
  }
  if (
    current.markupAltered
    || current.contentOmitted
    || current.alteredAttributes?.length
  ) {
    node.sanitization = current;
  }
}

function recordAttributeSanitization(
  node: DomNode,
  el: Element,
  attributes: DomAttribute[],
): void {
  const original = new Map(
    Array.from(el.attributes).map((attribute) => [
      attribute.name.toLowerCase(),
      attribute.value,
    ]),
  );
  const recorded = new Map(attributes.map((attribute) => [attribute.name, attribute.value]));
  let markupAltered = false;
  const alteredAttributes: string[] = [];

  for (const [name, value] of original) {
    if (!recorded.has(name)) {
      markupAltered = true;
      continue;
    }
    if (recorded.get(name) !== value) {
      markupAltered = true;
      alteredAttributes.push(name);
    }
  }
  for (const name of recorded.keys()) {
    if (!original.has(name)) {
      markupAltered = true;
      alteredAttributes.push(name);
    }
  }
  if (markupAltered) {
    mergeSanitization(node, { markupAltered: true, alteredAttributes });
  }
}

function recordOmittedChild(node: DomNode, child: Node): void {
  if (child.nodeType === Node.COMMENT_NODE) {
    mergeSanitization(node, { markupAltered: true });
    return;
  }
  if (child.nodeType === Node.TEXT_NODE) {
    const contentOmitted = Boolean(child.textContent?.replace(/\s+/g, ' ').trim());
    mergeSanitization(node, contentOmitted
      ? { markupAltered: true, contentOmitted: true }
      : { markupAltered: true });
    return;
  }
  if (child.nodeType === Node.ELEMENT_NODE) {
    mergeSanitization(node, { markupAltered: true, contentOmitted: true });
    return;
  }
  const contentOmitted = Boolean(child.textContent?.replace(/\s+/g, ' ').trim());
  mergeSanitization(node, contentOmitted
    ? { markupAltered: true, contentOmitted: true }
    : { markupAltered: true });
}

function serializeElement(el: Element, depth: number, state: State, parentRendered: boolean): DomNode | null {
  const tagName = el.tagName.toLowerCase();
  if (SKIP_TAGS.has(tagName)) {
    state.report.removedNodeCount++;
    return null;
  }
  if (!consumeNode(state)) return null;

  const sensitive = isSensitiveElement(el);
  const attributes: DomAttribute[] = [];
  for (const attr of Array.from(el.attributes)) {
    const name = attr.name.toLowerCase();
    if (!state.allowedAttrs.has(name) && !name.startsWith('aria-')) continue;
    let value = attr.value;
    if (name === 'class') value = sanitizeClassValue(value);
    if (URL_ATTRIBUTES.has(name)) value = sanitizeUrl(value, state);
    else if (name === 'value' && sensitive) value = redactValue(value, state);
    else value = sanitizeText(value, state, sensitive && (name === 'placeholder' || name === 'title'));
    if (value !== '' || name !== 'class') attributes.push({ name, value });
  }

  preserveLiveFormState(el, attributes, sensitive, state);

  const rendered = parentRendered && isElementSelfRendered(el);
  const node: DomNode = {
    type: 'element',
    tagName,
    attributes,
  };
  recordAttributeSanitization(node, el, attributes);
  // Serialize only negative evidence so a large semantic snapshot does not pay
  // for one extra boolean on every ordinary rendered node.
  if (!rendered) node.rendered = false;

  // Each permitted frame is captured independently by its all-frames content
  // script and merged by the background service worker.
  const childNodes = Array.from(el.childNodes);
  if (tagName === 'iframe') {
    // Fallback light-DOM children remain part of the iframe element's live
    // outerHTML even though captured frame documents are isolated below.
    // Record their omission before returning the placeholder.
    for (const child of childNodes) recordOmittedChild(node, child);
    node.framePlaceholder = true;
    node.frameStatus = 'unavailable';
    node.frameSrc = attributes.find((a) => a.name === 'src')?.value ?? '';
    return node;
  }

  if (depth >= state.maxDepth && childNodes.some(isSemanticNode)) {
    state.report.truncated = true;
    if (state.strictLimits) throw new SemanticDomLimitError('depth');
    for (const child of childNodes) recordOmittedChild(node, child);
    return node;
  }

  const children: DomNode[] = [];
  for (const child of childNodes) {
    const serialized = serializeNode(child, depth + 1, state, sensitive, rendered);
    if (serialized) children.push(serialized);
    else recordOmittedChild(node, child);
  }
  if (children.length > 0) node.children = children;
  return node;
}

function serializeNode(
  node: Node,
  depth: number,
  state: State,
  forceRedact: boolean,
  parentRendered: boolean,
): DomNode | null {
  if (node.nodeType === Node.TEXT_NODE) {
    const rawText = node.textContent || '';
    const text = rawText.replace(/\s+/g, ' ').trim();
    if (!text) return null;
    if (!consumeNode(state)) return null;
    const sanitizedText = sanitizeText(text, state, forceRedact);
    const result: DomNode = {
      type: 'text',
      text: sanitizedText,
    };
    if (text !== rawText) {
      mergeSanitization(result, { markupAltered: true });
    }
    if (sanitizedText !== text) {
      mergeSanitization(result, { markupAltered: true, contentOmitted: true });
    }
    return result;
  }
  if (node.nodeType === Node.ELEMENT_NODE) {
    return serializeElement(node as Element, depth, state, parentRendered);
  }
  if (node.nodeType === Node.COMMENT_NODE) state.report.removedNodeCount++;
  return null;
}

function isSemanticNode(node: Node): boolean {
  if (node.nodeType === Node.TEXT_NODE) return Boolean(node.textContent?.trim());
  if (node.nodeType !== Node.ELEMENT_NODE) return false;
  return !SKIP_TAGS.has((node as Element).tagName.toLowerCase());
}

function sanitizeText(value: string, state: State, forceRedact = false): string {
  let output = value;
  if (forceRedact && output !== '') {
    output = REDACTED_VALUE;
    state.report.redactionCount++;
  } else {
    const redacted = redactSensitiveText(output);
    output = redacted.value;
    if (redacted.redacted) state.report.redactionCount++;
  }
  if (output.length > state.maxTextLength) {
    state.report.truncated = true;
    if (state.strictLimits) throw new SemanticDomLimitError('text');
    output = output.slice(0, state.maxTextLength);
  }
  return output;
}

function redactValue(value: string, state: State): string {
  if (value === '') return '';
  state.report.redactionCount++;
  return REDACTED_VALUE;
}

function sanitizeUrl(value: string, state: State): string {
  if (/^(?:data|blob|javascript):/i.test(value.trim())) {
    state.report.redactionCount++;
    return REMOVED_URL_VALUE;
  }
  let output = value.replace(/:\/\/[^/@\s]+:[^/@\s]+@/g, `://${REDACTED_VALUE}@`);
  output = output.replace(
    // M-10: expanded sensitive query-param allowlist to cover OAuth/OIDC
    // callback params (access_token, refresh_token, code, state) and
    // session identifiers (sessionid, sid, jwt) commonly leaked in URLs.
    /([?&](?:password|passwd|pwd|secret|token|api[-_]?key|authorization|cookie|credential|access[-_]?token|refresh[-_]?token|session(?:id|key)?|sid|jwt|code|state|otp|mfa[-_]?code)=)[^&#]*/gi,
    `$1${REDACTED_VALUE}`,
  );
  const redacted = redactSensitiveText(output);
  if (redacted.redacted || output !== value) state.report.redactionCount++;
  return redacted.value;
}

function sanitizeClassValue(value: string): string {
  return value
    .split(/\s+/)
    .filter((token) => token && token.length <= 80 && !/[a-f0-9]{12,}/i.test(token) && !/^css-[a-z0-9]{6,}$/i.test(token))
    .join(' ');
}

function preserveLiveFormState(
  el: Element,
  attributes: DomAttribute[],
  sensitive: boolean,
  state: State,
): void {
  const set = (name: string, value: string) => {
    const existing = attributes.find((attribute) => attribute.name === name);
    if (existing) existing.value = value;
    else attributes.push({ name, value });
  };
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    set('value', sensitive ? redactValue(el.value, state) : sanitizeText(el.value, state));
  }
  if (el instanceof HTMLInputElement && el.checked) set('checked', 'true');
  if (el instanceof HTMLOptionElement && el.selected) set('selected', 'true');
  if ('disabled' in el && (el as HTMLButtonElement).disabled) set('disabled', 'true');
}

export function estimateNodeCount(node: DomNode): number {
  let count = 1;
  if (node.children) {
    for (const child of node.children) count += estimateNodeCount(child);
  }
  return count;
}
