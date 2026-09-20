import type { Target, RetryConfig } from './types';

export function interpolate(value: any, ctx: { variables: Record<string, any>; extracted: Record<string, any>; evaluated: Record<string, any>; captured: Record<string, any>; loopIndex?: number; loopItem?: any; page?: { url: string; title: string } }): any {
  if (value === null || value === undefined) return value;
  if (typeof value === 'string') {
    const trimmed = value.trim();
    const exactMatch = trimmed.match(/^\{\{([^}]+)\}\}$/);
    if (exactMatch) {
      // A placeholder that occupies the whole value should preserve the
      // resolved value's original type (string, number, array, object, etc.).
      return resolveExpression(exactMatch[1].trim(), ctx) ?? '';
    }
    return value.replace(/\{\{([^}]+)\}\}/g, (_, expr) => {
      return String(resolveExpression(expr.trim(), ctx) ?? '');
    });
  }
  if (Array.isArray(value)) {
    return value.map((v) => interpolate(v, ctx));
  }
  if (typeof value === 'object') {
    const out: Record<string, any> = {};
    for (const [k, v] of Object.entries(value)) {
      out[k] = interpolate(v, ctx);
    }
    return out;
  }
  return value;
}

export function resolveExpression(expr: string, ctx: { variables: Record<string, any>; extracted: Record<string, any>; evaluated: Record<string, any>; captured: Record<string, any>; loopIndex?: number; loopItem?: any; page?: { url: string; title: string } }): any {
  // Support simple paths: variables.foo, extracted.items, loopItem, loopIndex
  const parts = expr.split('.');
  let rootName = parts[0];
  let root: any;
  if (rootName === 'loopIndex') return ctx.loopIndex;
  if (rootName === 'loopItem') root = ctx.loopItem;
  else if (rootName === 'variables') root = ctx.variables;
  else if (rootName === 'extracted') root = ctx.extracted;
  else if (rootName === 'evaluated') root = ctx.evaluated;
  else if (rootName === 'captured') root = ctx.captured;
  else if (rootName === 'page') root = ctx.page;
  else {
    // Try variables first, then extracted
    root = ctx.variables[rootName] ?? ctx.extracted[rootName] ?? ctx.evaluated[rootName];
  }
  if (parts.length <= 1) return root;
  parts.shift();
  return parts.reduce((obj, key) => (obj == null ? undefined : obj[key]), root);
}

export function resolveSelectorAlias(target: Target, selectors?: Record<string, Target>): Target {
  if (typeof target.$ref === 'string' && target.$ref.trim().length > 0 && selectors) {
    const ref = selectors[target.$ref];
    if (!ref) throw new Error(`Selector alias not found: ${target.$ref}`);
    return { ...ref, ...target, $ref: undefined };
  }
  return target;
}

export function parseRetry(
  retry: number | RetryConfig | undefined,
  defaults: number,
  maxCeiling?: number,
): RetryConfig {
  let parsed: RetryConfig;
  if (retry === undefined) parsed = { maxAttempts: defaults };
  else if (typeof retry === 'number') parsed = { maxAttempts: retry };
  else parsed = { ...retry, maxAttempts: retry.maxAttempts ?? defaults };
  if (typeof maxCeiling === 'number') {
    parsed.maxAttempts = Math.min(parsed.maxAttempts, maxCeiling);
  }
  return parsed;
}

export function randomBetween(range: [number, number]): number {
  return Math.floor(range[0] + Math.random() * (range[1] - range[0]));
}

export function toNumber(value: any): number | null {
  if (typeof value === 'number') return value;
  if (typeof value === 'string') {
    const match = value.match(/[+-]?\d[\d,\s]*(?:\.\d+)?/);
    if (!match) return null;
    const normalized = match[0].replace(/[,\s\u00a0\u202f]/g, '');
    const parsed = Number(normalized);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
