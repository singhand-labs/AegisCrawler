import { describe, expect, it } from 'vitest';
import {
  REAL_SITE_COST_BUDGET_USD,
  capRealSiteCostBudget,
  estimateCappedRequestCostUpperBound,
  estimateContextWindowCostUpperBound,
  estimateCostUpperBound,
  estimateRecordingInputTokens,
  resolveRealSiteCostBudget,
} from './real-site-cost';

describe('real-site model-dependent cost budget', () => {
  it('uses the conservative high tier for Beijing qwen3.6-flash', () => {
    const budget = resolveRealSiteCostBudget({
      AEGIS_LOCAL_LLM_MODEL: 'qwen3.6-flash',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
    });
    expect(budget).toMatchObject({
      budgetUSD: 3,
      pricingRegion: 'China (Beijing)',
      inputUSDPerMillion: 0.66,
      outputUSDPerMillion: 3.961,
      inputTokenLimit: 4_545_454,
    });
    expect(estimateCostUpperBound(budget, 100_000, 2_000)).toBeCloseTo(0.073922, 6);
    expect(estimateRecordingInputTokens({ recording: '123456' })).toBe(8);
  });

  it('uses reviewed Alibaba Beijing deepseek-v4-flash pricing', () => {
    const budget = resolveRealSiteCostBudget({
      AEGIS_LOCAL_LLM_MODEL: 'deepseek-v4-flash',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
    });
    expect(budget).toMatchObject({
      budgetUSD: 3,
      pricingRegion: 'China (Beijing)',
      inputUSDPerMillion: 0.138,
      outputUSDPerMillion: 0.275,
      inputTokenLimit: 21_739_130,
    });
    expect(estimateCostUpperBound(budget, 2_200_000, 4_000)).toBeCloseTo(0.3047, 6);
    const capped = capRealSiteCostBudget(budget, 0.10);
    expect(capped).toMatchObject({
      budgetUSD: 0.10,
      inputTokenLimit: 724_637,
    });
    expect(estimateCappedRequestCostUpperBound(capped, 200_000, 4_096))
      .toBeCloseTo(0.0287264, 7);
    expect(estimateContextWindowCostUpperBound(capped, 1_000_000, 4_096))
      .toBeCloseTo(0.138561152, 9);
  });

  it('derives an explicit model price without hard-coding a token count', () => {
    const budget = resolveRealSiteCostBudget({
      AEGIS_LOCAL_LLM_MODEL: 'future-model',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://provider.example/v1',
      AEGIS_LIVE_INPUT_USD_PER_MILLION: '2.5',
      AEGIS_LIVE_OUTPUT_USD_PER_MILLION: '10',
      AEGIS_LIVE_PRICING_SOURCE: 'https://provider.example/pricing',
    });
    expect(budget.inputTokenLimit).toBe(1_200_000);
    expect(budget.budgetUSD).toBe(REAL_SITE_COST_BUDGET_USD);
    expect(estimateCostUpperBound(budget, 30_000, 2_000)).toBe(0.095);
  });

  it('fails closed for unknown pricing or incomplete overrides', () => {
    expect(() => resolveRealSiteCostBudget({
      AEGIS_LOCAL_LLM_MODEL: 'unknown',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://provider.example/v1',
    })).toThrow(/no reviewed real-site pricing/);
    expect(() => resolveRealSiteCostBudget({
      AEGIS_LOCAL_LLM_MODEL: 'unknown',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://provider.example/v1',
      AEGIS_LIVE_INPUT_USD_PER_MILLION: '1',
    })).toThrow(/require both/);
    const budget = resolveRealSiteCostBudget({
      AEGIS_LOCAL_LLM_MODEL: 'deepseek-v4-flash',
      AEGIS_LOCAL_LLM_BASE_URL: 'https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
    });
    expect(() => capRealSiteCostBudget(budget, 4)).toThrow(/no larger/);
    expect(() => estimateCappedRequestCostUpperBound(budget, 0, 1)).toThrow(/positive/);
    expect(() => estimateContextWindowCostUpperBound(budget, 4_096, 4_096))
      .toThrow(/output below context/);
  });

  it('allocates a shared context window to the more expensive token direction', () => {
    const inputExpensive = {
      budgetUSD: 1,
      model: 'test',
      pricingRegion: 'test',
      pricingSource: 'https://example.test/pricing',
      inputUSDPerMillion: 2,
      outputUSDPerMillion: 1,
      inputTokenLimit: 500_000,
    };
    expect(estimateContextWindowCostUpperBound(inputExpensive, 100_000, 10_000))
      .toBeCloseTo(0.2, 8);
  });
});
