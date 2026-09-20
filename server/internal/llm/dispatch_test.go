package llm

import (
	"context"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
)

func TestDispatchAttemptCountsFromOne(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-1, 1}, {0, 1}, {1, 1}, {2, 2}, {3, 3},
	} {
		if got := DispatchAttempt(tc.in); got != tc.want {
			t.Fatalf("DispatchAttempt(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDispatchIdentityRequiresBoundOperation(t *testing.T) {
	if _, err := dispatchIdentity(context.Background(), budget.OrdinalPrimary); err == nil {
		t.Fatal("expected a completion without a bound operation to fail closed")
	}
}

func TestDispatchIdentityUsesPersistedWorkspace(t *testing.T) {
	ctx := WithDispatchOperation(context.Background(), DispatchOperation{
		Kind:           budget.OperationDSL,
		ID:             "job-1",
		LogicalAttempt: 2,
		WorkspaceID:    "workspace-7",
	})
	identity, err := dispatchIdentity(ctx, budget.OrdinalFallback)
	if err != nil {
		t.Fatalf("dispatchIdentity returned error: %v", err)
	}
	want := budget.DispatchIdentity{
		WorkspaceID: "workspace-7", OperationKind: budget.OperationDSL,
		OperationID: "job-1", LogicalAttempt: 2, PhysicalOrdinal: budget.OrdinalFallback,
	}
	if identity != want {
		t.Fatalf("identity = %+v, want %+v", identity, want)
	}
}

func TestDispatchIdentityUsesAuthenticatedPrincipal(t *testing.T) {
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{WorkspaceID: "workspace-9", Subject: "admin"})
	ctx = WithDispatchOperation(ctx, DispatchOperation{
		Kind:           budget.OperationIntent,
		ID:             "operation-1",
		LogicalAttempt: 1,
	})
	identity, err := dispatchIdentity(ctx, budget.OrdinalPrimary)
	if err != nil {
		t.Fatalf("dispatchIdentity returned error: %v", err)
	}
	if identity.WorkspaceID != "workspace-9" {
		t.Fatalf("workspace = %q, want the authenticated principal's workspace", identity.WorkspaceID)
	}
}

func TestDispatchIdentityPrefersPersistedWorkspaceOverPrincipal(t *testing.T) {
	// A durable job carries its own workspace, which must win over whatever
	// principal happens to be on the worker context.
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{WorkspaceID: "principal-workspace"})
	ctx = WithDispatchOperation(ctx, DispatchOperation{
		Kind:           budget.OperationDSL,
		ID:             "job-1",
		LogicalAttempt: 1,
		WorkspaceID:    "persisted-workspace",
	})
	identity, err := dispatchIdentity(ctx, budget.OrdinalPrimary)
	if err != nil {
		t.Fatalf("dispatchIdentity returned error: %v", err)
	}
	if identity.WorkspaceID != "persisted-workspace" {
		t.Fatalf("workspace = %q, want the persisted job workspace", identity.WorkspaceID)
	}
}

func TestDispatchIdentityRejectsUnattributableOperation(t *testing.T) {
	// No principal and no persisted workspace: enforced mode must fail closed
	// rather than silently charge the default workspace.
	ctx := WithDispatchOperation(context.Background(), DispatchOperation{
		Kind:           budget.OperationEnhance,
		ID:             "job-1",
		LogicalAttempt: 1,
	})
	if _, err := dispatchIdentity(ctx, budget.OrdinalPrimary); err == nil {
		t.Fatal("expected an unattributable operation to fail closed")
	}
}

func TestDispatchIdentityRejectsIncompleteOperation(t *testing.T) {
	cases := []struct {
		name      string
		operation DispatchOperation
	}{
		{"missing kind", DispatchOperation{ID: "job-1", LogicalAttempt: 1, WorkspaceID: "w"}},
		{"missing id", DispatchOperation{Kind: budget.OperationDSL, LogicalAttempt: 1, WorkspaceID: "w"}},
		{"zero attempt", DispatchOperation{Kind: budget.OperationDSL, ID: "job-1", WorkspaceID: "w"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithDispatchOperation(context.Background(), tc.operation)
			if _, err := dispatchIdentity(ctx, budget.OrdinalPrimary); err == nil {
				t.Fatal("expected an incomplete operation to be rejected")
			}
		})
	}
}

func TestVerificationDispatchContextDerivesADistinctIdentity(t *testing.T) {
	ctx := WithDispatchOperation(context.Background(), DispatchOperation{
		Kind:           budget.OperationEnhance,
		ID:             "job-1",
		LogicalAttempt: 1,
		WorkspaceID:    "default",
	})
	primary, err := dispatchIdentity(ctx, budget.OrdinalPrimary)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := dispatchIdentity(verificationDispatchContext(ctx), budget.OrdinalPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if primary == verification {
		t.Fatal("a reflection call must not reuse the enhancement dispatch identity")
	}
	if verification.OperationID != "job-1:verify" {
		t.Fatalf("verification operation id = %q, want job-1:verify", verification.OperationID)
	}
	// The derivation is idempotent so a nested call cannot keep appending.
	twice := verificationDispatchContext(verificationDispatchContext(ctx))
	again, err := dispatchIdentity(twice, budget.OrdinalPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if again != verification {
		t.Fatalf("verification identity = %+v, want it to be stable", again)
	}
}

func TestVerificationDispatchContextWithoutOperationStaysUnbound(t *testing.T) {
	ctx := verificationDispatchContext(context.Background())
	if _, ok := DispatchOperationFrom(ctx); ok {
		t.Fatal("verification must not invent an identity where none was bound")
	}
}

func TestCompletionDispatchContextDerivesStableStageIdentity(t *testing.T) {
	base := WithDispatchOperation(context.Background(), DispatchOperation{
		Kind: budget.OperationRequirement, ID: "job-1", LogicalAttempt: 2, WorkspaceID: "default",
	})
	chunk := 3
	cases := []struct {
		metadata CompletionTraceMetadata
		wantID   string
	}{
		{CompletionTraceMetadata{Phase: CompletionPhaseAnalysis, ChunkIndex: &chunk}, "job-1:analysis:3"},
		{CompletionTraceMetadata{Phase: CompletionPhaseSynthesis}, "job-1:synthesis"},
		{CompletionTraceMetadata{Phase: CompletionPhaseFinal}, "job-1:final"},
		{CompletionTraceMetadata{Phase: CompletionPhaseSelectorRepair}, "job-1:selector-repair"},
	}
	for _, tc := range cases {
		ctx := completionDispatchContext(base, tc.metadata)
		operation, ok := DispatchOperationFrom(ctx)
		if !ok || operation.ID != tc.wantID {
			t.Fatalf("metadata %+v produced %+v, want id %q", tc.metadata, operation, tc.wantID)
		}
		again, _ := DispatchOperationFrom(completionDispatchContext(ctx, tc.metadata))
		if again.ID != tc.wantID {
			t.Fatalf("stage suffix is not idempotent: %q", again.ID)
		}
	}
}

func TestCompletionDispatchContextDoesNotInventSuffixForIncompleteMetadata(t *testing.T) {
	base := WithDispatchOperation(context.Background(), DispatchOperation{
		Kind: budget.OperationDSL, ID: "job-1", LogicalAttempt: 1, WorkspaceID: "default",
	})
	ctx := completionDispatchContext(base, CompletionTraceMetadata{Phase: CompletionPhaseAnalysis})
	operation, _ := DispatchOperationFrom(ctx)
	if operation.ID != "job-1" {
		t.Fatalf("incomplete metadata invented operation id %q", operation.ID)
	}
}

func TestUsageFromResponse(t *testing.T) {
	cases := []struct {
		name            string
		resp            *CompletionResponse
		wantOK          bool
		wantViolation   bool
		wantReason      string
		wantUncached    int
		wantCached      int
		wantOutput      int
		wantCachedSplit bool
	}{
		{name: "nil response", wantReason: "missing_usage"},
		{name: "no usage reported", resp: &CompletionResponse{Content: "ok"}, wantReason: "missing_usage"},
		{
			name: "omitted output is not reported zero",
			resp: &CompletionResponse{
				InputTokens: 10,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent: true,
				},
			},
			wantReason: "missing_usage",
		},
		{
			name: "omitted input is not reported zero",
			resp: &CompletionResponse{
				OutputTokens: 5,
				UsageMetadata: CompletionUsageMetadata{
					OutputTokensPresent: true,
				},
			},
			wantReason: "missing_usage",
		},
		{
			name: "reported zeroes are trustworthy",
			resp: &CompletionResponse{
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:  true,
					OutputTokensPresent: true,
				},
			},
			wantOK: true,
		},
		{
			name: "both totals without cached split",
			resp: &CompletionResponse{
				InputTokens: 10, OutputTokens: 5,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:  true,
					OutputTokensPresent: true,
				},
			},
			wantOK: true, wantUncached: 10, wantOutput: 5,
		},
		{
			name: "trusted cached split",
			resp: &CompletionResponse{
				InputTokens: 10, CachedInputTokens: 3, OutputTokens: 5,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:       true,
					OutputTokensPresent:      true,
					CachedInputTokensPresent: true,
				},
			},
			wantOK: true, wantUncached: 7, wantCached: 3, wantOutput: 5, wantCachedSplit: true,
		},
		{
			name: "negative input",
			resp: &CompletionResponse{
				InputTokens: -1, OutputTokens: 5,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:  true,
					OutputTokensPresent: true,
				},
			},
			wantViolation: true, wantReason: "contradictory_usage",
		},
		{
			name: "cached exceeds total",
			resp: &CompletionResponse{
				InputTokens: 1, CachedInputTokens: 2, OutputTokens: 5,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:       true,
					OutputTokensPresent:      true,
					CachedInputTokensPresent: true,
				},
			},
			wantViolation: true, wantReason: "contradictory_usage",
		},
		{
			name: "unknown category",
			resp: &CompletionResponse{
				InputTokens: 1, OutputTokens: 1,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:  true,
					OutputTokensPresent: true,
					UnknownCategories:   []string{"future_billable_tokens"},
				},
			},
			wantViolation: true, wantReason: "unknown_usage_category",
		},
		{
			name: "adapter reports contradictory redundant usage",
			resp: &CompletionResponse{
				InputTokens: 1, OutputTokens: 1,
				UsageMetadata: CompletionUsageMetadata{
					InputTokensPresent:  true,
					OutputTokensPresent: true,
					Contradictory:       true,
				},
			},
			wantViolation: true, wantReason: "contradictory_usage",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assessment := usageFromResponse(tc.resp)
			if assessment.Trustworthy != tc.wantOK {
				t.Fatalf("trustworthy = %v, want %v", assessment.Trustworthy, tc.wantOK)
			}
			if assessment.ContractViolation != tc.wantViolation {
				t.Fatalf("contract violation = %v, want %v", assessment.ContractViolation, tc.wantViolation)
			}
			if assessment.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", assessment.Reason, tc.wantReason)
			}
			if !tc.wantOK {
				return
			}
			if assessment.Usage.UncachedInputTokens != tc.wantUncached ||
				assessment.Usage.CachedInputTokens != tc.wantCached ||
				assessment.Usage.OutputTokens != tc.wantOutput ||
				assessment.Usage.CachedInputReported != tc.wantCachedSplit {
				t.Fatalf("usage = %+v", assessment.Usage)
			}
		})
	}
}
