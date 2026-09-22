package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	platformrule "github.com/singhand-labs/AegisCrawler/internal/rule"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

const (
	maxReplayDiagnosticsBytes          = 64 * 1024
	maxReplayDiagnosticsInputBytes     = 2 * 1024 * 1024
	maxReplayDiagnosticLogs            = 20
	maxReplayOutputBytes               = 5 * 1024 * 1024
	maxReplayArtifactsBytes            = 2 * 1024 * 1024
	maxRepairArtifactContextBytes      = 64 * 1024
	maxRepairArtifactContextItems      = 4
	maxDSLValidationFeedbackBytes      = 2000
	MaxAdminReviewedAttemptExportBytes = store.MaxLLMAttemptReportArtifactBytes + (256 << 10)
)

var (
	ErrInvalidWorkflowInput  = errors.New("invalid dsl workflow input")
	ErrUnsafeGeneratedRule   = errors.New("generated rule failed the security scanner")
	ErrReplayPayloadTooLarge = errors.New("replay payload exceeds configured safety limit")
	// ErrReplayOutputRequired rejects a success completion that carries no
	// collected output rows; the attempt stays completable by a later
	// row-carrying completion (wizard or fallback retry).
	ErrReplayOutputRequired = errors.New("successful replay completion requires the collected output rows")
)

type WorkflowInputError struct {
	Phase string
	Err   error
}

func (e *WorkflowInputError) Error() string { return fmt.Sprintf("%s: %v", e.Phase, e.Err) }
func (e *WorkflowInputError) Unwrap() error { return ErrInvalidWorkflowInput }

func workflowInputError(phase string, err error) error {
	return &WorkflowInputError{Phase: phase, Err: err}
}

func WorkflowInputPhase(err error) string {
	var inputErr *WorkflowInputError
	if errors.As(err, &inputErr) {
		return inputErr.Phase
	}
	return "workflow-input"
}

func workflowBaselineValidationPhase(err error) string {
	switch {
	case strings.Contains(err.Error(), "does not match the DSL schema"),
		strings.Contains(err.Error(), "has no DSL schema definition"):
		return "baseline-action-schema"
	case errors.Is(err, platformrule.ErrUnsafeProvisionalRule):
		return "baseline-navigation-policy"
	case strings.Contains(err.Error(), "required fields are missing"):
		return "baseline-required-fields"
	case strings.Contains(err.Error(), "entry") || strings.Contains(err.Error(), "domain"):
		return "baseline-entry-domain"
	default:
		return "baseline-structure"
	}
}

type GenerationJobRequest struct {
	BaselineRule *models.Rule `json:"baselineRule"`
}

const AdminReviewedDSLAttemptExportVersion = "aegiscrawler.admin-reviewed-dsl-attempt-export.v1"

// AdminReviewedDSLAttemptExport is the bounded, provider-response-free shape
// accepted for manual adoption. Provider and call metadata are retained only
// as untrusted review context and never become provider provenance.
type AdminReviewedDSLAttemptExport struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Attempt       *models.LLMAttemptReport `json:"attempt"`
	Artifact      *DSLAttemptArtifact      `json:"artifact"`
}

type RepairJobRequest struct {
	CurrentRule    *models.Rule            `json:"currentRule"`
	Diagnostics    any                     `json:"diagnostics" swaggertype:"object"`
	Artifacts      []models.ReplayArtifact `json:"artifacts,omitempty"`
	ReplaySequence int                     `json:"replaySequence"`
}

type ReplayCompletionInput struct {
	Succeeded    bool                    `json:"succeeded"`
	Diagnostics  any                     `json:"diagnostics,omitempty" swaggertype:"object"`
	Output       any                     `json:"output,omitempty" swaggertype:"object"`
	Artifacts    []models.ReplayArtifact `json:"artifacts,omitempty"`
	ErrorCode    string                  `json:"errorCode,omitempty"`
	ErrorMessage string                  `json:"errorMessage,omitempty"`
}

type Manager struct {
	store     *store.Store
	cfg       *config.Config
	recording *platformrecording.Service
	workflow  *Workflow
	logger    *zap.Logger
}

func NewDSLManager(persistence *store.Store, cfg *config.Config, workflow *Workflow, logger *zap.Logger) *Manager {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Manager{store: persistence, cfg: cfg, recording: platformrecording.NewService(persistence, cfg), workflow: workflow, logger: logger}
}

func (m *Manager) policyFingerprint() string {
	if m == nil || m.cfg == nil {
		return ""
	}
	if policy := m.cfg.EnforcedLLMPolicy(); policy != nil {
		return policy.Fingerprint
	}
	return ""
}

func (m *Manager) Submit(ctx context.Context, requirementID, browserProfileID string, baseline *models.Rule) (*models.DSLWorkflow, *models.DSLJob, error) {
	if m == nil || m.store == nil || m.workflow == nil {
		return nil, nil, errors.New("dsl workflow service is unavailable")
	}
	if strings.TrimSpace(browserProfileID) == "" || baseline == nil {
		return nil, nil, workflowInputError("required-fields", errors.New("browserProfileId and baselineRule are required"))
	}
	requirement, err := m.store.GetCollectionRequirement(ctx, requirementID)
	if err != nil {
		return nil, nil, err
	}
	if requirement.Status != models.CollectionRequirementConfirmed {
		return nil, nil, store.ErrRequirementState
	}
	if _, err := platformrule.BuildRequirementInputSchema(requirement.Requirement); err != nil {
		return nil, nil, workflowInputError("requirement-schema", err)
	}
	recording, err := m.recording.Get(ctx, requirement.RecordingID)
	if err != nil {
		return nil, nil, err
	}
	if err := validateBaselineDomainsAgainstRecording(baseline, recording); err != nil {
		return nil, nil, workflowInputError("recording-domain-evidence", err)
	}
	baselineCopy, err := copyRule(baseline)
	if err != nil {
		return nil, nil, workflowInputError("baseline-copy", err)
	}
	if _, err := platformrule.ValidateWorkflowBaseline(baselineCopy, baseline, requirement.Requirement); err != nil {
		return nil, nil, workflowInputError(workflowBaselineValidationPhase(err), err)
	}
	baselineMap, err := ruleAsMap(baselineCopy)
	if err != nil {
		return nil, nil, workflowInputError("baseline-map", err)
	}
	if flags := llm.ScanSafety(baselineMap); len(flags) > 0 {
		return nil, nil, workflowInputError("baseline-security-scan", errors.New("baseline rule failed the security scanner"))
	}
	workflow := &models.DSLWorkflow{
		ID: store.NewID(), RequirementID: requirement.ID, RecordingID: requirement.RecordingID,
		BrowserProfileID: strings.TrimSpace(browserProfileID),
		Owner:            authz.Subject(ctx, "system"),
	}
	job, err := m.store.CreateDSLWorkflow(ctx, workflow, GenerationJobRequest{BaselineRule: baselineCopy}, store.DSLWorkflowOptions{
		JobMaxAttempts: m.cfg.DSLGenerationMaxAttempts(),
		MaxRepairs:     m.cfg.DSLMaxRepairs(),
		BudgetUSDNanos: int64(m.cfg.WorkflowBudgetUSD()),
	})
	if err != nil {
		return nil, nil, err
	}
	return workflow, job, nil
}

func validateBaselineDomainsAgainstRecording(baseline *models.Rule, recording *models.Recording) error {
	if baseline == nil || recording == nil || recording.Payload == nil {
		return errors.New("baseline domains require a persisted recording")
	}
	var domains []string
	if err := json.Unmarshal(baseline.Domain, &domains); err != nil {
		var single string
		if err := json.Unmarshal(baseline.Domain, &single); err != nil || strings.TrimSpace(single) == "" {
			return errors.New("baseline domain must be a string or non-empty string array")
		}
		domains = []string{single}
	}
	if len(domains) == 0 {
		return errors.New("baseline domain set is empty")
	}
	evidenced := make(map[string]struct{})
	addURL := func(value any) {
		raw, ok := value.(string)
		if !ok || strings.TrimSpace(raw) == "" {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed == nil {
			return
		}
		host := parsed.Hostname()
		loopbackHTTP := parsed.Scheme == "http" && (strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()))
		if (parsed.Scheme != "https" && !loopbackHTTP) || host == "" || parsed.User != nil {
			return
		}
		evidenced[strings.ToLower(host)] = struct{}{}
	}
	if meta, ok := recording.Payload["meta"].(map[string]any); ok {
		addURL(meta["startUrl"])
	}
	for _, field := range []string{"events", "snapshots"} {
		items, _ := recording.Payload[field].([]any)
		for _, item := range items {
			if object, ok := item.(map[string]any); ok {
				addURL(object["url"])
			}
		}
	}
	for _, claimed := range domains {
		claimed = strings.ToLower(strings.TrimSpace(claimed))
		if _, ok := evidenced[claimed]; !ok {
			return fmt.Errorf("baseline domain %q is not evidenced by an exact persisted HTTPS recording URL", claimed)
		}
	}
	return nil
}

// AdoptAdminReviewedAttempt revalidates an exported attempt solely as a
// manually reviewed rule candidate. It creates no DSL job and never invokes a
// provider, cache, repair, qualification, or promotion path.
func (m *Manager) AdoptAdminReviewedAttempt(ctx context.Context, requirementID, browserProfileID string, exported AdminReviewedDSLAttemptExport) (*models.DSLWorkflow, error) {
	if m == nil || m.store == nil || m.recording == nil {
		return nil, errors.New("dsl workflow service is unavailable")
	}
	browserProfileID = strings.TrimSpace(browserProfileID)
	if browserProfileID == "" || len(browserProfileID) > 200 || exported.SchemaVersion != AdminReviewedDSLAttemptExportVersion ||
		exported.Attempt == nil || exported.Artifact == nil {
		return nil, fmt.Errorf("%w: browserProfileId and a supported attempt export are required", ErrInvalidWorkflowInput)
	}
	encodedExport, err := json.Marshal(exported)
	if err != nil || len(encodedExport) > MaxAdminReviewedAttemptExportBytes {
		return nil, fmt.Errorf("%w: attempt export exceeds the %d-byte bound", ErrInvalidWorkflowInput, MaxAdminReviewedAttemptExportBytes)
	}
	if exported.Attempt.JobType != models.LLMJobTypeDSL ||
		exported.Artifact.SchemaVersion != "aegiscrawler.dsl-attempt.v2" ||
		dslJSONHash(exported.Artifact) != exported.Attempt.ArtifactHash {
		return nil, fmt.Errorf("%w: attempt export artifact binding is invalid", ErrInvalidWorkflowInput)
	}
	if exported.Artifact.TrustedBaseline == nil || exported.Artifact.ResolvedRule == nil || exported.Artifact.SelectorCatalog == nil {
		return nil, fmt.Errorf("%w: attempt export is missing its baseline, selector catalog, or resolved rule", ErrInvalidWorkflowInput)
	}

	requirement, err := m.store.GetCollectionRequirement(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	if requirement.Status != models.CollectionRequirementConfirmed {
		return nil, store.ErrRequirementState
	}
	if _, err := platformrule.BuildRequirementInputSchema(requirement.Requirement); err != nil {
		return nil, fmt.Errorf("%w: confirmed requirement input contract is invalid: %v", ErrInvalidWorkflowInput, err)
	}
	recording, err := m.recording.Get(ctx, requirement.RecordingID)
	if err != nil {
		return nil, err
	}
	if err := validateCompleteAdoptionRecording(recording); err != nil {
		return nil, err
	}
	if recording.ContentHash != exported.Attempt.RecordingHash ||
		requirement.ContentHash != exported.Attempt.RequirementHash ||
		dslJSONHash(exported.Artifact.TrustedBaseline) != exported.Attempt.BaselineHash {
		return nil, fmt.Errorf("%w: recording, requirement, or baseline hash does not match the export", ErrInvalidWorkflowInput)
	}

	rawBaselineMap, err := ruleAsMap(exported.Artifact.TrustedBaseline)
	if err != nil || len(llm.ScanSafety(rawBaselineMap)) > 0 {
		return nil, fmt.Errorf("%w: exported baseline failed the security scanner", ErrInvalidWorkflowInput)
	}
	baseline, historicalInputsNormalized, ok := canonicalAdminReviewedBaseline(
		exported.Artifact.TrustedBaseline, requirement.Requirement,
	)
	if !ok {
		return nil, fmt.Errorf("%w: exported baseline variables are invalid", ErrInvalidWorkflowInput)
	}
	validatedBaseline, err := copyRule(baseline)
	if err != nil {
		return nil, fmt.Errorf("%w: trusted baseline is invalid", ErrInvalidWorkflowInput)
	}
	if _, err := platformrule.ValidateWorkflowBaseline(validatedBaseline, baseline, requirement.Requirement); err != nil {
		return nil, fmt.Errorf("%w: exported baseline failed current validation: %v", ErrInvalidWorkflowInput, err)
	}
	if dslSemanticJSONHash(validatedBaseline) != dslSemanticJSONHash(baseline) {
		return nil, fmt.Errorf("%w: current validation would mutate the exported baseline", ErrInvalidWorkflowInput)
	}
	baseline = validatedBaseline
	baselineMap, err := ruleAsMap(baseline)
	if err != nil || len(llm.ScanSafety(baselineMap)) > 0 {
		return nil, fmt.Errorf("%w: exported baseline failed the security scanner", ErrInvalidWorkflowInput)
	}

	selectorCatalog, err := platformrule.BuildSelectorEvidenceCatalog(recordingPayloadWithRequirementMarks(recording.Payload, requirement.Marks))
	if err != nil {
		return nil, fmt.Errorf("%w: selector evidence could not be derived: %v", ErrInvalidWorkflowInput, err)
	}
	storedCatalog, err := json.Marshal(exported.Artifact.SelectorCatalog)
	if err != nil {
		return nil, fmt.Errorf("%w: stored selector catalog is invalid", ErrInvalidWorkflowInput)
	}
	rebuiltCatalog, _, err := selectorCatalog.ReconstructProviderPrompt(string(storedCatalog), baseline)
	if err != nil || dslSemanticJSONStringHash(rebuiltCatalog) != dslSemanticJSONStringHash(string(storedCatalog)) ||
		selectorCatalog.PreparedProviderPromptCatalogHash() != exported.Attempt.SelectorCatalogHash {
		return nil, fmt.Errorf("%w: selector catalog cannot be reconstructed exactly from the current recording", ErrInvalidWorkflowInput)
	}
	var catalogIdentity struct {
		CatalogHash string `json:"catalogHash"`
	}
	if err := json.Unmarshal(storedCatalog, &catalogIdentity); err != nil ||
		catalogIdentity.CatalogHash == "" || catalogIdentity.CatalogHash != exported.Attempt.SelectorCatalogHash {
		return nil, fmt.Errorf("%w: selector catalog hash does not match the export", ErrInvalidWorkflowInput)
	}

	if exported.Artifact.SelectorRepair != nil && exported.Artifact.ProviderIR == nil {
		return nil, fmt.Errorf("%w: selector-repair export is missing its patch IR", ErrInvalidWorkflowInput)
	}
	if exported.Artifact.ProviderIR != nil {
		effectiveIR := exported.Artifact.ProviderIR
		if lineage := exported.Artifact.SelectorRepair; lineage != nil {
			if lineage.SourceProviderIR == nil || lineage.SourceProviderIRHash == "" ||
				dslSemanticJSONHash(lineage.SourceProviderIR) != lineage.SourceProviderIRHash {
				return nil, fmt.Errorf("%w: selector-repair source IR binding is invalid", ErrInvalidWorkflowInput)
			}
			if lineage.PatchProviderIRHash == "" ||
				dslSemanticJSONHash(exported.Artifact.ProviderIR) != lineage.PatchProviderIRHash {
				return nil, fmt.Errorf("%w: selector-repair patch IR binding is invalid", ErrInvalidWorkflowInput)
			}
			encodedPatch, patchErr := json.Marshal(exported.Artifact.ProviderIR)
			if patchErr != nil {
				return nil, fmt.Errorf("%w: selector-repair patch IR is invalid", ErrInvalidWorkflowInput)
			}
			patch, patchErr := parseSelectorRepairResponse(string(encodedPatch))
			if patchErr != nil {
				return nil, fmt.Errorf("%w: selector-repair patch IR is invalid", ErrInvalidWorkflowInput)
			}
			if patch.SelectorCatalogHash != exported.Attempt.SelectorCatalogHash {
				return nil, fmt.Errorf("%w: selector-repair patch catalog binding is invalid", ErrInvalidWorkflowInput)
			}
			sourceIR, reconstructionErr := copyRule(lineage.SourceProviderIR)
			if reconstructionErr != nil {
				return nil, fmt.Errorf("%w: selector-repair source IR is invalid", ErrInvalidWorkflowInput)
			}
			_, resolutionErr := selectorCatalog.ResolveHistoricalProviderCandidates(
				sourceIR, exported.Attempt.SelectorCatalogHash,
			)
			var reconstructedFailure *platformrule.SelectorCandidateSelectionError
			if !errors.As(resolutionErr, &reconstructedFailure) {
				return nil, fmt.Errorf("%w: selector-repair source failure cannot be reconstructed", ErrInvalidWorkflowInput)
			}
			reconstructedFailure, reconstructionErr = normalizeSelectorRepairFailure(reconstructedFailure)
			if reconstructionErr != nil {
				return nil, fmt.Errorf("%w: selector-repair source failure is invalid", ErrInvalidWorkflowInput)
			}
			exportedFailure, reconstructionErr := normalizeSelectorRepairFailure(lineage.Failure)
			if reconstructionErr != nil || !reflect.DeepEqual(reconstructedFailure, exportedFailure) {
				return nil, fmt.Errorf("%w: selector-repair source failure binding is invalid", ErrInvalidWorkflowInput)
			}
			reconstructedDiagnostic := failedDSLAttemptValidation(
				"selector-candidate-resolution", resolutionErr, "selector-candidates:failed",
			)
			if !reflect.DeepEqual(reconstructedDiagnostic, lineage.Diagnostic) {
				return nil, fmt.Errorf("%w: selector-repair diagnostic binding is invalid", ErrInvalidWorkflowInput)
			}
			outputContract, reconstructionErr := platformrule.BuildRequirementOutputSchema(requirement.Requirement)
			if reconstructionErr != nil {
				return nil, fmt.Errorf("%w: selector-repair output contract is invalid", ErrInvalidWorkflowInput)
			}
			if !reflect.DeepEqual(json.RawMessage(outputContract), lineage.OutputContract) {
				return nil, fmt.Errorf("%w: selector-repair output contract binding is invalid", ErrInvalidWorkflowInput)
			}
			sourceNonce := lineage.SourceAttemptReportID + "\x00" + lineage.SourceProviderIRHash
			reconstructedPlan, reconstructionErr := selectorCatalog.BuildSelectorRepairPlan(
				lineage.SourceProviderIR, reconstructedFailure, sourceNonce,
			)
			if reconstructionErr != nil || reconstructedPlan == nil ||
				!reflect.DeepEqual(reconstructedPlan.Slots, lineage.Slots) ||
				!reflect.DeepEqual(reconstructedPlan.AllowedAssignments, lineage.AllowedAssignments) {
				return nil, fmt.Errorf("%w: selector-repair slot mapping is invalid", ErrInvalidWorkflowInput)
			}
			plan := SelectorRepairJobRequest{
				SourceAttemptReportID: lineage.SourceAttemptReportID,
				SourceProviderIRHash:  lineage.SourceProviderIRHash,
				RecordingHash:         exported.Attempt.RecordingHash,
				RequirementHash:       exported.Attempt.RequirementHash,
				BaselineHash:          exported.Attempt.BaselineHash,
				SelectorCatalogHash:   exported.Attempt.SelectorCatalogHash,
				SelectorCatalog:       rebuiltCatalog,
				ProviderIR:            lineage.SourceProviderIR,
				Diagnostic:            reconstructedDiagnostic,
				OutputContract:        json.RawMessage(outputContract),
				Failure:               reconstructedFailure,
				Slots:                 reconstructedPlan.Slots,
				AllowedAssignments:    reconstructedPlan.AllowedAssignments,
			}
			if lineage.RepairPlanHash == "" || selectorRepairPlanHash(plan) != lineage.RepairPlanHash {
				return nil, fmt.Errorf("%w: selector-repair plan binding is invalid", ErrInvalidWorkflowInput)
			}
			derived, patchErr := applySelectorRepairPatch(
				lineage.SourceProviderIR, lineage.Slots, lineage.AllowedAssignments, patch,
			)
			if patchErr != nil || lineage.DerivedProviderIR == nil ||
				lineage.DerivedProviderIRHash == "" ||
				dslSemanticJSONHash(derived) != lineage.DerivedProviderIRHash ||
				dslSemanticJSONHash(lineage.DerivedProviderIR) != lineage.DerivedProviderIRHash {
				return nil, fmt.Errorf("%w: selector-repair derived IR binding is invalid", ErrInvalidWorkflowInput)
			}
			effectiveIR = derived
		}
		providerRule, decodeErr := ruleFromAttemptValue(effectiveIR)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: exported provider IR is not a rule", ErrInvalidWorkflowInput)
		}
		restoreOmittedWorkflowMetadata(providerRule, baseline)
		if _, err := selectorCatalog.ResolveHistoricalProviderCandidates(providerRule, exported.Attempt.SelectorCatalogHash); err != nil {
			return nil, fmt.Errorf("%w: exported provider IR cannot be resolved: %v", ErrInvalidWorkflowInput, err)
		}
		if _, err := platformrule.ValidateProvisionalRule(providerRule, baseline, requirement.Requirement); err != nil {
			return nil, fmt.Errorf("%w: exported provider IR failed current validation: %v", ErrInvalidWorkflowInput, err)
		}
		providerRuleMap, err := ruleAsMap(providerRule)
		if err != nil || len(llm.ScanSafety(providerRuleMap)) > 0 {
			return nil, fmt.Errorf("%w: exported provider IR failed the security scanner", ErrInvalidWorkflowInput)
		}
		if _, err := platformrule.ValidateAndStabilizeGeneratedExtractionSelectors(providerRule, selectorCatalog); err != nil {
			return nil, fmt.Errorf("%w: exported provider IR failed selector evidence validation: %v", ErrInvalidWorkflowInput, err)
		}
		if dslSemanticJSONHash(providerRule) != dslSemanticJSONHash(exported.Artifact.ResolvedRule) {
			return nil, fmt.Errorf("%w: provider IR does not resolve to the exported rule", ErrInvalidWorkflowInput)
		}
	}

	resolved, err := copyRule(exported.Artifact.ResolvedRule)
	if err != nil {
		return nil, fmt.Errorf("%w: exported resolved rule is invalid", ErrInvalidWorkflowInput)
	}
	immutableHash := dslSemanticJSONHash(resolved)
	if immutableHash == "" {
		return nil, fmt.Errorf("%w: exported resolved rule cannot be hashed", ErrInvalidWorkflowInput)
	}
	switch strings.TrimSpace(resolved.Source) {
	case "", "pageagent", "pageagent-workflow", models.DSLWorkflowSourceAdminReviewedAttemptExport:
	default:
		return nil, fmt.Errorf("%w: imported rule source cannot claim provider, qualification, or promotion provenance", ErrInvalidWorkflowInput)
	}
	if _, err := platformrule.ValidateProvisionalRule(resolved, baseline, requirement.Requirement); err != nil {
		return nil, fmt.Errorf("%w: exported rule failed current validation: %v", ErrInvalidWorkflowInput, err)
	}
	if dslSemanticJSONHash(resolved) != immutableHash {
		return nil, fmt.Errorf("%w: current validation would mutate the exported resolved rule", ErrInvalidWorkflowInput)
	}
	if historicalInputsNormalized {
		baselineVariables, baselineVariablesOK := strictRuleVariables(baseline.Variables)
		resolvedVariables, resolvedVariablesOK := strictRuleVariables(resolved.Variables)
		if !baselineVariablesOK || !resolvedVariablesOK || !reflect.DeepEqual(baselineVariables, resolvedVariables) {
			return nil, fmt.Errorf("%w: historical input normalization does not match the exported rule", ErrInvalidWorkflowInput)
		}
	}
	ruleMap, err := ruleAsMap(resolved)
	if err != nil || len(llm.ScanSafety(ruleMap)) > 0 {
		return nil, fmt.Errorf("%w: exported resolved rule failed the security scanner", ErrInvalidWorkflowInput)
	}
	evidence, err := platformrule.ValidateAndStabilizeGeneratedExtractionSelectors(resolved, selectorCatalog)
	if err != nil {
		return nil, fmt.Errorf("%w: exported resolved rule failed selector evidence validation: %v", ErrInvalidWorkflowInput, err)
	}
	if evidence.Canonicalized != 0 || dslSemanticJSONHash(resolved) != immutableHash {
		return nil, fmt.Errorf("%w: selector evidence validation would mutate the exported resolved rule", ErrInvalidWorkflowInput)
	}
	yaml, err := RuleToYAML(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: exported resolved rule could not be serialized", ErrInvalidWorkflowInput)
	}
	workflow := &models.DSLWorkflow{
		ID: store.NewID(), RequirementID: requirement.ID, RecordingID: recording.ID,
		BrowserProfileID: browserProfileID, Owner: authz.Subject(ctx, "system"),
		SourceArtifactHash: exported.Attempt.ArtifactHash,
		SourceExportHash:   dslContentHash(string(encodedExport)),
	}
	persistedID, err := m.store.CreateAdminReviewedDSLWorkflow(ctx, workflow, resolved, yaml)
	if err != nil {
		return nil, err
	}
	if persistedID != workflow.ID {
		return m.store.GetDSLWorkflow(ctx, persistedID)
	}
	return workflow, nil
}

func canonicalAdminReviewedBaseline(
	exported *models.Rule,
	requirement models.CollectionRequirementSpec,
) (*models.Rule, bool, bool) {
	if exported == nil {
		return nil, false, false
	}
	variables, ok := strictRuleVariables(exported.Variables)
	if !ok {
		return nil, false, false
	}
	canonical, err := copyRule(exported)
	if err != nil {
		return nil, false, false
	}

	seenInputs := make(map[string]bool)
	missingInputs := make([]models.RequirementInput, 0)
	for _, input := range requirement.RequiredInputs {
		name := strings.TrimSpace(input.Name)
		if name == "" || name != input.Name || seenInputs[name] {
			return nil, false, false
		}
		seenInputs[name] = true
		if _, exists := variables[name]; exists {
			continue
		}
		missingInputs = append(missingInputs, input)
	}
	if len(missingInputs) == 0 {
		return canonical, false, true
	}
	if len(exported.Variables) == 0 || len(variables) != 0 {
		return nil, false, false
	}
	for _, input := range missingInputs {
		value, ok := canonicalRequirementInputDefault(input)
		if !ok {
			return nil, false, false
		}
		variables[input.Name] = value
	}
	encoded, err := json.Marshal(variables)
	if err != nil {
		return nil, false, false
	}
	canonical.Variables = encoded
	return canonical, true, true
}

func strictRuleVariables(raw models.JSON) (map[string]any, bool) {
	if len(raw) == 0 {
		return map[string]any{}, true
	}
	if err := rejectDuplicateJSONObjectKeys(raw); err != nil {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, false
	}
	variables, ok := decoded.(map[string]any)
	return variables, ok
}

func canonicalRequirementInputDefault(input models.RequirementInput) (any, bool) {
	value := input.Default
	if value == nil {
		switch input.Type {
		case models.RequirementValueString:
			value = ""
		case models.RequirementValueNumber:
			value = 0
		case models.RequirementValueBoolean:
			value = false
		case models.RequirementValueObject:
			value = map[string]any{}
		case models.RequirementValueArray:
			value = []any{}
		default:
			return nil, false
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, false
	}
	switch input.Type {
	case models.RequirementValueString:
		_, ok := normalized.(string)
		return normalized, ok
	case models.RequirementValueNumber:
		_, ok := normalized.(float64)
		return normalized, ok
	case models.RequirementValueBoolean:
		_, ok := normalized.(bool)
		return normalized, ok
	case models.RequirementValueObject:
		_, ok := normalized.(map[string]any)
		return normalized, ok
	case models.RequirementValueArray:
		_, ok := normalized.([]any)
		return normalized, ok
	default:
		return nil, false
	}
}

func validateCompleteAdoptionRecording(recording *models.Recording) error {
	if recording == nil || recording.Status != models.RecordingStatusCaptured || recording.Payload == nil {
		return fmt.Errorf("%w: a captured complete recording is required", ErrInvalidWorkflowInput)
	}
	snapshots, _ := recording.Payload["snapshots"].([]any)
	if len(snapshots) == 0 {
		snapshots, _ = recording.Payload["domSnapshots"].([]any)
	}
	if len(snapshots) < 2 {
		return fmt.Errorf("%w: recording must contain complete initial and final snapshots", ErrInvalidWorkflowInput)
	}
	for _, raw := range snapshots {
		snapshot, ok := raw.(map[string]any)
		capture, captureOK := snapshot["capture"].(map[string]any)
		_, domOK := snapshot["domTree"].(map[string]any)
		if !ok || !captureOK || strings.TrimSpace(fmt.Sprint(capture["status"])) != "complete" || !domOK {
			return fmt.Errorf("%w: recording contains an incomplete semantic snapshot", ErrInvalidWorkflowInput)
		}
	}
	return nil
}

func dslSemanticJSONStringHash(value string) string {
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return ""
	}
	return dslSemanticJSONHash(decoded)
}

func ruleFromAttemptValue(value any) (*models.Rule, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var rule models.Rule
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rule); err != nil {
		return nil, err
	}
	return &rule, nil
}

func (m *Manager) GetWorkflow(ctx context.Context, id string) (*models.DSLWorkflow, error) {
	return m.store.GetDSLWorkflow(ctx, id)
}

func (m *Manager) GetJob(ctx context.Context, id string) (*models.DSLJob, error) {
	return m.store.GetDSLJob(ctx, id)
}

func (m *Manager) ListProviderAttempts(ctx context.Context, jobID string) ([]*models.LLMAttemptReport, error) {
	if _, err := m.store.GetDSLJob(ctx, jobID); err != nil {
		return nil, err
	}
	return m.store.ListLLMAttemptReports(ctx, models.LLMJobTypeDSL, jobID)
}

func (m *Manager) GetProviderAttempt(ctx context.Context, jobID string, attemptNumber int) (*DSLAttemptDetail, error) {
	if _, err := m.store.GetDSLJob(ctx, jobID); err != nil {
		return nil, err
	}
	reports, err := m.store.ListLLMAttemptReports(ctx, models.LLMJobTypeDSL, jobID)
	if err != nil {
		return nil, err
	}
	var metadata *models.LLMAttemptReport
	for _, report := range reports {
		if report.AttemptNumber == attemptNumber {
			metadata = report
			break
		}
	}
	if metadata == nil {
		return nil, store.ErrLLMAttemptReportNotFound
	}
	report, err := m.store.GetLLMAttemptReport(ctx, metadata.ID)
	if err != nil {
		return nil, err
	}
	calls, err := m.store.ListLLMProviderCalls(ctx, models.LLMJobTypeDSL, jobID, attemptNumber)
	if err != nil {
		return nil, err
	}
	return &DSLAttemptDetail{Report: report, Calls: calls, Artifact: report.Artifact}, nil
}

func (m *Manager) GetProviderCall(ctx context.Context, jobID string, attemptNumber int, callID string) (*models.LLMProviderCall, error) {
	if _, err := m.store.GetDSLJob(ctx, jobID); err != nil {
		return nil, err
	}
	calls, err := m.store.ListLLMProviderCalls(ctx, models.LLMJobTypeDSL, jobID, attemptNumber)
	if err != nil {
		return nil, err
	}
	for _, call := range calls {
		if call.ID == callID {
			return m.store.GetLLMProviderCall(ctx, call.ID)
		}
	}
	return nil, store.ErrLLMProviderCallNotFound
}

func (m *Manager) GetReplay(ctx context.Context, id string) (*models.ReplayAttempt, error) {
	return m.store.GetDSLReplay(ctx, id)
}

func (m *Manager) StartReplay(ctx context.Context, workflowID string) (*models.ReplayAttempt, error) {
	return m.store.StartDSLReplay(ctx, workflowID, m.cfg.ReplayAttemptTimeout())
}

// Correct replaces a failed or awaiting-replay provisional rule with a
// human-authored complete rule. It deliberately runs the same validation and
// safety gates as provider output and never bypasses replay or confirmation.
func (m *Manager) Correct(ctx context.Context, workflowID string, corrected *models.Rule) (*models.DSLWorkflow, error) {
	if m == nil || m.store == nil || corrected == nil {
		return nil, fmt.Errorf("%w: complete corrected rule is required", ErrInvalidWorkflowInput)
	}
	current, err := m.store.GetDSLWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if current.Status != models.DSLWorkflowFailed && current.Status != models.DSLWorkflowAwaitingReplay {
		return nil, store.ErrDSLWorkflowState
	}
	trustedBaseline := current.ProvisionalRule
	if trustedBaseline == nil {
		requestValue, requestErr := m.store.GetDSLWorkflowGenerationRequest(ctx, workflowID)
		if requestErr != nil {
			return nil, requestErr
		}
		var generationRequest GenerationJobRequest
		if requestErr := decodeDSLRequest(requestValue, &generationRequest); requestErr != nil ||
			generationRequest.BaselineRule == nil {
			return nil, fmt.Errorf("%w: trusted generation baseline is unavailable", ErrInvalidWorkflowInput)
		}
		trustedBaseline = generationRequest.BaselineRule
	}
	requirement, err := m.store.GetCollectionRequirement(ctx, current.RequirementID)
	if err != nil {
		return nil, err
	}
	correctedCopy, err := copyRule(corrected)
	if err != nil {
		return nil, fmt.Errorf("%w: corrected rule is invalid", ErrInvalidWorkflowInput)
	}
	if _, err := platformrule.ValidateProvisionalRule(correctedCopy, trustedBaseline, requirement.Requirement); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWorkflowInput, err)
	}
	ruleMap, err := ruleAsMap(correctedCopy)
	if err != nil {
		return nil, fmt.Errorf("%w: corrected rule is invalid", ErrInvalidWorkflowInput)
	}
	if flags := llm.ScanSafety(ruleMap); len(flags) > 0 {
		return nil, fmt.Errorf("%w: corrected rule failed the security scanner", ErrInvalidWorkflowInput)
	}
	recording, err := m.recording.Get(ctx, current.RecordingID)
	if err != nil {
		return nil, err
	}
	selectorCatalog, err := platformrule.BuildSelectorEvidenceCatalog(recordingPayloadWithRequirementMarks(recording.Payload, requirement.Marks))
	if err != nil {
		return nil, fmt.Errorf("%w: selector evidence could not be derived: %v", ErrInvalidWorkflowInput, err)
	}
	if _, err := platformrule.ValidateAndStabilizeGeneratedExtractionSelectors(correctedCopy, selectorCatalog); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWorkflowInput, err)
	}
	yaml, err := RuleToYAML(correctedCopy)
	if err != nil {
		return nil, fmt.Errorf("%w: corrected rule could not be serialized", ErrInvalidWorkflowInput)
	}
	if err := m.store.CorrectDSLWorkflow(ctx, workflowID, correctedCopy, yaml); err != nil {
		return nil, err
	}
	return m.store.GetDSLWorkflow(ctx, workflowID)
}

func (m *Manager) CompleteReplay(ctx context.Context, workflowID, replayID string, input ReplayCompletionInput) (*models.ReplayAttempt, *models.DSLJob, error) {
	attempt, err := m.store.GetDSLReplay(ctx, replayID)
	if err != nil {
		return nil, nil, err
	}
	if attempt.WorkflowID != workflowID {
		return nil, nil, store.ErrReplayNotFound
	}
	workflow, err := m.store.GetDSLWorkflow(ctx, workflowID)
	if err != nil {
		return nil, nil, err
	}
	if workflow.Status != models.DSLWorkflowReplaying || workflow.ProvisionalRule == nil {
		return nil, nil, store.ErrDSLWorkflowState
	}
	requirement, err := m.store.GetCollectionRequirement(ctx, workflow.RequirementID)
	if err != nil {
		return nil, nil, err
	}
	if err := enforceJSONSize(input.Output, maxReplayOutputBytes, "output"); err != nil {
		return nil, nil, err
	}
	if err := enforceJSONSize(input.Artifacts, maxReplayArtifactsBytes, "artifacts"); err != nil {
		return nil, nil, err
	}
	// Keep a larger hard transport ceiling to prevent resource abuse, then
	// redact and summarize ordinary oversized browser diagnostics to the much
	// smaller persisted/LLM budget. A verbose page failure must not strand a
	// replay attempt before the bounded repair workflow can run.
	if err := enforceJSONSize(input.Diagnostics, maxReplayDiagnosticsInputBytes, "diagnostics"); err != nil {
		return nil, nil, err
	}
	safeDiagnostics := sanitizeReplayDiagnostics(input.Diagnostics)
	if err := enforceJSONSize(safeDiagnostics, maxReplayDiagnosticsBytes, "sanitized diagnostics"); err != nil {
		return nil, nil, err
	}
	safeArtifacts := sanitizeReplayArtifacts(input.Artifacts)
	code := safeCode(input.ErrorCode)
	message := safeMessage(input.ErrorMessage, 1000)
	outputValid := false
	if input.Succeeded && input.Output == nil {
		// A success completion with no collected output comes from the
		// extension's no-wizard fallback racing the wizard's row-carrying
		// completion. Consuming the attempt here would terminally fail a
		// replay the browser executed well; reject without consuming.
		return nil, nil, ErrReplayOutputRequired
	}
	if input.Succeeded {
		if validationErr := platformrule.ValidateReplayOutput(input.Output, requirement.Requirement); validationErr == nil {
			outputValid = true
		} else {
			code = "OUTPUT_SCHEMA_INVALID"
			message = safeMessage(validationErr.Error(), 1000)
			safeDiagnostics = mergeDiagnostics(safeDiagnostics, map[string]any{"outputSchemaError": message})
		}
	}
	if !input.Succeeded && code == "" {
		code = "REPLAY_FAILED"
	}
	if !input.Succeeded && message == "" {
		message = "full rule replay failed"
	}
	repairDiagnostics := mergeDiagnostics(safeDiagnostics, map[string]any{
		"errorCode": code, "errorMessage": message, "outputValid": outputValid,
	})
	repairRequest := RepairJobRequest{
		CurrentRule: workflow.ProvisionalRule, Diagnostics: repairDiagnostics,
		Artifacts:      repairArtifactContext(safeArtifacts),
		ReplaySequence: attempt.Sequence,
	}
	var repair any = repairRequest
	if input.Succeeded && outputValid {
		repair = nil
	}
	repairJob, err := m.store.CompleteDSLReplay(ctx, attempt, input.Succeeded, outputValid,
		safeDiagnostics, input.Output, safeArtifacts, code, message, repair, m.cfg.DSLGenerationMaxAttempts())
	if err != nil {
		return nil, nil, err
	}
	return attempt, repairJob, nil
}

func (m *Manager) Confirm(ctx context.Context, workflowID string, opts store.ApproveDSLWorkflowOptions) (*models.RuleVersion, *models.RuleVersionContract, error) {
	return m.store.ApproveDSLWorkflow(ctx, workflowID, opts)
}

func (m *Manager) GetDSLApprovalProvenance(ctx context.Context, workflowID string) (*store.DSLApprovalProvenance, error) {
	return m.store.GetDSLApprovalProvenance(ctx, workflowID)
}

func (m *Manager) StartWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.ProcessOnce(ctx)
		}
	}
}

func (m *Manager) ProcessOnce(ctx context.Context) {
	if m == nil || m.store == nil || m.cfg == nil || m.workflow == nil || !m.cfg.LLMEnabled {
		return
	}
	batch := m.cfg.LLMJobBatchSize
	if batch <= 0 {
		batch = 10
	}
	lease := m.cfg.LLMRequestTimeout + 2*time.Minute
	for index := 0; index < batch; index++ {
		job, err := m.store.ClaimPendingDSLJob(ctx, lease)
		if err != nil {
			if !errors.Is(err, store.ErrNoTaskAvailable) {
				m.logger.Error("claim dsl workflow job failed", zap.Error(err))
			}
			return
		}
		jobContext := authz.WithPrincipal(ctx, authz.Principal{
			Subject: "dsl-worker", WorkspaceID: job.WorkspaceID,
			Roles: []authz.Role{authz.RoleAdmin}, Kind: authz.PrincipalSystem,
		})
		m.runJob(jobContext, job)
	}
}

func (m *Manager) runJob(ctx context.Context, job *models.DSLJob) {
	// Bind the durable dispatch identity so every physical call this attempt
	// makes is admitted against the hard budget under the workspace persisted
	// with the job. WorkflowID carries the parent DSL workflow so each call is
	// also gated against the per-workflow budget envelope.
	ctx = llm.WithDispatchOperation(ctx, llm.DispatchOperation{
		Kind:           budget.OperationDSL,
		ID:             job.ID,
		LogicalAttempt: llm.DispatchAttempt(job.AttemptCount),
		WorkspaceID:    job.WorkspaceID,
		WorkflowID:     job.WorkflowID,
	})

	workflow, err := m.store.GetDSLWorkflow(ctx, job.WorkflowID)
	if err != nil {
		m.failJob(ctx, job, err)
		return
	}
	requirement, err := m.store.GetCollectionRequirement(ctx, workflow.RequirementID)
	if err != nil {
		m.failJob(ctx, job, err)
		return
	}
	var result *WorkflowResult
	var metadata WorkflowRunMetadata
	var baseline *models.Rule
	var selectorCatalog *platformrule.SelectorEvidenceCatalog
	var selectorPrompt string
	var recorder *dslAttemptRecorder
	var providerIR any
	var resolvedRule *models.Rule
	providerAttempted := false
	validations := []DSLAttemptValidation{}

	recording, recordingErr := m.recording.Get(ctx, workflow.RecordingID)
	if recordingErr != nil {
		m.failJob(ctx, job, recordingErr)
		return
	}
	if job.Kind == models.DSLJobSelectorRepair {
		m.runSelectorRepairJob(ctx, job, workflow, requirement, recording)
		return
	}
	recorder = newDSLAttemptRecorder(
		m.store, job, recording.ContentHash, requirement.ContentHash,
		"", "", m.policyFingerprint(),
	)
	selectorCatalog, err = platformrule.BuildSelectorEvidenceCatalog(recording.Payload)
	if err != nil {
		err = fmt.Errorf("selector evidence could not be derived: %w", err)
		validations = append(validations, failedDSLAttemptValidation("selector-catalog", err))
		attemptReport, attemptArtifact, reportErr := recorder.buildReport(nil, nil, validations, err)
		if reportErr != nil {
			err = reportErr
		}
		m.failJobWithAttemptReport(ctx, job, err, attemptReport, attemptArtifact)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "selector-evidence-source", Status: "passed"})

	switch job.Kind {
	case models.DSLJobGenerate:
		var request GenerationJobRequest
		if err = decodeDSLRequest(job.Request, &request); err == nil {
			baseline = request.BaselineRule
			recorder.baseline = baseline
			if baseline != nil {
				recorder.baselineHash = dslJSONHash(baseline)
			}
			if err == nil {
				var providerBaseline *models.Rule
				selectorPrompt, providerBaseline, err = selectorCatalog.PrepareProviderPrompt(baseline)
				if err != nil {
					err = fmt.Errorf("prepare opaque selector candidates: %w", err)
					validations = append(validations, failedDSLAttemptValidation("selector-catalog", err))
				}
				if err == nil {
					err = platformrule.EncodeProviderOrdinaryTargets(providerBaseline)
					if err != nil {
						err = fmt.Errorf("prepare provider ordinary targets: %w", err)
						validations = append(validations, failedDSLAttemptValidation("provider-target-wire", err))
					}
				}
				if err != nil {
					break
				}
				recorder.selectorCatalogHash = selectorCatalog.PreparedProviderPromptCatalogHash()
				recorder.selectorCatalogPrompt = selectorPrompt
				validations = append(validations, DSLAttemptValidation{
					Phase: "selector-catalog", Status: "passed",
				})
				onProgress := func(completed, total int) {
					job.ChunkCount = total
					job.CompletedChunks = completed
					if updateErr := m.store.UpdateDSLJobAttemptProgress(ctx, job.ID, job.AttemptCount, total, completed); updateErr != nil {
						m.logger.Warn("record dsl job progress failed", zap.String("jobId", job.ID), zap.Error(updateErr))
					}
				}
				workflowCtx := llm.WithCompletionTraceSink(ctx, recorder)
				providerAttempted = true
				result, metadata, err = m.workflow.Generate(
					workflowCtx, recording.Payload, requirement.Requirement, providerBaseline,
					onProgress, previousAttemptFeedback(job.AttemptCount, job.ErrorMessage), selectorPrompt,
				)
				if err != nil && requirement.Source == models.RequirementSourceManual && errors.Is(err, ErrProviderUnavailable) && m.cfg.AllowDegradedFallback() {
					providerMetadata := metadata
					var fallbackMetadata WorkflowRunMetadata
					result, fallbackMetadata, err = DeterministicBaseline(baseline, requirement.Requirement, "configured LLM providers were unavailable")
					metadata = combineWorkflowRunMetadata(providerMetadata, fallbackMetadata)
				}
			}
		} else {
			validations = append(validations, failedDSLAttemptValidation("request-decode", err))
		}
	case models.DSLJobRepair:
		var request RepairJobRequest
		if err = decodeDSLRequest(job.Request, &request); err == nil {
			baseline = request.CurrentRule
			recorder.baseline = baseline
			if baseline != nil {
				recorder.baselineHash = dslJSONHash(baseline)
			}
			if err == nil {
				var providerCurrent *models.Rule
				selectorPrompt, providerCurrent, err = selectorCatalog.PrepareProviderPrompt(request.CurrentRule)
				if err != nil {
					err = fmt.Errorf("prepare opaque selector candidates: %w", err)
					validations = append(validations, failedDSLAttemptValidation("selector-catalog", err))
					break
				}
				if err = platformrule.EncodeProviderOrdinaryTargets(providerCurrent); err != nil {
					err = fmt.Errorf("prepare provider ordinary targets: %w", err)
					validations = append(validations, failedDSLAttemptValidation("provider-target-wire", err))
					break
				}
				recorder.selectorCatalogHash = selectorCatalog.PreparedProviderPromptCatalogHash()
				recorder.selectorCatalogPrompt = selectorPrompt
				validations = append(validations, DSLAttemptValidation{
					Phase: "selector-catalog", Status: "passed",
				})
				diagnostics := request.Diagnostics
				if len(request.Artifacts) > 0 {
					diagnostics = mergeDiagnostics(diagnostics, map[string]any{"replayArtifacts": request.Artifacts})
				}
				workflowCtx := llm.WithCompletionTraceSink(ctx, recorder)
				providerAttempted = true
				result, metadata, err = m.workflow.Repair(
					workflowCtx, requirement.Requirement, providerCurrent, diagnostics, selectorPrompt,
				)
			}
		} else {
			validations = append(validations, failedDSLAttemptValidation("request-decode", err))
		}
	default:
		err = fmt.Errorf("unknown dsl job kind %q", job.Kind)
	}
	accumulateDSLJobMetadata(job, metadata)
	if providerAttempted && err != nil {
		validations = append(validations, failedDSLAttemptValidation("provider-output", err))
	} else if providerAttempted {
		validations = append(validations, DSLAttemptValidation{Phase: "provider-output", Status: "passed"})
	}
	if err == nil && (result == nil || result.Rule == nil || baseline == nil) {
		err = fmt.Errorf("%w: generated result is empty", ErrInvalidLLMRule)
	}
	if err == nil {
		if !result.Degraded {
			providerIR = result.ProviderIR
		}
		validations = append(validations, DSLAttemptValidation{Phase: "provider-output-parse", Status: "passed"})
	} else if errors.Is(err, ErrInvalidLLMRule) {
		validations = append(validations, failedDSLAttemptValidation("provider-output-parse", err))
	}
	var safetyFlags []string
	if err == nil && !result.Degraded {
		resolution, resolutionErr := selectorCatalog.ResolveProviderCandidates(
			result.Rule, result.SelectorCatalogHash,
		)
		if resolutionErr != nil {
			safetyFlags = append(safetyFlags, "selector-candidates:failed")
			err = resolutionErr
			validations = append(validations, failedDSLAttemptValidation(
				"selector-candidate-resolution", err, "selector-candidates:failed",
			))
		} else {
			safetyFlags = append(
				safetyFlags,
				"selector-candidates:resolved",
				fmt.Sprintf("selector-candidates:targets=%d", resolution.Targets),
				fmt.Sprintf("selector-candidates:fields=%d", resolution.Fields),
				fmt.Sprintf("provider-targets:resolved=%d", resolution.OrdinaryTargets),
			)
			validations = append(validations, DSLAttemptValidation{
				Phase: "selector-candidate-resolution", Status: "passed",
				SafetyFlags: []string{
					"selector-candidates:resolved",
					fmt.Sprintf("selector-candidates:targets=%d", resolution.Targets),
					fmt.Sprintf("selector-candidates:fields=%d", resolution.Fields),
					fmt.Sprintf("provider-targets:resolved=%d", resolution.OrdinaryTargets),
				},
			})
		}
	}
	if err == nil {
		var validationFlags []string
		validationFlags, err = platformrule.ValidateProvisionalRule(result.Rule, baseline, requirement.Requirement)
		safetyFlags = append(safetyFlags, validationFlags...)
		if err != nil {
			validations = append(validations, failedDSLAttemptValidation(
				"schema-and-contract-validation", err, validationFlags...,
			))
		} else {
			validations = append(validations, DSLAttemptValidation{
				Phase: "schema-and-contract-validation", Status: "passed",
				SafetyFlags: append([]string(nil), validationFlags...),
			})
		}
	}
	if err == nil {
		ruleMap, marshalErr := ruleAsMap(result.Rule)
		if marshalErr != nil {
			err = marshalErr
			validations = append(validations, failedDSLAttemptValidation("security-scan", err))
		} else if scannerFlags := llm.ScanSafety(ruleMap); len(scannerFlags) > 0 {
			for _, flag := range scannerFlags {
				safetyFlags = append(safetyFlags, fmt.Sprintf("security-scan:%s:%s", flag.StepIndex, flag.Action))
			}
			err = ErrUnsafeGeneratedRule
			validations = append(validations, failedDSLAttemptValidation("security-scan", err, safetyFlags...))
		} else {
			safetyFlags = append(safetyFlags, "security-scan:passed")
			validations = append(validations, DSLAttemptValidation{Phase: "security-scan", Status: "passed"})
		}
	}
	if err == nil {
		report, evidenceErr := platformrule.ValidateAndStabilizeGeneratedExtractionSelectors(result.Rule, selectorCatalog)
		if evidenceErr != nil {
			safetyFlags = append(safetyFlags, "selector-evidence:failed")
			err = evidenceErr
			validations = append(validations, failedDSLAttemptValidation(
				"selector-evidence", err, "selector-evidence:failed",
			))
		} else {
			safetyFlags = append(safetyFlags,
				"selector-evidence:passed",
				fmt.Sprintf("selector-evidence:checked=%d", report.Checked),
				fmt.Sprintf("selector-evidence:canonicalized=%d", report.Canonicalized),
			)
			validations = append(validations, DSLAttemptValidation{
				Phase: "selector-evidence", Status: "passed",
				SafetyFlags: []string{
					"selector-evidence:passed",
					fmt.Sprintf("selector-evidence:checked=%d", report.Checked),
					fmt.Sprintf("selector-evidence:canonicalized=%d", report.Canonicalized),
				},
			})
			resolvedRule, _ = copyRule(result.Rule)
		}
	}
	if err == nil {
		cacheFlag := "cache:miss"
		if workflowResultCacheHit(result) {
			cacheFlag = "cache:hit"
		}
		degradedFlag := "degraded:false"
		if result.Degraded {
			degradedFlag = "degraded:true"
		}
		safetyFlags = append(safetyFlags, cacheFlag, degradedFlag)
	}
	if err != nil {
		eligibleSelectorRepair := isEligibleSelectorRepairError(err)
		selectorRepairEnabled := m.cfg.SelectorRepairEnabled()
		if eligibleSelectorRepair && !selectorRepairEnabled {
			const disabledFlag = "selector-repair:auto-disabled"
			safetyFlags = append(safetyFlags, disabledFlag)
			if len(validations) == 0 {
				validations = append(validations, DSLAttemptValidation{
					Phase: "selector-repair-scheduling", Status: "passed",
					SafetyFlags: []string{disabledFlag},
				})
			} else {
				last := &validations[len(validations)-1]
				last.SafetyFlags = append(last.SafetyFlags, disabledFlag)
			}
		}
		job.SafetyFlags = safetyFlags
		attemptReport, attemptArtifact, reportErr := recorder.buildReport(providerIR, resolvedRule, validations, err)
		if reportErr != nil {
			err = reportErr
		}
		if eligibleSelectorRepair && selectorRepairEnabled &&
			isEligibleSelectorRepairError(err) {
			scheduled, scheduleErr := m.scheduleEligibleSelectorRepair(
				ctx, job, attemptReport, attemptArtifact, providerIR, baseline,
				selectorCatalog, selectorPrompt, requirement.Requirement, validations, err,
			)
			if scheduleErr != nil {
				job.MaxAttempts = job.AttemptCount
				fields := []zap.Field{zap.String("jobId", job.ID)}
				if m.cfg.EnforcedLLMPolicy() != nil {
					fields = append(fields, llm.HashedAuditFields(
						"error",
						"selector_repair_schedule_failed",
						scheduleErr.Error(),
					)...)
				} else {
					fields = append(fields,
						zap.String("error", safeMessage(scheduleErr.Error(), maxDSLValidationFeedbackBytes)))
				}
				m.logger.Warn("selector-only repair was not scheduled", fields...)
			} else if scheduled {
				return
			}
		}
		m.failJobWithAttemptReport(ctx, job, err, attemptReport, attemptArtifact)
		return
	}
	yaml, err := RuleToYAML(result.Rule)
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("serialization", err))
		attemptReport, attemptArtifact, reportErr := recorder.buildReport(providerIR, resolvedRule, validations, err)
		if reportErr != nil {
			err = reportErr
		}
		m.failJobWithAttemptReport(ctx, job, err, attemptReport, attemptArtifact)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "serialization", Status: "passed"})
	job.CompletedChunks = job.ChunkCount
	// WI-7: scan for sealing-only concerns. These do NOT fail generation but
	// become gating flags at ApproveDSLWorkflow (interpreted by the blocking
	// taxonomy in models/safety.go). WI-15: ContentFilterRule adds
	// step-attributed content-filter:* flags alongside the sealing keys.
	if resolvedRule != nil {
		if ruleMap, marshalErr := ruleAsMap(resolvedRule); marshalErr == nil {
			safetyFlags = append(safetyFlags, llm.ScanSealingFlags(ruleMap)...)
			for _, cf := range llm.ContentFilterRule(ruleMap) {
				safetyFlags = append(safetyFlags, fmt.Sprintf("%s:%s:%s", cf.Action, cf.StepIndex, cf.Reason))
			}
		}
	}
	job.SafetyFlags = safetyFlags
	attemptReport, attemptArtifact, reportErr := recorder.buildReport(providerIR, resolvedRule, validations, nil)
	if reportErr != nil {
		m.failJobWithAttemptReport(ctx, job, reportErr, attemptReport, attemptArtifact)
		return
	}
	if err := m.store.CompleteDSLJobWithAttemptReport(ctx, job, result.Rule, yaml, result, attemptReport, attemptArtifact); err != nil {
		m.logger.Error("complete dsl workflow job failed", zap.String("jobId", job.ID), zap.Error(err))
	}
}

func (m *Manager) scheduleEligibleSelectorRepair(
	ctx context.Context,
	job *models.DSLJob,
	report *models.LLMAttemptReport,
	reportArtifact any,
	providerIR any,
	baseline *models.Rule,
	catalog *platformrule.SelectorEvidenceCatalog,
	selectorCatalog string,
	requirement models.CollectionRequirementSpec,
	validations []DSLAttemptValidation,
	runErr error,
) (bool, error) {
	var selection *platformrule.SelectorCandidateSelectionError
	if !errors.As(runErr, &selection) || report == nil || !report.Replayable ||
		providerIR == nil || baseline == nil || catalog == nil || selectorCatalog == "" {
		return false, nil
	}
	if report.ID == "" {
		report.ID = store.NewID()
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Now().UTC()
	}
	normalizedSelection, err := normalizeSelectorRepairFailure(selection)
	if err != nil {
		return false, err
	}
	selection = normalizedSelection
	encodedIR, err := json.Marshal(providerIR)
	if err != nil || len(encodedIR) == 0 || len(encodedIR) > maxSelectorRepairProviderIRBytes {
		return false, fmt.Errorf("%w: eligible provider IR exceeds the selector repair bound", ErrWorkflowSourceUnavailable)
	}
	var sourceIR models.Rule
	if err := json.Unmarshal(encodedIR, &sourceIR); err != nil {
		return false, fmt.Errorf("%w: eligible provider IR is not a rule", ErrInvalidLLMRule)
	}
	sourceNonce := report.ID + "\x00" + dslSemanticJSONHash(&sourceIR)
	plan, err := catalog.BuildSelectorRepairPlan(&sourceIR, selection, sourceNonce)
	if err != nil {
		return false, err
	}
	if err := validateSelectorRepairImmutableRemainder(
		&sourceIR, baseline, catalog, report.SelectorCatalogHash,
		requirement, plan,
	); err != nil {
		return false, err
	}
	artifact, ok := reportArtifact.(DSLAttemptArtifact)
	if !ok || artifact.ProviderIRSourceCallID == "" {
		return false, fmt.Errorf("%w: source attempt lineage is incomplete", ErrWorkflowSourceUnavailable)
	}
	var sourceCall *models.LLMProviderCall
	for _, call := range artifact.Calls {
		if call != nil && call.ID == artifact.ProviderIRSourceCallID {
			sourceCall = call
			break
		}
	}
	if sourceCall == nil || !sourceCall.Replayable {
		return false, fmt.Errorf("%w: source provider call is not replayable", ErrWorkflowSourceUnavailable)
	}
	diagnostic := DSLAttemptValidation{}
	for index := len(validations) - 1; index >= 0; index-- {
		if validations[index].Status == "failed" {
			diagnostic = validations[index]
			break
		}
	}
	if diagnostic.Phase != "selector-candidate-resolution" {
		return false, nil
	}
	outputContract, err := platformrule.BuildRequirementOutputSchema(requirement)
	if err != nil {
		return false, err
	}
	request := SelectorRepairJobRequest{
		SourceJobID: job.ID, SourceAttemptNumber: job.AttemptCount,
		SourceAttemptReportID:      report.ID,
		SourceProviderCallID:       artifact.ProviderIRSourceCallID,
		SourceProviderResponseHash: sourceCall.ResponseHash,
		SourceProviderIRHash:       dslSemanticJSONHash(&sourceIR),
		RecordingHash:              report.RecordingHash, RequirementHash: report.RequirementHash,
		BaselineHash: report.BaselineHash, SelectorCatalogHash: report.SelectorCatalogHash,
		SelectorCatalog: selectorCatalog, ProviderIR: &sourceIR,
		TrustedBaseline: baseline, Diagnostic: diagnostic,
		OutputContract: json.RawMessage(outputContract), Failure: selection,
		Slots: plan.Slots, AllowedAssignments: plan.AllowedAssignments,
	}
	request.RepairPlanHash = selectorRepairPlanHash(request)
	if request.RepairPlanHash == "" {
		return false, fmt.Errorf("%w: selector repair plan could not be authenticated", ErrWorkflowSourceUnavailable)
	}
	// Constructing the exact prompt enforces every independent input bound
	// before the source attempt is terminalized and before any call can occur.
	if _, err := selectorRepairUserPrompt(request); err != nil {
		return false, err
	}
	_, err = m.store.ScheduleDSLSelectorRepairWithAttemptReport(
		ctx, job, report, reportArtifact, request,
	)
	if err != nil {
		return false, err
	}
	return true, nil
}

func validateSelectorRepairImmutableRemainder(
	sourceIR, baseline *models.Rule,
	catalog *platformrule.SelectorEvidenceCatalog,
	catalogHash string,
	requirement models.CollectionRequirementSpec,
	plan *platformrule.SelectorRepairPlan,
) error {
	if plan == nil {
		return fmt.Errorf("%w: selector repair plan is empty", ErrInvalidWorkflowInput)
	}
	for _, assignment := range plan.AllowedAssignments {
		replacements := make([]selectorRepairReplacement, 0, len(plan.Slots))
		for _, slot := range plan.Slots {
			candidateID := assignment[slot.ID]
			if candidateID != "" && candidateID != slot.CurrentCandidateID {
				replacements = append(replacements, selectorRepairReplacement{
					SlotID: slot.ID, CandidateID: candidateID,
				})
			}
		}
		if len(replacements) == 0 {
			continue
		}
		candidate, err := applySelectorRepairPatch(
			sourceIR, plan.Slots, plan.AllowedAssignments,
			&selectorRepairResponse{
				SelectorCatalogHash: catalogHash, Replacements: replacements,
			},
		)
		if err != nil {
			continue
		}
		restoreOmittedWorkflowMetadata(candidate, baseline)
		if _, err = catalog.ResolveHistoricalProviderCandidates(candidate, catalogHash); err != nil {
			continue
		}
		if _, err = platformrule.ValidateProvisionalRule(candidate, baseline, requirement); err != nil {
			continue
		}
		ruleMap, err := ruleAsMap(candidate)
		if err != nil || len(llm.ScanSafety(ruleMap)) > 0 {
			continue
		}
		if _, err = platformrule.ValidateAndStabilizeGeneratedExtractionSelectors(candidate, catalog); err != nil {
			continue
		}
		if _, err = RuleToYAML(candidate); err != nil {
			continue
		}
		return nil
	}
	return fmt.Errorf(
		"%w: selector repair cannot isolate the selector failure from immutable contract or safety defects",
		ErrInvalidWorkflowInput,
	)
}

func (m *Manager) runSelectorRepairJob(
	ctx context.Context,
	job *models.DSLJob,
	workflow *models.DSLWorkflow,
	requirement *models.CollectionRequirement,
	recording *models.Recording,
) {
	recorder := newDSLAttemptRecorder(
		m.store, job, recording.ContentHash, requirement.ContentHash, "", "",
		m.policyFingerprint(),
	)
	validations := []DSLAttemptValidation{}
	var request SelectorRepairJobRequest
	err := decodeDSLRequest(job.Request, &request)
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("request-decode", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, nil, nil, validations, err)
		return
	}
	recorder.baselineHash = request.BaselineHash
	recorder.selectorCatalogHash = request.SelectorCatalogHash
	recorder.baseline = request.TrustedBaseline
	if err = m.validateSelectorRepairLineage(ctx, job, workflow, requirement, recording, request); err != nil {
		validations = append(validations, failedDSLAttemptValidation("source-lineage", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, nil, nil, validations, err)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "source-lineage", Status: "passed"})
	recorder.selectorRepair = &DSLSelectorRepairLineage{
		SourceAttemptReportID: request.SourceAttemptReportID,
		SourceProviderCallID:  request.SourceProviderCallID,
		SourceProviderIRHash:  request.SourceProviderIRHash,
		RepairPlanHash:        request.RepairPlanHash,
		Diagnostic:            request.Diagnostic,
		OutputContract:        request.OutputContract,
		Failure:               request.Failure,
		Slots:                 request.Slots,
		AllowedAssignments:    request.AllowedAssignments,
	}
	recorder.selectorRepair.SourceProviderIR, _ = copyRule(request.ProviderIR)

	catalog, err := platformrule.BuildSelectorEvidenceCatalog(recording.Payload)
	if err == nil {
		var rebuilt string
		rebuilt, _, err = catalog.ReconstructProviderPrompt(request.SelectorCatalog, request.TrustedBaseline)
		if err == nil && (rebuilt != request.SelectorCatalog ||
			catalog.PreparedProviderPromptCatalogHash() != request.SelectorCatalogHash) {
			err = fmt.Errorf("%w: exact selector catalog rebuild does not match the source attempt", ErrWorkflowSourceUnavailable)
		}
		if err == nil {
			recorder.selectorCatalogPrompt = rebuilt
		}
	}
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("selector-catalog-rebuild", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, nil, nil, validations, err)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "selector-catalog-rebuild", Status: "passed"})

	preparedCompletion, err := m.workflow.prepareSelectorRepairCompletion(request)
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("provider-readiness", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, nil, nil, validations, err)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "provider-readiness", Status: "passed"})

	if err = m.store.MarkDSLSelectorRepairDispatched(ctx, job); err != nil {
		validations = append(validations, failedDSLAttemptValidation("provider-dispatch", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, nil, nil, validations, err)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "provider-dispatch", Status: "passed"})

	workflowCtx := llm.WithCompletionTraceSink(ctx, recorder)
	response, metadata, audit, err := m.workflow.repairSelectorsPrepared(workflowCtx, preparedCompletion)
	accumulateDSLJobMetadata(job, metadata)
	job.PromptVersion = selectorRepairPromptVersion()
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("provider-output", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, nil, nil, validations, err)
		return
	}
	validations = append(validations,
		DSLAttemptValidation{Phase: "provider-output", Status: "passed"},
		DSLAttemptValidation{Phase: "provider-output-parse", Status: "passed"},
	)
	providerIR := any(response)
	recorder.selectorRepair.PatchProviderIRHash = dslSemanticJSONHash(providerIR)
	if response.SelectorCatalogHash != request.SelectorCatalogHash {
		err = fmt.Errorf("%w: selector repair returned a stale catalog hash", ErrInvalidLLMRule)
		validations = append(validations, failedDSLAttemptValidation("selector-repair-patch", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, providerIR, nil, validations, err)
		return
	}
	derived, err := applySelectorRepairPatch(
		request.ProviderIR, request.Slots, request.AllowedAssignments, response,
	)
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("selector-repair-patch", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, providerIR, nil, validations, err)
		return
	}
	derivedHash := dslSemanticJSONHash(derived)
	recorder.selectorRepair.DerivedProviderIRHash = derivedHash
	recorder.selectorRepair.DerivedProviderIR, _ = copyRule(derived)
	validations = append(validations, DSLAttemptValidation{Phase: "selector-repair-patch", Status: "passed"})
	restoreOmittedWorkflowMetadata(derived, request.TrustedBaseline)

	var safetyFlags []string
	resolution, err := catalog.ResolveHistoricalProviderCandidates(derived, response.SelectorCatalogHash)
	if err != nil {
		safetyFlags = append(safetyFlags, "selector-candidates:failed")
		validations = append(validations, failedDSLAttemptValidation(
			"selector-candidate-resolution", err, safetyFlags...,
		))
	} else {
		safetyFlags = append(safetyFlags, "selector-candidates:resolved",
			fmt.Sprintf("selector-candidates:targets=%d", resolution.Targets),
			fmt.Sprintf("selector-candidates:fields=%d", resolution.Fields),
			fmt.Sprintf("provider-targets:resolved=%d", resolution.OrdinaryTargets))
		validations = append(validations, DSLAttemptValidation{
			Phase: "selector-candidate-resolution", Status: "passed",
			SafetyFlags: append([]string(nil), safetyFlags...),
		})
	}
	if err == nil {
		var flags []string
		flags, err = platformrule.ValidateProvisionalRule(derived, request.TrustedBaseline, requirement.Requirement)
		safetyFlags = append(safetyFlags, flags...)
		if err != nil {
			validations = append(validations, failedDSLAttemptValidation("schema-and-contract-validation", err, flags...))
		} else {
			validations = append(validations, DSLAttemptValidation{
				Phase: "schema-and-contract-validation", Status: "passed",
				SafetyFlags: append([]string(nil), flags...),
			})
		}
	}
	if err == nil {
		ruleMap, marshalErr := ruleAsMap(derived)
		if marshalErr != nil {
			err = marshalErr
		} else if scannerFlags := llm.ScanSafety(ruleMap); len(scannerFlags) > 0 {
			for _, flag := range scannerFlags {
				safetyFlags = append(safetyFlags, fmt.Sprintf("security-scan:%s:%s", flag.StepIndex, flag.Action))
			}
			err = ErrUnsafeGeneratedRule
		}
		if err != nil {
			validations = append(validations, failedDSLAttemptValidation("security-scan", err, safetyFlags...))
		} else {
			safetyFlags = append(safetyFlags, "security-scan:passed")
			validations = append(validations, DSLAttemptValidation{Phase: "security-scan", Status: "passed"})
		}
	}
	var resolvedRule *models.Rule
	if err == nil {
		evidenceReport, evidenceErr := platformrule.ValidateAndStabilizeGeneratedExtractionSelectors(derived, catalog)
		err = evidenceErr
		if err != nil {
			safetyFlags = append(safetyFlags, "selector-evidence:failed")
			validations = append(validations, failedDSLAttemptValidation("selector-evidence", err, "selector-evidence:failed"))
		} else {
			safetyFlags = append(safetyFlags, "selector-evidence:passed",
				fmt.Sprintf("selector-evidence:checked=%d", evidenceReport.Checked),
				fmt.Sprintf("selector-evidence:canonicalized=%d", evidenceReport.Canonicalized))
			validations = append(validations, DSLAttemptValidation{
				Phase: "selector-evidence", Status: "passed",
				SafetyFlags: []string{"selector-evidence:passed"},
			})
			resolvedRule, _ = copyRule(derived)
		}
	}
	if err != nil {
		job.SafetyFlags = safetyFlags
		m.finishFailedSelectorRepair(ctx, job, recorder, providerIR, resolvedRule, validations, err)
		return
	}
	yaml, err := RuleToYAML(derived)
	if err != nil {
		validations = append(validations, failedDSLAttemptValidation("serialization", err))
		m.finishFailedSelectorRepair(ctx, job, recorder, providerIR, nil, validations, err)
		return
	}
	validations = append(validations, DSLAttemptValidation{Phase: "serialization", Status: "passed"})
	job.ChunkCount, job.CompletedChunks = 1, 1
	// WI-7: scan for sealing-only concerns on the derived rule. These do NOT
	// fail the repair but become gating flags at ApproveDSLWorkflow.
	// WI-15: ContentFilterRule adds step-attributed content-filter:* flags.
	if ruleMap, marshalErr := ruleAsMap(derived); marshalErr == nil {
		safetyFlags = append(safetyFlags, llm.ScanSealingFlags(ruleMap)...)
		for _, cf := range llm.ContentFilterRule(ruleMap) {
			safetyFlags = append(safetyFlags, fmt.Sprintf("%s:%s:%s", cf.Action, cf.StepIndex, cf.Reason))
		}
	}
	job.SafetyFlags = append(safetyFlags, "selector-repair:one-shot", "degraded:false")
	attemptReport, attemptArtifact, reportErr := recorder.buildReport(providerIR, resolvedRule, validations, nil)
	if reportErr != nil {
		m.finishFailedSelectorRepair(ctx, job, recorder, providerIR, nil, validations, reportErr)
		return
	}
	result := selectorRepairResult{
		Rule: derived, SourceAttemptReportID: request.SourceAttemptReportID,
		SourceProviderIRHash:  request.SourceProviderIRHash,
		DerivedProviderIRHash: derivedHash, RepairPlanHash: request.RepairPlanHash,
		Replacements: response.Replacements, FinalResponse: audit,
	}
	if err := m.store.CompleteDSLJobWithAttemptReport(
		ctx, job, derived, yaml, result, attemptReport, attemptArtifact,
	); err != nil {
		m.logger.Error("complete selector repair job failed", zap.String("jobId", job.ID), zap.Error(err))
	}
}

func (m *Manager) validateSelectorRepairLineage(
	ctx context.Context,
	job *models.DSLJob,
	workflow *models.DSLWorkflow,
	requirement *models.CollectionRequirement,
	recording *models.Recording,
	request SelectorRepairJobRequest,
) error {
	if job.SourceAttemptReportID == "" ||
		job.SourceAttemptReportID != request.SourceAttemptReportID ||
		request.SourceJobID == "" || request.SourceAttemptNumber <= 0 ||
		request.SourceProviderCallID == "" || request.SourceProviderResponseHash == "" ||
		request.ProviderIR == nil || request.TrustedBaseline == nil ||
		request.SourceProviderIRHash == "" || request.RepairPlanHash == "" ||
		request.Failure == nil {
		return fmt.Errorf("%w: selector repair source binding is incomplete", ErrInvalidWorkflowInput)
	}
	if recording.ContentHash != request.RecordingHash ||
		requirement.ContentHash != request.RequirementHash ||
		dslJSONHash(request.TrustedBaseline) != request.BaselineHash ||
		dslSemanticJSONHash(request.ProviderIR) != request.SourceProviderIRHash ||
		selectorRepairPlanHash(request) != request.RepairPlanHash {
		return fmt.Errorf("%w: selector repair source hashes do not match", ErrInvalidWorkflowInput)
	}
	sourceNonce := request.SourceAttemptReportID + "\x00" + request.SourceProviderIRHash
	catalog, err := platformrule.BuildSelectorEvidenceCatalog(recording.Payload)
	if err != nil {
		return err
	}
	rebuiltPrompt, _, err := catalog.ReconstructProviderPrompt(
		request.SelectorCatalog,
		request.TrustedBaseline,
	)
	if err != nil || rebuiltPrompt != request.SelectorCatalog ||
		catalog.PreparedProviderPromptCatalogHash() != request.SelectorCatalogHash {
		return fmt.Errorf("%w: selector repair catalog binding does not match", ErrInvalidWorkflowInput)
	}
	plan, err := catalog.BuildSelectorRepairPlan(request.ProviderIR, request.Failure, sourceNonce)
	if err != nil || !reflect.DeepEqual(plan.Slots, request.Slots) ||
		!reflect.DeepEqual(plan.AllowedAssignments, request.AllowedAssignments) {
		return fmt.Errorf("%w: selector repair slot mapping does not match", ErrInvalidWorkflowInput)
	}
	sourceReport, err := m.store.GetLLMAttemptReport(ctx, request.SourceAttemptReportID)
	if err != nil {
		return err
	}
	if sourceReport.JobType != models.LLMJobTypeDSL ||
		sourceReport.JobID != request.SourceJobID ||
		sourceReport.AttemptNumber != request.SourceAttemptNumber ||
		sourceReport.RecordingID != workflow.RecordingID ||
		sourceReport.RecordingHash != request.RecordingHash ||
		sourceReport.RequirementHash != request.RequirementHash ||
		sourceReport.BaselineHash != request.BaselineHash ||
		sourceReport.SelectorCatalogHash != request.SelectorCatalogHash ||
		!sourceReport.Replayable || sourceReport.Outcome != models.LLMAttemptFailed {
		return fmt.Errorf("%w: source attempt report lineage does not match", ErrInvalidWorkflowInput)
	}
	encodedArtifact, err := json.Marshal(sourceReport.Artifact)
	if err != nil {
		return err
	}
	var sourceArtifact DSLAttemptArtifact
	if err := json.Unmarshal(encodedArtifact, &sourceArtifact); err != nil {
		return fmt.Errorf("%w: source attempt artifact is invalid", ErrInvalidWorkflowInput)
	}
	if sourceArtifact.ProviderIRSourceCallID != request.SourceProviderCallID ||
		dslSemanticJSONHash(sourceArtifact.ProviderIR) != request.SourceProviderIRHash {
		return fmt.Errorf("%w: source provider IR lineage does not match", ErrInvalidWorkflowInput)
	}
	sourceCall, err := m.store.GetLLMProviderCall(ctx, request.SourceProviderCallID)
	if err != nil {
		return err
	}
	if sourceCall.JobType != models.LLMJobTypeDSL ||
		sourceCall.JobID != request.SourceJobID ||
		sourceCall.AttemptNumber != request.SourceAttemptNumber ||
		sourceCall.ResponseHash != request.SourceProviderResponseHash {
		return fmt.Errorf("%w: source provider call lineage does not match", ErrInvalidWorkflowInput)
	}
	sourceCalls, err := m.store.ListLLMProviderCalls(
		ctx, models.LLMJobTypeDSL, request.SourceJobID, request.SourceAttemptNumber,
	)
	if err != nil {
		return err
	}
	if err := validateSelectorRepairTerminalSourceCall(sourceCall, sourceCalls); err != nil {
		return err
	}
	return nil
}

func validateSelectorRepairTerminalSourceCall(
	sourceCall *models.LLMProviderCall,
	sourceCalls []*models.LLMProviderCall,
) error {
	if sourceCall == nil || !sourceCall.Replayable ||
		(sourceCall.CallKind != models.LLMProviderCallKindResponse &&
			sourceCall.CallKind != models.LLMProviderCallKindCacheHit) ||
		(sourceCall.Phase != llm.CompletionPhaseFinal &&
			sourceCall.Phase != llm.CompletionPhaseSynthesis) {
		return fmt.Errorf("%w: source provider call lineage does not match", ErrInvalidWorkflowInput)
	}
	if len(sourceCalls) == 0 ||
		sourceCall.CallIndex != len(sourceCalls) ||
		sourceCalls[len(sourceCalls)-1] == nil ||
		sourceCalls[len(sourceCalls)-1].ID != sourceCall.ID {
		return fmt.Errorf("%w: source provider call is not terminal", ErrInvalidWorkflowInput)
	}
	return nil
}

func (m *Manager) finishFailedSelectorRepair(
	ctx context.Context,
	job *models.DSLJob,
	recorder *dslAttemptRecorder,
	providerIR any,
	resolvedRule *models.Rule,
	validations []DSLAttemptValidation,
	err error,
) {
	job.MaxAttempts = job.AttemptCount
	report, artifact, reportErr := recorder.buildReport(providerIR, resolvedRule, validations, err)
	if reportErr != nil {
		err = reportErr
	}
	m.failJobWithAttemptReport(ctx, job, err, report, artifact)
}

func workflowResultCacheHit(result *WorkflowResult) bool {
	if result == nil {
		return false
	}
	if result.FinalResponse.CacheHit {
		return true
	}
	for _, chunk := range result.ChunkLineage {
		if chunk.CacheHit {
			return true
		}
	}
	return false
}

func accumulateDSLJobMetadata(job *models.DSLJob, metadata WorkflowRunMetadata) {
	if job == nil {
		return
	}
	if metadata.Provider != "" {
		job.Provider = metadata.Provider
	}
	if metadata.Model != "" {
		job.Model = metadata.Model
	}
	job.InputTokens += metadata.InputTokens
	job.OutputTokens += metadata.OutputTokens
	if metadata.ChunkCount > 0 {
		job.ChunkCount = metadata.ChunkCount
	}
	job.PromptVersion = WorkflowPromptVersion
}

// combineWorkflowRunMetadata retains paid-provider attribution and usage when
// a partially completed manual generation falls back to a deterministic rule.
func combineWorkflowRunMetadata(first, second WorkflowRunMetadata) WorkflowRunMetadata {
	combined := second
	combined.InputTokens += first.InputTokens
	combined.OutputTokens += first.OutputTokens
	if first.Provider != "" {
		combined.Provider = first.Provider
	}
	if first.Model != "" {
		combined.Model = first.Model
	}
	if first.ChunkCount > combined.ChunkCount {
		combined.ChunkCount = first.ChunkCount
	}
	return combined
}

func (m *Manager) failJob(ctx context.Context, job *models.DSLJob, err error) {
	m.failJobWithAttemptReport(ctx, job, err, nil, nil)
}

func (m *Manager) failJobWithAttemptReport(
	ctx context.Context,
	job *models.DSLJob,
	err error,
	report *models.LLMAttemptReport,
	reportArtifact any,
) {
	terminal, code, message := classifyDSLJobError(err)
	if terminal {
		job.MaxAttempts = job.AttemptCount
	}
	delay := time.Duration(job.AttemptCount*job.AttemptCount) * time.Second
	var updateErr error
	if report != nil {
		updateErr = m.store.FailDSLJobWithAttemptReport(ctx, job, code, message, delay, report, reportArtifact)
	} else {
		updateErr = m.store.FailDSLJob(ctx, job, code, message, delay)
	}
	if updateErr != nil {
		m.logger.Error("record dsl workflow failure", zap.String("jobId", job.ID), zap.Error(updateErr))
	}
	fields := []zap.Field{
		zap.String("jobId", job.ID),
		zap.String("code", code),
	}
	if m.cfg.EnforcedLLMPolicy() != nil {
		fields = append(fields, llm.HashedAuditFields("error", code, err.Error())...)
	} else {
		fields = append(fields, zap.Error(err))
	}
	m.logger.Warn("dsl workflow job failed", fields...)
}

func classifyDSLJobError(err error) (bool, string, string) {
	if stableCode, ok := llm.StableDispatchErrorCode(err); ok {
		switch stableCode {
		case budget.CodeWorkflowBudgetExceeded:
			return true, stableCode, "workflow budget exhausted"
		case budget.CodeBudgetExceeded:
			return true, stableCode, "the configured LLM budget is exhausted"
		case budget.CodeLedgerUnavailable:
			return true, stableCode, "the LLM budget ledger is unavailable"
		case llm.CodeProviderUnavailable:
			return true, stableCode, "every configured LLM route is unavailable"
		default:
			return true, stableCode, "this LLM dispatch identity cannot be sent again"
		}
	}
	var completionCode llm.CompletionErrorCodeError
	if errors.As(err, &completionCode) &&
		strings.HasPrefix(completionCode.CompletionErrorCode(), "structured_output_") {
		message := "provider strict structured output did not match the generated DSL schema (" +
			completionCode.CompletionErrorCode() + ")"
		var feedbackError llm.CompletionValidationFeedbackError
		if errors.As(err, &feedbackError) {
			if feedback := strings.TrimSpace(feedbackError.CompletionValidationFeedback()); feedback != "" {
				message += ": " + feedback
			}
		}
		return false, "INVALID_DSL", safeMessage(message, maxDSLValidationFeedbackBytes)
	}
	switch {
	case errors.Is(err, llm.ErrCompletionCapture):
		return false, "ARTIFACT_CAPTURE_FAILED", "provider output could not be captured durably; the response was not used"
	case errors.Is(err, platformrule.ErrUnsafeProvisionalRule), errors.Is(err, ErrUnsafeGeneratedRule):
		return true, "UNSAFE_DSL", "generated rule failed the replay security policy"
	case errors.Is(err, ErrWorkflowSourceUnavailable):
		return true, "SOURCE_UNAVAILABLE", "the workflow source cannot fit the configured model context"
	case errors.Is(err, platformrule.ErrSelectorEvidenceUnavailable),
		errors.Is(err, platformrule.ErrSelectorCatalogUnavailable),
		errors.Is(err, platformrule.ErrSelectorSourceUnavailable):
		return true, "SOURCE_UNAVAILABLE", "selector evidence could not be derived from the recording"
	case errors.Is(err, platformrule.ErrInvalidProvisionalRule), errors.Is(err, ErrInvalidLLMRule):
		message := safeMessage(err.Error(), maxDSLValidationFeedbackBytes)
		if message == "" {
			message = "generated rule failed schema validation"
		}
		return false, "INVALID_DSL", message
	case errors.Is(err, ErrInvalidWorkflowInput):
		return true, "INVALID_DSL", "generated rule failed schema validation"
	case errors.Is(err, store.ErrRecordingDeleted), errors.Is(err, store.ErrRecordingNotFound), errors.Is(err, store.ErrRequirementNotFound):
		return true, "SOURCE_UNAVAILABLE", "the confirmed workflow source is unavailable"
	default:
		return false, "PROVIDER_UNAVAILABLE", "dsl generation is temporarily unavailable"
	}
}

// previousAttemptFeedback returns the server-recorded failure of the previous
// attempt so a retry prompt can correct it. The first attempt has no feedback.
// The reclaimed job retains the previous error message in memory even though
// the claim cleared it in the database.
func previousAttemptFeedback(attemptCount int, errorMessage string) string {
	if attemptCount > 1 {
		return errorMessage
	}
	return ""
}

func decodeDSLRequest(value any, output any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("decode dsl job request: %w", err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("decode dsl job request: %w", err)
	}
	return nil
}

func copyRule(value *models.Rule) (*models.Rule, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var copied models.Rule
	err = json.Unmarshal(encoded, &copied)
	return &copied, err
}

func ruleAsMap(value *models.Rule) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(encoded, &result)
	return result, err
}

func enforceJSONSize(value any, limit int, label string) error {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %s is not valid JSON", ErrInvalidWorkflowInput, label)
	}
	if len(encoded) > limit {
		return fmt.Errorf("%w: %s is %d bytes, limit %d", ErrReplayPayloadTooLarge, label, len(encoded), limit)
	}
	return nil
}

func sanitizeReplayDiagnostics(value any) any {
	if value == nil {
		return nil
	}
	// redact.Recording not required: input is replay diagnostics, not page content; redact.Any is the right boundary here.
	safe := redact.Any(value)
	encoded, err := json.Marshal(safe)
	if err != nil {
		return map[string]any{"truncated": true, "message": "replay diagnostics could not be encoded"}
	}
	if len(encoded) <= maxReplayDiagnosticsBytes {
		return safe
	}

	summary := map[string]any{
		"truncated":     true,
		"originalBytes": len(encoded),
		"truncationReason": fmt.Sprintf(
			"diagnostics exceeded the %d-byte persisted and repair-context limit",
			maxReplayDiagnosticsBytes,
		),
	}
	if object, ok := safe.(map[string]any); ok {
		copyReplayDiagnosticFields(summary, object)
		if logs, ok := object["logs"].([]any); ok {
			start := 0
			if len(logs) > maxReplayDiagnosticLogs {
				start = len(logs) - maxReplayDiagnosticLogs
				summary["omittedLogCount"] = start
			}
			boundedLogs := make([]any, 0, len(logs)-start)
			for _, log := range logs[start:] {
				boundedLogs = append(boundedLogs, summarizeReplayDiagnosticLog(log))
			}
			summary["logs"] = boundedLogs
		}
	} else {
		summary["details"] = safeMessage(string(encoded), 2000)
	}

	if summaryBytes, marshalErr := json.Marshal(summary); marshalErr == nil && len(summaryBytes) <= maxReplayDiagnosticsBytes {
		return summary
	}
	// Defensive final fallback: pathological control characters can expand
	// during JSON encoding. This shape is deliberately tiny and deterministic.
	return map[string]any{
		"truncated":     true,
		"originalBytes": len(encoded),
		"message":       "replay diagnostics exceeded the persisted and repair-context limit",
	}
}

func copyReplayDiagnosticFields(destination, source map[string]any) {
	for _, key := range []string{"terminalStatus", "message", "errorCode", "errorMessage", "outputSchemaError"} {
		if text, ok := source[key].(string); ok {
			destination[key] = safeMessage(text, 2000)
		}
	}
	if outputValid, ok := source["outputValid"].(bool); ok {
		destination["outputValid"] = outputValid
	}
}

func summarizeReplayDiagnosticLog(value any) any {
	log, ok := value.(map[string]any)
	if !ok {
		encoded, _ := json.Marshal(value)
		return map[string]any{"message": safeMessage(string(encoded), 1000), "truncated": true}
	}
	summary := map[string]any{}
	for _, key := range []string{"type", "level", "status", "message"} {
		if text, ok := log[key].(string); ok {
			summary[key] = safeMessage(text, 1000)
		}
	}
	if extra, ok := log["extra"].(map[string]any); ok {
		extraSummary := map[string]any{}
		for _, key := range []string{"error", "type", "errorType", "stepId", "attempt", "delay"} {
			switch item := extra[key].(type) {
			case string:
				extraSummary[key] = safeMessage(item, 1000)
			case float64, bool:
				extraSummary[key] = item
			}
		}
		if len(extraSummary) > 0 {
			summary["extra"] = extraSummary
		}
		if len(extraSummary) < len(extra) {
			summary["extraTruncated"] = true
		}
	}
	return summary
}

func sanitizeReplayArtifacts(artifacts []models.ReplayArtifact) []models.ReplayArtifact {
	result := make([]models.ReplayArtifact, len(artifacts))
	for index, artifact := range artifacts {
		result[index] = models.ReplayArtifact{
			Name: safeMessage(artifact.Name, 200), Type: safeMessage(artifact.Type, 100),
			Data: safeMessageValue(artifact.Data),
		}
	}
	return result
}

// repairArtifactContext exposes a small, already-redacted window of current
// failure evidence to the repair model. Keep both ends because semantic DOM
// snapshots often place the relevant application content after global chrome.
func repairArtifactContext(artifacts []models.ReplayArtifact) []models.ReplayArtifact {
	if len(artifacts) == 0 {
		return nil
	}
	start := 0
	if len(artifacts) > maxRepairArtifactContextItems {
		start = len(artifacts) - maxRepairArtifactContextItems
	}
	selected := artifacts[start:]
	perItem := maxRepairArtifactContextBytes/len(selected) - 512
	if perItem < 1024 {
		perItem = 1024
	}
	result := make([]models.ReplayArtifact, 0, len(selected))
	for _, artifact := range selected {
		result = append(result, models.ReplayArtifact{
			Name: artifact.Name,
			Type: artifact.Type,
			Data: boundedArtifactWindow(artifact.Data, perItem),
		})
	}
	encoded, err := json.Marshal(result)
	if err == nil && len(encoded) <= maxRepairArtifactContextBytes {
		return result
	}
	// Deterministic metadata-only fallback for pathological escaping expansion.
	metadata := make([]models.ReplayArtifact, 0, len(selected))
	for _, artifact := range selected {
		metadata = append(metadata, models.ReplayArtifact{
			Name: artifact.Name,
			Type: artifact.Type,
			Data: fmt.Sprintf("artifact omitted from repair context after exceeding %d bytes", maxRepairArtifactContextBytes),
		})
	}
	return metadata
}

func boundedArtifactWindow(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	const marker = "\n...[middle omitted to preserve repair-context budget]...\n"
	available := limit - len(marker)
	if available <= 0 {
		return marker[:limit]
	}
	head := available / 2
	tail := available - head
	return strings.ToValidUTF8(value[:head], "") + marker + strings.ToValidUTF8(value[len(value)-tail:], "")
}

func safeMessageValue(value string) string {
	if safe, ok := redact.Any(value).(string); ok {
		return safe
	}
	return ""
}

func recordingPayloadWithRequirementMarks(payload map[string]any, marks []models.PageMark) map[string]any {
	if len(marks) == 0 {
		copy := make(map[string]any, len(payload)+1)
		for key, value := range payload {
			copy[key] = value
		}
		copy["marks"] = []models.PageMark{}
		return copy
	}
	copy := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		copy[key] = value
	}
	copy["marks"] = marks
	return copy
}

func mergeDiagnostics(current any, additions map[string]any) map[string]any {
	result := map[string]any{}
	if existing, ok := current.(map[string]any); ok {
		for key, value := range existing {
			result[key] = value
		}
	} else if current != nil {
		result["details"] = current
	}
	for key, value := range additions {
		result[key] = value
	}
	return result
}

func safeCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) > 64 {
		value = value[:64]
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return "REPLAY_FAILED"
		}
	}
	return value
}

func safeMessage(value string, max int) string {
	value = safeMessageValue(strings.TrimSpace(value))
	if len(value) > max {
		value = value[:max]
	}
	return value
}
