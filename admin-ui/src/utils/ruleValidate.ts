import type { Rule, Action, ActionType } from '../types/rule';

export interface ValidationResult {
  valid: boolean;
  errors: string[];
}

const TARGET_REQUIRED_ACTIONS = new Set<ActionType>([
  'click',
  'doubleClick',
  'rightClick',
  'middleClick',
  'hover',
  'focus',
  'blur',
  'scrollTo',
  'scrollIntoView',
  'removeElement',
  'setStyle',
  'type',
  'paste',
  'clear',
  'select',
  'check',
  'selectRadio',
  'typeAndSelect',
  'uploadFile',
  'waitFor',
  'waitForElementHidden',
  'waitForElementVisible',
  'waitForText',
  'screenshot',
  'solveCaptcha',
]);

const VALUE_REQUIRED_ACTIONS = new Set<ActionType>(['type', 'paste']);

const URL_REQUIRED_ACTIONS = new Set<ActionType>(['navigate', 'openTab']);

const CONTAINER_ACTIONS = new Set<ActionType>([
  'loop',
  'group',
  'retry',
  'cleanup',
  'parallel',
]);

const LOOP_TYPES = new Set<string>(['while', 'until', 'count', 'forEach']);

const PRIORITIES = new Set<string>(['low', 'normal', 'high']);

function isNonNegativeNumber(n: unknown): n is number {
  return typeof n === 'number' && !Number.isNaN(n) && n >= 0;
}

function isPositiveInteger(n: unknown): n is number {
  return Number.isInteger(n) && (n as number) > 0;
}

function isValidHttpUrl(url: unknown): boolean {
  if (typeof url !== 'string') return false;
  const trimmed = url.trim();
  if (!trimmed.startsWith('http://') && !trimmed.startsWith('https://')) {
    return false;
  }
  try {
    // eslint-disable-next-line no-new
    new URL(trimmed);
    return true;
  } catch {
    return false;
  }
}

function requiresTarget(type: ActionType): boolean {
  if (TARGET_REQUIRED_ACTIONS.has(type)) return true;
  return typeof type === 'string' && type.startsWith('extract');
}

function hasTarget(action: Action): boolean {
  const t = action.target;
  if (!t || typeof t !== 'object') return false;
  return !!(
    t.$ref ||
    t.selector ||
    t.xpath ||
    t.text ||
    t.ariaLabel ||
    t.role ||
    t.position
  );
}

function formatPath(prefix: string, segment: string | number): string {
  if (prefix === '') {
    return typeof segment === 'number' ? `[${segment}]` : segment;
  }
  return typeof segment === 'number' ? `${prefix}[${segment}]` : `${prefix}.${segment}`;
}

function validateActionList(
  actions: unknown,
  path: string,
  errors: string[],
): void {
  if (!Array.isArray(actions)) {
    errors.push(`${path} 必须是动作数组`);
    return;
  }
  actions.forEach((action, index) => {
    validateAction(action, formatPath(path, index), errors);
  });
}

function validateAction(
  action: unknown,
  path: string,
  errors: string[],
): void {
  if (!action || typeof action !== 'object') {
    errors.push(`${path} 动作格式无效`);
    return;
  }

  const a = action as Action;

  if (typeof a.action !== 'string' || a.action.trim() === '') {
    errors.push(`${path} 缺少动作类型`);
    return;
  }

  const type = a.action;

  if (requiresTarget(type) && !hasTarget(a)) {
    errors.push(`${path} 缺少目标元素`);
  }

  if (VALUE_REQUIRED_ACTIONS.has(type) && (typeof a.value !== 'string' || a.value === '')) {
    errors.push(`${path} 缺少输入值`);
  }

  if (URL_REQUIRED_ACTIONS.has(type) && !isValidHttpUrl(a.url)) {
    errors.push(`${path} URL 不合法或必须以 http(s):// 开头`);
  }

  if (CONTAINER_ACTIONS.has(type)) {
    if (!Array.isArray(a.steps) || a.steps.length === 0) {
      errors.push(`${path} 子步骤数组必须存在且非空`);
    } else {
      validateActionList(a.steps, formatPath(path, 'steps'), errors);
    }
  }

  if (type === 'if') {
    if (!a.condition) {
      errors.push(`${path} 缺少 condition`);
    }
    if (!Array.isArray(a.then) || a.then.length === 0) {
      errors.push(`${formatPath(path, 'then')} 子步骤数组必须存在且非空`);
    } else {
      validateActionList(a.then, formatPath(path, 'then'), errors);
    }
    if (a.else !== undefined) {
      if (!Array.isArray(a.else) || a.else.length === 0) {
        errors.push(`${formatPath(path, 'else')} 子步骤数组必须存在且非空`);
      } else {
        validateActionList(a.else, formatPath(path, 'else'), errors);
      }
    }
  }

  if (type === 'loop') {
    const loopType = typeof a.type === 'string' ? a.type : undefined;
    if (!loopType || !LOOP_TYPES.has(loopType)) {
      errors.push(`${formatPath(path, 'type')} 必须是 while/until/count/forEach 之一`);
    }
    if (loopType === 'count' && !isPositiveInteger(a.count)) {
      errors.push(`${formatPath(path, 'count')} 必须为正整数`);
    }
    if ((loopType === 'while' || loopType === 'until') && !a.condition) {
      errors.push(`${path} 缺少 condition`);
    }
  }

  if (type === 'switch') {
    if (!Array.isArray(a.cases)) {
      errors.push(`${path} 缺少 cases 数组`);
    } else if (a.cases.length === 0) {
      errors.push(`${formatPath(path, 'cases')} 不能为空`);
    } else {
      a.cases.forEach((c, i) => {
        if (!c || typeof c !== 'object') {
          errors.push(`${formatPath(path, `cases[${i}]`)} 格式无效`);
          return;
        }
        if (typeof c.value !== 'string') {
          errors.push(`${formatPath(path, `cases[${i}]`)} 缺少 value`);
        }
        if (!Array.isArray(c.steps) || c.steps.length === 0) {
          errors.push(`${formatPath(path, `cases[${i}]`)}.steps 必须存在且非空`);
        } else {
          validateActionList(c.steps, formatPath(path, `cases[${i}].steps`), errors);
        }
      });
    }
    if (a.default !== undefined) {
      if (!Array.isArray(a.default)) {
        errors.push(`${formatPath(path, 'default')} 必须是动作数组`);
      } else {
        validateActionList(a.default, formatPath(path, 'default'), errors);
      }
    }
  }

  if ((type === 'waitForTimeout' || type === 'sleep') && !isNonNegativeNumber(a.ms)) {
    errors.push(`${formatPath(path, 'ms')} 必须为非负数`);
  }

  if (a.timeout !== undefined && !isNonNegativeNumber(a.timeout)) {
    errors.push(`${formatPath(path, 'timeout')} 必须为非负数`);
  }
}

/**
 * Comprehensive client-side validation for the AegisCrawler rule DSL.
 *
 * Validates rule-level fields (name, entry, steps, priority) and recursively
 * checks every action including nested branches (then/else), loops, groups,
 * switch cases/default and hooks. Returns Chinese error messages prefixed
 * with the action path, e.g. "steps[1].then[0] 缺少目标元素".
 *
 * The function is pure and does not mutate the input rule.
 */
export function validateRule(rule: Rule): ValidationResult {
  const errors: string[] = [];

  if (!rule.name?.trim()) {
    errors.push('规则名称不能为空');
  }

  if (!isValidHttpUrl(rule.entry)) {
    errors.push('入口 URL 不合法或必须以 http(s):// 开头');
  }

  if (rule.priority !== undefined && rule.priority !== '' && !PRIORITIES.has(rule.priority)) {
    errors.push('priority 必须是 low/normal/high 之一');
  }

  if (!Array.isArray(rule.steps)) {
    errors.push('steps 必须是数组');
  } else if (rule.steps.length === 0) {
    errors.push('steps 不能为空');
  } else {
    validateActionList(rule.steps, 'steps', errors);
  }

  const hookKeys: Array<keyof Rule['hooks']> = [
    'beforeAll',
    'afterAll',
    'onError',
    'cleanup',
  ];
  hookKeys.forEach((key) => {
    const hooks = rule.hooks?.[key];
    if (hooks !== undefined) {
      validateActionList(hooks, `hooks.${key}`, errors);
    }
  });

  return {
    valid: errors.length === 0,
    errors,
  };
}
