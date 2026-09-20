package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/prompt"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func createConfirmedDSLRequirement(t *testing.T, s *Store, ctx context.Context, recordingID, requirementID string) *models.CollectionRequirement {
	t.Helper()
	createRequirementTestRecording(t, s, ctx, recordingID)
	requirement := &models.CollectionRequirement{
		ID: requirementID, RecordingID: recordingID, Source: models.RequirementSourceManual,
		Requirement: requirementSpec(), Owner: "spoofed",
	}
	if err := s.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.ConfirmCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	return confirmed
}

func createClaimedDSLWorkflow(t *testing.T, s *Store, ctx context.Context, suffix string) (*models.DSLWorkflow, *models.DSLJob) {
	t.Helper()
	requirement := createConfirmedDSLRequirement(t, s, ctx, "dsl-recording-"+suffix, "dsl-requirement-"+suffix)
	workflow := &models.DSLWorkflow{
		ID: "dsl-workflow-" + suffix, RequirementID: requirement.ID,
		RecordingID: requirement.RecordingID, BrowserProfileID: "current-chrome-profile",
	}
	job, err := s.CreateDSLWorkflow(ctx, workflow, map[string]any{
		"baselineRule": map[string]any{"id": "dsl-rule-" + suffix},
		"marker":       "dsl-request-plaintext-marker-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID || claimed.AttemptCount != 1 {
		t.Fatalf("unexpected claimed dsl job: %+v", claimed)
	}
	if claimed.PromptVersion != prompt.DSLWorkflowVersion {
		t.Fatalf("unexpected dsl prompt version: %q", claimed.PromptVersion)
	}
	return workflow, claimed
}

func TestCreateAdminReviewedDSLWorkflowHasNoProviderJobAndImmutableSource(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	requirement := createConfirmedDSLRequirement(t, s, ctx, "dsl-recording-import", "dsl-requirement-import")
	rule := workspaceRule("dsl-rule-import", time.Now().UTC())
	rule.Version = "1"
	workflow := &models.DSLWorkflow{
		ID: "dsl-workflow-import", RequirementID: requirement.ID,
		RecordingID: requirement.RecordingID, BrowserProfileID: "reviewed-profile",
		SourceArtifactHash: strings.Repeat("a", 64), SourceExportHash: strings.Repeat("b", 64),
	}
	persistedID, err := s.CreateAdminReviewedDSLWorkflow(ctx, workflow, rule, "id: dsl-rule-import\n")
	if err != nil {
		t.Fatal(err)
	}
	if persistedID != workflow.ID {
		t.Fatalf("unexpected persisted workflow id: %q", persistedID)
	}
	stored, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.DSLWorkflowAwaitingReplay || stored.MaxRepairs != 0 ||
		stored.CurrentJobID != "" || stored.ProvisionalRule == nil ||
		stored.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		stored.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		stored.SourceArtifactHash != strings.Repeat("a", 64) || stored.SourceExportHash != strings.Repeat("b", 64) {
		t.Fatalf("unexpected imported workflow: %+v", stored)
	}
	var jobs int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, workflow.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("imported workflow created %d provider jobs", jobs)
	}
	logs, total, err := s.ListAuditLogs(ctx, ListAuditLogsFilter{
		Action: "dsl_workflow_adopted_from_attempt_export", ResourceID: workflow.ID,
	})
	if err != nil || total != 1 || len(logs) != 1 {
		t.Fatalf("imported workflow was not atomically audited: total=%d logs=%+v err=%v", total, logs, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE dsl_workflows SET source_authority = 'qualification' WHERE id = ?`, workflow.ID); err == nil {
		t.Fatal("imported workflow source authority was mutable")
	}
}

func TestCreateAdminReviewedDSLWorkflowIsIdempotentByReviewedExport(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	requirement := createConfirmedDSLRequirement(t, s, ctx, "dsl-recording-import-retry", "dsl-requirement-import-retry")
	rule := workspaceRule("dsl-rule-import-retry", time.Now().UTC())
	rule.Version = "1"
	first := &models.DSLWorkflow{
		ID: "dsl-workflow-import-first", RequirementID: requirement.ID,
		RecordingID: requirement.RecordingID, BrowserProfileID: "reviewed-profile",
		SourceArtifactHash: strings.Repeat("a", 64), SourceExportHash: strings.Repeat("b", 64),
	}
	firstID, err := s.CreateAdminReviewedDSLWorkflow(ctx, first, rule, "id: dsl-rule-import-retry\n")
	if err != nil {
		t.Fatal(err)
	}
	retry := &models.DSLWorkflow{
		ID: "dsl-workflow-import-retry", RequirementID: requirement.ID,
		RecordingID: requirement.RecordingID, BrowserProfileID: "reviewed-profile",
		SourceArtifactHash: first.SourceArtifactHash, SourceExportHash: first.SourceExportHash,
	}
	retryID, err := s.CreateAdminReviewedDSLWorkflow(ctx, retry, rule, "id: dsl-rule-import-retry\n")
	if err != nil {
		t.Fatal(err)
	}
	if retryID != firstID {
		t.Fatalf("retry created a second workflow: first=%q retry=%q", firstID, retryID)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE dsl_workflows SET status = 'failed' WHERE id = ?
	`, firstID); err != nil {
		t.Fatal(err)
	}
	replacementID, err := s.CreateAdminReviewedDSLWorkflow(ctx, retry, rule, "id: dsl-rule-import-retry\n")
	if err != nil {
		t.Fatal(err)
	}
	if replacementID != retry.ID {
		t.Fatalf("failed workflow blocked a fresh adoption: failed=%q replacement=%q", firstID, replacementID)
	}
	third := &models.DSLWorkflow{
		ID: "dsl-workflow-import-third", RequirementID: requirement.ID,
		RecordingID: requirement.RecordingID, BrowserProfileID: "reviewed-profile",
		SourceArtifactHash: first.SourceArtifactHash, SourceExportHash: first.SourceExportHash,
	}
	thirdID, err := s.CreateAdminReviewedDSLWorkflow(ctx, third, rule, "id: dsl-rule-import-retry\n")
	if err != nil {
		t.Fatal(err)
	}
	if thirdID != replacementID {
		t.Fatalf("retry selected stale failed workflow: replacement=%q retry=%q", replacementID, thirdID)
	}
	var workflows, jobs, audits int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dsl_workflows
		WHERE requirement_id = ? AND browser_profile_id = ? AND source_export_hash = ?
	`, requirement.ID, first.BrowserProfileID, first.SourceExportHash).Scan(&workflows); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id IN (?, ?)
	`, first.ID, retry.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_logs
		WHERE action = 'dsl_workflow_adopted_from_attempt_export'
		  AND resource_id IN (?, ?)
	`, first.ID, retry.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if workflows != 2 || jobs != 0 || audits != 2 {
		t.Fatalf("unexpected retry persistence: workflows=%d jobs=%d audits=%d", workflows, jobs, audits)
	}
}

func completeDSLGeneration(t *testing.T, s *Store, ctx context.Context, workflow *models.DSLWorkflow, job *models.DSLJob, suffix string) *models.Rule {
	t.Helper()
	rule := workspaceRule("dsl-rule-"+suffix, time.Now().UTC())
	rule.Name = "Collect product prices"
	rule.Version = "1"
	rule.Source = "pageagent-workflow"
	job.Provider = "fake"
	job.Model = "fake-dsl-model"
	job.ChunkCount = 2
	job.InputTokens = 101
	job.OutputTokens = 37
	job.SafetyFlags = []string{"schema-validation:passed", "security-scan:passed"}
	if err := s.CompleteDSLJob(ctx, job, rule, "id: "+rule.ID+"\n", map[string]any{"marker": "dsl-result-plaintext-marker-" + suffix}); err != nil {
		t.Fatal(err)
	}
	return rule
}

func createApprovalReadyDSLWorkflow(t *testing.T, s *Store, ctx context.Context, suffix string) (*models.DSLWorkflow, *models.Rule) {
	t.Helper()
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, suffix)
	rule := completeDSLGeneration(t, s, ctx, workflow, job, suffix)
	attempt, err := s.StartDSLReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repair, err := s.CompleteDSLReplay(
		ctx, attempt, true, true, nil,
		map[string]any{"name": "Example", "price": 10.5}, nil, "", "", nil,
	); err != nil || repair != nil {
		t.Fatalf("complete approval-ready replay: repair=%+v err=%v", repair, err)
	}
	return workflow, rule
}

func TestApproveDSLWorkflowRollsBackWhenApprovalAuditFails(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, rule := createApprovalReadyDSLWorkflow(t, s, ctx, "approval-audit-rollback")

	if _, err := s.db.Exec(`
		CREATE TRIGGER fail_dsl_workflow_approval_audit
		BEFORE INSERT ON audit_logs
		WHEN NEW.action = 'dsl_workflow_approved'
		BEGIN SELECT RAISE(ABORT, 'forced approval audit failure'); END
	`); err != nil {
		t.Fatal(err)
	}
	if version, contract, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{}); err == nil || version != nil || contract != nil {
		t.Fatalf("approval audit failure committed or returned approval: version=%+v contract=%+v err=%v",
			version, contract, err)
	}

	stored, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.DSLWorkflowAwaitingConfirmation ||
		stored.ApprovedRuleID != "" || stored.ApprovedVersion != 0 || stored.ApprovedAt != nil {
		t.Fatalf("approval audit failure leaked workflow approval: %+v", stored)
	}
	for _, check := range []struct {
		table, column, value string
	}{
		{table: "rules", column: "id", value: rule.ID},
		{table: "rule_versions", column: "rule_id", value: rule.ID},
		{table: "rule_version_contracts", column: "rule_id", value: rule.ID},
		{table: "dsl_approvals", column: "workflow_id", value: workflow.ID},
	} {
		var count int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM `+check.table+` WHERE `+check.column+` = ?`, check.value,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("approval audit failure leaked %d rows into %s", count, check.table)
		}
	}
	logs, total, err := s.ListAuditLogs(ctx, ListAuditLogsFilter{
		Action: "dsl_workflow_approved", ResourceType: "dsl_workflow", ResourceID: workflow.ID,
	})
	if err != nil || total != 0 || len(logs) != 0 {
		t.Fatalf("approval audit failure leaked its audit: total=%d logs=%+v err=%v", total, logs, err)
	}

	if _, err := s.db.Exec(`DROP TRIGGER fail_dsl_workflow_approval_audit`); err != nil {
		t.Fatal(err)
	}
	version, contract, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if version == nil || contract == nil || contract.RuleID != version.RuleID ||
		contract.Version != version.Version || contract.SourceWorkflowID != "" {
		t.Fatalf("retry did not return the committed immutable contract: version=%+v contract=%+v",
			version, contract)
	}
	logs, total, err = s.ListAuditLogs(ctx, ListAuditLogsFilter{
		Action: "dsl_workflow_approved", ResourceType: "dsl_workflow", ResourceID: workflow.ID,
	})
	if err != nil || total != 1 || len(logs) != 1 || logs[0].Actor != "alice" ||
		!bytes.Contains(logs[0].Payload, []byte(version.ContentHash)) {
		t.Fatalf("approval retry did not commit exact atomic audit: total=%d logs=%+v err=%v",
			total, logs, err)
	}
}

func TestDSLJobAndAttemptReportCommitAtomicallyAndFenceStaleLease(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, first := createClaimedDSLWorkflow(t, s, ctx, "attempt-atomic")
	call, callArtifact := providerCallForAttempt(models.LLMJobTypeDSL, first.ID, first.AttemptCount, 1, "dsl-call")
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_jobs SET lease_until = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.AttemptCount != 2 {
		t.Fatalf("unexpected reclaimed job: %+v", second)
	}
	if err := s.UpdateDSLJobAttemptProgress(ctx, first.ID, first.AttemptCount, 9, 8); !errors.Is(err, ErrDSLJobState) {
		t.Fatalf("stale progress update should be fenced, got %v", err)
	}
	lateCall, lateArtifact := providerCallForAttempt(models.LLMJobTypeDSL, first.ID, first.AttemptCount, 2, "late-dsl-call")
	if err := s.CreateLLMProviderCall(ctx, lateCall, lateArtifact); err == nil {
		t.Fatal("migration 23 allowed a reclaimed DSL lease to append to the stale attempt")
	}

	rule := workspaceRule("dsl-rule-attempt-atomic", time.Now().UTC())
	rule.Version = "1"
	report, reportArtifact := attemptReportForJob(models.LLMJobTypeDSL, first.ID, first.AttemptCount)
	if err := s.CompleteDSLJobWithAttemptReport(ctx, first, rule, "id: stale\n", map[string]any{"stale": true}, report, reportArtifact); !errors.Is(err, ErrDSLJobState) {
		t.Fatalf("stale completion should be fenced, got %v", err)
	}
	reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeDSL, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Fatalf("stale transaction leaked attempt report: %+v", reports)
	}
	current, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ProvisionalRule != nil || current.Status != models.DSLWorkflowGenerating {
		t.Fatalf("stale worker published provisional: %+v", current)
	}

	secondCall, secondCallArtifact := providerCallForAttempt(models.LLMJobTypeDSL, second.ID, second.AttemptCount, 1, "dsl-call-2")
	if err := s.CreateLLMProviderCall(ctx, secondCall, secondCallArtifact); err != nil {
		t.Fatal(err)
	}
	secondReport, secondReportArtifact := attemptReportForJob(models.LLMJobTypeDSL, second.ID, second.AttemptCount)
	if err := s.CompleteDSLJobWithAttemptReport(ctx, second, rule, "id: current\n", map[string]any{"ok": true}, secondReport, secondReportArtifact); err != nil {
		t.Fatal(err)
	}
	reports, err = s.ListLLMAttemptReports(ctx, models.LLMJobTypeDSL, first.ID)
	if err != nil || len(reports) != 1 || reports[0].AttemptNumber != 2 {
		t.Fatalf("current attempt was not committed atomically: reports=%+v err=%v", reports, err)
	}
}

func TestExpiredFinalDSLAttemptFailsJobAndWorkflowAtomically(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	requirement := createConfirmedDSLRequirement(
		t, s, ctx, "dsl-recording-final-lease", "dsl-requirement-final-lease",
	)
	workflow := &models.DSLWorkflow{
		ID: "dsl-workflow-final-lease", RequirementID: requirement.ID,
		RecordingID: requirement.RecordingID, BrowserProfileID: "current-chrome-profile",
	}
	job, err := s.CreateDSLWorkflow(
		ctx, workflow, map[string]any{"baselineRule": map[string]any{"id": "rule"}},
		DSLWorkflowOptions{JobMaxAttempts: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil || claimed.ID != job.ID || claimed.AttemptCount != 1 {
		t.Fatalf("claim final attempt: job=%+v err=%v", claimed, err)
	}
	if _, err := s.db.Exec(
		`UPDATE dsl_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second), claimed.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPendingDSLJob(context.Background(), time.Minute); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expired exhausted job was reclaimed: %v", err)
	}
	failedJob, err := s.GetDSLJob(ctx, job.ID)
	if err != nil || failedJob.Status != models.DSLJobFailed ||
		failedJob.ErrorCode != "JOB_LEASE_EXPIRED" {
		t.Fatalf("final expired attempt did not fail closed: job=%+v err=%v", failedJob, err)
	}
	failedWorkflow, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil || failedWorkflow.Status != models.DSLWorkflowFailed ||
		failedWorkflow.ErrorCode != "JOB_LEASE_EXPIRED" {
		t.Fatalf("workflow did not fail with final expired attempt: workflow=%+v err=%v",
			failedWorkflow, err)
	}
}

func TestSelectorRepairScheduleIsAtomicUniqueAndDispatchFailsClosed(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, source := createClaimedDSLWorkflow(t, s, ctx, "selector-repair")
	call, callArtifact := providerCallForAttempt(
		models.LLMJobTypeDSL, source.ID, source.AttemptCount, 1, "selector-source",
	)
	call.Phase = "final"
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	report, reportArtifact := attemptReportForJob(
		models.LLMJobTypeDSL, source.ID, source.AttemptCount,
	)
	report.ID = "selector-source-report"
	report.Outcome = models.LLMAttemptFailed
	report.ValidationPhase = "selector-candidate-resolution"
	report.ErrorCode = "INVALID_DSL"
	report.ErrorMessage = "wrong selector cohort"
	request := map[string]any{
		"sourceAttemptReportId": report.ID,
		"sourceProviderIrHash":  artifactHash([]byte("provider-ir")),
		"repairPlanHash":        artifactHash([]byte("repair-plan")),
		"marker":                "sealed-selector-repair-request",
	}
	child, err := s.ScheduleDSLSelectorRepairWithAttemptReport(
		ctx, source, report, reportArtifact, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if child.Kind != models.DSLJobSelectorRepair || child.MaxAttempts != 1 ||
		child.SourceAttemptReportID != report.ID {
		t.Fatalf("unexpected selector repair child: %+v", child)
	}
	currentSource, err := s.GetDSLJob(ctx, source.ID)
	if err != nil || currentSource.Status != models.DSLJobFailed {
		t.Fatalf("source attempt was not terminalized: job=%+v err=%v", currentSource, err)
	}
	currentWorkflow, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil || currentWorkflow.Status != models.DSLWorkflowRepairing ||
		currentWorkflow.CurrentJobID != child.ID || currentWorkflow.RepairCount != 0 {
		t.Fatalf("workflow did not atomically install selector repair: workflow=%+v err=%v", currentWorkflow, err)
	}
	reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeDSL, source.ID)
	if err != nil || len(reports) != 1 || reports[0].ID != report.ID {
		t.Fatalf("source report was not atomically inserted: reports=%+v err=%v", reports, err)
	}
	if _, err := s.db.Exec(`DELETE FROM llm_attempt_reports WHERE id = ?`, report.ID); err == nil {
		t.Fatal("selector repair source report deletion orphaned its child")
	}
	claimed, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != child.ID || claimed.SourceAttemptReportID != report.ID {
		t.Fatalf("wrong child claimed: %+v", claimed)
	}
	requestMap, _ := claimed.Request.(map[string]any)
	if requestMap["sourceAttemptReportId"] != report.ID ||
		requestMap["marker"] != "sealed-selector-repair-request" {
		t.Fatalf("sealed child request lost source lineage: %#v", claimed.Request)
	}
	if _, err := s.db.Exec(`UPDATE dsl_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second), claimed.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
	if err != nil || reclaimed.ID != claimed.ID ||
		reclaimed.AttemptCount != claimed.AttemptCount {
		t.Fatalf("pre-dispatch selector repair did not safely reclaim the same attempt: job=%+v err=%v", reclaimed, err)
	}
	if err := s.MarkDSLSelectorRepairDispatched(ctx, reclaimed); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDSLSelectorRepairDispatched(ctx, reclaimed); !errors.Is(err, ErrDSLJobState) {
		t.Fatalf("duplicate dispatch marker was accepted: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_jobs SET provider_dispatched = 0 WHERE id = ?`, reclaimed.ID); err == nil ||
		!strings.Contains(err.Error(), "monotonic") {
		t.Fatalf("dispatch marker could be reset: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second), reclaimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPendingDSLJob(context.Background(), time.Minute); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("ambiguous dispatched lease was redispatched: %v", err)
	}
	failedChild, err := s.GetDSLJob(ctx, child.ID)
	if err != nil || failedChild.Status != models.DSLJobFailed ||
		failedChild.ErrorCode != "SELECTOR_REPAIR_DISPATCH_AMBIGUOUS" {
		t.Fatalf("ambiguous child was not failed closed: child=%+v err=%v", failedChild, err)
	}
	failedWorkflow, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil || failedWorkflow.Status != models.DSLWorkflowFailed {
		t.Fatalf("ambiguous workflow remains runnable: workflow=%+v err=%v", failedWorkflow, err)
	}
}

func TestSelectorRepairLineageIsPurgedWithRecording(t *testing.T) {
	tests := []struct {
		name   string
		expire bool
	}{
		{name: "explicit deletion"},
		{name: "expiry deletion", expire: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newEncryptedTestStore(t)
			ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
			workflow, _, child, report := createPendingSelectorRepairChild(
				t, s, ctx, "purge-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if test.expire {
				now := time.Now().UTC()
				if _, err := s.db.Exec(
					`UPDATE recordings SET expires_at = ? WHERE id = ?`,
					now.Add(-time.Minute), workflow.RecordingID,
				); err != nil {
					t.Fatal(err)
				}
				if deleted, err := s.DeleteExpiredRecordings(ctx, now, 10); err != nil || deleted != 1 {
					t.Fatalf("expire selector lineage: deleted=%d err=%v", deleted, err)
				}
			} else if err := s.DeleteRecording(ctx, workflow.RecordingID); err != nil {
				t.Fatal(err)
			}
			for table, id := range map[string]string{
				"dsl_jobs": child.ID, "dsl_workflows": workflow.ID,
				"llm_attempt_reports": report.ID,
			} {
				var count int
				if err := s.db.QueryRow(
					`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id,
				).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("recording cleanup retained %s row %s", table, id)
				}
			}
		})
	}
}

func createPendingSelectorRepairChild(
	t *testing.T,
	s *Store,
	ctx context.Context,
	suffix string,
) (*models.DSLWorkflow, *models.DSLJob, *models.DSLJob, *models.LLMAttemptReport) {
	t.Helper()
	workflow, source := createClaimedDSLWorkflow(t, s, ctx, suffix)
	call, callArtifact := providerCallForAttempt(
		models.LLMJobTypeDSL, source.ID, source.AttemptCount, 1, "selector-source-"+suffix,
	)
	call.Phase = "final"
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	report, reportArtifact := attemptReportForJob(
		models.LLMJobTypeDSL, source.ID, source.AttemptCount,
	)
	report.ID = "selector-source-report-" + suffix
	report.Outcome = models.LLMAttemptFailed
	report.ValidationPhase = "selector-candidate-resolution"
	report.ErrorCode = "INVALID_DSL"
	report.ErrorMessage = "wrong selector cohort"
	child, err := s.ScheduleDSLSelectorRepairWithAttemptReport(
		ctx, source, report, reportArtifact, map[string]any{
			"sourceAttemptReportId": report.ID,
			"sourceProviderIrHash":  artifactHash([]byte("provider-ir-" + suffix)),
			"repairPlanHash":        artifactHash([]byte("repair-plan-" + suffix)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return workflow, source, child, report
}

func TestGetDSLWorkflowGenerationRequestIsEncryptedAndWorkspaceScoped(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createClaimedDSLWorkflow(t, s, ctx, "baseline-recovery")
	request, err := s.GetDSLWorkflowGenerationRequest(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(request)
	if !bytes.Contains(encoded, []byte("dsl-request-plaintext-marker-baseline-recovery")) {
		t.Fatalf("trusted request was not decrypted in authorized memory: %s", encoded)
	}
	foreign := workspaceContext("mallory", "tenant-b")
	if _, err := s.GetDSLWorkflowGenerationRequest(foreign, workflow.ID); !errors.Is(err, ErrDSLJobNotFound) {
		t.Fatalf("cross-workspace generation request leaked: %v", err)
	}
}

func TestDSLAttemptReportCaptureFailureLeavesJobAndWorkflowRunning(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, claimed := createClaimedDSLWorkflow(t, s, ctx, "capture-rollback")
	call, callArtifact := providerCallForAttempt(models.LLMJobTypeDSL, claimed.ID, claimed.AttemptCount, 1, "dsl-call")
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	report, _ := attemptReportForJob(models.LLMJobTypeDSL, claimed.ID, claimed.AttemptCount)
	rule := workspaceRule("dsl-rule-capture-rollback", time.Now().UTC())
	rule.Version = "1"
	oversized := map[string]any{"content": strings.Repeat("x", MaxLLMAttemptReportArtifactBytes+1)}
	if err := s.CompleteDSLJobWithAttemptReport(ctx, claimed, rule, "id: never\n", map[string]any{"ok": true}, report, oversized); !errors.Is(err, ErrLLMArtifactTooLarge) {
		t.Fatalf("expected fail-closed capture error, got %v", err)
	}
	storedJob, err := s.GetDSLJob(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedWorkflow, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeDSL, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedJob.Status != models.DSLJobRunning || storedWorkflow.Status != models.DSLWorkflowGenerating ||
		storedWorkflow.ProvisionalRule != nil || len(reports) != 0 {
		t.Fatalf("capture failure split report/state: job=%+v workflow=%+v reports=%+v", storedJob, storedWorkflow, reports)
	}
}

func TestApproveDSLWorkflowDeduplicatesOrdinaryContractWithoutWorkflowIdentity(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	firstWorkflow, sharedRule := createApprovalReadyDSLWorkflow(t, s, ctx, "ordinary-dedup-first")
	firstVersion, firstContract, err := s.ApproveDSLWorkflow(ctx, firstWorkflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if firstContract.SourceWorkflowID != "" {
		t.Fatalf("ordinary workflow polluted immutable contract identity: %+v", firstContract)
	}

	secondWorkflow, secondJob := createClaimedDSLWorkflow(t, s, ctx, "ordinary-dedup-second")
	secondJob.Provider = "fake"
	secondJob.Model = "fake-dsl-model"
	secondJob.ChunkCount = 2
	secondJob.InputTokens = 101
	secondJob.OutputTokens = 37
	secondJob.SafetyFlags = []string{"schema-validation:passed", "security-scan:passed"}
	if err := s.CompleteDSLJob(
		ctx, secondJob, sharedRule, "id: "+sharedRule.ID+"\n",
		map[string]any{"marker": "same-ordinary-rule"},
	); err != nil {
		t.Fatal(err)
	}
	replay, err := s.StartDSLReplay(ctx, secondWorkflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repair, err := s.CompleteDSLReplay(
		ctx, replay, true, true, nil,
		map[string]any{"name": "Example", "price": 10.5}, nil, "", "", nil,
	); err != nil || repair != nil {
		t.Fatalf("complete second replay: repair=%+v err=%v", repair, err)
	}
	secondVersion, secondContract, err := s.ApproveDSLWorkflow(ctx, secondWorkflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secondVersion.Version != firstVersion.Version ||
		secondVersion.ContentHash != firstVersion.ContentHash ||
		secondContract.SourceWorkflowID != "" {
		t.Fatalf("identical ordinary contract did not deduplicate: first=%+v/%+v second=%+v/%+v",
			firstVersion, firstContract, secondVersion, secondContract)
	}
	var approvalCount int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM dsl_approvals
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, authz.DefaultWorkspaceID, firstVersion.RuleID, firstVersion.Version).Scan(&approvalCount); err != nil {
		t.Fatal(err)
	}
	if approvalCount != 2 {
		t.Fatalf("expected both workflow approvals to retain lineage, got %d", approvalCount)
	}
}

func TestDSLWorkflowArtifactsAreEncryptedScopedReplayedAndApproved(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	workflow, claimed := createClaimedDSLWorkflow(t, s, ctxA, "happy")

	var requestArtifact []byte
	if err := s.db.QueryRow(`SELECT request_artifact FROM dsl_jobs WHERE id = ?`, claimed.ID).Scan(&requestArtifact); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(requestArtifact, []byte("dsl-request-plaintext-marker")) {
		t.Fatal("dsl request was stored in plaintext")
	}
	request, ok := claimed.Request.(map[string]any)
	if !ok || request["marker"] != "dsl-request-plaintext-marker-happy" {
		t.Fatalf("claimed request was not decrypted: %#v", claimed.Request)
	}
	if _, err := s.GetDSLWorkflow(ctxB, workflow.ID); !errors.Is(err, ErrDSLWorkflowNotFound) {
		t.Fatalf("cross-workspace workflow lookup should be hidden, got %v", err)
	}
	if _, err := s.GetDSLJob(ctxB, claimed.ID); !errors.Is(err, ErrDSLJobNotFound) {
		t.Fatalf("cross-workspace dsl job lookup should be hidden, got %v", err)
	}

	rule := completeDSLGeneration(t, s, ctxA, workflow, claimed, "happy")
	var provisionalArtifact, resultArtifact []byte
	if err := s.db.QueryRow(`SELECT provisional_artifact FROM dsl_workflows WHERE id = ?`, workflow.ID).Scan(&provisionalArtifact); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT result_artifact FROM dsl_jobs WHERE id = ?`, claimed.ID).Scan(&resultArtifact); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(provisionalArtifact, []byte(rule.Name)) || bytes.Contains(resultArtifact, []byte("dsl-result-plaintext-marker")) {
		t.Fatal("generated dsl artifacts were stored in plaintext")
	}
	generated, err := s.GetDSLWorkflow(ctxA, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Status != models.DSLWorkflowAwaitingReplay || generated.ProvisionalRule == nil || generated.ProvisionalRule.ID != rule.ID || generated.ProvisionalHash == "" {
		t.Fatalf("unexpected generated workflow: %+v", generated)
	}
	completedJob, err := s.GetDSLJob(ctxA, claimed.ID)
	if err != nil || completedJob.ResultHash == "" || completedJob.Provider != "fake" {
		t.Fatalf("unexpected completed dsl job: job=%+v err=%v", completedJob, err)
	}

	attempt, err := s.StartDSLReplay(ctxA, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := map[string]any{"logs": []any{"safe replay log"}, "marker": "diagnostic-plaintext-marker"}
	output := map[string]any{"name": "Example", "price": 10.5, "marker": "output-plaintext-marker"}
	artifacts := []models.ReplayArtifact{{Name: "final", Type: "screenshot", Data: "artifact-plaintext-marker"}}
	if repair, err := s.CompleteDSLReplay(ctxA, attempt, true, true, diagnostics, output, artifacts, "", "", nil); err != nil || repair != nil {
		t.Fatalf("complete successful replay: repair=%+v err=%v", repair, err)
	}
	var encryptedDiagnostics, encryptedOutput, encryptedArtifacts []byte
	if err := s.db.QueryRow(`SELECT diagnostics_artifact, output_artifact, artifacts_artifact FROM dsl_replay_attempts WHERE id = ?`, attempt.ID).Scan(&encryptedDiagnostics, &encryptedOutput, &encryptedArtifacts); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encryptedDiagnostics, []byte("diagnostic-plaintext-marker")) || bytes.Contains(encryptedOutput, []byte("output-plaintext-marker")) || bytes.Contains(encryptedArtifacts, []byte("artifact-plaintext-marker")) {
		t.Fatal("replay material was stored in plaintext")
	}
	storedAttempt, err := s.GetDSLReplay(ctxA, attempt.ID)
	if err != nil || storedAttempt.Status != models.ReplayAttemptSucceeded || !storedAttempt.OutputValid || storedAttempt.DiagnosticsHash == "" {
		t.Fatalf("unexpected replay attempt: attempt=%+v err=%v", storedAttempt, err)
	}
	if _, err := s.GetDSLReplay(ctxB, attempt.ID); !errors.Is(err, ErrReplayNotFound) {
		t.Fatalf("cross-workspace replay lookup should be hidden, got %v", err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_replay_attempts SET error_message = 'tampered' WHERE id = ?`, attempt.ID); err == nil {
		t.Fatal("database trigger allowed terminal replay mutation")
	}

	approved, contract, err := s.ApproveDSLWorkflow(ctxA, workflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != models.RuleApprovalApproved || approved.RuleID != rule.ID || approved.ApprovedBy != "alice" {
		t.Fatalf("unexpected approved rule version: %+v", approved)
	}
	if approved.Rule == nil {
		t.Fatal("approved workflow rule is missing")
	}
	var engineOutput map[string]any
	if err := json.Unmarshal(approved.Rule.Output, &engineOutput); err != nil {
		t.Fatal(err)
	}
	if len(engineOutput) != 0 {
		t.Fatalf("workflow rule must keep the per-row schema outside engine-level output: %#v", engineOutput)
	}
	var outputSchema map[string]any
	if err := json.Unmarshal(contract.OutputSchema, &outputSchema); err != nil {
		t.Fatal(err)
	}
	properties, _ := outputSchema["properties"].(map[string]any)
	if outputSchema["type"] != "object" || properties["name"] == nil || properties["price"] == nil {
		t.Fatalf("immutable rule-version contract lost confirmed output fields: %#v", outputSchema)
	}
	approvedWorkflow, err := s.GetDSLWorkflow(ctxA, workflow.ID)
	if err != nil || approvedWorkflow.Status != models.DSLWorkflowApproved || approvedWorkflow.ApprovedRuleID != rule.ID || approvedWorkflow.ApprovedVersion != approved.Version {
		t.Fatalf("unexpected approved workflow: workflow=%+v err=%v", approvedWorkflow, err)
	}
	retried, retriedContract, err := s.ApproveDSLWorkflow(ctxA, workflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil || retried.RuleID != approved.RuleID || retried.Version != approved.Version ||
		retried.ContentHash != approved.ContentHash || retriedContract.RuleID != contract.RuleID ||
		retriedContract.Version != contract.Version {
		t.Fatalf("workflow approval retry did not return the committed version: version=%+v contract=%+v err=%v",
			retried, retriedContract, err)
	}
	logs, total, err := s.ListAuditLogs(ctxA, ListAuditLogsFilter{
		Action: "dsl_workflow_approved", ResourceType: "dsl_workflow", ResourceID: workflow.ID,
	})
	if err != nil || total != 1 || len(logs) != 1 {
		t.Fatalf("workflow approval retry duplicated its atomic audit: total=%d logs=%+v err=%v",
			total, logs, err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_approvals SET rule_id = 'tampered' WHERE workspace_id = ? AND workflow_id = ?`, authz.DefaultWorkspaceID, workflow.ID); err == nil {
		t.Fatal("database trigger allowed approval lineage update")
	}
	if _, err := s.db.Exec(`DELETE FROM dsl_approvals WHERE workspace_id = ? AND workflow_id = ?`, authz.DefaultWorkspaceID, workflow.ID); err == nil {
		t.Fatal("database trigger allowed approval lineage deletion")
	}
	if _, err := s.db.Exec(`UPDATE dsl_workflows SET provisional_hash = 'tampered' WHERE id = ?`, workflow.ID); err == nil {
		t.Fatal("database trigger allowed approved workflow mutation")
	}

	if err := s.DeleteRecording(ctxA, workflow.RecordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDSLWorkflow(ctxA, workflow.ID); !errors.Is(err, ErrDSLWorkflowNotFound) {
		t.Fatalf("recording deletion retained derived workflow artifacts: %v", err)
	}
	var approvalCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dsl_approvals WHERE workspace_id = ? AND workflow_id = ?`, authz.DefaultWorkspaceID, workflow.ID).Scan(&approvalCount); err != nil {
		t.Fatal(err)
	}
	if approvalCount != 1 {
		t.Fatalf("expected one retained approval audit row, got %d", approvalCount)
	}
}

func TestCorrectDSLWorkflowReplacesEncryptedProvisionalAndPreservesHistory(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, claimed := createClaimedDSLWorkflow(t, s, ctx, "correction")
	rule := completeDSLGeneration(t, s, ctx, workflow, claimed, "correction")
	before, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := s.StartDSLReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteDSLReplay(ctx, attempt, false, false, nil, nil, nil, "REPLAY_FAILED", "missing selector", nil); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil || failed.Status != models.DSLWorkflowFailed || failed.ErrorCode != "REPLAY_FAILED" {
		t.Fatalf("unexpected failed workflow: workflow=%+v err=%v", failed, err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_workflows SET status = 'awaiting_replay' WHERE id = ?`, workflow.ID); err == nil {
		t.Fatal("database trigger allowed failed workflow reopening without a new provisional hash")
	}

	corrected := *rule
	corrected.Name = "human-correction-plaintext-marker"
	corrected.Steps = models.JSON(`[{"action":"extractText","name":"name","target":{"selector":".corrected"}}]`)
	if err := s.CorrectDSLWorkflow(ctx, workflow.ID, &corrected, "name: human-correction-plaintext-marker\n"); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != models.DSLWorkflowAwaitingReplay || after.ProvisionalHash == "" || after.ProvisionalHash == before.ProvisionalHash || after.CurrentJobID != "" || after.ErrorCode != "" || after.ErrorMessage != "" {
		t.Fatalf("unexpected corrected workflow: before=%+v after=%+v", before, after)
	}
	if after.LastReplaySequence != 1 || after.RepairCount != failed.RepairCount {
		t.Fatalf("correction changed replay history: failed=%+v after=%+v", failed, after)
	}
	var encrypted []byte
	if err := s.db.QueryRow(`SELECT provisional_artifact FROM dsl_workflows WHERE id = ?`, workflow.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("human-correction-plaintext-marker")) || bytes.Contains(encrypted, []byte(".corrected")) {
		t.Fatal("human correction was stored in plaintext")
	}
	if err := s.CorrectDSLWorkflow(workspaceContext("bob", "tenant-b"), workflow.ID, &corrected, "hidden"); !errors.Is(err, ErrDSLWorkflowState) {
		t.Fatalf("cross-workspace correction should not mutate the workflow, got %v", err)
	}
	if _, err := s.StartDSLReplay(ctx, workflow.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CorrectDSLWorkflow(ctx, workflow.ID, &corrected, "hidden"); !errors.Is(err, ErrDSLWorkflowState) {
		t.Fatalf("replaying workflow accepted a correction: %v", err)
	}
}

func TestUpdateDSLJobProgressTracksRunningJobOnly(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, claimed := createClaimedDSLWorkflow(t, s, ctx, "progress")

	if err := s.UpdateDSLJobProgress(ctx, claimed.ID, 5, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDSLJobProgress(ctx, claimed.ID, 5, 3); err != nil {
		t.Fatal(err)
	}
	running, err := s.GetDSLJob(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.ChunkCount != 5 || running.CompletedChunks != 3 || running.Status != models.DSLJobRunning {
		t.Fatalf("unexpected running dsl progress: %+v", running)
	}
	if err := s.UpdateDSLJobProgress(ctx, "missing-dsl-job", 5, 1); !errors.Is(err, ErrDSLJobState) {
		t.Fatalf("unknown dsl job must not accept progress: %v", err)
	}
	claimed.CompletedChunks = 2
	completeDSLGeneration(t, s, ctx, workflow, claimed, "progress")
	completed, err := s.GetDSLJob(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ChunkCount != 2 || completed.CompletedChunks != 2 {
		t.Fatalf("completion must persist matching totals: %+v", completed)
	}
	if err := s.UpdateDSLJobProgress(ctx, claimed.ID, 2, 1); !errors.Is(err, ErrDSLJobState) {
		t.Fatalf("terminal dsl job must not accept progress: %v", err)
	}
}

func TestDSLWorkflowRequiresConfirmedScopedRequirementAndLegalStates(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	createRequirementTestRecording(t, s, ctxA, "dsl-recording-unconfirmed")
	requirement := &models.CollectionRequirement{ID: "dsl-requirement-unconfirmed", RecordingID: "dsl-recording-unconfirmed", Requirement: requirementSpec()}
	if err := s.CreateCollectionRequirement(ctxA, requirement); err != nil {
		t.Fatal(err)
	}
	workflow := &models.DSLWorkflow{ID: "dsl-workflow-unconfirmed", RequirementID: requirement.ID, RecordingID: requirement.RecordingID, BrowserProfileID: "profile"}
	if _, err := s.CreateDSLWorkflow(ctxA, workflow, map[string]any{}); !errors.Is(err, ErrRequirementState) {
		t.Fatalf("unconfirmed requirement should be rejected, got %v", err)
	}
	if _, err := s.CreateDSLWorkflow(ctxB, workflow, map[string]any{}); !errors.Is(err, ErrRequirementState) {
		t.Fatalf("cross-workspace requirement should be rejected, got %v", err)
	}
	confirmed, err := s.ConfirmCollectionRequirement(ctxA, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow.RequirementID = confirmed.ID
	job, err := s.CreateDSLWorkflow(ctxA, workflow, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDSLReplay(ctxA, workflow.ID); !errors.Is(err, ErrDSLWorkflowState) {
		t.Fatalf("replay before generation should fail, got %v", err)
	}
	if _, _, err := s.ApproveDSLWorkflow(ctxA, workflow.ID, ApproveDSLWorkflowOptions{}); !errors.Is(err, ErrDSLWorkflowState) {
		t.Fatalf("approval before successful replay should fail, got %v", err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_workflows SET status = 'approved' WHERE id = ?`, workflow.ID); err == nil {
		t.Fatal("database trigger allowed illegal workflow transition")
	}
	if _, err := s.GetDSLJob(ctxA, job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDSLReplaySchedulesAtMostThreeRepairs(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, "repairs")
	completeDSLGeneration(t, s, ctx, workflow, job, "repairs")

	for replayNumber := 0; replayNumber < 4; replayNumber++ {
		attempt, err := s.StartDSLReplay(ctx, workflow.ID)
		if err != nil {
			t.Fatalf("start replay %d: %v", replayNumber+1, err)
		}
		repairRequest := map[string]any{"replay": replayNumber + 1, "diagnostics": "bounded safe diagnostics"}
		repair, err := s.CompleteDSLReplay(ctx, attempt, false, false,
			map[string]any{"code": "ELEMENT_NOT_FOUND"}, nil, nil,
			"REPLAY_FAILED", "replay failed safely", repairRequest)
		if err != nil {
			t.Fatalf("complete replay %d: %v", replayNumber+1, err)
		}
		if replayNumber == 3 {
			if repair != nil {
				t.Fatalf("repair budget exceeded: %+v", repair)
			}
			break
		}
		if repair == nil || repair.Kind != models.DSLJobRepair {
			t.Fatalf("expected repair job %d, got %+v", replayNumber+1, repair)
		}
		claimedRepair, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claimedRepair.ID != repair.ID || claimedRepair.Kind != models.DSLJobRepair {
			t.Fatalf("unexpected claimed repair: %+v", claimedRepair)
		}
		completeDSLGeneration(t, s, ctx, workflow, claimedRepair, "repairs")
	}
	failed, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != models.DSLWorkflowFailed || failed.RepairCount != 3 || failed.CurrentJobID != "" {
		t.Fatalf("unexpected exhausted workflow: %+v", failed)
	}
	if _, err := s.StartDSLReplay(ctx, workflow.ID); !errors.Is(err, ErrDSLWorkflowState) {
		t.Fatalf("failed workflow should not replay, got %v", err)
	}
}

func TestDSLJobRetriesLeaseAndThenFailsWorkflow(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, claimed := createClaimedDSLWorkflow(t, s, ctx, "job-failure")
	claimed.Provider = "fake"
	claimed.Model = "fake-model"
	claimed.ChunkCount = 2
	claimed.CompletedChunks = 1
	claimed.InputTokens = 11
	claimed.OutputTokens = 3
	claimed.SafetyFlags = []string{"schema-validation:failed"}
	if err := s.FailDSLJob(ctx, claimed, "PROVIDER_UNAVAILABLE", "provider unavailable", 0); err != nil {
		t.Fatal(err)
	}
	for attemptNumber := 2; attemptNumber <= 3; attemptNumber++ {
		next, err := s.ClaimPendingDSLJob(context.Background(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if next.AttemptCount != attemptNumber {
			t.Fatalf("expected attempt %d, got %d", attemptNumber, next.AttemptCount)
		}
		if next.Provider != "fake" || next.Model != "fake-model" || next.InputTokens != (attemptNumber-1)*11 || next.OutputTokens != (attemptNumber-1)*3 {
			t.Fatalf("retry claim lost prior provider usage: %+v", next)
		}
		next.CompletedChunks = 2
		next.InputTokens += 11
		next.OutputTokens += 3
		if err := s.FailDSLJob(ctx, next, "PROVIDER_UNAVAILABLE", "provider unavailable", 0); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil || failed.Status != models.DSLWorkflowFailed || failed.ErrorCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("unexpected terminal generation failure: workflow=%+v err=%v", failed, err)
	}
	failedJob, err := s.GetDSLJob(ctx, claimed.ID)
	if err != nil || failedJob.Status != models.DSLJobFailed || failedJob.InputTokens != 33 || failedJob.OutputTokens != 9 ||
		failedJob.Provider != "fake" || failedJob.Model != "fake-model" || failedJob.CompletedChunks != 2 || len(failedJob.SafetyFlags) != 1 {
		t.Fatalf("terminal job lost cumulative failed-attempt metadata: job=%+v err=%v", failedJob, err)
	}
	if _, err := s.db.Exec(`UPDATE dsl_jobs SET error_message = 'tampered' WHERE id = ?`, claimed.ID); err == nil {
		t.Fatal("database trigger allowed terminal job mutation")
	}
}

func TestFailExpiredReplayAttemptsTransitionsRunningToFailed(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, "replay-reaper")
	completeDSLGeneration(t, s, ctx, workflow, job, "replay-reaper")
	attempt, err := s.StartDSLReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Force the attempt to be expired.
	now := time.Now().UTC()
	if _, err := s.db.Exec(
		`UPDATE dsl_replay_attempts SET expires_at = ? WHERE id = ?`,
		now.Add(-time.Minute), attempt.ID,
	); err != nil {
		t.Fatal(err)
	}

	// The sweep runs inside ClaimPendingDSLJob; with no pending DSL jobs it
	// returns ErrNoTaskAvailable after the reaper has done its work.
	if _, err := s.ClaimPendingDSLJob(context.Background(), time.Minute); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expected ErrNoTaskAvailable after sweep, got %v", err)
	}

	var status string
	if err := s.db.QueryRow(`SELECT status FROM dsl_replay_attempts WHERE id = ?`, attempt.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("expected attempt status='failed', got %q", status)
	}
	var errorCode string
	if err := s.db.QueryRow(`SELECT COALESCE(error_code, '') FROM dsl_replay_attempts WHERE id = ?`, attempt.ID).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "REPLAY_ATTEMPT_EXPIRED" {
		t.Fatalf("expected error_code='REPLAY_ATTEMPT_EXPIRED', got %q", errorCode)
	}

	// Workflow was created with MaxRepairs=0 (no repair budget), so it should
	// transition to 'failed' rather than scheduling a repair.
	failedWorkflow, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedWorkflow.Status != models.DSLWorkflowFailed {
		t.Fatalf("expected workflow status='failed', got %q", failedWorkflow.Status)
	}
	if failedWorkflow.ErrorCode != "REPLAY_ATTEMPT_EXPIRED" {
		t.Fatalf("expected workflow error_code='REPLAY_ATTEMPT_EXPIRED', got %q", failedWorkflow.ErrorCode)
	}
}

func TestFailExpiredReplayAttemptsDoesNotScheduleRepairEvenWithBudget(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, "replay-reaper-budget")
	completeDSLGeneration(t, s, ctx, workflow, job, "replay-reaper-budget")

	// CreateDSLWorkflow defaults MaxRepairs=3, so this workflow has repair
	// budget. Confirm that before exercising the reaper.
	stored, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MaxRepairs != 3 {
		t.Fatalf("expected workflow MaxRepairs=3, got %d", stored.MaxRepairs)
	}

	attempt, err := s.StartDSLReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Force the attempt to be expired.
	now := time.Now().UTC()
	if _, err := s.db.Exec(
		`UPDATE dsl_replay_attempts SET expires_at = ? WHERE id = ?`,
		now.Add(-time.Minute), attempt.ID,
	); err != nil {
		t.Fatal(err)
	}

	// The sweep runs inside ClaimPendingDSLJob; with no pending DSL jobs it
	// returns ErrNoTaskAvailable after the reaper has done its work.
	if _, err := s.ClaimPendingDSLJob(context.Background(), time.Minute); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expected ErrNoTaskAvailable after sweep, got %v", err)
	}

	// Attempt is failed.
	var status string
	if err := s.db.QueryRow(`SELECT status FROM dsl_replay_attempts WHERE id = ?`, attempt.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("expected attempt status='failed', got %q", status)
	}

	// Workflow is failed (NOT repairing — no repair scheduled, even with
	// MaxRepairs=3, because the reaper passes repairRequest=nil).
	failedWorkflow, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedWorkflow.Status != models.DSLWorkflowFailed {
		t.Fatalf("expected workflow status='failed' (no repair scheduled), got %q", failedWorkflow.Status)
	}

	// No dsl_jobs row beyond the initial generation job. After generation
	// completes the generation job is terminal, so the only way a new row
	// could appear is a repair job — assert none was created.
	var jobCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dsl_jobs WHERE workflow_id = ?`, workflow.ID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("expected 1 dsl_jobs row (generation only) for workflow after reaper, got %d", jobCount)
	}
}

func TestApproveDSLWorkflowRejectsBlockingSafetyFlag(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createApprovalReadyDSLWorkflow(t, s, ctx, "blocking-flag-reject")
	if _, err := s.db.Exec(
		`UPDATE dsl_workflows SET safety_flags = ? WHERE id = ?`,
		`["external-resource-load"]`, workflow.ID,
	); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{})
	if !errors.Is(err, ErrSafetyBlockingFlag) {
		t.Fatalf("expected ErrSafetyBlockingFlag, got %v", err)
	}
	var flagErr *SafetyBlockingFlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("expected *SafetyBlockingFlagError, got %T: %v", err, err)
	}
	if len(flagErr.Flags) != 1 || flagErr.Flags[0] != "external-resource-load" {
		t.Fatalf("unexpected blocking flags: %+v", flagErr.Flags)
	}
	var versionCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM rule_versions WHERE workspace_id = ?`, authz.DefaultWorkspaceID,
	).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 0 {
		t.Fatalf("expected 0 rule_versions, got %d", versionCount)
	}
	stored, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.DSLWorkflowAwaitingConfirmation {
		t.Fatalf("workflow status changed despite blocking gate: %s", stored.Status)
	}
}

func TestApproveDSLWorkflowApprovesAdvisoryFlagWithoutOverride(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createApprovalReadyDSLWorkflow(t, s, ctx, "advisory-flag-approve")
	if _, err := s.db.Exec(
		`UPDATE dsl_workflows SET safety_flags = ? WHERE id = ?`,
		`["selector-evidence:passed"]`, workflow.ID,
	); err != nil {
		t.Fatal(err)
	}
	version, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatalf("advisory flag should not block approval: %v", err)
	}
	if version == nil || version.Status != models.RuleApprovalApproved {
		t.Fatalf("unexpected approved version: %+v", version)
	}
	var storedFlags string
	if err := s.db.QueryRow(
		`SELECT COALESCE(safety_flags, '[]') FROM rule_versions WHERE workspace_id = ? AND rule_id = ?`,
		authz.DefaultWorkspaceID, version.RuleID,
	).Scan(&storedFlags); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedFlags, "selector-evidence:passed") {
		t.Fatalf("rule_versions.safety_flags not carried: %s", storedFlags)
	}
}

func TestApproveDSLWorkflowOverrideSucceedsAndWritesAuditLog(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createApprovalReadyDSLWorkflow(t, s, ctx, "blocking-flag-override")
	if _, err := s.db.Exec(
		`UPDATE dsl_workflows SET safety_flags = ? WHERE id = ?`,
		`["external-resource-load","script-tag-in-selector"]`, workflow.ID,
	); err != nil {
		t.Fatal(err)
	}
	version, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{
		OverrideSafety: true,
		OverrideActor:  "admin@example.com",
	})
	if err != nil {
		t.Fatalf("override should succeed: %v", err)
	}
	if version == nil || version.Status != models.RuleApprovalApproved {
		t.Fatalf("unexpected approved version: %+v", version)
	}
	overrideLogs, overrideTotal, err := s.ListAuditLogs(ctx, ListAuditLogsFilter{
		Action: "dsl_workflow_safety_override", ResourceType: "dsl_workflow", ResourceID: workflow.ID,
	})
	if err != nil || overrideTotal != 1 || len(overrideLogs) != 1 {
		t.Fatalf("expected exactly 1 override audit entry: total=%d logs=%+v err=%v",
			overrideTotal, overrideLogs, err)
	}
	if overrideLogs[0].Actor != "admin@example.com" {
		t.Fatalf("override audit actor mismatch: %s", overrideLogs[0].Actor)
	}
	if !bytes.Contains(overrideLogs[0].Payload, []byte("external-resource-load")) ||
		!bytes.Contains(overrideLogs[0].Payload, []byte("script-tag-in-selector")) {
		t.Fatalf("override audit payload missing blocking flags: %s", overrideLogs[0].Payload)
	}
	if !bytes.Contains(overrideLogs[0].Payload, []byte(version.RuleID)) {
		t.Fatalf("override audit payload missing ruleId: %s", overrideLogs[0].Payload)
	}
}

func TestApproveDSLWorkflowOverrideWithoutActorFails(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createApprovalReadyDSLWorkflow(t, s, ctx, "blocking-flag-override-no-actor")
	if _, err := s.db.Exec(
		`UPDATE dsl_workflows SET safety_flags = ? WHERE id = ?`,
		`["external-resource-load"]`, workflow.ID,
	); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{
		OverrideSafety: true,
		// OverrideActor intentionally empty
	})
	if !errors.Is(err, ErrSafetyBlockingFlag) {
		t.Fatalf("expected ErrSafetyBlockingFlag, got %v", err)
	}
	// The override-without-actor branch returns fmt.Errorf("%w: ...",
	// ErrSafetyBlockingFlag) rather than *SafetyBlockingFlagError, so only
	// the sentinel survives in the wrap chain. Verify the message carries
	// the OverrideActor hint to aid operator diagnosis.
	if !strings.Contains(err.Error(), "OverrideActor") {
		t.Fatalf("expected error mentioning OverrideActor, got: %v", err)
	}
	var versionCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM rule_versions WHERE workspace_id = ?`, authz.DefaultWorkspaceID,
	).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 0 {
		t.Fatalf("expected 0 rule_versions, got %d", versionCount)
	}
	stored, err := s.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.DSLWorkflowAwaitingConfirmation {
		t.Fatalf("workflow status changed despite missing override actor: %s", stored.Status)
	}
}

func TestWorkflowBudgetDefaultsToFiveCentsAtCreation(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createClaimedDSLWorkflow(t, s, ctx, "budget-default")

	var budgetNanos int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT budget_usd_nanos FROM dsl_workflows WHERE id = ?`,
		workflow.ID,
	).Scan(&budgetNanos); err != nil {
		t.Fatal(err)
	}
	if budgetNanos != 5_000_000 {
		t.Fatalf("expected default budget 5_000_000 (5¢ in nanodollars), got %d", budgetNanos)
	}

	var spent int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT spent_usd_nanos FROM dsl_workflows WHERE id = ?`,
		workflow.ID,
	).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 0 {
		t.Fatalf("expected initial spent 0, got %d", spent)
	}
}

func TestWorkflowBudgetAddSpendIncrementsSpent(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createClaimedDSLWorkflow(t, s, ctx, "budget-spend")

	if err := s.AddWorkflowSpend(ctx, workflow.ID, config.USDNanos(60)); err != nil {
		t.Fatalf("AddWorkflowSpend: %v", err)
	}
	if err := s.AddWorkflowSpend(ctx, workflow.ID, config.USDNanos(40)); err != nil {
		t.Fatalf("AddWorkflowSpend second: %v", err)
	}

	var spent int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT spent_usd_nanos FROM dsl_workflows WHERE id = ?`,
		workflow.ID,
	).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 100 {
		t.Fatalf("expected spent 100 after two increments (60+40), got %d", spent)
	}
}

func TestWorkflowBudgetCheckAdmitsBelowCapAndDeniesAbove(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createClaimedDSLWorkflow(t, s, ctx, "budget-check")

	// Shrink the envelope to a known small value so we can test the boundary.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE dsl_workflows SET budget_usd_nanos = 100 WHERE id = ?`,
		workflow.ID,
	); err != nil {
		t.Fatal(err)
	}

	// Admit a 60-nano call.
	if err := s.CheckAndReserveWorkflowBudget(ctx, workflow.ID, config.USDNanos(60)); err != nil {
		t.Fatalf("expected admission for 60 < 100, got: %v", err)
	}
	// Record the spend so the next check sees it.
	if err := s.AddWorkflowSpend(ctx, workflow.ID, config.USDNanos(60)); err != nil {
		t.Fatal(err)
	}

	// A 50-nano estimate would push spent (60) + 50 = 110 > 100.
	err := s.CheckAndReserveWorkflowBudget(ctx, workflow.ID, config.USDNanos(50))
	if err == nil {
		t.Fatalf("expected denial when 60+50 > 100, got nil error")
	}
	var denied *budget.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *budget.DeniedError, got %T: %v", err, err)
	}
	if denied.CodeString() != budget.CodeWorkflowBudgetExceeded {
		t.Fatalf("expected code %q, got %q", budget.CodeWorkflowBudgetExceeded, denied.CodeString())
	}
	if denied.Scope != "workflow" || denied.Limit != "envelope" {
		t.Fatalf("unexpected scope/limit: %q / %q", denied.Scope, denied.Limit)
	}
}

// TestApproveDSLWorkflowPopulatesProvenanceOnApproval verifies that the five
// LLM provenance columns on dsl_approvals are sourced from the latest provider
// call linked to the workflow's completed generation DSL job.
func TestApproveDSLWorkflowPopulatesProvenanceOnApproval(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, "provenance")

	// Seed a provider call for the DSL job so the provenance join has data.
	call, callArtifact := providerCallForAttempt(models.LLMJobTypeDSL, job.ID, job.AttemptCount, 1, "provenance-call")
	call.RequestHash = "req-hash-provenance"
	call.Model = "provenance-model"
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}

	completeDSLGeneration(t, s, ctx, workflow, job, "provenance")

	attempt, err := s.StartDSLReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteDSLReplay(
		ctx, attempt, true, true, nil,
		map[string]any{"name": "Example", "price": 10.5}, nil, "", "", nil,
	); err != nil {
		t.Fatalf("complete replay: %v", err)
	}

	if _, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{}); err != nil {
		t.Fatal(err)
	}

	var dslJobID, providerCallID, promptHash, modelID string
	var cacheHit bool
	if err := s.db.QueryRow(`
		SELECT COALESCE(dsl_job_id, ''), COALESCE(provider_call_id, ''),
		       COALESCE(prompt_hash, ''), COALESCE(model_id, ''), cache_hit
		FROM dsl_approvals WHERE workflow_id = ?
	`, workflow.ID).Scan(&dslJobID, &providerCallID, &promptHash, &modelID, &cacheHit); err != nil {
		t.Fatalf("read approval provenance: %v", err)
	}
	if dslJobID == "" {
		t.Error("expected dsl_job_id populated")
	}
	if dslJobID != job.ID {
		t.Errorf("dsl_job_id = %q, want %q", dslJobID, job.ID)
	}
	if providerCallID == "" {
		t.Error("expected provider_call_id populated")
	}
	if promptHash != "req-hash-provenance" {
		t.Errorf("prompt_hash = %q, want %q", promptHash, "req-hash-provenance")
	}
	if modelID != "provenance-model" {
		t.Errorf("model_id = %q, want %q", modelID, "provenance-model")
	}
	if cacheHit {
		t.Error("expected cache_hit = false for non-cache-hit call kind")
	}
}

// TestApproveDSLWorkflowProvenanceImmutableViaTrigger verifies the existing
// dsl_approvals immutability trigger covers the new provenance columns.
func TestApproveDSLWorkflowProvenanceImmutableViaTrigger(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, _ := createApprovalReadyDSLWorkflow(t, s, ctx, "prov-immutable")
	if _, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE dsl_approvals SET prompt_hash = 'tampered' WHERE workflow_id = ?`,
		workflow.ID,
	); err == nil {
		t.Fatal("expected immutability trigger to reject provenance column update")
	}
}

// TestGetRuleVersionReturnsProvenanceFields verifies that the rule version
// created by DSL approval carries dsl_workflow_id and dsl_job_id.
func TestGetRuleVersionReturnsProvenanceFields(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, "rv-provenance")
	completeDSLGeneration(t, s, ctx, workflow, job, "rv-provenance")

	attempt, err := s.StartDSLReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteDSLReplay(
		ctx, attempt, true, true, nil,
		map[string]any{"name": "Example"}, nil, "", "", nil,
	); err != nil {
		t.Fatalf("complete replay: %v", err)
	}

	version, _, err := s.ApproveDSLWorkflow(ctx, workflow.ID, ApproveDSLWorkflowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := s.GetRuleVersion(ctx, version.RuleID, version.Version)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.DSLWorkflowID != workflow.ID {
		t.Errorf("DSLWorkflowID = %q, want %q", fetched.DSLWorkflowID, workflow.ID)
	}
	if fetched.DSLJobID != job.ID {
		t.Errorf("DSLJobID = %q, want %q", fetched.DSLJobID, job.ID)
	}
}
