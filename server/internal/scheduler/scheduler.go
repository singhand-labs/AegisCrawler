package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

// Ensure Scheduler embeds scheduleStore methods.
var _ scheduleStore = (schedulerStore)(nil)

// Scheduler manages task leases and recovery.
type Scheduler struct {
	store            schedulerStore
	cfg              *config.Config
	logger           *zap.Logger
	leaseDur         time.Duration
	maxRetries       int
	onRetentionPurge func(store.PurgeReport)
}

// schedulerStore is the subset of store.Store used by the scheduler.
type schedulerStore interface {
	scheduleStore
	ExpireHumanInterventions(ctx context.Context) (int64, error)
	EvictExpiredIdempotency(ctx context.Context, now time.Time) (int64, error)
	ListExpiredLeases(ctx context.Context, before time.Time) ([]string, error)
	GetTaskByID(ctx context.Context, id string) (*models.Task, error)
	UpdateTaskStatus(ctx context.Context, taskID, workerID, status, message, errorType string) error
	ReleaseTask(ctx context.Context, taskID string) error
	PurgeOldData(ctx context.Context, cfg *config.Config) (store.PurgeReport, error)
}

// New creates a scheduler.
func New(s schedulerStore, cfg *config.Config, logger *zap.Logger, leaseDur time.Duration, maxRetries int) *Scheduler {
	return &Scheduler{
		store:      s,
		cfg:        cfg,
		logger:     logger,
		leaseDur:   leaseDur,
		maxRetries: maxRetries,
	}
}

// SetRetentionCallback registers a callback invoked after each successful retention sweep.
func (s *Scheduler) SetRetentionCallback(cb func(store.PurgeReport)) {
	s.onRetentionPurge = cb
}

// Start begins the sweeper loop.
func (s *Scheduler) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				if err := s.sweep(ctx); err != nil {
					s.logger.Warn("sweep failed", zap.Error(err))
				}
			}
		}
	}()
}

// StartRetention begins the data-retention sweeper loop.
func (s *Scheduler) StartRetention(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				report, err := s.store.PurgeOldData(ctx, s.cfg)
				if err != nil {
					s.logger.Warn("retention sweep failed", zap.Error(err))
					continue
				}
				if s.onRetentionPurge != nil {
					s.onRetentionPurge(report)
				}
			}
		}
	}()
}

// StartSchedules begins the schedule evaluation loop.
func (s *Scheduler) StartSchedules(ctx context.Context, interval time.Duration) {
	runner := NewScheduleRunner(s.store, s.cfg, s.logger)
	runner.Start(ctx, interval)
}

// TriggerSchedule manually creates a task from a schedule and returns the task ID.
func (s *Scheduler) TriggerSchedule(ctx context.Context, scheduleID string) (string, error) {
	runner := NewScheduleRunner(s.store, s.cfg, s.logger)
	return runner.TriggerSchedule(ctx, scheduleID)
}

// sweep requeues expired leases or marks tasks as dead letter.
func (s *Scheduler) sweep(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := s.store.ExpireHumanInterventions(ctx); err != nil {
		return fmt.Errorf("expire human interventions: %w", err)
	}
	if _, err := s.store.EvictExpiredIdempotency(ctx, now); err != nil {
		return fmt.Errorf("evict expired idempotency keys: %w", err)
	}
	ids, err := s.store.ListExpiredLeases(ctx, now)
	if err != nil {
		return fmt.Errorf("list expired leases: %w", err)
	}
	for _, id := range ids {
		task, err := s.store.GetTaskByID(ctx, id)
		if err != nil {
			s.logger.Warn("failed to get task for sweep", zap.String("taskId", id), zap.Error(err))
			continue
		}
		if task.RetryCount >= task.MaxRetries {
			if err := s.store.UpdateTaskStatus(ctx, id, task.WorkerID.String, string(models.TaskStatusDeadLetter), "max retries exceeded", "MaxRetries"); err != nil {
				s.logger.Warn("failed to mark dead letter", zap.String("taskId", id), zap.Error(err))
			}
			s.logger.Info("task moved to dead letter", zap.String("taskId", id))
			continue
		}
		if err := s.store.ReleaseTask(ctx, id); err != nil {
			s.logger.Warn("failed to release task", zap.String("taskId", id), zap.Error(err))
			continue
		}
		s.logger.Info("task lease expired, requeued", zap.String("taskId", id), zap.Int("retryCount", task.RetryCount+1))
	}
	return nil
}
