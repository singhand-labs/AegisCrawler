package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func newTestServerWithLLM(t *testing.T) (*httptest.Server, *store.Store, *llm.JobManager) {
	t.Helper()
	f, err := os.CreateTemp("", "api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
		AuditActor:                     "admin",
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	jm := llm.NewJobManager(s, cfg, nil, logger)
	h := NewHandler(s, sch, jm, nil, cfg, logger)
	metrics := NewMetrics()
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	return httptest.NewServer(router), s, jm
}

func TestSubmitLogEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	body, _ := json.Marshal(LogRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Level:    "info",
		Message:  "hello",
		Extra:    map[string]any{"foo": "bar"},
	})
	resp, err := http.Post(srv.URL+"/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Invalid JSON.
	resp, err = http.Post(srv.URL+"/logs", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetTaskLogsEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	body, _ := json.Marshal(LogRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Level:    "info",
		Message:  "hello",
	})
	resp, err := http.Post(srv.URL+"/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/tasks/" + taskID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var logs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
}

func TestSubmitSnapshotEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	body, _ := json.Marshal(SnapshotRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Name:     "error-screenshot",
		Type:     "html",
		Data:     "<html></html>",
	})
	resp, err := http.Post(srv.URL+"/snapshots", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Invalid JSON.
	resp, err = http.Post(srv.URL+"/snapshots", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetLLMJobEndpoint(t *testing.T) {
	srv, st, jm := newTestServerWithLLM(t)
	defer srv.Close()

	job := &models.LLMJob{
		ID:        store.NewID(),
		RuleID:    "rule-1",
		Baseline:  models.JSON(`{"id":"rule-1"}`),
		Recording: models.JSON(`{}`),
		Status:    string(models.LLMJobStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateLLMJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/admin/rules/enhancements/jobs/" + job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got models.LLMJob
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != job.ID {
		t.Fatalf("expected job %s, got %s", job.ID, got.ID)
	}

	// Missing job returns 404.
	resp, err = http.Get(srv.URL + "/admin/rules/enhancements/jobs/missing-job")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	_ = jm
}

func TestRejectRuleEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "reject-rule",
		Version:        "1.0.0",
		Name:           "Reject Me",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalPending),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:        store.NewID(),
		RuleID:    rule.ID,
		Baseline:  models.JSON(`{"id":"reject-rule"}`),
		Enhanced:  models.JSON(`{"id":"reject-rule"}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Post(srv.URL+"/admin/rules/reject-rule/enhancement/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Missing rule returns 404.
	resp, err = http.Post(srv.URL+"/admin/rules/missing-rule/enhancement/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestApproveRuleEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "approve-rule",
		Version:        "1.0.0",
		Name:           "Approve Me",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalPending),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Post(srv.URL+"/admin/rules/approve-rule/approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Missing rule returns 404.
	resp, err = http.Post(srv.URL+"/admin/rules/missing-rule/approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestCreateRuleValidationAndGetTask(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Invalid JSON.
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Get missing task.
	resp, err = http.Get(srv.URL + "/tasks/missing-task")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSubmitResultValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Invalid JSON.
	resp, err := http.Post(srv.URL+"/results", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing required fields.
	body, _ := json.Marshal(ResultRequest{TaskID: "x"})
	resp, err = http.Post(srv.URL+"/results", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSubmitHeartbeatValidationAndLeaseConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Invalid JSON.
	resp, err := http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing fields.
	body, _ := json.Marshal(HeartbeatRequest{TaskID: "x"})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Lease conflict for non-existent task leased to another worker.
	taskID := createRuleAndTask(t, srv)
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	hbBody, _ := json.Marshal(HeartbeatRequest{TaskID: taskID, WorkerID: "worker-2"})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(hbBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 lease conflict, got %d", resp.StatusCode)
	}
}

func TestSubmitStatusValidationAndTerminalStates(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Invalid JSON.
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing fields.
	body, _ := json.Marshal(StatusRequest{TaskID: taskID})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Terminal status transitions use distinct attempts; a terminal task cannot
	// be rewritten into another terminal state.
	for i, status := range []string{string(models.TaskStatusDone), string(models.TaskStatusFailed)} {
		if i > 0 {
			taskID = createRuleAndTask(t, srv)
			claimBody, _ = json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
			resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		}
		body, _ = json.Marshal(StatusRequest{TaskID: taskID, WorkerID: "worker-1", Status: status, Message: status})
		resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", status, resp.StatusCode)
		}

		got, err := st.GetTaskByID(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != models.TaskStatus(status) {
			t.Fatalf("expected %s, got %s", status, got.Status)
		}
	}
}

func TestSubmitCheckpointValidationAndGetCheckpointNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Invalid JSON.
	resp, err := http.Post(srv.URL+"/tasks/x/checkpoints", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing checkpoint returns 404.
	resp, err = http.Get(srv.URL + "/tasks/x/checkpoints/latest")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestListTasksEndpointValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createRuleAndTask(t, srv)

	cases := []struct {
		query    string
		wantCode int
	}{
		{"limit=notanumber", http.StatusBadRequest},
		{"offset=-1", http.StatusBadRequest},
		{"created_after=not-a-time", http.StatusBadRequest},
		{"created_before=not-a-time", http.StatusBadRequest},
		{"limit=1&offset=0", http.StatusOK},
	}

	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/admin/tasks?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("query %q: expected %d, got %d", tc.query, tc.wantCode, resp.StatusCode)
		}
	}
}

func TestListAuditLogsEndpointValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createRuleAndTask(t, srv)

	cases := []struct {
		query    string
		wantCode int
	}{
		{"limit=bad", http.StatusBadRequest},
		{"offset=-1", http.StatusBadRequest},
		{"created_after=bad", http.StatusBadRequest},
		{"created_before=bad", http.StatusBadRequest},
		{"limit=1&offset=0", http.StatusOK},
	}

	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/admin/audit_logs?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("query %q: expected %d, got %d", tc.query, tc.wantCode, resp.StatusCode)
		}
	}
}

func TestCreateScheduleValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-val-rule")

	cases := []struct {
		name string
		req  CreateScheduleRequest
	}{
		{"missing ruleId", CreateScheduleRequest{Type: string(models.ScheduleTypeOnce), Expression: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}},
		{"missing expression", CreateScheduleRequest{RuleID: "sched-val-rule", Type: string(models.ScheduleTypeOnce)}},
		{"bad type", CreateScheduleRequest{RuleID: "sched-val-rule", Type: "daily", Expression: "x"}},
		{"bad once expression", CreateScheduleRequest{RuleID: "sched-val-rule", Type: string(models.ScheduleTypeOnce), Expression: "not-a-time"}},
		{"bad cron expression", CreateScheduleRequest{RuleID: "sched-val-rule", Type: string(models.ScheduleTypeCron), Expression: "invalid"}},
		{"bad catchup", CreateScheduleRequest{RuleID: "sched-val-rule", Type: string(models.ScheduleTypeOnce), Expression: time.Now().UTC().Add(time.Hour).Format(time.RFC3339), Catchup: stringPtr("always")}},
		{"rule not approved", CreateScheduleRequest{RuleID: "missing-rule", Type: string(models.ScheduleTypeOnce), Expression: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}},
	}

	for _, tc := range cases {
		body, _ := json.Marshal(tc.req)
		resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", tc.name, resp.StatusCode)
		}
	}
}

func TestUpdateScheduleValidationAndMissing(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-update-rule")

	req := CreateScheduleRequest{
		RuleID:     "sched-update-rule",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
		Name:       "update-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	list, _, err := st.ListSchedules(context.Background(), store.ListSchedulesFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(list))
	}
	id := list[0].ID

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"invalid cron", map[string]any{"expression": "bad"}},
		{"invalid once expression", map[string]any{"expression": "bad-time"}},
		{"invalid catchup", map[string]any{"catchup": "always"}},
	}

	for _, tc := range cases {
		body, _ := json.Marshal(tc.payload)
		hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(body))
		hreq.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(hreq)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", tc.name, resp.StatusCode)
		}
	}

	// Missing schedule returns 404.
	body, _ = json.Marshal(map[string]any{"name": "x"})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/missing-schedule", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDeleteScheduleAndGetScheduleMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/schedules/missing-schedule")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	dreq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/schedules/missing-schedule", nil)
	resp, err = http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestTriggerScheduleMissingAndInactive(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/admin/schedules/missing/trigger", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestClaimTaskValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	body, _ := json.Marshal(ClaimTaskRequest{WorkerID: ""})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	body, _ = json.Marshal(ClaimTaskRequest{WorkerID: "worker-1", BrowserProfileID: strings.Repeat("p", 201)})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized browser profile id, got %d", resp.StatusCode)
	}

	// No task available.
	body, _ = json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err = http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestRejectRuleEnhancementNotPending(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "reject-not-pending",
		Version:        "1.0.0",
		Name:           "Reject Not Pending",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:        store.NewID(),
		RuleID:    rule.ID,
		Baseline:  models.JSON(`{"id":"reject-not-pending"}`),
		Enhanced:  models.JSON(`{"id":"reject-not-pending"}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusApproved),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Post(srv.URL+"/admin/rules/reject-not-pending/enhancement/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNewConstructsRouter(t *testing.T) {
	f, err := os.CreateTemp("", "api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
		AuditActor:                     "admin",
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	handler := New(s, sch, nil, nil, nil, cfg, logger, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAdminGetResultsAndMissingTask(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	resultBody, _ := json.Marshal(ResultRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Payload:  map[string]any{"items": []any{"a"}},
	})
	resp, err := http.Post(srv.URL+"/results", "application/json", bytes.NewReader(resultBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 submitting result, got %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/admin/tasks/" + taskID + "/results")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var results []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	resp, err = http.Get(srv.URL + "/admin/tasks/missing-task/results")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for missing task, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestGetTaskLogsMissingAndEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	resp, err := http.Get(srv.URL + "/admin/tasks/missing-task/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var logs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(logs))
	}

	resp, err = http.Get(srv.URL + "/admin/tasks/" + taskID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(logs))
	}
}

func TestCreateTaskValidationAndErrors(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "task-create-rule")

	resp, err := http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad JSON, got %d", resp.StatusCode)
	}

	// Missing ruleId is rejected as an invalid client request.
	body, _ := json.Marshal(models.Task{
		ID:          store.NewID(),
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
	})
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing ruleId, got %d", resp.StatusCode)
	}

	// Invalid rule id violates the foreign-key constraint.
	body, _ = json.Marshal(models.Task{
		ID:          store.NewID(),
		RuleID:      "missing-rule",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
	})
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 for invalid rule, got %d", resp.StatusCode)
	}
}

func TestCreateRuleValidationAndErrors(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad JSON, got %d", resp.StatusCode)
	}

	// Database error branch: close the store and try to create a valid rule.
	rule := models.Rule{
		ID:        "db-error-rule",
		Version:   "1.0.0",
		Name:      "DB Error",
		Domain:    store.JSON("example.com"),
		Steps:     store.JSON([]any{"step1"}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	st.Close()
	body, _ := json.Marshal(rule)
	resp, err = http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 for DB error, got %d", resp.StatusCode)
	}
}

func TestListRulesQueryValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	cases := []struct {
		query    string
		wantCode int
	}{
		{"limit=notanumber", http.StatusBadRequest},
		{"limit=-1", http.StatusBadRequest},
		{"offset=notanumber", http.StatusBadRequest},
		{"offset=-1", http.StatusBadRequest},
		{"limit=1&offset=0", http.StatusOK},
	}

	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/admin/rules?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("query %q: expected %d, got %d", tc.query, tc.wantCode, resp.StatusCode)
		}
	}
}

func TestApproveRuleDBError(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "approve-db-error")
	st.Close()

	resp, err := http.Post(srv.URL+"/admin/rules/approve-db-error/approve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestDeleteRuleDBError(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "delete-db-error")
	st.Close()

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/admin/rules/delete-db-error", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestGetCheckpointDBError(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	st.Close()

	resp, err := http.Get(srv.URL + "/tasks/" + taskID + "/checkpoints/latest")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestGetLLMJobDBError(t *testing.T) {
	srv, st, _ := newTestServerWithLLM(t)
	defer srv.Close()

	st.Close()

	resp, err := http.Get(srv.URL + "/admin/rules/enhancements/jobs/job-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestGetTaskDBError(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	st.Close()

	resp, err := http.Get(srv.URL + "/tasks/" + taskID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestGetResultsDBError(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	st.Close()

	resp, err := http.Get(srv.URL + "/admin/tasks/" + taskID + "/results")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestGetTaskLogsDBError(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	st.Close()

	resp, err := http.Get(srv.URL + "/admin/tasks/" + taskID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func newTestServerWithGenerator(t *testing.T, provider llm.Provider) (*httptest.Server, *store.Store) {
	t.Helper()
	f, err := os.CreateTemp("", "api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
		AuditActor:                     "admin",
		LLMEnabled:                     true,
		LLMProvider:                    "fake",
		LLMModel:                       "fake-model",
		LLMMaxRetries:                  0,
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	orch := llm.NewOrchestrator(cfg, logger)
	if provider != nil {
		orch.RegisterProvider(provider)
	}
	gen := dsl.NewGenerator(cfg, orch, logger)

	handler := New(s, sch, nil, nil, gen, cfg, logger, nil)
	srv := httptest.NewServer(handler)
	return srv, s
}

type fakeLLMProvider struct {
	resp string
	err  error
}

func (f *fakeLLMProvider) Name() string { return "fake" }

func (f *fakeLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &llm.CompletionResponse{Content: f.resp, InputTokens: 1, OutputTokens: 1}, nil
}

func TestGenerateFromIntent(t *testing.T) {
	// Missing baselineRule.
	srv, _ := newTestServerWithGenerator(t, nil)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad JSON, got %d", resp.StatusCode)
	}

	body, _ := json.Marshal(GenerateFromIntentRequest{Recording: map[string]any{}})
	resp, err = http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing baseline, got %d", resp.StatusCode)
	}

	// DSL generator not configured -> 500.
	srv2, _ := newTestServer(t)
	defer srv2.Close()
	baseline := &models.Rule{
		ID:        "gen-rule",
		Version:   "1.0.0",
		Name:      "Gen",
		Domain:    store.JSON("example.com"),
		Entry:     "https://example.com",
		Steps:     store.JSON([]any{map[string]any{"action": "navigate"}}),
		Priority:  models.PriorityNormal,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	body, _ = json.Marshal(GenerateFromIntentRequest{Recording: map[string]any{}, BaselineRule: baseline})
	resp, err = http.Post(srv2.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 when generator not configured, got %d", resp.StatusCode)
	}

	// Happy path with LLM-enabled provider returning a known intent type.
	srv3, _ := newTestServerWithGenerator(t, &fakeLLMProvider{resp: `{"intentType":"list-collection"}`})
	defer srv3.Close()
	body, _ = json.Marshal(GenerateFromIntentRequest{
		Recording:         map[string]any{},
		BaselineRule:      baseline,
		CustomDescription: "collect product list",
	})
	resp, err = http.Post(srv3.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var genResp GenerateFromIntentResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		t.Fatal(err)
	}
	if genResp.Rule == nil {
		t.Fatal("expected generated rule")
	}

	// Provider error falls back to keyword classification and still succeeds.
	srv4, _ := newTestServerWithGenerator(t, &fakeLLMProvider{err: errors.New("provider down")})
	defer srv4.Close()
	body, _ = json.Marshal(GenerateFromIntentRequest{
		Recording:         map[string]any{},
		BaselineRule:      baseline,
		CustomDescription: "submit form",
	})
	resp, err = http.Post(srv4.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after provider error fallback, got %d", resp.StatusCode)
	}
}

func TestEnhanceRuleValidationAndLLMNotConfigured(t *testing.T) {
	// Validation tests with a configured LLM job manager.
	srv, _, _ := newTestServerWithLLM(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/admin/rules/enhance", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad JSON, got %d", resp.StatusCode)
	}

	body, _ := json.Marshal(EnhanceRuleRequest{BaselineRule: map[string]any{"name": "x"}, Recording: map[string]any{}})
	resp, err = http.Post(srv.URL+"/admin/rules/enhance", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing baseline id, got %d", resp.StatusCode)
	}

	body, _ = json.Marshal(EnhanceRuleRequest{BaselineRule: map[string]any{"id": "r1"}, Recording: map[string]any{}})
	resp, err = http.Post(srv.URL+"/admin/rules/enhance", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid baseline, got %d", resp.StatusCode)
	}

	// LLM job manager not configured -> 500.
	srv2, _ := newTestServer(t)
	defer srv2.Close()
	body, _ = json.Marshal(EnhanceRuleRequest{
		BaselineRule: map[string]any{
			"id":    "r1",
			"name":  "n",
			"entry": "https://example.com",
			"steps": []any{map[string]any{"action": "navigate"}},
		},
		Recording: map[string]any{},
	})
	resp, err = http.Post(srv2.URL+"/admin/rules/enhance", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 when LLM not configured, got %d", resp.StatusCode)
	}
}

func TestGetRuleEnhancementHTTPCoverage(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "enh-rule",
		Version:        "1.0.0",
		Name:           "Enh",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalPending),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:        store.NewID(),
		RuleID:    rule.ID,
		Baseline:  models.JSON(`{"id":"enh-rule"}`),
		Enhanced:  models.JSON(`{"id":"enh-rule"}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(srv.URL + "/admin/rules/enh-rule/enhancement")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got RuleEnhancementResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != enh.ID {
		t.Fatalf("expected enhancement %s, got %s", enh.ID, got.ID)
	}

	resp, err = http.Get(srv.URL + "/admin/rules/missing-rule/enhancement")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	st.Close()
	resp, err = http.Get(srv.URL + "/admin/rules/enh-rule/enhancement")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 for DB error, got %d", resp.StatusCode)
	}
}

func TestAcceptEnhancementHTTPCoverage(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createdAt := time.Now().UTC()
	rule := models.Rule{
		ID:             "accept-rule",
		Version:        "1.0.0",
		Name:           "Accept",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalPending),
		CreatedAt:      createdAt,
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:       store.NewID(),
		RuleID:   rule.ID,
		Baseline: models.JSON(`{"id":"accept-rule"}`),
		Enhanced: models.JSON(`{
			"id": "accept-rule",
			"version": "1.0.0",
			"name": "Accept Enhanced",
			"domain": "example.com",
			"entry": "https://example.com",
			"steps": [{"action":"navigate"}]
		}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Post(srv.URL+"/admin/rules/accept-rule/enhancement/accept", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 accepting enhancement, got %d", resp.StatusCode)
	}
	var accepted RuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Name != "Accept Enhanced" {
		t.Fatalf("expected enhanced name, got %s", accepted.Name)
	}
	if accepted.ApprovalStatus != string(models.RuleApprovalApproved) {
		t.Fatalf("expected approved status, got %s", accepted.ApprovalStatus)
	}

	// Accepting again fails because the enhancement is no longer pending.
	resp, err = http.Post(srv.URL+"/admin/rules/accept-rule/enhancement/accept", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for not pending, got %d", resp.StatusCode)
	}

	// Missing enhancement.
	resp, err = http.Post(srv.URL+"/admin/rules/missing-rule/enhancement/accept", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing enhancement, got %d", resp.StatusCode)
	}

}

func TestRejectRuleEndpointDirect(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "reject-rule-direct",
		Version:        "1.0.0",
		Name:           "Reject Direct",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalPending),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Post(srv.URL+"/admin/rules/reject-rule-direct/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Missing rule returns 404.
	resp, err = http.Post(srv.URL+"/admin/rules/missing-rule/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetResultsAndTaskLogsEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	// Submit a result.
	resBody, _ := json.Marshal(ResultRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Payload:  map[string]any{"title": "Hello"},
	})
	resp, err := http.Post(srv.URL+"/results", "application/json", bytes.NewReader(resBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/tasks/" + taskID + "/results")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 results, got %d", resp.StatusCode)
	}
	var results []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Missing task still returns 200 with empty results.
	resp, err = http.Get(srv.URL + "/tasks/missing-task/results")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for missing task results, got %d", resp.StatusCode)
	}

	// Submit a log.
	logBody, _ := json.Marshal(LogRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Level:    "info",
		Message:  "hello",
	})
	resp, err = http.Post(srv.URL+"/logs", "application/json", bytes.NewReader(logBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/tasks/" + taskID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 logs, got %d", resp.StatusCode)
	}
	var logs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	// Missing task returns empty logs.
	resp, err = http.Get(srv.URL + "/admin/tasks/missing-task/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for missing task logs, got %d", resp.StatusCode)
	}
}

func TestUpdateRuleEndpointDirect(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "update-rule",
		Version:        "1.0.0",
		Name:           "Original",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Happy path: update name.
	newName := "Updated"
	patch, _ := json.Marshal(UpdateRuleRequest{Name: &newName})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/update-rule", bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got RuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Updated" {
		t.Fatalf("expected Updated, got %s", got.Name)
	}

	// Invalid JSON.
	hreq, _ = http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/update-rule", bytes.NewReader([]byte("bad")))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing rule.
	hreq, _ = http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/missing-rule", bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestCreateTaskEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Need a rule first.
	rule := models.Rule{
		ID:             "create-task-rule",
		Version:        "1.0.0",
		Name:           "Task Rule",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Happy path with minimal task.
	task := models.Task{
		RuleID:   "create-task-rule",
		Priority: models.PriorityNormal,
	}
	body, _ = json.Marshal(task)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created["taskId"] == "" {
		t.Fatal("expected taskId")
	}

	// Invalid JSON.
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestListRulesEndpointValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	cases := []struct {
		query    string
		wantCode int
	}{
		{"enabled=notbool", http.StatusBadRequest},
		{"limit=bad", http.StatusBadRequest},
		{"offset=-1", http.StatusBadRequest},
		{"limit=10&offset=0", http.StatusOK},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/admin/rules?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("query %q: expected %d, got %d", tc.query, tc.wantCode, resp.StatusCode)
		}
	}
}

func TestGetTaskAndCheckpointMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/tasks/missing-task")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing task, got %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/tasks/missing-task/checkpoints/latest")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing checkpoint, got %d", resp.StatusCode)
	}
}

func TestAcceptEnhancementEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "accept-enh-rule",
		Version:        "1.0.0",
		Name:           "Accept Enh",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalPending),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:        store.NewID(),
		RuleID:    rule.ID,
		Baseline:  models.JSON(`{"id":"accept-enh-rule"}`),
		Enhanced:  models.JSON(`{"id":"accept-enh-rule","version":"1.0.0","name":"Better","domain":"example.com","steps":["step2"]}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	// Happy path.
	resp, err = http.Post(srv.URL+"/admin/rules/accept-enh-rule/enhancement/accept", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Re-accepting should fail because enhancement is no longer pending.
	resp, err = http.Post(srv.URL+"/admin/rules/accept-enh-rule/enhancement/accept", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing enhancement.
	resp, err = http.Post(srv.URL+"/admin/rules/missing-rule/enhancement/accept", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetRuleEnhancementEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "get-enh-rule",
		Version:        "1.0.0",
		Name:           "Get Enh",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:        store.NewID(),
		RuleID:    rule.ID,
		Baseline:  models.JSON(`{"id":"get-enh-rule"}`),
		Enhanced:  models.JSON(`{"id":"get-enh-rule"}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusPending),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(srv.URL + "/admin/rules/get-enh-rule/enhancement")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Missing enhancement.
	resp, err = http.Get(srv.URL + "/admin/rules/missing-rule/enhancement")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestPreviewScheduleEndpointCoverage(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Happy path.
	resp, err := http.Get(srv.URL + "/admin/schedules/preview?expression=*/5+*+*+*+*&count=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var preview map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if len(preview["runs"].([]any)) != 3 {
		t.Fatalf("expected 3 runs, got %v", preview["runs"])
	}

	// Missing expression.
	resp, err = http.Get(srv.URL + "/admin/schedules/preview")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Invalid cron.
	resp, err = http.Get(srv.URL + "/admin/schedules/preview?expression=bad")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Invalid after.
	resp, err = http.Get(srv.URL + "/admin/schedules/preview?expression=*/5+*+*+*+*&after=bad")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Invalid count.
	resp, err = http.Get(srv.URL + "/admin/schedules/preview?expression=*/5+*+*+*+*&count=bad")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetAndDeleteScheduleEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-get-rule")
	req := CreateScheduleRequest{
		RuleID:     "sched-get-rule",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
		Name:       "get-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// List to get ID.
	resp, err = http.Get(srv.URL + "/admin/schedules?rule_id=sched-get-rule")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list.Schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(list.Schedules))
	}
	id := list.Schedules[0].ID

	// Get happy path.
	resp, err = http.Get(srv.URL + "/admin/schedules/" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 get, got %d", resp.StatusCode)
	}

	// Delete happy path.
	dreq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/schedules/"+id, nil)
	resp, err = http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 delete, got %d", resp.StatusCode)
	}

	// Get missing after delete.
	resp, err = http.Get(srv.URL + "/admin/schedules/" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestUpdateScheduleEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-update-rule-2")
	req := CreateScheduleRequest{
		RuleID:     "sched-update-rule-2",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
		Name:       "update-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/schedules?rule_id=sched-update-rule-2")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := list.Schedules[0].ID

	// Happy path: update name.
	newName := "updated-sched"
	patch, _ := json.Marshal(UpdateScheduleRequest{Name: &newName})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Invalid JSON.
	hreq, _ = http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader([]byte("bad")))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing schedule.
	hreq, _ = http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/missing-schedule", bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	// Invalid cron expression.
	badCron := "invalid"
	patch, _ = json.Marshal(UpdateScheduleRequest{Expression: &badCron})
	hreq, _ = http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid cron, got %d", resp.StatusCode)
	}

	// Invalid catchup.
	badCatchup := "always"
	patch, _ = json.Marshal(UpdateScheduleRequest{Catchup: &badCatchup})
	hreq, _ = http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid catchup, got %d", resp.StatusCode)
	}
}

func TestTriggerScheduleEndpointCoverage(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-trigger-rule")
	req := CreateScheduleRequest{
		RuleID:     "sched-trigger-rule",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
		Name:       "trigger-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/schedules?rule_id=sched-trigger-rule")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := list.Schedules[0].ID

	// Happy path trigger.
	resp, err = http.Post(srv.URL+"/admin/schedules/"+id+"/trigger", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 trigger, got %d", resp.StatusCode)
	}

	// Trigger missing schedule.
	resp, err = http.Post(srv.URL+"/admin/schedules/missing-schedule/trigger", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSubmitCheckpointAndSnapshotEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	// Happy path checkpoint.
	cpBody, _ := json.Marshal(CheckpointRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Name:     "checkpoint-1",
		Payload:  map[string]any{"url": "https://example.com"},
	})
	resp, err := http.Post(srv.URL+"/tasks/"+taskID+"/checkpoints", "application/json", bytes.NewReader(cpBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 checkpoint, got %d", resp.StatusCode)
	}

	// Invalid JSON.
	resp, err = http.Post(srv.URL+"/tasks/"+taskID+"/checkpoints", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing fields.
	cpBody, _ = json.Marshal(CheckpointRequest{TaskID: taskID})
	resp, err = http.Post(srv.URL+"/tasks/"+taskID+"/checkpoints", "application/json", bytes.NewReader(cpBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 missing fields, got %d", resp.StatusCode)
	}

	// Happy path snapshot.
	snapBody, _ := json.Marshal(SnapshotRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Name:     "snap",
		Type:     "html",
		Data:     "<html></html>",
	})
	resp, err = http.Post(srv.URL+"/snapshots", "application/json", bytes.NewReader(snapBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 snapshot, got %d", resp.StatusCode)
	}
}

func TestGenerateFromIntentValidation(t *testing.T) {
	srv, _, _ := newTestServerWithLLM(t)
	defer srv.Close()

	// Invalid JSON.
	resp, err := http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Missing baseline rule.
	body, _ := json.Marshal(GenerateFromIntentRequest{Recording: map[string]any{}})
	resp, err = http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 missing baseline, got %d", resp.StatusCode)
	}

	// DSL generator not configured.
	body, _ = json.Marshal(GenerateFromIntentRequest{
		Recording:    map[string]any{},
		BaselineRule: &models.Rule{ID: "rule-1"},
	})
	resp, err = http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 no generator, got %d", resp.StatusCode)
	}
}

func TestCancelAndRetryTaskEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "cancel-retry-rule")
	taskID := createTaskForRule(t, srv, "cancel-retry-rule")

	// Cancel happy path (pending task).
	resp, err := http.Post(srv.URL+"/admin/tasks/"+taskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 cancel, got %d", resp.StatusCode)
	}

	// Retry happy path (cancelled task is retryable).
	resp, err = http.Post(srv.URL+"/admin/tasks/"+taskID+"/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 retry, got %d", resp.StatusCode)
	}

	// Cancel missing task.
	resp, err = http.Post(srv.URL+"/admin/tasks/missing-task/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 cancel missing, got %d", resp.StatusCode)
	}

	// Retry missing task.
	resp, err = http.Post(srv.URL+"/admin/tasks/missing-task/retry", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 retry missing, got %d", resp.StatusCode)
	}
}

func createTaskForRule(t *testing.T, srv *httptest.Server, ruleID string) string {
	t.Helper()
	task := models.Task{
		ID:          store.NewID(),
		RuleID:      ruleID,
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	body, _ := json.Marshal(task)
	resp, err := http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return task.ID
}

func newTestServerWithDSL(t *testing.T) *httptest.Server {
	t.Helper()
	f, err := os.CreateTemp("", "api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
		AuditActor:                     "admin",
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	gen := dsl.NewGenerator(cfg, nil, logger)
	h := NewHandler(s, sch, nil, nil, cfg, logger)
	metrics := NewMetrics()
	h.SetMetrics(metrics)
	h.SetDSLGenerator(gen)
	router := NewRouter(h, cfg, logger, metrics)
	return httptest.NewServer(router)
}

func TestGenerateFromIntentHappyPath(t *testing.T) {
	srv := newTestServerWithDSL(t)
	defer srv.Close()

	baseline := &models.Rule{
		ID:      "gen-rule",
		Version: "1.0.0",
		Name:    "Baseline",
		Domain:  models.JSON(`"example.com"`),
		Steps:   models.JSON(`[]`),
	}
	body, _ := json.Marshal(GenerateFromIntentRequest{
		Recording:    map[string]any{},
		BaselineRule: baseline,
		Intent:       &intent.Candidate{Label: "list", Description: "collect list items"},
	})
	resp, err := http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result GenerateFromIntentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Rule == nil || result.Rule.ID != "gen-rule" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAdminUIHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}

	// Redirect /admin to /admin/.
	resp, err := client.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}

	// Serve index.html.
	resp, err = http.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	wantStatus := http.StatusOK
	index, indexErr := adminFS.Open("index.html")
	if indexErr != nil {
		// A clean source checkout intentionally embeds only .gitkeep. The
		// production build copies index.html before compiling the server.
		wantStatus = http.StatusServiceUnavailable
	} else {
		index.Close()
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("expected %d, got %d", wantStatus, resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", resp.Header.Get("Content-Type"))
	}

	// Missing asset returns 404.
	resp, err = http.Get(srv.URL + "/admin/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestTriggerScheduleInactive(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-inactive-rule")
	req := CreateScheduleRequest{
		RuleID:     "sched-inactive-rule",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
		Name:       "inactive-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/schedules?rule_id=sched-inactive-rule")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := list.Schedules[0].ID

	// Disable the rule so TriggerSchedule sees an inactive rule.
	enabled := false
	patch, _ := json.Marshal(UpdateRuleRequest{Enabled: &enabled})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/sched-inactive-rule", bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Trigger inactive schedule returns 400.
	resp, err = http.Post(srv.URL+"/admin/schedules/"+id+"/trigger", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for inactive schedule, got %d", resp.StatusCode)
	}
}

func TestSubmitHeartbeatHappyPath(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "heartbeat-rule")
	taskID := createTaskForRule(t, srv, "heartbeat-rule")

	// Claim the task first so heartbeat can renew the lease.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(HeartbeatRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Payload:  map[string]any{"progress": 50},
	})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSubmitStatusTerminal(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	// Claim then mark done.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(StatusRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Status:   string(models.TaskStatusDone),
		Message:  "completed",
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	got, err := st.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TaskStatusDone {
		t.Fatalf("expected done, got %s", got.Status)
	}
}

func TestSubmitStatusPersistsRunningAndRejectsUnboundHumanWait(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(StatusRequest{
		TaskID: taskID, WorkerID: "worker-1", Status: string(models.TaskStatusRunning), Message: "started",
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("running status: expected 200, got %d", resp.StatusCode)
	}

	body, _ = json.Marshal(StatusRequest{
		TaskID: taskID, WorkerID: "worker-1", Status: string(models.TaskStatusWaitingHuman), Message: "unbound wait",
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unbound human wait: expected 400, got %d", resp.StatusCode)
	}
	got, err := st.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TaskStatusRunning {
		t.Fatalf("unbound human wait mutated task: %+v", got)
	}
}

func TestSubmitStatusRejectsInactiveWorkerLease(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(StatusRequest{
		TaskID: taskID, WorkerID: "worker-2", Status: string(models.TaskStatusRunning),
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestRejectEnhancementNotPending(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "reject-not-pending-2",
		Version:        "1.0.0",
		Name:           "Reject Not Pending",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	enh := &models.RuleEnhancement{
		ID:        store.NewID(),
		RuleID:    rule.ID,
		Baseline:  models.JSON(`{"id":"reject-not-pending-2"}`),
		Enhanced:  models.JSON(`{"id":"reject-not-pending-2"}`),
		Patch:     models.JSON(`{}`),
		Status:    string(models.EnhancementStatusApproved),
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRuleEnhancement(context.Background(), enh); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Post(srv.URL+"/admin/rules/reject-not-pending-2/enhancement/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSubmitSnapshotValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Missing fields cause store insert to fail and handler returns 500.
	body, _ := json.Marshal(SnapshotRequest{TaskID: "x"})
	resp, err := http.Post(srv.URL+"/snapshots", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestSubmitLogValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Missing fields cause store insert to fail and handler returns 500.
	body, _ := json.Marshal(LogRequest{TaskID: "x"})
	resp, err := http.Post(srv.URL+"/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
}

func TestSubmitStatusInvalidAndCancelled(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)

	// Invalid JSON.
	resp, err := http.Post(srv.URL+"/status", "application/json", bytes.NewReader([]byte("bad")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Unknown statuses are rejected instead of being stored as arbitrary task state.
	body, _ := json.Marshal(StatusRequest{TaskID: taskID, WorkerID: "worker-1", Status: "unknown", Message: "x"})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestListSchedulesValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	cases := []struct {
		query    string
		wantCode int
	}{
		{"enabled=notbool", http.StatusBadRequest},
		{"limit=bad", http.StatusBadRequest},
		{"offset=-1", http.StatusBadRequest},
		{"limit=10&offset=0", http.StatusOK},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/admin/schedules?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("query %q: expected %d, got %d", tc.query, tc.wantCode, resp.StatusCode)
		}
	}
}

func TestUpdateRuleMoreFields(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "update-rule-full",
		Version:        "1.0.0",
		Name:           "Original",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	priority := "high"
	entry := "https://example.com/entry"
	enabled := true
	patch, _ := json.Marshal(UpdateRuleRequest{
		Priority: &priority,
		Entry:    &entry,
		Enabled:  &enabled,
	})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/update-rule-full", bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got RuleResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Priority != "high" || got.Entry != entry || !got.Enabled {
		t.Fatalf("unexpected rule: %+v", got)
	}
}

func newTestServerNoAudit(t *testing.T) *httptest.Server {
	t.Helper()
	f, err := os.CreateTemp("", "api-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name(), "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		LeaseDuration:                  60 * time.Second,
		MaxRetries:                     3,
		MaxWorkerTasks:                 5,
		RateLimitPerSecond:             1000,
		RateLimitBurst:                 2000,
		WorkerRateLimitPerSecond:       1000,
		WorkerRateLimitBurst:           2000,
		SiteRateLimitPerSecond:         1000,
		SiteRateLimitBurst:             2000,
		CircuitBreakerFailureThreshold: 1000,
		CircuitBreakerFailureWindow:    time.Minute,
		CircuitBreakerOpenDuration:     time.Minute,
	}
	logger := zap.NewNop()
	sch := scheduler.New(s, cfg, logger, cfg.LeaseDuration, cfg.MaxRetries)
	sch.Start(context.Background(), 30*time.Second)

	h := NewHandler(s, sch, nil, nil, cfg, logger)
	metrics := NewMetrics()
	h.SetMetrics(metrics)
	router := NewRouter(h, cfg, logger, metrics)
	return httptest.NewServer(router)
}

func TestAuditLogSkippedWhenActorEmpty(t *testing.T) {
	srv := newTestServerNoAudit(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "audit-skip-rule",
		Version:        "1.0.0",
		Name:           "Audit Skip",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// The admin UI route is a wildcard; panic should be recovered.
	// Instead, test RecoveryMiddleware directly.
	logger := zap.NewNop()
	handler := RecoveryMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestGetTaskResponseWithNullFields(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	taskID := createRuleAndTask(t, srv)
	resp, err := http.Get(srv.URL + "/admin/tasks/" + taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var task TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.ID != taskID {
		t.Fatalf("unexpected task id: %s", task.ID)
	}
}

func TestGetRuleByIDMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/rules/missing-rule")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSubmitHeartbeatLeaseConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "heartbeat-conflict-rule")
	taskID := createTaskForRule(t, srv, "heartbeat-conflict-rule")

	// Claim by worker-1.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Heartbeat from worker-2 should conflict.
	body, _ := json.Marshal(HeartbeatRequest{
		TaskID:   taskID,
		WorkerID: "worker-2",
	})
	resp, err = http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestCancelTaskConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "cancel-conflict-rule")
	taskID := createTaskForRule(t, srv, "cancel-conflict-rule")

	// Claim and complete the task.
	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(StatusRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Status:   string(models.TaskStatusDone),
		Message:  "done",
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Cancel a done task should conflict.
	resp, err = http.Post(srv.URL+"/admin/tasks/"+taskID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestUpdateScheduleOnceExpression(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-update-once-rule")
	runAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	req := CreateScheduleRequest{
		RuleID:     "sched-update-once-rule",
		Type:       string(models.ScheduleTypeOnce),
		Expression: runAt.Format(time.RFC3339),
		Name:       "once-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/schedules?rule_id=sched-update-once-rule")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := list.Schedules[0].ID

	newExpr := runAt.Add(2 * time.Hour).Format(time.RFC3339)
	patch, _ := json.Marshal(UpdateScheduleRequest{Expression: &newExpr})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestUpdateRuleAllFields(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "update-rule-all",
		Version:        "1.0.0",
		Name:           "Original",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	body, _ := json.Marshal(rule)
	resp, err := http.Post(srv.URL+"/admin/rules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	version := "2.0.0"
	name := "Updated"
	domain := any("updated.com")
	urlPattern := any("/items/*")
	enabled := true
	priority := "high"
	entry := "https://updated.com"
	variables := jsonRaw{"x": 1}
	selectors := jsonRaw{"title": "h1"}
	humanize := jsonRaw{"delay": 100}
	steps := any([]any{map[string]any{"action": "navigate"}})
	output := jsonRaw{"schema": map[string]any{}}
	sendPolicy := jsonRaw{"target": "webhook"}
	hooks := jsonRaw{"onComplete": "notify"}
	tags := jsonRaw{"labels": "prod"}
	owner := "admin"
	approvalStatus := string(models.RuleApprovalApproved)
	source := "manual"
	patch, _ := json.Marshal(UpdateRuleRequest{
		Version:        &version,
		Name:           &name,
		Domain:         &domain,
		URLPattern:     &urlPattern,
		Enabled:        &enabled,
		Priority:       &priority,
		Entry:          &entry,
		Variables:      &variables,
		Selectors:      &selectors,
		Humanize:       &humanize,
		Steps:          &steps,
		Output:         &output,
		SendPolicy:     &sendPolicy,
		Hooks:          &hooks,
		Tags:           &tags,
		Owner:          &owner,
		ApprovalStatus: &approvalStatus,
		Source:         &source,
	})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/rules/update-rule-all", bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestUpdateScheduleAllFields(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-update-all-rule")
	runAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	req := CreateScheduleRequest{
		RuleID:     "sched-update-all-rule",
		Type:       string(models.ScheduleTypeOnce),
		Expression: runAt.Format(time.RFC3339),
		Name:       "once-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/schedules?rule_id=sched-update-all-rule")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := list.Schedules[0].ID

	name := "updated"
	ruleVersion := "2.0.0"
	newExpr := runAt.Add(30 * time.Minute).Format(time.RFC3339)
	enabled := true
	priority := "high"
	maxRetries := 5
	catchup := "run_once"
	variables := map[string]any{"x": 1}
	patch, _ := json.Marshal(UpdateScheduleRequest{
		RuleVersion: &ruleVersion,
		Name:        &name,
		Expression:  &newExpr,
		Enabled:     &enabled,
		Priority:    &priority,
		MaxRetries:  &maxRetries,
		Catchup:     &catchup,
		Variables:   &variables,
	})
	hreq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(patch))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSubmitStatusCancelled(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "status-cancel-rule")
	taskID := createTaskForRule(t, srv, "status-cancel-rule")

	claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
	resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(StatusRequest{
		TaskID:   taskID,
		WorkerID: "worker-1",
		Status:   string(models.TaskStatusCancelled),
		Message:  "cancelled",
	})
	resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	got, err := st.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TaskStatusCancelled || !got.CompletedAt.Valid || got.LeaseUntil.Valid {
		t.Fatalf("expected terminal cancelled task with released lease, got %#v", got)
	}
}

func TestSubmitHeartbeatMissingTask(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body, _ := json.Marshal(HeartbeatRequest{
		TaskID:   "missing-task",
		WorkerID: "worker-1",
	})
	resp, err := http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestRejectEnhancementMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/admin/rules/missing-rule/enhancement/reject", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHelperFunctions(t *testing.T) {
	m := map[string]any{
		"id":       "r1",
		"version":  "1.0.0",
		"name":     "Test",
		"enabled":  true,
		"priority": "normal",
	}
	r := ruleFromMap(m)
	if r.ID != "r1" || r.Name != "Test" || !r.Enabled || r.Priority != models.PriorityNormal {
		t.Fatalf("unexpected rule: %+v", r)
	}

	if getString(m, "missing") != "" {
		t.Fatal("expected empty string")
	}
	if getBool(m, "missing") != false {
		t.Fatal("expected false")
	}

	if rawToMap(models.JSON(`{}`)) == nil {
		t.Fatal("expected empty map")
	}
	if rawToMap(models.JSON(``)) != nil {
		t.Fatal("expected nil")
	}
	if len(marshalJSON(map[string]any{"x": 1})) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}

func TestGenerateFromIntentCustomDescription(t *testing.T) {
	srv := newTestServerWithDSL(t)
	defer srv.Close()

	baseline := &models.Rule{
		ID:      "gen-rule-custom",
		Version: "1.0.0",
		Name:    "Baseline",
		Domain:  models.JSON(`"example.com"`),
		Steps:   models.JSON(`[]`),
	}
	body, _ := json.Marshal(GenerateFromIntentRequest{
		Recording:         map[string]any{},
		BaselineRule:      baseline,
		CustomDescription: "collect a list of products",
	})
	resp, err := http.Post(srv.URL+"/admin/rules/generate-from-intent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSubmitStatusFailedAndDeadLetter(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "status-failed-rule")
	for _, status := range []models.TaskStatus{models.TaskStatusFailed, models.TaskStatusDeadLetter} {
		taskID := createTaskForRule(t, srv, "status-failed-rule")
		claimBody, _ := json.Marshal(ClaimTaskRequest{WorkerID: "worker-1"})
		resp, err := http.Post(srv.URL+"/tasks/claim", "application/json", bytes.NewReader(claimBody))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		body, _ := json.Marshal(StatusRequest{
			TaskID:   taskID,
			WorkerID: "worker-1",
			Status:   string(status),
			Message:  string(status),
		})
		resp, err = http.Post(srv.URL+"/status", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %s: expected 200, got %d", status, resp.StatusCode)
		}
	}
}

func TestTaskResponseWithFullFields(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "full-task-rule")
	taskID := createTaskForRule(t, srv, "full-task-rule")

	// Update task with error/completed fields to cover toTaskResponse branches.
	_, err := st.DB().ExecContext(context.Background(),
		`UPDATE tasks SET error_type = ?, error_message = ?, completed_at = ? WHERE id = ?`,
		"TEST_ERROR", "something failed", time.Now().UTC(), taskID)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/admin/tasks/" + taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMetricsLLM(t *testing.T) {
	m := NewMetrics()
	lm := m.LLM()
	lm.RecordCompletion("fake", "model", time.Millisecond, 1, 2, false, nil)
	lm.RecordCompletion("fake", "model", time.Millisecond, 1, 2, true, nil)
}

func TestMetricsResponseWriterWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	mw := NewMetricsResponseWriter(rec)
	mw.WriteHeader(http.StatusCreated)
	mw.Write([]byte("hello"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
}

func TestHandlerDBErrors(t *testing.T) {
	cases := []struct {
		name string
		do   func(srv *httptest.Server, id string) *http.Response
		want int
	}{
		{
			name: "get schedule db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/admin/schedules/" + id)
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "delete schedule db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				dreq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/admin/schedules/"+id, nil)
				resp, _ := http.DefaultClient.Do(dreq)
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "reject rule db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Post(srv.URL+"/admin/rules/"+id+"/reject", "application/json", nil)
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "submit checkpoint db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				body, _ := json.Marshal(CheckpointRequest{TaskID: id, WorkerID: "w"})
				resp, _ := http.Post(srv.URL+"/tasks/"+id+"/checkpoints", "application/json", bytes.NewReader(body))
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "submit heartbeat db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				body, _ := json.Marshal(HeartbeatRequest{TaskID: id, WorkerID: "w"})
				resp, _ := http.Post(srv.URL+"/heartbeat", "application/json", bytes.NewReader(body))
				return resp
			},
			want: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := newTestServer(t)
			defer srv.Close()

			createApprovedRule(t, srv, "db-error-rule")
			taskID := createTaskForRule(t, srv, "db-error-rule")
			var id string
			if tc.name == "get schedule db error" || tc.name == "delete schedule db error" {
				req := CreateScheduleRequest{
					RuleID:     "db-error-rule",
					Type:       string(models.ScheduleTypeCron),
					Expression: "*/5 * * * *",
					Name:       "db-error-sched",
				}
				body, _ := json.Marshal(req)
				resp, _ := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
				resp.Body.Close()

				resp, _ = http.Get(srv.URL + "/admin/schedules?rule_id=db-error-rule")
				var list ListSchedulesResponse
				json.NewDecoder(resp.Body).Decode(&list)
				resp.Body.Close()
				id = list.Schedules[0].ID
			} else {
				id = taskID
			}

			st.Close()
			resp := tc.do(srv, id)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, resp.StatusCode)
			}
		})
	}
}

func TestHandlerDBErrorsMore(t *testing.T) {
	cases := []struct {
		name string
		do   func(srv *httptest.Server, id string) *http.Response
		want int
	}{
		{
			name: "get task db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/tasks/" + id)
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "get rule db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/admin/rules/" + id)
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "get results db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/tasks/" + id + "/results")
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "get task logs db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/admin/tasks/" + id + "/logs")
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "list rules db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/admin/rules")
				return resp
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "list tasks db error",
			do: func(srv *httptest.Server, id string) *http.Response {
				resp, _ := http.Get(srv.URL + "/admin/tasks")
				return resp
			},
			want: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st := newTestServer(t)
			defer srv.Close()

			createApprovedRule(t, srv, "db-error-rule-2")
			taskID := createTaskForRule(t, srv, "db-error-rule-2")
			st.Close()

			resp := tc.do(srv, taskID)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, resp.StatusCode)
			}
		})
	}
}
