/**
 * Shared script execution sandbox.
 *
 * SECURITY: This sandbox is defense-in-depth, NOT a true security boundary.
 * The Proxy + `with()` approach cannot fully isolate code running in the same
 * JavaScript realm as the page. The real protections are:
 *   1. The `allowEvaluate` gate on each Environment (defaults to false)
 *   2. The server URL allowlist in `loadTaskFromHash`
 * Do not rely on this sandbox alone for untrusted input.
 */

export const SAFE_BUILTINS = [
  'Math',
  'JSON',
  'Date',
  'Array',
  'Object',
  'String',
  'Number',
  'Boolean',
  'RegExp',
  'Error',
  'Map',
  'Set',
  'Promise',
  'console',
] as const;

const FORBIDDEN_SUBSTRINGS = ['eval(', 'new Function', 'Function(', 'import('];
const FORBIDDEN_TOKENS = ['eval', 'Function', 'import', 'constructor', '__proto__'];

function tokenize(script: string): string[] {
  return script
    .replace(/\/\/.*$/gm, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split(/[^a-zA-Z0-9_$]+/)
    .filter(Boolean);
}

export interface SandboxOptions {
  /** Object from which to read builtin globals (e.g. window or globalThis). */
  globalObj: any;
  /** If true, expose window/document in the sandbox (disables effective isolation). */
  allowDOM?: boolean;
  /** Window object for DOM access. */
  window?: any;
  /** Document object for DOM access. */
  document?: any;
}

export function createSandbox(ctx: any, options: SandboxOptions): Record<string | symbol, any> {
  const target: Record<string | symbol, any> = { ctx };
  Object.setPrototypeOf(target, null);

  for (const name of SAFE_BUILTINS) {
    const value = options.globalObj[name];
    if (value !== undefined) {
      // console is intentionally not frozen so tests and runtime logging can spy/replace it
      target[name] = name === 'console' ? value : Object.freeze(value);
    }
  }

  if (options.allowDOM) {
    // eslint-disable-next-line no-console
    console.warn(
      'Security warning: allowEvaluateDOM is enabled; evaluate exposes window/document and the sandbox is effectively disabled.',
    );
    target.window = options.window;
    target.document = options.document;
  }

  const whitelist = new Set(Object.getOwnPropertyNames(target));

  const sandbox = new Proxy(Object.freeze(target), {
    has: (_target, prop) => prop !== Symbol.unscopables,
    get: (_target, prop) => {
      if (prop === Symbol.unscopables) return undefined;
      if (prop === 'ctx') return ctx;
      const name = String(prop);
      if (name === 'constructor' || name === '__proto__') {
        throw new Error(`Security error: access to "${name}" is not allowed`);
      }
      if (!whitelist.has(name)) {
        throw new Error(`Security error: access to "${name}" is not allowed`);
      }
      return (target as any)[prop];
    },
  });

  return Object.freeze(sandbox);
}

export async function evaluateInSandbox(
  script: string,
  ctx: any,
  _args: any[] | undefined,
  options: SandboxOptions,
): Promise<any> {
  for (const pattern of FORBIDDEN_SUBSTRINGS) {
    if (script.includes(pattern)) {
      throw new Error(`Security error: forbidden pattern "${pattern}"`);
    }
  }

  for (const token of tokenize(script)) {
    if (FORBIDDEN_TOKENS.includes(token)) {
      throw new Error(`Security error: forbidden token "${token}"`);
    }
  }

  const sandbox = createSandbox(ctx, options);

  const wrapper = new Function(
    'sandbox',
    `with(sandbox) { return (async function() { "use strict"; ${script} })(); }`,
  );
  return wrapper.call(undefined, sandbox);
}
