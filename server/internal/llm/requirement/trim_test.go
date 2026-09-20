package requirement

import (
	"encoding/json"
	"strings"
	"testing"
)

func bigSnapshot(childCount, childBytes int) timelineItem {
	children := make([]any, 0, childCount)
	for i := 0; i < childCount; i++ {
		padding := strings.Repeat("x", childBytes)
		children = append(children, map[string]any{
			"type":  "element", "tagName": "div",
			"class": padding,
			"children": []any{
				map[string]any{"type": "text", "text": padding},
			},
		})
	}
	return timelineItem{
		Kind: "snapshot", Index: 3, Position: 4,
		Value: map[string]any{
			"phase": "final", "url": "https://fixture.test/page",
			"domTree": map[string]any{
				"type": "element", "tagName": "main", "children": children,
			},
		},
	}
}

// A single item far larger than the chunk budget must be trimmed to fit and
// must carry explicit trim markers instead of silently dropping content.
func TestTrimTimelineItemReducesOversizedSnapshot(t *testing.T) {
	item := bigSnapshot(40, 4000) // ~160KB+ payload
	budget := 8_000
	trimmed, err := trimTimelineItem(item, budget)
	if err != nil {
		t.Fatalf("trimTimelineItem error = %v", err)
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > budget {
		t.Fatalf("trimmed item is %d bytes, budget %d", len(encoded), budget)
	}
	payload, _ := trimmed.Value.(map[string]any)
	if payload["snapshotTrimmed"] == nil {
		t.Fatal("trimmed snapshot lacks the snapshotTrimmed marker")
	}
	if !strings.Contains(string(encoded), "__trimmedSiblings__") {
		t.Fatal("expected sibling trim markers inside the trimmed domTree")
	}
}

// Items already within budget pass through byte-identical (no markers added).
func TestTrimTimelineItemLeavesSmallItemsUnchanged(t *testing.T) {
	item := bigSnapshot(2, 50)
	trimmed, err := trimTimelineItem(item, 64_000)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(item)
	after, _ := json.Marshal(trimmed)
	if string(original) != string(after) {
		t.Fatal("small item must not be modified")
	}
}

// Items without a trimmable snapshot payload fail loudly instead of emitting
// an over-limit chunk.
func TestTrimTimelineItemFailsClosedWithoutDomTree(t *testing.T) {
	item := timelineItem{Kind: "event", Index: 1, Value: "plain string"}
	_, err := trimTimelineItem(item, 4)
	if err == nil {
		t.Fatal("expected an error for an untrimmable oversized item")
	}
}

// The prompt chunker must never emit a chunk whose content exceeds the byte
// budget derived from the input token limit, even when one snapshot dwarfs
// everything else in the recording.
func TestBuildPromptChunksBoundsOversizedSnapshot(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"sanitizationVersion": "extension-v2"},
		"snapshots": []any{
			bigSnapshot(2, 40).Value,
			bigSnapshot(120, 3000).Value, // the React-page monster snapshot
			bigSnapshot(2, 40).Value,
		},
	}
	chunks, _, err := buildPromptChunks(recording, 8_000)
	if err != nil {
		t.Fatalf("buildPromptChunks error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for _, chunk := range chunks {
		if len(chunk.Content) > 8_000*3+2_048 {
			t.Fatalf("chunk %d content is %d bytes, over budget", chunk.Index, len(chunk.Content))
		}
	}
	joined := strings.Join(chunkContents(chunks), "\n")
	if !strings.Contains(joined, "__trimmedSiblings__") {
		t.Fatal("expected trim markers in the chunked content")
	}
}

func chunkContents(chunks []promptChunk) []string {
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, chunk.Content)
	}
	return out
}
