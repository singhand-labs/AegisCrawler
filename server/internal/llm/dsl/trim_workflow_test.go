package dsl

import (
	"encoding/json"
	"strings"
	"testing"
)

func bigWorkflowSnapshot(childCount, childBytes int) map[string]any {
	children := make([]any, 0, childCount)
	for i := 0; i < childCount; i++ {
		padding := strings.Repeat("x", childBytes)
		children = append(children, map[string]any{
			"type": "element", "tagName": "div",
			"class": padding,
			"children": []any{map[string]any{"type": "text", "text": padding}},
		})
	}
	return map[string]any{
		"phase": "final", "url": "https://fixture.test/page",
		"domTree": map[string]any{
			"type": "element", "tagName": "main", "children": children,
		},
	}
}

// One monster snapshot (a heavy React page) must not terminally fail the DSL
// workflow: it gets trimmed into the chunk budget with explicit markers.
func TestBuildWorkflowChunksTrimsOversizedSnapshot(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"sanitizationVersion": "extension-v2"},
		"snapshots": []any{
			bigWorkflowSnapshot(2, 40),
			bigWorkflowSnapshot(400, 2000), // ~800KB+ monster
			bigWorkflowSnapshot(2, 40),
		},
	}
	// small budgets force chunking; the monster alone far exceeds them
	chunks, _, err := buildWorkflowChunks(recording, 4_096, 12_000)
	if err != nil {
		t.Fatalf("buildWorkflowChunks error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	joined := ""
	for _, chunk := range chunks {
		joined += chunk.Content
	}
	if !strings.Contains(joined, "__trimmedSiblings__") {
		t.Fatal("expected trim markers inside the chunked recording")
	}
	var parseTarget map[string]any
	if err := json.Unmarshal([]byte(chunks[0].Content), &parseTarget); err != nil {
		t.Fatalf("chunk content is not valid JSON: %v", err)
	}
}

// Items that already fit pass through without markers.
func TestBuildWorkflowChunksLeavesSmallRecordingsUnchanged(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"sanitizationVersion": "extension-v2"},
		"snapshots": []any{bigWorkflowSnapshot(2, 40)},
	}
	chunks, direct, err := buildWorkflowChunks(recording, 64_000, 64_000)
	if err != nil {
		t.Fatal(err)
	}
	if !direct || len(chunks) != 1 {
		t.Fatalf("expected direct single chunk, direct=%v chunks=%d", direct, len(chunks))
	}
	if strings.Contains(chunks[0].Content, "__trimmed") {
		t.Fatal("small recording must not carry trim markers")
	}
}
