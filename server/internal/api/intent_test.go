package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeIntentProvider struct {
	resp *llm.CompletionResponse
	name string
}

func (f *fakeIntentProvider) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}

func (f *fakeIntentProvider) InputTokenUpperBound(req llm.CompletionRequest) (int, error) {
	encoded, err := json.Marshal(req)
	return len(encoded), err
}

func (f *fakeIntentProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return f.resp, nil
}

type fakePredictor struct {
	result *intent.PredictionResult
	err    error
}

func (f *fakePredictor) Predict(ctx context.Context, recording map[string]any) (*intent.PredictionResult, error) {
	return f.result, f.err
}

func newTestPredictor(t *testing.T, content string) *intent.Predictor {
	t.Helper()
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeIntentProvider{resp: &llm.CompletionResponse{Content: content}})
	return intent.NewPredictor(cfg, orch, zap.NewNop())
}

func TestPredictIntentHandler_Success(t *testing.T) {
	pred := newTestPredictor(t, `{"candidates": [{"id": "c1", "label": "采集标题", "description": "抓取标题", "confidence": 0.9, "suggestedVariables": ["maxItems"]}]}`)
	h := NewHandler(nil, nil, nil, pred, &config.Config{}, zap.NewNop())

	body, _ := json.Marshal(PredictIntentRequest{
		Recording: map[string]any{
			"meta": map[string]any{"startUrl": "https://example.com"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.PredictIntent(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp PredictIntentResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Candidates, 3)
	assert.Equal(t, "c1", resp.Candidates[0].ID)
	assert.Equal(t, "采集标题", resp.Candidates[0].Label)
	assert.Equal(t, "custom", resp.FallbackIntent.ID)
	assert.Equal(t, "fake-model", resp.Model)
}

func TestPredictIntentHandler_MissingRecording(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, &config.Config{}, zap.NewNop())

	body, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.PredictIntent(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "BAD_REQUEST", resp.Code)
}

func TestPredictIntentHandler_InvalidJSON(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, &config.Config{}, zap.NewNop())

	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.PredictIntent(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Inline recordings arrive in the extension's compressed form (refs/deltas);
// the handler must expand them and fail closed on malformed pointers.
func TestPredictIntentHandler_ExpandsCompressedRecording(t *testing.T) {
	pred := newTestPredictor(t, `{"candidates": []}`)
	h := NewHandler(nil, nil, nil, pred, &config.Config{}, zap.NewNop())

	compressed := func() map[string]any {
		return map[string]any{
			"meta": map[string]any{"startUrl": "https://example.com", "title": "Example"},
			"snapshots": []any{
				map[string]any{
					"timestamp": 1, "url": "https://example.com/", "phase": "initial", "sequence": 0,
					"selectorMap": map[string]any{}, "domTree": map[string]any{"type": "element", "tagName": "html"},
					"capture": map[string]any{"status": "complete", "frames": []any{}},
				},
				map[string]any{
					"timestamp": 2, "url": "https://example.com/", "phase": "before-action", "sequence": 1,
					"selectorMap": map[string]any{}, "base": 0,
					"patch": []any{map[string]any{"op": "replace", "path": "/domTree/tagName", "value": "main"}},
				},
			},
		}
	}

	t.Run("valid refs and deltas are expanded before prediction", func(t *testing.T) {
		body, _ := json.Marshal(PredictIntentRequest{Recording: compressed()})
		req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.PredictIntent(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("malformed snapshot pointers fail closed with 400", func(t *testing.T) {
		payload := compressed()
		snapshots := payload["snapshots"].([]any)
		snapshots[1].(map[string]any)["base"] = float64(99) // unknown base
		body, _ := json.Marshal(PredictIntentRequest{Recording: payload})
		req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.PredictIntent(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp ErrorResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "INVALID_RECORDING", resp.Code)
	})
}

func TestPredictIntentHandler_LLMFallback(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	pred := intent.NewPredictor(cfg, nil, zap.NewNop())
	h := NewHandler(nil, nil, nil, pred, cfg, zap.NewNop())

	body, _ := json.Marshal(PredictIntentRequest{
		Recording: map[string]any{
			"meta": map[string]any{"startUrl": "https://example.com", "title": "Example"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.PredictIntent(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp PredictIntentResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Candidates, 3)
	assert.Equal(t, "采集 Example 数据", resp.Candidates[0].Label)
	assert.Equal(t, "导航并采集目标页数据", resp.Candidates[1].Label)
	assert.Equal(t, "搜索关键词并采集结果", resp.Candidates[2].Label)
	assert.InDelta(t, 0.5, resp.Candidates[0].Confidence, 0.001)
	assert.InDelta(t, 0.3, resp.Candidates[1].Confidence, 0.001)
	assert.InDelta(t, 0.2, resp.Candidates[2].Confidence, 0.001)
	assert.Equal(t, "custom", resp.FallbackIntent.ID)
	assert.Equal(t, "baseline", resp.Model)
}

func TestPredictIntentHandler_AuditLog(t *testing.T) {
	s, err := store.NewWithConfig(&config.Config{SQLiteJournalMode: "WAL"}, ":memory:")
	require.NoError(t, err)
	defer s.Close()

	cfg := &config.Config{AdminAPIKey: "test-admin-key", AuditActor: "test"}
	sch := scheduler.New(s, cfg, zap.NewNop(), time.Minute, 3)
	pred := newTestPredictor(t, `{"candidates": [{"id": "c1", "label": "采集标题", "description": "抓取标题", "confidence": 0.9}]}`)
	h := NewHandler(s, sch, nil, pred, cfg, zap.NewNop())

	body, _ := json.Marshal(PredictIntentRequest{
		Recording: map[string]any{
			"meta": map[string]any{"startUrl": "https://example.com"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.PredictIntent(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	logs, total, err := s.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{
		Action:       "predict_intent",
		ResourceType: "recording",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, logs, 1)
	payload := map[string]any{}
	require.NoError(t, json.Unmarshal(logs[0].Payload, &payload))
	assert.Equal(t, "fake-model", payload["model"])
	assert.Equal(t, float64(3), payload["count"])
}

func TestPredictIntentHandler_RouteRequiresAuth(t *testing.T) {
	cfg := &config.Config{
		AdminAPIKey:           "test-admin-key",
		RateLimitPerSecond:    1000,
		RateLimitBurst:        2000,
		LLMRateLimitPerSecond: 1000,
		LLMRateLimitBurst:     2000,
	}
	pred := newTestPredictor(t, `{"candidates": [{"id": "c1", "label": "采集标题", "description": "抓取标题", "confidence": 0.9}]}`)
	h := NewHandler(nil, nil, nil, pred, cfg, zap.NewNop())
	router := NewRouter(h, cfg, zap.NewNop(), NewMetrics())

	body, _ := json.Marshal(PredictIntentRequest{
		Recording: map[string]any{"meta": map[string]any{}},
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-admin-key")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestPredictIntentHandler_PredictorError(t *testing.T) {
	pred := &fakePredictor{err: assert.AnError}
	h := NewHandler(nil, nil, nil, pred, &config.Config{}, zap.NewNop())

	body, _ := json.Marshal(PredictIntentRequest{
		Recording: map[string]any{"meta": map[string]any{}},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.PredictIntent(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "INTERNAL_ERROR", resp.Code)
}

func TestPredictIntentHandlerMapsStableDispatchErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "budget denied",
			err: &budget.DeniedError{
				Scope: budget.ScopeWorkspace, Limit: budget.LimitDaily,
			},
			wantStatus: http.StatusTooManyRequests,
			wantCode:   budget.CodeBudgetExceeded,
		},
		{
			name:       "ledger unavailable",
			err:        budget.ErrLedgerUnavailable,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   budget.CodeLedgerUnavailable,
		},
		{
			name:       "route unavailable",
			err:        &llm.RouteUnavailableError{Route: "primary", Reason: "usage_contract_violation"},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   llm.CodeProviderUnavailable,
		},
	}
	body, _ := json.Marshal(PredictIntentRequest{
		Recording: map[string]any{"meta": map[string]any{}},
	})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(nil, nil, nil, &fakePredictor{err: tc.err}, &config.Config{}, zap.NewNop())
			req := httptest.NewRequest(http.MethodPost, "/admin/rules/predict-intent", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			h.PredictIntent(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var response ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", response.Code, tc.wantCode)
			}
		})
	}
}

func TestToIntentCandidate(t *testing.T) {
	c := toIntentCandidate(intent.Candidate{
		ID:                 "c1",
		Label:              "label",
		Description:        "desc",
		Confidence:         0.5,
		SuggestedVariables: []string{"a", "b"},
	})
	assert.Equal(t, "c1", c.ID)
	assert.Equal(t, "label", c.Label)
	assert.Equal(t, "desc", c.Description)
	assert.InDelta(t, 0.5, c.Confidence, 0.001)
	assert.Equal(t, []string{"a", "b"}, c.SuggestedVariables)
}
