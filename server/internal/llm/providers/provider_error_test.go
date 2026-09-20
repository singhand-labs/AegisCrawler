package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type providerReadTimeoutError struct{}

func (providerReadTimeoutError) Error() string { return "provider response read timed out" }
func (providerReadTimeoutError) Timeout() bool { return true }

type providerTimeoutReadCloser struct{}

func (providerTimeoutReadCloser) Read([]byte) (int, error) {
	return 0, providerReadTimeoutError{}
}

func (providerTimeoutReadCloser) Close() error { return nil }

func TestProviderResponseErrorExposesSanitizedClassificationMetadata(t *testing.T) {
	err := newProviderResponseError("provider", 503, "overloaded", "synthetic detail")
	if err.CompletionHTTPStatus() != 503 || err.CompletionErrorCode() != "overloaded" {
		t.Fatalf("classification metadata = status %d code %q", err.CompletionHTTPStatus(), err.CompletionErrorCode())
	}
}

func TestProviderResponseErrorDoesNotExposeDynamicDetailToErrorOrZap(t *testing.T) {
	const sentinel = "provider-http-body-secret-sentinel"

	err := newProviderResponseError("provider", 503, "overloaded", sentinel)
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Error() exposed provider detail: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "provider 503 overloaded") {
		t.Fatalf("Error() lost stable classification: %q", err.Error())
	}

	core, observed := observer.New(zap.ErrorLevel)
	zap.New(core).Error("provider request failed", zap.Error(err))
	logs := observed.All()
	if len(logs) != 1 {
		t.Fatalf("expected one error log, got %d", len(logs))
	}
	loggedError, _ := logs[0].ContextMap()["error"].(string)
	if loggedError == "" || strings.Contains(loggedError, sentinel) {
		t.Fatalf("zap error field = %q, want stable classification without detail", loggedError)
	}
}

func TestProviderResponseErrorKeepsSafeTemperatureRetryClassification(t *testing.T) {
	const sentinel = "provider-temperature-body-secret-sentinel"

	err := newProviderResponseError(
		"provider",
		400,
		"http_error",
		"invalid temperature: only 1 is allowed "+sentinel,
	)
	if !err.temperatureRetrySafe {
		t.Fatal("expected internal temperature compatibility classification")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Error() exposed provider temperature detail: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid temperature") || !strings.Contains(err.Error(), "only 1") {
		t.Fatalf("Error() = %q, want fixed compatibility retry marker", err.Error())
	}
}

func TestHTTPProvidersPreserveCancellationAndForbidFallback(t *testing.T) {
	for _, test := range providerErrorAdapterTests() {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: providerRoundTripFunc(
				func(*http.Request) (*http.Response, error) {
					return nil, context.Canceled
				},
			)}
			err := test.complete(client)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want preserved context.Canceled", err)
			}
			if llm.IsAvailabilityError(err) {
				t.Fatalf("cancelled request was classified as fallback-safe: %v", err)
			}
		})
	}
}

func TestHTTPProvidersPreserveResponseReadTimeoutAndAllowFallback(t *testing.T) {
	for _, test := range providerErrorAdapterTests() {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: providerRoundTripFunc(
				func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       providerTimeoutReadCloser{},
					}, nil
				},
			)}
			err := test.complete(client)
			var timeout interface{ Timeout() bool }
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("error = %v, want preserved timeout cause", err)
			}
			if !llm.IsAvailabilityError(err) {
				t.Fatalf("response read timeout was not classified as fallback-safe: %v", err)
			}
		})
	}
}

func TestHTTPProvidersDefinitiveClientStatusForbidsFallbackAfterReadTimeout(t *testing.T) {
	for _, test := range providerErrorAdapterTests() {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: providerRoundTripFunc(
				func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Header:     make(http.Header),
						Body:       providerTimeoutReadCloser{},
					}, nil
				},
			)}
			err := test.complete(client)
			var timeout interface{ Timeout() bool }
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("error = %v, want preserved timeout cause", err)
			}
			if llm.IsAvailabilityError(err) {
				t.Fatalf("HTTP 401 read timeout was classified as fallback-safe: %v", err)
			}
		})
	}
}

type providerErrorAdapterTest struct {
	name     string
	complete func(*http.Client) error
}

func providerErrorAdapterTests() []providerErrorAdapterTest {
	return []providerErrorAdapterTest{
		{
			name: "openai",
			complete: func(client *http.Client) error {
				provider := NewOpenAIProvider(
					"openai",
					"test-key",
					"https://provider.invalid",
					"test-model",
					time.Second,
					0,
					false,
				)
				provider.client = client
				_, err := provider.Complete(
					context.Background(),
					llm.CompletionRequest{User: "test"},
				)
				return err
			},
		},
		{
			name: "anthropic",
			complete: func(client *http.Client) error {
				provider := NewAnthropicProvider(
					"anthropic",
					"test-key",
					"https://provider.invalid",
					"test-model",
					time.Second,
					0,
					false,
				)
				provider.client = client
				_, err := provider.Complete(
					context.Background(),
					llm.CompletionRequest{User: "test", MaxOutputTokens: 1},
				)
				return err
			},
		},
	}
}
