package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/prompt"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	rulecontract "github.com/singhand-labs/AegisCrawler/internal/rule"
)

var (
	ErrDSLWorkflowNotFound = errors.New("dsl workflow not found")
	ErrDSLWorkflowState    = errors.New("dsl workflow cannot transition from its current state")
	ErrDSLJobNotFound      = errors.New("dsl job not found")
	ErrDSLJobState         = errors.New("dsl job cannot transition from its current state")
	ErrReplayNotFound      = errors.New("dsl replay attempt not found")
	ErrReplayState         = errors.New("dsl replay attempt cannot transition from its current state")
	ErrSafetyBlockingFlag  = errors.New("dsl workflow has blocking safety flags requiring override")
)

// SafetyBlockingFlagError carries the blocking flag list so the HTTP layer
// can serialize them in the 409 response body without parsing the error
// message string.
type SafetyBlockingFlagError struct {
	Flags []string
}

func (e *SafetyBlockingFlagError) Error() string {
	return fmt.Sprintf("%s: %s", ErrSafetyBlockingFlag, strings.Join(e.Flags, ","))
}

func (e *SafetyBlockingFlagError) Unwrap() error { return ErrSafetyBlockingFlag }

// ApproveDSLWorkflowOptions controls the blocking-flag override path in
// ApproveDSLWorkflow.
type ApproveDSLWorkflowOptions struct {
	OverrideSafety bool
	OverrideActor  string
}

type provisionalDSLArtifact struct {
	Rule *models.Rule `json:"rule"`
	YAML string       `json:"yaml"`
}

func (s *Store) validateCurrentDSLJobAttempt(ctx context.Context, job *models.DSLJob) error {
	if job == nil {
		return ErrDSLJobState
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `
		SELECT attempt_count FROM dsl_jobs
		WHERE id = ? AND workspace_id = ? AND workflow_id = ? AND status = ?
	`, job.ID, job.WorkspaceID, job.WorkflowID, models.DSLJobRunning).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDSLJobState
		}
		return err
	}
	if current != job.AttemptCount {
		return ErrDSLJobState
	}
	return nil
}

// DSLWorkflowOptions configures durable attempt and repair limits. Callers
// that omit it retain the historical three-attempt/three-repair defaults.
// BudgetUSDNanos sets the per-workflow spend envelope; zero keeps the
// historical 5_000_000-nanos column default (see CreateDSLWorkflowBudget).
type DSLWorkflowOptions struct {
	JobMaxAttempts int
	MaxRepairs     int
	BudgetUSDNanos int64
}

// DefaultDSLWorkflowBudgetNanos matches the migration-029 column default so
// options that do not carry an explicit budget keep historical behavior.
const DefaultDSLWorkflowBudgetNanos = 5_000_000

// CreateDSLWorkflowBudget resolves the envelope an INSERT should persist.
func CreateDSLWorkflowBudget(options []DSLWorkflowOptions) int64 {
	if len(options) > 0 && options[0].BudgetUSDNanos > 0 {
		return options[0].BudgetUSDNanos
	}
	return DefaultDSLWorkflowBudgetNanos
}

// CreateDSLWorkflow atomically persists the workflow and its initial durable
// generation job. The database trigger requires a confirmed requirement in
// the same workspace and recording lineage.
func (s *Store) CreateDSLWorkflow(ctx context.Context, workflow *models.DSLWorkflow, request any, options ...DSLWorkflowOptions) (*models.DSLJob, error) {
	if workflow == nil || workflow.RequirementID == "" || workflow.RecordingID == "" {
		return nil, ErrRequirementNotFound
	}
	if workflow.BrowserProfileID == "" {
		return nil, errors.New("browser profile id is required")
	}
	workflow.WorkspaceID = workspaceID(ctx)
	if workflow.ID == "" {
		workflow.ID = NewID()
	}
	workflow.Status = models.DSLWorkflowGenerating
	jobMaxAttempts := 3
	workflow.MaxRepairs = 3
	if len(options) > 0 {
		if options[0].JobMaxAttempts >= 1 && options[0].JobMaxAttempts <= 3 {
			jobMaxAttempts = options[0].JobMaxAttempts
		}
		if options[0].MaxRepairs >= 0 && options[0].MaxRepairs <= 3 {
			workflow.MaxRepairs = options[0].MaxRepairs
		}
	}
	if workflow.Owner == "" {
		workflow.Owner = authz.Subject(ctx, "system")
	}
	now := time.Now().UTC()
	if workflow.CreatedAt.IsZero() {
		workflow.CreatedAt = now
	}
	workflow.UpdatedAt = now

	budgetNanos := CreateDSLWorkflowBudget(options)
	job := &models.DSLJob{
		ID: NewID(), WorkspaceID: workflow.WorkspaceID, WorkflowID: workflow.ID,
		Kind: models.DSLJobGenerate, Status: models.DSLJobPending,
		PromptVersion: prompt.DSLWorkflowVersion, MaxAttempts: jobMaxAttempts, AvailableAt: now,
		CreatedAt: now, UpdatedAt: now, SafetyFlags: []string{},
	}
	requestArtifact, requestHash, err := s.sealRequirementArtifact(job.WorkspaceID, "dsl-job", job.ID, "request", request)
	if err != nil {
		return nil, err
	}
	job.RequestHash = requestHash
	workflow.CurrentJobID = job.ID

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dsl_workflows (
				id, workspace_id, requirement_id, recording_id, status,
				browser_profile_id, current_job_id, repair_count, max_repairs,
				last_replay_sequence, owner, budget_usd_nanos, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, 0, ?, ?, ?, ?)
		`, workflow.ID, workflow.WorkspaceID, workflow.RequirementID, workflow.RecordingID,
			workflow.Status, workflow.BrowserProfileID, workflow.CurrentJobID,
			workflow.MaxRepairs, workflow.Owner, budgetNanos, workflow.CreatedAt, workflow.UpdatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO dsl_jobs (
				id, workspace_id, workflow_id, kind, status, request_artifact,
				request_hash, prompt_version, max_attempts, available_at,
				safety_flags, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', ?, ?)
		`, job.ID, job.WorkspaceID, job.WorkflowID, job.Kind, job.Status,
			requestArtifact, job.RequestHash, job.PromptVersion, job.MaxAttempts,
			job.AvailableAt, job.CreatedAt, job.UpdatedAt)
		return err
	})
	if err != nil {
		if isConstraintMessage(err, "confirmed requirement not found") {
			return nil, ErrRequirementState
		}
		return nil, err
	}
	return job, nil
}

func isConstraintMessage(err error, fragment string) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), strings.ToLower(fragment))
}

// GetDSLWorkflow returns workspace-scoped state and decrypts the provisional
// rule only for its authenticated workspace.
func (s *Store) GetDSLWorkflow(ctx context.Context, id string) (*models.DSLWorkflow, error) {
	workflow := &models.DSLWorkflow{}
	var provisionalArtifact []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT w.id, w.workspace_id, w.requirement_id, w.recording_id, w.status,
		       w.browser_profile_id, COALESCE(w.current_job_id, ''), w.repair_count,
		       w.max_repairs, w.provisional_artifact, COALESCE(w.provisional_hash, ''),
		       w.last_replay_sequence, COALESCE(w.error_code, ''),
		       COALESCE(w.error_message, ''), w.owner, COALESCE(w.source_kind, ''),
		       COALESCE(w.source_authority, ''), COALESCE(w.source_artifact_hash, ''),
		       COALESCE(w.source_export_hash, ''), w.created_at, w.updated_at,
		       w.approved_at, COALESCE(a.rule_id, ''), COALESCE(a.version_number, 0)
		FROM dsl_workflows w
		LEFT JOIN dsl_approvals a
		  ON a.workspace_id = w.workspace_id AND a.workflow_id = w.id
		WHERE w.id = ? AND w.workspace_id = ?
	`, id, workspaceID(ctx)).Scan(
		&workflow.ID, &workflow.WorkspaceID, &workflow.RequirementID, &workflow.RecordingID,
		&workflow.Status, &workflow.BrowserProfileID, &workflow.CurrentJobID,
		&workflow.RepairCount, &workflow.MaxRepairs, &provisionalArtifact,
		&workflow.ProvisionalHash, &workflow.LastReplaySequence, &workflow.ErrorCode,
		&workflow.ErrorMessage, &workflow.Owner, &workflow.SourceKind, &workflow.SourceAuthority,
		&workflow.SourceArtifactHash, &workflow.SourceExportHash,
		&workflow.CreatedAt, &workflow.UpdatedAt,
		&workflow.ApprovedAt, &workflow.ApprovedRuleID, &workflow.ApprovedVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDSLWorkflowNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(provisionalArtifact) > 0 {
		var artifact provisionalDSLArtifact
		if err := s.openRequirementArtifact(workflow.WorkspaceID, "dsl-workflow", workflow.ID, "provisional", provisionalArtifact, workflow.ProvisionalHash, &artifact); err != nil {
			return nil, err
		}
		workflow.ProvisionalRule = artifact.Rule
		workflow.ProvisionalYAML = artifact.YAML
	}
	return workflow, nil
}

// CreateAdminReviewedDSLWorkflow persists a validated imported rule directly
// at the replay boundary. It deliberately creates no DSL job or provider
// lineage; the source labels are immutable and carry no qualification or
// promotion authority.
func (s *Store) CreateAdminReviewedDSLWorkflow(ctx context.Context, workflow *models.DSLWorkflow, rule *models.Rule, yaml string) (string, error) {
	if workflow == nil || rule == nil || workflow.RequirementID == "" || workflow.RecordingID == "" {
		return "", ErrRequirementNotFound
	}
	if strings.TrimSpace(workflow.BrowserProfileID) == "" {
		return "", errors.New("browser profile id is required")
	}
	workspace := workspaceID(ctx)
	workflow.WorkspaceID = workspace
	if workflow.ID == "" {
		workflow.ID = NewID()
	}
	workflow.Status = models.DSLWorkflowAwaitingReplay
	workflow.CurrentJobID = ""
	workflow.RepairCount = 0
	workflow.MaxRepairs = 0
	workflow.SourceKind = models.DSLWorkflowSourceAdminReviewedAttemptExport
	workflow.SourceAuthority = models.DSLWorkflowSourceAuthorityNonAuthoritative
	if len(workflow.SourceArtifactHash) != 64 || len(workflow.SourceExportHash) != 64 {
		return "", errors.New("reviewed attempt artifact and export hashes are required")
	}
	if workflow.Owner == "" {
		workflow.Owner = authz.Subject(ctx, "system")
	}
	now := time.Now().UTC()
	workflow.CreatedAt = now
	workflow.UpdatedAt = now
	artifact, hash, err := s.sealRequirementArtifact(
		workspace, "dsl-workflow", workflow.ID, "provisional",
		provisionalDSLArtifact{Rule: rule, YAML: yaml},
	)
	if err != nil {
		return "", err
	}
	workflow.ProvisionalRule = rule
	workflow.ProvisionalYAML = yaml
	workflow.ProvisionalHash = hash

	auditPayload, err := json.Marshal(map[string]any{
		"requirementId":      workflow.RequirementID,
		"recordingId":        workflow.RecordingID,
		"sourceKind":         workflow.SourceKind,
		"sourceAuthority":    workflow.SourceAuthority,
		"sourceArtifactHash": workflow.SourceArtifactHash,
		"sourceExportHash":   workflow.SourceExportHash,
		"provisionalHash":    hash,
	})
	if err != nil {
		return "", err
	}
	persistedID := workflow.ID
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO dsl_workflows (
				id, workspace_id, requirement_id, recording_id, status,
				browser_profile_id, current_job_id, repair_count, max_repairs,
				provisional_artifact, provisional_hash, last_replay_sequence,
				owner, source_kind, source_authority, source_artifact_hash,
				source_export_hash, budget_usd_nanos, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, NULL, 0, 0, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_id, requirement_id, browser_profile_id, source_export_hash)
			WHERE source_kind = 'admin_reviewed_attempt_export'
			  AND source_export_hash <> ''
			  AND status <> 'failed'
			DO NOTHING
		`, workflow.ID, workspace, workflow.RequirementID, workflow.RecordingID,
			workflow.Status, workflow.BrowserProfileID, artifact, hash, workflow.Owner,
			workflow.SourceKind, workflow.SourceAuthority, workflow.SourceArtifactHash,
			workflow.SourceExportHash, DefaultDSLWorkflowBudgetNanos, now, now)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return tx.QueryRowContext(ctx, `
				SELECT id
				FROM dsl_workflows
				WHERE workspace_id = ?
				  AND requirement_id = ?
				  AND browser_profile_id = ?
				  AND source_kind = 'admin_reviewed_attempt_export'
				  AND source_export_hash = ?
				  AND status <> 'failed'
			`, workspace, workflow.RequirementID, workflow.BrowserProfileID,
				workflow.SourceExportHash).Scan(&persistedID)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO audit_logs (
				id, workspace_id, actor, action, resource_type, resource_id, payload, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, NewID(), workspace, authz.Subject(ctx, "system"),
			"dsl_workflow_adopted_from_attempt_export", "dsl_workflow", workflow.ID,
			models.JSON(auditPayload), now)
		return err
	})
	if isConstraintMessage(err, "confirmed requirement not found") {
		return "", ErrRequirementState
	}
	if err != nil {
		return "", err
	}
	return persistedID, nil
}

func (s *Store) GetDSLJob(ctx context.Context, id string) (*models.DSLJob, error) {
	job, _, resultArtifact, err := s.scanDSLJob(s.db.QueryRowContext(ctx, dslJobSelect+` WHERE id = ? AND workspace_id = ?`, id, workspaceID(ctx)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDSLJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(resultArtifact) > 0 {
		var result any
		if err := s.openRequirementArtifact(job.WorkspaceID, "dsl-job", job.ID, "result", resultArtifact, job.ResultHash, &result); err != nil {
			return nil, err
		}
		job.Result = result
	}
	return job, nil
}

// GetDSLWorkflowGenerationRequest returns the authenticated workflow's
// original encrypted generation request. It is intentionally not exposed by
// the API and exists so a failed workflow with no provisional rule can still
// validate a human correction against the trusted recording-derived baseline.
func (s *Store) GetDSLWorkflowGenerationRequest(ctx context.Context, workflowID string) (any, error) {
	job, requestArtifact, _, err := s.scanDSLJob(s.db.QueryRowContext(ctx, dslJobSelect+`
		WHERE workflow_id = ? AND workspace_id = ? AND kind = ?
		ORDER BY created_at ASC LIMIT 1
	`, workflowID, workspaceID(ctx), models.DSLJobGenerate))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDSLJobNotFound
	}
	if err != nil {
		return nil, err
	}
	var request any
	if err := s.openRequirementArtifact(job.WorkspaceID, "dsl-job", job.ID, "request", requestArtifact, job.RequestHash, &request); err != nil {
		return nil, err
	}
	return request, nil
}

// ClaimPendingDSLJob atomically leases the next runnable generation or repair
// job across workspaces. Request content is decrypted only in worker memory.
func (s *Store) ClaimPendingDSLJob(ctx context.Context, lease time.Duration) (*models.DSLJob, error) {
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(lease)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := failExpiredDispatchedSelectorRepairs(ctx, tx, now); err != nil {
		return nil, err
	}
	if err := failExpiredExhaustedDSLJobs(ctx, tx, now); err != nil {
		return nil, err
	}
	if err := s.failExpiredReplayAttempts(ctx, tx, now); err != nil {
		return nil, err
	}
	job, requestArtifact, _, err := s.scanDSLJob(tx.QueryRowContext(ctx, dslJobSelect+`
		WHERE (status = ? AND available_at <= ? AND attempt_count < max_attempts)
		   OR (status = ? AND lease_until IS NOT NULL AND lease_until <= ?
		       AND (
		            (kind = ? AND provider_dispatched = 0)
		         OR (kind != ? AND attempt_count < max_attempts)
		       ))
		ORDER BY available_at, created_at LIMIT 1`,
		models.DSLJobPending, now, models.DSLJobRunning, now,
		models.DSLJobSelectorRepair, models.DSLJobSelectorRepair,
	))
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return nil, ErrNoTaskAvailable
	}
	if err != nil {
		return nil, err
	}
	reuseSelectorAttempt := job.Status == models.DSLJobRunning &&
		job.Kind == models.DSLJobSelectorRepair && !job.ProviderDispatched
	var updated sql.Result
	if reuseSelectorAttempt {
		updated, err = tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET lease_until = ?, updated_at = ?, error_code = NULL, error_message = NULL
			WHERE id = ? AND workspace_id = ? AND kind = ? AND status = ?
			  AND attempt_count = ? AND provider_dispatched = 0
			  AND lease_until IS NOT NULL AND lease_until <= ?
		`, leaseUntil, now, job.ID, job.WorkspaceID, models.DSLJobSelectorRepair,
			models.DSLJobRunning, job.AttemptCount, now)
	} else {
		updated, err = tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET status = ?, attempt_count = attempt_count + 1,
			    started_at = COALESCE(started_at, ?), lease_until = ?, updated_at = ?,
			    error_code = NULL, error_message = NULL
			WHERE id = ? AND workspace_id = ? AND attempt_count < max_attempts
			  AND ((status = ? AND available_at <= ?)
			    OR (status = ? AND lease_until IS NOT NULL AND lease_until <= ?))
		`, models.DSLJobRunning, now, leaseUntil, now, job.ID, job.WorkspaceID,
			models.DSLJobPending, now, models.DSLJobRunning, now)
	}
	if err != nil {
		return nil, err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return nil, ErrNoTaskAvailable
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	job.Status = models.DSLJobRunning
	if !reuseSelectorAttempt {
		job.AttemptCount++
	}
	job.StartedAt = firstTime(job.StartedAt, now)
	job.LeaseUntil = &leaseUntil
	var request any
	if err := s.openRequirementArtifact(job.WorkspaceID, "dsl-job", job.ID, "request", requestArtifact, job.RequestHash, &request); err != nil {
		return nil, err
	}
	job.Request = request
	return job, nil
}

func failExpiredExhaustedDSLJobs(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, workspace_id, workflow_id
		FROM dsl_jobs
		WHERE kind != ? AND status = ?
		  AND lease_until IS NOT NULL AND lease_until <= ?
		  AND attempt_count >= max_attempts
	`, models.DSLJobSelectorRepair, models.DSLJobRunning, now)
	if err != nil {
		return err
	}
	type expired struct{ id, workspace, workflow string }
	var jobs []expired
	for rows.Next() {
		var job expired
		if err := rows.Scan(&job.id, &job.workspace, &job.workflow); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, job := range jobs {
		updated, err := tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET status = ?, lease_until = NULL, error_code = ?,
			    error_message = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = ?
			  AND kind != ? AND attempt_count >= max_attempts
			  AND lease_until IS NOT NULL AND lease_until <= ?
		`, models.DSLJobFailed, "JOB_LEASE_EXPIRED",
			"dsl worker lease expired on the final allowed attempt",
			now, now, job.id, job.workspace, models.DSLJobRunning,
			models.DSLJobSelectorRepair, now)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLJobState
		}
		updated, err = tx.ExecContext(ctx, `
			UPDATE dsl_workflows
			SET status = ?, error_code = ?, error_message = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND current_job_id = ?
			  AND status IN (?, ?)
		`, models.DSLWorkflowFailed, "JOB_LEASE_EXPIRED",
			"dsl worker lease expired on the final allowed attempt",
			now, job.workflow, job.workspace, job.id,
			models.DSLWorkflowGenerating, models.DSLWorkflowRepairing)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
	}
	return nil
}

// failExpiredReplayAttempts transitions any dsl_replay_attempts row whose
// expires_at has passed from 'running' to 'failed', then drives the same
// completeDSLReplayInTx state machine as the public CompleteDSLReplay path
// (succeeded=false, errorCode=REPLAY_ATTEMPT_EXPIRED). No repair is
// scheduled even when the workflow has remaining repair budget — the
// reaper lives in the store layer and cannot construct the manager-layer
// repairRequest (which requires LLM prompt assembly). Operators can
// manually re-trigger the workflow if a repair is desired.
// No artifacts are sealed — the reaper has no payload.
func (s *Store) failExpiredReplayAttempts(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, workspace_id, workflow_id, sequence
		FROM dsl_replay_attempts
		WHERE status = ? AND expires_at IS NOT NULL AND expires_at <= ?
	`, models.ReplayAttemptRunning, now)
	if err != nil {
		return err
	}
	type expired struct {
		id, workspace, workflow string
		sequence                int
	}
	var attempts []expired
	for rows.Next() {
		var a expired
		if err := rows.Scan(&a.id, &a.workspace, &a.workflow, &a.sequence); err != nil {
			rows.Close()
			return err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, a := range attempts {
		if _, _, _, _, _, err := s.completeDSLReplayInTx(ctx, tx, a.workspace, a.id,
			false, false,
			"REPLAY_ATTEMPT_EXPIRED", "replay attempt timed out waiting for browser completion",
			nil, "", nil, "", nil, "",
			nil, 3, now,
		); err != nil {
			return fmt.Errorf("complete expired replay attempt %s: %w", a.id, err)
		}
	}
	return nil
}

func failExpiredDispatchedSelectorRepairs(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, workspace_id, workflow_id
		FROM dsl_jobs
		WHERE kind = ? AND status = ? AND provider_dispatched = 1
		  AND lease_until IS NOT NULL AND lease_until <= ?
	`, models.DSLJobSelectorRepair, models.DSLJobRunning, now)
	if err != nil {
		return err
	}
	type expired struct{ id, workspace, workflow string }
	var jobs []expired
	for rows.Next() {
		var job expired
		if err := rows.Scan(&job.id, &job.workspace, &job.workflow); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, job := range jobs {
		result, err := tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET status = ?, lease_until = NULL, error_code = ?,
			    error_message = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = ?
			  AND kind = ? AND provider_dispatched = 1
		`, models.DSLJobFailed, "SELECTOR_REPAIR_DISPATCH_AMBIGUOUS",
			"selector repair dispatch outcome is ambiguous; automatic redispatch is disabled",
			now, now, job.id, job.workspace, models.DSLJobRunning, models.DSLJobSelectorRepair)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrDSLJobState
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE dsl_workflows
			SET status = ?, error_code = ?, error_message = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND current_job_id = ? AND status = ?
		`, models.DSLWorkflowFailed, "SELECTOR_REPAIR_DISPATCH_AMBIGUOUS",
			"selector repair dispatch outcome is ambiguous; use the authorized correction workflow",
			now, job.workflow, job.workspace, job.id, models.DSLWorkflowRepairing)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
	}
	return nil
}

// MarkDSLSelectorRepairDispatched is the durable at-most-once boundary. It
// must commit before the completion call begins. A reclaimed lease with this
// marker is terminalized by ClaimPendingDSLJob instead of being redispatched.
func (s *Store) MarkDSLSelectorRepairDispatched(ctx context.Context, job *models.DSLJob) error {
	if job == nil || job.Kind != models.DSLJobSelectorRepair || job.AttemptCount <= 0 {
		return ErrDSLJobState
	}
	now := time.Now().UTC()
	updated, err := s.db.ExecContext(ctx, `
		UPDATE dsl_jobs
		SET provider_dispatched = 1, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND workflow_id = ?
		  AND kind = ? AND status = ? AND attempt_count = ?
		  AND provider_dispatched = 0
	`, now, job.ID, job.WorkspaceID, job.WorkflowID, models.DSLJobSelectorRepair,
		models.DSLJobRunning, job.AttemptCount)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrDSLJobState
	}
	job.ProviderDispatched = true
	return nil
}

// ScheduleDSLSelectorRepairWithAttemptReport atomically terminalizes the
// eligible failed source attempt, seals its immutable one-shot repair plan,
// and installs exactly one child job. It never increments replay repairCount.
func (s *Store) ScheduleDSLSelectorRepairWithAttemptReport(
	ctx context.Context,
	source *models.DSLJob,
	report *models.LLMAttemptReport,
	reportArtifact any,
	request any,
) (*models.DSLJob, error) {
	if source == nil || source.Kind == models.DSLJobSelectorRepair ||
		report == nil || report.JobType != models.LLMJobTypeDSL ||
		report.JobID != source.ID || report.AttemptNumber != source.AttemptCount ||
		report.Outcome != models.LLMAttemptFailed || !report.Replayable {
		return nil, ErrLLMArtifactInvalid
	}
	if err := s.validateCurrentDSLJobAttempt(ctx, source); err != nil {
		return nil, err
	}
	preparedReport, err := s.prepareLLMAttemptReport(ctx, report, reportArtifact)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	child := &models.DSLJob{
		ID: NewID(), WorkspaceID: source.WorkspaceID, WorkflowID: source.WorkflowID,
		Kind: models.DSLJobSelectorRepair, Status: models.DSLJobPending,
		PromptVersion: prompt.DSLSelectorRepairVersion, MaxAttempts: 1,
		AvailableAt: now, SourceAttemptReportID: report.ID,
		SafetyFlags: []string{"selector-repair:bounded", "selector-repair:one-shot"},
		CreatedAt:   now, UpdatedAt: now,
	}
	requestArtifact, requestHash, err := s.sealRequirementArtifact(
		child.WorkspaceID, "dsl-job", child.ID, "request", request,
	)
	if err != nil {
		return nil, err
	}
	child.RequestHash = requestHash
	sourceSafetyFlags, err := json.Marshal(source.SafetyFlags)
	if err != nil {
		return nil, err
	}
	childSafetyFlags, err := json.Marshal(child.SafetyFlags)
	if err != nil {
		return nil, err
	}
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := insertLLMAttemptReport(ctx, tx, report, preparedReport); err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET status = ?, lease_until = NULL, provider = NULLIF(?, ''),
			    model = NULLIF(?, ''), prompt_version = ?, chunk_count = ?,
			    completed_chunks = ?, input_tokens = ?, output_tokens = ?,
			    safety_flags = ?, error_code = ?, error_message = ?,
			    completed_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND workflow_id = ?
			  AND status = ? AND attempt_count = ?
		`, models.DSLJobFailed, source.Provider, source.Model, source.PromptVersion,
			source.ChunkCount, source.CompletedChunks, source.InputTokens,
			source.OutputTokens, string(sourceSafetyFlags), report.ErrorCode,
			report.ErrorMessage, now, now, source.ID, source.WorkspaceID,
			source.WorkflowID, models.DSLJobRunning, source.AttemptCount)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLJobState
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dsl_jobs (
				id, workspace_id, workflow_id, kind, status, request_artifact,
				request_hash, prompt_version, max_attempts, available_at,
				source_attempt_report_id, safety_flags, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
		`, child.ID, child.WorkspaceID, child.WorkflowID, child.Kind, child.Status,
			requestArtifact, child.RequestHash, child.PromptVersion, child.AvailableAt,
			child.SourceAttemptReportID, string(childSafetyFlags), child.CreatedAt,
			child.UpdatedAt); err != nil {
			return err
		}
		updated, err = tx.ExecContext(ctx, `
			UPDATE dsl_workflows
			SET status = ?, current_job_id = ?, error_code = NULL,
			    error_message = NULL, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND current_job_id = ?
			  AND status IN (?, ?)
		`, models.DSLWorkflowRepairing, child.ID, now, source.WorkflowID,
			source.WorkspaceID, source.ID, models.DSLWorkflowGenerating,
			models.DSLWorkflowRepairing)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return child, nil
}

// CompleteDSLJob stores the generated rule and moves the workflow to replay.
func (s *Store) CompleteDSLJob(ctx context.Context, job *models.DSLJob, rule *models.Rule, yaml string, result any) error {
	return s.completeDSLJobWithOptionalAttemptReport(ctx, job, rule, yaml, result, nil, nil)
}

// CompleteDSLJobWithAttemptReport atomically seals the successful attempt
// report, completes the exact claimed job attempt, and publishes its
// provisional rule. A stale worker cannot publish either half.
func (s *Store) CompleteDSLJobWithAttemptReport(
	ctx context.Context,
	job *models.DSLJob,
	rule *models.Rule,
	yaml string,
	result any,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if report == nil || job == nil || report.JobType != models.LLMJobTypeDSL ||
		report.JobID != job.ID || report.AttemptNumber != job.AttemptCount ||
		report.Outcome != models.LLMAttemptSucceeded {
		return ErrLLMArtifactInvalid
	}
	return s.completeDSLJobWithOptionalAttemptReport(ctx, job, rule, yaml, result, report, reportArtifact)
}

func (s *Store) completeDSLJobWithOptionalAttemptReport(
	ctx context.Context,
	job *models.DSLJob,
	rule *models.Rule,
	yaml string,
	result any,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if job == nil || rule == nil {
		return ErrDSLJobState
	}
	provisionalArtifact, provisionalHash, err := s.sealRequirementArtifact(job.WorkspaceID, "dsl-workflow", job.WorkflowID, "provisional", provisionalDSLArtifact{Rule: rule, YAML: yaml})
	if err != nil {
		return err
	}
	resultArtifact, resultHash, err := s.sealRequirementArtifact(job.WorkspaceID, "dsl-job", job.ID, "result", result)
	if err != nil {
		return err
	}
	safetyFlags, err := json.Marshal(job.SafetyFlags)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expectedWorkflowStatus := models.DSLWorkflowGenerating
	if job.Kind == models.DSLJobRepair || job.Kind == models.DSLJobSelectorRepair {
		expectedWorkflowStatus = models.DSLWorkflowRepairing
	}
	var preparedReport *preparedLLMAttemptReport
	if report != nil {
		if err := s.validateCurrentDSLJobAttempt(ctx, job); err != nil {
			return err
		}
		preparedReport, err = s.prepareLLMAttemptReport(ctx, report, reportArtifact)
		if err != nil {
			return err
		}
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if report != nil {
			if err := insertLLMAttemptReport(ctx, tx, report, preparedReport); err != nil {
				return err
			}
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET status = ?, result_artifact = ?, result_hash = ?, provider = NULLIF(?, ''),
			    model = NULLIF(?, ''), prompt_version = ?, chunk_count = ?, completed_chunks = ?,
			    input_tokens = ?, output_tokens = ?, safety_flags = ?,
			    lease_until = NULL, error_code = NULL, error_message = NULL,
			    completed_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND workflow_id = ? AND status = ?
			  AND attempt_count = ?
		`, models.DSLJobCompleted, resultArtifact, resultHash, job.Provider, job.Model,
			job.PromptVersion, job.ChunkCount, job.CompletedChunks, job.InputTokens, job.OutputTokens,
			string(safetyFlags), now, now, job.ID, job.WorkspaceID, job.WorkflowID,
			models.DSLJobRunning, job.AttemptCount)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLJobState
		}
		updated, err = tx.ExecContext(ctx, `
			UPDATE dsl_workflows
			SET status = ?, provisional_artifact = ?, provisional_hash = ?,
			    safety_flags = ?, error_code = NULL, error_message = NULL, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND current_job_id = ? AND status = ?
		`, models.DSLWorkflowAwaitingReplay, provisionalArtifact, provisionalHash,
			string(safetyFlags), now, job.WorkflowID, job.WorkspaceID, job.ID, expectedWorkflowStatus)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
		return nil
	})
}

// CorrectDSLWorkflow replaces a failed or not-yet-replayed provisional rule
// after the manager has applied the full schema, identity, output, and safety
// gates. Historical jobs and replay attempts remain immutable for audit.
func (s *Store) CorrectDSLWorkflow(ctx context.Context, workflowID string, rule *models.Rule, yaml string) error {
	if rule == nil {
		return ErrDSLWorkflowState
	}
	workspace := workspaceID(ctx)
	provisionalArtifact, provisionalHash, err := s.sealRequirementArtifact(
		workspace, "dsl-workflow", workflowID, "provisional",
		provisionalDSLArtifact{Rule: rule, YAML: yaml},
	)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	updated, err := s.db.ExecContext(ctx, `
		UPDATE dsl_workflows
		SET status = ?, provisional_artifact = ?, provisional_hash = ?,
		    current_job_id = NULL, error_code = NULL, error_message = NULL,
		    updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status IN (?, ?)
	`, models.DSLWorkflowAwaitingReplay, provisionalArtifact, provisionalHash, now,
		workflowID, workspace, models.DSLWorkflowFailed, models.DSLWorkflowAwaitingReplay)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrDSLWorkflowState
	}
	return nil
}

// UpdateDSLJobProgress records chunked analysis progress for a generation or
// repair job that is currently running. Terminal or not-yet-claimed jobs are
// left untouched so completion metadata is never clobbered.
func (s *Store) UpdateDSLJobProgress(ctx context.Context, id string, total, completed int) error {
	return s.updateDSLJobProgress(ctx, id, 0, total, completed)
}

// UpdateDSLJobAttemptProgress additionally fences progress to the exact lease
// attempt. Workflow workers use this form; the legacy method remains for
// callers that do not hold a claimed attempt object.
func (s *Store) UpdateDSLJobAttemptProgress(ctx context.Context, id string, attemptNumber, total, completed int) error {
	if attemptNumber <= 0 {
		return ErrDSLJobState
	}
	return s.updateDSLJobProgress(ctx, id, attemptNumber, total, completed)
}

func (s *Store) updateDSLJobProgress(ctx context.Context, id string, attemptNumber, total, completed int) error {
	now := time.Now().UTC()
	query := `
		UPDATE dsl_jobs
		SET chunk_count = ?, completed_chunks = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ?
	`
	args := []any{total, completed, now, id, workspaceID(ctx), models.DSLJobRunning}
	if attemptNumber > 0 {
		query += ` AND attempt_count = ?`
		args = append(args, attemptNumber)
	}
	updated, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrDSLJobState
	}
	return nil
}

// FailDSLJob schedules bounded retry/backoff, or terminally fails its workflow.
func (s *Store) FailDSLJob(ctx context.Context, job *models.DSLJob, code, message string, retryDelay time.Duration) error {
	return s.failDSLJobWithOptionalAttemptReport(ctx, job, code, message, retryDelay, nil, nil)
}

// FailDSLJobWithAttemptReport atomically records a failed physical attempt and
// its matching retry or terminal workflow transition.
func (s *Store) FailDSLJobWithAttemptReport(
	ctx context.Context,
	job *models.DSLJob,
	code, message string,
	retryDelay time.Duration,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if report == nil || job == nil || report.JobType != models.LLMJobTypeDSL ||
		report.JobID != job.ID || report.AttemptNumber != job.AttemptCount ||
		report.Outcome != models.LLMAttemptFailed {
		return ErrLLMArtifactInvalid
	}
	return s.failDSLJobWithOptionalAttemptReport(ctx, job, code, message, retryDelay, report, reportArtifact)
}

func (s *Store) failDSLJobWithOptionalAttemptReport(
	ctx context.Context,
	job *models.DSLJob,
	code, message string,
	retryDelay time.Duration,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if job == nil {
		return ErrDSLJobState
	}
	safetyFlags, err := json.Marshal(job.SafetyFlags)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	status := models.DSLJobFailed
	availableAt := now
	var completedAt any = now
	if job.AttemptCount < job.MaxAttempts {
		status = models.DSLJobPending
		if retryDelay < 0 {
			retryDelay = 0
		}
		availableAt = now.Add(retryDelay)
		completedAt = nil
	}
	var preparedReport *preparedLLMAttemptReport
	if report != nil {
		if err := s.validateCurrentDSLJobAttempt(ctx, job); err != nil {
			return err
		}
		preparedReport, err = s.prepareLLMAttemptReport(ctx, report, reportArtifact)
		if err != nil {
			return err
		}
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if report != nil {
			if err := insertLLMAttemptReport(ctx, tx, report, preparedReport); err != nil {
				return err
			}
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE dsl_jobs
			SET status = ?, available_at = ?, lease_until = NULL,
			    provider = NULLIF(?, ''), model = NULLIF(?, ''), prompt_version = ?,
			    chunk_count = ?, completed_chunks = ?, input_tokens = ?, output_tokens = ?,
			    safety_flags = ?, error_code = ?, error_message = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND workflow_id = ? AND status = ?
			  AND attempt_count = ?
		`, status, availableAt, job.Provider, job.Model, job.PromptVersion,
			job.ChunkCount, job.CompletedChunks, job.InputTokens, job.OutputTokens,
			string(safetyFlags), code, message, completedAt, now,
			job.ID, job.WorkspaceID, job.WorkflowID, models.DSLJobRunning, job.AttemptCount)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLJobState
		}
		if status != models.DSLJobFailed {
			return nil
		}
		updated, err = tx.ExecContext(ctx, `
			UPDATE dsl_workflows SET status = ?, error_code = ?, error_message = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND current_job_id = ?
			  AND status IN (?, ?)
		`, models.DSLWorkflowFailed, code, message, now, job.WorkflowID, job.WorkspaceID,
			job.ID, models.DSLWorkflowGenerating, models.DSLWorkflowRepairing)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
		return nil
	})
}

// StartDSLReplay creates a full replay attempt for the current provisional
// hash and changes the durable workflow state to replaying. The optional
// timeout overrides the default 35-minute server-side expiry.
func (s *Store) StartDSLReplay(ctx context.Context, workflowID string, timeout ...time.Duration) (*models.ReplayAttempt, error) {
	workspace := workspaceID(ctx)
	now := time.Now().UTC()
	expiresAt := now.Add(35 * time.Minute)
	if len(timeout) > 0 && timeout[0] > 0 {
		expiresAt = now.Add(timeout[0])
	}
	attempt := &models.ReplayAttempt{ID: NewID(), WorkspaceID: workspace, WorkflowID: workflowID, Status: models.ReplayAttemptRunning, StartedAt: now}
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var status models.DSLWorkflowStatus
		if err := tx.QueryRowContext(ctx, `
			SELECT status, COALESCE(provisional_hash, ''), browser_profile_id, last_replay_sequence
			FROM dsl_workflows WHERE id = ? AND workspace_id = ?
		`, workflowID, workspace).Scan(&status, &attempt.RuleHash, &attempt.BrowserProfileID, &attempt.Sequence); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDSLWorkflowNotFound
			}
			return err
		}
		if status != models.DSLWorkflowAwaitingReplay && status != models.DSLWorkflowAwaitingConfirmation {
			return ErrDSLWorkflowState
		}
		attempt.Sequence++
		updated, err := tx.ExecContext(ctx, `
			UPDATE dsl_workflows SET status = ?, last_replay_sequence = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = ?
		`, models.DSLWorkflowReplaying, attempt.Sequence, now, workflowID, workspace, status)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO dsl_replay_attempts (
				id, workspace_id, workflow_id, sequence, status, rule_hash,
				browser_profile_id, started_at, expires_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, attempt.ID, attempt.WorkspaceID, attempt.WorkflowID, attempt.Sequence,
			attempt.Status, attempt.RuleHash, attempt.BrowserProfileID, attempt.StartedAt, expiresAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return attempt, nil
}

// CompleteDSLReplay encrypts all browser-submitted material and either waits
// for confirmation, schedules one of at most three repairs, or fails safely.
func (s *Store) CompleteDSLReplay(ctx context.Context, attempt *models.ReplayAttempt, succeeded, outputValid bool, diagnostics, output any, artifacts []models.ReplayArtifact, code, message string, repairRequest any, repairMaxAttempts ...int) (*models.DSLJob, error) {
	if attempt == nil {
		return nil, ErrReplayNotFound
	}
	workspace := workspaceID(ctx)
	if attempt.WorkspaceID != "" && attempt.WorkspaceID != workspace {
		return nil, ErrReplayNotFound
	}
	attempt.WorkspaceID = workspace
	diagnosticsArtifact, diagnosticsHash, err := s.sealOptionalDSLArtifact(workspace, "dsl-replay", attempt.ID, "diagnostics", diagnostics)
	if err != nil {
		return nil, err
	}
	outputArtifact, outputHash, err := s.sealOptionalDSLArtifact(workspace, "dsl-replay", attempt.ID, "output", output)
	if err != nil {
		return nil, err
	}
	artifactsArtifact, artifactsHash, err := s.sealOptionalDSLArtifact(workspace, "dsl-replay", attempt.ID, "artifacts", artifacts)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	jobMaxAttempts := 3
	if len(repairMaxAttempts) > 0 && repairMaxAttempts[0] >= 1 && repairMaxAttempts[0] <= 3 {
		jobMaxAttempts = repairMaxAttempts[0]
	}
	var repairJob *models.DSLJob
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		var workflowID, ruleHash, browserProfileID string
		var sequence int
		var err error
		repairJob, workflowID, ruleHash, browserProfileID, sequence, err = s.completeDSLReplayInTx(ctx, tx, workspace, attempt.ID,
			succeeded, outputValid, code, message,
			diagnosticsArtifact, diagnosticsHash,
			outputArtifact, outputHash,
			artifactsArtifact, artifactsHash,
			repairRequest, jobMaxAttempts, now)
		if err != nil {
			return err
		}
		attempt.WorkflowID = workflowID
		attempt.Sequence = sequence
		attempt.RuleHash = ruleHash
		attempt.BrowserProfileID = browserProfileID
		attempt.Status = models.ReplayAttemptFailed
		if succeeded && outputValid {
			attempt.Status = models.ReplayAttemptSucceeded
			code, message = "", ""
		}
		attempt.OutputValid = outputValid
		attempt.DiagnosticsHash = diagnosticsHash
		attempt.OutputHash = outputHash
		attempt.ArtifactsHash = artifactsHash
		attempt.ErrorCode = code
		attempt.ErrorMessage = message
		attempt.CompletedAt = &now
		return nil
	})
	return repairJob, err
}

// completeDSLReplayInTx runs ONLY the state-machine interior: SELECT attempt,
// validate state, optionally create repair job, UPDATE attempt + workflow.
// Artifact sealing happens BEFORE this in the public CompleteDSLReplay path;
// the reaper calls this with nil artifacts and a nil repairRequest.
// Returns the repair job (if any) plus the read-back identity fields.
func (s *Store) completeDSLReplayInTx(
	ctx context.Context, tx *sql.Tx,
	workspace, attemptID string,
	succeeded, outputValid bool,
	code, message string,
	diagnosticsArtifact []byte, diagnosticsHash string,
	outputArtifact []byte, outputHash string,
	artifactsArtifact []byte, artifactsHash string,
	repairRequest any, jobMaxAttempts int,
	now time.Time,
) (*models.DSLJob, string, string, string, int, error) {
	terminalStatus := models.ReplayAttemptFailed
	workflowStatus := models.DSLWorkflowFailed
	if succeeded && outputValid {
		terminalStatus = models.ReplayAttemptSucceeded
		workflowStatus = models.DSLWorkflowAwaitingConfirmation
		code, message = "", ""
	}
	var workflowID, ruleHash, browserProfileID string
	var sequence, repairCount, maxRepairs int
	var replayStatus models.ReplayAttemptStatus
	if err := tx.QueryRowContext(ctx, `
		SELECT workflow_id, sequence, status, rule_hash, browser_profile_id
		FROM dsl_replay_attempts WHERE id = ? AND workspace_id = ?
	`, attemptID, workspace).Scan(&workflowID, &sequence, &replayStatus, &ruleHash, &browserProfileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", "", "", 0, ErrReplayNotFound
		}
		return nil, "", "", "", 0, err
	}
	if replayStatus != models.ReplayAttemptRunning {
		return nil, "", "", "", 0, ErrReplayState
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT repair_count, max_repairs FROM dsl_workflows
		WHERE id = ? AND workspace_id = ? AND status = ?
	`, workflowID, workspace, models.DSLWorkflowReplaying).Scan(&repairCount, &maxRepairs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", "", "", 0, ErrDSLWorkflowState
		}
		return nil, "", "", "", 0, err
	}
	var repairJob *models.DSLJob
	if terminalStatus == models.ReplayAttemptFailed && repairCount < maxRepairs && repairRequest != nil {
		repairJob = &models.DSLJob{
			ID: NewID(), WorkspaceID: workspace, WorkflowID: workflowID,
			Kind: models.DSLJobRepair, Status: models.DSLJobPending,
			PromptVersion: prompt.DSLWorkflowVersion, MaxAttempts: jobMaxAttempts, AvailableAt: now,
			CreatedAt: now, UpdatedAt: now, SafetyFlags: []string{},
		}
		requestArtifact, requestHash, err := s.sealRequirementArtifact(workspace, "dsl-job", repairJob.ID, "request", repairRequest)
		if err != nil {
			return nil, "", "", "", 0, err
		}
		repairJob.RequestHash = requestHash
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dsl_jobs (
				id, workspace_id, workflow_id, kind, status, request_artifact,
				request_hash, prompt_version, max_attempts, available_at,
				safety_flags, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', ?, ?)
		`, repairJob.ID, workspace, workflowID, repairJob.Kind, repairJob.Status,
			requestArtifact, requestHash, repairJob.PromptVersion, repairJob.MaxAttempts,
			repairJob.AvailableAt, now, now); err != nil {
			return nil, "", "", "", 0, err
		}
		workflowStatus = models.DSLWorkflowRepairing
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE dsl_replay_attempts
		SET status = ?, output_valid = ?, diagnostics_artifact = ?, diagnostics_hash = NULLIF(?, ''),
		    output_artifact = ?, output_hash = NULLIF(?, ''), artifacts_artifact = ?,
		    artifacts_hash = NULLIF(?, ''), error_code = NULLIF(?, ''),
		    error_message = NULLIF(?, ''), completed_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ?
	`, terminalStatus, outputValid, diagnosticsArtifact, diagnosticsHash, outputArtifact,
		outputHash, artifactsArtifact, artifactsHash, code, message, now,
		attemptID, workspace, models.ReplayAttemptRunning)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return nil, "", "", "", 0, ErrReplayState
	}
	currentJobID := ""
	if repairJob != nil {
		currentJobID = repairJob.ID
		repairCount++
	}
	updated, err = tx.ExecContext(ctx, `
		UPDATE dsl_workflows
		SET status = ?, current_job_id = NULLIF(?, ''), repair_count = ?,
		    error_code = NULLIF(?, ''), error_message = NULLIF(?, ''), updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ?
	`, workflowStatus, currentJobID, repairCount, code, message, now,
		workflowID, workspace, models.DSLWorkflowReplaying)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return nil, "", "", "", 0, ErrDSLWorkflowState
	}
	return repairJob, workflowID, ruleHash, browserProfileID, sequence, nil
}

func (s *Store) sealOptionalDSLArtifact(workspace, resourceType, id, field string, value any) ([]byte, string, error) {
	if value == nil {
		return nil, "", nil
	}
	return s.sealRequirementArtifact(workspace, resourceType, id, field, value)
}

func (s *Store) GetDSLReplay(ctx context.Context, id string) (*models.ReplayAttempt, error) {
	attempt := &models.ReplayAttempt{}
	var diagnosticsArtifact, outputArtifact, artifactsArtifact []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, workflow_id, sequence, status, rule_hash,
		       browser_profile_id, output_valid, diagnostics_artifact,
		       COALESCE(diagnostics_hash, ''), output_artifact,
		       COALESCE(output_hash, ''), artifacts_artifact,
		       COALESCE(artifacts_hash, ''), COALESCE(error_code, ''),
		       COALESCE(error_message, ''), started_at, completed_at
		FROM dsl_replay_attempts WHERE id = ? AND workspace_id = ?
	`, id, workspaceID(ctx)).Scan(
		&attempt.ID, &attempt.WorkspaceID, &attempt.WorkflowID, &attempt.Sequence,
		&attempt.Status, &attempt.RuleHash, &attempt.BrowserProfileID, &attempt.OutputValid,
		&diagnosticsArtifact, &attempt.DiagnosticsHash, &outputArtifact, &attempt.OutputHash,
		&artifactsArtifact, &attempt.ArtifactsHash, &attempt.ErrorCode,
		&attempt.ErrorMessage, &attempt.StartedAt, &attempt.CompletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReplayNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(diagnosticsArtifact) > 0 {
		if err := s.openRequirementArtifact(attempt.WorkspaceID, "dsl-replay", attempt.ID, "diagnostics", diagnosticsArtifact, attempt.DiagnosticsHash, &attempt.Diagnostics); err != nil {
			return nil, err
		}
	}
	if len(outputArtifact) > 0 {
		if err := s.openRequirementArtifact(attempt.WorkspaceID, "dsl-replay", attempt.ID, "output", outputArtifact, attempt.OutputHash, &attempt.Output); err != nil {
			return nil, err
		}
	}
	if len(artifactsArtifact) > 0 {
		if err := s.openRequirementArtifact(attempt.WorkspaceID, "dsl-replay", attempt.ID, "artifacts", artifactsArtifact, attempt.ArtifactsHash, &attempt.Artifacts); err != nil {
			return nil, err
		}
	}
	return attempt, nil
}

// ApproveDSLWorkflow creates and approves the immutable rule version in the
// same transaction as the workflow lineage and approval audit records.
func (s *Store) ApproveDSLWorkflow(ctx context.Context, workflowID string, opts ApproveDSLWorkflowOptions) (*models.RuleVersion, *models.RuleVersionContract, error) {
	workspace := workspaceID(ctx)
	var approved *models.RuleVersion
	var approvedContract *models.RuleVersionContract
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var requirementID, recordingID, requirementHash, recordingHash, provisionalHash, browserProfileID string
		var sourceKind, sourceAuthority, sourceArtifactHash, sourceExportHash string
		var provisionalArtifact, requirementArtifact []byte
		var status models.DSLWorkflowStatus
		var lastReplaySequence int
		var safetyFlagsJSON string
		if err := tx.QueryRowContext(ctx, `
			SELECT w.requirement_id, w.recording_id, w.status, w.provisional_artifact,
			       COALESCE(w.provisional_hash, ''), w.last_replay_sequence,
			       r.content_artifact, r.content_hash, rec.content_hash, w.browser_profile_id,
			       COALESCE(w.source_kind, ''), COALESCE(w.source_authority, ''),
			       COALESCE(w.source_artifact_hash, ''), COALESCE(w.source_export_hash, ''),
			       COALESCE(w.safety_flags, '[]')
			FROM dsl_workflows w
			JOIN collection_requirements r
			  ON r.workspace_id = w.workspace_id AND r.id = w.requirement_id
			JOIN recordings rec
			  ON rec.workspace_id = w.workspace_id AND rec.id = w.recording_id
			WHERE w.id = ? AND w.workspace_id = ?
		`, workflowID, workspace).Scan(&requirementID, &recordingID, &status,
			&provisionalArtifact, &provisionalHash, &lastReplaySequence,
			&requirementArtifact, &requirementHash, &recordingHash, &browserProfileID,
			&sourceKind, &sourceAuthority, &sourceArtifactHash, &sourceExportHash,
			&safetyFlagsJSON); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDSLWorkflowNotFound
			}
			return err
		}
		if status == models.DSLWorkflowApproved {
			var ruleID string
			var versionNumber int
			if err := tx.QueryRowContext(ctx, `
				SELECT rule_id, version_number
				FROM dsl_approvals
				WHERE workspace_id = ? AND workflow_id = ?
			`, workspace, workflowID).Scan(&ruleID, &versionNumber); err != nil {
				return ErrDSLWorkflowState
			}
			version, err := getRuleVersion(ctx, tx, workspace, ruleID, versionNumber)
			if err != nil || version.Status != models.RuleApprovalApproved {
				return ErrDSLWorkflowState
			}
			contract, err := getRuleVersionContractWithExec(
				ctx, tx, workspace, ruleID, versionNumber,
			)
			if err != nil {
				return err
			}
			expectedWorkflowID := ""
			if sourceKind == models.DSLWorkflowSourceAdminReviewedAttemptExport {
				expectedWorkflowID = workflowID
			}
			if contract.SourceKind != sourceKind ||
				contract.SourceAuthority != sourceAuthority ||
				contract.SourceArtifactHash != sourceArtifactHash ||
				contract.SourceExportHash != sourceExportHash ||
				contract.SourceWorkflowID != expectedWorkflowID {
				return ErrDSLWorkflowState
			}
			approved = version
			approvedContract = contract
			return nil
		}
		if status != models.DSLWorkflowAwaitingConfirmation || len(provisionalArtifact) == 0 {
			return ErrDSLWorkflowState
		}
		var safetyFlags []string
		if err := json.Unmarshal([]byte(safetyFlagsJSON), &safetyFlags); err != nil {
			return fmt.Errorf("unmarshal safety_flags: %w", err)
		}
		blocking := models.BlockingFlags(safetyFlags)
		if len(blocking) > 0 {
			if !opts.OverrideSafety {
				return &SafetyBlockingFlagError{Flags: blocking}
			}
			if opts.OverrideActor == "" {
				return fmt.Errorf("%w: override requires OverrideActor", ErrSafetyBlockingFlag)
			}
		}
		var replaySucceeded bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM dsl_replay_attempts
				WHERE workspace_id = ? AND workflow_id = ? AND sequence = ?
				  AND status = ? AND output_valid = 1 AND rule_hash = ?
			)
		`, workspace, workflowID, lastReplaySequence, models.ReplayAttemptSucceeded,
			provisionalHash).Scan(&replaySucceeded); err != nil {
			return err
		}
		if !replaySucceeded {
			return ErrDSLWorkflowState
		}
		var provisional provisionalDSLArtifact
		if err := s.openRequirementArtifact(workspace, "dsl-workflow", workflowID, "provisional", provisionalArtifact, provisionalHash, &provisional); err != nil {
			return err
		}
		if provisional.Rule == nil {
			return ErrDSLWorkflowState
		}
		if sourceKind == models.DSLWorkflowSourceAdminReviewedAttemptExport {
			if sourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
				len(sourceArtifactHash) != 64 || len(sourceExportHash) != 64 {
				return ErrDSLWorkflowState
			}
		} else if sourceKind != "" || sourceAuthority != "" || sourceArtifactHash != "" || sourceExportHash != "" {
			return ErrDSLWorkflowState
		}
		requirementContent, err := s.openCollectionRequirementContent(workspace, requirementID, requirementArtifact, requirementHash)
		if err != nil {
			return err
		}
		requirement := requirementContent.Requirement
		inputSchema, err := rulecontract.BuildRequirementInputSchema(requirement)
		if err != nil {
			return err
		}
		outputSchema, err := rulecontract.BuildRequirementOutputSchema(requirement)
		if err != nil {
			return err
		}
		sourceWorkflowID := ""
		if sourceKind == models.DSLWorkflowSourceAdminReviewedAttemptExport {
			sourceWorkflowID = workflowID
		}
		lineage := models.RuleVersionContract{
			SourceKind: sourceKind, SourceAuthority: sourceAuthority,
			SourceArtifactHash: sourceArtifactHash, SourceExportHash: sourceExportHash,
			SourceWorkflowID: sourceWorkflowID,
		}
		// Source LLM provenance from the latest completed DSL generation job.
		// Link is dsl_jobs -> llm_provider_calls via shared
		// (workspace_id, job_type='dsl', job_id) on the latest attempt.
		var dslJobID, providerCallID, promptHash, modelID string
		var cacheHit bool
		row := tx.QueryRowContext(ctx, `
			SELECT j.id,
			       COALESCE(pc.id, ''),
			       COALESCE(pc.request_hash, ''),
			       COALESCE(pc.model, ''),
			       COALESCE(pc.cache_hit, 0)
			FROM dsl_jobs j
			LEFT JOIN llm_provider_calls pc
			  ON pc.workspace_id = j.workspace_id
			 AND pc.job_type = 'dsl'
			 AND pc.job_id = j.id
			 AND pc.attempt_number = (
			        SELECT MAX(attempt_number)
			        FROM llm_provider_calls
			        WHERE workspace_id = j.workspace_id
			          AND job_type = 'dsl'
			          AND job_id = j.id
			    )
			WHERE j.workspace_id = ? AND j.workflow_id = ? AND j.status = ? AND j.kind = ?
			ORDER BY j.completed_at DESC, pc.call_index DESC
			LIMIT 1
		`, workspace, workflowID, models.DSLJobCompleted, models.DSLJobGenerate)
		if err := row.Scan(&dslJobID, &providerCallID, &promptHash, &modelID, &cacheHit); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("source dsl provenance: %w", err)
			}
			// Legacy workflow without provider-call lineage — fall back to blanks.
		}
		version, err := createRuleVersionWithContractAndLineageExec(
			ctx, tx, provisional.Rule, recordingID, inputSchema, outputSchema,
			browserProfileID, lineage, safetyFlags, workflowID, dslJobID,
		)
		if err != nil {
			return err
		}
		if version.Status == models.RuleApprovalPending {
			version, err = approveRuleVersionWithExec(ctx, tx, version)
			if err != nil {
				return err
			}
		}
		contract, err := getRuleVersionContractWithExec(
			ctx, tx, workspace, version.RuleID, version.Version,
		)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		updated, err := tx.ExecContext(ctx, `
			UPDATE dsl_workflows SET status = ?, approved_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = ?
		`, models.DSLWorkflowApproved, now, now, workflowID, workspace,
			models.DSLWorkflowAwaitingConfirmation)
		if err != nil {
			return err
		}
		if count, _ := updated.RowsAffected(); count != 1 {
			return ErrDSLWorkflowState
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dsl_approvals (
				workspace_id, workflow_id, requirement_id, requirement_hash,
				recording_id, recording_hash, rule_id, version_number, source_kind,
				source_authority, source_artifact_hash, source_export_hash,
				dsl_job_id, provider_call_id, prompt_hash, model_id, cache_hit,
				created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, workspace, workflowID, requirementID, requirementHash, recordingID,
			recordingHash, version.RuleID, version.Version, sourceKind, sourceAuthority,
			sourceArtifactHash, sourceExportHash,
			dslJobID, providerCallID, promptHash, modelID, cacheHit,
			now); err != nil {
			return err
		}
		auditPayload, err := json.Marshal(map[string]any{
			"ruleId":             version.RuleID,
			"version":            version.Version,
			"contentHash":        version.ContentHash,
			"sourceKind":         contract.SourceKind,
			"sourceAuthority":    contract.SourceAuthority,
			"sourceArtifactHash": contract.SourceArtifactHash,
			"sourceExportHash":   contract.SourceExportHash,
			"sourceWorkflowId":   contract.SourceWorkflowID,
			"dslJobId":           dslJobID,
			"providerCallId":     providerCallID,
			"promptHash":         promptHash,
			"modelId":            modelID,
			"cacheHit":           cacheHit,
		})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audit_logs (
				id, workspace_id, actor, action, resource_type, resource_id, payload, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, NewID(), workspace, authz.Subject(ctx, "system"), "dsl_workflow_approved",
			"dsl_workflow", workflowID, models.JSON(auditPayload), now); err != nil {
			return err
		}
		if opts.OverrideSafety && len(blocking) > 0 {
			overridePayload, err := json.Marshal(map[string]any{
				"workflowId":    workflowID,
				"blockingFlags": blocking,
				"ruleId":        version.RuleID,
				"version":       version.Version,
			})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO audit_logs (
					id, workspace_id, actor, action, resource_type, resource_id, payload, created_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, NewID(), workspace, opts.OverrideActor, "dsl_workflow_safety_override",
				"dsl_workflow", workflowID, models.JSON(overridePayload), now); err != nil {
				return err
			}
		}
		approved = version
		approvedContract = contract
		return nil
	})
	return approved, approvedContract, err
}

// DSLApprovalProvenance carries the LLM provider-call lineage sourced from the
// dsl_approvals row at approval time. It is the read-side view of the
// provenance columns written by ApproveDSLWorkflow.
type DSLApprovalProvenance struct {
	DSLJobID       string
	ProviderCallID string
	PromptHash     string
	ModelID        string
	CacheHit       bool
	SafetyFlags    []string
}

// GetDSLApprovalProvenance reads the LLM provenance block stored on the
// dsl_approvals row for the given workflow. Returns the zero value (with empty
// strings) when the workflow has no approval row yet.
func (s *Store) GetDSLApprovalProvenance(ctx context.Context, workflowID string) (*DSLApprovalProvenance, error) {
	workspace := workspaceID(ctx)
	var p DSLApprovalProvenance
	var safetyFlagsJSON string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(dsl_job_id, ''), COALESCE(provider_call_id, ''),
		       COALESCE(prompt_hash, ''), COALESCE(model_id, ''), cache_hit,
		       COALESCE((SELECT safety_flags FROM rule_versions rv
			             WHERE rv.workspace_id = a.workspace_id
			               AND rv.rule_id = a.rule_id
			               AND rv.version_number = a.version_number), '[]')
		FROM dsl_approvals a
		WHERE workspace_id = ? AND workflow_id = ?
	`, workspace, workflowID).Scan(&p.DSLJobID, &p.ProviderCallID, &p.PromptHash,
		&p.ModelID, &p.CacheHit, &safetyFlagsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &DSLApprovalProvenance{}, nil
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(safetyFlagsJSON), &p.SafetyFlags); err != nil {
		return nil, fmt.Errorf("decode approval safety_flags: %w", err)
	}
	return &p, nil
}

const dslJobSelect = `
	SELECT id, workspace_id, workflow_id, kind, status, request_artifact,
	       result_artifact, request_hash, COALESCE(result_hash, ''),
	       COALESCE(provider, ''), COALESCE(model, ''), prompt_version,
	       chunk_count, completed_chunks, input_tokens, output_tokens, attempt_count, max_attempts,
	       available_at, lease_until, provider_dispatched,
	       COALESCE(source_attempt_report_id, ''), COALESCE(error_code, ''),
	       COALESCE(error_message, ''), safety_flags, created_at, updated_at,
	       started_at, completed_at
	FROM dsl_jobs`

type dslJobScanner interface {
	Scan(...any) error
}

func (s *Store) scanDSLJob(scanner dslJobScanner) (*models.DSLJob, []byte, []byte, error) {
	job := &models.DSLJob{}
	var requestArtifact, resultArtifact []byte
	var safetyFlags string
	err := scanner.Scan(
		&job.ID, &job.WorkspaceID, &job.WorkflowID, &job.Kind, &job.Status,
		&requestArtifact, &resultArtifact, &job.RequestHash, &job.ResultHash,
		&job.Provider, &job.Model, &job.PromptVersion, &job.ChunkCount,
		&job.CompletedChunks, &job.InputTokens, &job.OutputTokens, &job.AttemptCount, &job.MaxAttempts,
		&job.AvailableAt, &job.LeaseUntil, &job.ProviderDispatched,
		&job.SourceAttemptReportID, &job.ErrorCode, &job.ErrorMessage,
		&safetyFlags, &job.CreatedAt, &job.UpdatedAt, &job.StartedAt, &job.CompletedAt,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := json.Unmarshal([]byte(safetyFlags), &job.SafetyFlags); err != nil {
		return nil, nil, nil, fmt.Errorf("decode dsl safety flags: %w", err)
	}
	if job.SafetyFlags == nil {
		job.SafetyFlags = []string{}
	}
	return job, requestArtifact, resultArtifact, nil
}

// CheckAndReserveWorkflowBudget checks whether the workflow's spent + estimated
// cost exceeds its budget. Returns nil if admitted, *budget.DeniedError with
// Code=CodeWorkflowBudgetExceeded if denied.
//
// NOTE: This is a read-only check inside a transaction. Concurrent DSL calls
// under the same workflow can both pass the check (both see the same spent
// amount) and then both settle, exceeding the envelope by up to one call's
// worst-case reservation. The global/workspace BudgetLedger (which IS
// transactional with respect to its own reserve/settle cycle) remains the
// authoritative gate; the workflow envelope is a secondary defense that bounds
// total per-workflow spend in the common (non-concurrent) case.
func (s *Store) CheckAndReserveWorkflowBudget(ctx context.Context, workflowID string, estimateNanos config.USDNanos) error {
	if workflowID == "" {
		return fmt.Errorf("workflow budget check requires a workflow id")
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		var budgetNanos, spentNanos int64
		if err := tx.QueryRowContext(ctx,
			`SELECT budget_usd_nanos, spent_usd_nanos FROM dsl_workflows WHERE id = ?`,
			workflowID,
		).Scan(&budgetNanos, &spentNanos); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("workflow %s not found: %w", workflowID, ErrDSLWorkflowNotFound)
			}
			return err
		}
		if spentNanos+int64(estimateNanos) > budgetNanos {
			return &budget.DeniedError{
				Scope:     "workflow",
				Limit:     "envelope",
				Requested: estimateNanos,
				Remaining: config.USDNanos(budgetNanos - spentNanos),
				Reason:    fmt.Sprintf("workflow %s envelope would be exceeded", workflowID),
				Code:      budget.CodeWorkflowBudgetExceeded,
			}
		}
		return nil
	})
}

// AddWorkflowSpend increments the workflow's spent_usd_nanos by costNanos. It
// is best-effort: callers in the settlement path log failures and continue.
func (s *Store) AddWorkflowSpend(ctx context.Context, workflowID string, costNanos config.USDNanos) error {
	if workflowID == "" {
		return fmt.Errorf("workflow spend update requires a workflow id")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE dsl_workflows SET spent_usd_nanos = spent_usd_nanos + ?, updated_at = ? WHERE id = ?`,
		int64(costNanos), time.Now().UTC(), workflowID,
	)
	return err
}
