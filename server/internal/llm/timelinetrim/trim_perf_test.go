package timelinetrim

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A deep, wide React-like tree (thousands of nodes, long hashed class names)
// must trim in bounded time — the earlier one-marker-per-pass implementation
// CPU-spun for minutes on exactly this shape.
func TestTrimHandlesReactShapedTreeInBoundedTime(t *testing.T) {
	var buildNode func(depth int) map[string]any
	buildNode = func(depth int) map[string]any {
		if depth <= 0 {
			return map[string]any{"type": "text", "text": strings.Repeat("t", 200)}
		}
		kids := make([]any, 0, 12)
		for i := 0; i < 12; i++ {
			kids = append(kids, map[string]any{
				"type": "element", "tagName": "div",
				"class": strings.Repeat("css-module-hash-", 12),
				"children": []any{buildNode(depth - 1)},
			})
		}
		return map[string]any{"type": "element", "tagName": "section", "children": kids}
	}
	item := Item{Kind: "snapshot", Index: 0, Position: 0, Value: map[string]any{
		"phase": "final", "url": "https://duckduckgo.com/?q=x",
		"domTree": buildNode(4), // 12^4 * ~250B ≈ 5MB+
	}}
	raw, _ := json.Marshal(item)
	if len(raw) < 1_000_000 {
		t.Fatalf("fixture too small to exercise the hot path: %dB", len(raw))
	}
	done := make(chan struct{})
	var trimmed Item
	var err error
	go func() { trimmed, err = Trim(item, 20_000); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Trim did not finish within 10s on a React-shaped tree")
	}
	if err != nil {
		t.Fatalf("Trim error = %v", err)
	}
	encoded, _ := json.Marshal(trimmed)
	if len(encoded) > 20_000 {
		t.Fatalf("trimmed item %dB exceeds budget 20000", len(encoded))
	}
}
