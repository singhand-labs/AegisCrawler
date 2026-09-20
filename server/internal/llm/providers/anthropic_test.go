package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

func anthropicResponseBodyOfSize(t *testing.T, size int) string {
	t.Helper()
	const prefix = `{"content":[{"type":"text","text":"`
	const suffix = `"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	contentBytes := size - len(prefix) - len(suffix)
	if contentBytes < 0 {
		t.Fatalf("Anthropic response size %d is below envelope overhead", size)
	}
	return prefix + strings.Repeat("x", contentBytes) + suffix
}

type testAnthropicRequest struct {
	Model       string  `json:"model"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
}

func decodeAnthropicRequest(t *testing.T, r *http.Request) testAnthropicRequest {
	t.Helper()
	var body testAnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func TestAnthropicProviderComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing api key header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"msg-test",
			"content":[{"type":"text","text":"{\"suggestions\":[]}"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.5)
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user", Temperature: 0.5,
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("empty content")
	}
	if resp.InputTokens != 10 || resp.OutputTokens != 5 {
		t.Fatalf("unexpected tokens: %d/%d", resp.InputTokens, resp.OutputTokens)
	}
	if !resp.UsageMetadata.InputTokensPresent || !resp.UsageMetadata.OutputTokensPresent {
		t.Fatalf("provider lost usage presence: %+v", resp.UsageMetadata)
	}
	if resp.ResponseID != "msg-test" || resp.FinishReason != "end_turn" {
		t.Fatalf("provider response metadata was not preserved: %+v", resp)
	}
}

func TestAnthropicProviderSendsExactOutputCapAndBoundsExactWireBody(t *testing.T) {
	const maxOutputTokens = 73
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var body testAnthropicRequest
		if err := json.Unmarshal(receivedBody, &body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.MaxTokens != maxOutputTokens {
			t.Fatalf("max_tokens = %d, want %d", body.MaxTokens, maxOutputTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":2,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	provider := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-test", 5*time.Second, 0)
	request := llm.CompletionRequest{
		Model: "claude-test", System: "system", User: "user",
		MaxOutputTokens: maxOutputTokens,
	}
	bound, err := provider.InputTokenUpperBound(request)
	if err != nil {
		t.Fatalf("InputTokenUpperBound: %v", err)
	}
	if _, err := provider.Complete(context.Background(), request); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if bound != len(receivedBody) {
		t.Fatalf("input bound = %d, exact wire body bytes = %d", bound, len(receivedBody))
	}
}

func TestAnthropicProviderReportsCachedAndUnknownUsageCategories(t *testing.T) {
	tests := []struct {
		name              string
		usage             string
		wantInput         int
		wantCached        int
		wantUnknown       string
		wantCachedPresent bool
	}{
		{
			name:              "cache read is normalized",
			usage:             `"input_tokens":7,"cache_read_input_tokens":3,"output_tokens":2`,
			wantInput:         10,
			wantCached:        3,
			wantCachedPresent: true,
		},
		{
			name:        "cache creation is an unsupported priced category",
			usage:       `"input_tokens":7,"cache_creation_input_tokens":4,"output_tokens":2`,
			wantInput:   7,
			wantUnknown: "cache_creation_input_tokens",
		},
		{
			name:        "future category is not silently dropped",
			usage:       `"input_tokens":7,"output_tokens":2,"future_billable_tokens":9`,
			wantInput:   7,
			wantUnknown: "future_billable_tokens",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"content":[{"type":"text","text":"ok"}],
					"usage":{`+tc.usage+`}
				}`)
			}))
			defer server.Close()

			provider := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-test", 5*time.Second, 0)
			response, err := provider.Complete(context.Background(), llm.CompletionRequest{User: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if response.InputTokens != tc.wantInput || response.CachedInputTokens != tc.wantCached {
				t.Fatalf("normalized usage = %+v", response)
			}
			if response.UsageMetadata.CachedInputTokensPresent != tc.wantCachedPresent {
				t.Fatalf("cached presence = %v, want %v", response.UsageMetadata.CachedInputTokensPresent, tc.wantCachedPresent)
			}
			if tc.wantUnknown == "" {
				if len(response.UsageMetadata.UnknownCategories) != 0 {
					t.Fatalf("unexpected unknown categories: %v", response.UsageMetadata.UnknownCategories)
				}
			} else if len(response.UsageMetadata.UnknownCategories) != 1 ||
				response.UsageMetadata.UnknownCategories[0] != tc.wantUnknown {
				t.Fatalf("unknown categories = %v, want %q", response.UsageMetadata.UnknownCategories, tc.wantUnknown)
			}
		})
	}
}

func TestAnthropicProviderCompleteRetriesOnTemperatureError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body := decodeAnthropicRequest(t, r)
		if body.Temperature != 1.0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"temperature must be 1 for this model"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"content":[{"type":"text","text":"{\"suggestions\":[]}"}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3-opus", 5*time.Second, 0.5)
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3-opus", System: "sys", User: "user", Temperature: 0.5,
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if resp.Content == "" {
		t.Fatal("empty content")
	}
}

func TestAnthropicProviderTraceCapturesPhysicalTemperatureRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body := decodeAnthropicRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		if body.Temperature != 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"temperature must be 1 for this model"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"retry-message",
			"content":[{"type":"text","text":"{\"ok\":true}"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()
	provider := NewAnthropicProvider("anthropic", "test-key", server.URL, "reasoning-model", 5*time.Second, 0.5)
	sink := &providerTraceSink{}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		llm.CompletionRequest{Model: "reasoning-model", User: "collect", Temperature: 0.5},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || result.ResponseID != "retry-message" || len(sink.events) != 2 {
		t.Fatalf("physical anthropic retry lineage missing: requests=%d result=%+v events=%+v", requests, result, sink.events)
	}
	if sink.events[0].BoundaryKind != llm.CompletionBoundaryProviderError ||
		sink.events[0].ProviderAttempt != 1 ||
		!sink.events[0].OriginalBytesExact ||
		sink.events[1].BoundaryKind != llm.CompletionBoundaryProviderResponse ||
		sink.events[1].ProviderAttempt != 2 ||
		!sink.events[1].OriginalBytesExact {
		t.Fatalf("unexpected anthropic physical boundaries: %+v", sink.events)
	}
}

func TestAnthropicProviderAcceptsResponseAtHardBodyLimit(t *testing.T) {
	body := anthropicResponseBodyOfSize(t, maxProviderResponseBodyBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	provider := NewAnthropicProvider("anthropic", "test-key", server.URL, "test-model", 5*time.Second, 0.2, false)
	response, err := provider.Complete(context.Background(), llm.CompletionRequest{Model: "test-model", User: "collect"})
	if err != nil {
		t.Fatalf("exact-boundary response was rejected: %v", err)
	}
	const prefix = `{"content":[{"type":"text","text":"`
	const suffix = `"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	if len(response.Content) != maxProviderResponseBodyBytes-len(prefix)-len(suffix) {
		t.Fatalf("unexpected exact-boundary content length: %d", len(response.Content))
	}
}

func TestAnthropicProviderRejectsAndCapturesResponseAboveHardBodyLimit(t *testing.T) {
	body := anthropicResponseBodyOfSize(t, maxProviderResponseBodyBytes+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	provider := NewAnthropicProvider("anthropic", "test-key", server.URL, "test-model", 5*time.Second, 0.2, false)
	sink := &providerTraceSink{}
	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		llm.CompletionRequest{Model: "test-model", User: "collect"},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if result != nil || err == nil {
		t.Fatalf("oversized successful response was accepted: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("oversized boundary was not captured once: %+v", sink.events)
	}
	event := sink.events[0]
	if event.BoundaryKind != llm.CompletionBoundaryProviderError ||
		event.ErrorCode != "response_too_large" ||
		event.OriginalBytes != maxProviderResponseBodyBytes+1 ||
		event.OriginalBytesExact ||
		!event.ContentTruncated ||
		!strings.Contains(event.Result.Content, "[TRUNCATED]") {
		t.Fatalf("oversized response provenance is incomplete: %+v", event)
	}
}

func TestAnthropicProviderCompleteCanDisableTemperatureRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"temperature must be 1 for this model"}}`))
	}))
	defer server.Close()
	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0, false)
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{Temperature: 0}); err == nil {
		t.Fatal("expected temperature rejection")
	}
	if requests != 1 {
		t.Fatalf("strict adapter made %d HTTP requests, want exactly 1", requests)
	}
}

func TestAnthropicProviderAtMostOncePolicyOverridesCompatibilityRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"temperature must be 1 for this model"}}`))
	}))
	defer server.Close()
	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0, true)
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Temperature: 0, ExecutionPolicy: llm.CompletionExecutionAtMostOnce,
	}); err == nil {
		t.Fatal("expected temperature rejection")
	}
	if requests != 1 {
		t.Fatalf("at-most-once policy made %d physical HTTP requests", requests)
	}
}

func TestAnthropicProviderAtMostOnceDoesNotReplayPostRedirect(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/messages" {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{}"}]}`))
	}))
	defer server.Close()
	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "model", 5*time.Second, 0, true)
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		ExecutionPolicy: llm.CompletionExecutionAtMostOnce,
	}); err == nil {
		t.Fatal("expected redirect to be terminal")
	}
	if requests != 1 {
		t.Fatalf("at-most-once request followed a replayable redirect: requests=%d", requests)
	}
}

func TestAnthropicProviderCompleteReturnsNonOKError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.5)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user", Temperature: 0.5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error, got %v", err)
	}
}

func TestAnthropicProviderCompleteReturnsEmptyContentError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"content":[],"usage":{"input_tokens":10,"output_tokens":0}}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.5)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user", Temperature: 0.5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty content") {
		t.Fatalf("expected empty content error, got %v", err)
	}
}

func TestAnthropicProviderCompleteDoesNotRetryOnNonTemperatureError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"context length exceeded"}}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.5)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user", Temperature: 0.5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestAnthropicProviderCompleteReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"},"content":[],"usage":{}}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.5)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user", Temperature: 0.5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "rate limit exceeded") ||
		!strings.Contains(err.Error(), "provider_error") {
		t.Fatalf("expected classified API error without provider detail, got %v", err)
	}
}

func TestAnthropicProviderCompleteUsesDefaultTemperature(t *testing.T) {
	var receivedTemperature float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeAnthropicRequest(t, r)
		receivedTemperature = body.Temperature
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.7)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user",
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if receivedTemperature != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", receivedTemperature)
	}
}

func TestAnthropicProviderCompleteReturnsDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	p := NewAnthropicProvider("anthropic", "test-key", server.URL, "claude-3", 5*time.Second, 0.5)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "claude-3", System: "sys", User: "user", Temperature: 0.5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
