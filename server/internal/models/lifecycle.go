package models

import "time"

type RecordingStatus string

const (
	RecordingStatusCaptured RecordingStatus = "captured"
	RecordingStatusDeleted  RecordingStatus = "deleted"
)

// Recording contains safe metadata for an encrypted recording archive. The
// reconstructed payload is returned separately and is never written to logs.
type Recording struct {
	ID                  string          `json:"id"`
	WorkspaceID         string          `json:"workspaceId"`
	Owner               string          `json:"owner"`
	Status              RecordingStatus `json:"status"`
	ProtocolVersion     string          `json:"protocolVersion"`
	SanitizationVersion string          `json:"sanitizationVersion"`
	RedactionCount      int             `json:"redactionCount"`
	RemovedFieldCount   int             `json:"removedFieldCount"`
	ActionCount         int             `json:"actionCount"`
	SnapshotCount       int             `json:"snapshotCount"`
	CompressedBytes     int64           `json:"compressedBytes"`
	ContentHash         string          `json:"contentHash"`
	StartedAt           time.Time       `json:"startedAt"`
	EndedAt             time.Time       `json:"endedAt"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
	ExpiresAt           time.Time       `json:"expiresAt"`
	DeletedAt           *time.Time      `json:"deletedAt,omitempty"`
	Payload             map[string]any  `json:"payload,omitempty"`
}

// RuleVersion is an immutable full rule snapshot. Approval metadata is the
// only mutable state and can transition from pending exactly once.
type RuleVersion struct {
	WorkspaceID    string             `json:"workspaceId"`
	RuleID         string             `json:"ruleId"`
	Version        int                `json:"version"`
	VersionLabel   string             `json:"versionLabel"`
	Rule           *Rule              `json:"rule"`
	ContentHash    string             `json:"contentHash"`
	Status         RuleApprovalStatus `json:"status"`
	Owner          string             `json:"owner"`
	Source         string             `json:"source"`
	RecordingID    string             `json:"recordingId,omitempty"`
	SafetyFlags    []string           `json:"safetyFlags,omitempty"`
	DSLWorkflowID  string             `json:"dslWorkflowId,omitempty"`
	DSLJobID       string             `json:"dslJobId,omitempty"`
	CreatedAt      time.Time          `json:"createdAt"`
	ApprovedAt     *time.Time         `json:"approvedAt,omitempty"`
	ApprovedBy     string             `json:"approvedBy,omitempty"`
	RejectedAt     *time.Time         `json:"rejectedAt,omitempty"`
	RejectedBy     string             `json:"rejectedBy,omitempty"`
}
