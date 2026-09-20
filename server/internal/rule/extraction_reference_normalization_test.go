package rule

import (
	"encoding/json"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestNormalizeWorkflowExtractionReferences(t *testing.T) {
	rule := &models.Rule{
		ID: "r", Version: "1", Name: "R",
		Domain: models.JSON(`["example.com"]`), Entry: "https://example.com",
		Variables: models.JSON(`{}`),
		Steps: models.JSON(`[
			{"action":"extract","name":"rows","multiple":true,"fields":{"text":{"type":"text"}}},
			{"action":"loop","type":"forEach","items":"{{extracted.rows}}","as":"item","steps":[
				{"action":"click","target":{"family":"textVisible","value":"{{loopItem.text}}","name":""}},
				{"action":"extractPageInfo","name":"dest","fields":{"website":{"type":"domain"}}},
				{"action":"sendResult","immediate":true,"payload":{"text":"{{loopItem.text}}","website":"{{dest.website}}"}}
			]}
		]`),
	}
	changed, err := normalizeWorkflowExtractionReferences(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected canonicalization to trigger")
	}
	var steps []map[string]any
	if err := json.Unmarshal(rule.Steps, &steps); err != nil {
		t.Fatal(err)
	}
	payload := steps[1]["steps"].([]any)[2].(map[string]any)["payload"].(map[string]any)
	if payload["website"] != "{{extracted.dest.website}}" {
		t.Fatalf("website not canonicalized: %v", payload["website"])
	}
	// loopItem and already-canonical references must remain untouched.
	if payload["text"] != "{{loopItem.text}}" {
		t.Fatalf("loopItem reference was rewritten: %v", payload["text"])
	}
}

func TestNormalizeWorkflowExtractionReferencesLeavesUnknownRoots(t *testing.T) {
	rule := &models.Rule{
		ID: "r", Version: "1", Name: "R",
		Domain: models.JSON(`["example.com"]`), Entry: "https://example.com",
		Variables: models.JSON(`{}`),
		Steps: models.JSON(`[
			{"action":"extract","name":"rows","fields":{"text":{"type":"text"}}},
			{"action":"sendResult","immediate":true,"payload":{"text":"{{unrelated.value}}"}}
		]`),
	}
	changed, err := normalizeWorkflowExtractionReferences(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unknown root must not be rewritten; validation fails closed instead")
	}
}
