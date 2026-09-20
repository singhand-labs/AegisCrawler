import * as yaml from 'js-yaml';
import type { Rule } from '../types';

export function writeYaml(rule: Rule): string {
  return yaml.dump(rule, {
    indent: 2,
    lineWidth: -1,
    noRefs: true,
    sortKeys: false,
  });
}
