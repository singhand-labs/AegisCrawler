package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func TestParseCronValid(t *testing.T) {
	cases := []struct {
		expr string
	}{
		{"* * * * *"},
		{"0 * * * *"},
		{"*/5 * * * *"},
		{"0 9-17 * * 1-5"},
		{"0,15,30,45 * * * *"},
		{"0 0 1 * *"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			if err := ValidateCron(tc.expr); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestParseCronInvalid(t *testing.T) {
	cases := []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"abc * * * *",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if err := ValidateCron(expr); err == nil {
				t.Fatal("expected invalid")
			}
		})
	}
}

func TestCronNextRun(t *testing.T) {
	ref := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	next, err := ComputeNextRun("0 * * * *", ref)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected %v, got %v", want, next)
	}

	// Reference at 12:05 should next run at 13:00 for hourly schedule.
	next, err = ComputeNextRun("0 * * * *", ref.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(want) {
		t.Fatalf("expected %v, got %v", want, next)
	}

	// Every 5 minutes starting at 12:01 should next run at 12:05.
	next, err = ComputeNextRun("*/5 * * * *", ref.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2024, 1, 1, 12, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected %v, got %v", want, next)
	}
}

func TestCronDayOfWeek(t *testing.T) {
	// 2024-01-01 is Monday. Next Monday at 09:00.
	ref := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	next, err := ComputeNextRun("0 9 * * 1", ref)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 8, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected %v, got %v", want, next)
	}
}

func TestComputeNextRunInLocationUsesScheduleTimezone(t *testing.T) {
	// 00:30 UTC is 08:30 in Taipei, so the next local 09:00 is 01:00 UTC.
	reference := time.Date(2024, 1, 1, 0, 30, 0, 0, time.UTC)
	next, err := ComputeNextRunInLocation("0 9 * * *", reference, "Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected local schedule at %v, got %v", want, next)
	}
	if next.Location() != time.UTC {
		t.Fatalf("persisted schedule instant must be UTC, got %v", next.Location())
	}
	if _, err := ComputeNextRunInLocation("0 9 * * *", reference, "Mars/Olympus"); err == nil {
		t.Fatal("expected invalid IANA timezone to fail")
	}
}

type scheduleFakeStore struct {
	createdTasks      []*models.Task
	updatedSchedules  []*models.Schedule
	rules             map[string]*models.Rule
	schedules         map[string]*models.Schedule
	due               []*models.Schedule
	locks             map[string]*lockRow
	acquireErr        error
	acquireOK         bool
	renewErr          error
	getRuleErr        error
	createTaskErr     error
	updateScheduleErr error
}

func (f *scheduleFakeStore) ExpireHumanInterventions(context.Context) (int64, error) {
	return 0, nil
}

func (f *scheduleFakeStore) EvictExpiredIdempotency(context.Context, time.Time) (int64, error) {
	return 0, nil
}

type lockRow struct {
	owner     string
	expiresAt time.Time
}

func newScheduleFakeStore() *scheduleFakeStore {
	return &scheduleFakeStore{
		rules:     make(map[string]*models.Rule),
		schedules: make(map[string]*models.Schedule),
		locks:     make(map[string]*lockRow),
		acquireOK: true,
	}
}

func (f *scheduleFakeStore) ListDueSchedules(ctx context.Context, before time.Time) ([]*models.Schedule, error) {
	return f.due, nil
}

func (f *scheduleFakeStore) GetRuleByID(ctx context.Context, id string) (*models.Rule, error) {
	if f.getRuleErr != nil {
		return nil, f.getRuleErr
	}
	r, ok := f.rules[id]
	if !ok {
		return nil, store.ErrRuleNotFound
	}
	return r, nil
}

func (f *scheduleFakeStore) CreateTask(ctx context.Context, t *models.Task) error {
	if f.createTaskErr != nil {
		return f.createTaskErr
	}
	f.createdTasks = append(f.createdTasks, t)
	return nil
}

func (f *scheduleFakeStore) UpdateSchedule(ctx context.Context, s *models.Schedule) error {
	if f.updateScheduleErr != nil {
		return f.updateScheduleErr
	}
	f.updatedSchedules = append(f.updatedSchedules, s)
	f.schedules[s.ID] = s
	return nil
}

func (f *scheduleFakeStore) GetScheduleByID(ctx context.Context, id string) (*models.Schedule, error) {
	s, ok := f.schedules[id]
	if !ok {
		return nil, store.ErrRuleNotFound
	}
	return s, nil
}

func (f *scheduleFakeStore) AcquireSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	if f.acquireErr != nil {
		return false, f.acquireErr
	}
	if !f.acquireOK {
		return false, nil
	}
	now := time.Now().UTC()
	row, ok := f.locks[name]
	if !ok || row.expiresAt.Before(now) || row.expiresAt.Equal(now) {
		f.locks[name] = &lockRow{owner: owner, expiresAt: now.Add(lease)}
		return true, nil
	}
	return false, nil
}

func (f *scheduleFakeStore) RenewSchedulerLock(ctx context.Context, name, owner string, lease time.Duration) (bool, error) {
	if f.renewErr != nil {
		return false, f.renewErr
	}
	row, ok := f.locks[name]
	if !ok || row.owner != owner {
		return false, nil
	}
	row.expiresAt = time.Now().UTC().Add(lease)
	return true, nil
}

func (f *scheduleFakeStore) ReleaseSchedulerLock(ctx context.Context, name, owner string) error {
	row, ok := f.locks[name]
	if ok && row.owner == owner {
		delete(f.locks, name)
	}
	return nil
}

// schedulerStore methods so scheduleFakeStore can be used with New/Scheduler.
func (f *scheduleFakeStore) ListExpiredLeases(ctx context.Context, before time.Time) ([]string, error) {
	return nil, nil
}

func (f *scheduleFakeStore) GetTaskByID(ctx context.Context, id string) (*models.Task, error) {
	return nil, nil
}

func (f *scheduleFakeStore) UpdateTaskStatus(ctx context.Context, taskID, workerID, status, message, errorType string) error {
	return nil
}

func (f *scheduleFakeStore) ReleaseTask(ctx context.Context, taskID string) error {
	return nil
}

func (f *scheduleFakeStore) PurgeOldData(ctx context.Context, cfg *config.Config) (store.PurgeReport, error) {
	return store.PurgeReport{}, nil
}

func TestRunnerCreatesTaskForDueCronSchedule(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
		Priority:       models.PriorityNormal,
	}
	now := time.Now().UTC()
	sch := &models.Schedule{
		ID:          "sch-1",
		RuleID:      "rule-1",
		RuleVersion: "1.0.0",
		Type:        models.ScheduleTypeCron,
		Expression:  "*/5 * * * *",
		Enabled:     true,
		NextRunAt:   sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
		Priority:    models.PriorityHigh,
		MaxRetries:  5,
		Catchup:     models.CatchupSkip,
	}
	fs.due = []*models.Schedule{sch}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.createdTasks))
	}
	task := fs.createdTasks[0]
	if task.RuleID != "rule-1" {
		t.Fatalf("expected rule-1, got %s", task.RuleID)
	}
	if task.Priority != models.PriorityHigh {
		t.Fatalf("expected high priority, got %s", task.Priority)
	}
	if task.MaxRetries != 5 {
		t.Fatalf("expected maxRetries 5, got %d", task.MaxRetries)
	}
	if !task.ScheduleID.Valid || task.ScheduleID.String != "sch-1" {
		t.Fatalf("expected schedule id sch-1, got %v", task.ScheduleID)
	}
	if !task.ScheduledAt.Valid {
		t.Fatal("expected scheduled_at set")
	}
	if len(fs.updatedSchedules) != 1 {
		t.Fatalf("expected schedule update, got %d", len(fs.updatedSchedules))
	}
	updated := fs.updatedSchedules[0]
	if !updated.LastRunAt.Valid {
		t.Fatal("expected last_run_at set")
	}
	if !updated.NextRunAt.Valid {
		t.Fatal("expected next_run_at set")
	}
	if updated.NextRunAt.Time.Before(now) {
		t.Fatalf("expected next_run_at after now, got %v", updated.NextRunAt.Time)
	}
}

func TestRunnerDisablesOnceSchedule(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	sch := &models.Schedule{
		ID:         "sch-2",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeOnce,
		Expression: now.Add(-time.Minute).Format(time.RFC3339),
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}
	fs.due = []*models.Schedule{sch}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.createdTasks))
	}
	updated := fs.updatedSchedules[0]
	if updated.Enabled {
		t.Fatal("expected once schedule disabled")
	}
	if updated.NextRunAt.Valid {
		t.Fatal("expected next_run_at null")
	}
}

func TestRunnerDisablesScheduleForInactiveRule(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        false,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	sch := &models.Schedule{
		ID:         "sch-3",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}
	fs.due = []*models.Schedule{sch}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks, got %d", len(fs.createdTasks))
	}
	if len(fs.updatedSchedules) != 1 || fs.updatedSchedules[0].Enabled {
		t.Fatal("expected schedule disabled")
	}
}

// H-5 regression: a schedule that is far behind (e.g., server offline for
// days) must not cause the catch-up advancement loop to iterate once per
// missed interval. Without an iteration cap, `* * * * *` 7 days behind
// iterates ~10,000 times, blocking the schedule runner goroutine and
// starving every other schedule. The runner must cap the loop and disable
// the schedule (or jump NextRunAt near now) instead.
func TestRunnerCatchupLoopBounded(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	sch := &models.Schedule{
		ID:         "sch-far-behind",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "* * * * *", // every minute
		Enabled:    true,
		// 365 days behind → ~525k missed runs without a cap.
		NextRunAt: sql.NullTime{Time: now.Add(-365 * 24 * time.Hour), Valid: true},
		Catchup:   models.CatchupSkip,
	}
	fs.due = []*models.Schedule{sch}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())

	start := time.Now()
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	// 10k ComputeNextRunInLocation calls take seconds; the cap must keep
	// this well under 1s. Use 2s to tolerate slow CI.
	if elapsed > 2*time.Second {
		t.Fatalf("catch-up loop ran unbounded: %v elapsed for a 7-day-behind every-minute schedule", elapsed)
	}
	// Schedule must be left in a sane state — either disabled or moved forward.
	updated := fs.updatedSchedules[0]
	if !updated.Enabled && updated.NextRunAt.Valid && updated.NextRunAt.Time.Before(now) {
		t.Fatalf("schedule left disabled with stale NextRunAt: %+v", updated.NextRunAt)
	}
}

func TestRunnerCatchupRunOnce(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC().Truncate(time.Minute)
	// Last run was 12 minutes ago; expression every 5 minutes means two missed runs.
	missed := now.Add(-12 * time.Minute)
	sch := &models.Schedule{
		ID:         "sch-4",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: missed, Valid: true},
		Catchup:    models.CatchupRunOnce,
	}
	fs.due = []*models.Schedule{sch}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Should create one task at the original due time and one catch-up task.
	if len(fs.createdTasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(fs.createdTasks))
	}
	// Next run should be in the future (or at the current minute boundary).
	updated := fs.updatedSchedules[0]
	if !updated.NextRunAt.Valid || updated.NextRunAt.Time.Before(now) {
		t.Fatalf("expected future next_run_at, got %v", updated.NextRunAt)
	}
}

func TestTriggerSchedule(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	sch := &models.Schedule{
		ID:         "sch-5",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now, Valid: true},
	}
	fs.schedules["sch-5"] = sch

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	taskID, err := runner.TriggerSchedule(context.Background(), "sch-5")
	if err != nil {
		t.Fatal(err)
	}
	if taskID == "" {
		t.Fatal("expected task id")
	}
	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.createdTasks))
	}
	// Trigger should not update the schedule.
	if len(fs.updatedSchedules) != 0 {
		t.Fatalf("expected no schedule updates, got %d", len(fs.updatedSchedules))
	}
}

func TestTriggerScheduleInactiveRule(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        false,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	fs.schedules["sch-6"] = &models.Schedule{
		ID:     "sch-6",
		RuleID: "rule-1",
	}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	_, err := runner.TriggerSchedule(context.Background(), "sch-6")
	if !errors.Is(err, ErrRuleNotActive) {
		t.Fatalf("expected ErrRuleNotActive, got %v", err)
	}
}

func TestRunnerSkipsWhenLockHeldByAnother(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
		Priority:       models.PriorityNormal,
	}
	now := time.Now().UTC()
	sch := &models.Schedule{
		ID:         "sch-lock",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}
	fs.due = []*models.Schedule{sch}

	// Seed the lock as held by another instance with a far-future expiry.
	fs.locks["schedule_runner"] = &lockRow{owner: "other-instance", expiresAt: now.Add(time.Hour)}

	ctx, cancel := context.WithCancel(context.Background())
	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3, SchedulerLockLease: time.Minute}, zap.NewNop())
	runner.Start(ctx, 20*time.Millisecond)

	time.Sleep(60 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks when lock held by another, got %d", len(fs.createdTasks))
	}

	// After the lock expires, this instance should acquire it and run.
	fs.locks["schedule_runner"].expiresAt = now.Add(-time.Second)

	ctx, cancel = context.WithCancel(context.Background())
	runner.Start(ctx, 20*time.Millisecond)

	time.Sleep(60 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task after lock expiry, got %d", len(fs.createdTasks))
	}
}

func TestParseCron_InvalidStepFormat(t *testing.T) {
	cases := []string{
		"*/a * * * *",
		"1/2/3 * * * *",
		"1-5/0 * * * *",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if err := ValidateCron(expr); err == nil {
				t.Fatal("expected invalid")
			}
		})
	}
}

func TestCronMatches_BothDayFieldsRestricted(t *testing.T) {
	// At 00:00 on the 1st of the month AND on Monday.
	cs, err := parseCron("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2024-01-01 is Monday, so both dom and dow match.
	if !cs.matches(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expected match on Jan 1 2024 (Monday)")
	}
	// 2024-01-08 is Monday but not the 1st.
	if !cs.matches(time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expected match on Monday")
	}
	// 2024-01-02 is Tuesday and not the 1st.
	if cs.matches(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expected no match")
	}
}

func TestNewScheduleRunner_FallbackInstanceID(t *testing.T) {
	// We cannot easily make rand.Read fail, but we can verify the runner is created.
	runner := NewScheduleRunner(newScheduleFakeStore(), &config.Config{MaxRetries: 3}, zap.NewNop())
	if runner.instanceID == "" {
		t.Fatal("expected non-empty instance id")
	}
}

func TestRunner_Start_ZeroInterval(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:         "sch-zero",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	// Zero interval should be replaced with one minute, so use a long context and short timeout.
	runner.Start(ctx, 0)
	time.Sleep(20 * time.Millisecond)
	cancel()
	// No tick should fire within 20ms with minute interval.
	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks with minute interval, got %d", len(fs.createdTasks))
	}
}

func TestRunner_LockAcquireError(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.acquireErr = errors.New("lock error")
	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3, SchedulerLockLease: time.Minute}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx, 20*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	cancel()
	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks when lock acquisition errors, got %d", len(fs.createdTasks))
	}
}

func TestRunner_LockNotAcquired(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.acquireOK = false
	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3, SchedulerLockLease: time.Minute}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx, 20*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	cancel()
	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks when lock not acquired, got %d", len(fs.createdTasks))
	}
}

func TestRunner_RenewError(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:         "sch-renew",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}
	fs.renewErr = errors.New("renew failed")

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3, SchedulerLockLease: time.Minute}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx, 20*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	cancel()

	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task despite renew error, got %d", len(fs.createdTasks))
	}
}

func TestRunner_ReleasesLockOnShutdown(t *testing.T) {
	fs := newScheduleFakeStore()
	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3, SchedulerLockLease: time.Minute}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx, 20*time.Millisecond)

	// Wait for lock to be acquired.
	time.Sleep(30 * time.Millisecond)
	if _, ok := fs.locks["schedule_runner"]; !ok {
		t.Fatal("expected lock to be acquired")
	}

	cancel()
	time.Sleep(30 * time.Millisecond)
	if _, ok := fs.locks["schedule_runner"]; ok {
		t.Fatal("expected lock to be released on shutdown")
	}
}

func TestRunner_ProcessSchedule_MissingRuleDisables(t *testing.T) {
	fs := newScheduleFakeStore()
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:        "sch-missing",
		RuleID:    "missing-rule",
		Type:      models.ScheduleTypeCron,
		Enabled:   true,
		NextRunAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks, got %d", len(fs.createdTasks))
	}
	if len(fs.updatedSchedules) != 1 || fs.updatedSchedules[0].Enabled {
		t.Fatal("expected schedule disabled")
	}
}

func TestRunner_ProcessSchedule_GetRuleError(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.getRuleErr = errors.New("db error")
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:        "sch-rule-err",
		RuleID:    "rule-1",
		Type:      models.ScheduleTypeCron,
		Enabled:   true,
		NextRunAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The schedule should still be disabled despite the non-ErrRuleNotFound error.
	if len(fs.updatedSchedules) != 1 || fs.updatedSchedules[0].Enabled {
		t.Fatal("expected schedule disabled after rule error")
	}
}

func TestRunner_ProcessSchedule_CreateTaskError(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	fs.createTaskErr = errors.New("create failed")
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:         "sch-create-err",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.createdTasks) != 0 {
		t.Fatalf("expected no tasks on create error, got %d", len(fs.createdTasks))
	}
}

func TestRunner_ProcessSchedule_UpdateScheduleError(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	fs.updateScheduleErr = errors.New("update failed")
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:        "sch-update-err",
		RuleID:    "rule-1",
		Type:      models.ScheduleTypeOnce,
		Enabled:   true,
		NextRunAt: sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Task should still be created even if schedule update fails.
	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.createdTasks))
	}
}

func TestRunner_ProcessSchedule_CatchupSkip(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC().Truncate(time.Minute)
	missed := now.Add(-12 * time.Minute)
	fs.due = []*models.Schedule{{
		ID:         "sch-skip",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "*/5 * * * *",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: missed, Valid: true},
		Catchup:    models.CatchupSkip,
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Should create only the current task, no catch-up task.
	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.createdTasks))
	}
	updated := fs.updatedSchedules[0]
	if !updated.NextRunAt.Valid || updated.NextRunAt.Time.Before(now) {
		t.Fatalf("expected future next_run_at, got %v", updated.NextRunAt)
	}
}

func TestRunner_ProcessSchedule_InvalidCronDisables(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
	}
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:         "sch-bad-cron",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeCron,
		Expression: "invalid",
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The current run still creates a task; the schedule is disabled afterwards.
	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task for current run, got %d", len(fs.createdTasks))
	}
	if len(fs.updatedSchedules) != 1 || fs.updatedSchedules[0].Enabled {
		t.Fatal("expected schedule disabled")
	}
}

func TestRunner_CreateTaskFromSchedule_PriorityFallback(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.rules["rule-1"] = &models.Rule{
		ID:             "rule-1",
		Version:        "1.0.0",
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
		Priority:       models.PriorityLow,
	}
	now := time.Now().UTC()
	fs.due = []*models.Schedule{{
		ID:         "sch-prio",
		RuleID:     "rule-1",
		Type:       models.ScheduleTypeOnce,
		Enabled:    true,
		NextRunAt:  sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
		MaxRetries: 2,
	}}

	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.createdTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.createdTasks))
	}
	if fs.createdTasks[0].Priority != models.PriorityLow {
		t.Fatalf("expected priority low, got %s", fs.createdTasks[0].Priority)
	}
	if fs.createdTasks[0].MaxRetries != 2 {
		t.Fatalf("expected maxRetries 2, got %d", fs.createdTasks[0].MaxRetries)
	}
}

func TestComputeNextRun_InvalidExpression(t *testing.T) {
	_, err := ComputeNextRun("invalid", time.Now().UTC())
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestTriggerSchedule_MissingSchedule(t *testing.T) {
	fs := newScheduleFakeStore()
	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	_, err := runner.TriggerSchedule(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing schedule")
	}
}

func TestTriggerSchedule_MissingRule(t *testing.T) {
	fs := newScheduleFakeStore()
	fs.schedules["sch-1"] = &models.Schedule{ID: "sch-1", RuleID: "missing"}
	runner := NewScheduleRunner(fs, &config.Config{MaxRetries: 3}, zap.NewNop())
	_, err := runner.TriggerSchedule(context.Background(), "sch-1")
	if err == nil {
		t.Fatal("expected error for missing rule")
	}
}
