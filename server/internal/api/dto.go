package api

import (
	"time"

	llmdsl "github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

// ClaimTaskRequest is the body for claiming a task.
type ClaimTaskRequest struct {
	WorkerID         string `json:"workerId" example:"worker-1"`
	BrowserProfileID string `json:"browserProfileId,omitempty" example:"production-account"`
}

// ClaimTaskResponse returns the claimed task.
type ClaimTaskResponse struct {
	TaskID             string       `json:"taskId" example:"task-uuid"`
	AttemptID          string       `json:"attemptId" example:"attempt-uuid"`
	WorkspaceID        string       `json:"workspaceId" example:"default"`
	RuleID             string       `json:"ruleId" example:"ecommerce-product-list"`
	RuleVersion        string       `json:"ruleVersion" example:"1.0.0"`
	RuleVersionNumber  int          `json:"ruleVersionNumber" example:"1"`
	Rule               *models.Rule `json:"rule,omitempty"`
	BrowserProfileID   string       `json:"browserProfileId,omitempty"`
	SourceKind         string       `json:"sourceKind,omitempty"`
	SourceAuthority    string       `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string       `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string       `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string       `json:"sourceWorkflowId,omitempty"`
	Variables          jsonRaw      `json:"variables"`
	LeaseUntil         time.Time    `json:"leaseUntil"`
}

// HeartbeatRequest renews a task lease.
type HeartbeatRequest struct {
	TaskID   string  `json:"taskId" example:"task-uuid"`
	WorkerID string  `json:"workerId" example:"worker-1"`
	Payload  jsonRaw `json:"payload"`
}

// HeartbeatResponse returns the result of a heartbeat, including any cancel signal.
type HeartbeatResponse struct {
	Success         bool `json:"success" example:"true"`
	CancelRequested bool `json:"cancelRequested" example:"false"`
}

// ResultRequest stores a result payload.
type ResultRequest struct {
	TaskID         string `json:"taskId" example:"task-uuid"`
	WorkerID       string `json:"workerId" example:"worker-1"`
	AttemptID      string `json:"attemptId" example:"attempt-uuid"`
	IdempotencyKey string `json:"idempotencyKey" example:"attempt-uuid:1:batch"`
	Sequence       int    `json:"sequence" example:"1"`
	Kind           string `json:"kind" example:"batch"`
	Immediate      bool   `json:"immediate" example:"true"`
	Payload        any    `json:"payload" swaggertype:"object"`
}

type ResultSubmissionResponse struct {
	Success   bool   `json:"success"`
	Valid     bool   `json:"valid"`
	Duplicate bool   `json:"duplicate"`
	Error     string `json:"error,omitempty"`
}

// LogRequest stores a log entry.
type LogRequest struct {
	TaskID   string  `json:"taskId" example:"task-uuid"`
	WorkerID string  `json:"workerId" example:"worker-1"`
	Level    string  `json:"level" example:"info"`
	Message  string  `json:"message" example:"page loaded"`
	Extra    jsonRaw `json:"extra"`
}

// StatusRequest updates task status.
type StatusRequest struct {
	TaskID    string `json:"taskId" example:"task-uuid"`
	WorkerID  string `json:"workerId" example:"worker-1"`
	AttemptID string `json:"attemptId,omitempty" example:"attempt-uuid"`
	Status    string `json:"status" example:"running"`
	Message   string `json:"message" example:"started extraction"`
}

// SnapshotRequest stores a snapshot.
type SnapshotRequest struct {
	TaskID   string `json:"taskId" example:"task-uuid"`
	WorkerID string `json:"workerId" example:"worker-1"`
	Name     string `json:"name" example:"error-screenshot"`
	Type     string `json:"type" example:"html"`
	Data     string `json:"data"`
}

// CheckpointRequest stores a checkpoint.
type CheckpointRequest struct {
	TaskID   string  `json:"taskId" example:"task-uuid"`
	WorkerID string  `json:"workerId" example:"worker-1"`
	Name     string  `json:"name" example:"after-login"`
	Payload  jsonRaw `json:"payload"`
}

// CheckpointResponse returns a checkpoint.
type CheckpointResponse struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskId"`
	WorkerID  string    `json:"workerId"`
	Name      string    `json:"name"`
	Payload   jsonRaw   `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

// ErrorResponse is the standard error response.
type ErrorResponse struct {
	Error   string `json:"error" example:"task not found"`
	Code    string `json:"code" example:"NOT_FOUND"`
	Details string `json:"details,omitempty"`
}

// SuccessResponse is a generic success response.
type SuccessResponse struct {
	Success bool `json:"success" example:"true"`
}

// CapabilitiesResponse lets independently deployed clients select only
// protocols the server has explicitly enabled.
type CapabilitiesResponse struct {
	APIVersion             string               `json:"apiVersion"`
	WorkspaceMode          string               `json:"workspaceMode"`
	DefaultWorkspaceID     string               `json:"defaultWorkspaceId"`
	WorkerProtocolVersions []string             `json:"workerProtocolVersions"`
	Features               CapabilityFeatureSet `json:"features"`
	RecordingLimits        RecordingLimits      `json:"recordingLimits"`
}

type RecordingLimits struct {
	MaxActions         int   `json:"maxActions"`
	MaxDurationMs      int64 `json:"maxDurationMs"`
	MaxCompressedBytes int64 `json:"maxCompressedBytes"`
}

type CapabilityFeatureSet struct {
	RecordingV2 bool `json:"recordingV2"`
	WorkflowV2  bool `json:"workflowV2"`
	PageMarks   bool `json:"pageMarks"`
	MCP         bool `json:"mcp"`
}

type CreateRecordingRequest struct {
	Recording map[string]any `json:"recording"`
	StartedAt *time.Time     `json:"startedAt,omitempty"`
	EndedAt   *time.Time     `json:"endedAt,omitempty"`
}

type RecordingResponse struct {
	Recording *models.Recording `json:"recording"`
}

type CreateRequirementCandidatesRequest struct {
	MarksOverride *[]models.PageMark `json:"marksOverride,omitempty"`
}

type NormalizeRequirementRequest struct {
	Requirement    *models.CollectionRequirementSpec `json:"requirement,omitempty"`
	CustomText     string                            `json:"customText,omitempty"`
	CandidateJobID string                            `json:"candidateJobId,omitempty"`
	CandidateID    string                            `json:"candidateId,omitempty"`
	MarksOverride  *[]models.PageMark                `json:"marksOverride,omitempty"`
}

type RequirementJobResponse struct {
	Job       *models.RequirementJob `json:"job"`
	StatusURL string                 `json:"statusUrl"`
}

type RequirementProviderAttemptListResponse struct {
	Attempts []*models.LLMAttemptReport `json:"attempts"`
}

type RequirementProviderAttemptResponse struct {
	Attempt  *models.LLMAttemptReport  `json:"attempt"`
	Calls    []*models.LLMProviderCall `json:"calls"`
	Artifact any                       `json:"artifact" swaggertype:"object"`
}

type RequirementProviderCallContentResponse struct {
	Call    *models.LLMProviderCall `json:"call"`
	Content any                     `json:"content" swaggertype:"object"`
}

type CollectionRequirementResponse struct {
	Requirement *models.CollectionRequirement `json:"requirement"`
}

type CreateDSLWorkflowRequest struct {
	BrowserProfileID string       `json:"browserProfileId"`
	BaselineRule     *models.Rule `json:"baselineRule" swaggertype:"object"`
}

type AdoptDSLAttemptExportRequest struct {
	BrowserProfileID string                               `json:"browserProfileId"`
	Export           llmdsl.AdminReviewedDSLAttemptExport `json:"export" swaggertype:"object"`
}

type CorrectDSLWorkflowRequest struct {
	Rule *models.Rule `json:"rule" swaggertype:"object"`
}

type DSLWorkflowResponse struct {
	Workflow  *models.DSLWorkflow `json:"workflow"`
	Job       *models.DSLJob      `json:"job,omitempty"`
	StatusURL string              `json:"statusUrl,omitempty"`
}

type DSLJobResponse struct {
	Job       *models.DSLJob `json:"job"`
	StatusURL string         `json:"statusUrl"`
}

type DSLProviderAttemptListResponse struct {
	Attempts []*models.LLMAttemptReport `json:"attempts"`
}

type DSLProviderAttemptResponse struct {
	Attempt  *models.LLMAttemptReport  `json:"attempt"`
	Calls    []*models.LLMProviderCall `json:"calls"`
	Artifact any                       `json:"artifact" swaggertype:"object"`
}

type DSLProviderCallContentResponse struct {
	Call    *models.LLMProviderCall `json:"call"`
	Content any                     `json:"content" swaggertype:"object"`
}

type DSLReplayResponse struct {
	Replay    *models.ReplayAttempt `json:"replay"`
	RepairJob *models.DSLJob        `json:"repairJob,omitempty"`
}

type CreateRuleVersionRequest struct {
	Rule        models.Rule `json:"rule"`
	RecordingID string      `json:"recordingId,omitempty"`
}

type RuleVersionResponse struct {
	RuleVersion *models.RuleVersion         `json:"ruleVersion"`
	Contract    *models.RuleVersionContract `json:"contract,omitempty"`
	Provenance  *ProvenanceBlock            `json:"provenance,omitempty"`
}

// ProvenanceBlock surfaces the LLM provider-call lineage that produced the
// sealed rule version. It is only populated when the version originated from a
// DSL workflow (DSLWorkflowID != "").
type ProvenanceBlock struct {
	DSLWorkflowID  string   `json:"dslWorkflowId,omitempty"`
	DSLJobID       string   `json:"dslJobId,omitempty"`
	ProviderCallID string   `json:"providerCallId,omitempty"`
	PromptHash     string   `json:"promptHash,omitempty"`
	ModelID        string   `json:"modelId,omitempty"`
	CacheHit       bool     `json:"cacheHit,omitempty"`
	SafetyFlags    []string `json:"safetyFlags,omitempty"`
}

type ListRuleVersionsResponse struct {
	RuleVersions []*models.RuleVersion         `json:"ruleVersions"`
	Contracts    []*models.RuleVersionContract `json:"contracts"`
}

// ListRulesResponse is the response for listing rules.
type ListRulesResponse struct {
	Rules []*RuleResponse `json:"rules"`
	Total int             `json:"total"`
}

// ListTasksResponse is the response for listing tasks.
type ListTasksResponse struct {
	Tasks []*TaskResponse `json:"tasks"`
	Total int             `json:"total"`
}

// ListAuditLogsResponse is the response for listing audit logs.
type ListAuditLogsResponse struct {
	Logs  []*models.AuditLog `json:"logs"`
	Total int                `json:"total"`
}

type CreateMCPTokenRequest struct {
	Name        string     `json:"name"`
	Permissions []string   `json:"permissions"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

type MCPTokenResponse struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"tokenPrefix"`
	Permissions []string   `json:"permissions"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	Token       string     `json:"token,omitempty"`
}

type ListMCPTokensResponse struct {
	Tokens []*MCPTokenResponse `json:"tokens"`
}

// UpdateRuleRequest is a partial update payload. Only fields that are sent are changed.
// Pointer fields distinguish "not provided" (nil) from "provided as empty/zero".
// JSON fields use *any when they may hold non-objects (domain, urlPattern, steps)
// and *jsonRaw for object-only fields so swaggo can render them correctly.
type UpdateRuleRequest struct {
	Version        *string  `json:"version,omitempty"`
	Name           *string  `json:"name,omitempty"`
	Domain         *any     `json:"domain,omitempty" swaggertype:"object"`
	URLPattern     *any     `json:"urlPattern,omitempty" swaggertype:"object"`
	Enabled        *bool    `json:"enabled,omitempty"`
	Priority       *string  `json:"priority,omitempty"`
	Entry          *string  `json:"entry,omitempty"`
	Variables      *jsonRaw `json:"variables,omitempty"`
	Selectors      *jsonRaw `json:"selectors,omitempty"`
	Humanize       *jsonRaw `json:"humanize,omitempty"`
	Steps          *any     `json:"steps,omitempty" swaggertype:"object"`
	Output         *jsonRaw `json:"output,omitempty"`
	SendPolicy     *jsonRaw `json:"sendPolicy,omitempty"`
	Hooks          *jsonRaw `json:"hooks,omitempty"`
	Tags           *jsonRaw `json:"tags,omitempty"`
	Owner          *string  `json:"owner,omitempty"`
	ApprovalStatus *string  `json:"approvalStatus,omitempty"`
	Source         *string  `json:"source,omitempty"`
}

// RuleResponse is the API representation of a rule.
type RuleResponse struct {
	ID             string    `json:"id" example:"ecommerce-product-list"`
	WorkspaceID    string    `json:"workspaceId" example:"default"`
	Version        string    `json:"version" example:"1.0.0"`
	Name           string    `json:"name" example:"电商商品列表采集"`
	Domain         any       `json:"domain" swaggertype:"object"`
	URLPattern     any       `json:"urlPattern" swaggertype:"object"`
	Enabled        bool      `json:"enabled"`
	Priority       string    `json:"priority" example:"normal"`
	Entry          string    `json:"entry"`
	Variables      jsonRaw   `json:"variables"`
	Selectors      jsonRaw   `json:"selectors"`
	Humanize       jsonRaw   `json:"humanize"`
	Steps          any       `json:"steps" swaggertype:"object"`
	Output         jsonRaw   `json:"output"`
	SendPolicy     jsonRaw   `json:"sendPolicy"`
	Hooks          jsonRaw   `json:"hooks"`
	Tags           jsonRaw   `json:"tags"`
	Owner          string    `json:"owner"`
	ApprovalStatus string    `json:"approvalStatus"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// TaskResponse is the API representation of a task.
type TaskResponse struct {
	ID                 string     `json:"id"`
	WorkspaceID        string     `json:"workspaceId"`
	RuleID             string     `json:"ruleId"`
	RuleVersion        string     `json:"ruleVersion"`
	RuleVersionNumber  int        `json:"ruleVersionNumber"`
	Status             string     `json:"status"`
	Priority           string     `json:"priority"`
	Variables          jsonRaw    `json:"variables"`
	InputSchema        jsonRaw    `json:"inputSchema"`
	WorkerID           string     `json:"workerId"`
	LeaseUntil         *time.Time `json:"leaseUntil"`
	RetryCount         int        `json:"retryCount"`
	MaxRetries         int        `json:"maxRetries"`
	OutputSchema       jsonRaw    `json:"outputSchema"`
	SendPolicy         jsonRaw    `json:"sendPolicy"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	ScheduledAt        *time.Time `json:"scheduledAt"`
	CompletedAt        *time.Time `json:"completedAt"`
	ErrorType          string     `json:"errorType"`
	ErrorMessage       string     `json:"errorMessage"`
	ScheduleID         string     `json:"scheduleId"`
	CurrentAttemptID   string     `json:"currentAttemptId"`
	BrowserProfileID   string     `json:"browserProfileId"`
	SourceKind         string     `json:"sourceKind,omitempty"`
	SourceAuthority    string     `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string     `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string     `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string     `json:"sourceWorkflowId,omitempty"`
	CancelRequested    bool       `json:"cancelRequested"`
}

type CreateTaskRequest struct {
	RuleID            string     `json:"ruleId"`
	RuleVersion       string     `json:"ruleVersion,omitempty"`
	RuleVersionNumber int        `json:"ruleVersionNumber,omitempty"`
	Variables         *jsonRaw   `json:"variables,omitempty"`
	Priority          *string    `json:"priority,omitempty"`
	MaxRetries        *int       `json:"maxRetries,omitempty"`
	ScheduledAt       *time.Time `json:"scheduledAt,omitempty"`
	BrowserProfileID  string     `json:"browserProfileId,omitempty"`
}

type TaskResultsResponse struct {
	TaskID             string             `json:"taskId"`
	RuleID             string             `json:"ruleId"`
	RuleVersion        string             `json:"ruleVersion"`
	RuleVersionNumber  int                `json:"ruleVersionNumber"`
	OutputSchema       jsonRaw            `json:"outputSchema"`
	SourceKind         string             `json:"sourceKind,omitempty"`
	SourceAuthority    string             `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string             `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string             `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string             `json:"sourceWorkflowId,omitempty"`
	Page               *models.ResultPage `json:"page"`
}

// CreateScheduleRequest creates a new schedule.
type CreateScheduleRequest struct {
	RuleID            string   `json:"ruleId"`
	RuleVersion       string   `json:"ruleVersion"`
	RuleVersionNumber int      `json:"ruleVersionNumber,omitempty"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Expression        string   `json:"expression"`
	Enabled           *bool    `json:"enabled,omitempty"`
	Variables         *jsonRaw `json:"variables,omitempty"`
	Priority          *string  `json:"priority,omitempty"`
	MaxRetries        *int     `json:"maxRetries,omitempty"`
	Catchup           *string  `json:"catchup,omitempty"`
	BrowserProfileID  string   `json:"browserProfileId,omitempty"`
	Timezone          string   `json:"timezone,omitempty"`
}

// UpdateScheduleRequest partially updates a schedule.
type UpdateScheduleRequest struct {
	RuleVersion       *string  `json:"ruleVersion,omitempty"`
	RuleVersionNumber *int     `json:"ruleVersionNumber,omitempty"`
	Name              *string  `json:"name,omitempty"`
	Expression        *string  `json:"expression,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"`
	Variables         *jsonRaw `json:"variables,omitempty"`
	Priority          *string  `json:"priority,omitempty"`
	MaxRetries        *int     `json:"maxRetries,omitempty"`
	Catchup           *string  `json:"catchup,omitempty"`
	BrowserProfileID  *string  `json:"browserProfileId,omitempty"`
	Timezone          *string  `json:"timezone,omitempty"`
}

// ScheduleResponse is the API representation of a schedule.
type ScheduleResponse struct {
	ID                string     `json:"id"`
	WorkspaceID       string     `json:"workspaceId"`
	RuleID            string     `json:"ruleId"`
	RuleVersion       string     `json:"ruleVersion"`
	RuleVersionNumber int        `json:"ruleVersionNumber"`
	Name              string     `json:"name"`
	Type              string     `json:"type"`
	Expression        string     `json:"expression"`
	Enabled           bool       `json:"enabled"`
	NextRunAt         *time.Time `json:"nextRunAt"`
	LastRunAt         *time.Time `json:"lastRunAt"`
	Variables         jsonRaw    `json:"variables"`
	InputSchema       jsonRaw    `json:"inputSchema"`
	BrowserProfileID  string     `json:"browserProfileId"`
	Timezone          string     `json:"timezone"`
	Priority          string     `json:"priority"`
	MaxRetries        int        `json:"maxRetries"`
	Catchup           string     `json:"catchup"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// ListSchedulesResponse is the response for listing schedules.
type ListSchedulesResponse struct {
	Schedules []*ScheduleResponse `json:"schedules"`
	Total     int                 `json:"total"`
}

// TriggerScheduleResponse returns the ID of the manually created task.
type TriggerScheduleResponse struct {
	TaskID string `json:"taskId"`
}

// PreviewScheduleResponse returns upcoming run times for a cron expression.
type PreviewScheduleResponse struct {
	Expression string   `json:"expression"`
	Timezone   string   `json:"timezone"`
	Runs       []string `json:"runs"`
}

// jsonRaw is a placeholder for swagger documentation.
type jsonRaw = map[string]any

// IntentCandidate is one predicted user intent.
type IntentCandidate struct {
	ID                 string   `json:"id"`
	Label              string   `json:"label"`
	Description        string   `json:"description"`
	Confidence         float64  `json:"confidence"`
	SuggestedVariables []string `json:"suggestedVariables"`
}

// PredictIntentRequest submits a recording for intent prediction.
type PredictIntentRequest struct {
	Recording jsonRaw `json:"recording" swaggertype:"object"`
}

// PredictIntentResponse returns predicted intents.
type PredictIntentResponse struct {
	Candidates     []IntentCandidate `json:"candidates"`
	FallbackIntent IntentCandidate   `json:"fallbackIntent"`
	Model          string            `json:"model"`
	CacheHit       bool              `json:"cacheHit"`
}

// EnhanceRuleRequest submits a recording and its baseline rule for LLM enhancement.
type EnhanceRuleRequest struct {
	Recording    jsonRaw `json:"recording" swaggertype:"object"`
	BaselineRule jsonRaw `json:"baselineRule" swaggertype:"object"`
	UserHint     string  `json:"userHint"`
}

// EnhanceRuleResponse returns the job id and status URL for an asynchronous
// LLM enhancement job.
type EnhanceRuleResponse struct {
	JobID     string `json:"jobId"`
	StatusURL string `json:"statusUrl"`
}

// RuleEnhancementResponse returns an enhancement record.
type RuleEnhancementResponse struct {
	*models.RuleEnhancement
}
