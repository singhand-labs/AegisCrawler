package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"go.uber.org/zap"
)

type providerTraceSink struct {
	events []llm.CompletionTraceEvent
	err    error
}

type partialErrorReadCloser struct {
	content string
	read    bool
}

func (r *partialErrorReadCloser) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	return copy(buffer, r.content), io.ErrUnexpectedEOF
}

func (r *partialErrorReadCloser) Close() error { return nil }

type providerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn providerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (s *providerTraceSink) CaptureCompletion(_ context.Context, event llm.CompletionTraceEvent) error {
	s.events = append(s.events, event)
	return s.err
}

type tracedProviderCompleter struct {
	provider llm.Provider
}

func (c tracedProviderCompleter) Complete(ctx context.Context, request llm.CompletionRequest) (*llm.CompletionResult, error) {
	response, err := c.provider.Complete(ctx, request)
	if err != nil {
		return nil, err
	}
	return &llm.CompletionResult{
		CompletionResponse: response,
		Provider:           c.provider.Name(),
		Model:              request.Model,
	}, nil
}

type successfulFallbackProvider struct {
	calls int
}

func (p *successfulFallbackProvider) Name() string { return "fallback" }

func (p *successfulFallbackProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls++
	return &llm.CompletionResponse{Content: `{"fallback":true}`}, nil
}

func openAIResponseBodyOfSize(t *testing.T, size int) string {
	t.Helper()
	const prefix = `{"choices":[{"message":{"content":"`
	const suffix = `"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	contentBytes := size - len(prefix) - len(suffix)
	if contentBytes < 0 {
		t.Fatalf("OpenAI response size %d is below envelope overhead", size)
	}
	return prefix + strings.Repeat("x", contentBytes) + suffix
}

func TestOpenAIProviderComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		body := decodeRequestBody(t, r)
		if body.MaxTokens != nil {
			t.Fatalf("uncapped request emitted max_tokens=%d", *body.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"chatcmpl-test",
			"choices":[{"message":{"content":"{\"suggestions\":[]}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.2)
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user", Temperature: 0.2, JSONMode: true,
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
	if resp.ResponseID != "chatcmpl-test" || resp.FinishReason != "stop" {
		t.Fatalf("provider response metadata was not preserved: %+v", resp)
	}
}

func TestOpenAIProviderBoundsExactWireBody(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2,"completion_tokens":1}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0)
	request := llm.CompletionRequest{
		Model: "test-model", System: "system", User: "user",
		JSONMode: true, MaxOutputTokens: 31,
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

func TestOpenAIProviderDoesNotSilentlyDropUnknownUsageCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"future_billable_tokens":7
			}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0)
	response, err := provider.Complete(context.Background(), llm.CompletionRequest{User: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.UsageMetadata.UnknownCategories) != 1 ||
		response.UsageMetadata.UnknownCategories[0] != "future_billable_tokens" {
		t.Fatalf("unknown categories = %v", response.UsageMetadata.UnknownCategories)
	}
}

func TestOpenAIProviderValidatesRedundantUsageFields(t *testing.T) {
	for _, test := range []struct {
		name              string
		usage             string
		wantContradictory bool
		wantUnknown       []string
	}{
		{
			name: "consistent total and cache partitions",
			usage: `"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"prompt_cache_hit_tokens":3,
				"prompt_cache_miss_tokens":7,
				"prompt_tokens_details":{"cached_tokens":3,"audio_tokens":0},
				"completion_tokens_details":{
					"reasoning_tokens":2,
					"audio_tokens":0,
					"accepted_prediction_tokens":0,
					"rejected_prediction_tokens":0
				}`,
		},
		{
			name:              "total contradicts components",
			usage:             `"prompt_tokens":10,"completion_tokens":5,"total_tokens":100`,
			wantContradictory: true,
		},
		{
			name: "cache partitions contradict prompt total",
			usage: `"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"prompt_cache_hit_tokens":3,
				"prompt_cache_miss_tokens":8`,
			wantContradictory: true,
		},
		{
			name: "cache representations disagree",
			usage: `"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"prompt_cache_hit_tokens":3,
				"prompt_cache_miss_tokens":7,
				"prompt_tokens_details":{"cached_tokens":4}`,
			wantContradictory: true,
		},
		{
			name:              "negative redundant field",
			usage:             `"prompt_tokens":10,"completion_tokens":5,"total_tokens":-1`,
			wantContradictory: true,
		},
		{
			name:              "negative primary usage",
			usage:             `"prompt_tokens":-10,"completion_tokens":-5,"total_tokens":-15`,
			wantContradictory: true,
		},
		{
			name: "reasoning partition exceeds completion total",
			usage: `"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"completion_tokens_details":{"reasoning_tokens":6}`,
			wantContradictory: true,
		},
		{
			name: "unknown nested usage category",
			usage: `"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"completion_tokens_details":{"future_billable_tokens":1}`,
			wantUnknown: []string{"completion_tokens_details.future_billable_tokens"},
		},
		{
			name: "known but unpriced nonzero usage categories",
			usage: `"prompt_tokens":10,
				"completion_tokens":5,
				"total_tokens":15,
				"prompt_tokens_details":{"audio_tokens":1},
				"completion_tokens_details":{
					"audio_tokens":1,
					"accepted_prediction_tokens":1
				}`,
			wantUnknown: []string{
				"completion_tokens_details.accepted_prediction_tokens",
				"completion_tokens_details.audio_tokens",
				"prompt_tokens_details.audio_tokens",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
					"usage":{` + test.usage + `}
				}`))
			}))
			defer server.Close()

			provider := NewOpenAIProvider(
				"openai",
				"test-key",
				server.URL,
				"test-model",
				5*time.Second,
				0,
			)
			response, err := provider.Complete(
				context.Background(),
				llm.CompletionRequest{User: "test"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if response.UsageMetadata.Contradictory != test.wantContradictory {
				t.Fatalf(
					"contradictory = %v, want %v; metadata=%+v",
					response.UsageMetadata.Contradictory,
					test.wantContradictory,
					response.UsageMetadata,
				)
			}
			if response.InputTokens < 0 || response.OutputTokens < 0 {
				t.Fatalf(
					"provider boundary exposed negative usage: input=%d output=%d",
					response.InputTokens,
					response.OutputTokens,
				)
			}
			if !reflect.DeepEqual(
				append([]string(nil), response.UsageMetadata.UnknownCategories...),
				test.wantUnknown,
			) {
				t.Fatalf(
					"unknown categories = %v, want %v",
					response.UsageMetadata.UnknownCategories,
					test.wantUnknown,
				)
			}
		})
	}
}

func TestOpenAIProviderCompleteSendsPositiveMaxOutputTokens(t *testing.T) {
	const maxOutputTokens = 2048
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequestBody(t, r)
		if body.MaxTokens == nil || *body.MaxTokens != maxOutputTokens {
			t.Fatalf("positive output cap was not sent exactly: %+v", body.MaxTokens)
		}
		if body.MaxCompletionTokens != nil {
			t.Fatalf("default output cap also sent max_completion_tokens: %d", *body.MaxCompletionTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0)
	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{
		Model: "test-model", User: "collect", JSONMode: true, MaxOutputTokens: maxOutputTokens,
	}); err != nil {
		t.Fatalf("complete with positive output cap failed: %v", err)
	}
}

func TestOpenAIProviderCompleteSendsAliyunMaxCompletionTokensWithTolerance(t *testing.T) {
	const configuredMaxOutputTokens = 2048
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequestBody(t, r)
		if body.MaxTokens != nil {
			t.Fatalf("max_tokens must be omitted for max_completion_tokens dialect: %d", *body.MaxTokens)
		}
		wantWireCap := configuredMaxOutputTokens - config.AliyunMaxCompletionTokensTolerance
		if body.MaxCompletionTokens == nil || *body.MaxCompletionTokens != wantWireCap {
			t.Fatalf("max_completion_tokens = %v, want %d", body.MaxCompletionTokens, wantWireCap)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("aliyun", "test-key", server.URL, "qwen-test", 5*time.Second, 0)
	provider.outputCapDialect = config.OutputCapDialectMaxCompletionTokens
	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{
		Model: "qwen-test", User: "collect", JSONMode: true, MaxOutputTokens: configuredMaxOutputTokens,
	}); err != nil {
		t.Fatalf("complete with Aliyun completion cap failed: %v", err)
	}
}

func TestOpenAIProviderRejectsInvalidMaxCompletionTokenCapBeforeDispatch(t *testing.T) {
	dispatched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("aliyun", "test-key", server.URL, "qwen-test", 5*time.Second, 0)
	provider.outputCapDialect = config.OutputCapDialectMaxCompletionTokens
	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{
		MaxOutputTokens: config.AliyunMaxCompletionTokensTolerance,
	}); err == nil {
		t.Fatal("invalid max_completion_tokens cap was accepted")
	}
	if dispatched {
		t.Fatal("invalid max_completion_tokens cap crossed the HTTP boundary")
	}
}

func TestOpenAIProviderRejectsUnknownOutputCapDialect(t *testing.T) {
	provider := NewOpenAIProvider("openai", "test-key", "", "test-model", 5*time.Second, 0)
	provider.outputCapDialect = config.OutputCapDialect("provider_magic")
	if _, _, err := provider.requestPayload(llm.CompletionRequest{MaxOutputTokens: 100}); err == nil {
		t.Fatal("unknown output-cap dialect was accepted")
	}
}

func TestOpenAIProviderCompleteRetriesOnTemperatureError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body := decodeRequestBody(t, r)
		if body.Temperature != 1.0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"invalid temperature: only 1 is allowed for this model","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"suggestions\":[]}"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "kimi-for-coding-highspeed", 5*time.Second, 0.2)
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "kimi-for-coding-highspeed", System: "sys", User: "user", Temperature: 0.2, JSONMode: true,
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

func TestOpenAIProviderTraceCapturesPhysicalTemperatureRetryAndRedactsErrorBody(t *testing.T) {
	requests := 0
	const rawErrorBody = `{"error":{"message":"invalid temperature: only 1 is allowed","apiToken":"provider-secret"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body := decodeRequestBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		if body.Temperature != 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(rawErrorBody))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"retry-response",
			"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`))
	}))
	defer server.Close()
	provider := NewOpenAIProvider("openai", "test-key", server.URL, "reasoning-model", 5*time.Second, 0.2)
	sink := &providerTraceSink{}
	request := llm.CompletionRequest{Model: "reasoning-model", User: "collect", Temperature: 0.2}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		request,
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || result.ResponseID != "retry-response" || len(sink.events) != 2 {
		t.Fatalf("physical retry lineage missing: requests=%d result=%+v events=%+v", requests, result, sink.events)
	}
	first, second := sink.events[0], sink.events[1]
	if first.BoundaryKind != llm.CompletionBoundaryProviderError ||
		first.ProviderAttempt != 1 ||
		first.HTTPStatus != http.StatusBadRequest ||
		first.ErrorCode != "http_error" ||
		first.OriginalBytes != len(rawErrorBody) ||
		!first.OriginalBytesExact ||
		!first.ContentRedacted ||
		strings.Contains(first.Result.Content, "provider-secret") ||
		!strings.Contains(first.Result.Content, "[REDACTED]") {
		t.Fatalf("unsafe provider error boundary: %+v", first)
	}
	if second.BoundaryKind != llm.CompletionBoundaryProviderResponse ||
		second.ProviderAttempt != 2 ||
		second.HTTPStatus != http.StatusOK ||
		!second.OriginalBytesExact ||
		second.Result.ResponseID != "retry-response" {
		t.Fatalf("unexpected successful retry boundary: %+v", second)
	}
}

func TestOpenAIProviderCaptureFailureStopsTemperatureRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid temperature: only 1 is allowed"}}`))
	}))
	defer server.Close()
	provider := NewOpenAIProvider("openai", "test-key", server.URL, "reasoning-model", 5*time.Second, 0.2)
	sink := &providerTraceSink{err: errors.New("artifact store unavailable")}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		llm.CompletionRequest{Model: "reasoning-model", User: "collect", Temperature: 0.2},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if result != nil || !errors.Is(err, llm.ErrCompletionCapture) {
		t.Fatalf("capture failure did not fail closed: result=%+v err=%v", result, err)
	}
	if requests != 1 || len(sink.events) != 1 {
		t.Fatalf("capture failure triggered temperature retry: requests=%d events=%d", requests, len(sink.events))
	}
}

func TestOpenAIProviderCapturesPartialBodyWhenResponseReadFails(t *testing.T) {
	partialBody := `{"apiToken":"partial-provider-secret"` + string([]byte{0xff})
	provider := NewOpenAIProvider("openai", "test-key", "https://provider.invalid", "test-model", 5*time.Second, 0.2, false)
	provider.client = &http.Client{Transport: providerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &partialErrorReadCloser{content: partialBody},
			Header:     make(http.Header),
		}, nil
	})}
	sink := &providerTraceSink{}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		llm.CompletionRequest{Model: "test-model", User: "collect"},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if result != nil || err == nil {
		t.Fatalf("partial response read should fail: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].ErrorCode != "response_read_error" ||
		sink.events[0].OriginalBytes != len(partialBody) ||
		sink.events[0].OriginalBytesExact ||
		!sink.events[0].ContentTruncated ||
		!sink.events[0].ContentRedacted ||
		!utf8.ValidString(sink.events[0].Result.Content) ||
		strings.Contains(sink.events[0].Result.Content, "partial-provider-secret") {
		t.Fatalf("partial response body was not safely captured: %+v", sink.events)
	}
}

func TestOpenAIProviderCarriesTransportErrorRedactionProvenance(t *testing.T) {
	provider := NewOpenAIProvider("openai", "test-key", "https://provider.invalid", "test-model", 5*time.Second, 0.2, false)
	provider.client = &http.Client{Transport: providerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("token:transport-secret")
	})}
	sink := &providerTraceSink{}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		llm.CompletionRequest{Model: "test-model", User: "collect"},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if result != nil || err == nil || strings.Contains(err.Error(), "transport-secret") {
		t.Fatalf("transport error was not safely returned: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].Result.Content != "" ||
		!sink.events[0].ErrorRedacted ||
		strings.Contains(sink.events[0].ErrorMessage, "transport-secret") {
		t.Fatalf("transport error redaction provenance was lost: %+v", sink.events)
	}
}

func TestOpenAIProviderMarksBoundedErrorBodyAsTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", maxProviderErrorBodyBytes+1024)))
	}))
	defer server.Close()
	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0.2, false)
	sink := &providerTraceSink{}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		llm.CompletionRequest{Model: "test-model", User: "collect"},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if result != nil || err == nil {
		t.Fatalf("oversized provider error should fail: result=%+v err=%v", result, err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].OriginalBytes != maxProviderErrorBodyBytes+1 ||
		sink.events[0].OriginalBytesExact ||
		!sink.events[0].ContentTruncated ||
		!strings.Contains(sink.events[0].Result.Content, "[TRUNCATED]") {
		t.Fatalf("provider error truncation provenance was lost: %+v", sink.events)
	}
}

func TestOpenAIProviderAcceptsResponseAtHardBodyLimit(t *testing.T) {
	body := openAIResponseBodyOfSize(t, maxProviderResponseBodyBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0.2, false)
	response, err := provider.Complete(context.Background(), llm.CompletionRequest{Model: "test-model", User: "collect"})
	if err != nil {
		t.Fatalf("exact-boundary response was rejected: %v", err)
	}
	const prefix = `{"choices":[{"message":{"content":"`
	const suffix = `"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	if len(response.Content) != maxProviderResponseBodyBytes-len(prefix)-len(suffix) {
		t.Fatalf("unexpected exact-boundary content length: %d", len(response.Content))
	}
}

func TestOpenAIProviderRejectsAndCapturesResponseAboveHardBodyLimit(t *testing.T) {
	body := openAIResponseBodyOfSize(t, maxProviderResponseBodyBytes+1)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	for _, test := range []struct {
		name        string
		sinkErr     error
		wantCapture bool
	}{
		{name: "captured", wantCapture: true},
		{name: "capture failure", sinkErr: errors.New("artifact store unavailable"), wantCapture: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0.2, false)
			sink := &providerTraceSink{err: test.sinkErr}
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
			if test.wantCapture {
				event := sink.events[0]
				if event.BoundaryKind != llm.CompletionBoundaryProviderError ||
					event.ErrorCode != "response_too_large" ||
					event.OriginalBytes != maxProviderResponseBodyBytes+1 ||
					event.OriginalBytesExact ||
					!event.ContentTruncated ||
					!strings.Contains(event.Result.Content, "[TRUNCATED]") {
					t.Fatalf("oversized response provenance is incomplete: %+v", event)
				}
			} else if !errors.Is(err, llm.ErrCompletionCapture) {
				t.Fatalf("capture failure did not withhold oversized response: %v", err)
			}
		})
	}
	if requests != 2 {
		t.Fatalf("unexpected oversized request count: %d", requests)
	}
}

func TestOpenAIProviderCapturesOversizedResponseBeforeFallback(t *testing.T) {
	body := openAIResponseBodyOfSize(t, maxProviderResponseBodyBytes+1)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	cfg := &config.Config{
		LLMProvider:         "primary",
		LLMFallbackProvider: "fallback",
		LLMMaxRetries:       0,
		LLMModel:            "test-model",
	}
	orchestrator := llm.NewOrchestrator(cfg, zap.NewNop())
	orchestrator.RegisterProvider(
		NewOpenAIProvider("primary", "test-key", server.URL, "test-model", 5*time.Second, 0.2, false),
	)
	fallback := &successfulFallbackProvider{}
	orchestrator.RegisterProvider(fallback)
	sink := &providerTraceSink{}

	result, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		orchestrator,
		llm.CompletionRequest{Model: "test-model", User: "collect"},
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "fallback" || result.Content != `{"fallback":true}` ||
		requests != 1 || fallback.calls != 1 {
		t.Fatalf(
			"fallback did not follow one bounded primary failure: result=%+v primary=%d fallback=%d",
			result,
			requests,
			fallback.calls,
		)
	}
	if len(sink.events) != 2 {
		t.Fatalf("oversized failure and fallback response were not both captured: %+v", sink.events)
	}
	first, second := sink.events[0], sink.events[1]
	if first.ProviderAttempt != 1 ||
		first.BoundaryKind != llm.CompletionBoundaryProviderError ||
		first.ErrorCode != "response_too_large" ||
		first.OriginalBytesExact ||
		!first.ContentTruncated ||
		second.ProviderAttempt != 2 ||
		second.BoundaryKind != llm.CompletionBoundaryProviderResponse ||
		!second.OriginalBytesExact ||
		second.Result.Provider != "fallback" {
		t.Fatalf("bounded failure was not durably ordered before fallback: %+v", sink.events)
	}
}

func TestOpenAIProviderCompleteCanDisableTemperatureRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid temperature: only 1 is allowed"}}`))
	}))
	defer server.Close()
	p := NewOpenAIProvider("openai", "test-key", server.URL, "qwen3.6-flash", 5*time.Second, 0, false)
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{Temperature: 0}); err == nil {
		t.Fatal("expected temperature rejection")
	}
	if requests != 1 {
		t.Fatalf("strict adapter made %d HTTP requests, want exactly 1", requests)
	}
}

func TestOpenAIProviderAtMostOncePolicyOverridesCompatibilityRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid temperature: only 1 is allowed"}}`))
	}))
	defer server.Close()
	p := NewOpenAIProvider("openai", "test-key", server.URL, "qwen3.6-flash", 5*time.Second, 0, true)
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Temperature: 0, ExecutionPolicy: llm.CompletionExecutionAtMostOnce,
	}); err == nil {
		t.Fatal("expected temperature rejection")
	}
	if requests != 1 {
		t.Fatalf("at-most-once policy made %d physical HTTP requests", requests)
	}
}

func TestOpenAIProviderAtMostOnceDoesNotReplayPostRedirect(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	p := NewOpenAIProvider("openai", "test-key", server.URL, "model", 5*time.Second, 0, true)
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		ExecutionPolicy: llm.CompletionExecutionAtMostOnce,
	}); err == nil {
		t.Fatal("expected redirect to be terminal")
	}
	if requests != 1 {
		t.Fatalf("at-most-once request followed a replayable redirect: requests=%d", requests)
	}
}

func TestOpenAIProviderCompleteReturnsNonOKError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.2)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user", Temperature: 0.2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error, got %v", err)
	}
}

func TestOpenAIProviderCompleteReturnsEmptyChoicesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":0}}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.2)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user", Temperature: 0.2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "empty choices") {
		t.Fatalf("expected empty choices error, got %v", err)
	}
}

func TestOpenAIProviderCompleteDoesNotRetryOnNonTemperatureError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"context length exceeded","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.2)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user", Temperature: 0.2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestOpenAIProviderCompleteReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"},"choices":[],"usage":{}}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.2)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user", Temperature: 0.2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "rate limit exceeded") ||
		!strings.Contains(err.Error(), "provider_error") {
		t.Fatalf("expected classified API error without provider detail, got %v", err)
	}
}

func TestOpenAIProviderCompleteUsesDefaultTemperature(t *testing.T) {
	var receivedTemperature float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequestBody(t, r)
		receivedTemperature = body.Temperature
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"choices":[{"message":{"content":"ok"}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1}
		}`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.7)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user",
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if receivedTemperature != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", receivedTemperature)
	}
}

func TestOpenAIProviderCompleteOnlySendsConfiguredThinkingMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		setting  *bool
		expected *bool
	}{
		{name: "omitted"},
		{name: "disabled", setting: boolPointer(false), expected: boolPointer(false)},
		{name: "enabled", setting: boolPointer(true), expected: boolPointer(true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var received *bool
			var receivedFormat *responseFormat
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := decodeRequestBody(t, r)
				received = body.EnableThinking
				receivedFormat = body.ResponseFormat
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer server.Close()

			provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0)
			provider.enableThinking = test.setting
			if _, err := provider.Complete(context.Background(), llm.CompletionRequest{JSONMode: true}); err != nil {
				t.Fatalf("complete failed: %v", err)
			}
			if receivedFormat == nil || receivedFormat.Type != "json_object" {
				t.Fatalf("structured output request missing JSON mode: %+v", receivedFormat)
			}
			if test.expected == nil {
				if received != nil {
					t.Fatalf("enable_thinking must be omitted by default, got %v", *received)
				}
				return
			}
			if received == nil || *received != *test.expected {
				t.Fatalf("enable_thinking = %v, want %v", received, *test.expected)
			}
		})
	}
}

func TestOpenAIProviderCompleteUsesDeepSeekV4ThinkingObject(t *testing.T) {
	var received testOpenAIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = decodeRequestBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "deepseek-v4-flash", 5*time.Second, 0)
	provider.enableThinking = boolPointer(false)
	provider.deepSeekBetaDialect = true
	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{JSONMode: true}); err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if received.EnableThinking != nil {
		t.Fatalf("DeepSeek V4 must omit legacy enable_thinking, got %v", *received.EnableThinking)
	}
	if received.Thinking == nil || received.Thinking.Type != "disabled" {
		t.Fatalf("DeepSeek V4 thinking mode = %+v, want disabled", received.Thinking)
	}
}

func TestOpenAIProviderCompleteUsesAlibabaDeepSeekThinkingField(t *testing.T) {
	var received testOpenAIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = decodeRequestBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "deepseek-v4-flash", 5*time.Second, 0)
	provider.enableThinking = boolPointer(false)
	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{JSONMode: true}); err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if received.Thinking != nil {
		t.Fatalf("Alibaba-compatible DeepSeek must omit the direct-beta thinking object: %+v", received.Thinking)
	}
	if received.EnableThinking == nil || *received.EnableThinking {
		t.Fatalf("Alibaba-compatible DeepSeek enable_thinking = %v, want false", received.EnableThinking)
	}
}

func TestStructuredOutputObjectShapesRedactsValues(t *testing.T) {
	shapes := structuredOutputObjectShapes(map[string]any{
		"rule": map[string]any{
			"secret": "must-not-appear",
			"steps": []any{
				map[string]any{
					"action": "extract",
					"fields": []any{"also-secret"},
				},
			},
		},
	})
	for _, expected := range []string{
		`$.rule.steps[0] action="extract"`,
		"keys=[action fields]",
	} {
		if !strings.Contains(shapes, expected) {
			t.Fatalf("shape diagnostic %q is missing %q", shapes, expected)
		}
	}
	for _, secret := range []string{"must-not-appear", "also-secret"} {
		if strings.Contains(shapes, secret) {
			t.Fatalf("shape diagnostic leaked value %q: %s", secret, shapes)
		}
	}
}

func TestStructuredOutputSchemaForDeepSeekV4UsesDocumentedDefDialect(t *testing.T) {
	standard := json.RawMessage(`{
		"type":"object",
		"properties":{"node":{"$ref":"#/$defs/node"}},
		"required":["node"],
		"additionalProperties":false,
		"$defs":{"node":{
			"type":"object",
			"properties":{"children":{"type":"array","items":{"$ref":"#/$defs/node"}}},
			"required":["children"],
			"additionalProperties":false
		}}
	}`)
	converted, err := structuredOutputSchemaForModel("deepseek-v4-flash", standard)
	if err != nil {
		t.Fatalf("convert DeepSeek schema: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(converted, &value); err != nil {
		t.Fatalf("decode converted schema: %v", err)
	}
	if _, exists := value["$defs"]; exists {
		t.Fatalf("DeepSeek schema retained unsupported $defs: %s", converted)
	}
	if _, exists := value["$def"]; !exists {
		t.Fatalf("DeepSeek schema is missing documented $def: %s", converted)
	}
	encoded := string(converted)
	if strings.Contains(encoded, "#/$defs/") || !strings.Contains(encoded, "#/$def/node") {
		t.Fatalf("DeepSeek references were not translated: %s", converted)
	}

	unchanged, err := structuredOutputSchemaForModel("gpt-4o", standard)
	if err != nil {
		t.Fatalf("preserve standard schema: %v", err)
	}
	if string(unchanged) != string(standard) {
		t.Fatalf("ordinary OpenAI schema changed:\n got: %s\nwant: %s", unchanged, standard)
	}

	alibaba, err := structuredOutputSchemaForModel("deepseek-v4-flash", standard, false)
	if err != nil {
		t.Fatalf("preserve Alibaba-compatible DeepSeek schema: %v", err)
	}
	if string(alibaba) != string(standard) {
		t.Fatalf("Alibaba-compatible DeepSeek schema changed:\n got: %s\nwant: %s", alibaba, standard)
	}
}

func TestOpenAIProviderCompleteUsesForcedStrictToolOutput(t *testing.T) {
	const maxOutputTokens = 4096
	output := &llm.StructuredOutput{
		Name:        "submit_rule",
		Description: "Submit a rule.",
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{"value":{"type":"string"}},
			"required":["value"],
			"additionalProperties":false
		}`),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequestBody(t, r)
		if body.MaxTokens == nil || *body.MaxTokens != maxOutputTokens {
			t.Fatalf("strict tool request lost max_tokens: %+v", body.MaxTokens)
		}
		if body.ResponseFormat != nil {
			t.Fatalf("strict tool output must omit response_format: %+v", body.ResponseFormat)
		}
		if len(body.Tools) != 1 || body.Tools[0].Type != "function" ||
			!body.Tools[0].Function.Strict ||
			body.Tools[0].Function.Name != output.Name {
			t.Fatalf("unexpected strict tool definition: %+v", body.Tools)
		}
		var gotSchema, wantSchema any
		if err := json.Unmarshal(body.Tools[0].Function.Parameters, &gotSchema); err != nil {
			t.Fatalf("decode sent schema: %v", err)
		}
		if err := json.Unmarshal(output.Schema, &wantSchema); err != nil {
			t.Fatalf("decode expected schema: %v", err)
		}
		if !reflect.DeepEqual(gotSchema, wantSchema) {
			t.Fatalf("sent schema does not match request: got=%#v want=%#v", gotSchema, wantSchema)
		}
		if body.ToolChoice == nil || body.ToolChoice.Type != "function" ||
			body.ToolChoice.Function.Name != output.Name {
			t.Fatalf("strict tool was not forced: %+v", body.ToolChoice)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"strict-response",
			"choices":[{
				"message":{"content":null,"tool_calls":[{
					"id":"call-1",
					"type":"function",
					"function":{"name":"submit_rule","arguments":"{\"value\":\"ok\"}"}
				}]},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":12,"completion_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0)
	provider.strictToolOutput = true
	response, err := provider.Complete(context.Background(), llm.CompletionRequest{
		JSONMode: true, MaxOutputTokens: maxOutputTokens, StructuredOutput: output,
	})
	if err != nil {
		t.Fatalf("strict completion failed: %v", err)
	}
	if response.Content != `{"value":"ok"}` ||
		response.ResponseID != "strict-response" ||
		response.FinishReason != "tool_calls" {
		t.Fatalf("unexpected normalized strict response: %+v", response)
	}
}

func TestOpenAIProviderCompleteRejectsInvalidStrictToolArguments(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{"tool_calls":[{
					"id":"call-1",
					"type":"function",
					"function":{"name":"submit_rule","arguments":"{}"}
				}]},
				"finish_reason":"tool_calls"
			}]
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider("openai", "test-key", server.URL, "test-model", 5*time.Second, 0, true)
	provider.strictToolOutput = true
	sink := &providerTraceSink{}
	request := llm.CompletionRequest{
		JSONMode: true,
		StructuredOutput: &llm.StructuredOutput{
			Name: "submit_rule",
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"value":{"type":"string"}},
				"required":["value"],
				"additionalProperties":false
			}`),
		},
	}
	_, err := llm.CompleteWithTrace(
		llm.WithCompletionTraceSink(context.Background(), sink),
		tracedProviderCompleter{provider: provider},
		request,
		llm.CompletionTraceMetadata{Phase: llm.CompletionPhaseFinal},
	)
	if err == nil || !strings.Contains(err.Error(), "structured_output_invalid_arguments") {
		t.Fatalf("invalid strict arguments were accepted: %v", err)
	}
	var feedbackError llm.CompletionValidationFeedbackError
	if !errors.As(err, &feedbackError) {
		t.Fatalf("strict argument failure omitted structural feedback: %v", err)
	}
	feedback := feedbackError.CompletionValidationFeedback()
	if !strings.Contains(feedback, "path=root") ||
		!strings.Contains(feedback, "keyword=required") ||
		!strings.Contains(feedback, "object shapes:") ||
		!strings.Contains(feedback, "structural fragment:") ||
		!strings.Contains(feedback, `{}`) {
		t.Fatalf("strict argument feedback omitted redacted structure: %q", feedback)
	}
	if requests != 1 {
		t.Fatalf("invalid strict arguments triggered %d physical requests, want 1", requests)
	}
	if len(sink.events) != 1 ||
		sink.events[0].BoundaryKind != llm.CompletionBoundaryProviderError ||
		sink.events[0].ErrorCode != "structured_output_invalid_arguments" ||
		sink.events[0].RequestHash != llm.CanonicalCompletionRequestHash(request) ||
		!strings.Contains(sink.events[0].Result.Content, `"arguments":"{}"`) {
		t.Fatalf("strict argument failure was not captured exactly: %+v", sink.events)
	}
}

func TestValidateStructuredOutputArgumentsRedactsScalarValuesFromFeedback(t *testing.T) {
	const sentinel = "private-page-value-sentinel"
	err := validateStructuredOutputArguments(json.RawMessage(`{
		"type":"object",
		"properties":{"value":{"type":"string","enum":["allowed"]}},
		"required":["value"],
		"additionalProperties":false
	}`), `{"value":"`+sentinel+`"}`)
	if err == nil {
		t.Fatal("invalid enum value was accepted")
	}
	feedback := err.Error()
	if strings.Contains(feedback, sentinel) {
		t.Fatalf("validation feedback exposed scalar value: %q", feedback)
	}
	if !strings.Contains(feedback, "structural fragment:") || !strings.Contains(feedback, `"value":"<string>"`) {
		t.Fatalf("validation feedback omitted redacted fragment: %q", feedback)
	}
}

func TestOpenAIProviderCompleteRejectsMissingOrWrongStrictTool(t *testing.T) {
	for name, testCase := range map[string]struct {
		response   string
		diagnostic string
	}{
		"missing": {
			response:   `{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`,
			diagnostic: "structured_output_call_count",
		},
		"wrong": {
			response: `{"choices":[{"message":{"tool_calls":[{
				"type":"function","function":{"name":"other","arguments":"{}"}
			}]},"finish_reason":"tool_calls"}]}`,
			diagnostic: "structured_output_wrong_tool",
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(testCase.response))
			}))
			defer server.Close()
			provider := NewOpenAIProvider("openai", "", server.URL, "model", 5*time.Second, 0, false)
			provider.strictToolOutput = true
			_, err := provider.Complete(context.Background(), llm.CompletionRequest{
				StructuredOutput: &llm.StructuredOutput{
					Name: "submit_rule",
					Schema: json.RawMessage(`{
						"type":"object","properties":{},"required":[],"additionalProperties":false
					}`),
				},
			})
			if err == nil || !strings.Contains(err.Error(), testCase.diagnostic) {
				t.Fatalf("unexpected strict response was accepted: %v", err)
			}
		})
	}
}

func TestOpenAIProviderCompleteReturnsDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	p := NewOpenAIProvider("openai", "test-key", server.URL, "gpt-4o", 5*time.Second, 0.2)
	_, err := p.Complete(context.Background(), llm.CompletionRequest{
		Model: "gpt-4o", System: "sys", User: "user", Temperature: 0.2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

type testOpenAIRequest struct {
	Model               string            `json:"model"`
	Temperature         float64           `json:"temperature"`
	MaxTokens           *int              `json:"max_tokens"`
	MaxCompletionTokens *int              `json:"max_completion_tokens"`
	ResponseFormat      *responseFormat   `json:"response_format"`
	EnableThinking      *bool             `json:"enable_thinking"`
	Thinking            *openAIThinking   `json:"thinking"`
	Tools               []openAITool      `json:"tools"`
	ToolChoice          *openAIToolChoice `json:"tool_choice"`
}

func boolPointer(value bool) *bool { return &value }

func decodeRequestBody(t *testing.T, r *http.Request) testOpenAIRequest {
	t.Helper()
	var body testOpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}
