package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type fakeStore struct {
	purgeCalls int
}

func (f *fakeStore) ExpireHumanInterventions(context.Context) (int64, error) {
	return 0, nil
}

func (f *fakeStore) EvictExpiredIdempotency(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeStore) ListExpiredLeases(ctx context.Context, before time.Time) ([]string, error) {
	return nil, nil
}

func (f *fakeStore) GetTaskByID(ctx context.Context, id string) (*models.Task, error) {
	return nil, nil
}

func (f *fakeStore) UpdateTaskStatus(ctx context.Context, taskID, workerID, status, message, errorType string) error {
	return nil
}

func (f *fakeStore) ReleaseTask(ctx context.Context, taskID string) error {
	return nil
}

func (f *fakeStore) PurgeOldData(ctx context.Context, cfg *config.Config) (store.PurgeReport, error) {
	f.purgeCalls++
	return store.PurgeReport{}, nil
}

func (f *fakeStore) ListDueSchedules(ctx context.Context, before time.Time) ([]*models.Schedule, error) {
	return nil, nil
}

func (f *fakeStore) GetRuleByID(ctx context.Context, id string) (*models.Rule, error) {
	return nil, nil
}

func (f *fakeStore) CreateTask(ctx context.Context, t *models.Task) error {
	return nil
}

func (f *fakeStore) UpdateSchedule(ctx context.Context, s *models.Schedule) error {
	return nil
}

func (f *fakeStore) GetScheduleByID(ctx context.Context, id string) (*models.Schedule, error) {
	return nil, nil
}

func (f *fakeStore) AcquireSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	return true, nil
}

func (f *fakeStore) RenewSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	return true, nil
}

func (f *fakeStore) ReleaseSchedulerLock(ctx context.Context, name, owner string) error {
	return nil
}

func TestStartRetention(t *testing.T) {
	cfg := &config.Config{
		LeaseDuration:          time.Minute,
		MaxRetries:             3,
		ResultRetention:        time.Hour,
		LogRetention:           time.Hour,
		SnapshotRetention:      time.Hour,
		HeartbeatRetention:     time.Hour,
		StatusUpdateRetention:  time.Hour,
		CheckpointRetention:    time.Hour,
		CompletedTaskRetention: time.Hour,
		RetentionBatchSize:     100,
	}
	fs := &fakeStore{}
	sch := &Scheduler{
		store:      fs,
		cfg:        cfg,
		logger:     zap.NewNop(),
		leaseDur:   cfg.LeaseDuration,
		maxRetries: cfg.MaxRetries,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sch.StartRetention(ctx, 10*time.Millisecond)

	// Wait long enough for at least one tick to fire.
	time.Sleep(35 * time.Millisecond)

	if fs.purgeCalls < 1 {
		t.Fatalf("expected at least one PurgeOldData call, got %d", fs.purgeCalls)
	}
}

type callbackFakeStore struct {
	purgeCalls int
	report     store.PurgeReport
}

func (f *callbackFakeStore) ExpireHumanInterventions(context.Context) (int64, error) {
	return 0, nil
}

func (f *callbackFakeStore) EvictExpiredIdempotency(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (f *callbackFakeStore) ListExpiredLeases(ctx context.Context, before time.Time) ([]string, error) {
	return nil, nil
}

func (f *callbackFakeStore) GetTaskByID(ctx context.Context, id string) (*models.Task, error) {
	return nil, nil
}

func (f *callbackFakeStore) UpdateTaskStatus(ctx context.Context, taskID, workerID, status, message, errorType string) error {
	return nil
}

func (f *callbackFakeStore) ReleaseTask(ctx context.Context, taskID string) error {
	return nil
}

func (f *callbackFakeStore) PurgeOldData(ctx context.Context, cfg *config.Config) (store.PurgeReport, error) {
	f.purgeCalls++
	return f.report, nil
}

func (f *callbackFakeStore) ListDueSchedules(ctx context.Context, before time.Time) ([]*models.Schedule, error) {
	return nil, nil
}

func (f *callbackFakeStore) GetRuleByID(ctx context.Context, id string) (*models.Rule, error) {
	return nil, nil
}

func (f *callbackFakeStore) CreateTask(ctx context.Context, t *models.Task) error {
	return nil
}

func (f *callbackFakeStore) UpdateSchedule(ctx context.Context, s *models.Schedule) error {
	return nil
}

func (f *callbackFakeStore) GetScheduleByID(ctx context.Context, id string) (*models.Schedule, error) {
	return nil, nil
}

func (f *callbackFakeStore) AcquireSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	return true, nil
}

func (f *callbackFakeStore) RenewSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	return true, nil
}

func (f *callbackFakeStore) ReleaseSchedulerLock(ctx context.Context, name, owner string) error {
	return nil
}

func TestStartRetentionCallback(t *testing.T) {
	cfg := &config.Config{
		LeaseDuration:          time.Minute,
		MaxRetries:             3,
		ResultRetention:        time.Hour,
		LogRetention:           time.Hour,
		SnapshotRetention:      time.Hour,
		HeartbeatRetention:     time.Hour,
		StatusUpdateRetention:  time.Hour,
		CheckpointRetention:    time.Hour,
		CompletedTaskRetention: time.Hour,
		RetentionBatchSize:     100,
	}
	report := store.PurgeReport{"results": 5, "logs": 0}
	fs := &callbackFakeStore{report: report}
	sch := &Scheduler{
		store:      fs,
		cfg:        cfg,
		logger:     zap.NewNop(),
		leaseDur:   cfg.LeaseDuration,
		maxRetries: cfg.MaxRetries,
	}

	received := make(chan store.PurgeReport, 1)
	sch.SetRetentionCallback(func(r store.PurgeReport) {
		received <- r
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sch.StartRetention(ctx, 10*time.Millisecond)

	select {
	case got := <-received:
		if got["results"] != 5 {
			t.Fatalf("expected callback report results=5, got %v", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected retention callback to be invoked")
	}
}
