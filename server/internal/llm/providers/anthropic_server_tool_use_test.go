package providers

import (
	"testing"
)

// The official Anthropic API reports server-tool usage alongside token usage.
// The adapter must accept it without failing the usage contract.
func TestAnthropicUsageAcceptsServerToolUse(t *testing.T) {
	payload := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"glm-5-turbo",
		"content":[{"type":"text","text":"ok"}],
		"usage":{"input_tokens":14,"output_tokens":16,"cache_read_input_tokens":0,
			"server_tool_use":{"web_search_requests":0},"service_tier":"standard"}}`)
	unknown := unknownUsageCategories(payload, map[string]struct{}{
		"input_tokens":                {},
		"cache_read_input_tokens":     {},
		"cache_creation_input_tokens": {},
		"output_tokens":               {},
		"service_tier":                {},
		"server_tool_use":             {},
	})
	if len(unknown) != 0 {
		t.Fatalf("server_tool_use must be a known usage category, got unknown: %v", unknown)
	}
}
