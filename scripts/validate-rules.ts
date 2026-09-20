import Ajv from 'ajv';
import addFormats from 'ajv-formats';
import * as fs from 'fs';
import * as path from 'path';
import * as yaml from 'js-yaml';

const schema = JSON.parse(fs.readFileSync('src/rule-engine/schemas/action.schema.json', 'utf8'));
const ajv = new Ajv({ allErrors: true, strict: false });
addFormats(ajv);
const validate = ajv.compile(schema);

const examplesDir = 'examples';
const files = fs.readdirSync(examplesDir).filter(f => f.endsWith('.yaml') || f.endsWith('.yml'));

let failed = false;
for (const file of files) {
  const content = fs.readFileSync(path.join(examplesDir, file), 'utf8');
  // js-yaml v5 defaults to DEFAULT_SAFE_SCHEMA, so yaml.load is safe here.
  // If js-yaml is ever downgraded to v3, switch to yaml.safeLoad().
  const doc = yaml.load(content);

  // Validate as a single-rule registry against the full schema.
  const wrapper = { version: '0.0.0', rules: [doc] };
  const valid = validate(wrapper);
  if (valid) {
    console.log(`✓ ${file}`);
  } else {
    failed = true;
    console.error(`✗ ${file}`);
    console.error(validate.errors);
  }
}

if (failed) {
  process.exit(1);
}
console.log(`\nAll ${files.length} rule files validated.`);
