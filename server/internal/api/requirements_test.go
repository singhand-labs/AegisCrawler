package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	llmrequirement "github.com/singhand-labs/AegisCrawler/internal/llm/requirement"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type fakeRequirementManager struct {
	lastRecordingID string
	lastInput       llmrequirement.NormalizationInput
	job             *models.RequirementJob
	requirement     *models.CollectionRequirement
	attempts        []*models.LLMAttemptReport
	attemptDetail   *llmrequirement.RequirementAttemptDetail
	providerCall    *models.LLMProviderCall
	retryErr        error
	submitNormErr   error
}

func (f *fakeRequirementManager) SubmitCandidates(_ context.Context, recordingID string) (*models.RequirementJob, error) {
	f.lastRecordingID = recordingID
	return f.job, nil
}

func (f *fakeRequirementManager) SubmitNormalization(_ context.Context, recordingID string, input llmrequirement.NormalizationInput) (*models.RequirementJob, error) {
	f.lastRecordingID = recordingID
	f.lastInput = input
	if f.submitNormErr != nil {
		return nil, f.submitNormErr
	}
	return f.job, nil
}

func (f *fakeRequirementManager) GetJob(context.Context, string) (*models.RequirementJob, error) {
	if f.job == nil {
		return nil, store.ErrRequirementJobNotFound
	}
	return f.job, nil
}

func (f *fakeRequirementManager) ListProviderAttempts(context.Context, string) ([]*models.LLMAttemptReport, error) {
	if f.job == nil {
		return nil, store.ErrRequirementJobNotFound
	}
	return f.attempts, nil
}

func (f *fakeRequirementManager) GetProviderAttempt(context.Context, string, int) (*llmrequirement.RequirementAttemptDetail, error) {
	if f.job == nil {
		return nil, store.ErrRequirementJobNotFound
	}
	if f.attemptDetail == nil {
		return nil, store.ErrLLMAttemptReportNotFound
	}
	return f.attemptDetail, nil
}

func (f *fakeRequirementManager) GetProviderCall(context.Context, string, int, string) (*models.LLMProviderCall, error) {
	if f.job == nil {
		return nil, store.ErrRequirementJobNotFound
	}
	if f.providerCall == nil {
		return nil, store.ErrLLMProviderCallNotFound
	}
	return f.providerCall, nil
}

func (f *fakeRequirementManager) RetryJob(context.Context, string) error { return f.retryErr }

func (f *fakeRequirementManager) GetRequirement(context.Context, string) (*models.CollectionRequirement, error) {
	if f.requirement == nil {
		return nil, store.ErrRequirementNotFound
	}
	return f.requirement, nil
}

func (f *fakeRequirementManager) ConfirmRequirement(context.Context, string) (*models.CollectionRequirement, error) {
	if f.requirement == nil {
		return nil, store.ErrRequirementNotFound
	}
	confirmed := *f.requirement
	confirmed.Status = models.CollectionRequirementConfirmed
	return &confirmed, nil
}

func requirementAPITestRouter(t *testing.T, manager requirementManager, enabled bool) http.Handler {
	t.Helper()
	cfg := &config.Config{
		WorkflowV2Enabled: enabled, RateLimitPerSecond: 1000, RateLimitBurst: 1000,
		MaxRequestBodyBytes: 1024 * 1024,
	}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	h.SetRequirementManager(manager)
	return NewRouter(h, cfg, zap.NewNop(), NewMetrics())
}

func apiRequirementSpec() models.CollectionRequirementSpec {
	return models.CollectionRequirementSpec{
		Title: "Collect products", Description: "Collect visible products.",
		RequiredInputs: []models.RequirementInput{}, OptionalInputs: []models.RequirementInput{},
		OutputFields: []models.RequirementOutputField{{Name: "name", Type: models.RequirementValueString, Description: "Product name"}},
		SampleOutput: map[string]any{"name": "Example"},
	}
}

func TestRequirementWorkflowRoutesSubmitPollAndConfirm(t *testing.T) {
	manager := &fakeRequirementManager{
		job:         &models.RequirementJob{ID: "job-1", RecordingID: "recording-1", Kind: models.RequirementJobCandidates, Status: models.RequirementJobPending},
		requirement: &models.CollectionRequirement{ID: "requirement-1", RecordingID: "recording-1", Status: models.CollectionRequirementDraft, Requirement: apiRequirementSpec()},
	}
	router := requirementAPITestRouter(t, manager, true)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/recordings/recording-1/requirement-jobs", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || manager.lastRecordingID != "recording-1" {
		t.Fatalf("candidate submission failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var jobResponse RequirementJobResponse
	if err := json.NewDecoder(response.Body).Decode(&jobResponse); err != nil || jobResponse.StatusURL != "/api/v1/requirement-jobs/job-1" {
		t.Fatalf("unexpected candidate response: %+v err=%v", jobResponse, err)
	}

	spec := apiRequirementSpec()
	body, _ := json.Marshal(NormalizeRequirementRequest{Requirement: &spec})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/recordings/recording-1/requirement-jobs/normalize", bytes.NewReader(body))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || manager.lastInput.Requirement == nil || manager.lastInput.Requirement.Title != spec.Title {
		t.Fatalf("normalization submission failed: status=%d body=%s input=%+v", response.Code, response.Body.String(), manager.lastInput)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/job-1", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("job polling failed: status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/requirements/requirement-1/confirm", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"confirmed"`)) {
		t.Fatalf("confirmation failed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequirementWorkflowFeatureAndSafeErrors(t *testing.T) {
	manager := &fakeRequirementManager{}
	disabled := requirementAPITestRouter(t, manager, false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/recordings/recording-1/requirement-jobs", nil)
	response := httptest.NewRecorder()
	disabled.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte("FEATURE_DISABLED")) {
		t.Fatalf("disabled workflow should be hidden: status=%d body=%s", response.Code, response.Body.String())
	}

	enabled := requirementAPITestRouter(t, manager, true)
	request = httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/missing", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte("NOT_FOUND")) {
		t.Fatalf("missing job should be hidden: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/missing/provider-attempts", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing provider-attempt job should be hidden: status=%d body=%s", response.Code, response.Body.String())
	}

	manager.job = &models.RequirementJob{ID: "failed", Status: models.RequirementJobFailed}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/failed/provider-attempts/not-a-number", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid attempt should be rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/failed/provider-attempts/1", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing provider attempt should be hidden: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/failed/provider-attempts/1/calls/missing/content", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing provider call should be hidden: status=%d body=%s", response.Code, response.Body.String())
	}

	manager.retryErr = store.ErrRequirementJobState
	request = httptest.NewRequest(http.MethodPost, "/api/v1/requirement-jobs/failed/retry", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("INVALID_STATE")) {
		t.Fatalf("invalid retry state should conflict: status=%d body=%s", response.Code, response.Body.String())
	}

	manager.retryErr = errors.New("database unavailable")
	request = httptest.NewRequest(http.MethodPost, "/api/v1/requirement-jobs/failed/retry", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || bytes.Contains(response.Body.Bytes(), []byte("database unavailable")) {
		t.Fatalf("internal retry error leaked details: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequirementProviderArtifactRoutesAreAuthorizedAuditedAndBoundedToExplicitReads(t *testing.T) {
	persistence, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer persistence.Close()

	const providerMarker = "authorized-provider-history-marker"
	report := &models.LLMAttemptReport{
		ID: "attempt-report-1", JobType: models.LLMJobTypeRequirement,
		JobID: "job-1", AttemptNumber: 1, Outcome: models.LLMAttemptSucceeded,
		PromptVersion: "test-v1", RecordingHash: "recording-hash",
		ArtifactHash: "attempt-artifact-hash",
	}
	call := &models.LLMProviderCall{
		ID: "provider-call-1", JobType: models.LLMJobTypeRequirement,
		JobID: "job-1", AttemptNumber: 1, CallIndex: 1,
		Phase: "final", Provider: "fake", Model: "fake-model",
		OriginalBytesExact: true,
		ResponseHash:       "response-hash", ArtifactHash: "call-artifact-hash",
		Artifact: map[string]any{"content": providerMarker},
	}
	manager := &fakeRequirementManager{
		job: &models.RequirementJob{
			ID: "job-1", RecordingID: "recording-1",
			Kind: models.RequirementJobCandidates, Status: models.RequirementJobCompleted,
		},
		attempts: []*models.LLMAttemptReport{report},
		attemptDetail: &llmrequirement.RequirementAttemptDetail{
			Report: report,
			Calls:  []*models.LLMProviderCall{call},
			Artifact: map[string]any{
				"validation": map[string]any{"status": "passed"},
				"parsed":     providerMarker,
			},
		},
		providerCall: call,
	}
	cfg := &config.Config{
		WorkflowV2Enabled:     true,
		AdminAPIKey:           "provider-artifact-admin",
		AuditActor:            "test-admin",
		RateLimitPerSecond:    1000,
		RateLimitBurst:        1000,
		LLMRateLimitPerSecond: 1000,
		LLMRateLimitBurst:     1000,
		MaxRequestBodyBytes:   1024 * 1024,
	}
	handler := NewHandler(persistence, nil, nil, nil, cfg, zap.NewNop())
	handler.SetRequirementManager(manager)
	router := NewRouter(handler, cfg, zap.NewNop(), NewMetrics())

	request := httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/job-1/provider-attempts/1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), providerMarker) {
		t.Fatalf("unauthorized detail read was not rejected: status=%d body=%s", response.Code, response.Body.String())
	}

	doAuthorizedGet := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer provider-artifact-admin")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	poll := doAuthorizedGet("/api/v1/requirement-jobs/job-1")
	if poll.Code != http.StatusOK || strings.Contains(poll.Body.String(), providerMarker) {
		t.Fatalf("ordinary polling exposed provider history: status=%d body=%s", poll.Code, poll.Body.String())
	}
	list := doAuthorizedGet("/api/v1/requirement-jobs/job-1/provider-attempts")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), providerMarker) {
		t.Fatalf("metadata list exposed provider content: status=%d body=%s", list.Code, list.Body.String())
	}
	detail := doAuthorizedGet("/api/v1/requirement-jobs/job-1/provider-attempts/1")
	if detail.Code != http.StatusOK ||
		!strings.Contains(detail.Body.String(), providerMarker) ||
		!strings.Contains(detail.Body.String(), `"originalBytesExact":true`) {
		t.Fatalf("authorized attempt detail was unavailable: status=%d body=%s", detail.Code, detail.Body.String())
	}
	if detail.Header().Get("Cache-Control") != "no-store" ||
		detail.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("provider attempt artifact response was cacheable: %+v", detail.Header())
	}
	content := doAuthorizedGet("/api/v1/requirement-jobs/job-1/provider-attempts/1/calls/provider-call-1/content")
	if content.Code != http.StatusOK || !strings.Contains(content.Body.String(), providerMarker) {
		t.Fatalf("authorized provider content was unavailable: status=%d body=%s", content.Code, content.Body.String())
	}
	if content.Header().Get("Cache-Control") != "no-store" ||
		content.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("provider call artifact response was cacheable: %+v", content.Header())
	}

	logs, total, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		ResourceType: "requirement_job",
		ResourceID:   "job-1",
		Limit:        20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("expected list, detail, and content audits, got %d: %+v", total, logs)
	}
	actions := map[string]bool{}
	for _, log := range logs {
		actions[log.Action] = true
		if strings.Contains(string(log.Payload), providerMarker) {
			t.Fatalf("audit payload retained provider content: %s", log.Payload)
		}
	}
	for _, action := range []string{
		"requirement_provider_attempts_read",
		"requirement_provider_attempt_read",
		"requirement_provider_call_content_read",
	} {
		if !actions[action] {
			t.Fatalf("missing audit action %q: %+v", action, actions)
		}
	}
}

func TestRequirementProviderArtifactReadsAreRateLimited(t *testing.T) {
	persistence, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer persistence.Close()

	report := &models.LLMAttemptReport{
		ID: "attempt-report-1", JobType: models.LLMJobTypeRequirement,
		JobID: "job-1", AttemptNumber: 1, Outcome: models.LLMAttemptSucceeded,
		PromptVersion: "test-v1", RecordingHash: "recording-hash",
	}
	manager := &fakeRequirementManager{
		job: &models.RequirementJob{ID: "job-1"},
		attemptDetail: &llmrequirement.RequirementAttemptDetail{
			Report: report,
			Calls:  []*models.LLMProviderCall{},
		},
	}
	cfg := &config.Config{
		WorkflowV2Enabled:     true,
		AdminAPIKey:           "rate-limit-admin",
		RateLimitPerSecond:    1000,
		RateLimitBurst:        1000,
		LLMRateLimitPerSecond: 0.0001,
		LLMRateLimitBurst:     1,
		MaxRequestBodyBytes:   1024 * 1024,
	}
	handler := NewHandler(persistence, nil, nil, nil, cfg, zap.NewNop())
	handler.SetRequirementManager(manager)
	router := NewRouter(handler, cfg, zap.NewNop(), NewMetrics())
	for index, expected := range []int{http.StatusOK, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/requirement-jobs/job-1/provider-attempts/1", nil)
		request.Header.Set("Authorization", "Bearer rate-limit-admin")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("request %d: expected %d, got %d: %s", index+1, expected, response.Code, response.Body.String())
		}
	}
}

func TestRequirementProviderArtifactReadsFailClosedWhenAuditUnavailable(t *testing.T) {
	const providerMarker = "must-not-be-returned-without-audit"
	report := &models.LLMAttemptReport{
		ID: "attempt-report-1", JobType: models.LLMJobTypeRequirement,
		JobID: "job-1", AttemptNumber: 1, Outcome: models.LLMAttemptSucceeded,
	}
	call := &models.LLMProviderCall{
		ID: "provider-call-1", JobType: models.LLMJobTypeRequirement,
		JobID: "job-1", AttemptNumber: 1,
		Artifact: map[string]any{"content": providerMarker},
	}
	manager := &fakeRequirementManager{
		job: &models.RequirementJob{ID: "job-1"},
		attemptDetail: &llmrequirement.RequirementAttemptDetail{
			Report:   report,
			Artifact: map[string]any{"content": providerMarker},
		},
		providerCall: call,
	}
	cfg := &config.Config{
		WorkflowV2Enabled: true, RateLimitPerSecond: 1000, RateLimitBurst: 1000,
		LLMRateLimitPerSecond: 1000, LLMRateLimitBurst: 1000,
		MaxRequestBodyBytes: 1024 * 1024,
	}
	handler := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	handler.SetRequirementManager(manager)
	router := NewRouter(handler, cfg, zap.NewNop(), NewMetrics())

	for _, path := range []string{
		"/api/v1/requirement-jobs/job-1/provider-attempts/1",
		"/api/v1/requirement-jobs/job-1/provider-attempts/1/calls/provider-call-1/content",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "AUDIT_UNAVAILABLE") ||
			strings.Contains(response.Body.String(), providerMarker) {
			t.Fatalf("audit failure leaked decrypted content for %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

// TestNormalizeRequirementOnSafetyRejectionWritesAuditLog verifies WI-15:
// when a normalization request is rejected as ErrUnsafeRequirement, the
// handler emits an audit_log row with action="requirement_safety_rejected"
// and a redacted payload.
func TestNormalizeRequirementOnSafetyRejectionWritesAuditLog(t *testing.T) {
	persistence, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer persistence.Close()

	// customText contains a credential-pattern string that redact.String
	// must scrub before the audit row is persisted.
	const sensitiveValue = "hunter2-leaked-cred"
	const sensitiveMarker = "password: " + sensitiveValue
	manager := &fakeRequirementManager{
		submitNormErr: llmrequirement.ErrUnsafeRequirement,
	}
	cfg := &config.Config{
		WorkflowV2Enabled:   true,
		AdminAPIKey:         "safety-audit-admin",
		AuditActor:          "test-admin",
		RateLimitPerSecond:  1000,
		RateLimitBurst:      1000,
		MaxRequestBodyBytes: 1024 * 1024,
	}
	handler := NewHandler(persistence, nil, nil, nil, cfg, zap.NewNop())
	handler.SetRequirementManager(manager)
	router := NewRouter(handler, cfg, zap.NewNop(), NewMetrics())

	body, _ := json.Marshal(NormalizeRequirementRequest{
		CustomText: sensitiveMarker,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/recordings/recording-1/requirement-jobs/normalize", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer safety-audit-admin")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "UNSAFE_REQUIREMENT") {
		t.Fatalf("expected UNSAFE_REQUIREMENT rejection: status=%d body=%s", response.Code, response.Body.String())
	}

	logs, _, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		Action: "requirement_safety_rejected",
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 requirement_safety_rejected audit row, got %d", len(logs))
	}
	if logs[0].ResourceType != "recording" || logs[0].ResourceID != "recording-1" {
		t.Fatalf("unexpected audit resource: type=%s id=%s", logs[0].ResourceType, logs[0].ResourceID)
	}
	// WI-15: the sensitive customText must be redacted before persisting.
	if strings.Contains(string(logs[0].Payload), sensitiveValue) {
		t.Fatalf("audit payload retained sensitive customText: %s", logs[0].Payload)
	}
}
