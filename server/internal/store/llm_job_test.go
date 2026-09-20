package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func newTestStoreForLLMJob(t *testing.T) *Store {
	t.Helper()
	s, err := NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetLLMJob(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()
	job := &models.LLMJob{
		ID:     "job-1",
		RuleID: "rule-1",
		Status: string(models.LLMJobStatusPending),
	}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLLMJob(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RuleID != "rule-1" {
		t.Fatalf("expected rule-1, got %s", got.RuleID)
	}
	if got.Status != string(models.LLMJobStatusPending) {
		t.Fatalf("expected pending, got %s", got.Status)
	}
}

func TestGetLLMJobNotFound(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	if _, err := s.GetLLMJob(context.Background(), "missing"); !isNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func isNotFound(err error) bool {
	return err != nil && err == ErrRuleNotFound
}

func TestUpdateLLMJobStatus(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()
	job := &models.LLMJob{
		ID:     "job-1",
		RuleID: "rule-1",
		Status: string(models.LLMJobStatusPending),
	}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	job.Status = string(models.LLMJobStatusCompleted)
	job.Provider = "openai"
	job.Model = "gpt-4o"
	job.InputTokens = 10
	job.OutputTokens = 5
	if err := s.UpdateLLMJobStatus(ctx, job); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLLMJob(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != string(models.LLMJobStatusCompleted) {
		t.Fatalf("expected completed, got %s", got.Status)
	}
	if got.Provider != "openai" || got.Model != "gpt-4o" {
		t.Fatalf("unexpected provider/model: %s/%s", got.Provider, got.Model)
	}
	if got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Fatalf("unexpected tokens: %d/%d", got.InputTokens, got.OutputTokens)
	}
}

func TestClaimPendingLLMJob(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()
	job := &models.LLMJob{
		ID:     "job-1",
		RuleID: "rule-1",
		Status: string(models.LLMJobStatusPending),
	}
	if err := s.CreateLLMJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	claimed, err := s.ClaimPendingLLMJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != "job-1" {
		t.Fatalf("expected job-1, got %s", claimed.ID)
	}
	if claimed.Status != string(models.LLMJobStatusRunning) {
		t.Fatalf("expected running, got %s", claimed.Status)
	}
	if claimed.StartedAt == nil {
		t.Fatal("expected started_at set")
	}
	if claimed.AttemptCount != 1 {
		t.Fatalf("expected attempt count 1, got %d", claimed.AttemptCount)
	}

	if _, err := s.ClaimPendingLLMJob(ctx); err != ErrNoTaskAvailable {
		t.Fatalf("expected no task available, got %v", err)
	}
}

func TestListPendingLLMJobs(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()
	for _, id := range []string{"job-1", "job-2"} {
		job := &models.LLMJob{ID: id, RuleID: "rule-1", Status: string(models.LLMJobStatusPending)}
		if err := s.CreateLLMJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	jobs, err := s.ListPendingLLMJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 pending jobs, got %d", len(jobs))
	}
}

func TestClaimPendingLLMJobConcurrency(t *testing.T) {
	s := newTestStoreForLLMJob(t)
	ctx := context.Background()
	for _, id := range []string{"job-1", "job-2", "job-3"} {
		job := &models.LLMJob{ID: id, RuleID: "rule-1", Status: string(models.LLMJobStatusPending)}
		if err := s.CreateLLMJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	var claimed int64
	var duplicates int64
	seen := make(map[string]struct{})
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := s.ClaimPendingLLMJob(ctx)
				if err != nil {
					return
				}
				mu.Lock()
				if _, ok := seen[job.ID]; ok {
					duplicates++
				}
				seen[job.ID] = struct{}{}
				mu.Unlock()
				atomic.AddInt64(&claimed, 1)
			}
		}()
	}

	wg.Wait()

	if claimed != 3 {
		t.Fatalf("expected 3 claimed jobs, got %d", claimed)
	}
	if duplicates != 0 {
		t.Fatalf("expected no duplicate claims, got %d", duplicates)
	}
}
