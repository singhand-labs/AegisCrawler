package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

const (
	defaultHumanInterventionTimeout = 2 * time.Minute
	maxHumanInterventionTimeout     = 24 * time.Hour
)

type HumanCheckpointRequest struct {
	StepID string `json:"stepId" example:"confirm-login"`
	URL    string `json:"url" example:"https://example.com/account"`
}

type CreateHumanInterventionRequest struct {
	WorkerID   string                 `json:"workerId" example:"worker-1"`
	AttemptID  string                 `json:"attemptId" example:"attempt-uuid"`
	Type       string                 `json:"type" example:"2fa"`
	Prompt     string                 `json:"prompt" example:"Complete two-factor authentication, then approve resume."`
	TimeoutMs  int64                  `json:"timeoutMs" example:"120000"`
	Checkpoint HumanCheckpointRequest `json:"checkpoint"`
}

type HumanInterventionResponse struct {
	Intervention *models.HumanIntervention `json:"intervention"`
}

type WorkerHumanIntervention struct {
	ID           string                         `json:"id"`
	TaskID       string                         `json:"taskId"`
	AttemptID    string                         `json:"attemptId"`
	CheckpointID string                         `json:"checkpointId"`
	Status       models.HumanInterventionStatus `json:"status"`
	ExpiresAt    time.Time                      `json:"expiresAt"`
}

type WorkerHumanInterventionResponse struct {
	Intervention *WorkerHumanIntervention `json:"intervention"`
}

type ListHumanInterventionsResponse struct {
	Interventions []*models.HumanIntervention `json:"interventions"`
}

type DecideHumanInterventionRequest struct {
	Decision     string `json:"decision" example:"approved"`
	CheckpointID string `json:"checkpointId" example:"checkpoint-uuid"`
	Note         string `json:"note,omitempty" example:"Operator completed the approved step."`
}

func humanRequestedAction(kind string) (string, bool) {
	switch kind {
	case "captcha":
		return "complete_captcha_then_resume", true
	case "2fa":
		return "complete_2fa_then_resume", true
	case "confirmation":
		return "confirm_then_resume", true
	case "generic":
		return "complete_manual_step_then_resume", true
	default:
		return "", false
	}
}

func targetOrigin(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", "", errors.New("checkpoint URL must be an HTTP(S) URL without credentials")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")), nil
}

func toWorkerHumanIntervention(item *models.HumanIntervention) *WorkerHumanIntervention {
	return &WorkerHumanIntervention{
		ID: item.ID, TaskID: item.TaskID, AttemptID: item.AttemptID,
		CheckpointID: item.CheckpointID, Status: item.Status, ExpiresAt: item.ExpiresAt,
	}
}

func hostnameAllowedByRule(rule *models.Rule, hostname string) bool {
	if rule == nil || hostname == "" {
		return false
	}
	var raw any
	if err := json.Unmarshal(rule.Domain, &raw); err != nil {
		return false
	}
	domains := []string{}
	switch value := raw.(type) {
	case string:
		domains = append(domains, value)
	case []any:
		for _, item := range value {
			if domain, ok := item.(string); ok {
				domains = append(domains, domain)
			}
		}
	}
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if hostname == domain || strings.HasSuffix(hostname, "."+domain) {
			return true
		}
	}
	return false
}

func (h *Handler) humanInterventionRule(r *http.Request, task *models.Task) (*models.Rule, error) {
	if task.RuleVersionNumber > 0 {
		version, err := h.store.GetRuleVersion(r.Context(), task.RuleID, task.RuleVersionNumber)
		if err == nil {
			return version.Rule, nil
		}
		if !errors.Is(err, store.ErrRuleVersionNotFound) {
			return nil, err
		}
	}
	// The legacy task API stores a synthetic numeric version even when the rule
	// predates immutable rule_versions. Only a missing version takes this
	// compatibility path; existing immutable versions always win above.
	return h.store.GetRuleByID(r.Context(), task.RuleID)
}

// CreateHumanIntervention godoc
// @Summary Pause an attempt for an operator decision
// @Description Atomically stores a sanitized checkpoint and moves the same task and attempt to waiting_for_human.
// @Tags tasks
// @Accept json
// @Produce json
// @Param id path string true "Task ID"
// @Param request body CreateHumanInterventionRequest true "Human intervention request"
// @Success 201 {object} WorkerHumanInterventionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /tasks/{id}/human-interventions [post]
func (h *Handler) CreateHumanIntervention(w http.ResponseWriter, r *http.Request) {
	var req CreateHumanInterventionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.AttemptID = strings.TrimSpace(req.AttemptID)
	req.Type = strings.TrimSpace(req.Type)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Checkpoint.StepID = strings.TrimSpace(req.Checkpoint.StepID)
	requestedAction, validType := humanRequestedAction(req.Type)
	if req.WorkerID == "" || req.AttemptID == "" || req.Checkpoint.StepID == "" || !validType {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "workerId, attemptId, checkpoint.stepId, and a valid type are required")
		return
	}
	if len(req.WorkerID) > 256 || len(req.AttemptID) > 256 || len(req.Checkpoint.StepID) > 256 || len(req.Checkpoint.URL) > 4096 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "workerId, attemptId, checkpoint.stepId, or checkpoint.url is too long")
		return
	}
	if len(req.Prompt) == 0 || len(req.Prompt) > 2000 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "prompt must contain 1 to 2000 bytes")
		return
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if req.TimeoutMs == 0 {
		timeout = defaultHumanInterventionTimeout
	}
	if timeout < time.Second || timeout > maxHumanInterventionTimeout {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "timeoutMs must be between 1000 and 86400000")
		return
	}
	origin, hostname, err := targetOrigin(req.Checkpoint.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	taskID := r.PathValue("id")
	task, err := h.store.GetTaskByID(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		h.logger.Error("get task for human intervention failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create human intervention")
		return
	}
	rule, err := h.humanInterventionRule(r, task)
	if err != nil {
		h.logger.Error("get immutable rule for human intervention failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create human intervention")
		return
	}
	if !hostnameAllowedByRule(rule, hostname) {
		writeError(w, http.StatusBadRequest, "DOMAIN_NOT_APPROVED", "checkpoint origin is outside the approved rule domains")
		return
	}
	now := time.Now().UTC()
	interventionID := store.NewID()
	checkpointID := store.NewID()
	checkpoint := store.JSON(map[string]any{
		"stepId": req.Checkpoint.StepID, "targetOrigin": origin,
		"ruleId": task.RuleID, "ruleVersionNumber": task.RuleVersionNumber,
		"attemptId": req.AttemptID, "capturedAt": now,
	})
	intervention, err := h.store.CreateHumanIntervention(r.Context(), store.HumanInterventionInput{
		ID: interventionID, TaskID: taskID, AttemptID: req.AttemptID,
		WorkerID: req.WorkerID, CheckpointID: checkpointID,
		CheckpointName: "human-intervention:" + interventionID, Checkpoint: checkpoint,
		Type: req.Type, Prompt: req.Prompt, RequestedAction: requestedAction,
		TargetOrigin: origin, ExpiresAt: now.Add(timeout), CreatedAt: now,
	})
	if err != nil {
		if errors.Is(err, store.ErrHumanInterventionConflict) || errors.Is(err, store.ErrHumanInterventionExpired) {
			writeError(w, http.StatusConflict, "INTERVENTION_CONFLICT", err.Error())
			return
		}
		h.logger.Error("create human intervention failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create human intervention")
		return
	}
	h.auditLog(r.Context(), "human_intervention_requested", "human_intervention", intervention.ID, map[string]any{
		"taskId": taskID, "attemptId": req.AttemptID, "checkpointId": checkpointID,
		"type": req.Type, "targetOrigin": origin, "expiresAt": intervention.ExpiresAt,
	})
	writeJSON(w, http.StatusCreated, WorkerHumanInterventionResponse{Intervention: toWorkerHumanIntervention(intervention)})
}

// GetHumanInterventionDecision godoc
// @Summary Poll an operator decision
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Param interventionId path string true "Human intervention ID"
// @Param workerId query string true "Worker ID"
// @Param attemptId query string true "Attempt ID"
// @Success 200 {object} WorkerHumanInterventionResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /tasks/{id}/human-interventions/{interventionId} [get]
func (h *Handler) GetHumanInterventionDecision(w http.ResponseWriter, r *http.Request) {
	intervention, err := h.store.GetHumanIntervention(r.Context(), r.PathValue("interventionId"))
	if err != nil {
		if errors.Is(err, store.ErrHumanInterventionNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "human intervention not found")
			return
		}
		h.logger.Error("get human intervention failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get human intervention")
		return
	}
	if intervention.TaskID != r.PathValue("id") || intervention.WorkerID != r.URL.Query().Get("workerId") || intervention.AttemptID != r.URL.Query().Get("attemptId") {
		writeError(w, http.StatusConflict, "INTERVENTION_CONFLICT", "human intervention is not active for this worker attempt")
		return
	}
	writeJSON(w, http.StatusOK, WorkerHumanInterventionResponse{Intervention: toWorkerHumanIntervention(intervention)})
}

// ListTaskHumanInterventions godoc
// @Summary List operator decisions for a task
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Task ID"
// @Success 200 {object} ListHumanInterventionsResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /admin/tasks/{id}/human-interventions [get]
func (h *Handler) ListTaskHumanInterventions(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListHumanInterventions(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		h.logger.Error("list human interventions failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list human interventions")
		return
	}
	writeJSON(w, http.StatusOK, ListHumanInterventionsResponse{Interventions: items})
}

// DecideHumanIntervention godoc
// @Summary Approve or reject a checkpoint-bound resume
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Task ID"
// @Param interventionId path string true "Human intervention ID"
// @Param request body DecideHumanInterventionRequest true "Decision"
// @Success 200 {object} HumanInterventionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /admin/tasks/{id}/human-interventions/{interventionId}/decision [post]
func (h *Handler) DecideHumanIntervention(w http.ResponseWriter, r *http.Request) {
	var req DecideHumanInterventionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || len(req.Note) > 2000 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid decision body")
		return
	}
	decision := models.HumanInterventionStatus(req.Decision)
	if decision != models.HumanInterventionApproved && decision != models.HumanInterventionRejected {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "decision must be approved or rejected")
		return
	}
	pending, err := h.store.GetHumanIntervention(r.Context(), r.PathValue("interventionId"))
	if err != nil {
		if errors.Is(err, store.ErrHumanInterventionNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "human intervention not found")
			return
		}
		h.logger.Error("get human intervention before decision failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to decide human intervention")
		return
	}
	if pending.TaskID != r.PathValue("id") {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "human intervention not found")
		return
	}
	actor := authz.Subject(r.Context(), "admin")
	intervention, err := h.store.DecideHumanIntervention(r.Context(), r.PathValue("interventionId"),
		req.CheckpointID, decision, strings.TrimSpace(req.Note), actor, h.cfg.LeaseDuration)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrHumanInterventionNotFound):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "human intervention not found")
		case errors.Is(err, store.ErrHumanCheckpointConflict):
			writeError(w, http.StatusConflict, "CHECKPOINT_CONFLICT", "checkpoint does not match the pending intervention")
		case errors.Is(err, store.ErrHumanInterventionExpired):
			writeError(w, http.StatusConflict, "INTERVENTION_EXPIRED", "human intervention has expired")
		case errors.Is(err, store.ErrHumanInterventionConflict):
			writeError(w, http.StatusConflict, "INTERVENTION_CONFLICT", "human intervention is no longer pending")
		default:
			h.logger.Error("decide human intervention failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to decide human intervention")
		}
		return
	}
	h.auditLog(r.Context(), "human_intervention_"+string(decision), "human_intervention", intervention.ID, map[string]any{
		"taskId": intervention.TaskID, "attemptId": intervention.AttemptID,
		"checkpointId": intervention.CheckpointID, "decision": decision,
	})
	if decision == models.HumanInterventionRejected && h.metrics != nil {
		h.metrics.IncTasksCompleted(string(models.TaskStatusFailed))
	}
	writeJSON(w, http.StatusOK, HumanInterventionResponse{Intervention: intervention})
}
