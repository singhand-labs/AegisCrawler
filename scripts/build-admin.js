#!/usr/bin/env node
"use strict";

/**
 * Build the React admin UI and copy the production assets into the Go server
 * embedding directory (server/web/admin).
 *
 * This script is cross-platform (Windows, macOS, Linux) and avoids shell
 * commands such as `cp -r` or `mkdir -p` that fail on Windows when npm uses
 * cmd.exe as the default script shell.
 */

const fs = require("fs");
const path = require("path");
const { spawnSync } = require("child_process");

const rootDir = path.resolve(__dirname, "..");
const adminDir = path.join(rootDir, "admin-ui");
const targetDir = path.join(rootDir, "server", "web", "admin");

function run(command, cwd) {
  // Run through a shell so that `npm` resolves to npm.cmd on Windows without
  // needing to spawn a .cmd file directly (which recent Node versions block).
  // The command is passed as a single string, avoiding the deprecation warning
  // about passing an args array together with shell: true.
  const result = spawnSync(command, {
    cwd,
    stdio: "inherit",
    shell: true,
    windowsHide: true,
  });
  if (result.error) {
    console.error(result.error);
    process.exit(1);
  }
  if (result.status !== 0) {
    process.exit(result.status || 1);
  }
}

function cleanTarget() {
  if (!fs.existsSync(targetDir)) {
    fs.mkdirSync(targetDir, { recursive: true });
    return;
  }

  // Remove existing files while preserving the .gitkeep marker so that the
  // directory stays tracked and go:embed continues to match when no build has
  // been run yet.
  for (const entry of fs.readdirSync(targetDir)) {
    if (entry === ".gitkeep") {
      continue;
    }
    const fullPath = path.join(targetDir, entry);
    fs.rmSync(fullPath, { recursive: true, force: true });
  }
}

function copyDir(src, dest) {
  fs.mkdirSync(dest, { recursive: true });
  for (const entry of fs.readdirSync(src, { withFileTypes: true })) {
    const srcPath = path.join(src, entry.name);
    const destPath = path.join(dest, entry.name);
    if (entry.isDirectory()) {
      copyDir(srcPath, destPath);
    } else {
      fs.copyFileSync(srcPath, destPath);
    }
  }
}

function main() {
  // Use a project-local npm cache to avoid EACCES errors when the user's
  // global ~/.npm/_cacache contains root-owned files (a known issue caused
  // by prior `sudo npm` usage). The local cache is gitignored.
  run("npm ci --cache .npmcache", adminDir);
  run("npm run build", adminDir);

  cleanTarget();
  copyDir(path.join(adminDir, "dist"), targetDir);

  console.log(`Admin UI built and copied to ${path.relative(rootDir, targetDir)}`);
}

main();
