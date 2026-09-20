package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestGenerateFromIntentHandler_Success(t *testing.T) {
	g := dsl.NewGenerator(&config.Config{}, nil, zap.NewNop())
	h := NewHandler(nil, nil, nil, nil, &config.Config{}, zap.NewNop())
	h.SetDSLGenerator(g)

	body, _ := json.Marshal(GenerateFromIntentRequest{
		Recording: map[string]any{"meta": map[string]any{"startUrl": "https://example.com"}},
		BaselineRule: &models.Rule{
			ID:      "ext-test",
			Version: "1.0.0",
			Name:    "Baseline",
			Domain:  models.JSON(`"example.com"`),
			Entry:   "https://example.com",
			Steps:   models.JSON(`[{"action":"navigate","url":"https://example.com"}]`),
		},
		CustomDescription: "采集商品列表",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/generate-from-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.GenerateFromIntent(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp GenerateFromIntentResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ext-test", resp.Rule.ID)
	assert.NotEmpty(t, resp.YAML)
}

func TestGenerateFromIntentHandler_MissingBaseline(t *testing.T) {
	g := dsl.NewGenerator(&config.Config{}, nil, zap.NewNop())
	h := NewHandler(nil, nil, nil, nil, &config.Config{}, zap.NewNop())
	h.SetDSLGenerator(g)

	body, _ := json.Marshal(GenerateFromIntentRequest{Recording: map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/generate-from-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.GenerateFromIntent(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "BAD_REQUEST", resp.Code)
}

func TestGenerateFromIntentHandler_InvalidJSON(t *testing.T) {
	h := NewHandler(nil, nil, nil, nil, &config.Config{}, zap.NewNop())
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/generate-from-intent", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.GenerateFromIntent(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGenerateFromIntentHandler_RouteRequiresAuth(t *testing.T) {
	g := dsl.NewGenerator(&config.Config{}, nil, zap.NewNop())
	cfg := &config.Config{
		AdminAPIKey:           "test-admin-key",
		RateLimitPerSecond:    1000,
		RateLimitBurst:        2000,
		LLMRateLimitPerSecond: 1000,
		LLMRateLimitBurst:     2000,
	}
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	h.SetDSLGenerator(g)
	router := NewRouter(h, cfg, zap.NewNop(), NewMetrics())

	body, _ := json.Marshal(GenerateFromIntentRequest{
		BaselineRule: &models.Rule{
			ID:     "ext-test",
			Name:   "Baseline",
			Domain: models.JSON(`"example.com"`),
			Entry:  "https://example.com",
			Steps:  models.JSON(`[{"action":"navigate","url":"https://example.com"}]`),
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/rules/generate-from-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/admin/rules/generate-from-intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-admin-key")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGenerateFromIntentHandlerPropagatesEnforcedLedgerFailure(t *testing.T) {
	setEnforcedPolicyEnvironment(t, "https://provider.example.test/v1")
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatal(err)
	}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProviderAs("primary", &fakeIntentProvider{
		name: "aliyun",
		resp: &llm.CompletionResponse{Content: `{"intentType":"custom"}`},
	})
	generator := dsl.NewGenerator(cfg, orch, zap.NewNop())
	h := NewHandler(nil, nil, nil, nil, cfg, zap.NewNop())
	h.SetDSLGenerator(generator)

	body, _ := json.Marshal(GenerateFromIntentRequest{
		BaselineRule: &models.Rule{
			ID: "rule-1", Name: "Baseline", Domain: models.JSON(`"example.com"`),
			Entry: "https://example.com", Steps: models.JSON(`[]`),
		},
		CustomDescription: "an unclassified custom workflow",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/generate-from-intent", bytes.NewReader(body))
	req = req.WithContext(authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "admin", WorkspaceID: "default", Roles: []authz.Role{authz.RoleAdmin},
	}))
	rec := httptest.NewRecorder()
	h.GenerateFromIntent(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body.String())
	}
	var response ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "BUDGET_LEDGER_UNAVAILABLE" {
		t.Fatalf("code = %q, want BUDGET_LEDGER_UNAVAILABLE", response.Code)
	}
}
