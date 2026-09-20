package timelinetrim

import (
	"encoding/json"
	"strings"
	"testing"
)

func bigItem(childCount, childBytes int) Item {
	children := make([]any, 0, childCount)
	for i := 0; i < childCount; i++ {
		padding := strings.Repeat("x", childBytes)
		children = append(children, map[string]any{
			"type": "element", "tagName": "div", "class": padding,
			"children": []any{map[string]any{"type": "text", "text": padding}},
		})
	}
	return Item{
		Kind: "snapshot", Index: 1, Position: 2,
		Value: map[string]any{
			"phase": "final", "url": "https://fixture.test/page",
			"capture": map[string]any{"status": "complete", "notes": strings.Repeat("n", 400)},
			"domTree": map[string]any{
				"type": "element", "tagName": "main", "children": children,
			},
		},
	}
}

// The final wrapped item — domTree plus the snapshot's own fields plus the
// snapshotTrimmed marker — must fit the budget, not just the trimmed tree.
func TestTrimWholeItemFitsBudgetIncludingWrapper(t *testing.T) {
	item := bigItem(120, 1500) // ~360KB+
	budget := 20_000
	trimmed, err := Trim(item, budget)
	if err != nil {
		t.Fatalf("Trim error = %v", err)
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > budget {
		t.Fatalf("wrapped item is %d bytes, budget %d", len(encoded), budget)
	}
	if !strings.Contains(string(encoded), "snapshotTrimmed") {
		t.Fatal("expected snapshotTrimmed marker")
	}
	if !strings.Contains(string(encoded), "__trimmedSiblings__") {
		t.Fatal("expected structural trim markers")
	}
}

// Items already within budget pass through byte-identical.
func TestTrimPassesThroughSmallItems(t *testing.T) {
	item := bigItem(2, 30)
	trimmed, err := Trim(item, 64_000)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(item)
	b, _ := json.Marshal(trimmed)
	if string(a) != string(b) {
		t.Fatal("small item must not be modified")
	}
}

// Non-map payloads fail loudly instead of emitting oversized content.
func TestTrimFailsClosedOnUntrimmablePayload(t *testing.T) {
	_, err := Trim(Item{Kind: "event", Index: 0, Value: strings.Repeat("x", 100)}, 4)
	if err == nil {
		t.Fatal("expected fail-closed error for untrimmable payload")
	}
}
