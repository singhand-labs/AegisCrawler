package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

func TestCircuitBreakerOpensAfterFailures(t *testing.T) {
	cfg := &config.Config{
		CircuitBreakerFailureThreshold: 3,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     50 * time.Millisecond,
	}

	failures := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failures++
		w.WriteHeader(http.StatusInternalServerError)
	})

	cb := CircuitBreakerMiddleware(cfg)(handler)

	// Three failures should exceed the threshold and open the circuit.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		cb.ServeHTTP(rec, httptest.NewRequest("GET", "/test", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	}

	// The next request should be rejected with 503 without calling the handler.
	rec := httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/test", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if failures != 3 {
		t.Fatalf("expected handler to be called 3 times, got %d", failures)
	}
}

func TestCircuitBreakerRecoversAfterTimeout(t *testing.T) {
	cfg := &config.Config{
		CircuitBreakerFailureThreshold: 2,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     30 * time.Millisecond,
	}

	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	cb := CircuitBreakerMiddleware(cfg)(handler)

	// Open the circuit with two failures.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		cb.ServeHTTP(rec, httptest.NewRequest("GET", "/recover", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	}

	// Circuit should be open.
	rec := httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/recover", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	// Wait for half-open, then the next successful request should close it.
	time.Sleep(cfg.CircuitBreakerOpenDuration + 5*time.Millisecond)

	rec = httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/recover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 in half-open, got %d", rec.Code)
	}

	// Subsequent requests should continue to succeed.
	rec = httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/recover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after recovery, got %d", rec.Code)
	}

	if callCount != 4 {
		t.Fatalf("expected handler to be called 4 times, got %d", callCount)
	}
}

func TestCircuitBreakerPrunesStaleFailures(t *testing.T) {
	cfg := &config.Config{
		CircuitBreakerFailureThreshold: 3,
		CircuitBreakerFailureWindow:    100 * time.Millisecond,
		CircuitBreakerOpenDuration:     50 * time.Millisecond,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	cb := CircuitBreakerMiddleware(cfg)(handler)

	// Two failures inside the window should not open the circuit.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		cb.ServeHTTP(rec, httptest.NewRequest("GET", "/prune", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	}

	// Wait for the failure window to elapse so the failures become stale.
	time.Sleep(cfg.CircuitBreakerFailureWindow + 10*time.Millisecond)

	// A third failure now should still be within threshold because stale
	// failures were pruned; the circuit must remain closed.
	rec := httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/prune", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after pruning stale failures, got %d", rec.Code)
	}
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	cfg := &config.Config{
		CircuitBreakerFailureThreshold: 2,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     30 * time.Millisecond,
	}

	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
	})

	cb := CircuitBreakerMiddleware(cfg)(handler)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		cb.ServeHTTP(rec, httptest.NewRequest("GET", "/reopen", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/reopen", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	time.Sleep(cfg.CircuitBreakerOpenDuration + 5*time.Millisecond)

	// Half-open trial fails; circuit should reopen.
	rec = httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/reopen", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 in half-open, got %d", rec.Code)
	}

	// Circuit should be open again.
	rec = httptest.NewRecorder()
	cb.ServeHTTP(rec, httptest.NewRequest("GET", "/reopen", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after reopen, got %d", rec.Code)
	}

	if callCount != 3 {
		t.Fatalf("expected handler to be called 3 times, got %d", callCount)
	}
}
