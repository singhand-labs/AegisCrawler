package api

import (
	"encoding/json"
	"net/http"

	"github.com/singhand-labs/AegisCrawler/internal/llm/budget"
	"github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	"go.uber.org/zap"
)

// GenerateFromIntentRequest submits a recording, baseline rule, and intent hint
// for server-side DSL generation.
type GenerateFromIntentRequest struct {
	Recording         map[string]any    `json:"recording" swaggertype:"object"`
	BaselineRule      *models.Rule      `json:"baselineRule" swaggertype:"object"`
	Intent            *intent.Candidate `json:"intent,omitempty" swaggertype:"object"`
	CustomDescription string            `json:"customDescription,omitempty"`
}

// GenerateFromIntentResponse returns the generated rule and its YAML form.
type GenerateFromIntentResponse struct {
	Rule *models.Rule `json:"rule" swaggertype:"object"`
	YAML string       `json:"yaml"`
}

// SetDSLGenerator injects the DSL generator used by GenerateFromIntent.
func (h *Handler) SetDSLGenerator(g *dsl.Generator) {
	h.dslGenerator = g
}

// GenerateFromIntent godoc
// @Summary Generate a rule from intent (admin)
// @Description Takes a recording, baseline rule, and intent hint, then returns an enhanced rule and its YAML representation.
// @Tags admin
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param request body GenerateFromIntentRequest true "Recording, baseline rule, and intent"
// @Success 200 {object} GenerateFromIntentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse "Unauthorized"
// @Failure 429 {object} ErrorResponse "LLM hard budget exceeded"
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse "LLM provider or budget ledger unavailable"
// @Router /admin/rules/generate-from-intent [post]
func (h *Handler) GenerateFromIntent(w http.ResponseWriter, r *http.Request) {
	var req GenerateFromIntentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.BaselineRule == nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "baselineRule is required")
		return
	}
	// Expand reference/delta snapshots from the extension's compressed form
	// before generation resolves element indexes.
	if err := platformrecording.ExpandSnapshotReferences(req.Recording); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_RECORDING", err.Error())
		return
	}
	if h.dslGenerator == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "dsl generator not configured")
		return
	}

	ctx := withServerOwnedDispatch(r.Context(), budget.OperationDSL)
	rule, err := h.dslGenerator.Generate(ctx, req.Recording, req.BaselineRule, req.Intent, req.CustomDescription)
	if err != nil {
		h.logger.Error("generate from intent failed", zap.Error(err))
		if writeLLMDispatchError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate rule")
		return
	}

	yaml, err := dsl.RuleToYAML(rule)
	if err != nil {
		h.logger.Error("serialize generated rule to yaml failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to serialize rule")
		return
	}

	writeJSON(w, http.StatusOK, GenerateFromIntentResponse{Rule: rule, YAML: yaml})
}
