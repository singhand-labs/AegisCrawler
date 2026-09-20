/**
 * Builds the worker page runtime bundle in memory at host startup. The bundle
 * is an esbuild IIFE (same style as scripts/build-userscript.js) that installs
 * __ocWorkerBoot / __ocWorkerResume / __ocWorkerAbort on globalThis. It is
 * injected into task pages with page.addScriptTag and never written to dist.
 *
 * The bundle is built from source only — no credentials or host configuration
 * are ever passed to esbuild, so no key material can appear in the output.
 */

import * as path from 'path';
import * as esbuild from 'esbuild';

export async function buildWorkerPageBundle(): Promise<string> {
  const result = await esbuild.build({
    entryPoints: [path.resolve(__dirname, '..', 'worker-page', 'entry.ts')],
    bundle: true,
    write: false,
    format: 'iife',
    target: 'es2020',
    platform: 'browser',
    logLevel: 'silent',
  });
  return result.outputFiles[0].text;
}
