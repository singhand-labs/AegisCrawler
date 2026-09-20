package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

// DefaultIdempotencyTTL is the lifetime of cached idempotent responses.
const DefaultIdempotencyTTL = 24 * time.Hour

// IdempotencyKeyHeader is the HTTP header clients send to opt in to
// server-side idempotency.
const IdempotencyKeyHeader = "Idempotency-Key"

// H-2: in-process per-key serialization. Without this, two concurrent
// requests with the same Idempotency-Key both see a cache miss via
// GetIdempotency, both run the handler, and the second handler's response
// silently overwrites the first via PutIdempotency. Each distinct
// (workspace, key, body-hash) triple gets its own mutex; concurrent
// requests with the same triple serialize, so the second sees the cached
// response written by the first.
var (
	idempotencyMu       sync.Mutex
	idempotencyInflight = make(map[string]*sync.Mutex)
)

func idempotencyKeyMutex(key string) *sync.Mutex {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	if mu, ok := idempotencyInflight[key]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	idempotencyInflight[key] = mu
	return mu
}

// IdempotencyMiddleware wraps a mutating handler with a server-side idempotency
// cache keyed on the Idempotency-Key header + workspace_id + SHA256(body).
//
//   - No header → pass-through (handler invoked every time, no caching).
//   - Same key + same body → cached response replayed, handler invoked once.
//   - Same key + different body → HTTP 409 Conflict.
//   - Only 2xx responses are cached.
func IdempotencyMiddleware(s *store.Store, ttl time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyKeyHeader)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Read the body into memory so we can hash it, then restore it for
		// the wrapped handler.
		bodyBytes, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "could not read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		requestHash := sha256Hex(bodyBytes)

		workspaceID := authz.WorkspaceID(r.Context())

		// H-2: serialize same-key requests. The first request proceeds; the
		// second waits, then re-checks the cache and replays the cached
		// response written by the first. Eliminates the TOCTOU window
		// between GetIdempotency and PutIdempotency.
		keyMu := idempotencyKeyMutex(workspaceID + "\x00" + key + "\x00" + requestHash)
		keyMu.Lock()
		defer keyMu.Unlock()

		// Check the cache.
		lookup, err := s.GetIdempotency(r.Context(), workspaceID, key, requestHash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "IDEMPOTENCY_LOOKUP_FAILED", "idempotency lookup failed")
			return
		}
		if lookup.Conflict {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "idempotency key was used with a different request body")
			return
		}
		if lookup.Entry != nil {
			// Replay the cached response.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(lookup.Entry.ResponseStatus)
			_, _ = w.Write(lookup.Entry.ResponseBody)
			return
		}

		// Cache miss — invoke the handler via a response recorder so we can
		// capture the result and optionally cache it.
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		// Copy the captured response to the real ResponseWriter.
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())

		// Only cache 2xx responses.
		if rec.Code >= 200 && rec.Code < 300 {
			if err := s.PutIdempotency(r.Context(), workspaceID, key, requestHash, rec.Code, rec.Body.Bytes(), ttl); err != nil {
				// Logging the error would be ideal, but we don't have a logger
				// here. The response already succeeded; a cache-write failure
				// is non-fatal (the next retry will just re-invoke the handler).
				return
			}
		}
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
