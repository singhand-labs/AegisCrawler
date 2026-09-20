package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	"golang.org/x/time/rate"
)

// workerRateLimiters holds per-worker token buckets.
type workerRateLimiters struct {
	mu       sync.RWMutex
	limiters map[string]*rate.Limiter
	rps      float64
	burst    int
}

// WorkerRateLimitMiddleware enforces a per-worker token bucket for worker-facing
// endpoints. Requests without a workerId pass through unthrottled.
func WorkerRateLimitMiddleware(rps float64, burst int) func(http.Handler) http.Handler {
	if rps <= 0 || burst <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	rl := &workerRateLimiters{
		limiters: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			workerID := extractWorkerID(r)
			if workerID == "" {
				next.ServeHTTP(w, r)
				return
			}

			limiter := rl.limiter(workerID)
			if !limiter.Allow() {
				writeError(w, http.StatusTooManyRequests, "WORKER_RATE_LIMITED", "worker quota exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *workerRateLimiters) limiter(workerID string) *rate.Limiter {
	rl.mu.RLock()
	l, ok := rl.limiters[workerID]
	rl.mu.RUnlock()
	if ok {
		return l
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if l, ok := rl.limiters[workerID]; ok {
		return l
	}
	l = rate.NewLimiter(rate.Limit(rl.rps), rl.burst)
	rl.limiters[workerID] = l
	return l
}

// domainResolver resolves a task to its rule domain. It matches the subset of
// store.Store used by the site rate limiter, allowing tests to inject mocks.
type domainResolver interface {
	GetTaskByID(ctx context.Context, id string) (*models.Task, error)
	GetRuleByID(ctx context.Context, id string) (*models.Rule, error)
}

// cacheEntry holds a cached domain resolution for a single task.
type cacheEntry struct {
	domain    string
	expiresAt time.Time
}

// siteRateLimiters holds per-site token buckets keyed by domain (or taskId fallback).
type siteRateLimiters struct {
	mu          sync.RWMutex
	limiters    map[string]*rate.Limiter
	rps         float64
	burst       int
	store       domainResolver
	cacheMu     sync.RWMutex
	domainCache map[string]cacheEntry
	cacheTTL    time.Duration
}

// SiteRateLimitMiddleware enforces a per-site token bucket keyed by the task's
// target domain. When domain resolution fails, the taskId is used as a fallback key.
func SiteRateLimitMiddleware(rps float64, burst int, s domainResolver, cacheTTL time.Duration) func(http.Handler) http.Handler {
	if rps <= 0 || burst <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	rl := &siteRateLimiters{
		limiters:    make(map[string]*rate.Limiter),
		rps:         rps,
		burst:       burst,
		store:       s,
		domainCache: make(map[string]cacheEntry),
		cacheTTL:    cacheTTL,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rl.siteKey(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			limiter := rl.limiter(key)
			if !limiter.Allow() {
				writeError(w, http.StatusTooManyRequests, "SITE_RATE_LIMITED", "site quota exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *siteRateLimiters) limiter(key string) *rate.Limiter {
	rl.mu.RLock()
	l, ok := rl.limiters[key]
	rl.mu.RUnlock()
	if ok {
		return l
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if l, ok := rl.limiters[key]; ok {
		return l
	}
	l = rate.NewLimiter(rate.Limit(rl.rps), rl.burst)
	rl.limiters[key] = l
	return l
}

// extractWorkerID reads the request body once, restores it, and returns the
// workerId field if present.
func extractWorkerID(r *http.Request) string {
	_, workerID := peekBody(r)
	return workerID
}

// siteKey returns the rate-limit key for a request. It prefers the task's
// target domain and falls back to the taskId when resolution is unavailable.
// Resolved domains are cached for the configured TTL to reduce store lookups.
func (rl *siteRateLimiters) siteKey(r *http.Request) string {
	taskID, _ := peekBody(r)
	if taskID == "" {
		return ""
	}
	if rl.store == nil {
		return taskID
	}

	now := time.Now()
	rl.cacheMu.RLock()
	entry, ok := rl.domainCache[taskID]
	rl.cacheMu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		if entry.domain == "" {
			return taskID
		}
		return entry.domain
	}

	ctx := r.Context()
	task, err := rl.store.GetTaskByID(ctx, taskID)
	if err != nil || task == nil {
		rl.cacheDomain(taskID, "")
		return taskID
	}

	rule, err := rl.store.GetRuleByID(ctx, task.RuleID)
	if err != nil || rule == nil {
		rl.cacheDomain(taskID, "")
		return taskID
	}

	var domain string
	if err := json.Unmarshal(rule.Domain, &domain); err != nil || domain == "" {
		rl.cacheDomain(taskID, "")
		return taskID
	}
	rl.cacheDomain(taskID, domain)
	return domain
}

// cacheDomain stores a resolved domain for the configured TTL. An empty domain
// represents a failed resolution that should fall back to the taskId.
func (rl *siteRateLimiters) cacheDomain(taskID, domain string) {
	if rl.cacheTTL <= 0 {
		return
	}
	rl.cacheMu.Lock()
	defer rl.cacheMu.Unlock()
	rl.domainCache[taskID] = cacheEntry{
		domain:    domain,
		expiresAt: time.Now().Add(rl.cacheTTL),
	}
}

// peekBody reads the JSON body to extract taskId and workerId, then restores
// the body so downstream handlers can decode it again.
func peekBody(r *http.Request) (taskID, workerID string) {
	if r.Body == nil {
		return "", ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var payload struct {
		TaskID   string `json:"taskId"`
		WorkerID string `json:"workerId"`
	}
	// Best-effort parse; ignore errors so malformed bodies reach the handler.
	_ = json.Unmarshal(body, &payload)
	return payload.TaskID, payload.WorkerID
}
