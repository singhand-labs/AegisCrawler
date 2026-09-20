//go:build eval

package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/eval"
	"github.com/singhand-labs/AegisCrawler/internal/llm/providers"
	"go.uber.org/zap"
)

type fakeProvider struct {
	name string
	resp *llm.CompletionResponse
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return p.resp, nil
}

func newTestEnhancer(resp *llm.CompletionResponse) *llm.Enhancer {
	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         "fake",
		LLMModel:            "fake-model",
		LLMTemperature:      0.0,
		LLMCacheTTL:         0,
		LLMEnableReflection: false,
	}
	o := llm.NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: resp})
	return llm.NewEnhancer(cfg, o, zap.NewNop())
}

func TestLoadCasesFromSample(t *testing.T) {
	cases, err := eval.LoadCasesFromFile(filepath.Join("fixtures", "sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("expected 2 cases, got %d", len(cases))
	}
}

func TestRunSampleCases(t *testing.T) {
	if os.Getenv("RUN_LLM_EVAL") != "1" {
		t.Skip("set RUN_LLM_EVAL=1 to run LLM eval tests")
	}

	resp := &llm.CompletionResponse{
		Content: `{"selectors":{"title":{"selector":"[data-testid='title']","reason":"stable"},"price":{"selector":"[data-testid='price']","reason":"stable"}},"steps":[],"variables":{},"suggestions":[]}`,
	}
	enhancer := newTestEnhancer(resp)

	cases, err := eval.LoadCasesFromFile(filepath.Join("fixtures", "sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := eval.Run(context.Background(), enhancer, cases)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("accuracy=%.2f precision=%.2f recall=%.2f passed=%d/%d", res.Accuracy, res.Precision, res.Recall, res.Passed, res.Total)

	if res.Total != 2 {
		t.Fatalf("expected 2 cases, got %d", res.Total)
	}
	if res.Passed != 2 {
		t.Fatalf("expected all cases to pass, got %d passed; failures=%v", res.Passed, res.Failures)
	}
	if res.Accuracy != 1.0 {
		t.Fatalf("expected accuracy 1.0, got %f", res.Accuracy)
	}
}

func TestRunDetectsMissingFields(t *testing.T) {
	if os.Getenv("RUN_LLM_EVAL") != "1" {
		t.Skip("set RUN_LLM_EVAL=1 to run LLM eval tests")
	}

	resp := &llm.CompletionResponse{
		Content: `{"selectors":{"title":{"selector":"[data-testid='title']","reason":"stable"}},"steps":[],"variables":{},"suggestions":[]}`,
	}
	enhancer := newTestEnhancer(resp)

	cases := []eval.Case{
		{
			Baseline: map[string]any{
				"name":  "sample",
				"entry": "http://example.com",
				"steps": []any{map[string]any{"action": "navigate", "url": "http://example.com"}},
			},
			Recording:              map[string]any{"events": []any{}, "domSnapshots": []any{}, "meta": map[string]any{"startUrl": "http://example.com"}},
			ExpectedFields:         []string{"title", "price"},
			ExpectedStableSelector: true,
		},
	}

	res, err := eval.Run(context.Background(), enhancer, cases)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed != 0 {
		t.Fatalf("expected failure, got passed=%d", res.Passed)
	}
	if len(res.Failures) == 0 {
		t.Fatal("expected failure message")
	}
	if res.Recall != 0.5 {
		t.Fatalf("expected recall 0.5, got %f", res.Recall)
	}
}

func TestRunDetectsUnstableSelector(t *testing.T) {
	if os.Getenv("RUN_LLM_EVAL") != "1" {
		t.Skip("set RUN_LLM_EVAL=1 to run LLM eval tests")
	}

	resp := &llm.CompletionResponse{
		Content: `{"selectors":{"title":{"selector":".dynamic-class-123","reason":"unstable"}},"steps":[],"variables":{},"suggestions":[]}`,
	}
	enhancer := newTestEnhancer(resp)

	cases := []eval.Case{
		{
			Baseline: map[string]any{
				"name":  "sample",
				"entry": "http://example.com",
				"steps": []any{map[string]any{"action": "navigate", "url": "http://example.com"}},
			},
			Recording:              map[string]any{"events": []any{}, "domSnapshots": []any{}, "meta": map[string]any{"startUrl": "http://example.com"}},
			ExpectedFields:         []string{"title"},
			ExpectedStableSelector: true,
		},
	}

	res, err := eval.Run(context.Background(), enhancer, cases)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed != 0 {
		t.Fatalf("expected failure due to unstable selector, got passed=%d", res.Passed)
	}
}

func TestRunCountsFalsePositives(t *testing.T) {
	if os.Getenv("RUN_LLM_EVAL") != "1" {
		t.Skip("set RUN_LLM_EVAL=1 to run LLM eval tests")
	}

	resp := &llm.CompletionResponse{
		Content: `{"selectors":{"title":{"selector":"[data-testid='title']","reason":"stable"},"extra":{"selector":"[data-testid='extra']","reason":"stable"}},"steps":[],"variables":{},"suggestions":[]}`,
	}
	enhancer := newTestEnhancer(resp)

	cases := []eval.Case{
		{
			Baseline: map[string]any{
				"name":  "sample",
				"entry": "http://example.com",
				"steps": []any{map[string]any{"action": "navigate", "url": "http://example.com"}},
			},
			Recording:              map[string]any{"events": []any{}, "domSnapshots": []any{}, "meta": map[string]any{"startUrl": "http://example.com"}},
			ExpectedFields:         []string{"title"},
			ExpectedStableSelector: false,
		},
	}

	res, err := eval.Run(context.Background(), enhancer, cases)
	if err != nil {
		t.Fatal(err)
	}
	if res.Precision != 0.5 {
		t.Fatalf("expected precision 0.5, got %f", res.Precision)
	}
}

func TestRunWithRealLLM(t *testing.T) {
	if os.Getenv("RUN_LLM_EVAL") != "1" {
		t.Skip("set RUN_LLM_EVAL=1 to run LLM eval tests")
	}
	key := os.Getenv("LLM_API_KEY")
	if key == "" {
		t.Skip("LLM_API_KEY not set, skipping real LLM eval")
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
		LLMPromptVersion:    "v1",
	}
	if cfg.LLMProvider == "" {
		cfg.LLMProvider = "openai"
	}

	logger := zap.NewNop()
	orch := llm.NewOrchestrator(cfg, logger)
	provider, err := providers.Build(cfg.LLMProvider, config.ProviderConfig{
		Provider: cfg.LLMProvider,
		APIKey:   cfg.LLMAPIKey,
		BaseURL:  cfg.LLMBaseURL,
		Model:    cfg.LLMModel,
		Timeout:  cfg.LLMRequestTimeout,
	})
	if err != nil {
		t.Fatalf("failed to build LLM provider: %v", err)
	}
	orch.RegisterProvider(provider)
	e := llm.NewEnhancer(cfg, orch, logger)

	cases, err := eval.LoadCasesFromFile(filepath.Join("fixtures", "sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := eval.Run(ctx, e, cases)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total == 0 {
		t.Fatal("expected at least one eval case")
	}

	var caseErrors int
	for _, cr := range res.PerCase {
		if cr.Error != "" {
			caseErrors++
		}
	}
	if caseErrors != 0 {
		t.Fatalf("expected no runtime errors, got %d", caseErrors)
	}

	t.Logf("accuracy=%.2f precision=%.2f recall=%.2f passed=%d/%d failures=%v",
		res.Accuracy, res.Precision, res.Recall, res.Passed, res.Total, res.Failures)
}
