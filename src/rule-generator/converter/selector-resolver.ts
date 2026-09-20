import type { PageAgentRecording, DomSnapshot, DomElementInfo, Target } from '../types';
import { optimizeSelector } from './selector-optimizer';

/**
 * Optional disambiguating fields carried alongside a selector when registering
 * an alias. Both fields accept the recording's `DomElementInfo` values as-is;
 * the registry drops sentinels (`'[REDACTED]'`) and undefined values itself.
 */
export interface AliasExtras {
  tagName?: string;
  ariaLabel?: string;
  role?: string;
  text?: string;
  boundingRect?: { x: number; y: number; width: number; height: number };
}

/** Sentinel value the recording layer substitutes for sensitive attributes. */
const REDACTED = '[REDACTED]';
/** Unit-separator used to build a collision-free composite dedup key. */
const KEY_SEP = '\u001F';

function stableTargetText(extras: AliasExtras | undefined): string {
  const text = extras?.text && extras.text !== REDACTED ? extras.text : '';
  const tagName = extras?.tagName?.trim().toLowerCase();
  const role = extras?.role?.trim().toLowerCase();
  if (tagName === 'input' || tagName === 'textarea' || tagName === 'select'
    || role === 'combobox' || role === 'textbox' || role === 'searchbox'
    || role === 'spinbutton') return '';
  return text;
}

/**
 * Build the canonical composite dedup key for an alias. Canonical field order
 * is `selector → ariaLabel → role`; the recording layer and this registry are
 * the only sites that need to agree on it, hence the single helper here.
 *
 * `'[REDACTED]'` and empty/undefined disambiguators normalize to the empty
 * string so a redacted placeholder does not fragment aliases or leak into the
 * emitted rule (a `[REDACTED]` ariaLabel would never match at replay time and
 * would force every lookup through the verification-error path).
 */
function compositeKey(selector: string, extras: AliasExtras | undefined): string {
  const ariaLabel = extras?.ariaLabel && extras.ariaLabel !== REDACTED ? extras.ariaLabel : '';
  const role = extras?.role && extras.role !== REDACTED ? extras.role : '';
  const text = stableTargetText(extras);
  const position = extras?.boundingRect ? quantizePosition(extras.boundingRect) : '';
  return `${selector}${KEY_SEP}${ariaLabel}${KEY_SEP}${role}${KEY_SEP}${text}${KEY_SEP}${position}`;
}

/**
 * Quantize the bounding-rect center to a 5px bucket so sub-pixel jitter from
 * scroll / resize / font load does not fragment aliases. The Target's emitted
 * `position` field keeps the raw center point; quantization is dedup-only.
 */
function quantizePosition(rect: AliasExtras['boundingRect']): string {
  const cx = rect!.x + rect!.width / 2;
  const cy = rect!.y + rect!.height / 2;
  return `${Math.round(cx / 5) * 5},${Math.round(cy / 5) * 5}`;
}

/** Build the structured Target body stored under an alias. */
function buildTarget(selector: string, extras: AliasExtras | undefined): Target {
  const target: Target = { selector };
  const ariaLabel = extras?.ariaLabel && extras.ariaLabel !== REDACTED ? extras.ariaLabel : undefined;
  const role = extras?.role && extras.role !== REDACTED ? extras.role : undefined;
  const text = stableTargetText(extras) || undefined;
  if (ariaLabel) target.ariaLabel = ariaLabel;
  if (role) {
    target.role = role;
    if (text) target.roleName = text;
  }
  if (text) target.text = text;
  // position is a LAST-RESORT disambiguator: selectors.ts:51,116 short-
  // circuits to elementFromPoint whenever target.position is set,
  // bypassing the 5-branch fallback chain AND R-verify. Only emit when
  // no semantic disambiguator is present; otherwise the multi-strategy
  // feature this PR unlocks would be silently defeated.
  if (extras?.boundingRect && !ariaLabel && !role && !text) {
    target.position = {
      x: extras.boundingRect.x + extras.boundingRect.width / 2,
      y: extras.boundingRect.y + extras.boundingRect.height / 2,
    };
  }
  return target;
}

export class SelectorAliasRegistry {
  /** Maps composite dedup key → { alias, target }. Insertion order preserved. */
  private aliases = new Map<string, { alias: string; target: Target }>();
  private counter = 0;

  getAlias(selector: string, extras?: AliasExtras): string {
    const key = compositeKey(selector, extras);
    const existing = this.aliases.get(key);
    if (existing) return existing.alias;
    const alias = `el${++this.counter}`;
    this.aliases.set(key, { alias, target: buildTarget(selector, extras) });
    return alias;
  }

  getAliases(): Record<string, Target> {
    const result: Record<string, Target> = {};
    for (const { alias, target } of this.aliases.values()) {
      result[alias] = target;
    }
    return result;
  }
}

/** Per-snapshot cache for allSelectors to avoid recomputing on every resolveTarget call. */
const allSelectorsCache = new WeakMap<DomSnapshot, string[]>();

function getAllSelectors(snapshot: DomSnapshot): string[] {
  let cached = allSelectorsCache.get(snapshot);
  if (!cached) {
    cached = Object.values(snapshot.selectorMap).map((i) => i.selector);
    allSelectorsCache.set(snapshot, cached);
  }
  return cached;
}

export function findSnapshot(recording: PageAgentRecording, timestamp: number): DomSnapshot | null {
  const snapshots = recording.snapshots;
  if (snapshots.length === 0) return null;

  // Snapshots are pushed in monotonically increasing timestamp order.
  // Binary search for the last snapshot with timestamp <= target.
  let lo = 0;
  let hi = snapshots.length - 1;
  let best = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (snapshots[mid].timestamp <= timestamp) {
      best = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return best >= 0 ? snapshots[best] : null;
}

function findEventSnapshot(recording: PageAgentRecording, index: number, timestamp: number): DomSnapshot | null {
  const exactPreAction = recording.snapshots.filter((snapshot) => snapshot.timestamp === timestamp
    && snapshot.phase === 'before-action'
    && snapshot.selectorMap[index] !== undefined);
  if (exactPreAction.length > 1) {
    throw new Error(`Ambiguous before-action snapshots for index=${index}, timestamp=${timestamp}`);
  }
  return exactPreAction[0] ?? findSnapshot(recording, timestamp);
}

export function resolveElementInfo(recording: PageAgentRecording, index: number, timestamp: number): DomElementInfo | null {
  const snapshot = findEventSnapshot(recording, index, timestamp);
  if (!snapshot) return null;
  return snapshot.selectorMap[index] ?? null;
}

export function resolveTarget(
  recording: PageAgentRecording,
  index: number,
  timestamp: number,
  registry: SelectorAliasRegistry,
  optimize = true,
): Target {
  const snapshot = findEventSnapshot(recording, index, timestamp);
  if (!snapshot) {
    throw new Error(`No snapshot found for timestamp=${timestamp}`);
  }

  const info = snapshot.selectorMap[index] ?? null;
  if (!info) {
    throw new Error(`Element not found in recording snapshots: index=${index}, timestamp=${timestamp}`);
  }

  const allSelectors = optimize ? getAllSelectors(snapshot) : [];
  const selector = optimize ? optimizeSelector(info, { allSelectors }) : info.selector;
  const alias = registry.getAlias(selector, {
    tagName: info.tagName,
    ariaLabel: info.ariaLabel,
    role: info.role,
    text: info.text,
    boundingRect: info.boundingRect,
  });
  return { $ref: alias };
}
