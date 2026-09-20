package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/prompt"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func testBaseline() map[string]any {
	return map[string]any{
		"name":  "基础",
		"entry": "http://example.com",
		"steps": []any{
			map[string]any{"action": "navigate", "url": "http://example.com"},
		},
	}
}

func TestEnhancerDisabledReturnsBaseline(t *testing.T) {
	cfg := &config.Config{LLMEnabled: false}
	e := NewEnhancer(cfg, nil, zap.NewNop())

	baseline := testBaseline()
	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline provider, got %q", res.Provider)
	}
	if res.Rule["name"] != baseline["name"] {
		t.Fatalf("expected baseline name, got %v", res.Rule["name"])
	}
}

func TestEnhancerReturnsPatchedRule(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:     true,
		LLMProvider:    "fake",
		LLMModel:       "m",
		LLMTemperature: 0.2,
		LLMCacheTTL:    0,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{
		name: "fake",
		resp: &CompletionResponse{
			Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"增强后"}],"variables":{},"suggestions":["rename"]}`},
	})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rule["name"] != "增强后" {
		t.Fatalf("expected patched name, got %v", res.Rule["name"])
	}
	if res.Provider != "fake" {
		t.Fatalf("expected provider fake, got %q", res.Provider)
	}
	if res.CacheHit {
		t.Fatal("unexpected cache hit")
	}
	if len(res.Suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
}

func TestEnhancerInvalidJSONFallsBack(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{Content: "not-json"}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if res.Rule["name"] != testBaseline()["name"] {
		t.Fatal("expected baseline rule unchanged")
	}
}

func TestEnhancerEnforcedProviderFailureReturnsErrorWithoutBaseline(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProviderAs("primary", &fakeProvider{name: "aliyun", err: errors.New("synthetic provider failure")})
	e := NewEnhancer(cfg, o, zap.NewNop())

	result, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err == nil || result != nil {
		t.Fatalf("Enhance() = %+v, %v; want nil enforced error", result, err)
	}
}

func TestEnhancerPatchFailureFallsBack(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/missing/path","value":"x"}],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" || res.Error == "" {
		t.Fatalf("expected fallback with error, got provider=%q error=%q", res.Provider, res.Error)
	}
}

func TestEnhancerValidationFailureFallsBack(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	// Patch clears the required name field.
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":""}],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" || res.Error == "" {
		t.Fatalf("expected fallback with error, got provider=%q error=%q", res.Provider, res.Error)
	}
}

type verifyRejectProvider struct {
	calls int
}

func (v *verifyRejectProvider) Name() string { return "verify-reject" }

func (v *verifyRejectProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	v.calls++
	if v.calls == 1 {
		return &CompletionResponse{Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"增强后"}],"variables":{},"suggestions":["rename"]}`}, nil
	}
	return &CompletionResponse{Content: `{"valid":false,"issues":["语义不一致"]}`}, nil
}

func TestEnhancerReflectionRejectsInvalidPatch(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         "verify-reject",
		LLMModel:            "m",
		LLMEnableReflection: true,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &verifyRejectProvider{}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if res.Rule["name"] != testBaseline()["name"] {
		t.Fatal("expected baseline rule unchanged")
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 llm calls, got %d", p.calls)
	}
}

type verifyAcceptProvider struct {
	calls int
}

func (v *verifyAcceptProvider) Name() string { return "verify-accept" }

func (v *verifyAcceptProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	v.calls++
	if v.calls == 1 {
		return &CompletionResponse{Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"增强后"}],"variables":{},"suggestions":["rename"]}`}, nil
	}
	return &CompletionResponse{Content: `{"valid":true,"issues":[]}`}, nil
}

func TestEnhancerReflectionAcceptsValidPatch(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         "verify-accept",
		LLMModel:            "m",
		LLMEnableReflection: true,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &verifyAcceptProvider{}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "verify-accept" {
		t.Fatalf("expected provider verify-accept, got %q", res.Provider)
	}
	if res.Rule["name"] != "增强后" {
		t.Fatalf("expected patched name, got %v", res.Rule["name"])
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 llm calls, got %d", p.calls)
	}
}

func TestEnhancerCacheHits(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:  true,
		LLMProvider: "fake",
		LLMModel:    "m",
		LLMCacheTTL: time.Hour,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"cached"}],"variables":{},"suggestions":[]}`,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	baseline := testBaseline()
	req := EnhanceRequest{BaselineRule: baseline, UserHint: "hint"}
	res1, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit")
	}
	if res1.Rule["name"] != res2.Rule["name"] {
		t.Fatal("cached result differs")
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}

func TestEnhancerCacheExpires(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:  true,
		LLMProvider: "fake",
		LLMModel:    "m",
		LLMCacheTTL: time.Millisecond,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"cached"}],"variables":{},"suggestions":[]}`,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	req := EnhanceRequest{BaselineRule: testBaseline(), UserHint: "hint"}
	if _, err := e.Enhance(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	res2, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res2.CacheHit {
		t.Fatal("expected cache miss after expiration")
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 provider calls, got %d", p.calls)
	}
}

func TestEnhancerSafetyFlags(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"add","path":"/steps/1","value":{"action":"submit"}}],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	baseline := testBaseline()
	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SafetyFlags) == 0 {
		t.Fatal("expected safety flags for submit action")
	}
	fmt.Printf("flags: %+v\n", res.SafetyFlags)
}

func TestParseSuggestionStripsMarkdownFences(t *testing.T) {
	content := "```json\n{\"selectors\":{},\"steps\":[{\"op\":\"replace\",\"path\":\"/name\",\"value\":\"x\"}],\"variables\":{},\"suggestions\":[]}\n```"
	s, err := parseSuggestion(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Steps) != 1 || s.Steps[0].Path != "/name" {
		t.Fatalf("expected parsed suggestion, got %+v", s)
	}
}

func TestStripMarkdownFencesVariations(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		expected string
	}{
		{"no fences", `{"a":1}`, `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"plain fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"trailing whitespace", "```json\n{\"a\":1}\n```\n\n", `{"a":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMarkdownFences(tc.content)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

func TestCopyMapFallbackReturnsShallowCopy(t *testing.T) {
	baseline := map[string]any{
		"name": "base",
		"ch":   make(chan int), // cannot be deep-copied via JSON
	}
	got := copyMap(baseline)
	if got["name"] != "base" {
		t.Fatalf("expected shallow copy to retain baseline fields, got %v", got)
	}
	if len(got) != len(baseline) {
		t.Fatalf("expected same length, got %d", len(got))
	}
}

func TestEnhancerInputTokenLimitFallsBack(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:        true,
		LLMProvider:       "fake",
		LLMModel:          "m",
		LLMMaxInputTokens: 1,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"x"}],"variables":{},"suggestions":[]}`,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if p.calls != 0 {
		t.Fatalf("expected no provider calls, got %d", p.calls)
	}
	if res.InputTokens == 0 {
		t.Fatal("expected input tokens to be reported")
	}
}

func TestEnhancerOutputTokenLimitFallsBack(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:         true,
		LLMProvider:        "fake",
		LLMModel:           "m",
		LLMMaxOutputTokens: 1,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content:      `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"x"}],"variables":{},"suggestions":[]}`,
		InputTokens:  2,
		OutputTokens: 10,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
	if res.OutputTokens != 10 {
		t.Fatalf("expected output tokens reported, got %d", res.OutputTokens)
	}
}

func TestEnhancerDailyCostBudgetExhausted(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:         true,
		LLMProvider:        "fake",
		LLMModel:           "m",
		LLMDailyCostBudget: 0.000001, // tiny budget, quickly exhausted
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content:      `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"enhanced"}],"variables":{},"suggestions":[]}`,
		InputTokens:  1000,
		OutputTokens: 1000,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	// First call should consume the budget.
	res1, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Provider == "baseline" {
		t.Fatal("expected first enhancement to succeed")
	}

	// A fresh enhancer with the same config has its own budget instance, so to
	// test exhaustion we reuse the same enhancer.
	res2, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Provider != "baseline" {
		t.Fatalf("expected budget-exhausted fallback, got %q", res2.Provider)
	}
	if res2.Error == "" {
		t.Fatal("expected error in result")
	}
	if p.calls != 1 {
		t.Fatalf("expected only 1 provider call, got %d", p.calls)
	}
}

func TestEnhancerCacheHitDoesNotRecordBudget(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:         true,
		LLMProvider:        "fake",
		LLMModel:           "m",
		LLMCacheTTL:        time.Hour,
		LLMDailyCostBudget: 1, // large enough to not exhaust
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content:      `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"cached"}],"variables":{},"suggestions":[]}`,
		InputTokens:  1000,
		OutputTokens: 1000,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	req := EnhanceRequest{BaselineRule: testBaseline(), UserHint: "hint"}
	res1, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res1.CacheHit {
		t.Fatal("expected first enhancement to be cache miss")
	}
	usedAfterFirst := e.budget.used

	res2, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit on second request")
	}
	if e.budget.used != usedAfterFirst {
		t.Fatalf("cache hit changed budget usage: before %v after %v", usedAfterFirst, e.budget.used)
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}

func TestEnhancerCacheHitBypassesExhaustedBudget(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:         true,
		LLMProvider:        "fake",
		LLMModel:           "m",
		LLMCacheTTL:        time.Hour,
		LLMDailyCostBudget: 0.000001, // tiny budget, exhausted by the first call
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{
		Content:      `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"cached"}],"variables":{},"suggestions":[]}`,
		InputTokens:  1000,
		OutputTokens: 1000,
	}}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	req := EnhanceRequest{BaselineRule: testBaseline(), UserHint: "hint"}
	res1, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Provider == "baseline" || res1.CacheHit {
		t.Fatalf("expected first enhancement to succeed as cache miss, got provider=%q cacheHit=%v", res1.Provider, res1.CacheHit)
	}

	// Budget is exhausted, but the identical request should still hit the cache
	// without falling back to baseline.
	res2, err := e.Enhance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Provider != "fake" {
		t.Fatalf("expected cache hit to succeed, got provider=%q", res2.Provider)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit")
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}

func TestEnhancerContentFilterRejectsScript(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{"x":{"selector":"<script>alert(1)</script>","reason":"bad"}},"steps":[],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if len(res.SafetyFlags) == 0 {
		t.Fatal("expected content safety flags")
	}
}

func TestEnhancerContentFilterRejectsEventHandlerInVariable(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[],"variables":{"hook":"<body onload='steal()'>"},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" {
		t.Fatalf("expected baseline fallback, got %q", res.Provider)
	}
	if res.Error == "" {
		t.Fatal("expected error in result")
	}
	if !strings.Contains(res.Error, "content filter") {
		t.Fatalf("expected content-filter error, got %q", res.Error)
	}
	if len(res.SafetyFlags) == 0 {
		t.Fatal("expected content safety flags")
	}
}

func TestEnhancerContentFilterFlagsExternalURL(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/steps/0/url","value":"https://evil.example.com"}],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider == "baseline" {
		t.Fatal("external URL should be flagged but not rejected")
	}
	found := false
	for _, f := range res.SafetyFlags {
		if strings.Contains(f.Reason, "外部 URL") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected external URL flag, got %+v", res.SafetyFlags)
	}
}

func TestEnhancerSetMetrics(t *testing.T) {
	cfg := &config.Config{LLMEnabled: true, LLMProvider: "fake", LLMModel: "m"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"x"}],"variables":{},"suggestions":[]}`,
	}})
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	e := NewEnhancer(cfg, o, zap.NewNop())
	e.SetMetrics(metrics)

	_, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(metrics.Requests.WithLabelValues("fake", "m")); got != 1 {
		t.Fatalf("expected 1 request metric after SetMetrics, got %v", got)
	}
}

func TestEnhancerAuditPrompt(t *testing.T) {
	core, observed := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	cfg := &config.Config{
		LLMEnabled:      true,
		LLMProvider:     "fake",
		LLMModel:        "m",
		LLMAuditPrompts: true,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"x"}],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, logger)
	_, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if observed.FilterMessage("llm prompt audit").Len() != 1 {
		t.Fatal("expected prompt audit log")
	}
}

func TestEnhancerEnforcedAuditDoesNotLogUserHintOrPromptPrefix(t *testing.T) {
	const (
		hintSentinel     = "enforced-user-hint-secret-sentinel"
		baselineSentinel = "enforced-baseline-name-secret-sentinel"
	)

	core, observed := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	cfg := loadEnforcedOrchestratorConfig(t, "audit-sentinel")
	cfg.LLMAuditPrompts = true
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProviderAs("primary", &fakeProvider{name: "aliyun", resp: &CompletionResponse{
		Content: `{"selectors":{},"steps":[],"variables":{},"suggestions":[]}`,
	}})
	e := NewEnhancer(cfg, o, logger)
	baseline := testBaseline()
	baseline["name"] = baselineSentinel

	if result, err := e.Enhance(context.Background(), EnhanceRequest{
		BaselineRule: baseline,
		UserHint:     hintSentinel,
	}); err == nil || result != nil {
		t.Fatalf("Enhance() = %+v, %v; want enforced dispatch identity error", result, err)
	}

	for _, entry := range observed.All() {
		for _, sentinel := range []string{hintSentinel, baselineSentinel} {
			if strings.Contains(entry.Message, sentinel) {
				t.Fatalf("log message exposed enforced content: %q", entry.Message)
			}
			for key, value := range entry.ContextMap() {
				if strings.Contains(fmt.Sprint(value), sentinel) {
					t.Fatalf("log field %q exposed enforced content: %v", key, value)
				}
			}
		}
	}

	requestLogs := observed.FilterMessage("rule enhancement requested").All()
	if len(requestLogs) != 1 {
		t.Fatalf("expected one request log, got %d", len(requestLogs))
	}
	requestFields := requestLogs[0].ContextMap()
	if _, ok := requestFields["hint"]; ok {
		t.Fatal("enforced request log must not include a raw hint field")
	}
	if present, _ := requestFields["hintPresent"].(bool); !present {
		t.Fatalf("hintPresent = %v, want true", requestFields["hintPresent"])
	}
	if _, ok := requestFields["baselineName"]; ok {
		t.Fatal("enforced request log must not include a raw baselineName field")
	}
	if present, _ := requestFields["baselineNamePresent"].(bool); !present {
		t.Fatalf("baselineNamePresent = %v, want true", requestFields["baselineNamePresent"])
	}
	if requestFields["baselineNameHash"] == "" ||
		requestFields["baselineNameBytes"] != int64(len(baselineSentinel)) {
		t.Fatalf("missing hashed baseline-name evidence: %+v", requestFields)
	}

	auditLogs := observed.FilterMessage("llm prompt audit").All()
	if len(auditLogs) != 1 {
		t.Fatalf("expected one prompt audit log, got %d", len(auditLogs))
	}
	auditFields := auditLogs[0].ContextMap()
	if hash, _ := auditFields["promptHash"].(string); hash == "" {
		t.Fatal("expected a non-empty prompt hash")
	}
	if _, ok := auditFields["promptPrefix"]; ok {
		t.Fatal("enforced prompt audit must not include promptPrefix")
	}
}

type enforcedReflectionProvider struct {
	responses []*CompletionResponse
	calls     int
}

func (p *enforcedReflectionProvider) Name() string { return "aliyun" }

func (p *enforcedReflectionProvider) InputTokenUpperBound(request CompletionRequest) (int, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func (p *enforcedReflectionProvider) Complete(context.Context, CompletionRequest) (*CompletionResponse, error) {
	if p.calls >= len(p.responses) {
		return nil, errors.New("unexpected enforced reflection provider call")
	}
	response := *p.responses[p.calls]
	p.calls++
	return &response, nil
}

func TestEnhancerEnforcedReflectionLogHashesProviderReason(t *testing.T) {
	const sentinel = "reflection-provider-reason-secret-sentinel"

	core, observed := observer.New(zap.WarnLevel)
	cfg := loadEnforcedOrchestratorConfig(t, "reflection-audit-sentinel")
	cfg.LLMEnableReflection = true
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	orchestrator.SetBudgetLedger(newFakeLedger())
	provider := &enforcedReflectionProvider{responses: []*CompletionResponse{
		{
			Content:      `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"enhanced"}],"variables":{},"suggestions":[]}`,
			InputTokens:  10,
			OutputTokens: 5,
			UsageMetadata: CompletionUsageMetadata{
				InputTokensPresent: true, OutputTokensPresent: true,
			},
		},
		{
			Content:      `{"valid":false,"issues":["` + sentinel + `"]}`,
			InputTokens:  8,
			OutputTokens: 4,
			UsageMetadata: CompletionUsageMetadata{
				InputTokensPresent: true, OutputTokensPresent: true,
			},
		},
	}}
	orchestrator.RegisterProviderAs("primary", provider)
	enhancer := NewEnhancer(cfg, orchestrator, zap.New(core))

	result, err := enhancer.Enhance(
		enforcedDispatchContext("reflection-audit"),
		EnhanceRequest{BaselineRule: testBaseline()},
	)
	if err == nil || result != nil {
		t.Fatalf("Enhance() = %+v, %v; want enforced reflection rejection", result, err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}

	entries := observed.FilterMessage("llm reflection rejected patch, falling back").All()
	if len(entries) != 1 {
		t.Fatalf("reflection rejection logs = %d, want 1", len(entries))
	}
	entry := entries[0]
	for key, value := range entry.ContextMap() {
		if strings.Contains(fmt.Sprint(value), sentinel) {
			t.Fatalf("log field %q exposed provider reflection reason: %v", key, value)
		}
	}
	fields := entry.ContextMap()
	if fields["reasonClass"] != "reflection_rejected" || fields["reasonHash"] == "" ||
		fields["reasonBytes"] != int64(len(sentinel)) {
		t.Fatalf("missing hashed reflection evidence: %+v", fields)
	}
	if _, ok := fields["reason"]; ok {
		t.Fatal("enforced reflection log must not contain a raw reason field")
	}
}

type verifyRejectNoIssuesProvider struct{ calls int }

func (v *verifyRejectNoIssuesProvider) Name() string { return "verify-reject-no-issues" }

func (v *verifyRejectNoIssuesProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	v.calls++
	if v.calls == 1 {
		return &CompletionResponse{Content: `{"selectors":{},"steps":[{"op":"replace","path":"/name","value":"增强后"}],"variables":{},"suggestions":["rename"]}`}, nil
	}
	return &CompletionResponse{Content: `{"valid":false,"issues":[]}`}, nil
}

func TestEnhancerReflectionRejectsInvalidPatchWithoutIssues(t *testing.T) {
	cfg := &config.Config{
		LLMEnabled:          true,
		LLMProvider:         "verify-reject-no-issues",
		LLMModel:            "m",
		LLMEnableReflection: true,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &verifyRejectNoIssuesProvider{}
	o.RegisterProvider(p)
	e := NewEnhancer(cfg, o, zap.NewNop())

	res, err := e.Enhance(context.Background(), EnhanceRequest{BaselineRule: testBaseline()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "baseline" || res.Error == "" {
		t.Fatalf("expected baseline fallback with error, got provider=%q error=%q", res.Provider, res.Error)
	}
}

func TestPromptAuditPreview(t *testing.T) {
	user := strings.Repeat("a", 300)
	hash, prefix := prompt.AuditPreview("", user, "")
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if len(prefix) != 200 {
		t.Fatalf("expected prefix length 200, got %d", len(prefix))
	}

	// Same prompt produces the same hash.
	hash2, _ := prompt.AuditPreview("", user, "")
	if hash != hash2 {
		t.Fatal("expected deterministic hash")
	}
}
