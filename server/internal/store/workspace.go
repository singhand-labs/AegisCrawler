package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var ErrWorkspaceNotFound = errors.New("workspace not found")

func workspaceID(ctx context.Context) string {
	return authz.WorkspaceID(ctx)
}

func (s *Store) ensureTaskInWorkspace(ctx context.Context, taskID string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM tasks WHERE id = ? AND workspace_id = ?)`,
		taskID, workspaceID(ctx)).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrTaskNotFound
	}
	return nil
}

func (s *Store) CreateWorkspace(ctx context.Context, workspace *models.Workspace) error {
	if workspace.ID == "" {
		workspace.ID = NewID()
	}
	if workspace.Name == "" {
		workspace.Name = workspace.ID
	}
	now := time.Now().UTC()
	if workspace.CreatedAt.IsZero() {
		workspace.CreatedAt = now
	}
	if workspace.UpdatedAt.IsZero() {
		workspace.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		workspace.ID, workspace.Name, workspace.CreatedAt, workspace.UpdatedAt)
	return err
}

func (s *Store) GetWorkspaceByID(ctx context.Context, id string) (*models.Workspace, error) {
	workspace := &models.Workspace{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, created_at, updated_at FROM workspaces WHERE id = ?`, id,
	).Scan(&workspace.ID, &workspace.Name, &workspace.CreatedAt, &workspace.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrWorkspaceNotFound
	}
	return workspace, err
}
