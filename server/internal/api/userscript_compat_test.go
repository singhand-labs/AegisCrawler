package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestLegacyUserscriptFinalMarkerBecomesSummary(t *testing.T) {
	srv, persistence := newTestServer(t)
	defer srv.Close()
	taskID := createRuleAndTask(t, srv)

	for _, request := range []ResultRequest{
		{TaskID: taskID, WorkerID: "userscript-worker", Payload: map[string]any{"items": []any{"value"}}},
		{TaskID: taskID, WorkerID: "userscript-worker", Payload: map[string]any{"__final": true, "taskId": taskID}},
	} {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(srv.URL+"/results", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("submit userscript result: expected 200, got %d", response.StatusCode)
		}
	}

	page, err := persistence.ListResultPage(context.Background(), taskID, 100, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Batches) != 1 {
		t.Fatalf("expected one collection batch, got total=%d batches=%d", page.Total, len(page.Batches))
	}
	if page.Summary == nil || page.Summary.Kind != models.ResultKindSummary {
		t.Fatalf("expected final marker to be stored as summary, got %+v", page.Summary)
	}
}

func TestLegacyResultKindRequiresExactFinalMarker(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload any
	}{
		{name: "wrong task", payload: map[string]any{"__final": true, "taskId": "other"}},
		{name: "additional data", payload: map[string]any{"__final": true, "taskId": "task-1", "items": []any{1}}},
		{name: "not final", payload: map[string]any{"__final": false, "taskId": "task-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := legacyResultKind(test.payload, "task-1"); got != models.ResultKindBatch {
				t.Fatalf("expected batch, got %s", got)
			}
		})
	}
}
