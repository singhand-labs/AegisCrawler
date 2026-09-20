package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/prompt"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/rule"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

// EnhanceRule godoc
// @Summary Enhance a rule with LLM (admin)
// @Description Takes a recording and baseline rule, enqueues an asynchronous LLM enhancement job, and returns a job id for polling.
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param request body EnhanceRuleRequest true "Recording and baseline"
// @Success 202 {object} EnhanceRuleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/enhance [post]
func (h *Handler) EnhanceRule(w http.ResponseWriter, r *http.Request) {
	var req EnhanceRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}

	recording := map[string]any(req.Recording)
	baseline := map[string]any(req.BaselineRule)
	if baseline["id"] == nil || baseline["id"] == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "baselineRule.id is required")
		return
	}
	// H-3: ensure id is a string before any code path performs an unchecked
	// type assertion (e.g., auditLog payload construction). A non-string id
	// previously passed the nil/"" check and then either panicked at
	// baseline["id"].(string) or surfaced as a confusing 500 from Submit.
	ruleID, ok := baseline["id"].(string)
	if !ok {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "baselineRule.id must be a string")
		return
	}

	if h.llmJobManager == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "llm job manager not configured")
		return
	}

	if err := rule.Validate(baseline); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	jobID, err := h.llmJobManager.Submit(r.Context(), llm.EnhanceRequest{
		Recording:    recording,
		BaselineRule: baseline,
		UserHint:     req.UserHint,
	})
	if err != nil {
		h.logger.Error("submit enhance job failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to submit enhancement job")
		return
	}

	auditPayload := map[string]any{
		"ruleId": ruleID,
		"jobId":  jobID,
	}
	if h.cfg.LLMAuditPrompts {
		if sys, user, err := prompt.Build(recording, baseline, req.UserHint); err == nil {
			hash, prefix := prompt.AuditPreview(sys, user, h.cfg.LLMModel)
			auditPayload["promptHash"] = hash
			if h.cfg.EnforcedLLMPolicy() == nil {
				auditPayload["promptPrefix"] = prefix
			}
		}
	}

	h.auditLog(r.Context(), "rule_enhance_submitted", "rule", ruleID, auditPayload)

	writeJSON(w, http.StatusAccepted, EnhanceRuleResponse{
		JobID:     jobID,
		StatusURL: "/admin/rules/enhancements/jobs/" + jobID,
	})
}

// GetLLMJob godoc
// @Summary Get the status of an LLM enhancement job
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Job ID"
// @Success 200 {object} models.LLMJob
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/enhancements/jobs/{id} [get]
func (h *Handler) GetLLMJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := h.store.GetLLMJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "job not found")
			return
		}
		h.logger.Error("get llm job failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get job")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// GetRuleEnhancement godoc
// @Summary Get the latest enhancement for a rule
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} RuleEnhancementResponse
// @Router /admin/rules/{id}/enhancement [get]
func (h *Handler) GetRuleEnhancement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := h.store.GetRuleEnhancementByRuleID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrRuleNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "enhancement not found")
			return
		}
		h.logger.Error("get enhancement failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get enhancement")
		return
	}
	writeJSON(w, http.StatusOK, RuleEnhancementResponse{RuleEnhancement: e})
}

// AcceptEnhancement godoc
// @Summary Accept an LLM enhancement
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} RuleResponse
// @Router /admin/rules/{id}/enhancement/accept [post]
func (h *Handler) AcceptEnhancement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := h.store.GetRuleEnhancementByRuleID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "enhancement not found")
		return
	}
	if e.Status != string(models.EnhancementStatusPending) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "enhancement is not pending")
		return
	}

	rule, err := h.store.GetRuleByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
		return
	}

	enhancedMap := rawToMap(models.JSON(e.Enhanced))
	createdAt := rule.CreatedAt
	rule = ruleFromMap(enhancedMap)
	rule.ID = id
	rule.CreatedAt = createdAt
	rule.ApprovalStatus = string(models.RuleApprovalApproved)
	rule.Enabled = true
	rule.UpdatedAt = time.Now().UTC()

	if err := h.store.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.store.CreateRuleTx(r.Context(), tx, rule); err != nil {
			return err
		}
		return h.store.UpdateRuleEnhancementStatusTx(r.Context(), tx, e.ID, string(models.EnhancementStatusApproved))
	}); err != nil {
		h.logger.Error("accept enhancement failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to accept enhancement")
		return
	}

	h.auditLog(r.Context(), "enhancement_accepted", "rule", id, map[string]any{"enhancementId": e.ID})
	writeJSON(w, http.StatusOK, toRuleResponse(rule))
}

// RejectEnhancement godoc
// @Summary Reject an LLM enhancement
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} SuccessResponse
// @Router /admin/rules/{id}/enhancement/reject [post]
func (h *Handler) RejectEnhancement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := h.store.GetRuleEnhancementByRuleID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "enhancement not found")
		return
	}
	if e.Status != string(models.EnhancementStatusPending) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "enhancement is not pending")
		return
	}

	if err := h.store.WithTx(r.Context(), func(tx *sql.Tx) error {
		if err := h.store.DeleteRuleTx(r.Context(), tx, id); err != nil {
			return err
		}
		return h.store.UpdateRuleEnhancementStatusTx(r.Context(), tx, e.ID, string(models.EnhancementStatusRejected))
	}); err != nil {
		h.logger.Error("reject enhancement failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to reject enhancement")
		return
	}

	h.auditLog(r.Context(), "enhancement_rejected", "rule", id, map[string]any{"enhancementId": e.ID})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}

func ruleFromMap(m map[string]any) *models.Rule {
	return &models.Rule{
		ID:             getString(m, "id"),
		Version:        getString(m, "version"),
		Name:           getString(m, "name"),
		Domain:         marshalJSON(m["domain"]),
		URLPattern:     marshalJSON(m["urlPattern"]),
		Enabled:        getBool(m, "enabled"),
		Priority:       models.Priority(getString(m, "priority")),
		Entry:          getString(m, "entry"),
		Variables:      marshalJSON(m["variables"]),
		Selectors:      marshalJSON(m["selectors"]),
		Humanize:       marshalJSON(m["humanize"]),
		Steps:          marshalJSON(m["steps"]),
		Output:         marshalJSON(m["output"]),
		SendPolicy:     marshalJSON(m["sendPolicy"]),
		Hooks:          marshalJSON(m["hooks"]),
		Tags:           marshalJSON(m["tags"]),
		Owner:          getString(m, "owner"),
		ApprovalStatus: getString(m, "approvalStatus"),
	}
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
