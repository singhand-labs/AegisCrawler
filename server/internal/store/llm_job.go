package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

// CreateLLMJob inserts a new LLM enhancement job.
func (s *Store) CreateLLMJob(ctx context.Context, job *models.LLMJob) error {
	job.WorkspaceID = workspaceID(ctx)
	if job.ID == "" {
		job.ID = NewID()
	}
	if job.Status == "" {
		job.Status = string(models.LLMJobStatusPending)
	}
	if len(job.Baseline) == 0 {
		job.Baseline = models.JSON("{}")
	}
	if len(job.Recording) == 0 {
		job.Recording = models.JSON("{}")
	}
	if len(job.ResultRule) == 0 {
		job.ResultRule = models.JSON("{}")
	}
	if len(job.ResultPatch) == 0 {
		job.ResultPatch = models.JSON("{}")
	}
	if len(job.SafetyFlags) == 0 {
		job.SafetyFlags = models.JSON("[]")
	}
	if len(job.Suggestions) == 0 {
		job.Suggestions = models.JSON("[]")
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}

	const q = `
		INSERT INTO llm_jobs (
			id, workspace_id, rule_id, baseline, recording, user_hint, status,
			result_rule, result_patch, result_error,
			provider, model, input_tokens, output_tokens,
			safety_flags, suggestions,
			created_at, started_at, completed_at, attempt_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, q,
		job.ID, job.WorkspaceID, job.RuleID, job.Baseline, job.Recording, job.UserHint, job.Status,
		job.ResultRule, job.ResultPatch, job.ResultError,
		job.Provider, job.Model, job.InputTokens, job.OutputTokens,
		job.SafetyFlags, job.Suggestions,
		job.CreatedAt, job.StartedAt, job.CompletedAt, job.AttemptCount,
	)
	return err
}

// GetLLMJob returns an LLM job by id.
func (s *Store) GetLLMJob(ctx context.Context, id string) (*models.LLMJob, error) {
	const q = `
		SELECT
			id, workspace_id, rule_id, baseline, recording, user_hint, status,
			result_rule, result_patch, result_error,
			provider, model, input_tokens, output_tokens,
			safety_flags, suggestions,
			created_at, started_at, completed_at, attempt_count
		FROM llm_jobs WHERE id = ? AND workspace_id = ?
	`
	row := s.db.QueryRowContext(ctx, q, id, workspaceID(ctx))
	job := &models.LLMJob{}
	err := row.Scan(
		&job.ID, &job.WorkspaceID, &job.RuleID, &job.Baseline, &job.Recording, &job.UserHint, &job.Status,
		&job.ResultRule, &job.ResultPatch, &job.ResultError,
		&job.Provider, &job.Model, &job.InputTokens, &job.OutputTokens,
		&job.SafetyFlags, &job.Suggestions,
		&job.CreatedAt, &job.StartedAt, &job.CompletedAt, &job.AttemptCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuleNotFound
	}
	return job, err
}

// UpdateLLMJobStatus persists the current state and result of a job.
func (s *Store) UpdateLLMJobStatus(ctx context.Context, job *models.LLMJob) error {
	const q = `
		UPDATE llm_jobs SET
			status = ?, result_rule = ?, result_patch = ?, result_error = ?,
			provider = ?, model = ?, input_tokens = ?, output_tokens = ?,
			safety_flags = ?, suggestions = ?,
			started_at = ?, completed_at = ?, attempt_count = ?
		WHERE id = ? AND workspace_id = ?
	`
	_, err := s.db.ExecContext(ctx, q,
		job.Status, job.ResultRule, job.ResultPatch, job.ResultError,
		job.Provider, job.Model, job.InputTokens, job.OutputTokens,
		job.SafetyFlags, job.Suggestions,
		job.StartedAt, job.CompletedAt, job.AttemptCount,
		job.ID, workspaceID(ctx),
	)
	return err
}

// ClaimPendingLLMJob atomically claims the oldest pending LLM job and transitions
// it to running. Returns ErrNoTaskAvailable when no pending job exists.
func (s *Store) ClaimPendingLLMJob(ctx context.Context) (*models.LLMJob, error) {
	workspace := workspaceID(ctx)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	const q = `SELECT id FROM llm_jobs WHERE workspace_id = ? AND status = ? ORDER BY created_at ASC LIMIT 1`
	var id string
	if err := tx.QueryRowContext(ctx, q, workspace, string(models.LLMJobStatusPending)).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoTaskAvailable
		}
		return nil, err
	}

	now := time.Now().UTC()
	const update = `UPDATE llm_jobs SET status = ?, started_at = ?, attempt_count = attempt_count + 1 WHERE id = ? AND workspace_id = ?`
	if _, err := tx.ExecContext(ctx, update, string(models.LLMJobStatusRunning), now, id, workspace); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetLLMJob(ctx, id)
}

// ListPendingLLMJobs returns pending LLM jobs ordered by creation time.
func (s *Store) ListPendingLLMJobs(ctx context.Context, limit int) ([]*models.LLMJob, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		SELECT
			id, workspace_id, rule_id, baseline, recording, user_hint, status,
			result_rule, result_patch, result_error,
			provider, model, input_tokens, output_tokens,
			safety_flags, suggestions,
			created_at, started_at, completed_at, attempt_count
		FROM llm_jobs
		WHERE workspace_id = ? AND status = ?
		ORDER BY created_at ASC
		LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, q, workspaceID(ctx), string(models.LLMJobStatusPending), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*models.LLMJob
	for rows.Next() {
		job := &models.LLMJob{}
		if err := rows.Scan(
			&job.ID, &job.WorkspaceID, &job.RuleID, &job.Baseline, &job.Recording, &job.UserHint, &job.Status,
			&job.ResultRule, &job.ResultPatch, &job.ResultError,
			&job.Provider, &job.Model, &job.InputTokens, &job.OutputTokens,
			&job.SafetyFlags, &job.Suggestions,
			&job.CreatedAt, &job.StartedAt, &job.CompletedAt, &job.AttemptCount,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
