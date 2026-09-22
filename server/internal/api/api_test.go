package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	f, err := os.CreateTemp("", "api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
		AuditActor:                     "admin",
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	h := NewHandler(s, sch, nil, nil, cfg, logger)
	metrics := NewMetrics()
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	return httptest.NewServer(router), s
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestCapabilities(t *testing.T) {
	cfg := &config.Config{
		RecordingV2Enabled:      true,
		WorkflowV2Enabled:       true,
		WorkerProtocolV2Enabled: true,
		MCPEnabled:              false,
	}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	recorder := httptest.NewRecorder()
	h.Capabilities(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	var got CapabilitiesResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != "v1" || got.WorkspaceMode != "single" || got.DefaultWorkspaceID != "default" {
		t.Fatalf("unexpected capability identity: %+v", got)
	}
	if len(got.WorkerProtocolVersions) != 2 || got.WorkerProtocolVersions[1] != "v2" {
		t.Fatalf("unexpected worker protocols: %v", got.WorkerProtocolVersions)
	}
	if !got.Features.RecordingV2 || !got.Features.WorkflowV2 || !got.Features.PageMarks || got.Features.MCP {
		t.Fatalf("unexpected feature flags: %+v", got.Features)
	}
	if got.RecordingLimits.MaxActions != 500 || got.RecordingLimits.MaxDurationMs != int64((2*time.Hour)/time.Millisecond) || got.RecordingLimits.MaxCompressedBytes != 25*1024*1024 {
		t.Fatalf("unexpected recording limits: %+v", got.RecordingLimits)
	}
}

func TestAdminHandlersDeriveOwnerAndWorkspaceFromPrincipal(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	ctxDefault := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "admin", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin}})
	ctxTenant := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "tenant-admin", WorkspaceID: "tenant-b", Roles: []authz.Role{authz.RoleAdmin}})
	if err := st.CreateWorkspace(ctxDefault, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AuditActor: "configured-fallback"}
	h := NewHandler(st, nil, nil, nil, cfg, zap.NewNop())
	payload, _ := json.Marshal(models.Rule{
		ID: "tenant-rule", Version: "1.0.0", Name: "Tenant rule", Owner: "spoofed-owner",
		Domain: store.JSON("example.com"), Steps: store.JSON([]any{"step"}), Priority: models.PriorityNormal,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules", bytes.NewReader(payload)).WithContext(ctxTenant)
	recorder := httptest.NewRecorder()
	h.CreateRule(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	rule, err := st.GetRuleByID(ctxTenant, "tenant-rule")
	if err != nil {
		t.Fatal(err)
	}
	if rule.WorkspaceID != "tenant-b" || rule.Owner != "tenant-admin" {
		t.Fatalf("request identity fields were trusted: %+v", rule)
	}
	if _, err := st.GetRuleByID(ctxDefault, "tenant-rule"); !errors.Is(err, store.ErrRuleNotFound) {
		t.Fatalf("default workspace should not see tenant rule, got %v", err)
	}

	audit, total, err := st.ListAuditLogs(ctxTenant, store.ListAuditLogsFilter{})
	if err != nil || total != 1 || audit[0].Actor != "tenant-admin" || audit[0].WorkspaceID != "tenant-b" {
		t.Fatalf("unexpected principal-derived audit: total=%d audit=%v err=%v", total, audit, err)
	}
}

func TestHealthReady(t *testing.T) {
	srv, s := newTestServer(t)
	defer srv.Close()

	// Default readiness check succeeds when the database is reachable.
	resp, err := http.Get(srv.URL + "/health?ready=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Close the store to simulate a database failure.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(srv.URL + "/health?ready=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "error" {
		t.Fatalf("expected status error, got %v", body["status"])
	}
	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("expected checks map, got %T", body["checks"])
	}
	if checks["database"] != "unreachable" {
		t.Fatalf("expected database unreachable, got %v", checks["database"])
	}
}

func createRuleAndTask(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	rule := models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return task.ID
}

// H-4 regression: POST /admin/rules with an existing rule ID must NOT silently
// overwrite the rule. The store's CreateRule is an upsert (INSERT ... ON
// CONFLICT DO UPDATE), so without a handler-level guard a client can replace
// any existing rule by supplying its ID, bypassing UpdateRule's approval
// checks. The handler must reject the second POST with 409.
func TestCreateRuleRejectsDuplicateClientID(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:        "dup-rule-H4",
		Version:   "1.0.0",
		Name:      "Original",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected first create to succeed with 200, got %d", resp.StatusCode)
	}

	// Second POST with same ID but different content → must be rejected.
	rule.Name = "Hijacked"
	body, _ = json.Marshal(rule)
	resp2, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 409 for duplicate rule ID, got %d: %s", resp2.StatusCode, string(body))
	}
}

// M-2 regression: List endpoints must cap the limit parameter. Without an
// upper bound, limit=999999999 asks the database to load all rows.
func TestListRulesCapsLimit(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/rules?limit=999999")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for limit above cap, got %d: %s", resp.StatusCode, string(body))
	}
}

func TestCreateRuleAndTask(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:        "test-rule",
		Version:   "1.0.0",
		Name:      "Test",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var claim ClaimTaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	if claim.RuleID != "test-rule" {
		t.Fatalf("expected test-rule, got %s", claim.RuleID)
	}
}

func TestCheckpointAndResultsEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	// Submit a checkpoint
	cpBody, _ := json.Marshal(CheckpointRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Name:     "after-login",
		Payload:  map[string]any{"url": "https://example.com/page2"},
	})
	resp, err := http.Post(srv.URL+"/tasks/"+taskID+"/checkpoints", "application/json", bytes.NewReader(cpBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Get latest checkpoint
	resp, err = http.Get(srv.URL + "/tasks/" + taskID + "/checkpoints/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var cp CheckpointResponse
	if err := json.NewDecoder(resp.Body).Decode(&cp); err != nil {
		t.Fatal(err)
	}
	if cp.Name != "after-login" {
		t.Fatalf("expected after-login, got %s", cp.Name)
	}

	// Submit a result
	resultBody, _ := json.Marshal(ResultRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Payload:  map[string]any{"items": []any{"a", "b"}},
	})
	resp, err = http.Post(srv.URL+"/results", "application/json", bytes.NewReader(resultBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Get aggregated results
	resp, err = http.Get(srv.URL + "/tasks/" + taskID + "/results")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var results []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestClaimRespectsApprovalStatus(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "pending-rule",
		Version:        "1.0.0",
		Name:           "Pending Rule",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: "pending",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 creating rule, got %d", resp.StatusCode)
	}

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      "pending-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 creating task, got %d", resp.StatusCode)
	}

	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content for pending rule, got %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/admin/rules/pending-rule/approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 approving rule, got %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK after approval, got %d", resp.StatusCode)
	}
}

func TestListRulesEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	for i, id := range []string{"rule-1", "rule-2"} {
		rule := models.Rule{
			ID:             id,
			Version:        "1.0.0",
			Name:           id,
			Domain:         store.JSON(`"example.com"`),
			Steps:          store.JSON([]any{"step1"}),
			Enabled:        i == 0,
			Owner:          "owner-" + id,
			ApprovalStatus: "approved",
			Priority:       models.PriorityNormal,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}
		body, _ := json.Marshal(rule)
		resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(srv.URL + "/admin/rules?limit=10&offset=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list ListRulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected total 2, got %d", list.Total)
	}
	if len(list.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(list.Rules))
	}

	// Filter by enabled.
	resp, err = http.Get(srv.URL + "/admin/rules?enabled=true")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Rules[0].ID != "rule-1" {
		t.Fatalf("unexpected enabled filter result: %+v", list)
	}

	// Bad enabled param.
	resp, err = http.Get(srv.URL + "/admin/rules?enabled=notbool")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAdminGetRuleEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "get-rule",
		Version:        "1.0.0",
		Name:           "Get Rule Test",
		Domain:         store.JSON(`"example.com"`),
		Steps:          store.JSON([]any{"step1"}),
		Enabled:        true,
		Owner:          "alice",
		ApprovalStatus: "approved",
		Priority:       models.PriorityNormal,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Get existing rule.
	resp, err = http.Get(srv.URL + "/admin/rules/get-rule")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got RuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "get-rule" {
		t.Fatalf("unexpected rule id: %s", got.ID)
	}
	if got.Source != "pageagent" {
		t.Fatalf("expected default source pageagent, got %s", got.Source)
	}

	// Missing rule returns 404.
	resp, err = http.Get(srv.URL + "/admin/rules/missing-rule")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestUpdateRuleEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "update-rule",
		Version:        "1.0.0",
		Name:           "Original",
		Domain:         store.JSON(`"example.com"`),
		Steps:          store.JSON([]any{"step1"}),
		Enabled:        true,
		Owner:          "alice",
		ApprovalStatus: "approved",
		Priority:       models.PriorityNormal,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Partial update: only name and enabled.
	update := map[string]any{
		"name":    "Updated",
		"enabled": false,
	}
	body, _ = json.Marshal(update)
	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/update-rule", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var updated RuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Updated" {
		t.Fatalf("expected name Updated, got %s", updated.Name)
	}
	if updated.Enabled != false {
		t.Fatalf("expected enabled false, got %v", updated.Enabled)
	}
	if updated.Owner != "admin" {
		t.Fatalf("expected principal-derived owner admin, got %s", updated.Owner)
	}

	// Verify in store.
	got, err := st.GetRuleByID(context.Background(), "update-rule")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Updated" || got.Enabled != false || got.Owner != "admin" {
		t.Fatalf("unexpected stored rule: %+v", got)
	}

	// Update missing rule returns 404.
	req, err = http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/missing-rule", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDeleteRuleEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "delete-rule",
		Version:        "1.0.0",
		Name:           "ToDelete",
		Domain:         store.JSON(`"example.com"`),
		Steps:          store.JSON([]any{"step1"}),
		Enabled:        true,
		ApprovalStatus: "approved",
		Priority:       models.PriorityNormal,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/admin/rules/delete-rule", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if _, err := st.GetRuleByID(context.Background(), rule.ID); !errors.Is(err, store.ErrRuleNotFound) {
		t.Fatalf("expected rule deleted, got %v", err)
	}

	// Delete missing returns 404.
	req, err = http.NewRequest(http.MethodDelete, srv.URL+"/admin/rules/missing-rule", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestListTasksEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	resp, err := http.Get(srv.URL + "/admin/tasks?limit=10&offset=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list ListTasksResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("expected total 1, got %d", list.Total)
	}
	if len(list.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(list.Tasks))
	}
	if list.Tasks[0].ID != taskID {
		t.Fatalf("expected task %s, got %s", taskID, list.Tasks[0].ID)
	}

	// Filter by status.
	resp, err = http.Get(srv.URL + "/admin/tasks?status=pending")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Tasks[0].Status != string(models.TaskStatusPending) {
		t.Fatalf("unexpected status filter result: %+v", list)
	}

	// Bad created_after param.
	resp, err = http.Get(srv.URL + "/admin/tasks?created_after=not-a-time")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCancelTaskEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	resp, err := http.Post(srv.URL+"/admin/tasks/"+taskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	task, err := st.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusCancelled {
		t.Fatalf("expected cancelled, got %s", task.Status)
	}

	// Cancel again returns 409.
	resp, err = http.Post(srv.URL+"/admin/tasks/"+taskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}

	// Running task gets cancel_requested flag.
	runningTask := models.Task{
		ID:          store.NewID(),
		RuleID:      "test-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusRunning,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ := json.Marshal(runningTask)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 creating running task, got %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/admin/tasks/"+runningTask.ID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 cancelling running task, got %d", resp.StatusCode)
	}

	task, err = st.GetTaskByID(context.Background(), runningTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusRunning {
		t.Fatalf("expected running, got %s", task.Status)
	}
	if !task.CancelRequested {
		t.Fatal("expected cancel_requested set")
	}

	// GET /tasks/{id} exposes cancelRequested.
	resp, err = http.Get(srv.URL + "/tasks/" + runningTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var taskResp TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatal(err)
	}
	if !taskResp.CancelRequested {
		t.Fatal("expected task response cancelRequested true")
	}

	// Missing task returns 404.
	resp, err = http.Post(srv.URL+"/admin/tasks/missing-task/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHeartbeatResponseIncludesCancelRequested(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	// Claim the task so heartbeat lease renewal succeeds.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 claiming task, got %d", resp.StatusCode)
	}

	hbBody, _ := json.Marshal(HeartbeatRequest{TaskID: taskID, WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(hbBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var hb HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&hb); err != nil {
		t.Fatal(err)
	}
	if !hb.Success {
		t.Fatal("expected heartbeat success")
	}
	if hb.CancelRequested {
		t.Fatal("expected cancelRequested false")
	}

	// Request cancellation and verify the next heartbeat signals it.
	resp, err = http.Post(srv.URL+"/admin/tasks/"+taskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	task, err := st.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusCancelled {
		t.Fatalf("expected cancelled, got %s", task.Status)
	}

	// For a cancelled task the heartbeat lease renewal will fail; test the running path separately.
}

func TestHeartbeatCancelSignalForRunningTask(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "running-cancel-rule",
		Version:        "1.0.0",
		Name:           "Running Cancel",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: "approved",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      rule.ID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Claim the task so it is leased to worker-1, then transition to running.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 claiming task, got %d", resp.StatusCode)
	}
	if err := st.UpdateTaskStatus(context.Background(), task.ID, "worker-1", string(models.TaskStatusRunning), "", ""); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Post(srv.URL+"/admin/tasks/"+task.ID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	hbBody, _ := json.Marshal(HeartbeatRequest{TaskID: task.ID, WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(hbBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var hb HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&hb); err != nil {
		t.Fatal(err)
	}
	if !hb.Success || !hb.CancelRequested {
		t.Fatalf("expected success and cancelRequested true, got %+v", hb)
	}
}

func TestPreviewScheduleEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/schedules/preview?expression=0+*+*+*+*&count=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var preview PreviewScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(preview.Runs))
	}
	if preview.Expression != "0 * * * *" {
		t.Fatalf("expected expression preserved, got %s", preview.Expression)
	}
	if preview.Timezone != "UTC" {
		t.Fatalf("expected default UTC timezone, got %s", preview.Timezone)
	}
	for _, r := range preview.Runs {
		if _, err := time.Parse(time.RFC3339, r); err != nil {
			t.Fatalf("expected RFC3339 run time, got %s: %v", r, err)
		}
	}

	// Invalid cron expression returns 400.
	resp, err = http.Get(srv.URL + "/admin/schedules/preview?expression=invalid")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/admin/schedules/preview?expression=0+9+*+*+*&after=2024-01-01T00%3A30%3A00Z&timezone=Asia%2FTaipei&count=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected timezone preview 200, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if preview.Timezone != "Asia/Taipei" || len(preview.Runs) != 1 || preview.Runs[0] != "2024-01-01T01:00:00Z" {
		t.Fatalf("unexpected timezone preview: %+v", preview)
	}
}

func TestRetryTaskEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "retry-test-rule",
		Version:        "1.0.0",
		Name:           "Retry Test",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: "approved",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	task := models.Task{
		ID:           store.NewID(),
		RuleID:       "retry-test-rule",
		RuleVersion:  "1.0.0",
		Status:       models.TaskStatusFailed,
		Priority:     models.PriorityNormal,
		RetryCount:   2,
		MaxRetries:   3,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		CompletedAt:  sql.NullTime{Time: time.Now().UTC(), Valid: true},
		ErrorType:    sql.NullString{String: "timeout", Valid: true},
		ErrorMessage: sql.NullString{String: "timed out", Valid: true},
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Post(srv.URL+"/admin/tasks/"+task.ID+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	got, err := st.GetTaskByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TaskStatusPending {
		t.Fatalf("expected pending, got %s", got.Status)
	}
	if got.RetryCount != 0 {
		t.Fatalf("expected retry_count 0, got %d", got.RetryCount)
	}
	if got.ErrorType.Valid {
		t.Fatal("expected error_type cleared")
	}

	// Retry pending task returns 409.
	resp, err = http.Post(srv.URL+"/admin/tasks/"+task.ID+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}

	// Missing task returns 404.
	resp, err = http.Post(srv.URL+"/admin/tasks/missing-task/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWorkerCannotCancelOrRetry(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	resp, err := http.Post(srv.URL+"/tasks/"+taskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected worker cancel 404, got %d", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/tasks/"+taskID+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected worker retry 404, got %d", resp.StatusCode)
	}
}

func TestClaimSetsLeasedStatus(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	task, err := st.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusLeased {
		t.Fatalf("expected leased, got %s", task.Status)
	}
}

func TestAuditLogsRecordedAndQueryable(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "audit-rule",
		Version:        "1.0.0",
		Name:           "Audit Test",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: "pending",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 creating rule, got %d", resp.StatusCode)
	}

	// Approve the rule.
	resp, err = http.Post(srv.URL+"/admin/rules/audit-rule/approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 approving rule, got %d", resp.StatusCode)
	}

	task := models.Task{
		ID:          store.NewID(),
		RuleID:      "audit-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 creating task, got %d", resp.StatusCode)
	}

	// Cancel the task.
	resp, err = http.Post(srv.URL+"/admin/tasks/"+task.ID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 cancelling task, got %d", resp.StatusCode)
	}

	// Query audit logs.
	resp, err = http.Get(srv.URL + "/admin/audit_logs?limit=10&offset=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 listing audit logs, got %d", resp.StatusCode)
	}
	var list ListAuditLogsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total < 3 {
		t.Fatalf("expected at least 3 audit logs, got %d", list.Total)
	}

	actions := map[string]bool{}
	for _, log := range list.Logs {
		actions[log.Action] = true
	}
	if !actions["create_rule"] || !actions["approve_rule"] || !actions["create_task"] || !actions["cancel_task"] {
		t.Fatalf("expected audit actions recorded, got actions=%v logs=%v", actions, list.Logs)
	}

	// Verify the create_rule payload contains the rule id and name.
	var createPayload map[string]any
	for _, log := range list.Logs {
		if log.Action == "create_rule" {
			createPayload = rawToMap(log.Payload)
			break
		}
	}
	if createPayload["ruleId"] != "audit-rule" || createPayload["name"] != "Audit Test" {
		t.Fatalf("unexpected create_rule audit payload: %v", createPayload)
	}

	// Filter by action.
	resp, err = http.Get(srv.URL + "/admin/audit_logs?action=create_rule")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Logs[0].Action != "create_rule" {
		t.Fatalf("unexpected action filter result: %+v", list)
	}

	// Filter by resource_type.
	resp, err = http.Get(srv.URL + "/admin/audit_logs?resource_type=task")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected 2 task audit logs, got %d", list.Total)
	}
	for _, log := range list.Logs {
		if log.ResourceType != "task" {
			t.Fatalf("expected task resource type, got %s", log.ResourceType)
		}
	}

	// Filter by resource_id.
	resp, err = http.Get(srv.URL + "/admin/audit_logs?resource_id=audit-rule")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected 2 audit logs for audit-rule, got %d", list.Total)
	}

	// Bad created_after param.
	resp, err = http.Get(srv.URL + "/admin/audit_logs?created_after=not-a-time")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Verify actor is the configured default.
	logs, _, err := st.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range logs {
		if log.Actor != "admin" {
			t.Fatalf("expected actor admin, got %s", log.Actor)
		}
	}
}

func TestMaxBodySizeMiddlewareRejectsLargeBody(t *testing.T) {
	handler := MaxBodySizeMiddleware(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		w.Write(body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this body is way too long"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestMaxBodySizeMiddlewareAllowsSmallBody(t *testing.T) {
	handler := MaxBodySizeMiddleware(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		w.Write(body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("tiny"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "tiny" {
		t.Fatalf("expected body tiny, got %q", got)
	}
}

func TestRequestTimeoutMiddlewareTimesOut(t *testing.T) {
	logger := zap.NewNop()
	handler := RequestTimeoutMiddleware(50*time.Millisecond, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", rec.Code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout middleware took too long: %v", elapsed)
	}
}

func TestRequestTimeoutMiddlewareRespectsShorterDeadline(t *testing.T) {
	logger := zap.NewNop()
	handler := RequestTimeoutMiddleware(5*time.Second, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", rec.Code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout middleware took too long: %v", elapsed)
	}
}

func TestRequestTimeoutMiddlewareRecoversPanic(t *testing.T) {
	logger := zap.NewNop()
	handler := RequestTimeoutMiddleware(5*time.Second, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("handler panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "INTERNAL_ERROR") {
		t.Fatalf("expected internal error body, got %q", rec.Body.String())
	}
}

func TestRequestTimeoutMiddlewareLateWriteDoesNotLeak(t *testing.T) {
	logger := zap.NewNop()
	handlerDone := make(chan struct{})
	parentReturned := make(chan struct{})
	handler := RequestTimeoutMiddleware(50*time.Millisecond, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-parentReturned
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("late write"))
		close(handlerDone)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	close(parentReturned)
	<-handlerDone

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "late write") {
		t.Fatalf("late write leaked into response: %q", rec.Body.String())
	}
}

func TestRequestTimeoutMiddlewareRespectsCanceledContext(t *testing.T) {
	logger := zap.NewNop()
	handler := RequestTimeoutMiddleware(50*time.Millisecond, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the middleware cancels the context, then return promptly.
		<-r.Context().Done()
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", rec.Code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout middleware took too long: %v", elapsed)
	}
}
