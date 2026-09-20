package requirement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func candidateResponseWithProviderOnlyField(t *testing.T, marker string) string {
	t.Helper()
	var response struct {
		Candidates []models.RequirementCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(candidateResponse(t)), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Candidates) > 0 {
		response.Candidates[0].Requirement.Description += " " + marker
	}
	encoded, err := json.Marshal(struct {
		Candidates []models.RequirementCandidate `json:"candidates"`
	}{response.Candidates})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestRequirementManagerCapturesSuccessfulProviderAttempt(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	const providerOnlyMarker = "provider-only-history-marker"
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{
				Content:     candidateResponseWithProviderOnlyField(t, providerOnlyMarker),
				InputTokens: 50, OutputTokens: 25,
				ResponseID: "response-123", FinishReason: "stop",
			},
			Provider: "primary", Model: "test-model", CacheHit: true,
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil || completed.Status != models.RequirementJobCompleted {
		t.Fatalf("candidate job did not complete: job=%+v err=%v", completed, err)
	}
	pollJSON, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pollJSON), "rawResponse") {
		t.Fatalf("ordinary job polling leaked raw provider content: %s", pollJSON)
	}

	attempts, err := manager.ListProviderAttempts(context.Background(), job.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("unexpected retained attempts: attempts=%+v err=%v", attempts, err)
	}
	if attempts[0].Outcome != models.LLMAttemptSucceeded || !attempts[0].Replayable ||
		attempts[0].InputTokens != 50 || attempts[0].OutputTokens != 25 {
		t.Fatalf("unexpected successful attempt metadata: %+v", attempts[0])
	}
	detail, err := manager.GetProviderAttempt(context.Background(), job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Calls) != 1 || detail.Calls[0].Phase != llm.CompletionPhaseFinal ||
		!detail.Calls[0].CacheHit || detail.Calls[0].ResponseID != "response-123" ||
		detail.Calls[0].FinishReason != "stop" ||
		detail.Calls[0].CallKind != models.LLMProviderCallKindCacheHit ||
		detail.Calls[0].ProviderAttempt != 0 ||
		!detail.Calls[0].OriginalBytesExact {
		t.Fatalf("unexpected provider call lineage: %+v", detail.Calls)
	}
	call, err := manager.GetProviderCall(context.Background(), job.ID, 1, detail.Calls[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	callArtifact, ok := call.Artifact.(map[string]any)
	if !ok || !strings.Contains(callArtifact["content"].(string), providerOnlyMarker) {
		t.Fatalf("provider response was not recoverable from encrypted history: %#v", call.Artifact)
	}
	foreignContext := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "other", WorkspaceID: "tenant-b", Roles: []authz.Role{authz.RoleAdmin},
	})
	if _, err := manager.ListProviderAttempts(foreignContext, job.ID); !errors.Is(err, store.ErrRequirementJobNotFound) {
		t.Fatalf("cross-workspace attempt lookup should be hidden, got %v", err)
	}
}

func TestRequirementManagerCapturesMalformedAndUnsafeProviderOutput(t *testing.T) {
	tests := []struct {
		name    string
		content func(*testing.T) string
	}{
		{name: "malformed", content: func(*testing.T) string { return `{"candidates":` }},
		{name: "unsafe", content: unsafeCandidateResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newManagerTestStore(t)
			cfg := &config.Config{
				LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
				LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
			}
			recordingID := createManagerRecording(t, s, cfg)
			content := test.content(t)
			fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
				return &llm.CompletionResult{
					CompletionResponse: &llm.CompletionResponse{Content: content, InputTokens: 10, OutputTokens: 5},
					Provider:           "primary", Model: "test-model",
				}, nil
			}}
			manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
			job, err := manager.SubmitCandidates(context.Background(), recordingID)
			if err != nil {
				t.Fatal(err)
			}
			manager.ProcessOnce(context.Background())
			failed, err := manager.GetJob(context.Background(), job.ID)
			if err != nil || failed.Status != models.RequirementJobFailed || failed.ErrorCode != "PROVIDER_OUTPUT_INVALID" {
				t.Fatalf("provider validation failure was not terminally recorded: job=%+v err=%v", failed, err)
			}
			attempts, err := manager.ListProviderAttempts(context.Background(), job.ID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("provider failure artifact missing: attempts=%+v err=%v", attempts, err)
			}
			expectReplayable := test.name == "malformed"
			if attempts[0].Outcome != models.LLMAttemptFailed || attempts[0].ErrorCode != "PROVIDER_OUTPUT_INVALID" ||
				attempts[0].ValidationPhase != "provider-output-validation" ||
				attempts[0].Replayable != expectReplayable {
				t.Fatalf("unexpected provider failure report: %+v", attempts[0])
			}
			detail, err := manager.GetProviderAttempt(context.Background(), job.ID, 1)
			if err != nil || len(detail.Calls) != 1 {
				t.Fatalf("provider failure call lineage missing: detail=%+v err=%v", detail, err)
			}
		})
	}
}

func TestRequirementManagerCapturesPartialMultiChunkProviderFailure(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 80,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	service := platformrecording.NewService(s, cfg)
	large := strings.Repeat("visible semantic text ", 40)
	recording, err := service.Create(context.Background(), platformrecording.CreateInput{Payload: map[string]any{
		"version": "2",
		"snapshots": []any{
			map[string]any{"actionIndex": 0, "dom": large + "first"},
			map[string]any{"actionIndex": 1, "dom": large + "second"},
			map[string]any{"actionIndex": 2, "dom": large + "third"},
		},
		"events": []any{map[string]any{"type": "click"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("provider failed after the first chunk")
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: `{"summary":"first chunk"}`, InputTokens: 10, OutputTokens: 5},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	attempts, err := manager.ListProviderAttempts(context.Background(), job.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("partial attempt report missing: attempts=%+v err=%v", attempts, err)
	}
	if attempts[0].CallCount != 2 || attempts[0].Replayable || attempts[0].ValidationPhase != "provider-completion" {
		t.Fatalf("partial attempt was not marked non-replayable: %+v", attempts[0])
	}
	detail, err := manager.GetProviderAttempt(context.Background(), job.ID, 1)
	if err != nil || len(detail.Calls) != 2 ||
		detail.Calls[0].Phase != llm.CompletionPhaseAnalysis ||
		detail.Calls[1].CallKind != models.LLMProviderCallKindError {
		t.Fatalf("partial call lineage was not retained: detail=%+v err=%v", detail, err)
	}
}

func TestRequirementManagerCapturesProviderFailureWithoutResponse(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	const rawProviderError = `{"message":"provider unavailable","apiToken":"provider-secret"}`
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return nil, errors.New(rawProviderError)
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())

	attempts, err := manager.ListProviderAttempts(context.Background(), job.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("provider failure report missing: attempts=%+v err=%v", attempts, err)
	}
	if attempts[0].Outcome != models.LLMAttemptFailed ||
		attempts[0].CallCount != 1 ||
		attempts[0].ValidationPhase != "provider-completion" {
		t.Fatalf("unexpected provider failure report: %+v", attempts[0])
	}
	detail, err := manager.GetProviderAttempt(context.Background(), job.ID, 1)
	if err != nil || len(detail.Calls) != 1 ||
		detail.Calls[0].CallKind != models.LLMProviderCallKindError ||
		!detail.Calls[0].Redacted ||
		detail.Calls[0].Replayable ||
		detail.Calls[0].OriginalBytes != 0 {
		t.Fatalf("provider failure call missing: detail=%+v err=%v", detail, err)
	}
	call, err := manager.GetProviderCall(context.Background(), job.ID, 1, detail.Calls[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	artifact := call.Artifact.(map[string]any)
	content := artifact["content"].(string)
	errorMessage := artifact["errorMessage"].(string)
	if content != "" || strings.Contains(errorMessage, "provider-secret") ||
		!strings.Contains(errorMessage, "[REDACTED]") {
		t.Fatalf("provider error metadata was not defensively redacted: %#v", artifact)
	}
}

func TestRequirementCaptureRedactsAndBoundsProviderResponses(t *testing.T) {
	large := strings.Repeat(`"quoted\\provider-content"`, 8_000)
	artifact, capturedBytes, truncated, err := boundedCompletionArtifact(large, requirementAnalysisArtifactLimit)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(encoded) > requirementAnalysisArtifactLimit || capturedBytes >= len(large) ||
		!strings.Contains(artifact.Content, "[TRUNCATED]") || artifact.ContentHash != hashCompletionContent(large) {
		t.Fatalf("analysis response bound failed: encoded=%d captured=%d original=%d artifact=%+v", len(encoded), capturedBytes, len(large), artifact)
	}

	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	content := candidateResponseWithProviderOnlyField(t,
		"token: provider-secret "+strings.Repeat("large-provider-output ", 60_000))
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: content},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	detail, err := manager.GetProviderAttempt(context.Background(), job.ID, 1)
	if err != nil || len(detail.Calls) != 1 {
		t.Fatalf("bounded attempt missing: detail=%+v err=%v", detail, err)
	}
	call := detail.Calls[0]
	if !call.Redacted || !call.Truncated || call.Replayable || detail.Report.Replayable ||
		call.ArtifactBytes > requirementFinalArtifactLimit {
		t.Fatalf("redacted/truncated response was treated as replayable: call=%+v report=%+v", call, detail.Report)
	}
	opened, err := manager.GetProviderCall(context.Background(), job.ID, 1, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	value := opened.Artifact.(map[string]any)["content"].(string)
	if strings.Contains(value, "provider-secret") || !strings.Contains(value, "[REDACTED]") {
		t.Fatalf("defensive response redaction failed: %q", value[:min(len(value), 500)])
	}
}

func TestRequirementCapturePreservesRawLengthWhenRedactionExpandsContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "plain", content: "token:x"},
		{name: "json", content: `{"apiToken":"x"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newManagerTestStore(t)
			cfg := &config.Config{
				LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
				LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
			}
			recordingID := createManagerRecording(t, s, cfg)
			fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
				return &llm.CompletionResult{
					CompletionResponse: &llm.CompletionResponse{Content: test.content},
					Provider:           "primary", Model: "test-model",
				}, nil
			}}
			manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
			job, err := manager.SubmitCandidates(context.Background(), recordingID)
			if err != nil {
				t.Fatal(err)
			}
			manager.ProcessOnce(context.Background())
			detail, err := manager.GetProviderAttempt(context.Background(), job.ID, 1)
			if err != nil || len(detail.Calls) != 1 {
				t.Fatalf("expanded redaction was not retained: detail=%+v err=%v", detail, err)
			}
			call := detail.Calls[0]
			if !call.Redacted ||
				call.OriginalBytes != len(test.content) ||
				call.CapturedBytes <= call.OriginalBytes {
				t.Fatalf("raw/captured byte accounting is incorrect: %+v", call)
			}
			opened, err := manager.GetProviderCall(context.Background(), job.ID, 1, call.ID)
			if err != nil {
				t.Fatal(err)
			}
			content := opened.Artifact.(map[string]any)["content"].(string)
			if strings.Contains(content, `"x"`) || strings.Contains(content, "token:x") ||
				!strings.Contains(content, "[REDACTED]") {
				t.Fatalf("short secret was not redacted: %q", content)
			}
		})
	}
}

func TestRequirementCapturePreservesUpstreamTruncationProvenance(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	job := &models.RequirementJob{
		ID: "upstream-truncation-job", RecordingID: recordingID,
		Kind: models.RequirementJobCandidates, Status: models.RequirementJobPending,
		Source: models.RequirementSourceLLM, PromptVersion: PromptVersion,
		MaxAttempts: 1,
	}
	if err := s.CreateRequirementJob(context.Background(), job, map[string]any{"kind": "candidates"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recorder := newRequirementAttemptRecorder(s, claimed, "recording-hash")
	const originalBytes = 64<<10 + 1
	content := strings.Repeat("x", 64<<10) + "\n...[TRUNCATED]..."
	if err := recorder.CaptureCompletion(context.Background(), llm.CompletionTraceEvent{
		Metadata:           llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
		RequestHash:        "request-hash",
		Result:             &llm.CompletionResult{CompletionResponse: &llm.CompletionResponse{Content: content}, Provider: "provider", Model: "model"},
		BoundaryKind:       llm.CompletionBoundaryProviderError,
		ProviderAttempt:    1,
		ErrorCode:          "http_error",
		ErrorMessage:       "provider failed",
		OriginalBytes:      originalBytes,
		OriginalBytesExact: false,
		ContentTruncated:   true,
	}); err != nil {
		t.Fatal(err)
	}
	calls, err := s.ListLLMProviderCalls(context.Background(), models.LLMJobTypeRequirement, claimed.ID, 1)
	if err != nil || len(calls) != 1 {
		t.Fatalf("truncated call was not retained: calls=%+v err=%v", calls, err)
	}
	if !calls[0].Truncated || calls[0].Replayable ||
		calls[0].OriginalBytes != originalBytes ||
		calls[0].OriginalBytesExact ||
		calls[0].CapturedBytes <= calls[0].OriginalBytes {
		t.Fatalf("upstream truncation provenance was lost: %+v", calls[0])
	}
	opened, err := s.GetLLMProviderCall(context.Background(), calls[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	artifact := opened.Artifact.(map[string]any)
	if artifact["truncated"] != true ||
		!strings.Contains(artifact["content"].(string), "[TRUNCATED]") {
		t.Fatalf("truncated artifact marker was lost: %#v", artifact)
	}
}

func TestRequirementAttemptReportRedactsParsedResult(t *testing.T) {
	recorder := &requirementAttemptRecorder{
		job: &models.RequirementJob{
			ID:            "redacted-result-job",
			AttemptCount:  1,
			PromptVersion: PromptVersion,
			RequestHash:   "request-hash",
		},
		recordingHash: "recording-hash",
		calls: []*models.LLMProviderCall{{
			ID:           "call-1",
			CallIndex:    1,
			Phase:        llm.CompletionPhaseFinal,
			Provider:     "fake",
			Model:        "fake-model",
			RequestHash:  "request-hash",
			ResponseHash: "response-hash",
			Replayable:   true,
			InputTokens:  12,
			OutputTokens: 7,
		}},
	}
	report, artifact, runErr := recorder.buildReport(map[string]any{
		"apiToken":     "provider-secret",
		"outputTokens": 7,
	}, nil)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if report.Replayable || !containsString(report.SafetyFlags, "artifact-result-redacted:true") {
		t.Fatalf("redacted parsed result was treated as replayable: %+v", report)
	}
	parsed := artifact.(requirementAttemptArtifact).Result.(map[string]any)
	if parsed["apiToken"] != "[REDACTED]" || parsed["outputTokens"] != float64(7) {
		t.Fatalf("parsed result was not selectively redacted: %#v", parsed)
	}
}

func TestRequirementAttemptReportFallsBackWhenParsedResultExceedsBound(t *testing.T) {
	recorder := &requirementAttemptRecorder{
		job: &models.RequirementJob{
			ID:            "oversized-result-job",
			AttemptCount:  1,
			PromptVersion: PromptVersion,
			RequestHash:   "request-hash",
		},
		recordingHash: "recording-hash",
		calls: []*models.LLMProviderCall{{
			ID:           "call-1",
			CallIndex:    1,
			Phase:        llm.CompletionPhaseFinal,
			Provider:     "fake",
			Model:        "fake-model",
			RequestHash:  "request-hash",
			ResponseHash: "response-hash",
			Replayable:   true,
		}},
	}
	report, artifact, runErr := recorder.buildReport(map[string]any{
		"payload": strings.Repeat("x", store.MaxLLMAttemptReportArtifactBytes+1),
	}, nil)
	if !errors.Is(runErr, llm.ErrCompletionCapture) {
		t.Fatalf("oversized report should fail closed, got %v", runErr)
	}
	if report.Outcome != models.LLMAttemptFailed || report.Replayable ||
		report.ErrorCode != "ARTIFACT_CAPTURE_FAILED" ||
		!containsString(report.SafetyFlags, "artifact-capture:bounded-fallback") {
		t.Fatalf("unexpected fallback report metadata: %+v", report)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > store.MaxLLMAttemptReportArtifactBytes ||
		strings.Contains(string(encoded), strings.Repeat("x", 256)) {
		t.Fatalf("fallback artifact was not bounded: bytes=%d", len(encoded))
	}
	fallback := artifact.(requirementAttemptFallbackArtifact)
	if !fallback.Result.Omitted || fallback.Result.Hash == "" {
		t.Fatalf("fallback did not retain an omission hash: %+v", fallback.Result)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// TestStrictDecoderRejectsProviderOnlyFieldsAndPreventsPollingLeak verifies the
// second leak-prevention vector from the original polling test: provider-only
// content (a top-level field that exists in the raw provider response but is
// not part of the parsed candidate schema) cannot reach the polling payload.
//
// After WI-4, this property is structurally enforced because strictDecodeJSON
// rejects unknown top-level fields, so the parse fails and no candidate data is
// ever stored on the job. This test documents that contract explicitly so a
// future regression that loosens the decoder (or adds a new top-level field to
// the candidate schema without updating polling redaction) is caught.
func TestStrictDecoderRejectsProviderOnlyFieldsAndPreventsPollingLeak(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	// Build a candidate payload whose only marker occurrence lives in a
	// sibling top-level key ("providerNotes") that is not part of the
	// CandidateResult schema. strictDecodeJSON must reject this object before
	// any candidate data is read, so the marker can never flow into polling.
	base := struct {
		Candidates []models.RequirementCandidate `json:"candidates"`
	}{}
	if err := json.Unmarshal([]byte(candidateResponse(t)), &base); err != nil {
		t.Fatal(err)
	}
	const providerOnlyMarker = "provider-only-history-marker"
	leakingContent, err := json.Marshal(map[string]any{
		"candidates":    base.Candidates,
		"providerNotes": "internal context " + providerOnlyMarker,
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: string(leakingContent)},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	failed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil || failed.Status != models.RequirementJobFailed ||
		failed.ErrorCode != "PROVIDER_OUTPUT_INVALID" {
		t.Fatalf("strict-decoder rejection did not terminally fail the job: job=%+v err=%v", failed, err)
	}
	pollJSON, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pollJSON), providerOnlyMarker) {
		t.Fatalf("provider-only marker leaked into polling payload: %s", pollJSON)
	}
	if strings.Contains(string(pollJSON), "rawResponse") {
		t.Fatalf("raw provider response field surfaced in polling payload: %s", pollJSON)
	}
}

func TestRequirementManualRetryKeepsArtifactAttemptNumbersMonotonic(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	callCount := 0
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		callCount++
		content := `{"candidates":`
		if callCount == 2 {
			content = candidateResponse(t)
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: content},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	if err := manager.RetryJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil || completed.Status != models.RequirementJobCompleted ||
		completed.AttemptCount != 2 || completed.MaxAttempts != 2 {
		t.Fatalf("manual retry did not preserve monotonic attempt sequence: job=%+v err=%v", completed, err)
	}
	attempts, err := manager.ListProviderAttempts(context.Background(), job.ID)
	if err != nil || len(attempts) != 2 ||
		attempts[0].AttemptNumber != 1 || attempts[1].AttemptNumber != 2 {
		t.Fatalf("manual retry overwrote attempt history: attempts=%+v err=%v", attempts, err)
	}
}
