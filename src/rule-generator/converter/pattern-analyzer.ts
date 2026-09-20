import type { PageAgentEvent, ExtractEvent, ClickEvent } from '../types';

export interface ExtractFieldPattern {
  type: 'field' | 'listItem';
  events: ExtractEvent[];
  /** 原始事件流起始索引（含） */
  startIndex: number;
  /** 原始事件流结束索引（含） */
  endIndex: number;
}

export interface PaginationPattern {
  type: 'nextPage';
  clickEvent: ClickEvent;
  /** 分页动作在事件流中的索引 */
  eventIndex: number;
}

export interface AuthPattern {
  type: 'login' | 'captcha';
  eventIndex: number;
  annotation: 'login' | 'captcha';
}

export interface AnalyzedPatterns {
  /** 分组后的提取模式：每个组会生成一个 extract action */
  extractGroups: ExtractFieldPattern[];
  /** 分页模式 */
  pagination: PaginationPattern[];
  /** 登录/验证码模式 */
  auth: AuthPattern[];
}

export function analyzePatterns(events: PageAgentEvent[]): AnalyzedPatterns {
  const extractGroups: ExtractFieldPattern[] = [];
  const pagination: PaginationPattern[] = [];
  const auth: AuthPattern[] = [];

  let i = 0;
  while (i < events.length) {
    const event = events[i];

    if (event.type === 'click' && event.annotation) {
      if (event.annotation === 'nextPage') {
        pagination.push({ type: 'nextPage', clickEvent: event, eventIndex: i });
        i++;
        continue;
      }
      if (event.annotation === 'login' || event.annotation === 'captcha') {
        auth.push({ type: event.annotation, eventIndex: i, annotation: event.annotation });
        i++;
        continue;
      }
    }

    if (event.type === 'extract' && event.annotation) {
      const group = collectExtractGroup(events, i, event.annotation);
      extractGroups.push(group);
      i = group.endIndex + 1;
      continue;
    }

    i++;
  }

  return { extractGroups, pagination, auth };
}

function collectExtractGroup(
  events: PageAgentEvent[],
  startIndex: number,
  annotation: 'listItem' | 'field',
): ExtractFieldPattern {
  const groupEvents: ExtractEvent[] = [events[startIndex] as ExtractEvent];
  let endIndex = startIndex;

  for (let j = startIndex + 1; j < events.length; j++) {
    const ev = events[j];
    if (ev.type !== 'extract' || ev.annotation !== annotation) break;
    groupEvents.push(ev);
    endIndex = j;
  }

  return {
    type: annotation,
    events: groupEvents,
    startIndex,
    endIndex,
  };
}
