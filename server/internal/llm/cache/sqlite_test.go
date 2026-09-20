package cache

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

func newTestCache(t *testing.T) *SQLiteCache {
	t.Helper()
	c, _ := newTestCacheAndStore(t)
	return c
}

func newTestCacheAndStore(t *testing.T) (*SQLiteCache, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cache-test.db")
	s, err := store.NewWithConfig(
		&config.Config{SQLiteJournalMode: "WAL"},
		dbPath,
		"llm-cache-encryption-key-for-tests",
	)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	c, err := NewSQLiteCache(s)
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	return c, s
}

func TestSQLiteCacheMiss(t *testing.T) {
	c := newTestCache(t)
	ent, hit, err := c.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hit {
		t.Fatal("expected cache miss")
	}
	if ent != nil {
		t.Fatalf("expected nil entry on miss, got %+v", ent)
	}
}

func TestSQLiteCacheSetGet(t *testing.T) {
	c, s := newTestCacheAndStore(t)
	ctx := context.Background()

	want := &Entry{
		Content: "hello", InputTokens: 10, OutputTokens: 5,
		ResponseID: "response-1", FinishReason: "stop",
		Provider: "fallback", Model: "fallback-model",
	}
	if err := c.Set(ctx, "k", want, time.Hour); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	got, hit, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit")
	}
	if got.Content != want.Content || got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens ||
		got.ResponseID != want.ResponseID || got.FinishReason != want.FinishReason ||
		got.Provider != want.Provider || got.Model != want.Model {
		t.Fatalf("entry mismatch: got %+v, want %+v", got, want)
	}
	var encrypted []byte
	if err := s.DB().QueryRow(`SELECT artifact FROM llm_cache WHERE cache_key = ?`, "k").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(want.Content)) {
		t.Fatal("cache content was persisted in plaintext")
	}
}

func TestSQLiteCacheRejectsContentThatRequiresRedaction(t *testing.T) {
	c, s := newTestCacheAndStore(t)
	err := c.Set(
		context.Background(),
		"unsafe",
		&Entry{Content: `{"apiToken":"provider-secret"}`},
		time.Hour,
	)
	if !errors.Is(err, ErrUnsafeCacheContent) {
		t.Fatalf("expected unsafe cache rejection, got %v", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM llm_cache WHERE cache_key = ?`, "unsafe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unsafe provider output reached the response cache")
	}
}

func TestSQLiteCacheSanitizesPlaintextResponseMetadata(t *testing.T) {
	c, s := newTestCacheAndStore(t)
	entry := &Entry{
		Content:  "safe",
		Provider: "provider", Model: "model",
		ResponseID: `token: provider-secret`,
	}
	if err := c.Set(context.Background(), "metadata", entry, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, hit, err := c.Get(context.Background(), "metadata")
	if err != nil || !hit {
		t.Fatalf("metadata cache read failed: hit=%v err=%v", hit, err)
	}
	if strings.Contains(got.ResponseID, "provider-secret") ||
		!strings.Contains(got.ResponseID, "[REDACTED]") {
		t.Fatalf("cache metadata was not redacted: %+v", got)
	}
	var storedResponseID string
	if err := s.DB().QueryRow(
		`SELECT COALESCE(response_id, '') FROM llm_cache WHERE cache_key = ?`,
		"metadata",
	).Scan(&storedResponseID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedResponseID, "provider-secret") {
		t.Fatalf("plaintext cache metadata retained a secret: %q", storedResponseID)
	}
}

func TestSQLiteCacheOverwrite(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", &Entry{Content: "first"}, time.Hour); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if err := c.Set(ctx, "k", &Entry{Content: "second", InputTokens: 2}, time.Hour); err != nil {
		t.Fatalf("overwrite failed: %v", err)
	}

	got, hit, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !hit || got.Content != "second" || got.InputTokens != 2 {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestSQLiteCacheIsWorkspaceScoped(t *testing.T) {
	c, s := newTestCacheAndStore(t)
	ctxA := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "a", WorkspaceID: authz.DefaultWorkspaceID})
	ctxB := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "b", WorkspaceID: "tenant-b"})
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}

	if err := c.Set(ctxA, "same-key", &Entry{Content: "workspace-a"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := c.Get(ctxB, "same-key"); err != nil || hit {
		t.Fatalf("workspace B should not read workspace A cache: hit=%v err=%v", hit, err)
	}
	if err := c.Set(ctxB, "same-key", &Entry{Content: "workspace-b"}, time.Hour); err != nil {
		t.Fatal(err)
	}

	gotA, hit, err := c.Get(ctxA, "same-key")
	if err != nil || !hit || gotA.Content != "workspace-a" {
		t.Fatalf("unexpected workspace A cache entry: got=%+v hit=%v err=%v", gotA, hit, err)
	}
	gotB, hit, err := c.Get(ctxB, "same-key")
	if err != nil || !hit || gotB.Content != "workspace-b" {
		t.Fatalf("unexpected workspace B cache entry: got=%+v hit=%v err=%v", gotB, hit, err)
	}
}

func TestSQLiteCacheExpires(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", &Entry{Content: "x"}, time.Millisecond); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	_, hit, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if hit {
		t.Fatal("expected expired entry to miss")
	}
}

func TestSQLiteCacheZeroTTLIgnored(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", &Entry{Content: "x"}, 0); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	_, hit, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if hit {
		t.Fatal("expected entry with zero TTL not to be cached")
	}
}

func TestSQLiteCacheNilStoreRejected(t *testing.T) {
	_, err := NewSQLiteCache(nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}
