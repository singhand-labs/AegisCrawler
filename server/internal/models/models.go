package models

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// JSON is a nullable json.RawMessage with database driver support.
type JSON json.RawMessage

// Scan implements sql.Scanner.
func (j *JSON) Scan(value any) error {
	if value == nil {
		*j = JSON("{}")
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*j = JSON(v)
	case string:
		*j = JSON(v)
	default:
		return fmt.Errorf("cannot scan %T into JSON", value)
	}
	return nil
}

// Value implements driver.Valuer.
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "{}", nil
	}
	return string(j), nil
}

// MarshalJSON implements json.Marshaler.
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("{}"), nil
	}
	return []byte(j), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (j *JSON) UnmarshalJSON(data []byte) error {
	*j = JSON(data)
	return nil
}

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending      TaskStatus = "pending"
	TaskStatusLeased       TaskStatus = "leased"
	TaskStatusRunning      TaskStatus = "running"
	TaskStatusDone         TaskStatus = "done"
	TaskStatusFailed       TaskStatus = "failed"
	TaskStatusCancelled    TaskStatus = "cancelled"
	TaskStatusWaitingHuman TaskStatus = "waiting_for_human"
	TaskStatusDeadLetter   TaskStatus = "dead_letter"
)

// Priority represents task priority.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// RuleApprovalStatus represents the governance state of a rule.
type RuleApprovalStatus string

const (
	RuleApprovalPending  RuleApprovalStatus = "pending"
	RuleApprovalApproved RuleApprovalStatus = "approved"
	RuleApprovalRejected RuleApprovalStatus = "rejected"
)

// EnhancementStatus represents the governance state of an AI rule enhancement.
type EnhancementStatus string

const (
	EnhancementStatusPending  EnhancementStatus = "pending"
	EnhancementStatusApproved EnhancementStatus = "approved"
	EnhancementStatusRejected EnhancementStatus = "rejected"
)

// RuleEnhancement stores an LLM-generated enhancement suggestion for a rule.
type RuleEnhancement struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspaceId"`
	RuleID       string    `json:"ruleId"`
	Baseline     JSON      `json:"baseline" swaggertype:"object"`
	Enhanced     JSON      `json:"enhanced" swaggertype:"object"`
	Patch        JSON      `json:"patch" swaggertype:"object"`
	UserHint     string    `json:"userHint"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"inputTokens"`
	OutputTokens int       `json:"outputTokens"`
	Suggestions  JSON      `json:"suggestions" swaggertype:"array,string"`
	SafetyFlags  JSON      `json:"safetyFlags" swaggertype:"array,object"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Task is the unit of work assigned to a worker.
type Task struct {
	ID                string         `json:"id"`
	WorkspaceID       string         `json:"workspaceId"`
	RuleID            string         `json:"ruleId"`
	RuleVersion       string         `json:"ruleVersion"`
	RuleVersionNumber int            `json:"ruleVersionNumber"`
	Status            TaskStatus     `json:"status"`
	Priority          Priority       `json:"priority"`
	Variables         JSON           `json:"variables" swaggertype:"object"`
	InputSchema       JSON           `json:"inputSchema" swaggertype:"object"`
	WorkerID          sql.NullString `json:"workerId" swaggertype:"string"`
	LeaseUntil        sql.NullTime   `json:"leaseUntil" swaggertype:"string"`
	RetryCount        int            `json:"retryCount"`
	MaxRetries        int            `json:"maxRetries"`
	OutputSchema      JSON           `json:"outputSchema" swaggertype:"object"`
	SendPolicy        JSON           `json:"sendPolicy" swaggertype:"object"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	ScheduledAt       sql.NullTime   `json:"scheduledAt" swaggertype:"string"`
	CompletedAt       sql.NullTime   `json:"completedAt" swaggertype:"string"`
	ErrorType         sql.NullString `json:"errorType" swaggertype:"string"`
	ErrorMessage      sql.NullString `json:"errorMessage" swaggertype:"string"`
	ScheduleID        sql.NullString `json:"scheduleId" swaggertype:"string"`
	CurrentAttemptID  sql.NullString `json:"currentAttemptId" swaggertype:"string"`
	BrowserProfileID  string         `json:"browserProfileId"`
	CancelRequested   bool           `json:"cancelRequested"`
}

// ScheduleType represents the execution type of a schedule.
type ScheduleType string

const (
	ScheduleTypeOnce ScheduleType = "once"
	ScheduleTypeCron ScheduleType = "cron"
)

// CatchupMode represents how a cron schedule should handle missed runs.
type CatchupMode string

const (
	CatchupSkip    CatchupMode = "skip"
	CatchupRunOnce CatchupMode = "run_once"
)

// Schedule defines a recurring or one-time execution plan for a rule.
type Schedule struct {
	ID                string       `json:"id"`
	WorkspaceID       string       `json:"workspaceId"`
	RuleID            string       `json:"ruleId"`
	RuleVersion       string       `json:"ruleVersion"`
	RuleVersionNumber int          `json:"ruleVersionNumber"`
	Name              string       `json:"name"`
	Type              ScheduleType `json:"type"`
	Expression        string       `json:"expression"`
	Enabled           bool         `json:"enabled"`
	NextRunAt         sql.NullTime `json:"nextRunAt" swaggertype:"string"`
	LastRunAt         sql.NullTime `json:"lastRunAt" swaggertype:"string"`
	Variables         JSON         `json:"variables" swaggertype:"object"`
	InputSchema       JSON         `json:"inputSchema" swaggertype:"object"`
	BrowserProfileID  string       `json:"browserProfileId"`
	Timezone          string       `json:"timezone"`
	Priority          Priority     `json:"priority"`
	MaxRetries        int          `json:"maxRetries"`
	Catchup           CatchupMode  `json:"catchup"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
}

// Rule represents a collection configuration.
type Rule struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspaceId"`
	Version        string    `json:"version"`
	Name           string    `json:"name"`
	Domain         JSON      `json:"domain" swaggertype:"object"`
	URLPattern     JSON      `json:"urlPattern" swaggertype:"object"`
	Enabled        bool      `json:"enabled"`
	Priority       Priority  `json:"priority"`
	Entry          string    `json:"entry"`
	Variables      JSON      `json:"variables" swaggertype:"object"`
	Selectors      JSON      `json:"selectors" swaggertype:"object"`
	Humanize       JSON      `json:"humanize" swaggertype:"object"`
	Steps          JSON      `json:"steps" swaggertype:"object"`
	Output         JSON      `json:"output" swaggertype:"object"`
	SendPolicy     JSON      `json:"sendPolicy" swaggertype:"object"`
	Hooks          JSON      `json:"hooks" swaggertype:"object"`
	Tags           JSON      `json:"tags" swaggertype:"object"`
	Owner          string    `json:"owner"`
	ApprovalStatus string    `json:"approvalStatus" swaggertype:"string"`
	Source         string    `json:"source" gorm:"default:pageagent"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Result is a data payload sent by a worker.
type Result struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspaceId"`
	TaskID          string     `json:"taskId"`
	WorkerID        string     `json:"workerId"`
	AttemptID       string     `json:"attemptId"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	Sequence        int        `json:"sequence"`
	Kind            ResultKind `json:"kind"`
	Payload         JSON       `json:"payload" swaggertype:"object"`
	PayloadHash     string     `json:"payloadHash"`
	Valid           bool       `json:"valid"`
	ValidationError string     `json:"validationError,omitempty"`
	Immediate       bool       `json:"immediate"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// LogEntry is a structured log from a worker.
type LogEntry struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	TaskID      string    `json:"taskId"`
	WorkerID    string    `json:"workerId"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Extra       JSON      `json:"extra" swaggertype:"object"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Snapshot stores debugging artifacts.
type Snapshot struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	TaskID      string    `json:"taskId"`
	WorkerID    string    `json:"workerId"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Data        string    `json:"data"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Heartbeat is a worker lease renewal signal.
type Heartbeat struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	TaskID      string    `json:"taskId"`
	WorkerID    string    `json:"workerId"`
	Payload     JSON      `json:"payload" swaggertype:"object"`
	CreatedAt   time.Time `json:"createdAt"`
}

// TaskStatusUpdate is a status change sent by a worker.
type TaskStatusUpdate struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	TaskID      string    `json:"taskId"`
	WorkerID    string    `json:"workerID"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Checkpoint stores a recoverable execution state.
type Checkpoint struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	TaskID      string    `json:"taskId"`
	WorkerID    string    `json:"workerId"`
	Name        string    `json:"name"`
	Payload     JSON      `json:"payload" swaggertype:"object"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AuditLog records a privileged admin action for security and compliance.
type AuditLog struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspaceId"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Payload      JSON      `json:"payload" swaggertype:"object"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Workspace is the top-level tenant boundary. Release one creates and uses a
// single default workspace while every resource remains explicitly scoped.
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
