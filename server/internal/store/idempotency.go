package store

import (
	"context"
	"database/sql"
	"time"
)

// IdempotencyEntry is a cached idempotent response.
type IdempotencyEntry struct {
	Key            string
	RequestHash    string
	ResponseStatus int
	ResponseBody   []byte
	ExpiresAt      time.Time
}

// IdempotencyLookup is the result of looking up an idempotency key.
//
//   - Entry != nil, Conflict == false → cache hit, replay the stored response.
//   - Entry == nil, Conflict == true  → key exists but request_hash differs; reject with 409.
//   - Entry == nil, Conflict == false → miss; invoke the handler and cache the result.
type IdempotencyLookup struct {
	Entry    *IdempotencyEntry
	Conflict bool
}

// GetIdempotency looks up an idempotency key within a workspace. Expired
// entries are treated as misses.
func (s *Store) GetIdempotency(ctx context.Context, workspaceID, key, requestHash string) (IdempotencyLookup, error) {
	var (
		storedHash    string
		status        int
		body          string
		expiresAt     time.Time
	)
	row := s.db.QueryRowContext(ctx, `
SELECT request_hash, response_status, response_body, expires_at
FROM idempotency_keys
WHERE workspace_id = ? AND key = ?
`, workspaceID, key)
	if err := row.Scan(&storedHash, &status, &body, &expiresAt); err != nil {
		if err == sql.ErrNoRows {
			return IdempotencyLookup{}, nil
		}
		return IdempotencyLookup{}, err
	}
	now := time.Now().UTC()
	if !expiresAt.After(now) {
		// Expired — treat as a miss.
		return IdempotencyLookup{}, nil
	}
	if storedHash != requestHash {
		return IdempotencyLookup{Conflict: true}, nil
	}
	return IdempotencyLookup{
		Entry: &IdempotencyEntry{
			Key:            key,
			RequestHash:    storedHash,
			ResponseStatus: status,
			ResponseBody:   []byte(body),
			ExpiresAt:      expiresAt,
		},
	}, nil
}

// PutIdempotency inserts or replaces an idempotency cache entry.
func (s *Store) PutIdempotency(ctx context.Context, workspaceID, key, requestHash string, status int, body []byte, ttl time.Duration) error {
	expiresAt := time.Now().UTC().Add(ttl)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO idempotency_keys (workspace_id, key, request_hash, response_status, response_body, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, key) DO UPDATE SET
    request_hash = excluded.request_hash,
    response_status = excluded.response_status,
    response_body = excluded.response_body,
    expires_at = excluded.expires_at,
    created_at = CURRENT_TIMESTAMP
`, workspaceID, key, requestHash, status, string(body), expiresAt)
	return err
}

// EvictExpiredIdempotency deletes idempotency cache entries whose expires_at is
// at or before the supplied timestamp. Returns the number of rows deleted.
func (s *Store) EvictExpiredIdempotency(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
