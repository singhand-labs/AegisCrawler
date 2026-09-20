package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type jobTestProvider struct {
	resp *CompletionResponse
}

func (p *jobTestProvider) Name() string { return "fake" }

func (p *jobTestProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	return p.resp, nil
}

func newTestManager(t *testing.T, content string) (*JobManager, *store.Store) {
	t.Helper()
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&jobTestProvider{resp: &CompletionResponse{Content: content}})
	return NewJobManager(s, cfg, orch, zap.NewNop()), s
}

func TestJobManagerSubmitAndProcess(t *testing.T) {
	manager, s := newTestManager(t, `{"selectors":{"title":{"selector":"h1"}},"steps":[{"op":"replace","path":"/selectors/title/selector","value":"h2"}],"variables":{},"suggestions":["use h2"]}`)
	ctx := context.Background()

	baseline := map[string]any{
		"id":    "rule-1",
		"name":  "Base",
		"entry": "https://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
	}
	jobID, err := manager.Submit(ctx, EnhanceRequest{
		Recording:    map[string]any{"url": "https://example.com?token=live-secret"},
		BaselineRule: baseline,
		UserHint:     "use h2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if jobID == "" {
		t.Fatal("expected job id")
	}
	pendingJob, err := s.GetLLMJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pendingJob.Recording), "live-secret") {
		t.Fatalf("LLM job persisted an unsanitized recording: %s", pendingJob.Recording)
	}

	manager.ProcessOnce(ctx)

	job, err := s.GetLLMJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed, got %s", job.Status)
	}
	if job.Provider != "fake" {
		t.Fatalf("expected provider fake, got %s", job.Provider)
	}
	if job.RuleID != "rule-1" {
		t.Fatalf("expected rule-1, got %s", job.RuleID)
	}

	enhancement, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending enhancement, got %s", enhancement.Status)
	}

	rule, err := s.GetRuleByID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if rule.ApprovalStatus != string(models.RuleApprovalPending) {
		t.Fatalf("expected pending rule, got %s", rule.ApprovalStatus)
	}
	if rule.Enabled {
		t.Fatal("expected rule disabled")
	}
}

func TestJobManagerSubmitRequiresBaselineID(t *testing.T) {
	manager, _ := newTestManager(t, `{}`)
	_, err := manager.Submit(context.Background(), EnhanceRequest{
		BaselineRule: map[string]any{"name": "no id"},
	})
	if err == nil {
		t.Fatal("expected error for missing baseline id")
	}
}

func TestJobManagerFallbackOnBadSuggestion(t *testing.T) {
	manager, s := newTestManager(t, `{"selectors":{},"steps":[{"op":"replace"}],"variables":{},"suggestions":[]}`)
	ctx := context.Background()
	baseline := map[string]any{
		"id":    "rule-1",
		"name":  "Base",
		"entry": "https://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
	}
	jobID, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: baseline})
	if err != nil {
		t.Fatal(err)
	}

	manager.ProcessOnce(ctx)

	job, err := s.GetLLMJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed, got %s", job.Status)
	}
	if job.ResultError == "" {
		t.Fatal("expected result error")
	}

	enhancement, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending enhancement, got %s", enhancement.Status)
	}
}

func TestJobManagerGetJob(t *testing.T) {
	manager, _ := newTestManager(t, `{}`)
	ctx := context.Background()
	jobID, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: map[string]any{"id": "rule-1", "name": "Base"}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != jobID {
		t.Fatalf("expected %s, got %s", jobID, job.ID)
	}
	if _, err := manager.GetJob(ctx, "missing"); err == nil {
		t.Fatal("expected error for missing job")
	}
}

func TestJobManagerHardFailureFallback(t *testing.T) {
	manager, s := newTestManager(t, `{}`)
	ctx := context.Background()
	baseline := map[string]any{
		"id":    "rule-1",
		"name":  "Base",
		"entry": "https://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
	}
	jobID, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: baseline})
	if err != nil {
		t.Fatal(err)
	}

	// Force the worker to return a hard error.
	manager.worker = &errorEnhancer{err: errors.New("llm service unavailable")}
	manager.ProcessOnce(ctx)

	job, err := s.GetLLMJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed, got %s", job.Status)
	}
	if job.ResultError == "" {
		t.Fatal("expected result error")
	}

	enhancement, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.Status != string(models.EnhancementStatusPending) {
		t.Fatalf("expected pending enhancement, got %s", enhancement.Status)
	}
}

func TestJobManagerEnforcedDispatchFailuresAreTerminalStableCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "budget denied",
			err: &budget.DeniedError{
				Scope: budget.ScopeWorkspace, Limit: budget.LimitDaily,
			},
			code: budget.CodeBudgetExceeded,
		},
		{name: "ledger unavailable", err: budget.ErrLedgerUnavailable, code: budget.CodeLedgerUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cfg := loadEnforcedOrchestratorConfig(t, "job-policy")
			manager := NewJobManager(s, cfg, NewOrchestrator(cfg, zap.NewNop()), zap.NewNop())
			manager.worker = &errorEnhancer{err: fmt.Errorf("wrapped dispatch: %w", tc.err)}
			ctx := context.Background()
			jobID, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: map[string]any{
				"id": "rule-1", "name": "Base", "entry": "https://example.com",
			}})
			if err != nil {
				t.Fatal(err)
			}

			manager.ProcessOnce(ctx)
			job, err := s.GetLLMJob(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != string(models.LLMJobStatusFailed) || job.ResultError != tc.code {
				t.Fatalf("job status/code = %q/%q, want failed/%q", job.Status, job.ResultError, tc.code)
			}
			if _, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1"); err == nil {
				t.Fatal("an enforced dispatch failure must not persist a baseline enhancement")
			}
		})
	}
}

type errorEnhancer struct {
	err error
}

func (e *errorEnhancer) Enhance(ctx context.Context, req EnhanceRequest) (*EnhanceResult, error) {
	return nil, e.err
}

func TestJobManagerTokenAlert(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &config.Config{
		LLMEnabled:             true,
		LLMProvider:            "fake",
		LLMModel:               "fake-model",
		LLMTokenAlertThreshold: 50,
	}
	orch := NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&jobTestProvider{resp: &CompletionResponse{
		Content:      `{"selectors":{},"steps":[],"variables":{},"suggestions":[]}`,
		InputTokens:  100,
		OutputTokens: 10,
	}})
	manager := NewJobManager(s, cfg, orch, logger)
	ctx := context.Background()
	baseline := map[string]any{
		"id":    "rule-1",
		"name":  "Base",
		"entry": "https://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
	}
	jobID, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: baseline})
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(ctx)

	job, err := s.GetLLMJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.InputTokens != 100 || job.OutputTokens != 10 {
		t.Fatalf("unexpected tokens: %d/%d", job.InputTokens, job.OutputTokens)
	}

	alerts := observed.FilterMessage("llm token usage exceeded alert threshold").AllUntimed()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 token alert, got %d", len(alerts))
	}
}

func TestJobManagerProcessOnceBatch(t *testing.T) {
	manager, s := newTestManager(t, `{"selectors":{},"steps":[],"variables":{},"suggestions":[]}`)
	manager.cfg.LLMJobBatchSize = 2
	ctx := context.Background()
	for _, id := range []string{"rule-1", "rule-2", "rule-3"} {
		baseline := map[string]any{
			"id":    id,
			"name":  "Base",
			"entry": "https://example.com",
			"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
		}
		if _, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: baseline}); err != nil {
			t.Fatal(err)
		}
	}

	manager.ProcessOnce(ctx)

	completed, err := s.ListPendingLLMJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 remaining pending job, got %d", len(completed))
	}
}

func TestJobManagerDuplicateSubmission(t *testing.T) {
	manager, s := newTestManager(t, `{"selectors":{"title":{"selector":"h1"}},"steps":[{"op":"replace","path":"/selectors/title/selector","value":"h2"}],"variables":{},"suggestions":["use h2"]}`)
	ctx := context.Background()
	baseline := map[string]any{
		"id":    "rule-1",
		"name":  "Base",
		"entry": "https://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "https://example.com"}},
	}
	if _, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: baseline}); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(ctx)

	// Submit a second enhancement for the same rule and process it.
	if _, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: baseline, UserHint: "second"}); err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(ctx)

	enhancement, err := s.GetRuleEnhancementByRuleID(ctx, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	if enhancement.UserHint != "second" {
		t.Fatalf("expected latest enhancement, got userHint %q", enhancement.UserHint)
	}

	// There should only be one pending rule and one pending enhancement.
	var pendingCount int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rules WHERE id = ? AND approval_status = ?`,
		"rule-1", string(models.RuleApprovalPending)).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 1 {
		t.Fatalf("expected 1 pending rule, got %d", pendingCount)
	}
	var enhancementCount int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rule_enhancements WHERE rule_id = ? AND status = ?`,
		"rule-1", string(models.EnhancementStatusPending)).Scan(&enhancementCount); err != nil {
		t.Fatal(err)
	}
	if enhancementCount != 1 {
		t.Fatalf("expected 1 pending enhancement, got %d", enhancementCount)
	}

	var orphanedCount int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rule_enhancements WHERE rule_id IS NULL`).Scan(&orphanedCount); err != nil {
		t.Fatal(err)
	}
	if orphanedCount != 0 {
		t.Fatalf("expected 0 orphaned rule_enhancements rows, got %d", orphanedCount)
	}
}

func TestJobManagerSetMetrics(t *testing.T) {
	manager, _ := newTestManager(t, `{}`)
	manager.SetMetrics(NewMetricsWithRegistry(nil))
}

func TestJobManagerStartWorkerStops(t *testing.T) {
	manager, _ := newTestManager(t, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.StartWorker(ctx, 10*time.Millisecond)
		close(done)
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartWorker did not stop after context cancellation")
	}
}

func TestJobManagerProcessOnceLeavesPendingJobsUnclaimedWhenLLMDisabled(t *testing.T) {
	for _, mode := range []string{"legacy", "enforced"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("LLM_ENABLED", "false")
			t.Setenv("LLM_POLICY_MODE", mode)
			cfg := config.Load()
			s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			manager := NewJobManager(s, cfg, nil, zap.NewNop())

			jobID, err := manager.Submit(context.Background(), EnhanceRequest{
				BaselineRule: map[string]any{"id": "disabled-" + mode},
			})
			if err != nil {
				t.Fatal(err)
			}

			manager.ProcessOnce(context.Background())

			job, err := manager.GetJob(context.Background(), jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != string(models.LLMJobStatusPending) || job.AttemptCount != 0 {
				t.Fatalf("disabled worker mutated pending job: %+v", job)
			}
		})
	}
}

func TestJobManagerSubmitMarshalErrors(t *testing.T) {
	manager, _ := newTestManager(t, `{}`)
	ctx := context.Background()

	_, err := manager.Submit(ctx, EnhanceRequest{BaselineRule: map[string]any{"id": "x", "bad": make(chan int)}})
	if err == nil {
		t.Fatal("expected error when baseline cannot be marshaled")
	}

	_, err = manager.Submit(ctx, EnhanceRequest{
		BaselineRule: map[string]any{"id": "x"},
		Recording:    map[string]any{"bad": make(chan int)},
	})
	if err == nil {
		t.Fatal("expected error when recording cannot be marshaled")
	}
}

func TestJobManagerProcessOnceClaimError(t *testing.T) {
	core, observed := observer.New(zap.ErrorLevel)
	logger := zap.New(core)
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	manager := NewJobManager(s, cfg, NewOrchestrator(cfg, zap.NewNop()), logger)
	_ = s.Close()

	manager.ProcessOnce(context.Background())
	if observed.FilterMessage("claim pending llm job failed").Len() != 1 {
		t.Fatal("expected error log for claim failure")
	}
}

func TestJobManagerRunJobBaselineUnmarshalError(t *testing.T) {
	manager, s := newTestManager(t, `{}`)
	ctx := context.Background()
	job := &models.LLMJob{
		ID:        store.NewID(),
		RuleID:    "rule-bad",
		Baseline:  models.JSON("not-json"),
		Recording: models.JSON(`{}`),
		Status:    string(models.LLMJobStatusPending),
	}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	manager.runJob(ctx, job)

	updated, err := s.GetLLMJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed, got %s", updated.Status)
	}
}

func TestJobManagerRunJobRecordingUnmarshalError(t *testing.T) {
	manager, s := newTestManager(t, `{}`)
	ctx := context.Background()
	job := &models.LLMJob{
		ID:        store.NewID(),
		RuleID:    "rule-bad",
		Baseline:  models.JSON(`{}`),
		Recording: models.JSON("not-json"),
		Status:    string(models.LLMJobStatusPending),
	}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	manager.runJob(ctx, job)

	updated, err := s.GetLLMJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed, got %s", updated.Status)
	}
}

type badResultEnhancer struct{}

func (badResultEnhancer) Enhance(ctx context.Context, req EnhanceRequest) (*EnhanceResult, error) {
	return &EnhanceResult{
		Rule: map[string]any{"id": "rule-bad", "bad": make(chan int)},
	}, nil
}

func TestJobManagerRunJobPersistEnhancementError(t *testing.T) {
	core, observed := observer.New(zap.ErrorLevel)
	logger := zap.New(core)
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &config.Config{LLMProvider: "fake", LLMModel: "fake-model"}
	manager := NewJobManager(s, cfg, NewOrchestrator(cfg, zap.NewNop()), logger)
	manager.worker = badResultEnhancer{}
	ctx := context.Background()
	job := &models.LLMJob{
		ID:       store.NewID(),
		RuleID:   "rule-bad",
		Baseline: models.JSON(`{"id":"rule-bad","name":"Base","entry":"http://example.com"}`),
		Status:   string(models.LLMJobStatusPending),
	}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	manager.runJob(ctx, job)

	if observed.FilterMessage("persist enhancement for job failed").Len() != 1 {
		t.Fatal("expected error log for persist enhancement failure")
	}
}
