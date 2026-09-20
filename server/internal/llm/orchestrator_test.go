package llm

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/cache"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type fakeProvider struct {
	name            string
	resp            *CompletionResponse
	err             error
	calls           int
	requests        []CompletionRequest
	inputUpperBound int
	inputBoundErr   error
	boundStarted    chan struct{}
	boundRelease    chan struct{}
	boundRequests   []CompletionRequest
}

type completeOnceTestAdapter struct{ orchestrator *Orchestrator }

func (a completeOnceTestAdapter) Complete(
	ctx context.Context,
	request CompletionRequest,
) (*CompletionResult, error) {
	return a.orchestrator.CompleteOnce(ctx, request)
}

type internalTraceProvider struct {
	name        string
	internalErr error
	response    *CompletionResponse
	terminalErr error
}

func (p *internalTraceProvider) Name() string { return p.name }

func (p *internalTraceProvider) Complete(ctx context.Context, request CompletionRequest) (*CompletionResponse, error) {
	internalErr := p.internalErr
	if internalErr == nil {
		internalErr = errors.New("internal provider attempt failed")
	}
	if err := TraceProviderError(
		ctx,
		request,
		p.name,
		request.Model,
		503,
		"internal_error",
		"",
		internalErr,
	); err != nil {
		return nil, err
	}
	return p.response, p.terminalErr
}

type traceAwareCache struct {
	sink                      *traceTestSink
	setBeforeTerminalResponse bool
}

func (c *traceAwareCache) Get(context.Context, string) (*cache.Entry, bool, error) {
	return nil, false, nil
}

func (c *traceAwareCache) Set(context.Context, string, *cache.Entry, time.Duration) error {
	if len(c.sink.events) == 0 ||
		c.sink.events[len(c.sink.events)-1].BoundaryKind != CompletionBoundaryProviderResponse {
		c.setBeforeTerminalResponse = true
	}
	return nil
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) InputTokenUpperBound(req CompletionRequest) (int, error) {
	f.boundRequests = append(f.boundRequests, req)
	if f.boundStarted != nil {
		close(f.boundStarted)
	}
	if f.boundRelease != nil {
		<-f.boundRelease
	}
	if f.inputBoundErr != nil {
		return 0, f.inputBoundErr
	}
	if f.inputUpperBound > 0 {
		return f.inputUpperBound, nil
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func (f *fakeProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.resp == nil {
		return nil, nil
	}
	response := *f.resp
	if !response.UsageMetadata.InputTokensPresent &&
		!response.UsageMetadata.OutputTokensPresent &&
		len(response.UsageMetadata.UnknownCategories) == 0 &&
		(response.InputTokens != 0 || response.OutputTokens != 0) {
		// Fake nonzero usage models a provider usage object containing both
		// fields. Tests for omitted fields construct the assessment directly.
		response.UsageMetadata.InputTokensPresent = true
		response.UsageMetadata.OutputTokensPresent = true
	}
	return &response, nil
}

func TestOrchestratorEnforcedPrepareCallAndAvailabilityFallback(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	primary := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 503}}
	fallback := &fakeProvider{name: "anthropic-secondary", resp: &CompletionResponse{Content: "ok"}}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)

	request := CompletionRequest{Model: "caller-model", Temperature: 0.9, MaxOutputTokens: 9, User: "test"}
	prepared, err := o.PrepareCall("primary", request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Route != "primary" || prepared.Provider != "aliyun" || prepared.Model != "qwen-test" ||
		prepared.Request.Model != "qwen-test" || prepared.Request.Temperature != 0 ||
		prepared.Request.MaxOutputTokens != 512 || prepared.Request.ExecutionPolicy != CompletionExecutionAtMostOnce {
		t.Fatalf("PreparedCall = %+v", prepared)
	}

	result, err := o.Complete(enforcedDispatchContext("op-1"), request)
	if err != nil {
		t.Fatal(err)
	}
	// The primary and the fallback are each admitted and settled independently.
	// Neither fake response reports usage, so both settle their full
	// reservation conservatively.
	wantEvents := []string{
		"reserve:0", "dispatch:0", "settle_uncertain:0:availability_failure",
		"reserve:1", "dispatch:1", "settle_uncertain:1:missing_usage",
	}
	if got := ledger.Events(); !slices.Equal(got, wantEvents) {
		t.Fatalf("ledger events = %v, want %v", got, wantEvents)
	}
	if primary.calls != 1 || fallback.calls != 1 || result.Provider != "anthropic-secondary" || result.Model != "claude-test" {
		t.Fatalf("unexpected enforced routing: primary=%d fallback=%d result=%+v", primary.calls, fallback.calls, result)
	}
	if got := primary.requests[0]; got.Model != "qwen-test" || got.Temperature != 0 || got.MaxOutputTokens != 512 || got.ExecutionPolicy != CompletionExecutionAtMostOnce {
		t.Fatalf("primary request = %+v", got)
	}
}

func TestOrchestratorEnforcedPrepareCallDeepClonesStructuredOutput(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-clone")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	originalSchema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)
	request := CompletionRequest{
		User: "test",
		StructuredOutput: &StructuredOutput{
			Name:   "result",
			Schema: originalSchema,
		},
	}

	prepared, err := orchestrator.PrepareCall("primary", request)
	if err != nil {
		t.Fatal(err)
	}
	hashBefore := prepared.RequestHash
	request.StructuredOutput.Name = "mutated"
	request.StructuredOutput.Schema[0] = '['

	if prepared.Request.StructuredOutput.Name != "result" ||
		string(prepared.Request.StructuredOutput.Schema) != `{"type":"object","properties":{"ok":{"type":"boolean"}}}` {
		t.Fatalf("prepared request shares mutable schema state: %+v", prepared.Request.StructuredOutput)
	}
	if prepared.RequestHash != hashBefore ||
		CanonicalCompletionRequestHash(prepared.Request) != hashBefore {
		t.Fatal("prepared request hash changed after caller mutation")
	}
}

func TestOrchestratorEnforcedCompleteFreezesRequestBeforeAdmission(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-concurrent-clone")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	orchestrator.SetBudgetLedger(newFakeLedger())
	provider := &fakeProvider{
		name:            "aliyun",
		resp:            &CompletionResponse{Content: "ok"},
		boundStarted:    make(chan struct{}),
		boundRelease:    make(chan struct{}),
		inputUpperBound: 128,
	}
	orchestrator.RegisterProviderAs("primary", provider)
	request := CompletionRequest{
		User: "test",
		StructuredOutput: &StructuredOutput{
			Name:   "result",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.Complete(enforcedDispatchContext("op-freeze"), request)
		done <- err
	}()
	<-provider.boundStarted
	request.StructuredOutput.Name = "mutated"
	request.StructuredOutput.Schema[0] = '['
	close(provider.boundRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if len(provider.requests) != 1 ||
		provider.requests[0].StructuredOutput == nil ||
		provider.requests[0].StructuredOutput.Name != "result" ||
		string(provider.requests[0].StructuredOutput.Schema) != `{"type":"object"}` {
		t.Fatalf("provider received caller mutation: %+v", provider.requests)
	}
}

func TestOrchestratorEnforcedRejectsInputAboveRouteBoundBeforeReservation(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-input-bound")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	provider := &fakeProvider{
		name:            "aliyun",
		resp:            &CompletionResponse{Content: "must not run"},
		inputUpperBound: 4097,
	}
	orchestrator.RegisterProviderAs("primary", provider)

	_, err := orchestrator.Complete(enforcedDispatchContext("op-too-large"), CompletionRequest{User: "test"})
	if err == nil || !strings.Contains(err.Error(), "input upper bound 4097 exceeds enforced maximum 4096") {
		t.Fatalf("completion error = %v", err)
	}
	if !errors.Is(err, ErrRouteUnavailable) || IsAvailabilityError(err) {
		t.Fatalf("input limit error classification = %v", err)
	}
	if code, ok := StableDispatchErrorCode(err); !ok || code != CodeProviderUnavailable {
		t.Fatalf("stable input-limit code = %q, %v", code, ok)
	}
	if provider.calls != 0 {
		t.Fatalf("oversized input reached provider %d times", provider.calls)
	}
	if events := ledger.Events(); len(events) != 0 {
		t.Fatalf("oversized input reached ledger: %v", events)
	}
}

func TestOrchestratorEnforcedRouteRequiresInputBoundCapability(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-input-capability")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	provider := &internalTraceProvider{
		name:     "aliyun",
		response: &CompletionResponse{Content: "must not run"},
	}
	orchestrator.RegisterProviderAs("primary", provider)

	if ready, reason := orchestrator.RouteReady("primary"); ready || reason != "input_bound_unavailable" {
		t.Fatalf("route ready=%v reason=%q", ready, reason)
	}
	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-no-input-bound"),
		CompletionRequest{User: "test"},
	); !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("completion error = %v, want ErrRouteUnavailable", err)
	}
	if events := ledger.Events(); len(events) != 0 {
		t.Fatalf("unbounded adapter reached ledger: %v", events)
	}
}

func TestOrchestratorEnforcedAcceptsExactInputBound(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-input-boundary")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	provider := &fakeProvider{
		name:            "aliyun",
		resp:            &CompletionResponse{Content: "ok"},
		inputUpperBound: 4096,
	}
	orchestrator.RegisterProviderAs("primary", provider)

	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-boundary"),
		CompletionRequest{User: "test"},
	); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("exact-bound request provider calls = %d, want 1", provider.calls)
	}
}

func TestOrchestratorEnforcedRechecksSmallerFallbackInputBound(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-fallback-input")
	cfg.EnforcedLLMPolicy().Fallback.MaxInputTokens = 64
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	primary := &fakeProvider{
		name:            "aliyun",
		err:             availabilityHTTPError{status: 503},
		inputUpperBound: 64,
	}
	fallback := &fakeProvider{
		name:            "anthropic-secondary",
		resp:            &CompletionResponse{Content: "must not run"},
		inputUpperBound: 65,
	}
	orchestrator.RegisterProviderAs("primary", primary)
	orchestrator.RegisterProviderAs("fallback", fallback)

	_, err := orchestrator.Complete(
		enforcedDispatchContext("op-fallback-too-large"),
		CompletionRequest{User: "test"},
	)
	if err == nil || !strings.Contains(err.Error(), "fallback route input upper bound 65 exceeds enforced maximum 64") {
		t.Fatalf("completion error = %v", err)
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("provider calls primary=%d fallback=%d", primary.calls, fallback.calls)
	}
	wantEvents := []string{
		"reserve:0",
		"dispatch:0",
		"settle_uncertain:0:availability_failure",
	}
	if got := ledger.Events(); !slices.Equal(got, wantEvents) {
		t.Fatalf("ledger events = %v, want %v", got, wantEvents)
	}
}

func TestOrchestratorEnforcedDoesNotFallbackNonAvailabilityError(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	primary := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 400}}
	fallback := &fakeProvider{name: "anthropic-secondary", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)

	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); err == nil {
		t.Fatal("expected primary error")
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("non-availability error routed incorrectly: primary=%d fallback=%d", primary.calls, fallback.calls)
	}
	// A non-availability failure charges the primary conservatively and never
	// admits a second physical call.
	wantEvents := []string{"reserve:0", "dispatch:0", "settle_uncertain:0:failed_call"}
	if got := ledger.Events(); !slices.Equal(got, wantEvents) {
		t.Fatalf("ledger events = %v, want %v", got, wantEvents)
	}
}

func TestOrchestratorEnforcedFallsBackOnTypedTransportError(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetBudgetLedger(newFakeLedger())
	primary := &fakeProvider{name: "aliyun", err: availabilityTransportError{}}
	fallback := &fakeProvider{name: "anthropic-secondary", resp: &CompletionResponse{Content: "fallback"}}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)

	result, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"})
	if err != nil || result.Content != "fallback" || primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("typed transport fallback = %+v, %v; primary=%d fallback=%d", result, err, primary.calls, fallback.calls)
	}
}

func TestOrchestratorEnforcedCompleteOnceDoesNotFallback(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetBudgetLedger(newFakeLedger())
	primary := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 503}}
	fallback := &fakeProvider{name: "anthropic-secondary", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)

	if _, err := o.CompleteOnce(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); err == nil {
		t.Fatal("expected primary error")
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("CompleteOnce routed incorrectly: primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestOrchestratorEnforcedCacheIsPartitionedByPolicyFingerprint(t *testing.T) {
	sharedCache := newMemoryCache()
	request := CompletionRequest{User: "same prompt"}

	firstConfig := loadEnforcedOrchestratorConfig(t, "policy-a")
	first := NewOrchestratorWithCache(firstConfig, sharedCache, zap.NewNop())
	firstLedger := newFakeLedger()
	first.SetBudgetLedger(firstLedger)
	firstProvider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "first"}}
	first.RegisterProviderAs("primary", firstProvider)
	if _, err := first.Complete(enforcedDispatchContext("op-1"), request); err != nil {
		t.Fatal(err)
	}
	if result, hit := first.PeekCache(enforcedDispatchContext("peek-1"), request); !hit || result.Content != "first" {
		t.Fatalf("PeekCache() = %+v, %t; want enforced cached result", result, hit)
	}

	secondConfig := loadEnforcedOrchestratorConfig(t, "policy-b")
	second := NewOrchestratorWithCache(secondConfig, sharedCache, zap.NewNop())
	secondLedger := newFakeLedger()
	second.SetBudgetLedger(secondLedger)
	secondProvider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "second"}}
	second.RegisterProviderAs("primary", secondProvider)
	result, err := second.Complete(enforcedDispatchContext("op-2"), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHit || result.Content != "second" || secondProvider.calls != 1 {
		t.Fatalf("policy change reused a stale cache entry: result=%+v calls=%d", result, secondProvider.calls)
	}
}

func TestOrchestratorEnforcedCacheHitCreatesNoReservation(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "cached", InputTokens: 10, OutputTokens: 5}}
	o.RegisterProviderAs("primary", provider)

	request := CompletionRequest{User: "same prompt"}
	if _, err := o.Complete(enforcedDispatchContext("op-1"), request); err != nil {
		t.Fatal(err)
	}
	firstEvents := len(ledger.Events())

	// The second identical request is served from the policy-bound cache, so it
	// must reach neither the provider nor the ledger.
	result, err := o.Complete(enforcedDispatchContext("op-2"), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CacheHit {
		t.Fatal("expected a cache hit")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if got := len(ledger.Events()); got != firstEvents {
		t.Fatalf("a cache hit must create no ledger activity, got %v", ledger.Events()[firstEvents:])
	}
}

func TestOrchestratorEnforcedTraceSharesExactDispatchLineage(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-lineage")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	primary := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 503}}
	fallback := &fakeProvider{
		name: "anthropic-secondary",
		resp: &CompletionResponse{Content: "fallback", InputTokens: 10, OutputTokens: 5},
	}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)
	sink := &traceTestSink{}

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(enforcedDispatchContext("lineage-op"), sink),
		o,
		CompletionRequest{User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err != nil || result == nil || result.Provider != "anthropic-secondary" {
		t.Fatalf("enforced fallback = %+v, %v", result, err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("expected exactly two real provider boundaries, got %+v", sink.events)
	}
	policy := cfg.EnforcedLLMPolicy()
	for index, event := range sink.events {
		if event.Dispatch == nil ||
			event.Dispatch.OperationKind != budget.OperationEnhance ||
			event.Dispatch.OperationID != "lineage-op:final" ||
			event.Dispatch.LogicalAttempt != 1 ||
			event.Dispatch.PhysicalOrdinal != index ||
			event.Dispatch.RequestHash != event.RequestHash ||
			event.Dispatch.PolicyFingerprint != policy.Fingerprint {
			t.Fatalf("event %d has incomplete dispatch lineage: %+v", index, event)
		}
	}
	if sink.events[0].Dispatch.RouteSlot != "primary" ||
		sink.events[0].Dispatch.PriceRevision != "policy-lineage" ||
		sink.events[0].Result.Provider != "aliyun" ||
		sink.events[1].Dispatch.RouteSlot != "fallback" ||
		sink.events[1].Dispatch.PriceRevision != "fallback-policy-lineage" ||
		sink.events[1].Result.Provider != "anthropic-secondary" {
		t.Fatalf("primary/fallback trace provenance is wrong: %+v", sink.events)
	}

	// A fallback completion cannot be stored under the primary cache key
	// without losing its distinct route/model/request-hash lineage.
	if _, err := o.Complete(enforcedDispatchContext("lineage-op-2"), CompletionRequest{User: "collect"}); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 2 || fallback.calls != 2 {
		t.Fatalf("fallback response was incorrectly cached: primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestOrchestratorEnforcedPreDispatchFailuresDoNotCreateProviderTraces(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Orchestrator, *fakeLedger)
	}{
		{
			name: "budget denial",
			setup: func(_ *Orchestrator, ledger *fakeLedger) {
				ledger.denyOrdinals[budget.OrdinalPrimary] = true
			},
		},
		{
			name: "ledger unavailable",
			setup: func(_ *Orchestrator, ledger *fakeLedger) {
				ledger.unavailable = true
			},
		},
		{
			name: "route closed",
			setup: func(o *Orchestrator, _ *fakeLedger) {
				o.closeRoute("primary", "usage_contract_violation")
			},
		},
		{
			name: "dispatch marker failure",
			setup: func(_ *Orchestrator, ledger *fakeLedger) {
				ledger.failMark = true
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadEnforcedOrchestratorConfig(t, "pre-dispatch")
			o := NewOrchestrator(cfg, zap.NewNop())
			ledger := newFakeLedger()
			o.SetBudgetLedger(ledger)
			provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "must not run"}}
			o.RegisterProviderAs("primary", provider)
			tc.setup(o, ledger)
			sink := &traceTestSink{}

			result, err := CompleteWithTrace(
				WithCompletionTraceSink(enforcedDispatchContext("pre-dispatch-op"), sink),
				o,
				CompletionRequest{User: "collect"},
				CompletionTraceMetadata{Phase: CompletionPhaseFinal},
			)
			if err == nil || result != nil {
				t.Fatalf("expected fail-closed pre-dispatch error: result=%+v err=%v", result, err)
			}
			if provider.calls != 0 || len(sink.events) != 0 {
				t.Fatalf("pre-dispatch failure invented provider activity: calls=%d events=%+v", provider.calls, sink.events)
			}
		})
	}
}

func TestOrchestratorEnforcedFallbackDenialKeepsOnlyPrimaryTrace(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "fallback-denial")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	ledger.denyOrdinals[budget.OrdinalFallback] = true
	o.SetBudgetLedger(ledger)
	primary := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 503}}
	fallback := &fakeProvider{name: "anthropic-secondary", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)
	sink := &traceTestSink{}

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(enforcedDispatchContext("fallback-denied-op"), sink),
		o,
		CompletionRequest{User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if result != nil || !budget.IsDenied(err) {
		t.Fatalf("fallback denial = %+v, %v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].BoundaryKind != CompletionBoundaryProviderError ||
		sink.events[0].Result.Provider != "aliyun" ||
		sink.events[0].Dispatch == nil ||
		sink.events[0].Dispatch.PhysicalOrdinal != budget.OrdinalPrimary ||
		fallback.calls != 0 {
		t.Fatalf("fallback denial created false provider provenance: events=%+v fallback=%d", sink.events, fallback.calls)
	}
}

func TestOrchestratorEnforcedCacheHitCarriesCurrentLogicalLineage(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "cache-lineage")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{
		name: "aliyun",
		resp: &CompletionResponse{Content: "cached", InputTokens: 10, OutputTokens: 5},
	}
	o.RegisterProviderAs("primary", provider)
	request := CompletionRequest{User: "same prompt"}
	if _, err := o.Complete(enforcedDispatchContext("cache-fill"), request); err != nil {
		t.Fatal(err)
	}
	ledgerEvents := len(ledger.Events())
	sink := &traceTestSink{}

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(enforcedDispatchContext("cache-read"), sink),
		o,
		request,
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err != nil || result == nil || !result.CacheHit {
		t.Fatalf("cache read = %+v, %v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].BoundaryKind != CompletionBoundaryCacheHit ||
		sink.events[0].Dispatch == nil ||
		sink.events[0].Dispatch.OperationID != "cache-read:final" ||
		sink.events[0].Dispatch.PhysicalOrdinal != budget.OrdinalPrimary ||
		sink.events[0].Dispatch.RequestHash != sink.events[0].RequestHash {
		t.Fatalf("cache hit lost current logical lineage: %+v", sink.events)
	}
	if len(ledger.Events()) != ledgerEvents || provider.calls != 1 {
		t.Fatalf("cache hit created physical activity: ledger=%v calls=%d", ledger.Events()[ledgerEvents:], provider.calls)
	}
}

func TestOrchestratorEnforcedCacheHitStillRequiresDispatchOwnership(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{
		Content: "cached", InputTokens: 10, OutputTokens: 5,
	}}
	o.RegisterProviderAs("primary", provider)
	request := CompletionRequest{User: "same prompt"}
	if _, err := o.Complete(enforcedDispatchContext("op-1"), request); err != nil {
		t.Fatal(err)
	}

	if _, err := o.Complete(context.Background(), request); err == nil {
		t.Fatal("an unattributable operation must not receive a cache hit")
	}
	if provider.calls != 1 {
		t.Fatalf("the ownership failure must not reach the provider, got %d calls", provider.calls)
	}
}

func TestOrchestratorEnforcedWorkflowStagesUseDistinctDispatchIdentities(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{
		Content: "ok", InputTokens: 10, OutputTokens: 5,
	}}
	o.RegisterProviderAs("primary", provider)
	base := enforcedDispatchContext("requirement-job-1")
	chunk := 0

	if _, err := CompleteWithTrace(base, o, CompletionRequest{User: "analysis"}, CompletionTraceMetadata{
		Phase: CompletionPhaseAnalysis, ChunkIndex: &chunk, ChunkCount: 2,
	}); err != nil {
		t.Fatalf("analysis completion failed: %v", err)
	}
	if _, err := CompleteWithTrace(base, o, CompletionRequest{User: "synthesis"}, CompletionTraceMetadata{
		Phase: CompletionPhaseSynthesis, ChunkCount: 2,
	}); err != nil {
		t.Fatalf("synthesis completion failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want one for each distinct stage", provider.calls)
	}
	for _, operationID := range []string{"requirement-job-1:analysis:0", "requirement-job-1:synthesis"} {
		entry, err := ledger.Lookup(context.Background(), budget.DispatchIdentity{
			WorkspaceID: "default", OperationKind: budget.OperationEnhance,
			OperationID: operationID, LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
		})
		if err != nil || entry == nil || entry.State != budget.StateSettled {
			t.Fatalf("entry %q = %+v, %v; want settled", operationID, entry, err)
		}
	}
}

func TestOrchestratorUsageContractViolationClosesRoute(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{
		Content: "response already received",
		// The configured primary input bound is 4096.
		InputTokens: 4097, OutputTokens: 1,
	}}
	o.RegisterProviderAs("primary", provider)

	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "first"}); err != nil {
		t.Fatalf("the already-returned response may remain usable: %v", err)
	}
	if ready, reason := o.RouteReady("primary"); ready || reason != "usage_contract_violation" {
		t.Fatalf("route ready=%v reason=%q, want closed usage contract", ready, reason)
	}
	_, err := o.Complete(enforcedDispatchContext("op-2"), CompletionRequest{User: "second"})
	if !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("second completion = %v, want ErrRouteUnavailable", err)
	}
	if provider.calls != 1 {
		t.Fatalf("closed route reached provider %d times, want 1 total", provider.calls)
	}
}

func TestOrchestratorUnknownUsageCategorySettlesFullAndClosesRoute(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-unknown-usage")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	orchestrator.SetMetrics(metrics)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{
		Content: "usable response", InputTokens: 10, OutputTokens: 2,
		UsageMetadata: CompletionUsageMetadata{
			InputTokensPresent:  true,
			OutputTokensPresent: true,
			UnknownCategories:   []string{"future_billable_tokens"},
		},
	}}
	orchestrator.RegisterProviderAs("primary", provider)

	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-unknown-usage"),
		CompletionRequest{User: "first"},
	); err != nil {
		t.Fatalf("the already-returned response may remain usable: %v", err)
	}
	identity := budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-unknown-usage", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	}
	entry, err := ledger.Lookup(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != budget.StateUsageUncertain || entry.Settled != entry.Reserved ||
		entry.ErrorClass != "unknown_usage_category" {
		t.Fatalf("ledger entry = %+v, want full uncertain settlement", entry)
	}
	if ready, reason := orchestrator.RouteReady("primary"); ready || reason != "usage_contract_violation" {
		t.Fatalf("route ready=%v reason=%q", ready, reason)
	}
	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-after-unknown"),
		CompletionRequest{User: "second"},
	); !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("closed-route completion error = %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("closed route reached provider %d times", provider.calls)
	}
	if got := testutil.ToFloat64(
		metrics.UncertainUsage.WithLabelValues("unknown_usage_category"),
	); got != 1 {
		t.Fatalf("unknown-usage metric = %v, want 1", got)
	}
}

func TestOrchestratorContradictoryUsageSettlesFullAndClosesRoute(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-contradictory-usage")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	orchestrator.SetMetrics(metrics)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{
		Content: "usable response", InputTokens: -10, CachedInputTokens: -3, OutputTokens: -2,
		UsageMetadata: CompletionUsageMetadata{
			InputTokensPresent:       true,
			CachedInputTokensPresent: true,
			OutputTokensPresent:      true,
		},
	}}
	orchestrator.RegisterProviderAs("primary", provider)

	result, err := orchestrator.Complete(
		enforcedDispatchContext("op-contradictory-usage"),
		CompletionRequest{User: "first"},
	)
	if err != nil {
		t.Fatalf("the already-returned response may remain usable: %v", err)
	}
	if result.InputTokens != 0 || result.CachedInputTokens != 0 || result.OutputTokens != 0 ||
		!result.UsageMetadata.Contradictory {
		t.Fatalf("negative provider usage was not normalized at the boundary: %+v", result.CompletionResponse)
	}
	identity := budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-contradictory-usage", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	}
	entry, err := ledger.Lookup(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != budget.StateUsageUncertain || entry.Settled != entry.Reserved ||
		entry.ErrorClass != "contradictory_usage" {
		t.Fatalf("ledger entry = %+v, want full contradictory-usage settlement", entry)
	}
	if ready, reason := orchestrator.RouteReady("primary"); ready || reason != "usage_contract_violation" {
		t.Fatalf("route ready=%v reason=%q", ready, reason)
	}
	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-after-contradictory"),
		CompletionRequest{User: "second"},
	); !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("closed-route completion error = %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("closed route reached provider %d times", provider.calls)
	}
	if got := testutil.ToFloat64(
		metrics.UncertainUsage.WithLabelValues("contradictory_usage"),
	); got != 1 {
		t.Fatalf("contradictory-usage metric = %v, want 1", got)
	}
}

func TestOrchestratorReleasesDefinitelyUndispatchedReservationAfterMarkerFailure(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	ledger.failMark = true
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", provider)

	_, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"})
	if !budget.IsUnavailable(err) {
		t.Fatalf("completion error = %v, want ledger unavailable", err)
	}
	if provider.calls != 0 {
		t.Fatalf("marker failure reached provider %d times", provider.calls)
	}
	entry, lookupErr := ledger.Lookup(context.Background(), budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-1", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	})
	if lookupErr != nil || entry == nil || entry.State != budget.StateReleased {
		t.Fatalf("reservation after marker failure = %+v, %v; want released", entry, lookupErr)
	}
}

func TestOrchestratorEnforcedRequiresDispatchIdentity(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetBudgetLedger(newFakeLedger())
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", provider)

	// No dispatch operation is bound, so the call is unattributable and must
	// fail closed rather than be charged to the default workspace.
	if _, err := o.Complete(context.Background(), CompletionRequest{User: "test"}); err == nil {
		t.Fatal("expected an unattributable enforced completion to fail closed")
	}
	if provider.calls != 0 {
		t.Fatalf("provider must not be reached, got %d calls", provider.calls)
	}
}

func TestOrchestratorEnforcedFailsClosedWithoutLedger(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", provider)

	_, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"})
	if !budget.IsUnavailable(err) {
		t.Fatalf("expected %s, got %v", budget.CodeLedgerUnavailable, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider must not be reached without a ledger, got %d calls", provider.calls)
	}
}

func TestOrchestratorEnforcedBudgetDenialPreventsDispatch(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	ledger.denyOrdinals[budget.OrdinalPrimary] = true
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", provider)

	_, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"})
	if !budget.IsDenied(err) {
		t.Fatalf("expected a budget denial, got %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("a denied call must never reach the provider, got %d calls", provider.calls)
	}
	// Only the refused reservation is recorded; there is no dispatch marker.
	if got := ledger.Events(); !slices.Equal(got, []string{"reserve:0"}) {
		t.Fatalf("ledger events = %v, want only a refused reservation", got)
	}
}

func TestOrchestratorEnforcedDeniedFallbackIsTerminal(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	// The primary is admitted but the fallback admission is refused by the
	// remaining budget. That denial is terminal.
	ledger.denyOrdinals[budget.OrdinalFallback] = true
	o.SetBudgetLedger(ledger)
	primary := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 503}}
	fallback := &fakeProvider{name: "anthropic-secondary", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProviderAs("primary", primary)
	o.RegisterProviderAs("fallback", fallback)

	_, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"})
	if !budget.IsDenied(err) {
		t.Fatalf("expected the terminal fallback denial, got %v", err)
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("routing = primary %d fallback %d, want 1 and 0", primary.calls, fallback.calls)
	}
}

func TestOrchestratorEnforcedSettlesTrustedUsageExactly(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{
		name: "aliyun",
		resp: &CompletionResponse{Content: "ok", InputTokens: 1_000, OutputTokens: 100},
	}
	o.RegisterProviderAs("primary", provider)

	if _, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"}); err != nil {
		t.Fatal(err)
	}

	identity := budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-1", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	}
	entry, err := ledger.Lookup(context.Background(), identity)
	if err != nil || entry == nil {
		t.Fatalf("Lookup = %v, %v", entry, err)
	}
	if entry.State != budget.StateSettled {
		t.Fatalf("state = %q, want %q", entry.State, budget.StateSettled)
	}
	// Input is charged at the higher configured input rate because the
	// completion contract exposes no cached-input split.
	rates := budget.Rates{
		InputUSDPerMillion:       entry.Rates.InputUSDPerMillion,
		CachedInputUSDPerMillion: entry.Rates.CachedInputUSDPerMillion,
		OutputUSDPerMillion:      entry.Rates.OutputUSDPerMillion,
	}
	want, err := budget.SettledCost(rates, budget.Usage{UncachedInputTokens: 1_000, OutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Settled != want {
		t.Fatalf("settled = %d, want the exact trusted cost %d", entry.Settled, want)
	}
	if entry.Settled > entry.Reserved {
		t.Fatalf("settled %d exceeds reservation %d", entry.Settled, entry.Reserved)
	}
}

func TestOrchestratorEnforcedSettlesTrustedCachedUsageExactly(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-cached-usage")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	provider := &fakeProvider{
		name: "aliyun",
		resp: &CompletionResponse{
			Content: "ok", InputTokens: 1_000, CachedInputTokens: 400, OutputTokens: 100,
			UsageMetadata: CompletionUsageMetadata{
				InputTokensPresent:       true,
				OutputTokensPresent:      true,
				CachedInputTokensPresent: true,
			},
		},
	}
	orchestrator.RegisterProviderAs("primary", provider)

	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-cached-usage"),
		CompletionRequest{User: "test"},
	); err != nil {
		t.Fatal(err)
	}
	identity := budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-cached-usage", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	}
	entry, err := ledger.Lookup(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	want, err := budget.SettledCost(entry.Rates, budget.Usage{
		UncachedInputTokens: 600,
		CachedInputTokens:   400,
		OutputTokens:        100,
		CachedInputReported: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != budget.StateSettled || entry.Settled != want {
		t.Fatalf("entry = %+v, want trusted cached cost %d", entry, want)
	}
}

func TestOrchestratorEnforcedMissingUsageSettlesFullReservation(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	// A usable response that reports no usage is still charged conservatively.
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "ok"}}
	o.RegisterProviderAs("primary", provider)

	result, err := o.Complete(enforcedDispatchContext("op-1"), CompletionRequest{User: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q, want the usable response", result.Content)
	}

	identity := budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-1", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	}
	entry, err := ledger.Lookup(context.Background(), identity)
	if err != nil || entry == nil {
		t.Fatalf("Lookup = %v, %v", entry, err)
	}
	if entry.State != budget.StateUsageUncertain {
		t.Fatalf("state = %q, want %q", entry.State, budget.StateUsageUncertain)
	}
	if entry.Settled != entry.Reserved {
		t.Fatalf("settled = %d, want the full reservation %d", entry.Settled, entry.Reserved)
	}
}

func TestOrchestratorEnforcedOmittedUsageFieldSettlesFullReservation(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-partial-usage")
	orchestrator := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	orchestrator.SetBudgetLedger(ledger)
	provider := &fakeProvider{
		name: "aliyun",
		resp: &CompletionResponse{
			Content: "ok", InputTokens: 10,
			UsageMetadata: CompletionUsageMetadata{InputTokensPresent: true},
		},
	}
	orchestrator.RegisterProviderAs("primary", provider)

	if _, err := orchestrator.Complete(
		enforcedDispatchContext("op-partial-usage"),
		CompletionRequest{User: "test"},
	); err != nil {
		t.Fatal(err)
	}
	entry, err := ledger.Lookup(context.Background(), budget.DispatchIdentity{
		WorkspaceID: "default", OperationKind: budget.OperationEnhance,
		OperationID: "op-partial-usage", LogicalAttempt: 1, PhysicalOrdinal: budget.OrdinalPrimary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != budget.StateUsageUncertain || entry.Settled != entry.Reserved ||
		entry.ErrorClass != "missing_usage" {
		t.Fatalf("entry = %+v, want full missing-usage settlement", entry)
	}
}

func TestOrchestratorEnforcedNeverRedispatchesADuplicateIdentity(t *testing.T) {
	cfg := loadEnforcedOrchestratorConfig(t, "policy-a")
	o := NewOrchestrator(cfg, zap.NewNop())
	ledger := newFakeLedger()
	o.SetBudgetLedger(ledger)
	provider := &fakeProvider{name: "aliyun", err: availabilityHTTPError{status: 400}}
	o.RegisterProviderAs("primary", provider)

	ctx := enforcedDispatchContext("op-1")
	if _, err := o.Complete(ctx, CompletionRequest{User: "first"}); err == nil {
		t.Fatal("expected the primary error")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}

	// Reusing the same logical attempt must not send a second provider request,
	// even for a different prompt.
	_, err := o.Complete(ctx, CompletionRequest{User: "second"})
	if !errors.Is(err, budget.ErrDuplicateDispatch) {
		t.Fatalf("expected ErrDuplicateDispatch, got %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("a duplicate identity must never be redispatched, got %d calls", provider.calls)
	}
}

func loadEnforcedOrchestratorConfig(t *testing.T, revision string) *config.Config {
	t.Helper()
	for key, value := range map[string]string{
		"LLM_ENABLED":                         "true",
		"LLM_POLICY_MODE":                     "enforced",
		"LLM_PRIMARY_PROVIDER":                "aliyun",
		"LLM_PRIMARY_ADAPTER":                 "openai",
		"LLM_PRIMARY_MODEL":                   "qwen-test",
		"LLM_PRIMARY_BASE_URL":                "https://provider.example.test/v1",
		"LLM_PRIMARY_API_KEY":                 "synthetic-primary-key",
		"LLM_PRIMARY_REQUEST_TIMEOUT":         "30s",
		"LLM_PRIMARY_TEMPERATURE":             "0",
		"LLM_PRIMARY_STRICT_TOOL_OUTPUT":      "true",
		"LLM_PRIMARY_ENABLE_THINKING":         "false",
		"LLM_PRIMARY_INPUT_USD_PER_MILLION":   "0.14",
		"LLM_PRIMARY_OUTPUT_USD_PER_MILLION":  "0.28",
		"LLM_PRIMARY_MAX_INPUT_TOKENS":        "4096",
		"LLM_PRIMARY_MAX_OUTPUT_TOKENS":       "512",
		"LLM_PRIMARY_PRICE_REVISION":          revision,
		"LLM_FALLBACK_PROVIDER":               "anthropic-secondary",
		"LLM_FALLBACK_ADAPTER":                "anthropic",
		"LLM_FALLBACK_MODEL":                  "claude-test",
		"LLM_FALLBACK_BASE_URL":               "https://fallback.example.test/v1",
		"LLM_FALLBACK_API_KEY":                "synthetic-fallback-key",
		"LLM_FALLBACK_REQUEST_TIMEOUT":        "45s",
		"LLM_FALLBACK_TEMPERATURE":            "0.1",
		"LLM_FALLBACK_STRICT_TOOL_OUTPUT":     "false",
		"LLM_FALLBACK_ENABLE_THINKING":        "false",
		"LLM_FALLBACK_INPUT_USD_PER_MILLION":  "0.2",
		"LLM_FALLBACK_OUTPUT_USD_PER_MILLION": "0.4",
		"LLM_FALLBACK_MAX_INPUT_TOKENS":       "8192",
		"LLM_FALLBACK_MAX_OUTPUT_TOKENS":      "1024",
		"LLM_FALLBACK_PRICE_REVISION":         "fallback-" + revision,
		"LLM_GLOBAL_MAX_REQUEST_USD":          "0.60",
		"LLM_GLOBAL_DAILY_BUDGET_USD":         "3.00",
		"LLM_WORKSPACE_BUDGETS_JSON":          `{"default":{"maxRequestUSD":"0.60","dailyBudgetUSD":"3.00"}}`,
		"LLM_REQUIREMENT_MAX_ATTEMPTS":        "2",
		"LLM_DSL_GENERATION_MAX_ATTEMPTS":     "3",
		"LLM_DSL_MAX_REPAIRS":                 "1",
		"LLM_SELECTOR_MAX_REPAIRS":            "1",
		"LLM_PROVIDER_CONFIGS":                "",
		"LLM_PROVIDER":                        "",
		"LLM_API_KEY":                         "",
		"LLM_BASE_URL":                        "",
		"LLM_MODEL":                           "",
		"LLM_TEMPERATURE":                     "",
		"LLM_REQUEST_TIMEOUT":                 "",
		"LLM_MAX_INPUT_TOKENS":                "",
		"LLM_MAX_OUTPUT_TOKENS":               "",
		"LLM_OPENAI_ENABLE_THINKING":          "",
		"LLM_OPENAI_STRICT_TOOL_OUTPUT":       "",
		"LLM_JOB_MAX_ATTEMPTS":                "",
		"LLM_DSL_SELECTOR_REPAIR_ENABLED":     "",
		"LLM_DAILY_COST_BUDGET":               "",
		"LLM_MAX_RETRIES":                     "",
		// fallback semantics tests isolate from call-level retries
		"LLM_CALL_MAX_RETRIES":                "0",
		"LLM_ALLOW_DEGRADED_FALLBACK":         "",
		"LLM_TEMPERATURE_COMPATIBILITY_RETRY": "",
	} {
		t.Setenv(key, value)
	}
	cfg := config.Load()
	if err := cfg.ValidateLLMPolicy(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOrchestratorUnknownProvider(t *testing.T) {
	cfg := &config.Config{LLMProvider: "unknown"}
	o := NewOrchestrator(cfg, zap.NewNop())
	_, err := o.Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestOrchestratorRegisterProviderAsUsesRouteKey(t *testing.T) {
	o := NewOrchestrator(&config.Config{}, zap.NewNop())
	provider := &fakeProvider{name: "aliyun", resp: &CompletionResponse{Content: "ok"}}

	o.RegisterProviderAs("primary", provider)

	o.providersMu.RLock()
	registered := o.providers["primary"]
	_, registeredByProvenance := o.providers["aliyun"]
	o.providersMu.RUnlock()
	if registered != provider || registeredByProvenance {
		t.Fatalf("registered routes = %+v, want only primary -> aliyun", o.providers)
	}
}

func TestOrchestratorSuccess(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "fake", resp: &CompletionResponse{Content: "ok", InputTokens: 1, OutputTokens: 2}}
	o.RegisterProvider(p)

	res, err := o.Complete(context.Background(), CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "ok" {
		t.Fatalf("expected content ok, got %q", res.Content)
	}
	if res.InputTokens != 1 || res.OutputTokens != 2 {
		t.Fatalf("unexpected tokens: %d/%d", res.InputTokens, res.OutputTokens)
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 call, got %d", p.calls)
	}
}

func TestOrchestratorCompleteOnceDisablesRetryAndFallback(t *testing.T) {
	cfg := &config.Config{
		LLMProvider: "primary", LLMFallbackProvider: "fallback", LLMMaxRetries: 3,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	primary := &fakeProvider{name: "primary", err: errors.New("primary failed")}
	fallback := &fakeProvider{
		name: "fallback", resp: &CompletionResponse{Content: "must not run"},
	}
	o.RegisterProvider(primary)
	o.RegisterProvider(fallback)
	request := CompletionRequest{User: "bounded selector repair"}
	if _, err := o.CompleteOnce(context.Background(), request); err == nil {
		t.Fatal("expected primary failure")
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("one-shot completion retried or fell back: primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestOrchestratorCompleteOnceCacheIsAtMostOnceAndPolicyPartitioned(t *testing.T) {
	cfg := &config.Config{
		LLMProvider: "primary", LLMCacheTTL: time.Hour, LLMModel: "model",
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	provider := &fakeProvider{
		name: "primary",
		resp: &CompletionResponse{
			Content:    `{"replacements":[{"slotId":"slot","candidateId":"candidate"}]}`,
			ResponseID: "selector-repair-response", FinishReason: "stop",
		},
	}
	o.RegisterProvider(provider)
	request := CompletionRequest{Model: "model", User: "same logical prompt", JSONMode: true}
	adapter := completeOnceTestAdapter{orchestrator: o}

	firstSink := &traceTestSink{}
	first, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), firstSink),
		adapter, request,
		CompletionTraceMetadata{Phase: CompletionPhaseSelectorRepair},
	)
	if err != nil || first.CacheHit {
		t.Fatalf("first one-shot completion was not a physical miss: result=%+v err=%v", first, err)
	}
	secondSink := &traceTestSink{}
	second, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), secondSink),
		adapter, request,
		CompletionTraceMetadata{Phase: CompletionPhaseSelectorRepair},
	)
	if err != nil || !second.CacheHit || provider.calls != 1 {
		t.Fatalf("one-shot cache consumed another physical call: result=%+v calls=%d err=%v",
			second, provider.calls, err)
	}
	if len(firstSink.events) != 1 ||
		firstSink.events[0].BoundaryKind != CompletionBoundaryProviderResponse ||
		firstSink.events[0].ProviderAttempt != 1 ||
		len(secondSink.events) != 1 ||
		secondSink.events[0].BoundaryKind != CompletionBoundaryCacheHit ||
		secondSink.events[0].ProviderAttempt != 0 {
		t.Fatalf("unexpected one-shot cache trace: first=%+v second=%+v",
			firstSink.events, secondSink.events)
	}

	ordinary, err := o.Complete(context.Background(), request)
	if err != nil || ordinary.CacheHit || provider.calls != 2 {
		t.Fatalf("default request reused at-most-once cache partition: result=%+v calls=%d err=%v",
			ordinary, provider.calls, err)
	}
	ordinaryCached, err := o.Complete(context.Background(), request)
	if err != nil || !ordinaryCached.CacheHit || provider.calls != 2 {
		t.Fatalf("default cache partition was not stable: result=%+v calls=%d err=%v",
			ordinaryCached, provider.calls, err)
	}
}

func TestOrchestratorFallbackProvider(t *testing.T) {
	cfg := &config.Config{
		LLMProvider:         "fail",
		LLMFallbackProvider: "fallback",
		LLMMaxRetries:       0,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	fail := &fakeProvider{name: "fail", err: errors.New("boom")}
	fallback := &fakeProvider{name: "fallback", resp: &CompletionResponse{Content: "fallback ok"}}
	o.RegisterProvider(fail)
	o.RegisterProvider(fallback)

	res, err := o.Complete(context.Background(), CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "fallback ok" {
		t.Fatalf("expected fallback response, got %q", res.Content)
	}
	if res.Provider != "fallback" {
		t.Fatalf("expected fallback provider provenance, got %q", res.Provider)
	}
	if fail.calls != 1 || fallback.calls != 1 {
		t.Fatalf("expected 1 primary and 1 fallback call, got %d/%d", fail.calls, fallback.calls)
	}
}

func TestOrchestratorTraceCapturesEveryRetryAndFallbackBoundary(t *testing.T) {
	cfg := &config.Config{
		LLMProvider: "primary", LLMFallbackProvider: "fallback",
		LLMCallMaxRetries: 1, LLMMaxRetries: 1, LLMModel: "test-model",
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	primary := &fakeProvider{name: "primary", err: errors.New("primary unavailable")}
	fallback := &fakeProvider{
		name: "fallback",
		resp: &CompletionResponse{
			Content: `{"ok":true}`, ResponseID: "fallback-response", FinishReason: "stop",
		},
	}
	o.RegisterProvider(primary)
	o.RegisterProvider(fallback)
	sink := &traceTestSink{}
	ctx := WithCompletionTraceSink(context.Background(), sink)

	result, err := CompleteWithTrace(
		ctx,
		o,
		CompletionRequest{Model: "test-model", User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "fallback" || primary.calls != 2 || fallback.calls != 1 {
		t.Fatalf("unexpected fallback result/calls: result=%+v primary=%d fallback=%d", result, primary.calls, fallback.calls)
	}
	if len(sink.events) != 3 {
		t.Fatalf("expected every physical boundary, got %+v", sink.events)
	}
	for index, event := range sink.events {
		if event.ProviderAttempt != index+1 {
			t.Fatalf("non-contiguous physical attempt lineage: %+v", sink.events)
		}
	}
	if sink.events[0].BoundaryKind != CompletionBoundaryProviderError ||
		sink.events[1].BoundaryKind != CompletionBoundaryProviderError ||
		sink.events[2].BoundaryKind != CompletionBoundaryProviderResponse ||
		sink.events[0].Result.Provider != "primary" ||
		sink.events[2].Result.Provider != "fallback" ||
		sink.events[2].Result.ResponseID != "fallback-response" {
		t.Fatalf("unexpected physical boundary provenance: %+v", sink.events)
	}
}

func TestOrchestratorTraceCapturesAllProviderFailures(t *testing.T) {
	cfg := &config.Config{
		LLMProvider: "primary", LLMFallbackProvider: "fallback",
		LLMMaxRetries: 0, LLMModel: "test-model",
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	primary := &fakeProvider{name: "primary", err: errors.New("primary unavailable")}
	fallback := &fakeProvider{name: "fallback", err: errors.New("fallback unavailable")}
	o.RegisterProvider(primary)
	o.RegisterProvider(fallback)
	sink := &traceTestSink{}

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		CompletionRequest{User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err == nil || result != nil {
		t.Fatalf("expected all-provider failure, result=%+v err=%v", result, err)
	}
	if len(sink.events) != 3 || primary.calls != 1 || fallback.calls != 2 {
		t.Fatalf("provider failures were not fully captured: events=%+v calls=%d/%d", sink.events, primary.calls, fallback.calls)
	}
	for _, event := range sink.events {
		if event.BoundaryKind != CompletionBoundaryProviderError || event.ErrorCode != "provider_error" {
			t.Fatalf("unexpected failure boundary: %+v", event)
		}
	}
}

func TestOrchestratorCaptureFailureStopsRetryAndFallback(t *testing.T) {
	cfg := &config.Config{
		LLMProvider: "primary", LLMFallbackProvider: "fallback",
		LLMMaxRetries: 3,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	primary := &fakeProvider{name: "primary", err: errors.New("provider unavailable")}
	fallback := &fakeProvider{name: "fallback", resp: &CompletionResponse{Content: "must not run"}}
	o.RegisterProvider(primary)
	o.RegisterProvider(fallback)
	sink := &traceTestSink{err: errors.New("artifact database unavailable")}

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		CompletionRequest{User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if result != nil || !errors.Is(err, ErrCompletionCapture) {
		t.Fatalf("capture failure did not fail closed: result=%+v err=%v", result, err)
	}
	if primary.calls != 1 || fallback.calls != 0 || len(sink.events) != 1 {
		t.Fatalf("capture failure triggered another provider call: primary=%d fallback=%d events=%d", primary.calls, fallback.calls, len(sink.events))
	}
}

func TestOrchestratorCapturesNilProviderResponseWithoutPanicking(t *testing.T) {
	cfg := &config.Config{LLMProvider: "nil-provider", LLMMaxRetries: 0}
	o := NewOrchestrator(cfg, zap.NewNop())
	provider := &fakeProvider{name: "nil-provider"}
	o.RegisterProvider(provider)
	sink := &traceTestSink{}

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		CompletionRequest{User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err == nil || result != nil {
		t.Fatalf("nil provider response should fail: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].BoundaryKind != CompletionBoundaryProviderError ||
		sink.events[0].ErrorCode != "empty_response" {
		t.Fatalf("nil provider response was not captured: %+v", sink.events)
	}
}

func TestOrchestratorCapturesReturnedSuccessAfterProviderInternalTraceBeforeCaching(t *testing.T) {
	sink := &traceTestSink{}
	responseCache := &traceAwareCache{sink: sink}
	cfg := &config.Config{
		LLMProvider: "custom", LLMMaxRetries: 0,
		LLMCacheTTL: time.Hour, LLMModel: "test-model",
	}
	o := NewOrchestratorWithCache(cfg, responseCache, zap.NewNop())
	o.RegisterProvider(&internalTraceProvider{
		name:     "custom",
		response: &CompletionResponse{Content: `{"ok":true}`, ResponseID: "terminal-response"},
	})

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		CompletionRequest{Model: "test-model", User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "terminal-response" ||
		len(sink.events) != 2 ||
		sink.events[0].BoundaryKind != CompletionBoundaryProviderError ||
		sink.events[1].BoundaryKind != CompletionBoundaryProviderResponse {
		t.Fatalf("terminal success was not captured after internal trace: result=%+v events=%+v", result, sink.events)
	}
	if responseCache.setBeforeTerminalResponse {
		t.Fatal("terminal provider response reached cache before durable capture")
	}
}

func TestOrchestratorCapturesReturnedTerminalErrorAfterDifferentInternalTrace(t *testing.T) {
	sink := &traceTestSink{}
	cfg := &config.Config{LLMProvider: "custom", LLMMaxRetries: 0, LLMModel: "test-model"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&internalTraceProvider{
		name:        "custom",
		terminalErr: errors.New("terminal provider failure"),
	})

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		CompletionRequest{Model: "test-model", User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err == nil || result != nil {
		t.Fatalf("expected terminal provider failure: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 2 ||
		sink.events[0].ErrorMessage != "internal provider attempt failed" ||
		sink.events[1].ErrorMessage != "terminal provider failure" {
		t.Fatalf("terminal error was not the final captured boundary: %+v", sink.events)
	}
}

func TestOrchestratorDistinguishesErrorsThatRedactToTheSameMessage(t *testing.T) {
	sink := &traceTestSink{}
	cfg := &config.Config{LLMProvider: "custom", LLMMaxRetries: 0, LLMModel: "test-model"}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&internalTraceProvider{
		name:        "custom",
		internalErr: errors.New("token:secret-a"),
		terminalErr: errors.New("token:secret-b"),
	})

	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		CompletionRequest{Model: "test-model", User: "collect"},
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err == nil || result != nil {
		t.Fatalf("expected terminal provider failure: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 2 ||
		sink.events[0].ErrorMessage != "[REDACTED]" ||
		sink.events[1].ErrorMessage != "[REDACTED]" {
		t.Fatalf("redaction collision suppressed the final error boundary: %+v", sink.events)
	}
}

func TestOrchestratorRetriesBeforeFailure(t *testing.T) {
	cfg := &config.Config{LLMProvider: "flaky", LLMCallMaxRetries: 2}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{name: "flaky", err: errors.New("transient")}
	o.RegisterProvider(p)

	_, err := o.Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatal("expected error after retries")
	}
	if p.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", p.calls)
	}
}

func TestOrchestratorCacheHits(t *testing.T) {
	cfg := &config.Config{
		LLMProvider:   "fake",
		LLMMaxRetries: 0,
		LLMCacheTTL:   time.Hour,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	p := &fakeProvider{
		name: "fake",
		resp: &CompletionResponse{Content: "cached", InputTokens: 1, OutputTokens: 2},
	}
	o.RegisterProvider(p)

	req := CompletionRequest{Model: "m", User: "hello", JSONMode: true}
	res1, err := o.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1.CacheHit {
		t.Fatal("expected cache miss on first call")
	}

	res2, err := o.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit on second call")
	}
	if res2.Content != "cached" || res2.InputTokens != 1 || res2.OutputTokens != 2 {
		t.Fatalf("cached response mismatch: %+v", res2)
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}

func TestOrchestratorCacheHitTraceRetainsProviderResponseMetadata(t *testing.T) {
	cfg := &config.Config{
		LLMProvider: "fake", LLMMaxRetries: 0, LLMCacheTTL: time.Hour,
		LLMModel: "configured-model",
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	provider := &fakeProvider{
		name: "fake",
		resp: &CompletionResponse{
			Content: "cached", InputTokens: 4, OutputTokens: 2,
			ResponseID: "response-123", FinishReason: "stop",
		},
	}
	o.RegisterProvider(provider)
	request := CompletionRequest{Model: "request-model", User: "hello"}
	if _, err := o.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	sink := &traceTestSink{}
	result, err := CompleteWithTrace(
		WithCompletionTraceSink(context.Background(), sink),
		o,
		request,
		CompletionTraceMetadata{Phase: CompletionPhaseFinal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CacheHit || result.Provider != "fake" || result.Model != "request-model" ||
		result.ResponseID != "response-123" || result.FinishReason != "stop" {
		t.Fatalf("cache provenance was lost: %+v", result)
	}
	if provider.calls != 1 || len(sink.events) != 1 ||
		sink.events[0].BoundaryKind != CompletionBoundaryCacheHit ||
		sink.events[0].ProviderAttempt != 0 {
		t.Fatalf("cache hit was duplicated or counted as a provider attempt: calls=%d events=%+v", provider.calls, sink.events)
	}
}

func TestOrchestratorDoesNotCacheContentThatRequiresRedaction(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0, LLMCacheTTL: time.Hour}
	o := NewOrchestrator(cfg, zap.NewNop())
	provider := &fakeProvider{
		name: "fake",
		resp: &CompletionResponse{Content: `{"apiToken":"provider-secret"}`},
	}
	o.RegisterProvider(provider)
	request := CompletionRequest{User: "hello"}
	for iteration := 0; iteration < 2; iteration++ {
		result, err := o.Complete(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if result.CacheHit {
			t.Fatal("sensitive provider output was returned from cache")
		}
	}
	if provider.calls != 2 {
		t.Fatalf("sensitive provider output was cached after defensive scanning: calls=%d", provider.calls)
	}
}

func TestOrchestratorRedactsProviderErrorsBeforeLoggingOrReturning(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0}
	o := NewOrchestrator(cfg, logger)
	o.RegisterProvider(&fakeProvider{
		name: "fake",
		err:  errors.New(`{"error":"unavailable","apiToken":"provider-secret"}`),
	})

	_, err := o.Complete(context.Background(), CompletionRequest{User: "hello"})
	if err == nil || strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("returned provider error was not redacted: %v", err)
	}
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message, "provider-secret") ||
			strings.Contains(entry.ContextMap()["error"].(string), "provider-secret") {
			t.Fatalf("provider secret reached logs: %+v", entry)
		}
	}
}

func TestOrchestratorCacheReturnsDeepCopy(t *testing.T) {
	cfg := &config.Config{
		LLMProvider:   "fake",
		LLMMaxRetries: 0,
		LLMCacheTTL:   time.Hour,
	}
	o := NewOrchestrator(cfg, zap.NewNop())
	o.RegisterProvider(&fakeProvider{
		name: "fake",
		resp: &CompletionResponse{Content: "cached"},
	})

	req := CompletionRequest{Model: "m", User: "hello"}
	res1, err := o.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := o.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	res1.Content = "mutated"
	if res2.Content != "cached" {
		t.Fatal("cache hit returned shared pointer")
	}
}

func TestOrchestratorRecordsMetricsOnSuccess(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0, LLMModel: "test-model"}
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetMetrics(metrics)
	o.RegisterProvider(&fakeProvider{
		name: "fake",
		resp: &CompletionResponse{Content: "ok", InputTokens: 3, OutputTokens: 7},
	})

	_, err := o.Complete(context.Background(), CompletionRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := testutil.ToFloat64(metrics.Requests.WithLabelValues("fake", "test-model")); got != 1 {
		t.Fatalf("expected 1 request metric, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.Tokens.WithLabelValues("fake", "test-model", "input")); got != 3 {
		t.Fatalf("expected 3 input tokens, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.Tokens.WithLabelValues("fake", "test-model", "output")); got != 7 {
		t.Fatalf("expected 7 output tokens, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.CacheHits.WithLabelValues("fake", "test-model")); got != 0 {
		t.Fatalf("expected no cache hits, got %v", got)
	}
}

func TestOrchestratorRecordsMetricsOnCacheHit(t *testing.T) {
	cfg := &config.Config{
		LLMProvider:   "fake",
		LLMMaxRetries: 0,
		LLMCacheTTL:   time.Hour,
		LLMModel:      "test-model",
	}
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetMetrics(metrics)
	o.RegisterProvider(&fakeProvider{
		name: "fake",
		resp: &CompletionResponse{Content: "cached", InputTokens: 1, OutputTokens: 2},
	})

	req := CompletionRequest{Model: "m", User: "hello", JSONMode: true}
	if _, err := o.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	if got := testutil.ToFloat64(metrics.CacheHits.WithLabelValues("fake", "m")); got != 1 {
		t.Fatalf("expected 1 cache hit metric, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.Requests.WithLabelValues("fake", "m")); got != 2 {
		t.Fatalf("expected 2 request metrics (cache miss + cache hit), got %v", got)
	}
}

func TestOrchestratorRecordsMetricsOnError(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0, LLMModel: "test-model"}
	metrics := NewMetricsWithRegistry(prometheus.NewRegistry())
	o := NewOrchestrator(cfg, zap.NewNop())
	o.SetMetrics(metrics)
	o.RegisterProvider(&fakeProvider{name: "fake", err: errors.New("boom")})

	_, err := o.Complete(context.Background(), CompletionRequest{})
	if err == nil {
		t.Fatal("expected error")
	}

	if got := testutil.ToFloat64(metrics.Requests.WithLabelValues("fake", "test-model")); got != 1 {
		t.Fatalf("expected 1 request metric, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.Errors.WithLabelValues("fake", "test-model", "provider_error")); got != 1 {
		t.Fatalf("expected 1 provider_error metric, got %v", got)
	}
}

func TestOrchestratorWithNilCacheUsesMemoryCache(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0, LLMCacheTTL: time.Hour}
	o := NewOrchestratorWithCache(cfg, nil, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{Content: "ok"}})

	res1, err := o.Complete(context.Background(), CompletionRequest{User: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res1.CacheHit {
		t.Fatal("expected cache miss")
	}

	res2, err := o.Complete(context.Background(), CompletionRequest{User: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit")
	}
}

type errorCache struct{}

func (errorCache) Get(ctx context.Context, key string) (*cache.Entry, bool, error) {
	return nil, false, errors.New("cache get failed")
}

func (errorCache) Set(ctx context.Context, key string, entry *cache.Entry, ttl time.Duration) error {
	return errors.New("cache set failed")
}

func TestOrchestratorCacheGetErrorLogsAndMisses(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0, LLMCacheTTL: time.Hour}
	o := NewOrchestratorWithCache(cfg, errorCache{}, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{Content: "ok"}})

	res, err := o.Complete(context.Background(), CompletionRequest{User: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHit {
		t.Fatal("expected cache miss after cache error")
	}
}

func TestOrchestratorPeekCacheHit(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0, LLMModel: "m", LLMCacheTTL: time.Hour}
	o := NewOrchestratorWithCache(cfg, nil, zap.NewNop())
	o.RegisterProvider(&fakeProvider{name: "fake", resp: &CompletionResponse{Content: "cached", InputTokens: 2, OutputTokens: 3}})

	req := CompletionRequest{User: "hello"}
	if _, err := o.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	cached, ok := o.PeekCache(context.Background(), req)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if cached.Content != "cached" || cached.InputTokens != 2 || cached.OutputTokens != 3 {
		t.Fatalf("unexpected cached result: %+v", cached)
	}
}

func TestOrchestratorPeekCacheError(t *testing.T) {
	cfg := &config.Config{LLMProvider: "fake", LLMMaxRetries: 0}
	o := NewOrchestratorWithCache(cfg, errorCache{}, zap.NewNop())

	_, ok := o.PeekCache(context.Background(), CompletionRequest{User: "hello"})
	if ok {
		t.Fatal("expected cache miss after cache error")
	}
}

func TestMemoryCacheSetIgnoresZeroTTL(t *testing.T) {
	c := newMemoryCache()
	if err := c.Set(context.Background(), "k", &cache.Entry{Content: "x"}, 0); err != nil {
		t.Fatal(err)
	}
	_, hit, _ := c.Get(context.Background(), "k")
	if hit {
		t.Fatal("expected zero-ttl entry to be ignored")
	}
}

func TestMemoryCacheSetIgnoresNilEntry(t *testing.T) {
	c := newMemoryCache()
	if err := c.Set(context.Background(), "k", nil, time.Hour); err != nil {
		t.Fatal(err)
	}
	_, hit, _ := c.Get(context.Background(), "k")
	if hit {
		t.Fatal("expected nil entry to be ignored")
	}
}

func TestOrchestratorSQLiteCacheHits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "orch-cache-test.db")
	s, err := store.NewWithConfig(
		&config.Config{SQLiteJournalMode: "WAL"},
		dbPath,
		"orchestrator-cache-encryption-key-for-tests",
	)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	c, err := cache.NewSQLiteCache(s)
	if err != nil {
		t.Fatalf("create sqlite cache: %v", err)
	}

	cfg := &config.Config{
		LLMProvider:   "fake",
		LLMMaxRetries: 0,
		LLMCacheTTL:   time.Hour,
	}
	o := NewOrchestratorWithCache(cfg, c, zap.NewNop())
	p := &fakeProvider{
		name: "fake",
		resp: &CompletionResponse{Content: "cached", InputTokens: 1, OutputTokens: 2},
	}
	o.RegisterProvider(p)

	req := CompletionRequest{Model: "m", User: "hello", JSONMode: true}
	res1, err := o.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1.CacheHit {
		t.Fatal("expected cache miss on first call")
	}

	res2, err := o.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit on second call")
	}
	if res2.Content != "cached" || res2.InputTokens != 1 || res2.OutputTokens != 2 {
		t.Fatalf("cached response mismatch: %+v", res2)
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}
