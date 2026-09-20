package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/crypto"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var (
	ErrRequirementJobNotFound = errors.New("requirement job not found")
	ErrRequirementJobState    = errors.New("requirement job cannot transition from its current state")
	ErrRequirementNotFound    = errors.New("collection requirement not found")
	ErrRequirementState       = errors.New("collection requirement cannot transition from its current state")
)

const maxRequirementAttemptBudget = 3

func requirementArtifactAAD(workspace, resourceType, id, field string) []byte {
	return []byte(workspace + "\x00" + resourceType + "\x00" + id + "\x00" + field)
}

func artifactHash(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func (s *Store) sealRequirementArtifact(workspace, resourceType, id, field string, value any) ([]byte, string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("marshal %s artifact: %w", field, err)
	}
	compressed, err := gzipBytes(plain)
	if err != nil {
		return nil, "", fmt.Errorf("compress %s artifact: %w", field, err)
	}
	encrypted, err := crypto.EncryptArtifact(s.encryptionKey, compressed, requirementArtifactAAD(workspace, resourceType, id, field))
	if err != nil {
		return nil, "", fmt.Errorf("encrypt %s artifact: %w", field, err)
	}
	return encrypted, artifactHash(plain), nil
}

func (s *Store) openRequirementArtifact(workspace, resourceType, id, field string, encrypted []byte, expectedHash string, output any) error {
	compressed, err := crypto.DecryptArtifact(s.encryptionKey, encrypted, requirementArtifactAAD(workspace, resourceType, id, field))
	if err != nil {
		return fmt.Errorf("decrypt %s artifact: %w", field, err)
	}
	plain, err := gunzipBytes(compressed)
	if err != nil {
		return fmt.Errorf("decompress %s artifact: %w", field, err)
	}
	if expectedHash != "" && subtle.ConstantTimeCompare([]byte(artifactHash(plain)), []byte(expectedHash)) != 1 {
		return fmt.Errorf("%s artifact hash mismatch", field)
	}
	if err := json.Unmarshal(plain, output); err != nil {
		return fmt.Errorf("unmarshal %s artifact: %w", field, err)
	}
	return nil
}

// CreateRequirementJob persists an encrypted asynchronous request tied to an
// existing sanitized recording in the authenticated workspace.
func (s *Store) CreateRequirementJob(ctx context.Context, job *models.RequirementJob, request any) error {
	if job == nil || job.RecordingID == "" {
		return ErrRecordingNotFound
	}
	job.WorkspaceID = workspaceID(ctx)
	if job.ID == "" {
		job.ID = NewID()
	}
	if job.Status == "" {
		job.Status = models.RequirementJobPending
	}
	if job.Source == "" {
		job.Source = models.RequirementSourceLLM
	}
	if job.MaxAttempts <= 0 || job.MaxAttempts > maxRequirementAttemptBudget {
		job.MaxAttempts = maxRequirementAttemptBudget
	}
	if job.AttemptBudget <= 0 {
		job.AttemptBudget = job.MaxAttempts
	}
	if job.AttemptBudget > maxRequirementAttemptBudget {
		job.AttemptBudget = maxRequirementAttemptBudget
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	if job.AvailableAt.IsZero() {
		job.AvailableAt = now
	}
	var recordingExists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recordings WHERE id = ? AND workspace_id = ? AND status != ?)`,
		job.RecordingID, job.WorkspaceID, models.RecordingStatusDeleted).Scan(&recordingExists); err != nil {
		return err
	}
	if !recordingExists {
		return ErrRecordingNotFound
	}
	requestArtifact, requestHash, err := s.sealRequirementArtifact(job.WorkspaceID, "requirement-job", job.ID, "request", request)
	if err != nil {
		return err
	}
	job.RequestHash = requestHash
	safetyFlags, err := json.Marshal(job.SafetyFlags)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO requirement_jobs (
			id, workspace_id, recording_id, kind, status, source, request_artifact,
			request_hash, provider, model, prompt_version, chunk_count, completed_chunks, input_tokens,
			output_tokens, attempt_count, max_attempts, available_at, lease_until,
			attempt_budget,
			error_code, error_message, safety_flags, requirement_id, created_at,
			updated_at, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?)
	`, job.ID, job.WorkspaceID, job.RecordingID, job.Kind, job.Status, job.Source, requestArtifact,
		job.RequestHash, job.Provider, job.Model, job.PromptVersion, job.ChunkCount, job.CompletedChunks, job.InputTokens,
		job.OutputTokens, job.AttemptCount, job.MaxAttempts, job.AvailableAt, job.LeaseUntil,
		job.AttemptBudget, job.ErrorCode, job.ErrorMessage, string(safetyFlags), job.RequirementID, job.CreatedAt,
		job.UpdatedAt, job.StartedAt, job.CompletedAt)
	return err
}

// GetRequirementJob returns workspace-scoped metadata and its decrypted result.
// The original request remains server-internal.
func (s *Store) GetRequirementJob(ctx context.Context, id string) (*models.RequirementJob, error) {
	job, _, resultArtifact, err := s.scanRequirementJob(s.db.QueryRowContext(ctx, requirementJobSelect+` WHERE id = ? AND workspace_id = ?`, id, workspaceID(ctx)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequirementJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(resultArtifact) > 0 {
		var result any
		if err := s.openRequirementArtifact(job.WorkspaceID, "requirement-job", job.ID, "result", resultArtifact, job.ResultHash, &result); err != nil {
			return nil, err
		}
		job.Result = result
	}
	return job, nil
}

// ClaimPendingRequirementJob atomically leases the next runnable job across
// workspaces. The returned request is decrypted only in worker memory.
func (s *Store) ClaimPendingRequirementJob(ctx context.Context, lease time.Duration) (*models.RequirementJob, error) {
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	for {
		job, exhausted, err := s.claimPendingRequirementJob(ctx, lease)
		if err != nil {
			return nil, err
		}
		if exhausted {
			continue
		}
		return job, nil
	}
}

func (s *Store) claimPendingRequirementJob(ctx context.Context, lease time.Duration) (*models.RequirementJob, bool, error) {
	now := time.Now().UTC()
	leaseUntil := now.Add(lease)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, requirementJobSelect+`
		WHERE (status = ? AND available_at <= ? AND attempt_count < max_attempts)
		   OR (status = ? AND lease_until IS NOT NULL AND lease_until <= ?)
		ORDER BY available_at, created_at LIMIT 1`,
		models.RequirementJobPending, now, models.RequirementJobRunning, now)
	job, requestArtifact, _, err := s.scanRequirementJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNoTaskAvailable
	}
	if err != nil {
		return nil, false, err
	}
	if job.Status == models.RequirementJobRunning {
		if err := s.finalizeInterruptedRequirementAttempt(ctx, tx, job, now); err != nil {
			return nil, false, err
		}
		if job.AttemptCount >= job.MaxAttempts {
			updated, err := tx.ExecContext(ctx, `
				UPDATE requirement_jobs
				SET status = ?, lease_until = NULL, error_code = ?,
				    error_message = ?, completed_at = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND status = ? AND attempt_count = ?
			`, models.RequirementJobFailed, "ATTEMPT_INTERRUPTED",
				"the worker lease expired after the final permitted attempt",
				now, now, job.ID, job.WorkspaceID, models.RequirementJobRunning,
				job.AttemptCount)
			if err != nil {
				return nil, false, err
			}
			if count, _ := updated.RowsAffected(); count != 1 {
				return nil, false, ErrNoTaskAvailable
			}
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			return nil, true, nil
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE requirement_jobs
		SET status = ?, attempt_count = attempt_count + 1, started_at = COALESCE(started_at, ?),
		    lease_until = ?, updated_at = ?, error_code = NULL, error_message = NULL
		WHERE id = ? AND workspace_id = ? AND attempt_count < max_attempts
		  AND ((status = ? AND available_at <= ?) OR (status = ? AND lease_until <= ?))
	`, models.RequirementJobRunning, now, leaseUntil, now, job.ID, job.WorkspaceID,
		models.RequirementJobPending, now, models.RequirementJobRunning, now)
	if err != nil {
		return nil, false, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, false, ErrNoTaskAvailable
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	job.Status = models.RequirementJobRunning
	job.AttemptCount++
	job.StartedAt = firstTime(job.StartedAt, now)
	job.LeaseUntil = &leaseUntil
	var request any
	if err := s.openRequirementArtifact(job.WorkspaceID, "requirement-job", job.ID, "request", requestArtifact, job.RequestHash, &request); err != nil {
		return nil, false, err
	}
	job.Request = request
	return job, false, nil
}

func (s *Store) finalizeInterruptedRequirementAttempt(ctx context.Context, tx *sql.Tx, job *models.RequirementJob, now time.Time) error {
	if job == nil || job.AttemptCount <= 0 {
		return nil
	}
	var reportExists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM llm_attempt_reports
			WHERE workspace_id = ? AND job_type = ? AND job_id = ? AND attempt_number = ?
		)
	`, job.WorkspaceID, models.LLMJobTypeRequirement, job.ID, job.AttemptCount).Scan(&reportExists); err != nil {
		return err
	}
	if reportExists {
		return nil
	}

	rows, err := tx.QueryContext(ctx, llmProviderCallSelect+`
		WHERE workspace_id = ? AND job_type = ? AND job_id = ? AND attempt_number = ?
		ORDER BY call_index
	`, job.WorkspaceID, models.LLMJobTypeRequirement, job.ID, job.AttemptCount)
	if err != nil {
		return err
	}
	calls := []*models.LLMProviderCall{}
	for rows.Next() {
		call, scanErr := scanLLMProviderCall(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var recordingHash string
	if err := tx.QueryRowContext(ctx, `
		SELECT content_hash FROM recordings
		WHERE workspace_id = ? AND id = ? AND status != ?
	`, job.WorkspaceID, job.RecordingID, models.RecordingStatusDeleted).Scan(&recordingHash); err != nil {
		return err
	}
	report := &models.LLMAttemptReport{
		ID:              NewID(),
		WorkspaceID:     job.WorkspaceID,
		RecordingID:     job.RecordingID,
		JobType:         models.LLMJobTypeRequirement,
		JobID:           job.ID,
		AttemptNumber:   job.AttemptCount,
		Outcome:         models.LLMAttemptFailed,
		PromptVersion:   job.PromptVersion,
		RecordingHash:   recordingHash,
		RequirementHash: job.RequestHash,
		CallCount:       len(calls),
		ValidationPhase: "attempt-recovery",
		ErrorCode:       "ATTEMPT_INTERRUPTED",
		ErrorMessage:    "the worker lease expired before the attempt could be finalized",
		SafetyFlags:     []string{"attempt-recovery:lease-expired", "artifact-replayable:false"},
		Replayable:      false,
		CreatedAt:       now,
	}
	lineage := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		report.Provider = call.Provider
		report.Model = call.Model
		report.InputTokens += call.InputTokens
		report.OutputTokens += call.OutputTokens
		lineage = append(lineage, map[string]any{
			"id": call.ID, "callIndex": call.CallIndex, "callKind": call.CallKind,
			"providerAttempt": call.ProviderAttempt, "phase": call.Phase,
			"requestHash": call.RequestHash, "responseHash": call.ResponseHash,
		})
	}
	artifact := map[string]any{
		"schemaVersion": "aegiscrawler.requirement-attempt-interrupted.v1",
		"calls":         lineage,
		"validation": map[string]any{
			"phase": "attempt-recovery", "status": "failed",
			"code":    "ATTEMPT_INTERRUPTED",
			"message": "the worker lease expired before the attempt could be finalized",
		},
	}
	encrypted, hash, artifactBytes, err := s.sealBoundedLLMArtifact(
		report.WorkspaceID,
		"llm-attempt-report",
		report.ID,
		artifact,
		MaxLLMAttemptReportArtifactBytes,
	)
	if errors.Is(err, ErrLLMArtifactTooLarge) {
		artifact = map[string]any{
			"schemaVersion": "aegiscrawler.requirement-attempt-interrupted.v1",
			"calls": map[string]any{
				"count": len(calls), "firstId": calls[0].ID, "lastId": calls[len(calls)-1].ID,
			},
			"validation": map[string]any{
				"phase": "attempt-recovery", "status": "failed", "code": "ATTEMPT_INTERRUPTED",
			},
		}
		encrypted, hash, artifactBytes, err = s.sealBoundedLLMArtifact(
			report.WorkspaceID,
			"llm-attempt-report",
			report.ID,
			artifact,
			MaxLLMAttemptReportArtifactBytes,
		)
	}
	if err != nil {
		return err
	}
	report.Artifact = artifact
	report.ArtifactHash = hash
	report.ArtifactBytes = artifactBytes
	flags, err := json.Marshal(report.SafetyFlags)
	if err != nil {
		return err
	}
	return insertLLMAttemptReport(ctx, tx, report, &preparedLLMAttemptReport{
		encrypted:   encrypted,
		safetyFlags: string(flags),
	})
}

func firstTime(existing *time.Time, fallback time.Time) *time.Time {
	if existing != nil {
		return existing
	}
	return &fallback
}

type preparedRequirementCompletion struct {
	resultArtifact []byte
	resultHash     string
	safetyFlags    string
	now            time.Time
}

func (s *Store) prepareRequirementCompletion(job *models.RequirementJob, result any) (*preparedRequirementCompletion, error) {
	if job == nil || job.ID == "" || job.WorkspaceID == "" {
		return nil, ErrRequirementJobState
	}
	resultArtifact, resultHash, err := s.sealRequirementArtifact(job.WorkspaceID, "requirement-job", job.ID, "result", result)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	safetyFlags, err := json.Marshal(job.SafetyFlags)
	if err != nil {
		return nil, err
	}
	return &preparedRequirementCompletion{
		resultArtifact: resultArtifact,
		resultHash:     resultHash,
		safetyFlags:    string(safetyFlags),
		now:            now,
	}, nil
}

func completeRequirementJob(ctx context.Context, execer llmArtifactExecer, job *models.RequirementJob, prepared *preparedRequirementCompletion) error {
	updated, err := execer.ExecContext(ctx, `
		UPDATE requirement_jobs
		SET status = ?, result_artifact = ?, result_hash = ?, provider = NULLIF(?, ''),
		    model = NULLIF(?, ''), prompt_version = ?, chunk_count = ?, completed_chunks = ?,
		    input_tokens = ?,
		    output_tokens = ?, safety_flags = ?, requirement_id = NULLIF(?, ''),
		    error_code = NULL, error_message = NULL, lease_until = NULL,
		    completed_at = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ? AND attempt_count = ?
	`, models.RequirementJobCompleted, prepared.resultArtifact, prepared.resultHash, job.Provider, job.Model,
		job.PromptVersion, job.ChunkCount, job.CompletedChunks, job.InputTokens, job.OutputTokens, prepared.safetyFlags,
		job.RequirementID, prepared.now, prepared.now, job.ID, job.WorkspaceID, models.RequirementJobRunning,
		job.AttemptCount)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrRequirementJobState
	}
	return nil
}

// CompleteRequirementJob stores an encrypted result and terminal metadata.
func (s *Store) CompleteRequirementJob(ctx context.Context, job *models.RequirementJob, result any) error {
	prepared, err := s.prepareRequirementCompletion(job, result)
	if err != nil {
		return err
	}
	return completeRequirementJob(ctx, s.db, job, prepared)
}

// CompleteRequirementJobWithAttemptReport commits the successful provider
// attempt report and the corresponding terminal job result in one transaction.
// A failure in either write leaves the job running for lease recovery and
// prevents a report/job-state split brain.
func (s *Store) CompleteRequirementJobWithAttemptReport(
	ctx context.Context,
	job *models.RequirementJob,
	result any,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if report == nil || job == nil || report.JobType != models.LLMJobTypeRequirement ||
		report.JobID != job.ID || report.AttemptNumber != job.AttemptCount ||
		report.Outcome != models.LLMAttemptSucceeded {
		return ErrLLMArtifactInvalid
	}
	completion, err := s.prepareRequirementCompletion(job, result)
	if err != nil {
		return err
	}
	preparedReport, err := s.prepareLLMAttemptReport(ctx, report, reportArtifact)
	if err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := insertLLMAttemptReport(ctx, tx, report, preparedReport); err != nil {
			return err
		}
		return completeRequirementJob(ctx, tx, job, completion)
	})
}

// CompleteRequirementJobWithDraft atomically creates the immutable normalized
// draft and completes a deterministic/manual normalization job.
func (s *Store) CompleteRequirementJobWithDraft(
	ctx context.Context,
	job *models.RequirementJob,
	result any,
	requirement *models.CollectionRequirement,
) error {
	return s.completeRequirementJobWithDraftAndOptionalReport(
		ctx,
		job,
		result,
		requirement,
		nil,
		nil,
	)
}

// CompleteRequirementJobWithAttemptReportAndDraft atomically creates the
// normalized draft, records the provider attempt, and completes the job.
func (s *Store) CompleteRequirementJobWithAttemptReportAndDraft(
	ctx context.Context,
	job *models.RequirementJob,
	result any,
	requirement *models.CollectionRequirement,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	return s.completeRequirementJobWithDraftAndOptionalReport(
		ctx,
		job,
		result,
		requirement,
		report,
		reportArtifact,
	)
}

func (s *Store) completeRequirementJobWithDraftAndOptionalReport(
	ctx context.Context,
	job *models.RequirementJob,
	result any,
	requirement *models.CollectionRequirement,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if job == nil || requirement == nil ||
		requirement.SourceJobID != job.ID ||
		requirement.RecordingID != job.RecordingID ||
		requirement.ID == "" ||
		job.RequirementID != requirement.ID {
		return ErrRequirementState
	}
	if report != nil && (report.JobType != models.LLMJobTypeRequirement ||
		report.JobID != job.ID || report.AttemptNumber != job.AttemptCount ||
		report.Outcome != models.LLMAttemptSucceeded) {
		return ErrLLMArtifactInvalid
	}
	completion, err := s.prepareRequirementCompletion(job, result)
	if err != nil {
		return err
	}
	preparedRequirement, err := s.prepareCollectionRequirement(ctx, requirement)
	if err != nil {
		return err
	}
	var preparedReport *preparedLLMAttemptReport
	if report != nil {
		preparedReport, err = s.prepareLLMAttemptReport(ctx, report, reportArtifact)
		if err != nil {
			return err
		}
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := insertCollectionRequirement(ctx, tx, requirement, preparedRequirement); err != nil {
			return err
		}
		if report != nil {
			if err := insertLLMAttemptReport(ctx, tx, report, preparedReport); err != nil {
				return err
			}
		}
		return completeRequirementJob(ctx, tx, job, completion)
	})
}

// UpdateRequirementJobProgress records chunked analysis progress for a job
// that is currently running. Terminal or not-yet-claimed jobs are left
// untouched so completion metadata is never clobbered.
func (s *Store) UpdateRequirementJobProgress(ctx context.Context, id string, attemptNumber, total, completed int) error {
	now := time.Now().UTC()
	updated, err := s.db.ExecContext(ctx, `
		UPDATE requirement_jobs
		SET chunk_count = ?, completed_chunks = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ? AND attempt_count = ?
	`, total, completed, now, id, workspaceID(ctx), models.RequirementJobRunning, attemptNumber)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrRequirementJobState
	}
	return nil
}

type preparedRequirementFailure struct {
	status      models.RequirementJobStatus
	availableAt time.Time
	completedAt any
	now         time.Time
}

func prepareRequirementFailure(job *models.RequirementJob, retryDelay time.Duration) (*preparedRequirementFailure, error) {
	if job == nil || job.ID == "" || job.WorkspaceID == "" {
		return nil, ErrRequirementJobState
	}
	now := time.Now().UTC()
	status := models.RequirementJobFailed
	availableAt := now
	var completedAt any = now
	if job.AttemptCount < job.MaxAttempts {
		status = models.RequirementJobPending
		if retryDelay < 0 {
			retryDelay = 0
		}
		availableAt = now.Add(retryDelay)
		completedAt = nil
	}
	return &preparedRequirementFailure{
		status:      status,
		availableAt: availableAt,
		completedAt: completedAt,
		now:         now,
	}, nil
}

func failRequirementJob(ctx context.Context, execer llmArtifactExecer, job *models.RequirementJob, code, message string, prepared *preparedRequirementFailure) error {
	updated, err := execer.ExecContext(ctx, `
		UPDATE requirement_jobs
		SET status = ?, available_at = ?, lease_until = NULL, error_code = ?, error_message = ?,
		    completed_at = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ? AND attempt_count = ?
	`, prepared.status, prepared.availableAt, code, message, prepared.completedAt, prepared.now,
		job.ID, job.WorkspaceID, models.RequirementJobRunning, job.AttemptCount)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return ErrRequirementJobState
	}
	return nil
}

// FailRequirementJob schedules bounded backoff or records a terminal safe error.
func (s *Store) FailRequirementJob(ctx context.Context, job *models.RequirementJob, code, message string, retryDelay time.Duration) error {
	prepared, err := prepareRequirementFailure(job, retryDelay)
	if err != nil {
		return err
	}
	return failRequirementJob(ctx, s.db, job, code, message, prepared)
}

// FailRequirementJobWithAttemptReport commits one failed provider attempt and
// the matching retry/terminal job transition atomically.
func (s *Store) FailRequirementJobWithAttemptReport(
	ctx context.Context,
	job *models.RequirementJob,
	code string,
	message string,
	retryDelay time.Duration,
	report *models.LLMAttemptReport,
	reportArtifact any,
) error {
	if report == nil || job == nil || report.JobType != models.LLMJobTypeRequirement ||
		report.JobID != job.ID || report.AttemptNumber != job.AttemptCount ||
		report.Outcome != models.LLMAttemptFailed {
		return ErrLLMArtifactInvalid
	}
	failure, err := prepareRequirementFailure(job, retryDelay)
	if err != nil {
		return err
	}
	preparedReport, err := s.prepareLLMAttemptReport(ctx, report, reportArtifact)
	if err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := insertLLMAttemptReport(ctx, tx, report, preparedReport); err != nil {
			return err
		}
		return failRequirementJob(ctx, tx, job, code, message, failure)
	})
}

// RetryRequirementJob makes a terminal failed job runnable again while
// preserving a monotonically increasing attempt sequence for immutable
// provider-attempt artifacts. Each manual retry grants another copy of the
// original bounded attempt budget.
func (s *Store) RetryRequirementJob(ctx context.Context, id string) error {
	now := time.Now().UTC()
	updated, err := s.db.ExecContext(ctx, `
		UPDATE requirement_jobs
		SET status = ?, max_attempts = attempt_count + attempt_budget,
		    available_at = ?, lease_until = NULL,
		    error_code = NULL, error_message = NULL, completed_at = NULL, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ?
	`, models.RequirementJobPending, now, now, id, workspaceID(ctx), models.RequirementJobFailed)
	if err != nil {
		return err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM requirement_jobs WHERE id = ? AND workspace_id = ?)`, id, workspaceID(ctx)).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrRequirementJobNotFound
		}
		return ErrRequirementJobState
	}
	return nil
}

type preparedCollectionRequirement struct {
	artifact []byte
}

func (s *Store) prepareCollectionRequirement(ctx context.Context, requirement *models.CollectionRequirement) (*preparedCollectionRequirement, error) {
	if requirement == nil || requirement.RecordingID == "" {
		return nil, ErrRecordingNotFound
	}
	requirement.WorkspaceID = workspaceID(ctx)
	if requirement.ID == "" {
		requirement.ID = NewID()
	}
	if requirement.Status == "" {
		requirement.Status = models.CollectionRequirementDraft
	}
	if requirement.Source == "" {
		requirement.Source = models.RequirementSourceLLM
	}
	if requirement.Owner == "" {
		requirement.Owner = authz.Subject(ctx, "system")
	}
	now := time.Now().UTC()
	if requirement.CreatedAt.IsZero() {
		requirement.CreatedAt = now
	}
	requirement.UpdatedAt = now
	artifact, hash, err := s.sealRequirementArtifact(requirement.WorkspaceID, "collection-requirement", requirement.ID, "content", requirement.Requirement)
	if err != nil {
		return nil, err
	}
	requirement.ContentHash = hash
	return &preparedCollectionRequirement{artifact: artifact}, nil
}

func insertCollectionRequirement(ctx context.Context, execer llmArtifactExecer, requirement *models.CollectionRequirement, prepared *preparedCollectionRequirement) error {
	if requirement == nil || prepared == nil {
		return ErrRequirementState
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO collection_requirements (
			id, workspace_id, recording_id, source_job_id, source, status,
			content_artifact, content_hash, owner, created_at, updated_at
		) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)
	`, requirement.ID, requirement.WorkspaceID, requirement.RecordingID, requirement.SourceJobID,
		requirement.Source, requirement.Status, prepared.artifact, requirement.ContentHash, requirement.Owner,
		requirement.CreatedAt, requirement.UpdatedAt)
	return err
}

// CreateCollectionRequirement stores an immutable encrypted draft.
func (s *Store) CreateCollectionRequirement(ctx context.Context, requirement *models.CollectionRequirement) error {
	prepared, err := s.prepareCollectionRequirement(ctx, requirement)
	if err != nil {
		return err
	}
	return insertCollectionRequirement(ctx, s.db, requirement, prepared)
}

func (s *Store) GetCollectionRequirement(ctx context.Context, id string) (*models.CollectionRequirement, error) {
	requirement := &models.CollectionRequirement{}
	var artifact []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, recording_id, COALESCE(source_job_id, ''), source, status,
		       content_artifact, content_hash, owner, created_at, updated_at,
		       confirmed_at, COALESCE(confirmed_by, '')
		FROM collection_requirements WHERE id = ? AND workspace_id = ?
	`, id, workspaceID(ctx)).Scan(
		&requirement.ID, &requirement.WorkspaceID, &requirement.RecordingID, &requirement.SourceJobID,
		&requirement.Source, &requirement.Status, &artifact, &requirement.ContentHash, &requirement.Owner,
		&requirement.CreatedAt, &requirement.UpdatedAt, &requirement.ConfirmedAt, &requirement.ConfirmedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequirementNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.openRequirementArtifact(requirement.WorkspaceID, "collection-requirement", requirement.ID, "content", artifact, requirement.ContentHash, &requirement.Requirement); err != nil {
		return nil, err
	}
	return requirement, nil
}

func (s *Store) GetCollectionRequirementByJob(ctx context.Context, jobID string) (*models.CollectionRequirement, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM collection_requirements WHERE source_job_id = ? AND workspace_id = ?`, jobID, workspaceID(ctx)).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequirementNotFound
		}
		return nil, err
	}
	return s.GetCollectionRequirement(ctx, id)
}

func (s *Store) ConfirmCollectionRequirement(ctx context.Context, id string) (*models.CollectionRequirement, error) {
	now := time.Now().UTC()
	actor := authz.Subject(ctx, "system")
	updated, err := s.db.ExecContext(ctx, `
		UPDATE collection_requirements
		SET status = ?, confirmed_at = ?, confirmed_by = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = ?
	`, models.CollectionRequirementConfirmed, now, actor, now, id, workspaceID(ctx), models.CollectionRequirementDraft)
	if err != nil {
		return nil, err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		current, err := s.GetCollectionRequirement(ctx, id)
		if err != nil {
			return nil, err
		}
		// Confirming an already-confirmed requirement is idempotent: the
		// wizard (or any client) may retry after navigating back. Returning
		// the confirmed requirement lets clients resume instead of dead-
		// ending on a state error. Any other non-draft status stays fail-closed.
		if current.Status == models.CollectionRequirementConfirmed {
			return current, nil
		}
		return nil, ErrRequirementState
	}
	return s.GetCollectionRequirement(ctx, id)
}

const requirementJobSelect = `
	SELECT id, workspace_id, recording_id, kind, status, source, request_artifact,
	       result_artifact, request_hash, COALESCE(result_hash, ''), COALESCE(provider, ''),
	       COALESCE(model, ''), prompt_version, chunk_count, completed_chunks, input_tokens, output_tokens,
	       attempt_count, max_attempts, attempt_budget, available_at, lease_until, COALESCE(error_code, ''),
	       COALESCE(error_message, ''), safety_flags, COALESCE(requirement_id, ''),
	       created_at, updated_at, started_at, completed_at
	FROM requirement_jobs`

type requirementJobScanner interface {
	Scan(...any) error
}

func (s *Store) scanRequirementJob(scanner requirementJobScanner) (*models.RequirementJob, []byte, []byte, error) {
	job := &models.RequirementJob{}
	var requestArtifact, resultArtifact []byte
	var safetyFlags string
	err := scanner.Scan(
		&job.ID, &job.WorkspaceID, &job.RecordingID, &job.Kind, &job.Status, &job.Source,
		&requestArtifact, &resultArtifact, &job.RequestHash, &job.ResultHash, &job.Provider,
		&job.Model, &job.PromptVersion, &job.ChunkCount, &job.CompletedChunks, &job.InputTokens, &job.OutputTokens,
		&job.AttemptCount, &job.MaxAttempts, &job.AttemptBudget, &job.AvailableAt, &job.LeaseUntil, &job.ErrorCode,
		&job.ErrorMessage, &safetyFlags, &job.RequirementID, &job.CreatedAt, &job.UpdatedAt,
		&job.StartedAt, &job.CompletedAt,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := json.Unmarshal([]byte(safetyFlags), &job.SafetyFlags); err != nil {
		return nil, nil, nil, fmt.Errorf("decode requirement safety flags: %w", err)
	}
	if job.SafetyFlags == nil {
		job.SafetyFlags = []string{}
	}
	return job, requestArtifact, resultArtifact, nil
}
