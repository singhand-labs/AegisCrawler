import * as fs from 'fs';
import * as path from 'path';
import { convertToYaml } from '../src/rule-generator';
import type { PageAgentRecording } from '../src/rule-generator/types';

export function run(args: string[]): number {
  if (args.length < 2) {
    throw new Error('Usage: ts-node scripts/generate-rule.ts <recording.json> <output.yaml>');
  }

  const [inputPath, outputPath] = args;
  const fullInput = path.resolve(inputPath);
  const fullOutput = path.resolve(outputPath);

  if (!fs.existsSync(fullInput)) {
    throw new Error(`Input file not found: ${fullInput}`);
  }

  const recording: PageAgentRecording = JSON.parse(fs.readFileSync(fullInput, 'utf-8'));
  const yaml = convertToYaml(recording, { ruleIdPrefix: 'recorded' });

  fs.mkdirSync(path.dirname(fullOutput), { recursive: true });
  fs.writeFileSync(fullOutput, yaml, 'utf-8');
  console.log(`Generated rule: ${fullOutput}`);
  return 0;
}

function main() {
  try {
    const code = run(process.argv.slice(2));
    if (code !== 0) {
      process.exit(code);
    }
  } catch (err) {
    console.error(err instanceof Error ? err.message : String(err));
    process.exit(1);
  }
}

if (path.resolve(process.argv[1] ?? '') === path.resolve(__filename)) {
  main();
}
