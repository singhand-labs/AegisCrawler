package budget

import (
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

func admissibleTestPolicy(t *testing.T) (*config.ProductionLLMPolicy, config.USDNanos) {
	t.Helper()
	route := config.RoutePolicy{
		Slot:                     "primary",
		InputUSDPerMillion:       1_000_000_000,
		CachedInputUSDPerMillion: 1_000_000_000,
		OutputUSDPerMillion:      1_000_000_000,
		MaxInputTokens:           100,
		MaxOutputTokens:          10,
	}
	reserved, err := Reservation(
		Rates{
			InputUSDPerMillion:       route.InputUSDPerMillion,
			CachedInputUSDPerMillion: route.CachedInputUSDPerMillion,
			OutputUSDPerMillion:      route.OutputUSDPerMillion,
		},
		Caps{MaxInputTokens: route.MaxInputTokens, MaxOutputTokens: route.MaxOutputTokens},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &config.ProductionLLMPolicy{
		Primary:              route,
		GlobalMaxRequestUSD:  reserved,
		GlobalDailyBudgetUSD: reserved,
		WorkspaceBudgets: map[string]config.WorkspaceBudget{
			"workspace-secret-sentinel": {
				MaxRequestUSD:  reserved,
				DailyBudgetUSD: reserved,
			},
		},
	}, reserved
}

func TestValidatePolicyAdmissibilityAcceptsExactBoundaries(t *testing.T) {
	policy, _ := admissibleTestPolicy(t)
	if err := ValidatePolicyAdmissibility(policy); err != nil {
		t.Fatalf("exact-bound policy rejected: %v", err)
	}
}

func TestValidatePolicyAdmissibilityRejectsEveryAlwaysDeniedCap(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.ProductionLLMPolicy, config.USDNanos)
	}{
		{
			name: "global request",
			mutate: func(policy *config.ProductionLLMPolicy, reserved config.USDNanos) {
				policy.GlobalMaxRequestUSD = reserved - 1
			},
		},
		{
			name: "global daily",
			mutate: func(policy *config.ProductionLLMPolicy, reserved config.USDNanos) {
				policy.GlobalDailyBudgetUSD = reserved - 1
			},
		},
		{
			name: "workspace request",
			mutate: func(policy *config.ProductionLLMPolicy, reserved config.USDNanos) {
				workspace := policy.WorkspaceBudgets["workspace-secret-sentinel"]
				workspace.MaxRequestUSD = reserved - 1
				policy.WorkspaceBudgets["workspace-secret-sentinel"] = workspace
			},
		},
		{
			name: "workspace daily",
			mutate: func(policy *config.ProductionLLMPolicy, reserved config.USDNanos) {
				workspace := policy.WorkspaceBudgets["workspace-secret-sentinel"]
				workspace.DailyBudgetUSD = reserved - 1
				policy.WorkspaceBudgets["workspace-secret-sentinel"] = workspace
			},
		},
		{
			name: "fallback",
			mutate: func(policy *config.ProductionLLMPolicy, reserved config.USDNanos) {
				fallback := policy.Primary
				fallback.Slot = "fallback"
				fallback.MaxInputTokens++
				policy.Fallback = &fallback
				policy.GlobalMaxRequestUSD = reserved
				policy.GlobalDailyBudgetUSD = reserved
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, reserved := admissibleTestPolicy(t)
			test.mutate(policy, reserved)
			err := ValidatePolicyAdmissibility(policy)
			if err == nil {
				t.Fatal("expected an always-denied policy to be rejected")
			}
			if got := err.Error(); got == "" ||
				strings.Contains(got, "workspace-secret-sentinel") {
				t.Fatalf("unsafe or empty validation error: %q", got)
			}
		})
	}
}
