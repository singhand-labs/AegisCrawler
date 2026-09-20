package dsl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/prompt"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrule "github.com/singhand-labs/AegisCrawler/internal/rule"
)

const (
	maxSelectorRepairProviderIRBytes    = 256 << 10
	maxSelectorRepairCatalogBytes       = 32 << 10
	maxSelectorRepairDiagnosticBytes    = 8 << 10
	maxSelectorRepairOutputBytes        = 64 << 10
	maxSelectorRepairPatchBytes         = 64 << 10
	maxSelectorRepairFailureCodeBytes   = 128
	maxSelectorRepairFailureSlotBytes   = 2 << 10
	maxSelectorRepairFailureDetailBytes = 4 << 10
)

const selectorRepairSystemPrompt = `You repair only opaque selector-candidate choices in an AegisCrawler provider rule. Return exactly one JSON object with exactly:
{"selectorCatalogHash":"...","replacements":[{"slotId":"...","candidateId":"..."}]}

Use only slotId values present in repairSlots and candidate ID string values copied byte-for-byte from the supplied exact selector catalog. Never invent an ID or ordinal alias such as row_1, field_1, or target_1. The slotized provider IR shows each mutable candidate position as {"slotId","currentCandidateId"}; everything else is immutable. After applying replacements over current values, every slot must exactly equal one complete object in allowedAssignments. Choose the smallest complete set of replacements that addresses the exact diagnostic and confirmed output contract. Do not return a rule, CSS, JSON paths, recording data, explanations, markdown, unknown keys, or duplicate slot IDs.`

// SelectorRepairJobRequest is sealed at rest and never returned by the API.
// It binds the derived job to the exact replayable source attempt and to every
// immutable input used to prepare the bounded prompt.
type SelectorRepairJobRequest struct {
	SourceJobID                string                                        `json:"sourceJobId"`
	SourceAttemptNumber        int                                           `json:"sourceAttemptNumber"`
	SourceAttemptReportID      string                                        `json:"sourceAttemptReportId"`
	SourceProviderCallID       string                                        `json:"sourceProviderCallId"`
	SourceProviderResponseHash string                                        `json:"sourceProviderResponseHash"`
	SourceProviderIRHash       string                                        `json:"sourceProviderIrHash"`
	DerivedProviderIRHash      string                                        `json:"derivedProviderIrHash,omitempty"`
	RecordingHash              string                                        `json:"recordingHash"`
	RequirementHash            string                                        `json:"requirementHash"`
	BaselineHash               string                                        `json:"baselineHash"`
	SelectorCatalogHash        string                                        `json:"selectorCatalogHash"`
	SelectorCatalog            string                                        `json:"selectorCatalog"`
	ProviderIR                 *models.Rule                                  `json:"providerIr"`
	TrustedBaseline            *models.Rule                                  `json:"trustedBaseline"`
	Diagnostic                 DSLAttemptValidation                          `json:"diagnostic"`
	OutputContract             json.RawMessage                               `json:"outputContract"`
	Failure                    *platformrule.SelectorCandidateSelectionError `json:"failure"`
	Slots                      []platformrule.SelectorRepairSlot             `json:"slots"`
	AllowedAssignments         []map[string]string                           `json:"allowedAssignments"`
	RepairPlanHash             string                                        `json:"repairPlanHash"`
}

type selectorRepairReplacement struct {
	SlotID      string `json:"slotId"`
	CandidateID string `json:"candidateId"`
}

type selectorRepairResponse struct {
	SelectorCatalogHash string                      `json:"selectorCatalogHash"`
	Replacements        []selectorRepairReplacement `json:"replacements"`
}

type selectorRepairResult struct {
	Rule                  *models.Rule                `json:"rule"`
	SourceAttemptReportID string                      `json:"sourceAttemptReportId"`
	SourceProviderIRHash  string                      `json:"sourceProviderIrHash"`
	DerivedProviderIRHash string                      `json:"derivedProviderIrHash"`
	RepairPlanHash        string                      `json:"repairPlanHash"`
	Replacements          []selectorRepairReplacement `json:"replacements"`
	FinalResponse         WorkflowCompletionAudit     `json:"finalResponse"`
}

func isEligibleSelectorRepairError(err error) bool {
	var selection *platformrule.SelectorCandidateSelectionError
	return errors.As(err, &selection)
}

type oneShotWorkflowCompleter interface {
	CompleteOnce(context.Context, llm.CompletionRequest) (*llm.CompletionResult, error)
}

type oneShotCompleterAdapter struct {
	completer oneShotWorkflowCompleter
}

func (a oneShotCompleterAdapter) Complete(ctx context.Context, request llm.CompletionRequest) (*llm.CompletionResult, error) {
	return a.completer.CompleteOnce(ctx, request)
}

type preparedSelectorRepairCompletion struct {
	completer oneShotWorkflowCompleter
	user      string
}

func (w *Workflow) prepareSelectorRepairCompletion(
	request SelectorRepairJobRequest,
) (*preparedSelectorRepairCompletion, error) {
	if w == nil || !w.cfg.LLMEnabled {
		return nil, ErrProviderUnavailable
	}
	oneShot, ok := w.completer.(oneShotWorkflowCompleter)
	if !ok {
		return nil, errors.New("selector repair requires a one-shot completion provider")
	}
	user, err := selectorRepairUserPrompt(request)
	if err != nil {
		return nil, err
	}
	if workflowEstimatedContextTokens(selectorRepairSystemPrompt, user, w.outputReserveTokens()) > w.inputLimit() {
		return nil, fmt.Errorf(
			"%w: selector repair request estimate exceeds %d tokens",
			ErrWorkflowSourceUnavailable, w.inputLimit(),
		)
	}
	return &preparedSelectorRepairCompletion{completer: oneShot, user: user}, nil
}

func (w *Workflow) repairSelectorsOnce(
	ctx context.Context,
	request SelectorRepairJobRequest,
) (*selectorRepairResponse, WorkflowRunMetadata, WorkflowCompletionAudit, error) {
	prepared, err := w.prepareSelectorRepairCompletion(request)
	if err != nil {
		return nil, WorkflowRunMetadata{}, WorkflowCompletionAudit{}, err
	}
	return w.repairSelectorsPrepared(ctx, prepared)
}

func (w *Workflow) repairSelectorsPrepared(
	ctx context.Context,
	prepared *preparedSelectorRepairCompletion,
) (*selectorRepairResponse, WorkflowRunMetadata, WorkflowCompletionAudit, error) {
	result, err := llm.CompleteWithTrace(
		ctx,
		oneShotCompleterAdapter{completer: prepared.completer},
		llm.CompletionRequest{
			Model: w.cfg.LLMModel, System: selectorRepairSystemPrompt, User: prepared.user,
			Temperature: 0, JSONMode: true, MaxOutputTokens: w.outputTokenCap(),
			ExecutionPolicy: llm.CompletionExecutionAtMostOnce,
		},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseSelectorRepair, ChunkCount: 1},
	)
	if err != nil {
		return nil, WorkflowRunMetadata{}, WorkflowCompletionAudit{}, workflowCompletionError("repair selector candidates", err)
	}
	if len(result.Content) > maxSelectorRepairPatchBytes {
		return nil, workflowMetadata(result, 1), workflowAudit(result), fmt.Errorf(
			"%w: selector repair response exceeds %d bytes", ErrInvalidLLMRule, maxSelectorRepairPatchBytes,
		)
	}
	parsed, err := parseSelectorRepairResponse(result.Content)
	return parsed, workflowMetadata(result, 1), workflowAudit(result), err
}

func selectorRepairUserPrompt(request SelectorRepairJobRequest) (string, error) {
	providerIR, err := json.Marshal(request.ProviderIR)
	if err != nil || len(providerIR) == 0 || len(providerIR) > maxSelectorRepairProviderIRBytes {
		return "", fmt.Errorf("%w: provider IR exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	if len(request.SelectorCatalog) == 0 || len(request.SelectorCatalog) > maxSelectorRepairCatalogBytes {
		return "", fmt.Errorf("%w: selector catalog exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	diagnostic, err := json.Marshal(request.Diagnostic)
	if err != nil || len(diagnostic) > maxSelectorRepairDiagnosticBytes {
		return "", fmt.Errorf("%w: selector diagnostic exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	selectionFailure, err := json.Marshal(request.Failure)
	if err != nil || len(selectionFailure) == 0 || len(selectionFailure) > maxSelectorRepairDiagnosticBytes {
		return "", fmt.Errorf("%w: selector selection failure exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	if len(request.OutputContract) == 0 || len(request.OutputContract) > maxSelectorRepairOutputBytes {
		return "", fmt.Errorf("%w: output contract exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	slotized, err := slotizedSelectorRepairIR(request.ProviderIR, request.Slots)
	if err != nil {
		return "", err
	}
	slots := make([]map[string]any, len(request.Slots))
	for index, slot := range request.Slots {
		slots[index] = map[string]any{
			"slotId": slot.ID, "kind": slot.Kind,
			"currentCandidateId":  slot.CurrentCandidateID,
			"allowedCandidateIds": append([]string(nil), slot.AllowedCandidateIDs...),
		}
	}
	envelope := map[string]any{
		"sourceAttemptReportId":   request.SourceAttemptReportID,
		"sourceProviderIrHash":    request.SourceProviderIRHash,
		"repairPlanHash":          request.RepairPlanHash,
		"selectorCatalogHash":     request.SelectorCatalogHash,
		"selectorCatalog":         json.RawMessage(request.SelectorCatalog),
		"slotizedProviderIr":      slotized,
		"repairSlots":             slots,
		"allowedAssignments":      request.AllowedAssignments,
		"diagnostic":              request.Diagnostic,
		"selectionFailure":        request.Failure,
		"confirmedOutputContract": json.RawMessage(request.OutputContract),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("%w: encode selector repair prompt", ErrWorkflowSourceUnavailable)
	}
	return string(encoded), nil
}

func normalizeSelectorRepairFailure(
	failure *platformrule.SelectorCandidateSelectionError,
) (*platformrule.SelectorCandidateSelectionError, error) {
	if failure == nil {
		return nil, fmt.Errorf("%w: selector selection failure is missing", ErrWorkflowSourceUnavailable)
	}
	normalized := &platformrule.SelectorCandidateSelectionError{
		Code:   strings.TrimSpace(failure.Code),
		Slot:   strings.TrimSpace(failure.Slot),
		Detail: strings.TrimSpace(failure.Detail),
	}
	if normalized.Code == "" || normalized.Slot == "" || normalized.Detail == "" ||
		len(normalized.Code) > maxSelectorRepairFailureCodeBytes ||
		len(normalized.Slot) > maxSelectorRepairFailureSlotBytes ||
		len(normalized.Detail) > maxSelectorRepairFailureDetailBytes {
		return nil, fmt.Errorf("%w: selector selection failure exceeds its field bounds", ErrWorkflowSourceUnavailable)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil || len(encoded) > maxSelectorRepairDiagnosticBytes {
		return nil, fmt.Errorf("%w: selector selection failure exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	return normalized, nil
}

func parseSelectorRepairResponse(content string) (*selectorRepairResponse, error) {
	if err := rejectDuplicateJSONObjectKeys([]byte(content)); err != nil {
		return nil, fmt.Errorf("%w: selector repair response is not strict JSON: %v", ErrInvalidLLMRule, err)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var response selectorRepairResponse
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("%w: invalid selector repair response: %v", ErrInvalidLLMRule, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: invalid selector repair response: %v", ErrInvalidLLMRule, err)
	}
	if strings.TrimSpace(response.SelectorCatalogHash) == "" || len(response.Replacements) == 0 {
		return nil, fmt.Errorf("%w: selector repair response is incomplete", ErrInvalidLLMRule)
	}
	seen := map[string]bool{}
	for _, replacement := range response.Replacements {
		if strings.TrimSpace(replacement.SlotID) == "" ||
			strings.TrimSpace(replacement.CandidateID) == "" ||
			seen[replacement.SlotID] {
			return nil, fmt.Errorf("%w: selector repair replacements are invalid", ErrInvalidLLMRule)
		}
		seen[replacement.SlotID] = true
	}
	return &response, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONObjectKeys(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				rawKey, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := rawKey.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid object key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func slotizedSelectorRepairIR(rule *models.Rule, slots []platformrule.SelectorRepairSlot) (any, error) {
	encoded, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	for _, slot := range slots {
		if err := setSelectorRepairPath(value, slot.Path, map[string]string{
			"slotId": slot.ID, "currentCandidateId": slot.CurrentCandidateID,
		}); err != nil {
			return nil, err
		}
	}
	return value, nil
}

func applySelectorRepairPatch(
	rule *models.Rule,
	slots []platformrule.SelectorRepairSlot,
	allowedAssignments []map[string]string,
	response *selectorRepairResponse,
) (*models.Rule, error) {
	if rule == nil || response == nil {
		return nil, fmt.Errorf("%w: selector repair patch is empty", ErrInvalidLLMRule)
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	byID := map[string]platformrule.SelectorRepairSlot{}
	for _, slot := range slots {
		byID[slot.ID] = slot
	}
	changed := false
	for _, replacement := range response.Replacements {
		slot, ok := byID[replacement.SlotID]
		if !ok {
			return nil, fmt.Errorf("%w: selector repair references unknown slot", ErrInvalidLLMRule)
		}
		allowed := false
		for _, candidateID := range slot.AllowedCandidateIDs {
			if replacement.CandidateID == candidateID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("%w: selector repair candidate is not allowed for its slot", ErrInvalidLLMRule)
		}
		if err := setSelectorRepairPath(value, slot.Path, replacement.CandidateID); err != nil {
			return nil, fmt.Errorf("%w: selector repair slot is invalid", ErrInvalidLLMRule)
		}
		if replacement.CandidateID != slot.CurrentCandidateID {
			changed = true
		}
	}
	if !changed {
		return nil, fmt.Errorf("%w: selector repair patch makes no change", ErrInvalidLLMRule)
	}
	effective := map[string]string{}
	for _, slot := range slots {
		effective[slot.ID] = slot.CurrentCandidateID
	}
	for _, replacement := range response.Replacements {
		effective[replacement.SlotID] = replacement.CandidateID
	}
	compatible := false
	for _, assignment := range allowedAssignments {
		if reflect.DeepEqual(effective, assignment) {
			compatible = true
			break
		}
	}
	if !compatible {
		return nil, fmt.Errorf("%w: selector repair patch is not a complete compatible assignment", ErrInvalidLLMRule)
	}
	derived, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var repaired models.Rule
	if err := json.Unmarshal(derived, &repaired); err != nil {
		return nil, fmt.Errorf("%w: repaired provider IR is invalid", ErrInvalidLLMRule)
	}
	return &repaired, nil
}

func setSelectorRepairPath(root any, path []string, replacement any) error {
	if len(path) == 0 {
		return errors.New("empty selector repair path")
	}
	current := root
	for index, part := range path {
		last := index == len(path)-1
		if strings.HasPrefix(part, "#") {
			array, ok := current.([]any)
			position, parseErr := strconv.Atoi(strings.TrimPrefix(part, "#"))
			if !ok || parseErr != nil || position < 0 || position >= len(array) {
				return errors.New("invalid selector repair array path")
			}
			if last {
				array[position] = replacement
				return nil
			}
			current = array[position]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return errors.New("invalid selector repair object path")
		}
		if last {
			if _, exists := object[part]; !exists {
				return errors.New("selector repair path does not exist")
			}
			object[part] = replacement
			return nil
		}
		next, exists := object[part]
		if !exists {
			return errors.New("selector repair path does not exist")
		}
		current = next
	}
	return errors.New("invalid selector repair path")
}

func selectorRepairPlanHash(request SelectorRepairJobRequest) string {
	value := struct {
		SourceAttemptReportID string                                        `json:"sourceAttemptReportId"`
		SourceProviderIRHash  string                                        `json:"sourceProviderIrHash"`
		RecordingHash         string                                        `json:"recordingHash"`
		RequirementHash       string                                        `json:"requirementHash"`
		BaselineHash          string                                        `json:"baselineHash"`
		SelectorCatalogHash   string                                        `json:"selectorCatalogHash"`
		SelectorCatalog       string                                        `json:"selectorCatalog"`
		ProviderIR            *models.Rule                                  `json:"providerIr"`
		Diagnostic            DSLAttemptValidation                          `json:"diagnostic"`
		OutputContract        json.RawMessage                               `json:"outputContract"`
		Failure               *platformrule.SelectorCandidateSelectionError `json:"failure"`
		Slots                 []platformrule.SelectorRepairSlot             `json:"slots"`
		AllowedAssignments    []map[string]string                           `json:"allowedAssignments"`
	}{
		request.SourceAttemptReportID, request.SourceProviderIRHash,
		request.RecordingHash, request.RequirementHash, request.BaselineHash,
		request.SelectorCatalogHash, request.SelectorCatalog, request.ProviderIR,
		request.Diagnostic, request.OutputContract, request.Failure, request.Slots,
		request.AllowedAssignments,
	}
	return dslJSONHash(value)
}

func selectorRepairPromptVersion() string {
	return prompt.DSLSelectorRepairVersion
}
