package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestMCPTokensAreHashedRevocableAndWorkspaceScoped(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	now := time.Now().UTC()
	defaultCtx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "admin", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin},
	})
	if err := s.CreateWorkspace(defaultCtx, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	tenantCtx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "tenant-admin", WorkspaceID: "tenant-b", Roles: []authz.Role{authz.RoleAdmin},
	})

	permissions := models.JSON(`["read","write"]`)
	defaultToken := &models.MCPToken{ID: "default-token", Name: "Default token", TokenHash: "hash-default",
		TokenPrefix: "aegis_mcp_12345678", Permissions: permissions, CreatedBy: "admin", CreatedAt: now}
	if err := s.CreateMCPToken(defaultCtx, defaultToken); err != nil {
		t.Fatal(err)
	}
	tenantToken := &models.MCPToken{ID: "tenant-token", Name: "Tenant token", TokenHash: "hash-tenant",
		TokenPrefix: "aegis_mcp_87654321", Permissions: models.JSON(`["read"]`), CreatedBy: "tenant-admin", CreatedAt: now}
	if err := s.CreateMCPToken(tenantCtx, tenantToken); err != nil {
		t.Fatal(err)
	}

	tokens, err := s.ListMCPTokens(defaultCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ID != defaultToken.ID || tokens[0].WorkspaceID != authz.DefaultWorkspaceID {
		t.Fatalf("unexpected default-workspace tokens: %+v", tokens)
	}
	encoded, err := json.Marshal(tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hash-default") || strings.Contains(string(encoded), "tokenHash") {
		t.Fatalf("token hash leaked through JSON: %s", encoded)
	}

	authenticated, err := s.AuthenticateMCPToken(context.Background(), "hash-tenant", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.WorkspaceID != "tenant-b" || !authenticated.LastUsedAt.Valid {
		t.Fatalf("unexpected authenticated token: %+v", authenticated)
	}
	if err := s.RevokeMCPToken(defaultCtx, tenantToken.ID, now); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("expected cross-workspace revoke to fail, got %v", err)
	}
	if err := s.RevokeMCPToken(tenantCtx, tenantToken.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateMCPToken(context.Background(), "hash-tenant", now.Add(time.Second)); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("expected revoked token to be inactive, got %v", err)
	}
}

func TestAuthenticateMCPTokenRejectsExpiredCredentials(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	now := time.Now().UTC()
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "admin", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin},
	})
	token := &models.MCPToken{ID: "expired-token", Name: "Expired", TokenHash: "expired-hash",
		TokenPrefix: "aegis_mcp_expired", Permissions: models.JSON(`["read"]`), CreatedBy: "admin",
		CreatedAt: now.Add(-time.Hour), ExpiresAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true}}
	if err := s.CreateMCPToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateMCPToken(context.Background(), token.TokenHash, now); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("expected expired token rejection, got %v", err)
	}
}
