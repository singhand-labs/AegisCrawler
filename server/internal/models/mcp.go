package models

import (
	"database/sql"
	"time"
)

const (
	MCPPermissionRead  = "read"
	MCPPermissionWrite = "write"
)

// MCPToken is a revocable workspace-scoped credential. TokenHash is never
// serialized, and the plaintext bearer token is never persisted.
type MCPToken struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspaceId"`
	Name        string       `json:"name"`
	TokenHash   string       `json:"-"`
	TokenPrefix string       `json:"tokenPrefix"`
	Permissions JSON         `json:"permissions" swaggertype:"array,string"`
	CreatedBy   string       `json:"createdBy"`
	CreatedAt   time.Time    `json:"createdAt"`
	ExpiresAt   sql.NullTime `json:"expiresAt" swaggertype:"string"`
	RevokedAt   sql.NullTime `json:"revokedAt" swaggertype:"string"`
	LastUsedAt  sql.NullTime `json:"lastUsedAt" swaggertype:"string"`
}
