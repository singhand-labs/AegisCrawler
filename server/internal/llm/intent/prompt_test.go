package intent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPredictPrompt(t *testing.T) {
	recording := map[string]any{
		"meta":         map[string]any{"startUrl": "https://example.com", "title": "Example"},
		"events":       []any{map[string]any{"type": "click", "index": 1}},
		"domSnapshots": []any{},
	}
	sys, user, err := BuildPredictPrompt(recording)
	require.NoError(t, err)
	assert.Contains(t, sys, "网页数据采集意图分析专家")
	assert.Contains(t, user, "https://example.com")
	assert.Contains(t, user, "click")
}

func TestBuildPredictPrompt_RedactsSensitiveData(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"startUrl": "https://example.com"},
		"events": []any{
			map[string]any{"type": "input", "value": "password: secret123"},
		},
		"domSnapshots": []any{},
	}
	_, user, err := BuildPredictPrompt(recording)
	require.NoError(t, err)
	assert.NotContains(t, user, "secret123")
}

func TestBuildPredictPrompt_HandlesMissingFields(t *testing.T) {
	recording := map[string]any{}
	sys, user, err := BuildPredictPrompt(recording)
	require.NoError(t, err)
	assert.NotEmpty(t, sys)
	assert.NotEmpty(t, user)
}

func TestBuildPredictPrompt_ReturnsErrorOnUnmarshalableData(t *testing.T) {
	recording := map[string]any{
		"meta": make(chan int),
	}
	_, _, err := BuildPredictPrompt(recording)
	require.Error(t, err)
}

func TestBuildPredictPrompt_UsesSnapshotsField(t *testing.T) {
	recording := map[string]any{
		"meta":   map[string]any{"title": "Test"},
		"events": []any{},
		"snapshots": []any{
			map[string]any{
				"timestamp": 1,
				"url":       "https://example.com",
				"domTree":   map[string]any{"type": "element", "tagName": "body"},
			},
		},
	}
	_, user, err := BuildPredictPrompt(recording)
	require.NoError(t, err)
	require.Contains(t, user, `"type": "element"`)
	require.Contains(t, user, `"tagName": "body"`)
}

func TestExtractDomTree_NonMapLastSnapshot(t *testing.T) {
	recording := map[string]any{
		"snapshots": []any{
			map[string]any{"domTree": "ok"},
			"not-a-map",
		},
	}
	assert.Nil(t, extractDomTree(recording))
}

func TestBuildPredictPrompt_NonMapLastSnapshotDoesNotPanic(t *testing.T) {
	recording := map[string]any{
		"meta":      map[string]any{"title": "Test"},
		"events":    []any{},
		"snapshots": []any{"not-a-map"},
	}
	assert.NotPanics(t, func() {
		sys, user, err := BuildPredictPrompt(recording)
		assert.NoError(t, err)
		assert.NotEmpty(t, sys)
		assert.NotEmpty(t, user)
	})
}

func TestCondenseRecordingForPrompt_KeepsOnlyLastSnapshots(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"title": "Test"},
		"events": []any{
			map[string]any{"type": "click", "index": 1},
			map[string]any{"type": "click", "index": 2},
			map[string]any{"type": "click", "index": 3},
		},
		"snapshots": []any{
			map[string]any{"timestamp": 1, "url": "http://a", "domTree": map[string]any{"tagName": "a"}, "selectorMap": map[string]any{"a": 1}},
			map[string]any{"timestamp": 2, "url": "http://b", "domTree": map[string]any{"tagName": "b"}, "selectorMap": map[string]any{"b": 2}},
			map[string]any{"timestamp": 3, "url": "http://c", "domTree": map[string]any{"tagName": "c"}, "selectorMap": map[string]any{"c": 3}},
		},
	}
	out := condenseRecordingForPrompt(recording)

	snapshots, ok := out["snapshots"].([]any)
	require.True(t, ok)
	require.Len(t, snapshots, 2)

	last, ok := snapshots[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "http://c", last["url"])
	assert.Contains(t, last, "domTree")
	assert.NotContains(t, last, "selectorMap")

	events, ok := out["events"].([]any)
	require.True(t, ok)
	assert.Len(t, events, 3)
}

func TestCondenseRecordingForPrompt_TruncatesEvents(t *testing.T) {
	events := make([]any, 100)
	for i := 0; i < 100; i++ {
		events[i] = map[string]any{"type": "click", "index": i}
	}
	recording := map[string]any{
		"events": events,
	}
	out := condenseRecordingForPrompt(recording)
	truncated, ok := out["events"].([]any)
	require.True(t, ok)
	require.Len(t, truncated, maxPromptEvents)
	first, ok := truncated[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 50, first["index"])
}

func TestTruncateDomTree_TruncatesNodesAndText(t *testing.T) {
	tree := map[string]any{
		"type":    "element",
		"tagName": "div",
		"children": []any{
			map[string]any{"type": "text", "text": strings.Repeat("a", maxPromptTextLen*2)},
			map[string]any{
				"type":    "element",
				"tagName": "span",
				"children": []any{
					map[string]any{"type": "text", "text": "short"},
				},
			},
		},
	}
	out := truncateDomTree(tree, 10, maxPromptTextLen).(map[string]any)
	assert.Equal(t, "div", out["tagName"])
	children := out["children"].([]any)
	require.Len(t, children, 2)
	text := children[0].(map[string]any)
	assert.Len(t, text["text"].(string), maxPromptTextLen+3) // includes ellipsis
}

func TestBuildPredictPrompt_LargeRecordingFitsWithinLimit(t *testing.T) {
	bigDom := buildBigDomTree(3000, 500)
	snapshots := make([]any, 10)
	for i := 0; i < 10; i++ {
		snapshots[i] = map[string]any{
			"timestamp":   float64(i),
			"url":         "https://example.com/page" + strconv.Itoa(i),
			"domTree":     bigDom,
			"selectorMap": map[string]any{"a": strings.Repeat("x", 1000)},
		}
	}
	events := make([]any, 200)
	for i := 0; i < 200; i++ {
		events[i] = map[string]any{"type": "click", "index": i}
	}
	recording := map[string]any{
		"meta":      map[string]any{"title": "Big page", "startUrl": "https://example.com"},
		"events":    events,
		"snapshots": snapshots,
	}

	sys, user, err := BuildPredictPrompt(recording)
	require.NoError(t, err)
	assert.Less(t, len(user), 2*1024*1024, "user prompt should fit within 2MB")
	assert.Contains(t, user, "Big page")
	assert.Contains(t, user, "https://example.com/page9")
	assert.NotContains(t, user, "https://example.com/page0")
	_ = sys
}

func buildBigDomTree(nodes int, textLen int) map[string]any {
	root := map[string]any{
		"type":    "element",
		"tagName": "html",
		"children": []any{
			map[string]any{
				"type":     "element",
				"tagName":  "body",
				"children": []any{},
			},
		},
	}
	body := root["children"].([]any)[0].(map[string]any)["children"].([]any)
	for i := 0; i < nodes; i++ {
		body = append(body, map[string]any{
			"type":    "element",
			"tagName": "div",
			"attributes": []map[string]any{
				{"name": "class", "value": "item"},
				{"name": "data-index", "value": strconv.Itoa(i)},
			},
			"children": []any{
				map[string]any{
					"type": "text",
					"text": strings.Repeat("x", textLen),
				},
			},
		})
	}
	root["children"].([]any)[0].(map[string]any)["children"] = body
	return root
}
