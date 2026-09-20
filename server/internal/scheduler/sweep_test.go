package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type sweepFakeStore struct {
	expired         []string
	tasks           map[string]*models.Task
	updateStatusErr error
	releaseErr      error
	listErr         error
	getErr          error
	released        []string
	deadLettered    []string
	humanExpired    int
	humanExpireErr  error
}

func (f *sweepFakeStore) ExpireHumanInterventions(context.Context) (int64, error) {
	f.humanExpired++
	return 0, f.humanExpireErr
}

func (f *sweepFakeStore) EvictExpiredIdempotency(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (f *sweepFakeStore) ListExpiredLeases(ctx context.Context, before time.Time) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.expired, nil
}

func (f *sweepFakeStore) GetTaskByID(ctx context.Context, id string) (*models.Task, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.tasks[id], nil
}

func (f *sweepFakeStore) UpdateTaskStatus(ctx context.Context, taskID, workerID, status, message, errorType string) error {
	if f.updateStatusErr != nil {
		return f.updateStatusErr
	}
	f.deadLettered = append(f.deadLettered, taskID)
	return nil
}

func (f *sweepFakeStore) ReleaseTask(ctx context.Context, taskID string) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.released = append(f.released, taskID)
	return nil
}

func (f *sweepFakeStore) PurgeOldData(ctx context.Context, cfg *config.Config) (store.PurgeReport, error) {
	return store.PurgeReport{}, nil
}

func (f *sweepFakeStore) ListDueSchedules(ctx context.Context, before time.Time) ([]*models.Schedule, error) {
	return nil, nil
}

func (f *sweepFakeStore) GetRuleByID(ctx context.Context, id string) (*models.Rule, error) {
	return nil, nil
}

func (f *sweepFakeStore) CreateTask(ctx context.Context, t *models.Task) error {
	return nil
}

func (f *sweepFakeStore) UpdateSchedule(ctx context.Context, s *models.Schedule) error {
	return nil
}

func (f *sweepFakeStore) GetScheduleByID(ctx context.Context, id string) (*models.Schedule, error) {
	return nil, nil
}

func (f *sweepFakeStore) AcquireSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	return true, nil
}

func (f *sweepFakeStore) RenewSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	return true, nil
}

func (f *sweepFakeStore) ReleaseSchedulerLock(ctx context.Context, name, owner string) error {
	return nil
}

func TestNewScheduler(t *testing.T) {
	fs := &sweepFakeStore{}
	cfg := &config.Config{LeaseDuration: time.Minute, MaxRetries: 3}
	sch := New(fs, cfg, zap.NewNop(), time.Minute, 3)
	if sch == nil {
		t.Fatal("expected scheduler")
	}
	if sch.store != fs || sch.cfg != cfg || sch.leaseDur != time.Minute || sch.maxRetries != 3 {
		t.Fatal("scheduler fields not set correctly")
	}
}

func TestSweep_ReleasesExpiredLease(t *testing.T) {
	fs := &sweepFakeStore{
		expired: []string{"task-1"},
		tasks: map[string]*models.Task{
			"task-1": {ID: "task-1", RetryCount: 0, MaxRetries: 3},
		},
	}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.released) != 1 || fs.released[0] != "task-1" {
		t.Fatalf("expected task-1 released, got %v", fs.released)
	}
	if fs.humanExpired != 1 {
		t.Fatalf("expected human intervention expiry sweep, got %d calls", fs.humanExpired)
	}
}

func TestSweep_HumanInterventionExpiryError(t *testing.T) {
	fs := &sweepFakeStore{humanExpireErr: errors.New("db down")}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err == nil {
		t.Fatal("expected human intervention expiry error")
	}
}

func TestSweep_MarksDeadLetterWhenMaxRetriesReached(t *testing.T) {
	fs := &sweepFakeStore{
		expired: []string{"task-1"},
		tasks: map[string]*models.Task{
			"task-1": {ID: "task-1", RetryCount: 3, MaxRetries: 3},
		},
	}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.deadLettered) != 1 || fs.deadLettered[0] != "task-1" {
		t.Fatalf("expected task-1 dead-lettered, got %v", fs.deadLettered)
	}
	if len(fs.released) != 0 {
		t.Fatalf("expected no release, got %v", fs.released)
	}
}

func TestSweep_ListExpiredLeasesError(t *testing.T) {
	fs := &sweepFakeStore{listErr: errors.New("db down")}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestSweep_GetTaskError(t *testing.T) {
	fs := &sweepFakeStore{
		expired: []string{"task-1"},
		getErr:  errors.New("not found"),
	}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSweep_UpdateStatusError(t *testing.T) {
	fs := &sweepFakeStore{
		expired:         []string{"task-1"},
		tasks:           map[string]*models.Task{"task-1": {ID: "task-1", RetryCount: 3, MaxRetries: 3}},
		updateStatusErr: errors.New("update failed"),
	}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSweep_ReleaseError(t *testing.T) {
	fs := &sweepFakeStore{
		expired:    []string{"task-1"},
		tasks:      map[string]*models.Task{"task-1": {ID: "task-1", RetryCount: 0, MaxRetries: 3}},
		releaseErr: errors.New("release failed"),
	}
	sch := &Scheduler{store: fs, logger: zap.NewNop()}
	if err := sch.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStart_RunsSweepLoop(t *testing.T) {
	fs := &sweepFakeStore{
		expired: []string{"task-1"},
		tasks:   map[string]*models.Task{"task-1": {ID: "task-1", RetryCount: 0, MaxRetries: 3}},
	}
	sch := New(fs, &config.Config{LeaseDuration: time.Minute, MaxRetries: 3}, zap.NewNop(), time.Minute, 3)

	ctx, cancel := context.WithCancel(context.Background())
	sch.Start(ctx, 10*time.Millisecond)

	time.Sleep(35 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	if len(fs.released) < 1 {
		t.Fatalf("expected at least one release, got %d", len(fs.released))
	}
}

func TestScheduler_TriggerSchedule(t *testing.T) {
	sfs := newScheduleFakeStore()
	sfs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	sfs.schedules["sch-1"] = &models.Schedule{ID: "sch-1", RuleID: "rule-1"}

	sch := New(sfs, &config.Config{MaxRetries: 3}, zap.NewNop(), time.Minute, 3)
	taskID, err := sch.TriggerSchedule(context.Background(), "sch-1")
	if err != nil {
		t.Fatal(err)
	}
	if taskID == "" {
		t.Fatal("expected task id")
	}
}

func TestScheduler_StartSchedules(t *testing.T) {
	sfs := newScheduleFakeStore()
	sch := New(sfs, &config.Config{MaxRetries: 3}, zap.NewNop(), time.Minute, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Should not panic.
	sch.StartSchedules(ctx, time.Minute)
}
