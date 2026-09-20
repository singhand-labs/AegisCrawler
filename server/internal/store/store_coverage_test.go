package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestDBPingAndWithTx(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// DB returns the underlying handle.
	if s.DB() == nil {
		t.Fatal("expected non-nil db handle")
	}

	// Ping succeeds on a healthy database.
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}

	// WithTx commits successful work.
	var ran bool
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("with tx commit: %v", err)
	}
	if !ran {
		t.Fatal("expected transaction function to run")
	}

	// WithTx rolls back on error.
	errBoom := errors.New("boom")
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestCreateRuleTxAndDeleteRuleTx(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "tx-rule",
		Version:   "1.0.0",
		Name:      "Tx Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.CreateRuleTx(ctx, tx, rule)
	}); err != nil {
		t.Fatalf("create rule tx: %v", err)
	}

	got, err := s.GetRuleByID(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != rule.Name {
		t.Fatalf("expected %s, got %s", rule.Name, got.Name)
	}

	// Delete via transaction.
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.DeleteRuleTx(ctx, tx, rule.ID)
	}); err != nil {
		t.Fatalf("delete rule tx: %v", err)
	}

	if _, err := s.GetRuleByID(ctx, rule.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected rule deleted, got %v", err)
	}

	// Deleting a missing rule returns ErrRuleNotFound.
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.DeleteRuleTx(ctx, tx, "missing-rule")
	}); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestListEnabledRules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rules := []*models.Rule{
		{
			ID:             "enabled-approved",
			Version:        "1.0.0",
			Name:           "Enabled Approved",
			Domain:         JSON("example.com"),
			Steps:          JSON([]any{"step1"}),
			Enabled:        true,
			ApprovalStatus: string(models.RuleApprovalApproved),
			Priority:       models.PriorityNormal,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		},
		{
			ID:             "enabled-pending",
			Version:        "1.0.0",
			Name:           "Enabled Pending",
			Domain:         JSON("example.com"),
			Steps:          JSON([]any{"step1"}),
			Enabled:        true,
			ApprovalStatus: string(models.RuleApprovalPending),
			Priority:       models.PriorityNormal,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		},
		{
			ID:             "disabled-approved",
			Version:        "1.0.0",
			Name:           "Disabled Approved",
			Domain:         JSON("example.com"),
			Steps:          JSON([]any{"step1"}),
			Enabled:        false,
			ApprovalStatus: string(models.RuleApprovalApproved),
			Priority:       models.PriorityNormal,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		},
	}
	for _, r := range rules {
		if err := s.CreateRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListEnabledRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "enabled-approved" {
		t.Fatalf("expected only enabled-approved, got %+v", got)
	}
}

func TestRenewLeaseAndUpdateTaskStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "lease-rule",
		Version:   "1.0.0",
		Name:      "Lease Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ID:          "lease-task",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Claim the task so it is leased to worker-1.
	claimed, err := s.ClaimTask(ctx, "worker-1", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}

	// RenewLease extends the lease.
	if err := s.RenewLease(ctx, claimed.ID, "worker-1", 2*time.Minute); err != nil {
		t.Fatalf("renew lease: %v", err)
	}

	// UpdateTaskStatus to running.
	if err := s.UpdateTaskStatus(ctx, claimed.ID, "worker-1", string(models.TaskStatusRunning), "started", ""); err != nil {
		t.Fatalf("update task status: %v", err)
	}

	// RenewLease works for running tasks too.
	if err := s.RenewLease(ctx, claimed.ID, "worker-1", 2*time.Minute); err != nil {
		t.Fatalf("renew lease running: %v", err)
	}

	// Wrong worker causes lease conflict.
	if err := s.RenewLease(ctx, claimed.ID, "worker-2", 2*time.Minute); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("expected ErrLeaseConflict, got %v", err)
	}

	// Update to terminal status sets completed_at.
	if err := s.UpdateTaskStatus(ctx, claimed.ID, "worker-1", string(models.TaskStatusDone), "done", ""); err != nil {
		t.Fatalf("update task status done: %v", err)
	}
	got, err := s.GetTaskByID(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TaskStatusDone {
		t.Fatalf("expected done, got %s", got.Status)
	}
	if !got.CompletedAt.Valid {
		t.Fatal("expected completed_at set")
	}
}

func TestGetLatestCheckpointAndListResultsLogs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "child-rule",
		Version:   "1.0.0",
		Name:      "Child Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ID:          "child-task",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusDone,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// No checkpoint yet.
	if _, err := s.GetLatestCheckpoint(ctx, task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}

	now := time.Now().UTC()
	cp1 := &models.Checkpoint{ID: "cp1", TaskID: task.ID, WorkerID: "w", Name: "first", Payload: []byte(`{"x":1}`), CreatedAt: now.Add(-time.Hour)}
	cp2 := &models.Checkpoint{ID: "cp2", TaskID: task.ID, WorkerID: "w", Name: "latest", Payload: []byte(`{"x":2}`), CreatedAt: now}
	for _, cp := range []*models.Checkpoint{cp1, cp2} {
		if err := s.InsertCheckpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
	}

	latest, err := s.GetLatestCheckpoint(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Name != "latest" {
		t.Fatalf("expected latest checkpoint, got %s", latest.Name)
	}

	result := &models.Result{ID: "r1", TaskID: task.ID, WorkerID: "w", Payload: []byte(`{"ok":true}`), CreatedAt: now}
	if err := s.InsertResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	results, err := s.ListResults(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "r1" {
		t.Fatalf("unexpected results: %+v", results)
	}

	log := &models.LogEntry{ID: "l1", TaskID: task.ID, WorkerID: "w", Level: "info", Message: "hello", Extra: []byte(`{}`), CreatedAt: now}
	if err := s.InsertLog(ctx, log); err != nil {
		t.Fatal(err)
	}
	logs, err := s.ListLogs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].ID != "l1" {
		t.Fatalf("unexpected logs: %+v", logs)
	}
}

func TestScheduleCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:             "sched-crud-rule",
		Version:        "1.0.0",
		Name:           "Schedule CRUD Rule",
		Domain:         JSON("example.com"),
		Steps:          JSON([]any{"step1"}),
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
		Priority:       models.PriorityNormal,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	runAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	sch := &models.Schedule{
		ID:          "sched-1",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Name:        "once",
		Type:        models.ScheduleTypeOnce,
		Expression:  runAt.Format(time.RFC3339),
		Enabled:     true,
		NextRunAt:   sql.NullTime{Time: runAt, Valid: true},
		Variables:   JSON(map[string]any{}),
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		Catchup:     models.CatchupSkip,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	if err := s.CreateSchedule(ctx, sch); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	got, err := s.GetScheduleByID(ctx, sch.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if got.Name != sch.Name {
		t.Fatalf("expected name %s, got %s", sch.Name, got.Name)
	}

	// Missing schedule.
	if _, err := s.GetScheduleByID(ctx, "missing-schedule"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}

	// Update.
	got.Name = "updated"
	got.NextRunAt = sql.NullTime{Time: runAt.Add(time.Hour), Valid: true}
	if err := s.UpdateSchedule(ctx, got); err != nil {
		t.Fatalf("update schedule: %v", err)
	}
	updated, err := s.GetScheduleByID(ctx, sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "updated" {
		t.Fatalf("expected updated name, got %s", updated.Name)
	}

	// Update missing schedule.
	if err := s.UpdateSchedule(ctx, &models.Schedule{ID: "missing-schedule"}); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}

	// List with filters.
	list, total, err := s.ListSchedules(ctx, ListSchedulesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 schedule, got total=%d len=%d", total, len(list))
	}

	enabled := true
	list, total, err = s.ListSchedules(ctx, ListSchedulesFilter{RuleID: rule.ID, Type: string(models.ScheduleTypeOnce), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 filtered schedule, got total=%d len=%d", total, len(list))
	}

	// ListDueSchedules.
	due, err := s.ListDueSchedules(ctx, runAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due schedule, got %d", len(due))
	}

	// Delete.
	if err := s.DeleteSchedule(ctx, sch.ID); err != nil {
		t.Fatalf("delete schedule: %v", err)
	}
	if _, err := s.GetScheduleByID(ctx, sch.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected schedule deleted, got %v", err)
	}

	// Delete missing.
	if err := s.DeleteSchedule(ctx, "missing-schedule"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestRuleEnhancementTx(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "rule-1",
		Version:   "1.0.0",
		Name:      "Rule One",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	e := &models.RuleEnhancement{
		ID:        "enh-1",
		RuleID:    rule.ID,
		Baseline:  JSON(map[string]any{"id": rule.ID}),
		Enhanced:  JSON(map[string]any{"id": rule.ID, "name": "better"}),
		Patch:     JSON(map[string]any{}),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}

	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.CreateRuleEnhancementTx(ctx, tx, e)
	}); err != nil {
		t.Fatalf("create enhancement tx: %v", err)
	}

	got, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending, got %s", got.Status)
	}

	// Update status via transaction.
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.UpdateRuleEnhancementStatusTx(ctx, tx, e.ID, string(models.EnhancementStatusApproved))
	}); err != nil {
		t.Fatalf("update enhancement status tx: %v", err)
	}

	got, err = s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(models.EnhancementStatusApproved) {
		t.Fatalf("expected approved, got %s", got.Status)
	}
}

func TestMigrations005And006And010(t *testing.T) {
	// Simulate a database that has migrations 1-4 applied but not 5/6/10.
	f, err := os.CreateTemp("", "opencrawler-mid-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	rawDB, err := sql.Open("sqlite", f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(migration001Schema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, v := range []int{1, 2, 3, 4} {
		if _, err := rawDB.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, v); err != nil {
			t.Fatalf("record migration %d: %v", v, err)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := New(f.Name(), "")
	if err != nil {
		t.Fatalf("open mid-state database: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// Migration 5 added the schedules table and tasks.schedule_id.
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schedules'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected schedules table to exist, got %d", count)
	}

	// Migration 6 added cancel_requested.
	var colCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name='cancel_requested'`).Scan(&colCount); err != nil {
		t.Fatal(err)
	}
	if colCount != 1 {
		t.Fatalf("expected cancel_requested column, got %d", colCount)
	}

	// Migration 10 added source.
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('rules') WHERE name='source'`).Scan(&colCount); err != nil {
		t.Fatal(err)
	}
	if colCount != 1 {
		t.Fatalf("expected source column, got %d", colCount)
	}
}

func TestNewWithConfigDefaultsAndErrors(t *testing.T) {
	f, err := os.CreateTemp("", "opencrawler-defaults-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	// nil config defaults to WAL mode.
	s, err := NewWithConfig(nil, f.Name(), "")
	if err != nil {
		t.Fatalf("nil cfg: %v", err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("expected wal, got %q", mode)
	}
	s.Close()

	// empty journal mode defaults to WAL.
	s, err = NewWithConfig(&config.Config{}, f.Name(), "")
	if err != nil {
		t.Fatalf("empty journal mode: %v", err)
	}
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("expected wal for empty mode, got %q", mode)
	}
	s.Close()

	// opening a directory as a database fails during migration.
	dir, err := os.MkdirTemp("", "opencrawler-dir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if _, err := NewWithConfig(nil, dir, ""); err == nil {
		t.Fatal("expected error opening directory as database")
	}
}

func TestWithTxBeginError(t *testing.T) {
	s := newTestStore(t)
	s.Close()

	if err := s.WithTx(context.Background(), func(tx *sql.Tx) error { return nil }); err == nil {
		t.Fatal("expected WithTx error after db closed")
	}
}

func TestDeleteRuleBeginError(t *testing.T) {
	s := newTestStore(t)
	s.Close()

	if err := s.DeleteRule(context.Background(), "any"); err == nil {
		t.Fatal("expected DeleteRule error after db closed")
	}
}

func TestMigrateError(t *testing.T) {
	rawDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	rawDB.Close()

	if err := migrate(rawDB); err == nil {
		t.Fatal("expected migrate error on closed database")
	}
}

func TestMigrationSkipBranches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, v := range []int{2, 5, 6, 10, 19, 20} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v); err != nil {
			t.Fatalf("delete migration %d: %v", v, err)
		}
		if err := migrate(s.db); err != nil {
			t.Fatalf("re-run migration %d: %v", v, err)
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, v).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected migration %d to be recorded, got %d", v, count)
		}
	}
}

func TestEncryptDecryptVariableErrors(t *testing.T) {
	f, err := os.CreateTemp("", "opencrawler-enc-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := New(f.Name(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "enc-rule",
		Version:   "1.0.0",
		Name:      "Enc Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	// Invalid JSON variables cannot be encrypted.
	badTask := &models.Task{
		ID:          "bad-task",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		Variables:   models.JSON("{not json"),
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, badTask); err == nil {
		t.Fatal("expected CreateTask error for invalid encrypted variables")
	}

	// A task with corrupt ciphertext cannot be decrypted.
	task := &models.Task{
		ID:          "decrypt-task",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET variables = ? WHERE id = ?`, models.JSON("!!!not-valid"), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTaskByID(ctx, task.ID); err == nil {
		t.Fatal("expected GetTaskByID error for corrupt variables")
	}
	if _, _, err := s.ListTasks(ctx, ListTasksFilter{}); err == nil {
		t.Fatal("expected ListTasks error for corrupt variables")
	}
}

func TestClaimTaskFiltersAndLimits(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:             "claim-filter-rule",
		Version:        "1.0.0",
		Name:           "Claim Filter Rule",
		Domain:         JSON("example.com"),
		Steps:          JSON([]any{"step1"}),
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
		Priority:       models.PriorityNormal,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	t1 := &models.Task{
		ID:          "task-1",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC().Add(-3 * time.Hour),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}

	claimed, err := s.ClaimTask(ctx, "worker-1", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim first task: %v", err)
	}
	if claimed.ID != t1.ID {
		t.Fatalf("expected %s, got %s", t1.ID, claimed.ID)
	}

	// Worker is already at its concurrency limit.
	if _, err := s.ClaimTask(ctx, "worker-1", time.Minute, 1); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expected ErrNoTaskAvailable, got %v", err)
	}

	// A task scheduled in the future is not available.
	t2 := &models.Task{
		ID:          "task-2",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		ScheduledAt: sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true},
		CreatedAt:   time.Now().UTC().Add(-2 * time.Hour),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, t2); err != nil {
		t.Fatal(err)
	}

	// A pending task with a future lease_until is not available.
	t3 := &models.Task{
		ID:          "task-3",
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC().Add(-time.Hour),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, t3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(time.Hour), t3.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ClaimTask(ctx, "worker-1", time.Minute, 5); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expected ErrNoTaskAvailable for filtered tasks, got %v", err)
	}

	// Release the leased task and make the filtered tasks available.
	if err := s.ReleaseTask(ctx, t1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET scheduled_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour), t2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour), t3.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{t1.ID, t2.ID, t3.ID} {
		claimed, err := s.ClaimTask(ctx, "worker-1", time.Minute, 5)
		if err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
		if claimed.ID != id {
			t.Fatalf("expected %s, got %s", id, claimed.ID)
		}
	}
}

func TestListEmptyAndDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rules, total, err := s.ListRules(ctx, ListRulesFilter{Limit: 0, Offset: -1})
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if total != 0 || len(rules) != 0 {
		t.Fatalf("expected empty rules, got total=%d len=%d", total, len(rules))
	}

	tasks, total, err := s.ListTasks(ctx, ListTasksFilter{Limit: 0, Offset: -1})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if total != 0 || len(tasks) != 0 {
		t.Fatalf("expected empty tasks, got total=%d len=%d", total, len(tasks))
	}

	logs, total, err := s.ListAuditLogs(ctx, ListAuditLogsFilter{Limit: 0, Offset: -1})
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if total != 0 || len(logs) != 0 {
		t.Fatalf("expected empty audit logs, got total=%d len=%d", total, len(logs))
	}

	schedules, total, err := s.ListSchedules(ctx, ListSchedulesFilter{Limit: 0, Offset: -1})
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if total != 0 || len(schedules) != 0 {
		t.Fatalf("expected empty schedules, got total=%d len=%d", total, len(schedules))
	}

	enabled, err := s.ListEnabledRules(ctx)
	if err != nil {
		t.Fatalf("list enabled rules: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("expected no enabled rules, got %d", len(enabled))
	}

	results, err := s.ListResults(ctx, "missing-task")
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}

	logEntries, err := s.ListLogs(ctx, "missing-task")
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logEntries) != 0 {
		t.Fatalf("expected no logs, got %d", len(logEntries))
	}

	pending, err := s.ListPendingLLMJobs(ctx, 0)
	if err != nil {
		t.Fatalf("list pending llm jobs: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending llm jobs, got %d", len(pending))
	}

	due, err := s.ListDueSchedules(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("list due schedules: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected no due schedules, got %d", len(due))
	}
}

func TestCreateLLMJobDefaults(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()

	job := &models.LLMJob{RuleID: "rule-1"}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected ID to be generated")
	}
	if job.Status != string(models.LLMJobStatusPending) {
		t.Fatalf("expected pending status, got %s", job.Status)
	}
	if string(job.Baseline) != "{}" {
		t.Fatalf("expected default baseline {}, got %s", job.Baseline)
	}
	if string(job.Recording) != "{}" {
		t.Fatalf("expected default recording {}, got %s", job.Recording)
	}

	got, err := s.GetLLMJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(models.LLMJobStatusPending) {
		t.Fatalf("expected pending, got %s", got.Status)
	}
}

func TestListPendingLLMJobsDefaults(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()

	jobs, err := s.ListPendingLLMJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(jobs))
	}

	if err := s.CreateLLMJob(ctx, &models.LLMJob{ID: "j1", RuleID: "r", Status: string(models.LLMJobStatusPending)}); err != nil {
		t.Fatal(err)
	}

	jobs, err = s.ListPendingLLMJobs(ctx, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job with default limit, got %d", len(jobs))
	}
}

func TestGetRuleEnhancementNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetRuleEnhancementByRuleID(context.Background(), "missing"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestRenewSchedulerLockDenied(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	acquired, err := s.AcquireSchedulerLock(ctx, "lock", "owner1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected owner1 to acquire")
	}

	renewed, err := s.RenewSchedulerLock(ctx, "lock", "owner2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed {
		t.Fatal("expected owner2 renew to fail")
	}
}

func TestPurgeOldDataBatchingAndDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:             "purge-rule",
		Version:        "1.0.0",
		Name:           "Purge Rule",
		Domain:         JSON("example.com"),
		Steps:          JSON([]any{"step1"}),
		Enabled:        true,
		ApprovalStatus: string(models.RuleApprovalApproved),
		Priority:       models.PriorityNormal,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("old-task-%d", i)
		task := &models.Task{
			ID:          id,
			RuleID:      rule.ID,
			RuleVersion: "1.0.0",
			Status:      models.TaskStatusDone,
			Priority:    models.PriorityNormal,
			MaxRetries:  3,
			CreatedAt:   old,
			UpdatedAt:   old,
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, old, id); err != nil {
			t.Fatal(err)
		}

		if err := s.InsertResult(ctx, &models.Result{ID: fmt.Sprintf("r-%d", i), TaskID: id, WorkerID: "w", Payload: []byte("{}"), CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertLog(ctx, &models.LogEntry{ID: fmt.Sprintf("l-%d", i), TaskID: id, WorkerID: "w", Level: "info", Message: "msg", CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertSnapshot(ctx, &models.Snapshot{ID: fmt.Sprintf("s-%d", i), TaskID: id, WorkerID: "w", Name: "snap", Type: "html", Data: "data", CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertHeartbeat(ctx, &models.Heartbeat{ID: fmt.Sprintf("h-%d", i), TaskID: id, WorkerID: "w", CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertStatusUpdate(ctx, &models.TaskStatusUpdate{ID: fmt.Sprintf("u-%d", i), TaskID: id, WorkerID: "w", Status: "done", CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertCheckpoint(ctx, &models.Checkpoint{ID: fmt.Sprintf("c-%d", i), TaskID: id, WorkerID: "w", Name: "cp", Payload: []byte("{}"), CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		ResultRetention:        time.Minute,
		LogRetention:           time.Minute,
		SnapshotRetention:      time.Minute,
		HeartbeatRetention:     time.Minute,
		StatusUpdateRetention:  time.Minute,
		CheckpointRetention:    time.Minute,
		CompletedTaskRetention: time.Minute,
		RetentionBatchSize:     1,
	}

	report, err := s.PurgeOldData(ctx, cfg)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}

	if report["results"] != 3 || report["logs"] != 3 || report["snapshots"] != 3 ||
		report["heartbeats"] != 3 || report["status_updates"] != 3 || report["checkpoints"] != 3 ||
		report["completed_tasks"] != 3 {
		t.Fatalf("unexpected report: %v", report)
	}

	assertCount := func(table string, expected int) {
		t.Helper()
		var got int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != expected {
			t.Fatalf("expected %s count %d, got %d", table, expected, got)
		}
	}
	assertCount("results", 0)
	assertCount("logs", 0)
	assertCount("snapshots", 0)
	assertCount("heartbeats", 0)
	assertCount("task_status_updates", 0)
	assertCount("checkpoints", 0)
	assertCount("tasks", 0)

	// Default batch size path with nothing to purge.
	report, err = s.PurgeOldData(ctx, &config.Config{})
	if err != nil {
		t.Fatalf("purge with defaults: %v", err)
	}
	for k, v := range report {
		if v != 0 {
			t.Fatalf("expected %s=0, got %d", k, v)
		}
	}
}

// failingExecer is an execer that always returns errExec.
type failingExecer struct {
	errExec error
}

func (f failingExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return nil, f.errExec
}

func TestCreateRuleWithExecError(t *testing.T) {
	ctx := context.Background()
	r := &models.Rule{
		ID:        "fail-rule",
		Version:   "1.0.0",
		Name:      "Fail",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	boom := errors.New("boom")
	if err := createRuleWithExec(ctx, failingExecer{boom}, r); !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestDeleteRuleWithExecError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	if err := deleteRuleWithExec(ctx, failingExecer{boom}, "rule-id"); !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestCreateRuleEnhancementWithExecError(t *testing.T) {
	ctx := context.Background()
	e := &models.RuleEnhancement{
		ID:        "enh-fail",
		RuleID:    "rule-1",
		Baseline:  JSON(map[string]any{}),
		Enhanced:  JSON(map[string]any{}),
		Patch:     JSON(map[string]any{}),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	boom := errors.New("boom")
	if err := createRuleEnhancementWithExec(ctx, failingExecer{boom}, e); !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestUpdateRuleEnhancementStatusWithExecError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	if err := updateRuleEnhancementStatusWithExec(ctx, failingExecer{boom}, "enh-1", "approved"); !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestStoreMethodsAfterClose(t *testing.T) {
	s := newTestStore(t)
	s.Close()
	ctx := context.Background()

	if err := s.Ping(ctx); err == nil {
		t.Error("Ping")
	}
	if err := s.WithTx(ctx, func(tx *sql.Tx) error { return nil }); err == nil {
		t.Error("WithTx")
	}

	rule := &models.Rule{
		ID:        "closed-rule",
		Version:   "1.0.0",
		Name:      "Closed",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err == nil {
		t.Error("CreateRule")
	}
	if _, err := s.GetRuleByID(ctx, "closed-rule"); err == nil {
		t.Error("GetRuleByID")
	}
	if _, _, err := s.ListRules(ctx, ListRulesFilter{}); err == nil {
		t.Error("ListRules")
	}
	if err := s.DeleteRule(ctx, "closed-rule"); err == nil {
		t.Error("DeleteRule")
	}
	if _, err := s.ListEnabledRules(ctx); err == nil {
		t.Error("ListEnabledRules")
	}
	if err := s.UpdateRuleApprovalStatus(ctx, "closed-rule", "approved"); err == nil {
		t.Error("UpdateRuleApprovalStatus")
	}

	task := &models.Task{
		ID:          "closed-task",
		RuleID:      "closed-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, task); err == nil {
		t.Error("CreateTask")
	}
	if _, err := s.GetTaskByID(ctx, "closed-task"); err == nil {
		t.Error("GetTaskByID")
	}
	if _, _, err := s.ListTasks(ctx, ListTasksFilter{}); err == nil {
		t.Error("ListTasks")
	}
	if err := s.CancelTask(ctx, "closed-task"); err == nil {
		t.Error("CancelTask")
	}
	if err := s.RetryTask(ctx, "closed-task"); err == nil {
		t.Error("RetryTask")
	}
	if _, err := s.ClaimTask(ctx, "w", time.Minute, 5); err == nil {
		t.Error("ClaimTask")
	}
	if err := s.RenewLease(ctx, "closed-task", "w", time.Minute); err == nil {
		t.Error("RenewLease")
	}
	if err := s.UpdateTaskStatus(ctx, "closed-task", "w", string(models.TaskStatusRunning), "", ""); err == nil {
		t.Error("UpdateTaskStatus")
	}
	if _, err := s.ListExpiredLeases(ctx, time.Now().UTC()); err == nil {
		t.Error("ListExpiredLeases")
	}
	if _, err := s.ListResults(ctx, "closed-task"); err == nil {
		t.Error("ListResults")
	}
	if _, err := s.ListLogs(ctx, "closed-task"); err == nil {
		t.Error("ListLogs")
	}

	audit := &models.AuditLog{
		ID:           "a1",
		Actor:        "x",
		Action:       "y",
		ResourceType: "z",
		ResourceID:   "r",
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.InsertAuditLog(ctx, audit); err == nil {
		t.Error("InsertAuditLog")
	}
	if _, _, err := s.ListAuditLogs(ctx, ListAuditLogsFilter{}); err == nil {
		t.Error("ListAuditLogs")
	}

	sch := &models.Schedule{
		ID:          "closed-schedule",
		RuleID:      "closed-rule",
		RuleVersion: "1.0.0",
		Name:        "s",
		Type:        models.ScheduleTypeOnce,
		Expression:  time.Now().UTC().Format(time.RFC3339),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateSchedule(ctx, sch); err == nil {
		t.Error("CreateSchedule")
	}
	if _, err := s.GetScheduleByID(ctx, "closed-schedule"); err == nil {
		t.Error("GetScheduleByID")
	}
	if err := s.UpdateSchedule(ctx, sch); err == nil {
		t.Error("UpdateSchedule")
	}
	if err := s.DeleteSchedule(ctx, "closed-schedule"); err == nil {
		t.Error("DeleteSchedule")
	}
	if _, _, err := s.ListSchedules(ctx, ListSchedulesFilter{}); err == nil {
		t.Error("ListSchedules")
	}
	if _, err := s.ListDueSchedules(ctx, time.Now().UTC()); err == nil {
		t.Error("ListDueSchedules")
	}

	if _, err := s.PurgeOldData(ctx, &config.Config{}); err == nil {
		t.Error("PurgeOldData")
	}

	job := &models.LLMJob{ID: "closed-job", RuleID: "closed-rule", Status: string(models.LLMJobStatusPending)}
	if err := s.CreateLLMJob(ctx, job); err == nil {
		t.Error("CreateLLMJob")
	}
	if _, err := s.GetLLMJob(ctx, "closed-job"); err == nil {
		t.Error("GetLLMJob")
	}
	if err := s.UpdateLLMJobStatus(ctx, job); err == nil {
		t.Error("UpdateLLMJobStatus")
	}
	if _, err := s.ClaimPendingLLMJob(ctx); err == nil {
		t.Error("ClaimPendingLLMJob")
	}
	if _, err := s.ListPendingLLMJobs(ctx, 10); err == nil {
		t.Error("ListPendingLLMJobs")
	}

	enh := &models.RuleEnhancement{
		ID:        "closed-enh",
		RuleID:    "closed-rule",
		Baseline:  JSON(map[string]any{}),
		Enhanced:  JSON(map[string]any{}),
		Patch:     JSON(map[string]any{}),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateRuleEnhancement(ctx, enh); err == nil {
		t.Error("CreateRuleEnhancement")
	}
	if _, err := s.GetRuleEnhancementByRuleID(ctx, "closed-rule"); err == nil {
		t.Error("GetRuleEnhancementByRuleID")
	}

	if _, err := s.AcquireSchedulerLock(ctx, "l", "o", time.Minute); err == nil {
		t.Error("AcquireSchedulerLock")
	}
	if _, err := s.RenewSchedulerLock(ctx, "l", "o", time.Minute); err == nil {
		t.Error("RenewSchedulerLock")
	}
	if err := s.ReleaseSchedulerLock(ctx, "l", "o"); err == nil {
		t.Error("ReleaseSchedulerLock")
	}
}
