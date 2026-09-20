package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	f, err := os.CreateTemp("", "opencrawler-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateAndGetRule(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRuleByID(ctx, "test-rule")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Test Rule" {
		t.Fatalf("expected Test Rule, got %s", got.Name)
	}
	if got.Source != "pageagent" {
		t.Fatalf("expected default source pageagent, got %s", got.Source)
	}
}

func TestCreateAndClaimTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test Rule",
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
		ID:          NewID(),
		RuleID:      "test-rule",
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

	claimed, err := s.ClaimTask(ctx, "worker-1", 60*time.Second, 5)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != models.TaskStatusLeased {
		t.Fatalf("expected leased, got %s", claimed.Status)
	}

	_, err = s.ClaimTask(ctx, "worker-1", 60*time.Second, 5)
	if err != ErrNoTaskAvailable {
		t.Fatalf("expected no task available, got %v", err)
	}
}

func TestClaimTaskForBrowserProfileEnforcesAffinity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rule := &models.Rule{
		ID: "profile-rule", Version: "1.0.0", Name: "Profile Rule",
		Domain: JSON("example.com"), Steps: JSON([]any{"step1"}),
		Enabled: true, ApprovalStatus: string(models.RuleApprovalApproved),
		Priority: models.PriorityNormal, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	createTask := func(id, profileID string, createdAt time.Time) {
		t.Helper()
		task := &models.Task{
			ID: id, RuleID: rule.ID, RuleVersion: rule.Version,
			Status: models.TaskStatusPending, Priority: models.PriorityNormal,
			MaxRetries: 3, BrowserProfileID: profileID,
			CreatedAt: createdAt, UpdatedAt: now,
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	createTask("profile-b-old", "profile-b", now.Add(-5*time.Hour))
	createTask("unbound-old", "", now.Add(-4*time.Hour))
	createTask("profile-a-new", "profile-a", now.Add(-3*time.Hour))
	createTask("unbound-new", "", now.Add(-2*time.Hour))

	claimed, err := s.ClaimTaskForBrowserProfile(ctx, "worker-unbound", "", time.Minute, 5)
	if err != nil || claimed.ID != "unbound-old" {
		t.Fatalf("unprofiled worker claimed incompatible task: task=%v err=%v", claimed, err)
	}

	claimed, err = s.ClaimTaskForBrowserProfile(ctx, "worker-a", "profile-a", time.Minute, 5)
	if err != nil || claimed.ID != "profile-a-new" {
		t.Fatalf("profile-a worker did not prefer exact match: task=%v err=%v", claimed, err)
	}

	claimed, err = s.ClaimTaskForBrowserProfile(ctx, "worker-b", "profile-b", time.Minute, 5)
	if err != nil || claimed.ID != "profile-b-old" {
		t.Fatalf("profile-b worker claimed incompatible task: task=%v err=%v", claimed, err)
	}

	claimed, err = s.ClaimTaskForBrowserProfile(ctx, "worker-c", "profile-c", time.Minute, 5)
	if err != nil || claimed.ID != "unbound-new" {
		t.Fatalf("profiled worker could not claim unbound fallback: task=%v err=%v", claimed, err)
	}

	createTask("profile-z-only", "profile-z", now.Add(-time.Hour))
	if _, err := s.ClaimTaskForBrowserProfile(ctx, "worker-no-match", "profile-a", time.Minute, 5); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expected mismatched profile task to remain pending, got %v", err)
	}
	remaining, err := s.GetTaskByID(ctx, "profile-z-only")
	if err != nil || remaining.Status != models.TaskStatusPending {
		t.Fatalf("mismatched task was mutated: task=%v err=%v", remaining, err)
	}
}

func TestExpiredLeaseRequeue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test Rule",
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
		ID:          NewID(),
		RuleID:      "test-rule",
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

	claimed, err := s.ClaimTask(ctx, "worker-1", 1*time.Millisecond, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskStatus(ctx, claimed.ID, "worker-1", string(models.TaskStatusRunning), "started", ""); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	ids, err := s.ListExpiredLeases(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 expired lease, got %d", len(ids))
	}

	if err := s.ReleaseTask(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}

	claimed2, err := s.ClaimTask(ctx, "worker-2", 60*time.Second, 5)
	if err != nil {
		t.Fatal(err)
	}
	if claimed2.WorkerID.String != "worker-2" {
		t.Fatalf("expected worker-2, got %s", claimed2.WorkerID.String)
	}
}

func TestRuleApprovalStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "approval-rule",
		Version:   "1.0.0",
		Name:      "Approval Test Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRuleByID(ctx, "approval-rule")
	if err != nil {
		t.Fatal(err)
	}
	if got.ApprovalStatus != "approved" {
		t.Fatalf("expected default approval status approved, got %s", got.ApprovalStatus)
	}

	if err := s.UpdateRuleApprovalStatus(ctx, "approval-rule", "rejected"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRuleByID(ctx, "approval-rule")
	if err != nil {
		t.Fatal(err)
	}
	if got.ApprovalStatus != "rejected" {
		t.Fatalf("expected rejected, got %s", got.ApprovalStatus)
	}

	if err := s.UpdateRuleApprovalStatus(ctx, "missing-rule", "approved"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestListRules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	enabled := true
	disabled := false

	rules := []*models.Rule{
		{
			ID:             "rule-a",
			Version:        "1.0.0",
			Name:           "Rule A",
			Domain:         JSON("example.com"),
			Steps:          JSON([]any{"step1"}),
			Enabled:        true,
			Owner:          "alice",
			ApprovalStatus: "approved",
			Priority:       models.PriorityNormal,
			CreatedAt:      time.Now().UTC().Add(-time.Hour),
			UpdatedAt:      time.Now().UTC().Add(-time.Hour),
		},
		{
			ID:             "rule-b",
			Version:        "1.0.0",
			Name:           "Rule B",
			Domain:         JSON("example.org"),
			Steps:          JSON([]any{"step1"}),
			Enabled:        false,
			Owner:          "bob",
			ApprovalStatus: "pending",
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

	// No filter returns all.
	got, total, err := s.ListRules(ctx, ListRulesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("expected total 2, got %d", total)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(got))
	}

	// Filter by enabled.
	got, total, err = s.ListRules(ctx, ListRulesFilter{Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != "rule-a" {
		t.Fatalf("unexpected enabled filter result: total=%d, rules=%v", total, got)
	}

	got, total, err = s.ListRules(ctx, ListRulesFilter{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != "rule-b" {
		t.Fatalf("unexpected disabled filter result: total=%d, rules=%v", total, got)
	}

	// Filter by approval_status.
	got, total, err = s.ListRules(ctx, ListRulesFilter{ApprovalStatus: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "rule-b" {
		t.Fatalf("unexpected approval filter result: total=%d, rules=%v", total, got)
	}

	// Filter by domain raw JSON.
	got, total, err = s.ListRules(ctx, ListRulesFilter{Domain: `"example.com"`})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "rule-a" {
		t.Fatalf("unexpected domain filter result: total=%d, rules=%v", total, got)
	}

	// Filter by owner.
	got, total, err = s.ListRules(ctx, ListRulesFilter{Owner: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "rule-b" {
		t.Fatalf("unexpected owner filter result: total=%d, rules=%v", total, got)
	}

	// Pagination.
	got, total, err = s.ListRules(ctx, ListRulesFilter{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 1 {
		t.Fatalf("unexpected pagination result: total=%d, len=%d", total, len(got))
	}
	got, total, err = s.ListRules(ctx, ListRulesFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 1 {
		t.Fatalf("unexpected pagination offset result: total=%d, len=%d", total, len(got))
	}
}

func TestDeleteRule(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "delete-rule",
		Version:   "1.0.0",
		Name:      "Delete Me",
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
		ID:          "delete-task",
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

	if err := s.InsertResult(ctx, &models.Result{ID: "r1", TaskID: task.ID, WorkerID: "w", Payload: []byte("{}"), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLog(ctx, &models.LogEntry{ID: "l1", TaskID: task.ID, WorkerID: "w", Level: "info", Message: "msg", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSnapshot(ctx, &models.Snapshot{ID: "s1", TaskID: task.ID, WorkerID: "w", Name: "snap", Type: "html", Data: "data", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertHeartbeat(ctx, &models.Heartbeat{ID: "h1", TaskID: task.ID, WorkerID: "w", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertStatusUpdate(ctx, &models.TaskStatusUpdate{ID: "u1", TaskID: task.ID, WorkerID: "w", Status: "done", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCheckpoint(ctx, &models.Checkpoint{ID: "c1", TaskID: task.ID, WorkerID: "w", Name: "cp", Payload: []byte("{}"), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
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

	assertCount("rules", 0)
	assertCount("tasks", 0)
	assertCount("results", 0)
	assertCount("logs", 0)
	assertCount("snapshots", 0)
	assertCount("heartbeats", 0)
	assertCount("task_status_updates", 0)
	assertCount("checkpoints", 0)

	if err := s.DeleteRule(ctx, "missing-rule"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestListTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"list-rule", "other-rule"} {
		rule := &models.Rule{
			ID:        id,
			Version:   "1.0.0",
			Name:      "List Rule " + id,
			Domain:    JSON("example.com"),
			Steps:     JSON([]any{"step1"}),
			Priority:  models.PriorityNormal,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.CreateRule(ctx, rule); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Now().UTC().Truncate(time.Second)
	tasks := []*models.Task{
		{
			ID:          "task-pending",
			RuleID:      "list-rule",
			RuleVersion: "1.0.0",
			Status:      models.TaskStatusPending,
			Priority:    models.PriorityHigh,
			MaxRetries:  3,
			CreatedAt:   base.Add(-time.Hour),
			UpdatedAt:   base.Add(-time.Hour),
		},
		{
			ID:          "task-running",
			RuleID:      "list-rule",
			RuleVersion: "1.0.0",
			Status:      models.TaskStatusRunning,
			Priority:    models.PriorityNormal,
			WorkerID:    sql.NullString{String: "worker-1", Valid: true},
			MaxRetries:  3,
			CreatedAt:   base,
			UpdatedAt:   base,
		},
		{
			ID:          "task-failed",
			RuleID:      "other-rule",
			RuleVersion: "1.0.0",
			Status:      models.TaskStatusFailed,
			Priority:    models.PriorityLow,
			MaxRetries:  3,
			CreatedAt:   base.Add(time.Hour),
			UpdatedAt:   base.Add(time.Hour),
		},
	}
	for _, task := range tasks {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	// No filter returns all.
	got, total, err := s.ListTasks(ctx, ListTasksFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("expected total 3, got %d", total)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(got))
	}

	// Filter by status.
	got, total, err = s.ListTasks(ctx, ListTasksFilter{Status: string(models.TaskStatusPending)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != "task-pending" {
		t.Fatalf("unexpected status filter result: total=%d, tasks=%v", total, got)
	}

	// Filter by rule_id.
	got, total, err = s.ListTasks(ctx, ListTasksFilter{RuleID: "list-rule"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("unexpected rule_id filter result: total=%d, tasks=%v", total, got)
	}

	// Filter by worker_id.
	got, total, err = s.ListTasks(ctx, ListTasksFilter{WorkerID: "worker-1"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "task-running" {
		t.Fatalf("unexpected worker_id filter result: total=%d, tasks=%v", total, got)
	}

	// Filter by priority.
	got, total, err = s.ListTasks(ctx, ListTasksFilter{Priority: string(models.PriorityHigh)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "task-pending" {
		t.Fatalf("unexpected priority filter result: total=%d, tasks=%v", total, got)
	}

	// Filter by created_after / created_before.
	got, total, err = s.ListTasks(ctx, ListTasksFilter{CreatedAfter: base.Add(-30 * time.Minute), CreatedBefore: base.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "task-running" {
		t.Fatalf("unexpected time filter result: total=%d, tasks=%v", total, got)
	}

	// Pagination.
	got, total, err = s.ListTasks(ctx, ListTasksFilter{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 1 || got[0].ID != "task-failed" {
		t.Fatalf("unexpected pagination result: total=%d, len=%d", total, len(got))
	}
}

func TestCancelTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "cancel-rule",
		Version:   "1.0.0",
		Name:      "Cancel Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	for _, status := range []models.TaskStatus{models.TaskStatusPending, models.TaskStatusLeased} {
		task := &models.Task{
			ID:          string(status) + "-task",
			RuleID:      "cancel-rule",
			RuleVersion: "1.0.0",
			Status:      status,
			Priority:    models.PriorityNormal,
			MaxRetries:  3,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := s.CancelTask(ctx, task.ID); err != nil {
			t.Fatalf("cancel %s task: %v", status, err)
		}
		got, err := s.GetTaskByID(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != models.TaskStatusCancelled {
			t.Fatalf("expected cancelled, got %s", got.Status)
		}
		if !got.CompletedAt.Valid {
			t.Fatal("expected completed_at set")
		}
	}

	// Running tasks should get cancel_requested flag but stay running.
	runningTask := &models.Task{
		ID:          "running-task",
		RuleID:      "cancel-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusRunning,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, runningTask); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelTask(ctx, runningTask.ID); err != nil {
		t.Fatalf("cancel running task: %v", err)
	}
	got, err := s.GetTaskByID(ctx, runningTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TaskStatusRunning {
		t.Fatalf("expected running, got %s", got.Status)
	}
	if !got.CancelRequested {
		t.Fatal("expected cancel_requested set")
	}

	// Terminal states cannot be cancelled.
	for _, status := range []models.TaskStatus{models.TaskStatusDone, models.TaskStatusFailed, models.TaskStatusCancelled, models.TaskStatusDeadLetter} {
		task := &models.Task{
			ID:          string(status) + "-terminal-task",
			RuleID:      "cancel-rule",
			RuleVersion: "1.0.0",
			Status:      status,
			Priority:    models.PriorityNormal,
			MaxRetries:  3,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := s.CancelTask(ctx, task.ID); !errors.Is(err, ErrTaskNotCancellable) {
			t.Fatalf("expected ErrTaskNotCancellable for %s, got %v", status, err)
		}
	}

	// Missing task returns 404.
	if err := s.CancelTask(ctx, "missing-task"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}
}

func TestSchedulerLockAcquireRenewRelease(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	owner1 := "owner-1"
	owner2 := "owner-2"
	lease := time.Minute

	acquired, err := s.AcquireSchedulerLock(ctx, "schedule_runner", owner1, lease)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected owner1 to acquire lock")
	}

	// Acquiring again as the same owner renews the lock. The runner uses this
	// path on each tick before its explicit renewal.
	acquired, err = s.AcquireSchedulerLock(ctx, "schedule_runner", owner1, lease)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected owner1 to reacquire its own lock")
	}

	// Same owner can renew.
	renewed, err := s.RenewSchedulerLock(ctx, "schedule_runner", owner1, lease)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed {
		t.Fatal("expected owner1 to renew lock")
	}

	// Different owner cannot acquire while lock is held.
	acquired, err = s.AcquireSchedulerLock(ctx, "schedule_runner", owner2, lease)
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("expected owner2 to fail acquiring lock")
	}

	// Release and re-acquire by another owner.
	if err := s.ReleaseSchedulerLock(ctx, "schedule_runner", owner1); err != nil {
		t.Fatal(err)
	}
	acquired, err = s.AcquireSchedulerLock(ctx, "schedule_runner", owner2, lease)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected owner2 to acquire lock after release")
	}
}

func TestSchedulerLockStealsExpiredLock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	owner1 := "owner-1"
	owner2 := "owner-2"

	// Acquire with a very short lease.
	acquired, err := s.AcquireSchedulerLock(ctx, "schedule_runner", owner1, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected owner1 to acquire lock")
	}

	time.Sleep(5 * time.Millisecond)

	acquired, err = s.AcquireSchedulerLock(ctx, "schedule_runner", owner2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected owner2 to steal expired lock")
	}

	// Original owner can no longer renew.
	renewed, err := s.RenewSchedulerLock(ctx, "schedule_runner", owner1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed {
		t.Fatal("expected owner1 renew to fail")
	}
}

func TestRetryTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "retry-rule",
		Version:   "1.0.0",
		Name:      "Retry Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	for _, status := range []models.TaskStatus{models.TaskStatusFailed, models.TaskStatusDeadLetter, models.TaskStatusCancelled} {
		task := &models.Task{
			ID:           string(status) + "-task",
			RuleID:       "retry-rule",
			RuleVersion:  "1.0.0",
			Status:       status,
			Priority:     models.PriorityNormal,
			WorkerID:     sql.NullString{String: "worker-1", Valid: true},
			RetryCount:   2,
			MaxRetries:   3,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
			CompletedAt:  sql.NullTime{Time: time.Now().UTC(), Valid: true},
			ErrorType:    sql.NullString{String: "timeout", Valid: true},
			ErrorMessage: sql.NullString{String: "timed out", Valid: true},
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := s.RetryTask(ctx, task.ID); err != nil {
			t.Fatalf("retry %s task: %v", status, err)
		}
		got, err := s.GetTaskByID(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != models.TaskStatusPending {
			t.Fatalf("expected pending, got %s", got.Status)
		}
		if got.RetryCount != 0 {
			t.Fatalf("expected retry_count 0, got %d", got.RetryCount)
		}
		if got.WorkerID.Valid {
			t.Fatal("expected worker_id cleared")
		}
		if got.LeaseUntil.Valid {
			t.Fatal("expected lease_until cleared")
		}
		if got.CompletedAt.Valid {
			t.Fatal("expected completed_at cleared")
		}
		if got.ErrorType.Valid {
			t.Fatal("expected error_type cleared")
		}
		if got.ErrorMessage.Valid {
			t.Fatal("expected error_message cleared")
		}
	}

	// Non-retryable states.
	for _, status := range []models.TaskStatus{models.TaskStatusPending, models.TaskStatusLeased, models.TaskStatusRunning, models.TaskStatusDone} {
		task := &models.Task{
			ID:          string(status) + "-noretry-task",
			RuleID:      "retry-rule",
			RuleVersion: "1.0.0",
			Status:      status,
			Priority:    models.PriorityNormal,
			MaxRetries:  3,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := s.RetryTask(ctx, task.ID); !errors.Is(err, ErrTaskNotRetryable) {
			t.Fatalf("expected ErrTaskNotRetryable for %s, got %v", status, err)
		}
	}

	// Missing task returns 404.
	if err := s.RetryTask(ctx, "missing-task"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}
}

func TestJSON(t *testing.T) {
	b := JSON(map[string]string{"key": "value"})
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["key"] != "value" {
		t.Fatal("unexpected value")
	}
}

func TestInsertAndListAuditLogs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	logs := []*models.AuditLog{
		{
			ID:           "audit-1",
			Actor:        "admin",
			Action:       "create_rule",
			ResourceType: "rule",
			ResourceID:   "rule-a",
			Payload:      JSON(map[string]any{"ruleId": "rule-a"}),
			CreatedAt:    base.Add(-time.Hour),
		},
		{
			ID:           "audit-2",
			Actor:        "admin",
			Action:       "create_task",
			ResourceType: "task",
			ResourceID:   "task-b",
			Payload:      JSON(map[string]any{"taskId": "task-b"}),
			CreatedAt:    base,
		},
		{
			ID:           "audit-3",
			Actor:        "operator",
			Action:       "delete_rule",
			ResourceType: "rule",
			ResourceID:   "rule-c",
			Payload:      JSON(map[string]any{"ruleId": "rule-c"}),
			CreatedAt:    base.Add(time.Hour),
		},
	}
	for _, log := range logs {
		if err := s.InsertAuditLog(ctx, log); err != nil {
			t.Fatal(err)
		}
	}

	// No filter returns all, ordered by created_at DESC.
	got, total, err := s.ListAuditLogs(ctx, ListAuditLogsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("expected total 3, got %d", total)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(got))
	}
	if got[0].ID != "audit-3" {
		t.Fatalf("expected newest first, got %s", got[0].ID)
	}

	// Filter by actor.
	got, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != "audit-3" {
		t.Fatalf("unexpected actor filter result: total=%d, logs=%v", total, got)
	}

	// Filter by action.
	got, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{Action: "create_rule"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "audit-1" {
		t.Fatalf("unexpected action filter result: total=%d, logs=%v", total, got)
	}

	// Filter by resource_type and resource_id.
	got, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{ResourceType: "task", ResourceID: "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "audit-2" {
		t.Fatalf("unexpected resource filter result: total=%d, logs=%v", total, got)
	}

	// Filter by created_after / created_before.
	got, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{CreatedAfter: base.Add(-30 * time.Minute), CreatedBefore: base.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || got[0].ID != "audit-2" {
		t.Fatalf("unexpected time filter result: total=%d, logs=%v", total, got)
	}

	// Pagination.
	got, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 1 || got[0].ID != "audit-3" {
		t.Fatalf("unexpected pagination result: total=%d, len=%d", total, len(got))
	}
	got, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 1 || got[0].ID != "audit-2" {
		t.Fatalf("unexpected pagination offset result: total=%d, len=%d", total, len(got))
	}
}

func TestTaskVariableEncryption(t *testing.T) {
	ctx := context.Background()
	key := "test-encryption-key-for-variables"
	f, err := os.CreateTemp("", "opencrawler-encrypted-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := New(f.Name(), key)
	if err != nil {
		t.Fatal(err)
	}

	rule := &models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	vars := JSON(map[string]any{"secret": "value", "count": 42})
	task := &models.Task{
		ID:          NewID(),
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		Variables:   vars,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var gotVars map[string]any
	if err := json.Unmarshal(got.Variables, &gotVars); err != nil {
		t.Fatal(err)
	}
	if gotVars["secret"] != "value" || int(gotVars["count"].(float64)) != 42 {
		t.Fatalf("expected decrypted variables, got %v", gotVars)
	}

	// Verify the value stored in the database is not plain JSON.
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT variables FROM tasks WHERE id = ?`, task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == string(vars) {
		t.Fatal("expected encrypted variables in database, got plain JSON")
	}
}

func TestPurgeOldData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rule := &models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test Rule",
		Domain:    JSON("example.com"),
		Steps:     JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	oldCompletedAt := time.Now().UTC().Add(-30 * time.Minute)
	recentCompletedAt := time.Now().UTC().Add(-time.Minute)

	oldTask := &models.Task{
		ID:          "old-task",
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusDone,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   oldCompletedAt.Add(-time.Hour),
		UpdatedAt:   oldCompletedAt,
		CompletedAt: sql.NullTime{Time: oldCompletedAt, Valid: true},
	}
	recentTask := &models.Task{
		ID:          "recent-task",
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusDone,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   recentCompletedAt.Add(-time.Hour),
		UpdatedAt:   recentCompletedAt,
		CompletedAt: sql.NullTime{Time: recentCompletedAt, Valid: true},
	}
	if err := s.CreateTask(ctx, oldTask); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, recentTask); err != nil {
		t.Fatal(err)
	}

	// CreateTask does not accept completed_at; set it explicitly.
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, oldCompletedAt, oldTask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, recentCompletedAt, recentTask.ID); err != nil {
		t.Fatal(err)
	}

	oldChildTime := time.Now().UTC().Add(-2 * time.Hour)
	recentChildTime := time.Now().UTC().Add(-time.Minute)

	if err := s.InsertResult(ctx, &models.Result{ID: "r-old", TaskID: oldTask.ID, WorkerID: "w", Payload: []byte("{}"), CreatedAt: oldChildTime}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertResult(ctx, &models.Result{ID: "r-new", TaskID: recentTask.ID, WorkerID: "w", Payload: []byte("{}"), CreatedAt: recentChildTime}); err != nil {
		t.Fatal(err)
	}

	if err := s.InsertLog(ctx, &models.LogEntry{ID: "l-old", TaskID: oldTask.ID, WorkerID: "w", Level: "info", Message: "old", CreatedAt: oldChildTime}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLog(ctx, &models.LogEntry{ID: "l-new", TaskID: recentTask.ID, WorkerID: "w", Level: "info", Message: "new", CreatedAt: recentChildTime}); err != nil {
		t.Fatal(err)
	}

	if err := s.InsertSnapshot(ctx, &models.Snapshot{ID: "snap-old", TaskID: oldTask.ID, WorkerID: "w", Name: "old", Type: "html", Data: "old", CreatedAt: oldChildTime}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSnapshot(ctx, &models.Snapshot{ID: "snap-new", TaskID: recentTask.ID, WorkerID: "w", Name: "new", Type: "html", Data: "new", CreatedAt: recentChildTime}); err != nil {
		t.Fatal(err)
	}

	if err := s.InsertHeartbeat(ctx, &models.Heartbeat{ID: "hb-old", TaskID: oldTask.ID, WorkerID: "w", CreatedAt: oldChildTime}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertHeartbeat(ctx, &models.Heartbeat{ID: "hb-new", TaskID: recentTask.ID, WorkerID: "w", CreatedAt: recentChildTime}); err != nil {
		t.Fatal(err)
	}

	if err := s.InsertStatusUpdate(ctx, &models.TaskStatusUpdate{ID: "su-old", TaskID: oldTask.ID, WorkerID: "w", Status: "done", CreatedAt: oldChildTime}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertStatusUpdate(ctx, &models.TaskStatusUpdate{ID: "su-new", TaskID: recentTask.ID, WorkerID: "w", Status: "done", CreatedAt: recentChildTime}); err != nil {
		t.Fatal(err)
	}

	if err := s.InsertCheckpoint(ctx, &models.Checkpoint{ID: "cp-old", TaskID: oldTask.ID, WorkerID: "w", Name: "old", Payload: []byte("{}"), CreatedAt: oldChildTime}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCheckpoint(ctx, &models.Checkpoint{ID: "cp-new", TaskID: recentTask.ID, WorkerID: "w", Name: "new", Payload: []byte("{}"), CreatedAt: recentChildTime}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ResultRetention:        time.Hour,
		LogRetention:           time.Hour,
		SnapshotRetention:      time.Hour,
		HeartbeatRetention:     time.Hour,
		StatusUpdateRetention:  time.Hour,
		CheckpointRetention:    time.Hour,
		CompletedTaskRetention: 72 * time.Hour,
		RetentionBatchSize:     10,
	}

	report, err := s.PurgeOldData(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if report["results"] != 1 || report["logs"] != 1 || report["snapshots"] != 1 ||
		report["heartbeats"] != 1 || report["status_updates"] != 1 || report["checkpoints"] != 1 ||
		report["completed_tasks"] != 0 {
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

	assertCount("results", 1)
	assertCount("logs", 1)
	assertCount("snapshots", 1)
	assertCount("heartbeats", 1)
	assertCount("task_status_updates", 1)
	assertCount("checkpoints", 1)
	assertCount("tasks", 2)

	// Now run with short completed-task retention to purge the old task and any remaining children.
	cfg.CompletedTaskRetention = 30 * time.Minute
	report, err = s.PurgeOldData(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if report["completed_tasks"] != 1 {
		t.Fatalf("expected 1 completed task purged, got %d", report["completed_tasks"])
	}

	assertCount("tasks", 1)

	// Verify the recent task remains.
	got, err := s.GetTaskByID(ctx, recentTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != recentTask.ID {
		t.Fatalf("expected recent task to remain, got %s", got.ID)
	}
}

func TestMigrationsRecordVersions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rows, err := s.db.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	if !slices.Equal(versions, want) {
		t.Fatalf("expected schema_migrations versions %v, got %v", want, versions)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	s := newTestStore(t)

	if err := migrate(s.db); err != nil {
		t.Fatalf("re-running migrate: %v", err)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 31 {
		t.Fatalf("expected 31 recorded migrations after idempotent re-run, got %d", count)
	}
}

func TestMigration022BackfillsLegacyProviderAttemptProvenance(t *testing.T) {
	file, err := os.CreateTemp("", "aegis-migration-022-*.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	rawDB, err := sql.Open("sqlite", file.Name()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)

	for _, migration := range migrations {
		if migration.version > 21 {
			break
		}
		tx, err := rawDB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := migration.up(tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	if _, err := rawDB.Exec(`
		INSERT INTO recordings (
			id, workspace_id, owner, status, protocol_version,
			sanitization_version, content_hash, started_at, ended_at,
			created_at, updated_at, expires_at
		) VALUES (?, 'default', 'migration', 'captured', '2', '2', 'recording-hash', ?, ?, ?, ?, ?)
	`, "migration-022-recording", now, now, now, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`
		INSERT INTO requirement_jobs (
			id, workspace_id, recording_id, kind, status, source,
			request_artifact, request_hash, prompt_version, attempt_count,
			max_attempts, available_at, safety_flags, created_at, updated_at
		) VALUES (
			'migration-022-job', 'default', 'migration-022-recording',
			'candidates', 'running', 'llm', X'01', 'request-hash',
			'test-v1', 1, 6, ?, '[]', ?, ?
		)
	`, now, now, now); err != nil {
		t.Fatal(err)
	}
	insertLegacyCall := func(id string, callIndex int, cacheHit, truncated bool) {
		t.Helper()
		capturedBytes := 2
		replayable := true
		if truncated {
			capturedBytes = 1
			replayable = false
		}
		if _, err := rawDB.Exec(`
			INSERT INTO llm_provider_calls (
				id, workspace_id, recording_id, job_type, job_id,
				attempt_number, call_index, phase, chunk_count, provider,
				model, prompt_version, request_hash, response_hash,
				input_tokens, output_tokens, cache_hit, original_bytes,
				captured_bytes, artifact_bytes, redacted, truncated,
				replayable, artifact, artifact_hash, created_at
			) VALUES (
				?, 'default', 'migration-022-recording', 'requirement',
				'migration-022-job', 1, ?, 'final', 1, 'provider',
				'model', 'test-v1', 'request-hash', 'response-hash',
				1, 1, ?, 2, ?, 2, 0, ?, ?, X'0102', 'artifact-hash', ?
			)
		`, id, callIndex, cacheHit, capturedBytes, truncated, replayable, now); err != nil {
			t.Fatal(err)
		}
	}
	insertLegacyCall("legacy-provider-response", 1, false, false)
	insertLegacyCall("legacy-cache-hit", 2, true, false)
	insertLegacyCall("legacy-truncated-response", 3, false, true)

	tx, err := rawDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration022(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []struct {
		id              string
		kind            models.LLMProviderCallKind
		providerAttempt int
		originalExact   bool
	}{
		{
			id: "legacy-provider-response", kind: models.LLMProviderCallKindResponse,
			providerAttempt: 1, originalExact: true,
		},
		{
			id: "legacy-cache-hit", kind: models.LLMProviderCallKindCacheHit,
			providerAttempt: 0, originalExact: true,
		},
		{
			id: "legacy-truncated-response", kind: models.LLMProviderCallKindResponse,
			providerAttempt: 1, originalExact: false,
		},
	} {
		var kind models.LLMProviderCallKind
		var providerAttempt int
		var originalBytesExact bool
		if err := rawDB.QueryRow(
			`SELECT call_kind, provider_attempt, original_bytes_exact FROM llm_provider_calls WHERE id = ?`,
			expected.id,
		).Scan(&kind, &providerAttempt, &originalBytesExact); err != nil {
			t.Fatal(err)
		}
		if kind != expected.kind ||
			providerAttempt != expected.providerAttempt ||
			originalBytesExact != expected.originalExact {
			t.Fatalf(
				"unexpected migrated provenance for %s: kind=%s attempt=%d originalBytesExact=%v",
				expected.id,
				kind,
				providerAttempt,
				originalBytesExact,
			)
		}
	}
	var attemptBudget int
	if err := rawDB.QueryRow(
		`SELECT attempt_budget FROM requirement_jobs WHERE id = 'migration-022-job'`,
	).Scan(&attemptBudget); err != nil {
		t.Fatal(err)
	}
	if attemptBudget != 3 {
		t.Fatalf("inflated legacy retry ceiling was not clamped to the durable bound: %d", attemptBudget)
	}
	if _, err := rawDB.Exec(
		`UPDATE llm_provider_calls SET response_hash = 'tampered' WHERE id = 'legacy-provider-response'`,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("migration did not restore immutable provider calls: %v", err)
	}
}

func TestMigration024PreservesDSLJobsAndProviderHistory(t *testing.T) {
	file, err := os.CreateTemp("", "aegis-migration-024-*.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	rawDB, err := sql.Open("sqlite", file.Name()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)
	for _, migration := range migrations {
		if migration.version > 23 {
			break
		}
		tx, err := rawDB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := migration.up(tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// The v23 schema predates migration 028 (dsl_workflows.safety_flags,
	// rule_versions.safety_flags). Add the columns manually so fixture setup
	// using current store code (CompleteDSLJobWithAttemptReport,
	// ApproveDSLWorkflow) doesn't fail on the missing columns.
	// This mirrors what migration 028 would add in a full upgrade path.
	if _, err := rawDB.Exec(
		`ALTER TABLE dsl_workflows ADD COLUMN safety_flags TEXT NOT NULL DEFAULT '[]'`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(
		`ALTER TABLE rule_versions ADD COLUMN safety_flags TEXT NOT NULL DEFAULT '[]'`,
	); err != nil {
		t.Fatal(err)
	}
	// Mirror migration 029 (per-workflow budget envelope): current
	// CreateDSLWorkflow persists budget_usd_nanos explicitly.
	if _, err := rawDB.Exec(
		`ALTER TABLE dsl_workflows ADD COLUMN budget_usd_nanos INTEGER NOT NULL DEFAULT 5000000`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(
		`ALTER TABLE dsl_workflows ADD COLUMN spent_usd_nanos INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{
		db: rawDB, encryptionKey: "recording-encryption-key-for-tests",
	}
	ctx := workspaceContext("migration", "default")
	createLegacyClaimed := func(suffix string, maxAttempts int) (*models.DSLWorkflow, *models.DSLJob) {
		t.Helper()
		requirement := createConfirmedDSLRequirement(
			t, legacy, ctx, "dsl-recording-"+suffix, "dsl-requirement-"+suffix,
		)
		workflow := &models.DSLWorkflow{
			ID: "dsl-workflow-" + suffix, RequirementID: requirement.ID,
			RecordingID: requirement.RecordingID, BrowserProfileID: "current-chrome-profile",
		}
		job, err := legacy.CreateDSLWorkflow(
			ctx, workflow, map[string]any{"marker": suffix},
			DSLWorkflowOptions{JobMaxAttempts: maxAttempts},
		)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		lease := now.Add(time.Minute)
		if _, err := rawDB.Exec(`
			UPDATE dsl_jobs
			SET status = 'running', attempt_count = 1, started_at = ?,
			    lease_until = ?, updated_at = ?
			WHERE id = ?
		`, now, lease, now, job.ID); err != nil {
			t.Fatal(err)
		}
		job.Status = models.DSLJobRunning
		job.AttemptCount = 1
		job.StartedAt = &now
		job.LeaseUntil = &lease
		return workflow, job
	}

	completedWorkflow, completedJob := createLegacyClaimed("migration-024-completed", 3)
	call, callArtifact := providerCallForAttempt(
		models.LLMJobTypeDSL, completedJob.ID, completedJob.AttemptCount, 1,
		"migration-024-provider-response",
	)
	call.ID = "migration-024-provider-call"
	call.Phase = "final"
	if err := legacy.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	report, reportArtifact := attemptReportForJob(
		models.LLMJobTypeDSL, completedJob.ID, completedJob.AttemptCount,
	)
	report.ID = "migration-024-attempt-report"
	rule := workspaceRule("migration-024-rule", time.Now().UTC())
	rule.Version = "1"
	if err := legacy.CompleteDSLJobWithAttemptReport(
		ctx, completedJob, rule, "id: migration-024-rule\n",
		map[string]any{"providerIR": map[string]any{"id": rule.ID}},
		report, reportArtifact,
	); err != nil {
		t.Fatal(err)
	}
	_, runningJob := createLegacyClaimed("migration-024-running", 3)
	if err := legacy.UpdateDSLJobAttemptProgress(
		ctx, runningJob.ID, runningJob.AttemptCount, 3, 2,
	); err != nil {
		t.Fatal(err)
	}
	_, failedJob := createLegacyClaimed("migration-024-failed", 1)
	if err := legacy.FailDSLJob(ctx, failedJob, "MIGRATION_FIXTURE", "expected", 0); err != nil {
		t.Fatal(err)
	}
	pendingRequirement := createConfirmedDSLRequirement(
		t, legacy, ctx, "dsl-recording-migration-024-pending",
		"dsl-requirement-migration-024-pending",
	)
	pendingWorkflow := &models.DSLWorkflow{
		ID:               "dsl-workflow-migration-024-pending",
		RequirementID:    pendingRequirement.ID,
		RecordingID:      pendingRequirement.RecordingID,
		BrowserProfileID: "current-chrome-profile",
	}
	pendingJob, err := legacy.CreateDSLWorkflow(
		ctx, pendingWorkflow, map[string]any{"marker": "pending"},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshotJobs := func() []string {
		t.Helper()
		rows, err := rawDB.Query(`
			SELECT
			  quote(id) || '|' || quote(workspace_id) || '|' || quote(workflow_id) || '|' ||
			  quote(kind) || '|' || quote(status) || '|' || quote(request_artifact) || '|' ||
			  quote(result_artifact) || '|' || quote(request_hash) || '|' || quote(result_hash) || '|' ||
			  quote(provider) || '|' || quote(model) || '|' || quote(prompt_version) || '|' ||
			  quote(chunk_count) || '|' || quote(completed_chunks) || '|' ||
			  quote(input_tokens) || '|' || quote(output_tokens) || '|' ||
			  quote(attempt_count) || '|' || quote(max_attempts) || '|' ||
			  quote(available_at) || '|' || quote(lease_until) || '|' ||
			  quote(error_code) || '|' || quote(error_message) || '|' ||
			  quote(safety_flags) || '|' || quote(created_at) || '|' ||
			  quote(updated_at) || '|' || quote(started_at) || '|' || quote(completed_at)
			FROM dsl_jobs ORDER BY id
		`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []string
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	beforeJobs := snapshotJobs()

	tx, err := rawDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration024(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	expectedStatuses := map[string]models.DSLJobStatus{
		completedJob.ID: models.DSLJobCompleted,
		runningJob.ID:   models.DSLJobRunning,
		failedJob.ID:    models.DSLJobFailed,
		pendingJob.ID:   models.DSLJobPending,
	}
	afterJobs := snapshotJobs()
	if !slices.Equal(beforeJobs, afterJobs) {
		t.Fatalf("migration changed copied DSL job columns:\nbefore=%v\nafter=%v",
			beforeJobs, afterJobs)
	}
	for id, expected := range expectedStatuses {
		var status models.DSLJobStatus
		var dispatched bool
		var sourceReport sql.NullString
		if err := rawDB.QueryRow(`
			SELECT status, provider_dispatched, source_attempt_report_id
			FROM dsl_jobs WHERE id = ?
		`, id).Scan(&status, &dispatched, &sourceReport); err != nil {
			t.Fatal(err)
		}
		if status != expected || dispatched || sourceReport.Valid {
			t.Fatalf("migration changed legacy job %s: status=%s dispatched=%v source=%+v",
				id, status, dispatched, sourceReport)
		}
	}
	var preserved int
	if err := rawDB.QueryRow(`
		SELECT
		  (SELECT COUNT(*) FROM llm_provider_calls WHERE id = 'migration-024-provider-call')
		  + (SELECT COUNT(*) FROM llm_attempt_reports WHERE id = 'migration-024-attempt-report')
	`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != 2 {
		t.Fatalf("migration lost provider history: preserved=%d", preserved)
	}
	rows, err := rawDB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		var table, parent string
		var rowID int64
		var fkID int
		_ = rows.Scan(&table, &rowID, &parent, &fkID)
		rows.Close()
		t.Fatalf("migration left foreign-key violation: table=%s row=%d parent=%s fk=%d",
			table, rowID, parent, fkID)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var indexCount int
	if err := rawDB.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name IN (
		  'idx_dsl_jobs_claim', 'idx_dsl_jobs_workflow',
		  'idx_dsl_jobs_selector_repair_source'
		)
	`).Scan(&indexCount); err != nil || indexCount != 3 {
		t.Fatalf("migration indexes missing: count=%d err=%v", indexCount, err)
	}
	var triggerCount int
	if err := rawDB.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger' AND name IN (
		  'trg_dsl_jobs_workflow_insert',
		  'trg_dsl_selector_repair_source_insert',
		  'trg_dsl_jobs_immutable_identity',
		  'trg_dsl_jobs_state_transition',
		  'trg_dsl_jobs_terminal_immutable',
		  'trg_dsl_jobs_provider_dispatch_monotonic',
		  'trg_llm_provider_calls_dsl_parent',
		  'trg_llm_attempt_reports_dsl_parent',
		  'trg_dsl_workflows_state_transition'
		)
	`).Scan(&triggerCount); err != nil || triggerCount != 9 {
		t.Fatalf("migration triggers missing: count=%d err=%v", triggerCount, err)
	}
	if _, err := rawDB.Exec(`
		INSERT INTO dsl_jobs (
		  id, workspace_id, workflow_id, kind, status, request_artifact,
		  request_hash, prompt_version, max_attempts, available_at,
		  source_attempt_report_id, safety_flags, created_at, updated_at
		) VALUES (
		  'invalid-selector-child', 'default', ?, 'selector_repair', 'pending',
		  X'01', 'request', 'dsl-selector-repair-v1', 1, CURRENT_TIMESTAMP,
		  'migration-024-attempt-report', '[]', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		)
	`, completedWorkflow.ID); err == nil ||
		!strings.Contains(err.Error(), "source report") {
		t.Fatalf("migration accepted ineligible selector source: %v", err)
	}

	postCall, postCallArtifact := providerCallForAttempt(
		models.LLMJobTypeDSL, runningJob.ID, runningJob.AttemptCount, 1,
		"migration-024-post-upgrade",
	)
	postCall.Phase = "final"
	if err := legacy.CreateLLMProviderCall(ctx, postCall, postCallArtifact); err != nil {
		t.Fatalf("post-upgrade provider lineage insert failed: %v", err)
	}
	postReport, postReportArtifact := attemptReportForJob(
		models.LLMJobTypeDSL, runningJob.ID, runningJob.AttemptCount,
	)
	postRule := workspaceRule("migration-024-post-rule", time.Now().UTC())
	postRule.Version = "1"
	if err := legacy.CompleteDSLJobWithAttemptReport(
		ctx, runningJob, postRule, "id: migration-024-post-rule\n",
		map[string]any{"providerIR": map[string]any{"id": postRule.ID}},
		postReport, postReportArtifact,
	); err != nil {
		t.Fatalf("post-upgrade completion/report insertion failed: %v", err)
	}
	var postCount int
	if err := rawDB.QueryRow(`
		SELECT COUNT(*) FROM llm_attempt_reports WHERE job_id = ?
	`, runningJob.ID).Scan(&postCount); err != nil || postCount != 1 {
		t.Fatalf("post-upgrade attempt report missing: count=%d err=%v", postCount, err)
	}
}

func TestMigration025PreservesLegacyLineageAndSupportsReviewedAdoption(t *testing.T) {
	file, err := os.CreateTemp("", "aegis-migration-025-*.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	rawDB, err := sql.Open("sqlite", file.Name()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)
	for _, migration := range migrations {
		if migration.version > 24 {
			break
		}
		tx, err := rawDB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := migration.up(tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// The v24 schema predates migration 028 (dsl_workflows.safety_flags,
	// rule_versions.safety_flags). Add the columns manually so store code
	// that reads safety_flags doesn't fail on the missing columns.
	for _, col := range []string{
		`ALTER TABLE dsl_workflows ADD COLUMN safety_flags TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE rule_versions ADD COLUMN safety_flags TEXT NOT NULL DEFAULT '[]'`,
	} {
		if _, err := rawDB.Exec(col); err != nil {
			t.Fatal(err)
		}
	}

	// The v24 schema also predates migration 029 (per-workflow budget
	// envelope); current workflow INSERTs persist the columns explicitly.
	for _, col := range []string{
		`ALTER TABLE dsl_workflows ADD COLUMN budget_usd_nanos INTEGER NOT NULL DEFAULT 5000000`,
		`ALTER TABLE dsl_workflows ADD COLUMN spent_usd_nanos INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := rawDB.Exec(col); err != nil {
			t.Fatal(err)
		}
	}

	// The v24 schema also predates migration 030 (LLM provenance columns on
	// dsl_approvals and rule_versions). Add them manually so store code that
	// reads provenance doesn't fail on the missing columns.
	for _, col := range []string{
		`ALTER TABLE dsl_approvals ADD COLUMN dsl_job_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE dsl_approvals ADD COLUMN provider_call_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE dsl_approvals ADD COLUMN prompt_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE dsl_approvals ADD COLUMN model_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE dsl_approvals ADD COLUMN cache_hit BOOLEAN NOT NULL DEFAULT 0`,
		`ALTER TABLE rule_versions ADD COLUMN dsl_workflow_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rule_versions ADD COLUMN dsl_job_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := rawDB.Exec(col); err != nil {
			t.Fatal(err)
		}
	}

	legacy := &Store{
		db: rawDB, encryptionKey: "recording-encryption-key-for-tests",
	}
	ctx := workspaceContext("migration", "default")
	now := time.Now().UTC()
	requirement := createConfirmedDSLRequirement(
		t, legacy, ctx, "migration-025-legacy-recording", "migration-025-legacy-requirement",
	)
	recording, _, err := legacy.GetRecordingByID(ctx, requirement.RecordingID)
	if err != nil {
		t.Fatal(err)
	}
	rule := workspaceRule("migration-025-legacy-rule", now)
	rule.Owner = "migration"
	if err := legacy.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	ruleJSON, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`
		INSERT INTO rule_versions (
			workspace_id, rule_id, version_number, version_label, rule_json,
			content_hash, approval_status, owner, source, recording_id,
			created_at, approved_at, approved_by
		) VALUES (
			'default', ?, 1, ?, ?, ?, 'approved', ?, ?, ?, ?, ?, ?
		)
	`, rule.ID, rule.Version, ruleJSON, strings.Repeat("c", 64), rule.Owner,
		rule.Source, requirement.RecordingID, now, now, rule.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`
		INSERT INTO rule_version_contracts (
			workspace_id, rule_id, version_number, input_schema, output_schema,
			browser_profile_id, created_at
		) VALUES ('default', ?, 1, ?, ?, 'legacy-profile', ?)
	`, rule.ID,
		models.JSON(`{"type":"object","properties":{},"additionalProperties":false}`),
		models.JSON(`{"type":"object","properties":{},"additionalProperties":true}`),
		now); err != nil {
		t.Fatal(err)
	}
	const legacyWorkflowID = "migration-025-legacy-workflow"
	if _, err := rawDB.Exec(`
		INSERT INTO dsl_workflows (
			id, workspace_id, requirement_id, recording_id, status,
			browser_profile_id, repair_count, max_repairs, last_replay_sequence,
			owner, created_at, updated_at, approved_at
		) VALUES (?, 'default', ?, ?, 'approved', 'legacy-profile', 0, 3, 0, ?, ?, ?, ?)
	`, legacyWorkflowID, requirement.ID, requirement.RecordingID, rule.Owner,
		now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`
		INSERT INTO dsl_approvals (
			workspace_id, workflow_id, requirement_id, requirement_hash,
			recording_id, recording_hash, rule_id, version_number, created_at
		) VALUES ('default', ?, ?, ?, ?, ?, ?, 1, ?)
	`, legacyWorkflowID, requirement.ID, requirement.ContentHash,
		requirement.RecordingID, recording.ContentHash, rule.ID, now); err != nil {
		t.Fatal(err)
	}

	tx, err := rawDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migration025(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := migration026(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := migration027(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	legacyWorkflow, err := legacy.GetDSLWorkflow(ctx, legacyWorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	if legacyWorkflow.Status != models.DSLWorkflowApproved ||
		legacyWorkflow.ApprovedRuleID != rule.ID ||
		legacyWorkflow.ApprovedVersion != 1 ||
		legacyWorkflow.SourceKind != "" ||
		legacyWorkflow.SourceAuthority != "" ||
		legacyWorkflow.SourceArtifactHash != "" ||
		legacyWorkflow.SourceExportHash != "" {
		t.Fatalf("legacy workflow lineage was not preserved with empty defaults: %+v", legacyWorkflow)
	}
	legacyVersion, err := legacy.GetRuleVersion(ctx, rule.ID, 1)
	if err != nil || legacyVersion.Status != models.RuleApprovalApproved {
		t.Fatalf("legacy rule version was not readable: version=%+v err=%v", legacyVersion, err)
	}
	legacyContract, err := legacy.GetRuleVersionContract(ctx, rule.ID, 1)
	if err != nil ||
		legacyContract.SourceKind != "" ||
		legacyContract.SourceAuthority != "" ||
		legacyContract.SourceArtifactHash != "" ||
		legacyContract.SourceExportHash != "" ||
		legacyContract.SourceWorkflowID != "" {
		t.Fatalf("legacy contract lineage was not preserved with empty defaults: contract=%+v err=%v",
			legacyContract, err)
	}
	var approvalKind, approvalAuthority, approvalArtifactHash, approvalExportHash string
	if err := rawDB.QueryRow(`
		SELECT source_kind, source_authority, source_artifact_hash, source_export_hash
		FROM dsl_approvals WHERE workflow_id = ?
	`, legacyWorkflowID).Scan(
		&approvalKind, &approvalAuthority, &approvalArtifactHash, &approvalExportHash,
	); err != nil {
		t.Fatal(err)
	}
	if approvalKind != "" || approvalAuthority != "" ||
		approvalArtifactHash != "" || approvalExportHash != "" {
		t.Fatalf("legacy approval received invented lineage: %q %q %q %q",
			approvalKind, approvalAuthority, approvalArtifactHash, approvalExportHash)
	}
	legacyApprovalLogs, legacyApprovalTotal, err := legacy.ListAuditLogs(ctx, ListAuditLogsFilter{
		Action: "dsl_workflow_approved", ResourceType: "dsl_workflow",
		ResourceID: legacyWorkflowID,
	})
	if err != nil || legacyApprovalTotal != 1 || len(legacyApprovalLogs) != 1 ||
		legacyApprovalLogs[0].Actor != "system:migration-025" ||
		!bytes.Contains(legacyApprovalLogs[0].Payload, []byte(rule.ID)) ||
		!bytes.Contains(legacyApprovalLogs[0].Payload, []byte(strings.Repeat("c", 64))) {
		t.Fatalf("legacy approval audit was not backfilled exactly once: total=%d logs=%+v err=%v",
			legacyApprovalTotal, legacyApprovalLogs, err)
	}
	legacyApproval, legacyApprovalContract, err := legacy.ApproveDSLWorkflow(ctx, legacyWorkflowID, ApproveDSLWorkflowOptions{})
	if err != nil || legacyApproval == nil || legacyApprovalContract == nil ||
		legacyApproval.RuleID != rule.ID || legacyApproval.Version != 1 ||
		legacyApprovalContract.SourceWorkflowID != "" {
		t.Fatalf("migrated legacy approval retry was not readable: version=%+v contract=%+v err=%v",
			legacyApproval, legacyApprovalContract, err)
	}
	for label, statement := range map[string]string{
		"workflow identity": `UPDATE dsl_workflows
			SET source_authority = 'tampered' WHERE id = 'migration-025-legacy-workflow'`,
		"approval lineage": `UPDATE dsl_approvals
			SET source_authority = 'tampered' WHERE workflow_id = 'migration-025-legacy-workflow'`,
		"contract lineage": `UPDATE rule_version_contracts
			SET source_authority = 'tampered'
			WHERE rule_id = 'migration-025-legacy-rule' AND version_number = 1`,
		"approval deletion": `DELETE FROM dsl_approvals
			WHERE workflow_id = 'migration-025-legacy-workflow'`,
		"contract deletion": `DELETE FROM rule_version_contracts
			WHERE rule_id = 'migration-025-legacy-rule' AND version_number = 1`,
	} {
		if _, err := rawDB.Exec(statement); err == nil {
			t.Fatalf("migration left %s mutable", label)
		}
	}

	postRequirement := createConfirmedDSLRequirement(
		t, legacy, ctx, "migration-025-post-recording", "migration-025-post-requirement",
	)
	postRule := workspaceRule("migration-025-post-rule", time.Now().UTC())
	postRule.Version = "1"
	postWorkflow := &models.DSLWorkflow{
		ID:                 "migration-025-post-workflow",
		RequirementID:      postRequirement.ID,
		RecordingID:        postRequirement.RecordingID,
		BrowserProfileID:   "reviewed-profile",
		SourceArtifactHash: strings.Repeat("a", 64),
		SourceExportHash:   strings.Repeat("b", 64),
	}
	if _, err := legacy.CreateAdminReviewedDSLWorkflow(
		ctx, postWorkflow, postRule, "id: migration-025-post-rule\n",
	); err != nil {
		t.Fatalf("post-upgrade reviewed adoption failed: %v", err)
	}
	if _, err := rawDB.Exec(`
		UPDATE dsl_workflows SET source_authority = 'tampered' WHERE id = ?
	`, postWorkflow.ID); err == nil {
		t.Fatal("migrated workflow identity trigger allowed reviewed source mutation")
	}
	replay, err := legacy.StartDSLReplay(ctx, postWorkflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repair, err := legacy.CompleteDSLReplay(
		ctx, replay, true, true, map[string]any{"status": "passed"},
		map[string]any{"name": "Example", "price": 10.5}, nil, "", "", nil,
	); err != nil || repair != nil {
		t.Fatalf("post-upgrade reviewed replay failed: repair=%+v err=%v", repair, err)
	}
	approved, approvedContract, err := legacy.ApproveDSLWorkflow(ctx, postWorkflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if approved == nil || approvedContract == nil ||
		approved.Status != models.RuleApprovalApproved ||
		approved.Source != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		approvedContract.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		approvedContract.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		approvedContract.SourceArtifactHash != postWorkflow.SourceArtifactHash ||
		approvedContract.SourceExportHash != postWorkflow.SourceExportHash ||
		approvedContract.SourceWorkflowID != postWorkflow.ID {
		t.Fatalf("post-upgrade approval lost reviewed lineage: version=%+v contract=%+v",
			approved, approvedContract)
	}
	retried, retriedContract, err := legacy.ApproveDSLWorkflow(ctx, postWorkflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil || retried == nil || retriedContract == nil ||
		retried.Version != approved.Version ||
		retriedContract.SourceWorkflowID != postWorkflow.ID {
		t.Fatalf("post-upgrade approval retry was not idempotent: version=%+v contract=%+v err=%v",
			retried, retriedContract, err)
	}
	if err := rawDB.QueryRow(`
		SELECT source_kind, source_authority, source_artifact_hash, source_export_hash
		FROM dsl_approvals WHERE workflow_id = ?
	`, postWorkflow.ID).Scan(
		&approvalKind, &approvalAuthority, &approvalArtifactHash, &approvalExportHash,
	); err != nil {
		t.Fatal(err)
	}
	if approvalKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		approvalAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		approvalArtifactHash != postWorkflow.SourceArtifactHash ||
		approvalExportHash != postWorkflow.SourceExportHash {
		t.Fatalf("post-upgrade approval row lost reviewed lineage: %q %q %q %q",
			approvalKind, approvalAuthority, approvalArtifactHash, approvalExportHash)
	}
	postApprovalLogs, postApprovalTotal, err := legacy.ListAuditLogs(ctx, ListAuditLogsFilter{
		Action: "dsl_workflow_approved", ResourceType: "dsl_workflow",
		ResourceID: postWorkflow.ID,
	})
	if err != nil || postApprovalTotal != 1 || len(postApprovalLogs) != 1 ||
		postApprovalLogs[0].Actor != "migration" {
		t.Fatalf("post-upgrade approval audit was not atomic and singular: total=%d logs=%+v err=%v",
			postApprovalTotal, postApprovalLogs, err)
	}
	var postJobs int
	if err := rawDB.QueryRow(
		`SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, postWorkflow.ID,
	).Scan(&postJobs); err != nil || postJobs != 0 {
		t.Fatalf("post-upgrade reviewed adoption created provider jobs: count=%d err=%v",
			postJobs, err)
	}

	rows, err := rawDB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID int64
		var fkID int
		_ = rows.Scan(&table, &rowID, &parent, &fkID)
		t.Fatalf("migration left foreign-key violation: table=%s row=%d parent=%s fk=%d",
			table, rowID, parent, fkID)
	}
}

func TestMigration002AddsMissingApprovalStatus(t *testing.T) {
	f, err := os.CreateTemp("", "opencrawler-legacy-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	rawDB, err := sql.Open("sqlite", f.Name())
	if err != nil {
		t.Fatal(err)
	}

	legacyRules := `CREATE TABLE rules (
		id TEXT PRIMARY KEY,
		version TEXT NOT NULL,
		name TEXT NOT NULL,
		domain TEXT NOT NULL,
		url_pattern TEXT,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		priority TEXT NOT NULL DEFAULT 'normal',
		entry TEXT,
		variables TEXT,
		selectors TEXT,
		humanize TEXT,
		steps TEXT NOT NULL,
		output TEXT,
		send_policy TEXT,
		hooks TEXT,
		tags TEXT,
		owner TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := rawDB.Exec(legacyRules); err != nil {
		t.Fatalf("create legacy rules table: %v", err)
	}

	now := time.Now().UTC()
	if _, err := rawDB.Exec(
		`INSERT INTO rules (id, version, name, domain, steps, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"legacy-rule", "1.0.0", "Legacy Rule", JSON("example.com"), JSON([]any{"step1"}), now, now,
	); err != nil {
		t.Fatalf("insert legacy rule: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := New(f.Name(), "")
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT approval_status FROM rules WHERE id = ?`, "legacy-rule").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Fatalf("expected approval_status default 'approved' for legacy row, got %q", status)
	}
	var workspace string
	if err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM rules WHERE id = ?`, "legacy-rule").Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	if workspace != "default" {
		t.Fatalf("expected legacy rule in default workspace, got %q", workspace)
	}
	var versionNumber int
	var versionStatus, versionHash string
	if err := s.db.QueryRowContext(ctx, `
		SELECT version_number, approval_status, content_hash
		FROM rule_versions WHERE workspace_id = ? AND rule_id = ?
	`, "default", "legacy-rule").Scan(&versionNumber, &versionStatus, &versionHash); err != nil {
		t.Fatal(err)
	}
	if versionNumber != 1 || versionStatus != "approved" || versionHash == "" {
		t.Fatalf("legacy rule was not backfilled as approved immutable version 1: version=%d status=%q hash=%q", versionNumber, versionStatus, versionHash)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version IN (1,2,3,4,5,6)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("expected all 6 migrations recorded for legacy db, got %d", count)
	}
}

func TestSQLiteJournalModeWAL(t *testing.T) {
	f, err := os.CreateTemp("", "opencrawler-wal-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	cfg := &config.Config{DatabasePath: f.Name(), SQLiteJournalMode: "WAL"}
	s, err := NewWithConfig(cfg, f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(mode) != "wal" {
		t.Fatalf("expected journal_mode wal, got %q", mode)
	}
}

func TestSQLiteJournalModeDELETE(t *testing.T) {
	f, err := os.CreateTemp("", "opencrawler-delete-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	cfg := &config.Config{DatabasePath: f.Name(), SQLiteJournalMode: "DELETE"}
	s, err := NewWithConfig(cfg, f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(mode) != "delete" {
		t.Fatalf("expected journal_mode delete, got %q", mode)
	}
}
