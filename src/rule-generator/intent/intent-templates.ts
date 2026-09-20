import type { Rule, Action, Target } from '../types';
import type { ExtractAction } from '../../rule-engine/types/action';

export interface IntentCandidate {
  id: string;
  label: string;
  description: string;
  confidence: number;
  suggestedVariables?: string[];
}

export type IntentType = 'list-collection' | 'search-pagination' | 'form-submit' | 'custom';

export interface IntentTemplate {
  type: IntentType;
  matchLabels: string[];
  apply: (rule: Rule, candidate: IntentCandidate) => Rule;
}

export function classifyIntent(candidate: IntentCandidate): IntentType {
  const label = candidate.label.toLowerCase();
  if (label.includes('搜索') || label.includes('翻页') || label.includes('keyword')) {
    return 'search-pagination';
  }
  if (label.includes('表单') || label.includes('提交') || label.includes('submit')) {
    return 'form-submit';
  }
  if (label.includes('列表') || label.includes('商品') || label.includes('采集')) {
    return 'list-collection';
  }
  return 'custom';
}

function findLastStepIndex(steps: Action[], actionType: string): number {
  for (let i = steps.length - 1; i >= 0; i--) {
    if (steps[i].action === actionType) {
      return i;
    }
  }
  return -1;
}

export const listCollectionTemplate: IntentTemplate = {
  type: 'list-collection',
  matchLabels: ['列表', '商品', '采集'],
  apply: (rule, _candidate) => {
    const enhanced: Rule = JSON.parse(JSON.stringify(rule));
    const lastClickIndex = findLastStepIndex(enhanced.steps, 'click');
    if (lastClickIndex >= 0) {
      const clickStep = enhanced.steps[lastClickIndex];
      const clickTarget = clickStep.action === 'click' ? clickStep.target : undefined;
      const extractAction: ExtractAction = {
        action: 'extract',
        name: 'itemData',
        target: clickTarget ?? { selector: 'body' },
        multiple: true,
        fields: {
          title: { selector: 'h1, h2, .title', type: 'text', trim: true },
          price: { selector: '.price', type: 'text', trim: true },
          url: { selector: 'a', type: 'attr', attr: 'href', resolve: true },
        },
      };
      enhanced.steps.splice(lastClickIndex + 1, 0, extractAction);
    }
    enhanced.variables = {
      ...enhanced.variables,
      maxItems: 10,
    };
    return enhanced;
  },
};

export const searchPaginationTemplate: IntentTemplate = {
  type: 'search-pagination',
  matchLabels: ['搜索', '翻页', 'keyword'],
  apply: (rule, _candidate) => {
    const enhanced: Rule = JSON.parse(JSON.stringify(rule));
    // Preserve a keyword already templatized by the baseline converter; only
    // capture a new default from the recorded type value when one is absent.
    const existingKeyword = enhanced.variables?.keyword;
    let recordedKeyword = typeof existingKeyword === 'string' ? existingKeyword : '';
    enhanced.steps.forEach((step) => {
      if (step.action === 'type' && typeof step.value === 'string' && step.value.length > 0) {
        if (!recordedKeyword && step.value !== '{{keyword}}') {
          recordedKeyword = step.value;
        }
        step.value = '{{keyword}}';
      }
    });
    enhanced.variables = {
      ...enhanced.variables,
      keyword: recordedKeyword,
      pages: 1,
    };

    let lastSubmitIdx = -1;
    enhanced.steps.forEach((step, i) => {
      if (step.action === 'click' || step.action === 'pressKey') {
        lastSubmitIdx = i;
      }
      if (step.action === 'type' && step.submit) {
        lastSubmitIdx = i;
      }
    });

    // If the submit is immediately followed by a navigate (full-page form
    // submission), the extraction must happen on the result page, i.e. after
    // the navigate step.
    let insertAfterIdx = lastSubmitIdx;
    if (lastSubmitIdx >= 0) {
      for (let i = lastSubmitIdx + 1; i < enhanced.steps.length; i++) {
        if (enhanced.steps[i].action === 'navigate') {
          insertAfterIdx = i;
        } else {
          break;
        }
      }
    }

    const extractStep: ExtractAction = {
      action: 'extract',
      name: 'searchResults',
      target: { selector: '.result, .c-container' },
      multiple: true,
      fields: {
        title: { selector: 'h3 a, .t', type: 'text', trim: true },
        url: { selector: 'h3 a, .t', type: 'attr', attr: 'href', resolve: true },
        summary: { selector: '.c-abstract, .content-right_8Zs40', type: 'text', trim: true },
      },
    };

    if (insertAfterIdx >= 0) {
      enhanced.steps.splice(insertAfterIdx + 1, 0, { action: 'waitForTimeout', ms: 1500 }, extractStep);
    } else {
      enhanced.steps.push(extractStep);
    }

    enhanced.steps.push(
      { action: 'sendResult', payload: { searchResults: '{{extracted.searchResults}}' }, immediate: true },
      { action: 'updateStatus', status: 'done', message: '搜索采集完成' },
    );

    return enhanced;
  },
};

export const formSubmitTemplate: IntentTemplate = {
  type: 'form-submit',
  matchLabels: ['表单', '提交', 'submit'],
  apply: (rule, _candidate) => {
    const enhanced: Rule = JSON.parse(JSON.stringify(rule));
    enhanced.variables = {
      ...enhanced.variables,
      formData: {},
    };
    return enhanced;
  },
};

export const templates: IntentTemplate[] = [
  listCollectionTemplate,
  searchPaginationTemplate,
  formSubmitTemplate,
];
