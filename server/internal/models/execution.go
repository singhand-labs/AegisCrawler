package models

import "time"

type ExecutionAttemptStatus string

const (
	ExecutionAttemptLeased       ExecutionAttemptStatus = "leased"
	ExecutionAttemptRunning      ExecutionAttemptStatus = "running"
	ExecutionAttemptWaitingHuman ExecutionAttemptStatus = "waiting_for_human"
	ExecutionAttemptSucceeded    ExecutionAttemptStatus = "succeeded"
	ExecutionAttemptFailed       ExecutionAttemptStatus = "failed"
	ExecutionAttemptCancelled    ExecutionAttemptStatus = "cancelled"
	ExecutionAttemptLeaseExpired ExecutionAttemptStatus = "lease_expired"
	ExecutionAttemptDeadLetter   ExecutionAttemptStatus = "dead_letter"
)

type ExecutionAttempt struct {
	ID           string                 `json:"id"`
	WorkspaceID  string                 `json:"workspaceId"`
	TaskID       string                 `json:"taskId"`
	Number       int                    `json:"number"`
	WorkerID     string                 `json:"workerId"`
	Status       ExecutionAttemptStatus `json:"status"`
	LeaseUntil   *time.Time             `json:"leaseUntil,omitempty"`
	StartedAt    time.Time              `json:"startedAt"`
	CompletedAt  *time.Time             `json:"completedAt,omitempty"`
	ErrorType    string                 `json:"errorType,omitempty"`
	ErrorMessage string                 `json:"errorMessage,omitempty"`
}

type ResultKind string

const (
	ResultKindBatch   ResultKind = "batch"
	ResultKindSummary ResultKind = "summary"
)

type RuleVersionContract struct {
	WorkspaceID        string    `json:"workspaceId"`
	RuleID             string    `json:"ruleId"`
	Version            int       `json:"version"`
	InputSchema        JSON      `json:"inputSchema" swaggertype:"object"`
	OutputSchema       JSON      `json:"outputSchema" swaggertype:"object"`
	BrowserProfileID   string    `json:"browserProfileId"`
	SourceKind         string    `json:"sourceKind,omitempty"`
	SourceAuthority    string    `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string    `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string    `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string    `json:"sourceWorkflowId,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
}

type ResultPage struct {
	Batches        []*Result `json:"batches"`
	InvalidBatches []*Result `json:"invalidBatches,omitempty"`
	Summary        *Result   `json:"summary,omitempty"`
	Total          int       `json:"total"`
	Limit          int       `json:"limit"`
	Offset         int       `json:"offset"`
}
