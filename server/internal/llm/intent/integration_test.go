//go:build llm_integration

package intent

import (
	"context"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeIntentProvider is a deterministic LLM provider used for integration tests.
type fakeIntentProvider struct {
	response string
}

func (f *fakeIntentProvider) Name() string { return "fake" }

func (f *fakeIntentProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{
		Content:      f.response,
		InputTokens:  100,
		OutputTokens: 50,
	}, nil
}

func TestIntegrationPredictIntent(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:  true,
		LLMProvider: "fake",
		LLMModel:    "fake-model",
	}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeIntentProvider{response: `{"candidates": [
		{"id": "c1", "label": "采集商品", "description": "抓取商品标题和价格", "confidence": 0.9},
		{"id": "c2", "label": "搜索", "description": "搜索关键词并采集结果", "confidence": 0.05},
		{"id": "c3", "label": "提交表单", "description": "填写表单并提交", "confidence": 0.05}
	]}`})

	p := NewPredictor(cfg, orch, zap.NewNop())

	res, err := p.Predict(context.Background(), map[string]any{
		"meta": map[string]any{
			"startUrl": "https://shop.example.com",
			"title":    "商品列表",
			"domain":   "shop.example.com",
		},
		"events": []any{
			map[string]any{"type": "click", "index": 1},
			map[string]any{"type": "click", "index": 2},
		},
		"domSnapshots": []any{},
	})
	require.NoError(t, err)
	require.Len(t, res.Candidates, 3)
	assert.Equal(t, "c1", res.Candidates[0].ID)
	assert.Equal(t, "custom", res.FallbackIntent.ID)
	assert.Equal(t, "fake-model", res.Model)
	assert.False(t, res.CacheHit)
}

func TestIntegrationPredictIntent_FallbackWhenDisabled(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	p := NewPredictor(cfg, nil, zap.NewNop())

	res, err := p.Predict(context.Background(), map[string]any{
		"meta": map[string]any{
			"startUrl": "https://example.com",
			"title":    "Example",
			"domain":   "example.com",
		},
		"events":       []any{},
		"domSnapshots": []any{},
	})
	require.NoError(t, err)
	require.Len(t, res.Candidates, 1)
	assert.Equal(t, "generic", res.Candidates[0].ID)
	assert.Equal(t, "baseline", res.Model)
}
