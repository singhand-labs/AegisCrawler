package authz

import (
	"context"
	"testing"
)

func TestPrincipalContextDefaultsWorkspace(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{Subject: "admin", Roles: []Role{RoleAdmin}})
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("expected principal")
	}
	if principal.WorkspaceID != DefaultWorkspaceID || WorkspaceID(ctx) != DefaultWorkspaceID {
		t.Fatalf("expected default workspace, got %#v", principal)
	}
	if Subject(ctx, "fallback") != "admin" {
		t.Fatal("expected principal subject")
	}
}

func TestPrincipalRolesAndFallbacks(t *testing.T) {
	admin := Principal{Roles: []Role{RoleAdmin}}
	if !admin.HasRole(RoleViewer) || !admin.HasRole(RoleOperator) {
		t.Fatal("admin should inherit all roles")
	}
	worker := Principal{Roles: []Role{RoleWorker}}
	if !worker.HasRole(RoleWorker) || worker.HasRole(RoleViewer) {
		t.Fatal("unexpected worker roles")
	}
	if WorkspaceID(context.Background()) != DefaultWorkspaceID {
		t.Fatal("background calls should use default workspace")
	}
	if Subject(context.Background(), "system") != "system" {
		t.Fatal("expected fallback subject")
	}
	viewer := Principal{Roles: []Role{RoleViewer}, Permissions: []string{"read"}}
	if !viewer.HasPermission("read") || viewer.HasPermission("write") {
		t.Fatal("principal permissions must be explicit")
	}
	if !admin.HasPermission("any-admin-permission") {
		t.Fatal("admin role should inherit all permissions")
	}
}
