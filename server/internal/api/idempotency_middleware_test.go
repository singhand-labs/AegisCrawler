package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

func newIdempotencyTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "idemp-mw-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newIdempotencyRequest(t *testing.T, method, path, body, key string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, r)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req = req.WithContext(authz.WithPrincipal(context.Background(), authz.Principal{
		Subject:     "admin",
		WorkspaceID: authz.DefaultWorkspaceID,
		Roles:       []authz.Role{authz.RoleAdmin},
	}))
	return req
}

func TestIdempotencyMiddlewareReplaysCachedResponse(t *testing.T) {
	s := newIdempotencyTestStore(t)
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "res-1"})
	})
	mw := IdempotencyMiddleware(s, time.Hour, next)

	body := `{"input":"foo"}`
	key := "abc-123"

	// First request — handler invoked, response cached.
	req1 := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", body, key)
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 handler call, got %d", calls)
	}
	firstBody := rec1.Body.String()

	// Second request with same key + same body — cached response replayed.
	req2 := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", body, key)
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay, got %d", rec2.Code)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected handler NOT to be called again, got %d calls", calls)
	}
	if rec2.Body.String() != firstBody {
		t.Fatalf("expected cached body %q, got %q", firstBody, rec2.Body.String())
	}
}

func TestIdempotencyMiddlewareRejectsKeyReuseWithDifferentBody(t *testing.T) {
	s := newIdempotencyTestStore(t)
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "res-1"})
	})
	mw := IdempotencyMiddleware(s, time.Hour, next)

	key := "abc-456"

	// First request — caches the response.
	req1 := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", `{"a":1}`, key)
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	// Second request with same key but different body → 409.
	req2 := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", `{"a":2}`, key)
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec2.Code)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected handler called once (not on conflict), got %d", calls)
	}
}

func TestIdempotencyMiddlewareNoHeaderPassesThrough(t *testing.T) {
	s := newIdempotencyTestStore(t)
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mw := IdempotencyMiddleware(s, time.Hour, next)

	// Two requests, no Idempotency-Key header → handler called both times.
	for i := 0; i < 2; i++ {
		req := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", `{"x":1}`, "")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected handler called twice without header, got %d", calls)
	}
}

func TestIdempotencyMiddlewareDoesNotCacheNon2xx(t *testing.T) {
	s := newIdempotencyTestStore(t)
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeError(w, http.StatusInternalServerError, "BOOM", "something broke")
	})
	mw := IdempotencyMiddleware(s, time.Hour, next)

	body := `{"input":"err"}`
	key := "abc-789"

	// First request — 500, should NOT be cached.
	req1 := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", body, key)
	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec1.Code)
	}

	// Second request with same key + body — handler invoked again (no cache).
	req2 := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", body, key)
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 again, got %d", rec2.Code)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected handler called twice for non-2xx, got %d", calls)
	}
}

// H-2 regression: two concurrent requests carrying the same Idempotency-Key
// (and same body) must NOT both invoke the wrapped handler. Without
// per-key serialization both see a cache miss, both run the handler, and
// the second handler's response overwrites the first in the cache.
func TestIdempotencyMiddlewareConcurrentSameKeyRunsHandlerOnce(t *testing.T) {
	s := newIdempotencyTestStore(t)
	var calls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// Widen the race window so a TOCTOU between Get and Put is observable.
		time.Sleep(150 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{"id": "res-1"})
	})
	mw := IdempotencyMiddleware(s, time.Hour, next)

	body := `{"input":"foo"}`
	key := "race-key-H2"

	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := newIdempotencyRequest(t, http.MethodPost, "/api/v1/test", body, key)
			recs[i] = httptest.NewRecorder()
			mw.ServeHTTP(recs[i], req)
		}(i)
	}
	wg.Wait()

	for i, rec := range recs {
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected handler invoked exactly once under concurrent same-key, got %d", got)
	}
}
