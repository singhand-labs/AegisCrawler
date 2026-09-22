package recording

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

var (
	ErrActionLimitExceeded   = errors.New("recording action limit exceeded")
	ErrDurationLimitExceeded = errors.New("recording duration limit exceeded")
	ErrInvalidTimeRange      = errors.New("recording end time precedes start time")
)

type CreateInput struct {
	Payload   map[string]any
	StartedAt time.Time
	EndedAt   time.Time
}

type Service struct {
	store *store.Store
	cfg   *config.Config
}

func NewService(persistence *store.Store, cfg *config.Config) *Service {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Service{store: persistence, cfg: cfg}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (*models.Recording, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("recording store is unavailable")
	}
	now := time.Now().UTC()
	startedAt := input.StartedAt.UTC()
	if input.StartedAt.IsZero() {
		startedAt = now
	}
	endedAt := input.EndedAt.UTC()
	if input.EndedAt.IsZero() {
		endedAt = now
	}
	if endedAt.Before(startedAt) {
		return nil, ErrInvalidTimeRange
	}
	maxDuration := s.cfg.RecordingMaxDuration
	if maxDuration <= 0 {
		maxDuration = 2 * time.Hour
	}
	if endedAt.Sub(startedAt) > maxDuration {
		return nil, fmt.Errorf("%w: got %s, limit %s", ErrDurationLimitExceeded, endedAt.Sub(startedAt), maxDuration)
	}

	// Resolve client-side snapshot references before sanitize/archive so every
	// downstream consumer keeps seeing full snapshots. Invalid references
	// fail closed rather than silently dropping per-action evidence.
	if err := ExpandSnapshotReferences(input.Payload); err != nil {
		return nil, err
	}

	sanitized, report := Sanitize(input.Payload)
	archive, stats, err := BuildArchive(sanitized)
	if err != nil {
		return nil, err
	}
	maxActions := s.cfg.RecordingMaxActions
	if maxActions <= 0 {
		maxActions = 500
	}
	if stats.ActionCount > maxActions {
		return nil, fmt.Errorf("%w: got %d, limit %d", ErrActionLimitExceeded, stats.ActionCount, maxActions)
	}
	archiveJSON, err := json.Marshal(archive)
	if err != nil {
		return nil, fmt.Errorf("marshal recording archive: %w", err)
	}
	digest := sha256.Sum256(archiveJSON)
	owner := authz.Subject(ctx, "system")
	protocolVersion := "2"
	if value, ok := sanitized["version"].(string); ok && value != "" {
		protocolVersion = value
	}
	retention := s.cfg.RecordingRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	rec := &models.Recording{
		ID:                  store.NewID(),
		Owner:               owner,
		Status:              models.RecordingStatusCaptured,
		ProtocolVersion:     protocolVersion,
		SanitizationVersion: "server-v1",
		RedactionCount:      report.RedactedValues,
		RemovedFieldCount:   report.RemovedFields,
		ActionCount:         stats.ActionCount,
		SnapshotCount:       stats.SnapshotCount,
		ContentHash:         fmt.Sprintf("%x", digest),
		StartedAt:           startedAt,
		EndedAt:             endedAt,
		CreatedAt:           now,
		UpdatedAt:           now,
		ExpiresAt:           now.Add(retention),
	}
	maxCompressed := s.cfg.RecordingMaxCompressedBytes
	if maxCompressed <= 0 {
		maxCompressed = 25 * 1024 * 1024
	}
	if err := s.store.CreateRecording(ctx, rec, archiveJSON, maxCompressed); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) Get(ctx context.Context, id string) (*models.Recording, error) {
	rec, archiveJSON, err := s.store.GetRecordingByID(ctx, id)
	if err != nil {
		return rec, err
	}
	archive := &Archive{}
	if err := json.Unmarshal(archiveJSON, archive); err != nil {
		return nil, fmt.Errorf("decode recording archive: %w", err)
	}
	payload, err := archive.Reconstruct()
	if err != nil {
		return nil, err
	}
	rec.Payload = payload
	return rec, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DeleteRecording(ctx, id)
}
