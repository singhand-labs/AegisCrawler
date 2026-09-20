package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	rulecontract "github.com/singhand-labs/AegisCrawler/internal/rule"
)

var (
	ErrRuleVersionNotApproved   = errors.New("approved rule version not found")
	ErrExecutionAttemptNotFound = errors.New("execution attempt not found")
	ErrExecutionAttemptConflict = errors.New("execution attempt is not active for this worker")
	ErrResultConflict           = errors.New("result idempotency or sequence conflict")
	ErrIncompleteAttemptResults = errors.New("execution attempt results are incomplete")
)

// UpdateTaskStatusForAttempt applies an attempt-bound status transition. Exact
// retries of an already committed state are acknowledged. A successful
// completion is committed only when the same transaction observes at least
// one valid batch, exactly one summary, and no invalid batches.
func (s *Store) UpdateTaskStatusForAttempt(
	ctx context.Context,
	taskID, workerID, attemptID, status, message, errorType string,
) (bool, error) {
	if taskID == "" || workerID == "" || attemptID == "" {
		return false, ErrExecutionAttemptConflict
	}
	workspace := workspaceID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var taskStatus, taskWorker, currentAttempt string
	var attemptWorker string
	var attemptStatus models.ExecutionAttemptStatus
	err = tx.QueryRowContext(ctx, `
		SELECT t.status, COALESCE(t.worker_id, ''), COALESCE(t.current_attempt_id, ''),
		       a.worker_id, a.status
		FROM tasks t
		JOIN execution_attempts a ON a.workspace_id = t.workspace_id AND a.task_id = t.id
		WHERE t.workspace_id = ? AND t.id = ? AND a.id = ?
	`, workspace, taskID, attemptID).Scan(
		&taskStatus, &taskWorker, &currentAttempt, &attemptWorker, &attemptStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrExecutionAttemptConflict
	}
	if err != nil {
		return false, err
	}
	if taskWorker != workerID || attemptWorker != workerID || currentAttempt != attemptID {
		return false, ErrExecutionAttemptConflict
	}

	targetAttemptStatus := models.ExecutionAttemptRunning
	terminal := false
	mayCloseHumanWait := false
	switch models.TaskStatus(status) {
	case models.TaskStatusRunning:
		targetAttemptStatus = models.ExecutionAttemptRunning
	case models.TaskStatusDone:
		targetAttemptStatus = models.ExecutionAttemptSucceeded
		terminal = true
	case models.TaskStatusFailed:
		targetAttemptStatus = models.ExecutionAttemptFailed
		terminal = true
		mayCloseHumanWait = true
	case models.TaskStatusCancelled:
		targetAttemptStatus = models.ExecutionAttemptCancelled
		terminal = true
		mayCloseHumanWait = true
	case models.TaskStatusDeadLetter:
		targetAttemptStatus = models.ExecutionAttemptDeadLetter
		terminal = true
		mayCloseHumanWait = true
	default:
		return false, ErrExecutionAttemptConflict
	}
	if taskStatus == status && attemptStatus == targetAttemptStatus {
		return true, nil
	}
	activeTask := taskStatus == string(models.TaskStatusLeased) || taskStatus == string(models.TaskStatusRunning)
	if taskStatus == string(models.TaskStatusWaitingHuman) && mayCloseHumanWait {
		activeTask = true
	}
	activeAttempt := attemptStatus == models.ExecutionAttemptLeased || attemptStatus == models.ExecutionAttemptRunning
	if attemptStatus == models.ExecutionAttemptWaitingHuman && mayCloseHumanWait {
		activeAttempt = true
	}
	if !activeTask || !activeAttempt {
		return false, ErrExecutionAttemptConflict
	}

	if status == string(models.TaskStatusDone) {
		var validBatches, invalidBatches, summaries int
		if err := tx.QueryRowContext(ctx, `
			SELECT
				COALESCE(SUM(CASE WHEN kind = 'batch' AND valid = 1 THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN kind = 'batch' AND valid = 0 THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN kind = 'summary' THEN 1 ELSE 0 END), 0)
			FROM results
			WHERE workspace_id = ? AND task_id = ? AND attempt_id = ?
		`, workspace, taskID, attemptID).Scan(&validBatches, &invalidBatches, &summaries); err != nil {
			return false, err
		}
		if validBatches < 1 || invalidBatches != 0 || summaries != 1 {
			return false, fmt.Errorf("%w: require at least one valid batch, exactly one summary, and zero invalid batches (valid=%d summary=%d invalid=%d)",
				ErrIncompleteAttemptResults, validBatches, summaries, invalidBatches)
		}
	}

	now := time.Now().UTC()
	var completedAt any
	if terminal {
		completedAt = now
	}
	releaseLease := terminal
	res, err := tx.ExecContext(ctx, `
		UPDATE tasks SET status = ?, updated_at = ?, completed_at = COALESCE(?, completed_at),
			error_type = COALESCE(NULLIF(?, ''), error_type),
			error_message = COALESCE(NULLIF(?, ''), error_message),
			lease_until = CASE WHEN ? THEN NULL ELSE lease_until END
		WHERE id = ? AND workspace_id = ? AND worker_id = ? AND current_attempt_id = ?
	`, status, now, completedAt, errorType, message, releaseLease,
		taskID, workspace, workerID, attemptID)
	if err != nil {
		return false, err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return false, ErrExecutionAttemptConflict
	}
	if mayCloseHumanWait {
		if _, err := tx.ExecContext(ctx, `UPDATE human_interventions
			SET status = 'cancelled', decided_at = ?, decided_by = 'system:worker_terminal',
			    decision_note = 'worker ended the attempt while waiting for operator'
			WHERE workspace_id = ? AND task_id = ? AND attempt_id = ? AND status = 'pending'`,
			now, workspace, taskID, attemptID); err != nil {
			return false, err
		}
	}
	res, err = tx.ExecContext(ctx, `UPDATE execution_attempts
		SET status = ?, completed_at = CASE WHEN ? THEN ? ELSE completed_at END,
		    error_type = CASE WHEN ? THEN ? ELSE error_type END,
		    error_message = CASE WHEN ? THEN ? ELSE error_message END,
		    lease_until = CASE WHEN ? THEN NULL ELSE lease_until END
		WHERE workspace_id = ? AND id = ? AND task_id = ? AND worker_id = ?`,
		targetAttemptStatus, terminal, now, terminal, errorType, terminal, message,
		releaseLease, workspace, attemptID, taskID, workerID)
	if err != nil {
		return false, err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return false, ErrExecutionAttemptConflict
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) GetRuleVersionContract(ctx context.Context, ruleID string, version int) (*models.RuleVersionContract, error) {
	return getRuleVersionContractWithExec(ctx, s.db, workspaceID(ctx), ruleID, version)
}

func getRuleVersionContractWithExec(ctx context.Context, ex ruleVersionExecer, workspace, ruleID string, version int) (*models.RuleVersionContract, error) {
	contract := &models.RuleVersionContract{}
	err := ex.QueryRowContext(ctx, `
		SELECT workspace_id, rule_id, version_number, input_schema, output_schema,
		       browser_profile_id, COALESCE(source_kind, ''),
		       COALESCE(source_authority, ''), COALESCE(source_artifact_hash, ''),
		       COALESCE(source_export_hash, ''), COALESCE(source_workflow_id, ''), created_at
		FROM rule_version_contracts
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, workspace, ruleID, version).Scan(
		&contract.WorkspaceID, &contract.RuleID, &contract.Version,
		&contract.InputSchema, &contract.OutputSchema, &contract.BrowserProfileID,
		&contract.SourceKind, &contract.SourceAuthority, &contract.SourceArtifactHash,
		&contract.SourceExportHash, &contract.SourceWorkflowID,
		&contract.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuleVersionNotFound
	}
	return contract, err
}

// ResolveTaskRuleVersionContract returns the immutable rule version and
// execution contract bound to task. A task is treated as legacy only when no
// immutable rule-version row exists; once a version exists, its contract is
// required and all lookup errors are propagated.
func (s *Store) ResolveTaskRuleVersionContract(ctx context.Context, task *models.Task) (*models.RuleVersion, *models.RuleVersionContract, error) {
	return resolveTaskRuleVersionContract(ctx, s.db, workspaceID(ctx), task)
}

func resolveTaskRuleVersionContract(ctx context.Context, ex ruleVersionExecer, workspace string, task *models.Task) (*models.RuleVersion, *models.RuleVersionContract, error) {
	if task == nil || task.RuleID == "" {
		return nil, nil, nil
	}
	if task.RuleVersionNumber > 0 {
		version, err := getRuleVersion(ctx, ex, workspace, task.RuleID, task.RuleVersionNumber)
		if err == nil {
			contract, contractErr := getRuleVersionContractWithExec(ctx, ex, workspace, task.RuleID, task.RuleVersionNumber)
			if contractErr != nil {
				return nil, nil, contractErr
			}
			return version, contract, nil
		}
		if !errors.Is(err, ErrRuleVersionNotFound) {
			return nil, nil, err
		}
	}

	var versionExists bool
	if err := ex.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM rule_versions
			WHERE workspace_id = ? AND rule_id = ?
		)
	`, workspace, task.RuleID).Scan(&versionExists); err != nil {
		return nil, nil, err
	}
	if versionExists {
		return nil, nil, ErrRuleVersionNotFound
	}
	return nil, nil, nil
}

func insertRuleVersionContractWithExec(ctx context.Context, ex execer, contract *models.RuleVersionContract) error {
	if contract == nil {
		return errors.New("rule version contract is required")
	}
	if len(contract.InputSchema) == 0 {
		contract.InputSchema = models.JSON(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)
	}
	if len(contract.OutputSchema) == 0 {
		contract.OutputSchema = models.JSON(`{"type":"object","properties":{},"additionalProperties":true}`)
	}
	if contract.CreatedAt.IsZero() {
		contract.CreatedAt = time.Now().UTC()
	}
	_, err := ex.ExecContext(ctx, `
		INSERT INTO rule_version_contracts (
			workspace_id, rule_id, version_number, input_schema, output_schema,
			browser_profile_id, source_kind, source_authority, source_artifact_hash,
			source_export_hash, source_workflow_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, contract.WorkspaceID, contract.RuleID, contract.Version, contract.InputSchema,
		contract.OutputSchema, contract.BrowserProfileID, contract.SourceKind,
		contract.SourceAuthority, contract.SourceArtifactHash, contract.SourceExportHash,
		contract.SourceWorkflowID, contract.CreatedAt)
	return err
}

// BindTaskToRuleVersion resolves an approved immutable version, validates the
// supplied inputs, and copies the version contract onto the task snapshot.
func (s *Store) BindTaskToRuleVersion(ctx context.Context, task *models.Task, requestedVersion int) error {
	if task == nil || task.RuleID == "" {
		return ErrRuleNotFound
	}
	workspace := workspaceID(ctx)
	version := requestedVersion
	if version <= 0 {
		query := `SELECT version_number FROM rule_versions
			WHERE workspace_id = ? AND rule_id = ? AND approval_status = ?`
		args := []any{workspace, task.RuleID, models.RuleApprovalApproved}
		if task.RuleVersion != "" {
			query += ` AND version_label = ?`
			args = append(args, task.RuleVersion)
		}
		query += ` ORDER BY version_number DESC LIMIT 1`
		if err := s.db.QueryRowContext(ctx, query, args...).Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRuleVersionNotApproved
			}
			return err
		}
	}
	versionRecord, err := s.GetRuleVersion(ctx, task.RuleID, version)
	if err != nil {
		if errors.Is(err, ErrRuleVersionNotFound) {
			return ErrRuleVersionNotApproved
		}
		return err
	}
	if versionRecord.Status != models.RuleApprovalApproved {
		return ErrRuleVersionNotApproved
	}
	contract, err := s.GetRuleVersionContract(ctx, task.RuleID, version)
	if err != nil {
		return err
	}
	normalized, err := rulecontract.ApplyAndValidateTaskInputs(contract.InputSchema, task.Variables)
	if err != nil {
		return err
	}
	task.WorkspaceID = workspace
	task.RuleVersion = versionRecord.VersionLabel
	task.RuleVersionNumber = version
	task.Variables = normalized
	task.InputSchema = contract.InputSchema
	task.OutputSchema = contract.OutputSchema
	if task.BrowserProfileID == "" {
		task.BrowserProfileID = contract.BrowserProfileID
	}
	return nil
}

func (s *Store) GetExecutionAttempt(ctx context.Context, id string) (*models.ExecutionAttempt, error) {
	attempt := &models.ExecutionAttempt{}
	var leaseUntil sql.NullTime
	var completedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, task_id, attempt_number, worker_id, status,
		       lease_until, started_at, completed_at, error_type, error_message
		FROM execution_attempts WHERE id = ? AND workspace_id = ?
	`, id, workspaceID(ctx)).Scan(
		&attempt.ID, &attempt.WorkspaceID, &attempt.TaskID, &attempt.Number,
		&attempt.WorkerID, &attempt.Status, &leaseUntil, &attempt.StartedAt,
		&completedAt, &attempt.ErrorType, &attempt.ErrorMessage,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrExecutionAttemptNotFound
	}
	if err != nil {
		return nil, err
	}
	if leaseUntil.Valid {
		attempt.LeaseUntil = &leaseUntil.Time
	}
	if completedAt.Valid {
		attempt.CompletedAt = &completedAt.Time
	}
	return attempt, nil
}

// InsertResultIdempotent stores valid and invalid result submissions. An exact
// retry is acknowledged without creating another row; key/sequence reuse with
// different content is rejected.
func (s *Store) InsertResultIdempotent(ctx context.Context, result *models.Result) (bool, error) {
	if result == nil || result.TaskID == "" || result.WorkerID == "" || result.AttemptID == "" || result.IdempotencyKey == "" || result.Sequence <= 0 {
		return false, ErrResultConflict
	}
	if result.Kind != models.ResultKindBatch && result.Kind != models.ResultKindSummary {
		return false, ErrResultConflict
	}
	workspace := workspaceID(ctx)
	result.WorkspaceID = workspace
	digest := sha256.Sum256(result.Payload)
	result.PayloadHash = fmt.Sprintf("%x", digest)
	if result.ID == "" {
		result.ID = NewID()
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now().UTC()
	}

	var existingHash string
	var existingSequence int
	var existingKind models.ResultKind
	err := s.db.QueryRowContext(ctx, `
		SELECT payload_hash, sequence, kind FROM results
		WHERE workspace_id = ? AND task_id = ? AND attempt_id = ? AND idempotency_key = ?
	`, workspace, result.TaskID, result.AttemptID, result.IdempotencyKey).Scan(
		&existingHash, &existingSequence, &existingKind,
	)
	if err == nil {
		if existingHash == result.PayloadHash && existingSequence == result.Sequence && existingKind == result.Kind {
			return true, nil
		}
		return false, ErrResultConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	var active int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM execution_attempts a
		JOIN tasks t ON t.workspace_id = a.workspace_id AND t.id = a.task_id
		WHERE a.workspace_id = ? AND a.id = ? AND a.task_id = ? AND a.worker_id = ?
		  AND t.current_attempt_id = a.id
		  AND a.status IN ('leased','running','waiting_for_human')
	`, workspace, result.AttemptID, result.TaskID, result.WorkerID).Scan(&active); err != nil {
		return false, err
	}
	if active != 1 {
		return false, ErrExecutionAttemptConflict
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO results (
			id, workspace_id, task_id, worker_id, attempt_id, idempotency_key,
			sequence, kind, payload, payload_hash, valid, validation_error,
			immediate, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, result.ID, workspace, result.TaskID, result.WorkerID, result.AttemptID,
		result.IdempotencyKey, result.Sequence, result.Kind, result.Payload,
		result.PayloadHash, result.Valid, result.ValidationError, result.Immediate,
		result.CreatedAt)
	if err != nil {
		if isConstraintMessage(err, "unique") {
			return false, ErrResultConflict
		}
		return false, err
	}
	return false, nil
}

func (s *Store) ListResultPage(ctx context.Context, taskID string, limit, offset int, includeInvalid bool) (*models.ResultPage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	if err := s.ensureTaskInWorkspace(ctx, taskID); err != nil {
		return nil, err
	}
	workspace := workspaceID(ctx)
	page := &models.ResultPage{Batches: []*models.Result{}, InvalidBatches: []*models.Result{}, Limit: limit, Offset: offset}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM results
		WHERE workspace_id = ? AND task_id = ? AND kind = 'batch' AND valid = 1`, workspace, taskID).Scan(&page.Total); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.workspace_id, r.task_id, r.worker_id, r.attempt_id, r.idempotency_key,
		       r.sequence, r.kind, r.payload, r.payload_hash, r.valid, r.validation_error,
		       r.immediate, r.created_at
		FROM results r
		JOIN execution_attempts a ON a.workspace_id = r.workspace_id AND a.id = r.attempt_id
		WHERE r.workspace_id = ? AND r.task_id = ? AND r.kind = 'batch' AND r.valid = 1
		ORDER BY a.attempt_number ASC, r.sequence ASC, r.created_at ASC LIMIT ? OFFSET ?
	`, workspace, taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	page.Batches, err = scanResults(rows)
	if err != nil {
		return nil, err
	}
	if includeInvalid {
		invalidRows, err := s.db.QueryContext(ctx, `
			SELECT id, workspace_id, task_id, worker_id, attempt_id, idempotency_key,
			       sequence, kind, payload, payload_hash, valid, validation_error,
			       immediate, created_at
			FROM results WHERE workspace_id = ? AND task_id = ? AND kind = 'batch' AND valid = 0
			ORDER BY created_at ASC LIMIT ? OFFSET ?
		`, workspace, taskID, limit, offset)
		if err != nil {
			return nil, err
		}
		page.InvalidBatches, err = scanResults(invalidRows)
		if err != nil {
			return nil, err
		}
	}
	summaryRows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, task_id, worker_id, attempt_id, idempotency_key,
		       sequence, kind, payload, payload_hash, valid, validation_error,
		       immediate, created_at
		FROM results WHERE workspace_id = ? AND task_id = ? AND kind = 'summary'
		ORDER BY created_at DESC LIMIT 1
	`, workspace, taskID)
	if err != nil {
		return nil, err
	}
	summaries, err := scanResults(summaryRows)
	if err != nil {
		return nil, err
	}
	if len(summaries) > 0 {
		page.Summary = summaries[0]
	}
	return page, nil
}

func scanResults(rows *sql.Rows) ([]*models.Result, error) {
	defer rows.Close()
	results := []*models.Result{}
	for rows.Next() {
		result := &models.Result{}
		if err := rows.Scan(
			&result.ID, &result.WorkspaceID, &result.TaskID, &result.WorkerID,
			&result.AttemptID, &result.IdempotencyKey, &result.Sequence, &result.Kind,
			&result.Payload, &result.PayloadHash, &result.Valid, &result.ValidationError,
			&result.Immediate, &result.CreatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}
