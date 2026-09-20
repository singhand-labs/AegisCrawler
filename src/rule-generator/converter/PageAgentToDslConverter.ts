import type { Rule, PageAgentRecording, PageAgentEvent, ConvertOptions, Action, Humanize } from '../types';
import { SelectorAliasRegistry, resolveTarget } from './selector-resolver';
import { mapEvent, EventMapContext } from './event-mappers';
import { guessVariables } from './variable-guesser';
import { hasAnnotations, buildAnnotatedActions } from './annotated-converter';

const DEFAULT_HUMANIZE: Humanize = {
  preDelay: [100, 300],
  postDelay: [100, 300],
};

function mergeSubmitFormEvents(events: PageAgentEvent[]): PageAgentEvent[] {
  const result: PageAgentEvent[] = [];
  for (let i = 0; i < events.length; i++) {
    const event = events[i];
    if (event.type === 'submitForm' && result.length > 0) {
      const prev = result[result.length - 1];
      if (prev.type === 'inputText' && prev.index === event.index) {
        result[result.length - 1] = { ...prev, submit: true };
        continue;
      }
    }
    result.push(event);
  }
  return result;
}

/**
 * The recorder stores the control's complete value on every input event. Slow
 * human typing can therefore cross its debounce boundary and persist a prefix
 * sequence ("N", "Ne", ... "New York"). Replaying every snapshot as a type
 * action duplicates or corrupts the logical entry after variable inference.
 * Collapse only adjacent, same-control, strict prefix growth; edits,
 * replacements, other controls, and intervening actions remain explicit.
 */
function coalesceIncrementalInputTextEvents(events: PageAgentEvent[]): PageAgentEvent[] {
  const result: PageAgentEvent[] = [];
  for (const event of events) {
    const previous = result.at(-1);
    if (event.type === 'inputText'
      && previous?.type === 'inputText'
      && previous.index === event.index
      && event.text.length > previous.text.length
      && event.text.startsWith(previous.text)
      && previous.submit !== true) {
      result[result.length - 1] = event;
      continue;
    }
    result.push(event);
  }
  return result;
}

function recordedHttpsDomains(recording: PageAgentRecording): string | string[] {
  const domains: string[] = [];
  const seen = new Set<string>();
  const add = (raw: unknown): void => {
    if (typeof raw !== 'string' || raw.trim() === '') return;
    try {
      const parsed = new URL(raw);
      const loopbackHttp = parsed.protocol === 'http:'
        && (parsed.hostname === 'localhost' || parsed.hostname === '127.0.0.1' || parsed.hostname === '[::1]');
      if ((parsed.protocol !== 'https:' && !loopbackHttp) || parsed.username || parsed.password) return;
      const host = parsed.hostname.toLowerCase();
      if (host && !seen.has(host)) {
        seen.add(host);
        domains.push(host);
      }
    } catch {
      // Malformed recording URLs cannot authorize a rule domain.
    }
  };

  add(recording.meta.startUrl);
  for (const event of recording.events) {
    if (event.type === 'navigate') add(event.url);
  }
  for (const snapshot of recording.snapshots) add(snapshot.url);

  if (domains.length === 0) return recording.meta.domain;
  return domains.length === 1 ? domains[0] : domains;
}

function isSafeRecordedEntry(raw: unknown): raw is string {
  if (typeof raw !== 'string' || raw.trim() === '') return false;
  try {
    const parsed = new URL(raw);
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:')
      && !parsed.username
      && !parsed.password
      && parsed.hostname !== '';
  } catch {
    return false;
  }
}

function recordedEntry(recording: PageAgentRecording): string {
  if (isSafeRecordedEntry(recording.meta.startUrl)) return recording.meta.startUrl;
  const candidates: Array<{ url: unknown; timestamp: number; order: number }> = [];
  let order = 0;
  for (const event of recording.events) {
    if (event.type === 'navigate') candidates.push({ url: event.url, timestamp: event.timestamp, order: order++ });
  }
  for (const snapshot of recording.snapshots) {
    candidates.push({ url: snapshot.url, timestamp: snapshot.timestamp, order: order++ });
  }
  candidates.sort((a, b) => a.timestamp - b.timestamp || a.order - b.order);
  return candidates.find((candidate) => isSafeRecordedEntry(candidate.url))?.url as string
    ?? recording.meta.startUrl;
}

export function convert(recording: PageAgentRecording, options: ConvertOptions = {}): Rule {
  const registry = new SelectorAliasRegistry();
  const optimize = options.optimizeSelectors !== false;
  const guess = options.guessVariables !== false;
  const humanize = options.defaultHumanize ?? DEFAULT_HUMANIZE;

  const ctx: EventMapContext = {
    registry,
    resolve: (index, timestamp) => resolveTarget(recording, index, timestamp, registry, optimize),
  };

  const events = mergeSubmitFormEvents(coalesceIncrementalInputTextEvents(recording.events));

  const actions: Action[] = hasAnnotations(recording)
    ? buildAnnotatedActions({ ...recording, events }, ctx)
    : events.flatMap((event) => mapEvent(event, ctx));

  const normalizedRecording = { ...recording, events };
  const { variables, templatize } = guess
    ? guessVariables(normalizedRecording)
    : { variables: {}, templatize: (s: string) => s };

  for (const action of actions) {
    if (action.action === 'navigate') {
      action.url = templatize(action.url);
    }
    if (action.action === 'type') {
      action.value = templatize(action.value);
    }
    if (action.action === 'select' && typeof action.value === 'string') {
      action.value = templatize(action.value);
    }
  }

  const ruleId = options.ruleIdPrefix
    ? `${options.ruleIdPrefix}-${Date.now()}`
    : `recorded-${recording.meta.domain}-${Date.now()}`;

  const rule: Rule = {
    id: ruleId,
    version: '1.0.0',
    name: `录制-${recording.meta.title}`,
    domain: recordedHttpsDomains(recording),
    enabled: true,
    // Entry is recording-derived identity, not an input-dependent navigation.
    // Keep it literal so inferred variables cannot turn an otherwise safe URL
    // into an invalid value such as `https://example.test/?q={{keyword}}`.
    entry: recordedEntry(recording),
    variables,
    selectors: registry.getAliases(),
    humanize,
    steps: actions,
    hooks: {
      onError: [{ action: 'saveSnapshot', name: 'errorSnapshot' }, { action: 'flushResults' }],
    },
  };

  return rule;
}
