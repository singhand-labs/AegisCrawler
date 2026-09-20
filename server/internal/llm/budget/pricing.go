// Package budget holds the neutral persisted types and integer USD-nano
// arithmetic for the production hard-budget ledger. It deliberately depends on
// neither the provider adapters nor the store so a future external ledger can
// replace the SQLite implementation without changing provider or workflow
// contracts.
package budget

import (
	"errors"
	"fmt"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

// tokensPerRateUnit is the token count that configured USD-nano rates price.
const tokensPerRateUnit = 1_000_000

// reservationSplitRoundingHeadroom covers the single extra nano that can arise
// when trusted cached and uncached input categories round upward independently.
// It remains reserved only until exact settlement.
const reservationSplitRoundingHeadroom config.USDNanos = 1

// maxUSDNanos bounds every intermediate and final budget amount. Amounts are
// signed 64-bit USD nanos, so arithmetic checks against this ceiling before
// multiplying rather than detecting wraparound afterwards.
const maxUSDNanos = int64(^uint64(0) >> 1)

// ErrPricingOverflow reports that an exact integer cost is not representable.
// Callers must fail closed rather than dispatch an unpriceable call.
var ErrPricingOverflow = errors.New("usd nano amount overflows")

// Rates is the exact USD-nano price snapshot for one route. Rates price one
// million tokens of each configured category.
type Rates struct {
	InputUSDPerMillion       config.USDNanos
	CachedInputUSDPerMillion config.USDNanos
	OutputUSDPerMillion      config.USDNanos
}

// Caps is the exact per-call token ceiling for one route. Both bounds also act
// as the conservative reservation upper bound for a single physical call.
type Caps struct {
	MaxInputTokens  int
	MaxOutputTokens int
}

// Usage is provider-reported token usage separated into the v1 price
// vocabulary. CachedInputTokens is only meaningful when the provider reports
// cached input as a distinct category.
type Usage struct {
	UncachedInputTokens int
	CachedInputTokens   int
	OutputTokens        int
	// CachedInputReported distinguishes "provider reported zero cached input"
	// from "provider did not report cached input at all". When false, all
	// input is charged at the higher configured input rate.
	CachedInputReported bool
}

// InputTokens is the total input tokens across both input categories.
func (u Usage) InputTokens() int {
	return u.UncachedInputTokens + u.CachedInputTokens
}

// CategoryCost returns ceil(tokens * ratePerMillion / 1_000_000) in USD nanos.
// Each category rounds upward independently so a sum of categories is never
// cheaper than the provider's own per-category rounding.
func CategoryCost(tokens int, ratePerMillion config.USDNanos) (config.USDNanos, error) {
	if tokens < 0 {
		return 0, fmt.Errorf("token count must not be negative")
	}
	if ratePerMillion < 0 {
		return 0, fmt.Errorf("usd nano rate must not be negative")
	}
	if tokens == 0 || ratePerMillion == 0 {
		return 0, nil
	}
	rate := int64(ratePerMillion)
	if int64(tokens) > maxUSDNanos/rate {
		return 0, ErrPricingOverflow
	}
	product := int64(tokens) * rate
	// Ceiling division without adding to product, because product may already
	// be close to the signed 64-bit ceiling.
	quotient := product / tokensPerRateUnit
	if product%tokensPerRateUnit != 0 {
		quotient++
	}
	return config.USDNanos(quotient), nil
}

// Add sums USD nanos and fails closed on overflow.
func Add(amounts ...config.USDNanos) (config.USDNanos, error) {
	total := int64(0)
	for _, amount := range amounts {
		if amount < 0 {
			return 0, fmt.Errorf("usd nano amount must not be negative")
		}
		if int64(amount) > maxUSDNanos-total {
			return 0, ErrPricingOverflow
		}
		total += int64(amount)
	}
	return config.USDNanos(total), nil
}

// Reservation returns the worst-case cost of one physical call under the given
// rates and caps. Input is priced at the higher of the uncached and cached
// input rates because the split is unknown before dispatch.
func Reservation(rates Rates, caps Caps) (config.USDNanos, error) {
	if caps.MaxInputTokens <= 0 || caps.MaxOutputTokens <= 0 {
		return 0, fmt.Errorf("route token caps must be positive")
	}
	inputRate := rates.InputUSDPerMillion
	if rates.CachedInputUSDPerMillion > inputRate {
		inputRate = rates.CachedInputUSDPerMillion
	}
	input, err := CategoryCost(caps.MaxInputTokens, inputRate)
	if err != nil {
		return 0, err
	}
	output, err := CategoryCost(caps.MaxOutputTokens, rates.OutputUSDPerMillion)
	if err != nil {
		return 0, err
	}
	return Add(input, output, reservationSplitRoundingHeadroom)
}

// SettledCost returns the exact trusted cost of reported usage. Callers must
// have already validated the usage against the prepared bounds with
// ValidateUsage; unvalidated or untrustworthy usage settles the full
// reservation as uncertain instead.
func SettledCost(rates Rates, usage Usage) (config.USDNanos, error) {
	if usage.UncachedInputTokens < 0 || usage.CachedInputTokens < 0 || usage.OutputTokens < 0 {
		return 0, fmt.Errorf("reported usage must not be negative")
	}
	if !usage.CachedInputReported {
		// Without a trustworthy cached split, all input is charged at the
		// higher configured input rate.
		inputRate := rates.InputUSDPerMillion
		if rates.CachedInputUSDPerMillion > inputRate {
			inputRate = rates.CachedInputUSDPerMillion
		}
		input, err := CategoryCost(usage.InputTokens(), inputRate)
		if err != nil {
			return 0, err
		}
		output, err := CategoryCost(usage.OutputTokens, rates.OutputUSDPerMillion)
		if err != nil {
			return 0, err
		}
		return Add(input, output)
	}
	uncached, err := CategoryCost(usage.UncachedInputTokens, rates.InputUSDPerMillion)
	if err != nil {
		return 0, err
	}
	cached, err := CategoryCost(usage.CachedInputTokens, rates.CachedInputUSDPerMillion)
	if err != nil {
		return 0, err
	}
	output, err := CategoryCost(usage.OutputTokens, rates.OutputUSDPerMillion)
	if err != nil {
		return 0, err
	}
	return Add(uncached, cached, output)
}

// ValidateUsage reports whether reported usage is trustworthy enough to settle
// an exact cost. Usage must be non-negative, use only configured categories,
// and stay within the prepared bounds. Any violation must settle the full
// reservation as uncertain instead.
func ValidateUsage(usage Usage, caps Caps, unknownCategories []string) error {
	if len(unknownCategories) > 0 {
		return fmt.Errorf("provider reported unknown billing categories: %v", unknownCategories)
	}
	if usage.UncachedInputTokens < 0 || usage.CachedInputTokens < 0 || usage.OutputTokens < 0 {
		return fmt.Errorf("reported usage must not be negative")
	}
	if usage.CachedInputTokens > 0 && !usage.CachedInputReported {
		return fmt.Errorf("cached input tokens reported without a cached category")
	}
	if usage.UncachedInputTokens > caps.MaxInputTokens ||
		usage.CachedInputTokens > caps.MaxInputTokens-usage.UncachedInputTokens {
		return fmt.Errorf(
			"reported input token split %d + %d exceeds prepared bound %d",
			usage.UncachedInputTokens,
			usage.CachedInputTokens,
			caps.MaxInputTokens,
		)
	}
	if usage.OutputTokens > caps.MaxOutputTokens {
		return fmt.Errorf("reported output tokens %d exceed prepared bound %d", usage.OutputTokens, caps.MaxOutputTokens)
	}
	return nil
}
