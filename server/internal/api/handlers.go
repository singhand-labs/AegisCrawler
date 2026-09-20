package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	rulecontract "github.com/singhand-labs/AegisCrawler/internal/rule"
	"github.com/singhand-labs/AegisCrawler/internal/scheduler"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

// M-2: cap list endpoints so a malicious or buggy client cannot ask the store
// to load all rows into memory. Matches the existing GetVersionedTaskResults
// cap of 500.
const maxListLimit = 500

// JSON alias for handler use.
type JSON = models.JSON

// Handler holds HTTP handlers.
type Handler struct {
	store           *store.Store
	scheduler       *scheduler.Scheduler
	llmJobManager   *llm.JobManager
	intentPredictor intentPredictor
	dslGenerator    *dsl.Generator
	cfg             *config.Config
	logger          *zap.Logger
	metrics         *Metrics
	recordings      *platformrecording.Service
	requirements    requirementManager
	dslWorkflows    dslWorkflowManager

	// budgetLedger and budgetReconciled expose the hard-budget state to the
	// read-only Admin endpoints and to enforced readiness.
	budgetLedger     budget.Ledger
	budgetReconciled bool
	llmRuntime       interface {
		RouteReady(route string) (bool, string)
	}
	llmBudgetMetricsRefresher interface {
		RefreshBudgetMetrics(context.Context) error
	}
}

// SetLLMBudget binds the active hard-budget ledger and records that startup
// reconciliation completed. Enforced readiness stays unhealthy until both are
// set.
func (h *Handler) SetLLMBudget(ledger budget.Ledger, reconciled bool) {
	h.budgetLedger = ledger
	h.budgetReconciled = reconciled
}

// SetLLMRuntime binds the constructed route state used by readiness and the
// sanitized policy endpoint. A usage-contract violation can close a route
// after startup without mutating the immutable configuration.
func (h *Handler) SetLLMRuntime(runtime interface {
	RouteReady(route string) (bool, string)
}) {
	h.llmRuntime = runtime
	h.llmBudgetMetricsRefresher, _ = runtime.(interface {
		RefreshBudgetMetrics(context.Context) error
	})
}

// NewHandler creates a handler.
func NewHandler(s *store.Store, sch *scheduler.Scheduler, llmJobManager *llm.JobManager, intentPredictor intentPredictor, cfg *config.Config, logger *zap.Logger) *Handler {
	h := &Handler{store: s, scheduler: sch, llmJobManager: llmJobManager, intentPredictor: intentPredictor, cfg: cfg, logger: logger}
	if s != nil {
		h.recordings = platformrecording.NewService(s, cfg)
	}
	return h
}

// SetMetrics assigns the metrics collector to the handler.
func (h *Handler) SetMetrics(metrics *Metrics) {
	h.metrics = metrics
}

// SetRequirementManager enables the feature-negotiated collection requirement workflow.
func (h *Handler) SetRequirementManager(manager requirementManager) {
	h.requirements = manager
}

// SetDSLWorkflowManager enables durable generation, replay, repair, and approval.
func (h *Handler) SetDSLWorkflowManager(manager dslWorkflowManager) {
	h.dslWorkflows = manager
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: message, Code: code})
}

// auditLog records a privileged admin action. Failures are logged but never fail the request.
func (h *Handler) auditLog(ctx context.Context, action, resourceType, resourceID string, payload map[string]any) {
	if err := h.auditLogRequired(ctx, action, resourceType, resourceID, payload); err != nil {
		h.logger.Error("failed to insert audit log", zap.Error(err))
	}
}

func (h *Handler) auditLogRequired(ctx context.Context, action, resourceType, resourceID string, payload map[string]any) error {
	if h.store == nil {
		return errors.New("audit store is unavailable")
	}
	actor := authz.Subject(ctx, h.cfg.AuditActor)
	if actor == "" {
		actor = "admin"
	}
	p := store.JSON(payload)
	if len(p) == 0 {
		p = store.JSON(map[string]any{})
	}
	entry := &models.AuditLog{
		ID:           store.NewID(),
		Actor:        actor,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Payload:      p,
		CreatedAt:    time.Now().UTC(),
	}
	return h.store.InsertAuditLog(ctx, entry)
}

// @title AegisCrawler Page Agent Service
// @version 0.1.0
// @description Production-grade task coordination service for AegisCrawler data collection.
// @license.name GPL-3.0-or-later
// @BasePath /
// @securityDefinitions.apikey AdminApiKey
// @in header
// @name Authorization
// @description Provide the admin API key as `Bearer <token>`. Admin endpoints require this header when ADMIN_API_KEY is configured.

// ClaimTask godoc
// @Summary Claim a pending task
// @Description Workers call this endpoint to lease a task. Returns task parameters encoded in the response.
// @Tags tasks
// @Accept json
// @Produce json
// @Param request body ClaimTaskRequest true "Worker identification"
// @Success 200 {object} ClaimTaskResponse
// @Failure 204 {object} ErrorResponse "No task available"
// @Failure 429 {object} ErrorResponse "Too many requests"
// @Failure 500 {object} ErrorResponse
// @Router /tasks/claim [post]
func (h *Handler) ClaimTask(w http.ResponseWriter, r *http.Request) {
	var req ClaimTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.BrowserProfileID = strings.TrimSpace(req.BrowserProfileID)
	if req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "workerId is required")
		return
	}
	if len(req.BrowserProfileID) > 200 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "browserProfileId must not exceed 200 bytes")
		return
	}

	claim, err := h.store.ClaimTaskForBrowserProfileWithContract(r.Context(), req.WorkerID, req.BrowserProfileID, h.cfg.LeaseDuration, h.cfg.MaxWorkerTasks)
	if err != nil {
		if errors.Is(err, store.ErrNoTaskAvailable) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.logger.Error("claim task failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to claim task")
		return
	}
	task := claim.Task

	if h.metrics != nil {
		h.metrics.IncTasksClaimed()
	}
	var immutableRule *models.Rule
	if claim.RuleVersion != nil {
		immutableRule = claim.RuleVersion.Rule
	}

	response := ClaimTaskResponse{
		TaskID:            task.ID,
		AttemptID:         task.CurrentAttemptID.String,
		WorkspaceID:       task.WorkspaceID,
		RuleID:            task.RuleID,
		RuleVersion:       task.RuleVersion,
		RuleVersionNumber: task.RuleVersionNumber,
		Rule:              immutableRule,
		BrowserProfileID:  task.BrowserProfileID,
		Variables:         rawToMap(task.Variables),
		LeaseUntil:        task.LeaseUntil.Time,
	}
	applyClaimSourceLineage(&response, claim.Contract)
	writeJSON(w, http.StatusOK, response)
}

// GetRule godoc
// @Summary Get a rule by ID
// @Description Returns the full rule configuration for a worker to execute.
// @Tags rules
// @Produce json
// @Param id path string true "Rule ID"
// @Success 200 {object} RuleResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /rules/{id} [get]
func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.getRuleByID(w, r, id)
}

// AdminGetRule godoc
// @Summary Get a rule by ID for admin UI
// @Description Returns the full rule configuration for visualization and editing.
// @Tags admin
// @Produce json
// @Param id path string true "Rule ID"
// @Success 200 {object} RuleResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/{id} [get]
func (h *Handler) AdminGetRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.getRuleByID(w, r, id)
}

func (h *Handler) getRuleByID(w http.ResponseWriter, r *http.Request, id string) {
	rule, err := h.store.GetRuleByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
			return
		}
		h.logger.Error("get rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get rule")
		return
	}
	writeJSON(w, http.StatusOK, toRuleResponse(rule))
}

// GetTask godoc
// @Summary Get a task by ID
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} TaskResponse
// @Failure 404 {object} ErrorResponse
// @Router /tasks/{id} [get]
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.store.GetTaskByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		h.logger.Error("get task failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get task")
		return
	}
	response := toTaskResponse(task)
	_, contract, err := h.store.ResolveTaskRuleVersionContract(r.Context(), task)
	if err != nil {
		h.logger.Error("get task rule version contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get task")
		return
	}
	applyTaskSourceLineage(response, contract)
	writeJSON(w, http.StatusOK, response)
}

// SubmitResult godoc
// @Summary Submit a result payload
// @Description Workers send extracted data or control messages here.
// @Tags results
// @Accept json
// @Produce json
// @Param request body ResultRequest true "Result payload"
// @Success 200 {object} ResultSubmissionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /results [post]
func (h *Handler) SubmitResult(w http.ResponseWriter, r *http.Request) {
	var req ResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.TaskID == "" || req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "taskId and workerId required")
		return
	}

	payload, _ := json.Marshal(req.Payload)
	if req.AttemptID != "" || req.IdempotencyKey != "" || req.Sequence > 0 || req.Kind != "" {
		if req.AttemptID == "" || req.IdempotencyKey == "" || req.Sequence <= 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "attemptId, idempotencyKey, and a positive sequence are required")
			return
		}
		kind := models.ResultKind(req.Kind)
		if kind == "" {
			kind = models.ResultKindBatch
		}
		if kind != models.ResultKindBatch && kind != models.ResultKindSummary {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "kind must be batch or summary")
			return
		}
		valid := true
		validationMessage := ""
		if kind == models.ResultKindBatch {
			task, err := h.store.GetTaskByID(r.Context(), req.TaskID)
			if err != nil {
				if errors.Is(err, store.ErrTaskNotFound) {
					writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to validate result")
				return
			}
			var decoded any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				valid = false
				validationMessage = "payload is not valid JSON"
			} else if err := rulecontract.ValidateTaskResult(task.OutputSchema, decoded); err != nil {
				valid = false
				validationMessage = err.Error()
			}
		}
		result := &models.Result{
			ID: store.NewID(), TaskID: req.TaskID, WorkerID: req.WorkerID,
			AttemptID: req.AttemptID, IdempotencyKey: req.IdempotencyKey,
			Sequence: req.Sequence, Kind: kind, Payload: payload, Valid: valid,
			ValidationError: validationMessage, Immediate: req.Immediate,
			CreatedAt: time.Now().UTC(),
		}
		duplicate, err := h.store.InsertResultIdempotent(r.Context(), result)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrResultConflict), errors.Is(err, store.ErrExecutionAttemptConflict):
				writeError(w, http.StatusConflict, "RESULT_CONFLICT", err.Error())
			default:
				h.logger.Error("insert versioned result failed", zap.Error(err))
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store result")
			}
			return
		}
		if h.metrics != nil && !duplicate {
			h.metrics.IncResultsReceived()
		}
		writeJSON(w, http.StatusOK, ResultSubmissionResponse{
			Success: valid, Valid: valid, Duplicate: duplicate, Error: validationMessage,
		})
		return
	}
	result := &models.Result{
		ID:        store.NewID(),
		TaskID:    req.TaskID,
		WorkerID:  req.WorkerID,
		Kind:      legacyResultKind(req.Payload, req.TaskID),
		Payload:   payload,
		Immediate: req.Immediate,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.InsertResult(r.Context(), result); err != nil {
		h.logger.Error("insert result failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store result")
		return
	}
	if h.metrics != nil {
		h.metrics.IncResultsReceived()
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func legacyResultKind(payload any, taskID string) models.ResultKind {
	marker, ok := payload.(map[string]any)
	if !ok || len(marker) != 2 {
		return models.ResultKindBatch
	}
	final, finalOK := marker["__final"].(bool)
	markerTaskID, taskOK := marker["taskId"].(string)
	if finalOK && final && taskOK && markerTaskID == taskID {
		return models.ResultKindSummary
	}
	return models.ResultKindBatch
}

// GetVersionedTaskResults godoc
// @Summary Get versioned task result batches
// @Description Returns paginated schema-valid batches, the immutable output schema, an optional final summary, and optional invalid diagnostics.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Task ID"
// @Param limit query int false "Page size (1-500, default 100)"
// @Param offset query int false "Page offset (default 0)"
// @Param include_invalid query bool false "Include invalid batch diagnostics"
// @Success 200 {object} TaskResultsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/tasks/{id}/results [get]
func (h *Handler) GetVersionedTaskResults(w http.ResponseWriter, r *http.Request) {
	task, err := h.store.GetTaskByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get task")
		return
	}
	limit, offset := 100, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value <= 0 || value > 500 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "limit must be between 1 and 500")
			return
		}
		limit = value
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "offset must be non-negative")
			return
		}
		offset = value
	}
	includeInvalid, _ := strconv.ParseBool(r.URL.Query().Get("include_invalid"))
	page, err := h.store.ListResultPage(r.Context(), task.ID, limit, offset, includeInvalid)
	if err != nil {
		h.logger.Error("list versioned results failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list results")
		return
	}
	response := TaskResultsResponse{
		TaskID: task.ID, RuleID: task.RuleID, RuleVersion: task.RuleVersion,
		RuleVersionNumber: task.RuleVersionNumber, OutputSchema: rawToMap(task.OutputSchema), Page: page,
	}
	_, contract, err := h.store.ResolveTaskRuleVersionContract(r.Context(), task)
	if err != nil {
		h.logger.Error("get task results rule version contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list results")
		return
	}
	applyTaskResultsSourceLineage(&response, contract)
	writeJSON(w, http.StatusOK, response)
}

// SubmitLog godoc
// @Summary Submit a log entry
// @Tags logs
// @Accept json
// @Produce json
// @Param request body LogRequest true "Log entry"
// @Success 200 {object} SuccessResponse
// @Router /logs [post]
func (h *Handler) SubmitLog(w http.ResponseWriter, r *http.Request) {
	var req LogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	extra, _ := json.Marshal(req.Extra)
	log := &models.LogEntry{
		ID:        store.NewID(),
		TaskID:    req.TaskID,
		WorkerID:  req.WorkerID,
		Level:     req.Level,
		Message:   req.Message,
		Extra:     extra,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.InsertLog(r.Context(), log); err != nil {
		h.logger.Error("insert log failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store log")
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// SubmitHeartbeat godoc
// @Summary Submit a heartbeat
// @Description Renews the task lease and stores worker liveness data. Returns a cancel signal when the task has been requested to cancel.
// @Tags tasks
// @Accept json
// @Produce json
// @Param request body HeartbeatRequest true "Heartbeat"
// @Success 200 {object} HeartbeatResponse
// @Router /heartbeat [post]
func (h *Handler) SubmitHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.TaskID == "" || req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "taskId and workerId required")
		return
	}

	if err := h.store.RenewLease(r.Context(), req.TaskID, req.WorkerID, h.cfg.LeaseDuration); err != nil {
		if errors.Is(err, store.ErrLeaseConflict) {
			writeError(w, http.StatusConflict, "LEASE_CONFLICT", "task lease held by another worker")
			return
		}
		h.logger.Error("renew lease failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to renew lease")
		return
	}

	payload, _ := json.Marshal(req.Payload)
	hb := &models.Heartbeat{
		ID:        store.NewID(),
		TaskID:    req.TaskID,
		WorkerID:  req.WorkerID,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.InsertHeartbeat(r.Context(), hb); err != nil {
		h.logger.Error("insert heartbeat failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store heartbeat")
		return
	}
	if h.metrics != nil {
		h.metrics.IncHeartbeatsReceived()
	}

	task, err := h.store.GetTaskByID(r.Context(), req.TaskID)
	if err != nil {
		h.logger.Error("get task for heartbeat failed", zap.Error(err))
		writeJSON(w, http.StatusOK, HeartbeatResponse{Success: true})
		return
	}
	writeJSON(w, http.StatusOK, HeartbeatResponse{Success: true, CancelRequested: task.CancelRequested})
}

// SubmitStatus godoc
// @Summary Update task status
// @Tags tasks
// @Accept json
// @Produce json
// @Param request body StatusRequest true "Status update"
// @Success 200 {object} SuccessResponse
// @Router /status [post]
func (h *Handler) SubmitStatus(w http.ResponseWriter, r *http.Request) {
	var req StatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.TaskID == "" || req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "taskId and workerId required")
		return
	}

	validStatus := false
	terminalStatus := false
	switch req.Status {
	case string(models.TaskStatusRunning):
		validStatus = true
	case string(models.TaskStatusDone), string(models.TaskStatusFailed), string(models.TaskStatusCancelled), string(models.TaskStatusDeadLetter):
		validStatus = true
		terminalStatus = true
	}
	if !validStatus {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid task status")
		return
	}
	task, err := h.store.GetTaskByID(r.Context(), req.TaskID)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to inspect task execution contract")
		return
	}
	versionedTask := taskRequiresAttemptLineage(task)
	if versionedTask && req.AttemptID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "attemptId is required for a versioned task")
		return
	}

	duplicate := false
	err = nil
	if req.AttemptID != "" {
		duplicate, err = h.store.UpdateTaskStatusForAttempt(
			r.Context(), req.TaskID, req.WorkerID, req.AttemptID, req.Status, req.Message, "",
		)
	} else {
		err = h.store.UpdateTaskStatus(r.Context(), req.TaskID, req.WorkerID, req.Status, req.Message, "")
	}
	if err != nil {
		if errors.Is(err, store.ErrLeaseConflict) || errors.Is(err, store.ErrExecutionAttemptConflict) {
			writeError(w, http.StatusConflict, "LEASE_CONFLICT", "task lease is no longer active")
			return
		}
		if errors.Is(err, store.ErrIncompleteAttemptResults) {
			writeError(w, http.StatusConflict, "INCOMPLETE_RESULTS", err.Error())
			return
		}
		h.logger.Error("update task status failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update task status")
		return
	}

	if duplicate {
		writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
		return
	}

	update := &models.TaskStatusUpdate{
		ID:        store.NewID(),
		TaskID:    req.TaskID,
		WorkerID:  req.WorkerID,
		Status:    req.Status,
		Message:   req.Message,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.InsertStatusUpdate(r.Context(), update); err != nil {
		h.logger.Error("insert status update failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store status update")
		return
	}

	if terminalStatus && h.metrics != nil {
		h.metrics.IncTasksCompleted(req.Status)
	}

	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func taskRequiresAttemptLineage(task *models.Task) bool {
	if task == nil {
		return false
	}
	input := strings.TrimSpace(string(task.InputSchema))
	if input != "" && input != "{}" {
		return true
	}
	output := strings.TrimSpace(string(task.OutputSchema))
	if output == "" || output == "{}" {
		return false
	}
	// Migration 16 assigned this permissive contract to legacy rules that had
	// no declared output. Keep those tasks on the legacy status path. Any
	// meaningful output constraint is a versioned result contract and therefore
	// requires explicit attempt lineage even when its input schema is empty.
	var schema map[string]any
	if json.Unmarshal(task.OutputSchema, &schema) != nil {
		return true
	}
	properties, _ := schema["properties"].(map[string]any)
	additional, hasAdditional := schema["additionalProperties"].(bool)
	return len(properties) > 0 || !hasAdditional || !additional
}

// SubmitCheckpoint godoc
// @Summary Submit a checkpoint
// @Description Workers save recoverable state (URL, variables, extracted data) at critical steps.
// @Tags tasks
// @Accept json
// @Produce json
// @Param request body CheckpointRequest true "Checkpoint"
// @Success 200 {object} SuccessResponse
// @Router /tasks/{id}/checkpoints [post]
func (h *Handler) SubmitCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req CheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.TaskID == "" || req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "taskId and workerId required")
		return
	}

	payload, _ := json.Marshal(req.Payload)
	cp := &models.Checkpoint{
		ID:        store.NewID(),
		TaskID:    req.TaskID,
		WorkerID:  req.WorkerID,
		Name:      req.Name,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.InsertCheckpoint(r.Context(), cp); err != nil {
		h.logger.Error("insert checkpoint failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store checkpoint")
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// GetCheckpoint godoc
// @Summary Get the latest checkpoint for a task
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} CheckpointResponse
// @Failure 404 {object} ErrorResponse
// @Router /tasks/{id}/checkpoints/latest [get]
func (h *Handler) GetCheckpoint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cp, err := h.store.GetLatestCheckpoint(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no checkpoint found")
			return
		}
		h.logger.Error("get checkpoint failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get checkpoint")
		return
	}
	writeJSON(w, http.StatusOK, CheckpointResponse{
		ID:        cp.ID,
		TaskID:    cp.TaskID,
		WorkerID:  cp.WorkerID,
		Name:      cp.Name,
		Payload:   rawToMap(models.JSON(cp.Payload)),
		CreatedAt: cp.CreatedAt,
	})
}

// GetResults godoc
// @Summary Get aggregated results for a task
// @Tags results
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {array} models.Result
// @Failure 409 {object} ErrorResponse "Source lineage requires the versioned results endpoint"
// @Failure 500 {object} ErrorResponse
// @Router /tasks/{id}/results [get]
func (h *Handler) GetResults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.store.GetTaskByID(r.Context(), id)
	if err != nil {
		if !errors.Is(err, store.ErrTaskNotFound) {
			h.logger.Error("get result task failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list results")
			return
		}
	} else {
		_, contract, contractErr := h.store.ResolveTaskRuleVersionContract(r.Context(), task)
		if contractErr != nil {
			h.logger.Error("get result task rule version contract failed", zap.Error(contractErr))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list results")
			return
		}
		if contractHasSourceLineage(contract) {
			writeError(w, http.StatusConflict, "LINEAGE_REQUIRED",
				"task results include immutable source lineage; use /api/v1/tasks/"+id+"/results")
			return
		}
	}
	results, err := h.store.ListResults(r.Context(), id)
	if err != nil {
		h.logger.Error("list results failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list results")
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// GetAdminResults godoc
// @Summary Get aggregated results for a task (admin)
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Task ID"
// @Success 200 {array} models.Result
// @Failure 401 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse "Source lineage requires the versioned results endpoint"
// @Failure 500 {object} ErrorResponse
// @Router /admin/tasks/{id}/results [get]
func (h *Handler) GetAdminResults(w http.ResponseWriter, r *http.Request) {
	h.GetResults(w, r)
}

func contractHasSourceLineage(contract *models.RuleVersionContract) bool {
	return contract != nil && (contract.SourceKind != "" ||
		contract.SourceAuthority != "" ||
		contract.SourceArtifactHash != "" ||
		contract.SourceExportHash != "" ||
		contract.SourceWorkflowID != "")
}

// GetTaskLogs godoc
// @Summary Get log entries for a task
// @Tags logs
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {array} models.LogEntry
// @Router /tasks/{id}/logs [get]
func (h *Handler) GetTaskLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	logs, err := h.store.ListLogs(r.Context(), id)
	if err != nil {
		h.logger.Error("list logs failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list logs")
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// SubmitSnapshot godoc
// @Summary Submit a snapshot
// @Tags snapshots
// @Accept json
// @Produce json
// @Param request body SnapshotRequest true "Snapshot"
// @Success 200 {object} SuccessResponse
// @Router /snapshots [post]
func (h *Handler) SubmitSnapshot(w http.ResponseWriter, r *http.Request) {
	var req SnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	snap := &models.Snapshot{
		ID:        store.NewID(),
		TaskID:    req.TaskID,
		WorkerID:  req.WorkerID,
		Name:      req.Name,
		Type:      req.Type,
		Data:      req.Data,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.InsertSnapshot(r.Context(), snap); err != nil {
		h.logger.Error("insert snapshot failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store snapshot")
		return
	}
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// Health godoc
// @Summary Health check
// @Description Default liveness check returns a lightweight OK. Use ?ready=1 to verify the database is reachable.
// @Tags system
// @Produce json
// @Param ready query bool false "Run readiness check including database connectivity" default(false)
// @Success 200 {object} map[string]string
// @Failure 503 {object} map[string]any
// @Router /health [get]
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("ready") == "1" {
		if err := h.store.Ping(r.Context()); err != nil {
			h.logger.Error("health readiness check failed", zap.Error(err))
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "error",
				"checks": map[string]string{"database": "unreachable"},
			})
			return
		}
		// Liveness stays independent of this check so an unhealthy ledger cannot
		// cause a process restart loop.
		if check, ready := h.enforcedLLMReadiness(r.Context()); !ready {
			h.logger.Error("enforced llm readiness check failed", zap.String("check", check))
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "error",
				"checks": map[string]string{"enforcedLLM": check},
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// enforcedLLMReadiness verifies, when enforced LLM is enabled, that policy
// validation succeeded, startup reconciliation completed, and the ledger answers
// a bounded read transaction. A legacy LLM-enabled process is locally ready but
// reports productionEligible=false from the policy endpoint.
func (h *Handler) enforcedLLMReadiness(ctx context.Context) (string, bool) {
	if h.cfg == nil || !h.cfg.LLMEnabled {
		return "", true
	}
	if err := h.cfg.ValidateLLMPolicy(); err != nil {
		return "policy_invalid", false
	}
	policy := h.cfg.EnforcedLLMPolicy()
	if policy == nil {
		// Legacy mode remains locally ready for migration and development.
		return "", true
	}
	if err := budget.ValidatePolicyAdmissibility(policy); err != nil {
		return "budget_policy_inadmissible", false
	}
	if h.budgetLedger == nil {
		return "budget_ledger_unbound", false
	}
	if !h.budgetReconciled {
		return "reconciliation_incomplete", false
	}
	if h.llmRuntime == nil {
		return "primary_route_not_constructed", false
	}
	if ready, reason := h.llmRuntime.RouteReady("primary"); !ready {
		if reason == "not_constructed" {
			return "primary_route_not_constructed", false
		}
		return "primary_route_closed", false
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := h.budgetLedger.Snapshot(readCtx, "", budget.BudgetDay(time.Now().UTC())); err != nil {
		return "budget_ledger_unreadable", false
	}
	return "", true
}

// CreateRule godoc
// @Summary Create or update a rule (admin)
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param rule body models.Rule true "Rule"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Router /admin/rules [post]
func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	if h.rejectLegacyRuleMutationWhenVersioned(w) {
		return
	}
	var rule models.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	// H-4: reject client-supplied IDs that match an existing rule. The store's
	// CreateRule is an upsert (INSERT ... ON CONFLICT DO UPDATE), so without
	// this guard a second POST silently overwrites the existing rule and
	// bypasses UpdateRule's approval-state checks.
	if rule.ID != "" {
		if existing, err := h.store.GetRuleByID(r.Context(), rule.ID); err == nil && existing != nil {
			writeError(w, http.StatusConflict, "RULE_EXISTS", "rule with this id already exists; use PATCH /admin/rules/"+rule.ID+" to update")
			return
		}
	}
	now := time.Now().UTC()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.Owner = authz.Subject(r.Context(), h.cfg.AuditActor)
	if rule.Owner == "" {
		rule.Owner = "admin"
	}
	rule.UpdatedAt = now
	if err := h.store.CreateRule(r.Context(), &rule); err != nil {
		h.logger.Error("create rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create rule")
		return
	}
	h.auditLog(r.Context(), "create_rule", "rule", rule.ID, map[string]any{"ruleId": rule.ID, "name": rule.Name})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// ApproveRule godoc
// @Summary Approve a rule (admin)
// @Description Sets a rule's approval_status to approved so it becomes eligible for task claiming.
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/{id}/approve [post]
func (h *Handler) ApproveRule(w http.ResponseWriter, r *http.Request) {
	if h.rejectLegacyRuleMutationWhenVersioned(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.store.UpdateRuleApprovalStatus(r.Context(), id, string(models.RuleApprovalApproved)); err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
			return
		}
		h.logger.Error("approve rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to approve rule")
		return
	}
	h.auditLog(r.Context(), "approve_rule", "rule", id, map[string]any{"ruleId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// RejectRule godoc
// @Summary Reject a rule (admin)
// @Description Sets a rule's approval_status to rejected so it is ineligible for task claiming.
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/{id}/reject [post]
func (h *Handler) RejectRule(w http.ResponseWriter, r *http.Request) {
	if h.rejectLegacyRuleMutationWhenVersioned(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.store.UpdateRuleApprovalStatus(r.Context(), id, string(models.RuleApprovalRejected)); err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
			return
		}
		h.logger.Error("reject rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to reject rule")
		return
	}
	h.auditLog(r.Context(), "reject_rule", "rule", id, map[string]any{"ruleId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// ListRules godoc
// @Summary List rules (admin)
// @Description Returns a paginated list of rules with optional filters. Query parameters: enabled, approval_status, domain, owner, limit, offset.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param enabled query bool false "Filter by enabled state"
// @Param approval_status query string false "Filter by approval_status"
// @Param domain query string false "Filter by domain raw JSON"
// @Param owner query string false "Filter by owner"
// @Param limit query int false "Page size (default 100)"
// @Param offset query int false "Page offset (default 0)"
// @Success 200 {object} ListRulesResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules [get]
func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	filter := store.ListRulesFilter{}

	if v := r.URL.Query().Get("enabled"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid enabled parameter")
			return
		}
		filter.Enabled = &b
	}
	filter.ApprovalStatus = r.URL.Query().Get("approval_status")
	filter.Domain = r.URL.Query().Get("domain")
	filter.Owner = r.URL.Query().Get("owner")

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "limit must be between 0 and "+strconv.Itoa(maxListLimit))
			return
		}
		filter.Limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid offset parameter")
			return
		}
		filter.Offset = n
	}

	rules, total, err := h.store.ListRules(r.Context(), filter)
	if err != nil {
		h.logger.Error("list rules failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list rules")
		return
	}

	resp := &ListRulesResponse{
		Rules: make([]*RuleResponse, 0, len(rules)),
		Total: total,
	}
	for _, rule := range rules {
		resp.Rules = append(resp.Rules, toRuleResponse(rule))
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateRule godoc
// @Summary Update a rule (admin)
// @Description Partially updates a rule. Only fields sent in the request body are modified; omitted fields remain unchanged. Uses fetch-and-merge through the existing upsert to preserve fields and foreign-key stability.
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Param rule body UpdateRuleRequest true "Rule fields to update"
// @Success 200 {object} RuleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/{id} [patch]
func (h *Handler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	if h.rejectLegacyRuleMutationWhenVersioned(w) {
		return
	}
	id := r.PathValue("id")
	var req UpdateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}

	rule, err := h.store.GetRuleByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
			return
		}
		h.logger.Error("get rule for update failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update rule")
		return
	}

	if req.Version != nil {
		rule.Version = *req.Version
	}
	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Domain != nil {
		rule.Domain = marshalJSON(*req.Domain)
	}
	if req.URLPattern != nil {
		rule.URLPattern = marshalJSON(*req.URLPattern)
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.Priority != nil {
		rule.Priority = models.Priority(*req.Priority)
	}
	if req.Entry != nil {
		rule.Entry = *req.Entry
	}
	if req.Variables != nil {
		rule.Variables = store.JSON(*req.Variables)
	}
	if req.Selectors != nil {
		rule.Selectors = store.JSON(*req.Selectors)
	}
	if req.Humanize != nil {
		rule.Humanize = store.JSON(*req.Humanize)
	}
	if req.Steps != nil {
		rule.Steps = marshalJSON(*req.Steps)
	}
	if req.Output != nil {
		rule.Output = store.JSON(*req.Output)
	}
	if req.SendPolicy != nil {
		rule.SendPolicy = store.JSON(*req.SendPolicy)
	}
	if req.Hooks != nil {
		rule.Hooks = store.JSON(*req.Hooks)
	}
	if req.Tags != nil {
		rule.Tags = store.JSON(*req.Tags)
	}
	// Ownership is derived from the authenticated principal. The legacy owner
	// request field is intentionally ignored so callers cannot transfer a rule
	// by writing identity data into a request body.
	if req.ApprovalStatus != nil {
		rule.ApprovalStatus = *req.ApprovalStatus
	}
	if req.Source != nil {
		rule.Source = *req.Source
	}
	rule.UpdatedAt = time.Now().UTC()

	if err := h.store.CreateRule(r.Context(), rule); err != nil {
		h.logger.Error("update rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update rule")
		return
	}
	h.auditLog(r.Context(), "update_rule", "rule", rule.ID, map[string]any{"ruleId": rule.ID})
	writeJSON(w, http.StatusOK, toRuleResponse(rule))
}

// DeleteRule godoc
// @Summary Delete a rule (admin)
// @Description Deletes a rule and all of its tasks, results, logs, snapshots, heartbeats, status updates, and checkpoints.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/{id} [delete]
func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	if h.rejectLegacyRuleMutationWhenVersioned(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.store.DeleteRule(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
			return
		}
		h.logger.Error("delete rule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete rule")
		return
	}
	h.auditLog(r.Context(), "delete_rule", "rule", id, map[string]any{"ruleId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// CreateTask godoc
// @Summary Create a task (admin)
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param task body CreateTaskRequest true "Task"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Router /admin/tasks [post]
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var task models.Task
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if task.ID == "" {
		task.ID = store.NewID()
	}
	if task.RuleID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "ruleId is required")
		return
	}
	// The legacy API requires the display label in ruleVersion even though rules
	// created through that API may have no immutable rule_versions row.
	explicitVersion, err := h.strictVersionSelection(r.Context(), task.RuleID, task.RuleVersionNumber, task.RuleVersion)
	if err != nil {
		h.logger.Error("inspect task rule version contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task")
		return
	}
	if err := h.store.BindTaskToRuleVersion(r.Context(), &task, task.RuleVersionNumber); err != nil {
		if explicitVersion || !errors.Is(err, store.ErrRuleVersionNotApproved) {
			if errors.Is(err, store.ErrRuleVersionNotApproved) || errors.Is(err, store.ErrRuleVersionNotFound) || errors.Is(err, rulecontract.ErrInvalidTaskInput) {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
				return
			}
			h.logger.Error("bind task to rule version failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task")
			return
		}
	}
	now := time.Now().UTC()
	task.CreatedAt = now
	task.UpdatedAt = now
	if task.Status == "" {
		task.Status = models.TaskStatusPending
	}
	if task.Priority == "" {
		task.Priority = models.PriorityNormal
	}
	if task.MaxRetries == 0 {
		task.MaxRetries = h.cfg.MaxRetries
	}
	// Future scheduled_at creates a pending task visible to workers only after that time.
	// Past or present scheduled_at is treated as an immediate task.
	if task.ScheduledAt.Valid && !task.ScheduledAt.Time.After(now) {
		task.ScheduledAt = sql.NullTime{Valid: false}
	}
	if err := h.store.CreateTask(r.Context(), &task); err != nil {
		h.logger.Error("create task failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task")
		return
	}
	h.auditLog(r.Context(), "create_task", "task", task.ID, map[string]any{"taskId": task.ID, "ruleId": task.RuleID})
	writeJSON(w, http.StatusOK, map[string]string{"taskId": task.ID})
}

// ListTasks godoc
// @Summary List tasks (admin)
// @Description Returns a paginated list of tasks with optional filters. Query parameters: status, rule_id, worker_id, priority, created_after, created_before, limit, offset. created_after and created_before accept RFC3339 timestamps.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param status query string false "Filter by status"
// @Param rule_id query string false "Filter by rule ID"
// @Param worker_id query string false "Filter by worker ID"
// @Param priority query string false "Filter by priority"
// @Param created_after query string false "Filter by created_at >= value (RFC3339)"
// @Param created_before query string false "Filter by created_at <= value (RFC3339)"
// @Param limit query int false "Page size (default 100)"
// @Param offset query int false "Page offset (default 0)"
// @Success 200 {object} ListTasksResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 500 {object} ErrorResponse
// @Router /admin/tasks [get]
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	filter := store.ListTasksFilter{}
	q := r.URL.Query()
	filter.Status = q.Get("status")
	filter.RuleID = q.Get("rule_id")
	filter.WorkerID = q.Get("worker_id")
	filter.Priority = q.Get("priority")

	if v := q.Get("created_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid created_after parameter")
			return
		}
		filter.CreatedAfter = t
	}
	if v := q.Get("created_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid created_before parameter")
			return
		}
		filter.CreatedBefore = t
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "limit must be between 0 and "+strconv.Itoa(maxListLimit))
			return
		}
		filter.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid offset parameter")
			return
		}
		filter.Offset = n
	}

	tasks, total, err := h.store.ListTasks(r.Context(), filter)
	if err != nil {
		h.logger.Error("list tasks failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list tasks")
		return
	}

	resp := &ListTasksResponse{
		Tasks: make([]*TaskResponse, 0, len(tasks)),
		Total: total,
	}
	for _, task := range tasks {
		item := toTaskResponse(task)
		_, contract, contractErr := h.store.ResolveTaskRuleVersionContract(r.Context(), task)
		if contractErr != nil {
			h.logger.Error("get listed task rule version contract failed", zap.Error(contractErr))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list tasks")
			return
		}
		applyTaskSourceLineage(item, contract)
		resp.Tasks = append(resp.Tasks, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListAuditLogs godoc
// @Summary List audit logs (admin)
// @Description Returns a paginated list of audit logs with optional filters. Query parameters: actor, action, resource_type, resource_id, created_after, created_before, limit, offset. created_after and created_before accept RFC3339 timestamps.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param actor query string false "Filter by actor"
// @Param action query string false "Filter by action"
// @Param resource_type query string false "Filter by resource type"
// @Param resource_id query string false "Filter by resource ID"
// @Param created_after query string false "Filter by created_at >= value (RFC3339)"
// @Param created_before query string false "Filter by created_at <= value (RFC3339)"
// @Param limit query int false "Page size (default 100)"
// @Param offset query int false "Page offset (default 0)"
// @Success 200 {object} ListAuditLogsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 500 {object} ErrorResponse
// @Router /admin/audit_logs [get]
func (h *Handler) ListAuditLogs(w http.ResponseWriter, r *http.Request) {
	filter := store.ListAuditLogsFilter{}
	q := r.URL.Query()
	filter.Actor = q.Get("actor")
	filter.Action = q.Get("action")
	filter.ResourceType = q.Get("resource_type")
	filter.ResourceID = q.Get("resource_id")

	if v := q.Get("created_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid created_after parameter")
			return
		}
		filter.CreatedAfter = t
	}
	if v := q.Get("created_before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid created_before parameter")
			return
		}
		filter.CreatedBefore = t
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "limit must be between 0 and "+strconv.Itoa(maxListLimit))
			return
		}
		filter.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid offset parameter")
			return
		}
		filter.Offset = n
	}

	logs, total, err := h.store.ListAuditLogs(r.Context(), filter)
	if err != nil {
		h.logger.Error("list audit logs failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list audit logs")
		return
	}

	writeJSON(w, http.StatusOK, ListAuditLogsResponse{Logs: logs, Total: total})
}

// CreateSchedule godoc
// @Summary Create a schedule (admin)
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param request body CreateScheduleRequest true "Schedule"
// @Success 200 {object} ScheduleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 500 {object} ErrorResponse
// @Router /admin/schedules [post]
func (h *Handler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.RuleID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "ruleId is required")
		return
	}
	if req.Expression == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "expression is required")
		return
	}
	if req.Type != string(models.ScheduleTypeOnce) && req.Type != string(models.ScheduleTypeCron) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "type must be once or cron")
		return
	}
	timezone := req.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "timezone must be a valid IANA timezone")
		return
	}

	rule, err := h.store.GetRuleByID(r.Context(), req.RuleID)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "rule not found")
			return
		}
		h.logger.Error("get rule for schedule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create schedule")
		return
	}
	if !rule.Enabled || rule.ApprovalStatus != string(models.RuleApprovalApproved) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "rule must be enabled and approved")
		return
	}

	var nextRunAt sql.NullTime
	switch models.ScheduleType(req.Type) {
	case models.ScheduleTypeOnce:
		t, err := time.Parse(time.RFC3339, req.Expression)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "expression must be a valid RFC3339 timestamp for type once")
			return
		}
		nextRunAt = sql.NullTime{Time: t.UTC(), Valid: true}
	case models.ScheduleTypeCron:
		if err := scheduler.ValidateCron(req.Expression); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid cron expression: "+err.Error())
			return
		}
		next, err := scheduler.ComputeNextRunInLocation(req.Expression, time.Now().UTC(), timezone)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to compute next run: "+err.Error())
			return
		}
		nextRunAt = sql.NullTime{Time: next, Valid: true}
	}

	now := time.Now().UTC()
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	priority := models.PriorityNormal
	if req.Priority != nil {
		priority = models.Priority(*req.Priority)
	}
	maxRetries := h.cfg.MaxRetries
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
	}
	catchup := models.CatchupSkip
	if req.Catchup != nil {
		catchup = models.CatchupMode(*req.Catchup)
	}
	if catchup != models.CatchupSkip && catchup != models.CatchupRunOnce {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "catchup must be skip or run_once")
		return
	}
	variables := models.JSON("{}")
	if req.Variables != nil {
		variables = store.JSON(*req.Variables)
	}
	ruleVersion := rule.Version
	if req.RuleVersion != "" {
		ruleVersion = req.RuleVersion
	}
	versionNumber := req.RuleVersionNumber
	inputSchema := models.JSON("{}")
	browserProfileID := req.BrowserProfileID
	binding := &models.Task{RuleID: req.RuleID, RuleVersion: req.RuleVersion,
		RuleVersionNumber: versionNumber, Variables: variables,
		BrowserProfileID: browserProfileID}
	// Preserve schedules created against legacy catalog rules while treating a
	// label as strict when this rule already has immutable version contracts.
	explicitVersion, err := h.strictVersionSelection(r.Context(), req.RuleID, versionNumber, req.RuleVersion)
	if err != nil {
		h.logger.Error("inspect schedule rule version contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create schedule")
		return
	}
	if err := h.store.BindTaskToRuleVersion(r.Context(), binding, versionNumber); err != nil {
		if explicitVersion || !errors.Is(err, store.ErrRuleVersionNotApproved) {
			if errors.Is(err, store.ErrRuleVersionNotApproved) || errors.Is(err, store.ErrRuleVersionNotFound) || errors.Is(err, rulecontract.ErrInvalidTaskInput) {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
				return
			}
			h.logger.Error("bind schedule to rule version failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create schedule")
			return
		}
	} else {
		ruleVersion = binding.RuleVersion
		versionNumber = binding.RuleVersionNumber
		variables = binding.Variables
		inputSchema = binding.InputSchema
		browserProfileID = binding.BrowserProfileID
	}
	sch := &models.Schedule{
		ID:                store.NewID(),
		RuleID:            req.RuleID,
		RuleVersion:       ruleVersion,
		RuleVersionNumber: versionNumber,
		Name:              req.Name,
		Type:              models.ScheduleType(req.Type),
		Expression:        req.Expression,
		Enabled:           enabled,
		NextRunAt:         nextRunAt,
		Variables:         variables,
		InputSchema:       inputSchema,
		BrowserProfileID:  browserProfileID,
		Timezone:          timezone,
		Priority:          priority,
		MaxRetries:        maxRetries,
		Catchup:           catchup,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := h.store.CreateSchedule(r.Context(), sch); err != nil {
		h.logger.Error("create schedule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create schedule")
		return
	}
	h.auditLog(r.Context(), "create_schedule", "schedule", sch.ID, map[string]any{"scheduleId": sch.ID, "ruleId": sch.RuleID})
	writeJSON(w, http.StatusOK, toScheduleResponse(sch))
}

// ListSchedules godoc
// @Summary List schedules (admin)
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param rule_id query string false "Filter by rule ID"
// @Param type query string false "Filter by type"
// @Param enabled query bool false "Filter by enabled state"
// @Param limit query int false "Page size (default 100)"
// @Param offset query int false "Page offset (default 0)"
// @Success 200 {object} ListSchedulesResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 500 {object} ErrorResponse
// @Router /admin/schedules [get]
func (h *Handler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	filter := store.ListSchedulesFilter{}
	q := r.URL.Query()
	filter.RuleID = q.Get("rule_id")
	filter.Type = q.Get("type")
	if v := q.Get("enabled"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid enabled parameter")
			return
		}
		filter.Enabled = &b
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "limit must be between 0 and "+strconv.Itoa(maxListLimit))
			return
		}
		filter.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid offset parameter")
			return
		}
		filter.Offset = n
	}

	schedules, total, err := h.store.ListSchedules(r.Context(), filter)
	if err != nil {
		h.logger.Error("list schedules failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list schedules")
		return
	}

	resp := &ListSchedulesResponse{
		Schedules: make([]*ScheduleResponse, 0, len(schedules)),
		Total:     total,
	}
	for _, sch := range schedules {
		resp.Schedules = append(resp.Schedules, toScheduleResponse(sch))
	}
	writeJSON(w, http.StatusOK, resp)
}

// PreviewSchedule godoc
// @Summary Preview upcoming cron run times
// @Description Computes the next N run times for a cron expression starting from an optional reference time.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param expression query string true "Cron expression"
// @Param after query string false "Reference time in RFC3339"
// @Param timezone query string false "IANA timezone (default UTC)"
// @Param count query int false "Number of runs to compute (default 5, max 20)"
// @Success 200 {object} PreviewScheduleResponse
// @Failure 400 {object} ErrorResponse
// @Router /admin/schedules/preview [get]
func (h *Handler) PreviewSchedule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	expression := q.Get("expression")
	if expression == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "expression is required")
		return
	}
	if err := scheduler.ValidateCron(expression); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid cron expression: "+err.Error())
		return
	}
	timezone := q.Get("timezone")
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "timezone must be a valid IANA timezone")
		return
	}

	after := time.Now().UTC()
	if v := q.Get("after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid after parameter")
			return
		}
		after = t.UTC()
	}

	count := 5
	if v := q.Get("count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid count parameter")
			return
		}
		count = n
	}
	if count > 20 {
		count = 20
	}

	runs := make([]string, 0, count)
	ref := after
	for i := 0; i < count; i++ {
		next, err := scheduler.ComputeNextRunInLocation(expression, ref, timezone)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to compute run: "+err.Error())
			return
		}
		runs = append(runs, next.Format(time.RFC3339))
		ref = next
	}

	writeJSON(w, http.StatusOK, PreviewScheduleResponse{Expression: expression, Timezone: timezone, Runs: runs})
}

// GetSchedule godoc
// @Summary Get a schedule by ID (admin)
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Schedule ID"
// @Success 200 {object} ScheduleResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/schedules/{id} [get]
func (h *Handler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sch, err := h.store.GetScheduleByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "schedule not found")
			return
		}
		h.logger.Error("get schedule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get schedule")
		return
	}
	writeJSON(w, http.StatusOK, toScheduleResponse(sch))
}

// UpdateSchedule godoc
// @Summary Update a schedule (admin)
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Schedule ID"
// @Param request body UpdateScheduleRequest true "Schedule fields to update"
// @Success 200 {object} ScheduleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/schedules/{id} [patch]
func (h *Handler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req UpdateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}

	sch, err := h.store.GetScheduleByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "schedule not found")
			return
		}
		h.logger.Error("get schedule for update failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update schedule")
		return
	}

	if req.RuleVersion != nil {
		sch.RuleVersion = *req.RuleVersion
	}
	if req.RuleVersionNumber != nil {
		sch.RuleVersionNumber = *req.RuleVersionNumber
	}
	if req.Timezone != nil {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "timezone must be a valid IANA timezone")
			return
		}
		sch.Timezone = *req.Timezone
	}
	if req.BrowserProfileID != nil {
		sch.BrowserProfileID = *req.BrowserProfileID
	}
	if req.Name != nil {
		sch.Name = *req.Name
	}
	if req.Expression != nil {
		if sch.Type == models.ScheduleTypeCron {
			if err := scheduler.ValidateCron(*req.Expression); err != nil {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid cron expression: "+err.Error())
				return
			}
		} else {
			if _, err := time.Parse(time.RFC3339, *req.Expression); err != nil {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", "expression must be a valid RFC3339 timestamp for type once")
				return
			}
		}
		sch.Expression = *req.Expression
		// Recompute next run when expression changes.
		if sch.Type == models.ScheduleTypeOnce {
			t, _ := time.Parse(time.RFC3339, sch.Expression)
			sch.NextRunAt = sql.NullTime{Time: t.UTC(), Valid: true}
		} else {
			next, err := scheduler.ComputeNextRunInLocation(sch.Expression, time.Now().UTC(), sch.Timezone)
			if err != nil {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to compute next run: "+err.Error())
				return
			}
			sch.NextRunAt = sql.NullTime{Time: next, Valid: true}
		}
	}
	if req.Enabled != nil {
		sch.Enabled = *req.Enabled
	}
	if req.Variables != nil {
		sch.Variables = store.JSON(*req.Variables)
	}
	if req.Priority != nil {
		sch.Priority = models.Priority(*req.Priority)
	}
	if req.MaxRetries != nil {
		sch.MaxRetries = *req.MaxRetries
	}
	if req.Catchup != nil {
		catchup := models.CatchupMode(*req.Catchup)
		if catchup != models.CatchupSkip && catchup != models.CatchupRunOnce {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "catchup must be skip or run_once")
			return
		}
		sch.Catchup = catchup
	}
	if req.RuleVersion != nil || req.RuleVersionNumber != nil || req.Variables != nil || req.BrowserProfileID != nil {
		binding := &models.Task{RuleID: sch.RuleID, RuleVersion: sch.RuleVersion,
			RuleVersionNumber: sch.RuleVersionNumber, Variables: sch.Variables,
			BrowserProfileID: sch.BrowserProfileID}
		if err := h.store.BindTaskToRuleVersion(r.Context(), binding, sch.RuleVersionNumber); err != nil {
			strictVersion := req.RuleVersionNumber != nil || (req.RuleVersion != nil && string(sch.InputSchema) != "{}")
			if strictVersion || !errors.Is(err, store.ErrRuleVersionNotApproved) {
				if errors.Is(err, store.ErrRuleVersionNotApproved) || errors.Is(err, store.ErrRuleVersionNotFound) || errors.Is(err, rulecontract.ErrInvalidTaskInput) {
					writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
					return
				}
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update schedule")
				return
			}
		} else {
			sch.RuleVersion = binding.RuleVersion
			sch.RuleVersionNumber = binding.RuleVersionNumber
			sch.Variables = binding.Variables
			sch.InputSchema = binding.InputSchema
			sch.BrowserProfileID = binding.BrowserProfileID
		}
	}
	sch.UpdatedAt = time.Now().UTC()

	if err := h.store.UpdateSchedule(r.Context(), sch); err != nil {
		h.logger.Error("update schedule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update schedule")
		return
	}
	h.auditLog(r.Context(), "update_schedule", "schedule", sch.ID, map[string]any{"scheduleId": sch.ID})
	writeJSON(w, http.StatusOK, toScheduleResponse(sch))
}

func (h *Handler) strictVersionSelection(ctx context.Context, ruleID string, versionNumber int, versionLabel string) (bool, error) {
	if versionNumber > 0 {
		return true, nil
	}
	if versionLabel == "" {
		return false, nil
	}
	_, err := h.store.GetRuleVersionContract(ctx, ruleID, 1)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, store.ErrRuleVersionNotFound) {
		return false, nil
	}
	return false, err
}

// DeleteSchedule godoc
// @Summary Delete a schedule (admin)
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Schedule ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/schedules/{id} [delete]
func (h *Handler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteSchedule(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "schedule not found")
			return
		}
		h.logger.Error("delete schedule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete schedule")
		return
	}
	h.auditLog(r.Context(), "delete_schedule", "schedule", id, map[string]any{"scheduleId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// TriggerSchedule godoc
// @Summary Manually trigger a schedule (admin)
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Schedule ID"
// @Success 200 {object} TriggerScheduleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/schedules/{id}/trigger [post]
func (h *Handler) TriggerSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID, err := h.scheduler.TriggerSchedule(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "schedule not found")
			return
		}
		if errors.Is(err, scheduler.ErrRuleNotActive) {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		h.logger.Error("trigger schedule failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to trigger schedule")
		return
	}
	h.auditLog(r.Context(), "trigger_schedule", "schedule", id, map[string]any{"scheduleId": id, "taskId": taskID})
	writeJSON(w, http.StatusOK, TriggerScheduleResponse{TaskID: taskID})
}

// CancelTask godoc
// @Summary Cancel a task (admin)
// @Description Cancels a pending or leased task, or requests cancellation of a running task by setting cancel_requested. Returns 409 if the task is already terminal.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Task ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/tasks/{id}/cancel [post]
func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.CancelTask(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		if errors.Is(err, store.ErrTaskNotCancellable) {
			writeError(w, http.StatusConflict, "CONFLICT", "task cannot be cancelled in its current state")
			return
		}
		h.logger.Error("cancel task failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to cancel task")
		return
	}
	h.auditLog(r.Context(), "cancel_task", "task", id, map[string]any{"taskId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

// RetryTask godoc
// @Summary Retry a task (admin)
// @Description Transitions a failed, dead_letter, or cancelled task back to pending and resets retry bookkeeping.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Task ID"
// @Success 200 {object} SuccessResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/tasks/{id}/retry [post]
func (h *Handler) RetryTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.RetryTask(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		if errors.Is(err, store.ErrTaskNotRetryable) {
			writeError(w, http.StatusConflict, "CONFLICT", "task cannot be retried in its current state")
			return
		}
		h.logger.Error("retry task failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retry task")
		return
	}
	h.auditLog(r.Context(), "retry_task", "task", id, map[string]any{"taskId": id})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func rawToMap(raw JSON) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func marshalJSON(v any) models.JSON {
	b, _ := json.Marshal(v)
	return models.JSON(b)
}

func toRuleResponse(r *models.Rule) *RuleResponse {
	var domain any
	_ = json.Unmarshal(r.Domain, &domain)
	var urlPattern any
	_ = json.Unmarshal(r.URLPattern, &urlPattern)
	var steps any
	_ = json.Unmarshal(r.Steps, &steps)
	return &RuleResponse{
		ID:             r.ID,
		WorkspaceID:    r.WorkspaceID,
		Version:        r.Version,
		Name:           r.Name,
		Domain:         domain,
		URLPattern:     urlPattern,
		Enabled:        r.Enabled,
		Priority:       string(r.Priority),
		Entry:          r.Entry,
		Variables:      rawToMap(r.Variables),
		Selectors:      rawToMap(r.Selectors),
		Humanize:       rawToMap(r.Humanize),
		Steps:          steps,
		Output:         rawToMap(r.Output),
		SendPolicy:     rawToMap(r.SendPolicy),
		Hooks:          rawToMap(r.Hooks),
		Tags:           rawToMap(r.Tags),
		Owner:          r.Owner,
		ApprovalStatus: r.ApprovalStatus,
		Source:         r.Source,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func toTaskResponse(t *models.Task) *TaskResponse {
	resp := &TaskResponse{
		ID:                t.ID,
		WorkspaceID:       t.WorkspaceID,
		RuleID:            t.RuleID,
		RuleVersion:       t.RuleVersion,
		RuleVersionNumber: t.RuleVersionNumber,
		Status:            string(t.Status),
		Priority:          string(t.Priority),
		Variables:         rawToMap(t.Variables),
		InputSchema:       rawToMap(t.InputSchema),
		RetryCount:        t.RetryCount,
		MaxRetries:        t.MaxRetries,
		OutputSchema:      rawToMap(t.OutputSchema),
		SendPolicy:        rawToMap(t.SendPolicy),
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
		ErrorType:         t.ErrorType.String,
		ErrorMessage:      t.ErrorMessage.String,
		CancelRequested:   t.CancelRequested,
		BrowserProfileID:  t.BrowserProfileID,
	}
	if t.WorkerID.Valid {
		resp.WorkerID = t.WorkerID.String
	}
	if t.LeaseUntil.Valid {
		resp.LeaseUntil = &t.LeaseUntil.Time
	}
	if t.ScheduledAt.Valid {
		resp.ScheduledAt = &t.ScheduledAt.Time
	}
	if t.CompletedAt.Valid {
		resp.CompletedAt = &t.CompletedAt.Time
	}
	if t.ScheduleID.Valid {
		resp.ScheduleID = t.ScheduleID.String
	}
	if t.CurrentAttemptID.Valid {
		resp.CurrentAttemptID = t.CurrentAttemptID.String
	}
	return resp
}

func applyClaimSourceLineage(response *ClaimTaskResponse, contract *models.RuleVersionContract) {
	if response == nil || contract == nil {
		return
	}
	response.SourceKind = contract.SourceKind
	response.SourceAuthority = contract.SourceAuthority
	response.SourceArtifactHash = contract.SourceArtifactHash
	response.SourceExportHash = contract.SourceExportHash
	response.SourceWorkflowID = contract.SourceWorkflowID
}

func applyTaskSourceLineage(response *TaskResponse, contract *models.RuleVersionContract) {
	if response == nil || contract == nil {
		return
	}
	response.SourceKind = contract.SourceKind
	response.SourceAuthority = contract.SourceAuthority
	response.SourceArtifactHash = contract.SourceArtifactHash
	response.SourceExportHash = contract.SourceExportHash
	response.SourceWorkflowID = contract.SourceWorkflowID
}

func applyTaskResultsSourceLineage(response *TaskResultsResponse, contract *models.RuleVersionContract) {
	if response == nil || contract == nil {
		return
	}
	response.SourceKind = contract.SourceKind
	response.SourceAuthority = contract.SourceAuthority
	response.SourceArtifactHash = contract.SourceArtifactHash
	response.SourceExportHash = contract.SourceExportHash
	response.SourceWorkflowID = contract.SourceWorkflowID
}

func toScheduleResponse(s *models.Schedule) *ScheduleResponse {
	resp := &ScheduleResponse{
		ID:                s.ID,
		WorkspaceID:       s.WorkspaceID,
		RuleID:            s.RuleID,
		RuleVersion:       s.RuleVersion,
		RuleVersionNumber: s.RuleVersionNumber,
		Name:              s.Name,
		Type:              string(s.Type),
		Expression:        s.Expression,
		Enabled:           s.Enabled,
		Variables:         rawToMap(s.Variables),
		InputSchema:       rawToMap(s.InputSchema),
		BrowserProfileID:  s.BrowserProfileID,
		Timezone:          s.Timezone,
		Priority:          string(s.Priority),
		MaxRetries:        s.MaxRetries,
		Catchup:           string(s.Catchup),
		CreatedAt:         s.CreatedAt,
		UpdatedAt:         s.UpdatedAt,
	}
	if s.NextRunAt.Valid {
		resp.NextRunAt = &s.NextRunAt.Time
	}
	if s.LastRunAt.Valid {
		resp.LastRunAt = &s.LastRunAt.Time
	}
	return resp
}
