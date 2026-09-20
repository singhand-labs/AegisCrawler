package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

// CreateMCPToken persists token metadata and a one-way hash. Callers must
// generate and return the plaintext bearer token separately.
func (s *Store) CreateMCPToken(ctx context.Context, token *models.MCPToken) error {
	token.WorkspaceID = workspaceID(ctx)
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_tokens (
		id, workspace_id, name, token_hash, token_prefix, permissions,
		created_by, created_at, expires_at, revoked_at, last_used_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, token.ID, token.WorkspaceID,
		token.Name, token.TokenHash, token.TokenPrefix, token.Permissions,
		token.CreatedBy, token.CreatedAt, token.ExpiresAt, token.RevokedAt,
		token.LastUsedAt)
	return err
}

// AuthenticateMCPToken resolves a bearer-token hash without trusting a
// caller-supplied workspace. Revoked and expired credentials are indistinguishable
// from unknown credentials to the caller.
func (s *Store) AuthenticateMCPToken(ctx context.Context, tokenHash string, now time.Time) (*models.MCPToken, error) {
	token, err := scanMCPToken(s.db.QueryRowContext(ctx, `SELECT id, workspace_id,
		name, token_hash, token_prefix, permissions, created_by, created_at,
		expires_at, revoked_at, last_used_at FROM mcp_tokens
		WHERE token_hash = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, tokenHash, now))
	if err != nil {
		return nil, err
	}
	if token.RevokedAt.Valid || (token.ExpiresAt.Valid && !token.ExpiresAt.Time.After(now)) {
		return nil, ErrMCPTokenNotFound
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mcp_tokens SET last_used_at = ?
		WHERE id = ? AND token_hash = ? AND revoked_at IS NULL
		AND (expires_at IS NULL OR expires_at > ?)`, now, token.ID, tokenHash, now)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrMCPTokenNotFound
	}
	token.LastUsedAt = sql.NullTime{Time: now, Valid: true}
	return token, nil
}

func (s *Store) ListMCPTokens(ctx context.Context) ([]*models.MCPToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, name, token_hash,
		token_prefix, permissions, created_by, created_at, expires_at, revoked_at,
		last_used_at FROM mcp_tokens WHERE workspace_id = ? ORDER BY created_at DESC`,
		workspaceID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []*models.MCPToken{}
	for rows.Next() {
		token := &models.MCPToken{}
		if err := rows.Scan(&token.ID, &token.WorkspaceID, &token.Name,
			&token.TokenHash, &token.TokenPrefix, &token.Permissions,
			&token.CreatedBy, &token.CreatedAt, &token.ExpiresAt,
			&token.RevokedAt, &token.LastUsedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) RevokeMCPToken(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE mcp_tokens SET revoked_at = ?
		WHERE id = ? AND workspace_id = ? AND revoked_at IS NULL`, now, id, workspaceID(ctx))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM mcp_tokens WHERE id = ? AND workspace_id = ?`,
			id, workspaceID(ctx)).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMCPTokenNotFound
		}
		return err
	}
	return nil
}

func scanMCPToken(row *sql.Row) (*models.MCPToken, error) {
	token := &models.MCPToken{}
	if err := row.Scan(&token.ID, &token.WorkspaceID, &token.Name,
		&token.TokenHash, &token.TokenPrefix, &token.Permissions,
		&token.CreatedBy, &token.CreatedAt, &token.ExpiresAt,
		&token.RevokedAt, &token.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMCPTokenNotFound
		}
		return nil, err
	}
	return token, nil
}

// MCPTokenContext returns the workspace-scoped principal represented by a
// stored token. The stored permissions remain the source of authorization.
func MCPTokenContext(ctx context.Context, token *models.MCPToken) (context.Context, error) {
	permissions := []string{}
	if err := json.Unmarshal(token.Permissions, &permissions); err != nil {
		return nil, err
	}
	role := authz.RoleViewer
	for _, permission := range permissions {
		if permission == models.MCPPermissionWrite {
			role = authz.RoleOperator
		}
	}
	return authz.WithPrincipal(ctx, authz.Principal{
		Subject: "mcp-token:" + token.ID, WorkspaceID: token.WorkspaceID,
		Roles: []authz.Role{role}, Permissions: permissions, Kind: authz.PrincipalMCP,
	}), nil
}
