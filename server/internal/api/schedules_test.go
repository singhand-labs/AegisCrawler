package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

func createApprovedRule(t *testing.T, srv *httptest.Server, id string) {
	t.Helper()
	rule := models.Rule{
		ID:             id,
		Version:        "1.0.0",
		Name:           id,
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		Enabled:        true,
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
}

func TestCreateScheduleOnce(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-once")

	runAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	req := CreateScheduleRequest{
		RuleID:      "sched-rule-once",
		RuleVersion: "1.0.0",
		Name:        "once-schedule",
		Type:        string(models.ScheduleTypeOnce),
		Expression:  runAt.Format(time.RFC3339),
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var sch ScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&sch); err != nil {
		t.Fatal(err)
	}
	if sch.RuleID != "sched-rule-once" {
		t.Fatalf("expected rule id sched-rule-once, got %s", sch.RuleID)
	}
	if sch.Type != string(models.ScheduleTypeOnce) {
		t.Fatalf("expected type once, got %s", sch.Type)
	}
	if sch.NextRunAt == nil || !sch.NextRunAt.Equal(runAt) {
		t.Fatalf("expected next run %v, got %v", runAt, sch.NextRunAt)
	}

	// Verify in store.
	stored, err := st.GetScheduleByID(context.Background(), sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Type != models.ScheduleTypeOnce {
		t.Fatalf("expected type once, got %s", stored.Type)
	}
}

func TestCreateScheduleCron(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-cron")

	req := CreateScheduleRequest{
		RuleID:      "sched-rule-cron",
		RuleVersion: "1.0.0",
		Name:        "cron-schedule",
		Type:        string(models.ScheduleTypeCron),
		Expression:  "*/10 * * * *",
		Priority:    stringPtr("high"),
		MaxRetries:  intPtr(7),
		Catchup:     stringPtr("run_once"),
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var sch ScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&sch); err != nil {
		t.Fatal(err)
	}
	if sch.Type != string(models.ScheduleTypeCron) {
		t.Fatalf("expected type cron, got %s", sch.Type)
	}
	if sch.Priority != "high" {
		t.Fatalf("expected priority high, got %s", sch.Priority)
	}
	if sch.MaxRetries != 7 {
		t.Fatalf("expected maxRetries 7, got %d", sch.MaxRetries)
	}
	if sch.Catchup != "run_once" {
		t.Fatalf("expected catchup run_once, got %s", sch.Catchup)
	}
	if sch.NextRunAt == nil {
		t.Fatal("expected nextRunAt set")
	}

	stored, err := st.GetScheduleByID(context.Background(), sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Catchup != models.CatchupRunOnce {
		t.Fatalf("expected catchup run_once, got %s", stored.Catchup)
	}
}

func TestCreateScheduleRequiresApprovedRule(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	rule := models.Rule{
		ID:             "pending-sched-rule",
		Version:        "1.0.0",
		Name:           "Pending",
		Domain:         store.JSON("example.com"),
		Steps:          store.JSON([]any{"step1"}),
		Priority:       models.PriorityNormal,
		Enabled:        true,
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

	req := CreateScheduleRequest{
		RuleID:     "pending-sched-rule",
		Type:       string(models.ScheduleTypeOnce),
		Expression: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	body, _ = json.Marshal(req)
	resp, err = http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCreateScheduleValidatesCron(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-bad-cron")

	req := CreateScheduleRequest{
		RuleID:     "sched-rule-bad-cron",
		Type:       string(models.ScheduleTypeCron),
		Expression: "invalid",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestListSchedulesEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-list")

	for i := 0; i < 2; i++ {
		req := CreateScheduleRequest{
			RuleID:     "sched-rule-list",
			Type:       string(models.ScheduleTypeOnce),
			Expression: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			Name:       "list-sched",
		}
		body, _ := json.Marshal(req)
		resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(srv.URL + "/admin/schedules?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected total 2, got %d", list.Total)
	}
	if len(list.Schedules) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(list.Schedules))
	}

	// Filter by type.
	resp, err = http.Get(srv.URL + "/admin/schedules?type=once")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("expected 2 once schedules, got %d", list.Total)
	}

	// Bad enabled param.
	resp, err = http.Get(srv.URL + "/admin/schedules?enabled=notbool")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetUpdateDeleteScheduleEndpoints(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-crud")

	req := CreateScheduleRequest{
		RuleID:     "sched-rule-crud",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
		Name:       "crud-sched",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var created ScheduleResponse
	if err := json.Unmarshal(body, &created); err != nil {
		// not the response body; fetch instead
	}
	resp, err = http.Get(srv.URL + "/admin/schedules")
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

	// Get.
	resp, err = http.Get(srv.URL + "/admin/schedules/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got ScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Fatalf("expected id %s, got %s", id, got.ID)
	}

	// Update.
	update := map[string]any{
		"name":       "updated-name",
		"expression": "*/10 * * * *",
		"enabled":    false,
	}
	body, _ = json.Marshal(update)
	hreq, err := http.NewRequest(http.MethodPatch, srv.URL+"/admin/schedules/"+id, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	stored, err := st.GetScheduleByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "updated-name" {
		t.Fatalf("expected name updated-name, got %s", stored.Name)
	}
	if stored.Enabled {
		t.Fatal("expected schedule disabled")
	}

	// Delete.
	dreq, err := http.NewRequest(http.MethodDelete, srv.URL+"/admin/schedules/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if _, err := st.GetScheduleByID(context.Background(), id); !errors.Is(err, store.ErrRuleNotFound) {
		t.Fatalf("expected schedule deleted, got %v", err)
	}
}

func TestTriggerScheduleEndpoint(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-trigger")

	req := CreateScheduleRequest{
		RuleID:     "sched-rule-trigger",
		Type:       string(models.ScheduleTypeCron),
		Expression: "*/5 * * * *",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/admin/schedules", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/admin/schedules")
	if err != nil {
		t.Fatal(err)
	}
	var list ListSchedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := list.Schedules[0].ID

	resp, err = http.Post(srv.URL+"/admin/schedules/"+id+"/trigger", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var trig TriggerScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&trig); err != nil {
		t.Fatal(err)
	}
	if trig.TaskID == "" {
		t.Fatal("expected task id")
	}

	// Verify task exists and has schedule id.
	task, err := st.GetTaskByID(context.Background(), trig.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !task.ScheduleID.Valid || task.ScheduleID.String != id {
		t.Fatalf("expected schedule id %s, got %v", id, task.ScheduleID)
	}

	// Trigger missing schedule returns 404.
	resp, err = http.Post(srv.URL+"/admin/schedules/missing/trigger", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestCreateTaskWithFutureScheduledAt(t *testing.T) {
	srv, st := newTestServer(t)
	defer srv.Close()

	createApprovedRule(t, srv, "sched-rule-task")

	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	task := models.Task{
		ID:          store.NewID(),
		RuleID:      "sched-rule-task",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		ScheduledAt: sql.NullTime{Time: future, Valid: true},
	}
	body, _ := json.Marshal(task)
	resp, err := http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	stored, err := st.GetTaskByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ScheduledAt.Valid || !stored.ScheduledAt.Time.Equal(future) {
		t.Fatalf("expected scheduled_at %v, got %v", future, stored.ScheduledAt)
	}

	// Past scheduled_at is treated as immediate.
	past := time.Now().UTC().Add(-time.Hour)
	task2 := models.Task{
		ID:          store.NewID(),
		RuleID:      "sched-rule-task",
		RuleVersion: "1.0.0",
		Status:      models.TaskStatusPending,
		Priority:    models.PriorityNormal,
		MaxRetries:  3,
		ScheduledAt: sql.NullTime{Time: past, Valid: true},
	}
	body, _ = json.Marshal(task2)
	resp, err = http.Post(srv.URL+"/admin/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	stored2, err := st.GetTaskByID(context.Background(), task2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored2.ScheduledAt.Valid {
		t.Fatalf("expected scheduled_at cleared for past time, got %v", stored2.ScheduledAt)
	}
}

func stringPtr(s string) *string { return &s }
func intPtr(n int) *int          { return &n }
