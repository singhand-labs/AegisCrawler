package store

import "testing"

// The creation path must persist the explicitly provided envelope; options
// without a budget keep the historical column default.
func TestCreateDSLWorkflowBudgetResolution(t *testing.T) {
	if got := CreateDSLWorkflowBudget(nil); got != DefaultDSLWorkflowBudgetNanos {
		t.Fatalf("no options budget = %d, want default %d", got, DefaultDSLWorkflowBudgetNanos)
	}
	if got := CreateDSLWorkflowBudget([]DSLWorkflowOptions{{}}); got != DefaultDSLWorkflowBudgetNanos {
		t.Fatalf("zero budget = %d, want default %d", got, DefaultDSLWorkflowBudgetNanos)
	}
	if got := CreateDSLWorkflowBudget([]DSLWorkflowOptions{{BudgetUSDNanos: 500_000_000}}); got != 500_000_000 {
		t.Fatalf("explicit budget = %d, want 500000000", got)
	}
}
