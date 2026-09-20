const esbuild = require('esbuild');
const fs = require('fs');
const path = require('path');

const entry = path.join(__dirname, '../src/scriptcat-engine/userscript-entry.ts');
const packageJson = require(path.join(__dirname, '../package.json'));
const outFile = path.join(__dirname, '../dist', `aegiscrawler-${packageJson.version}.user.js`);

async function build() {
  const result = await esbuild.build({
    entryPoints: [entry],
    bundle: true,
    write: false,
    format: 'iife',
    target: 'es2020',
    platform: 'browser',
    globalName: '__OpenCrawlerExecutor',
    footer: { js: '__OpenCrawlerExecutor.boot();' },
  });

  const header = `// ==UserScript==
// @name         AegisCrawler PageResearch Agent Executor
// @namespace    https://github.com/singhand-labs/AegisCrawler
// @version      ${packageJson.version}
// @description  Universal rule executor for AegisCrawler data collection
// @author       Singhand Labs <zy@singhand.com>
// @match        *://*/*
// @grant        GM_openInTab
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        unsafeWindow
// Userscript managers (ScriptCat/Tampermonkey) refuse cross-origin
// GM_xmlhttpRequest without an @connect declaration. The runtime target is
// still constrained fail-closed by the trusted-origin allowlist checked on
// the task descriptor's serverUrl; @connect is only the manager-side gate.
// @connect      *
// @run-at       document-end
// @noframes
// ==/UserScript==

`;

  fs.mkdirSync(path.dirname(outFile), { recursive: true });
  fs.writeFileSync(outFile, header + result.outputFiles[0].text, 'utf8');
  console.log(`Built: ${outFile}`);
}

build().catch((err) => {
  console.error(err);
  process.exit(1);
});
