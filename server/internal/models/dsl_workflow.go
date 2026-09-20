package models

import "time"

// DSLWorkflowStatus describes the durable state between a confirmed
// requirement and an approved immutable rule version.
type DSLWorkflowStatus string

const (
	DSLWorkflowGenerating           DSLWorkflowStatus = "generating"
	DSLWorkflowAwaitingReplay       DSLWorkflowStatus = "awaiting_replay"
	DSLWorkflowReplaying            DSLWorkflowStatus = "replaying"
	DSLWorkflowRepairing            DSLWorkflowStatus = "repairing"
	DSLWorkflowAwaitingConfirmation DSLWorkflowStatus = "awaiting_confirmation"
	DSLWorkflowFailed               DSLWorkflowStatus = "failed"
	DSLWorkflowApproved             DSLWorkflowStatus = "approved"
)

const (
	DSLWorkflowSourceAdminReviewedAttemptExport = "admin_reviewed_attempt_export"
	DSLWorkflowSourceAuthorityNonAuthoritative  = "non-authoritative"
)

type DSLJobKind string

const (
	DSLJobGenerate       DSLJobKind = "generate"
	DSLJobRepair         DSLJobKind = "repair"
	DSLJobSelectorRepair DSLJobKind = "selector_repair"
)

type DSLJobStatus string

const (
	DSLJobPending   DSLJobStatus = "pending"
	DSLJobRunning   DSLJobStatus = "running"
	DSLJobCompleted DSLJobStatus = "completed"
	DSLJobFailed    DSLJobStatus = "failed"
)

type ReplayAttemptStatus string

const (
	ReplayAttemptRunning   ReplayAttemptStatus = "running"
	ReplayAttemptSucceeded ReplayAttemptStatus = "succeeded"
	ReplayAttemptFailed    ReplayAttemptStatus = "failed"
)

// DSLWorkflow stores safe metadata and the decrypted provisional rule for an
// authenticated caller. The rule and YAML are encrypted at rest.
type DSLWorkflow struct {
	ID                 string            `json:"id"`
	WorkspaceID        string            `json:"workspaceId"`
	RequirementID      string            `json:"requirementId"`
	RecordingID        string            `json:"recordingId"`
	Status             DSLWorkflowStatus `json:"status"`
	BrowserProfileID   string            `json:"browserProfileId"`
	CurrentJobID       string            `json:"currentJobId,omitempty"`
	RepairCount        int               `json:"repairCount"`
	MaxRepairs         int               `json:"maxRepairs"`
	ProvisionalRule    *Rule             `json:"provisionalRule,omitempty"`
	ProvisionalYAML    string            `json:"provisionalYaml,omitempty"`
	ProvisionalHash    string            `json:"provisionalHash,omitempty"`
	LastReplaySequence int               `json:"lastReplaySequence"`
	ErrorCode          string            `json:"errorCode,omitempty"`
	ErrorMessage       string            `json:"errorMessage,omitempty"`
	Owner              string            `json:"owner"`
	ApprovedRuleID     string            `json:"approvedRuleId,omitempty"`
	ApprovedVersion    int               `json:"approvedVersion,omitempty"`
	SourceKind         string            `json:"sourceKind,omitempty"`
	SourceAuthority    string            `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string            `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string            `json:"sourceExportHash,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
	UpdatedAt          time.Time         `json:"updatedAt"`
	ApprovedAt         *time.Time        `json:"approvedAt,omitempty"`
}

// DSLJob is a durable LLM generation or repair job. Request content is never
// returned; completed result content is represented by the parent workflow.
type DSLJob struct {
	ID            string       `json:"id"`
	WorkspaceID   string       `json:"workspaceId"`
	WorkflowID    string       `json:"workflowId"`
	Kind          DSLJobKind   `json:"kind"`
	Status        DSLJobStatus `json:"status"`
	Provider      string       `json:"provider,omitempty"`
	Model         string       `json:"model,omitempty"`
	PromptVersion string       `json:"promptVersion"`
	ChunkCount    int          `json:"chunkCount"`
	// CompletedChunks counts analyzed recording chunks while the job runs;
	// it equals ChunkCount once the job completes.
	CompletedChunks int        `json:"completedChunks"`
	InputTokens     int        `json:"inputTokens"`
	OutputTokens    int        `json:"outputTokens"`
	AttemptCount    int        `json:"attemptCount"`
	MaxAttempts     int        `json:"maxAttempts"`
	AvailableAt     time.Time  `json:"availableAt"`
	LeaseUntil      *time.Time `json:"-"`
	RequestHash     string     `json:"requestHash"`
	ResultHash      string     `json:"resultHash,omitempty"`
	// SourceAttemptReportID is present only for the one-shot selector repair
	// derived from that immutable failed provider attempt.
	SourceAttemptReportID string `json:"sourceAttemptReportId,omitempty"`
	// ProviderDispatched is internal crash-ambiguity state. Once true, an
	// expired selector-repair lease is failed closed and never dispatched again.
	ProviderDispatched bool       `json:"-"`
	SafetyFlags        []string   `json:"safetyFlags"`
	ErrorCode          string     `json:"errorCode,omitempty"`
	ErrorMessage       string     `json:"errorMessage,omitempty"`
	Request            any        `json:"-"`
	Result             any        `json:"-"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	StartedAt          *time.Time `json:"startedAt,omitempty"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
}

// ReplayArtifact is sanitized diagnostic material captured by the browser.
type ReplayArtifact struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Data string `json:"data"`
}

// ReplayAttempt stores one complete replay outcome. Diagnostics, output, and
// artifacts are encrypted at rest and exposed only to authenticated callers.
type ReplayAttempt struct {
	ID               string              `json:"id"`
	WorkspaceID      string              `json:"workspaceId"`
	WorkflowID       string              `json:"workflowId"`
	Sequence         int                 `json:"sequence"`
	Status           ReplayAttemptStatus `json:"status"`
	RuleHash         string              `json:"ruleHash"`
	BrowserProfileID string              `json:"browserProfileId"`
	OutputValid      bool                `json:"outputValid"`
	DiagnosticsHash  string              `json:"diagnosticsHash,omitempty"`
	OutputHash       string              `json:"outputHash,omitempty"`
	ArtifactsHash    string              `json:"artifactsHash,omitempty"`
	ErrorCode        string              `json:"errorCode,omitempty"`
	ErrorMessage     string              `json:"errorMessage,omitempty"`
	Diagnostics      any                 `json:"diagnostics,omitempty" swaggertype:"object"`
	Output           any                 `json:"output,omitempty" swaggertype:"object"`
	Artifacts        []ReplayArtifact    `json:"artifacts,omitempty"`
	StartedAt        time.Time           `json:"startedAt"`
	CompletedAt      *time.Time          `json:"completedAt,omitempty"`
}
