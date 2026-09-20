package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
)

const (
	// CodeProviderUnavailable is the stable synchronous/durable error code
	// after the permitted primary/fallback availability routing is exhausted.
	CodeProviderUnavailable = "LLM_PROVIDER_UNAVAILABLE"
	// CodeDuplicateDispatch reports a reused physical dispatch identity.
	CodeDuplicateDispatch = "LLM_DUPLICATE_DISPATCH"
)

// ErrRouteUnavailable is returned when an enforced route was not constructed
// or was administratively closed after a usage-contract violation. It is not
// an availability-class provider error and therefore never authorizes
// fallback.
var ErrRouteUnavailable = errors.New("enforced llm route unavailable")

// providerDispatchNotStartedError marks a failure that occurred before the
// provider network boundary. It preserves the underlying stable error while
// preventing CompleteWithTrace from inventing an "unknown provider" attempt.
type providerDispatchNotStartedError struct {
	err error
}

func (e *providerDispatchNotStartedError) Error() string { return e.err.Error() }
func (e *providerDispatchNotStartedError) Unwrap() error { return e.err }

func markProviderDispatchNotStarted(err error) error {
	if err == nil {
		return nil
	}
	var marked *providerDispatchNotStartedError
	if errors.As(err, &marked) {
		return err
	}
	return &providerDispatchNotStartedError{err: err}
}

func providerDispatchNotStarted(err error) bool {
	var marked *providerDispatchNotStartedError
	return errors.As(err, &marked)
}

type RouteUnavailableError struct {
	Route  string
	Reason string
}

func (e *RouteUnavailableError) Error() string {
	return fmt.Sprintf("%v: %s route: %s", ErrRouteUnavailable, e.Route, e.Reason)
}

func (e *RouteUnavailableError) Unwrap() error { return ErrRouteUnavailable }

// InputTokenLimitError reports that an exact adapter request cannot fit the
// enforced route's configured input ceiling. It unwraps to
// ErrRouteUnavailable so synchronous APIs and durable jobs treat the failure
// as terminal, while IsAvailabilityError still forbids fallback.
type InputTokenLimitError struct {
	Route      string
	UpperBound int
	Maximum    int
}

func (e *InputTokenLimitError) Error() string {
	return fmt.Sprintf(
		"%v: prepared %s route input upper bound %d exceeds enforced maximum %d",
		ErrRouteUnavailable,
		e.Route,
		e.UpperBound,
		e.Maximum,
	)
}

func (e *InputTokenLimitError) Unwrap() error { return ErrRouteUnavailable }

// StableDispatchErrorCode classifies the fail-closed errors shared by
// synchronous APIs and durable jobs. The returned codes are safe to persist
// and never include provider bodies, prompts, credentials, or SQL details.
func StableDispatchErrorCode(err error) (string, bool) {
	switch {
	case budget.IsDenied(err):
		// Surface CodeWorkflowBudgetExceeded so durable-job classifiers can
		// distinguish per-workflow denials from the generic global/workspace
		// cap. Both are still terminal.
		var denied *budget.DeniedError
		if errors.As(err, &denied) && denied.CodeString() == budget.CodeWorkflowBudgetExceeded {
			return budget.CodeWorkflowBudgetExceeded, true
		}
		return budget.CodeBudgetExceeded, true
	case budget.IsUnavailable(err):
		return budget.CodeLedgerUnavailable, true
	case errors.Is(err, budget.ErrDuplicateDispatch):
		return CodeDuplicateDispatch, true
	case errors.Is(err, ErrRouteUnavailable), IsAvailabilityError(err):
		return CodeProviderUnavailable, true
	default:
		return "", false
	}
}

// Provider abstracts an LLM completion backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

// InputTokenUpperBounder proves a conservative upper bound for the exact
// adapter request that would be sent for req. Enforced routes require this
// capability so the configured input-token ceiling is a real per-call bound,
// not merely a pricing assumption.
//
// Built-in adapters derive the bound from the byte length of their exact JSON
// wire body. A text tokenizer cannot emit more ordinary tokens than the UTF-8
// bytes it consumes, and the serialized envelope contains every prompt,
// schema, and protocol field supplied to the provider. JSON escaping only
// increases that byte count.
type InputTokenUpperBounder interface {
	InputTokenUpperBound(req CompletionRequest) (int, error)
}

// CompletionHTTPStatusError is implemented by sanitized provider errors that
// retain a transport status without exposing provider response bodies.
type CompletionHTTPStatusError interface {
	CompletionHTTPStatus() int
}

// CompletionErrorCodeError exposes a sanitized provider error code for
// observability without exposing provider response content.
type CompletionErrorCodeError interface {
	CompletionErrorCode() string
}

// CompletionValidationFeedbackError exposes bounded, sanitized structural
// diagnostics for a provider response that failed local schema validation.
// Implementations must never return raw provider content or scalar values.
type CompletionValidationFeedbackError interface {
	CompletionValidationFeedback() string
}

// IsAvailabilityError reports whether a failed completion is safe to route to
// the one permitted fallback. It deliberately excludes cancellation, capture,
// validation, authentication, and ordinary provider failures.
func IsAvailabilityError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrCompletionCapture) {
		return false
	}
	status := 0
	var statusError CompletionHTTPStatusError
	if errors.As(err, &statusError) {
		status = statusError.CompletionHTTPStatus()
		switch {
		case status == 408 || status == 429 || status >= 500 && status <= 599:
			return true
		case status >= 400 && status <= 499:
			// A provider supplied a definitive client/auth/validation status.
			// A subsequent body-read code or timeout cannot make that request
			// safe to repeat against another paid route.
			return false
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	var codeError CompletionErrorCodeError
	if errors.As(err, &codeError) {
		switch codeError.CompletionErrorCode() {
		case "transport_error":
			return true
		case "response_read_error":
			return status == 0 || status >= 200 && status <= 299
		}
	}
	return false
}

type CompletionExecutionPolicy string

const (
	// CompletionExecutionDefault preserves provider compatibility retries and
	// orchestrator retry/fallback behavior.
	CompletionExecutionDefault CompletionExecutionPolicy = ""
	// CompletionExecutionAtMostOnce prohibits every physical retry layer.
	CompletionExecutionAtMostOnce CompletionExecutionPolicy = "at_most_once"
)

// CompletionRequest is a normalized LLM request.
type CompletionRequest struct {
	Model            string
	System           string
	User             string
	Temperature      float64
	JSONMode         bool
	MaxOutputTokens  int                       `json:",omitempty"`
	StructuredOutput *StructuredOutput         `json:",omitempty"`
	ExecutionPolicy  CompletionExecutionPolicy `json:",omitempty"`
}

// PreparedCall is an immutable, policy-bound completion. In enforced mode it
// prevents callers from selecting an unapproved provider route, model, output
// cap, temperature, or execution policy after admission.
type PreparedCall struct {
	Route                    string
	Provider                 string
	Model                    string
	Endpoint                 string
	PolicyFingerprint        string
	PriceRevision            string
	RequestHash              string
	Request                  CompletionRequest
	MaxInputTokens           int
	MaxOutputTokens          int
	InputUSDPerMillion       config.USDNanos
	CachedInputUSDPerMillion config.USDNanos
	OutputUSDPerMillion      config.USDNanos
}

// StructuredOutput describes one forced function-call result. Providers that
// explicitly support strict tool output may send this as a strict function
// schema and return the validated function arguments as CompletionResponse
// content. Other providers preserve the existing JSON-mode path.
type StructuredOutput struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

// CompletionResponse is a normalized LLM response.
type CompletionResponse struct {
	Content           string
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	// UsageMetadata records whether the provider actually supplied every
	// category needed for trusted settlement. Zero-value metadata is
	// deliberately untrustworthy: custom adapters must opt in explicitly
	// rather than letting omitted JSON fields masquerade as reported zeroes.
	UsageMetadata CompletionUsageMetadata `json:",omitempty"`
	// ResponseID and FinishReason are optional, sanitized provider metadata.
	// Providers that do not expose these values leave them empty.
	ResponseID   string
	FinishReason string
}

// CompletionUsageMetadata is adapter evidence for the normalized usage
// values. UnknownCategories contains top-level usage members that the adapter
// cannot prove are already represented in the v1 pricing vocabulary.
type CompletionUsageMetadata struct {
	InputTokensPresent       bool     `json:",omitempty"`
	OutputTokensPresent      bool     `json:",omitempty"`
	CachedInputTokensPresent bool     `json:",omitempty"`
	Contradictory            bool     `json:",omitempty"`
	UnknownCategories        []string `json:",omitempty"`
}

// cloneCompletionRequest freezes every mutable member of a completion request.
func cloneCompletionRequest(req CompletionRequest) CompletionRequest {
	cloned := req
	if req.StructuredOutput != nil {
		output := *req.StructuredOutput
		output.Schema = append(json.RawMessage(nil), req.StructuredOutput.Schema...)
		cloned.StructuredOutput = &output
	}
	return cloned
}
