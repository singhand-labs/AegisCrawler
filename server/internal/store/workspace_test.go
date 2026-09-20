package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func workspaceContext(subject, workspace string) context.Context {
	return authz.WithPrincipal(context.Background(), authz.Principal{
		Subject:     subject,
		WorkspaceID: workspace,
		Roles:       []authz.Role{authz.RoleAdmin},
		Kind:        authz.PrincipalAdmin,
	})
}

func workspaceRule(id string, now time.Time) *models.Rule {
	return &models.Rule{
		ID:             id,
		Version:        "1.0.0",
		Name:           id,
		Domain:         JSON("example.com"),
		Steps:          JSON([]any{"step"}),
		Enabled:        true,
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func workspaceTask(id, ruleID string, now time.Time) *models.Task {
	return &models.Task{
		ID:          id,
		RuleID:      ruleID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestWorkspaceIsolationAcrossStores(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")

	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}

	ruleA := workspaceRule("rule-a", now)
	if err := s.CreateRule(ctxA, ruleA); err != nil {
		t.Fatal(err)
	}
	if ruleA.WorkspaceID != authz.DefaultWorkspaceID {
		t.Fatalf("expected default workspace, got %q", ruleA.WorkspaceID)
	}
	if _, err := s.GetRuleByID(ctxB, ruleA.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("cross-workspace rule lookup should be hidden, got %v", err)
	}
	if rules, total, err := s.ListRules(ctxB, ListRulesFilter{}); err != nil || total != 0 || len(rules) != 0 {
		t.Fatalf("cross-workspace rule list leaked data: total=%d rules=%v err=%v", total, rules, err)
	}

	collision := workspaceRule(ruleA.ID, now)
	collision.Name = "must-not-overwrite"
	if err := s.CreateRule(ctxB, collision); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("expected global id collision to be rejected, got %v", err)
	}
	gotA, err := s.GetRuleByID(ctxA, ruleA.ID)
	if err != nil || gotA.Name != ruleA.Name {
		t.Fatalf("cross-workspace upsert changed original rule: rule=%+v err=%v", gotA, err)
	}

	ruleB := workspaceRule("rule-b", now)
	if err := s.CreateRule(ctxB, ruleB); err != nil {
		t.Fatal(err)
	}
	taskA := workspaceTask("task-a", ruleA.ID, now)
	if err := s.CreateTask(ctxA, taskA); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctxB, workspaceTask("invalid-task", ruleA.ID, now)); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected cross-workspace rule binding to fail, got %v", err)
	}
	if _, err := s.GetTaskByID(ctxB, taskA.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-workspace task lookup should be hidden, got %v", err)
	}

	old := now.Add(-2 * time.Hour)
	if err := s.InsertResult(ctxA, &models.Result{ID: "result-a", TaskID: taskA.ID, WorkerID: "worker-a", Payload: JSON(map[string]any{"tenant": "a"}), CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLog(ctxA, &models.LogEntry{ID: "log-a", TaskID: taskA.ID, WorkerID: "worker-a", Level: "info", Message: "private", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCheckpoint(ctxA, &models.Checkpoint{ID: "checkpoint-a", TaskID: taskA.ID, WorkerID: "worker-a", Name: "private", Payload: JSON(map[string]any{}), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if results, err := s.ListResults(ctxB, taskA.ID); err != nil || len(results) != 0 {
		t.Fatalf("cross-workspace results leaked data: results=%v err=%v", results, err)
	}
	if logs, err := s.ListLogs(ctxB, taskA.ID); err != nil || len(logs) != 0 {
		t.Fatalf("cross-workspace logs leaked data: logs=%v err=%v", logs, err)
	}
	if _, err := s.GetLatestCheckpoint(ctxB, taskA.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-workspace checkpoint lookup should be hidden, got %v", err)
	}
	if err := s.InsertResult(ctxB, &models.Result{ID: "invalid-result", TaskID: taskA.ID, CreatedAt: now}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-workspace result insert should fail, got %v", err)
	}

	scheduleA := &models.Schedule{
		ID: "schedule-a", RuleID: ruleA.ID, RuleVersion: ruleA.Version, Name: "A",
		Type: models.ScheduleTypeCron, Expression: "0 * * * *", Enabled: true,
		Priority: models.PriorityNormal, MaxRetries: 3, Catchup: models.CatchupSkip,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateSchedule(ctxA, scheduleA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetScheduleByID(ctxB, scheduleA.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("cross-workspace schedule lookup should be hidden, got %v", err)
	}

	auditA := &models.AuditLog{ID: "audit-a", Actor: "alice", Action: "create", ResourceType: "rule", ResourceID: ruleA.ID, CreatedAt: now}
	if err := s.InsertAuditLog(ctxA, auditA); err != nil {
		t.Fatal(err)
	}
	if logs, total, err := s.ListAuditLogs(ctxB, ListAuditLogsFilter{}); err != nil || total != 0 || len(logs) != 0 {
		t.Fatalf("cross-workspace audit logs leaked data: total=%d logs=%v err=%v", total, logs, err)
	}

	jobA := &models.LLMJob{ID: "job-a", RuleID: ruleA.ID, Status: string(models.LLMJobStatusPending), CreatedAt: now}
	if err := s.CreateLLMJob(ctxA, jobA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLLMJob(ctxB, jobA.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("cross-workspace LLM job lookup should be hidden, got %v", err)
	}

	enhancementA := &models.RuleEnhancement{ID: "enhancement-a", RuleID: ruleA.ID, Status: string(models.EnhancementStatusPending), CreatedAt: now}
	if err := s.CreateRuleEnhancement(ctxA, enhancementA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRuleEnhancementByRuleID(ctxB, ruleA.ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("cross-workspace enhancement lookup should be hidden, got %v", err)
	}

	lockedA, err := s.AcquireSchedulerLock(ctxA, "scheduler", "owner-a", time.Minute)
	if err != nil || !lockedA {
		t.Fatalf("workspace A lock failed: locked=%v err=%v", lockedA, err)
	}
	lockedB, err := s.AcquireSchedulerLock(ctxB, "scheduler", "owner-b", time.Minute)
	if err != nil || !lockedB {
		t.Fatalf("same lock name should be independent across workspaces: locked=%v err=%v", lockedB, err)
	}

	taskB := workspaceTask("task-b", ruleB.ID, now)
	if err := s.CreateTask(ctxB, taskB); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertResult(ctxB, &models.Result{ID: "result-b", TaskID: taskB.ID, WorkerID: "worker-b", Payload: JSON(map[string]any{"tenant": "b"}), CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	retention := &config.Config{
		ResultRetention: time.Hour, LogRetention: 24 * time.Hour, SnapshotRetention: 24 * time.Hour,
		HeartbeatRetention: 24 * time.Hour, StatusUpdateRetention: 24 * time.Hour, CheckpointRetention: 24 * time.Hour,
		CompletedTaskRetention: 24 * time.Hour, RetentionBatchSize: 10,
	}
	if _, err := s.PurgeOldData(ctxA, retention); err != nil {
		t.Fatal(err)
	}
	if results, err := s.ListResults(ctxB, taskB.ID); err != nil || len(results) != 1 {
		t.Fatalf("workspace A retention changed workspace B: results=%v err=%v", results, err)
	}
}

func TestWorkspaceMigrationCreatesBoundaryColumns(t *testing.T) {
	s := newTestStore(t)

	workspace, err := s.GetWorkspaceByID(context.Background(), authz.DefaultWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Name != authz.DefaultWorkspaceID {
		t.Fatalf("unexpected default workspace: %+v", workspace)
	}

	for _, table := range []string{
		"rules", "tasks", "schedules", "results", "logs", "snapshots", "heartbeats",
		"task_status_updates", "checkpoints", "audit_logs", "rule_enhancements", "llm_jobs",
		"llm_cache", "scheduler_locks", "recordings", "rule_versions",
		"human_interventions",
	} {
		var count int
		if err := s.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = 'workspace_id'", table)).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected %s.workspace_id, got count %d", table, count)
		}
	}
}
