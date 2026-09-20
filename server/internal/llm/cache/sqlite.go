package cache

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

// SQLiteCache is a SQLite-backed implementation of Cache.
type SQLiteCache struct {
	db    *sql.DB
	store *store.Store
}

// NewSQLiteCache creates a cache backed by the given store. The store must have
// the llm_cache table (created by the store migration).
func NewSQLiteCache(s *store.Store) (*SQLiteCache, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	return &SQLiteCache{db: s.DB(), store: s}, nil
}

// Get looks up a cache entry by key. Expired entries are deleted on access.
func (c *SQLiteCache) Get(ctx context.Context, key string) (*Entry, bool, error) {
	workspace := authz.WorkspaceID(ctx)
	const q = `
		SELECT artifact, artifact_hash, provider, model, input_tokens,
		       output_tokens, COALESCE(response_id, ''),
		       COALESCE(finish_reason, ''), expires_at
		FROM llm_cache
		WHERE workspace_id = ? AND cache_key = ?`
	row := c.db.QueryRowContext(ctx, q, workspace, key)

	var ent Entry
	var artifact []byte
	var artifactHash string
	var expiresAt time.Time
	err := row.Scan(
		&artifact,
		&artifactHash,
		&ent.Provider,
		&ent.Model,
		&ent.InputTokens,
		&ent.OutputTokens,
		&ent.ResponseID,
		&ent.FinishReason,
		&expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	if time.Now().UTC().After(expiresAt) {
		_, _ = c.db.ExecContext(ctx, `DELETE FROM llm_cache WHERE workspace_id = ? AND cache_key = ?`, workspace, key)
		return nil, false, nil
	}
	ent.Content, err = c.store.OpenLLMCacheContent(ctx, key, artifact, artifactHash)
	if err != nil {
		return nil, false, err
	}
	return &ent, true, nil
}

// Set stores an entry with the given TTL. Entries with a non-positive TTL are
// ignored so that they never consume database space.
func (c *SQLiteCache) Set(ctx context.Context, key string, entry *Entry, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	if entry == nil {
		return errors.New("entry is nil")
	}
	if sanitized := redact.String(entry.Content); sanitized != entry.Content {
		return ErrUnsafeCacheContent
	}

	expiresAt := time.Now().UTC().Add(ttl)
	workspace := authz.WorkspaceID(ctx)
	artifact, artifactHash, err := c.store.SealLLMCacheContent(ctx, key, entry.Content)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO llm_cache (
			workspace_id, cache_key, artifact, artifact_hash, provider, model,
			input_tokens, output_tokens, response_id, finish_reason, expires_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
		ON CONFLICT(workspace_id, cache_key) DO UPDATE SET
			artifact = excluded.artifact,
			artifact_hash = excluded.artifact_hash,
			provider = excluded.provider,
			model = excluded.model,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			response_id = excluded.response_id,
			finish_reason = excluded.finish_reason,
			expires_at = excluded.expires_at,
			created_at = CURRENT_TIMESTAMP
	`
	_, err = c.db.ExecContext(
		ctx,
		q,
		workspace,
		key,
		artifact,
		artifactHash,
		sanitizeCacheMetadata(entry.Provider),
		sanitizeCacheMetadata(entry.Model),
		entry.InputTokens,
		entry.OutputTokens,
		sanitizeCacheMetadata(entry.ResponseID),
		sanitizeCacheMetadata(entry.FinishReason),
		expiresAt,
	)
	return err
}

func sanitizeCacheMetadata(value string) string {
	value = redact.String(value)
	const maxMetadataBytes = 256
	if len(value) <= maxMetadataBytes {
		return value
	}
	value = value[:maxMetadataBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
