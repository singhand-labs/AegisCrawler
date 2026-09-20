package dsl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"go.uber.org/zap"
)

// A success completion carrying no output rows must not consume the replay
// attempt: the no-wizard fallback's rowless ping stays recoverable and the
// wizard's row-carrying completion can still land afterwards.
func TestCompleteReplayRejectsRowlessSuccessWithoutConsuming(t *testing.T) {
	cfg := config.Load()
	cfg.LLMEnabled = true
	cfg.LLMModel = "rowless-model"
	cfg.LLMJobBatchSize = 10
	cfg.LLMRequestTimeout = time.Second
	persistence, ctx := newDSLManagerStore(t)
	requirement := createDSLManagerRequirement(t, persistence, ctx, cfg, models.RequirementSourceLLM)
	baseline := dslWorkflowBaseline()
	generated := dslManagerGeneratedRule(t, baseline)
	fake := &fakeWorkflowCompleter{respond: func(llm.CompletionRequest) (*llm.CompletionResult, error) {
		return completion(ruleEnvelope(t, generated)), nil
	}}
	manager := NewDSLManager(persistence, cfg, NewDSLWorkflow(cfg, fake), zap.NewNop())
	workflow, _, err := manager.Submit(ctx, requirement.ID, "rowless-profile", baseline)
	if err != nil {
		t.Fatal(err)
	}
	manager.ProcessOnce(context.Background())
	stored, err := persistence.GetDSLWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.DSLWorkflowAwaitingReplay {
		t.Fatalf("workflow not replay-ready: %s", stored.Status)
	}
	replay, err := manager.StartReplay(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = manager.CompleteReplay(ctx, workflow.ID, replay.ID, ReplayCompletionInput{Succeeded: true})
	if !errors.Is(err, ErrReplayOutputRequired) {
		t.Fatalf("rowless success must be rejected with ErrReplayOutputRequired, got %v", err)
	}
	after, err := persistence.GetDSLReplay(ctx, replay.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != models.ReplayAttemptRunning {
		t.Fatalf("attempt must stay completable after rejection, got %q", after.Status)
	}
	// The row-carrying completion still lands on the same attempt.
	rows := []any{map[string]any{"name": "Example"}}
	completed, repair, err := manager.CompleteReplay(ctx, workflow.ID, replay.ID, ReplayCompletionInput{Succeeded: true, Output: rows})
	if err != nil || completed.Status != models.ReplayAttemptSucceeded || repair != nil {
		t.Fatalf("row-carrying completion failed: replay=%+v repair=%+v err=%v", completed, repair, err)
	}
}
