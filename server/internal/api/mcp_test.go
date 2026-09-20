package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

const mcpTestAdminToken = "mcp-test-admin"

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func newMCPTestServer(t *testing.T, rateLimit float64, burst int) (*httptest.Server, *store.Store) {
	t.Helper()
	file, err := os.CreateTemp("", "mcp-api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	persistence, err := store.New(file.Name(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	cfg := &config.Config{
		MCPEnabled: true, MCPRateLimitPerSecond: rateLimit, MCPRateLimitBurst: burst,
		AdminAPIKey: mcpTestAdminToken, AuditActor: "admin", MaxRetries: 3,
		LeaseDuration: time.Minute, MaxWorkerTasks: 5,
		RateLimitPerSecond: 1000, RateLimitBurst: 2000,
		WorkerRateLimitPerSecond: 1000, WorkerRateLimitBurst: 2000,
		SiteRateLimitPerSecond: 1000, SiteRateLimitBurst: 2000,
		CircuitBreakerFailureThreshold: 1000, CircuitBreakerFailureWindow: time.Minute,
		CircuitBreakerOpenDuration: time.Minute, RequestTimeout: 10 * time.Second,
		MaxRequestBodyBytes: 1024 * 1024,
	}
	logger := zap.NewNop()
	runner := scheduler.New(persistence, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	handler := NewHandler(persistence, runner, nil, nil, cfg, logger)
	server := httptest.NewServer(NewRouter(handler, cfg, logger, NewMetrics()))
	t.Cleanup(server.Close)
	return server, persistence
}

func createMCPTokenForTest(t *testing.T, baseURL string, permissions []string) *MCPTokenResponse {
	t.Helper()
	body, _ := json.Marshal(CreateMCPTokenRequest{Name: "test token", Permissions: permissions})
	request, err := http.NewRequest(http.MethodPost, baseURL+"/admin/mcp/tokens", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+mcpTestAdminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create MCP token returned %d", response.StatusCode)
	}
	var token MCPTokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	if token.Token == "" {
		t.Fatal("expected plaintext token in create response")
	}
	return &token
}

func connectMCPClient(t *testing.T, baseURL, token string) *mcp.ClientSession {
	t.Helper()
	httpClient := &http.Client{Transport: bearerRoundTripper{base: http.DefaultTransport, token: token}}
	client := mcp.NewClient(&mcp.Implementation{Name: "AegisCrawler test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: baseURL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func createApprovedMCPRule(t *testing.T, persistence *store.Store) *models.RuleVersion {
	t.Helper()
	ctx := authz.WithPrincipal(context.Background(), authz.Principal{
		Subject: "admin", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin},
	})
	now := time.Now().UTC()
	rule := &models.Rule{
		ID: "mcp-rule", Version: "v1", Name: "MCP rule", Domain: models.JSON(`"example.com"`),
		URLPattern: models.JSON(`"https://example.com/*"`), Enabled: true,
		Priority: models.PriorityNormal, Entry: "https://example.com", Variables: models.JSON(`{"keyword":"","password":"server-secret"}`),
		Selectors: models.JSON(`{}`), Humanize: models.JSON(`{}`), Steps: models.JSON(`[]`),
		Output:     models.JSON(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
		SendPolicy: models.JSON(`{}`), Hooks: models.JSON(`{}`), Tags: models.JSON(`[]`),
		CreatedAt: now, UpdatedAt: now,
	}
	version, err := persistence.CreateRuleVersion(ctx, rule, "")
	if err != nil {
		t.Fatal(err)
	}
	version, err = persistence.ApproveRuleVersion(ctx, rule.ID, version.Version)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestMCPToolsEnforcePermissionsMaskSecretsAndAudit(t *testing.T) {
	server, persistence := newMCPTestServer(t, 1000, 2000)
	version := createApprovedMCPRule(t, persistence)
	readToken := createMCPTokenForTest(t, server.URL, []string{models.MCPPermissionRead})
	writeToken := createMCPTokenForTest(t, server.URL, []string{models.MCPPermissionRead, models.MCPPermissionWrite})

	var storedHash string
	if err := persistence.DB().QueryRow(`SELECT token_hash FROM mcp_tokens WHERE id = ?`, readToken.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(readToken.Token))
	if storedHash != hex.EncodeToString(digest[:]) || storedHash == readToken.Token {
		t.Fatal("MCP bearer token was not persisted as the expected one-way hash")
	}
	listRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/mcp/tokens", nil)
	listRequest.Header.Set("Authorization", "Bearer "+mcpTestAdminToken)
	listResponse, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	listBody, _ := io.ReadAll(listResponse.Body)
	listResponse.Body.Close()
	if listResponse.StatusCode != http.StatusOK || strings.Contains(string(listBody), readToken.Token) || strings.Contains(string(listBody), storedHash) {
		t.Fatalf("token list leaked credential material: status=%d body=%s", listResponse.StatusCode, listBody)
	}

	readSession := connectMCPClient(t, server.URL, readToken.Token)
	tools, err := readSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	wantNames := []string{"cancel_task", "create_schedule", "create_task", "delete_schedule", "get_rule", "get_task", "get_task_results", "list_rules", "list_schedules", "retry_task", "update_schedule"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("unexpected MCP tools: %v", names)
	}
	listedRules, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_rules", Arguments: map[string]any{}})
	if err != nil || listedRules.IsError {
		t.Fatalf("list_rules failed: result=%+v err=%v", listedRules, err)
	}

	ruleResult, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_rule", Arguments: map[string]any{
		"ruleId": version.RuleID, "version": version.Version,
	}})
	if err != nil || ruleResult.IsError {
		t.Fatalf("get_rule failed: result=%+v err=%v", ruleResult, err)
	}
	ruleOutput := ruleResult.StructuredContent.(map[string]any)
	definition := ruleOutput["definition"].(map[string]any)
	variables := definition["variables"].(map[string]any)
	if variables["password"] != "[REDACTED]" {
		t.Fatalf("rule secret was not redacted: %+v", variables)
	}

	denied, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_task", Arguments: map[string]any{
		"ruleId": version.RuleID, "ruleVersion": version.Version, "inputs": map[string]any{"keyword": "books", "password": "top-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !denied.IsError {
		t.Fatal("expected read-only token to be denied create_task")
	}

	writeSession := connectMCPClient(t, server.URL, writeToken.Token)
	rejectedSecret, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_task", Arguments: map[string]any{
		"ruleId": version.RuleID, "ruleVersion": version.Version, "inputs": map[string]any{"keyword": "books", "password": "top-secret"},
	}})
	if err != nil || !rejectedSecret.IsError {
		t.Fatalf("expected MCP secret input rejection: result=%+v err=%v", rejectedSecret, err)
	}
	created, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_task", Arguments: map[string]any{
		"ruleId": version.RuleID, "ruleVersion": version.Version, "inputs": map[string]any{"keyword": "books"},
	}})
	if err != nil || created.IsError {
		t.Fatalf("create_task failed: result=%+v err=%v", created, err)
	}
	createdOutput := created.StructuredContent.(map[string]any)
	createdInputs := createdOutput["inputs"].(map[string]any)
	if createdInputs["password"] != "[REDACTED]" {
		t.Fatalf("task secret was not masked: %+v", createdInputs)
	}
	taskID, _ := createdOutput["id"].(string)
	if taskID == "" {
		t.Fatal("create_task did not return a task id")
	}

	got, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_task", Arguments: map[string]any{"taskId": taskID}})
	if err != nil || got.IsError {
		t.Fatalf("get_task failed: result=%+v err=%v", got, err)
	}
	gotInputs := got.StructuredContent.(map[string]any)["inputs"].(map[string]any)
	if gotInputs["password"] != "[REDACTED]" {
		t.Fatalf("stored task secret was not masked: %+v", gotInputs)
	}
	invalidCall, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_task", Arguments: map[string]any{"taskId": 123}})
	if err == nil || invalidCall != nil {
		t.Fatalf("expected invalid get_task arguments to be a protocol validation error: result=%+v err=%v", invalidCall, err)
	}
	results, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_task_results", Arguments: map[string]any{"taskId": taskID}})
	if err != nil || results.IsError {
		t.Fatalf("get_task_results failed: result=%+v err=%v", results, err)
	}
	if total, _ := results.StructuredContent.(map[string]any)["total"].(float64); total != 0 {
		t.Fatalf("new task unexpectedly had results: %+v", results.StructuredContent)
	}
	cancelled, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "cancel_task", Arguments: map[string]any{"taskId": taskID}})
	if err != nil || cancelled.IsError {
		t.Fatalf("cancel_task failed: result=%+v err=%v", cancelled, err)
	}
	retried, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "retry_task", Arguments: map[string]any{"taskId": taskID}})
	if err != nil || retried.IsError {
		t.Fatalf("retry_task failed: result=%+v err=%v", retried, err)
	}

	createdSchedule, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_schedule", Arguments: map[string]any{
		"ruleId": version.RuleID, "ruleVersion": version.Version, "name": "Daily collection",
		"type": "cron", "expression": "0 9 * * *", "timezone": "UTC", "inputs": map[string]any{"keyword": "books"},
	}})
	if err != nil || createdSchedule.IsError {
		t.Fatalf("create_schedule failed: result=%+v err=%v", createdSchedule, err)
	}
	scheduleID, _ := createdSchedule.StructuredContent.(map[string]any)["id"].(string)
	if scheduleID == "" {
		t.Fatal("create_schedule did not return an id")
	}
	listedSchedules, err := readSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_schedules", Arguments: map[string]any{"ruleId": version.RuleID}})
	if err != nil || listedSchedules.IsError {
		t.Fatalf("list_schedules failed: result=%+v err=%v", listedSchedules, err)
	}
	updatedSchedule, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "update_schedule", Arguments: map[string]any{
		"scheduleId": scheduleID, "name": "Updated daily collection", "enabled": false,
	}})
	if err != nil || updatedSchedule.IsError {
		t.Fatalf("update_schedule failed: result=%+v err=%v", updatedSchedule, err)
	}
	deletedSchedule, err := writeSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "delete_schedule", Arguments: map[string]any{"scheduleId": scheduleID}})
	if err != nil || deletedSchedule.IsError {
		t.Fatalf("delete_schedule failed: result=%+v err=%v", deletedSchedule, err)
	}

	ctx := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "admin", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin}})
	logs, total, err := persistence.ListAuditLogs(ctx, store.ListAuditLogsFilter{Action: "mcp.create_task", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(logs) != 3 {
		t.Fatalf("expected denied, rejected-secret, and successful create_task audits, total=%d logs=%d", total, len(logs))
	}
	_, total, err = persistence.ListAuditLogs(ctx, store.ListAuditLogsFilter{Action: "mcp.get_task", Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("expected successful and schema-invalid get_task audits, total=%d err=%v", total, err)
	}
}

func TestMCPTaskLineageReadersFailClosedAndPreserveLegacyTasks(t *testing.T) {
	server, persistence := newMCPTestServer(t, 1000, 2000)
	version := createApprovedMCPRule(t, persistence)
	ctx := context.Background()
	now := time.Now().UTC()
	versionedTask := &models.Task{
		ID: store.NewID(), RuleID: version.RuleID, RuleVersionNumber: version.Version,
		Variables: models.JSON(`{"keyword":"books"}`), Status: models.TaskStatusPending,
		Priority: models.PriorityNormal, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := persistence.CreateTask(ctx, versionedTask); err != nil {
		t.Fatal(err)
	}
	mismatchedTask := &models.Task{
		ID: store.NewID(), RuleID: version.RuleID, RuleVersionNumber: version.Version,
		Variables: models.JSON(`{"keyword":"books"}`), Status: models.TaskStatusPending,
		Priority: models.PriorityNormal, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := persistence.CreateTask(ctx, mismatchedTask); err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.DB().Exec(`DROP TRIGGER trg_versioned_task_contract_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.DB().Exec(`
		UPDATE tasks SET rule_version_number = 999
		WHERE workspace_id = ? AND id = ?
	`, mismatchedTask.WorkspaceID, mismatchedTask.ID); err != nil {
		t.Fatal(err)
	}
	mismatchedTask.RuleVersionNumber = 999

	legacyRule := &models.Rule{
		ID: "mcp-legacy-lineage", Version: "1.0.0", Name: "MCP legacy lineage",
		Domain: models.JSON(`"example.com"`), Steps: models.JSON(`[]`),
		Enabled: true, Priority: models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved), CreatedAt: now, UpdatedAt: now,
	}
	if err := persistence.CreateRule(ctx, legacyRule); err != nil {
		t.Fatal(err)
	}
	legacyTask := &models.Task{
		ID: store.NewID(), RuleID: legacyRule.ID, RuleVersion: legacyRule.Version,
		Status: models.TaskStatusPending, Priority: models.PriorityNormal,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := persistence.CreateTask(ctx, legacyTask); err != nil {
		t.Fatal(err)
	}

	if _, err := persistence.DB().Exec(`DROP TRIGGER trg_rule_version_contracts_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.DB().Exec(`
		DELETE FROM rule_version_contracts
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, versionedTask.WorkspaceID, versionedTask.RuleID, versionedTask.RuleVersionNumber); err != nil {
		t.Fatal(err)
	}

	readToken := createMCPTokenForTest(t, server.URL, []string{models.MCPPermissionRead})
	session := connectMCPClient(t, server.URL, readToken.Token)
	for _, task := range []*models.Task{versionedTask, mismatchedTask} {
		for _, call := range []mcp.CallToolParams{
			{Name: "get_task", Arguments: map[string]any{"taskId": task.ID}},
			{Name: "get_task_results", Arguments: map[string]any{"taskId": task.ID}},
		} {
			result, err := session.CallTool(ctx, &call)
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("%s did not fail closed for task %s: result=%+v err=%v", call.Name, task.ID, result, err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(encoded, []byte("MCP tool failed")) || bytes.Contains(encoded, []byte("rule version not found")) {
				t.Fatalf("%s leaked or changed its internal contract error for task %s: %s", call.Name, task.ID, encoded)
			}
		}
	}

	for _, call := range []mcp.CallToolParams{
		{Name: "get_task", Arguments: map[string]any{"taskId": legacyTask.ID}},
		{Name: "get_task_results", Arguments: map[string]any{"taskId": legacyTask.ID}},
	} {
		result, err := session.CallTool(ctx, &call)
		if err != nil || result == nil || result.IsError {
			t.Fatalf("%s rejected a true legacy task: result=%+v err=%v", call.Name, result, err)
		}
		output, ok := result.StructuredContent.(map[string]any)
		if !ok || output["sourceAuthority"] != nil {
			t.Fatalf("%s invented lineage for a true legacy task: %+v", call.Name, result.StructuredContent)
		}
	}
}

func TestMCPWorkspaceIsolationAndRevocation(t *testing.T) {
	server, persistence := newMCPTestServer(t, 1000, 2000)
	readToken := createMCPTokenForTest(t, server.URL, []string{models.MCPPermissionRead})
	defaultCtx := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "admin", WorkspaceID: authz.DefaultWorkspaceID, Roles: []authz.Role{authz.RoleAdmin}})
	if err := persistence.CreateWorkspace(defaultCtx, &models.Workspace{ID: "tenant-b", Name: "Tenant B"}); err != nil {
		t.Fatal(err)
	}
	tenantCtx := authz.WithPrincipal(context.Background(), authz.Principal{Subject: "tenant-admin", WorkspaceID: "tenant-b", Roles: []authz.Role{authz.RoleAdmin}})
	now := time.Now().UTC()
	if err := persistence.CreateRule(tenantCtx, &models.Rule{ID: "tenant-rule", Version: "1", Name: "Tenant rule",
		Domain: models.JSON(`"example.com"`), Steps: models.JSON(`[]`), Priority: models.PriorityNormal, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := persistence.CreateTask(tenantCtx, &models.Task{ID: "tenant-task", RuleID: "tenant-rule", RuleVersion: "1",
		Status: models.TaskStatusPending, Priority: models.PriorityNormal, Variables: models.JSON(`{}`), MaxRetries: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	session := connectMCPClient(t, server.URL, readToken.Token)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_task", Arguments: map[string]any{"taskId": "tenant-task"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("default-workspace MCP token accessed a tenant task")
	}

	request, _ := http.NewRequest(http.MethodDelete, server.URL+"/admin/mcp/tokens/"+readToken.ID, nil)
	request.Header.Set("Authorization", "Bearer "+mcpTestAdminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke returned %d", response.StatusCode)
	}

	initialize := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/mcp", bytes.NewReader(initialize))
	request.Header.Set("Authorization", "Bearer "+readToken.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token returned %d, want 401", response.StatusCode)
	}
}

func TestMCPRejectsQueryTokensAndRateLimitsPerToken(t *testing.T) {
	server, _ := newMCPTestServer(t, 0.0001, 1)
	token := createMCPTokenForTest(t, server.URL, []string{models.MCPPermissionRead})
	initialize := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)

	queryRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp?access_token="+token.Token, bytes.NewReader(initialize))
	queryResponse, err := http.DefaultClient.Do(queryRequest)
	if err != nil {
		t.Fatal(err)
	}
	queryResponse.Body.Close()
	if queryResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("query token returned %d, want 400", queryResponse.StatusCode)
	}

	doInitialize := func() int {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", bytes.NewReader(initialize))
		request.Header.Set("Authorization", "Bearer "+token.Token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if status := doInitialize(); status != http.StatusOK {
		t.Fatalf("first request returned %d", status)
	}
	if status := doInitialize(); status != http.StatusTooManyRequests {
		t.Fatalf("second request returned %d, want 429", status)
	}
}

func TestMCPFeatureFlagOnlyControlsTransport(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()
	body := bytes.NewBufferString(`{"name":"pre-provisioned","permissions":["read"]}`)
	response, err := http.Post(server.URL+"/admin/mcp/tokens", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("token administration returned %d while MCP transport was disabled", response.StatusCode)
	}

	initialize := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	response, err = http.Post(server.URL+"/mcp", "application/json", initialize)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled MCP transport returned %d, want 404", response.StatusCode)
	}
}
