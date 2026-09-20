package config

import "testing"

// The envelope must price on the ledger's scale ($1 = 1e9 nanos): the 5¢
// default is 50_000_000 nanos and one configured cent is 10_000_000 nanos.
// The previous 1e8-scale helper made every new workflow cheaper than a
// single worst-case reservation, exhausting budgets on first dispatch.
func TestWorkflowBudgetUSDMatchesLedgerScale(t *testing.T) {
	if got := (&Config{}).WorkflowBudgetUSD(); int64(got) != 50_000_000 {
		t.Fatalf("default envelope = %d nanos, want 50_000_000 (5¢ at 1e9 scale)", int64(got))
	}
	if got := (&Config{WorkflowBudgetUSDCents: 50}).WorkflowBudgetUSD(); int64(got) != 500_000_000 {
		t.Fatalf("50-cent envelope = %d nanos, want 500_000_000", int64(got))
	}
	if got := (*Config)(nil).WorkflowBudgetUSD(); int64(got) != 50_000_000 {
		t.Fatalf("nil-config envelope = %d nanos, want the safe default", int64(got))
	}
}
