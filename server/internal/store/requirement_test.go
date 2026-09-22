package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func createRequirementTestRecording(t *testing.T, s *Store, ctx context.Context, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.CreateRecording(ctx, recordingMetadata(id, now), []byte(`{"snapshots":[{"html":"<main>safe</main>"}]}`), 1024*1024); err != nil {
		t.Fatal(err)
	}
}

func requirementSpec() models.CollectionRequirementSpec {
	return models.CollectionRequirementSpec{
		Title:       "Collect product prices",
		Description: "Collect the visible product name and price.",
		RequiredInputs: []models.RequirementInput{
			{Name: "keyword", Type: models.RequirementValueString, Description: "Search keyword"},
		},
		OptionalInputs: []models.RequirementInput{},
		OutputFields: []models.RequirementOutputField{
			{Name: "name", Type: models.RequirementValueString, Description: "Product name"},
			{Name: "price", Type: models.RequirementValueNumber, Description: "Product price"},
		},
		SampleOutput: map[string]any{"name": "Example", "price": 10.5},
	}
}

func requirementMark(t *testing.T, note string) models.PageMark {
	t.Helper()
	actionIndex, sequence := 1, 2
	return models.PageMark{
		ID: "mark-price", CanonicalID: "m_testprice", Timestamp: 1, URL: "https://example.test/list", Role: models.PageMarkRoleField,
		Note: note, ActionIndex: &actionIndex, SnapshotSequence: &sequence,
		Element: models.PageMarkElement{
			Index: 1, TagName: "span", Selector: ".price", StableSelector: ".price",
			Text: "$10", BoundingRect: models.PageMarkRect{X: 1, Y: 2, Width: 3, Height: 4},
		},
	}
}

func TestRequirementJobArtifactsAreEncryptedScopedAndRetryable(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctxA := workspaceContext("alice", authz.DefaultWorkspaceID)
	ctxB := workspaceContext("bob", "tenant-b")
	if err := s.CreateWorkspace(ctxA, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	createRequirementTestRecording(t, s, ctxA, "recording-requirements")

	job := &models.RequirementJob{
		ID: "requirement-job-a", RecordingID: "recording-requirements",
		Kind: models.RequirementJobCandidates, PromptVersion: "requirements-v1", MaxAttempts: 2,
	}
	request := map[string]any{"mode": "candidates", "marker": "request-plaintext-marker"}
	if err := s.CreateRequirementJob(ctxA, job, request); err != nil {
		t.Fatal(err)
	}
	var requestArtifact []byte
	if err := s.db.QueryRow(`SELECT request_artifact FROM requirement_jobs WHERE id = ?`, job.ID).Scan(&requestArtifact); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(requestArtifact, []byte("request-plaintext-marker")) {
		t.Fatal("requirement request was stored in plaintext")
	}
	if _, err := s.GetRequirementJob(ctxB, job.ID); !errors.Is(err, ErrRequirementJobNotFound) {
		t.Fatalf("cross-workspace job lookup should be hidden, got %v", err)
	}

	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID || claimed.WorkspaceID != authz.DefaultWorkspaceID || claimed.AttemptCount != 1 {
		t.Fatalf("unexpected claimed job: %+v", claimed)
	}
	claimedRequest, ok := claimed.Request.(map[string]any)
	if !ok || claimedRequest["marker"] != "request-plaintext-marker" {
		t.Fatalf("worker request was not decrypted: %#v", claimed.Request)
	}
	if err := s.FailRequirementJob(ctxA, claimed, "PROVIDER_UNAVAILABLE", "candidate generation is temporarily unavailable", 0); err != nil {
		t.Fatal(err)
	}
	retryable, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if retryable.AttemptCount != 2 {
		t.Fatalf("expected second bounded attempt, got %d", retryable.AttemptCount)
	}
	retryable.Provider = "openai"
	retryable.Model = "test-model"
	retryable.ChunkCount = 2
	retryable.InputTokens = 100
	retryable.OutputTokens = 50
	result := map[string]any{"candidates": []any{map[string]any{"marker": "result-plaintext-marker"}}}
	if err := s.CompleteRequirementJob(ctxA, retryable, result); err != nil {
		t.Fatal(err)
	}
	var resultArtifact []byte
	if err := s.db.QueryRow(`SELECT result_artifact FROM requirement_jobs WHERE id = ?`, job.ID).Scan(&resultArtifact); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(resultArtifact, []byte("result-plaintext-marker")) {
		t.Fatal("requirement result was stored in plaintext")
	}
	completed, err := s.GetRequirementJob(ctxA, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.ResultHash == "" || completed.ChunkCount != 2 {
		t.Fatalf("unexpected completed job: %+v", completed)
	}
	if err := s.DeleteRecording(ctxA, job.RecordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRequirementJob(ctxA, job.ID); !errors.Is(err, ErrRequirementJobNotFound) {
		t.Fatalf("recording deletion retained derived job artifacts: %v", err)
	}
}

func TestCollectionRequirementIsEncryptedImmutableAndConfirmable(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-draft")
	requirement := &models.CollectionRequirement{
		ID: "requirement-a", RecordingID: "recording-draft", Source: models.RequirementSourceManual,
		Requirement: requirementSpec(),
	}
	if err := s.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	var artifact []byte
	if err := s.db.QueryRow(`SELECT content_artifact FROM collection_requirements WHERE id = ?`, requirement.ID).Scan(&artifact); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(artifact, []byte("Collect product prices")) {
		t.Fatal("normalized requirement was stored in plaintext")
	}
	got, err := s.GetCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requirement.Title != requirement.Requirement.Title || got.Status != models.CollectionRequirementDraft {
		t.Fatalf("unexpected requirement round trip: %+v", got)
	}
	confirmed, err := s.ConfirmCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != models.CollectionRequirementConfirmed || confirmed.ConfirmedBy != "alice" || confirmed.ConfirmedAt == nil {
		t.Fatalf("unexpected confirmed requirement: %+v", confirmed)
	}
	// Confirming an already-confirmed requirement is idempotent: clients
	// (the intent wizard after a back-navigation) must resume, not dead-end.
	repeat, err := s.ConfirmCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("repeat confirm should be idempotent, got %v", err)
	}
	if repeat.Status != models.CollectionRequirementConfirmed {
		t.Fatalf("repeat confirm returned non-confirmed requirement: %+v", repeat)
	}
	if repeat.ConfirmedBy != confirmed.ConfirmedBy || repeat.ConfirmedAt == nil || !repeat.ConfirmedAt.Equal(*confirmed.ConfirmedAt) {
		t.Fatalf("repeat confirm rewrote confirmation metadata: first=%+v repeat=%+v", confirmed, repeat)
	}
	if _, err := s.db.Exec(`UPDATE collection_requirements SET content_hash = 'tampered' WHERE id = ?`, requirement.ID); err == nil {
		t.Fatal("immutable requirement content accepted an update")
	}
	if err := s.DeleteRecording(ctx, requirement.RecordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCollectionRequirement(ctx, requirement.ID); !errors.Is(err, ErrRequirementNotFound) {
		t.Fatalf("recording deletion retained derived requirement content: %v", err)
	}
}

func TestCollectionRequirementPersistsMarksInContentArtifact(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-marks")
	requirement := &models.CollectionRequirement{
		ID: "requirement-marks", RecordingID: "recording-marks", Source: models.RequirementSourceManual,
		Requirement: requirementSpec(), Marks: []models.PageMark{requirementMark(t, "price field")},
	}
	if err := s.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Marks) != 1 || got.Marks[0].Note != "price field" || got.Marks[0].CanonicalID == "" {
		t.Fatalf("marks did not round-trip in requirement content: %#v", got.Marks)
	}
	changed := &models.CollectionRequirement{
		ID: "requirement-marks-note", RecordingID: "recording-marks", Source: models.RequirementSourceManual,
		Requirement: requirementSpec(), Marks: []models.PageMark{requirementMark(t, "different note")},
	}
	if err := s.CreateCollectionRequirement(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if changed.ContentHash == requirement.ContentHash {
		t.Fatal("mark note changes must affect immutable requirement content hash")
	}
}

func TestCollectionRequirementReadsLegacyBareSpecArtifacts(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-legacy-requirement")
	requirement := &models.CollectionRequirement{
		ID: "requirement-legacy", RecordingID: "recording-legacy-requirement", Source: models.RequirementSourceManual,
		Requirement: requirementSpec(),
	}
	if err := s.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	legacyArtifact, legacyHash, err := s.sealRequirementArtifact(requirement.WorkspaceID, "collection-requirement", requirement.ID, "content", requirementSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE collection_requirements SET content_artifact = ?, content_hash = ? WHERE id = ?`, legacyArtifact, legacyHash, requirement.ID); err == nil {
		t.Fatal("direct content update should be blocked by immutability trigger")
	}
	if _, err := s.db.Exec(`DROP TRIGGER trg_collection_requirements_immutable_content`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE collection_requirements SET content_artifact = ?, content_hash = ? WHERE id = ?`, legacyArtifact, legacyHash, requirement.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCollectionRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requirement.Title != requirement.Requirement.Title || len(got.Marks) != 0 {
		t.Fatalf("legacy bare spec did not read as requirement without marks: %+v", got)
	}
}

func TestUpdateRequirementJobProgressTracksRunningJobOnly(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-progress")
	job := &models.RequirementJob{
		ID: "requirement-job-progress", RecordingID: "recording-progress",
		Kind: models.RequirementJobCandidates, PromptVersion: "requirements-v1",
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"kind": "candidates"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRequirementJobProgress(ctx, job.ID, 0, 4, 1); !errors.Is(err, ErrRequirementJobState) {
		t.Fatalf("pending job must not accept progress: %v", err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRequirementJobProgress(ctx, claimed.ID, claimed.AttemptCount, 4, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRequirementJobProgress(ctx, claimed.ID, claimed.AttemptCount, 4, 2); err != nil {
		t.Fatal(err)
	}
	running, err := s.GetRequirementJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.ChunkCount != 4 || running.CompletedChunks != 2 || running.Status != models.RequirementJobRunning {
		t.Fatalf("unexpected running progress: %+v", running)
	}
	claimed.ChunkCount = 4
	claimed.CompletedChunks = 4
	if err := s.CompleteRequirementJob(ctx, claimed, map[string]any{"candidates": []any{}}); err != nil {
		t.Fatal(err)
	}
	completed, err := s.GetRequirementJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.ChunkCount != 4 || completed.CompletedChunks != 4 {
		t.Fatalf("completion must persist matching totals: %+v", completed)
	}
	if err := s.UpdateRequirementJobProgress(ctx, job.ID, claimed.AttemptCount, 4, 1); !errors.Is(err, ErrRequirementJobState) {
		t.Fatalf("terminal job must not accept progress: %v", err)
	}
}

func TestReclaimedRequirementJobRejectsStaleWorkerTransitions(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-stale-worker")
	job := &models.RequirementJob{
		ID: "requirement-job-stale-worker", RecordingID: "recording-stale-worker",
		Kind: models.RequirementJobNormalize, PromptVersion: "requirements-v1",
		MaxAttempts: 2,
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"customText": "Collect headings"}); err != nil {
		t.Fatal(err)
	}
	stale, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE requirement_jobs SET lease_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute),
		stale.ID,
	); err != nil {
		t.Fatal(err)
	}
	current, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if current.AttemptCount != stale.AttemptCount+1 {
		t.Fatalf("job was not reclaimed: stale=%+v current=%+v", stale, current)
	}

	if err := s.UpdateRequirementJobProgress(
		ctx,
		stale.ID,
		stale.AttemptCount,
		2,
		1,
	); !errors.Is(err, ErrRequirementJobState) {
		t.Fatalf("stale worker updated current progress: %v", err)
	}
	if err := s.CompleteRequirementJob(
		ctx,
		stale,
		map[string]any{"requirement": requirementSpec()},
	); !errors.Is(err, ErrRequirementJobState) {
		t.Fatalf("stale worker completed current attempt: %v", err)
	}
	if err := s.FailRequirementJob(
		ctx,
		stale,
		"STALE_FAILURE",
		"stale worker failure",
		0,
	); !errors.Is(err, ErrRequirementJobState) {
		t.Fatalf("stale worker failed current attempt: %v", err)
	}
	stored, err := s.GetRequirementJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RequirementJobRunning ||
		stored.AttemptCount != current.AttemptCount ||
		stored.ChunkCount != current.ChunkCount {
		t.Fatalf("stale worker mutated reclaimed job: %+v", stored)
	}
}

func TestRequirementJobManualRetryRequiresTerminalFailure(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-retry")
	job := &models.RequirementJob{
		ID: "requirement-job-retry", RecordingID: "recording-retry", Kind: models.RequirementJobNormalize,
		PromptVersion: "requirements-v1", MaxAttempts: 1,
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"customText": "Collect headings"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryRequirementJob(ctx, job.ID); !errors.Is(err, ErrRequirementJobState) {
		t.Fatalf("pending job should not be manually retryable: %v", err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailRequirementJob(ctx, claimed, "PROVIDER_UNAVAILABLE", "temporarily unavailable", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryRequirementJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := s.GetRequirementJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != models.RequirementJobPending || retried.AttemptCount != 1 || retried.MaxAttempts != 2 {
		t.Fatalf("unexpected retried job: %+v", retried)
	}
}

func TestRequirementJobManualRetriesGrantFixedLinearAttemptBatches(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-linear-retry")
	job := &models.RequirementJob{
		ID: "requirement-job-linear-retry", RecordingID: "recording-linear-retry",
		Kind: models.RequirementJobNormalize, PromptVersion: "requirements-v1",
		MaxAttempts: 2,
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"customText": "Collect headings"}); err != nil {
		t.Fatal(err)
	}
	exhaustBatch := func(expectedFirst, expectedLast int) {
		t.Helper()
		for attempt := expectedFirst; attempt <= expectedLast; attempt++ {
			claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if claimed.AttemptCount != attempt || claimed.AttemptBudget != 2 {
				t.Fatalf("unexpected claimed attempt: %+v", claimed)
			}
			if err := s.FailRequirementJob(ctx, claimed, "PROVIDER_UNAVAILABLE", "temporarily unavailable", 0); err != nil {
				t.Fatal(err)
			}
		}
	}

	exhaustBatch(1, 2)
	for retry, expectedMax := range []int{4, 6} {
		if err := s.RetryRequirementJob(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		retried, err := s.GetRequirementJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if retried.MaxAttempts != expectedMax || retried.AttemptBudget != 2 {
			t.Fatalf("manual retry %d changed the fixed batch: %+v", retry+1, retried)
		}
		exhaustBatch(expectedMax-1, expectedMax)
	}
}

func TestRequirementJobEarlyTerminalRetriesGrantOnlyOneFixedAttemptBatch(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	createRequirementTestRecording(t, s, ctx, "recording-early-terminal-retry")
	job := &models.RequirementJob{
		ID: "requirement-job-early-terminal-retry", RecordingID: "recording-early-terminal-retry",
		Kind: models.RequirementJobNormalize, PromptVersion: "requirements-v1",
		MaxAttempts: 3,
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"customText": "Collect headings"}); err != nil {
		t.Fatal(err)
	}

	for expectedAttempt := 1; expectedAttempt <= 3; expectedAttempt++ {
		claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claimed.AttemptCount != expectedAttempt || claimed.AttemptBudget != 3 {
			t.Fatalf("unexpected claimed attempt: %+v", claimed)
		}
		// This mirrors the manager's fail-closed terminal transition without
		// mutating the durable maximum from the original batch.
		claimed.MaxAttempts = claimed.AttemptCount
		if err := s.FailRequirementJob(ctx, claimed, "PROVIDER_OUTPUT_INVALID", "terminal provider output", 0); err != nil {
			t.Fatal(err)
		}
		if err := s.RetryRequirementJob(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		retried, err := s.GetRequirementJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		expectedMax := expectedAttempt + 3
		if retried.MaxAttempts != expectedMax || retried.AttemptBudget != 3 {
			t.Fatalf("early terminal retry %d granted a growing batch: %+v", expectedAttempt, retried)
		}
	}
}

func TestExpiredRecordingDeletesDerivedRequirementArtifacts(t *testing.T) {
	s := newEncryptedTestStore(t)
	ctx := workspaceContext("alice", authz.DefaultWorkspaceID)
	now := time.Now().UTC()
	recording := recordingMetadata("recording-expired-requirements", now)
	recording.ExpiresAt = now.Add(-time.Minute)
	if err := s.CreateRecording(ctx, recording, []byte(`{"snapshots":[{},{}]}`), 1024*1024); err != nil {
		t.Fatal(err)
	}
	job := &models.RequirementJob{
		ID: "expired-requirement-job", RecordingID: recording.ID,
		Kind: models.RequirementJobNormalize, PromptVersion: "requirements-v1",
	}
	if err := s.CreateRequirementJob(ctx, job, map[string]any{"requirement": requirementSpec()}); err != nil {
		t.Fatal(err)
	}
	requirement := &models.CollectionRequirement{
		ID: "expired-requirement", RecordingID: recording.ID, SourceJobID: job.ID,
		Source: models.RequirementSourceManual, Requirement: requirementSpec(),
	}
	if err := s.CreateCollectionRequirement(ctx, requirement); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteExpiredRecordings(ctx, now, 10)
	if err != nil || deleted != 1 {
		t.Fatalf("unexpected expiration result: deleted=%d err=%v", deleted, err)
	}
	if _, err := s.GetRequirementJob(ctx, job.ID); !errors.Is(err, ErrRequirementJobNotFound) {
		t.Fatalf("expiration retained requirement job: %v", err)
	}
	if _, err := s.GetCollectionRequirement(ctx, requirement.ID); !errors.Is(err, ErrRequirementNotFound) {
		t.Fatalf("expiration retained normalized requirement: %v", err)
	}
}
