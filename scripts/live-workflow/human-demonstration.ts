export const LIVE_WORKFLOW_SCENARIO_NAMES = [
  'fixture-candidates',
  'fixture-intent',
  'fixture-models',
  'apple',
  'mdn-css-grid',
  'iana-link-roundtrip',
  'baidu-search',
  'bing-search-full',
  'bing-footer-link-roundtrip',
  'bing-search-readonly',
  'books-category-matrix',
  'scrape-site-hockey',
  'rfc-editor-section-readonly',
  'duckduckgo-search-readonly',
  'sqlite-docs-roundtrip',
  'quotes-by-tag',
  'quotes-one-page',
  'quotes-renamed-contract',
  'quotes-output-projection',
  'quotes-human-choose-requirement',
  'quotes-human-input-requirement',
] as const;

export type LiveWorkflowScenarioName = (typeof LIVE_WORKFLOW_SCENARIO_NAMES)[number];

export function isQuotesLiveScenario(name: string): boolean {
  return name === 'quotes-by-tag'
    || name === 'quotes-one-page'
    || name === 'quotes-renamed-contract'
    || name === 'quotes-output-projection'
    || name === 'quotes-human-choose-requirement'
    || name === 'quotes-human-input-requirement';
}

export function isHumanRequirementScenario(name: string): boolean {
  return name === 'quotes-human-choose-requirement'
    || name === 'quotes-human-input-requirement';
}

export function parseScenarioSelection(env: NodeJS.ProcessEnv): LiveWorkflowScenarioName[] {
  const raw = (env.AEGIS_LIVE_WORKFLOW_SCENARIOS ?? '').trim();
  const names = raw
    ? raw.split(',').map((name) => name.trim()).filter(Boolean)
    : ['fixture-candidates', 'fixture-models'];
  const known = new Set<string>(LIVE_WORKFLOW_SCENARIO_NAMES);
  for (const name of names) {
    if (!known.has(name)) {
      throw new Error(
        `unknown live workflow scenario "${name}" (known: ${LIVE_WORKFLOW_SCENARIO_NAMES.join(', ')})`,
      );
    }
  }
  return [...new Set(names)] as LiveWorkflowScenarioName[];
}

export const RECORD_ONLY_APPROVAL = 'I approve one temporary real-site recording preflight';

export function validateRecordOnlyConfiguration(
  enabled: boolean,
  approval: string | undefined,
  humanDemoEnabled: boolean,
  selection: readonly LiveWorkflowScenarioName[],
): void {
  if (!enabled) return;
  if (approval !== RECORD_ONLY_APPROVAL) {
    throw new Error('record-only real-site preflight approval is required');
  }
  if (selection.length !== 1
    || !(selection[0] === 'baidu-search' || selection[0] === 'books-category-matrix')) {
    throw new Error(
      'AEGIS_LIVE_RECORD_ONLY=1 currently requires baidu-search or books-category-matrix',
    );
  }
  if (selection[0] === 'baidu-search' && !humanDemoEnabled) {
    throw new Error('Baidu AEGIS_LIVE_RECORD_ONLY=1 requires AEGIS_LIVE_HUMAN_DEMO=1');
  }
  if (selection[0] === 'books-category-matrix' && humanDemoEnabled) {
    throw new Error('Books record-only preflight requires the reviewed scripted demonstration');
  }
}

export function validateHumanDemoConfiguration(
  enabled: boolean,
  headed: boolean,
  selection: readonly LiveWorkflowScenarioName[],
  reuseRecording = false,
): void {
  if (selection.some(isQuotesLiveScenario) && !enabled && !reuseRecording) {
    throw new Error('a quotes live scenario requires AEGIS_LIVE_HUMAN_DEMO=1');
  }
  if (!enabled) return;
  if (!headed) throw new Error('AEGIS_LIVE_HUMAN_DEMO=1 requires AEGIS_LIVE_WORKFLOW_HEADED=1');
  if (selection.length !== 1) {
    throw new Error('AEGIS_LIVE_HUMAN_DEMO=1 requires exactly one live-workflow scenario');
  }
}

export function validateHumanRequirementConfiguration(
  enabled: boolean,
  headed: boolean,
  selection: readonly LiveWorkflowScenarioName[],
): void {
  if (selection.some(isHumanRequirementScenario) && !enabled) {
    throw new Error('a human requirement scenario requires AEGIS_LIVE_HUMAN_REQUIREMENT=1');
  }
  if (!enabled) return;
  if (!headed) {
    throw new Error('AEGIS_LIVE_HUMAN_REQUIREMENT=1 requires AEGIS_LIVE_WORKFLOW_HEADED=1');
  }
  if (selection.length !== 1 || !isHumanRequirementScenario(selection[0])) {
    throw new Error('AEGIS_LIVE_HUMAN_REQUIREMENT=1 requires exactly one human requirement scenario');
  }
}

export function validateReusableRecordingConfiguration(options: {
  save: boolean;
  reuse: boolean;
  humanDemo: boolean;
  recordOnly: boolean;
  warmup: boolean;
  headed: boolean;
  selection: readonly LiveWorkflowScenarioName[];
}): void {
  const { save, reuse, humanDemo, recordOnly, warmup, headed, selection } = options;
  const books = selection.length === 1 && selection[0] === 'books-category-matrix';
  if (save && reuse) throw new Error('recording save and reuse modes are mutually exclusive');
  if ((save || reuse) && (selection.length !== 1
      || !(selection[0] === 'baidu-search' || books || isQuotesLiveScenario(selection[0])))) {
    throw new Error('reusable recording mode requires one supported real-site scenario');
  }
  if (save && books && (!recordOnly || humanDemo)) {
    throw new Error('Books recording save requires the provider-free scripted record-only mode');
  }
  if (save && !books && (!humanDemo || recordOnly)) {
    throw new Error('AEGIS_LIVE_SAVE_RECORDING=1 requires a paid real human demonstration');
  }
  if (reuse && books) {
    throw new Error('Books reusable recording is restricted to the generation-only canary');
  }
  if (books && (save || reuse) && (warmup || headed)) {
    throw new Error('Books reusable recording modes forbid profile warm-up and headed browsers');
  }
  if (reuse && (humanDemo || recordOnly)) {
    throw new Error('AEGIS_LIVE_REUSE_RECORDING=1 must not record or use record-only mode');
  }
  if (reuse && warmup) {
    if (!headed) {
      throw new Error('reusable Baidu profile warm-up requires AEGIS_LIVE_WORKFLOW_HEADED=1');
    }
    if (selection.length !== 1 || selection[0] !== 'baidu-search') {
      throw new Error('reusable profile warm-up is restricted to the baidu-search scenario');
    }
  }
}
