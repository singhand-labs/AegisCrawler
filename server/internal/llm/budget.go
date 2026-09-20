package llm

import (
	"sync"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"go.uber.org/zap"
)

// Default cost assumptions (USD per token). These are conservative defaults
// roughly aligned with OpenAI GPT-4o-class pricing. Accuracy is the priority;
// the budget is mainly used for counting and alerting.
const (
	defaultInputCostPerToken  = 5.0 / 1_000_000
	defaultOutputCostPerToken = 15.0 / 1_000_000
)

// tokenBudget tracks per-day estimated LLM cost and enforces soft limits.
type tokenBudget struct {
	cfg    *config.Config
	mu     sync.Mutex
	day    string
	used   float64
	clock  func() time.Time
	logger *zap.Logger
}

func newTokenBudget(cfg *config.Config, logger *zap.Logger) *tokenBudget {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &tokenBudget{
		cfg:    cfg,
		clock:  time.Now,
		logger: logger,
	}
}

func (b *tokenBudget) currentDay() string {
	return b.clock().UTC().Format("2006-01-02")
}

func (b *tokenBudget) resetIfNewDay() {
	d := b.currentDay()
	if b.day != d {
		b.day = d
		b.used = 0
	}
}

// estimateInputTokens returns a deterministic, heuristic token count for the
// combined prompt text. It uses 1 token per ~4 UTF-8 bytes.
func estimateInputTokens(text string) int {
	if text == "" {
		return 0
	}
	bytes := len([]byte(text))
	return (bytes + 3) / 4 // round up
}

func (b *tokenBudget) estimatedCost(inputTokens, outputTokens int) float64 {
	return float64(inputTokens)*defaultInputCostPerToken +
		float64(outputTokens)*defaultOutputCostPerToken
}

// checkBefore returns false when the daily cost budget is already exhausted.
// It still allows requests that would merely push the budget over the limit
// because accuracy is the priority; the caller is responsible for falling back
// to the baseline rule when this returns false.
func (b *tokenBudget) checkBefore(estimatedInputTokens int) bool {
	if b.cfg.LLMDailyCostBudget <= 0 {
		return true
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNewDay()

	if b.used >= b.cfg.LLMDailyCostBudget {
		b.logger.Warn("llm daily cost budget already exhausted",
			zap.Float64("usedUSD", b.used),
			zap.Float64("budgetUSD", b.cfg.LLMDailyCostBudget),
		)
		return false
	}

	estimatedCost := b.estimatedCost(estimatedInputTokens, 0)
	if b.used+estimatedCost > b.cfg.LLMDailyCostBudget {
		b.logger.Warn("llm daily cost budget would be exceeded by this request",
			zap.Float64("usedUSD", b.used),
			zap.Float64("estimatedCostUSD", estimatedCost),
			zap.Float64("budgetUSD", b.cfg.LLMDailyCostBudget),
		)
	}
	return true
}

// record records actual token usage and logs when the daily budget is exceeded.
func (b *tokenBudget) record(inputTokens, outputTokens int) {
	if b.cfg.LLMDailyCostBudget <= 0 {
		return
	}

	cost := b.estimatedCost(inputTokens, outputTokens)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNewDay()
	b.used += cost

	if b.used > b.cfg.LLMDailyCostBudget {
		b.logger.Warn("llm daily cost budget exceeded",
			zap.Float64("usedUSD", b.used),
			zap.Float64("budgetUSD", b.cfg.LLMDailyCostBudget),
			zap.Int("inputTokens", inputTokens),
			zap.Int("outputTokens", outputTokens),
		)
	}
}
