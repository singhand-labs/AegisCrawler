package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"go.uber.org/zap"
)

// flakyProvider fails for the first failCount calls and then succeeds, so
// retry behavior can be asserted by counting calls.
type flakyProvider struct {
	name       string
	failCount  int
	calls      int
	lastErrIdx int
}

func (p *flakyProvider) Name() string { return p.name }

func (p *flakyProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	p.calls++
	if p.calls <= p.failCount {
		return nil, availabilityHTTPError{status: 429}
	}
	return &CompletionResponse{Content: "recovered"}, nil
}

// Five call-level retries must carry a transient 429 to the sixth attempt.
func TestCompleteCallRetriesRecoverAfterFiveFailures(t *testing.T) {
	cfg := &config.Config{LLMProvider: "flaky", LLMCallMaxRetries: 5}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &flakyProvider{name: "flaky", failCount: 5}
	o.RegisterProvider(p)

	resp, err := o.Complete(context.Background(), CompletionRequest{User: "retry-me"})
	if err != nil {
		t.Fatalf("Complete error = %v, want recovery on attempt 6", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("content = %q, want the recovered payload", resp.Content)
	}
	if p.calls != 6 {
		t.Fatalf("provider calls = %d, want 6 (initial + 5 retries)", p.calls)
	}
}

// Exhausted retries surface the provider error after exactly maxRetries+1
// calls.
func TestCompleteCallRetriesExhaust(t *testing.T) {
	cfg := &config.Config{LLMProvider: "flaky", LLMCallMaxRetries: 2}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &flakyProvider{name: "flaky", failCount: 100}
	o.RegisterProvider(p)

	_, err := o.Complete(context.Background(), CompletionRequest{User: "always-fails"})
	if err == nil {
		t.Fatal("Complete must fail when every retry fails")
	}
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (initial + 2 retries)", p.calls)
	}
}

// Definitive capture errors must not be retried even with retries enabled.
func TestCompleteCallRetriesDoNotRepeatCaptureErrors(t *testing.T) {
	cfg := &config.Config{LLMProvider: "capture", LLMCallMaxRetries: 5}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "capture", err: ErrCompletionCapture}
	o.RegisterProvider(p)

	_, err := o.Complete(context.Background(), CompletionRequest{User: "capture"})
	if !errors.Is(err, ErrCompletionCapture) {
		t.Fatalf("error = %v, want ErrCompletionCapture", err)
	}
	if p.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (capture errors are terminal)", p.calls)
	}
}
