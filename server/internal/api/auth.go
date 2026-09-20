package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"go.uber.org/zap"
)

// AuthMiddleware enforces bearer-token authentication on worker-facing endpoints.
// When cfg.WorkerAPIKey is empty, authentication is skipped for backward-compatible
// development mode.
func AuthMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	principal := authz.Principal{
		Subject:     "worker-api-key",
		WorkspaceID: authz.DefaultWorkspaceID,
		Roles:       []authz.Role{authz.RoleWorker},
		Kind:        authz.PrincipalWorker,
	}
	return principalAuthMiddleware(cfg.WorkerAPIKey, principal)
}

// AdminAuthMiddleware enforces bearer-token authentication on admin endpoints.
// When cfg.AdminAPIKey is empty, authentication is skipped for backward-compatible
// development mode and a warning is logged.
func AdminAuthMiddleware(cfg *config.Config, logger *zap.Logger) func(http.Handler) http.Handler {
	subject := cfg.AuditActor
	if subject == "" {
		subject = "admin-api-key"
	}
	principal := authz.Principal{
		Subject:     subject,
		WorkspaceID: authz.DefaultWorkspaceID,
		Roles:       []authz.Role{authz.RoleAdmin},
		Kind:        authz.PrincipalAdmin,
	}
	return principalAuthMiddleware(cfg.AdminAPIKey, principal)
}

func principalAuthMiddleware(apiKey string, principal authz.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey != "" {
				auth := r.Header.Get("Authorization")
				if auth == "" {
					writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization header")
					return
				}

				parts := strings.SplitN(auth, " ", 2)
				validScheme := len(parts) == 2 && strings.EqualFold(parts[0], "Bearer")
				validToken := validScheme && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(apiKey)) == 1
				if !validToken {
					writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid authorization token")
					return
				}
			}

			ctx := authz.WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerAuthMiddleware returns a middleware that enforces the given bearer token.
// An empty apiKey disables authentication for development mode.
func bearerAuthMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization header")
				return
			}

			parts := strings.SplitN(auth, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid authorization token")
				return
			}
			// H-1: use constant-time comparison to avoid timing side-channels
			// that could leak the metrics/swagger API key byte-by-byte. Mirrors
			// the principalAuthMiddleware pattern at line 55.
			if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(apiKey)) != 1 {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid authorization token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// MetricsAuthMiddleware enforces bearer-token authentication on the /metrics endpoint.
// When cfg.MetricsAPIKey is empty, authentication is skipped for backward-compatible
// development mode.
func MetricsAuthMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	return bearerAuthMiddleware(cfg.MetricsAPIKey)
}

// SwaggerAuthMiddleware enforces bearer-token authentication on Swagger UI and spec
// endpoints. When cfg.SwaggerAPIKey is empty, authentication is skipped for
// backward-compatible development mode.
func SwaggerAuthMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	return bearerAuthMiddleware(cfg.SwaggerAPIKey)
}
