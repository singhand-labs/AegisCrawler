const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');
const AdmZip = require('adm-zip');

const projectRoot = path.join(__dirname, '..');
const packageJson = require(path.join(projectRoot, 'package.json'));
const distDir = path.join(projectRoot, 'dist/extension');
const outDir = path.join(projectRoot, 'dist');
const outFile = path.join(outDir, `aegiscrawler-extension-${packageJson.version}.zip`);

async function main() {
  console.log('Building extension...');
  execSync('npm run build:extension', { stdio: 'inherit', cwd: projectRoot });

  if (!fs.existsSync(distDir)) {
    throw new Error(`Extension output directory not found: ${distDir}`);
  }

  fs.mkdirSync(outDir, { recursive: true });

  const zip = new AdmZip();
  zip.addLocalFolder(distDir, '');
  zip.writeZip(outFile);

  const stats = fs.statSync(outFile);
  console.log(`Created: ${outFile} (${stats.size} bytes)`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
