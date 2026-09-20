package budget

import (
	"fmt"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

// Limits is the immutable startup cap snapshot the ledger admits against.
// Limits come from environment variables and never change without a restart.
type Limits struct {
	GlobalMaxRequestUSD  config.USDNanos
	GlobalDailyBudgetUSD config.USDNanos
	Workspaces           map[string]config.WorkspaceBudget
}

// LimitsFromPolicy derives the ledger caps from a validated enforced policy.
func LimitsFromPolicy(policy *config.ProductionLLMPolicy) Limits {
	if policy == nil {
		return Limits{}
	}
	workspaces := make(map[string]config.WorkspaceBudget, len(policy.WorkspaceBudgets))
	for id, budget := range policy.WorkspaceBudgets {
		workspaces[id] = budget
	}
	return Limits{
		GlobalMaxRequestUSD:  policy.GlobalMaxRequestUSD,
		GlobalDailyBudgetUSD: policy.GlobalDailyBudgetUSD,
		Workspaces:           workspaces,
	}
}

// Workspace returns the configured caps for a workspace. A workspace absent
// from the map cannot use an LLM at all.
func (l Limits) Workspace(workspaceID string) (config.WorkspaceBudget, bool) {
	budget, ok := l.Workspaces[workspaceID]
	return budget, ok
}

// Configured reports whether the limits carry usable global caps.
func (l Limits) Configured() bool {
	return l.GlobalMaxRequestUSD > 0 && l.GlobalDailyBudgetUSD > 0
}

// ValidatePolicyAdmissibility proves that every configured route can pass an
// empty-day admission for every workspace listed as LLM-enabled. Without this
// startup/readiness guard, a positive but undersized cap could make every
// physical call deterministically fail while the process still reported ready.
func ValidatePolicyAdmissibility(policy *config.ProductionLLMPolicy) error {
	if policy == nil {
		return fmt.Errorf("an enforced LLM policy is required")
	}
	routes := []config.RoutePolicy{policy.Primary}
	if policy.Fallback != nil {
		routes = append(routes, *policy.Fallback)
	}
	for _, route := range routes {
		reserved, err := Reservation(
			Rates{
				InputUSDPerMillion:       route.InputUSDPerMillion,
				CachedInputUSDPerMillion: route.CachedInputUSDPerMillion,
				OutputUSDPerMillion:      route.OutputUSDPerMillion,
			},
			Caps{
				MaxInputTokens:  route.MaxInputTokens,
				MaxOutputTokens: route.MaxOutputTokens,
			},
		)
		if err != nil {
			return fmt.Errorf("%s route worst-case reservation: %w", route.Slot, err)
		}
		switch {
		case reserved > policy.GlobalMaxRequestUSD:
			return fmt.Errorf("%s route worst-case reservation exceeds the global request cap", route.Slot)
		case reserved > policy.GlobalDailyBudgetUSD:
			return fmt.Errorf("%s route worst-case reservation exceeds the global daily cap", route.Slot)
		}
		for _, workspace := range policy.WorkspaceBudgets {
			switch {
			case reserved > workspace.MaxRequestUSD:
				return fmt.Errorf("%s route worst-case reservation exceeds a configured workspace request cap", route.Slot)
			case reserved > workspace.DailyBudgetUSD:
				return fmt.Errorf("%s route worst-case reservation exceeds a configured workspace daily cap", route.Slot)
			}
		}
	}
	return nil
}

// CheckRequestCaps verifies one reservation against both physical-request caps
// before any daily aggregation. It returns a *DeniedError when either exact cap
// would be exceeded, and reports the workspace caps for the daily check.
func (l Limits) CheckRequestCaps(workspaceID string, reserved config.USDNanos) (config.WorkspaceBudget, error) {
	workspace, ok := l.Workspace(workspaceID)
	if !ok {
		return config.WorkspaceBudget{}, &DeniedError{
			Scope:     ScopeWorkspace,
			Limit:     LimitRequest,
			Requested: reserved,
			Remaining: 0,
			Reason:    "workspace has no configured LLM budget",
		}
	}
	if reserved > l.GlobalMaxRequestUSD {
		return workspace, &DeniedError{
			Scope:     ScopeGlobal,
			Limit:     LimitRequest,
			Requested: reserved,
			Remaining: l.GlobalMaxRequestUSD,
		}
	}
	if reserved > workspace.MaxRequestUSD {
		return workspace, &DeniedError{
			Scope:     ScopeWorkspace,
			Limit:     LimitRequest,
			Requested: reserved,
			Remaining: workspace.MaxRequestUSD,
		}
	}
	return workspace, nil
}

// CheckDailyCap verifies that a reservation fits a daily cap given the amount
// already counted for the UTC day. Counted includes settled costs plus active
// reservations, so a cap is never exceeded by concurrently admitted calls.
func CheckDailyCap(scope string, reserved, counted, cap config.USDNanos) error {
	remaining := cap - counted
	if remaining < 0 {
		remaining = 0
	}
	if reserved > remaining {
		return &DeniedError{
			Scope:     scope,
			Limit:     LimitDaily,
			Requested: reserved,
			Remaining: remaining,
		}
	}
	return nil
}
