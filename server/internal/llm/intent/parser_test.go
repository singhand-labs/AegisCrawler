package intent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCandidates_ValidJSON(t *testing.T) {
	content := `{
		"candidates": [
			{"id": "c1", "label": "采集商品标题", "description": "...", "confidence": 0.9, "suggestedVariables": ["maxItems"]},
			{"id": "c2", "label": "搜索并翻页", "description": "...", "confidence": 0.1, "suggestedVariables": ["keyword"]}
		]
	}`
	cands, err := ParseCandidates(content)
	require.NoError(t, err)
	assert.Len(t, cands, 2)
	assert.Equal(t, "c1", cands[0].ID)
	assert.Equal(t, "采集商品标题", cands[0].Label)
	assert.InDelta(t, 0.9, cands[0].Confidence, 0.001)
}

func TestParseCandidates_MissingConfidenceDefaultsToZero(t *testing.T) {
	content := `{"candidates": [{"id": "c1", "label": "x"}]}`
	cands, err := ParseCandidates(content)
	require.NoError(t, err)
	assert.Len(t, cands, 1)
	assert.Equal(t, 0.0, cands[0].Confidence)
}

func TestParseCandidates_EmptyCandidatesReturnsError(t *testing.T) {
	content := `{"candidates": []}`
	_, err := ParseCandidates(content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no candidates")
}

func TestParseCandidates_MalformedJSON(t *testing.T) {
	_, err := ParseCandidates("not json")
	require.Error(t, err)
}

func TestParseCandidates_StripsMarkdownFence(t *testing.T) {
	content := "```json\n{\"candidates\":[{\"id\":\"c1\",\"label\":\"采集标题\",\"confidence\":0.85}]}\n```"
	cands, err := ParseCandidates(content)
	require.NoError(t, err)
	assert.Len(t, cands, 1)
	assert.Equal(t, "c1", cands[0].ID)
	assert.InDelta(t, 0.85, cands[0].Confidence, 0.001)
}

func TestParseCandidates_MissingLabelReturnsError(t *testing.T) {
	content := `{"candidates": [{"id": "c1", "description": "..."}]}`
	_, err := ParseCandidates(content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing label")
}

func TestParseCandidates_AssignsDefaultID(t *testing.T) {
	content := `{"candidates": [{"label": "采集标题", "confidence": 0.5}]}`
	cands, err := ParseCandidates(content)
	require.NoError(t, err)
	assert.Len(t, cands, 1)
	assert.Equal(t, "c1", cands[0].ID)
}

func TestParseCandidatesRejectsUnknownTopLevelFields(t *testing.T) {
	body := `{"candidates":[{"id":"c1","label":"L","description":"D","confidence":0.5}],"instructions":"x"}`
	if _, err := ParseCandidates(body); err == nil {
		t.Fatalf("ParseCandidates accepted unknown top-level field")
	}
}

func TestParseCandidatesRejectsUnknownCandidateFields(t *testing.T) {
	body := `{"candidates":[{"id":"c1","label":"L","description":"D","confidence":0.5,"secret_backdoor":"x"}]}`
	if _, err := ParseCandidates(body); err == nil {
		t.Fatalf("ParseCandidates accepted unknown candidate field")
	}
}
