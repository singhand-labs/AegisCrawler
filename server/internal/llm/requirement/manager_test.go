package requirement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestClassifyRequirementDispatchErrorsAsTerminalStableCodes(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{&budget.DeniedError{Scope: budget.ScopeGlobal, Limit: budget.LimitDaily}, budget.CodeBudgetExceeded},
		{budget.ErrLedgerUnavailable, budget.CodeLedgerUnavailable},
		{&llm.RouteUnavailableError{Route: "primary", Reason: "usage_contract_violation"}, llm.CodeProviderUnavailable},
	}
	for _, tc := range tests {
		terminal, code, _ := classifyJobError(fmt.Errorf("workflow: %w", tc.err))
		if !terminal || code != tc.code {
			t.Fatalf("classify(%v) = terminal %v code %q, want true %q", tc.err, terminal, code, tc.code)
		}
	}
}

func newManagerTestStore(t *testing.T) *store.Store {
	t.Helper()
	file, err := os.CreateTemp("", "aegis-requirement-manager-*.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	s, err := store.New(file.Name(), "requirement-manager-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func createManagerRecording(t *testing.T, s *store.Store, cfg *config.Config) string {
	t.Helper()
	service := platformrecording.NewService(s, cfg)
	recording, err := service.Create(context.Background(), platformrecording.CreateInput{Payload: map[string]any{
		"version": "2",
		"snapshots": []any{
			map[string]any{"timestamp": 1, "domTree": map[string]any{"tagName": "main", "text": "Products"}},
			map[string]any{"timestamp": 2, "domTree": map[string]any{"tagName": "main", "text": "Results"}},
		},
		"events": []any{map[string]any{"type": "input", "value": "[REDACTED]"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return recording.ID
}

func TestManagerProcessOnceLeavesPendingJobsUnclaimedWhenLLMDisabled(t *testing.T) {
	for _, mode := range []string{"legacy", "enforced"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("LLM_ENABLED", "false")
			t.Setenv("LLM_POLICY_MODE", mode)
			cfg := config.Load()
			s := newManagerTestStore(t)
			recordingID := createManagerRecording(t, s, cfg)
			manager := NewManager(s, cfg, NewWorkflow(cfg, nil), zap.NewNop())
			job, err := manager.SubmitCandidates(context.Background(), recordingID)
			if err != nil {
				t.Fatal(err)
			}

			manager.ProcessOnce(context.Background())

			pending, err := manager.GetJob(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pending.Status != models.RequirementJobPending || pending.AttemptCount != 0 {
				t.Fatalf("disabled worker mutated pending job: %+v", pending)
			}
		})
	}
}

func TestManagerEnforcedFailureLogHashesProviderValidationError(t *testing.T) {
	const sentinel = "requirement-provider-field-secret-sentinel"

	setLineageConflictEnforcedEnvironment(t)
	cfg := config.Load()
	s := newManagerTestStore(t)
	recordingID := createManagerRecording(t, s, cfg)
	core, observed := observer.New(zap.WarnLevel)
	manager := NewManager(s, cfg, NewWorkflow(cfg, nil), zap.New(core))
	job, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("claimed job %q, want %q", claimed.ID, job.ID)
	}
	providerErr := fmt.Errorf("%w: invalid field %s", ErrInvalidProviderOutput, sentinel)

	manager.failJobWithAttemptReport(context.Background(), claimed, providerErr, nil, nil)

	entries := observed.FilterMessage("collection requirement job failed").All()
	if len(entries) != 1 {
		t.Fatalf("failure logs = %d, want 1", len(entries))
	}
	entry := entries[0]
	for key, value := range entry.ContextMap() {
		if strings.Contains(fmt.Sprint(value), sentinel) {
			t.Fatalf("log field %q exposed provider validation content: %v", key, value)
		}
	}
	fields := entry.ContextMap()
	if fields["code"] != "PROVIDER_OUTPUT_INVALID" || fields["errorClass"] != "PROVIDER_OUTPUT_INVALID" {
		t.Fatalf("unexpected stable failure fields: %+v", fields)
	}
	if fields["errorHash"] == "" || fields["errorBytes"] != int64(len(providerErr.Error())) {
		t.Fatalf("missing hashed error evidence: %+v", fields)
	}
	if _, ok := fields["error"]; ok {
		t.Fatal("enforced failure log must not contain a raw error field")
	}
}

func TestManagerProcessesCandidateAndManualNormalizationJobs(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobBatchSize: 10, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	candidateJSON := candidateResponse(t)
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: candidateJSON, InputTokens: 50, OutputTokens: 25},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())

	candidateJob, err := manager.SubmitCandidates(context.Background(), recordingID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), candidateJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.Provider != "primary" || completed.Result == nil {
		t.Fatalf("unexpected candidate job: %+v", completed)
	}

	spec := validSpec("Collect visible products")
	normalizeJob, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{
		Requirement: &spec, CandidateJobID: candidateJob.ID, CandidateID: "c1",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	normalized, err := manager.GetJob(context.Background(), normalizeJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Status != models.RequirementJobCompleted || normalized.Source != models.RequirementSourceLLM || normalized.Provider != "deterministic" || normalized.RequirementID == "" {
		t.Fatalf("unexpected candidate-derived normalization job: %+v", normalized)
	}
	requirement, err := manager.GetRequirement(context.Background(), normalized.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if requirement.Requirement.Title != spec.Title || requirement.Status != models.CollectionRequirementDraft {
		t.Fatalf("unexpected draft: %+v", requirement)
	}
	confirmed, err := manager.ConfirmRequirement(context.Background(), requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != models.CollectionRequirementConfirmed {
		t.Fatalf("requirement was not confirmed: %+v", confirmed)
	}
	if _, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{
		Requirement: &spec, CandidateJobID: candidateJob.ID, CandidateID: "missing",
	}); !errors.Is(err, ErrInvalidRequirement) {
		t.Fatalf("expected invalid candidate lineage rejection, got %v", err)
	}

	manualSpec := validSpec("Manually entered requirement")
	manualJob, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{Requirement: &manualSpec})
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	manual, err := manager.GetJob(context.Background(), manualJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manual.Source != models.RequirementSourceManual || manual.Provider != "manual" || manual.RequirementID == "" {
		t.Fatalf("manual degraded path was not labeled accurately: %+v", manual)
	}

	var source, provider string
	var rawResult []byte
	if err := s.DB().QueryRow(`SELECT source, COALESCE(provider, ''), result_artifact FROM requirement_jobs WHERE id = ?`, manualJob.ID).Scan(&source, &provider, &rawResult); err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(manualSpec)
	if string(rawResult) == string(plain) {
		t.Fatal("manual normalized result was persisted in plaintext")
	}
}

func TestManagerOneAttemptProviderFailureIsTerminal(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "strict-model", LLMJobMaxAttempts: 1,
		LLMJobBatchSize: 10, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return nil, errors.New("provider unavailable")
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{CustomText: "Collect products"})
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	failed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != models.RequirementJobFailed || failed.AttemptCount != 1 || failed.MaxAttempts != 1 {
		t.Fatalf("one-attempt job was not terminal: %+v", failed)
	}
}

func TestManagerPersistsChunkProgressWhileAnalyzing(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 6000,
		LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	service := platformrecording.NewService(s, cfg)
	large := strings.Repeat("visible semantic text ", 200)
	recording, err := service.Create(context.Background(), platformrecording.CreateInput{Payload: map[string]any{
		"version": "2",
		"snapshots": []any{
			map[string]any{"actionIndex": 0, "dom": large + "initial"},
			map[string]any{"actionIndex": 1, "dom": large + "before"},
			map[string]any{"actionIndex": 2, "dom": large + "final"},
		},
		"events": []any{map[string]any{"type": "click"}, map[string]any{"type": "input", "value": "[REDACTED]"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	analysisCalls := 0
	fake := &fakeCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		if request.System == analysisSystemPrompt {
			analysisCalls++
			if analysisCalls == 2 {
				return nil, errors.New("provider crashed mid-analysis")
			}
			return &llm.CompletionResult{
				CompletionResponse: &llm.CompletionResponse{Content: `{"summary":"observed"}`, InputTokens: 10, OutputTokens: 5},
				Provider:           "primary", Model: "test-model",
			}, nil
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: candidateResponse(t), InputTokens: 10, OutputTokens: 5},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitCandidates(context.Background(), recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	pending, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.RequirementJobPending || pending.ErrorCode != "PROVIDER_UNAVAILABLE" {
		t.Fatalf("mid-analysis failure should stay retryable: %+v", pending)
	}
	if pending.ChunkCount < 2 || pending.CompletedChunks != 1 {
		t.Fatalf("per-chunk progress was not persisted: %+v", pending)
	}
	if _, err := s.DB().Exec(`UPDATE requirement_jobs SET available_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.ChunkCount != pending.ChunkCount || completed.CompletedChunks != completed.ChunkCount {
		t.Fatalf("completed job must persist matching chunk totals: %+v", completed)
	}
}

func TestClassifyJobErrorRetriesInvalidProviderOutputButRejectsUnsafeUserInput(t *testing.T) {
	providerErr := fmt.Errorf("%w: %w", ErrInvalidProviderOutput, ErrUnsafeRequirement)
	terminal, code, _ := classifyJobError(providerErr)
	if terminal || code != "PROVIDER_OUTPUT_INVALID" {
		t.Fatalf("provider contract violation should be retryable: terminal=%v code=%s", terminal, code)
	}
	terminal, code, _ = classifyJobError(ErrUnsafeRequirement)
	if !terminal || code != "UNSAFE_REQUIREMENT" {
		t.Fatalf("unsafe user input should remain terminal: terminal=%v code=%s", terminal, code)
	}
}

func TestManagerDurablyRetriesUnsafeProviderCandidates(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	calls := 0
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		calls++
		content := unsafeCandidateResponse(t)
		if calls > 1 {
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
	pending, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.RequirementJobPending || pending.AttemptCount != 1 || pending.ErrorCode != "PROVIDER_OUTPUT_INVALID" {
		t.Fatalf("unsafe provider output was not retained for bounded retry: %+v", pending)
	}
	if _, err := s.DB().Exec(`UPDATE requirement_jobs SET available_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.AttemptCount != 2 || calls != 2 {
		t.Fatalf("safe retry did not complete the same durable job: job=%+v calls=%d", completed, calls)
	}
}

func TestManagerRetriesInvalidCandidatesWithFeedback(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	invalidSpec := validSpec("Collect matching products")
	invalidSpec.OutputFields[0].Name = "Carrier_Deal_Status Updates"
	invalidCandidates, err := json.Marshal(map[string]any{"candidates": []models.RequirementCandidate{
		{Confidence: 0.9, Requirement: invalidSpec},
		{Confidence: 0.8, Requirement: validSpec("Compare visible prices")},
		{Confidence: 0.7, Requirement: validSpec("Monitor listed products")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The fake simulates the response cache: the identical first-attempt
	// prompt returns the same invalid output, while the feedback-augmented
	// retry prompt is a new request that returns a valid structure.
	fake := &fakeCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		content := string(invalidCandidates)
		if strings.Contains(request.User, "A previous attempt returned an invalid or unsafe structure") {
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
	pending, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.RequirementJobPending || pending.AttemptCount != 1 || pending.ErrorCode != "PROVIDER_OUTPUT_INVALID" {
		t.Fatalf("invalid provider output should stay retryable: %+v", pending)
	}
	if len(fake.requests) != 1 || strings.Contains(fake.requests[0].User, "A previous attempt") {
		t.Fatalf("first attempt must not carry retry feedback: %+v", fake.requests)
	}
	if _, err := s.DB().Exec(`UPDATE requirement_jobs SET available_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.AttemptCount != 2 || len(fake.requests) != 2 {
		t.Fatalf("feedback retry did not complete the same durable job: job=%+v calls=%d", completed, len(fake.requests))
	}
	if !strings.Contains(fake.requests[1].User, "A previous attempt returned an invalid or unsafe structure: the provider returned an invalid or unsafe structured requirement; retrying with bounded backoff") {
		t.Fatalf("second attempt prompt omitted the previous failure: %q", fake.requests[1].User)
	}
}

func TestManagerRetriesInvalidNormalizationWithFeedback(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	specJSON, err := json.Marshal(validSpec("Collect matching products"))
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors the live failure: the provider returned sampleOutput as an
	// array instead of an object, and the cached identical prompt kept
	// returning it until feedback changed the request.
	invalidContent := `{"requirement":{"title":"Collect prices","description":"d","requiredInputs":[],"optionalInputs":[],"outputFields":[{"name":"price","type":"number","description":"p"}],"sampleOutput":[{"price":10.5}]}}`
	fake := &fakeCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
		content := invalidContent
		if strings.Contains(request.User, "A previous attempt returned an invalid or unsafe structure") {
			content = `{"requirement":` + string(specJSON) + `}`
		}
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: content},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{CustomText: "collect visible prices"})
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	pending, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.RequirementJobPending || pending.AttemptCount != 1 || pending.ErrorCode != "PROVIDER_OUTPUT_INVALID" {
		t.Fatalf("invalid normalization output should stay retryable: %+v", pending)
	}
	if len(fake.requests) != 1 || strings.Contains(fake.requests[0].User, "A previous attempt") {
		t.Fatalf("first attempt must not carry retry feedback: %+v", fake.requests)
	}
	if _, err := s.DB().Exec(`UPDATE requirement_jobs SET available_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.AttemptCount != 2 || len(fake.requests) != 2 {
		t.Fatalf("feedback retry did not complete the normalize job: job=%+v calls=%d", completed, len(fake.requests))
	}
	if !strings.Contains(fake.requests[1].User, "A previous attempt returned an invalid or unsafe structure") {
		t.Fatalf("retry prompt must carry the previous validation failure: %+v", fake.requests[1])
	}
}

func TestManagerRecoversExactLegacyPrecompletedRequirementDraft(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 1, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	spec := validSpec("Collect matching products")
	response, err := json.Marshal(map[string]any{"requirement": spec})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: string(response)},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{CustomText: "collect products"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	legacyDraft := &models.CollectionRequirement{
		ID: "legacy-exact-draft", RecordingID: recordingID, SourceJobID: job.ID,
		Source: models.RequirementSourceLLM, Requirement: spec,
	}
	if err := s.CreateCollectionRequirement(context.Background(), legacyDraft); err != nil {
		t.Fatal(err)
	}

	manager.runJob(context.Background(), claimed)

	completed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != models.RequirementJobCompleted || completed.RequirementID != legacyDraft.ID {
		t.Fatalf("exact legacy draft was not reused: %+v", completed)
	}
	var draftCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM collection_requirements WHERE source_job_id = ?`, job.ID).Scan(&draftCount); err != nil {
		t.Fatal(err)
	}
	if draftCount != 1 {
		t.Fatalf("legacy recovery created %d normalized drafts", draftCount)
	}
	reports, err := manager.ListProviderAttempts(context.Background(), job.ID)
	if err != nil || len(reports) != 1 || reports[0].Outcome != models.LLMAttemptSucceeded {
		t.Fatalf("legacy recovery did not atomically retain the successful attempt: reports=%+v err=%v", reports, err)
	}
}

func TestManagerFailsClosedOnMismatchedLegacyRequirementDraft(t *testing.T) {
	s := newManagerTestStore(t)
	cfg := &config.Config{
		LLMEnabled: true, LLMModel: "test-model", LLMMaxInputTokens: 10_000,
		LLMJobMaxAttempts: 3, LLMJobBatchSize: 1, LLMRequestTimeout: time.Second,
	}
	recordingID := createManagerRecording(t, s, cfg)
	generated := validSpec("Generated requirement")
	response, err := json.Marshal(map[string]any{"requirement": generated})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return &llm.CompletionResult{
			CompletionResponse: &llm.CompletionResponse{Content: string(response)},
			Provider:           "primary", Model: "test-model",
		}, nil
	}}
	manager := NewManager(s, cfg, NewWorkflow(cfg, fake), zap.NewNop())
	job, err := manager.SubmitNormalization(context.Background(), recordingID, NormalizationInput{CustomText: "collect products"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingRequirementJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := generated
	conflicting.Title = "Different legacy content"
	legacyDraft := &models.CollectionRequirement{
		ID: "legacy-conflicting-draft", RecordingID: recordingID, SourceJobID: job.ID,
		Source: models.RequirementSourceLLM, Requirement: conflicting,
	}
	if err := s.CreateCollectionRequirement(context.Background(), legacyDraft); err != nil {
		t.Fatal(err)
	}

	manager.runJob(context.Background(), claimed)

	failed, err := manager.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != models.RequirementJobFailed ||
		failed.ErrorCode != "REQUIREMENT_LINEAGE_CONFLICT" ||
		failed.Result != nil ||
		failed.RequirementID != "" {
		t.Fatalf("mismatched legacy draft did not fail closed: %+v", failed)
	}
	reports, err := manager.ListProviderAttempts(context.Background(), job.ID)
	if err != nil || len(reports) != 1 ||
		reports[0].Outcome != models.LLMAttemptFailed ||
		reports[0].ErrorCode != "REQUIREMENT_LINEAGE_CONFLICT" ||
		reports[0].ValidationPhase != "lineage-validation" {
		t.Fatalf("lineage conflict report was not retained: reports=%+v err=%v", reports, err)
	}
}
