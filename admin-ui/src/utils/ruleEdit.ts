import type { Action, Rule } from '../types/rule';

export type ActionPath = (string | number)[];

function deepClone<T>(value: T): T {
  if (typeof structuredClone === 'function') {
    return structuredClone(value) as T;
  }
  return JSON.parse(JSON.stringify(value)) as T;
}

export function cloneRule(rule: Rule): Rule {
  return deepClone(rule);
}

export function cloneAction(action: Action, overrides?: Partial<Action>): Action {
  return { ...deepClone(action), ...overrides };
}

export function getValueByPath<T = unknown>(obj: unknown, path: ActionPath): T | undefined {
  let current: unknown = obj;
  for (const key of path) {
    if (current === null || current === undefined) return undefined;
    current = (current as Record<string | number, unknown>)[key];
  }
  return current as T;
}

function setValueByPath<T = unknown>(obj: unknown, path: ActionPath, value: T): void {
  if (path.length === 0) return;
  let current = obj as Record<string | number, unknown>;
  for (let i = 0; i < path.length - 1; i++) {
    const key = path[i];
    current = current[key] as Record<string | number, unknown>;
  }
  current[path[path.length - 1]] = value;
}

interface ArrayParent {
  array: Action[];
  parentPath: ActionPath;
  index: number;
}

function getArrayParent(rule: Rule, path: ActionPath): ArrayParent | null {
  if (path.length === 0) return null;
  const parentPath = path.slice(0, -1);
  const lastKey = path[path.length - 1];
  if (typeof lastKey !== 'number') return null;
  const array = getValueByPath<Action[]>(rule, parentPath);
  if (!Array.isArray(array)) return null;
  return { array, parentPath, index: lastKey };
}

export function getActionByPath(rule: Rule, path: ActionPath): Action | undefined {
  return getValueByPath<Action>(rule, path);
}

export function updateActionByPath(rule: Rule, path: ActionPath, action: Action): Rule {
  const next = cloneRule(rule);
  setValueByPath(next, path, action);
  return next;
}

export function removeActionByPath(rule: Rule, path: ActionPath): Rule {
  const parent = getArrayParent(rule, path);
  if (!parent) return rule;
  const next = cloneRule(rule);
  const array = getValueByPath<Action[]>(next, parent.parentPath);
  if (!Array.isArray(array)) return rule;
  array.splice(parent.index, 1);
  return next;
}

export function insertActionByPath(
  rule: Rule,
  path: ActionPath,
  position: 'before' | 'after',
  action: Action,
): Rule {
  const parent = getArrayParent(rule, path);
  if (!parent) return rule;
  const next = cloneRule(rule);
  const array = getValueByPath<Action[]>(next, parent.parentPath);
  if (!Array.isArray(array)) return rule;
  const insertIndex = position === 'before' ? parent.index : parent.index + 1;
  array.splice(insertIndex, 0, action);
  return next;
}

export function appendActionToContainer(
  rule: Rule,
  path: ActionPath,
  action: Action,
  branchKey: string = 'steps',
): Rule {
  const container = getActionByPath(rule, path);
  if (!container) return rule;

  // Determine which array property to append to.
  const record = container as Record<string, unknown>;
  const candidates = ['steps', 'then', branchKey].filter((k) => Array.isArray(record[k]));
  if (candidates.length === 0) return rule;
  const targetKey = candidates[0];

  const childPath = [...path, targetKey];
  const array = getValueByPath<Action[]>(rule, childPath);
  if (!Array.isArray(array)) return rule;

  const next = cloneRule(rule);
  const targetArray = getValueByPath<Action[]>(next, childPath);
  if (!Array.isArray(targetArray)) return rule;
  targetArray.push(action);
  return next;
}

export function moveActionByPath(rule: Rule, path: ActionPath, direction: 'up' | 'down'): Rule {
  const parent = getArrayParent(rule, path);
  if (!parent) return rule;
  const next = cloneRule(rule);
  const array = getValueByPath<Action[]>(next, parent.parentPath);
  if (!Array.isArray(array)) return rule;

  const newIndex = direction === 'up' ? parent.index - 1 : parent.index + 1;
  if (newIndex < 0 || newIndex >= array.length) return rule;

  const [moved] = array.splice(parent.index, 1);
  array.splice(newIndex, 0, moved);
  return next;
}

export function getParentPath(path: ActionPath): ActionPath | null {
  return path.length > 0 ? path.slice(0, -1) : null;
}

export function pathToString(path: ActionPath): string {
  return path.map((p) => (typeof p === 'number' ? `[${p}]` : `.${p}`)).join('').replace(/^\./, '');
}

export function stringToPath(str: string): ActionPath {
  if (!str) return [];
  const parts: ActionPath = [];
  const regex = /\.(?=[^\[])|\[(\d+)\]/g;
  let match;
  let lastIndex = 0;
  while ((match = regex.exec(str)) !== null) {
    if (match.index > lastIndex) {
      parts.push(str.slice(lastIndex, match.index));
    }
    if (match[1] !== undefined) {
      parts.push(Number(match[1]));
    }
    lastIndex = regex.lastIndex;
  }
  if (lastIndex < str.length) {
    parts.push(str.slice(lastIndex));
  }
  return parts;
}
