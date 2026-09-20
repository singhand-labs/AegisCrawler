package intent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type rawCandidate struct {
	ID                 string   `json:"id"`
	Label              string   `json:"label"`
	Description        string   `json:"description"`
	Confidence         float64  `json:"confidence"`
	SuggestedVariables []string `json:"suggestedVariables"`
}

type rawPrediction struct {
	Candidates []rawCandidate `json:"candidates"`
}

// ParseCandidates parses the LLM response content into normalized candidates.
func ParseCandidates(content string) ([]Candidate, error) {
	content = stripMarkdownFences(content)
	var raw rawPrediction
	if err := strictDecodeJSON([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("parse intent candidates: %w", err)
	}
	if len(raw.Candidates) == 0 {
		return nil, fmt.Errorf("parse intent candidates: no candidates returned")
	}
	out := make([]Candidate, 0, len(raw.Candidates))
	for i, rc := range raw.Candidates {
		if strings.TrimSpace(rc.Label) == "" {
			return nil, fmt.Errorf("candidate %d missing label", i)
		}
		if rc.ID == "" {
			rc.ID = fmt.Sprintf("c%d", i+1)
		}
		out = append(out, Candidate{
			ID:                 rc.ID,
			Label:              rc.Label,
			Description:        rc.Description,
			Confidence:         rc.Confidence,
			SuggestedVariables: rc.SuggestedVariables,
		})
	}
	return out, nil
}

func stripMarkdownFences(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	if idx := strings.Index(content, "\n"); idx >= 0 {
		content = content[idx+1:]
	}
	content = strings.TrimSpace(content)
	if strings.HasSuffix(content, "```") {
		if idx := strings.LastIndex(content, "\n"); idx >= 0 {
			content = content[:idx]
		}
	}
	return strings.TrimSpace(content)
}

// strictDecodeJSON mirrors requirement.strictDecodeJSON. Kept local to avoid
// a cross-package dependency from intent -> requirement; the two packages
// evolve independently.
func strictDecodeJSON(b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}
