package requirement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type NormalizationInput struct {
	Requirement    *models.CollectionRequirementSpec `json:"requirement,omitempty"`
	CustomText     string                            `json:"customText,omitempty"`
	CandidateJobID string                            `json:"candidateJobId,omitempty"`
	CandidateID    string                            `json:"candidateId,omitempty"`
}

type Manager struct {
	store     *store.Store
	cfg       *config.Config
	recording *platformrecording.Service
	workflow  *Workflow
	logger    *zap.Logger
}

func NewManager(s *store.Store, cfg *config.Config, workflow *Workflow, logger *zap.Logger) *Manager {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Manager{store: s, cfg: cfg, recording: platformrecording.NewService(s, cfg), workflow: workflow, logger: logger}
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

func (m *Manager) SubmitCandidates(ctx context.Context, recordingID string) (*models.RequirementJob, error) {
	job := &models.RequirementJob{
		ID: store.NewID(), RecordingID: recordingID, Kind: models.RequirementJobCandidates,
		Status: models.RequirementJobPending, Source: models.RequirementSourceLLM,
		PromptVersion: PromptVersion, MaxAttempts: m.cfg.RequirementMaxAttempts(),
	}
	if err := m.store.CreateRequirementJob(ctx, job, map[string]any{"kind": models.RequirementJobCandidates}); err != nil {
		return nil, err
	}
	return job, nil
}

func (m *Manager) SubmitNormalization(ctx context.Context, recordingID string, input NormalizationInput) (*models.RequirementJob, error) {
	structured := input.Requirement != nil
	custom := strings.TrimSpace(input.CustomText) != ""
	if structured == custom {
		return nil, fmt.Errorf("%w: provide exactly one structured requirement or custom text", ErrInvalidRequirement)
	}
	source := models.RequirementSourceLLM
	if structured {
		if err := ValidateSpec(*input.Requirement); err != nil {
			return nil, err
		}
		source = models.RequirementSourceManual
		if input.CandidateJobID != "" || input.CandidateID != "" {
			if err := m.validateCandidateLineage(ctx, input.CandidateJobID, input.CandidateID); err != nil {
				return nil, err
			}
			source = models.RequirementSourceLLM
		}
	} else if input.CandidateJobID != "" || input.CandidateID != "" {
		return nil, fmt.Errorf("%w: custom text cannot reference a candidate", ErrInvalidRequirement)
	}
	job := &models.RequirementJob{
		ID: store.NewID(), RecordingID: recordingID, Kind: models.RequirementJobNormalize,
		Status: models.RequirementJobPending, Source: source,
		PromptVersion: PromptVersion, MaxAttempts: m.cfg.RequirementMaxAttempts(),
	}
	if err := m.store.CreateRequirementJob(ctx, job, input); err != nil {
		return nil, err
	}
	return job, nil
}

func (m *Manager) validateCandidateLineage(ctx context.Context, jobID, candidateID string) error {
	if jobID == "" || candidateID == "" {
		return fmt.Errorf("%w: candidate job and candidate id must be provided together", ErrInvalidRequirement)
	}
	job, err := m.store.GetRequirementJob(ctx, jobID)
	if err != nil || job.Kind != models.RequirementJobCandidates || job.Status != models.RequirementJobCompleted || job.Source != models.RequirementSourceLLM {
		return fmt.Errorf("%w: candidate lineage is unavailable", ErrInvalidRequirement)
	}
	data, err := json.Marshal(job.Result)
	if err != nil {
		return fmt.Errorf("%w: candidate lineage is invalid", ErrInvalidRequirement)
	}
	var result CandidateResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("%w: candidate lineage is invalid", ErrInvalidRequirement)
	}
	for _, candidate := range result.Candidates {
		if candidate.ID == candidateID {
			return nil
		}
	}
	return fmt.Errorf("%w: candidate does not belong to the referenced job", ErrInvalidRequirement)
}

func (m *Manager) GetJob(ctx context.Context, id string) (*models.RequirementJob, error) {
	return m.store.GetRequirementJob(ctx, id)
}

func (m *Manager) ListProviderAttempts(ctx context.Context, jobID string) ([]*models.LLMAttemptReport, error) {
	if _, err := m.store.GetRequirementJob(ctx, jobID); err != nil {
		return nil, err
	}
	return m.store.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, jobID)
}

func (m *Manager) GetProviderAttempt(ctx context.Context, jobID string, attemptNumber int) (*RequirementAttemptDetail, error) {
	if _, err := m.store.GetRequirementJob(ctx, jobID); err != nil {
		return nil, err
	}
	reports, err := m.store.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, jobID)
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
	calls, err := m.store.ListLLMProviderCalls(ctx, models.LLMJobTypeRequirement, jobID, attemptNumber)
	if err != nil {
		return nil, err
	}
	return &RequirementAttemptDetail{Report: report, Calls: calls, Artifact: report.Artifact}, nil
}

func (m *Manager) GetProviderCall(ctx context.Context, jobID string, attemptNumber int, callID string) (*models.LLMProviderCall, error) {
	if _, err := m.store.GetRequirementJob(ctx, jobID); err != nil {
		return nil, err
	}
	calls, err := m.store.ListLLMProviderCalls(ctx, models.LLMJobTypeRequirement, jobID, attemptNumber)
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

func (m *Manager) RetryJob(ctx context.Context, id string) error {
	return m.store.RetryRequirementJob(ctx, id)
}

func (m *Manager) GetRequirement(ctx context.Context, id string) (*models.CollectionRequirement, error) {
	return m.store.GetCollectionRequirement(ctx, id)
}

func (m *Manager) ConfirmRequirement(ctx context.Context, id string) (*models.CollectionRequirement, error) {
	return m.store.ConfirmCollectionRequirement(ctx, id)
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
	for i := 0; i < batch; i++ {
		job, err := m.store.ClaimPendingRequirementJob(ctx, lease)
		if err != nil {
			if !errors.Is(err, store.ErrNoTaskAvailable) {
				m.logger.Error("claim requirement job failed", zap.Error(err))
			}
			return
		}
		jobCtx := authz.WithPrincipal(ctx, authz.Principal{
			Subject: "requirement-worker", WorkspaceID: job.WorkspaceID,
			Roles: []authz.Role{authz.RoleAdmin}, Kind: authz.PrincipalSystem,
		})
		m.runJob(jobCtx, job)
	}
}

func (m *Manager) runJob(ctx context.Context, job *models.RequirementJob) {
	m.logger.Info("processing collection requirement job", zap.String("jobId", job.ID), zap.String("kind", string(job.Kind)), zap.Int("attempt", job.AttemptCount))
	// Bind the durable dispatch identity so every physical call this attempt
	// makes is admitted against the hard budget under the workspace persisted
	// with the job.
	ctx = llm.WithDispatchOperation(ctx, llm.DispatchOperation{
		Kind:           budget.OperationRequirement,
		ID:             job.ID,
		LogicalAttempt: llm.DispatchAttempt(job.AttemptCount),
		WorkspaceID:    job.WorkspaceID,
	})
	onProgress := func(completed, total int) {
		if updateErr := m.store.UpdateRequirementJobProgress(ctx, job.ID, job.AttemptCount, total, completed); updateErr != nil {
			m.logger.Warn("record requirement job progress failed", zap.String("jobId", job.ID), zap.Error(updateErr))
		}
	}
	var result any
	var metadata RunMetadata
	var recorder *requirementAttemptRecorder
	var attemptReport *models.LLMAttemptReport
	var attemptArtifact any
	var normalizedSpec *models.CollectionRequirementSpec
	var err error

	switch job.Kind {
	case models.RequirementJobCandidates:
		var recording *models.Recording
		recording, err = m.recording.Get(ctx, job.RecordingID)
		if err == nil {
			recorder = newRequirementAttemptRecorder(
				m.store, job, recording.ContentHash, m.policyFingerprint(),
			)
			workflowCtx := llm.WithCompletionTraceSink(ctx, recorder)
			var candidateResult *CandidateResult
			candidateResult, metadata, err = m.workflow.GenerateCandidates(workflowCtx, recording.Payload, onProgress, previousAttemptFeedback(job.AttemptCount, job.ErrorMessage))
			result = candidateResult
		}
	case models.RequirementJobNormalize:
		var input NormalizationInput
		input, err = decodeNormalizationInput(job.Request)
		if err == nil && input.Requirement != nil {
			err = ValidateSpec(*input.Requirement)
			if err == nil {
				provider := "manual"
				if job.Source == models.RequirementSourceLLM {
					provider = "deterministic"
				}
				metadata.Provider = provider
				metadata.Model = provider
				result = &NormalizationResult{
					Requirement: *input.Requirement, ChunkLineage: []ChunkLineage{},
					FinalResponse: CompletionAudit{Provider: provider, Model: provider},
				}
			}
		} else if err == nil {
			var recording *models.Recording
			recording, err = m.recording.Get(ctx, job.RecordingID)
			if err == nil {
				recorder = newRequirementAttemptRecorder(
					m.store, job, recording.ContentHash, m.policyFingerprint(),
				)
				workflowCtx := llm.WithCompletionTraceSink(ctx, recorder)
				var normalized *NormalizationResult
				normalized, metadata, err = m.workflow.NormalizeCustom(workflowCtx, recording.Payload, input.CustomText, onProgress, previousAttemptFeedback(job.AttemptCount, job.ErrorMessage))
				result = normalized
			}
		}
		if err == nil {
			normalized := result.(*NormalizationResult)
			normalizedSpec = &normalized.Requirement
		}
	default:
		err = fmt.Errorf("unknown requirement job kind %q", job.Kind)
	}

	if recorder != nil {
		attemptReport, attemptArtifact, err = recorder.buildReport(result, err)
	}

	var requirementDraft *models.CollectionRequirement
	if err == nil && normalizedSpec != nil {
		existing, lookupErr := m.store.GetCollectionRequirementByJob(ctx, job.ID)
		switch {
		case lookupErr == nil:
			same, compareErr := sameRequirementSpec(existing.Requirement, *normalizedSpec)
			if compareErr != nil {
				err = fmt.Errorf("compare existing normalized requirement: %w", compareErr)
			} else if existing.RecordingID != job.RecordingID ||
				existing.Source != job.Source ||
				!same {
				err = ErrRequirementLineageConflict
			} else {
				// Older workers persisted the immutable draft before completing
				// the job. Reuse that exact content so lease recovery can
				// atomically add the report and terminal result without a
				// duplicate draft.
				job.RequirementID = existing.ID
			}
		case errors.Is(lookupErr, store.ErrRequirementNotFound):
			requirementDraft = &models.CollectionRequirement{
				ID:          store.NewID(),
				RecordingID: job.RecordingID,
				SourceJobID: job.ID,
				Source:      job.Source,
				Status:      models.CollectionRequirementDraft,
				Requirement: *normalizedSpec,
				Owner:       authz.Subject(ctx, "system"),
			}
			job.RequirementID = requirementDraft.ID
		default:
			err = fmt.Errorf("look up normalized requirement lineage: %w", lookupErr)
		}
		if err != nil && recorder != nil {
			attemptReport, attemptArtifact, err = recorder.buildReport(result, err)
		}
	}

	if err != nil {
		m.failJobWithAttemptReport(ctx, job, err, attemptReport, attemptArtifact)
		return
	}

	job.Provider = metadata.Provider
	job.Model = metadata.Model
	job.InputTokens = metadata.InputTokens
	job.OutputTokens = metadata.OutputTokens
	job.ChunkCount = metadata.ChunkCount
	job.CompletedChunks = metadata.ChunkCount
	job.PromptVersion = PromptVersion
	if job.Source == models.RequirementSourceManual {
		job.Provider = "manual"
		job.Model = "manual"
	}
	cacheFlag := "cache:miss"
	if requirementResultCacheHit(result) {
		cacheFlag = "cache:hit"
	}
	job.SafetyFlags = []string{"schema-validation:passed", "sensitive-data-scan:passed", cacheFlag, "degraded:false"}
	if recorder != nil {
		job.SafetyFlags = append(job.SafetyFlags, "artifact-capture:passed")
	}
	var completeErr error
	switch {
	case requirementDraft != nil && attemptReport != nil:
		completeErr = m.store.CompleteRequirementJobWithAttemptReportAndDraft(
			ctx,
			job,
			result,
			requirementDraft,
			attemptReport,
			attemptArtifact,
		)
	case requirementDraft != nil:
		completeErr = m.store.CompleteRequirementJobWithDraft(
			ctx,
			job,
			result,
			requirementDraft,
		)
	case attemptReport != nil:
		completeErr = m.store.CompleteRequirementJobWithAttemptReport(
			ctx,
			job,
			result,
			attemptReport,
			attemptArtifact,
		)
	default:
		completeErr = m.store.CompleteRequirementJob(ctx, job, result)
	}
	if completeErr != nil {
		m.logger.Error("complete collection requirement job failed", zap.String("jobId", job.ID), zap.Error(completeErr))
	}
}

func (m *Manager) failJobWithAttemptReport(
	ctx context.Context,
	job *models.RequirementJob,
	err error,
	attemptReport *models.LLMAttemptReport,
	attemptArtifact any,
) {
	terminal, code, message := classifyJobError(err)
	if terminal {
		// Preserve the actual immutable attempt number while forcing this
		// transition terminal in the store's bounded-retry calculation.
		job.MaxAttempts = job.AttemptCount
	}
	delay := time.Duration(job.AttemptCount*job.AttemptCount) * time.Second
	var updateErr error
	if attemptReport != nil {
		updateErr = m.store.FailRequirementJobWithAttemptReport(
			ctx,
			job,
			code,
			message,
			delay,
			attemptReport,
			attemptArtifact,
		)
	} else {
		updateErr = m.store.FailRequirementJob(ctx, job, code, message, delay)
	}
	if updateErr != nil {
		m.logger.Error("record requirement job failure", zap.String("jobId", job.ID), zap.Error(updateErr))
	}
	fields := []zap.Field{
		zap.String("jobId", job.ID),
		zap.String("code", code),
	}
	if m.cfg.EnforcedLLMPolicy() != nil {
		fields = append(fields, llm.HashedAuditFields("error", code, err.Error())...)
	} else {
		fields = append(fields, zap.String("error", llm.SanitizeCompletionError(err)))
	}
	m.logger.Warn("collection requirement job failed", fields...)
}

func sameRequirementSpec(left, right models.CollectionRequirementSpec) (bool, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func requirementResultCacheHit(result any) bool {
	switch value := result.(type) {
	case *CandidateResult:
		if value == nil {
			return false
		}
		if value.FinalResponse.CacheHit {
			return true
		}
		for _, chunk := range value.ChunkLineage {
			if chunk.CacheHit {
				return true
			}
		}
	case *NormalizationResult:
		if value == nil {
			return false
		}
		if value.FinalResponse.CacheHit {
			return true
		}
		for _, chunk := range value.ChunkLineage {
			if chunk.CacheHit {
				return true
			}
		}
	}
	return false
}

func decodeNormalizationInput(value any) (NormalizationInput, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return NormalizationInput{}, err
	}
	var input NormalizationInput
	if err := json.Unmarshal(data, &input); err != nil {
		return NormalizationInput{}, err
	}
	structured := input.Requirement != nil
	custom := strings.TrimSpace(input.CustomText) != ""
	if structured == custom {
		return NormalizationInput{}, fmt.Errorf("%w: invalid normalization request", ErrInvalidRequirement)
	}
	if !structured && (input.CandidateJobID != "" || input.CandidateID != "") {
		return NormalizationInput{}, fmt.Errorf("%w: custom request cannot reference a candidate", ErrInvalidRequirement)
	}
	return input, nil
}

func classifyJobError(err error) (terminal bool, code, message string) {
	if stableCode, ok := llm.StableDispatchErrorCode(err); ok {
		switch stableCode {
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
	switch {
	case errors.Is(err, llm.ErrCompletionCapture):
		return false, "ARTIFACT_CAPTURE_FAILED", "provider output could not be captured durably; the response was not used"
	case errors.Is(err, ErrInvalidProviderOutput):
		return false, "PROVIDER_OUTPUT_INVALID", "the provider returned an invalid or unsafe structured requirement; retrying with bounded backoff"
	case errors.Is(err, ErrRequirementLineageConflict):
		return true, "REQUIREMENT_LINEAGE_CONFLICT", "an existing normalized requirement does not match this job result"
	case errors.Is(err, ErrInvalidRequirement):
		return true, "INVALID_REQUIREMENT", "the collection requirement is invalid"
	case errors.Is(err, ErrUnsafeRequirement):
		return true, "UNSAFE_REQUIREMENT", "the collection requirement contains a disallowed sensitive-data request"
	case errors.Is(err, store.ErrRecordingNotFound), errors.Is(err, store.ErrRecordingDeleted):
		return true, "RECORDING_UNAVAILABLE", "the source recording is unavailable"
	default:
		return false, "PROVIDER_UNAVAILABLE", "requirement generation is temporarily unavailable; retry later or enter a structured requirement manually"
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
