package dsl

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/config"
)

// The real sanitized React-DDG recording (6.8MB, 21 snapshots of up to 451KB)
// must chunk deterministically instead of terminally failing on an oversized
// atomic item. Skips when the local recording dump is absent.
func TestBuildWorkflowChunksRealDDGRecording(t *testing.T) {
	raw, err := os.ReadFile("/tmp/aegis-test/ddg-sanitized.json")
	if err != nil {
		t.Skip("recording not present")
	}
	var recording map[string]any
	if err := json.Unmarshal(raw, &recording); err != nil {
		t.Fatal(err)
	}
	w := &Workflow{cfg: &config.Config{LLMEnabled: true, LLMMaxInputTokens: 40_000, LLMMaxOutputTokens: 1_024}}
	directBudget, err := w.requestContentBudget(generationSystemPrompt, generationUserPrompt("{}", "{}", "", "", "{}"), nil)
	if err != nil {
		t.Fatalf("direct budget: %v", err)
	}
	analysisBudget, err := w.requestContentBudget(analysisSystemPrompt, analysisUserPrompt(21, 21, "{}", "{}", "{}", ""), nil)
	if err != nil {
		t.Fatalf("analysis budget: %v", err)
	}
	chunks, _, err := buildWorkflowChunks(recording, directBudget, analysisBudget)
	if err != nil {
		t.Fatalf("buildWorkflowChunks error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for index, chunk := range chunks {
		if len(chunk.Content) > analysisBudget {
			t.Fatalf("chunk %d is %d bytes, over the %d-byte budget", index, len(chunk.Content), analysisBudget)
		}
	}
}
