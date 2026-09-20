import type { PageAgentEvent, PageAgentRecording, Action, ExtractEvent } from '../types';
import type { ExtractField } from '../../rule-engine/types/action';
import type { EventMapContext } from './event-mappers';
import type { ExtractFieldPattern } from './pattern-analyzer';
import { analyzePatterns } from './pattern-analyzer';
import { mapEvent } from './event-mappers';

export function hasAnnotations(recording: PageAgentRecording): boolean {
  return recording.events.some((e) => 'annotation' in e && e.annotation);
}

export function buildAnnotatedActions(recording: PageAgentRecording, ctx: EventMapContext): Action[] {
  const patterns = analyzePatterns(recording.events);
  const { pagination, auth, extractGroups } = patterns;

  const actions: Action[] = [];

  // Build index sets for quick lookup.
  const paginationIndexes = new Set(pagination.map((p) => p.eventIndex));
  const authIndexes = new Set(auth.map((a) => a.eventIndex));

  // Map extract group start indexes to their group objects for O(1) lookup.
  const extractGroupByStart = new Map<number, ExtractFieldPattern>();
  const extractGroupIndexes = new Set<number>();
  for (const group of extractGroups) {
    extractGroupByStart.set(group.startIndex, group);
    for (let k = group.startIndex; k <= group.endIndex; k++) {
      extractGroupIndexes.add(k);
    }
  }

  // 1. 前置线性步骤：导航、过滤、登录前的操作等（排除提取组和分页/认证事件）
  let i = 0;
  while (i < recording.events.length) {
    if (extractGroupIndexes.has(i) || paginationIndexes.has(i) || authIndexes.has(i)) break;
    actions.push(...mapEvent(recording.events[i], ctx));
    i++;
  }

  // 2. 收集后续事件（保持原始时间顺序）
  // 统一处理提取组、分页事件之间的普通动作，确保不丢失事件且顺序正确。
  const postLoopActions: Action[] = [];
  while (i < recording.events.length) {
    // Skip pagination and auth events — handled separately below.
    if (paginationIndexes.has(i) || authIndexes.has(i)) {
      i++;
      continue;
    }

    // If this is the start of an extract group, emit the extract action.
    const group = extractGroupByStart.get(i);
    if (group) {
      postLoopActions.push(buildExtractAction(group, ctx));
      i = group.endIndex + 1;
      continue;
    }

    // Otherwise, map the event normally.
    postLoopActions.push(...mapEvent(recording.events[i], ctx));
    i++;
  }

  // 3. 决定输出方式：有分页+提取组时包裹循环；否则直接输出
  if (pagination.length > 0 && extractGroups.length > 0) {
    const nextPageEvent = recording.events[pagination[0].eventIndex];
    if (nextPageEvent.type === 'click') {
      const nextPageTarget = ctx.resolve(nextPageEvent.index, nextPageEvent.timestamp);
      postLoopActions.push({ action: 'click', target: nextPageTarget });
      postLoopActions.push({ action: 'waitForTimeout', ms: 1000 });

      actions.push({
        action: 'loop',
        type: 'whileElementExists',
        target: nextPageTarget,
        maxIterations: 100,
        steps: postLoopActions,
      });
    } else {
      actions.push(...postLoopActions);
    }
  } else {
    actions.push(...postLoopActions);
  }

  // 4. 认证分支
  for (const a of auth) {
    const authEvent = recording.events[a.eventIndex];
    if (authEvent.type !== 'click') continue;
    const target = ctx.resolve(authEvent.index, authEvent.timestamp);
    const authAction: Action = a.type === 'captcha'
      ? { action: 'solveCaptcha', target, fallbackToHuman: true }
      : { action: 'requestHuman', type: 'generic', prompt: '请完成登录后继续', timeout: 300000 };

    actions.push({
      action: 'if',
      condition: { type: 'elementExists', target },
      then: [authAction],
      else: [],
    });
  }

  return actions;
}

function buildExtractAction(group: ExtractFieldPattern, ctx: EventMapContext): Action {
  const firstEvent = group.events[0];
  const target = ctx.resolve(firstEvent.index, firstEvent.timestamp);

  const fields: Record<string, ExtractField> = {};
  for (const ev of group.events) {
    fields[ev.fieldName] = extractFieldFromEvent(ev);
  }

  return {
    action: 'extract',
    name: group.type === 'listItem' ? 'items' : 'fields',
    target,
    multiple: group.type === 'listItem',
    fields,
    onEmpty: 'sendEmpty',
  };
}

function extractFieldFromEvent(ev: ExtractEvent): ExtractField {
  if (ev.mode === 'text') {
    return { type: 'text' };
  }
  if (ev.mode === 'html') {
    return { type: 'html' };
  }
  return { type: 'attr', attr: ev.attribute ?? 'href' };
}
