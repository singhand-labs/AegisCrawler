#!/usr/bin/env node
"use strict";

const fs = require("fs");
const path = require("path");
const zlib = require("zlib");
const { spawnSync } = require("child_process");

const rootDir = path.resolve(__dirname, "..");
const outputPath = path.join(rootDir, "src", "rule-engine", "schemas", "action.schema.json");
const serverOutputPath = path.join(rootDir, "server", "internal", "rule", "action.schema.json.gz");
const cliPath = path.join(rootDir, "node_modules", "typescript-json-schema", "bin", "typescript-json-schema");

const result = spawnSync(
  process.execPath,
  [cliPath, "tsconfig.json", "RuleRegistry", "--required", "--refs", "-o", outputPath],
  { cwd: rootDir, stdio: "inherit" },
);

if (result.error) {
  console.error(result.error);
  process.exit(1);
}
if (result.status !== 0) {
  process.exit(result.status || 1);
}

// The upstream CLI writes two trailing newlines. Normalize generated output so
// `git diff --check` and the CI drift check remain deterministic.
const schema = fs.readFileSync(outputPath, "utf8").trimEnd();
fs.writeFileSync(outputPath, `${schema}\n`);
fs.writeFileSync(serverOutputPath, zlib.gzipSync(Buffer.from(`${schema}\n`), { level: 9, mtime: 0 }));
