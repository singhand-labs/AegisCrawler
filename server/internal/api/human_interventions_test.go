package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

func postHumanJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(url, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHumanInterventionAPIResumesCheckpointBoundAttempt(t *testing.T) {
	srv, persistence := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, persistence, "human-api-rule")

	response := postHumanJSON(t, srv.Client(), srv.URL+"/admin/tasks", map[string]any{
		"ruleId": version.RuleID, "ruleVersionNumber": version.Version,
		"variables": map[string]any{"query": "books"},
	})
	var created map[string]string
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	taskID := created["taskId"]

	response = postHumanJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "human-api-worker", BrowserProfileID: "profile-api",
	})
	var claim ClaimTaskResponse
	if err := json.NewDecoder(response.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if claim.TaskID != taskID || claim.AttemptID == "" {
		t.Fatalf("unexpected claim: %+v", claim)
	}

	request := CreateHumanInterventionRequest{
		WorkerID: "human-api-worker", AttemptID: claim.AttemptID,
		Type: "2fa", Prompt: "Complete the approved two-factor step", TimeoutMs: 60_000,
		Checkpoint: HumanCheckpointRequest{StepID: "two-factor", URL: "https://example.com/account?token=must-not-persist"},
	}
	response = postHumanJSON(t, srv.Client(), srv.URL+"/tasks/"+taskID+"/human-interventions", request)
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		t.Fatalf("create intervention: expected 201, got %d", response.StatusCode)
	}
	var workerResponse WorkerHumanInterventionResponse
	if err := json.NewDecoder(response.Body).Decode(&workerResponse); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	intervention := workerResponse.Intervention
	if intervention == nil || intervention.Status != models.HumanInterventionPending || intervention.AttemptID != claim.AttemptID {
		t.Fatalf("unexpected worker response: %+v", workerResponse)
	}

	response, err := srv.Client().Get(srv.URL + "/admin/tasks/" + taskID + "/human-interventions")
	if err != nil {
		t.Fatal(err)
	}
	var listed ListHumanInterventionsResponse
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(listed.Interventions) != 1 {
		t.Fatalf("expected one intervention, got %+v", listed)
	}
	adminView := listed.Interventions[0]
	if adminView.TargetOrigin != "https://example.com" || adminView.RequestedAction != "complete_2fa_then_resume" || adminView.CheckpointID != intervention.CheckpointID {
		t.Fatalf("operator view omitted safety context: %+v", adminView)
	}
	if bytes.Contains(adminView.Checkpoint, []byte("must-not-persist")) {
		t.Fatalf("checkpoint persisted a URL secret: %s", adminView.Checkpoint)
	}

	response = postHumanJSON(t, srv.Client(), srv.URL+"/status", StatusRequest{
		TaskID: taskID, WorkerID: "human-api-worker", AttemptID: claim.AttemptID,
		Status: string(models.TaskStatusRunning),
	})
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("worker bypass: expected 409, got %d", response.StatusCode)
	}
	waiting, err := persistence.GetTaskByID(response.Request.Context(), taskID)
	if err != nil || waiting.Status != models.TaskStatusWaitingHuman {
		t.Fatalf("worker bypass changed pending checkpoint: task=%+v err=%v", waiting, err)
	}

	response = postHumanJSON(t, srv.Client(), srv.URL+"/admin/tasks/"+taskID+"/human-interventions/"+intervention.ID+"/decision", DecideHumanInterventionRequest{
		Decision: "approved", CheckpointID: "stale-checkpoint",
	})
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale checkpoint decision: expected 409, got %d", response.StatusCode)
	}

	response = postHumanJSON(t, srv.Client(), srv.URL+"/admin/tasks/"+taskID+"/human-interventions/"+intervention.ID+"/decision", DecideHumanInterventionRequest{
		Decision: "approved", CheckpointID: intervention.CheckpointID, Note: "approved manual step completed",
	})
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		t.Fatalf("approve intervention: expected 200, got %d", response.StatusCode)
	}
	response.Body.Close()

	pollURL := srv.URL + "/tasks/" + taskID + "/human-interventions/" + intervention.ID +
		"?workerId=human-api-worker&attemptId=" + claim.AttemptID
	response, err = srv.Client().Get(pollURL)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	encodedWorker, _ := json.Marshal(raw)
	if !bytes.Contains(encodedWorker, []byte(`"status":"approved"`)) || bytes.Contains(encodedWorker, []byte("decidedBy")) || bytes.Contains(encodedWorker, []byte("prompt")) {
		t.Fatalf("worker poll leaked admin-only fields or omitted approval: %s", encodedWorker)
	}

	task, err := persistence.GetTaskByID(response.Request.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusRunning || task.CurrentAttemptID.String != claim.AttemptID {
		t.Fatalf("approval did not resume same attempt: %+v", task)
	}
	logs, total, err := persistence.ListAuditLogs(response.Request.Context(), store.ListAuditLogsFilter{ResourceType: "human_intervention"})
	if err != nil || total != 2 || len(logs) != 2 {
		t.Fatalf("expected request and approval audit logs: logs=%+v total=%d err=%v", logs, total, err)
	}
}

func TestHumanInterventionAPIRejectsUnapprovedOrigin(t *testing.T) {
	srv, persistence := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, persistence, "human-domain-rule")
	response := postHumanJSON(t, srv.Client(), srv.URL+"/admin/tasks", map[string]any{
		"ruleId": version.RuleID, "ruleVersionNumber": version.Version,
		"variables": map[string]any{"query": "books"},
	})
	var created map[string]string
	_ = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	response = postHumanJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "human-domain-worker", BrowserProfileID: "profile-api",
	})
	var claim ClaimTaskResponse
	_ = json.NewDecoder(response.Body).Decode(&claim)
	response.Body.Close()

	response = postHumanJSON(t, srv.Client(), srv.URL+"/tasks/"+created["taskId"]+"/human-interventions", CreateHumanInterventionRequest{
		WorkerID: "human-domain-worker", AttemptID: claim.AttemptID,
		Type: "generic", Prompt: "continue", TimeoutMs: 60_000,
		Checkpoint: HumanCheckpointRequest{StepID: "manual", URL: "https://example.com.attacker.invalid/"},
	})
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected unapproved origin rejection, got %d", response.StatusCode)
	}
	task, _ := persistence.GetTaskByID(response.Request.Context(), created["taskId"])
	if task.Status != models.TaskStatusLeased {
		t.Fatalf("rejected checkpoint mutated task: %+v", task)
	}
}

func TestHumanInterventionAPISupportsLegacySyntheticVersionTask(t *testing.T) {
	srv, persistence := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	rule := &models.Rule{
		ID: "human-legacy-rule", Version: "1.0.0", Name: "Legacy human rule",
		Domain: store.JSON("example.com"), Steps: store.JSON([]any{"requestHuman"}),
		Enabled: true, ApprovalStatus: string(models.RuleApprovalApproved),
		Priority: models.PriorityNormal, CreatedAt: now, UpdatedAt: now,
	}
	if err := persistence.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ID: "human-legacy-task", RuleID: rule.ID, RuleVersion: rule.Version,
		Status: models.TaskStatusPending, Priority: models.PriorityNormal, MaxRetries: 3,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := persistence.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	claimed, err := persistence.ClaimTaskForBrowserProfile(ctx, "human-legacy-worker", "", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.UpdateTaskStatus(ctx, claimed.ID, "human-legacy-worker", string(models.TaskStatusRunning), "", ""); err != nil {
		t.Fatal(err)
	}
	response := postHumanJSON(t, srv.Client(), srv.URL+"/tasks/"+claimed.ID+"/human-interventions", CreateHumanInterventionRequest{
		WorkerID: "human-legacy-worker", AttemptID: claimed.CurrentAttemptID.String,
		Type: "generic", Prompt: "Complete the approved legacy step", TimeoutMs: 60_000,
		Checkpoint: HumanCheckpointRequest{StepID: "legacy-manual", URL: "https://example.com/account"},
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		t.Fatalf("legacy synthetic version: expected 201, got %d: %+v (claimed version %d)", response.StatusCode, body, claimed.RuleVersionNumber)
	}
}
