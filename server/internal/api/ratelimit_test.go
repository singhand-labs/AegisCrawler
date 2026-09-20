package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

func newRateLimitTestServer(t *testing.T, workerRPS float64, workerBurst int, siteRPS float64, siteBurst int) *httptest.Server {
	t.Helper()
	f, err := os.CreateTemp("", "ratelimit-*.db")
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
		WorkerRateLimitPerSecond:       workerRPS,
		WorkerRateLimitBurst:           workerBurst,
		SiteRateLimitPerSecond:         siteRPS,
		SiteRateLimitBurst:             siteBurst,
		SiteRateLimitCacheTTL:          5 * time.Minute,
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
	return httptest.NewServer(router)
}

func TestPerWorkerRateLimit(t *testing.T) {
	srv := newRateLimitTestServer(t, 1, 1, 1000, 2000)
	defer srv.Close()

	var allowed, rejected int
	for i := 0; i < 10; i++ {
		claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-rate"})
		resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			rejected++
		} else if resp.StatusCode == http.StatusNoContent {
			allowed++
		} else {
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}

	if allowed == 0 {
		t.Fatal("expected at least one allowed request")
	}
	if rejected == 0 {
		t.Fatal("expected at least one 429 per-worker rate limit response")
	}
}

func TestPerSiteRateLimit(t *testing.T) {
	srv := newRateLimitTestServer(t, 1000, 2000, 1, 1)
	defer srv.Close()

	rule := models.Rule{
		ID:        "site-rule",
		Version:   "1.0.0",
		Name:      "Site Test",
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
		RuleID:      "site-rule",
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

	var allowed, rejected int
	for i := 0; i < 10; i++ {
		resultBody, _ := json.Marshal(ResultRequest{
			TaskID:   task.ID,
			WorkerID: "worker-site-1",
			Payload:  map[string]any{"i": i},
		})
		resp, err := http.Post(srv.URL+"/results", "application/json", bytes.NewReader(resultBody))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			rejected++
		} else if resp.StatusCode == http.StatusOK {
			allowed++
		} else {
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}

	if allowed == 0 {
		t.Fatal("expected at least one allowed request")
	}
	if rejected == 0 {
		t.Fatal("expected at least one 429 per-site rate limit response")
	}
}

// mockDomainResolver is a test double that counts resolver calls.
type mockDomainResolver struct {
	task      *models.Task
	rule      *models.Rule
	taskCalls int
	ruleCalls int
}

func (m *mockDomainResolver) GetTaskByID(ctx context.Context, id string) (*models.Task, error) {
	m.taskCalls++
	return m.task, nil
}

func (m *mockDomainResolver) GetRuleByID(ctx context.Context, id string) (*models.Rule, error) {
	m.ruleCalls++
	return m.rule, nil
}

func newSiteRateLimitTestRequest(taskID string) *http.Request {
	body := []byte(`{"taskId":"` + taskID + `"}`)
	r := httptest.NewRequest(http.MethodPost, "/results", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestSiteRateLimitDomainCache(t *testing.T) {
	rule := &models.Rule{ID: "rule-1", Domain: store.JSON("example.com")}
	task := &models.Task{ID: "task-1", RuleID: "rule-1"}
	mock := &mockDomainResolver{task: task, rule: rule}
	rl := &siteRateLimiters{
		limiters:    make(map[string]*rate.Limiter),
		store:       mock,
		domainCache: make(map[string]cacheEntry),
		cacheTTL:    5 * time.Minute,
	}

	key1 := rl.siteKey(newSiteRateLimitTestRequest("task-1"))
	if key1 != "example.com" {
		t.Fatalf("expected domain example.com, got %s", key1)
	}
	if mock.taskCalls != 1 || mock.ruleCalls != 1 {
		t.Fatalf("expected one store lookup each on miss, got taskCalls=%d ruleCalls=%d", mock.taskCalls, mock.ruleCalls)
	}

	key2 := rl.siteKey(newSiteRateLimitTestRequest("task-1"))
	if key2 != "example.com" {
		t.Fatalf("expected domain example.com, got %s", key2)
	}
	if mock.taskCalls != 1 || mock.ruleCalls != 1 {
		t.Fatalf("expected cache hit with no additional store lookups, got taskCalls=%d ruleCalls=%d", mock.taskCalls, mock.ruleCalls)
	}
}

func TestAdminEnhanceRateLimit(t *testing.T) {
	f, err := os.CreateTemp("", "admin-enhance-ratelimit-*.db")
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
		AdminAPIKey:                    "test-admin-key",
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		LLMRateLimitPerSecond:          1,
		LLMRateLimitBurst:              1,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	metrics := NewMetrics()
	manager := newTestJobManager(t, s, `{"selectors": {}, "steps": [], "variables": {}, "suggestions": []}`)
	h := NewHandler(s, sch, manager, nil, cfg, logger)
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	srv := httptest.NewServer(router)
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"baselineRule": map[string]any{"id": "rule-1", "name": "Test"},
	})

	var allowed, rejected int
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/rules/enhance", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-admin-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusAccepted, http.StatusBadRequest:
			allowed++
		case http.StatusTooManyRequests:
			rejected++
		default:
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}

	if allowed == 0 {
		t.Fatal("expected at least one allowed request")
	}
	if rejected == 0 {
		t.Fatal("expected at least one 429 admin enhance rate limit response")
	}
}

func TestAdminEnhanceRateLimitDisabledWhenZero(t *testing.T) {
	f, err := os.CreateTemp("", "admin-enhance-ratelimit-zero-*.db")
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
		AdminAPIKey:                    "test-admin-key",
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		LLMRateLimitPerSecond:          0,
		LLMRateLimitBurst:              0,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	metrics := NewMetrics()
	manager := newTestJobManager(t, s, `{"selectors": {}, "steps": [], "variables": {}, "suggestions": []}`)
	h := NewHandler(s, sch, manager, nil, cfg, logger)
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	srv := httptest.NewServer(router)
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"baselineRule": map[string]any{"id": "rule-1", "name": "Test"},
	})

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/rules/enhance", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-admin-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 when rate limit disabled, got %d", resp.StatusCode)
		}
	}
}

func TestSiteRateLimitDomainCacheExpiry(t *testing.T) {
	rule := &models.Rule{ID: "rule-1", Domain: store.JSON("example.com")}
	task := &models.Task{ID: "task-1", RuleID: "rule-1"}
	mock := &mockDomainResolver{task: task, rule: rule}
	rl := &siteRateLimiters{
		limiters:    make(map[string]*rate.Limiter),
		store:       mock,
		domainCache: make(map[string]cacheEntry),
		cacheTTL:    50 * time.Millisecond,
	}

	key1 := rl.siteKey(newSiteRateLimitTestRequest("task-1"))
	if key1 != "example.com" {
		t.Fatalf("expected domain example.com, got %s", key1)
	}
	if mock.taskCalls != 1 || mock.ruleCalls != 1 {
		t.Fatalf("expected one store lookup each on miss, got taskCalls=%d ruleCalls=%d", mock.taskCalls, mock.ruleCalls)
	}

	time.Sleep(100 * time.Millisecond)

	key2 := rl.siteKey(newSiteRateLimitTestRequest("task-1"))
	if key2 != "example.com" {
		t.Fatalf("expected domain example.com after expiry, got %s", key2)
	}
	if mock.taskCalls != 2 || mock.ruleCalls != 2 {
		t.Fatalf("expected cache expiry and re-lookup, got taskCalls=%d ruleCalls=%d", mock.taskCalls, mock.ruleCalls)
	}
}

// Regression: one healthy extraction legitimately emits dozens of immediate
// per-row result submissions plus logs, one summary, and terminal statuses in
// a burst. The default worker and per-site budgets must cover one full
// single-page extraction (~20-40 rows) so a compliant worker never exhausts
// its own bucket at default configuration.
func TestDefaultReportingBurstBudgetsCoverOneExtraction(t *testing.T) {
	cfg := config.Load()
	if cfg.WorkerRateLimitPerSecond != 20 || cfg.WorkerRateLimitBurst != 64 {
		t.Fatalf("worker reporting defaults = %v/%d, want 20/64", cfg.WorkerRateLimitPerSecond, cfg.WorkerRateLimitBurst)
	}
	if cfg.SiteRateLimitPerSecond != 10 || cfg.SiteRateLimitBurst != 64 {
		t.Fatalf("site reporting defaults = %v/%d, want 10/64", cfg.SiteRateLimitPerSecond, cfg.SiteRateLimitBurst)
	}

	srv := newRateLimitTestServer(t,
		cfg.WorkerRateLimitPerSecond, cfg.WorkerRateLimitBurst,
		cfg.SiteRateLimitPerSecond, cfg.SiteRateLimitBurst)
	defer srv.Close()

	rule := models.Rule{
		ID:        "burst-rule",
		Version:   "1.0.0",
		Name:      "Burst Test",
		Domain:    store.JSON("burst.example.com"),
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
		RuleID:      "burst-rule",
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

	// 20 immediate rows + running status + logs + summary + terminal status.
	for i := 0; i < 24; i++ {
		resultBody, _ := json.Marshal(ResultRequest{
			TaskID:   task.ID,
			WorkerID: "worker-burst-1",
			Payload:  map[string]any{"i": i},
		})
		resp, err := http.Post(srv.URL+"/results", "application/json", bytes.NewReader(resultBody))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("request %d of a single legitimate extraction burst was rate limited at default budgets", i+1)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status %d at request %d", resp.StatusCode, i+1)
		}
	}
}
