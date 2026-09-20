package mcpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// Handler authenticates every Streamable HTTP request before passing it to a
// stateless MCP transport. Stateless mode avoids binding a transport session
// to a different bearer token on a later request and is sufficient for the
// request/response tools exposed by AegisCrawler.
type Handler struct {
	store     *store.Store
	cfg       *config.Config
	logger    *zap.Logger
	transport http.Handler
	mu        sync.Mutex
	limiters  map[string]*rate.Limiter
}

func New(s *store.Store, cfg *config.Config, logger *zap.Logger) *Handler {
	service := newService(s, cfg, logger)
	server := mcp.NewServer(&mcp.Implementation{Name: "AegisCrawler", Version: "1.0.0"}, nil)
	server.AddReceivingMiddleware(service.auditMiddleware)
	service.registerTools(server)
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return &Handler{store: s, cfg: cfg, logger: logger, transport: transport, limiters: map[string]*rate.Limiter{}}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("access_token") {
		h.writeAuthError(w, http.StatusBadRequest, "bearer tokens are not accepted in the query string")
		return
	}
	authorization := r.Header.Get("Authorization")
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		h.writeAuthError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return
	}
	digest := sha256.Sum256([]byte(parts[1]))
	token, err := h.store.AuthenticateMCPToken(r.Context(), hex.EncodeToString(digest[:]), time.Now().UTC())
	if err != nil {
		if !errors.Is(err, store.ErrMCPTokenNotFound) {
			h.logger.Error("authenticate mcp token failed", zap.Error(err))
		}
		h.writeAuthError(w, http.StatusUnauthorized, "invalid or inactive bearer token")
		return
	}
	if !h.allow(token.ID) {
		w.Header().Set("Retry-After", "1")
		h.writeAuthError(w, http.StatusTooManyRequests, "MCP token rate limit exceeded")
		return
	}
	ctx, err := store.MCPTokenContext(r.Context(), token)
	if err != nil {
		h.logger.Error("decode mcp token permissions failed", zap.String("tokenId", token.ID), zap.Error(err))
		h.writeAuthError(w, http.StatusUnauthorized, "invalid or inactive bearer token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.transport.ServeHTTP(w, r.WithContext(ctx))
}

func (h *Handler) allow(tokenID string) bool {
	if h.cfg.MCPRateLimitPerSecond <= 0 || h.cfg.MCPRateLimitBurst <= 0 {
		return true
	}
	h.mu.Lock()
	limiter := h.limiters[tokenID]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Limit(h.cfg.MCPRateLimitPerSecond), h.cfg.MCPRateLimitBurst)
		h.limiters[tokenID] = limiter
	}
	h.mu.Unlock()
	return limiter.Allow()
}

func (h *Handler) writeAuthError(w http.ResponseWriter, status int, message string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="AegisCrawler MCP"`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "status": status})
}
