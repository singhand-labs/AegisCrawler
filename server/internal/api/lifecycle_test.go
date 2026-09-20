package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func newLifecycleServer(t *testing.T, encryptionKey string) (*httptest.Server, *store.Store) {
	t.Helper()
	persistence, err := store.New(filepath.Join(t.TempDir(), "lifecycle.db"), encryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		RecordingV2Enabled: true, WorkflowV2Enabled: true,
		RecordingMaxDuration: time.Hour, RecordingMaxActions: 500,
		RecordingMaxCompressedBytes: 1024 * 1024, RecordingRetention: 24 * time.Hour,
		LeaseDuration: time.Minute, MaxRetries: 3, MaxWorkerTasks: 5,
		RateLimitPerSecond: 1000, RateLimitBurst: 2000,
		WorkerRateLimitPerSecond: 1000, WorkerRateLimitBurst: 2000,
		SiteRateLimitPerSecond: 1000, SiteRateLimitBurst: 2000,
		CircuitBreakerFailureThreshold: 1000, CircuitBreakerFailureWindow: time.Minute,
		CircuitBreakerOpenDuration: time.Minute, AuditActor: "admin",
	}
	logger := zap.NewNop()
	handler := NewHandler(persistence, nil, nil, nil, cfg, logger)
	server := httptest.NewServer(NewRouter(handler, cfg, logger, NewMetrics()))
	t.Cleanup(func() {
		server.Close()
		_ = persistence.Close()
	})
	return server, persistence
}

func lifecycleRecordingPayload() map[string]any {
	return map[string]any{
		"version": "2",
		"events":  []any{map[string]any{"type": "input", "value": "token=secret-value"}},
		"snapshots": []any{
			map[string]any{"url": "https://example.com", "type": "password", "value": "password-value", "outerHTML": "<input>"},
			map[string]any{"url": "https://example.com", "text": "complete"},
		},
	}
}

func TestRecordingLifecycleAPI(t *testing.T) {
	server, persistence := newLifecycleServer(t, "api-recording-encryption-key")
	body, _ := json.Marshal(CreateRecordingRequest{Recording: lifecycleRecordingPayload()})
	response, err := http.Post(server.URL+"/api/v1/recordings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", response.StatusCode)
	}
	var created RecordingResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Recording.Owner != "admin" || created.Recording.ActionCount != 1 || created.Recording.SnapshotCount != 2 || created.Recording.Payload != nil {
		t.Fatalf("unexpected create response: %+v", created.Recording)
	}

	response, err = http.Get(server.URL + "/api/v1/recordings/" + created.Recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	var got RecordingResponse
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(got.Recording.Payload)
	if strings.Contains(string(serialized), "secret-value") || strings.Contains(string(serialized), "password-value") || strings.Contains(string(serialized), "outerHTML") {
		t.Fatalf("API returned unsafe recording content: %s", serialized)
	}

	request, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/recordings/"+created.Recording.ID, nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected delete 200, got %d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/api/v1/recordings/" + created.Recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusGone {
		t.Fatalf("expected deleted recording 410, got %d", response.StatusCode)
	}

	audit, total, err := persistence.ListAuditLogs(context.Background(), store.ListAuditLogsFilter{ResourceType: "recording"})
	if err != nil || total != 2 || len(audit) != 2 {
		t.Fatalf("expected create/delete audit lineage: total=%d audit=%v err=%v", total, audit, err)
	}
}

func TestRecordingAPIRequiresConfiguredEncryption(t *testing.T) {
	server, _ := newLifecycleServer(t, "")
	body, _ := json.Marshal(CreateRecordingRequest{Recording: lifecycleRecordingPayload()})
	response, err := http.Post(server.URL+"/api/v1/recordings", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without encryption, got %d", response.StatusCode)
	}
}

func TestRuleVersionLifecycleAPI(t *testing.T) {
	server, _ := newLifecycleServer(t, "api-rule-version-key")
	now := time.Now().UTC()
	rule := models.Rule{
		ID: "version-api-rule", Version: "1.0.0", Name: "immutable v1", Domain: store.JSON("example.com"),
		Steps: store.JSON([]any{"step"}), Priority: models.PriorityNormal, Owner: "spoofed",
		CreatedAt: now, UpdatedAt: now,
	}
	body, _ := json.Marshal(CreateRuleVersionRequest{Rule: rule})
	response, err := http.Post(server.URL+"/admin/rules/version-api-rule/versions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("expected version create 201, got %d", response.StatusCode)
	}
	var created RuleVersionResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.RuleVersion.Status != models.RuleApprovalPending || created.RuleVersion.Version != 1 || created.RuleVersion.Owner != "admin" {
		t.Fatalf("unexpected created version: %+v", created.RuleVersion)
	}
	if created.Contract == nil || created.Contract.Version != 1 || len(created.Contract.InputSchema) == 0 || len(created.Contract.OutputSchema) == 0 {
		t.Fatalf("missing created version contract: %+v", created.Contract)
	}

	approveURL := server.URL + "/admin/rules/version-api-rule/versions/1/approve"
	response, err = http.Post(approveURL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected approval 200, got %d", response.StatusCode)
	}
	response, err = http.Post(approveURL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("expected repeated approval 409, got %d", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/admin/rules/version-api-rule/versions")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var versions ListRuleVersionsResponse
	if err := json.NewDecoder(response.Body).Decode(&versions); err != nil {
		t.Fatal(err)
	}
	if len(versions.RuleVersions) != 1 || versions.RuleVersions[0].Status != models.RuleApprovalApproved {
		t.Fatalf("unexpected version list: %+v", versions.RuleVersions)
	}
	if len(versions.Contracts) != 1 || versions.Contracts[0].Version != versions.RuleVersions[0].Version {
		t.Fatalf("unexpected version contracts: %+v", versions.Contracts)
	}

	legacyBody, _ := json.Marshal(rule)
	response, err = http.Post(server.URL+"/admin/rules", "application/json", bytes.NewReader(legacyBody))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("expected legacy mutation guard 409, got %d", response.StatusCode)
	}
}
