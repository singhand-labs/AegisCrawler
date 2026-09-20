import type { DomElementInfo } from '../types';

export interface OptimizerContext {
  allSelectors: string[];
}

const ID_RE = /#([A-Za-z0-9_\-]+)/;
const DATA_TESTID_RE = /\[data-testid="([^"]+)"\]/;
const DATA_QA_RE = /\[data-qa="([^"]+)"\]/;
const DATA_AUTOMATION_ID_RE = /\[data-automation-id="([^"]+)"\]/;
const CLASS_RE = /\.([A-Za-z0-9_\-]+)/g;

export function optimizeSelector(info: DomElementInfo, ctx: OptimizerContext): string {
  // 1. id — strip attribute selectors first so hashes inside attribute values are ignored.
  const selectorWithoutAttrs = info.selector.replace(/\[[^\]]*\]/g, '');
  const idMatch = selectorWithoutAttrs.match(ID_RE);
  if (idMatch) return `#${idMatch[1]}`;

  // 2. data-testid / data-qa / data-automation-id
  const dataTestId = info.selector.match(DATA_TESTID_RE)?.[1];
  if (dataTestId) return `[data-testid="${dataTestId}"]`;
  const dataQa = info.selector.match(DATA_QA_RE)?.[1];
  if (dataQa) return `[data-qa="${dataQa}"]`;
  const dataAutomationId = info.selector.match(DATA_AUTOMATION_ID_RE)?.[1];
  if (dataAutomationId) return `[data-automation-id="${dataAutomationId}"]`;

  // 3. name attribute
  if (info.name) return `${info.tagName.toLowerCase()}[name="${info.name}"]`;

  // 4. aria-label
  if (info.ariaLabel) return `[aria-label="${info.ariaLabel}"]`;

  // 5. unique class combination
  const classes = extractClasses(info.selector);
  if (classes.length > 0) {
    const classSelector = '.' + classes.join('.');
    const matches = ctx.allSelectors.filter((s) => hasAllClasses(s, classes)).length;
    if (matches === 1) return classSelector;
  }

  // 6. placeholder for inputs
  if (info.placeholder) return `${info.tagName.toLowerCase()}[placeholder="${info.placeholder}"]`;

  // 7. fallback to original selector
  return info.selector;
}

function extractClasses(selector: string): string[] {
  const matches = selector.match(CLASS_RE);
  return matches ? matches.map((m) => m.slice(1)) : [];
}

function hasAllClasses(selector: string, classes: string[]): boolean {
  const tokens = extractClasses(selector);
  return classes.every((c) => tokens.includes(c));
}
