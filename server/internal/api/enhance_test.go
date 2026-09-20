package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type fakeEnhancerProvider struct {
	resp *llm.CompletionResponse
	name string
}

func (p *fakeEnhancerProvider) Name() string { return p.name }

func (p *fakeEnhancerProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return p.resp, nil
}

func newTestJobManager(t *testing.T, s *store.Store, content string) *llm.JobManager {
	t.Helper()
	cfg := &config.Config{
		LLMEnabled:  true,
		LLMProvider: "fake",
		LLMModel:    "fake-model",
	}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeEnhancerProvider{
		name: "fake",
		resp: &llm.CompletionResponse{Content: content},
	})
	return llm.NewJobManager(s, cfg, orch, zap.NewNop())
}

func newDefaultTestJobManager(t *testing.T, s *store.Store) *llm.JobManager {
	t.Helper()
	return newTestJobManager(t, s, `{"selectors": {"title": {"selector": "h1", "reason": "main title"}}, "steps": [{"op": "replace", "path": "/selectors/title/selector", "value": "h2"}], "variables": {}, "suggestions": ["use h2 for title"]}`)
}

func setupEnhanceTest(t *testing.T) (*store.Store, *scheduler.Scheduler, *llm.JobManager, *config.Config) {
	t.Helper()
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{AdminAPIKey: "test-admin-key", AuditActor: "test"}
	sch := scheduler.New(s, cfg, zap.NewNop(), time.Minute, 3)
	return s, sch, newDefaultTestJobManager(t, s), cfg
}

func TestEnhanceRuleRequiresBaselineId(t *testing.T) {
	cfg := &config.Config{AdminAPIKey: "admin", AuditActor: "test"}
	s, err := store.NewWithConfig(cfg, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sch := scheduler.New(s, cfg, zap.NewNop(), cfg.LeaseDuration, cfg.MaxRetries)
	h := NewHandler(s, sch, nil, nil, cfg, zap.NewNop())

	body, _ := json.Marshal(EnhanceRuleRequest{
		Recording:    map[string]any{"events": []any{}},
		BaselineRule: map[string]any{"name": "test", "entry": "http://example.com", "steps": []any{}},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	h.EnhanceRule(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestEnhanceRule(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Enabled:        true,
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{"url": "http://example.com"},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "make title selector h2",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.EnhanceRule(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp EnhanceRuleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.JobID == "" {
		t.Fatal("expected job id")
	}

	manager.ProcessOnce(context.Background())

	rule, err := s.GetRuleByID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	if rule.ApprovalStatus != string(models.RuleApprovalPending) {
		t.Fatalf("expected pending approval status, got %s", rule.ApprovalStatus)
	}
	if rule.Enabled {
		t.Fatal("expected rule to be disabled")
	}

	enhancement, err := s.GetRuleEnhancementByRuleID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.RuleID != "rule-base" {
		t.Fatalf("expected rule id rule-base, got %s", enhancement.RuleID)
	}
	if enhancement.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending status, got %s", enhancement.Status)
	}

	job, err := s.GetLLMJob(context.Background(), resp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed job, got %s", job.Status)
	}
	if job.RuleID != "rule-base" {
		t.Fatalf("expected job rule id rule-base, got %s", job.RuleID)
	}
}

func TestEnhanceRuleAuditPrompts(t *testing.T) {
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &config.Config{AdminAPIKey: "test-admin-key", AuditActor: "test", LLMAuditPrompts: true}
	sch := scheduler.New(s, cfg, zap.NewNop(), time.Minute, 3)
	manager := newDefaultTestJobManager(t, s)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	body := map[string]any{
		"recording":    map[string]any{"events": []any{}, "domSnapshots": []any{}, "meta": map[string]any{}},
		"baselineRule": map[string]any{"id": "rule-audit", "version": "1.0.0", "name": "Audit", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "audit me",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.EnhanceRule(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	logs, total, err := s.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		Action:       "rule_enhance_submitted",
		ResourceType: "rule",
		ResourceID:   "rule-audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("expected 1 audit log, got %d", total)
	}
	payload := map[string]any{}
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["promptHash"] == "" {
		t.Fatal("expected non-empty promptHash in audit payload")
	}
	prefix, _ := payload["promptPrefix"].(string)
	if prefix == "" {
		t.Fatal("expected non-empty promptPrefix in audit payload")
	}
	if len(prefix) > 200 {
		t.Fatalf("expected promptPrefix <= 200 chars, got %d", len(prefix))
	}
}

func TestEnhanceRuleEnforcedAuditStoresHashOnly(t *testing.T) {
	const sentinel = "enforced-audit-user-hint-secret-sentinel"

	setEnforcedPolicyEnvironment(t, "https://provider.example.test/v1")
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatal(err)
	}
	cfg.AdminAPIKey = "test-admin-key"
	cfg.AuditActor = "test"
	cfg.LLMAuditPrompts = true

	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sch := scheduler.New(s, cfg, zap.NewNop(), time.Minute, 3)
	h := NewHandler(s, sch, newDefaultTestJobManager(t, s), nil, cfg, zap.NewNop())

	body := map[string]any{
		"recording":    map[string]any{"events": []any{}, "domSnapshots": []any{}, "meta": map[string]any{}},
		"baselineRule": map[string]any{"id": "rule-enforced-audit", "version": "1.0.0", "name": "Audit", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     sentinel,
	}
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(encoded))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	response := httptest.NewRecorder()
	h.EnhanceRule(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", response.Code, response.Body.String())
	}

	logs, total, err := s.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		Action:       "rule_enhance_submitted",
		ResourceType: "rule",
		ResourceID:   "rule-enforced-audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("expected one audit log, got total=%d len=%d", total, len(logs))
	}
	if bytes.Contains(logs[0].Payload, []byte(sentinel)) {
		t.Fatal("enforced audit payload exposed the raw user hint")
	}

	payload := map[string]any{}
	if err := json.Unmarshal(logs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if hash, _ := payload["promptHash"].(string); hash == "" {
		t.Fatal("expected non-empty promptHash in audit payload")
	}
	if _, ok := payload["promptPrefix"]; ok {
		t.Fatal("enforced audit payload must not include promptPrefix")
	}
}

func TestGetRuleEnhancement(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	h.EnhanceRule(httptest.NewRecorder(), req)
	manager.ProcessOnce(context.Background())

	req = httptest.NewRequest(http.MethodGet, "/admin/rules/rule-base/enhancement", nil)
	req.SetPathValue("id", "rule-base")
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.GetRuleEnhancement(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp RuleEnhancementResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RuleID != "rule-base" {
		t.Fatalf("expected rule-base, got %s", resp.RuleID)
	}
	if resp.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending status, got %s", resp.Status)
	}
}

func TestAcceptEnhancement(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	createdAt := time.Now().UTC().Add(-time.Hour)
	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Enabled:        true,
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	enhanceRR := httptest.NewRecorder()
	h.EnhanceRule(enhanceRR, req)
	if enhanceRR.Code != http.StatusAccepted {
		t.Fatalf("enhance expected 202, got %d: %s", enhanceRR.Code, enhanceRR.Body.String())
	}
	manager.ProcessOnce(context.Background())

	req = httptest.NewRequest(http.MethodPost, "/admin/rules/rule-base/enhancement/accept", nil)
	req.SetPathValue("id", "rule-base")
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.AcceptEnhancement(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	active, err := s.GetRuleByID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	if active.ApprovalStatus != string(models.RuleApprovalApproved) {
		t.Fatalf("expected approved status, got %s", active.ApprovalStatus)
	}
	if !active.Enabled {
		t.Fatal("expected rule to be enabled")
	}
	if !active.CreatedAt.Equal(createdAt) {
		t.Fatalf("expected createdAt preserved, got %v", active.CreatedAt)
	}

	enhancement, err := s.GetRuleEnhancementByRuleID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.Status != string(models.EnhancementStatusApproved) {
		t.Fatalf("expected approved status, got %s", enhancement.Status)
	}
}

func TestRejectEnhancement(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	h.EnhanceRule(httptest.NewRecorder(), req)
	manager.ProcessOnce(context.Background())

	enhancement, err := s.GetRuleEnhancementByRuleID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	enhancementID := enhancement.ID

	req = httptest.NewRequest(http.MethodPost, "/admin/rules/rule-base/enhancement/reject", nil)
	req.SetPathValue("id", "rule-base")
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.RejectEnhancement(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var status string
	if err := s.DB().QueryRowContext(context.Background(), `SELECT status FROM rule_enhancements WHERE id = ?`, enhancementID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(models.EnhancementStatusRejected) {
		t.Fatalf("expected rejected status, got %s", status)
	}

	if _, err := s.GetRuleByID(context.Background(), "rule-base"); !errors.Is(err, store.ErrRuleNotFound) {
		t.Fatalf("expected rule to be deleted, got err %v", err)
	}
}

func TestEnhanceRuleReturnsBaselineWhenEnhancerFails(t *testing.T) {
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &config.Config{AdminAPIKey: "test-admin-key", AuditActor: "test"}
	sch := scheduler.New(s, cfg, zap.NewNop(), time.Minute, 3)
	manager := newTestJobManager(t, s, `{"selectors": {}, "steps": [{"op": "replace"}], "variables": {}, "suggestions": []}`)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Enabled:        true,
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.EnhanceRule(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	manager.ProcessOnce(context.Background())

	rule, err := s.GetRuleByID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	// On failure the worker falls back to the baseline but still creates a pending
	// enhancement so the user can accept/reject the unchanged rule.
	if rule.ApprovalStatus != string(models.RuleApprovalPending) {
		t.Fatalf("expected pending approval status, got %s", rule.ApprovalStatus)
	}

	enhancement, err := s.GetRuleEnhancementByRuleID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending enhancement status, got %s", enhancement.Status)
	}

	job, err := s.GetLLMJob(context.Background(), jsonMap(rr.Body.Bytes())["jobId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed job status, got %s", job.Status)
	}
	if job.ResultError == "" {
		t.Fatal("expected job to record result error")
	}
}

func TestAcceptEnhancementRequiresPendingStatus(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Enabled:        true,
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "",
	}
	b, _ := json.Marshal(body)
	enhanceReq := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	enhanceReq.Header.Set("Authorization", "Bearer test-admin-key")
	h.EnhanceRule(httptest.NewRecorder(), enhanceReq)
	manager.ProcessOnce(context.Background())

	accept := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/rules/rule-base/enhancement/accept", nil)
		req.SetPathValue("id", "rule-base")
		req.Header.Set("Authorization", "Bearer test-admin-key")
		rr := httptest.NewRecorder()
		h.AcceptEnhancement(rr, req)
		return rr
	}

	if rr := accept(); rr.Code != http.StatusOK {
		t.Fatalf("first accept expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := accept(); rr.Code != http.StatusBadRequest {
		t.Fatalf("second accept expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRejectEnhancementRequiresPendingStatus(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	baseline := &models.Rule{
		ID:             "rule-base",
		Version:        "1.0.0",
		Name:           "Baseline",
		Entry:          "https://example.com",
		ApprovalStatus: string(models.RuleApprovalApproved),
		Enabled:        true,
		Steps:          models.JSON(`[{"type":"open","url":"https://example.com"}]`),
	}
	if err := s.CreateRule(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"recording":    map[string]any{},
		"baselineRule": map[string]any{"id": "rule-base", "version": "1.0.0", "name": "Baseline", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open", "url": "https://example.com"}}},
		"userHint":     "",
	}
	b, _ := json.Marshal(body)
	enhanceReq := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	enhanceReq.Header.Set("Authorization", "Bearer test-admin-key")
	h.EnhanceRule(httptest.NewRecorder(), enhanceReq)
	manager.ProcessOnce(context.Background())

	enhancement, err := s.GetRuleEnhancementByRuleID(context.Background(), "rule-base")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRuleEnhancementStatus(context.Background(), enhancement.ID, string(models.EnhancementStatusRejected)); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/rules/rule-base/enhancement/reject", nil)
	req.SetPathValue("id", "rule-base")
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.RejectEnhancement(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func jsonMap(b []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// H-3 regression: a non-string baselineRule.id (e.g., JSON number) used to
// pass the nil/"" check and then panic on the unchecked type assertion at
// enhance.go:67/80. The handler must return 400 instead.
func TestEnhanceRuleRejectsNonStringBaselineID(t *testing.T) {
	s, sch, manager, cfg := setupEnhanceTest(t)
	h := NewHandler(s, sch, manager, nil, cfg, zap.NewNop())

	body := map[string]any{
		"recording":    map[string]any{"events": []any{}},
		"baselineRule": map[string]any{"id": 12345, "name": "x", "entry": "https://example.com", "steps": []any{map[string]any{"type": "open"}}},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/enhance", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-admin-key")
	rr := httptest.NewRecorder()
	h.EnhanceRule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-string baselineRule.id, got %d: %s", rr.Code, rr.Body.String())
	}
}
