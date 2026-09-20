package llm

// EnhancementSuggestion is the LLM's patch-shaped output.
type EnhancementSuggestion struct {
	Selectors   map[string]SelectorSuggestion `json:"selectors"`
	Steps       []PatchOp                     `json:"steps"`
	Variables   map[string]string             `json:"variables"`
	Suggestions []string                      `json:"suggestions"`
}

// SelectorSuggestion describes a selector change.
type SelectorSuggestion struct {
	Selector string `json:"selector"`
	Reason   string `json:"reason"`
}

// PatchOp is an RFC-6902-like operation on the baseline rule.
type PatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}
