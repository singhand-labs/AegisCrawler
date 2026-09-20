import type { PageAgentEvent, Action, Target, InputTextEvent, SubmitFormEvent, ClickEvent, ScrollEvent, ExtractEvent } from '../types';
import type { SelectorAliasRegistry } from './selector-resolver';

export interface UnknownEventInfo {
  type: string;
  event: PageAgentEvent;
  rule?: unknown;
}

export interface EventMapContext {
  registry: SelectorAliasRegistry;
  resolve(index: number, timestamp: number): Target;
  /** Optional rule reference, forwarded to onUnknownEvent for telemetry. */
  rule?: unknown;
  /** Optional hook invoked when mapEvent encounters an unknown event type. */
  onUnknownEvent?: (info: UnknownEventInfo) => void;
}

export function mapEvent(event: PageAgentEvent, ctx: EventMapContext): Action[] {
  const { type } = event;
  switch (event.type) {
    case 'navigate':
      return [{ action: 'navigate', url: event.url, waitUntil: 'load' }];
    case 'click':
      return mapClick(event, ctx);
    case 'inputText':
      return mapInputText(event, ctx);
    case 'submitForm':
      return mapSubmitForm(event, ctx);
    case 'selectOption':
      return [{ action: 'select', target: ctx.resolve(event.index, event.timestamp), value: event.optionText, by: 'text' }];
    case 'scroll':
      return mapScroll(event, ctx);
    case 'executeJavascript':
      // M-1: recorded JS scripts are NOT auto-converted to evaluate actions.
      // A crafted recording could inject arbitrary code into the rule, which
      // would execute in worker-host mode (allowEvaluate=true). Users can
      // manually add evaluate actions after reviewing the generated rule.
      console.warn('[event-mappers] executeJavascript event skipped (recorded scripts are not auto-converted for safety)');
      ctx.onUnknownEvent?.({ type: 'executeJavascript', event, rule: ctx.rule });
      return [];
    case 'wait':
      return [{ action: 'waitForTimeout', ms: event.duration }];
    case 'extract':
      return mapExtract(event, ctx);
    default: {
      const msg = `[event-mappers] unknown event type: ${type} (skipped)`;
      console.warn(msg);
      ctx.onUnknownEvent?.({ type: String(type), event, rule: ctx.rule });
      return [];
    }
  }
}

function mapInputText(event: InputTextEvent, ctx: EventMapContext): Action[] {
  const action: Action = {
    action: 'type',
    target: ctx.resolve(event.index, event.timestamp),
    value: event.text,
  };
  if (event.submit) {
    action.submit = true;
  }
  return [action];
}

function mapSubmitForm(event: SubmitFormEvent, ctx: EventMapContext): Action[] {
  if (event.submitterIndex !== undefined) {
    return [{ action: 'click', target: ctx.resolve(event.submitterIndex, event.timestamp) }];
  }
  return [{ action: 'pressKey', keys: ['Enter'] }];
}

function mapClick(event: ClickEvent, ctx: EventMapContext): Action[] {
  const target = ctx.resolve(event.index, event.timestamp);
  return [{ action: 'click', target }];
}

function mapScroll(event: ScrollEvent, ctx: EventMapContext): Action[] {
  if (event.unit === 'pages') {
    return [{
      action: 'scrollBy',
      direction: event.direction,
      distance: event.amount,
      unit: 'pages',
    }];
  }
  const target = event.index === undefined
    ? undefined
    : ctx.resolve(event.index, event.timestamp);
  return [{
    action: 'scrollBy',
    direction: event.direction,
    distance: event.amount,
    ...(target ? { target } : {}),
  }];
}

function mapExtract(event: ExtractEvent, ctx: EventMapContext): Action[] {
  const target = ctx.resolve(event.index, event.timestamp);
  if (event.mode === 'text') {
    return [{ action: 'extractText', name: event.fieldName, target }];
  }
  if (event.mode === 'html') {
    return [{ action: 'extractHtml', name: event.fieldName, target }];
  }
  return [{ action: 'extractAttribute', name: event.fieldName, target, attr: event.attribute ?? '' }];
}
