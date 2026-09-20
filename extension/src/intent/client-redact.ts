// Client-side redaction patterns mirroring server/internal/llm/redact/redact.go.
// Defense in depth — the server still runs redact.Any on all inputs.

const REDACTED = '[REDACTED]';

const patterns: RegExp[] = [
  // HTTP credential headers (mirrors the server's header redaction line).
  /\b(authorization|proxy-authorization|cookie|set-cookie)\s*[:=]\s*[^\r\n]+/gi,
  // Key-value style secrets (password, token, api key, authorization header).
  /(password|passwd|pwd|secret|token|api[_-]?key|authorization)\s*[:=]\s*["']?[^"'\s<>]+["']?/gi,
  // Bank card numbers (16-19 digits).
  /\b\d{16,19}\b/g,
  // Email addresses.
  /\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/g,
];

export function redactString(s: string): string {
  let result = s;
  for (const p of patterns) {
    result = result.replace(p, REDACTED);
  }
  return result;
}

export function hasSensitiveContent(s: string): boolean {
  return redactString(s) !== s;
}
