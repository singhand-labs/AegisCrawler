export const REAL_SITE_COST_BUDGET_USD = 3;
const TOKENS_PER_MILLION = 1_000_000;
const ALIBABA_MODEL_PRICING_SOURCE = 'https://www.alibabacloud.com/help/en/model-studio/model-pricing';

export interface RealSiteCostBudget {
  budgetUSD: number;
  model: string;
  pricingRegion: string;
  pricingSource: string;
  /** Conservative highest non-cached, real-time tier price. */
  inputUSDPerMillion: number;
  /** Conservative highest non-cached, real-time tier price. */
  outputUSDPerMillion: number;
  inputTokenLimit: number;
}

function positivePrice(env: NodeJS.ProcessEnv, name: string): number | undefined {
  const raw = env[name]?.trim();
  if (!raw) return undefined;
  const value = Number(raw);
  if (!Number.isFinite(value) || value <= 0) throw new Error(`${name} must be a positive USD price`);
  return value;
}

function inputLimit(inputUSDPerMillion: number): number {
  return Math.floor((REAL_SITE_COST_BUDGET_USD * TOKENS_PER_MILLION) / inputUSDPerMillion);
}

function inputLimitForBudget(budgetUSD: number, inputUSDPerMillion: number): number {
  return Math.floor((budgetUSD * TOKENS_PER_MILLION) / inputUSDPerMillion);
}

function safePricingSource(raw: string | undefined): string {
  let url: URL;
  try {
    url = new URL(raw?.trim() ?? '');
  } catch {
    throw new Error('AEGIS_LIVE_PRICING_SOURCE must be an absolute HTTPS URL');
  }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash) {
    throw new Error('AEGIS_LIVE_PRICING_SOURCE must be credential-free HTTPS without query or fragment');
  }
  return url.toString();
}

/**
 * Resolve conservative list pricing for real-site qualifications. Tiered
 * models use their highest applicable real-time tier because individual full
 * semantic snapshots are atomic and can exceed the nominal chunk budget.
 */
export function resolveRealSiteCostBudget(env: NodeJS.ProcessEnv): RealSiteCostBudget {
  const model = env.AEGIS_LOCAL_LLM_MODEL?.trim() ?? '';
  if (!model) throw new Error('real-site qualification requires AEGIS_LOCAL_LLM_MODEL for cost calculation');

  const explicitInput = positivePrice(env, 'AEGIS_LIVE_INPUT_USD_PER_MILLION');
  const explicitOutput = positivePrice(env, 'AEGIS_LIVE_OUTPUT_USD_PER_MILLION');
  if (explicitInput !== undefined || explicitOutput !== undefined) {
    if (explicitInput === undefined || explicitOutput === undefined) {
      throw new Error('real-site pricing overrides require both input and output USD-per-million prices');
    }
    const pricingSource = safePricingSource(env.AEGIS_LIVE_PRICING_SOURCE);
    return {
      budgetUSD: REAL_SITE_COST_BUDGET_USD,
      model,
      pricingRegion: 'explicit',
      pricingSource,
      inputUSDPerMillion: explicitInput,
      outputUSDPerMillion: explicitOutput,
      inputTokenLimit: inputLimit(explicitInput),
    };
  }

  let baseURL: URL;
  try {
    baseURL = new URL(env.AEGIS_LOCAL_LLM_BASE_URL?.trim() ?? '');
  } catch {
    throw new Error('real-site qualification requires AEGIS_LOCAL_LLM_BASE_URL for pricing region detection');
  }
  const qwen36Flash = model === 'qwen3.6-flash' || model === 'qwen3.6-flash-2026-04-16';
  const deepSeekV4Flash = model === 'deepseek-v4-flash';
  const aliyunBeijing = baseURL.hostname.endsWith('.cn-beijing.maas.aliyuncs.com');
  if (qwen36Flash && aliyunBeijing) {
    const inputUSDPerMillion = 0.66;
    return {
      budgetUSD: REAL_SITE_COST_BUDGET_USD,
      model,
      pricingRegion: 'China (Beijing)',
      pricingSource: ALIBABA_MODEL_PRICING_SOURCE,
      inputUSDPerMillion,
      outputUSDPerMillion: 3.961,
      inputTokenLimit: inputLimit(inputUSDPerMillion),
    };
  }
  if (deepSeekV4Flash && aliyunBeijing) {
    const inputUSDPerMillion = 0.138;
    return {
      budgetUSD: REAL_SITE_COST_BUDGET_USD,
      model,
      pricingRegion: 'China (Beijing)',
      pricingSource: ALIBABA_MODEL_PRICING_SOURCE,
      inputUSDPerMillion,
      outputUSDPerMillion: 0.275,
      inputTokenLimit: inputLimit(inputUSDPerMillion),
    };
  }

  throw new Error(
    `no reviewed real-site pricing for ${model} at ${baseURL.hostname}; `
    + 'set explicit input/output USD-per-million prices and an HTTPS pricing source',
  );
}

export function estimateCostUpperBound(
  budget: RealSiteCostBudget,
  inputTokens: number,
  outputTokens: number,
): number {
  return (
    inputTokens * budget.inputUSDPerMillion
    + outputTokens * budget.outputUSDPerMillion
  ) / TOKENS_PER_MILLION;
}

/** Narrow an already-reviewed model price to a scenario-specific dollar cap. */
export function capRealSiteCostBudget(
  budget: RealSiteCostBudget,
  budgetUSD: number,
): RealSiteCostBudget {
  if (!Number.isFinite(budgetUSD) || budgetUSD <= 0 || budgetUSD > budget.budgetUSD) {
    throw new Error('scenario real-site cost budget must be positive and no larger than the reviewed budget');
  }
  return {
    ...budget,
    budgetUSD,
    inputTokenLimit: inputLimitForBudget(budgetUSD, budget.inputUSDPerMillion),
  };
}

/**
 * Conservative cost ceiling for one provider request with independently
 * enforced input and output token limits.
 */
export function estimateCappedRequestCostUpperBound(
  budget: RealSiteCostBudget,
  maxInputTokens: number,
  maxOutputTokens: number,
): number {
  if (!Number.isSafeInteger(maxInputTokens) || maxInputTokens <= 0
    || !Number.isSafeInteger(maxOutputTokens) || maxOutputTokens <= 0) {
    throw new Error('request token caps must be positive safe integers');
  }
  return estimateCostUpperBound(budget, maxInputTokens, maxOutputTokens);
}

/**
 * Maximum accepted-request cost when input plus output share one hard context
 * window and output has its own hard cap. Allocate the shared window to the
 * more expensive token direction for a conservative bound.
 */
export function estimateContextWindowCostUpperBound(
  budget: RealSiteCostBudget,
  contextTokens: number,
  maxOutputTokens: number,
): number {
  if (!Number.isSafeInteger(contextTokens) || contextTokens <= 0
    || !Number.isSafeInteger(maxOutputTokens) || maxOutputTokens <= 0
    || maxOutputTokens >= contextTokens) {
    throw new Error('context and output token caps must be positive safe integers with output below context');
  }
  const outputTokens = budget.outputUSDPerMillion >= budget.inputUSDPerMillion
    ? maxOutputTokens
    : 0;
  return estimateCostUpperBound(
    budget,
    contextTokens - outputTokens,
    outputTokens,
  );
}

/** A lower-bound forecast: every real workflow sends at least one full recording. */
export function estimateRecordingInputTokens(recording: unknown): number {
  return Math.ceil(Buffer.byteLength(JSON.stringify(recording), 'utf8') / 3);
}
