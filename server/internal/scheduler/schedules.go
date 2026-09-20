package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

// ErrRuleNotActive is returned when a schedule's rule is disabled or not approved.
var ErrRuleNotActive = errors.New("rule is not active")

// scheduleStore is the subset of store.Store used by the schedule runner.
type scheduleStore interface {
	ListDueSchedules(ctx context.Context, before time.Time) ([]*models.Schedule, error)
	GetRuleByID(ctx context.Context, id string) (*models.Rule, error)
	CreateTask(ctx context.Context, t *models.Task) error
	UpdateSchedule(ctx context.Context, s *models.Schedule) error
	GetScheduleByID(ctx context.Context, id string) (*models.Schedule, error)
	AcquireSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error)
	RenewSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error)
	ReleaseSchedulerLock(ctx context.Context, name, owner string) error
}

// cronField holds the allowed values for a single cron field.
type cronField struct {
	min   int
	max   int
	valid map[int]bool
	star  bool // true when the field was literally "*"
}

// cronSchedule is a parsed 5-field cron expression.
type cronSchedule struct {
	minute *cronField
	hour   *cronField
	dom    *cronField
	month  *cronField
	dow    *cronField
	expr   string
}

// parseCron parses a standard 5-field cron expression:
// minute hour day-of-month month day-of-week.
// Supported syntax: * , - */n and plain integers.
func parseCron(expr string) (*cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}

	cs := &cronSchedule{expr: expr}
	var err error
	cs.minute, err = parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	cs.hour, err = parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	cs.dom, err = parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	cs.month, err = parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	cs.dow, err = parseCronField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}
	return cs, nil
}

func parseCronField(field string, min, max int) (*cronField, error) {
	cf := &cronField{min: min, max: max, valid: make(map[int]bool)}
	if field == "*" {
		cf.star = true
		for i := min; i <= max; i++ {
			cf.valid[i] = true
		}
		return cf, nil
	}

	// Handle step expressions like */5 or 1-10/2.
	step := 1
	base := field
	if parts := strings.Split(field, "/"); len(parts) == 2 {
		s, err := strconv.Atoi(parts[1])
		if err != nil || s <= 0 {
			return nil, fmt.Errorf("invalid step %q", parts[1])
		}
		step = s
		base = parts[0]
	} else if len(parts) > 2 {
		return nil, fmt.Errorf("invalid field %q", field)
	}

	// Expand base into individual values.
	values := make(map[int]bool)
	for _, part := range strings.Split(base, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			for i := min; i <= max; i++ {
				values[i] = true
			}
			continue
		}
		if rng := strings.Split(part, "-"); len(rng) == 2 {
			start, err := strconv.Atoi(rng[0])
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q", rng[0])
			}
			end, err := strconv.Atoi(rng[1])
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q", rng[1])
			}
			if start < min || end > max || start > end {
				return nil, fmt.Errorf("range %s out of bounds [%d,%d]", part, min, max)
			}
			for i := start; i <= end; i++ {
				values[i] = true
			}
		} else {
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", part)
			}
			if v < min || v > max {
				return nil, fmt.Errorf("value %d out of bounds [%d,%d]", v, min, max)
			}
			values[v] = true
		}
	}

	// Apply step by selecting every nth value in sorted order.
	sorted := make([]int, 0, len(values))
	for v := range values {
		sorted = append(sorted, v)
	}
	// Values can be sparse, so sort them and pick every step-th item.
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	for i := 0; i < len(sorted); i += step {
		cf.valid[sorted[i]] = true
	}
	return cf, nil
}

// nextRun returns the first time strictly after `after` that matches the cron schedule.
func (cs *cronSchedule) nextRun(after time.Time) (time.Time, error) {
	// Start searching one minute after the reference time so the result is strictly in the future.
	t := after.Add(time.Minute).Truncate(time.Minute)
	limit := after.Add(4 * 366 * 24 * time.Hour) // Guard against expressions that rarely match.
	for t.Before(limit) {
		if cs.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("could not find next run within 4 years for %q", cs.expr)
}

func (cs *cronSchedule) matches(t time.Time) bool {
	if !cs.minute.valid[t.Minute()] {
		return false
	}
	if !cs.hour.valid[t.Hour()] {
		return false
	}
	if !cs.month.valid[int(t.Month())] {
		return false
	}
	// Standard cron day matching: if either day field is unrestricted, the other is used.
	if cs.dom.star && cs.dow.star {
		return true
	}
	if cs.dom.star {
		return cs.dow.valid[int(t.Weekday())]
	}
	if cs.dow.star {
		return cs.dom.valid[t.Day()]
	}
	// Both restricted: OR semantics.
	return cs.dom.valid[t.Day()] || cs.dow.valid[int(t.Weekday())]
}

// ScheduleRunner periodically evaluates due schedules and creates tasks.
type ScheduleRunner struct {
	store      scheduleStore
	cfg        *config.Config
	logger     *zap.Logger
	instanceID string
}

// NewScheduleRunner creates a runner.
func NewScheduleRunner(s scheduleStore, cfg *config.Config, logger *zap.Logger) *ScheduleRunner {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a timestamp-based identifier; this should rarely happen.
		b = []byte(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return &ScheduleRunner{
		store:      s,
		cfg:        cfg,
		logger:     logger,
		instanceID: hex.EncodeToString(b),
	}
}

// Start begins the schedule evaluation loop.
func (r *ScheduleRunner) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	lockLease := r.cfg.SchedulerLockLease
	if lockLease <= 0 {
		lockLease = 2 * time.Minute
	}

	ticker := time.NewTicker(interval)
	acquired := false
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				if acquired {
					// M-1: ReleaseSchedulerLock must run even though ctx is
					// cancelled. Using the cancelled ctx makes the DB call fail
					// immediately with context.Canceled, so the lock lingers
					// until lease expiry (default 2min) and blocks failover.
					releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if err := r.store.ReleaseSchedulerLock(releaseCtx, "schedule_runner", r.instanceID); err != nil {
						r.logger.Warn("failed to release schedule runner lock", zap.Error(err))
					}
					cancel()
				}
				return
			case <-ticker.C:
				ok, err := r.store.AcquireSchedulerLock(ctx, "schedule_runner", r.instanceID, lockLease)
				if err != nil {
					r.logger.Warn("failed to acquire schedule runner lock", zap.Error(err))
					continue
				}
				if !ok {
					acquired = false
					continue
				}
				if !acquired {
					r.logger.Info("acquired schedule runner lock", zap.String("instanceId", r.instanceID))
					acquired = true
				}

				if err := r.runOnce(ctx); err != nil {
					r.logger.Warn("schedule run failed", zap.Error(err))
				}

				if _, err := r.store.RenewSchedulerLock(ctx, "schedule_runner", r.instanceID, lockLease); err != nil {
					r.logger.Warn("failed to renew schedule runner lock", zap.Error(err))
				}
			}
		}
	}()
}

func (r *ScheduleRunner) runOnce(ctx context.Context) error {
	now := time.Now().UTC()
	schedules, err := r.store.ListDueSchedules(ctx, now)
	if err != nil {
		return fmt.Errorf("list due schedules: %w", err)
	}
	for _, sch := range schedules {
		r.processSchedule(ctx, sch, now)
	}
	return nil
}

func (r *ScheduleRunner) processSchedule(ctx context.Context, sch *models.Schedule, now time.Time) {
	rule, err := r.store.GetRuleByID(ctx, sch.RuleID)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			r.logger.Warn("schedule references missing rule, disabling", zap.String("scheduleId", sch.ID), zap.String("ruleId", sch.RuleID))
		} else {
			r.logger.Warn("failed to load rule for schedule", zap.String("scheduleId", sch.ID), zap.Error(err))
		}
		_ = r.disableSchedule(ctx, sch)
		return
	}
	if !rule.Enabled || rule.ApprovalStatus != string(models.RuleApprovalApproved) {
		r.logger.Warn("schedule references inactive rule, disabling", zap.String("scheduleId", sch.ID), zap.String("ruleId", sch.RuleID))
		_ = r.disableSchedule(ctx, sch)
		return
	}

	scheduledAt := sch.NextRunAt
	if !scheduledAt.Valid {
		scheduledAt = sql.NullTime{Time: now, Valid: true}
	}

	taskID, err := r.createTaskFromSchedule(ctx, sch, rule, scheduledAt.Time)
	if err != nil {
		r.logger.Warn("failed to create task from schedule", zap.String("scheduleId", sch.ID), zap.Error(err))
		return
	}
	r.logger.Info("created task from schedule", zap.String("scheduleId", sch.ID), zap.String("taskId", taskID))

	// Update schedule bookkeeping and compute next run.
	sch.LastRunAt = sql.NullTime{Time: now, Valid: true}
	if sch.Type == models.ScheduleTypeOnce {
		sch.NextRunAt = sql.NullTime{Valid: false}
		sch.Enabled = false
	} else {
		next, err := ComputeNextRunInLocation(sch.Expression, sch.NextRunAt.Time, sch.Timezone)
		if err != nil {
			r.logger.Warn("failed to compute next run, disabling schedule", zap.String("scheduleId", sch.ID), zap.Error(err))
			sch.Enabled = false
		} else {
			// Catch-up logic: if the computed next run is still in the past, decide whether
			// to run once at the missed time or skip ahead to the next future run.
			if !next.After(now) {
				if sch.Catchup == models.CatchupRunOnce {
					missedTaskID, err := r.createTaskFromSchedule(ctx, sch, rule, next)
					if err != nil {
						r.logger.Warn("failed to create catch-up task", zap.String("scheduleId", sch.ID), zap.Error(err))
					} else {
						r.logger.Info("created catch-up task from schedule", zap.String("scheduleId", sch.ID), zap.String("taskId", missedTaskID))
					}
					next, err = ComputeNextRunInLocation(sch.Expression, next, sch.Timezone)
					if err != nil {
						r.logger.Warn("failed to compute next run after catch-up, disabling schedule", zap.String("scheduleId", sch.ID), zap.Error(err))
						sch.Enabled = false
					}
				}
				// Advance to the next future run regardless of catchup mode.
				// H-5: cap iterations so a schedule that fell days/weeks behind
				// (server offline, migration backfill, etc.) cannot starve the
				// schedule runner goroutine. If the cap is hit, jump NextRunAt
				// forward to "now" so the next iteration computes a fresh future
				// run in O(1) instead of O(elapsed/interval).
				maxCatchUpIterations := 1000
				iter := 0
				for !next.After(now) {
					iter++
					if iter > maxCatchUpIterations {
						r.logger.Warn("schedule catch-up iteration cap hit; jumping NextRunAt to now",
							zap.String("scheduleId", sch.ID),
							zap.Int("iterations", iter),
							zap.Time("originalNextRunAt", sch.NextRunAt.Time),
							zap.Time("now", now),
						)
						next = now
						break
					}
					next, err = ComputeNextRunInLocation(sch.Expression, next, sch.Timezone)
					if err != nil {
						r.logger.Warn("failed to compute next run while catching up, disabling schedule", zap.String("scheduleId", sch.ID), zap.Error(err))
						sch.Enabled = false
						break
					}
				}
			}
			sch.NextRunAt = sql.NullTime{Time: next, Valid: true}
		}
	}
	sch.UpdatedAt = now
	if err := r.store.UpdateSchedule(ctx, sch); err != nil {
		r.logger.Warn("failed to update schedule after run", zap.String("scheduleId", sch.ID), zap.Error(err))
	}
}

func (r *ScheduleRunner) createTaskFromSchedule(ctx context.Context, sch *models.Schedule, rule *models.Rule, scheduledAt time.Time) (string, error) {
	versionNumber := sch.RuleVersionNumber
	// Legacy schedules predate immutable rule-version contracts. Their migrated
	// input schema is the empty object marker; keep using the compatible catalog
	// path rather than pretending that a durable version exists.
	if schema := strings.TrimSpace(string(sch.InputSchema)); schema == "" || schema == "{}" {
		versionNumber = 0
	}
	task := &models.Task{
		ID:                store.NewID(),
		RuleID:            sch.RuleID,
		RuleVersion:       sch.RuleVersion,
		RuleVersionNumber: versionNumber,
		Status:            models.TaskStatusPending,
		Priority:          sch.Priority,
		Variables:         sch.Variables,
		InputSchema:       sch.InputSchema,
		BrowserProfileID:  sch.BrowserProfileID,
		MaxRetries:        sch.MaxRetries,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		ScheduledAt:       sql.NullTime{Time: scheduledAt, Valid: true},
		ScheduleID:        sql.NullString{String: sch.ID, Valid: true},
	}
	if task.Priority == "" {
		task.Priority = rule.Priority
	}
	if task.Priority == "" {
		task.Priority = models.PriorityNormal
	}
	if err := r.store.CreateTask(ctx, task); err != nil {
		return "", err
	}
	return task.ID, nil
}

func (r *ScheduleRunner) disableSchedule(ctx context.Context, sch *models.Schedule) error {
	sch.Enabled = false
	sch.UpdatedAt = time.Now().UTC()
	return r.store.UpdateSchedule(ctx, sch)
}

// TriggerSchedule manually creates a task from a schedule without updating NextRunAt.
func (r *ScheduleRunner) TriggerSchedule(ctx context.Context, scheduleID string) (string, error) {
	sch, err := r.store.GetScheduleByID(ctx, scheduleID)
	if err != nil {
		return "", err
	}
	rule, err := r.store.GetRuleByID(ctx, sch.RuleID)
	if err != nil {
		return "", err
	}
	if !rule.Enabled || rule.ApprovalStatus != string(models.RuleApprovalApproved) {
		return "", fmt.Errorf("%w: rule %s is not active", ErrRuleNotActive, sch.RuleID)
	}
	scheduledAt := time.Now().UTC()
	if sch.NextRunAt.Valid {
		scheduledAt = sch.NextRunAt.Time
	}
	return r.createTaskFromSchedule(ctx, sch, rule, scheduledAt)
}

// ComputeNextRun parses the cron expression and returns the first run strictly after `after`.
func ComputeNextRun(expr string, after time.Time) (time.Time, error) {
	return ComputeNextRunInLocation(expr, after, "UTC")
}

// ComputeNextRunInLocation evaluates cron fields in an IANA timezone and
// returns the persisted instant in UTC.
func ComputeNextRunInLocation(expr string, after time.Time, timezone string) (time.Time, error) {
	cs, err := parseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	next, err := cs.nextRun(after.In(location))
	if err != nil {
		return time.Time{}, err
	}
	return next.UTC(), nil
}

// ValidateCron checks whether a cron expression is parsable.
func ValidateCron(expr string) error {
	_, err := parseCron(expr)
	return err
}
