package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
)

const (
	CompletionPhaseAnalysis       = "analysis"
	CompletionPhaseSynthesis      = "synthesis"
	CompletionPhaseFinal          = "final"
	CompletionPhaseSelectorRepair = "selector-repair"
)

type CompletionBoundaryKind string

const (
	CompletionBoundaryProviderResponse CompletionBoundaryKind = "provider_response"
	CompletionBoundaryProviderError    CompletionBoundaryKind = "provider_error"
	CompletionBoundaryCacheHit         CompletionBoundaryKind = "cache_hit"
)

var ErrCompletionCapture = errors.New("llm completion capture failed")

// The sink may defensively redact, compress, encrypt, and persist up to the
// bounded artifact ceiling. Keep that work independent from request
// cancellation, but never let it block a worker indefinitely.
const completionCaptureTimeout = 30 * time.Second

// CompletionCompleter is the provider-independent completion surface used by
// workflow tracing.
type CompletionCompleter interface {
	Complete(context.Context, CompletionRequest) (*CompletionResult, error)
}

// CompletionTraceMetadata identifies one logical workflow call. Chunk indices
// are zero-based and omitted for non-chunk calls.
type CompletionTraceMetadata struct {
	Phase      string
	ChunkIndex *int
	ChunkCount int
}

// CompletionDispatchLineage is the secret-free join key shared by enforced
// completion traces, attempt artifacts, and budget ledger entries. Cache hits
// carry the current logical identity and active policy lineage even though
// they create no ledger reservation.
type CompletionDispatchLineage struct {
	OperationKind     budget.OperationKind
	OperationID       string
	LogicalAttempt    int
	PhysicalOrdinal   int
	RouteSlot         string
	PolicyFingerprint string
	PriceRevision     string
	RequestHash       string
}

// CompletionTraceEvent exists only in worker memory. Durable sinks must store
// the canonical request hash rather than the request prompt itself.
type CompletionTraceEvent struct {
	Metadata        CompletionTraceMetadata
	Dispatch        *CompletionDispatchLineage
	Request         CompletionRequest
	RequestHash     string
	Result          *CompletionResult
	BoundaryKind    CompletionBoundaryKind
	ProviderAttempt int
	HTTPStatus      int
	ErrorCode       string
	ErrorMessage    string
	OriginalBytes   int
	// OriginalBytesExact distinguishes a fully observed response length from
	// the lower bound recorded when a provider body is cut off at a transport
	// limit or interrupted while reading.
	OriginalBytesExact bool
	ContentRedacted    bool
	ContentTruncated   bool
	ErrorRedacted      bool
}

// CompletionContentProvenance describes bytes discarded before a bounded
// provider error reaches the durable trace sink.
type CompletionContentProvenance struct {
	OriginalBytes      int
	OriginalBytesExact bool
	Truncated          bool
}

// CompletionTraceSink durably captures a physical provider response/error or a
// logical cache hit before workflow parsing and validation can consume it.
type CompletionTraceSink interface {
	CaptureCompletion(context.Context, CompletionTraceEvent) error
}

type completionTraceSinkContextKey struct{}
type completionTraceStateContextKey struct{}
type completionDispatchLineageContextKey struct{}

type completionTraceState struct {
	sink     CompletionTraceSink
	metadata CompletionTraceMetadata

	mu               sync.Mutex
	providerAttempts int
	events           int
	last             completionBoundaryRecord
}

type completionBoundaryRecord struct {
	kind         CompletionBoundaryKind
	requestHash  string
	responseHash string
	provider     string
	errorHash    [sha256.Size]byte
}

// WithCompletionTraceSink attaches a per-attempt durable completion sink.
func WithCompletionTraceSink(ctx context.Context, sink CompletionTraceSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, completionTraceSinkContextKey{}, sink)
}

// CanonicalCompletionRequestHash returns the stable SHA-256 hash used by both
// response caching and durable call lineage. CompletionRequest is a struct, so
// its JSON field order is deterministic.
func CanonicalCompletionRequestHash(request CompletionRequest) string {
	encoded, _ := json.Marshal(request)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest)
}

func traceState(ctx context.Context) *completionTraceState {
	state, _ := ctx.Value(completionTraceStateContextKey{}).(*completionTraceState)
	return state
}

func withCompletionDispatchLineage(
	ctx context.Context,
	prepared PreparedCall,
	identity budget.DispatchIdentity,
) context.Context {
	lineage := CompletionDispatchLineage{
		OperationKind:     identity.OperationKind,
		OperationID:       identity.OperationID,
		LogicalAttempt:    identity.LogicalAttempt,
		PhysicalOrdinal:   identity.PhysicalOrdinal,
		RouteSlot:         prepared.Route,
		PolicyFingerprint: prepared.PolicyFingerprint,
		PriceRevision:     prepared.PriceRevision,
		RequestHash:       prepared.RequestHash,
	}
	return context.WithValue(ctx, completionDispatchLineageContextKey{}, lineage)
}

func completionDispatchLineage(ctx context.Context) *CompletionDispatchLineage {
	lineage, ok := ctx.Value(completionDispatchLineageContextKey{}).(CompletionDispatchLineage)
	if !ok {
		return nil
	}
	return &lineage
}

func (s *completionTraceState) eventCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

func canonicalCompletionResponseHash(response *CompletionResponse) string {
	if response == nil {
		return ""
	}
	encoded, _ := json.Marshal(response)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest)
}

func (s *completionTraceState) terminalResponseCapturedSince(
	eventsBefore int,
	kind CompletionBoundaryKind,
	provider string,
	response *CompletionResponse,
) bool {
	if s == nil || response == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events > eventsBefore &&
		s.last.kind == kind &&
		s.last.provider == SanitizeCompletionMetadata(provider) &&
		s.last.responseHash == canonicalCompletionResponseHash(response)
}

func (s *completionTraceState) terminalErrorCapturedSince(eventsBefore int, terminalErr error) bool {
	if s == nil || terminalErr == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events <= eventsBefore || s.last.kind != CompletionBoundaryProviderError {
		return false
	}
	for current := terminalErr; current != nil; current = errors.Unwrap(current) {
		if s.last.errorHash == sha256.Sum256([]byte(current.Error())) {
			return true
		}
	}
	return false
}

func traceCompletionBoundary(
	ctx context.Context,
	request CompletionRequest,
	result *CompletionResult,
	kind CompletionBoundaryKind,
	httpStatus int,
	errorCode string,
	errorMessage string,
	originalBytes int,
	originalBytesExact bool,
	contentRedacted bool,
	contentTruncated bool,
	errorRedacted bool,
) error {
	state := traceState(ctx)
	if state == nil || state.sink == nil {
		return nil
	}
	if result == nil || result.CompletionResponse == nil {
		return errors.New("completion boundary returned an empty response")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	providerAttempt := 0
	if kind != CompletionBoundaryCacheHit {
		state.providerAttempts++
		providerAttempt = state.providerAttempts
	}
	requestHash := CanonicalCompletionRequestHash(request)
	dispatch := completionDispatchLineage(ctx)
	if dispatch != nil && dispatch.RequestHash != requestHash {
		return fmt.Errorf("%w: completion request hash does not match admitted dispatch", ErrCompletionCapture)
	}
	safeErrorMessage := SanitizeCompletionError(errors.New(errorMessage))
	event := CompletionTraceEvent{
		Metadata:           state.metadata,
		Dispatch:           dispatch,
		Request:            request,
		RequestHash:        requestHash,
		Result:             result,
		BoundaryKind:       kind,
		ProviderAttempt:    providerAttempt,
		HTTPStatus:         httpStatus,
		ErrorCode:          SanitizeCompletionMetadata(errorCode),
		ErrorMessage:       safeErrorMessage,
		OriginalBytes:      originalBytes,
		OriginalBytesExact: originalBytesExact,
		ContentRedacted:    contentRedacted,
		ContentTruncated:   contentTruncated,
		ErrorRedacted:      errorRedacted || safeErrorMessage != strings.TrimSpace(errorMessage),
	}
	record := completionBoundaryRecord{
		kind:         kind,
		requestHash:  event.RequestHash,
		responseHash: canonicalCompletionResponseHash(result.CompletionResponse),
		provider:     SanitizeCompletionMetadata(result.Provider),
		errorHash:    sha256.Sum256([]byte(errorMessage)),
	}
	// A provider may return its terminal response at the same instant that the
	// worker's request context is cancelled. Preserve workspace/auth values but
	// detach cancellation long enough for one bounded fail-closed persistence
	// attempt. This never lets a provider response escape without a durable ack.
	captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completionCaptureTimeout)
	defer cancel()
	if err := state.sink.CaptureCompletion(captureCtx, event); err != nil {
		return fmt.Errorf("%w: %s", ErrCompletionCapture, SanitizeCompletionError(err))
	}
	state.events++
	state.last = record
	return nil
}

// TraceProviderResponse records one physical provider response. Provider
// adapters call this at the HTTP boundary; the orchestrator provides the same
// fallback for test/custom providers that do not implement boundary tracing.
func TraceProviderResponse(ctx context.Context, request CompletionRequest, provider, model string, response *CompletionResponse, httpStatus int) error {
	if response == nil {
		return errors.New("provider returned an empty response")
	}
	normalizeCompletionUsage(response)
	return traceCompletionBoundary(ctx, request, &CompletionResult{
		CompletionResponse: response,
		Provider:           SanitizeCompletionMetadata(provider),
		Model:              SanitizeCompletionMetadata(model),
	}, CompletionBoundaryProviderResponse, httpStatus, "", "", len(response.Content), true, false, false, false)
}

// normalizeCompletionUsage keeps impossible provider counters away from
// durable artifacts and monotonic metrics while preserving the evidence needed
// for fail-closed settlement. Provider responses are normalized in place so
// tracing, hashing, settlement, caching, and the caller all observe one value.
func normalizeCompletionUsage(response *CompletionResponse) {
	if response == nil {
		return
	}
	if response.InputTokens < 0 {
		response.InputTokens = 0
		response.UsageMetadata.Contradictory = true
	}
	if response.CachedInputTokens < 0 {
		response.CachedInputTokens = 0
		response.UsageMetadata.Contradictory = true
	}
	if response.OutputTokens < 0 {
		response.OutputTokens = 0
		response.UsageMetadata.Contradictory = true
	}
}

// TraceProviderError records one physical provider failure. The body and error
// are recursively redacted before the sink sees them.
func TraceProviderError(
	ctx context.Context,
	request CompletionRequest,
	provider, model string,
	httpStatus int,
	errorCode, body string,
	providerErr error,
	provenance ...CompletionContentProvenance,
) error {
	rawContent := body
	rawError := ""
	if providerErr != nil {
		rawError = providerErr.Error()
	}
	safeBody := redact.String(rawContent)
	originalBytes := len(rawContent)
	originalBytesExact := true
	contentTruncated := false
	if len(provenance) > 0 {
		if provenance[0].OriginalBytes >= 0 {
			originalBytes = provenance[0].OriginalBytes
		}
		originalBytesExact = provenance[0].OriginalBytesExact
		contentTruncated = provenance[0].Truncated
	}
	errorRedacted := false
	var redactionSource interface{ CompletionErrorRedacted() bool }
	if errors.As(providerErr, &redactionSource) {
		errorRedacted = redactionSource.CompletionErrorRedacted()
	}
	return traceCompletionBoundary(ctx, request, &CompletionResult{
		CompletionResponse: &CompletionResponse{Content: safeBody},
		Provider:           SanitizeCompletionMetadata(provider),
		Model:              SanitizeCompletionMetadata(model),
	}, CompletionBoundaryProviderError, httpStatus, errorCode, rawError, originalBytes, originalBytesExact, safeBody != rawContent, contentTruncated, errorRedacted)
}

// TraceCacheHit records provenance for one logical cache read without counting
// it as another physical provider attempt.
func TraceCacheHit(ctx context.Context, request CompletionRequest, result *CompletionResult) error {
	originalBytes := 0
	if result != nil && result.CompletionResponse != nil {
		originalBytes = len(result.Content)
	}
	return traceCompletionBoundary(ctx, request, result, CompletionBoundaryCacheHit, 0, "", "", originalBytes, true, false, false, false)
}

// CompleteWithTrace supplies logical phase metadata to lower physical provider
// boundaries. A capture failure stops retry/fallback and withholds the response
// from both workflow parsing and cache insertion.
func CompleteWithTrace(ctx context.Context, completer CompletionCompleter, request CompletionRequest, metadata CompletionTraceMetadata) (*CompletionResult, error) {
	if completer == nil {
		return nil, errors.New("llm completer is unavailable")
	}
	// A durable workflow attempt may contain several independently billed
	// completion stages. Bind the stable phase/chunk suffix before cache lookup
	// or admission so each stage has one unique primary/fallback tuple.
	ctx = completionDispatchContext(ctx, metadata)
	sink, _ := ctx.Value(completionTraceSinkContextKey{}).(CompletionTraceSink)
	if sink == nil {
		result, err := completer.Complete(ctx, request)
		if err == nil && (result == nil || result.CompletionResponse == nil) {
			return nil, errors.New("llm completer returned an empty response")
		}
		return result, err
	}
	state := &completionTraceState{sink: sink, metadata: metadata}
	tracedCtx := context.WithValue(ctx, completionTraceStateContextKey{}, state)
	eventsBefore := state.eventCount()
	result, err := completer.Complete(tracedCtx, request)
	if errors.Is(err, ErrCompletionCapture) {
		return nil, err
	}
	if providerDispatchNotStarted(err) {
		// Admission, identity, readiness, and durable-marker failures are not
		// provider attempts. Any real primary boundary already captured before
		// a fallback admission failure remains in the sink; no synthetic
		// "unknown provider" event is added.
		return nil, err
	}
	if err == nil && (result == nil || result.CompletionResponse == nil) {
		err = errors.New("llm completer returned an empty response")
	}
	terminalCaptured := false
	switch {
	case err != nil:
		terminalCaptured = state.terminalErrorCapturedSince(eventsBefore, err)
	case result.CacheHit:
		terminalCaptured = state.terminalResponseCapturedSince(
			eventsBefore,
			CompletionBoundaryCacheHit,
			result.Provider,
			result.CompletionResponse,
		)
	default:
		terminalCaptured = state.terminalResponseCapturedSince(
			eventsBefore,
			CompletionBoundaryProviderResponse,
			result.Provider,
			result.CompletionResponse,
		)
	}
	if !terminalCaptured {
		if err != nil {
			if captureErr := TraceProviderError(
				tracedCtx,
				request,
				"unknown",
				request.Model,
				0,
				"provider_error",
				"",
				err,
			); captureErr != nil {
				return nil, captureErr
			}
		} else {
			var captureErr error
			if result.CacheHit {
				captureErr = TraceCacheHit(tracedCtx, request, result)
			} else {
				captureErr = traceCompletionBoundary(
					tracedCtx,
					request,
					result,
					CompletionBoundaryProviderResponse,
					0,
					"",
					"",
					len(result.Content),
					true,
					false,
					false,
					false,
				)
			}
			if captureErr != nil {
				return nil, captureErr
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SanitizeCompletionMetadata defensively redacts and bounds provider-supplied
// identifiers and stop reasons before they reach logs, models, or storage.
func SanitizeCompletionMetadata(value string) string {
	value = strings.TrimSpace(value)
	sanitized := redact.String(value)
	const maxMetadataBytes = 256
	return truncateValidUTF8(sanitized, maxMetadataBytes)
}

// SanitizeCompletionError produces a bounded recursively-redacted message safe
// for logs, diagnostics, and durable metadata.
func SanitizeCompletionError(err error) string {
	if err == nil {
		return ""
	}
	const maxErrorBytes = 8 << 10
	return truncateValidUTF8(redact.String(err.Error()), maxErrorBytes)
}

func truncateValidUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

type sanitizedCompletionError struct {
	prefix string
	cause  error
}

func (e *sanitizedCompletionError) Error() string {
	if e.prefix == "" {
		return SanitizeCompletionError(e.cause)
	}
	return e.prefix + ": " + SanitizeCompletionError(e.cause)
}

func (e *sanitizedCompletionError) Unwrap() error {
	return e.cause
}

// WrapCompletionError preserves errors.Is/errors.As while preventing an
// arbitrary provider error string from reaching logs unredacted.
func WrapCompletionError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return &sanitizedCompletionError{prefix: prefix, cause: err}
}
