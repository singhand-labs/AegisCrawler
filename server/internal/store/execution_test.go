package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	rulecontract "github.com/singhand-labs/AegisCrawler/internal/rule"
)

var executionInputSchema = models.JSON(`{
	"type":"object",
	"properties":{
		"query":{"type":"string","minLength":2},
		"limit":{"type":"number","default":10,"minimum":1,"maximum":100}
	},
	"required":["query"],
	"additionalProperties":false
}`)

var executionOutputSchema = models.JSON(`{
	"type":"object",
	"properties":{"name":{"type":"string"},"price":{"type":"number"}},
	"required":["name","price"],
	"additionalProperties":false
}`)

func createApprovedExecutionVersion(t *testing.T, s *Store, ctx context.Context, id string) *models.RuleVersion {
	t.Helper()
	rule := workspaceRule(id, time.Now().UTC())
	rule.Version = "1.0.0"
	rule.Output = executionOutputSchema
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	var version *models.RuleVersion
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		version, err = s.CreateRuleVersionWithContractTx(ctx, tx, rule, "", executionInputSchema, executionOutputSchema, "profile-a")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApproveRuleVersion(ctx, id, version.Version)
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func createVersionedExecutionTask(t *testing.T, s *Store, ctx context.Context, ruleID string, version int) *models.Task {
	t.Helper()
	now := time.Now().UTC()
	task := &models.Task{
		ID: NewID(), RuleID: ruleID, RuleVersionNumber: version,
		Variables: models.JSON(`{"query":"books"}`), Status: models.TaskStatusPending,
		Priority: models.PriorityNormal, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func removeRuleVersionContractForTest(t *testing.T, s *Store, ctx context.Context, ruleID string, version int) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER trg_rule_version_contracts_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM rule_version_contracts
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, workspaceID(ctx), ruleID, version); err != nil {
		t.Fatal(err)
	}
}

func setTaskRuleVersionForTest(t *testing.T, s *Store, ctx context.Context, task *models.Task, version int) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER trg_versioned_task_contract_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET rule_version_number = ?
		WHERE workspace_id = ? AND id = ?
	`, version, workspaceID(ctx), task.ID); err != nil {
		t.Fatal(err)
	}
	task.RuleVersionNumber = version
}

func TestVersionedTaskContractResolutionAndClaimFailClosed(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	version := createApprovedExecutionVersion(t, s, ctx, "missing-claim-contract")
	task := createVersionedExecutionTask(t, s, ctx, version.RuleID, version.Version)
	removeRuleVersionContractForTest(t, s, ctx, version.RuleID, version.Version)

	if _, _, err := s.ResolveTaskRuleVersionContract(ctx, task); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("versioned task with a missing contract did not fail closed: %v", err)
	}
	if _, err := s.ClaimTaskForBrowserProfileWithContract(ctx, "worker-a", "profile-a", time.Minute, 5); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("claim with a missing immutable contract did not fail closed: %v", err)
	}
	stored, err := s.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.TaskStatusPending || stored.WorkerID.Valid || stored.LeaseUntil.Valid || stored.CurrentAttemptID.Valid {
		t.Fatalf("failed contract lookup consumed a task lease: %+v", stored)
	}
	var attempts int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM execution_attempts WHERE workspace_id = ? AND task_id = ?
	`, workspaceID(ctx), task.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("failed contract lookup committed %d execution attempts", attempts)
	}
}

func TestVersionedTaskMissingExactVersionAndClaimFailClosed(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	version := createApprovedExecutionVersion(t, s, ctx, "missing-exact-claim-version")
	task := createVersionedExecutionTask(t, s, ctx, version.RuleID, version.Version)
	setTaskRuleVersionForTest(t, s, ctx, task, 999)

	if _, _, err := s.ResolveTaskRuleVersionContract(ctx, task); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("task bound to a nonexistent immutable version was treated as legacy: %v", err)
	}
	if _, err := s.ClaimTaskForBrowserProfileWithContract(ctx, "worker-a", "profile-a", time.Minute, 5); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("claim with a nonexistent immutable version did not fail closed: %v", err)
	}
	stored, err := s.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.TaskStatusPending || stored.WorkerID.Valid || stored.LeaseUntil.Valid || stored.CurrentAttemptID.Valid {
		t.Fatalf("failed version lookup consumed a task lease: %+v", stored)
	}
	var attempts int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM execution_attempts WHERE workspace_id = ? AND task_id = ?
	`, workspaceID(ctx), task.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("failed version lookup committed %d execution attempts", attempts)
	}
}

func TestLegacyTaskContractResolutionAndClaimRemainCompatible(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	now := time.Now().UTC()
	rule := workspaceRule("legacy-contract-resolution", now)
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	task := workspaceTask("legacy-contract-task", rule.ID, now)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	version, contract, err := s.ResolveTaskRuleVersionContract(ctx, stored)
	if err != nil || version != nil || contract != nil {
		t.Fatalf("legacy task unexpectedly required an immutable contract: version=%+v contract=%+v err=%v", version, contract, err)
	}
	claim, err := s.ClaimTaskForBrowserProfileWithContract(ctx, "legacy-worker", "", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Task.ID != task.ID || claim.RuleVersion != nil || claim.Contract != nil {
		t.Fatalf("legacy claim changed its public contract: %+v", claim)
	}
}

func TestTaskBindsApprovedImmutableRuleVersionContract(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	rule := workspaceRule("contract-rule", time.Now().UTC())
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	var pending *models.RuleVersion
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		pending, err = s.CreateRuleVersionWithContractTx(ctx, tx, rule, "", executionInputSchema, executionOutputSchema, "profile-a")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTaskToRuleVersion(ctx, &models.Task{RuleID: rule.ID}, pending.Version); !errors.Is(err, ErrRuleVersionNotApproved) {
		t.Fatalf("pending version should not bind, got %v", err)
	}
	if _, err := s.ApproveRuleVersion(ctx, rule.ID, pending.Version); err != nil {
		t.Fatal(err)
	}

	task := createVersionedExecutionTask(t, s, ctx, rule.ID, pending.Version)
	stored, err := s.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RuleVersionNumber != pending.Version || stored.RuleVersion != "1.0.0" || stored.BrowserProfileID != "profile-a" {
		t.Fatalf("version lineage was not copied to task: %+v", stored)
	}
	if string(stored.Variables) != `{"limit":10,"query":"books"}` {
		t.Fatalf("expected normalized immutable input snapshot, got %s", stored.Variables)
	}
	if string(stored.InputSchema) != string(executionInputSchema) || string(stored.OutputSchema) != string(executionOutputSchema) {
		t.Fatalf("version schemas were not copied to task: %+v", stored)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET variables = '{"query":"changed"}' WHERE id = ?`, task.ID); err == nil {
		t.Fatal("database allowed a versioned task input snapshot to change")
	}
	if _, err := s.db.Exec(`UPDATE rule_version_contracts SET output_schema = '{}' WHERE workspace_id = ? AND rule_id = ? AND version_number = ?`,
		authz.DefaultWorkspaceID, rule.ID, pending.Version); err == nil {
		t.Fatal("database allowed an immutable rule version contract to change")
	}

	badInputs := []models.JSON{models.JSON(`{}`), models.JSON(`{"query":"x"}`), models.JSON(`{"query":"books","extra":true}`)}
	for _, variables := range badInputs {
		candidate := &models.Task{RuleID: rule.ID, RuleVersionNumber: pending.Version, Variables: variables}
		if err := s.BindTaskToRuleVersion(ctx, candidate, pending.Version); !errors.Is(err, rulecontract.ErrInvalidTaskInput) {
			t.Fatalf("invalid input %s should fail binding, got %v", variables, err)
		}
	}

	// The execution contract participates in content identity even when the DSL
	// itself is unchanged.
	changedInput := models.JSON(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)
	var version2 *models.RuleVersion
	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		version2, err = s.CreateRuleVersionWithContractTx(ctx, tx, rule, "", changedInput, executionOutputSchema, "profile-a")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if version2.Version != pending.Version+1 || version2.ContentHash == pending.ContentHash {
		t.Fatalf("changed contract must create distinct immutable version: v1=%+v v2=%+v", pending, version2)
	}
}

func TestExecutionAttemptsAndIdempotentResultBatches(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	version := createApprovedExecutionVersion(t, s, ctx, "results-rule")
	task := createVersionedExecutionTask(t, s, ctx, version.RuleID, version.Version)

	claimed, err := s.ClaimTask(ctx, "worker-a", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed.CurrentAttemptID.Valid || claimed.CurrentAttemptID.String == "" {
		t.Fatalf("claim did not create an attempt: %+v", claimed)
	}
	attemptID := claimed.CurrentAttemptID.String
	attempt, err := s.GetExecutionAttempt(ctx, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Number != 1 || attempt.Status != models.ExecutionAttemptLeased || attempt.WorkerID != "worker-a" {
		t.Fatalf("unexpected first attempt: %+v", attempt)
	}

	batch := &models.Result{
		TaskID: task.ID, WorkerID: "worker-a", AttemptID: attemptID,
		IdempotencyKey: "batch-1", Sequence: 1, Kind: models.ResultKindBatch,
		Payload: models.JSON(`[{"name":"Book","price":10}]`), Valid: true,
	}
	duplicate, err := s.InsertResultIdempotent(ctx, batch)
	if err != nil || duplicate {
		t.Fatalf("first batch insert failed: duplicate=%v err=%v", duplicate, err)
	}
	duplicate, err = s.InsertResultIdempotent(ctx, batch)
	if err != nil || !duplicate {
		t.Fatalf("exact retry was not acknowledged: duplicate=%v err=%v", duplicate, err)
	}
	conflict := *batch
	conflict.Payload = models.JSON(`[{"name":"Changed","price":10}]`)
	if _, err := s.InsertResultIdempotent(ctx, &conflict); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("idempotency key reuse should conflict, got %v", err)
	}
	sequenceConflict := *batch
	sequenceConflict.ID = ""
	sequenceConflict.IdempotencyKey = "different-key"
	if _, err := s.InsertResultIdempotent(ctx, &sequenceConflict); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("valid sequence reuse should conflict, got %v", err)
	}

	invalid := &models.Result{
		TaskID: task.ID, WorkerID: "worker-a", AttemptID: attemptID,
		IdempotencyKey: "invalid-2", Sequence: 2, Kind: models.ResultKindBatch,
		Payload: models.JSON(`{"name":"missing price"}`), Valid: false,
		ValidationError: "missing price",
	}
	if duplicate, err := s.InsertResultIdempotent(ctx, invalid); err != nil || duplicate {
		t.Fatalf("invalid diagnostic was not retained: duplicate=%v err=%v", duplicate, err)
	}
	summary := &models.Result{
		TaskID: task.ID, WorkerID: "worker-a", AttemptID: attemptID,
		IdempotencyKey: "summary-3", Sequence: 3, Kind: models.ResultKindSummary,
		Payload: models.JSON(`{"rowCount":1}`), Valid: true,
	}
	if _, err := s.InsertResultIdempotent(ctx, summary); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListResultPage(ctx, task.ID, 1, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Batches) != 1 || len(page.InvalidBatches) != 1 || page.Summary == nil {
		t.Fatalf("unexpected result page: %+v", page)
	}
	if page.Batches[0].AttemptID != attemptID || page.InvalidBatches[0].ValidationError == "" {
		t.Fatalf("result lineage or diagnostics missing: %+v", page)
	}

	if err := s.UpdateTaskStatus(ctx, task.ID, "worker-a", string(models.TaskStatusDone), "", ""); err != nil {
		t.Fatal(err)
	}
	late := *batch
	late.ID = ""
	late.IdempotencyKey = "late-4"
	late.Sequence = 4
	if _, err := s.InsertResultIdempotent(ctx, &late); !errors.Is(err, ErrExecutionAttemptConflict) {
		t.Fatalf("new data after terminal status should fail, got %v", err)
	}
	if duplicate, err := s.InsertResultIdempotent(ctx, batch); err != nil || !duplicate {
		t.Fatalf("terminal exact retry should remain idempotent: duplicate=%v err=%v", duplicate, err)
	}
}

func TestAttemptBoundDoneRequiresCompleteValidResultsAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	version := createApprovedExecutionVersion(t, s, ctx, "done-gate-rule")
	task := createVersionedExecutionTask(t, s, ctx, version.RuleID, version.Version)
	claimed, err := s.ClaimTask(ctx, "worker-done", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := claimed.CurrentAttemptID.String

	if _, err := s.UpdateTaskStatusForAttempt(ctx, task.ID, "worker-done", attemptID,
		string(models.TaskStatusDone), "done", ""); !errors.Is(err, ErrIncompleteAttemptResults) {
		t.Fatalf("done without results should fail closed, got %v", err)
	}
	batch := &models.Result{
		TaskID: task.ID, WorkerID: "worker-done", AttemptID: attemptID,
		IdempotencyKey: "batch-1", Sequence: 1, Kind: models.ResultKindBatch,
		Payload: models.JSON(`[{"name":"Book","price":10}]`), Valid: true,
	}
	if _, err := s.InsertResultIdempotent(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTaskStatusForAttempt(ctx, task.ID, "worker-done", attemptID,
		string(models.TaskStatusDone), "done", ""); !errors.Is(err, ErrIncompleteAttemptResults) {
		t.Fatalf("done without summary should fail closed, got %v", err)
	}
	summary := &models.Result{
		TaskID: task.ID, WorkerID: "worker-done", AttemptID: attemptID,
		IdempotencyKey: "summary-2", Sequence: 2, Kind: models.ResultKindSummary,
		Payload: models.JSON(`{"status":"success"}`), Valid: true,
	}
	if _, err := s.InsertResultIdempotent(ctx, summary); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.UpdateTaskStatusForAttempt(ctx, task.ID, "worker-done", attemptID,
		string(models.TaskStatusDone), "done", "")
	if err != nil || duplicate {
		t.Fatalf("first complete status failed: duplicate=%v err=%v", duplicate, err)
	}
	duplicate, err = s.UpdateTaskStatusForAttempt(ctx, task.ID, "worker-done", attemptID,
		string(models.TaskStatusDone), "done", "")
	if err != nil || !duplicate {
		t.Fatalf("exact terminal retry was not acknowledged: duplicate=%v err=%v", duplicate, err)
	}
	if _, err := s.UpdateTaskStatusForAttempt(ctx, task.ID, "worker-done", attemptID,
		string(models.TaskStatusFailed), "changed", ""); !errors.Is(err, ErrExecutionAttemptConflict) {
		t.Fatalf("conflicting terminal retry should fail, got %v", err)
	}
}

func TestAttemptBoundDoneRejectsRetainedInvalidBatch(t *testing.T) {
	s := newTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	version := createApprovedExecutionVersion(t, s, ctx, "invalid-done-rule")
	task := createVersionedExecutionTask(t, s, ctx, version.RuleID, version.Version)
	claimed, err := s.ClaimTask(ctx, "worker-invalid", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := claimed.CurrentAttemptID.String
	for _, result := range []*models.Result{
		{TaskID: task.ID, WorkerID: "worker-invalid", AttemptID: attemptID, IdempotencyKey: "batch-1", Sequence: 1, Kind: models.ResultKindBatch, Payload: models.JSON(`[{"name":"Book","price":10}]`), Valid: true},
		{TaskID: task.ID, WorkerID: "worker-invalid", AttemptID: attemptID, IdempotencyKey: "invalid-2", Sequence: 2, Kind: models.ResultKindBatch, Payload: models.JSON(`{"name":"Book"}`), Valid: false, ValidationError: "price required"},
		{TaskID: task.ID, WorkerID: "worker-invalid", AttemptID: attemptID, IdempotencyKey: "summary-3", Sequence: 3, Kind: models.ResultKindSummary, Payload: models.JSON(`{"status":"success"}`), Valid: true},
	} {
		if _, err := s.InsertResultIdempotent(ctx, result); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateTaskStatusForAttempt(ctx, task.ID, "worker-invalid", attemptID,
		string(models.TaskStatusDone), "done", ""); !errors.Is(err, ErrIncompleteAttemptResults) {
		t.Fatalf("done with an invalid batch should fail closed, got %v", err)
	}
}

func TestLeaseRecoveryCreatesNewAttemptAndWorkspaceIsolation(t *testing.T) {
	s := newTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	version := createApprovedExecutionVersion(t, s, ctxA, "recovery-rule")
	task := createVersionedExecutionTask(t, s, ctxA, version.RuleID, version.Version)
	first, err := s.ClaimTask(ctxA, "worker-a", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	firstAttemptID := first.CurrentAttemptID.String
	if err := s.ReleaseTask(ctxA, task.ID); err != nil {
		t.Fatal(err)
	}
	firstAttempt, err := s.GetExecutionAttempt(ctxA, firstAttemptID)
	if err != nil || firstAttempt.Status != models.ExecutionAttemptLeaseExpired || firstAttempt.CompletedAt == nil {
		t.Fatalf("expired attempt not preserved: attempt=%+v err=%v", firstAttempt, err)
	}
	second, err := s.ClaimTask(ctxA, "worker-b", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	secondAttempt, err := s.GetExecutionAttempt(ctxA, second.CurrentAttemptID.String)
	if err != nil || secondAttempt.Number != 2 || secondAttempt.ID == firstAttemptID {
		t.Fatalf("requeue did not create a distinct second attempt: %+v err=%v", secondAttempt, err)
	}
	if err := s.CancelTask(ctxA, task.ID); err != nil {
		t.Fatal(err)
	}
	secondAttempt, err = s.GetExecutionAttempt(ctxA, secondAttempt.ID)
	if err != nil || secondAttempt.Status != models.ExecutionAttemptCancelled || secondAttempt.CompletedAt == nil {
		t.Fatalf("leased cancellation did not close the attempt: %+v err=%v", secondAttempt, err)
	}

	if _, err := s.GetExecutionAttempt(ctxB, firstAttemptID); !errors.Is(err, ErrExecutionAttemptNotFound) {
		t.Fatalf("cross-workspace attempt lookup leaked data: %v", err)
	}
	if _, err := s.ListResultPage(ctxB, task.ID, 10, 0, true); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-workspace results lookup leaked task: %v", err)
	}
	if _, err := s.GetRuleVersionContract(ctxB, version.RuleID, version.Version); !errors.Is(err, ErrRuleVersionNotFound) {
		t.Fatalf("cross-workspace contract lookup leaked data: %v", err)
	}
}

func TestMigration016BackfillsLegacyExecutionLineage(t *testing.T) {
	f, err := os.CreateTemp("", "aegis-execution-migration-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	apply := func(from, through int) {
		t.Helper()
		for _, migration := range migrations {
			if migration.version < from || migration.version > through {
				continue
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := migration.up(tx); err != nil {
				_ = tx.Rollback()
				t.Fatalf("apply migration %d: %v", migration.version, err)
			}
			if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, migration.version); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	apply(1, 12)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`INSERT INTO rules (
		id, workspace_id, version, name, domain, enabled, priority, variables,
		steps, output, approval_status, source, owner, created_at, updated_at
	) VALUES (?, 'default', '1.0.0', 'Legacy', ?, 1, 'normal', ?, ?, ?, 'approved', 'pageagent', 'legacy', ?, ?)`,
		"legacy-execution-rule", models.JSON(`"example.com"`), models.JSON(`{"query":"books"}`),
		models.JSON(`["collect"]`), executionOutputSchema, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (
		id, workspace_id, rule_id, rule_version, status, priority, variables,
		retry_count, max_retries, output_schema, created_at, updated_at, cancel_requested
	) VALUES ('legacy-task', 'default', 'legacy-execution-rule', '1.0.0', 'pending',
		'normal', ?, 0, 3, ?, ?, ?, 0)`, models.JSON(`{"query":"books"}`), executionOutputSchema, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO results (
		id, workspace_id, task_id, worker_id, payload, immediate, created_at
	) VALUES ('legacy-result', 'default', 'legacy-task', 'legacy-worker', ?, 0, ?)`,
		models.JSON(`{"name":"Book","price":10}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schedules (
		id, workspace_id, rule_id, rule_version, name, type, expression, enabled,
		variables, priority, max_retries, catchup, created_at, updated_at
	) VALUES ('legacy-schedule', 'default', 'legacy-execution-rule', '1.0.0', 'Legacy schedule',
		'cron', '0 9 * * *', 1, ?, 'normal', 3, 'skip', ?, ?)`,
		models.JSON(`{"query":"books"}`), now, now); err != nil {
		t.Fatal(err)
	}
	apply(13, 15)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := New(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	task, err := s.GetTaskByID(ctx, "legacy-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.RuleVersionNumber != 1 || task.CurrentAttemptID.String != "legacy-legacy-task" || len(task.InputSchema) == 0 {
		t.Fatalf("legacy task lineage was not backfilled: %+v", task)
	}
	attempt, err := s.GetExecutionAttempt(ctx, task.CurrentAttemptID.String)
	if err != nil || attempt.Number != 1 || attempt.Status != models.ExecutionAttemptLeaseExpired {
		t.Fatalf("legacy attempt was not preserved: attempt=%+v err=%v", attempt, err)
	}
	page, err := s.ListResultPage(ctx, task.ID, 10, 0, false)
	if err != nil || page.Total != 1 || len(page.Batches) != 1 || page.Batches[0].IdempotencyKey != "legacy-legacy-result" {
		t.Fatalf("legacy result lineage was not backfilled: page=%+v err=%v", page, err)
	}
	schedule, err := s.GetScheduleByID(ctx, "legacy-schedule")
	if err != nil || schedule.RuleVersionNumber != 1 || schedule.Timezone != "UTC" || len(schedule.InputSchema) == 0 {
		t.Fatalf("legacy schedule contract was not backfilled: schedule=%+v err=%v", schedule, err)
	}
}

func TestLegacyResultSubmissionUsesActiveOrSyntheticAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rule := workspaceRule("legacy-result-rule", now)
	if err := s.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	directTask := workspaceTask("legacy-direct-task", rule.ID, now)
	if err := s.CreateTask(ctx, directTask); err != nil {
		t.Fatal(err)
	}
	direct := &models.Result{ID: "direct-result", TaskID: directTask.ID, WorkerID: "legacy-worker", Payload: models.JSON(`{"value":1}`), CreatedAt: now}
	if err := s.InsertResult(ctx, direct); err != nil {
		t.Fatal(err)
	}
	if direct.AttemptID != "legacy-direct-"+directTask.ID {
		t.Fatalf("direct legacy result did not receive synthetic attempt: %+v", direct)
	}
	if page, err := s.ListResultPage(ctx, directTask.ID, 10, 0, false); err != nil || page.Total != 1 || len(page.Batches) != 1 {
		t.Fatalf("synthetic-attempt result missing from versioned view: page=%+v err=%v", page, err)
	}

	claimedTask := workspaceTask("legacy-claimed-task", rule.ID, now.Add(time.Second))
	if err := s.CreateTask(ctx, claimedTask); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimTask(ctx, "legacy-worker", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	claimedResult := &models.Result{ID: "claimed-result", TaskID: claimed.ID, WorkerID: "legacy-worker", Payload: models.JSON(`{"value":2}`), CreatedAt: now}
	if err := s.InsertResult(ctx, claimedResult); err != nil {
		t.Fatal(err)
	}
	if claimedResult.AttemptID != claimed.CurrentAttemptID.String {
		t.Fatalf("legacy result did not use active attempt: result=%+v task=%+v", claimedResult, claimed)
	}
}
