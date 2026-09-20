package recording

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
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

func newRecordingService(t *testing.T, cfg *config.Config) (*Service, *store.Store) {
	t.Helper()
	persistence, err := store.New(filepath.Join(t.TempDir(), "recordings.db"), "recording-service-test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	return NewService(persistence, cfg), persistence
}

func validRecordingPayload() map[string]any {
	return map[string]any{
		"version": "2",
		"events":  []any{map[string]any{"type": "input", "value": "password=hunter2"}},
		"snapshots": []any{
			map[string]any{"url": "https://example.com", "type": "password", "value": "plain-secret", "outerHTML": "<input>"},
			map[string]any{"url": "https://example.com", "text": "done for user@example.com"},
		},
	}
}

func TestServiceSanitizesArchivesAndReconstructsRecording(t *testing.T) {
	service, persistence := newRecordingService(t, &config.Config{
		RecordingMaxActions: 500, RecordingMaxDuration: time.Hour,
		RecordingMaxCompressedBytes: 1024 * 1024, RecordingRetention: 24 * time.Hour,
	})
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "alice", WorkspaceID: authz.DefaultWorkspaceID})
	now := time.Now().UTC()
	recording, err := service.Create(ctx, CreateInput{Payload: validRecordingPayload(), StartedAt: now.Add(-time.Minute), EndedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if recording.Owner != "alice" || recording.ActionCount != 1 || recording.SnapshotCount != 2 || recording.RedactionCount < 3 || recording.RemovedFieldCount != 1 {
		t.Fatalf("unexpected recording metadata: %+v", recording)
	}
	got, err := service.Get(ctx, recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(marshalCanonical(got.Payload))
	for _, secret := range []string{"hunter2", "plain-secret", "user@example.com", "<input>"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("reconstructed payload retained %q: %s", secret, serialized)
		}
	}
	if !strings.Contains(serialized, Redacted) {
		t.Fatalf("expected redaction markers: %s", serialized)
	}

	var ciphertext []byte
	if err := persistence.DB().QueryRow(`SELECT encrypted_payload FROM recordings WHERE id = ?`, recording.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("hunter2")) || bytes.Contains(ciphertext, []byte("REDACTED")) {
		t.Fatal("database blob exposed recording content")
	}
}

func TestServiceRejectsLimitAndShapeViolations(t *testing.T) {
	service, _ := newRecordingService(t, &config.Config{
		RecordingMaxActions: 1, RecordingMaxDuration: time.Minute, RecordingMaxCompressedBytes: 1024 * 1024,
	})
	payload := validRecordingPayload()
	payload["events"] = []any{map[string]any{"type": "click"}, map[string]any{"type": "click"}}
	if _, err := service.Create(context.Background(), CreateInput{Payload: payload}); !errors.Is(err, ErrActionLimitExceeded) {
		t.Fatalf("expected action limit error, got %v", err)
	}
	if _, err := service.Create(context.Background(), CreateInput{
		Payload: validRecordingPayload(), StartedAt: time.Now().Add(-2 * time.Minute), EndedAt: time.Now(),
	}); !errors.Is(err, ErrDurationLimitExceeded) {
		t.Fatalf("expected duration limit error, got %v", err)
	}
	if _, err := service.Create(context.Background(), CreateInput{
		Payload: validRecordingPayload(), StartedAt: time.Now(), EndedAt: time.Now().Add(-time.Minute),
	}); !errors.Is(err, ErrInvalidTimeRange) {
		t.Fatalf("expected invalid time range, got %v", err)
	}
	if _, err := service.Create(context.Background(), CreateInput{Payload: map[string]any{"snapshots": []any{map[string]any{}}}}); !errors.Is(err, ErrSnapshotsRequired) {
		t.Fatalf("expected required snapshots error, got %v", err)
	}
}
