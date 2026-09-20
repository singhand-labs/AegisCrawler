package dsl

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
	platformrule "github.com/singhand-labs/AegisCrawler/internal/rule"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

const (
	dslAnalysisArtifactLimit = 64 << 10
	dslFinalArtifactLimit    = 1 << 20
	dslDiagnosticLimit       = 8 << 10
)

type dslProviderCompletionArtifact struct {
	SchemaVersion string                     `json:"schemaVersion"`
	BoundaryKind  llm.CompletionBoundaryKind `json:"boundaryKind"`
	Content       string                     `json:"content"`
	ContentHash   string                     `json:"contentHash"`
	HTTPStatus    int                        `json:"httpStatus,omitempty"`
	ErrorCode     string                     `json:"errorCode,omitempty"`
	ErrorMessage  string                     `json:"errorMessage,omitempty"`
	Truncated     bool                       `json:"truncated"`
}

// DSLAttemptValidation records each server gate separately. ProviderIR is the
// parsed provider object before server resolution/canonicalization; ResolvedRule
// is the exact CSS rule that was validated and would be replayed. Keeping both
// fields now avoids an artifact format change when opaque selectors land.
type DSLAttemptValidation struct {
	Phase       string   `json:"phase"`
	Status      string   `json:"status"`
	Code        string   `json:"code,omitempty"`
	Message     string   `json:"message,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	SafetyFlags []string `json:"safetyFlags,omitempty"`
}

func failedDSLAttemptValidation(phase string, err error, flags ...string) DSLAttemptValidation {
	_, code, message := classifyDSLJobError(err)
	return DSLAttemptValidation{
		Phase: phase, Status: "failed", Code: code, Message: message,
		Detail: boundedDSLDiagnostic(err), SafetyFlags: append([]string(nil), flags...),
	}
}

type DSLAttemptArtifact struct {
	SchemaVersion          string                    `json:"schemaVersion"`
	Calls                  []*models.LLMProviderCall `json:"calls"`
	ProviderIRSourceCallID string                    `json:"providerIRSourceCallId,omitempty"`
	ProviderIR             any                       `json:"providerIR,omitempty"`
	SelectorRepair         *DSLSelectorRepairLineage `json:"selectorRepair,omitempty"`
	ResolvedRule           *models.Rule              `json:"resolvedRule,omitempty"`
	// TrustedBaseline and SelectorCatalog retain the exact server-prepared
	// trusted baseline rule and opaque selector-candidate catalog most
	// recently prepared for this attempt. They make a durable attempt report
	// self-contained so a qualification archive can be reconstructed without
	// re-deriving or re-fetching either artifact. The catalog's catalogHash
	// remains its base64url protocol identity; the baseline hash on the report
	// remains hex.
	TrustedBaseline *models.Rule           `json:"trustedBaseline,omitempty"`
	SelectorCatalog any                    `json:"selectorCatalog,omitempty"`
	Validation      []DSLAttemptValidation `json:"validation"`
}

// DSLSelectorRepairLineage proves the full source attempt -> patch call ->
// derived opaque provider IR -> resolved CSS rule chain.
type DSLSelectorRepairLineage struct {
	SourceAttemptReportID string                                        `json:"sourceAttemptReportId"`
	SourceProviderCallID  string                                        `json:"sourceProviderCallId"`
	SourceProviderIRHash  string                                        `json:"sourceProviderIrHash"`
	SourceProviderIR      *models.Rule                                  `json:"sourceProviderIr,omitempty"`
	PatchProviderIRHash   string                                        `json:"patchProviderIrHash,omitempty"`
	RepairPlanHash        string                                        `json:"repairPlanHash"`
	Diagnostic            DSLAttemptValidation                          `json:"diagnostic"`
	OutputContract        json.RawMessage                               `json:"outputContract"`
	Failure               *platformrule.SelectorCandidateSelectionError `json:"failure"`
	DerivedProviderIRHash string                                        `json:"derivedProviderIrHash,omitempty"`
	DerivedProviderIR     *models.Rule                                  `json:"derivedProviderIr,omitempty"`
	Slots                 []platformrule.SelectorRepairSlot             `json:"slots,omitempty"`
	AllowedAssignments    []map[string]string                           `json:"allowedAssignments,omitempty"`
}

type dslAttemptCallLineage struct {
	ID              string                     `json:"id"`
	CallIndex       int                        `json:"callIndex"`
	CallKind        models.LLMProviderCallKind `json:"callKind"`
	ProviderAttempt int                        `json:"providerAttempt"`
	Phase           string                     `json:"phase"`
	RequestHash     string                     `json:"requestHash"`
	Dispatch        *models.LLMDispatchLineage `json:"dispatch,omitempty"`
	ResponseHash    string                     `json:"responseHash"`
}

type dslAttemptFallbackArtifact struct {
	SchemaVersion          string                    `json:"schemaVersion"`
	Calls                  any                       `json:"calls"`
	ProviderIRSourceCallID string                    `json:"providerIRSourceCallId,omitempty"`
	ProviderIR             map[string]any            `json:"providerIR"`
	SelectorRepair         *DSLSelectorRepairLineage `json:"selectorRepair,omitempty"`
	ResolvedRule           map[string]any            `json:"resolvedRule"`
	TrustedBaseline        map[string]any            `json:"trustedBaseline"`
	SelectorCatalog        map[string]any            `json:"selectorCatalog"`
	Validation             []DSLAttemptValidation    `json:"validation"`
}

type dslAttemptRecorder struct {
	store                 *store.Store
	job                   *models.DSLJob
	recordingHash         string
	requirementHash       string
	baselineHash          string
	selectorCatalogHash   string
	policyFingerprint     string
	baseline              *models.Rule
	selectorCatalogPrompt string
	selectorRepair        *DSLSelectorRepairLineage

	mu    sync.Mutex
	calls []*models.LLMProviderCall
}

func newDSLAttemptRecorder(
	persistence *store.Store,
	job *models.DSLJob,
	recordingHash, requirementHash, baselineHash, selectorCatalogHash string,
	policyFingerprints ...string,
) *dslAttemptRecorder {
	policyFingerprint := ""
	if len(policyFingerprints) > 0 {
		policyFingerprint = policyFingerprints[0]
	}
	return &dslAttemptRecorder{
		store: persistence, job: job, recordingHash: recordingHash,
		requirementHash: requirementHash, baselineHash: baselineHash,
		selectorCatalogHash: selectorCatalogHash, policyFingerprint: policyFingerprint,
		calls: []*models.LLMProviderCall{},
	}
}

func dslContentHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", digest)
}

func dslJSONHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return dslContentHash(string(encoded))
}

// dslSemanticJSONHash normalizes typed structs and decoded maps through the
// same generic JSON form before hashing. Attempt lineage uses it when the same
// provider IR crosses those two representations.
func dslSemanticJSONHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return ""
	}
	return dslJSONHash(normalized)
}

func dslCompletionArtifactLimit(phase string) int {
	if phase == llm.CompletionPhaseAnalysis {
		return dslAnalysisArtifactLimit
	}
	return dslFinalArtifactLimit
}

func truncateDSLUTF8Middle(value string, keptRunes int) string {
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

func boundedDSLCompletionArtifact(
	content string,
	limit int,
	metadata dslProviderCompletionArtifact,
) (dslProviderCompletionArtifact, int, bool, error) {
	artifact := metadata
	artifact.SchemaVersion = "aegiscrawler.llm-provider-completion.v1"
	artifact.Content = content
	artifact.ContentHash = dslContentHash(content)
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return dslProviderCompletionArtifact{}, 0, false, err
	}
	if len(encoded) <= limit {
		return artifact, len(content), false, nil
	}
	runes := []rune(content)
	low, high := 0, len(runes)
	var best dslProviderCompletionArtifact
	for low <= high {
		middle := low + (high-low)/2
		candidate := artifact
		candidate.Content = truncateDSLUTF8Middle(content, middle)
		candidate.Truncated = true
		encoded, err = json.Marshal(candidate)
		if err != nil {
			return dslProviderCompletionArtifact{}, 0, false, err
		}
		if len(encoded) <= limit {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best.SchemaVersion == "" {
		return dslProviderCompletionArtifact{}, 0, false, fmt.Errorf("provider completion metadata exceeds %d-byte bound", limit)
	}
	return best, len(best.Content), true, nil
}

// CaptureCompletion synchronously persists the sanitized physical boundary
// before the provider output is returned to DSL parsing.
func (r *dslAttemptRecorder) CaptureCompletion(ctx context.Context, event llm.CompletionTraceEvent) error {
	if r == nil || r.store == nil || r.job == nil || event.Result == nil || event.Result.CompletionResponse == nil {
		return store.ErrLLMArtifactInvalid
	}
	content := redact.String(event.Result.Content)
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
	artifact, capturedBytes, truncated, err := boundedDSLCompletionArtifact(
		content,
		dslCompletionArtifactLimit(event.Metadata.Phase),
		dslProviderCompletionArtifact{
			BoundaryKind: event.BoundaryKind,
			HTTPStatus:   event.HTTPStatus, ErrorCode: errorCode, ErrorMessage: errorMessage,
		},
	)
	if err != nil {
		return err
	}
	truncated = truncated || event.ContentTruncated
	artifact.Truncated = truncated
	redacted := event.ContentRedacted || event.ErrorRedacted ||
		content != event.Result.Content || metadataRedacted

	r.mu.Lock()
	defer r.mu.Unlock()
	call := &models.LLMProviderCall{
		JobType: models.LLMJobTypeDSL, JobID: r.job.ID,
		AttemptNumber: r.job.AttemptCount, CallIndex: len(r.calls) + 1,
		CallKind:        models.LLMProviderCallKind(event.BoundaryKind),
		ProviderAttempt: event.ProviderAttempt, Phase: event.Metadata.Phase,
		ChunkIndex: event.Metadata.ChunkIndex, ChunkCount: event.Metadata.ChunkCount,
		Provider: provider, Model: model, PromptVersion: r.job.PromptVersion,
		RequestHash: event.RequestHash, ResponseHash: artifact.ContentHash,
		ResponseID: responseID, FinishReason: finishReason,
		HTTPStatus: event.HTTPStatus, ErrorCode: errorCode,
		InputTokens: event.Result.InputTokens, OutputTokens: event.Result.OutputTokens,
		CacheHit: event.Result.CacheHit, OriginalBytes: event.OriginalBytes,
		OriginalBytesExact: event.OriginalBytesExact, CapturedBytes: capturedBytes,
		Redacted: redacted, Truncated: truncated,
		Replayable: event.OriginalBytesExact && !redacted && !truncated,
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

func boundedDSLDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	value := redact.String(err.Error())
	if len(value) <= dslDiagnosticLimit {
		return value
	}
	value = value[:dslDiagnosticLimit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func sanitizeDSLAttemptValue(value any) (any, bool, string, error) {
	original, err := json.Marshal(value)
	if err != nil {
		return nil, true, "", err
	}
	var normalized any
	if err := json.Unmarshal(original, &normalized); err != nil {
		return nil, true, "", err
	}
	// redact.Recording not required: input is LLM-attempt payload (LLM output + validation report), not page content.
	sanitized := redact.Any(normalized)
	if sanitized == nil && normalized != nil {
		return nil, true, dslContentHash(string(original)), errors.New("redact parsed provider result")
	}
	return sanitized, !reflect.DeepEqual(normalized, sanitized), dslContentHash(string(original)), nil
}

// sanitizeDSLCatalogPrompt parses the prepared opaque selector-candidate
// catalog JSON and redacts it through the same defensive pass as other parsed
// provider artifacts. The catalog is server-prepared trusted input rather than
// provider output, but it is retained beside provider IR and is therefore
// sanitized identically. Its catalogHash identity is verified separately by the
// caller against the recorder's base64url selectorCatalogHash.
func sanitizeDSLCatalogPrompt(prompt string) (any, bool, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, false, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(prompt), &parsed); err != nil {
		return nil, false, fmt.Errorf("parse selector catalog prompt: %w", err)
	}
	sanitized, redacted, _, err := sanitizeDSLAttemptValue(parsed)
	return sanitized, redacted, err
}

func dslCallsReplayable(calls []*models.LLMProviderCall, runErr error) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if !call.Replayable {
			return false
		}
	}
	// Parsed/schema/security/selector failures remain replayable. A missing
	// physical completion or capture failure does not.
	return !errors.Is(runErr, ErrProviderUnavailable) &&
		!errors.Is(runErr, llm.ErrCompletionCapture)
}

func compactDSLCallLineage(calls []*models.LLMProviderCall) []dslAttemptCallLineage {
	lineage := make([]dslAttemptCallLineage, 0, len(calls))
	for _, call := range calls {
		lineage = append(lineage, dslAttemptCallLineage{
			ID: call.ID, CallIndex: call.CallIndex, CallKind: call.CallKind,
			ProviderAttempt: call.ProviderAttempt, Phase: call.Phase,
			RequestHash: call.RequestHash, Dispatch: call.Dispatch, ResponseHash: call.ResponseHash,
		})
	}
	return lineage
}

func boundedDSLCalls(calls []*models.LLMProviderCall) any {
	lineage := compactDSLCallLineage(calls)
	encoded, err := json.Marshal(lineage)
	if err == nil && len(encoded) <= store.MaxLLMAttemptReportArtifactBytes/2 {
		return lineage
	}
	summary := map[string]any{"count": len(calls)}
	if len(calls) > 0 {
		summary["first"] = compactDSLCallLineage(calls[:1])[0]
		summary["last"] = compactDSLCallLineage(calls[len(calls)-1:])[0]
	}
	return summary
}

func providerIRSourceCallID(calls []*models.LLMProviderCall, providerIR any) (string, error) {
	if providerIR == nil {
		return "", nil
	}
	if len(calls) == 0 {
		return "", errors.New("provider IR has no terminal provider call")
	}
	call := calls[len(calls)-1]
	validSourcePhase := call != nil &&
		(call.Phase == llm.CompletionPhaseFinal ||
			call.Phase == llm.CompletionPhaseSynthesis ||
			call.Phase == llm.CompletionPhaseSelectorRepair)
	if call == nil || call.CallIndex != len(calls) ||
		!validSourcePhase ||
		(call.CallKind != models.LLMProviderCallKindResponse &&
			call.CallKind != models.LLMProviderCallKindCacheHit) {
		return "", errors.New("provider IR source is not the last terminal rule/patch response")
	}
	return call.ID, nil
}

func (r *dslAttemptRecorder) buildReport(
	providerIR any,
	resolvedRule *models.Rule,
	validations []DSLAttemptValidation,
	runErr error,
) (*models.LLMAttemptReport, any, error) {
	if r == nil {
		return nil, nil, runErr
	}
	r.mu.Lock()
	calls := append([]*models.LLMProviderCall(nil), r.calls...)
	r.mu.Unlock()

	sourceCallID, sourceErr := providerIRSourceCallID(calls, providerIR)
	safeIR, irRedacted, irHash, irErr := sanitizeDSLAttemptValue(providerIR)
	safeResolved, resolvedRedacted, resolvedHash, resolvedErr := sanitizeDSLAttemptValue(resolvedRule)
	safeBaseline, baselineRedacted, baselineOriginalHash, baselineErr := sanitizeDSLAttemptValue(r.baseline)
	safeCatalog, catalogRedacted, catalogErr := sanitizeDSLCatalogPrompt(r.selectorCatalogPrompt)
	if catalogErr == nil && safeCatalog != nil {
		if catalogMap, ok := safeCatalog.(map[string]any); ok {
			if hash, ok := catalogMap["catalogHash"].(string); ok && r.selectorCatalogHash != "" &&
				hash != r.selectorCatalogHash {
				catalogErr = fmt.Errorf("%w: selector catalog payload does not bind its catalog hash", llm.ErrCompletionCapture)
			}
		}
	}
	if baselineErr == nil && safeBaseline != nil && r.baselineHash != "" &&
		baselineOriginalHash != r.baselineHash {
		baselineErr = fmt.Errorf("%w: trusted baseline payload does not bind its baseline hash", llm.ErrCompletionCapture)
	}
	var safeSelectorRepair *DSLSelectorRepairLineage
	selectorRepairRedacted := false
	if r.selectorRepair != nil {
		encoded, copyErr := json.Marshal(r.selectorRepair)
		if copyErr == nil {
			copyErr = json.Unmarshal(encoded, &safeSelectorRepair)
		}
		if copyErr != nil {
			runErr = fmt.Errorf("%w: normalize selector repair lineage", llm.ErrCompletionCapture)
		} else if safeSelectorRepair.SourceProviderIR != nil {
			safeSource, redacted, _, sourceErr := sanitizeDSLAttemptValue(
				safeSelectorRepair.SourceProviderIR,
			)
			selectorRepairRedacted = redacted
			if sourceErr != nil {
				runErr = fmt.Errorf("%w: sanitize selector repair source provider IR", llm.ErrCompletionCapture)
			} else {
				encodedSource, _ := json.Marshal(safeSource)
				if err := json.Unmarshal(encodedSource, &safeSelectorRepair.SourceProviderIR); err != nil {
					runErr = fmt.Errorf("%w: normalize selector repair source provider IR", llm.ErrCompletionCapture)
				}
			}
		}
		if safeSelectorRepair != nil && safeSelectorRepair.DerivedProviderIR != nil {
			safeDerived, redacted, _, derivedErr := sanitizeDSLAttemptValue(
				safeSelectorRepair.DerivedProviderIR,
			)
			selectorRepairRedacted = selectorRepairRedacted || redacted
			if derivedErr != nil {
				runErr = fmt.Errorf("%w: sanitize derived provider IR", llm.ErrCompletionCapture)
			} else {
				encodedDerived, _ := json.Marshal(safeDerived)
				if err := json.Unmarshal(encodedDerived, &safeSelectorRepair.DerivedProviderIR); err != nil {
					runErr = fmt.Errorf("%w: normalize derived provider IR", llm.ErrCompletionCapture)
				}
			}
		}
	}
	if sourceErr != nil || irErr != nil || resolvedErr != nil || baselineErr != nil || catalogErr != nil {
		runErr = fmt.Errorf("%w: sanitize parsed dsl artifacts", llm.ErrCompletionCapture)
	}
	var safeResolvedRule *models.Rule
	if safeResolved != nil {
		encoded, _ := json.Marshal(safeResolved)
		if err := json.Unmarshal(encoded, &safeResolvedRule); err != nil {
			runErr = fmt.Errorf("%w: normalize resolved dsl artifact", llm.ErrCompletionCapture)
		}
	}
	var safeBaselineRule *models.Rule
	if safeBaseline != nil {
		encoded, _ := json.Marshal(safeBaseline)
		if err := json.Unmarshal(encoded, &safeBaselineRule); err != nil {
			runErr = fmt.Errorf("%w: normalize trusted baseline artifact", llm.ErrCompletionCapture)
		}
	}
	outcome := models.LLMAttemptSucceeded
	if runErr != nil {
		outcome = models.LLMAttemptFailed
	}
	replayable := dslCallsReplayable(calls, runErr) && !irRedacted &&
		!resolvedRedacted && !selectorRepairRedacted && !baselineRedacted && !catalogRedacted
	safetyFlags := []string{"artifact-capture:passed"}
	if replayable {
		safetyFlags = append(safetyFlags, "artifact-replayable:true")
	} else {
		safetyFlags = append(safetyFlags, "artifact-replayable:false")
	}
	if irRedacted || resolvedRedacted || baselineRedacted || catalogRedacted {
		safetyFlags = append(safetyFlags, "artifact-result-redacted:true")
	}
	if sourceCallID != "" {
		safetyFlags = append(safetyFlags, "provider-ir-source:bound")
	}
	phase := "complete"
	if len(validations) > 0 {
		phase = validations[len(validations)-1].Phase
	}
	report := &models.LLMAttemptReport{
		JobType: models.LLMJobTypeDSL, JobID: r.job.ID,
		AttemptNumber: r.job.AttemptCount, Outcome: outcome,
		PromptVersion: r.job.PromptVersion, PolicyFingerprint: r.policyFingerprint,
		RecordingHash:   r.recordingHash,
		RequirementHash: r.requirementHash, BaselineHash: r.baselineHash,
		SelectorCatalogHash: r.selectorCatalogHash, ValidationPhase: phase,
		SafetyFlags: safetyFlags, Replayable: replayable,
	}
	for _, call := range calls {
		report.Provider, report.Model = call.Provider, call.Model
		report.InputTokens += call.InputTokens
		report.OutputTokens += call.OutputTokens
	}
	if runErr != nil {
		_, report.ErrorCode, report.ErrorMessage = classifyDSLJobError(runErr)
	}
	artifact := DSLAttemptArtifact{
		SchemaVersion: "aegiscrawler.dsl-attempt.v2", Calls: calls,
		ProviderIRSourceCallID: sourceCallID,
		ProviderIR:             safeIR,
		SelectorRepair:         safeSelectorRepair,
		ResolvedRule:           safeResolvedRule,
		TrustedBaseline:        safeBaselineRule,
		SelectorCatalog:        safeCatalog,
		Validation:             validations,
	}
	encoded, marshalErr := json.Marshal(artifact)
	if marshalErr == nil && len(encoded) <= store.MaxLLMAttemptReportArtifactBytes {
		return report, artifact, runErr
	}
	if marshalErr != nil {
		runErr = fmt.Errorf("%w: marshal dsl attempt report: %v", llm.ErrCompletionCapture, marshalErr)
	} else {
		runErr = fmt.Errorf("%w: dsl attempt report exceeded %d-byte bound", llm.ErrCompletionCapture, store.MaxLLMAttemptReportArtifactBytes)
	}
	report.Outcome = models.LLMAttemptFailed
	report.ValidationPhase = "artifact-capture"
	_, report.ErrorCode, report.ErrorMessage = classifyDSLJobError(runErr)
	report.Replayable = false
	report.SafetyFlags = []string{"artifact-capture:bounded-fallback", "artifact-result-omitted:true", "artifact-replayable:false"}
	fallback := dslAttemptFallbackArtifact{
		SchemaVersion:          "aegiscrawler.dsl-attempt-fallback.v1",
		Calls:                  boundedDSLCalls(calls),
		ProviderIRSourceCallID: sourceCallID,
		ProviderIR:             map[string]any{"hash": irHash, "omitted": true},
		SelectorRepair:         safeSelectorRepair,
		ResolvedRule:           map[string]any{"hash": resolvedHash, "omitted": true},
		TrustedBaseline:        map[string]any{"hash": r.baselineHash, "omitted": true},
		SelectorCatalog:        map[string]any{"hash": r.selectorCatalogHash, "omitted": true},
		Validation: append(validations, DSLAttemptValidation{
			Phase: "artifact-capture", Status: "failed", Code: "ARTIFACT_CAPTURE_FAILED",
			Message: "dsl attempt artifact exceeded its durable bound", Detail: boundedDSLDiagnostic(runErr),
		}),
	}
	return report, fallback, runErr
}

type DSLAttemptDetail struct {
	Report   *models.LLMAttemptReport
	Calls    []*models.LLMProviderCall
	Artifact any
}
