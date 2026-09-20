package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func (h *Handler) workflowFeatureAvailable(w http.ResponseWriter) bool {
	if !h.cfg.WorkflowV2Enabled {
		writeError(w, http.StatusNotFound, "FEATURE_DISABLED", "workflow v2 is not enabled")
		return false
	}
	return true
}

func (h *Handler) rejectLegacyRuleMutationWhenVersioned(w http.ResponseWriter) bool {
	if !h.cfg.WorkflowV2Enabled {
		return false
	}
	writeError(w, http.StatusConflict, "VERSIONED_WORKFLOW_REQUIRED", "use the immutable rule version endpoints")
	return true
}

// CreateRuleVersion godoc
// @Summary Create an immutable rule version
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Param request body CreateRuleVersionRequest true "Immutable rule snapshot"
// @Success 201 {object} RuleVersionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /admin/rules/{id}/versions [post]
func (h *Handler) CreateRuleVersion(w http.ResponseWriter, r *http.Request) {
	if !h.workflowFeatureAvailable(w) {
		return
	}
	var request CreateRuleVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	request.Rule.ID = r.PathValue("id")
	version, err := h.store.CreateRuleVersion(r.Context(), &request.Rule, request.RecordingID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrRuleNotFound):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule not found")
		case errors.Is(err, store.ErrRecordingNotFound):
			writeError(w, http.StatusBadRequest, "INVALID_RECORDING", "recording not found")
		default:
			h.logger.Error("create rule version failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create rule version")
		}
		return
	}
	h.auditLog(r.Context(), "create_rule_version", "rule_version", version.RuleID, map[string]any{
		"version": version.Version, "recordingId": version.RecordingID,
	})
	contract, err := h.store.GetRuleVersionContract(r.Context(), version.RuleID, version.Version)
	if err != nil {
		h.logger.Error("get created rule version contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to read rule version contract")
		return
	}
	writeJSON(w, http.StatusCreated, RuleVersionResponse{RuleVersion: version, Contract: contract})
}

// ListRuleVersions godoc
// @Summary List immutable rule versions and execution contracts
// @Description Returns version metadata plus contracts keyed by rule ID and numeric version.
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Success 200 {object} ListRuleVersionsResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /admin/rules/{id}/versions [get]
func (h *Handler) ListRuleVersions(w http.ResponseWriter, r *http.Request) {
	if !h.workflowFeatureAvailable(w) {
		return
	}
	versions, err := h.store.ListRuleVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		h.logger.Error("list rule versions failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list rule versions")
		return
	}
	contracts := make([]*models.RuleVersionContract, 0, len(versions))
	for _, version := range versions {
		contract, contractErr := h.store.GetRuleVersionContract(r.Context(), version.RuleID, version.Version)
		if contractErr != nil {
			h.logger.Error("get listed rule version contract failed", zap.Error(contractErr))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to read rule version contracts")
			return
		}
		contracts = append(contracts, contract)
	}
	writeJSON(w, http.StatusOK, ListRuleVersionsResponse{RuleVersions: versions, Contracts: contracts})
}

// GetRuleVersion godoc
// @Summary Get an immutable rule version and execution contract
// @Tags admin
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Rule ID"
// @Param version path int true "Numeric version"
// @Success 200 {object} RuleVersionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /admin/rules/{id}/versions/{version} [get]
func (h *Handler) GetRuleVersion(w http.ResponseWriter, r *http.Request) {
	if !h.workflowFeatureAvailable(w) {
		return
	}
	versionNumber, ok := parseRuleVersionNumber(w, r)
	if !ok {
		return
	}
	version, err := h.store.GetRuleVersion(r.Context(), r.PathValue("id"), versionNumber)
	if err != nil {
		if errors.Is(err, store.ErrRuleVersionNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule version not found")
			return
		}
		h.logger.Error("get rule version failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get rule version")
		return
	}
	contract, err := h.store.GetRuleVersionContract(r.Context(), version.RuleID, version.Version)
	if err != nil {
		h.logger.Error("get rule version contract failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to read rule version contract")
		return
	}
	writeJSON(w, http.StatusOK, RuleVersionResponse{RuleVersion: version, Contract: contract})
}

func (h *Handler) ApproveRuleVersion(w http.ResponseWriter, r *http.Request) {
	h.transitionRuleVersion(w, r, true)
}

func (h *Handler) RejectRuleVersion(w http.ResponseWriter, r *http.Request) {
	h.transitionRuleVersion(w, r, false)
}

func (h *Handler) transitionRuleVersion(w http.ResponseWriter, r *http.Request, approve bool) {
	if !h.workflowFeatureAvailable(w) {
		return
	}
	versionNumber, ok := parseRuleVersionNumber(w, r)
	if !ok {
		return
	}
	var (
		version any
		err     error
	)
	if approve {
		version, err = h.store.ApproveRuleVersion(r.Context(), r.PathValue("id"), versionNumber)
	} else {
		version, err = h.store.RejectRuleVersion(r.Context(), r.PathValue("id"), versionNumber)
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrRuleVersionNotFound):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "rule version not found")
		case errors.Is(err, store.ErrRuleVersionState):
			writeError(w, http.StatusConflict, "INVALID_STATE", err.Error())
		default:
			h.logger.Error("transition rule version failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to transition rule version")
		}
		return
	}
	action := "reject_rule_version"
	if approve {
		action = "approve_rule_version"
	}
	h.auditLog(r.Context(), action, "rule_version", r.PathValue("id"), map[string]any{"version": versionNumber})
	writeJSON(w, http.StatusOK, map[string]any{"ruleVersion": version})
}

func parseRuleVersionNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "version must be a positive integer")
		return 0, false
	}
	return version, true
}
