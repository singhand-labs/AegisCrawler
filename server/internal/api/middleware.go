package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

const traceIDHeader = "X-Trace-Id"

// generateTraceID returns a random 16-byte hex string.
func generateTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// LoggingMiddleware logs requests and propagates trace IDs.
func LoggingMiddleware(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			traceID := r.Header.Get(traceIDHeader)
			if traceID == "" {
				traceID = generateTraceID()
			}
			w.Header().Set(traceIDHeader, traceID)

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r)
			logger.Info("http request",
				zap.String("traceId", traceID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rw.statusCode),
				zap.Duration("duration", time.Since(start)),
				zap.String("remote", r.RemoteAddr),
			)
		})
	}
}

// RateLimitMiddleware enforces a global rate limit.
func RateLimitMiddleware(rps float64, burst int) func(http.Handler) http.Handler {
	limiter := rate.NewLimiter(rate.Limit(rps), burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// adminRateLimiters holds per-admin-key token buckets.
type adminRateLimiters struct {
	mu       sync.RWMutex
	limiters map[string]*rate.Limiter
	rps      float64
	burst    int
}

func (rl *adminRateLimiters) limiter(key string) *rate.Limiter {
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

// AdminEnhanceRateLimitMiddleware enforces a per-admin-key rate limit on the
// LLM rule enhancement endpoint. The key is derived from the Authorization
// header so different admin keys receive independent quotas.
func AdminEnhanceRateLimitMiddleware(rps float64, burst int) func(http.Handler) http.Handler {
	if rps <= 0 || burst <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	rl := &adminRateLimiters{
		limiters: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Authorization")
			if key == "" {
				key = "anonymous"
			}
			limiter := rl.limiter(key)
			if !limiter.Allow() {
				writeError(w, http.StatusTooManyRequests, "LLM_RATE_LIMITED", "enhance quota exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RecoveryMiddleware recovers from panics.
func RecoveryMiddleware(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered", zap.Any("recover", rec))
					writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// safeResponseWriter wraps http.ResponseWriter so that the first WriteHeader
// call wins. This lets middleware commit an error status (e.g. 413 or 504)
// even if the handler later tries to write a different status.
type safeResponseWriter struct {
	http.ResponseWriter
	once sync.Once
}

func (w *safeResponseWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.ResponseWriter.WriteHeader(code)
	})
}

func (w *safeResponseWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

// limitedBody wraps a request body and commits 413 as soon as the underlying
// MaxBytesReader reports that the limit was exceeded.
type limitedBody struct {
	io.ReadCloser
	w        *safeResponseWriter
	exceeded bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) && !b.exceeded {
		b.exceeded = true
		b.w.WriteHeader(http.StatusRequestEntityTooLarge)
	}
	return n, err
}

// MaxBodySizeMiddleware limits the size of incoming request bodies using
// http.MaxBytesReader. When a client sends more than limit bytes in the body,
// the middleware responds with 413 Payload Too Large.
func MaxBodySizeMiddleware(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit <= 0 || r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			srw := &safeResponseWriter{ResponseWriter: w}
			r.Body = &limitedBody{
				ReadCloser: http.MaxBytesReader(srw, r.Body, limit),
				w:          srw,
			}
			next.ServeHTTP(srw, r)
		})
	}
}

// recordingResponseWriter buffers status code, headers, and body so the
// request timeout middleware can capture the full handler response before
// deciding whether to copy it to the real ResponseWriter.
type recordingResponseWriter struct {
	code        int
	header      http.Header
	body        *bytes.Buffer
	wroteHeader bool
}

func newRecordingResponseWriter() *recordingResponseWriter {
	return &recordingResponseWriter{
		code:   http.StatusOK,
		header: make(http.Header),
		body:   &bytes.Buffer{},
	}
}

func (r *recordingResponseWriter) Header() http.Header { return r.header }

func (r *recordingResponseWriter) WriteHeader(code int) {
	if !r.wroteHeader {
		r.code = code
		r.wroteHeader = true
	}
}

func (r *recordingResponseWriter) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(p)
}

// RequestTimeoutMiddleware enforces a per-request timeout. The handler runs in
// a goroutine that writes into an in-memory response recorder. The parent
// goroutine copies the buffered response to the real ResponseWriter when the
// handler finishes before the timeout, or writes 504 Gateway Timeout when the
// timeout fires first. Panics inside the handler goroutine are recovered,
// logged, and converted into a 500 response.
func RequestTimeoutMiddleware(timeout time.Duration, logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if timeout <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			effectiveTimeout := timeout
			if deadline, ok := r.Context().Deadline(); ok {
				if remaining := time.Until(deadline); remaining < effectiveTimeout {
					effectiveTimeout = remaining
				}
			}

			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()

			timer := time.NewTimer(effectiveTimeout)
			defer timer.Stop()

			rec := newRecordingResponseWriter()
			done := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					// Recover here because the handler runs in a separate
					// goroutine, so a panic cannot be caught by the outer
					// RecoveryMiddleware.
					if p := recover(); p != nil {
						logger.Error("handler panic in RequestTimeoutMiddleware",
							zap.Any("panic", p),
							zap.String("stack", string(debug.Stack())),
						)
						writeError(rec, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
					}
					close(done)
				}()
				next.ServeHTTP(rec, r.WithContext(ctx))
			}()

			select {
			case <-done:
				if !timer.Stop() {
					// The timer fired concurrently with handler completion.
					// Treat this as a timeout to avoid returning a 200 OK empty
					// response when the request already timed out.
					select {
					case <-timer.C:
					default:
					}
					writeError(w, http.StatusGatewayTimeout, "GATEWAY_TIMEOUT", "gateway timeout")
					break
				}
				copyRecorderToResponseWriter(w, rec)
			case <-timer.C:
				cancel()
				writeError(w, http.StatusGatewayTimeout, "GATEWAY_TIMEOUT", "gateway timeout")
				// Wait a short grace period for the handler goroutine to finish
				// before returning, reducing request-use-after-return risk.
				grace := make(chan struct{})
				go func() { defer close(grace); wg.Wait() }()
				select {
				case <-grace:
				case <-time.After(time.Second):
				}
			}
		})
	}
}

func copyRecorderToResponseWriter(w http.ResponseWriter, rec *recordingResponseWriter) {
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	code := rec.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	if rec.body != nil {
		_, _ = w.Write(rec.body.Bytes())
	}
}
