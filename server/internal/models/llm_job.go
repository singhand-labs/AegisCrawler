package models

import "time"

// LLMJobStatus represents the lifecycle state of an asynchronous LLM enhancement job.
type LLMJobStatus string

const (
	LLMJobStatusPending   LLMJobStatus = "pending"
	LLMJobStatusRunning   LLMJobStatus = "running"
	LLMJobStatusCompleted LLMJobStatus = "completed"
	LLMJobStatusFailed    LLMJobStatus = "failed"
)

// LLMJob stores an asynchronous rule enhancement request and its result.
type LLMJob struct {
	ID           string     `json:"id"`
	WorkspaceID  string     `json:"workspaceId"`
	RuleID       string     `json:"ruleId"`
	Baseline     JSON       `json:"baseline" swaggertype:"object"`
	Recording    JSON       `json:"recording" swaggertype:"object"`
	UserHint     string     `json:"userHint"`
	Status       string     `json:"status"`
	ResultRule   JSON       `json:"resultRule" swaggertype:"object"`
	ResultPatch  JSON       `json:"resultPatch" swaggertype:"object"`
	ResultError  string     `json:"resultError"`
	Provider     string     `json:"provider"`
	Model        string     `json:"model"`
	InputTokens  int        `json:"inputTokens"`
	OutputTokens int        `json:"outputTokens"`
	SafetyFlags  JSON       `json:"safetyFlags" swaggertype:"object"`
	Suggestions  JSON       `json:"suggestions" swaggertype:"object"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt"`
	CompletedAt  *time.Time `json:"completedAt"`
	AttemptCount int        `json:"attemptCount"`
}
