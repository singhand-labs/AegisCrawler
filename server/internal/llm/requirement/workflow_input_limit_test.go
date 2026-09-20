package requirement

import (
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

// Under an enforced LLM policy the legacy LLM_MAX_INPUT_TOKENS knob is
// rejected at startup, so the fit gate derives from the policy route in the
// same unit the dispatch bound enforces (payload runes) — the full window.
func TestWorkflowInputLimitDerivesFromEnforcedPolicyRoute(t *testing.T) {
	setLineageConflictEnforcedEnvironment(t)
	cfg := config.Load()
	if cfg.EnforcedLLMPolicy() == nil {
		t.Fatal("expected an enforced policy config")
	}
	if got := NewWorkflow(cfg, nil).inputLimit(); got != 4096 {
		t.Fatalf("inputLimit() = %d, want the declared window 4096", got)
	}
}

func TestWorkflowInputLimitLegacyKnobAndDefault(t *testing.T) {
	if got := NewWorkflow(&config.Config{LLMMaxInputTokens: 1000}, nil).inputLimit(); got != 1000 {
		t.Fatalf("inputLimit() = %d, want legacy knob 1000", got)
	}
	if got := NewWorkflow(&config.Config{}, nil).inputLimit(); got != 128_000 {
		t.Fatalf("inputLimit() = %d, want the 128K rune default", got)
	}
}
