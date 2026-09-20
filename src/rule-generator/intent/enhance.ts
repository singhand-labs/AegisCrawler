import type { Rule } from '../types';
import type { IntentCandidate } from './intent-templates';
import { classifyIntent, templates } from './intent-templates';

export function enhanceWithIntent(rule: Rule, candidate: IntentCandidate): Rule {
  const type = classifyIntent(candidate);
  const template = templates.find((t) => t.type === type);
  if (!template) {
    return rule;
  }
  return template.apply(rule, candidate);
}
