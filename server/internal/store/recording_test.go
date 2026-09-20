package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	platformcrypto "github.com/singhand-labs/AegisCrawler/internal/crypto"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func newEncryptedTestStore(t *testing.T) *Store {
	t.Helper()
	file, err := os.CreateTemp("", "aegis-encrypted-*.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	store, err := New(file.Name(), "recording-encryption-key-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func recordingMetadata(id string, now time.Time) *models.Recording {
	return &models.Recording{
		ID: id, Owner: "admin", Status: models.RecordingStatusCaptured,
		ProtocolVersion: "2", SanitizationVersion: "server-v1", ActionCount: 1, SnapshotCount: 2,
		ContentHash: "hash", StartedAt: now, EndedAt: now, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
}

func TestRecordingStoreEncryptsScopesAndSoftDeletes(t *testing.T) {
	store := newEncryptedTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := store.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	archive := []byte(`{"events":[{"value":"server-redacted"}],"initialSnapshot":{}}`)
	recording := recordingMetadata("recording-a", now)
	if err := store.CreateRecording(ctxA, recording, archive, 1024*1024); err != nil {
		t.Fatal(err)
	}
	if recording.WorkspaceID != authz.DefaultWorkspaceID || recording.CompressedBytes == 0 {
		t.Fatalf("unexpected stored metadata: %+v", recording)
	}

	var encrypted []byte
	if err := store.db.QueryRow(`SELECT encrypted_payload FROM recordings WHERE id = ?`, recording.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("server-redacted")) || bytes.Equal(encrypted, archive) {
		t.Fatal("recording archive was stored in plaintext")
	}
	got, gotArchive, err := store.GetRecordingByID(ctxA, recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != authz.DefaultWorkspaceID || !bytes.Equal(gotArchive, archive) {
		t.Fatalf("recording round trip mismatch: metadata=%+v payload=%q", got, gotArchive)
	}
	if _, err := store.db.Exec(`UPDATE recordings SET content_hash = 'tampered' WHERE id = ?`, recording.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRecordingByID(ctxA, recording.ID); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("expected metadata tampering to be detected, got %v", err)
	}
	if _, err := store.db.Exec(`UPDATE recordings SET content_hash = ? WHERE id = ?`, recording.ContentHash, recording.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRecordingByID(ctxB, recording.ID); !errors.Is(err, ErrRecordingNotFound) {
		t.Fatalf("cross-workspace recording lookup should be hidden, got %v", err)
	}
	if err := store.DeleteRecording(ctxB, recording.ID); !errors.Is(err, ErrRecordingNotFound) {
		t.Fatalf("cross-workspace recording delete should be hidden, got %v", err)
	}
	if err := store.DeleteRecording(ctxA, recording.ID); err != nil {
		t.Fatal(err)
	}
	deleted, payload, err := store.GetRecordingByID(ctxA, recording.ID)
	if !errors.Is(err, ErrRecordingDeleted) || deleted == nil || payload != nil || deleted.ContentHash == "" || deleted.DeletedAt == nil {
		t.Fatalf("expected metadata-only tombstone: recording=%+v payload=%v err=%v", deleted, payload, err)
	}
	if err := store.db.QueryRow(`SELECT encrypted_payload FROM recordings WHERE id = ?`, recording.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted != nil {
		t.Fatal("soft deletion retained encrypted payload")
	}
}

func TestRecordingStoreRequiresEncryptionAndEnforcesCompressedLimit(t *testing.T) {
	plainStore := newTestStore(t)
	now := time.Now().UTC()
	if err := plainStore.CreateRecording(context.Background(), recordingMetadata("plain", now), []byte("payload"), 1024); !errors.Is(err, platformcrypto.ErrEncryptionKeyRequired) {
		t.Fatalf("expected encryption requirement, got %v", err)
	}

	encryptedStore := newEncryptedTestStore(t)
	incompressible := make([]byte, 4096)
	for i := range incompressible {
		incompressible[i] = byte(i * 31)
	}
	if err := encryptedStore.CreateRecording(context.Background(), recordingMetadata("large", now), incompressible, 8); !errors.Is(err, ErrRecordingTooLarge) {
		t.Fatalf("expected compressed size rejection, got %v", err)
	}
}

func TestDeleteExpiredRecordingsIsWorkspaceScoped(t *testing.T) {
	store := newEncryptedTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := store.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, item := range []struct {
		ctx context.Context
		id  string
	}{
		{ctxA, "expired-a"}, {ctxB, "expired-b"},
	} {
		recording := recordingMetadata(item.id, now)
		recording.ExpiresAt = now.Add(-time.Minute)
		if err := store.CreateRecording(item.ctx, recording, []byte(`{"archive":true}`), 1024); err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.DeleteExpiredRecordings(ctxA, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("unexpected expiration result: count=%d err=%v", count, err)
	}
	if _, _, err := store.GetRecordingByID(ctxB, "expired-b"); err != nil {
		t.Fatalf("workspace A expiration changed workspace B: %v", err)
	}
}
