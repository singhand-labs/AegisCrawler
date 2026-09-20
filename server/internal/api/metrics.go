package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"go.uber.org/zap"
)

// Metrics holds Prometheus-style counters for the service.
type Metrics struct {
	mu sync.RWMutex

	requestsTotal           map[string]int64 // key: method|path|status
	tasksClaimedTotal       int64
	tasksCompletedTotal     map[string]int64 // key: status
	resultsReceivedTotal    int64
	heartbeatsReceivedTotal int64
	rowsPurgedTotal         map[string]int64 // key: entity
	dbSizeBytes             int64
	llmMetrics              *llm.Metrics
	gatherer                prometheus.Gatherer
}

// NewMetrics creates a new Metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		requestsTotal:       make(map[string]int64),
		tasksCompletedTotal: make(map[string]int64),
		rowsPurgedTotal:     make(map[string]int64),
		llmMetrics:          llm.NewMetrics(),
		gatherer:            prometheus.DefaultGatherer,
	}
}

// NewMetricsWithRegistry creates a new Metrics collector backed by the supplied
// Prometheus registry. This is intended for tests that need isolated metrics.
func NewMetricsWithRegistry(reg *prometheus.Registry) *Metrics {
	return &Metrics{
		requestsTotal:       make(map[string]int64),
		tasksCompletedTotal: make(map[string]int64),
		rowsPurgedTotal:     make(map[string]int64),
		llmMetrics:          llm.NewMetricsWithRegistry(reg),
		gatherer:            reg,
	}
}

// LLM returns the LLM-specific metrics collector.
func (m *Metrics) LLM() *llm.Metrics {
	if m == nil {
		return nil
	}
	return m.llmMetrics
}

func (m *Metrics) requestKey(method, path, status string) string {
	return method + "|" + path + "|" + status
}

// IncRequests increments the requests counter for the given method, path, and status.
func (m *Metrics) IncRequests(method, path string, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestsTotal[m.requestKey(method, path, strconv.Itoa(status))]++
}

// IncTasksClaimed increments the tasks claimed counter.
func (m *Metrics) IncTasksClaimed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasksClaimedTotal++
}

// IncTasksCompleted increments the tasks completed counter for the given status.
func (m *Metrics) IncTasksCompleted(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasksCompletedTotal[status]++
}

// IncResultsReceived increments the results received counter.
func (m *Metrics) IncResultsReceived() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resultsReceivedTotal++
}

// IncHeartbeatsReceived increments the heartbeats received counter.
func (m *Metrics) IncHeartbeatsReceived() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatsReceivedTotal++
}

// IncRowsPurged increments the rows purged counter for the given entity by count.
func (m *Metrics) IncRowsPurged(entity string, count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rowsPurgedTotal[entity] += count
}

// SetDBSize sets the database size gauge in bytes.
func (m *Metrics) SetDBSize(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dbSizeBytes = bytes
}

// MetricsResponseWriter wraps http.ResponseWriter to capture the status code.
type MetricsResponseWriter struct {
	http.ResponseWriter
	StatusCode int
}

// NewMetricsResponseWriter creates a wrapped response writer with a default status.
func NewMetricsResponseWriter(w http.ResponseWriter) *MetricsResponseWriter {
	return &MetricsResponseWriter{ResponseWriter: w, StatusCode: http.StatusOK}
}

// WriteHeader captures the status code before delegating.
func (mrw *MetricsResponseWriter) WriteHeader(code int) {
	mrw.StatusCode = code
	mrw.ResponseWriter.WriteHeader(code)
}

// pathFromPattern extracts the URL path portion from an http.ServeMux pattern.
// Patterns are of the form "METHOD /path" or "METHOD host/path".
func pathFromPattern(pattern string) string {
	fields := strings.Fields(pattern)
	if len(fields) == 0 {
		return pattern
	}
	return fields[len(fields)-1]
}

// MetricsCollectorMiddleware records request counts per method, path, and status.
// The path label is derived from the registered route pattern to avoid unbounded
// cardinality for routes such as /tasks/{id} and /rules/{id}.
func MetricsCollectorMiddleware(metrics *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mrw := NewMetricsResponseWriter(w)
			next.ServeHTTP(mrw, r)
			path := r.URL.Path
			if r.Pattern != "" {
				path = pathFromPattern(r.Pattern)
			}
			metrics.IncRequests(r.Method, path, mrw.StatusCode)
		})
	}
}

// MetricsHandler returns the Prometheus text exposition of current counters.
func MetricsHandler(metrics *Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		metrics.mu.RLock()

		fmt.Fprintln(w, "# HELP opencrawler_requests_total Total HTTP requests by method, path, and status")
		fmt.Fprintln(w, "# TYPE opencrawler_requests_total counter")
		for key, count := range metrics.requestsTotal {
			method, path, status := splitMetricKey(key)
			fmt.Fprintf(w, "opencrawler_requests_total{method=\"%s\",path=\"%s\",status=\"%s\"} %d\n", method, path, status, count)
		}

		fmt.Fprintln(w, "# HELP opencrawler_tasks_claimed_total Total tasks claimed by workers")
		fmt.Fprintln(w, "# TYPE opencrawler_tasks_claimed_total counter")
		fmt.Fprintf(w, "opencrawler_tasks_claimed_total %d\n", metrics.tasksClaimedTotal)

		fmt.Fprintln(w, "# HELP opencrawler_tasks_completed_total Total tasks completed by status")
		fmt.Fprintln(w, "# TYPE opencrawler_tasks_completed_total counter")
		for status, count := range metrics.tasksCompletedTotal {
			fmt.Fprintf(w, "opencrawler_tasks_completed_total{status=\"%s\"} %d\n", status, count)
		}

		fmt.Fprintln(w, "# HELP opencrawler_results_received_total Total results submitted by workers")
		fmt.Fprintln(w, "# TYPE opencrawler_results_received_total counter")
		fmt.Fprintf(w, "opencrawler_results_received_total %d\n", metrics.resultsReceivedTotal)

		fmt.Fprintln(w, "# HELP opencrawler_heartbeats_received_total Total heartbeats submitted by workers")
		fmt.Fprintln(w, "# TYPE opencrawler_heartbeats_received_total counter")
		fmt.Fprintf(w, "opencrawler_heartbeats_received_total %d\n", metrics.heartbeatsReceivedTotal)

		fmt.Fprintln(w, "# HELP opencrawler_rows_purged_total Total rows purged by entity")
		fmt.Fprintln(w, "# TYPE opencrawler_rows_purged_total counter")
		for entity, count := range metrics.rowsPurgedTotal {
			fmt.Fprintf(w, "opencrawler_rows_purged_total{entity=\"%s\"} %d\n", entity, count)
		}

		fmt.Fprintln(w, "# HELP opencrawler_db_size_bytes Size of the SQLite database file on disk in bytes")
		fmt.Fprintln(w, "# TYPE opencrawler_db_size_bytes gauge")
		fmt.Fprintf(w, "opencrawler_db_size_bytes %d\n", metrics.dbSizeBytes)

		metrics.mu.RUnlock()

		if metrics.gatherer != nil {
			families, err := metrics.gatherer.Gather()
			if err == nil {
				enc := expfmt.NewEncoder(w, expfmt.NewFormat(expfmt.TypeTextPlain))
				for _, fam := range families {
					_ = enc.Encode(fam)
				}
			}
		}
	}
}

// ServeMetrics refreshes current-day LLM budget gauges immediately before an
// authenticated scrape. A failed refresh keeps the endpoint available so the
// ledger-failure counters and other operational telemetry remain observable.
func (h *Handler) ServeMetrics(w http.ResponseWriter, r *http.Request) {
	if h != nil && h.cfg != nil && h.cfg.LLMEnabled &&
		h.cfg.EnforcedLLMPolicy() != nil &&
		h.llmBudgetMetricsRefresher != nil {
		refreshCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := h.llmBudgetMetricsRefresher.RefreshBudgetMetrics(refreshCtx)
		cancel()
		if err != nil && h.logger != nil {
			h.logger.Warn(
				"refresh current UTC-day LLM budget metrics failed",
				zap.String("error", llm.SanitizeCompletionError(err)),
			)
		}
	}
	MetricsHandler(h.metrics)(w, r)
}

func splitMetricKey(key string) (string, string, string) {
	parts := make([]string, 3)
	start := 0
	idx := 0
	for i, c := range key {
		if c == '|' {
			parts[idx] = key[start:i]
			idx++
			start = i + 1
			if idx == 2 {
				parts[2] = key[start:]
				return parts[0], parts[1], parts[2]
			}
		}
	}
	parts[idx] = key[start:]
	return parts[0], parts[1], parts[2]
}
