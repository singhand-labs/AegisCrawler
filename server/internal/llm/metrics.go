package llm

import (
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics collects Prometheus-style metrics for the LLM pipeline.
// It is safe for concurrent use.
type Metrics struct {
	Requests  *prometheus.CounterVec
	Errors    *prometheus.CounterVec
	Tokens    *prometheus.CounterVec
	CacheHits *prometheus.CounterVec
	Duration  *prometheus.HistogramVec

	// Hard-budget metrics. Workspace IDs are deliberately not labels, so
	// cardinality stays bounded. The SQLite ledger, not Prometheus, is the
	// financial source of truth.
	BudgetRejections  *prometheus.CounterVec
	LedgerFailures    *prometheus.CounterVec
	UncertainUsage    *prometheus.CounterVec
	PhysicalCalls     *prometheus.CounterVec
	FallbackDenials   prometheus.Counter
	BudgetSettled     prometheus.Gauge
	BudgetActive      prometheus.Gauge
	BudgetRemaining   prometheus.Gauge
	BudgetUtilization prometheus.Gauge
}

// latencyBucketUpperBounds defines the histogram buckets in seconds.
var latencyBucketUpperBounds = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var (
	defaultMetricsOnce sync.Once
	defaultMetrics     *Metrics
)

// NewMetrics creates a new LLM metrics collector and registers it with the
// default Prometheus registry. Subsequent calls return the same instance so
// that AlreadyRegisteredError is handled gracefully.
func NewMetrics() *Metrics {
	defaultMetricsOnce.Do(func() {
		defaultMetrics = NewMetricsWithRegistry(prometheus.DefaultRegisterer)
	})
	return defaultMetrics
}

// NewMetricsWithRegistry creates a new LLM metrics collector registered with
// the supplied registry. Passing a nil registerer skips registration; this is
// useful mainly for tests that want to inspect metrics without touching the
// global registry.
func NewMetricsWithRegistry(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "requests_total",
			Help:      "Total LLM completion requests by provider and model",
		}, []string{"provider", "model"}),
		Errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "errors_total",
			Help:      "Total LLM completion errors by provider, model, and type",
		}, []string{"provider", "model", "type"}),
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "tokens_total",
			Help:      "Total LLM tokens by provider, model, and direction",
		}, []string{"provider", "model", "direction"}),
		CacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "cache_hits_total",
			Help:      "Total LLM cache hits by provider and model",
		}, []string{"provider", "model"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "latency_seconds",
			Help:      "LLM completion latency in seconds",
			Buckets:   latencyBucketUpperBounds,
		}, []string{"provider", "model"}),
		BudgetRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_rejections_total",
			Help:      "Total hard-budget admission denials by scope and limit",
		}, []string{"scope", "limit"}),
		LedgerFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_ledger_failures_total",
			Help:      "Total budget ledger failures by operation",
		}, []string{"operation"}),
		UncertainUsage: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_uncertain_total",
			Help:      "Total calls settled at their full reservation because usage was uncertain, by reason",
		}, []string{"reason"}),
		PhysicalCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "physical_calls_total",
			Help:      "Total admitted physical provider calls by route slot and outcome",
		}, []string{"route", "outcome"}),
		FallbackDenials: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "fallback_admission_denials_total",
			Help:      "Total fallback admissions refused by the remaining budget",
		}),
		BudgetSettled: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_settled_usd_nanos",
			Help:      "Global settled USD nanos for the current UTC budget day",
		}),
		BudgetActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_active_reserved_usd_nanos",
			Help:      "Global active reservation USD nanos for the current UTC budget day",
		}),
		BudgetRemaining: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_remaining_usd_nanos",
			Help:      "Global remaining USD nanos for the current UTC budget day",
		}),
		BudgetUtilization: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "opencrawler",
			Subsystem: "llm",
			Name:      "budget_utilization_ratio",
			Help:      "Global consumed fraction of the daily budget for the current UTC day",
		}),
	}

	if reg != nil {
		reg.MustRegister(m.Requests, m.Errors, m.Tokens, m.CacheHits, m.Duration,
			m.BudgetRejections, m.LedgerFailures, m.UncertainUsage, m.PhysicalCalls,
			m.FallbackDenials, m.BudgetSettled, m.BudgetActive, m.BudgetRemaining,
			m.BudgetUtilization)
	}
	return m
}

// RecordBudgetDenial records one exact-cap admission denial.
func (m *Metrics) RecordBudgetDenial(scope, limit string) {
	if m == nil {
		return
	}
	scope = boundedBudgetScope(scope)
	limit = boundedBudgetLimit(limit)
	m.BudgetRejections.WithLabelValues(scope, limit).Inc()
}

// RecordLedgerFailure records one ledger availability failure by operation.
func (m *Metrics) RecordLedgerFailure(operation string) {
	if m == nil {
		return
	}
	operation = boundedLedgerOperation(operation)
	m.LedgerFailures.WithLabelValues(operation).Inc()
}

// RecordUncertainUsage records one call charged at its full reservation.
func (m *Metrics) RecordUncertainUsage(reason string) {
	m.RecordUncertainUsageCount(reason, 1)
}

// RecordUncertainUsageCount records a batch of calls charged at their full
// reservation. Startup reconciliation uses the recovered count so a restart
// publishes one metric event per call without an O(n) increment loop.
func (m *Metrics) RecordUncertainUsageCount(reason string, count int) {
	if m == nil {
		return
	}
	if count <= 0 {
		return
	}
	reason = boundedUncertainReason(reason)
	m.UncertainUsage.WithLabelValues(reason).Add(float64(count))
}

// RecordPhysicalCall records the outcome of one admitted physical call.
func (m *Metrics) RecordPhysicalCall(route, outcome string) {
	if m == nil {
		return
	}
	if route != "primary" && route != "fallback" {
		route = "unknown"
	}
	switch outcome {
	case "success", "failed", "unavailable":
	default:
		outcome = "other"
	}
	m.PhysicalCalls.WithLabelValues(route, outcome).Inc()
}

// RecordFallbackDenial records one fallback admission refused by the budget.
func (m *Metrics) RecordFallbackDenial() {
	if m == nil {
		return
	}
	m.FallbackDenials.Inc()
}

// ObserveBudgetSnapshot publishes the global daily budget gauges.
func (m *Metrics) ObserveBudgetSnapshot(settled, active, remaining, dailyCap int64) {
	if m == nil {
		return
	}
	m.BudgetSettled.Set(float64(settled))
	m.BudgetActive.Set(float64(active))
	m.BudgetRemaining.Set(float64(remaining))
	if dailyCap > 0 {
		m.BudgetUtilization.Set(float64(settled+active) / float64(dailyCap))
	} else {
		m.BudgetUtilization.Set(0)
	}
}

func boundedLedgerOperation(operation string) string {
	switch operation {
	case "reserve", "mark_dispatched", "release", "settle_trusted",
		"settle_uncertain", "snapshot", "recover":
		return operation
	default:
		return "other"
	}
}

func boundedBudgetScope(scope string) string {
	switch scope {
	case "global", "workspace":
		return scope
	default:
		return "unknown"
	}
}

func boundedBudgetLimit(limit string) string {
	switch limit {
	case "request", "daily":
		return limit
	default:
		return "unknown"
	}
}

func boundedUncertainReason(reason string) string {
	switch reason {
	case "missing_usage", "failed_call", "availability_failure",
		"usage_out_of_bounds", "unpriceable_usage", "unknown_usage_category",
		"contradictory_usage", "startup_reconciliation":
		return reason
	default:
		return "other"
	}
}

// RecordCompletion records a single completion result. It should be called once
// per completion invocation, including retries and cache hits, so requests and
// latency always reflect total LLM activity. Cache hits additionally increment
// cache_hits_total.
func (m *Metrics) RecordCompletion(provider, model string, latency time.Duration, inputTokens, outputTokens int, cacheHit bool, err error) {
	if m == nil {
		return
	}
	// Provider usage is untrusted. Adapters preserve a contradictory marker for
	// conservative settlement, but monotonic Prometheus counters must never see
	// a negative Add even if a custom adapter bypasses boundary normalization.
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}

	m.Requests.WithLabelValues(provider, model).Inc()
	m.Duration.WithLabelValues(provider, model).Observe(latency.Seconds())
	m.Tokens.WithLabelValues(provider, model, "input").Add(float64(inputTokens))
	m.Tokens.WithLabelValues(provider, model, "output").Add(float64(outputTokens))

	if cacheHit {
		m.CacheHits.WithLabelValues(provider, model).Inc()
	}

	if err != nil {
		m.Errors.WithLabelValues(provider, model, errorTypeString(err)).Inc()
	}
}

// errorTypeString classifies an error into a stable label value for metrics.
func errorTypeString(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown provider"):
		return "unknown_provider"
	case strings.Contains(msg, "context canceled"):
		return "canceled"
	case strings.Contains(msg, "context deadline exceeded"):
		return "timeout"
	default:
		return "provider_error"
	}
}
