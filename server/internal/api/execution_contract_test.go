package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

var apiExecutionInputSchema = models.JSON(`{
	"type":"object",
	"properties":{"query":{"type":"string"},"limit":{"type":"number","default":10}},
	"required":["query"],
	"additionalProperties":false
}`)

var apiExecutionOutputSchema = models.JSON(`{
	"type":"object",
	"properties":{"name":{"type":"string"},"price":{"type":"number"}},
	"required":["name","price"],
	"additionalProperties":false
}`)

func createAPIExecutionVersion(t *testing.T, st *store.Store, id string) *models.RuleVersion {
	return createAPIExecutionVersionWithInput(t, st, id, apiExecutionInputSchema)
}

func createAPIExecutionVersionWithInput(t *testing.T, st *store.Store, id string, inputSchema models.JSON) *models.RuleVersion {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	rule := &models.Rule{
		ID: id, Version: "1.0.0", Name: id, Domain: store.JSON("example.com"),
		Steps: store.JSON([]any{"collect"}), Output: apiExecutionOutputSchema,
		Enabled: true, Priority: models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved), CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	var version *models.RuleVersion
	if err := st.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		version, err = st.CreateRuleVersionWithContractTx(ctx, tx, rule, "", inputSchema, apiExecutionOutputSchema, "profile-api")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	approved, err := st.ApproveRuleVersion(ctx, id, version.Version)
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func TestEmptyInputVersionedTaskStillRequiresAttemptLineage(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, st, "api-empty-input-rule")
	now := time.Now().UTC()
	task := &models.Task{
		ID: store.NewID(), RuleID: version.RuleID, Variables: models.JSON(`{}`),
		InputSchema: models.JSON(`{}`), OutputSchema: apiExecutionOutputSchema,
		Status: models.TaskStatusPending, Priority: models.PriorityNormal,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	claimResp := postExecutionJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "empty-input-worker",
	})
	if claimResp.StatusCode != http.StatusOK {
		claimResp.Body.Close()
		t.Fatalf("claim empty-input versioned task: got %d", claimResp.StatusCode)
	}
	claimResp.Body.Close()
	resp := postExecutionJSON(t, srv.Client(), srv.URL+"/status", StatusRequest{
		TaskID: task.ID, WorkerID: "empty-input-worker", Status: string(models.TaskStatusDone),
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty-input versioned task bypassed attempt lineage: got %d", resp.StatusCode)
	}
}

func TestTaskRequiresAttemptLineagePreservesPermissiveLegacyContract(t *testing.T) {
	legacy := &models.Task{
		InputSchema:  models.JSON(`{}`),
		OutputSchema: models.JSON(`{"type":"object","properties":{},"additionalProperties":true}`),
	}
	if taskRequiresAttemptLineage(legacy) {
		t.Fatal("migration-16 permissive legacy contract should retain the legacy status path")
	}
	versioned := &models.Task{InputSchema: models.JSON(`{}`), OutputSchema: apiExecutionOutputSchema}
	if !taskRequiresAttemptLineage(versioned) {
		t.Fatal("a meaningful output contract must require attempt lineage even with empty inputs")
	}
}

func postExecutionJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func removeAPIExecutionContract(t *testing.T, st *store.Store, task *models.Task) {
	t.Helper()
	if _, err := st.DB().Exec(`DROP TRIGGER trg_rule_version_contracts_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`
		DELETE FROM rule_version_contracts
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, task.WorkspaceID, task.RuleID, task.RuleVersionNumber); err != nil {
		t.Fatal(err)
	}
}

func markAPIExecutionContractNonAuthoritative(t *testing.T, st *store.Store, task *models.Task) {
	t.Helper()
	if _, err := st.DB().Exec(`DROP TRIGGER trg_rule_version_contracts_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`
		UPDATE rule_version_contracts
		SET source_kind = ?, source_authority = ?, source_workflow_id = ?
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, models.DSLWorkflowSourceAdminReviewedAttemptExport,
		models.DSLWorkflowSourceAuthorityNonAuthoritative, "reviewed-workflow",
		task.WorkspaceID, task.RuleID, task.RuleVersionNumber); err != nil {
		t.Fatal(err)
	}
}

func setAPIExecutionTaskVersion(t *testing.T, st *store.Store, task *models.Task, version int) {
	t.Helper()
	if _, err := st.DB().Exec(`DROP TRIGGER trg_versioned_task_contract_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`
		UPDATE tasks SET rule_version_number = ?
		WHERE workspace_id = ? AND id = ?
	`, version, task.WorkspaceID, task.ID); err != nil {
		t.Fatal(err)
	}
	task.RuleVersionNumber = version
}

func requireExecutionStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s: got HTTP %d, want %d", response.Request.URL.Path, response.StatusCode, want)
	}
}

func TestTaskLineageReadersFailClosedWhenExactVersionIsMissing(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, st, "api-missing-exact-version")
	now := time.Now().UTC()
	task := &models.Task{
		ID: store.NewID(), RuleID: version.RuleID, RuleVersionNumber: version.Version,
		Variables: models.JSON(`{"query":"books"}`), Status: models.TaskStatusPending,
		Priority: models.PriorityNormal, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	setAPIExecutionTaskVersion(t, st, task, 999)

	requireExecutionStatus(t, postExecutionJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "missing-version-worker", BrowserProfileID: "profile-api",
	}), http.StatusInternalServerError)
	stored, err := st.GetTaskByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.TaskStatusPending || stored.CurrentAttemptID.Valid {
		t.Fatalf("failed version lookup exposed a lease without its lineage: %+v", stored)
	}

	for _, path := range []string{
		"/tasks/" + task.ID,
		"/admin/tasks/" + task.ID,
		"/admin/tasks?rule_id=" + task.RuleID,
		"/tasks/" + task.ID + "/results",
		"/admin/tasks/" + task.ID + "/results",
		"/api/v1/tasks/" + task.ID + "/results",
	} {
		response, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, response, http.StatusInternalServerError)
	}
}

func TestTaskLineageReadersFailClosedWhenVersionContractIsMissing(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, st, "api-missing-lineage-contract")
	now := time.Now().UTC()
	task := &models.Task{
		ID: store.NewID(), RuleID: version.RuleID, RuleVersionNumber: version.Version,
		Variables: models.JSON(`{"query":"books"}`), Status: models.TaskStatusPending,
		Priority: models.PriorityNormal, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	removeAPIExecutionContract(t, st, task)

	requireExecutionStatus(t, postExecutionJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "fail-closed-worker", BrowserProfileID: "profile-api",
	}), http.StatusInternalServerError)
	stored, err := st.GetTaskByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.TaskStatusPending || stored.CurrentAttemptID.Valid {
		t.Fatalf("failed claim exposed a lease without its lineage: %+v", stored)
	}

	for _, path := range []string{
		"/tasks/" + task.ID,
		"/admin/tasks/" + task.ID,
		"/admin/tasks?rule_id=" + task.RuleID,
		"/tasks/" + task.ID + "/results",
		"/admin/tasks/" + task.ID + "/results",
		"/api/v1/tasks/" + task.ID + "/results",
	} {
		response, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, response, http.StatusInternalServerError)
	}
}

func TestLegacyTaskLineageReadersRemainCompatible(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	now := time.Now().UTC()
	rule := &models.Rule{
		ID: "api-legacy-lineage", Version: "1.0.0", Name: "Legacy lineage",
		Domain: models.JSON(`"example.com"`), Steps: models.JSON(`[]`),
		Enabled: true, Priority: models.PriorityNormal,
		ApprovalStatus: string(models.RuleApprovalApproved), CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateRule(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ID: store.NewID(), RuleID: rule.ID, RuleVersion: rule.Version,
		Status: models.TaskStatusPending, Priority: models.PriorityNormal,
		MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/tasks/" + task.ID,
		"/admin/tasks/" + task.ID,
		"/admin/tasks?rule_id=" + task.RuleID,
		"/tasks/" + task.ID + "/results",
		"/admin/tasks/" + task.ID + "/results",
		"/api/v1/tasks/" + task.ID + "/results",
	} {
		response, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, response, http.StatusOK)
	}
	response := postExecutionJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{WorkerID: "legacy-lineage-worker"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("legacy claim got HTTP %d", response.StatusCode)
	}
	var claim ClaimTaskResponse
	if err := json.NewDecoder(response.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	if claim.TaskID != task.ID || claim.Rule != nil || claim.SourceKind != "" || claim.SourceAuthority != "" {
		t.Fatalf("legacy claim unexpectedly required or invented immutable lineage: %+v", claim)
	}
}

func TestLegacyResultRoutesRejectUnlabeledSourceLineage(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, st, "api-reviewed-result-lineage")
	now := time.Now().UTC()
	task := &models.Task{
		ID: store.NewID(), RuleID: version.RuleID, RuleVersionNumber: version.Version,
		Variables: models.JSON(`{"query":"books"}`), Status: models.TaskStatusPending,
		Priority: models.PriorityNormal, MaxRetries: 3, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	markAPIExecutionContractNonAuthoritative(t, st, task)

	for _, path := range []string{
		"/tasks/" + task.ID + "/results",
		"/admin/tasks/" + task.ID + "/results",
	} {
		response, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("%s: got HTTP %d, want %d", path, response.StatusCode, http.StatusConflict)
		}
		var apiError ErrorResponse
		if err := json.NewDecoder(response.Body).Decode(&apiError); err != nil {
			t.Fatal(err)
		}
		if apiError.Code != "LINEAGE_REQUIRED" ||
			apiError.Error != "task results include immutable source lineage; use /api/v1/tasks/"+task.ID+"/results" {
			t.Fatalf("%s returned an unclear lineage redirect: %+v", path, apiError)
		}
	}

	response, err := srv.Client().Get(srv.URL + "/api/v1/tasks/" + task.ID + "/results")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("versioned results endpoint rejected source lineage: got HTTP %d", response.StatusCode)
	}
	var results TaskResultsResponse
	if err := json.NewDecoder(response.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	if results.SourceKind != models.DSLWorkflowSourceAdminReviewedAttemptExport ||
		results.SourceAuthority != models.DSLWorkflowSourceAuthorityNonAuthoritative ||
		results.SourceWorkflowID != "reviewed-workflow" {
		t.Fatalf("versioned results endpoint omitted source lineage: %+v", results)
	}
}

func TestVersionedTaskExecutionAndResultAPI(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, st, "api-execution-rule")

	resp := postExecutionJSON(t, srv.Client(), srv.URL+"/admin/tasks", map[string]any{
		"ruleId": version.RuleID, "ruleVersionNumber": version.Version,
		"variables": map[string]any{"query": "books"},
	})
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("create versioned task: expected 200, got %d", resp.StatusCode)
	}
	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	taskID := created["taskId"]

	for _, path := range []string{
		"/tasks/" + taskID + "/results",
		"/admin/tasks/" + taskID + "/results",
	} {
		response, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, response, http.StatusOK)
	}

	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "wrong-profile-worker", BrowserProfileID: "profile-other",
	})
	if resp.StatusCode != http.StatusNoContent {
		defer resp.Body.Close()
		t.Fatalf("mismatched profile claimed bound task: expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/tasks/claim", ClaimTaskRequest{
		WorkerID: "worker-api", BrowserProfileID: "profile-api",
	})
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("claim versioned task: expected 200, got %d", resp.StatusCode)
	}
	var claim ClaimTaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if claim.TaskID != taskID || claim.AttemptID == "" || claim.RuleVersionNumber != version.Version || claim.Rule == nil || claim.BrowserProfileID != "profile-api" {
		t.Fatalf("claim omitted immutable execution contract: %+v", claim)
	}
	if claim.Variables["query"] != "books" || claim.Variables["limit"] != float64(10) {
		t.Fatalf("claim omitted normalized input snapshot: %v", claim.Variables)
	}
	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/status", StatusRequest{
		TaskID: taskID, WorkerID: "worker-api", Status: string(models.TaskStatusDone),
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("versioned done without attempt lineage bypassed result gate: got %d", resp.StatusCode)
	}

	validRequest := ResultRequest{
		TaskID: taskID, WorkerID: "worker-api", AttemptID: claim.AttemptID,
		IdempotencyKey: "batch-1", Sequence: 1, Kind: string(models.ResultKindBatch),
		Payload: []any{map[string]any{"name": "Book", "price": 10}},
	}
	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/results", validRequest)
	var submitted ResultSubmissionResponse
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !submitted.Success || !submitted.Valid || submitted.Duplicate {
		t.Fatalf("unexpected valid submission: status=%d response=%+v", resp.StatusCode, submitted)
	}

	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/results", validRequest)
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !submitted.Duplicate {
		t.Fatalf("exact retry was not acknowledged: status=%d response=%+v", resp.StatusCode, submitted)
	}

	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/status", StatusRequest{
		TaskID: taskID, WorkerID: "worker-api", AttemptID: claim.AttemptID,
		Status: string(models.TaskStatusDone), Message: "premature",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("done without a summary should fail closed, got %d", resp.StatusCode)
	}

	invalidRequest := ResultRequest{
		TaskID: taskID, WorkerID: "worker-api", AttemptID: claim.AttemptID,
		IdempotencyKey: "invalid-2", Sequence: 2, Kind: string(models.ResultKindBatch),
		Payload: map[string]any{"name": "missing price"},
	}
	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/results", invalidRequest)
	if err := json.NewDecoder(resp.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || submitted.Success || submitted.Valid || submitted.Error == "" {
		t.Fatalf("invalid payload was not retained as a diagnostic: status=%d response=%+v", resp.StatusCode, submitted)
	}

	conflictRequest := validRequest
	conflictRequest.IdempotencyKey = "different-key"
	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/results", conflictRequest)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("valid sequence reuse should conflict, got %d", resp.StatusCode)
	}

	summaryRequest := ResultRequest{
		TaskID: taskID, WorkerID: "worker-api", AttemptID: claim.AttemptID,
		IdempotencyKey: "summary-3", Sequence: 3, Kind: string(models.ResultKindSummary),
		Payload: map[string]any{"rowCount": 1, "status": "done"},
	}
	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/results", summaryRequest)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final summary submission failed: %d", resp.StatusCode)
	}

	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/status", StatusRequest{
		TaskID: taskID, WorkerID: "worker-api", AttemptID: claim.AttemptID,
		Status: string(models.TaskStatusDone), Message: "invalid attempt",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("done with a retained invalid batch should fail closed, got %d", resp.StatusCode)
	}

	resp, err := srv.Client().Get(srv.URL + "/api/v1/tasks/" + taskID + "/results?limit=1&include_invalid=true")
	if err != nil {
		t.Fatal(err)
	}
	var results TaskResultsResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || results.TaskID != taskID || results.RuleVersionNumber != version.Version || results.Page.Total != 1 {
		t.Fatalf("unexpected result page identity: status=%d response=%+v", resp.StatusCode, results)
	}
	if len(results.Page.Batches) != 1 || len(results.Page.InvalidBatches) != 1 || results.Page.Summary == nil {
		t.Fatalf("result page omitted batches, diagnostics, or summary: %+v", results.Page)
	}
	if results.OutputSchema["type"] != "object" || results.Page.Batches[0].AttemptID != claim.AttemptID {
		t.Fatalf("result page omitted schema or attempt lineage: %+v", results)
	}
}

func TestVersionedTaskAPIRejectsInvalidInputsAndEnvelope(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()
	version := createAPIExecutionVersion(t, st, "api-invalid-rule")

	for _, variables := range []map[string]any{{}, {"query": "books", "extra": true}} {
		resp := postExecutionJSON(t, srv.Client(), srv.URL+"/admin/tasks", map[string]any{
			"ruleId": version.RuleID, "ruleVersionNumber": version.Version, "variables": variables,
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid inputs should return 400, got %d for %v", resp.StatusCode, variables)
		}
	}
	resp := postExecutionJSON(t, srv.Client(), srv.URL+"/admin/tasks", map[string]any{
		"ruleId": version.RuleID, "ruleVersion": "missing-version",
		"variables": map[string]any{"query": "books"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid immutable version label should return 400, got %d", resp.StatusCode)
	}

	resp = postExecutionJSON(t, srv.Client(), srv.URL+"/results", map[string]any{
		"taskId": "task", "workerId": "worker", "attemptId": "attempt", "payload": map[string]any{},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("partial versioned envelope should return 400, got %d", resp.StatusCode)
	}

	resp, err := srv.Client().Get(srv.URL + "/api/v1/tasks/missing/results?limit=0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing task should remain hidden, got %d", resp.StatusCode)
	}
}
