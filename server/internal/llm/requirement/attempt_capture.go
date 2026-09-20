package requirement

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

const (
	requirementAnalysisArtifactLimit = 64 << 10
	requirementFinalArtifactLimit    = 1 << 20
	requirementDiagnosticLimit       = 8 << 10
)

type providerCompletionArtifact struct {
	SchemaVersion string                     `json:"schemaVersion"`
	BoundaryKind  llm.CompletionBoundaryKind `json:"boundaryKind"`
	Content       string                     `json:"content"`
	ContentHash   string                     `json:"contentHash"`
	HTTPStatus    int                        `json:"httpStatus,omitempty"`
	ErrorCode     string                     `json:"errorCode,omitempty"`
	ErrorMessage  string                     `json:"errorMessage,omitempty"`
	Truncated     bool                       `json:"truncated"`
}

type requirementAttemptValidation struct {
	Phase   string `json:"phase"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type requirementAttemptArtifact struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Calls         []*models.LLMProviderCall    `json:"calls"`
	Result        any                          `json:"result,omitempty"`
	Validation    requirementAttemptValidation `json:"validation"`
}

type requirementAttemptCallLineage struct {
	ID              string                     `json:"id"`
	CallIndex       int                        `json:"callIndex"`
	CallKind        models.LLMProviderCallKind `json:"callKind"`
	ProviderAttempt int                        `json:"providerAttempt"`
	Phase           string                     `json:"phase"`
	RequestHash     string                     `json:"requestHash"`
	Dispatch        *models.LLMDispatchLineage `json:"dispatch,omitempty"`
	ResponseHash    string                     `json:"responseHash"`
}

type requirementAttemptOmittedResult struct {
	Hash    string `json:"hash"`
	Omitted bool   `json:"omitted"`
}

type requirementAttemptFallbackArtifact struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Calls         any                             `json:"calls"`
	Result        requirementAttemptOmittedResult `json:"result"`
	Validation    requirementAttemptValidation    `json:"validation"`
}

type requirementAttemptRecorder struct {
	store             *store.Store
	job               *models.RequirementJob
	recordingHash     string
	policyFingerprint string

	mu    sync.Mutex
	calls []*models.LLMProviderCall
}

func newRequirementAttemptRecorder(
	persistence *store.Store,
	job *models.RequirementJob,
	recordingHash string,
	policyFingerprints ...string,
) *requirementAttemptRecorder {
	policyFingerprint := ""
	if len(policyFingerprints) > 0 {
		policyFingerprint = policyFingerprints[0]
	}
	return &requirementAttemptRecorder{
		store: persistence, job: job, recordingHash: recordingHash,
		policyFingerprint: policyFingerprint, calls: []*models.LLMProviderCall{},
	}
}

func hashCompletionContent(content string) string {
	digest := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", digest)
}

func completionArtifactLimit(phase string) int {
	if phase == llm.CompletionPhaseAnalysis {
		return requirementAnalysisArtifactLimit
	}
	return requirementFinalArtifactLimit
}

func truncateUTF8Middle(value string, keptRunes int) string {
	runes := []rune(value)
	if keptRunes >= len(runes) {
		return value
	}
	const marker = "\n...[TRUNCATED]...\n"
	if keptRunes <= 0 {
		return marker
	}
	head := (keptRunes + 1) / 2
	tail := keptRunes - head
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

func boundedCompletionArtifact(content string, limit int) (providerCompletionArtifact, int, bool, error) {
	return boundedCompletionArtifactWithMetadata(content, limit, providerCompletionArtifact{})
}

func boundedCompletionArtifactWithMetadata(content string, limit int, metadata providerCompletionArtifact) (providerCompletionArtifact, int, bool, error) {
	artifact := metadata
	artifact.SchemaVersion = "aegiscrawler.llm-provider-completion.v1"
	artifact.Content = content
	artifact.ContentHash = hashCompletionContent(content)
	artifact.Truncated = false
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return providerCompletionArtifact{}, 0, false, err
	}
	if len(encoded) <= limit {
		return artifact, len(content), false, nil
	}

	runes := []rune(content)
	low, high := 0, len(runes)
	var best providerCompletionArtifact
	for low <= high {
		middle := low + (high-low)/2
		candidate := artifact
		candidate.Content = truncateUTF8Middle(content, middle)
		candidate.Truncated = true
		encoded, err = json.Marshal(candidate)
		if err != nil {
			return providerCompletionArtifact{}, 0, false, err
		}
		if len(encoded) <= limit {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best.SchemaVersion == "" {
		return providerCompletionArtifact{}, 0, false, fmt.Errorf("provider completion metadata exceeds %d-byte bound", limit)
	}
	return best, len(best.Content), true, nil
}

func sanitizeCompletionContent(content string) (string, bool) {
	sanitized := redact.String(content)
	return sanitized, sanitized != content
}

// CaptureCompletion implements llm.CompletionTraceSink. It persists the
// sanitized response synchronously before CompleteWithTrace releases it to the
// workflow parser.
func (r *requirementAttemptRecorder) CaptureCompletion(ctx context.Context, event llm.CompletionTraceEvent) error {
	if r == nil || r.store == nil || r.job == nil || event.Result == nil || event.Result.CompletionResponse == nil {
		return store.ErrLLMArtifactInvalid
	}
	rawContent := event.Result.Content
	content, redactedContent := sanitizeCompletionContent(rawContent)
	responseID := llm.SanitizeCompletionMetadata(event.Result.ResponseID)
	finishReason := llm.SanitizeCompletionMetadata(event.Result.FinishReason)
	provider := llm.SanitizeCompletionMetadata(event.Result.Provider)
	model := llm.SanitizeCompletionMetadata(event.Result.Model)
	errorCode := llm.SanitizeCompletionMetadata(event.ErrorCode)
	errorMessage := llm.SanitizeCompletionError(errors.New(event.ErrorMessage))
	metadataRedacted := responseID != strings.TrimSpace(event.Result.ResponseID) ||
		finishReason != strings.TrimSpace(event.Result.FinishReason) ||
		provider != strings.TrimSpace(event.Result.Provider) ||
		model != strings.TrimSpace(event.Result.Model) ||
		errorCode != strings.TrimSpace(event.ErrorCode) ||
		errorMessage != strings.TrimSpace(event.ErrorMessage)
	artifact, capturedBytes, truncated, err := boundedCompletionArtifactWithMetadata(
		content,
		completionArtifactLimit(event.Metadata.Phase),
		providerCompletionArtifact{
			BoundaryKind: event.BoundaryKind,
			HTTPStatus:   event.HTTPStatus,
			ErrorCode:    errorCode,
			ErrorMessage: errorMessage,
		},
	)
	if err != nil {
		return err
	}
	truncated = truncated || event.ContentTruncated
	artifact.Truncated = truncated
	redacted := event.ContentRedacted || event.ErrorRedacted || redactedContent || metadataRedacted

	r.mu.Lock()
	defer r.mu.Unlock()
	call := &models.LLMProviderCall{
		JobType:            models.LLMJobTypeRequirement,
		JobID:              r.job.ID,
		AttemptNumber:      r.job.AttemptCount,
		CallIndex:          len(r.calls) + 1,
		CallKind:           models.LLMProviderCallKind(event.BoundaryKind),
		ProviderAttempt:    event.ProviderAttempt,
		Phase:              event.Metadata.Phase,
		ChunkIndex:         event.Metadata.ChunkIndex,
		ChunkCount:         event.Metadata.ChunkCount,
		Provider:           provider,
		Model:              model,
		PromptVersion:      r.job.PromptVersion,
		RequestHash:        event.RequestHash,
		ResponseHash:       artifact.ContentHash,
		ResponseID:         responseID,
		FinishReason:       finishReason,
		HTTPStatus:         event.HTTPStatus,
		ErrorCode:          errorCode,
		InputTokens:        event.Result.InputTokens,
		OutputTokens:       event.Result.OutputTokens,
		CacheHit:           event.Result.CacheHit,
		OriginalBytes:      event.OriginalBytes,
		OriginalBytesExact: event.OriginalBytesExact,
		CapturedBytes:      capturedBytes,
		Redacted:           redacted,
		Truncated:          truncated,
		Replayable:         event.OriginalBytesExact && !redacted && !truncated,
	}
	if event.Dispatch != nil {
		call.Dispatch = &models.LLMDispatchLineage{
			OperationKind:     string(event.Dispatch.OperationKind),
			OperationID:       event.Dispatch.OperationID,
			LogicalAttempt:    event.Dispatch.LogicalAttempt,
			PhysicalOrdinal:   event.Dispatch.PhysicalOrdinal,
			RouteSlot:         event.Dispatch.RouteSlot,
			PolicyFingerprint: event.Dispatch.PolicyFingerprint,
			PriceRevision:     event.Dispatch.PriceRevision,
		}
	}
	if call.Provider == "" {
		call.Provider = "unknown"
	}
	if call.Model == "" {
		call.Model = "unknown"
	}
	if call.Phase == "" {
		return store.ErrLLMArtifactInvalid
	}
	if err := r.store.CreateLLMProviderCall(ctx, call, artifact); err != nil {
		return err
	}
	r.calls = append(r.calls, call)
	return nil
}

func boundedDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	value := redact.String(err.Error())
	if len(value) <= requirementDiagnosticLimit {
		return value
	}
	value = value[:requirementDiagnosticLimit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validationPhase(err error) string {
	switch {
	case err == nil:
		return "complete"
	case errors.Is(err, llm.ErrCompletionCapture):
		return "artifact-capture"
	case errors.Is(err, ErrInvalidProviderOutput):
		return "provider-output-validation"
	case errors.Is(err, ErrRequirementLineageConflict):
		return "lineage-validation"
	case errors.Is(err, ErrUnsafeRequirement):
		return "safety-validation"
	case errors.Is(err, ErrInvalidRequirement):
		return "schema-validation"
	default:
		return "provider-completion"
	}
}

func callsReplayable(calls []*models.LLMProviderCall, runErr error) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if !call.Replayable {
			return false
		}
	}
	return runErr == nil || errors.Is(runErr, ErrInvalidProviderOutput)
}

func sanitizeAttemptResult(result any) (any, bool, string, error) {
	original, err := json.Marshal(result)
	if err != nil {
		return nil, true, "", err
	}
	var normalized any
	if err := json.Unmarshal(original, &normalized); err != nil {
		return nil, true, "", err
	}
	// redact.Recording not required: input is LLM-attempt payload, not page content.
	sanitized := redact.Any(normalized)
	if sanitized == nil && normalized != nil {
		return nil, true, hashCompletionContent(string(original)), errors.New("redact parsed provider result")
	}
	return sanitized, !reflect.DeepEqual(normalized, sanitized), hashCompletionContent(string(original)), nil
}

func attemptValidation(runErr error) (models.LLMAttemptOutcome, requirementAttemptValidation) {
	outcome := models.LLMAttemptSucceeded
	validation := requirementAttemptValidation{Phase: validationPhase(runErr), Status: "passed"}
	if runErr != nil {
		outcome = models.LLMAttemptFailed
		_, validation.Code, validation.Message = classifyJobError(runErr)
		validation.Status = "failed"
		validation.Detail = boundedDiagnostic(runErr)
	}
	return outcome, validation
}

func compactCallLineage(calls []*models.LLMProviderCall) []requirementAttemptCallLineage {
	lineage := make([]requirementAttemptCallLineage, 0, len(calls))
	for _, call := range calls {
		lineage = append(lineage, requirementAttemptCallLineage{
			ID:              call.ID,
			CallIndex:       call.CallIndex,
			CallKind:        call.CallKind,
			ProviderAttempt: call.ProviderAttempt,
			Phase:           call.Phase,
			RequestHash:     call.RequestHash,
			Dispatch:        call.Dispatch,
			ResponseHash:    call.ResponseHash,
		})
	}
	return lineage
}

func boundedFallbackCalls(calls []*models.LLMProviderCall) any {
	lineage := compactCallLineage(calls)
	encoded, err := json.Marshal(lineage)
	if err == nil && len(encoded) <= store.MaxLLMAttemptReportArtifactBytes/2 {
		return lineage
	}
	summary := map[string]any{"count": len(calls)}
	if len(calls) > 0 {
		summary["first"] = compactCallLineage(calls[:1])[0]
		summary["last"] = compactCallLineage(calls[len(calls)-1:])[0]
	}
	return summary
}

// buildReport produces the immutable attempt report without writing it. The
// manager pairs it transactionally with the corresponding job transition.
// If the full report exceeds its hard bound, this returns a bounded diagnostic
// report and an artifact-capture error so the job fails closed without losing
// the provider-call lineage.
func (r *requirementAttemptRecorder) buildReport(result any, runErr error) (*models.LLMAttemptReport, any, error) {
	if r == nil {
		return nil, nil, runErr
	}
	r.mu.Lock()
	calls := make([]*models.LLMProviderCall, len(r.calls))
	copy(calls, r.calls)
	r.mu.Unlock()

	sanitizedResult, resultRedacted, resultHash, sanitizeErr := sanitizeAttemptResult(result)
	if sanitizeErr != nil {
		runErr = fmt.Errorf("%w: sanitize parsed provider result: %v", llm.ErrCompletionCapture, sanitizeErr)
		sanitizedResult = nil
		resultRedacted = true
	}
	outcome, validation := attemptValidation(runErr)
	replayable := callsReplayable(calls, runErr) && !resultRedacted
	safetyFlags := []string{"artifact-capture:passed"}
	if resultRedacted {
		safetyFlags = append(safetyFlags, "artifact-result-redacted:true")
	}
	if replayable {
		safetyFlags = append(safetyFlags, "artifact-replayable:true")
	} else {
		safetyFlags = append(safetyFlags, "artifact-replayable:false")
	}
	report := &models.LLMAttemptReport{
		JobType:           models.LLMJobTypeRequirement,
		JobID:             r.job.ID,
		AttemptNumber:     r.job.AttemptCount,
		Outcome:           outcome,
		PromptVersion:     r.job.PromptVersion,
		PolicyFingerprint: r.policyFingerprint,
		RecordingHash:     r.recordingHash,
		RequirementHash:   r.job.RequestHash,
		ValidationPhase:   validation.Phase,
		SafetyFlags:       safetyFlags,
		Replayable:        replayable,
	}
	for _, call := range calls {
		report.Provider = call.Provider
		report.Model = call.Model
		report.InputTokens += call.InputTokens
		report.OutputTokens += call.OutputTokens
	}
	if runErr != nil {
		_, report.ErrorCode, report.ErrorMessage = classifyJobError(runErr)
	}
	artifact := requirementAttemptArtifact{
		SchemaVersion: "aegiscrawler.requirement-attempt.v1",
		Calls:         calls,
		Result:        sanitizedResult,
		Validation:    validation,
	}
	encoded, marshalErr := json.Marshal(artifact)
	if marshalErr == nil && len(encoded) <= store.MaxLLMAttemptReportArtifactBytes {
		return report, artifact, runErr
	}

	if marshalErr != nil {
		runErr = fmt.Errorf("%w: marshal requirement attempt report: %v", llm.ErrCompletionCapture, marshalErr)
	} else {
		runErr = fmt.Errorf(
			"%w: requirement attempt report exceeded %d-byte bound",
			llm.ErrCompletionCapture,
			store.MaxLLMAttemptReportArtifactBytes,
		)
	}
	outcome, validation = attemptValidation(runErr)
	report.Outcome = outcome
	report.ValidationPhase = validation.Phase
	_, report.ErrorCode, report.ErrorMessage = classifyJobError(runErr)
	report.Replayable = false
	report.SafetyFlags = []string{
		"artifact-capture:bounded-fallback",
		"artifact-result-omitted:true",
		"artifact-replayable:false",
	}
	fallback := requirementAttemptFallbackArtifact{
		SchemaVersion: "aegiscrawler.requirement-attempt-fallback.v1",
		Calls:         boundedFallbackCalls(calls),
		Result: requirementAttemptOmittedResult{
			Hash:    resultHash,
			Omitted: true,
		},
		Validation: validation,
	}
	return report, fallback, runErr
}

// RequirementAttemptDetail is the authorized, bounded attempt artifact and
// ordered provider-call metadata returned by the manager.
type RequirementAttemptDetail struct {
	Report   *models.LLMAttemptReport
	Calls    []*models.LLMProviderCall
	Artifact any
}
