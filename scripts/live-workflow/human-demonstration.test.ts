import { describe, expect, it } from 'vitest';
import {
  RECORD_ONLY_APPROVAL,
  isHumanRequirementScenario,
  isQuotesLiveScenario,
  parseScenarioSelection,
  validateHumanDemoConfiguration,
  validateHumanRequirementConfiguration,
  validateRecordOnlyConfiguration,
  validateReusableRecordingConfiguration,
} from './human-demonstration';

describe('real human live-workflow demonstration mode', () => {
  it('requires a headed browser and exactly one scenario', () => {
    expect(() => validateHumanDemoConfiguration(true, false, ['fixture-models']))
      .toThrow(/requires AEGIS_LIVE_WORKFLOW_HEADED=1/);
    expect(() => validateHumanDemoConfiguration(true, true, ['fixture-models', 'apple']))
      .toThrow(/exactly one/);
    expect(() => validateHumanDemoConfiguration(true, true, ['fixture-models']))
      .not.toThrow();
    expect(() => validateHumanDemoConfiguration(false, true, ['quotes-by-tag']))
      .toThrow(/requires AEGIS_LIVE_HUMAN_DEMO=1/);
    expect(() => validateHumanDemoConfiguration(true, true, ['quotes-by-tag']))
      .not.toThrow();
    expect(() => validateHumanDemoConfiguration(false, false, ['quotes-by-tag'], true))
      .not.toThrow();
  });

  it('keeps the mode opt-in and parses an explicit single scenario', () => {
    expect(() => validateHumanDemoConfiguration(false, false, ['fixture-models', 'apple']))
      .not.toThrow();
    expect(parseScenarioSelection({ AEGIS_LIVE_WORKFLOW_SCENARIOS: 'fixture-models' }))
      .toEqual(['fixture-models']);
    expect(parseScenarioSelection({ AEGIS_LIVE_WORKFLOW_SCENARIOS: 'quotes-by-tag' }))
      .toEqual(['quotes-by-tag']);
    expect(parseScenarioSelection({ AEGIS_LIVE_WORKFLOW_SCENARIOS: 'books-category-matrix' }))
      .toEqual(['books-category-matrix']);
    expect(parseScenarioSelection({ AEGIS_LIVE_WORKFLOW_SCENARIOS: 'rfc-editor-section-readonly' }))
      .toEqual(['rfc-editor-section-readonly']);
    expect(parseScenarioSelection({ AEGIS_LIVE_WORKFLOW_SCENARIOS: 'duckduckgo-search-readonly' }))
      .toEqual(['duckduckgo-search-readonly']);
  });

  it('recognizes every quote qualification scenario', () => {
    for (const name of [
      'quotes-by-tag',
      'quotes-one-page',
      'quotes-renamed-contract',
      'quotes-output-projection',
      'quotes-human-choose-requirement',
      'quotes-human-input-requirement',
    ]) {
      expect(isQuotesLiveScenario(name)).toBe(true);
    }
    expect(isQuotesLiveScenario('baidu-search')).toBe(false);
    expect(isHumanRequirementScenario('quotes-human-choose-requirement')).toBe(true);
    expect(isHumanRequirementScenario('quotes-human-input-requirement')).toBe(true);
    expect(isHumanRequirementScenario('quotes-by-tag')).toBe(false);
  });

  it('separates headed human requirement operation from reusable recording mode', () => {
    expect(() => validateHumanDemoConfiguration(
      false, true, ['quotes-human-choose-requirement'], true,
    )).not.toThrow();
    expect(() => validateHumanRequirementConfiguration(
      false, true, ['quotes-human-choose-requirement'],
    )).toThrow(/HUMAN_REQUIREMENT/);
    expect(() => validateHumanRequirementConfiguration(
      true, false, ['quotes-human-input-requirement'],
    )).toThrow(/HEADED/);
    expect(() => validateHumanRequirementConfiguration(
      true, true, ['quotes-human-input-requirement'],
    )).not.toThrow();
    expect(() => validateHumanRequirementConfiguration(
      true, true, ['quotes-human-input-requirement', 'quotes-by-tag'],
    )).toThrow(/exactly one/);
  });

  it('keeps the free record-only preflight explicit and Baidu-only', () => {
    expect(() => validateRecordOnlyConfiguration(
      true, RECORD_ONLY_APPROVAL, true, ['baidu-search'],
    )).not.toThrow();
    expect(() => validateRecordOnlyConfiguration(
      true, undefined, true, ['baidu-search'],
    )).toThrow(/approval/);
    expect(() => validateRecordOnlyConfiguration(
      true, RECORD_ONLY_APPROVAL, false, ['baidu-search'],
    )).toThrow(/HUMAN_DEMO/);
    expect(() => validateRecordOnlyConfiguration(
      true, RECORD_ONLY_APPROVAL, true, ['apple'],
    )).toThrow(/baidu-search or books-category-matrix/);
    expect(() => validateRecordOnlyConfiguration(
      true, RECORD_ONLY_APPROVAL, false, ['books-category-matrix'],
    )).not.toThrow();
    expect(() => validateRecordOnlyConfiguration(
      true, RECORD_ONLY_APPROVAL, true, ['books-category-matrix'],
    )).toThrow(/scripted demonstration/);
  });

  it('keeps encrypted recording save and reuse modes mutually exclusive and real-site-only', () => {
    const valid = {
      save: false,
      reuse: true,
      humanDemo: false,
      recordOnly: false,
      warmup: false,
      headed: false,
      selection: ['baidu-search'] as const,
    };
    expect(() => validateReusableRecordingConfiguration(valid)).not.toThrow();
    expect(() => validateReusableRecordingConfiguration({ ...valid, save: true })).toThrow(/mutually exclusive/);
    expect(() => validateReusableRecordingConfiguration({ ...valid, selection: ['quotes-by-tag'] })).not.toThrow();
    expect(() => validateReusableRecordingConfiguration({ ...valid, selection: ['quotes-renamed-contract'] })).not.toThrow();
    expect(() => validateReusableRecordingConfiguration({
      ...valid, selection: ['quotes-human-choose-requirement'],
    })).not.toThrow();
    expect(() => validateReusableRecordingConfiguration({ ...valid, selection: ['apple'] })).toThrow(/supported real-site/);
    expect(() => validateReusableRecordingConfiguration({ ...valid, humanDemo: true })).toThrow(/must not record/);
    expect(() => validateReusableRecordingConfiguration({ ...valid, warmup: true }))
      .toThrow(/requires AEGIS_LIVE_WORKFLOW_HEADED=1/);
    expect(() => validateReusableRecordingConfiguration({ ...valid, warmup: true, headed: true }))
      .not.toThrow();
    expect(() => validateReusableRecordingConfiguration({
      ...valid, warmup: true, headed: true, selection: ['quotes-by-tag'],
    })).toThrow(/restricted to the baidu-search/);
    expect(() => validateReusableRecordingConfiguration({
      ...valid, save: true, reuse: false, humanDemo: false,
    })).toThrow(/requires a paid real human demonstration/);
    expect(() => validateReusableRecordingConfiguration({
      ...valid,
      save: true,
      reuse: false,
      humanDemo: false,
      recordOnly: true,
      selection: ['books-category-matrix'],
    })).not.toThrow();
    expect(() => validateReusableRecordingConfiguration({
      ...valid,
      selection: ['books-category-matrix'],
    })).toThrow(/generation-only canary/);
    expect(() => validateReusableRecordingConfiguration({
      ...valid,
      save: true,
      reuse: false,
      humanDemo: false,
      recordOnly: false,
      selection: ['books-category-matrix'],
    })).toThrow(/provider-free scripted record-only/);
    expect(() => validateReusableRecordingConfiguration({
      ...valid,
      save: true,
      reuse: false,
      humanDemo: false,
      recordOnly: true,
      warmup: true,
      selection: ['books-category-matrix'],
    })).toThrow(/forbid profile warm-up/);
    expect(() => validateReusableRecordingConfiguration({
      ...valid,
      save: true,
      reuse: false,
      humanDemo: false,
      recordOnly: true,
      headed: true,
      selection: ['books-category-matrix'],
    })).toThrow(/headed browsers/);
  });
});
