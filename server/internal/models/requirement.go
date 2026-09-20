package models

import "time"

// RequirementValueType is the portable type vocabulary shared by collection
// inputs and output fields.
type RequirementValueType string

const (
	RequirementValueString  RequirementValueType = "string"
	RequirementValueNumber  RequirementValueType = "number"
	RequirementValueBoolean RequirementValueType = "boolean"
	RequirementValueObject  RequirementValueType = "object"
	RequirementValueArray   RequirementValueType = "array"
)

// RequirementInput describes one value supplied when a collection task is
// created. Required and optional inputs are stored in separate lists.
type RequirementInput struct {
	Name        string               `json:"name"`
	Type        RequirementValueType `json:"type"`
	Description string               `json:"description"`
	Default     any                  `json:"default,omitempty"`
	Constraints map[string]any       `json:"constraints,omitempty"`
	Secret      bool                 `json:"secret,omitempty"`
}

// RequirementOutputField describes one field emitted by the future rule.
type RequirementOutputField struct {
	Name        string               `json:"name"`
	Type        RequirementValueType `json:"type"`
	Description string               `json:"description"`
}

// CollectionRequirementSpec is the normalized contract confirmed before DSL
// generation. Candidate, edited, and custom requirements all use this schema.
type CollectionRequirementSpec struct {
	Title          string                   `json:"title"`
	Description    string                   `json:"description"`
	RequiredInputs []RequirementInput       `json:"requiredInputs"`
	OptionalInputs []RequirementInput       `json:"optionalInputs"`
	OutputFields   []RequirementOutputField `json:"outputFields"`
	SampleOutput   map[string]any           `json:"sampleOutput"`
}

// RequirementCandidate is one of exactly three distinct LLM-generated choices.
type RequirementCandidate struct {
	ID          string                    `json:"id"`
	Confidence  float64                   `json:"confidence"`
	Requirement CollectionRequirementSpec `json:"requirement"`
	Source      string                    `json:"source,omitempty"` // "llm" or "synthetic"
}

type RequirementJobKind string

const (
	RequirementJobCandidates RequirementJobKind = "candidates"
	RequirementJobNormalize  RequirementJobKind = "normalize"
)

type RequirementJobStatus string

const (
	RequirementJobPending   RequirementJobStatus = "pending"
	RequirementJobRunning   RequirementJobStatus = "running"
	RequirementJobCompleted RequirementJobStatus = "completed"
	RequirementJobFailed    RequirementJobStatus = "failed"
)

type RequirementSource string

const (
	RequirementSourceLLM    RequirementSource = "llm"
	RequirementSourceManual RequirementSource = "manual"
)

type CollectionRequirementStatus string

const (
	CollectionRequirementDraft     CollectionRequirementStatus = "draft"
	CollectionRequirementConfirmed CollectionRequirementStatus = "confirmed"
)

// RequirementJob stores durable execution metadata. Request and result content
// are populated only after authenticated decryption and are never stored plain.
type RequirementJob struct {
	ID            string               `json:"id"`
	WorkspaceID   string               `json:"workspaceId"`
	RecordingID   string               `json:"recordingId"`
	Kind          RequirementJobKind   `json:"kind"`
	Status        RequirementJobStatus `json:"status"`
	Source        RequirementSource    `json:"source"`
	Provider      string               `json:"provider,omitempty"`
	Model         string               `json:"model,omitempty"`
	PromptVersion string               `json:"promptVersion"`
	ChunkCount    int                  `json:"chunkCount"`
	// CompletedChunks counts analyzed recording chunks while the job runs;
	// it equals ChunkCount once the job completes.
	CompletedChunks int        `json:"completedChunks"`
	InputTokens     int        `json:"inputTokens"`
	OutputTokens    int        `json:"outputTokens"`
	AttemptCount    int        `json:"attemptCount"`
	MaxAttempts     int        `json:"maxAttempts"`
	AttemptBudget   int        `json:"attemptBudget"`
	AvailableAt     time.Time  `json:"availableAt"`
	LeaseUntil      *time.Time `json:"-"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	SafetyFlags     []string   `json:"safetyFlags"`
	RequestHash     string     `json:"requestHash"`
	ResultHash      string     `json:"resultHash,omitempty"`
	RequirementID   string     `json:"requirementId,omitempty"`
	Request         any        `json:"-"`
	Result          any        `json:"result,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
}

// CollectionRequirement is the durable normalized contract that feeds Stage 5.
type CollectionRequirement struct {
	ID          string                      `json:"id"`
	WorkspaceID string                      `json:"workspaceId"`
	RecordingID string                      `json:"recordingId"`
	SourceJobID string                      `json:"sourceJobId,omitempty"`
	Source      RequirementSource           `json:"source"`
	Status      CollectionRequirementStatus `json:"status"`
	ContentHash string                      `json:"contentHash"`
	Requirement CollectionRequirementSpec   `json:"requirement"`
	Owner       string                      `json:"owner"`
	CreatedAt   time.Time                   `json:"createdAt"`
	UpdatedAt   time.Time                   `json:"updatedAt"`
	ConfirmedAt *time.Time                  `json:"confirmedAt,omitempty"`
	ConfirmedBy string                      `json:"confirmedBy,omitempty"`
}
