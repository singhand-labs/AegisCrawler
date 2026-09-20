package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type refreshingLLMMetricsRuntime struct {
	metrics *llm.Metrics
	calls   int
}

func (r *refreshingLLMMetricsRuntime) RouteReady(string) (bool, string) {
	return true, ""
}

func (r *refreshingLLMMetricsRuntime) RefreshBudgetMetrics(context.Context) error {
	r.calls++
	r.metrics.ObserveBudgetSnapshot(123, 0, 877, 1_000)
	return nil
}

func newMetricsTestServer(t *testing.T) (*httptest.Server, *Metrics) {
	t.Helper()
	f, err := os.CreateTemp("", "metrics-*.db")
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
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	metrics := NewMetrics()
	h := NewHandler(s, sch, nil, nil, cfg, logger)
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	return httptest.NewServer(router), metrics
}

func TestMetricsScrapeRefreshesCurrentUTCBudgetSnapshot(t *testing.T) {
	setEnforcedPolicyEnvironment(t, "https://example.com/v1")
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatal(err)
	}
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	runtime := &refreshingLLMMetricsRuntime{metrics: metrics.LLM()}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	h.SetLLMRuntime(runtime)
	router := NewRouter(h, cfg, zap.NewNop(), metrics)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	if runtime.calls != 1 {
		t.Fatalf("budget metric refresh calls = %d, want 1", runtime.calls)
	}
	if !strings.Contains(rec.Body.String(), "opencrawler_llm_budget_settled_usd_nanos 123") {
		t.Fatalf("metrics scrape did not expose refreshed current-day budget:\n%s", rec.Body.String())
	}
}

func TestMetricsScrapeSkipsBudgetRefreshWhenLLMIsDisabled(t *testing.T) {
	setEnforcedPolicyEnvironment(t, "https://example.com/v1")
	t.Setenv("LLM_ENABLED", "false")
	cfg := config.Load()
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	runtime := &refreshingLLMMetricsRuntime{metrics: metrics.LLM()}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	h.SetLLMRuntime(runtime)
	router := NewRouter(h, cfg, zap.NewNop(), metrics)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	if runtime.calls != 0 {
		t.Fatalf("disabled LLM budget metric refresh calls = %d, want 0", runtime.calls)
	}
}

func createRule(t *testing.T, srv *httptest.Server) {
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
}

func createTask(t *testing.T, srv *httptest.Server) string {
	t.Helper()
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
	body, _ := json.Marshal(task)
	resp, err := http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return task.ID
}

func TestMetricsEndpoint(t *testing.T) {
	srv, _ := newMetricsTestServer(t)
	defer srv.Close()

	createRule(t, srv)
	taskID := createTask(t, srv)

	// Claim a task to increment tasks_claimed_total.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Submit a result to increment results_received_total.
	resultBody, _ := json.Marshal(ResultRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Payload:  map[string]any{"items": []any{"a"}},
	})
	resp, err = http.Post(srv.URL+"/results", "application/json", bytes.NewReader(resultBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Submit a heartbeat to increment heartbeats_received_total.
	hbBody, _ := json.Marshal(HeartbeatRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
	})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(hbBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Mark task done to increment tasks_completed_total.
	statusBody, _ := json.Marshal(StatusRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Status:   string(models.TaskStatusDone),
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(statusBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Request metrics output.
	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %s", ct)
	}

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	expected := []string{
		"# HELP opencrawler_requests_total",
		"# TYPE opencrawler_requests_total counter",
		"opencrawler_requests_total{",
		"# HELP opencrawler_tasks_claimed_total",
		"opencrawler_tasks_claimed_total 1",
		"# HELP opencrawler_tasks_completed_total",
		"opencrawler_tasks_completed_total{status=\"done\"} 1",
		"# HELP opencrawler_results_received_total",
		"opencrawler_results_received_total 1",
		"# HELP opencrawler_heartbeats_received_total",
		"opencrawler_heartbeats_received_total 1",
	}
	for _, want := range expected {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics to contain %q, got:\n%s", want, body)
		}
	}
}

func TestMetricsPathLabelUsesRoutePattern(t *testing.T) {
	srv, _ := newMetricsTestServer(t)
	defer srv.Close()

	createRule(t, srv)
	taskID1 := createTask(t, srv)
	taskID2 := createTask(t, srv)

	// GET /tasks/{id} with two different IDs should aggregate under one label.
	resp, err := http.Get(srv.URL + "/tasks/" + taskID1)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/tasks/" + taskID2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// GET /rules/{id} with two different IDs should aggregate under one label.
	resp, err = http.Get(srv.URL + "/rules/test-rule")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/rules/missing-rule")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	if strings.Contains(body, "path=\"/tasks/"+taskID1+"\"") {
		t.Fatalf("metrics should not contain concrete task id path, got:\n%s", body)
	}
	if strings.Contains(body, "path=\"/tasks/"+taskID2+"\"") {
		t.Fatalf("metrics should not contain concrete task id path, got:\n%s", body)
	}
	if strings.Contains(body, "path=\"/rules/test-rule\"") {
		t.Fatalf("metrics should not contain concrete rule id path, got:\n%s", body)
	}
	if strings.Contains(body, "path=\"/rules/missing-rule\"") {
		t.Fatalf("metrics should not contain concrete rule id path, got:\n%s", body)
	}

	expectedLabels := []string{
		"opencrawler_requests_total{method=\"GET\",path=\"/tasks/{id}\",status=\"200\"} 2",
		"opencrawler_requests_total{method=\"GET\",path=\"/rules/{id}\",status=\"200\"} 1",
		"opencrawler_requests_total{method=\"GET\",path=\"/rules/{id}\",status=\"404\"} 1",
	}
	for _, want := range expectedLabels {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics to contain %q, got:\n%s", want, body)
		}
	}
}

func TestPathFromPattern(t *testing.T) {
	cases := []struct {
		pattern  string
		expected string
	}{
		{"GET /tasks/{id}", "/tasks/{id}"},
		{"POST /tasks/{id}/checkpoints", "/tasks/{id}/checkpoints"},
		{"GET /health", "/health"},
		{"", ""},
		{"/plain/path", "/plain/path"},
	}
	for _, tc := range cases {
		got := pathFromPattern(tc.pattern)
		if got != tc.expected {
			t.Errorf("pathFromPattern(%q) = %q, want %q", tc.pattern, got, tc.expected)
		}
	}
}

func TestRateLimitedRequestsNotCountedInMetrics(t *testing.T) {
	f, err := os.CreateTemp("", "metrics-rate-limit-*.db")
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
		RateLimitPerSecond:             1,
		RateLimitBurst:                 1,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	metrics := NewMetrics()
	h := NewHandler(s, sch, nil, nil, cfg, logger)
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	srv := httptest.NewServer(router)
	defer srv.Close()

	var successes, rateLimited int
	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			successes++
		case http.StatusTooManyRequests:
			rateLimited++
		default:
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}

	if successes != 1 {
		t.Fatalf("expected exactly 1 successful request, got %d", successes)
	}
	if rateLimited == 0 {
		t.Fatal("expected at least one rate-limited request")
	}

	// With global rate limiting outermost, only the single successful /health
	// request should appear in metrics; 429s must not be recorded.
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()

	if _, ok := metrics.requestsTotal[metrics.requestKey("GET", "/health", "429")]; ok {
		t.Fatalf("metrics should not count rate-limited /health requests")
	}
	if metrics.requestsTotal[metrics.requestKey("GET", "/health", "200")] != 1 {
		t.Fatalf("metrics should count exactly one successful /health request, got %v", metrics.requestsTotal)
	}
}

func TestRowsPurgedMetric(t *testing.T) {
	m := NewMetrics()
	m.IncRowsPurged("results", 5)
	m.IncRowsPurged("results", 3)
	m.IncRowsPurged("logs", 2)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	MetricsHandler(m).ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `opencrawler_rows_purged_total{entity="results"} 8`) {
		t.Fatalf("expected results count 8 in metrics output, got:\n%s", body)
	}
	if !strings.Contains(body, `opencrawler_rows_purged_total{entity="logs"} 2`) {
		t.Fatalf("expected logs count 2 in metrics output, got:\n%s", body)
	}
}

func TestMetricsIncludeDBSize(t *testing.T) {
	m := NewMetrics()
	m.SetDBSize(12345678)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	MetricsHandler(m).ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "# HELP opencrawler_db_size_bytes") {
		t.Fatalf("expected db_size help in metrics output, got:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE opencrawler_db_size_bytes gauge") {
		t.Fatalf("expected db_size type in metrics output, got:\n%s", body)
	}
	if !strings.Contains(body, "opencrawler_db_size_bytes 12345678") {
		t.Fatalf("expected db_size value 12345678 in metrics output, got:\n%s", body)
	}
}

func TestMetricsIncludeLLMMetrics(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	m.LLM().RecordCompletion("openai", "gpt-4o", 50*1e6, 10, 20, false, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	MetricsHandler(m).ServeHTTP(rec, req)

	body := rec.Body.String()
	expected := []string{
		"# HELP opencrawler_llm_requests_total",
		"opencrawler_llm_requests_total{model=\"gpt-4o\",provider=\"openai\"} 1",
		"opencrawler_llm_tokens_total{direction=\"input\",model=\"gpt-4o\",provider=\"openai\"} 10",
		"opencrawler_llm_tokens_total{direction=\"output\",model=\"gpt-4o\",provider=\"openai\"} 20",
	}
	for _, want := range expected {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics to contain %q, got:\n%s", want, body)
		}
	}
}

func TestMetricsEndpointEmpty(t *testing.T) {
	srv, _ := newMetricsTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	if !strings.Contains(body, "opencrawler_tasks_claimed_total 0") {
		t.Fatalf("expected tasks_claimed_total 0, got:\n%s", body)
	}
}
