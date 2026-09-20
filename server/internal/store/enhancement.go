package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

// CreateRuleEnhancement inserts a new rule enhancement record.
func (s *Store) CreateRuleEnhancement(ctx context.Context, e *models.RuleEnhancement) error {
	return createRuleEnhancementWithExec(ctx, s.db, e)
}

// CreateRuleEnhancementTx inserts a new rule enhancement record within an existing transaction.
func (s *Store) CreateRuleEnhancementTx(ctx context.Context, tx *sql.Tx, e *models.RuleEnhancement) error {
	return createRuleEnhancementWithExec(ctx, tx, e)
}

func createRuleEnhancementWithExec(ctx context.Context, ex execer, e *models.RuleEnhancement) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	e.WorkspaceID = workspaceID(ctx)
	const q = `
		INSERT INTO rule_enhancements (id, workspace_id, rule_id, baseline, enhanced, patch, user_hint, provider, model, input_tokens, output_tokens, suggestions, safety_flags, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := ex.ExecContext(ctx, q,
		e.ID, e.WorkspaceID, e.RuleID, e.Baseline, e.Enhanced, e.Patch, e.UserHint, e.Provider, e.Model,
		e.InputTokens, e.OutputTokens, e.Suggestions, e.SafetyFlags, e.Status, e.CreatedAt,
	)
	return err
}

// GetRuleEnhancementByRuleID returns the pending enhancement for a rule.
func (s *Store) GetRuleEnhancementByRuleID(ctx context.Context, ruleID string) (*models.RuleEnhancement, error) {
	const q = `SELECT id, workspace_id, rule_id, baseline, enhanced, patch, user_hint, provider, model, input_tokens, output_tokens, suggestions, safety_flags, status, created_at FROM rule_enhancements WHERE rule_id = ? AND workspace_id = ? ORDER BY created_at DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, ruleID, workspaceID(ctx))
	e := &models.RuleEnhancement{}
	err := row.Scan(&e.ID, &e.WorkspaceID, &e.RuleID, &e.Baseline, &e.Enhanced, &e.Patch, &e.UserHint, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.Suggestions, &e.SafetyFlags, &e.Status, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuleNotFound
	}
	return e, err
}

func updateRuleEnhancementStatusWithExec(ctx context.Context, ex execer, id, status string) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE rule_enhancements SET status = ? WHERE id = ? AND workspace_id = ?`,
		status, id, workspaceID(ctx))
	return err
}

// UpdateRuleEnhancementStatus updates the status of the enhancement with the given ID.
func (s *Store) UpdateRuleEnhancementStatus(ctx context.Context, id, status string) error {
	return updateRuleEnhancementStatusWithExec(ctx, s.db, id, status)
}

// UpdateRuleEnhancementStatusTx updates the status of the enhancement within an existing transaction.
func (s *Store) UpdateRuleEnhancementStatusTx(ctx context.Context, tx *sql.Tx, id, status string) error {
	return updateRuleEnhancementStatusWithExec(ctx, tx, id, status)
}
