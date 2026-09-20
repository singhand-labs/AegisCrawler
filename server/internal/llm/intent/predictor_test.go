package intent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeProvider struct {
	response string
	name     string
}

func (f *fakeProvider) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}
func (f *fakeProvider) InputTokenUpperBound(req llm.CompletionRequest) (int, error) {
	encoded, err := json.Marshal(req)
	return len(encoded), err
}
func (f *fakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Content: f.response, InputTokens: 100, OutputTokens: 50}, nil
}

func TestPredictor_Fallback_ReturnsThreeCandidates(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	p := NewPredictor(cfg, nil, zap.NewNop())
	result, err := p.Predict(context.Background(), map[string]any{
		"meta": map[string]any{"title": "Example"},
	})
	require.NoError(t, err)
	require.Len(t, result.Candidates, 3)
}

func TestPredictor_Predict_Success(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{response: `{"candidates": [{"id": "c1", "label": "采集标题", "description": "...", "confidence": 0.9}]}`})

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Len(t, res.Candidates, 3)
	assert.Equal(t, "custom", res.FallbackIntent.ID)
	assert.False(t, res.CacheHit)
}

func TestPredictor_Predict_LLMFallback(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	p := NewPredictor(cfg, nil, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com", "title": "Example"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Len(t, res.Candidates, 3)
	assert.Equal(t, "采集 Example 数据", res.Candidates[0].Label)
}

func TestPredictor_Predict_ParseFailureReturnsFallback(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{response: "not json"})

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Equal(t, "baseline", res.Model)
	assert.Len(t, res.Candidates, 3)
}

func TestPredictor_Predict_UnsafeCandidatesFiltered(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{response: `{"candidates": [{"id": "c1", "label": "窃取密码", "description": "获取用户敏感信息", "confidence": 1.0}]}`})

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Equal(t, "baseline", res.Model)
	assert.Len(t, res.Candidates, 3)
	assert.Equal(t, "generic", res.Candidates[0].ID)
}

func TestPredictor_Predict_PadsCandidates(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{response: `{"candidates": [
		{"id": "c1", "label": "列表采集", "description": "...", "confidence": 0.5},
		{"id": "c2", "label": "搜索关键词", "description": "...", "confidence": 0.5}
	]}`})

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Len(t, res.Candidates, 3)
}

func TestPredictor_Predict_SkipsPaddingWhenEnoughCandidates(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{response: `{"candidates": [
		{"id": "c1", "label": "列表采集", "description": "...", "confidence": 0.5},
		{"id": "c2", "label": "搜索关键词", "description": "...", "confidence": 0.3},
		{"id": "c3", "label": "表单提交", "description": "...", "confidence": 0.2}
	]}`})

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Len(t, res.Candidates, 3)
}

func TestPredictor_Predict_CompleteErrorReturnsFallback(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "missing", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Equal(t, "baseline", res.Model)
	assert.Len(t, res.Candidates, 3)
}

func TestPredictor_EnforcedPropagatesLedgerFailure(t *testing.T) {
	cfg := loadEnforcedPredictorConfig(t)
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProviderAs("primary", &fakeProvider{name: "aliyun", response: `{"candidates":[]}`})
	predictor := NewPredictor(cfg, orch, zap.NewNop())
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{WorkspaceID: "default"})
	ctx = llm.WithDispatchOperation(ctx, llm.DispatchOperation{
		Kind: budget.OperationIntent, ID: "intent-1", LogicalAttempt: 1,
	})

	result, err := predictor.Predict(ctx, map[string]any{
		"meta": map[string]any{"startUrl": "https://example.com"},
	})
	if result != nil || !budget.IsUnavailable(err) {
		t.Fatalf("result=%+v err=%v, want terminal ledger unavailability", result, err)
	}
}

func loadEnforcedPredictorConfig(t *testing.T) *config.Config {
	t.Helper()
	values := map[string]string{
		"LLM_ENABLED": "true", "LLM_POLICY_MODE": "enforced",
		"LLM_PRIMARY_PROVIDER": "aliyun", "LLM_PRIMARY_ADAPTER": "openai",
		"LLM_PRIMARY_MODEL": "qwen-test", "LLM_PRIMARY_BASE_URL": "https://provider.example.test/v1",
		"LLM_PRIMARY_API_KEY": "synthetic", "LLM_PRIMARY_REQUEST_TIMEOUT": "30s",
		"LLM_PRIMARY_TEMPERATURE": "0", "LLM_PRIMARY_STRICT_TOOL_OUTPUT": "false",
		"LLM_PRIMARY_ENABLE_THINKING": "false", "LLM_PRIMARY_INPUT_USD_PER_MILLION": "0.6",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION": "2", "LLM_PRIMARY_MAX_INPUT_TOKENS": "100000",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS": "4000", "LLM_PRIMARY_PRICE_REVISION": "test-revision",
		"LLM_GLOBAL_MAX_REQUEST_USD": "1", "LLM_GLOBAL_DAILY_BUDGET_USD": "10",
		"LLM_WORKSPACE_BUDGETS_JSON":   `{"default":{"maxRequestUSD":"0.6","dailyBudgetUSD":"3"}}`,
		"LLM_REQUIREMENT_MAX_ATTEMPTS": "1", "LLM_DSL_GENERATION_MAX_ATTEMPTS": "1",
		"LLM_DSL_MAX_REPAIRS": "0", "LLM_SELECTOR_MAX_REPAIRS": "0",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPredictor_Predict_PromptErrorReturnsFallback(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{response: `{"candidates": []}`})

	p := NewPredictor(cfg, orch, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta": make(chan int),
	})
	require.NoError(t, err)
	assert.Equal(t, "baseline", res.Model)
	assert.Len(t, res.Candidates, 3)
}

func TestPadCandidatesTagsPaddedEntriesAsSynthetic(t *testing.T) {
	one := []Candidate{{ID: "c1", Label: "L1", Description: "D1", Confidence: 0.9}}
	result := padCandidates(one)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	if result[0].Source != "llm" {
		t.Errorf("first candidate should be llm-sourced, got %q", result[0].Source)
	}
	for i := 1; i < 3; i++ {
		if result[i].Source != "synthetic" {
			t.Errorf("candidate %d should be synthetic, got %q", i, result[i].Source)
		}
	}
}

func TestFallbackResultTagsAllAsSynthetic(t *testing.T) {
	p := NewPredictor(&config.Config{LLMEnabled: false}, nil, zap.NewNop())
	result := p.fallbackResult(map[string]any{})
	for i, c := range result.Candidates {
		if c.Source != "synthetic" {
			t.Errorf("fallback candidate %d should be synthetic, got %q", i, c.Source)
		}
	}
}

func TestPredictor_Predict_FallbackResultWithoutTitle(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	p := NewPredictor(cfg, nil, zap.NewNop())
	res, err := p.Predict(context.Background(), map[string]any{
		"meta":      map[string]any{"startUrl": "https://example.com"},
		"events":    []any{},
		"snapshots": []any{},
	})
	require.NoError(t, err)
	assert.Equal(t, "采集 当前页面 数据", res.Candidates[0].Label)
}
