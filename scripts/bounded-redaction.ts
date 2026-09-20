const DEFAULT_REPLACEMENT = '[REDACTED]';

export class BoundedRedactor {
  private readonly secrets: string[];
  private readonly overlapChars: number;
  private pending = '';
  private output = '';

  constructor(
    private readonly maxOutputChars: number,
    secrets: readonly string[],
    private readonly replacement = DEFAULT_REPLACEMENT,
  ) {
    if (!Number.isInteger(maxOutputChars) || maxOutputChars <= 0) {
      throw new Error('maxOutputChars must be a positive integer');
    }
    this.secrets = [...new Set(secrets.filter((secret) => secret.length > 0))]
      .sort((left, right) => right.length - left.length);
    this.overlapChars = Math.max(0, ...this.secrets.map((secret) => secret.length - 1));
  }

  append(chunk: string): void {
    const combined = this.pending + chunk;
    const { sanitized, pending } = this.sanitizePrefix(combined, this.overlapChars);
    this.pending = pending;
    this.output = this.tail(this.output + sanitized);
  }

  render(): string {
    const { sanitized } = this.sanitizePrefix(this.pending, 0);
    return this.tail(this.output + sanitized);
  }

  private sanitizePrefix(input: string, retainedChars: number): {
    sanitized: string;
    pending: string;
  } {
    const processingLimit = Math.max(0, input.length - retainedChars);
    let index = 0;
    let sanitized = '';
    while (index < processingLimit) {
      const secret = this.secrets.find((candidate) => input.startsWith(candidate, index));
      if (secret) {
        sanitized += this.replacement;
        index += secret.length;
      } else {
        sanitized += input[index];
        index += 1;
      }
    }
    return { sanitized, pending: input.slice(index) };
  }

  private tail(value: string): string {
    return value.length <= this.maxOutputChars ? value : value.slice(-this.maxOutputChars);
  }
}
