package mcpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	rulecontract "github.com/singhand-labs/AegisCrawler/internal/rule"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type service struct {
	store  *store.Store
	cfg    *config.Config
	logger *zap.Logger
}

func newService(s *store.Store, cfg *config.Config, logger *zap.Logger) *service {
	return &service{store: s, cfg: cfg, logger: logger}
}

func (s *service) registerTools(server *mcp.Server) {
	addTool(server, s, "list_rules", "List approved collection rules and their immutable versions", models.MCPPermissionRead, s.listRules)
	addTool(server, s, "get_rule", "Get one approved immutable rule version and its execution contract", models.MCPPermissionRead, s.getRule)
	addTool(server, s, "create_task", "Create a one-time task bound to an approved immutable rule version", models.MCPPermissionWrite, s.createTask)
	addTool(server, s, "get_task", "Get a workspace-scoped task with secret inputs masked", models.MCPPermissionRead, s.getTask)
	addTool(server, s, "get_task_results", "Get paginated validated task results and the bound output schema", models.MCPPermissionRead, s.getTaskResults)
	addTool(server, s, "cancel_task", "Cooperatively cancel a workspace-scoped task", models.MCPPermissionWrite, s.cancelTask)
	addTool(server, s, "retry_task", "Retry a failed, dead-letter, or cancelled task", models.MCPPermissionWrite, s.retryTask)
	addTool(server, s, "list_schedules", "List workspace-scoped one-time and cron schedules", models.MCPPermissionRead, s.listSchedules)
	addTool(server, s, "create_schedule", "Create a schedule bound to an approved immutable rule version", models.MCPPermissionWrite, s.createSchedule)
	addTool(server, s, "update_schedule", "Update a workspace-scoped schedule", models.MCPPermissionWrite, s.updateSchedule)
	addTool(server, s, "delete_schedule", "Delete a workspace-scoped schedule", models.MCPPermissionWrite, s.deleteSchedule)
}

func addTool[In, Out any](server *mcp.Server, s *service, name, description, permission string,
	handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description},
		func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
			principal, ok := authz.PrincipalFromContext(ctx)
			if !ok || principal.Kind != authz.PrincipalMCP || !principal.HasPermission(permission) {
				var zero Out
				return nil, zero, errors.New("permission denied")
			}
			result, output, err := handler(ctx, request, input)
			if err != nil {
				s.logger.Warn("mcp tool failed", zap.String("tool", name), zap.Error(err))
				err = publicMCPError(err)
			}
			return result, output, err
		})
}

func publicMCPError(err error) error {
	message := err.Error()
	publicFragments := []string{
		"is required", "not found", "cannot be", "must be", "must contain",
		"invalid rule version or inputs", "invalid cron expression",
		"secret-declared input", "permission denied",
	}
	for _, fragment := range publicFragments {
		if strings.Contains(message, fragment) {
			return errors.New(message)
		}
	}
	return errors.New("MCP tool failed")
}

func (s *service) auditMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if method != "tools/call" {
			return result, err
		}
		params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
		if !ok || params.Name == "" {
			return result, err
		}
		outcome := "success"
		if err != nil {
			outcome = "protocol_error"
		} else if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult.IsError {
			outcome = "error"
			if toolError := toolResult.GetError(); toolError != nil && toolError.Error() == "permission denied" {
				outcome = "denied"
			}
		}
		s.audit(ctx, params.Name, outcome)
		return result, err
	}
}

func (s *service) audit(ctx context.Context, tool, outcome string) {
	principal, ok := authz.PrincipalFromContext(ctx)
	if !ok {
		return
	}
	payload, _ := json.Marshal(map[string]any{"tool": tool, "outcome": outcome})
	entry := &models.AuditLog{
		ID: store.NewID(), Actor: principal.Subject, Action: "mcp." + tool,
		ResourceType: "mcp_tool", ResourceID: tool, Payload: models.JSON(payload),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.InsertAuditLog(ctx, entry); err != nil {
		s.logger.Error("insert mcp audit log failed", zap.String("tool", tool), zap.Error(err))
	}
}

type pageInput struct {
	Limit  int `json:"limit,omitempty" jsonschema:"Maximum number of items to return (1-100)"`
	Offset int `json:"offset,omitempty" jsonschema:"Zero-based result offset"`
}

type ruleListItem struct {
	ID                     string    `json:"id"`
	Name                   string    `json:"name"`
	Source                 string    `json:"source"`
	LatestApprovedVersion  int       `json:"latestApprovedVersion"`
	ApprovedVersionNumbers []int     `json:"approvedVersionNumbers"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

type listRulesOutput struct {
	Rules  []ruleListItem `json:"rules"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func (s *service) listRules(ctx context.Context, _ *mcp.CallToolRequest, input pageInput) (*mcp.CallToolResult, listRulesOutput, error) {
	limit, offset, err := normalizePage(input.Limit, input.Offset)
	if err != nil {
		return nil, listRulesOutput{}, err
	}
	rules, total, err := s.store.ListRules(ctx, store.ListRulesFilter{
		ApprovalStatus: string(models.RuleApprovalApproved), Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, listRulesOutput{}, err
	}
	output := listRulesOutput{Rules: []ruleListItem{}, Total: total, Limit: limit, Offset: offset}
	for _, rule := range rules {
		versions, err := s.store.ListRuleVersions(ctx, rule.ID)
		if err != nil {
			return nil, listRulesOutput{}, err
		}
		item := ruleListItem{ID: rule.ID, Name: rule.Name, Source: rule.Source, UpdatedAt: rule.UpdatedAt, ApprovedVersionNumbers: []int{}}
		for _, version := range versions {
			if version.Status != models.RuleApprovalApproved {
				continue
			}
			item.ApprovedVersionNumbers = append(item.ApprovedVersionNumbers, version.Version)
			if version.Version > item.LatestApprovedVersion {
				item.LatestApprovedVersion = version.Version
			}
		}
		if item.LatestApprovedVersion > 0 {
			output.Rules = append(output.Rules, item)
		}
	}
	return nil, output, nil
}

type getRuleInput struct {
	RuleID  string `json:"ruleId" jsonschema:"ID of the rule"`
	Version int    `json:"version,omitempty" jsonschema:"Approved immutable version number; omit for latest"`
}

type ruleVersionOutput struct {
	RuleID             string         `json:"ruleId"`
	Version            int            `json:"version"`
	VersionLabel       string         `json:"versionLabel"`
	ContentHash        string         `json:"contentHash"`
	Definition         map[string]any `json:"definition"`
	InputSchema        map[string]any `json:"inputSchema"`
	OutputSchema       map[string]any `json:"outputSchema"`
	BrowserProfileID   string         `json:"browserProfileId,omitempty"`
	SourceKind         string         `json:"sourceKind,omitempty"`
	SourceAuthority    string         `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string         `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string         `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string         `json:"sourceWorkflowId,omitempty"`
	ApprovedAt         *time.Time     `json:"approvedAt,omitempty"`
	ApprovedBy         string         `json:"approvedBy,omitempty"`
}

func (s *service) getRule(ctx context.Context, _ *mcp.CallToolRequest, input getRuleInput) (*mcp.CallToolResult, ruleVersionOutput, error) {
	if strings.TrimSpace(input.RuleID) == "" {
		return nil, ruleVersionOutput{}, errors.New("ruleId is required")
	}
	version, err := s.approvedRuleVersion(ctx, input.RuleID, input.Version)
	if err != nil {
		return nil, ruleVersionOutput{}, err
	}
	contract, err := s.store.GetRuleVersionContract(ctx, input.RuleID, version.Version)
	if err != nil {
		return nil, ruleVersionOutput{}, err
	}
	definition := map[string]any{}
	raw, _ := json.Marshal(version.Rule)
	_ = json.Unmarshal(raw, &definition)
	definition = sanitizeMap(definition)
	return nil, ruleVersionOutput{
		RuleID: version.RuleID, Version: version.Version, VersionLabel: version.VersionLabel,
		ContentHash: version.ContentHash, Definition: definition,
		InputSchema:      sanitizeSchema(jsonMap(contract.InputSchema)),
		OutputSchema:     sanitizeSchema(jsonMap(contract.OutputSchema)),
		BrowserProfileID: contract.BrowserProfileID, ApprovedAt: version.ApprovedAt,
		ApprovedBy: version.ApprovedBy, SourceKind: contract.SourceKind,
		SourceAuthority: contract.SourceAuthority, SourceArtifactHash: contract.SourceArtifactHash,
		SourceExportHash: contract.SourceExportHash, SourceWorkflowID: contract.SourceWorkflowID,
	}, nil
}

func (s *service) approvedRuleVersion(ctx context.Context, ruleID string, requested int) (*models.RuleVersion, error) {
	if requested > 0 {
		version, err := s.store.GetRuleVersion(ctx, ruleID, requested)
		if err != nil || version.Status != models.RuleApprovalApproved {
			return nil, errors.New("approved rule version not found")
		}
		return version, nil
	}
	versions, err := s.store.ListRuleVersions(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if version.Status == models.RuleApprovalApproved {
			return version, nil
		}
	}
	return nil, errors.New("approved rule version not found")
}

type createTaskInput struct {
	RuleID           string         `json:"ruleId" jsonschema:"ID of the approved rule"`
	RuleVersion      int            `json:"ruleVersion" jsonschema:"Approved immutable rule version number"`
	Inputs           map[string]any `json:"inputs,omitempty" jsonschema:"Values matching the rule input schema; secret fields must contain approved references only"`
	Priority         string         `json:"priority,omitempty" jsonschema:"Task priority: low, normal, or high"`
	MaxRetries       *int           `json:"maxRetries,omitempty" jsonschema:"Maximum execution retries"`
	ScheduledAt      string         `json:"scheduledAt,omitempty" jsonschema:"Optional future RFC3339 time"`
	BrowserProfileID string         `json:"browserProfileId,omitempty" jsonschema:"Named browser profile reference; secret contents are never exposed"`
}

type taskOutput struct {
	ID                 string         `json:"id"`
	RuleID             string         `json:"ruleId"`
	RuleVersion        int            `json:"ruleVersion"`
	RuleVersionLabel   string         `json:"ruleVersionLabel"`
	Status             string         `json:"status"`
	Priority           string         `json:"priority"`
	Inputs             map[string]any `json:"inputs"`
	InputSchema        map[string]any `json:"inputSchema"`
	OutputSchema       map[string]any `json:"outputSchema"`
	BrowserProfileID   string         `json:"browserProfileId,omitempty"`
	SourceKind         string         `json:"sourceKind,omitempty"`
	SourceAuthority    string         `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string         `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string         `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string         `json:"sourceWorkflowId,omitempty"`
	CurrentAttemptID   string         `json:"currentAttemptId,omitempty"`
	RetryCount         int            `json:"retryCount"`
	MaxRetries         int            `json:"maxRetries"`
	CancelRequested    bool           `json:"cancelRequested"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	ScheduledAt        *time.Time     `json:"scheduledAt,omitempty"`
	CompletedAt        *time.Time     `json:"completedAt,omitempty"`
	ErrorType          string         `json:"errorType,omitempty"`
	ErrorMessage       string         `json:"errorMessage,omitempty"`
}

func (s *service) createTask(ctx context.Context, _ *mcp.CallToolRequest, input createTaskInput) (*mcp.CallToolResult, taskOutput, error) {
	if input.RuleID == "" || input.RuleVersion <= 0 {
		return nil, taskOutput{}, errors.New("ruleId and a positive ruleVersion are required")
	}
	priority, err := normalizePriority(input.Priority)
	if err != nil {
		return nil, taskOutput{}, err
	}
	maxRetries := s.cfg.MaxRetries
	if input.MaxRetries != nil {
		if *input.MaxRetries < 0 {
			return nil, taskOutput{}, errors.New("maxRetries cannot be negative")
		}
		maxRetries = *input.MaxRetries
	}
	variables, err := json.Marshal(defaultMap(input.Inputs))
	if err != nil {
		return nil, taskOutput{}, err
	}
	contract, err := s.store.GetRuleVersionContract(ctx, input.RuleID, input.RuleVersion)
	if err != nil {
		return nil, taskOutput{}, executionInputError(err)
	}
	if err := rejectMCPSecretValues(input.Inputs, jsonMap(contract.InputSchema)); err != nil {
		return nil, taskOutput{}, err
	}
	now := time.Now().UTC()
	task := &models.Task{
		ID: store.NewID(), RuleID: input.RuleID, RuleVersionNumber: input.RuleVersion,
		Variables: models.JSON(variables), Status: models.TaskStatusPending,
		Priority: priority, MaxRetries: maxRetries, BrowserProfileID: input.BrowserProfileID,
		CreatedAt: now, UpdatedAt: now,
	}
	if input.ScheduledAt != "" {
		scheduledAt, err := time.Parse(time.RFC3339, input.ScheduledAt)
		if err != nil {
			return nil, taskOutput{}, errors.New("scheduledAt must be RFC3339")
		}
		if scheduledAt.After(now) {
			task.ScheduledAt = sql.NullTime{Time: scheduledAt.UTC(), Valid: true}
		}
	}
	if err := s.store.BindTaskToRuleVersion(ctx, task, input.RuleVersion); err != nil {
		return nil, taskOutput{}, executionInputError(err)
	}
	if err := s.store.CreateTask(ctx, task); err != nil {
		return nil, taskOutput{}, err
	}
	return nil, toTaskOutput(task, contract), nil
}

type getTaskInput struct {
	TaskID string `json:"taskId" jsonschema:"ID of the task"`
}

func (s *service) getTask(ctx context.Context, _ *mcp.CallToolRequest, input getTaskInput) (*mcp.CallToolResult, taskOutput, error) {
	if input.TaskID == "" {
		return nil, taskOutput{}, errors.New("taskId is required")
	}
	task, err := s.store.GetTaskByID(ctx, input.TaskID)
	if err != nil {
		return nil, taskOutput{}, taskError(err)
	}
	_, contract, err := s.store.ResolveTaskRuleVersionContract(ctx, task)
	if err != nil {
		s.logger.Error("resolve mcp task rule version contract failed", zap.Error(err))
		return nil, taskOutput{}, errors.New("task execution contract unavailable")
	}
	return nil, toTaskOutput(task, contract), nil
}

func toTaskOutput(task *models.Task, contract *models.RuleVersionContract) taskOutput {
	inputs := maskSecretInputs(jsonMap(task.Variables), jsonMap(task.InputSchema))
	output := taskOutput{
		ID: task.ID, RuleID: task.RuleID, RuleVersion: task.RuleVersionNumber,
		RuleVersionLabel: task.RuleVersion, Status: string(task.Status),
		Priority: string(task.Priority), Inputs: inputs,
		InputSchema:      sanitizeSchema(jsonMap(task.InputSchema)),
		OutputSchema:     sanitizeSchema(jsonMap(task.OutputSchema)),
		BrowserProfileID: task.BrowserProfileID, CurrentAttemptID: task.CurrentAttemptID.String,
		RetryCount: task.RetryCount, MaxRetries: task.MaxRetries,
		CancelRequested: task.CancelRequested, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		ErrorType: task.ErrorType.String,
	}
	if contract != nil {
		output.SourceKind = contract.SourceKind
		output.SourceAuthority = contract.SourceAuthority
		output.SourceArtifactHash = contract.SourceArtifactHash
		output.SourceExportHash = contract.SourceExportHash
		output.SourceWorkflowID = contract.SourceWorkflowID
	}
	if task.ErrorMessage.String != "" {
		output.ErrorMessage = "task execution failed; detailed diagnostics are available in the Admin UI"
	}
	if task.ScheduledAt.Valid {
		value := task.ScheduledAt.Time
		output.ScheduledAt = &value
	}
	if task.CompletedAt.Valid {
		value := task.CompletedAt.Time
		output.CompletedAt = &value
	}
	return output
}

type getTaskResultsInput struct {
	TaskID         string `json:"taskId" jsonschema:"ID of the task"`
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum validated batches to return (1-100)"`
	Offset         int    `json:"offset,omitempty" jsonschema:"Zero-based validated-batch offset"`
	IncludeInvalid bool   `json:"includeInvalid,omitempty" jsonschema:"Include invalid payload diagnostics"`
}

type resultOutput struct {
	ID              string    `json:"id"`
	AttemptID       string    `json:"attemptId"`
	Sequence        int       `json:"sequence"`
	Kind            string    `json:"kind"`
	Payload         any       `json:"payload"`
	Valid           bool      `json:"valid"`
	ValidationError string    `json:"validationError,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

type taskResultsOutput struct {
	TaskID             string         `json:"taskId"`
	RuleID             string         `json:"ruleId"`
	RuleVersion        int            `json:"ruleVersion"`
	OutputSchema       map[string]any `json:"outputSchema"`
	SourceKind         string         `json:"sourceKind,omitempty"`
	SourceAuthority    string         `json:"sourceAuthority,omitempty"`
	SourceArtifactHash string         `json:"sourceArtifactHash,omitempty"`
	SourceExportHash   string         `json:"sourceExportHash,omitempty"`
	SourceWorkflowID   string         `json:"sourceWorkflowId,omitempty"`
	Batches            []resultOutput `json:"batches"`
	Invalid            []resultOutput `json:"invalid,omitempty"`
	Summary            *resultOutput  `json:"summary,omitempty"`
	Total              int            `json:"total"`
	Limit              int            `json:"limit"`
	Offset             int            `json:"offset"`
}

func (s *service) getTaskResults(ctx context.Context, _ *mcp.CallToolRequest, input getTaskResultsInput) (*mcp.CallToolResult, taskResultsOutput, error) {
	if input.TaskID == "" {
		return nil, taskResultsOutput{}, errors.New("taskId is required")
	}
	limit, offset, err := normalizePage(input.Limit, input.Offset)
	if err != nil {
		return nil, taskResultsOutput{}, err
	}
	task, err := s.store.GetTaskByID(ctx, input.TaskID)
	if err != nil {
		return nil, taskResultsOutput{}, taskError(err)
	}
	page, err := s.store.ListResultPage(ctx, input.TaskID, limit, offset, input.IncludeInvalid)
	if err != nil {
		return nil, taskResultsOutput{}, err
	}
	output := taskResultsOutput{
		TaskID: task.ID, RuleID: task.RuleID, RuleVersion: task.RuleVersionNumber,
		OutputSchema: sanitizeSchema(jsonMap(task.OutputSchema)), Batches: []resultOutput{},
		Total: page.Total, Limit: page.Limit, Offset: page.Offset,
	}
	_, contract, err := s.store.ResolveTaskRuleVersionContract(ctx, task)
	if err != nil {
		s.logger.Error("resolve mcp task results rule version contract failed", zap.Error(err))
		return nil, taskResultsOutput{}, errors.New("task execution contract unavailable")
	}
	if contract != nil {
		output.SourceKind = contract.SourceKind
		output.SourceAuthority = contract.SourceAuthority
		output.SourceArtifactHash = contract.SourceArtifactHash
		output.SourceExportHash = contract.SourceExportHash
		output.SourceWorkflowID = contract.SourceWorkflowID
	}
	for _, result := range page.Batches {
		output.Batches = append(output.Batches, toResultOutput(result))
	}
	for _, result := range page.InvalidBatches {
		output.Invalid = append(output.Invalid, toResultOutput(result))
	}
	if page.Summary != nil {
		summary := toResultOutput(page.Summary)
		output.Summary = &summary
	}
	return nil, output, nil
}

func toResultOutput(result *models.Result) resultOutput {
	var payload any
	_ = json.Unmarshal(result.Payload, &payload)
	payload = sanitizeValue(payload)
	output := resultOutput{ID: result.ID, AttemptID: result.AttemptID, Sequence: result.Sequence,
		Kind: string(result.Kind), Payload: payload, Valid: result.Valid,
		CreatedAt: result.CreatedAt}
	if result.ValidationError != "" {
		output.ValidationError = "payload failed output schema validation"
	}
	return output
}

type mutationOutput struct {
	Success bool   `json:"success"`
	ID      string `json:"id"`
}

func (s *service) cancelTask(ctx context.Context, _ *mcp.CallToolRequest, input getTaskInput) (*mcp.CallToolResult, mutationOutput, error) {
	if input.TaskID == "" {
		return nil, mutationOutput{}, errors.New("taskId is required")
	}
	if err := s.store.CancelTask(ctx, input.TaskID); err != nil {
		return nil, mutationOutput{}, taskError(err)
	}
	return nil, mutationOutput{Success: true, ID: input.TaskID}, nil
}

func (s *service) retryTask(ctx context.Context, _ *mcp.CallToolRequest, input getTaskInput) (*mcp.CallToolResult, mutationOutput, error) {
	if input.TaskID == "" {
		return nil, mutationOutput{}, errors.New("taskId is required")
	}
	if err := s.store.RetryTask(ctx, input.TaskID); err != nil {
		return nil, mutationOutput{}, taskError(err)
	}
	return nil, mutationOutput{Success: true, ID: input.TaskID}, nil
}

type listSchedulesInput struct {
	RuleID  string `json:"ruleId,omitempty"`
	Type    string `json:"type,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
}

type scheduleOutput struct {
	ID               string         `json:"id"`
	RuleID           string         `json:"ruleId"`
	RuleVersion      int            `json:"ruleVersion"`
	RuleVersionLabel string         `json:"ruleVersionLabel"`
	Name             string         `json:"name"`
	Type             string         `json:"type"`
	Expression       string         `json:"expression"`
	Enabled          bool           `json:"enabled"`
	NextRunAt        *time.Time     `json:"nextRunAt,omitempty"`
	LastRunAt        *time.Time     `json:"lastRunAt,omitempty"`
	Inputs           map[string]any `json:"inputs"`
	InputSchema      map[string]any `json:"inputSchema"`
	BrowserProfileID string         `json:"browserProfileId,omitempty"`
	Timezone         string         `json:"timezone"`
	Priority         string         `json:"priority"`
	MaxRetries       int            `json:"maxRetries"`
	MissedRunPolicy  string         `json:"missedRunPolicy"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

type listSchedulesOutput struct {
	Schedules []scheduleOutput `json:"schedules"`
	Total     int              `json:"total"`
	Limit     int              `json:"limit"`
	Offset    int              `json:"offset"`
}

func (s *service) listSchedules(ctx context.Context, _ *mcp.CallToolRequest, input listSchedulesInput) (*mcp.CallToolResult, listSchedulesOutput, error) {
	limit, offset, err := normalizePage(input.Limit, input.Offset)
	if err != nil {
		return nil, listSchedulesOutput{}, err
	}
	if input.Type != "" && input.Type != string(models.ScheduleTypeOnce) && input.Type != string(models.ScheduleTypeCron) {
		return nil, listSchedulesOutput{}, errors.New("type must be once or cron")
	}
	schedules, total, err := s.store.ListSchedules(ctx, store.ListSchedulesFilter{
		RuleID: input.RuleID, Type: input.Type, Enabled: input.Enabled, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, listSchedulesOutput{}, err
	}
	output := listSchedulesOutput{Schedules: []scheduleOutput{}, Total: total, Limit: limit, Offset: offset}
	for _, schedule := range schedules {
		output.Schedules = append(output.Schedules, toScheduleOutput(schedule))
	}
	return nil, output, nil
}

type createScheduleInput struct {
	RuleID           string         `json:"ruleId"`
	RuleVersion      int            `json:"ruleVersion"`
	Name             string         `json:"name"`
	Type             string         `json:"type" jsonschema:"once or cron"`
	Expression       string         `json:"expression" jsonschema:"RFC3339 timestamp for once, or five-field cron expression"`
	Enabled          *bool          `json:"enabled,omitempty"`
	Inputs           map[string]any `json:"inputs,omitempty"`
	Priority         string         `json:"priority,omitempty"`
	MaxRetries       *int           `json:"maxRetries,omitempty"`
	MissedRunPolicy  string         `json:"missedRunPolicy,omitempty" jsonschema:"skip or run_once"`
	BrowserProfileID string         `json:"browserProfileId,omitempty"`
	Timezone         string         `json:"timezone,omitempty" jsonschema:"IANA timezone, default UTC"`
}

func (s *service) createSchedule(ctx context.Context, _ *mcp.CallToolRequest, input createScheduleInput) (*mcp.CallToolResult, scheduleOutput, error) {
	if input.RuleID == "" || input.RuleVersion <= 0 || strings.TrimSpace(input.Name) == "" || input.Expression == "" {
		return nil, scheduleOutput{}, errors.New("ruleId, positive ruleVersion, name, and expression are required")
	}
	if input.Type != string(models.ScheduleTypeOnce) && input.Type != string(models.ScheduleTypeCron) {
		return nil, scheduleOutput{}, errors.New("type must be once or cron")
	}
	priority, err := normalizePriority(input.Priority)
	if err != nil {
		return nil, scheduleOutput{}, err
	}
	maxRetries := s.cfg.MaxRetries
	if input.MaxRetries != nil {
		if *input.MaxRetries < 0 {
			return nil, scheduleOutput{}, errors.New("maxRetries cannot be negative")
		}
		maxRetries = *input.MaxRetries
	}
	catchup, err := normalizeCatchup(input.MissedRunPolicy)
	if err != nil {
		return nil, scheduleOutput{}, err
	}
	timezone, nextRun, err := scheduleNextRun(models.ScheduleType(input.Type), input.Expression, input.Timezone, time.Now().UTC())
	if err != nil {
		return nil, scheduleOutput{}, err
	}
	variables, _ := json.Marshal(defaultMap(input.Inputs))
	contract, err := s.store.GetRuleVersionContract(ctx, input.RuleID, input.RuleVersion)
	if err != nil {
		return nil, scheduleOutput{}, executionInputError(err)
	}
	if err := rejectMCPSecretValues(input.Inputs, jsonMap(contract.InputSchema)); err != nil {
		return nil, scheduleOutput{}, err
	}
	binding := &models.Task{RuleID: input.RuleID, RuleVersionNumber: input.RuleVersion,
		Variables: models.JSON(variables), BrowserProfileID: input.BrowserProfileID}
	if err := s.store.BindTaskToRuleVersion(ctx, binding, input.RuleVersion); err != nil {
		return nil, scheduleOutput{}, executionInputError(err)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	schedule := &models.Schedule{
		ID: store.NewID(), RuleID: input.RuleID, RuleVersion: binding.RuleVersion,
		RuleVersionNumber: binding.RuleVersionNumber, Name: strings.TrimSpace(input.Name),
		Type: models.ScheduleType(input.Type), Expression: input.Expression, Enabled: enabled,
		NextRunAt: nextRun, Variables: binding.Variables, InputSchema: binding.InputSchema,
		BrowserProfileID: binding.BrowserProfileID, Timezone: timezone, Priority: priority,
		MaxRetries: maxRetries, Catchup: catchup, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateSchedule(ctx, schedule); err != nil {
		return nil, scheduleOutput{}, err
	}
	return nil, toScheduleOutput(schedule), nil
}

type updateScheduleInput struct {
	ScheduleID       string          `json:"scheduleId"`
	RuleVersion      *int            `json:"ruleVersion,omitempty"`
	Name             *string         `json:"name,omitempty"`
	Expression       *string         `json:"expression,omitempty"`
	Enabled          *bool           `json:"enabled,omitempty"`
	Inputs           *map[string]any `json:"inputs,omitempty"`
	Priority         *string         `json:"priority,omitempty"`
	MaxRetries       *int            `json:"maxRetries,omitempty"`
	MissedRunPolicy  *string         `json:"missedRunPolicy,omitempty"`
	BrowserProfileID *string         `json:"browserProfileId,omitempty"`
	Timezone         *string         `json:"timezone,omitempty"`
}

func (s *service) updateSchedule(ctx context.Context, _ *mcp.CallToolRequest, input updateScheduleInput) (*mcp.CallToolResult, scheduleOutput, error) {
	if input.ScheduleID == "" {
		return nil, scheduleOutput{}, errors.New("scheduleId is required")
	}
	schedule, err := s.store.GetScheduleByID(ctx, input.ScheduleID)
	if err != nil {
		return nil, scheduleOutput{}, scheduleError(err)
	}
	if input.Name != nil {
		if strings.TrimSpace(*input.Name) == "" {
			return nil, scheduleOutput{}, errors.New("name cannot be empty")
		}
		schedule.Name = strings.TrimSpace(*input.Name)
	}
	if input.Enabled != nil {
		schedule.Enabled = *input.Enabled
	}
	if input.Priority != nil {
		priority, err := normalizePriority(*input.Priority)
		if err != nil {
			return nil, scheduleOutput{}, err
		}
		schedule.Priority = priority
	}
	if input.MaxRetries != nil {
		if *input.MaxRetries < 0 {
			return nil, scheduleOutput{}, errors.New("maxRetries cannot be negative")
		}
		schedule.MaxRetries = *input.MaxRetries
	}
	if input.MissedRunPolicy != nil {
		catchup, err := normalizeCatchup(*input.MissedRunPolicy)
		if err != nil {
			return nil, scheduleOutput{}, err
		}
		schedule.Catchup = catchup
	}
	if input.Expression != nil {
		schedule.Expression = *input.Expression
	}
	if input.Timezone != nil {
		schedule.Timezone = *input.Timezone
	}
	if input.Expression != nil || input.Timezone != nil {
		timezone, nextRun, err := scheduleNextRun(schedule.Type, schedule.Expression, schedule.Timezone, time.Now().UTC())
		if err != nil {
			return nil, scheduleOutput{}, err
		}
		schedule.Timezone, schedule.NextRunAt = timezone, nextRun
	}
	if input.RuleVersion != nil {
		if *input.RuleVersion <= 0 {
			return nil, scheduleOutput{}, errors.New("ruleVersion must be positive")
		}
		schedule.RuleVersionNumber = *input.RuleVersion
	}
	if input.Inputs != nil {
		contract, err := s.store.GetRuleVersionContract(ctx, schedule.RuleID, schedule.RuleVersionNumber)
		if input.RuleVersion != nil {
			contract, err = s.store.GetRuleVersionContract(ctx, schedule.RuleID, *input.RuleVersion)
		}
		if err != nil {
			return nil, scheduleOutput{}, executionInputError(err)
		}
		if err := rejectMCPSecretValues(*input.Inputs, jsonMap(contract.InputSchema)); err != nil {
			return nil, scheduleOutput{}, err
		}
		encoded, _ := json.Marshal(*input.Inputs)
		schedule.Variables = models.JSON(encoded)
	}
	if input.BrowserProfileID != nil {
		schedule.BrowserProfileID = *input.BrowserProfileID
	}
	if input.RuleVersion != nil || input.Inputs != nil || input.BrowserProfileID != nil {
		binding := &models.Task{RuleID: schedule.RuleID, RuleVersionNumber: schedule.RuleVersionNumber,
			Variables: schedule.Variables, BrowserProfileID: schedule.BrowserProfileID}
		if err := s.store.BindTaskToRuleVersion(ctx, binding, schedule.RuleVersionNumber); err != nil {
			return nil, scheduleOutput{}, executionInputError(err)
		}
		schedule.RuleVersion, schedule.RuleVersionNumber = binding.RuleVersion, binding.RuleVersionNumber
		schedule.Variables, schedule.InputSchema = binding.Variables, binding.InputSchema
		schedule.BrowserProfileID = binding.BrowserProfileID
	}
	schedule.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateSchedule(ctx, schedule); err != nil {
		return nil, scheduleOutput{}, scheduleError(err)
	}
	return nil, toScheduleOutput(schedule), nil
}

type deleteScheduleInput struct {
	ScheduleID string `json:"scheduleId"`
}

func (s *service) deleteSchedule(ctx context.Context, _ *mcp.CallToolRequest, input deleteScheduleInput) (*mcp.CallToolResult, mutationOutput, error) {
	if input.ScheduleID == "" {
		return nil, mutationOutput{}, errors.New("scheduleId is required")
	}
	if err := s.store.DeleteSchedule(ctx, input.ScheduleID); err != nil {
		return nil, mutationOutput{}, scheduleError(err)
	}
	return nil, mutationOutput{Success: true, ID: input.ScheduleID}, nil
}

func toScheduleOutput(schedule *models.Schedule) scheduleOutput {
	output := scheduleOutput{
		ID: schedule.ID, RuleID: schedule.RuleID, RuleVersion: schedule.RuleVersionNumber,
		RuleVersionLabel: schedule.RuleVersion, Name: schedule.Name, Type: string(schedule.Type),
		Expression: schedule.Expression, Enabled: schedule.Enabled,
		Inputs:           maskSecretInputs(jsonMap(schedule.Variables), jsonMap(schedule.InputSchema)),
		InputSchema:      sanitizeSchema(jsonMap(schedule.InputSchema)),
		BrowserProfileID: schedule.BrowserProfileID, Timezone: schedule.Timezone,
		Priority: string(schedule.Priority), MaxRetries: schedule.MaxRetries,
		MissedRunPolicy: string(schedule.Catchup), CreatedAt: schedule.CreatedAt, UpdatedAt: schedule.UpdatedAt,
	}
	if schedule.NextRunAt.Valid {
		value := schedule.NextRunAt.Time
		output.NextRunAt = &value
	}
	if schedule.LastRunAt.Valid {
		value := schedule.LastRunAt.Time
		output.LastRunAt = &value
	}
	return output
}

func scheduleNextRun(scheduleType models.ScheduleType, expression, timezone string, now time.Time) (string, sql.NullTime, error) {
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", sql.NullTime{}, errors.New("timezone must be a valid IANA timezone")
	}
	switch scheduleType {
	case models.ScheduleTypeOnce:
		value, err := time.Parse(time.RFC3339, expression)
		if err != nil {
			return "", sql.NullTime{}, errors.New("expression must be RFC3339 for a once schedule")
		}
		return timezone, sql.NullTime{Time: value.UTC(), Valid: true}, nil
	case models.ScheduleTypeCron:
		if err := scheduler.ValidateCron(expression); err != nil {
			return "", sql.NullTime{}, fmt.Errorf("invalid cron expression: %w", err)
		}
		next, err := scheduler.ComputeNextRunInLocation(expression, now, timezone)
		if err != nil {
			return "", sql.NullTime{}, err
		}
		return timezone, sql.NullTime{Time: next, Valid: true}, nil
	default:
		return "", sql.NullTime{}, errors.New("type must be once or cron")
	}
}

func normalizePriority(value string) (models.Priority, error) {
	if value == "" {
		return models.PriorityNormal, nil
	}
	priority := models.Priority(value)
	if priority != models.PriorityLow && priority != models.PriorityNormal && priority != models.PriorityHigh {
		return "", errors.New("priority must be low, normal, or high")
	}
	return priority, nil
}

func normalizeCatchup(value string) (models.CatchupMode, error) {
	if value == "" {
		return models.CatchupSkip, nil
	}
	catchup := models.CatchupMode(value)
	if catchup != models.CatchupSkip && catchup != models.CatchupRunOnce {
		return "", errors.New("missedRunPolicy must be skip or run_once")
	}
	return catchup, nil
}

func normalizePage(limit, offset int) (int, int, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return 0, 0, errors.New("limit must be between 1 and 100 and offset cannot be negative")
	}
	return limit, offset, nil
}

func executionInputError(err error) error {
	if errors.Is(err, store.ErrRuleVersionNotApproved) || errors.Is(err, store.ErrRuleVersionNotFound) || errors.Is(err, rulecontract.ErrInvalidTaskInput) {
		return fmt.Errorf("invalid rule version or inputs: %w", err)
	}
	return err
}

func taskError(err error) error {
	switch {
	case errors.Is(err, store.ErrTaskNotFound):
		return errors.New("task not found")
	case errors.Is(err, store.ErrTaskNotCancellable):
		return errors.New("task cannot be cancelled in its current state")
	case errors.Is(err, store.ErrTaskNotRetryable):
		return errors.New("task cannot be retried in its current state")
	default:
		return err
	}
}

func scheduleError(err error) error {
	if errors.Is(err, store.ErrRuleNotFound) {
		return errors.New("schedule not found")
	}
	return err
}

func jsonMap(raw models.JSON) map[string]any {
	value := map[string]any{}
	_ = json.Unmarshal(raw, &value)
	return value
}

func defaultMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func maskSecretInputs(inputs, schema map[string]any) map[string]any {
	clean := sanitizeMap(inputs)
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		if isSecretSchema(property) {
			if _, exists := clean[name]; exists {
				clean[name] = "[REDACTED]"
			}
		}
	}
	return clean
}

func rejectMCPSecretValues(inputs map[string]any, schema map[string]any) error {
	properties, _ := schema["properties"].(map[string]any)
	for name := range inputs {
		property, _ := properties[name].(map[string]any)
		if credentialKey(name) || isSecretSchema(property) {
			return fmt.Errorf("%s is a secret-declared input; MCP accepts browserProfileId references instead of secret values", name)
		}
	}
	return nil
}

func sanitizeSchema(schema map[string]any) map[string]any {
	clean := cloneMap(schema)
	sanitizeSchemaNode(clean)
	return clean
}

func sanitizeSchemaNode(schema map[string]any) {
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		if isSecretSchema(property) || credentialKey(name) {
			delete(property, "default")
			delete(property, "examples")
			delete(property, "enum")
		}
		sanitizeSchemaNode(property)
	}
	if items, ok := schema["items"].(map[string]any); ok {
		sanitizeSchemaNode(items)
	}
}

func cloneMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	clean := map[string]any{}
	_ = json.Unmarshal(raw, &clean)
	return clean
}

func isSecretSchema(property map[string]any) bool {
	for _, key := range []string{"secret", "x-secret", "writeOnly"} {
		if value, _ := property[key].(bool); value {
			return true
		}
	}
	return false
}

func sanitizeMap(value map[string]any) map[string]any {
	clean, _ := sanitizeValue(value).(map[string]any)
	if clean == nil {
		return map[string]any{}
	}
	return clean
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, child := range typed {
			if credentialKey(key) {
				clean[key] = "[REDACTED]"
				continue
			}
			clean[key] = sanitizeValue(child)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, child := range typed {
			clean[index] = sanitizeValue(child)
		}
		return clean
	default:
		return typed
	}
}

func credentialKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(key))
	switch normalized {
	case "password", "passwd", "authorization", "cookie", "setcookie", "apikey",
		"accesstoken", "refreshtoken", "authtoken", "clientsecret", "secret":
		return true
	default:
		return false
	}
}
