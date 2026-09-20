const esbuild = require('esbuild');
const fs = require('fs');
const path = require('path');

const projectRoot = path.join(__dirname, '..');
const srcDir = path.join(projectRoot, 'extension/src');
const outDir = path.join(projectRoot, 'dist/extension');

const entries = [
  { entry: path.join(srcDir, 'background.ts'), out: path.join(outDir, 'background.js') },
  { entry: path.join(srcDir, 'content.ts'), out: path.join(outDir, 'content.js') },
  { entry: path.join(srcDir, 'popup.ts'), out: path.join(outDir, 'popup.js') },
  { entry: path.join(srcDir, 'intent/intent-page.ts'), out: path.join(outDir, 'intent/intent-page.js') },
  { entry: path.join(srcDir, 'intent/replay-runner.ts'), out: path.join(outDir, 'intent/replay-runner.js') },
];

const staticFiles = [
  { from: path.join(srcDir, 'popup.html'), to: path.join(outDir, 'popup.html') },
  { from: path.join(srcDir, 'popup.css'), to: path.join(outDir, 'popup.css') },
  { from: path.join(srcDir, 'intent/intent-page.html'), to: path.join(outDir, 'intent/intent-page.html') },
  { from: path.join(srcDir, 'intent/intent-page.css'), to: path.join(outDir, 'intent/intent-page.css') },
  { from: path.join(projectRoot, 'extension/manifest.json'), to: path.join(outDir, 'manifest.json') },
];

function copyDirRecursive(src, dest) {
  if (!fs.existsSync(src)) {
    console.warn(`Skipping missing directory: ${src}`);
    return;
  }
  fs.mkdirSync(dest, { recursive: true });
  for (const entry of fs.readdirSync(src, { withFileTypes: true })) {
    const srcPath = path.join(src, entry.name);
    const destPath = path.join(dest, entry.name);
    if (entry.isDirectory()) {
      copyDirRecursive(srcPath, destPath);
    } else {
      fs.copyFileSync(srcPath, destPath);
      console.log(`Copied: ${destPath}`);
    }
  }
}

async function build() {
  fs.mkdirSync(outDir, { recursive: true });

  for (const { entry, out } of entries) {
    await esbuild.build({
      entryPoints: [entry],
      bundle: true,
      outfile: out,
      format: 'iife',
      target: 'es2020',
      platform: 'browser',
    });
    console.log(`Built: ${out}`);
  }

  for (const { from, to } of staticFiles) {
    fs.copyFileSync(from, to);
    console.log(`Copied: ${to}`);
  }

  // Copy all extension icons, including PNG variants required by the Chrome Web Store.
  copyDirRecursive(path.join(projectRoot, 'extension/icons'), path.join(outDir, 'icons'));
}

build().catch((err) => {
  console.error(err);
  process.exit(1);
});
