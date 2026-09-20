package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

// withServerOwnedDispatch binds a server-owned operation identity for one
// synchronous completion. The workspace is resolved from the authenticated
// principal inside the orchestrator; a client field never selects it.
func withServerOwnedDispatch(ctx context.Context, kind budget.OperationKind) context.Context {
	return llm.WithDispatchOperation(ctx, llm.DispatchOperation{
		Kind:           kind,
		ID:             store.NewID(),
		LogicalAttempt: 1,
	})
}

// writeLLMDispatchError maps an enforced-policy completion failure to its stable
// API contract:
//
//   - budget denial      -> 429 LLM_BUDGET_EXCEEDED
//   - ledger failure     -> 503 BUDGET_LEDGER_UNAVAILABLE
//   - availability class -> 503 LLM_PROVIDER_UNAVAILABLE
//
// It reports whether the error was mapped, so callers keep their existing
// behavior for every other failure. Messages are sanitized and never include
// provider content, prompts, or credentials.
func writeLLMDispatchError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case budget.IsDenied(err):
		writeError(w, http.StatusTooManyRequests, budget.CodeBudgetExceeded,
			"the configured LLM budget for this request or UTC day is exhausted")
		return true
	case budget.IsUnavailable(err):
		writeError(w, http.StatusServiceUnavailable, budget.CodeLedgerUnavailable,
			"the LLM budget ledger is unavailable, so no provider call can be admitted")
		return true
	case errors.Is(err, budget.ErrDuplicateDispatch):
		writeError(w, http.StatusConflict, llm.CodeDuplicateDispatch,
			"this logical attempt already dispatched a physical call")
		return true
	case llm.IsAvailabilityError(err), errors.Is(err, llm.ErrRouteUnavailable):
		writeError(w, http.StatusServiceUnavailable, llm.CodeProviderUnavailable,
			"every configured LLM route is unavailable")
		return true
	default:
		return false
	}
}
