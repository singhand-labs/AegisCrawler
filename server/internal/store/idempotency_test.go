package store

import (
	"context"
	"testing"
	"time"
)

func TestIdempotencyCacheGetMiss(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	lookup, err := s.GetIdempotency(ctx, "ws", "key-1", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry != nil {
		t.Fatalf("expected miss (nil entry), got %+v", lookup.Entry)
	}
	if lookup.Conflict {
		t.Fatal("expected Conflict=false on miss")
	}
}

func TestIdempotencyCacheInsertThenGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	body := []byte(`{"ok":true}`)
	ttl := 24 * time.Hour
	if err := s.PutIdempotency(ctx, "ws", "key-1", "hash-a", 200, body, ttl); err != nil {
		t.Fatal(err)
	}

	lookup, err := s.GetIdempotency(ctx, "ws", "key-1", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry == nil {
		t.Fatal("expected hit, got miss")
	}
	if lookup.Conflict {
		t.Fatal("expected Conflict=false on matching hash")
	}
	if lookup.Entry.ResponseStatus != 200 {
		t.Fatalf("expected status 200, got %d", lookup.Entry.ResponseStatus)
	}
	if string(lookup.Entry.ResponseBody) != string(body) {
		t.Fatalf("expected body %q, got %q", body, lookup.Entry.ResponseBody)
	}
}

func TestIdempotencyCacheGetHashMismatchReturnsConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutIdempotency(ctx, "ws", "key-1", "hash-a", 200, []byte(`{}`), 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	lookup, err := s.GetIdempotency(ctx, "ws", "key-1", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry != nil {
		t.Fatalf("expected nil entry on conflict, got %+v", lookup.Entry)
	}
	if !lookup.Conflict {
		t.Fatal("expected Conflict=true on hash mismatch")
	}
}

func TestIdempotencyCacheGetExpiredReturnsMiss(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert with a TTL in the past.
	if err := s.PutIdempotency(ctx, "ws", "key-1", "hash-a", 200, []byte(`{}`), -1*time.Minute); err != nil {
		t.Fatal(err)
	}

	lookup, err := s.GetIdempotency(ctx, "ws", "key-1", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry != nil {
		t.Fatalf("expected miss for expired entry, got %+v", lookup.Entry)
	}
	if lookup.Conflict {
		t.Fatal("expected Conflict=false for expired entry")
	}
}

func TestIdempotencyCacheEvictExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// expired entry
	if err := s.PutIdempotency(ctx, "ws", "key-expired", "hash-a", 200, []byte(`{}`), -1*time.Minute); err != nil {
		t.Fatal(err)
	}
	// live entry
	if err := s.PutIdempotency(ctx, "ws", "key-live", "hash-b", 200, []byte(`{}`), 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.EvictExpiredIdempotency(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	// Live entry should still be present.
	lookup, err := s.GetIdempotency(ctx, "ws", "key-live", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry == nil {
		t.Fatal("expected live entry to remain after eviction")
	}

	// Expired entry should be gone (miss, not conflict).
	lookup, err = s.GetIdempotency(ctx, "ws", "key-expired", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry != nil || lookup.Conflict {
		t.Fatal("expected expired entry to be fully gone")
	}
}

func TestIdempotencyCachePutUpserts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutIdempotency(ctx, "ws", "key-1", "hash-a", 200, []byte(`{"v":1}`), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// Upsert with new body and hash.
	if err := s.PutIdempotency(ctx, "ws", "key-1", "hash-b", 201, []byte(`{"v":2}`), 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	lookup, err := s.GetIdempotency(ctx, "ws", "key-1", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Entry == nil {
		t.Fatal("expected hit after upsert")
	}
	if lookup.Entry.ResponseStatus != 201 {
		t.Fatalf("expected status 201 after upsert, got %d", lookup.Entry.ResponseStatus)
	}
	if string(lookup.Entry.ResponseBody) != `{"v":2}` {
		t.Fatalf("expected updated body, got %q", lookup.Entry.ResponseBody)
	}

	// Old hash should now conflict.
	lookup, err = s.GetIdempotency(ctx, "ws", "key-1", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.Conflict {
		t.Fatal("expected Conflict=true for old hash after upsert")
	}
}
