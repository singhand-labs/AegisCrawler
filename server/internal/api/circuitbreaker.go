package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

// circuitBreakerState represents the three states of a circuit breaker.
type circuitBreakerState int

const (
	stateClosed circuitBreakerState = iota
	stateOpen
	stateHalfOpen
)

// CircuitBreakerMiddleware creates a per-endpoint circuit breaker. It tracks
// failures over a sliding window and returns 503 while the circuit is open.
func CircuitBreakerMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	if cfg.CircuitBreakerFailureThreshold <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	cb := &circuitBreaker{
		threshold:    cfg.CircuitBreakerFailureThreshold,
		window:       cfg.CircuitBreakerFailureWindow,
		openDuration: cfg.CircuitBreakerOpenDuration,
		endpoints:    make(map[string]*circuitEndpoint),
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pattern := r.Pattern
			if pattern == "" {
				pattern = r.URL.Path
			}

			ep := cb.endpoint(pattern)
			allow, rw := ep.allow(w, r)
			if !allow {
				writeError(w, http.StatusServiceUnavailable, "CIRCUIT_OPEN", "circuit breaker is open")
				return
			}

			next.ServeHTTP(rw, r)
			ep.record(rw.statusCode)
		})
	}
}

type circuitBreaker struct {
	mu           sync.Mutex
	threshold    int
	window       time.Duration
	openDuration time.Duration
	endpoints    map[string]*circuitEndpoint
}

func (cb *circuitBreaker) endpoint(pattern string) *circuitEndpoint {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if ep, ok := cb.endpoints[pattern]; ok {
		return ep
	}
	ep := &circuitEndpoint{
		threshold:    cb.threshold,
		window:       cb.window,
		openDuration: cb.openDuration,
	}
	cb.endpoints[pattern] = ep
	return ep
}

type circuitEndpoint struct {
	mu              sync.Mutex
	state           circuitBreakerState
	failures        []time.Time
	openedAt        time.Time
	halfOpenAllowed bool
	threshold       int
	window          time.Duration
	openDuration    time.Duration
}

// allow decides whether a request may proceed. It returns a wrapped
// responseWriter so the outcome can be observed.
func (ep *circuitEndpoint) allow(w http.ResponseWriter, r *http.Request) (bool, *responseWriter) {
	now := time.Now().UTC()

	ep.mu.Lock()
	defer ep.mu.Unlock()

	switch ep.state {
	case stateClosed:
		ep.pruneFailures(now)
		if len(ep.failures) >= ep.threshold {
			ep.open(now)
			return false, nil
		}
		return true, &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	case stateOpen:
		if now.Sub(ep.openedAt) >= ep.openDuration {
			ep.state = stateHalfOpen
			ep.halfOpenAllowed = true
		} else {
			return false, nil
		}
		fallthrough

	case stateHalfOpen:
		if !ep.halfOpenAllowed {
			return false, nil
		}
		ep.halfOpenAllowed = false
		return true, &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
	}

	return true, &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

// record updates the circuit breaker state based on the response status code.
func (ep *circuitEndpoint) record(statusCode int) {
	now := time.Now().UTC()

	ep.mu.Lock()
	defer ep.mu.Unlock()

	failure := statusCode >= http.StatusInternalServerError

	switch ep.state {
	case stateClosed:
		if failure {
			ep.failures = append(ep.failures, now)
			ep.pruneFailures(now)
			if len(ep.failures) >= ep.threshold {
				ep.open(now)
			}
		}

	case stateHalfOpen:
		if failure {
			ep.open(now)
		} else {
			ep.state = stateClosed
			ep.failures = ep.failures[:0]
		}
	}
}

func (ep *circuitEndpoint) pruneFailures(now time.Time) {
	cutoff := now.Add(-ep.window)
	idx := len(ep.failures)
	for i, t := range ep.failures {
		if t.After(cutoff) || t.Equal(cutoff) {
			idx = i
			break
		}
	}
	ep.failures = ep.failures[idx:]
}

func (ep *circuitEndpoint) open(now time.Time) {
	ep.state = stateOpen
	ep.openedAt = now
	ep.halfOpenAllowed = false
}
