package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func createClaimedRequirementArtifactJob(t *testing.T, s *Store, ctx context.Context, suffix string, maxAttempts int) *models.RequirementJob {
	t.Helper()
	recordingID := "llm-artifact-recording-" + suffix
	createRequirementTestRecording(t, s, ctx, recordingID)
	job := &models.RequirementJob{
		ID:            "llm-artifact-requirement-job-" + suffix,
		RecordingID:   recordingID,
		Kind:          models.RequirementJobCandidates,
		PromptVersion: "collection-requirement-test-v1",
		MaxAttempts:   maxAttempts,
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"kind": "candidates"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID || claimed.AttemptCount != 1 {
		t.Fatalf("unexpected claimed requirement job: %+v", claimed)
	}
	return claimed
}

func providerCallForAttempt(jobType models.LLMJobType, jobID string, attempt, callIndex int, marker string) (*models.LLMProviderCall, map[string]any) {
	artifact := map[string]any{
		"rawResponse": marker,
		"metadata":    map[string]any{"finishReason": "stop"},
	}
	contentBytes := len(marker)
	chunkIndex := 0
	return &models.LLMProviderCall{
		JobType:            jobType,
		JobID:              jobID,
		AttemptNumber:      attempt,
		CallIndex:          callIndex,
		CallKind:           models.LLMProviderCallKindResponse,
		ProviderAttempt:    1,
		Phase:              "chunk-analysis",
		ChunkIndex:         &chunkIndex,
		ChunkCount:         1,
		Provider:           "fake",
		Model:              "fake-model",
		PromptVersion:      "test-prompt-v1",
		RequestHash:        artifactHash([]byte("request-" + marker)),
		ResponseHash:       artifactHash([]byte(marker)),
		ResponseID:         "response-" + marker,
		FinishReason:       "stop",
		InputTokens:        11,
		OutputTokens:       7,
		OriginalBytes:      contentBytes,
		OriginalBytesExact: true,
		CapturedBytes:      contentBytes,
		Replayable:         true,
	}, artifact
}

func attemptReportForJob(jobType models.LLMJobType, jobID string, attempt int) (*models.LLMAttemptReport, map[string]any) {
	artifact := map[string]any{
		"schemaVersion": "aegiscrawler.llm-attempt-report.v1",
		"callLineage":   []any{map[string]any{"callIndex": 1, "phase": "chunk-analysis"}},
		"validation":    map[string]any{"status": "passed"},
	}
	return &models.LLMAttemptReport{
		JobType:         jobType,
		JobID:           jobID,
		AttemptNumber:   attempt,
		Outcome:         models.LLMAttemptSucceeded,
		Provider:        "fake",
		Model:           "fake-model",
		PromptVersion:   "test-prompt-v1",
		RecordingHash:   artifactHash([]byte("recording")),
		RequirementHash: artifactHash([]byte("requirement")),
		InputTokens:     11,
		OutputTokens:    7,
		ValidationPhase: "complete",
		SafetyFlags:     []string{"schema-validation:passed"},
		Replayable:      true,
	}, artifact
}

func TestLLMAttemptArtifactsAreEncryptedScopedBoundedAndImmutable(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	job := createClaimedRequirementArtifactJob(t, s, ctxA, "roundtrip", 2)

	call, callArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 1, "provider-plaintext-marker")
	if err := s.CreateLLMProviderCall(ctxA, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	if call.WorkspaceID != authz.DefaultWorkspaceID || call.RecordingID != job.RecordingID || call.ArtifactHash == "" || call.ArtifactBytes == 0 {
		t.Fatalf("unexpected provider call metadata: %+v", call)
	}
	var encryptedCall []byte
	if err := s.db.QueryRow(`SELECT artifact FROM llm_provider_calls WHERE id = ?`, call.ID).Scan(&encryptedCall); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encryptedCall, []byte("provider-plaintext-marker")) {
		t.Fatal("provider completion was stored in plaintext")
	}
	listedCalls, err := s.ListLLMProviderCalls(ctxA, call.JobType, call.JobID, 1)
	if err != nil || len(listedCalls) != 1 || listedCalls[0].Artifact != nil {
		t.Fatalf("unexpected provider call list: calls=%+v err=%v", listedCalls, err)
	}
	storedCall, err := s.GetLLMProviderCall(ctxA, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !storedCall.OriginalBytesExact {
		t.Fatalf("exact provider response provenance was not preserved: %+v", storedCall)
	}
	openedCall, ok := storedCall.Artifact.(map[string]any)
	if !ok || openedCall["rawResponse"] != "provider-plaintext-marker" {
		t.Fatalf("unexpected decrypted provider artifact: %#v", storedCall.Artifact)
	}
	if _, err := s.GetLLMProviderCall(ctxB, call.ID); !errors.Is(err, ErrLLMProviderCallNotFound) {
		t.Fatalf("cross-workspace provider call lookup should be hidden, got %v", err)
	}
	if calls, err := s.ListLLMProviderCalls(ctxB, call.JobType, call.JobID, 1); err != nil || len(calls) != 0 {
		t.Fatalf("cross-workspace provider call list leaked data: calls=%+v err=%v", calls, err)
	}
	if _, err := s.openLLMArtifact(authz.DefaultWorkspaceID, "llm-provider-call", "different-id", encryptedCall, call.ArtifactHash); err == nil {
		t.Fatal("provider artifact decrypted with different row AAD")
	}
	if _, err := s.openLLMArtifact(authz.DefaultWorkspaceID, "llm-provider-call", call.ID, encryptedCall, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("provider artifact hash tampering was not detected: %v", err)
	}

	report, reportArtifact := attemptReportForJob(models.LLMJobTypeRequirement, job.ID, 1)
	if err := s.CreateLLMAttemptReport(ctxA, report, reportArtifact); err != nil {
		t.Fatal(err)
	}
	if report.CallCount != 1 || report.RecordingID != job.RecordingID || report.ArtifactHash == "" {
		t.Fatalf("unexpected attempt report metadata: %+v", report)
	}
	var encryptedReport []byte
	if err := s.db.QueryRow(`SELECT artifact FROM llm_attempt_reports WHERE id = ?`, report.ID).Scan(&encryptedReport); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encryptedReport, []byte("schema-validation")) {
		t.Fatal("attempt validation report was stored in plaintext")
	}
	storedReport, err := s.GetLLMAttemptReport(ctxA, report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedReport.CallCount != 1 || storedReport.Artifact == nil || len(storedReport.SafetyFlags) != 1 {
		t.Fatalf("unexpected decrypted attempt report: %+v", storedReport)
	}
	listedReports, err := s.ListLLMAttemptReports(ctxA, report.JobType, report.JobID)
	if err != nil || len(listedReports) != 1 || listedReports[0].Artifact != nil {
		t.Fatalf("unexpected attempt report list: reports=%+v err=%v", listedReports, err)
	}
	if _, err := s.GetLLMAttemptReport(ctxB, report.ID); !errors.Is(err, ErrLLMAttemptReportNotFound) {
		t.Fatalf("cross-workspace attempt report lookup should be hidden, got %v", err)
	}
	if reports, err := s.ListLLMAttemptReports(ctxB, report.JobType, report.JobID); err != nil || len(reports) != 0 {
		t.Fatalf("cross-workspace attempt report list leaked data: reports=%+v err=%v", reports, err)
	}
	if _, err := s.db.Exec(`UPDATE llm_provider_calls SET response_hash = 'tampered' WHERE id = ?`, call.ID); err == nil {
		t.Fatal("database trigger allowed provider call mutation")
	}
	if _, err := s.db.Exec(`UPDATE llm_attempt_reports SET error_message = 'tampered' WHERE id = ?`, report.ID); err == nil {
		t.Fatal("database trigger allowed attempt report mutation")
	}
	lateCall, lateArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 2, "late-provider-output")
	if err := s.CreateLLMProviderCall(ctxA, lateCall, lateArtifact); err == nil {
		t.Fatal("provider call was appended after the immutable attempt report")
	}

	invalidCall, invalidArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 2, "redacted")
	invalidCall.Redacted = true
	if err := s.CreateLLMProviderCall(ctxA, invalidCall, invalidArtifact); !errors.Is(err, ErrLLMArtifactInvalid) {
		t.Fatalf("replayable redacted call should be rejected, got %v", err)
	}
	invalidLengthCall, invalidLengthArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 2, "lower-bound")
	invalidLengthCall.OriginalBytesExact = false
	if err := s.CreateLLMProviderCall(ctxA, invalidLengthCall, invalidLengthArtifact); !errors.Is(err, ErrLLMArtifactInvalid) {
		t.Fatalf("replayable lower-bound call should be rejected, got %v", err)
	}
	oversizedCall, _ := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 2, "oversized")
	oversizedArtifact := map[string]any{"content": strings.Repeat("x", MaxLLMProviderCallArtifactBytes+1)}
	if err := s.CreateLLMProviderCall(ctxA, oversizedCall, oversizedArtifact); !errors.Is(err, ErrLLMArtifactTooLarge) {
		t.Fatalf("oversized provider call should be rejected, got %v", err)
	}
	oversizedReport, _ := attemptReportForJob(models.LLMJobTypeRequirement, job.ID, 1)
	oversizedReport.ID = "oversized-attempt-report"
	if err := s.CreateLLMAttemptReport(ctxA, oversizedReport, map[string]any{"content": strings.Repeat("x", MaxLLMAttemptReportArtifactBytes+1)}); !errors.Is(err, ErrLLMArtifactTooLarge) {
		t.Fatalf("oversized attempt report should be rejected, got %v", err)
	}

	// Retention is the only mutation: complete rows can be deleted, but never
	// edited in place.
	if _, err := s.db.Exec(`DELETE FROM llm_attempt_reports WHERE id = ?`, report.ID); err != nil {
		t.Fatalf("retention could not delete attempt report: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM llm_provider_calls WHERE id = ?`, call.ID); err != nil {
		t.Fatalf("retention could not delete provider call: %v", err)
	}
}

func TestLLMProviderCallDispatchLineageRoundTripsAndJoinsLedger(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	job := createClaimedRequirementArtifactJob(t, s, ctx, "dispatch-lineage", 1)
	call, artifact := providerCallForAttempt(
		models.LLMJobTypeRequirement,
		job.ID,
		job.AttemptCount,
		1,
		"lineaged-provider-output",
	)
	const fingerprint = "policy-fingerprint-a"
	call.Dispatch = &models.LLMDispatchLineage{
		OperationKind:     string(models.LLMJobTypeRequirement),
		OperationID:       job.ID + ":analysis:0",
		LogicalAttempt:    job.AttemptCount,
		PhysicalOrdinal:   0,
		RouteSlot:         "primary",
		PolicyFingerprint: fingerprint,
		PriceRevision:     "price-revision-a",
	}

	if err := s.CreateLLMProviderCall(ctx, call, artifact); err == nil ||
		!strings.Contains(err.Error(), "does not match its budget reservation") {
		t.Fatalf("lineaged provider call without a reservation was accepted: %v", err)
	}

	row := validBudgetRow()
	now := time.Now().UTC()
	row.ID = "dispatch-lineage-reservation"
	row.OperationKind = call.Dispatch.OperationKind
	row.OperationID = call.Dispatch.OperationID
	row.LogicalAttempt = call.Dispatch.LogicalAttempt
	row.PhysicalOrdinal = call.Dispatch.PhysicalOrdinal
	row.RouteSlot = call.Dispatch.RouteSlot
	row.Provider = call.Provider
	row.Model = call.Model
	row.PolicyFingerprint = call.Dispatch.PolicyFingerprint
	row.PriceRevision = call.Dispatch.PriceRevision
	row.RequestHash = call.RequestHash
	row.State = "possibly_dispatched"
	row.DispatchedAt = &now
	mustInsertBudgetRow(t, s.db, row)

	if err := s.CreateLLMProviderCall(ctx, call, artifact); err != nil {
		t.Fatalf("create lineaged provider call: %v", err)
	}
	listed, err := s.ListLLMProviderCalls(
		ctx,
		models.LLMJobTypeRequirement,
		job.ID,
		job.AttemptCount,
	)
	if err != nil || len(listed) != 1 || listed[0].Dispatch == nil ||
		*listed[0].Dispatch != *call.Dispatch {
		t.Fatalf("lineaged provider call did not round trip in list: calls=%+v err=%v", listed, err)
	}
	stored, err := s.GetLLMProviderCall(ctx, call.ID)
	if err != nil || stored.Dispatch == nil || *stored.Dispatch != *call.Dispatch {
		t.Fatalf("lineaged provider call did not round trip in get: call=%+v err=%v", stored, err)
	}

	report, reportArtifact := attemptReportForJob(
		models.LLMJobTypeRequirement,
		job.ID,
		job.AttemptCount,
	)
	report.PolicyFingerprint = "different-policy"
	if err := s.CreateLLMAttemptReport(ctx, report, reportArtifact); err == nil ||
		!strings.Contains(err.Error(), "policy lineage mismatch") {
		t.Fatalf("attempt report with mismatched policy was accepted: %v", err)
	}
	report.PolicyFingerprint = fingerprint
	if err := s.CreateLLMAttemptReport(ctx, report, reportArtifact); err != nil {
		t.Fatalf("create lineaged attempt report: %v", err)
	}
	storedReport, err := s.GetLLMAttemptReport(ctx, report.ID)
	if err != nil || storedReport.PolicyFingerprint != fingerprint {
		t.Fatalf("attempt report policy fingerprint did not round trip: report=%+v err=%v", storedReport, err)
	}
}

func TestMigration026RejectsPartialProviderCallDispatchLineage(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	job := createClaimedRequirementArtifactJob(t, s, ctx, "partial-dispatch-lineage", 1)
	call, artifact := providerCallForAttempt(
		models.LLMJobTypeRequirement,
		job.ID,
		job.AttemptCount,
		1,
		"legacy-cache-hit",
	)
	call.CallKind = models.LLMProviderCallKindCacheHit
	call.ProviderAttempt = 0
	call.CacheHit = true
	if err := s.CreateLLMProviderCall(ctx, call, artifact); err != nil {
		t.Fatalf("create legacy cache hit: %v", err)
	}

	_, err := s.db.Exec(`
		INSERT INTO llm_provider_calls (
			id, workspace_id, recording_id, job_type, job_id, attempt_number,
			call_index, call_kind, provider_attempt, phase, chunk_index,
			chunk_count, provider, model, prompt_version, request_hash,
			response_hash, response_id, finish_reason, http_status, error_code,
			input_tokens, output_tokens, cache_hit, original_bytes,
			original_bytes_exact, captured_bytes, artifact_bytes, redacted,
			truncated, replayable, artifact, artifact_hash, created_at,
			operation_kind
		)
		SELECT
			'partial-dispatch-lineage', workspace_id, recording_id,
			job_type, job_id, attempt_number, call_index + 1, call_kind,
			provider_attempt, phase, chunk_index, chunk_count, provider, model,
			prompt_version, request_hash, response_hash, response_id,
			finish_reason, http_status, error_code, input_tokens, output_tokens,
			cache_hit, original_bytes, original_bytes_exact, captured_bytes,
			artifact_bytes, redacted, truncated, replayable, artifact,
			artifact_hash, created_at, 'requirement'
		FROM llm_provider_calls WHERE id = ?
	`, call.ID)
	if err == nil || !strings.Contains(err.Error(), "invalid llm provider call dispatch lineage") {
		t.Fatalf("partial dispatch lineage was accepted: %v", err)
	}
}

func TestLLMProviderCallPersistsLowerBoundByteProvenanceAsNonReplayable(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	job := createClaimedRequirementArtifactJob(t, s, ctx, "lower-bound", 1)

	call, artifact := providerCallForAttempt(
		models.LLMJobTypeRequirement,
		job.ID,
		1,
		1,
		"bounded-provider-prefix",
	)
	call.CallKind = models.LLMProviderCallKindError
	call.ErrorCode = "response_too_large"
	call.HTTPStatus = http.StatusOK
	call.OriginalBytes = (4 << 20) + 1
	call.OriginalBytesExact = false
	call.Truncated = true
	call.Replayable = false
	if err := s.CreateLLMProviderCall(ctx, call, artifact); err != nil {
		t.Fatal(err)
	}

	stored, err := s.GetLLMProviderCall(ctx, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OriginalBytes != call.OriginalBytes ||
		stored.OriginalBytesExact ||
		!stored.Truncated ||
		stored.Replayable ||
		stored.ErrorCode != "response_too_large" {
		t.Fatalf("lower-bound provider provenance did not round trip: %+v", stored)
	}

	_, err = s.db.Exec(`
		INSERT INTO llm_provider_calls (
			id, workspace_id, recording_id, job_type, job_id, attempt_number,
			call_index, call_kind, provider_attempt, phase, chunk_index,
			chunk_count, provider, model, prompt_version, request_hash,
			response_hash, response_id, finish_reason, http_status, error_code,
			input_tokens, output_tokens, cache_hit, original_bytes,
			original_bytes_exact, captured_bytes, artifact_bytes, redacted,
			truncated, replayable, artifact, artifact_hash, created_at
		)
		SELECT
			'invalid-replayable-lower-bound', workspace_id, recording_id,
			job_type, job_id, attempt_number, call_index + 1, call_kind,
			provider_attempt, phase, chunk_index, chunk_count, provider, model,
			prompt_version, request_hash, response_hash, response_id,
			finish_reason, http_status, error_code, input_tokens, output_tokens,
			cache_hit, original_bytes, 0, captured_bytes, artifact_bytes,
			redacted, truncated, 1, artifact, artifact_hash, created_at
		FROM llm_provider_calls WHERE id = ?
	`, call.ID)
	if err == nil {
		t.Fatal("database accepted a replayable call with lower-bound byte provenance")
	}
}

func TestLLMAttemptReportsSurviveDurableJobRetries(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	firstAttempt := createClaimedRequirementArtifactJob(t, s, ctx, "retry", 2)
	call, callArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, firstAttempt.ID, 1, 1, "first-attempt")
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	firstReport, firstReportArtifact := attemptReportForJob(models.LLMJobTypeRequirement, firstAttempt.ID, 1)
	firstReport.Outcome = models.LLMAttemptFailed
	firstReport.ErrorCode = "INVALID_PROVIDER_OUTPUT"
	firstReport.ErrorMessage = "provider output failed validation"
	if err := s.CreateLLMAttemptReport(ctx, firstReport, firstReportArtifact); err != nil {
		t.Fatal(err)
	}
	if err := s.FailRequirementJob(ctx, firstAttempt, "INVALID_PROVIDER_OUTPUT", "retry provider output", 0); err != nil {
		t.Fatal(err)
	}
	afterRetry, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, firstAttempt.ID)
	if err != nil || len(afterRetry) != 1 {
		t.Fatalf("retry discarded first attempt report: reports=%+v err=%v", afterRetry, err)
	}

	secondAttempt, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if secondAttempt.ID != firstAttempt.ID || secondAttempt.AttemptCount != 2 {
		t.Fatalf("unexpected second durable attempt: %+v", secondAttempt)
	}
	secondReport := &models.LLMAttemptReport{
		JobType:         models.LLMJobTypeRequirement,
		JobID:           secondAttempt.ID,
		AttemptNumber:   2,
		Outcome:         models.LLMAttemptFailed,
		PromptVersion:   "test-prompt-v1",
		RecordingHash:   artifactHash([]byte("recording")),
		ValidationPhase: "provider-request",
		ErrorCode:       "PROVIDER_UNAVAILABLE",
		ErrorMessage:    "no provider completion received",
		Replayable:      false,
	}
	if err := s.CreateLLMAttemptReport(ctx, secondReport, map[string]any{
		"callLineage": []any{},
		"error":       map[string]any{"code": "PROVIDER_UNAVAILABLE"},
	}); err != nil {
		t.Fatal(err)
	}
	if secondReport.CallCount != 0 {
		t.Fatalf("network failure should record zero provider calls, got %d", secondReport.CallCount)
	}
	if err := s.FailRequirementJob(ctx, secondAttempt, "PROVIDER_UNAVAILABLE", "provider unavailable", 0); err != nil {
		t.Fatal(err)
	}
	retained, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, firstAttempt.ID)
	if err != nil || len(retained) != 2 || retained[0].AttemptNumber != 1 || retained[1].AttemptNumber != 2 {
		t.Fatalf("terminal job lost retained attempts: reports=%+v err=%v", retained, err)
	}
	retainedCalls, err := s.ListLLMProviderCalls(ctx, models.LLMJobTypeRequirement, firstAttempt.ID, 1)
	if err != nil || len(retainedCalls) != 1 {
		t.Fatalf("terminal job lost retained provider completion: calls=%+v err=%v", retainedCalls, err)
	}
}

func TestExpiredRequirementLeaseFinalizesCapturedAttemptBeforeReclaim(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	first := createClaimedRequirementArtifactJob(t, s, ctx, "lease-reclaim", 2)
	call, callArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement,
		first.ID,
		first.AttemptCount,
		1,
		"captured-before-worker-stop",
	)
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE requirement_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute),
		first.ID,
	); err != nil {
		t.Fatal(err)
	}

	second, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.AttemptCount != 2 {
		t.Fatalf("unexpected reclaimed attempt: %+v", second)
	}
	reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 ||
		reports[0].AttemptNumber != 1 ||
		reports[0].Outcome != models.LLMAttemptFailed ||
		reports[0].ErrorCode != "ATTEMPT_INTERRUPTED" ||
		reports[0].ValidationPhase != "attempt-recovery" ||
		reports[0].Replayable {
		t.Fatalf("expired attempt was not closed before reclaim: %+v", reports)
	}
	detail, err := s.GetLLMAttemptReport(ctx, reports[0].ID)
	if err != nil || detail.Artifact == nil {
		t.Fatalf("interrupted attempt artifact is not discoverable: report=%+v err=%v", detail, err)
	}
}

func TestExpiredRequirementLeaseWithoutCallsClosesAttemptBeforeReclaim(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	first := createClaimedRequirementArtifactJob(t, s, ctx, "empty-lease-reclaim", 2)
	if _, err := s.db.Exec(
		`UPDATE requirement_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute),
		first.ID,
	); err != nil {
		t.Fatal(err)
	}

	second, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.AttemptCount != 2 {
		t.Fatalf("unexpected reclaimed attempt: %+v", second)
	}
	reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, first.ID)
	if err != nil || len(reports) != 1 ||
		reports[0].AttemptNumber != 1 ||
		reports[0].CallCount != 0 ||
		reports[0].ErrorCode != "ATTEMPT_INTERRUPTED" ||
		reports[0].Replayable {
		t.Fatalf("empty expired attempt was not closed before reclaim: reports=%+v err=%v", reports, err)
	}

	lateCall, lateArtifact := providerCallForAttempt(
		models.LLMJobTypeRequirement,
		first.ID,
		1,
		1,
		"late-old-worker-call",
	)
	if err := s.CreateLLMProviderCall(ctx, lateCall, lateArtifact); err == nil {
		t.Fatal("late worker appended a call after its interrupted attempt report")
	}
}

func TestExpiredFinalRequirementAttemptIsNotReclaimedBeyondBudget(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	first := createClaimedRequirementArtifactJob(t, s, ctx, "final-lease-expiry", 1)
	if _, err := s.db.Exec(
		`UPDATE requirement_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute),
		first.ID,
	); err != nil {
		t.Fatal(err)
	}

	if claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute); !errors.Is(err, ErrNoTaskAvailable) {
		t.Fatalf("expired final attempt was reclaimed: claimed=%+v err=%v", claimed, err)
	}
	stored, err := s.GetRequirementJob(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RequirementJobFailed ||
		stored.AttemptCount != 1 ||
		stored.MaxAttempts != 1 ||
		stored.ErrorCode != "ATTEMPT_INTERRUPTED" {
		t.Fatalf("expired final attempt exceeded its budget: %+v", stored)
	}
	reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, first.ID)
	if err != nil || len(reports) != 1 ||
		reports[0].AttemptNumber != 1 ||
		reports[0].ErrorCode != "ATTEMPT_INTERRUPTED" {
		t.Fatalf("expired final attempt report missing: reports=%+v err=%v", reports, err)
	}
}

func TestRequirementJobAndAttemptReportCommitAtomically(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		s := newEncryptedTestStore(t)
		ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
		job := createClaimedRequirementArtifactJob(t, s, ctx, "atomic-success", 1)
		call, callArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 1, "atomic-success")
		if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
			t.Fatal(err)
		}
		report, reportArtifact := attemptReportForJob(models.LLMJobTypeRequirement, job.ID, 1)
		job.Provider = "fake"
		job.Model = "fake-model"
		job.PromptVersion = "test-prompt-v1"
		if err := s.CompleteRequirementJobWithAttemptReport(ctx, job, map[string]any{"ok": true}, report, reportArtifact); err != nil {
			t.Fatal(err)
		}
		storedJob, err := s.GetRequirementJob(ctx, job.ID)
		if err != nil || storedJob.Status != models.RequirementJobCompleted {
			t.Fatalf("job completion was not committed: job=%+v err=%v", storedJob, err)
		}
		reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, job.ID)
		if err != nil || len(reports) != 1 || reports[0].Outcome != models.LLMAttemptSucceeded {
			t.Fatalf("matching report was not committed: reports=%+v err=%v", reports, err)
		}
	})

	t.Run("job transition failure rolls back report", func(t *testing.T) {
		s := newEncryptedTestStore(t)
		ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
		job := createClaimedRequirementArtifactJob(t, s, ctx, "atomic-rollback", 1)
		call, callArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 1, "atomic-rollback")
		if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRequirementJob(ctx, job, map[string]any{"already": "completed"}); err != nil {
			t.Fatal(err)
		}
		report, reportArtifact := attemptReportForJob(models.LLMJobTypeRequirement, job.ID, 1)
		report.Outcome = models.LLMAttemptFailed
		report.Replayable = false
		report.ErrorCode = "PROVIDER_UNAVAILABLE"
		if err := s.FailRequirementJobWithAttemptReport(
			ctx,
			job,
			"PROVIDER_UNAVAILABLE",
			"provider unavailable",
			0,
			report,
			reportArtifact,
		); !errors.Is(err, ErrRequirementJobState) {
			t.Fatalf("expected transition failure, got %v", err)
		}
		reports, err := s.ListLLMAttemptReports(ctx, models.LLMJobTypeRequirement, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(reports) != 0 {
			t.Fatalf("report insert escaped rolled-back transition: %+v", reports)
		}
	})
}

func TestLLMAttemptArtifactParentTriggersCoverDSLJobs(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	workflow, job := createClaimedDSLWorkflow(t, s, ctx, "artifact-parent")
	call, callArtifact := providerCallForAttempt(models.LLMJobTypeDSL, job.ID, 1, 1, "dsl-provider-output")
	if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
		t.Fatal(err)
	}
	if call.RecordingID != workflow.RecordingID {
		t.Fatalf("dsl provider call did not derive workflow recording: %+v", call)
	}

	createRequirementTestRecording(t, s, ctx, "unrelated-artifact-recording")
	_, err := s.db.Exec(`
		INSERT INTO llm_provider_calls (
			id, workspace_id, recording_id, job_type, job_id, attempt_number,
			call_index, phase, chunk_index, chunk_count, provider, model,
			prompt_version, request_hash, response_hash, response_id,
			finish_reason, input_tokens, output_tokens, cache_hit,
			original_bytes, original_bytes_exact, captured_bytes, artifact_bytes,
			redacted, truncated, replayable, artifact, artifact_hash, created_at
		)
		SELECT
			'wrong-dsl-parent', workspace_id, 'unrelated-artifact-recording',
			job_type, job_id, attempt_number, call_index + 1, phase, chunk_index,
			chunk_count, provider, model, prompt_version, request_hash,
			response_hash, response_id, finish_reason, input_tokens,
			output_tokens, cache_hit, original_bytes, original_bytes_exact,
			captured_bytes, artifact_bytes, redacted, truncated, replayable,
			artifact, artifact_hash, created_at
		FROM llm_provider_calls WHERE id = ?
	`, call.ID)
	if err == nil || !strings.Contains(err.Error(), "dsl job attempt not found in workspace") {
		t.Fatalf("dsl parent trigger accepted unrelated recording: %v", err)
	}
	report, reportArtifact := attemptReportForJob(models.LLMJobTypeDSL, job.ID, 1)
	if err := s.CreateLLMAttemptReport(ctx, report, reportArtifact); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingDeletionPurgesLLMAttemptArtifacts(t *testing.T) {
	t.Run("direct requirement recording deletion", func(t *testing.T) {
		s := newEncryptedTestStore(t)
		ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
		job := createClaimedRequirementArtifactJob(t, s, ctx, "delete", 1)
		call, callArtifact := providerCallForAttempt(models.LLMJobTypeRequirement, job.ID, 1, 1, "delete-me")
		if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
			t.Fatal(err)
		}
		report, reportArtifact := attemptReportForJob(models.LLMJobTypeRequirement, job.ID, 1)
		if err := s.CreateLLMAttemptReport(ctx, report, reportArtifact); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRecording(ctx, job.RecordingID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetLLMProviderCall(ctx, call.ID); !errors.Is(err, ErrLLMProviderCallNotFound) {
			t.Fatalf("recording deletion retained provider call: %v", err)
		}
		if _, err := s.GetLLMAttemptReport(ctx, report.ID); !errors.Is(err, ErrLLMAttemptReportNotFound) {
			t.Fatalf("recording deletion retained attempt report: %v", err)
		}
	})

	t.Run("expired dsl recording deletion", func(t *testing.T) {
		s := newEncryptedTestStore(t)
		ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
		workflow, job := createClaimedDSLWorkflow(t, s, ctx, "artifact-expiry")
		call, callArtifact := providerCallForAttempt(models.LLMJobTypeDSL, job.ID, 1, 1, "expire-me")
		if err := s.CreateLLMProviderCall(ctx, call, callArtifact); err != nil {
			t.Fatal(err)
		}
		report, reportArtifact := attemptReportForJob(models.LLMJobTypeDSL, job.ID, 1)
		if err := s.CreateLLMAttemptReport(ctx, report, reportArtifact); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, err := s.db.Exec(`UPDATE recordings SET expires_at = ? WHERE id = ?`, now.Add(-time.Minute), workflow.RecordingID); err != nil {
			t.Fatal(err)
		}
		deleted, err := s.DeleteExpiredRecordings(ctx, now, 10)
		if err != nil || deleted != 1 {
			t.Fatalf("unexpected recording expiration: deleted=%d err=%v", deleted, err)
		}
		for _, table := range []string{"llm_provider_calls", "llm_attempt_reports"} {
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE recording_id = ?`, workflow.RecordingID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("recording expiration retained %d rows in %s", count, table)
			}
		}
	})
}

func TestLLMAttemptArtifactJSONMetadataRemainsStable(t *testing.T) {
	call := &models.LLMProviderCall{
		JobType:  models.LLMJobTypeDSL,
		Artifact: map[string]any{"secret": "must-not-serialize"},
	}
	data, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-serialize")) {
		t.Fatalf("internal provider artifact leaked through model JSON: %s", data)
	}
	report := &models.LLMAttemptReport{
		JobType:  models.LLMJobTypeRequirement,
		Artifact: map[string]any{"secret": "must-not-serialize"},
	}
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-serialize")) {
		t.Fatalf("internal attempt report leaked through model JSON: %s", data)
	}
}
