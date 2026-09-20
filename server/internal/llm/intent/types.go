package intent

// Candidate represents one predicted user intent.
type Candidate struct {
	ID                 string   `json:"id"`
	Label              string   `json:"label"`
	Description        string   `json:"description"`
	Confidence         float64  `json:"confidence"`
	SuggestedVariables []string `json:"suggestedVariables"`
	Source             string   `json:"source,omitempty"` // "llm" or "synthetic"
}

// PredictionResult is the normalized output of the intent predictor.
type PredictionResult struct {
	Candidates     []Candidate `json:"candidates"`
	FallbackIntent Candidate   `json:"fallbackIntent"`
	Model          string      `json:"model"`
	CacheHit       bool        `json:"cacheHit"`
}
