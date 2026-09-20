package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

// JobID is the unique identifier of an LLM enhancement job.
type JobID = string

// enhancementWorker is the unit that actually executes an enhancement request.
type enhancementWorker interface {
	Enhance(ctx context.Context, req EnhanceRequest) (*EnhanceResult, error)
}

// JobManager submits and polls LLM enhancement jobs.
type JobManager struct {
	store  *store.Store
	cfg    *config.Config
	worker enhancementWorker
	logger *zap.Logger
}

// NewJobManager creates a new job manager that uses the given store and enhancer.
func NewJobManager(s *store.Store, cfg *config.Config, orch *Orchestrator, logger *zap.Logger) *JobManager {
	return &JobManager{
		store:  s,
		cfg:    cfg,
		worker: NewEnhancer(cfg, orch, logger),
		logger: logger,
	}
}

// Submit creates a pending LLM enhancement job and returns its id.
func (m *JobManager) Submit(ctx context.Context, req EnhanceRequest) (JobID, error) {
	baselineJSON, err := json.Marshal(req.BaselineRule)
	if err != nil {
		return "", err
	}
	if _, err := json.Marshal(req.Recording); err != nil {
		return "", err
	}
	sanitizedRecording, _ := platformrecording.Sanitize(req.Recording)
	recordingJSON, err := json.Marshal(sanitizedRecording)
	if err != nil {
		return "", err
	}
	ruleID, _ := req.BaselineRule["id"].(string)
	if ruleID == "" {
		return "", errors.New("baselineRule.id is required")
	}
	job := &models.LLMJob{
		ID:        store.NewID(),
		RuleID:    ruleID,
		Baseline:  models.JSON(baselineJSON),
		Recording: models.JSON(recordingJSON),
		UserHint:  req.UserHint,
		Status:    string(models.LLMJobStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.CreateLLMJob(ctx, job); err != nil {
		return "", err
	}
	return job.ID, nil
}

// GetJob returns the current state of a job.
func (m *JobManager) GetJob(ctx context.Context, id JobID) (*models.LLMJob, error) {
	return m.store.GetLLMJob(ctx, id)
}

// SetMetrics assigns the LLM metrics collector to the job manager's worker.
func (m *JobManager) SetMetrics(metrics *Metrics) {
	if w, ok := m.worker.(*Enhancer); ok {
		w.SetMetrics(metrics)
	}
}

// StartWorker runs a background worker that claims and processes pending jobs
// until ctx is cancelled.
func (m *JobManager) StartWorker(ctx context.Context, interval time.Duration) {
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

// ProcessOnce claims and processes pending jobs until no more are available or
// the configured batch size is reached. It is exposed for tests that want
// synchronous control over job execution.
func (m *JobManager) ProcessOnce(ctx context.Context) {
	if m == nil || m.store == nil || m.cfg == nil || !m.cfg.LLMEnabled {
		return
	}
	batchSize := m.cfg.LLMJobBatchSize
	if batchSize <= 0 {
		batchSize = 10
	}
	for i := 0; i < batchSize; i++ {
		job, err := m.store.ClaimPendingLLMJob(ctx)
		if err != nil {
			if !errors.Is(err, store.ErrNoTaskAvailable) {
				m.logger.Error("claim pending llm job failed", zap.Error(err))
			}
			return
		}
		m.runJob(ctx, job)
	}
}

func (m *JobManager) runJob(ctx context.Context, job *models.LLMJob) {
	m.logger.Info("processing llm enhancement job",
		zap.String("jobId", job.ID),
		zap.String("ruleId", job.RuleID),
	)

	var baseline, recording map[string]any
	if err := json.Unmarshal(job.Baseline, &baseline); err != nil {
		baseline = map[string]any{}
	}
	if err := json.Unmarshal(job.Recording, &recording); err != nil {
		recording = map[string]any{}
	}

	// Bind the durable dispatch identity so every physical call this attempt
	// makes is admitted against the hard budget under the workspace persisted
	// with the job rather than a substituted default.
	ctx = WithDispatchOperation(ctx, DispatchOperation{
		Kind:           budget.OperationEnhance,
		ID:             job.ID,
		LogicalAttempt: DispatchAttempt(job.AttemptCount),
		WorkspaceID:    job.WorkspaceID,
	})

	res, err := m.worker.Enhance(ctx, EnhanceRequest{
		Recording:    recording,
		BaselineRule: baseline,
		UserHint:     job.UserHint,
	})

	now := time.Now().UTC()
	job.CompletedAt = &now

	if err != nil {
		if m.cfg.EnforcedLLMPolicy() != nil {
			// Enforced mode never converts a denied/unavailable paid operation
			// into a successful baseline artifact. Persist only a stable,
			// sanitized terminal code for this logical attempt.
			job.Status = string(models.LLMJobStatusFailed)
			if code, ok := StableDispatchErrorCode(err); ok {
				job.ResultError = code
			} else {
				job.ResultError = "LLM_ENHANCEMENT_FAILED"
			}
			if updateErr := m.store.UpdateLLMJobStatus(ctx, job); updateErr != nil {
				m.logger.Error("update failed llm job failed", zap.Error(updateErr))
			}
			return
		}
		// Hard failure: the worker itself returned an error. Fall back to the
		// baseline rule so the user can still accept/reject the unchanged rule.
		m.logger.Warn("llm enhancement worker failed, falling back to baseline",
			zap.String("jobId", job.ID),
			zap.String("ruleId", job.RuleID),
			zap.Error(err),
		)
		res = &EnhanceResult{
			Rule:        baseline,
			Patch:       map[string]any{},
			Provider:    "baseline",
			Model:       "baseline",
			Suggestions: []string{"LLM enhancement failed; baseline returned"},
			SafetyFlags: ScanSafety(baseline),
			Error:       err.Error(),
		}
	}
	job.Status = string(models.LLMJobStatusCompleted)

	ruleJSON, _ := json.Marshal(res.Rule)
	patchJSON, _ := json.Marshal(res.Patch)
	suggestionsJSON, _ := json.Marshal(res.Suggestions)
	safetyFlagsJSON, _ := json.Marshal(res.SafetyFlags)

	job.ResultRule = models.JSON(ruleJSON)
	job.ResultPatch = models.JSON(patchJSON)
	job.Provider = res.Provider
	job.Model = res.Model
	job.InputTokens = res.InputTokens
	job.OutputTokens = res.OutputTokens
	job.Suggestions = models.JSON(suggestionsJSON)
	job.SafetyFlags = models.JSON(safetyFlagsJSON)
	if res.Error != "" {
		job.ResultError = res.Error
	}

	m.checkTokenAlert(job)

	if updateErr := m.store.UpdateLLMJobStatus(ctx, job); updateErr != nil {
		m.logger.Error("update completed llm job failed", zap.Error(updateErr))
		return
	}

	if err := m.persistEnhancement(ctx, job, res, now); err != nil {
		m.logger.Error("persist enhancement for job failed", zap.Error(err))
	}
}

func (m *JobManager) checkTokenAlert(job *models.LLMJob) {
	threshold := m.cfg.LLMTokenAlertThreshold
	if threshold <= 0 {
		return
	}
	total := job.InputTokens + job.OutputTokens
	if total > threshold {
		m.logger.Warn("llm token usage exceeded alert threshold",
			zap.String("jobId", job.ID),
			zap.String("ruleId", job.RuleID),
			zap.Int("inputTokens", job.InputTokens),
			zap.Int("outputTokens", job.OutputTokens),
			zap.Int("totalTokens", total),
			zap.Int("threshold", threshold),
		)
	}
}

func (m *JobManager) persistEnhancement(ctx context.Context, job *models.LLMJob, res *EnhanceResult, now time.Time) error {
	var rule models.Rule
	if err := json.Unmarshal(job.ResultRule, &rule); err != nil {
		return err
	}
	rule.CreatedAt = now
	rule.UpdatedAt = now
	rule.ApprovalStatus = string(models.RuleApprovalPending)
	rule.Enabled = false

	enhancement := &models.RuleEnhancement{
		ID:           store.NewID(),
		RuleID:       rule.ID,
		Baseline:     job.Baseline,
		Enhanced:     job.ResultRule,
		Patch:        job.ResultPatch,
		UserHint:     job.UserHint,
		Provider:     job.Provider,
		Model:        job.Model,
		InputTokens:  job.InputTokens,
		OutputTokens: job.OutputTokens,
		Suggestions:  job.Suggestions,
		SafetyFlags:  job.SafetyFlags,
		Status:       string(models.EnhancementStatusPending),
		CreatedAt:    now,
	}

	return m.store.WithTx(ctx, func(tx *sql.Tx) error {
		workspace := authz.WorkspaceID(ctx)
		// Remove any previous pending rule/enhancement for the same rule so that
		// repeated enhancement jobs do not fail on primary-key or foreign-key
		// conflicts. Because rule_enhancements.rule_id has ON DELETE SET NULL,
		// delete the enhancement rows first, otherwise the rules DELETE would
		// orphan them and the second DELETE would miss them.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM rule_enhancements WHERE workspace_id = ? AND rule_id = ? AND status = ?`,
			workspace, rule.ID, string(models.EnhancementStatusPending)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM rules WHERE workspace_id = ? AND id = ? AND approval_status = ?`,
			workspace, rule.ID, string(models.RuleApprovalPending)); err != nil {
			return err
		}
		if err := m.store.CreateRuleTx(ctx, tx, &rule); err != nil {
			return err
		}
		return m.store.CreateRuleEnhancementTx(ctx, tx, enhancement)
	})
}
