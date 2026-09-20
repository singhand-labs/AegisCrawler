package dsl

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

func loadEnforcedInputLimitConfig(t *testing.T, maxInput string) *config.Config {
	t.Helper()
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY_MODE", "enforced")
	t.Setenv("LLM_PRIMARY_PROVIDER", "aliyun")
	t.Setenv("LLM_PRIMARY_ADAPTER", "openai")
	t.Setenv("LLM_PRIMARY_MODEL", "qwen-test")
	t.Setenv("LLM_PRIMARY_BASE_URL", "https://provider.example.test/v1")
	t.Setenv("LLM_PRIMARY_API_KEY", "synthetic-key")
	t.Setenv("LLM_PRIMARY_REQUEST_TIMEOUT", "30s")
	t.Setenv("LLM_PRIMARY_TEMPERATURE", "0")
	t.Setenv("LLM_PRIMARY_STRICT_TOOL_OUTPUT", "true")
	t.Setenv("LLM_PRIMARY_ENABLE_THINKING", "false")
	t.Setenv("LLM_PRIMARY_INPUT_USD_PER_MILLION", "0.14")
	t.Setenv("LLM_PRIMARY_OUTPUT_USD_PER_MILLION", "0.28")
	t.Setenv("LLM_PRIMARY_MAX_INPUT_TOKENS", maxInput)
	t.Setenv("LLM_PRIMARY_MAX_OUTPUT_TOKENS", "512")
	t.Setenv("LLM_PRIMARY_PRICE_REVISION", "input-limit-test")
	t.Setenv("LLM_GLOBAL_MAX_REQUEST_USD", "0.60")
	t.Setenv("LLM_GLOBAL_DAILY_BUDGET_USD", "3.00")
	t.Setenv("LLM_WORKSPACE_BUDGETS_JSON", `{"default":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"}}`)
	t.Setenv("LLM_REQUIREMENT_MAX_ATTEMPTS", "2")
	t.Setenv("LLM_DSL_GENERATION_MAX_ATTEMPTS", "2")
	t.Setenv("LLM_DSL_MAX_REPAIRS", "1")
	t.Setenv("LLM_SELECTOR_MAX_REPAIRS", "1")
	cfg := config.Load()
	if cfg.EnforcedLLMPolicy() == nil {
		t.Fatal("expected an enforced policy config")
	}
	return cfg
}

// The fit gate derives from the enforced route's declared window in the
// same unit the dispatch bound enforces: the adapters' InputTokenUpperBound
// counts payload runes, and the workflow estimate counts content runes, so
// the full declared window applies.
func TestWorkflowInputLimitDerivesFromEnforcedPolicyRoute(t *testing.T) {
	cfg := loadEnforcedInputLimitConfig(t, "128000")
	if got := NewDSLWorkflow(cfg, nil).inputLimit(); got != 128000 {
		t.Fatalf("inputLimit() = %d, want the declared window 128000", got)
	}
	// A smaller declared window tightens the gate accordingly.
	cfg = loadEnforcedInputLimitConfig(t, "64000")
	if got := NewDSLWorkflow(cfg, nil).inputLimit(); got != 64000 {
		t.Fatalf("inputLimit() = %d, want the declared window 64000", got)
	}
}

func TestWorkflowInputLimitLegacyKnobAndDefault(t *testing.T) {
	if got := NewDSLWorkflow(&config.Config{LLMMaxInputTokens: 1000}, nil).inputLimit(); got != 1000 {
		t.Fatalf("inputLimit() = %d, want legacy knob 1000", got)
	}
	if got := NewDSLWorkflow(&config.Config{}, nil).inputLimit(); got != 128_000 {
		t.Fatalf("inputLimit() = %d, want the 128K rune default", got)
	}
}

type capturingCompleter struct {
	calls int
}

func (c *capturingCompleter) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResult, error) {
	c.calls++
	return &llm.CompletionResult{CompletionResponse: &llm.CompletionResponse{Content: "{}"}}, nil
}

// The rune-unit gate accepts a CJK source whose byte length is far past the
// historical byte bound but whose rune count fits the declared window —
// this is the regression for the multi-page ccgp.gov.cn recordings that
// fail-closed under the byte-count bound. An ASCII source past the window
// still fails closed.
func TestWorkflowCompleteGateCJKLiftAndASCIIFailClosed(t *testing.T) {
	cfg := loadEnforcedInputLimitConfig(t, "128000")
	cfg.LLMMaxOutputTokens = 512
	completer := &capturingCompleter{}
	w := NewDSLWorkflow(cfg, completer)

	// 60K CJK runes = 180KB UTF-8: ~1.4x past the old 128KB byte budget, and
	// 4.5x past the old bytes/4 token estimate — but 60K runes fit the window.
	cjk := strings.Repeat("公告", 30_000)
	if got := utf8.RuneCountInString(cjk); got != 60_000 {
		t.Fatalf("fixture rune count = %d, want 60000", got)
	}
	if _, err := w.complete(context.Background(), "system", cjk, nil, llm.CompletionTraceMetadata{}); err != nil {
		t.Fatalf("complete() rejected an in-window CJK source: %v", err)
	}
	if completer.calls != 1 {
		t.Fatalf("completer calls = %d, want 1", completer.calls)
	}

	// 200K ASCII runes: past the window and its reserves — fail closed.
	over := strings.Repeat("a", 200_000)
	if _, err := w.complete(context.Background(), "system", over, nil, llm.CompletionTraceMetadata{}); err == nil {
		t.Fatal("complete() accepted a source past the declared window")
	}
}
