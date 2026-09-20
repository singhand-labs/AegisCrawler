package llm

import (
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"go.uber.org/zap"
)

func TestEstimateInputTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"a", 1},
		{"你好世界", 3}, // 12 UTF-8 bytes -> ceil(12/4)=3
	}
	for _, tc := range cases {
		got := estimateInputTokens(tc.in)
		if got != tc.want {
			t.Errorf("estimateInputTokens(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTokenBudgetDailyReset(t *testing.T) {
	fixed := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	b := newTokenBudget(&config.Config{LLMDailyCostBudget: 1}, zap.NewNop())
	b.clock = func() time.Time { return fixed }

	if !b.checkBefore(1) {
		t.Fatal("expected first request to be allowed")
	}
	b.record(1_000_000, 0) // $5, over budget

	if b.checkBefore(1) {
		t.Fatal("expected budget exhausted on same day")
	}

	// Move to next day; budget should reset.
	b.clock = func() time.Time { return fixed.Add(24 * time.Hour) }
	if !b.checkBefore(1) {
		t.Fatal("expected budget to reset on new day")
	}
}

func TestTokenBudgetDisabled(t *testing.T) {
	b := newTokenBudget(&config.Config{LLMDailyCostBudget: 0}, zap.NewNop())
	if !b.checkBefore(1_000_000_000) {
		t.Fatal("expected budget check to pass when disabled")
	}
	b.record(1_000_000_000, 1_000_000_000)
	if !b.checkBefore(1) {
		t.Fatal("expected budget check to remain disabled after recording")
	}
}

func TestTokenBudgetWarnsBeforeOverage(t *testing.T) {
	cfg := &config.Config{LLMDailyCostBudget: 0.00001} // ~2 input tokens at default pricing
	b := newTokenBudget(cfg, zap.NewNop())

	if !b.checkBefore(10) {
		t.Fatal("expected request to be allowed even if it would exceed budget")
	}
}
