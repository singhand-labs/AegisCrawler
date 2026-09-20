package authz

import "context"

const DefaultWorkspaceID = "default"

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
	RoleWorker   Role = "worker"
)

type PrincipalKind string

const (
	PrincipalAdmin  PrincipalKind = "admin"
	PrincipalWorker PrincipalKind = "worker"
	PrincipalSystem PrincipalKind = "system"
	PrincipalMCP    PrincipalKind = "mcp"
)

// Principal is the authenticated identity attached to every authorized
// request. WorkspaceID is always server-derived; clients cannot select a
// workspace by writing resource fields.
type Principal struct {
	Subject     string
	WorkspaceID string
	Roles       []Role
	Permissions []string
	Kind        PrincipalKind
}

func (p Principal) HasRole(role Role) bool {
	for _, candidate := range p.Roles {
		if candidate == RoleAdmin || candidate == role {
			return true
		}
	}
	return false
}

func (p Principal) HasPermission(permission string) bool {
	if p.HasRole(RoleAdmin) {
		return true
	}
	for _, candidate := range p.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	if principal.WorkspaceID == "" {
		principal.WorkspaceID = DefaultWorkspaceID
	}
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	if principal.WorkspaceID == "" {
		principal.WorkspaceID = DefaultWorkspaceID
	}
	return principal, true
}

// WorkspaceID returns the authenticated workspace. Internal/background calls
// without a principal remain compatible by operating in the default workspace.
func WorkspaceID(ctx context.Context) string {
	if principal, ok := PrincipalFromContext(ctx); ok {
		return principal.WorkspaceID
	}
	return DefaultWorkspaceID
}

func Subject(ctx context.Context, fallback string) string {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.Subject != "" {
		return principal.Subject
	}
	return fallback
}
