package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var (
	ErrHumanInterventionNotFound = errors.New("human intervention not found")
	ErrHumanInterventionConflict = errors.New("human intervention is not active for this attempt")
	ErrHumanCheckpointConflict   = errors.New("human intervention checkpoint does not match")
	ErrHumanInterventionExpired  = errors.New("human intervention has expired")
)

type HumanInterventionInput struct {
	ID              string
	TaskID          string
	AttemptID       string
	WorkerID        string
	CheckpointID    string
	CheckpointName  string
	Checkpoint      models.JSON
	Type            string
	Prompt          string
	RequestedAction string
	TargetOrigin    string
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// CreateHumanIntervention atomically stores the sanitized checkpoint, creates
// the pending decision, and moves the same task/attempt into waiting state.
func (s *Store) CreateHumanIntervention(ctx context.Context, input HumanInterventionInput) (*models.HumanIntervention, error) {
	workspace := workspaceID(ctx)
	if input.ID == "" || input.CheckpointID == "" || input.TaskID == "" || input.AttemptID == "" || input.WorkerID == "" {
		return nil, ErrHumanInterventionConflict
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	if !input.ExpiresAt.After(input.CreatedAt) {
		return nil, ErrHumanInterventionExpired
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var ruleID, browserProfileID string
	var ruleVersionNumber int
	if err := tx.QueryRowContext(ctx, `
		SELECT t.rule_id, t.rule_version_number, t.browser_profile_id
		FROM tasks t
		JOIN execution_attempts a ON a.workspace_id = t.workspace_id AND a.id = t.current_attempt_id
		WHERE t.workspace_id = ? AND t.id = ? AND t.current_attempt_id = ?
		  AND t.worker_id = ? AND t.status IN ('leased','running')
		  AND a.task_id = t.id AND a.worker_id = ? AND a.status IN ('leased','running')
	`, workspace, input.TaskID, input.AttemptID, input.WorkerID, input.WorkerID).Scan(
		&ruleID, &ruleVersionNumber, &browserProfileID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHumanInterventionConflict
		}
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO checkpoints (id, workspace_id, task_id, worker_id, name, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, input.CheckpointID, workspace, input.TaskID, input.WorkerID,
		input.CheckpointName, input.Checkpoint, input.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO human_interventions (
			id, workspace_id, task_id, attempt_id, worker_id, checkpoint_id,
			checkpoint_payload, request_type, prompt, requested_action, target_origin, status,
			expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
	`, input.ID, workspace, input.TaskID, input.AttemptID, input.WorkerID,
		input.CheckpointID, input.Checkpoint, input.Type, input.Prompt, input.RequestedAction,
		input.TargetOrigin, input.ExpiresAt, input.CreatedAt); err != nil {
		if isConstraintMessage(err, "unique") {
			return nil, ErrHumanInterventionConflict
		}
		return nil, err
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE tasks SET status = 'waiting_for_human', lease_until = NULL, updated_at = ?
		WHERE workspace_id = ? AND id = ? AND current_attempt_id = ? AND worker_id = ?
		  AND status IN ('leased','running')
	`, input.CreatedAt, workspace, input.TaskID, input.AttemptID, input.WorkerID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrHumanInterventionConflict
	}
	res, err = tx.ExecContext(ctx, `
		UPDATE execution_attempts SET status = 'waiting_for_human', lease_until = NULL
		WHERE workspace_id = ? AND id = ? AND task_id = ? AND worker_id = ?
		  AND status IN ('leased','running')
	`, workspace, input.AttemptID, input.TaskID, input.WorkerID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrHumanInterventionConflict
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_status_updates (id, workspace_id, task_id, worker_id, status, message, created_at)
		VALUES (?, ?, ?, ?, 'waiting_for_human', ?, ?)
	`, NewID(), workspace, input.TaskID, input.WorkerID,
		"operator decision required: "+input.Type, input.CreatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetHumanIntervention(ctx, input.ID)
}

const humanInterventionSelect = `
	SELECT h.id, h.workspace_id, h.task_id, h.attempt_id, h.worker_id,
	       h.checkpoint_id, h.checkpoint_payload, h.request_type, h.prompt,
	       h.requested_action, h.target_origin, h.status, h.expires_at,
	       h.created_at, h.decided_at, h.decided_by, h.decision_note,
	       t.browser_profile_id, t.rule_id, t.rule_version_number
	FROM human_interventions h
	JOIN tasks t ON t.workspace_id = h.workspace_id AND t.id = h.task_id
`

func scanHumanIntervention(row interface{ Scan(...any) error }) (*models.HumanIntervention, error) {
	item := &models.HumanIntervention{}
	var decidedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.WorkspaceID, &item.TaskID, &item.AttemptID,
		&item.WorkerID, &item.CheckpointID, &item.Checkpoint, &item.Type,
		&item.Prompt, &item.RequestedAction, &item.TargetOrigin, &item.Status,
		&item.ExpiresAt, &item.CreatedAt, &decidedAt, &item.DecidedBy,
		&item.DecisionNote, &item.BrowserProfileID, &item.RuleID,
		&item.RuleVersionNumber,
	)
	if decidedAt.Valid {
		item.DecidedAt = &decidedAt.Time
	}
	return item, err
}

func (s *Store) getHumanIntervention(ctx context.Context, id string) (*models.HumanIntervention, error) {
	item, err := scanHumanIntervention(s.db.QueryRowContext(ctx,
		humanInterventionSelect+` WHERE h.workspace_id = ? AND h.id = ?`,
		workspaceID(ctx), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHumanInterventionNotFound
	}
	return item, err
}

// GetHumanIntervention returns one decision and atomically fails an overdue
// waiting attempt before exposing its expired state.
func (s *Store) GetHumanIntervention(ctx context.Context, id string) (*models.HumanIntervention, error) {
	if _, err := s.expireHumanInterventions(ctx, id, ""); err != nil {
		return nil, err
	}
	return s.getHumanIntervention(ctx, id)
}

func (s *Store) ListHumanInterventions(ctx context.Context, taskID string) ([]*models.HumanIntervention, error) {
	if err := s.ensureTaskInWorkspace(ctx, taskID); err != nil {
		return nil, err
	}
	if _, err := s.expireHumanInterventions(ctx, "", taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, humanInterventionSelect+`
		WHERE h.workspace_id = ? AND h.task_id = ? ORDER BY h.created_at DESC`,
		workspaceID(ctx), taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*models.HumanIntervention{}
	for rows.Next() {
		item, err := scanHumanIntervention(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ExpireHumanInterventions fails all overdue waiting attempts in the current
// workspace. The scheduler calls this independently of worker or Admin UI
// traffic so abandoned checkpoints cannot remain pending indefinitely.
func (s *Store) ExpireHumanInterventions(ctx context.Context) (int64, error) {
	return s.expireHumanInterventions(ctx, "", "")
}

func (s *Store) expireHumanInterventions(ctx context.Context, id, taskID string) (int64, error) {
	workspace := workspaceID(ctx)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	where := "h.workspace_id = ? AND h.status = 'pending' AND h.expires_at <= ?"
	args := []any{workspace, now}
	if id != "" {
		where += " AND h.id = ?"
		args = append(args, id)
	}
	if taskID != "" {
		where += " AND h.task_id = ?"
		args = append(args, taskID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT h.id, h.task_id, h.attempt_id, h.worker_id
		FROM human_interventions h WHERE `+where, args...)
	if err != nil {
		return 0, err
	}
	type overdue struct{ id, taskID, attemptID, workerID string }
	var pending []overdue
	for rows.Next() {
		var item overdue
		if err := rows.Scan(&item.id, &item.taskID, &item.attemptID, &item.workerID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, item := range pending {
		if _, err := tx.ExecContext(ctx, `UPDATE human_interventions
			SET status = 'expired', decided_at = ?, decided_by = 'system:expiry', decision_note = 'operator decision expired'
			WHERE workspace_id = ? AND id = ? AND status = 'pending'`, now, workspace, item.id); err != nil {
			return 0, err
		}
		if err := failWaitingHumanAttempt(ctx, tx, workspace, item.taskID, item.attemptID,
			item.workerID, now, "HumanTimeout", "operator decision expired"); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(pending)), nil
}

func failWaitingHumanAttempt(ctx context.Context, tx *sql.Tx, workspace, taskID, attemptID, workerID string, now time.Time, errorType, message string) error {
	res, err := tx.ExecContext(ctx, `UPDATE tasks
		SET status = 'failed', completed_at = ?, updated_at = ?, lease_until = NULL,
		    error_type = ?, error_message = ?
		WHERE workspace_id = ? AND id = ? AND current_attempt_id = ? AND worker_id = ?
		  AND status = 'waiting_for_human'`, now, now, errorType, message,
		workspace, taskID, attemptID, workerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrHumanInterventionConflict
	}
	res, err = tx.ExecContext(ctx, `UPDATE execution_attempts
		SET status = 'failed', completed_at = ?, lease_until = NULL,
		    error_type = ?, error_message = ?
		WHERE workspace_id = ? AND id = ? AND task_id = ? AND worker_id = ?
		  AND status = 'waiting_for_human'`, now, errorType, message,
		workspace, attemptID, taskID, workerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrHumanInterventionConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_status_updates
		(id, workspace_id, task_id, worker_id, status, message, created_at)
		VALUES (?, ?, ?, ?, 'failed', ?, ?)`, NewID(), workspace, taskID,
		workerID, message, now)
	return err
}

func (s *Store) DecideHumanIntervention(ctx context.Context, id, checkpointID string, decision models.HumanInterventionStatus, note, actor string, leaseDuration time.Duration) (*models.HumanIntervention, error) {
	if decision != models.HumanInterventionApproved && decision != models.HumanInterventionRejected {
		return nil, ErrHumanInterventionConflict
	}
	workspace := workspaceID(ctx)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var taskID, attemptID, workerID, storedCheckpoint string
	var status models.HumanInterventionStatus
	var expiresAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT task_id, attempt_id, worker_id, checkpoint_id, status, expires_at
		FROM human_interventions WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(
		&taskID, &attemptID, &workerID, &storedCheckpoint, &status, &expiresAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHumanInterventionNotFound
		}
		return nil, err
	}
	if status != models.HumanInterventionPending {
		return nil, ErrHumanInterventionConflict
	}
	if !expiresAt.After(now) {
		if _, err := tx.ExecContext(ctx, `UPDATE human_interventions SET status = 'expired',
			decided_at = ?, decided_by = 'system:expiry', decision_note = 'operator decision expired'
			WHERE workspace_id = ? AND id = ? AND status = 'pending'`, now, workspace, id); err != nil {
			return nil, err
		}
		if err := failWaitingHumanAttempt(ctx, tx, workspace, taskID, attemptID, workerID,
			now, "HumanTimeout", "operator decision expired"); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrHumanInterventionExpired
	}
	if checkpointID == "" || checkpointID != storedCheckpoint {
		return nil, ErrHumanCheckpointConflict
	}

	if decision == models.HumanInterventionApproved {
		leaseUntil := now.Add(leaseDuration)
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'running', lease_until = ?, updated_at = ?
			WHERE workspace_id = ? AND id = ? AND current_attempt_id = ? AND worker_id = ?
			  AND status = 'waiting_for_human'`, leaseUntil, now, workspace, taskID, attemptID, workerID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, ErrHumanInterventionConflict
		}
		res, err = tx.ExecContext(ctx, `UPDATE execution_attempts SET status = 'running', lease_until = ?
			WHERE workspace_id = ? AND id = ? AND task_id = ? AND worker_id = ?
			  AND status = 'waiting_for_human'`, leaseUntil, workspace, attemptID, taskID, workerID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, ErrHumanInterventionConflict
		}
	} else if err := failWaitingHumanAttempt(ctx, tx, workspace, taskID, attemptID,
		workerID, now, "HumanRejected", "operator rejected human intervention"); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE human_interventions SET status = ?,
		decided_at = ?, decided_by = ?, decision_note = ?
		WHERE workspace_id = ? AND id = ? AND status = 'pending'`, decision, now,
		actor, note, workspace, id); err != nil {
		return nil, err
	}
	message := "operator approved checkpoint resume"
	if decision == models.HumanInterventionRejected {
		message = "operator rejected human intervention"
	}
	if decision == models.HumanInterventionApproved {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_status_updates
			(id, workspace_id, task_id, worker_id, status, message, created_at)
			VALUES (?, ?, ?, ?, 'running', ?, ?)`, NewID(), workspace, taskID,
			workerID, message, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getHumanIntervention(ctx, id)
}

func migration018(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS human_interventions (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id),
			task_id TEXT NOT NULL REFERENCES tasks(id),
			attempt_id TEXT NOT NULL REFERENCES execution_attempts(id),
			worker_id TEXT NOT NULL,
			checkpoint_id TEXT NOT NULL,
			checkpoint_payload TEXT NOT NULL,
			request_type TEXT NOT NULL CHECK (request_type IN ('captcha','2fa','confirmation','generic')),
			prompt TEXT NOT NULL,
			requested_action TEXT NOT NULL,
			target_origin TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','expired','cancelled')),
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			decided_at DATETIME,
			decided_by TEXT NOT NULL DEFAULT '',
			decision_note TEXT NOT NULL DEFAULT ''
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_human_interventions_active
		ON human_interventions(workspace_id, task_id) WHERE status = 'pending';
		CREATE INDEX IF NOT EXISTS idx_human_interventions_attempt
		ON human_interventions(workspace_id, attempt_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_human_interventions_expiry
		ON human_interventions(workspace_id, status, expires_at);
	`)
	if err != nil {
		return fmt.Errorf("create human interventions: %w", err)
	}
	return nil
}
