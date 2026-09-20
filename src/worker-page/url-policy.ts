/**
 * Pure URL/domain policy helpers shared by the worker page runtime and the
 * Node worker host. Mirrors the replay trust model in
 * extension/src/background.ts (assertEntryUrlDomain) and
 * extension/src/intent/replay-runner.ts (resume URL checks).
 *
 * No DOM or Node dependencies — safe to bundle for the page and to import
 * from the host.
 */

import type { Rule } from '../scriptcat-engine/types';

export function ruleDomainList(rule: Pick<Rule, 'domain'>): string[] {
  const d = rule.domain;
  return Array.isArray(d) ? d.map(String) : [String(d)];
}

export function hostMatchesDomain(host: string, domains: string[]): boolean {
  return domains.some((d) => host === d || host.endsWith('.' + d));
}

// M-1: a domain entry must look like a registered name (at least one dot,
// so bare TLDs like 'com' / 'net' are rejected). Mirrors background.ts H-5,
// replay-runner.ts L-1, and scriptcat-engine's navigate validation.
export function isValidDomainEntry(d: string): boolean {
  // Loopback exemption: 'localhost' exact-matches only the loopback host and
  // '*.localhost' also resolves to loopback in Chromium, so it adds no
  // public-site replay surface. Public dotless TLDs stay rejected.
  if (d === 'localhost') return true;
  return d.length >= 3 && d.includes('.') && !d.startsWith('.') && !d.endsWith('.');
}

/**
 * Pre-navigation entry validation (port of extension background.ts
 * assertEntryUrlDomain): the URL must be a valid absolute HTTP(S) URL whose
 * hostname is covered by rule.domain.
 */
export function assertEntryUrlAllowed(entryUrl: string, rule: Pick<Rule, 'domain'>): URL {
  let parsed: URL;
  try {
    parsed = new URL(entryUrl);
  } catch {
    throw new Error(`Entry URL is not a valid URL: ${entryUrl}`);
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error(`Entry URL protocol ${parsed.protocol} is not allowed`);
  }
  const domains = ruleDomainList(rule);
  // M-1: reject overly-broad domain entries before the suffix check.
  const invalid = domains.filter((d) => !isValidDomainEntry(d));
  if (invalid.length > 0) {
    throw new Error(`rule.domain contains overly-broad entries [${invalid.join(', ')}]; refusing to run`);
  }
  if (!hostMatchesDomain(parsed.hostname, domains)) {
    throw new Error(`Entry URL host ${parsed.hostname} is not in rule.domain [${domains.join(', ')}]`);
  }
  return parsed;
}

/** True when a committed navigation target stays inside rule.domain. */
export function isNavigationAllowed(rawUrl: string, rule: Pick<Rule, 'domain'>): boolean {
  let parsed: URL;
  try {
    parsed = new URL(rawUrl);
  } catch {
    return false;
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return false;
  return hostMatchesDomain(parsed.hostname, ruleDomainList(rule));
}

/** Reduce a URL to origin + pathname; query/fragment can carry secrets. */
export function normalizeRunUrl(rawUrl: string, base?: string): string {
  const url = new URL(rawUrl, base);
  return `${url.origin}${url.pathname}`;
}

export function pathnameMatches(actual: string, expected: string): boolean {
  if (actual === expected) return true;
  const childPrefix = expected.endsWith('/') ? expected : `${expected}/`;
  return actual.startsWith(childPrefix);
}
