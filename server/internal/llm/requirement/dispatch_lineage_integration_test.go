package requirement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type lineageConflictTestProvider struct {
	name             string
	traceBoundary    bool
	boundaryResponse *llm.CompletionResponse
	terminalResponse *llm.CompletionResponse
	calls            int
}

func (p *lineageConflictTestProvider) Name() string {
	return p.name
}

func (p *lineageConflictTestProvider) InputTokenUpperBound(request llm.CompletionRequest) (int, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func (p *lineageConflictTestProvider) Complete(
	ctx context.Context,
	request llm.CompletionRequest,
) (*llm.CompletionResponse, error) {
	p.calls++
	if p.traceBoundary {
		if err := llm.TraceProviderResponse(
			ctx,
			request,
			p.name,
			request.Model,
			p.boundaryResponse,
			200,
		); err != nil {
			return nil, err
		}
	}
	if p.terminalResponse == nil {
		return nil, errors.New("test provider has no terminal response")
	}
	response := *p.terminalResponse
	return &response, nil
}

func TestEnforcedDispatchRejectsDuplicateTerminalCaptureWithoutRedispatch(t *testing.T) {
	setLineageConflictEnforcedEnvironment(t)
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatalf("load enforced policy: %v", err)
	}
	policy := cfg.EnforcedLLMPolicy()
	if policy == nil || policy.Fallback == nil {
		t.Fatal("test requires enforced primary and fallback routes")
	}

	ctx := context.Background()
	persistence := newManagerTestStore(t)
	recordingID := createManagerRecording(t, persistence, cfg)
	job := &models.RequirementJob{
		ID:            "duplicate-terminal-capture-job",
		RecordingID:   recordingID,
		Kind:          models.RequirementJobCandidates,
		Status:        models.RequirementJobPending,
		Source:        models.RequirementSourceLLM,
		PromptVersion: PromptVersion,
		MaxAttempts:   1,
	}
	if err := persistence.CreateRequirementJob(ctx, job, map[string]any{"kind": "candidates"}); err != nil {
		t.Fatalf("create requirement job: %v", err)
	}
	claimed, err := persistence.ClaimPendingRequirementJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim requirement job: %v", err)
	}

	usageEvidence := llm.CompletionUsageMetadata{
		InputTokensPresent:  true,
		OutputTokensPresent: true,
	}
	primary := &lineageConflictTestProvider{
		name:          policy.Primary.Provider,
		traceBoundary: true,
		boundaryResponse: &llm.CompletionResponse{
			Content:       `{"summary":"captured physical boundary"}`,
			InputTokens:   10,
			OutputTokens:  5,
			UsageMetadata: usageEvidence,
		},
		terminalResponse: &llm.CompletionResponse{
			Content:       `{"summary":"inconsistent returned terminal"}`,
			InputTokens:   11,
			OutputTokens:  6,
			UsageMetadata: usageEvidence,
		},
	}
	fallback := &lineageConflictTestProvider{
		name: policy.Fallback.Provider,
		terminalResponse: &llm.CompletionResponse{
			Content:       `{"summary":"fallback must not run"}`,
			InputTokens:   7,
			OutputTokens:  3,
			UsageMetadata: usageEvidence,
		},
	}
	ledger := store.NewBudgetLedger(persistence, budget.LimitsFromPolicy(policy))
	orchestrator := llm.NewOrchestrator(cfg, zap.NewNop())
	orchestrator.SetBudgetLedger(ledger)
	orchestrator.RegisterProviderAs("primary", primary)
	orchestrator.RegisterProviderAs("fallback", fallback)

	recorder := newRequirementAttemptRecorder(
		persistence,
		claimed,
		"duplicate-terminal-recording-hash",
		policy.Fingerprint,
	)
	dispatchContext := llm.WithDispatchOperation(ctx, llm.DispatchOperation{
		Kind:           budget.OperationRequirement,
		ID:             claimed.ID,
		LogicalAttempt: claimed.AttemptCount,
		WorkspaceID:    claimed.WorkspaceID,
	})
	dispatchContext = llm.WithCompletionTraceSink(dispatchContext, recorder)
	result, err := llm.CompleteWithTrace(
		dispatchContext,
		orchestrator,
		llm.CompletionRequest{System: "Return a JSON summary.", User: "Summarize the recording."},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if result != nil || !errors.Is(err, llm.ErrCompletionCapture) {
		t.Fatalf("CompleteWithTrace result=%+v err=%v, want nil ErrCompletionCapture", result, err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("capture failure did not originate from the UNIQUE lineage guard: %v", err)
	}
	if primary.calls != 1 {
		t.Fatalf("primary provider calls = %d, want exactly one physical dispatch", primary.calls)
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback provider calls = %d, want zero after capture failure", fallback.calls)
	}

	calls, err := persistence.ListLLMProviderCalls(
		ctx,
		models.LLMJobTypeRequirement,
		claimed.ID,
		claimed.AttemptCount,
	)
	if err != nil {
		t.Fatalf("list provider calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("stored provider calls = %d, want only the first physical boundary", len(calls))
	}
	call := calls[0]
	wantOperationID := claimed.ID + ":final"
	if call.Dispatch == nil ||
		call.Dispatch.OperationKind != string(budget.OperationRequirement) ||
		call.Dispatch.OperationID != wantOperationID ||
		call.Dispatch.LogicalAttempt != claimed.AttemptCount ||
		call.Dispatch.PhysicalOrdinal != budget.OrdinalPrimary ||
		call.Dispatch.RouteSlot != "primary" ||
		call.Dispatch.PolicyFingerprint != policy.Fingerprint ||
		call.CallKind != models.LLMProviderCallKindResponse ||
		call.ProviderAttempt != 1 {
		t.Fatalf("stored physical boundary has incorrect dispatch lineage: %+v", call)
	}
	opened, err := persistence.GetLLMProviderCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("open retained provider call: %v", err)
	}
	artifact, ok := opened.Artifact.(map[string]any)
	if !ok {
		t.Fatalf("retained provider artifact type = %T, want map", opened.Artifact)
	}
	content, _ := artifact["content"].(string)
	if !strings.Contains(content, "captured physical boundary") ||
		strings.Contains(content, "inconsistent returned terminal") {
		t.Fatalf("retained artifact is not the first physical boundary: %q", content)
	}

	entry, err := ledger.Lookup(ctx, budget.DispatchIdentity{
		WorkspaceID:     claimed.WorkspaceID,
		OperationKind:   budget.OperationRequirement,
		OperationID:     wantOperationID,
		LogicalAttempt:  claimed.AttemptCount,
		PhysicalOrdinal: budget.OrdinalPrimary,
	})
	if err != nil {
		t.Fatalf("lookup budget entry: %v", err)
	}
	if entry == nil || entry.State != budget.StateUsageUncertain || entry.Settled != entry.Reserved {
		t.Fatalf("capture-failed dispatch was not conservatively settled: %+v", entry)
	}
}

func setLineageConflictEnforcedEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"LLM_ENABLED":                         "true",
		"LLM_POLICY_MODE":                     "enforced",
		"LLM_PRIMARY_PROVIDER":                "aliyun",
		"LLM_PRIMARY_ADAPTER":                 "openai",
		"LLM_PRIMARY_MODEL":                   "qwen-lineage-test",
		"LLM_PRIMARY_BASE_URL":                "https://primary.example.test/v1",
		"LLM_PRIMARY_API_KEY":                 "synthetic-primary-key",
		"LLM_PRIMARY_REQUEST_TIMEOUT":         "30s",
		"LLM_PRIMARY_TEMPERATURE":             "0",
		"LLM_PRIMARY_STRICT_TOOL_OUTPUT":      "false",
		"LLM_PRIMARY_ENABLE_THINKING":         "false",
		"LLM_PRIMARY_INPUT_USD_PER_MILLION":   "0.14",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION":  "0.28",
		"LLM_PRIMARY_MAX_INPUT_TOKENS":        "4096",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS":       "512",
		"LLM_PRIMARY_PRICE_REVISION":          "lineage-primary-v1",
		"LLM_FALLBACK_PROVIDER":               "anthropic-secondary",
		"LLM_FALLBACK_ADAPTER":                "anthropic",
		"LLM_FALLBACK_MODEL":                  "claude-lineage-test",
		"LLM_FALLBACK_BASE_URL":               "https://fallback.example.test/v1",
		"LLM_FALLBACK_API_KEY":                "synthetic-fallback-key",
		"LLM_FALLBACK_REQUEST_TIMEOUT":        "30s",
		"LLM_FALLBACK_TEMPERATURE":            "0",
		"LLM_FALLBACK_STRICT_TOOL_OUTPUT":     "false",
		"LLM_FALLBACK_ENABLE_THINKING":        "false",
		"LLM_FALLBACK_INPUT_USD_PER_MILLION":  "0.2",
		"LLM_FALLBACK_OUTPUT_USD_PER_MILLION": "0.4",
		"LLM_FALLBACK_MAX_INPUT_TOKENS":       "4096",
		"LLM_FALLBACK_MAX_OUTPUT_TOKENS":      "512",
		"LLM_FALLBACK_PRICE_REVISION":         "lineage-fallback-v1",
		"LLM_GLOBAL_MAX_REQUEST_USD":          "0.60",
		"LLM_GLOBAL_DAILY_BUDGET_USD":         "3.00",
		"LLM_WORKSPACE_BUDGETS_JSON":          `{"default":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"}}`,
		"LLM_REQUIREMENT_MAX_ATTEMPTS":        "2",
		"LLM_DSL_GENERATION_MAX_ATTEMPTS":     "2",
		"LLM_DSL_MAX_REPAIRS":                 "1",
		"LLM_SELECTOR_MAX_REPAIRS":            "1",
		"LLM_PROVIDER_CONFIGS":                "",
		"LLM_PROVIDER":                        "",
		"LLM_API_KEY":                         "",
		"LLM_BASE_URL":                        "",
		"LLM_MODEL":                           "",
		"LLM_TEMPERATURE":                     "",
		"LLM_REQUEST_TIMEOUT":                 "",
		"LLM_MAX_INPUT_TOKENS":                "",
		"LLM_MAX_OUTPUT_TOKENS":               "",
		"LLM_OPENAI_ENABLE_THINKING":          "",
		"LLM_OPENAI_STRICT_TOOL_OUTPUT":       "",
		"LLM_JOB_MAX_ATTEMPTS":                "",
		"LLM_DSL_SELECTOR_REPAIR_ENABLED":     "",
		"LLM_DAILY_COST_BUDGET":               "",
		"LLM_MAX_RETRIES":                     "",
		"LLM_ALLOW_DEGRADED_FALLBACK":         "",
		"LLM_TEMPERATURE_COMPATIBILITY_RETRY": "",
	} {
		t.Setenv(key, value)
	}
}
