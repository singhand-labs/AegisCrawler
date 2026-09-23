package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write failed: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	return buf.Bytes()
}

func echoBodyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return // status already committed by the middleware (413)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

func TestGzipRequestMiddleware(t *testing.T) {
	const limit = 64 * 1024
	handler := GzipRequestMiddleware(limit)(echoBodyHandler())

	t.Run("decompresses gzip request bodies transparently", func(t *testing.T) {
		payload := []byte(`{"recording":{"version":"2.0.0"}}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings", bytes.NewReader(gzipBytes(t, payload)))
		req.Header.Set("Content-Encoding", "gzip")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.Bytes(); !bytes.Equal(got, payload) {
			t.Fatalf("handler saw %q, want the decompressed payload %q", got, payload)
		}
	})

	t.Run("caps the decompressed stream at the limit", func(t *testing.T) {
		payload := bytes.Repeat([]byte("a"), limit+1024)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings", bytes.NewReader(gzipBytes(t, payload)))
		req.Header.Set("Content-Encoding", "gzip")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
		}
	})

	t.Run("rejects malformed gzip bodies", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings", bytes.NewReader([]byte("not gzip")))
		req.Header.Set("Content-Encoding", "gzip")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("passes plain bodies through untouched", func(t *testing.T) {
		payload := []byte(`{"plain":true}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings", bytes.NewReader(payload))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.Bytes(); !bytes.Equal(got, payload) {
			t.Fatalf("handler saw %q, want %q", got, payload)
		}
	})

	t.Run("composes inside the wire-size cap", func(t *testing.T) {
		composed := MaxBodySizeMiddleware(1024)(GzipRequestMiddleware(limit)(echoBodyHandler()))
		// Decompressed payload is well under the gzip limit, but the wire
		// (compressed) body exceeds the outer 1KB cap: use pseudo-random
		// content so gzip cannot shrink it under the wire limit.
		payload := make([]byte, 8*1024)
		seed := uint64(0x9E3779B97F4A7C15)
		for i := range payload {
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			payload[i] = byte(seed)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/recordings", bytes.NewReader(gzipBytes(t, payload)))
		req.Header.Set("Content-Encoding", "gzip")
		rec := httptest.NewRecorder()

		composed.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d (wire cap)", rec.Code, http.StatusRequestEntityTooLarge)
		}
	})
}

// End-to-end: the recording ingest route accepts a gzip-encoded upload the
// way the extension sends it (Content-Encoding: gzip + JSON body).
func TestRecordingCreateAcceptsGzippedUpload(t *testing.T) {
	f, err := os.CreateTemp("", "gzip-api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	persistence, err := store.New(filepath.Join(t.TempDir(), "gzip-api.db"), "gzip-api-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	cfg := &config.Config{
		RecordingV2Enabled: true, WorkflowV2Enabled: true,
		RecordingMaxDuration: time.Hour, RecordingMaxActions: 500,
		RecordingMaxCompressedBytes: 1024 * 1024, RecordingRetention: 24 * time.Hour,
		MaxRequestBodyBytes: 32 << 20,
		LeaseDuration:       time.Minute, MaxRetries: 3, MaxWorkerTasks: 5,
		RateLimitPerSecond: 1000, RateLimitBurst: 2000,
		WorkerRateLimitPerSecond: 1000, WorkerRateLimitBurst: 2000,
		SiteRateLimitPerSecond: 1000, SiteRateLimitBurst: 2000,
		CircuitBreakerFailureThreshold: 1000, CircuitBreakerFailureWindow: time.Minute,
		CircuitBreakerOpenDuration: time.Minute, AuditActor: "admin",
	}
	handler := NewHandler(persistence, nil, nil, nil, cfg, zap.NewNop())
	server := httptest.NewServer(NewRouter(handler, cfg, zap.NewNop(), NewMetrics()))
	t.Cleanup(server.Close)

	body, _ := json.Marshal(CreateRecordingRequest{Recording: map[string]any{
		"version": "2",
		"events":  []any{map[string]any{"type": "click"}},
		"snapshots": []any{
			map[string]any{"url": "https://example.com", "text": "initial"},
			map[string]any{"url": "https://example.com", "text": "complete"},
		},
	}})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/recordings", bytes.NewReader(gzipBytes(t, body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("expected 201 for gzipped upload, got %d: %s", response.StatusCode, raw)
	}
	var created RecordingResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Recording.SnapshotCount != 2 {
		t.Fatalf("unexpected snapshot count: %d", created.Recording.SnapshotCount)
	}
}
