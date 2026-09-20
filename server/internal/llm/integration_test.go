//go:build llm_integration

package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/api"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/cache"
	"github.com/singhand-labs/AegisCrawler/internal/llm/providers"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

// fakeProvider is a deterministic LLM provider used for integration testing.
type fakeProvider struct {
	resp  *llm.CompletionResponse
	name  string
	calls int
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls++
	return p.resp, nil
}

// adminRequest returns a request with the test admin bearer token.
func adminRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-admin-key")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// newTestServer builds a full HTTP server with a fake LLM provider.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	if os.Getenv("RUN_LLM_INTEGRATION") != "1" {
		t.Skip("set RUN_LLM_INTEGRATION=1 to run LLM integration tests")
	}

	s, err := store.NewWithConfig(
		&config.Config{SQLiteJournalMode: "WAL"},
		":memory:",
		"integration-cache-encryption-key-for-tests",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		AdminAPIKey:                    "test-admin-key",
		AuditActor:                     "integration-test",
		LeaseDuration:                  time.Minute,
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
		LLMEnabled:                     true,
		LLMProvider:                    "fake",
		LLMModel:                       "fake-model",
		LLMTemperature:                 0.2,
		LLMCacheTTL:                    0,
		LLMEnableReflection:            false,
	}

	logger := zap.NewNop()
	orch := llm.NewOrchestrator(cfg, logger)
	orch.RegisterProvider(&fakeProvider{
		name: "fake",
		resp: &llm.CompletionResponse{
			Content: `{"selectors":{"title":{"selector":"h1","reason":"main heading"}},"steps":[{"op":"replace","path":"/selectors/title/selector","value":"h2"}],"variables":{},"suggestions":["use h2 for the main title"]}`},
	})
	manager := llm.NewJobManager(s, cfg, orch, logger)

	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	ctx, cancel := context.WithCancel(context.Background())
	sch.Start(ctx, 30*time.Second)
	go manager.StartWorker(ctx, 500*time.Millisecond)
	t.Cleanup(cancel)

	h := api.New(s, sch, manager, nil, nil, cfg, logger, nil)
	return httptest.NewServer(h)
}

func pollJob(t *testing.T, srv *httptest.Server, jobID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := adminRequest(t, http.MethodGet, srv.URL+"/admin/rules/enhancements/jobs/"+jobID, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("expected 200 polling job, got %d", resp.StatusCode)
		}
		var job models.LLMJob
		if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if job.Status == string(models.LLMJobStatusCompleted) || job.Status == string(models.LLMJobStatusFailed) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job %s did not complete in time", jobID)
}

func TestIntegrationEnhanceGetAcceptRejectFlow(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Seed baseline rule.
	baseline := map[string]any{
		"id":             "rule-base",
		"version":        "1.0.0",
		"name":           "Baseline",
		"entry":          "https://example.com",
		"approvalStatus": string(models.RuleApprovalApproved),
		"enabled":        true,
		"steps": []any{
			map[string]any{"type": "open", "url": "https://example.com"},
		},
	}
	body, _ := json.Marshal(baseline)
	resp, err := http.DefaultClient.Do(adminRequest(t, http.MethodPost, srv.URL+"/admin/rules", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 creating baseline rule, got %d", resp.StatusCode)
	}

	// Enhance the rule.
	enhanceReq := map[string]any{
		"recording":    map[string]any{"events": []any{}, "url": "https://example.com"},
		"baselineRule": baseline,
		"userHint":     "use h2 for title",
	}
	body, _ = json.Marshal(enhanceReq)
	req := adminRequest(t, http.MethodPost, srv.URL+"/admin/rules/enhance", body)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 202 enhancing rule, got %d: %s", resp.StatusCode, string(b))
	}
	var enhanceResp api.EnhanceRuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&enhanceResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if enhanceResp.JobID == "" {
		t.Fatal("expected job id")
	}

	pollJob(t, srv, enhanceResp.JobID)

	// Get the enhancement.
	req = adminRequest(t, http.MethodGet, srv.URL+"/admin/rules/rule-base/enhancement", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 getting enhancement, got %d", resp.StatusCode)
	}
	var getResp api.RuleEnhancementResponse
	if err := json.NewDecoder(resp.Body).Decode(&getResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if getResp.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending status, got %s", getResp.Status)
	}

	// Accept the enhancement.
	req = adminRequest(t, http.MethodPost, srv.URL+"/admin/rules/rule-base/enhancement/accept", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 accepting enhancement, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	req = adminRequest(t, http.MethodGet, srv.URL+"/admin/rules/rule-base", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 getting rule after accept, got %d", resp.StatusCode)
	}
	var accepted api.RuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if accepted.ApprovalStatus != string(models.RuleApprovalApproved) {
		t.Fatalf("expected approved status, got %s", accepted.ApprovalStatus)
	}
	if !accepted.Enabled {
		t.Fatal("expected rule enabled after accept")
	}

	// Reject a new enhancement on the same rule.
	body, _ = json.Marshal(enhanceReq)
	req = adminRequest(t, http.MethodPost, srv.URL+"/admin/rules/enhance", body)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 202 enhancing rule for reject, got %d: %s", resp.StatusCode, string(b))
	}
	var secondResp api.EnhanceRuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&secondResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pollJob(t, srv, secondResp.JobID)

	req = adminRequest(t, http.MethodPost, srv.URL+"/admin/rules/rule-base/enhancement/reject", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 rejecting enhancement, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	req = adminRequest(t, http.MethodGet, srv.URL+"/admin/rules/rule-base", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after reject, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestIntegrationEnhancerContentFilterFallback(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	baseline := map[string]any{
		"id":      "rule-filter",
		"name":    "Filter",
		"entry":   "https://example.com",
		"steps":   []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
		"enabled": true,
	}

	// Rebuild the enhancer with a provider that returns a patch containing an
	// event handler in a variable. The enhancer should reject it and fall back
	// to the baseline rule.
	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         "fake-malicious",
		LLMModel:            "fake-model",
		LLMTemperature:      0.2,
		LLMCacheTTL:         0,
		LLMEnableReflection: false,
	}
	logger := zap.NewNop()
	o := llm.NewOrchestrator(cfg, logger)
	o.RegisterProvider(&fakeProvider{
		name: "fake-malicious",
		resp: &llm.CompletionResponse{
			Content: `{"selectors":{},"steps":[],"variables":{"x":"<body onload='steal()'>"},"suggestions":[]}`,
		},
	})
	e := llm.NewEnhancer(cfg, o, logger)

	res, err := e.Enhance(context.Background(), llm.EnhanceRequest{BaselineRule: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if !strings.Contains(res.Error, "content filter") {
		t.Fatalf("expected content-filter error, got %q", res.Error)
	}
}

func TestIntegrationEnhanceRealLLM(t *testing.T) {
	if os.Getenv("RUN_LLM_INTEGRATION") != "1" {
		t.Skip("set RUN_LLM_INTEGRATION=1 to run LLM integration tests")
	}
	key := os.Getenv("LLM_API_KEY")
	if key == "" {
		t.Skip("LLM_API_KEY not set")
	}

	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         os.Getenv("LLM_PROVIDER"),
		LLMAPIKey:           key,
		LLMBaseURL:          os.Getenv("LLM_BASE_URL"),
		LLMModel:            os.Getenv("LLM_MODEL"),
		LLMTemperature:      0.2,
		LLMRequestTimeout:   60 * time.Second,
		LLMCacheTTL:         0,
		LLMEnableReflection: false,
	}
	if cfg.LLMProvider == "" {
		cfg.LLMProvider = "openai"
	}

	logger := zap.NewNop()
	orch := llm.NewOrchestrator(cfg, logger)
	switch cfg.LLMProvider {
	case "openai":
		orch.RegisterProvider(providers.NewOpenAIProvider(
			cfg.LLMProvider,
			cfg.LLMAPIKey,
			cfg.LLMBaseURL,
			cfg.LLMModel,
			cfg.LLMRequestTimeout,
			cfg.LLMTemperature,
		))
	default:
		t.Fatalf("unsupported provider %q for integration test", cfg.LLMProvider)
	}

	e := llm.NewEnhancer(cfg, orch, logger)
	res, err := e.Enhance(context.Background(), llm.EnhanceRequest{
		BaselineRule: map[string]any{
			"id":    "test",
			"name":  "test",
			"entry": "https://example.com",
			"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
		},
		Recording: map[string]any{
			"events":       []any{},
			"domSnapshots": []any{},
			"meta":         map[string]any{"startUrl": "https://example.com"},
		},
		UserHint: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rule["name"] == "" {
		t.Fatal("empty rule name")
	}
	if res.Error != "" {
		t.Fatalf("enhancement returned error: %s", res.Error)
	}
}

func TestIntegrationSQLiteCacheHits(t *testing.T) {
	if os.Getenv("RUN_LLM_INTEGRATION") != "1" {
		t.Skip("set RUN_LLM_INTEGRATION=1 to run LLM integration tests")
	}

	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := cache.NewSQLiteCache(s)
	if err != nil {
		t.Fatalf("create sqlite cache: %v", err)
	}

	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         "fake",
		LLMModel:            "fake-model",
		LLMTemperature:      0.2,
		LLMCacheTTL:         time.Hour,
		LLMEnableReflection: false,
	}
	logger := zap.NewNop()
	orch := llm.NewOrchestratorWithCache(cfg, c, logger)
	p := &fakeProvider{
		name: "fake",
		resp: &llm.CompletionResponse{
			Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"sqlite-cached"}],"variables":{},"suggestions":[]}`,
		},
	}
	orch.RegisterProvider(p)
	e := llm.NewEnhancer(cfg, orch, logger)

	baseline := map[string]any{
		"name":  "base",
		"entry": "https://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
	}
	req := llm.EnhanceRequest{BaselineRule: baseline, UserHint: "hint"}

	res1, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res1.CacheHit {
		t.Fatal("expected first enhancement to be cache miss")
	}

	res2, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit on second identical request")
	}
	if res2.Rule["name"] != "sqlite-cached" {
		t.Fatalf("expected cached rule name, got %v", res2.Rule["name"])
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}
