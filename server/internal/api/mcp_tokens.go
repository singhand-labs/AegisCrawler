package api

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

const mcpTokenPrefix = "aegis_mcp_"

// CreateMCPToken godoc
// @Summary Create an MCP bearer token
// @Description Creates a revocable workspace-scoped MCP token. The plaintext token is returned only once.
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param request body CreateMCPTokenRequest true "MCP token metadata and permissions"
// @Success 201 {object} MCPTokenResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/mcp/tokens [post]
func (h *Handler) CreateMCPToken(w http.ResponseWriter, r *http.Request) {
	var req CreateMCPTokenRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "name must be between 1 and 100 characters")
		return
	}
	permissions, err := normalizeMCPPermissions(req.Permissions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	now := time.Now().UTC()
	var expiresAt sql.NullTime
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(now) {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "expiresAt must be in the future")
			return
		}
		expiresAt = sql.NullTime{Time: req.ExpiresAt.UTC(), Valid: true}
	}
	plaintext, tokenHash, err := newMCPBearerToken()
	if err != nil {
		h.logger.Error("generate mcp token failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create MCP token")
		return
	}
	encodedPermissions, _ := json.Marshal(permissions)
	token := &models.MCPToken{
		ID: store.NewID(), Name: req.Name, TokenHash: tokenHash,
		TokenPrefix: plaintext[:min(len(plaintext), len(mcpTokenPrefix)+8)],
		Permissions: models.JSON(encodedPermissions), CreatedBy: authz.Subject(r.Context(), "admin"),
		CreatedAt: now, ExpiresAt: expiresAt,
	}
	if err := h.store.CreateMCPToken(r.Context(), token); err != nil {
		h.logger.Error("create mcp token failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create MCP token")
		return
	}
	h.auditLog(r.Context(), "create_mcp_token", "mcp_token", token.ID,
		map[string]any{"tokenId": token.ID, "permissions": permissions})
	response := toMCPTokenResponse(token)
	response.Token = plaintext
	writeJSON(w, http.StatusCreated, response)
}

// ListMCPTokens godoc
// @Summary List MCP bearer-token metadata
// @Description Lists workspace-scoped token metadata without plaintext tokens or token hashes.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Success 200 {object} ListMCPTokensResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/mcp/tokens [get]
func (h *Handler) ListMCPTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListMCPTokens(r.Context())
	if err != nil {
		h.logger.Error("list mcp tokens failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list MCP tokens")
		return
	}
	response := &ListMCPTokensResponse{Tokens: make([]*MCPTokenResponse, 0, len(tokens))}
	for _, token := range tokens {
		response.Tokens = append(response.Tokens, toMCPTokenResponse(token))
	}
	writeJSON(w, http.StatusOK, response)
}

// RevokeMCPToken godoc
// @Summary Revoke an MCP bearer token
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "MCP token ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/mcp/tokens/{id} [delete]
func (h *Handler) RevokeMCPToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.RevokeMCPToken(r.Context(), id, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrMCPTokenNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "MCP token not found")
			return
		}
		h.logger.Error("revoke mcp token failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to revoke MCP token")
		return
	}
	h.auditLog(r.Context(), "revoke_mcp_token", "mcp_token", id, map[string]any{"tokenId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func normalizeMCPPermissions(values []string) ([]string, error) {
	permissions := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != models.MCPPermissionRead && value != models.MCPPermissionWrite {
			return nil, errors.New("permissions may contain only read and write")
		}
		if !slices.Contains(permissions, value) {
			permissions = append(permissions, value)
		}
	}
	if len(permissions) == 0 {
		return nil, errors.New("at least one permission is required")
	}
	slices.Sort(permissions)
	return permissions, nil
}

func newMCPBearerToken() (plaintext, tokenHash string, err error) {
	random := make([]byte, 32)
	if _, err = cryptorand.Read(random); err != nil {
		return "", "", err
	}
	plaintext = mcpTokenPrefix + base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(digest[:]), nil
}

func toMCPTokenResponse(token *models.MCPToken) *MCPTokenResponse {
	permissions := []string{}
	_ = json.Unmarshal(token.Permissions, &permissions)
	response := &MCPTokenResponse{
		ID: token.ID, WorkspaceID: token.WorkspaceID, Name: token.Name,
		TokenPrefix: token.TokenPrefix, Permissions: permissions,
		CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt,
	}
	if token.ExpiresAt.Valid {
		value := token.ExpiresAt.Time
		response.ExpiresAt = &value
	}
	if token.RevokedAt.Valid {
		value := token.RevokedAt.Time
		response.RevokedAt = &value
	}
	if token.LastUsedAt.Valid {
		value := token.LastUsedAt.Time
		response.LastUsedAt = &value
	}
	return response
}
