package llm

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func TestMetricsRecordCompletion(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	m.RecordCompletion("openai", "gpt-4o", 50*time.Millisecond, 10, 20, false, nil)

	if got := testutil.ToFloat64(m.Requests.WithLabelValues("openai", "gpt-4o")); got != 1 {
		t.Fatalf("expected 1 request, got %v", got)
	}
	if got := testutil.ToFloat64(m.Tokens.WithLabelValues("openai", "gpt-4o", "input")); got != 10 {
		t.Fatalf("expected 10 input tokens, got %v", got)
	}
	if got := testutil.ToFloat64(m.Tokens.WithLabelValues("openai", "gpt-4o", "output")); got != 20 {
		t.Fatalf("expected 20 output tokens, got %v", got)
	}
	if got := testutil.ToFloat64(m.CacheHits.WithLabelValues("openai", "gpt-4o")); got != 0 {
		t.Fatalf("expected 0 cache hits, got %v", got)
	}
	if got := testutil.ToFloat64(m.Errors.WithLabelValues("openai", "gpt-4o", "provider_error")); got != 0 {
		t.Fatalf("expected 0 errors, got %v", got)
	}
}

func TestMetricsRecordCompletionClampsNegativeProviderUsage(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())

	m.RecordCompletion("openai", "gpt-4o", 50*time.Millisecond, -10, -20, false, nil)

	if got := testutil.ToFloat64(m.Requests.WithLabelValues("openai", "gpt-4o")); got != 1 {
		t.Fatalf("expected 1 request, got %v", got)
	}
	if got := testutil.ToFloat64(m.Tokens.WithLabelValues("openai", "gpt-4o", "input")); got != 0 {
		t.Fatalf("negative input usage must not reach a counter, got %v", got)
	}
	if got := testutil.ToFloat64(m.Tokens.WithLabelValues("openai", "gpt-4o", "output")); got != 0 {
		t.Fatalf("negative output usage must not reach a counter, got %v", got)
	}
}

func TestMetricsRecordCacheHit(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	m.RecordCompletion("openai", "gpt-4o", 1*time.Millisecond, 5, 5, true, nil)

	if got := testutil.ToFloat64(m.CacheHits.WithLabelValues("openai", "gpt-4o")); got != 1 {
		t.Fatalf("expected 1 cache hit, got %v", got)
	}
	if got := testutil.ToFloat64(m.Requests.WithLabelValues("openai", "gpt-4o")); got != 1 {
		t.Fatalf("expected cache hit to still count as a request, got %v", got)
	}
	if got := testutil.ToFloat64(m.Tokens.WithLabelValues("openai", "gpt-4o", "input")); got != 5 {
		t.Fatalf("expected cache hit tokens recorded, got %v", got)
	}
}

func TestMetricsRecordError(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	m.RecordCompletion("openai", "gpt-4o", 100*time.Millisecond, 0, 0, false, errors.New("provider failed"))

	if got := testutil.ToFloat64(m.Errors.WithLabelValues("openai", "gpt-4o", "provider_error")); got != 1 {
		t.Fatalf("expected 1 error, got %v", got)
	}
	if got := testutil.ToFloat64(m.Requests.WithLabelValues("openai", "gpt-4o")); got != 1 {
		t.Fatalf("expected error to still count as a request, got %v", got)
	}
}

func TestMetricsErrorTypeLabels(t *testing.T) {
	m := NewMetricsWithRegistry(prometheus.NewRegistry())
	m.RecordCompletion("openai", "gpt-4o", 1*time.Millisecond, 0, 0, false, errors.New("unknown provider \"foo\""))
	m.RecordCompletion("openai", "gpt-4o", 1*time.Millisecond, 0, 0, false, errors.New("context canceled"))
	m.RecordCompletion("openai", "gpt-4o", 1*time.Millisecond, 0, 0, false, errors.New("context deadline exceeded"))
	m.RecordCompletion("openai", "gpt-4o", 1*time.Millisecond, 0, 0, false, errors.New("something else"))

	if got := testutil.ToFloat64(m.Errors.WithLabelValues("openai", "gpt-4o", "unknown_provider")); got != 1 {
		t.Fatalf("expected 1 unknown_provider error, got %v", got)
	}
	if got := testutil.ToFloat64(m.Errors.WithLabelValues("openai", "gpt-4o", "canceled")); got != 1 {
		t.Fatalf("expected 1 canceled error, got %v", got)
	}
	if got := testutil.ToFloat64(m.Errors.WithLabelValues("openai", "gpt-4o", "timeout")); got != 1 {
		t.Fatalf("expected 1 timeout error, got %v", got)
	}
	if got := testutil.ToFloat64(m.Errors.WithLabelValues("openai", "gpt-4o", "provider_error")); got != 1 {
		t.Fatalf("expected 1 provider_error error, got %v", got)
	}
}

func TestMetricsLatencyHistogramBuckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegistry(reg)
	m.RecordCompletion("openai", "gpt-4o", 5*time.Millisecond, 0, 0, false, nil)
	m.RecordCompletion("openai", "gpt-4o", 150*time.Millisecond, 0, 0, false, nil)
	m.RecordCompletion("openai", "gpt-4o", 3*time.Second, 0, 0, false, nil)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	var hist *dto.Histogram
	for _, fam := range families {
		if fam.GetName() == "opencrawler_llm_latency_seconds" {
			hist = fam.Metric[0].Histogram
			break
		}
	}
	if hist == nil {
		t.Fatal("histogram not found")
	}
	if hist.GetSampleCount() != 3 {
		t.Fatalf("expected 3 samples, got %d", hist.GetSampleCount())
	}

	bucketCounts := make(map[float64]uint64)
	for _, b := range hist.Bucket {
		bucketCounts[b.GetUpperBound()] = b.GetCumulativeCount()
	}
	if bucketCounts[0.01] != 1 {
		t.Fatalf("expected 1 sample in 0.01s bucket, got %d", bucketCounts[0.01])
	}
	if bucketCounts[0.25] != 2 {
		t.Fatalf("expected 2 samples in 0.25s bucket, got %d", bucketCounts[0.25])
	}
	if bucketCounts[5] != 3 {
		t.Fatalf("expected 3 samples in 5s bucket, got %d", bucketCounts[5])
	}
	if bucketCounts[10] != 3 {
		t.Fatalf("expected 3 samples in 10s bucket, got %d", bucketCounts[10])
	}
}

func TestMetricsExposition(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegistry(reg)
	m.RecordCompletion("openai", "gpt-4o", 50*time.Millisecond, 10, 20, false, nil)
	m.RecordCompletion("openai", "gpt-4o", 1*time.Millisecond, 5, 5, true, nil)
	m.RecordCompletion("openai", "gpt-4o", 100*time.Millisecond, 0, 0, false, errors.New("provider failed"))

	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	for _, fam := range families {
		if err := enc.Encode(fam); err != nil {
			t.Fatalf("encode failed: %v", err)
		}
	}
	body := buf.String()

	expected := []string{
		"# HELP opencrawler_llm_requests_total",
		"# TYPE opencrawler_llm_requests_total counter",
		"opencrawler_llm_requests_total{model=\"gpt-4o\",provider=\"openai\"} 3",
		"# HELP opencrawler_llm_cache_hits_total",
		"opencrawler_llm_cache_hits_total{model=\"gpt-4o\",provider=\"openai\"} 1",
		"opencrawler_llm_tokens_total{direction=\"input\",model=\"gpt-4o\",provider=\"openai\"} 15",
		"opencrawler_llm_tokens_total{direction=\"output\",model=\"gpt-4o\",provider=\"openai\"} 25",
		"# HELP opencrawler_llm_errors_total",
		"opencrawler_llm_errors_total{model=\"gpt-4o\",provider=\"openai\",type=\"provider_error\"} 1",
		"# HELP opencrawler_llm_latency_seconds",
		"opencrawler_llm_latency_seconds_count{model=\"gpt-4o\",provider=\"openai\"} 3",
		"opencrawler_llm_latency_seconds_sum{model=\"gpt-4o\",provider=\"openai\"}",
	}
	for _, want := range expected {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics to contain %q, got:\n%s", want, body)
		}
	}
}

func TestMetricsNilSafe(t *testing.T) {
	var m *Metrics
	m.RecordCompletion("openai", "gpt-4o", 50*time.Millisecond, 10, 20, false, nil)
}

func TestNewMetricsReturnsSingleton(t *testing.T) {
	m1 := NewMetrics()
	m2 := NewMetrics()
	if m1 == nil || m2 == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m1 != m2 {
		t.Fatal("expected NewMetrics to return the same instance")
	}
}

func TestErrorTypeStringNil(t *testing.T) {
	if got := errorTypeString(nil); got != "" {
		t.Fatalf("expected empty string for nil error, got %q", got)
	}
}

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
	m2 := NewMetrics()
	if m2 != m {
		t.Fatal("NewMetrics should return the same singleton")
	}
}

func TestErrorTypeString(t *testing.T) {
	if got := errorTypeString(nil); got != "" {
		t.Fatalf("expected empty for nil, got %q", got)
	}
	cases := []struct {
		msg  string
		want string
	}{
		{"unknown provider foo", "unknown_provider"},
		{"request cancelled: context canceled", "canceled"},
		{"deadline exceeded: context deadline exceeded", "timeout"},
		{"something else", "provider_error"},
	}
	for _, c := range cases {
		if got := errorTypeString(errors.New(c.msg)); got != c.want {
			t.Fatalf("errorTypeString(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}
