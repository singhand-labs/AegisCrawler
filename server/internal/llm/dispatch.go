package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
)

// DispatchOperation identifies the logical attempt that owns a physical call.
// It is carried in the context rather than in CompletionRequest so it never
// enters the canonical request hash or the policy-bound cache key.
type DispatchOperation struct {
	// Kind and ID are the durable workflow identity, or a server-owned
	// operation ID for a synchronous Admin operation.
	Kind budget.OperationKind
	ID   string
	// LogicalAttempt counts from 1 and includes the initial attempt.
	LogicalAttempt int
	// WorkspaceID is the workspace persisted with a durable job. Background work
	// that carries no authenticated principal must supply it explicitly;
	// enforced mode never substitutes the default workspace.
	WorkspaceID string
	// WorkflowID is the parent DSL workflow id, populated only when
	// Kind == OperationDSL. It is the key for the per-workflow budget envelope
	// and is empty for synchronous admin operations.
	WorkflowID string
}

// DispatchAttempt normalizes a durable job attempt counter into a logical
// attempt number. Logical attempts count from 1 and include the initial
// attempt, so a job that has not yet recorded an attempt still dispatches under
// attempt 1.
func DispatchAttempt(attemptCount int) int {
	if attemptCount < 1 {
		return 1
	}
	return attemptCount
}

type dispatchOperationKey struct{}

// WithDispatchOperation binds the logical attempt identity for every physical
// call made under ctx.
func WithDispatchOperation(ctx context.Context, operation DispatchOperation) context.Context {
	return context.WithValue(ctx, dispatchOperationKey{}, operation)
}

// DispatchOperationFrom returns the logical attempt identity bound to ctx.
func DispatchOperationFrom(ctx context.Context) (DispatchOperation, bool) {
	operation, ok := ctx.Value(dispatchOperationKey{}).(DispatchOperation)
	return operation, ok
}

// resolveDispatchWorkspace returns the workspace that owns a physical call.
//
// It deliberately does not use authz.WorkspaceID, because that helper falls back
// to the default workspace for unauthenticated background calls. Enforced mode
// must fail closed instead of charging an unattributable call to the default
// workspace.
func resolveDispatchWorkspace(ctx context.Context, operation DispatchOperation) (string, error) {
	if workspace := strings.TrimSpace(operation.WorkspaceID); workspace != "" {
		return workspace, nil
	}
	principal, ok := authz.PrincipalFromContext(ctx)
	if ok && strings.TrimSpace(principal.WorkspaceID) != "" {
		return principal.WorkspaceID, nil
	}
	return "", fmt.Errorf(
		"enforced LLM policy requires an authenticated workspace or a workspace persisted with the operation")
}

// dispatchIdentity resolves the full ledger identity for one physical call.
func dispatchIdentity(ctx context.Context, ordinal int) (budget.DispatchIdentity, error) {
	operation, ok := DispatchOperationFrom(ctx)
	if !ok {
		return budget.DispatchIdentity{}, fmt.Errorf(
			"enforced LLM policy requires a dispatch operation identity for every completion")
	}
	workspace, err := resolveDispatchWorkspace(ctx, operation)
	if err != nil {
		return budget.DispatchIdentity{}, err
	}
	identity := budget.DispatchIdentity{
		WorkspaceID:     workspace,
		OperationKind:   operation.Kind,
		OperationID:     operation.ID,
		LogicalAttempt:  operation.LogicalAttempt,
		PhysicalOrdinal: ordinal,
	}
	if err := identity.Validate(); err != nil {
		return budget.DispatchIdentity{}, err
	}
	return identity, nil
}

// verificationOperationSuffix distinguishes a reflection/verification call from
// the enhancement call it checks. Both belong to the same workflow attempt but
// are separate physical calls, so each needs its own unique dispatch identity
// and its own independent budget admission.
const verificationOperationSuffix = ":verify"

// verificationDispatchContext derives the dispatch identity for a reflection
// call from the enclosing operation. When no operation is bound the context is
// returned unchanged so enforced mode still fails closed rather than inventing
// an identity.
func verificationDispatchContext(ctx context.Context) context.Context {
	operation, ok := DispatchOperationFrom(ctx)
	if !ok {
		return ctx
	}
	if strings.HasSuffix(operation.ID, verificationOperationSuffix) {
		return ctx
	}
	operation.ID += verificationOperationSuffix
	return WithDispatchOperation(ctx, operation)
}

// completionDispatchContext derives one stable operation id for each physical
// workflow stage. Durable requirement and DSL attempts can contain several
// analysis calls followed by one synthesis/final call; using only the parent
// job id would make the second cache miss collide with the first reservation.
//
// Unknown or incomplete metadata deliberately leaves the parent identity
// unchanged. A second call with that same incomplete identity then fails closed
// as a duplicate instead of inventing an unstable suffix.
func completionDispatchContext(ctx context.Context, metadata CompletionTraceMetadata) context.Context {
	operation, ok := DispatchOperationFrom(ctx)
	if !ok {
		return ctx
	}
	suffix := completionOperationSuffix(metadata)
	if suffix == "" || strings.HasSuffix(operation.ID, suffix) {
		return ctx
	}
	operation.ID += suffix
	return WithDispatchOperation(ctx, operation)
}

func completionOperationSuffix(metadata CompletionTraceMetadata) string {
	switch metadata.Phase {
	case CompletionPhaseAnalysis:
		if metadata.ChunkIndex == nil || *metadata.ChunkIndex < 0 {
			return ""
		}
		return fmt.Sprintf(":analysis:%d", *metadata.ChunkIndex)
	case CompletionPhaseSynthesis:
		return ":synthesis"
	case CompletionPhaseFinal:
		return ":final"
	case CompletionPhaseSelectorRepair:
		return ":selector-repair"
	default:
		return ""
	}
}

// preparedRates and preparedCaps project the immutable price and token snapshot
// out of a prepared call so the ledger never reaches into provider adapters.
func preparedRates(call PreparedCall) budget.Rates {
	return budget.Rates{
		InputUSDPerMillion:       call.InputUSDPerMillion,
		CachedInputUSDPerMillion: call.CachedInputUSDPerMillion,
		OutputUSDPerMillion:      call.OutputUSDPerMillion,
	}
}

func preparedCaps(call PreparedCall) budget.Caps {
	return budget.Caps{
		MaxInputTokens:  call.MaxInputTokens,
		MaxOutputTokens: call.MaxOutputTokens,
	}
}

type completionUsageAssessment struct {
	Usage             budget.Usage
	Trustworthy       bool
	ContractViolation bool
	Reason            string
	UnknownCategories []string
}

// usageFromResponse extracts adapter-proven usage from a provider response.
// Plain zero values are never enough: both total-input and output presence
// bits must be set by the adapter so an omitted JSON field cannot be mistaken
// for a provider-reported zero.
func usageFromResponse(resp *CompletionResponse) completionUsageAssessment {
	if resp == nil {
		return completionUsageAssessment{Reason: "missing_usage"}
	}
	metadata := resp.UsageMetadata
	if metadata.Contradictory {
		return completionUsageAssessment{
			ContractViolation: true,
			Reason:            "contradictory_usage",
		}
	}
	if len(metadata.UnknownCategories) > 0 {
		return completionUsageAssessment{
			ContractViolation: true,
			Reason:            "unknown_usage_category",
			UnknownCategories: append([]string(nil), metadata.UnknownCategories...),
		}
	}
	if !metadata.InputTokensPresent || !metadata.OutputTokensPresent {
		return completionUsageAssessment{Reason: "missing_usage"}
	}
	if resp.InputTokens < 0 || resp.CachedInputTokens < 0 || resp.OutputTokens < 0 {
		return completionUsageAssessment{
			ContractViolation: true,
			Reason:            "contradictory_usage",
		}
	}
	if resp.CachedInputTokens > 0 && !metadata.CachedInputTokensPresent {
		return completionUsageAssessment{
			ContractViolation: true,
			Reason:            "contradictory_usage",
		}
	}
	if resp.CachedInputTokens > resp.InputTokens {
		return completionUsageAssessment{
			ContractViolation: true,
			Reason:            "contradictory_usage",
		}
	}
	return completionUsageAssessment{
		Usage: budget.Usage{
			UncachedInputTokens: resp.InputTokens - resp.CachedInputTokens,
			CachedInputTokens:   resp.CachedInputTokens,
			OutputTokens:        resp.OutputTokens,
			CachedInputReported: metadata.CachedInputTokensPresent,
		},
		Trustworthy: true,
	}
}
