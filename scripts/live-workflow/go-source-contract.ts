/** Read a named package-level Go raw-string constant without depending on
 * which declaration follows it or on blank-line formatting. */
export function readGoRawStringConstant(source: string, name: string): string {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) {
    throw new Error('Go constant name must be an identifier');
  }
  const declaration = new RegExp(`\\bconst\\s+${name}\\s*=\\s*\``).exec(source);
  if (!declaration) throw new Error(`could not find Go raw-string constant ${name}`);
  const start = declaration.index + declaration[0].length;
  const end = source.indexOf('`', start);
  if (end < 0) throw new Error(`Go raw-string constant ${name} is unterminated`);
  return source.slice(start, end);
}
