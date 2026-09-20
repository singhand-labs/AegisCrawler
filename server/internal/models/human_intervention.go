package models

import "time"

type HumanInterventionStatus string

const (
	HumanInterventionPending   HumanInterventionStatus = "pending"
	HumanInterventionApproved  HumanInterventionStatus = "approved"
	HumanInterventionRejected  HumanInterventionStatus = "rejected"
	HumanInterventionExpired   HumanInterventionStatus = "expired"
	HumanInterventionCancelled HumanInterventionStatus = "cancelled"
)

// HumanIntervention is a durable operator decision bound to one execution
// attempt and one server-created checkpoint. It stores routing and audit
// metadata only; browser credentials and runtime variables are never included.
type HumanIntervention struct {
	ID                string                  `json:"id"`
	WorkspaceID       string                  `json:"workspaceId"`
	TaskID            string                  `json:"taskId"`
	AttemptID         string                  `json:"attemptId"`
	WorkerID          string                  `json:"workerId"`
	CheckpointID      string                  `json:"checkpointId"`
	Checkpoint        JSON                    `json:"checkpoint" swaggertype:"object"`
	Type              string                  `json:"type"`
	Prompt            string                  `json:"prompt"`
	RequestedAction   string                  `json:"requestedAction"`
	TargetOrigin      string                  `json:"targetOrigin"`
	Status            HumanInterventionStatus `json:"status"`
	ExpiresAt         time.Time               `json:"expiresAt"`
	CreatedAt         time.Time               `json:"createdAt"`
	DecidedAt         *time.Time              `json:"decidedAt,omitempty"`
	DecidedBy         string                  `json:"decidedBy,omitempty"`
	DecisionNote      string                  `json:"decisionNote,omitempty"`
	BrowserProfileID  string                  `json:"browserProfileId"`
	RuleID            string                  `json:"ruleId"`
	RuleVersionNumber int                     `json:"ruleVersionNumber"`
}
