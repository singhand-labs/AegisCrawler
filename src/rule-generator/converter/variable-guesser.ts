import type { PageAgentRecording } from '../types';

export interface GuessResult {
  variables: Record<string, string>;
  templatize(text: string): string;
}

export function guessVariables(recording: PageAgentRecording): GuessResult {
  const variables: Record<string, string> = {};
  for (const urlParam of extractUrlParams(recording.meta.startUrl)) {
    variables[urlParam.name] = urlParam.value;
  }

  const inputTexts = recording.events
    .filter((e): e is Extract<typeof e, { type: 'inputText' }> => e.type === 'inputText')
    .map((e) => e.text)
    .filter(Boolean);

  const commonInput = findMostCommon(inputTexts);
  if (commonInput && looksLikeVariable(commonInput)) {
    variables.keyword = commonInput;
  }

  function templatize(text: string): string {
    // Sort by descending value length so longer matches are replaced first,
    // preventing substring corruption (e.g. "cat" corrupting "catch").
    const entries = Object.entries(variables).sort(([, a], [, b]) => b.length - a.length);
    for (const [name, value] of entries) {
      if (text === value) return `{{${name}}}`;
      const encoded = encodeURIComponent(value);
      const formEncoded = encoded.replace(/%20/g, '+');
      for (const candidate of new Set([value, encoded, formEncoded])) {
        text = text.split(candidate).join(`{{${name}}}`);
      }
    }
    return text;
  }

  return { variables, templatize };
}

// Common query parameter names that represent a user-entered search keyword.
// These are checked first so that URLs like Baidu's `?ie=utf-8&wd=opencrawler`
// produce a `keyword` variable instead of picking `ie`.
const SEARCH_PARAM_NAMES = new Set(['q', 'wd', 'keyword', 'query', 'search', 'kw', 'k']);

// M-2: parameter names that may carry session tokens, API keys, or OAuth
// credentials. Extracting these as rule variables would leak real secrets
// into the generated rule file. Mirrors the sensitive-param allowlist in
// dom-serializer.ts and background.ts.
const SENSITIVE_PARAM_RE = /(?:password|passwd|pwd|secret|token|api[-_]?key|auth(?:orization)?|cookie|credential|access[-_]?token|refresh[-_]?token|session(?:id|key)?|sid|jwt|code|otp|mfa[-_]?code)/i;

function extractUrlParams(url: string): Array<{ name: string; value: string }> {
  try {
    const parsed = new URL(url);
    const result: Array<{ name: string; value: string }> = [];
    // First pass: prefer known search-related parameters (q, wd, keyword, ...).
    for (const [key, value] of parsed.searchParams.entries()) {
      if (SENSITIVE_PARAM_RE.test(key)) continue; // M-2: skip sensitive params
      if (value && SEARCH_PARAM_NAMES.has(key.toLowerCase()) && looksLikeVariable(value)) {
        result.push({ name: 'keyword', value });
      }
    }
    // Second pass: other values that look like meaningful business variables.
    // Require both key and value to be reasonably long to avoid tracking
    // internal flags such as Baidu's `ie=utf-8`, `f=8`, or `tn=baidu`.
    for (const [key, value] of parsed.searchParams.entries()) {
      if (SENSITIVE_PARAM_RE.test(key)) continue; // M-2: skip sensitive params
      if (
        value &&
        !SEARCH_PARAM_NAMES.has(key.toLowerCase()) &&
        looksLikeVariable(value) &&
        key.length >= 3 &&
        value.length >= 3 &&
        isSafeVariableName(key)
      ) {
        result.push({ name: key, value });
      }
    }
    return result;
  } catch {
    // ignore
  }
  return [];
}

/** Ensure variable names are safe identifiers (no template metacharacters). */
function isSafeVariableName(name: string): boolean {
  return /^[a-zA-Z][a-zA-Z0-9_]*$/.test(name);
}

function looksLikeVariable(value: string): boolean {
  if (value.length < 1 || value.length > 50) return false;
  // 中文搜索词、字母数字混合、纯数字 ID 都视为可能参数
  return /^[\u4e00-\u9fa5a-zA-Z0-9_\-@.]+(?: [\u4e00-\u9fa5a-zA-Z0-9_\-@.]+)*$/.test(value);
}

function findMostCommon(arr: string[]): string | null {
  if (arr.length === 0) return null;
  const counts = new Map<string, number>();
  for (const item of arr) {
    counts.set(item, (counts.get(item) ?? 0) + 1);
  }
  let best: string | null = null;
  let bestCount = 0;
  for (const [item, count] of counts.entries()) {
    if (count > bestCount) {
      best = item;
      bestCount = count;
    }
  }
  return best;
}
