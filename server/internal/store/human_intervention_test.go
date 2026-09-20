package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func createHumanInterventionAttempt(t *testing.T, s *Store, suffix string) (context.Context, *models.Task) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	rule := &models.Rule{
		ID: "human-rule-" + suffix, Version: "1.0.0", Name: "Human Rule",
		Domain: JSON("example.com"), Steps: JSON([]any{"requestHuman"}),
		Enabled: true, ApprovalStatus: string(models.RuleApprovalApproved),
		Priority: models.PriorityNormal, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ID: "human-task-" + suffix, RuleID: rule.ID, RuleVersion: rule.Version,
		Status: models.TaskStatusPending, Priority: models.PriorityNormal,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimTaskForBrowserProfile(ctx, "human-worker-"+suffix, "", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskStatus(ctx, claimed.ID, claimed.WorkerID.String, string(models.TaskStatusRunning), "started", ""); err != nil {
		t.Fatal(err)
	}
	return ctx, claimed
}

func humanInterventionInput(task *models.Task, suffix string, createdAt, expiresAt time.Time) HumanInterventionInput {
	return HumanInterventionInput{
		ID: "human-" + suffix, TaskID: task.ID,
		AttemptID: task.CurrentAttemptID.String, WorkerID: task.WorkerID.String,
		CheckpointID:   "checkpoint-" + suffix,
		CheckpointName: "human-intervention:human-" + suffix,
		Checkpoint: JSON(map[string]any{
			"stepId": "manual-step", "targetOrigin": "https://example.com",
			"attemptId": task.CurrentAttemptID.String,
		}),
		Type: "2fa", Prompt: "Complete the approved two-factor step",
		RequestedAction: "complete_2fa_then_resume", TargetOrigin: "https://example.com",
		CreatedAt: createdAt, ExpiresAt: expiresAt,
	}
}

func TestHumanInterventionApprovalResumesSameAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx, claimed := createHumanInterventionAttempt(t, s, "approve")
	now := time.Now().UTC()
	item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "approve", now, now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != models.HumanInterventionPending || item.AttemptID != claimed.CurrentAttemptID.String {
		t.Fatalf("unexpected pending intervention: %+v", item)
	}
	var statusMessage string
	if err := s.db.QueryRowContext(ctx, `SELECT message FROM task_status_updates
		WHERE task_id = ? AND status = 'waiting_for_human' ORDER BY created_at DESC LIMIT 1`, claimed.ID).Scan(&statusMessage); err != nil {
		t.Fatal(err)
	}
	if statusMessage != "operator decision required: 2fa" {
		t.Fatalf("status history persisted the operator prompt: %q", statusMessage)
	}
	waiting, err := s.GetTaskByID(ctx, claimed.ID)
	if err != nil || waiting.Status != models.TaskStatusWaitingHuman || waiting.LeaseUntil.Valid {
		t.Fatalf("task did not enter waiting state: task=%+v err=%v", waiting, err)
	}
	attempt, err := s.GetExecutionAttempt(ctx, claimed.CurrentAttemptID.String)
	if err != nil || attempt.Status != models.ExecutionAttemptWaitingHuman {
		t.Fatalf("attempt did not enter waiting state: attempt=%+v err=%v", attempt, err)
	}
	if err := s.RenewLease(ctx, claimed.ID, claimed.WorkerID.String, time.Minute); err != nil {
		t.Fatalf("waiting worker heartbeat was rejected: %v", err)
	}

	if _, err := s.DecideHumanIntervention(ctx, item.ID, "wrong-checkpoint",
		models.HumanInterventionApproved, "", "operator", time.Minute); !errors.Is(err, ErrHumanCheckpointConflict) {
		t.Fatalf("expected checkpoint conflict, got %v", err)
	}
	approved, err := s.DecideHumanIntervention(ctx, item.ID, item.CheckpointID,
		models.HumanInterventionApproved, "manual step complete", "operator", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != models.HumanInterventionApproved || approved.DecidedBy != "operator" {
		t.Fatalf("unexpected approval: %+v", approved)
	}
	resumed, err := s.GetTaskByID(ctx, claimed.ID)
	if err != nil || resumed.Status != models.TaskStatusRunning || resumed.CurrentAttemptID.String != claimed.CurrentAttemptID.String || !resumed.LeaseUntil.Valid {
		t.Fatalf("task did not resume same attempt: task=%+v err=%v", resumed, err)
	}
	attempt, err = s.GetExecutionAttempt(ctx, claimed.CurrentAttemptID.String)
	if err != nil || attempt.Status != models.ExecutionAttemptRunning || attempt.LeaseUntil == nil {
		t.Fatalf("attempt did not resume: attempt=%+v err=%v", attempt, err)
	}
	if _, err := s.DecideHumanIntervention(ctx, item.ID, item.CheckpointID,
		models.HumanInterventionApproved, "", "operator", time.Minute); !errors.Is(err, ErrHumanInterventionConflict) {
		t.Fatalf("expected repeated decision conflict, got %v", err)
	}
}

func TestHumanInterventionRejectExpireAndCancelFailClosed(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		s := newTestStore(t)
		ctx, claimed := createHumanInterventionAttempt(t, s, "reject")
		now := time.Now().UTC()
		item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "reject", now, now.Add(time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DecideHumanIntervention(ctx, item.ID, item.CheckpointID,
			models.HumanInterventionRejected, "unsafe", "operator", time.Minute); err != nil {
			t.Fatal(err)
		}
		task, _ := s.GetTaskByID(ctx, claimed.ID)
		if task.Status != models.TaskStatusFailed || task.ErrorType.String != "HumanRejected" {
			t.Fatalf("rejection did not fail closed: %+v", task)
		}
	})

	t.Run("expire", func(t *testing.T) {
		s := newTestStore(t)
		ctx, claimed := createHumanInterventionAttempt(t, s, "expire")
		now := time.Now().UTC()
		item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "expire", now.Add(-2*time.Minute), now.Add(-time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		if item.Status != models.HumanInterventionExpired {
			t.Fatalf("expected expired intervention, got %+v", item)
		}
		task, _ := s.GetTaskByID(ctx, claimed.ID)
		if task.Status != models.TaskStatusFailed || task.ErrorType.String != "HumanTimeout" {
			t.Fatalf("expiry did not fail closed: %+v", task)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		s := newTestStore(t)
		ctx, claimed := createHumanInterventionAttempt(t, s, "cancel")
		now := time.Now().UTC()
		item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "cancel", now, now.Add(time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CancelTask(ctx, claimed.ID); err != nil {
			t.Fatal(err)
		}
		cancelled, err := s.GetHumanIntervention(ctx, item.ID)
		if err != nil || cancelled.Status != models.HumanInterventionCancelled {
			t.Fatalf("pending intervention was not cancelled: item=%+v err=%v", cancelled, err)
		}
		task, _ := s.GetTaskByID(ctx, claimed.ID)
		if task.Status != models.TaskStatusCancelled {
			t.Fatalf("waiting task was not cancelled: %+v", task)
		}
	})

	t.Run("worker terminal failure", func(t *testing.T) {
		s := newTestStore(t)
		ctx, claimed := createHumanInterventionAttempt(t, s, "worker-failure")
		now := time.Now().UTC()
		item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "worker-failure", now, now.Add(time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateTaskStatus(ctx, claimed.ID, claimed.WorkerID.String,
			string(models.TaskStatusRunning), "bypass", ""); !errors.Is(err, ErrLeaseConflict) {
			t.Fatalf("expected approval bypass to fail, got %v", err)
		}
		if err := s.UpdateTaskStatus(ctx, claimed.ID, claimed.WorkerID.String,
			string(models.TaskStatusFailed), "worker stopped", "WorkerStopped"); err != nil {
			t.Fatal(err)
		}
		cancelled, err := s.GetHumanIntervention(ctx, item.ID)
		if err != nil || cancelled.Status != models.HumanInterventionCancelled {
			t.Fatalf("worker terminal failure left decision pending: item=%+v err=%v", cancelled, err)
		}
	})
}

func TestExpireHumanInterventionsWithoutPolling(t *testing.T) {
	s := newTestStore(t)
	ctx, claimed := createHumanInterventionAttempt(t, s, "background-expire")
	now := time.Now().UTC()
	item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "background-expire", now, now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE human_interventions SET expires_at = ? WHERE id = ?`, now.Add(-time.Minute), item.ID); err != nil {
		t.Fatal(err)
	}
	count, err := s.ExpireHumanInterventions(ctx)
	if err != nil || count != 1 {
		t.Fatalf("background expiry: count=%d err=%v", count, err)
	}
	expired, err := s.GetHumanIntervention(ctx, item.ID)
	if err != nil || expired.Status != models.HumanInterventionExpired {
		t.Fatalf("decision did not expire: item=%+v err=%v", expired, err)
	}
	task, err := s.GetTaskByID(ctx, claimed.ID)
	if err != nil || task.Status != models.TaskStatusFailed || task.ErrorType.String != "HumanTimeout" {
		t.Fatalf("background expiry did not fail task: task=%+v err=%v", task, err)
	}
}

func TestHumanInterventionIsWorkspaceScoped(t *testing.T) {
	s := newTestStore(t)
	ctx, claimed := createHumanInterventionAttempt(t, s, "workspace")
	now := time.Now().UTC()
	item, err := s.CreateHumanIntervention(ctx, humanInterventionInput(claimed, "workspace", now, now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	tenantCtx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "tenant-operator", WorkspaceID: "tenant-b", Roles: []authz.Role{authz.RoleAdmin},
	})
	if _, err := s.GetHumanIntervention(tenantCtx, item.ID); !errors.Is(err, ErrHumanInterventionNotFound) {
		t.Fatalf("cross-workspace intervention was visible: %v", err)
	}
}
