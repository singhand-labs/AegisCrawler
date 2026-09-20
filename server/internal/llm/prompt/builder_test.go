package prompt

import (
	"strings"
	"testing"
)

func TestBuildIncludesBaselineAndHint(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "http://example.com"},
		"events":       []any{},
		"domSnapshots": []any{},
	}
	baseline := map[string]any{"id": "r1", "steps": []any{}}
	sys, user, err := Build(recording, baseline, "把价格也抓下来")
	if err != nil {
		t.Fatal(err)
	}
	if sys == "" {
		t.Fatal("system prompt empty")
	}
	if !strings.Contains(user, "r1") {
		t.Fatal("baseline missing")
	}
	if !strings.Contains(user, "把价格也抓下来") {
		t.Fatal("user hint missing")
	}
}

func TestBuildWithVersionLoadsVersionedTemplate(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "http://example.com"},
		"events":       []any{},
		"domSnapshots": []any{},
	}
	baseline := map[string]any{"id": "r1", "steps": []any{}}
	sys, _, err := BuildWithVersion(recording, baseline, "", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sys, "prompt_version: 2026-07-09-v1") {
		t.Fatal("expected versioned system prompt to contain v1 header")
	}
}

func TestBuildWithVersionFallsBackToDefault(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "http://example.com"},
		"events":       []any{},
		"domSnapshots": []any{},
	}
	baseline := map[string]any{"id": "r1", "steps": []any{}}
	sys, _, err := BuildWithVersion(recording, baseline, "", "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if sys == "" {
		t.Fatal("system prompt empty")
	}
	if !strings.Contains(sys, "AegisCrawler") {
		t.Fatal("expected fallback to default system prompt")
	}
}

func TestBuildRedactsSecrets(t *testing.T) {
	recording := map[string]any{
		"meta":   map[string]any{"startUrl": "http://example.com"},
		"events": []any{},
		"domSnapshots": []any{
			map[string]any{
				"tag":       "input",
				"outerHTML": "<input value='password: secret123'>",
			},
		},
	}
	baseline := map[string]any{"id": "r1", "steps": []any{}}
	_, user, err := Build(recording, baseline, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(user, "secret123") {
		t.Fatal("secret value should be redacted from prompt")
	}
	if !strings.Contains(user, "[REDACTED]") {
		t.Fatal("expected [REDACTED] placeholder in prompt")
	}
	if !strings.Contains(user, "input") {
		t.Fatal("tag name should be preserved for selector inference")
	}
}

func TestBuildWithVersion_BaselineMarshalError(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "http://example.com"},
		"events":       []any{},
		"domSnapshots": []any{},
	}
	baseline := map[string]any{"id": make(chan int)}
	_, _, err := BuildWithVersion(recording, baseline, "", "")
	if err == nil {
		t.Fatal("expected baseline JSON marshal error")
	}
}

func TestExtractDomTree_LastSnapshotNotMap(t *testing.T) {
	recording := map[string]any{
		"domSnapshots": []any{
			map[string]any{"domTree": "tree1"},
			"not-a-map",
		},
	}
	got := extractDomTree(recording)
	if got != nil {
		t.Fatalf("expected nil for non-map last snapshot, got %v", got)
	}
}

func TestBuildWithVersion_EventsMarshalError(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "http://example.com"},
		"events":       []any{make(chan int)},
		"domSnapshots": []any{},
	}
	baseline := map[string]any{"id": "r1"}
	_, _, err := BuildWithVersion(recording, baseline, "", "")
	if err == nil {
		t.Fatal("expected events JSON marshal error")
	}
}

func TestBuildWithVersion_SnapshotsMarshalError(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "http://example.com"},
		"events":       []any{},
		"domSnapshots": []any{make(chan int)},
	}
	baseline := map[string]any{"id": "r1"}
	_, _, err := BuildWithVersion(recording, baseline, "", "")
	if err == nil {
		t.Fatal("expected snapshots JSON marshal error")
	}
}

func TestBuildWithVersion_MetaMarshalError(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"ch": make(chan int)},
		"events":       []any{},
		"domSnapshots": []any{},
	}
	baseline := map[string]any{"id": "r1"}
	_, _, err := BuildWithVersion(recording, baseline, "", "")
	if err == nil {
		t.Fatal("expected meta JSON marshal error")
	}
}

func TestBuildWithVersion_DomTreeMarshalError(t *testing.T) {
	recording := map[string]any{
		"meta":   map[string]any{"startUrl": "http://example.com"},
		"events": []any{},
		"domSnapshots": []any{
			map[string]any{"domTree": map[string]any{"ch": make(chan int)}},
		},
	}
	baseline := map[string]any{"id": "r1"}
	_, _, err := BuildWithVersion(recording, baseline, "", "")
	if err == nil {
		t.Fatal("expected domTree JSON marshal error")
	}
}
