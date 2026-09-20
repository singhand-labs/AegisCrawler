package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	llmdsl "github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type fakeDSLWorkflowManager struct {
	workflow       *models.DSLWorkflow
	job            *models.DSLJob
	replay         *models.ReplayAttempt
	version        *models.RuleVersion
	contract       *models.RuleVersionContract
	repairJob      *models.DSLJob
	err            error
	requirementID  string
	profileID      string
	baseline       *models.Rule
	correction     *models.Rule
	completion     llmdsl.ReplayCompletionInput
	completionFlow string
	completionID   string
	attempts       []*models.LLMAttemptReport
	attemptDetail  *llmdsl.DSLAttemptDetail
	providerCall   *models.LLMProviderCall
	adoptedExport  llmdsl.AdminReviewedDSLAttemptExport
	confirmOpts    store.ApproveDSLWorkflowOptions
}

func (f *fakeDSLWorkflowManager) Submit(_ context.Context, requirementID, profileID string, baseline *models.Rule) (*models.DSLWorkflow, *models.DSLJob, error) {
	f.requirementID, f.profileID, f.baseline = requirementID, profileID, baseline
	return f.workflow, f.job, f.err
}

func (f *fakeDSLWorkflowManager) AdoptAdminReviewedAttempt(_ context.Context, requirementID, profileID string, exported llmdsl.AdminReviewedDSLAttemptExport) (*models.DSLWorkflow, error) {
	f.requirementID, f.profileID, f.adoptedExport = requirementID, profileID, exported
	return f.workflow, f.err
}

func (f *fakeDSLWorkflowManager) GetWorkflow(context.Context, string) (*models.DSLWorkflow, error) {
	return f.workflow, f.err
}

func (f *fakeDSLWorkflowManager) GetJob(context.Context, string) (*models.DSLJob, error) {
	return f.job, f.err
}

func (f *fakeDSLWorkflowManager) ListProviderAttempts(context.Context, string) ([]*models.LLMAttemptReport, error) {
	return f.attempts, f.err
}

func (f *fakeDSLWorkflowManager) GetProviderAttempt(context.Context, string, int) (*llmdsl.DSLAttemptDetail, error) {
	return f.attemptDetail, f.err
}

func (f *fakeDSLWorkflowManager) GetProviderCall(context.Context, string, int, string) (*models.LLMProviderCall, error) {
	return f.providerCall, f.err
}

func (f *fakeDSLWorkflowManager) GetReplay(context.Context, string) (*models.ReplayAttempt, error) {
	return f.replay, f.err
}

func (f *fakeDSLWorkflowManager) StartReplay(context.Context, string) (*models.ReplayAttempt, error) {
	return f.replay, f.err
}

func (f *fakeDSLWorkflowManager) Correct(_ context.Context, _ string, correction *models.Rule) (*models.DSLWorkflow, error) {
	f.correction = correction
	return f.workflow, f.err
}

func (f *fakeDSLWorkflowManager) CompleteReplay(_ context.Context, workflowID, replayID string, input llmdsl.ReplayCompletionInput) (*models.ReplayAttempt, *models.DSLJob, error) {
	f.completionFlow, f.completionID, f.completion = workflowID, replayID, input
	return f.replay, f.repairJob, f.err
}

func (f *fakeDSLWorkflowManager) Confirm(_ context.Context, _ string, opts store.ApproveDSLWorkflowOptions) (*models.RuleVersion, *models.RuleVersionContract, error) {
	f.confirmOpts = opts
	return f.version, f.contract, f.err
}

func (f *fakeDSLWorkflowManager) GetDSLApprovalProvenance(context.Context, string) (*store.DSLApprovalProvenance, error) {
	return &store.DSLApprovalProvenance{}, f.err
}

func dslWorkflowAPIRouter(t *testing.T, manager dslWorkflowManager, enabled bool) http.Handler {
	t.Helper()
	cfg := &config.Config{
		WorkflowV2Enabled: enabled, RateLimitPerSecond: 1000, RateLimitBurst: 1000,
		LLMRateLimitPerSecond: 1000, LLMRateLimitBurst: 1000, MaxRequestBodyBytes: 1024 * 1024,
	}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	h.SetDSLWorkflowManager(manager)
	return NewRouter(h, cfg, zap.NewNop(), NewMetrics())
}

func apiBaselineRule() *models.Rule {
	return &models.Rule{
		ID: "rule-1", Version: "1", Name: "Rule", Domain: models.JSON(`"example.com"`),
		Entry: "https://example.com", Steps: models.JSON(`[{"action":"click","target":{"selector":"button"}}]`),
	}
}

func TestDSLProviderAttemptRoutesAreNoStoreAndAudited(t *testing.T) {
	persistence, err := store.New(filepath.Join(t.TempDir(), "dsl-provider-api.db"), "dsl-provider-api-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	report := &models.LLMAttemptReport{ID: "attempt-report", AttemptNumber: 1, ArtifactHash: "artifact-hash"}
	call := &models.LLMProviderCall{ID: "call-1", ArtifactHash: "call-hash", ResponseHash: "response-hash", Artifact: map[string]any{"content": "{}"}}
	manager := &fakeDSLWorkflowManager{
		job:      &models.DSLJob{ID: "job-1"},
		attempts: []*models.LLMAttemptReport{report},
		attemptDetail: &llmdsl.DSLAttemptDetail{
			Report: report, Calls: []*models.LLMProviderCall{call},
			Artifact: map[string]any{"providerIR": map[string]any{"rule": true}},
		},
		providerCall: call,
	}
	cfg := &config.Config{
		WorkflowV2Enabled: true, RateLimitPerSecond: 1000, RateLimitBurst: 1000,
		LLMRateLimitPerSecond: 1000, LLMRateLimitBurst: 1000,
		MaxRequestBodyBytes: 1024 * 1024,
	}
	handler := NewHandler(persistence, nil, nil, nil, cfg, zap.NewNop())
	handler.SetDSLWorkflowManager(manager)
	router := NewRouter(handler, cfg, zap.NewNop(), NewMetrics())

	for _, path := range []string{
		"/api/v1/dsl-jobs/job-1/provider-attempts/1",
		"/api/v1/dsl-jobs/job-1/provider-attempts/1/calls/call-1/content",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
			t.Fatalf("%s omitted no-store headers: %v", path, response.Header())
		}
	}
	logs, _, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(logs)
	if !bytes.Contains(encoded, []byte("dsl_provider_attempt_read")) ||
		!bytes.Contains(encoded, []byte("dsl_provider_call_content_read")) {
		t.Fatalf("provider artifact reads were not audited: %s", encoded)
	}
}

func TestAdoptDSLAttemptExportRouteIsAdminScopedAndAudited(t *testing.T) {
	persistence, err := store.New(filepath.Join(t.TempDir(), "dsl-adoption-api.db"), "dsl-adoption-api-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	manager := &fakeDSLWorkflowManager{workflow: &models.DSLWorkflow{
		ID: "adopted-workflow", RequirementID: "requirement-1", RecordingID: "recording-1",
		Status: models.DSLWorkflowAwaitingReplay, MaxRepairs: 0,
		SourceKind:         models.DSLWorkflowSourceAdminReviewedAttemptExport,
		SourceAuthority:    models.DSLWorkflowSourceAuthorityNonAuthoritative,
		SourceArtifactHash: strings.Repeat("a", 64),
		SourceExportHash:   strings.Repeat("b", 64),
	}}
	cfg := &config.Config{
		AdminAPIKey: "adoption-admin-key", AuditActor: "reviewing-admin",
		WorkflowV2Enabled: true, RateLimitPerSecond: 1000, RateLimitBurst: 1000,
		MaxRequestBodyBytes: 8 * 1024 * 1024,
	}
	handler := NewHandler(persistence, nil, nil, nil, cfg, zap.NewNop())
	handler.SetDSLWorkflowManager(manager)
	router := NewRouter(handler, cfg, zap.NewNop(), NewMetrics())
	payload, _ := json.Marshal(AdoptDSLAttemptExportRequest{
		BrowserProfileID: "reviewed-profile",
		Export: llmdsl.AdminReviewedDSLAttemptExport{
			SchemaVersion: llmdsl.AdminReviewedDSLAttemptExportVersion,
			Attempt:       &models.LLMAttemptReport{ArtifactHash: strings.Repeat("a", 64)},
			Artifact:      &llmdsl.DSLAttemptArtifact{SchemaVersion: "aegiscrawler.dsl-attempt.v2"},
		},
	})
	path := "/api/v1/requirements/requirement-1/dsl-workflows/adopt-attempt-export"
	unauthorized := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	unauthorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated adoption returned %d", unauthorizedResponse.Code)
	}
	withProviderBody := bytes.Replace(payload, []byte(`"calls":null`), []byte(`"calls":[{"artifact":{"content":"provider response"}}]`), 1)
	providerBodyRequest := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(withProviderBody))
	providerBodyRequest.Header.Set("Authorization", "Bearer adoption-admin-key")
	providerBodyResponse := httptest.NewRecorder()
	router.ServeHTTP(providerBodyResponse, providerBodyRequest)
	if providerBodyResponse.Code != http.StatusBadRequest {
		t.Fatalf("attempt export accepted a provider response body: status=%d body=%s", providerBodyResponse.Code, providerBodyResponse.Body.String())
	}
	oversizedBody := append([]byte(`{"padding":"`), bytes.Repeat([]byte("x"), llmdsl.MaxAdminReviewedAttemptExportBytes+1)...)
	oversizedBody = append(oversizedBody, []byte(`"}`)...)
	oversizedRequest := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(oversizedBody))
	oversizedRequest.Header.Set("Authorization", "Bearer adoption-admin-key")
	oversizedResponse := httptest.NewRecorder()
	router.ServeHTTP(oversizedResponse, oversizedRequest)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized attempt export returned %d: %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer adoption-admin-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || manager.requirementID != "requirement-1" ||
		manager.profileID != "reviewed-profile" ||
		manager.adoptedExport.SchemaVersion != llmdsl.AdminReviewedDSLAttemptExportVersion {
		t.Fatalf("attempt adoption failed: status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}
	logs, _, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(logs)
	if !bytes.Contains(encoded, []byte("dsl_attempt_export_adoption_reviewed")) ||
		!bytes.Contains(encoded, []byte(models.DSLWorkflowSourceAuthorityNonAuthoritative)) ||
		!bytes.Contains(encoded, []byte(strings.Repeat("a", 64))) ||
		bytes.Contains(encoded, []byte("dsl_attempt_export_adopted")) {
		t.Fatalf("attempt adoption audit lineage is incomplete: %s", encoded)
	}
}

func TestDSLWorkflowRoutesSubmitReplayPollAndConfirm(t *testing.T) {
	manager := &fakeDSLWorkflowManager{
		workflow: &models.DSLWorkflow{ID: "workflow-1", RequirementID: "requirement-1", RecordingID: "recording-1", BrowserProfileID: "profile-1", Status: models.DSLWorkflowGenerating},
		job: &models.DSLJob{
			ID: "job-1", WorkflowID: "workflow-1", Status: models.DSLJobPending,
			Kind:                  models.DSLJobSelectorRepair,
			SourceAttemptReportID: "source-attempt-report",
			ProviderDispatched:    true,
		},
		replay:    &models.ReplayAttempt{ID: "replay-1", WorkflowID: "workflow-1", Sequence: 1, Status: models.ReplayAttemptRunning},
		version:   &models.RuleVersion{RuleID: "rule-1", Version: 1, Status: models.RuleApprovalApproved},
		contract:  &models.RuleVersionContract{RuleID: "rule-1", Version: 1, BrowserProfileID: "profile-1", SourceWorkflowID: "workflow-1"},
		repairJob: &models.DSLJob{ID: "repair-1", WorkflowID: "workflow-1", Kind: models.DSLJobRepair},
	}
	router := dslWorkflowAPIRouter(t, manager, true)

	body, _ := json.Marshal(CreateDSLWorkflowRequest{BrowserProfileID: "profile-1", BaselineRule: apiBaselineRule()})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/requirements/requirement-1/dsl-workflows", bytes.NewReader(body))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || manager.requirementID != "requirement-1" || manager.profileID != "profile-1" || manager.baseline.ID != "rule-1" {
		t.Fatalf("workflow submission failed: status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}
	var workflowResponse DSLWorkflowResponse
	if err := json.NewDecoder(response.Body).Decode(&workflowResponse); err != nil || workflowResponse.StatusURL != "/api/v1/dsl-workflows/workflow-1" {
		t.Fatalf("unexpected workflow response: response=%+v err=%v", workflowResponse, err)
	}

	for path, want := range map[string]string{
		"/api/v1/dsl-workflows/workflow-1": `"id":"workflow-1"`,
		"/api/v1/dsl-jobs/job-1":           `"id":"job-1"`,
		"/api/v1/dsl-replays/replay-1":     `"id":"replay-1"`,
	} {
		request = httptest.NewRequest(http.MethodGet, path, nil)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(want)) {
			t.Fatalf("poll %s failed: status=%d body=%s", path, response.Code, response.Body.String())
		}
		if path == "/api/v1/dsl-jobs/job-1" &&
			(!bytes.Contains(response.Body.Bytes(), []byte(`"sourceAttemptReportId":"source-attempt-report"`)) ||
				bytes.Contains(response.Body.Bytes(), []byte("providerDispatched"))) {
			t.Fatalf("selector repair job API lineage/internal-state contract is wrong: %s",
				response.Body.String())
		}
	}

	correctionBody, _ := json.Marshal(CorrectDSLWorkflowRequest{Rule: apiBaselineRule()})
	request = httptest.NewRequest(http.MethodPut, "/api/v1/dsl-workflows/workflow-1/provisional", bytes.NewReader(correctionBody))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || manager.correction == nil || manager.correction.ID != "rule-1" {
		t.Fatalf("correct provisional workflow failed: status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/replays", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("start replay failed: status=%d body=%s", response.Code, response.Body.String())
	}

	completionBody, _ := json.Marshal(llmdsl.ReplayCompletionInput{Succeeded: false, Diagnostics: map[string]any{"message": "safe"}, ErrorCode: "ELEMENT_NOT_FOUND"})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/replays/replay-1/complete", bytes.NewReader(completionBody))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || manager.completionFlow != "workflow-1" || manager.completionID != "replay-1" || manager.completion.ErrorCode != "ELEMENT_NOT_FOUND" || !bytes.Contains(response.Body.Bytes(), []byte(`"repairJob"`)) {
		t.Fatalf("complete replay failed: status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/confirm", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"ruleId":"rule-1"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"browserProfileId":"profile-1"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"sourceWorkflowId":"workflow-1"`)) {
		t.Fatalf("confirm workflow failed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDSLWorkflowRoutesFeatureValidationAndSafeErrors(t *testing.T) {
	manager := &fakeDSLWorkflowManager{}
	disabled := dslWorkflowAPIRouter(t, manager, false)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dsl-workflows/missing", nil)
	response := httptest.NewRecorder()
	disabled.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte("FEATURE_DISABLED")) {
		t.Fatalf("disabled workflow should be hidden: status=%d body=%s", response.Code, response.Body.String())
	}

	enabled := dslWorkflowAPIRouter(t, manager, true)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/requirements/requirement-1/dsl-workflows", bytes.NewBufferString(`{"browserProfileId":"profile","unknown":true}`))
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid submission should fail: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/api/v1/dsl-workflows/workflow-1/provisional", bytes.NewBufferString(`{"rule":null,"unknown":true}`))
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid correction should fail: status=%d body=%s", response.Code, response.Body.String())
	}

	tests := []struct {
		err  error
		want int
		code string
	}{
		{err: store.ErrDSLWorkflowNotFound, want: http.StatusNotFound, code: "NOT_FOUND"},
		{err: store.ErrDSLWorkflowState, want: http.StatusConflict, code: "INVALID_STATE"},
		{err: llmdsl.ErrInvalidWorkflowInput, want: http.StatusBadRequest, code: "INVALID_DSL"},
		{err: llmdsl.ErrReplayPayloadTooLarge, want: http.StatusRequestEntityTooLarge, code: "PAYLOAD_TOO_LARGE"},
		{err: errors.New("database password=private"), want: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	}
	for _, test := range tests {
		manager.err = test.err
		request = httptest.NewRequest(http.MethodGet, "/api/v1/dsl-workflows/missing", nil)
		response = httptest.NewRecorder()
		enabled.ServeHTTP(response, request)
		if response.Code != test.want || !bytes.Contains(response.Body.Bytes(), []byte(test.code)) || bytes.Contains(response.Body.Bytes(), []byte("private")) {
			t.Fatalf("unexpected safe error for %v: status=%d body=%s", test.err, response.Code, response.Body.String())
		}
	}
	manager.err = &llmdsl.WorkflowInputError{Phase: "baseline-validation", Err: errors.New("private selector detail")}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/dsl-workflows/missing", nil)
	response = httptest.NewRecorder()
	enabled.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"details":"baseline-validation"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("private selector")) {
		t.Fatalf("typed workflow rejection did not retain only its safe phase: %s", response.Body.String())
	}
}

func TestConfirmDSLWorkflowReturns409WithBlockingFlags(t *testing.T) {
	manager := &fakeDSLWorkflowManager{
		err: &store.SafetyBlockingFlagError{Flags: []string{"external-resource-load"}},
	}
	router := dslWorkflowAPIRouter(t, manager, true)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/confirm", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"code":"SAFETY_BLOCKING_FLAG"`) {
		t.Fatalf("response missing SAFETY_BLOCKING_FLAG code: %s", body)
	}
	if !strings.Contains(body, `"blocking_flags":["external-resource-load"]`) {
		t.Fatalf("response missing blocking_flags: %s", body)
	}
}

func TestConfirmDSLWorkflowOverrideRequiresAdminPrincipal(t *testing.T) {
	version := &models.RuleVersion{RuleID: "rule-1", Version: 1, Status: models.RuleApprovalApproved}
	contract := &models.RuleVersionContract{RuleID: "rule-1", Version: 1, SourceWorkflowID: "workflow-1"}

	// dslConfirmHandler builds a bare Handler (no router middleware, which would
	// overwrite the request-context principal) so the inline admin check in
	// ConfirmDSLWorkflow can be exercised with controlled principals.
	dslConfirmHandler := func(t *testing.T, manager *fakeDSLWorkflowManager) *Handler {
		t.Helper()
		cfg := &config.Config{
			WorkflowV2Enabled: true, RateLimitPerSecond: 1000, RateLimitBurst: 1000,
			LLMRateLimitPerSecond: 1000, LLMRateLimitBurst: 1000, MaxRequestBodyBytes: 1024 * 1024,
		}
		h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
		h.SetDSLWorkflowManager(manager)
		return h
	}

	t.Run("non-admin gets 403", func(t *testing.T) {
		manager := &fakeDSLWorkflowManager{version: version, contract: contract}
		h := dslConfirmHandler(t, manager)

		request := httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/confirm?override_safety=true", nil)
		request = request.WithContext(authz.WithPrincipal(request.Context(), authz.Principal{
			Subject: "viewer-1", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleViewer},
		}))
		request.SetPathValue("id", "workflow-1")
		response := httptest.NewRecorder()
		h.ConfirmDSLWorkflow(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), `"code":"FORBIDDEN"`) {
			t.Fatalf("response missing FORBIDDEN code: %s", response.Body.String())
		}
		if manager.confirmOpts.OverrideSafety {
			t.Fatalf("Confirm should not have been called with override for non-admin")
		}
	})

	t.Run("no principal gets 403", func(t *testing.T) {
		manager := &fakeDSLWorkflowManager{version: version, contract: contract}
		h := dslConfirmHandler(t, manager)

		request := httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/confirm?override_safety=true", nil)
		request.SetPathValue("id", "workflow-1")
		response := httptest.NewRecorder()
		h.ConfirmDSLWorkflow(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for missing principal, got %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("admin gets 200 and override forwarded", func(t *testing.T) {
		manager := &fakeDSLWorkflowManager{version: version, contract: contract}
		h := dslConfirmHandler(t, manager)

		request := httptest.NewRequest(http.MethodPost, "/api/v1/dsl-workflows/workflow-1/confirm?override_safety=true", nil)
		request = request.WithContext(authz.WithPrincipal(request.Context(), authz.Principal{
			Subject: "admin-1", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin},
		}))
		request.SetPathValue("id", "workflow-1")
		response := httptest.NewRecorder()
		h.ConfirmDSLWorkflow(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected 200 for admin override, got %d: %s", response.Code, response.Body.String())
		}
		if !manager.confirmOpts.OverrideSafety {
			t.Fatalf("OverrideSafety not forwarded to manager")
		}
		if manager.confirmOpts.OverrideActor != "admin-1" {
			t.Fatalf("OverrideActor not set to admin subject, got %q", manager.confirmOpts.OverrideActor)
		}
	})
}
