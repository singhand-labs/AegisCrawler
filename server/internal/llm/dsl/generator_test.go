package dsl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type fakeProvider struct {
	resp *llm.CompletionResponse
	err  error
	name string
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

func (f *fakeProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return f.resp, f.err
}

func baselineRule() *models.Rule {
	return &models.Rule{
		ID:      "ext-test-1",
		Version: "1.0.0",
		Name:    "Test rule",
		Domain:  models.JSON(`"example.com"`),
		Entry:   "https://example.com",
		Steps: models.JSON(`[
			{"action": "navigate", "url": "https://example.com"},
			{"action": "click", "target": {"selector": ".item"}}
		]`),
		Variables: models.JSON(`{}`),
	}
}

func searchBaselineRule() *models.Rule {
	return &models.Rule{
		ID:      "ext-search-1",
		Version: "1.0.0",
		Name:    "Search rule",
		Domain:  models.JSON(`"example.com"`),
		Entry:   "https://example.com",
		Steps: models.JSON(`[
			{"action": "navigate", "url": "https://example.com"},
			{"action": "type", "target": {"selector": "#q"}, "value": "phones"},
			{"action": "click", "target": {"selector": "#su"}}
		]`),
		Variables: models.JSON(`{}`),
	}
}

func TestGenerate_BaselineFallback(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "")
	require.NoError(t, err)
	assert.Equal(t, "ext-test-1", rule.ID)
	assert.Equal(t, "Test rule", rule.Name)
}

func TestGenerate_ListCollectionTemplate(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	cand := &intent.Candidate{ID: "c1", Label: "采集商品列表", Description: "抓取商品列表", Confidence: 0.9}
	rule, err := g.Generate(context.Background(), nil, baselineRule(), cand, "")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 3)
	assert.Equal(t, "extract", steps[2]["action"])
	assert.Equal(t, "itemData", steps[2]["name"])

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.Equal(t, float64(10), variables["maxItems"])
}

func TestGenerate_SearchPaginationTemplate(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	cand := &intent.Candidate{ID: "c2", Label: "搜索关键词并翻页", Description: "搜索后翻页采集", Confidence: 0.9}
	rule, err := g.Generate(context.Background(), nil, searchBaselineRule(), cand, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.Equal(t, "phones", variables["keyword"])
	assert.Equal(t, float64(1), variables["pages"])

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	// Type value should be templatized.
	assert.Equal(t, "{{keyword}}", steps[1]["value"])
	// A wait and extract step should be appended after the last submit-like action.
	require.Len(t, steps, 5)
	assert.Equal(t, "waitForTimeout", steps[3]["action"])
	assert.Equal(t, "extract", steps[4]["action"])
	assert.Equal(t, "searchResults", steps[4]["name"])
	assert.Equal(t, true, steps[4]["multiple"])
}

func TestGenerate_FormSubmitTemplate(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	cand := &intent.Candidate{ID: "c3", Label: "提交表单", Description: "填写并提交表单", Confidence: 0.9}
	rule, err := g.Generate(context.Background(), nil, baselineRule(), cand, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.NotNil(t, variables["formData"])
}

func TestGenerate_CustomDescriptionKeyword(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "采集列表数据")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 3)
	assert.Equal(t, "extract", steps[2]["action"])
}

func TestGenerate_CustomDescriptionWithLLM(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{resp: &llm.CompletionResponse{Content: `{"intentType": "list-collection"}`}})
	g := NewGenerator(cfg, orch, zap.NewNop())

	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "get all product cards")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 3)
	assert.Equal(t, "extract", steps[2]["action"])
}

func TestGenerate_CustomDescriptionLLMFallsBackToKeyword(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{err: assert.AnError})
	g := NewGenerator(cfg, orch, zap.NewNop())

	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "提交表单")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.NotNil(t, variables["formData"])
}

func TestGenerate_RequiresBaseline(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	_, err := g.Generate(context.Background(), nil, nil, nil, "")
	assert.EqualError(t, err, "baseline rule is required")
}

func TestRuleToYAML(t *testing.T) {
	yaml, err := RuleToYAML(baselineRule())
	require.NoError(t, err)
	assert.Contains(t, yaml, "id: ext-test-1")
	assert.Contains(t, yaml, "name: Test rule")
	assert.Contains(t, yaml, "domain: example.com")
	assert.Contains(t, yaml, "action: click")
	assert.Contains(t, yaml, "selector: .item")
	assert.NotContains(t, yaml, "- 123")
}

func TestNormalizeIntentType(t *testing.T) {
	assert.Equal(t, "list-collection", normalizeIntentType("list_collection"))
	assert.Equal(t, "search-pagination", normalizeIntentType("SEARCH"))
	assert.Equal(t, "form-submit", normalizeIntentType("form"))
	assert.Equal(t, "custom", normalizeIntentType("unknown"))
}

func TestGenerate_NilBaseline(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	_, err := g.Generate(context.Background(), nil, nil, nil, "")
	require.Error(t, err)
	assert.EqualError(t, err, "baseline rule is required")
}

func TestGenerate_CustomDescriptionNoLLMFallsBackToKeyword(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	g := NewGenerator(cfg, nil, zap.NewNop())

	// Description contains no keyword -> custom -> baseline unchanged.
	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "do something generic")
	require.NoError(t, err)
	assert.Equal(t, baselineRule().ID, rule.ID)
}

func TestGenerate_CustomDescriptionLLMErrorFallsBack(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{err: assert.AnError})
	g := NewGenerator(cfg, orch, zap.NewNop())

	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "unknown thing")
	require.NoError(t, err)
	assert.Equal(t, baselineRule().ID, rule.ID)
}

func TestGenerate_CustomDescriptionEnforcedPropagatesLedgerFailure(t *testing.T) {
	cfg := loadEnforcedGeneratorConfig(t)
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProviderAs("primary", &fakeProvider{name: "aliyun", resp: &llm.CompletionResponse{
		Content: `{"intentType":"custom"}`,
	}})
	generator := NewGenerator(cfg, orch, zap.NewNop())
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{WorkspaceID: "default"})
	ctx = llm.WithDispatchOperation(ctx, llm.DispatchOperation{
		Kind: budget.OperationDSL, ID: "dsl-sync-1", LogicalAttempt: 1,
	})

	rule, err := generator.Generate(ctx, nil, baselineRule(), nil, "unclassified workflow")
	if rule != nil || !budget.IsUnavailable(err) {
		t.Fatalf("rule=%+v err=%v, want terminal ledger unavailability", rule, err)
	}
}

func loadEnforcedGeneratorConfig(t *testing.T) *config.Config {
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

func TestGenerate_ClassifyWithLLM_InvalidJSONResponse(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{resp: &llm.CompletionResponse{Content: "not-json"}})
	g := NewGenerator(cfg, orch, zap.NewNop())

	// LLM returns invalid JSON, falls back to keyword ("采集" triggers list-collection).
	rule, err := g.Generate(context.Background(), nil, baselineRule(), nil, "采集商品")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 3)
	assert.Equal(t, "extract", steps[2]["action"])
}

func TestClassifyWithLLMMalformedCompletionLogsHashOnly(t *testing.T) {
	const sentinel = "malformed-completion-secret-sentinel"

	core, observed := observer.New(zap.WarnLevel)
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "fake-model"}
	orch := llm.NewOrchestrator(cfg, zap.NewNop())
	orch.RegisterProvider(&fakeProvider{resp: &llm.CompletionResponse{Content: sentinel}})
	generator := NewGenerator(cfg, orch, zap.New(core))

	if _, err := generator.classifyWithLLM(context.Background(), "custom workflow"); err == nil {
		t.Fatal("expected malformed completion error")
	}

	logs := observed.FilterMessage("dsl intent classification parse failed").All()
	if len(logs) != 1 {
		t.Fatalf("expected one parse failure log, got %d", len(logs))
	}
	fields := logs[0].ContextMap()
	encodedFields, err := json.Marshal(fields)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedFields), sentinel)
	assert.NotEmpty(t, fields["contentHash"])
	assert.EqualValues(t, len(sentinel), fields["contentBytes"])
	if _, ok := fields["content"]; ok {
		t.Fatal("malformed completion log must not include raw content")
	}
}

func TestGenerate_ListCollection_NoSteps(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule, err := g.Generate(context.Background(), nil, &models.Rule{
		ID:        "empty",
		Steps:     models.JSON(`[]`),
		Variables: models.JSON(`{}`),
	}, &intent.Candidate{Label: "列表采集"}, "")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 1)
	assert.Equal(t, "extract", steps[0]["action"])
}

func TestGenerate_SearchPagination_NoSteps(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule, err := g.Generate(context.Background(), nil, &models.Rule{
		ID:        "empty-search",
		Steps:     models.JSON(`[]`),
		Variables: models.JSON(`{}`),
	}, &intent.Candidate{Label: "搜索翻页"}, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.Equal(t, "", variables["keyword"])
	assert.Equal(t, float64(1), variables["pages"])
}

func TestGenerate_SearchPagination_NavigateAfterSubmit(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID: "nav-after-submit",
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"#q"},"value":"phones"},
			{"action":"click","target":{"selector":"#su"}},
			{"action":"navigate","url":"http://example.com/page2"},
			{"action":"navigate","url":"http://example.com/page3"}
		]`),
		Variables: models.JSON(`{}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "搜索翻页"}, "")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	// wait/extract should be inserted after the last contiguous navigate following the submit.
	require.Len(t, steps, 6)
	assert.Equal(t, "navigate", steps[2]["action"])
	assert.Equal(t, "navigate", steps[3]["action"])
	assert.Equal(t, "waitForTimeout", steps[4]["action"])
	assert.Equal(t, "extract", steps[5]["action"])
}

func TestGenerate_FormSubmit_ExistingVariables(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule, err := g.Generate(context.Background(), nil, &models.Rule{
		ID:        "form",
		Steps:     models.JSON(`[]`),
		Variables: models.JSON(`{"existing":"value"}`),
	}, &intent.Candidate{Label: "提交表单"}, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.Equal(t, "value", variables["existing"])
	assert.NotNil(t, variables["formData"])
}

func TestGenerate_RuleToMapError(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	badRule := &models.Rule{
		ID:    "bad",
		Steps: models.JSON(`{"invalid"`),
	}
	_, err := g.Generate(context.Background(), nil, badRule, nil, "")
	require.Error(t, err)
}

func TestGenerate_ListCollection_NonMapStepAndTarget(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID: "list-non-map",
		Steps: models.JSON(`[
			{"action":"navigate","url":"https://example.com"},
			"not-a-step",
			{"action":"click","target":"not-a-map"}
		]`),
		Variables: models.JSON(`{}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "列表采集"}, "")
	require.NoError(t, err)

	var steps []any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 4)
}

func TestGenerate_SearchPagination_KeywordPreserved(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID: "search-preserve",
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"#q"},"value":"existing"}
		]`),
		Variables: models.JSON(`{"keyword":"existing"}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "搜索翻页"}, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.Equal(t, "existing", variables["keyword"])
}

func TestGenerate_SearchPagination_NonStringValue(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID: "search-non-string",
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"#q"},"value":123},
			{"action":"type","target":{"selector":"#q2"},"value":""}
		]`),
		Variables: models.JSON(`{}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "搜索翻页"}, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.Equal(t, "", variables["keyword"])
}

func TestGenerate_SearchPagination_PressKeySubmit(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID: "search-press",
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"#q"},"value":"phones","submit":true}
		]`),
		Variables: models.JSON(`{}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "搜索翻页"}, "")
	require.NoError(t, err)

	var steps []map[string]any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	require.Len(t, steps, 3)
	assert.Equal(t, "{{keyword}}", steps[0]["value"])
	assert.Equal(t, "waitForTimeout", steps[1]["action"])
	assert.Equal(t, "extract", steps[2]["action"])
}

func TestGenerate_SearchPagination_NonMapStep(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID: "search-non-map-step",
		Steps: models.JSON(`[
			"not-a-step",
			{"action":"type","target":{"selector":"#q"},"value":"phones"},
			{"action":"pressKey","key":"Enter"},
			{"action":"navigate","url":"http://example.com/page2"},
			"skip-me"
		]`),
		Variables: models.JSON(`{}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "搜索翻页"}, "")
	require.NoError(t, err)

	var steps []any
	require.NoError(t, json.Unmarshal(rule.Steps, &steps))
	// Non-map steps are skipped; wait/extract inserted after pressKey + contiguous navigate.
	require.Len(t, steps, 7)
}

func TestGenerate_FormSubmit_NilVariables(t *testing.T) {
	g := NewGenerator(&config.Config{}, nil, zap.NewNop())
	rule := &models.Rule{
		ID:        "form-nil-vars",
		Steps:     models.JSON(`[]`),
		Variables: models.JSON(`{}`),
	}
	rule, err := g.Generate(context.Background(), nil, rule, &intent.Candidate{Label: "提交表单"}, "")
	require.NoError(t, err)

	var variables map[string]any
	require.NoError(t, json.Unmarshal(rule.Variables, &variables))
	assert.NotNil(t, variables["formData"])
}

func TestRuleToMap_MapToRule_Errors(t *testing.T) {
	// models.Rule with Steps containing invalid JSON triggers json.Marshal error.
	badRule := &models.Rule{
		ID:    "bad",
		Steps: models.JSON(`{"invalid"`),
	}
	_, err := ruleToMap(badRule)
	require.Error(t, err)

	// map with an unmarshalable value triggers json.Marshal error in mapToRule.
	badMap := map[string]any{"id": make(chan int)}
	_, err = mapToRule(badMap)
	require.Error(t, err)
}
