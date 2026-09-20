import type { Target } from './types';
import { sleep } from './utils';
import { getImplicitRole } from '../rule-engine/aria-roles';

type Root = Document | Element | ShadowRoot;

/**
 * Thrown when an element matches a Target by one strategy (selector, xpath,
 * text) but fails verification against a disambiguating field declared on the
 * same Target (ariaLabel, role). The chain advances past the failed branch; if
 * NO branch yields a verified match, this error surfaces so callers can
 * distinguish silent wrong-element failures from genuine element-not-present.
 *
 * Classified as `ElementVerificationFailed` by `classifyError` (prefix match)
 * and intentionally excluded from `isRecoverable`'s retry set — retrying will
 * not fix a structural wrong-element match.
 */
export class ElementVerificationError extends Error {
  constructor(target: Target) {
    super(`ElementVerificationFailed: ${JSON.stringify(target)}`);
    this.name = 'ElementVerificationFailed';
  }
}

/** Read the `type` attribute of an `<input>` (used for implicit role lookup). */
function inputTypeOf(el: Element): string | undefined {
  if (el.tagName.toLowerCase() !== 'input') return undefined;
  return (el as HTMLInputElement).getAttribute('type') ?? undefined;
}

function isNonBlank(value: string | undefined): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

function normalizedTargetText(value: string): string {
  return value.trim();
}

function matchesDeclaredText(el: Element, target: Target): boolean {
  return !isNonBlank(target.text)
    || normalizedTargetText(el.textContent ?? '') === normalizedTargetText(target.text);
}

/**
 * Verify a matched element against the disambiguating fields of a Target.
 * Returns true when the element is consistent with every `text` / `ariaLabel`
 * / `role` declared on the Target. Tautologically true for branches that
 * matched by those same attributes; load-bearing for selector / xpath
 * branches, where a broad or drifted match can otherwise outrank the recorded
 * semantic identity.
 */
function verifyElement(el: Element, target: Target): boolean {
  if (!matchesDeclaredText(el, target)) {
    return false;
  }
  if (isNonBlank(target.ariaLabel) && el.getAttribute('aria-label') !== target.ariaLabel) {
    return false;
  }
  if (isNonBlank(target.role)) {
    const effective = el.getAttribute('role') ?? getImplicitRole(el.tagName, inputTypeOf(el)) ?? null;
    if (effective !== target.role) return false;
  }
  return true;
}

/**
 * Return whether an element is actually renderable to a user.
 *
 * Geometry alone is insufficient: a descendant can retain a mocked or stale
 * bounding box while an ancestor is hidden. Walk through light-DOM parents and
 * shadow hosts so `hidden`, `aria-hidden`, hidden inputs, and computed
 * display/visibility state all fail closed before accepting the candidate.
 */
export function isElementVisible(el: Element): boolean {
  let current: Element | null = el;
  let subjectDisplay = '';
  while (current) {
    if (current.hasAttribute('hidden')) return false;
    if (current.getAttribute('aria-hidden')?.trim().toLowerCase() === 'true') return false;
    if (
      current.tagName.toLowerCase() === 'input'
      && current.getAttribute('type')?.trim().toLowerCase() === 'hidden'
    ) {
      return false;
    }

    const view = current.ownerDocument.defaultView;
    if (view && typeof view.getComputedStyle === 'function') {
      let style: CSSStyleDeclaration;
      try {
        style = view.getComputedStyle(current);
      } catch {
        return false;
      }
      if (current === el) subjectDisplay = style.display;
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

    if (current.parentElement) {
      current = current.parentElement;
      continue;
    }
    const treeRoot: Node = current.getRootNode();
    const shadowHost: Element | null = 'host' in treeRoot
      ? (treeRoot as ShadowRoot).host
      : null;
    current = shadowHost?.nodeType === 1 ? shadowHost : null;
  }

  try {
    const rect = el.getBoundingClientRect();
    if (rect.width > 0 && rect.height > 0) return true;
  } catch {
    return false;
  }

  // `display: contents` intentionally gives the semantic container no box;
  // its rendered children participate directly in the parent's layout. Treat
  // that container as visible only when an actual descendant independently
  // passes the same hidden/style/geometry checks. Ordinary zero-box elements
  // remain fail-closed.
  return subjectDisplay === 'contents'
    && Array.from(el.querySelectorAll('*')).some((descendant) => isElementVisible(descendant));
}

function candidateAccepted(el: Element, target: Target): boolean {
  return verifyElement(el, target) && (!target.visible || isElementVisible(el));
}

export function resolveTarget(target: Target, root: Document = document, multiple = false): Element | Element[] | null {
  if (!multiple) {
    if (target.position && !isNonBlank(target.text)) {
      const el = root.elementFromPoint(target.position.x, target.position.y);
      return el && (!target.visible || isElementVisible(el)) ? el : null;
    }
    return queryOneWithRoot(target, root);
  }

  return queryAllWithRoot(target, root);
}

export async function findElement(target: Target, timeout = 5000): Promise<Element | null> {
  const start = Date.now();
  let verificationError: unknown = null;
  while (Date.now() - start < timeout) {
    let el: Element | null = null;
    try {
      el = queryOne(target);
    } catch (e) {
      if (e instanceof ElementVerificationError) {
        // Wrong-element match — polling will not fix it, but preserve the
        // wait-for-element timeout contract and surface the error after the
        // loop exits rather than short-circuiting the 5s window.
        verificationError = e;
      } else {
        console.debug('findElement: queryOne threw, continuing poll', asError(e));
      }
    }
    if (el) return el;
    await sleep(100);
  }
  if (verificationError) throw verificationError;
  return null;
}

export async function findElements(target: Target, timeout = 5000): Promise<Element[]> {
  const start = Date.now();
  let verificationError: unknown = null;
  while (Date.now() - start < timeout) {
    let els: Element[] = [];
    try {
      els = queryAll(target);
    } catch (e) {
      if (e instanceof ElementVerificationError) {
        verificationError = e;
      } else {
        console.debug('findElements: queryAll threw, continuing poll', asError(e));
      }
    }
    if (els.length > 0) return els;
    await sleep(100);
  }
  if (verificationError) throw verificationError;
  return [];
}

function queryOne(target: Target): Element | null {
  if (target.position && !isNonBlank(target.text)) {
    const el = document.elementFromPoint(target.position.x, target.position.y);
    return el && (!target.visible || isElementVisible(el)) ? el : null;
  }
  const root = getRoot(target);
  return queryOneWithRoot(target, root);
}

function queryAll(target: Target): Element[] {
  const root = getRoot(target);
  return queryAllWithRoot(target, root);
}

/**
 * Fallback-chain resolver for a single element.
 *
 * Chain order: selector → xpath → text → ariaLabel → role. Each branch is
 * wrapped in try/catch — a DOMException from a malformed selector advances
 * to the next strategy rather than crashing the engine. The first branch
 * that yields a *verified* element wins; subsequent branches are skipped.
 *
 * Nonblank text is a required identity verifier for every branch. When no
 * candidate has that text identity, unrelated broad-locator matches represent
 * genuine semantic absence rather than a wrong-element verification failure.
 *
 * R-verify (Unit 2): each candidate element is checked against the Target's
 * disambiguating fields (ariaLabel, role) before being accepted. A branch
 * that yields an element which fails verify records `sawUnverified` and
 * advances. At chain exhaustion, `sawUnverified` triggers an
 * {@link ElementVerificationError} so callers can distinguish silent
 * wrong-element matches from genuine element-not-present.
 */
function queryOneWithRoot(target: Target, root: Root): Element | null {
  let sawUnverified = false;
  const declaredText = isNonBlank(target.text) ? target.text : undefined;
  let sawDeclaredText = false;

  if (isNonBlank(target.selector)) {
    try {
      let els: Element[] = [];
      if (target.index !== undefined) {
        const all = root.querySelectorAll(target.selector);
        const indexed = all[target.index];
        if (indexed) els = [indexed];
      } else {
        els = Array.from(root.querySelectorAll(target.selector));
      }
      for (const el of els) {
        if (declaredText && matchesDeclaredText(el, target)) sawDeclaredText = true;
        if (!verifyElement(el, target)) {
          sawUnverified = true;
          continue;
        }
        if (!target.visible || isElementVisible(el)) return el;
      }
    } catch (e) {
      console.debug('queryOne selector branch failed', asError(e));
    }
  }
  if (isNonBlank(target.xpath)) {
    try {
      const els = evaluateXPathAll(target.xpath, root);
      for (const el of els) {
        if (declaredText && matchesDeclaredText(el, target)) sawDeclaredText = true;
        if (!verifyElement(el, target)) {
          sawUnverified = true;
          continue;
        }
        if (!target.visible || isElementVisible(el)) return el;
      }
    } catch (e) {
      console.debug('queryOne xpath branch failed', asError(e));
    }
  }
  if (declaredText) {
    try {
      const els = findAllByText(root, declaredText);
      for (const el of els) {
        if (matchesDeclaredText(el, target)) sawDeclaredText = true;
        if (!verifyElement(el, target)) {
          sawUnverified = true;
          continue;
        }
        if (!target.visible || isElementVisible(el)) return el;
      }
    } catch (e) {
      console.debug('queryOne text branch failed', asError(e));
    }
  }
  if (isNonBlank(target.ariaLabel)) {
    try {
      const els = Array.from(root.querySelectorAll(`[aria-label="${CSS.escape(target.ariaLabel)}"]`));
      for (const el of els) {
        if (declaredText && matchesDeclaredText(el, target)) sawDeclaredText = true;
        if (!verifyElement(el, target)) {
          sawUnverified = true;
          continue;
        }
        if (!target.visible || isElementVisible(el)) return el;
      }
    } catch (e) {
      console.debug('queryOne ariaLabel branch failed', asError(e));
    }
  }
  if (isNonBlank(target.role)) {
    try {
      const query = isNonBlank(target.roleName)
        ? `[role="${target.role}"][aria-label="${CSS.escape(target.roleName)}"]`
        : `[role="${target.role}"]`;
      const els = Array.from(root.querySelectorAll(query));
      for (const el of els) {
        if (declaredText && matchesDeclaredText(el, target)) sawDeclaredText = true;
        if (!verifyElement(el, target)) {
          sawUnverified = true;
          continue;
        }
        if (!target.visible || isElementVisible(el)) return el;
      }
    } catch (e) {
      console.debug('queryOne role branch failed', asError(e));
    }
  }

  if (declaredText && !sawDeclaredText) return null;
  if (sawUnverified) throw new ElementVerificationError(target);
  return null;
}

/**
 * Fallback-chain resolver for multiple elements. Same text-identity rule,
 * fallback order, R-verify discipline, and try/catch wrapping as
 * {@link queryOneWithRoot}. Each branch filters its matches through
 * {@link verifyElement}; the first branch that yields a non-empty *verified*
 * set wins.
 *
 * Note: the `[role="..."]` branch matches only elements with an explicit
 * `role` attribute. Elements whose role is *implicit* (e.g. native `<li>`
 * without `role="listitem"`) do not match — for those, `ariaLabel` is the
 * effective recovery path (see plan R8 note).
 */
function queryAllWithRoot(target: Target, root: Root): Element[] {
  let sawUnverified = false;
  const declaredText = isNonBlank(target.text) ? target.text : undefined;
  let sawDeclaredText = false;

  if (isNonBlank(target.selector)) {
    try {
      const els = Array.from(root.querySelectorAll(target.selector));
      if (els.length > 0) {
        if (declaredText && els.some((e) => matchesDeclaredText(e, target))) {
          sawDeclaredText = true;
        }
        const verified = els.filter((e) => candidateAccepted(e, target));
        if (verified.length > 0) return verified;
        if (els.some((e) => !verifyElement(e, target))) sawUnverified = true;
      }
    } catch (e) {
      console.debug('queryAll selector branch failed', asError(e));
    }
  }
  if (isNonBlank(target.xpath)) {
    try {
      const els = evaluateXPathAll(target.xpath, root);
      if (els.length > 0) {
        if (declaredText && els.some((e) => matchesDeclaredText(e, target))) {
          sawDeclaredText = true;
        }
        const verified = els.filter((e) => candidateAccepted(e, target));
        if (verified.length > 0) return verified;
        if (els.some((e) => !verifyElement(e, target))) sawUnverified = true;
      }
    } catch (e) {
      console.debug('queryAll xpath branch failed', asError(e));
    }
  }
  if (declaredText) {
    try {
      const els = findAllByText(root, declaredText);
      if (els.length > 0) {
        if (els.some((e) => matchesDeclaredText(e, target))) sawDeclaredText = true;
        const verified = els.filter((e) => candidateAccepted(e, target));
        if (verified.length > 0) return verified;
        if (els.some((e) => !verifyElement(e, target))) sawUnverified = true;
      }
    } catch (e) {
      console.debug('queryAll text branch failed', asError(e));
    }
  }
  if (isNonBlank(target.ariaLabel)) {
    try {
      const els = Array.from(root.querySelectorAll(`[aria-label="${CSS.escape(target.ariaLabel)}"]`));
      if (els.length > 0) {
        if (declaredText && els.some((e) => matchesDeclaredText(e, target))) {
          sawDeclaredText = true;
        }
        const verified = els.filter((e) => candidateAccepted(e, target));
        if (verified.length > 0) return verified;
        if (els.some((e) => !verifyElement(e, target))) sawUnverified = true;
      }
    } catch (e) {
      console.debug('queryAll ariaLabel branch failed', asError(e));
    }
  }
  if (isNonBlank(target.role)) {
    try {
      // M-3: include roleName in the selector to match queryOneWithRoot's
      // behavior. Without this, multiple=true queries by role+roleName return
      // all elements with the same role, ignoring the name filter.
      const roleSelector = isNonBlank(target.roleName)
        ? `[role="${target.role}"][aria-label="${CSS.escape(target.roleName)}"]`
        : `[role="${target.role}"]`;
      const els = Array.from(root.querySelectorAll(roleSelector));
      if (els.length > 0) {
        if (declaredText && els.some((e) => matchesDeclaredText(e, target))) {
          sawDeclaredText = true;
        }
        const verified = els.filter((e) => candidateAccepted(e, target));
        if (verified.length > 0) return verified;
        if (els.some((e) => !verifyElement(e, target))) sawUnverified = true;
      }
    } catch (e) {
      console.debug('queryAll role branch failed', asError(e));
    }
  }

  if (declaredText && !sawDeclaredText) return [];
  if (sawUnverified) throw new ElementVerificationError(target);
  return [];
}

function getRoot(target: Target): Root {
  let root: Root = document;
  if (typeof target.frame === 'number' || isNonBlank(target.frame)) {
    const frames = document.querySelectorAll('iframe');
    const frame = typeof target.frame === 'number' ? frames[target.frame] : Array.from(frames).find((f) => f.matches(target.frame as string));
    if (frame?.contentDocument) root = frame.contentDocument;
  }
  if (target.shadowPath) {
    for (const part of target.shadowPath) {
      if (!isNonBlank(part)) continue;
      const host: Element | null = root.querySelector(part);
      if (host?.shadowRoot) root = host.shadowRoot;
    }
  }
  return root;
}

export function evaluateXPath(xpath: string, context: Node, single: boolean): Element | null {
  const result = document.evaluate(xpath, context, null, single ? XPathResult.FIRST_ORDERED_NODE_TYPE : XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
  if (single) return (result.singleNodeValue as Element) || null;
  return result.snapshotLength > 0 ? (result.snapshotItem(0) as Element) : null;
}

function evaluateXPathAll(xpath: string, context: Node): Element[] {
  const result = document.evaluate(xpath, context, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
  const elements: Element[] = [];
  for (let i = 0; i < result.snapshotLength; i++) {
    const node = result.snapshotItem(i);
    if (node?.nodeType === 1) elements.push(node as Element);
  }
  return elements;
}

export function findByText(root: Node, text: string, multiple: boolean): Element | null {
  return findAllByText(root, text, multiple ? undefined : 1)[0] ?? null;
}

function findAllByText(root: Node, text: string, limit?: number): Element[] {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
  const matches: Element[] = [];
  const expected = normalizedTargetText(text);
  let node: Node | null;
  while ((node = walker.nextNode())) {
    if (normalizedTargetText(node.textContent ?? '') === expected) {
      const el = node.parentElement;
      if (el) matches.push(el);
      if (limit !== undefined && matches.length >= limit) break;
    }
  }
  return matches;
}

/** Coerce a caught value into an Error for logging. */
function asError(value: unknown): Error {
  if (value instanceof Error) return value;
  return new Error(String(value));
}
