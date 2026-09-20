package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	llmdsl "github.com/singhand-labs/AegisCrawler/internal/llm/dsl"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type dslWorkflowManager interface {
	Submit(context.Context, string, string, *models.Rule) (*models.DSLWorkflow, *models.DSLJob, error)
	AdoptAdminReviewedAttempt(context.Context, string, string, llmdsl.AdminReviewedDSLAttemptExport) (*models.DSLWorkflow, error)
	GetWorkflow(context.Context, string) (*models.DSLWorkflow, error)
	GetJob(context.Context, string) (*models.DSLJob, error)
	ListProviderAttempts(context.Context, string) ([]*models.LLMAttemptReport, error)
	GetProviderAttempt(context.Context, string, int) (*llmdsl.DSLAttemptDetail, error)
	GetProviderCall(context.Context, string, int, string) (*models.LLMProviderCall, error)
	GetReplay(context.Context, string) (*models.ReplayAttempt, error)
	StartReplay(context.Context, string) (*models.ReplayAttempt, error)
	Correct(context.Context, string, *models.Rule) (*models.DSLWorkflow, error)
	CompleteReplay(context.Context, string, string, llmdsl.ReplayCompletionInput) (*models.ReplayAttempt, *models.DSLJob, error)
	Confirm(context.Context, string, store.ApproveDSLWorkflowOptions) (*models.RuleVersion, *models.RuleVersionContract, error)
	GetDSLApprovalProvenance(context.Context, string) (*store.DSLApprovalProvenance, error)
}

// AdoptDSLAttemptExport godoc
// @Summary Adopt an admin-reviewed exported DSL attempt for replay
// @Description Revalidates a bounded provider-response-free attempt export against the current confirmed requirement and restored recording. The resulting non-authoritative workflow has no DSL job or provider call and still requires replay and explicit approval.
// @Tags dsl-workflows
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Confirmed requirement ID"
// @Param request body AdoptDSLAttemptExportRequest true "Reviewed attempt export and browser profile"
// @Success 201 {object} DSLWorkflowResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/requirements/{id}/dsl-workflows/adopt-attempt-export [post]
func (h *Handler) AdoptDSLAttemptExport(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(llmdsl.MaxAdminReviewedAttemptExportBytes))
	var request AdoptDSLAttemptExportRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "attempt export exceeds the safety limit")
			return
		}
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid attempt export")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid attempt export")
		return
	}
	requirementID := r.PathValue("id")
	if err := h.auditLogRequired(r.Context(), "dsl_attempt_export_adoption_reviewed", "collection_requirement", requirementID, map[string]any{
		"sourceKind":      models.DSLWorkflowSourceAdminReviewedAttemptExport,
		"sourceAuthority": models.DSLWorkflowSourceAuthorityNonAuthoritative,
		"claimedArtifactHash": func() string {
			if request.Export.Attempt != nil && len(request.Export.Attempt.ArtifactHash) == 64 {
				return request.Export.Attempt.ArtifactHash
			}
			return ""
		}(),
	}); err != nil {
		h.logger.Error("audit dsl attempt adoption failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "attempt export could not be adopted because auditing failed")
		return
	}
	workflow, err := h.dslWorkflows.AdoptAdminReviewedAttempt(
		r.Context(), requirementID, request.BrowserProfileID, request.Export,
	)
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, DSLWorkflowResponse{
		Workflow: workflow, StatusURL: "/api/v1/dsl-workflows/" + workflow.ID,
	})
}

func (h *Handler) dslWorkflowAvailable(w http.ResponseWriter) bool {
	if !h.cfg.WorkflowV2Enabled {
		writeError(w, http.StatusNotFound, "FEATURE_DISABLED", "dsl workflow v2 is not enabled")
		return false
	}
	if h.dslWorkflows == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "dsl workflow is unavailable")
		return false
	}
	return true
}

// CreateDSLWorkflow godoc
// @Summary Generate a provisional DSL from a confirmed requirement
// @Description Starts a durable asynchronous generation job. The named browser profile identifies the current Chrome profile used for fresh-tab replay; credential contents are never submitted.
// @Tags dsl-workflows
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Confirmed requirement ID"
// @Param request body CreateDSLWorkflowRequest true "Recording-derived baseline rule and browser profile reference"
// @Success 202 {object} DSLWorkflowResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/requirements/{id}/dsl-workflows [post]
func (h *Handler) CreateDSLWorkflow(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	var request CreateDSLWorkflowRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.BaselineRule == nil || request.BrowserProfileID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "browserProfileId and baselineRule are required")
		return
	}
	workflow, job, err := h.dslWorkflows.Submit(r.Context(), r.PathValue("id"), request.BrowserProfileID, request.BaselineRule)
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	h.auditLog(r.Context(), "dsl_workflow_submitted", "dsl_workflow", workflow.ID, map[string]any{
		"requirementId": workflow.RequirementID, "recordingId": workflow.RecordingID,
		"browserProfileId": workflow.BrowserProfileID, "jobId": job.ID,
	})
	writeJSON(w, http.StatusAccepted, DSLWorkflowResponse{Workflow: workflow, Job: job, StatusURL: "/api/v1/dsl-workflows/" + workflow.ID})
}

// GetDSLWorkflow godoc
// @Summary Get a durable DSL workflow
// @Description Returns generation, repair, replay, and approval state. The provisional rule is returned only within the authenticated workspace.
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL workflow ID"
// @Success 200 {object} DSLWorkflowResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-workflows/{id} [get]
func (h *Handler) GetDSLWorkflow(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	workflow, err := h.dslWorkflows.GetWorkflow(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DSLWorkflowResponse{Workflow: workflow, StatusURL: "/api/v1/dsl-workflows/" + workflow.ID})
}

// GetDSLJob godoc
// @Summary Get a durable DSL generation or repair job
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL job ID"
// @Success 200 {object} DSLJobResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-jobs/{id} [get]
func (h *Handler) GetDSLJob(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	job, err := h.dslWorkflows.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DSLJobResponse{Job: job, StatusURL: "/api/v1/dsl-jobs/" + job.ID})
}

// ListDSLProviderAttempts godoc
// @Summary List retained provider attempts for a DSL job
// @Description Returns workspace-scoped immutable attempt metadata without provider response content.
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL job ID"
// @Success 200 {object} DSLProviderAttemptListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/dsl-jobs/{id}/provider-attempts [get]
func (h *Handler) ListDSLProviderAttempts(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	jobID := r.PathValue("id")
	attempts, err := h.dslWorkflows.ListProviderAttempts(r.Context(), jobID)
	if err != nil {
		h.writeDSLProviderArtifactError(w, err)
		return
	}
	h.auditLog(r.Context(), "dsl_provider_attempts_read", "dsl_job", jobID, map[string]any{
		"attemptCount": len(attempts),
	})
	writeJSON(w, http.StatusOK, DSLProviderAttemptListResponse{Attempts: attempts})
}

// GetDSLProviderAttempt godoc
// @Summary Get a retained provider attempt for a DSL job
// @Description Returns the bounded decrypted provider IR, resolved rule, validation report, and ordered call metadata.
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL job ID"
// @Param attempt path int true "Attempt number"
// @Success 200 {object} DSLProviderAttemptResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/dsl-jobs/{id}/provider-attempts/{attempt} [get]
func (h *Handler) GetDSLProviderAttempt(w http.ResponseWriter, r *http.Request) {
	setProviderArtifactNoStoreHeaders(w)
	if !h.dslWorkflowAvailable(w) {
		return
	}
	attemptNumber, err := strconv.Atoi(r.PathValue("attempt"))
	if err != nil || attemptNumber <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "attempt must be a positive integer")
		return
	}
	jobID := r.PathValue("id")
	detail, err := h.dslWorkflows.GetProviderAttempt(r.Context(), jobID, attemptNumber)
	if err != nil {
		h.writeDSLProviderArtifactError(w, err)
		return
	}
	if err := h.auditLogRequired(r.Context(), "dsl_provider_attempt_read", "dsl_job", jobID, map[string]any{
		"attempt": attemptNumber, "attemptId": detail.Report.ID,
		"artifactHash": detail.Report.ArtifactHash,
	}); err != nil {
		h.logger.Error("audit dsl provider attempt read failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "provider attempt content could not be returned because auditing failed")
		return
	}
	writeJSON(w, http.StatusOK, DSLProviderAttemptResponse{
		Attempt: detail.Report, Calls: detail.Calls, Artifact: detail.Artifact,
	})
}

// GetDSLProviderCallContent godoc
// @Summary Get one retained provider response for a DSL job attempt
// @Description Returns one bounded, defensively redacted provider response artifact.
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL job ID"
// @Param attempt path int true "Attempt number"
// @Param callId path string true "Provider call ID"
// @Success 200 {object} DSLProviderCallContentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/dsl-jobs/{id}/provider-attempts/{attempt}/calls/{callId}/content [get]
func (h *Handler) GetDSLProviderCallContent(w http.ResponseWriter, r *http.Request) {
	setProviderArtifactNoStoreHeaders(w)
	if !h.dslWorkflowAvailable(w) {
		return
	}
	attemptNumber, err := strconv.Atoi(r.PathValue("attempt"))
	if err != nil || attemptNumber <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "attempt must be a positive integer")
		return
	}
	jobID := r.PathValue("id")
	call, err := h.dslWorkflows.GetProviderCall(r.Context(), jobID, attemptNumber, r.PathValue("callId"))
	if err != nil {
		h.writeDSLProviderArtifactError(w, err)
		return
	}
	if err := h.auditLogRequired(r.Context(), "dsl_provider_call_content_read", "dsl_job", jobID, map[string]any{
		"attempt": attemptNumber, "callId": call.ID,
		"artifactHash": call.ArtifactHash, "responseHash": call.ResponseHash,
	}); err != nil {
		h.logger.Error("audit dsl provider call read failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "provider response content could not be returned because auditing failed")
		return
	}
	writeJSON(w, http.StatusOK, DSLProviderCallContentResponse{Call: call, Content: call.Artifact})
}

func (h *Handler) writeDSLProviderArtifactError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDSLJobNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "dsl job not found")
	case errors.Is(err, store.ErrLLMAttemptReportNotFound), errors.Is(err, store.ErrLLMProviderCallNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "provider attempt artifact not found")
	default:
		h.logger.Error("get dsl provider artifact failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get provider attempt artifact")
	}
}

// CorrectDSLWorkflow godoc
// @Summary Replace a provisional DSL with a human-authored correction
// @Description Accepts only failed or awaiting-replay workflows. The complete corrected rule must preserve recording identity and pass schema, output-contract, replay-safety, and security validation. A successful correction must still complete replay and explicit confirmation before approval.
// @Tags dsl-workflows
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL workflow ID"
// @Param request body CorrectDSLWorkflowRequest true "Complete corrected provisional rule"
// @Success 200 {object} DSLWorkflowResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-workflows/{id}/provisional [put]
func (h *Handler) CorrectDSLWorkflow(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	var request CorrectDSLWorkflowRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Rule == nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "complete corrected rule is required")
		return
	}
	workflow, err := h.dslWorkflows.Correct(r.Context(), r.PathValue("id"), request.Rule)
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	h.auditLog(r.Context(), "dsl_workflow_corrected", "dsl_workflow", workflow.ID, map[string]any{
		"provisionalHash": workflow.ProvisionalHash, "repairCount": workflow.RepairCount,
	})
	writeJSON(w, http.StatusOK, DSLWorkflowResponse{Workflow: workflow, StatusURL: "/api/v1/dsl-workflows/" + workflow.ID})
}

// StartDSLReplay godoc
// @Summary Start a full fresh-tab replay
// @Description Creates a replay attempt bound to the current provisional rule hash and selected browser profile.
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL workflow ID"
// @Success 201 {object} DSLReplayResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-workflows/{id}/replays [post]
func (h *Handler) StartDSLReplay(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	attempt, err := h.dslWorkflows.StartReplay(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	h.auditLog(r.Context(), "dsl_replay_started", "dsl_replay", attempt.ID, map[string]any{
		"workflowId": attempt.WorkflowID, "sequence": attempt.Sequence,
		"ruleHash": attempt.RuleHash, "browserProfileId": attempt.BrowserProfileID,
	})
	writeJSON(w, http.StatusCreated, DSLReplayResponse{Replay: attempt})
}

// GetDSLReplay godoc
// @Summary Get a sanitized DSL replay attempt
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Replay attempt ID"
// @Success 200 {object} DSLReplayResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-replays/{id} [get]
func (h *Handler) GetDSLReplay(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	attempt, err := h.dslWorkflows.GetReplay(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DSLReplayResponse{Replay: attempt})
}

// CompleteDSLReplay godoc
// @Summary Submit a complete replay outcome
// @Description Stores bounded sanitized diagnostics and validates collected output. Failures schedule at most three durable repair jobs.
// @Tags dsl-workflows
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL workflow ID"
// @Param replayId path string true "Replay attempt ID"
// @Param request body dsl.ReplayCompletionInput true "Sanitized replay outcome"
// @Success 200 {object} DSLReplayResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-workflows/{id}/replays/{replayId}/complete [post]
func (h *Handler) CompleteDSLReplay(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	var input llmdsl.ReplayCompletionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid replay outcome")
		return
	}
	attempt, repairJob, err := h.dslWorkflows.CompleteReplay(r.Context(), r.PathValue("id"), r.PathValue("replayId"), input)
	if err != nil {
		if errors.Is(err, llmdsl.ErrReplayOutputRequired) {
			writeError(w, http.StatusConflict, "REPLAY_OUTPUT_REQUIRED", err.Error())
			return
		}
		h.writeDSLWorkflowError(w, err)
		return
	}
	h.auditLog(r.Context(), "dsl_replay_completed", "dsl_replay", attempt.ID, map[string]any{
		"workflowId": attempt.WorkflowID, "sequence": attempt.Sequence,
		"status": attempt.Status, "outputValid": attempt.OutputValid,
		"repairJobId": func() string {
			if repairJob != nil {
				return repairJob.ID
			}
			return ""
		}(),
	})
	writeJSON(w, http.StatusOK, DSLReplayResponse{Replay: attempt, RepairJob: repairJob})
}

// ConfirmDSLWorkflow godoc
// @Summary Confirm a successful replay and approve an immutable rule version
// @Description Approval is accepted only for the current provisional hash after a complete successful schema-valid replay. Pass ?override_safety=true to override blocking safety flags (requires admin principal).
// @Tags dsl-workflows
// @Produce json
// @Security AdminApiKey
// @Param id path string true "DSL workflow ID"
// @Param override_safety query bool false "Override blocking safety flags (admin-only)"
// @Success 200 {object} RuleVersionResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/dsl-workflows/{id}/confirm [post]
func (h *Handler) ConfirmDSLWorkflow(w http.ResponseWriter, r *http.Request) {
	if !h.dslWorkflowAvailable(w) {
		return
	}
	opts := store.ApproveDSLWorkflowOptions{}
	if r.URL.Query().Has("override_safety") {
		principal, ok := authz.PrincipalFromContext(r.Context())
		if !ok || !principal.HasRole(authz.RoleAdmin) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "safety override requires admin principal")
			return
		}
		opts.OverrideSafety = true
		opts.OverrideActor = principal.Subject
	}
	version, contract, err := h.dslWorkflows.Confirm(r.Context(), r.PathValue("id"), opts)
	if err != nil {
		h.writeDSLWorkflowError(w, err)
		return
	}
	resp := RuleVersionResponse{RuleVersion: version, Contract: contract}
	if version != nil && version.DSLWorkflowID != "" {
		prov, err := h.dslWorkflows.GetDSLApprovalProvenance(r.Context(), r.PathValue("id"))
		if err != nil {
			h.logger.Warn("failed to read dsl approval provenance", zap.Error(err))
		} else if prov != nil {
			resp.Provenance = &ProvenanceBlock{
				DSLWorkflowID:  version.DSLWorkflowID,
				DSLJobID:       prov.DSLJobID,
				ProviderCallID: prov.ProviderCallID,
				PromptHash:     prov.PromptHash,
				ModelID:        prov.ModelID,
				CacheHit:       prov.CacheHit,
				SafetyFlags:    prov.SafetyFlags,
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) writeDSLWorkflowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDSLWorkflowNotFound), errors.Is(err, store.ErrDSLJobNotFound), errors.Is(err, store.ErrReplayNotFound), errors.Is(err, store.ErrRequirementNotFound), errors.Is(err, store.ErrRecordingNotFound), errors.Is(err, store.ErrRecordingDeleted):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "dsl workflow resource not found")
	case errors.Is(err, store.ErrDSLWorkflowState), errors.Is(err, store.ErrDSLJobState), errors.Is(err, store.ErrReplayState), errors.Is(err, store.ErrRequirementState):
		writeError(w, http.StatusConflict, "INVALID_STATE", "dsl workflow cannot transition from its current state")
	case errors.Is(err, store.ErrSafetyBlockingFlag):
		blockingFlags := []string{}
		var flagErr *store.SafetyBlockingFlagError
		if errors.As(err, &flagErr) {
			blockingFlags = flagErr.Flags
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          "dsl workflow has blocking safety flags requiring override",
			"code":           "SAFETY_BLOCKING_FLAG",
			"blocking_flags": blockingFlags,
		})
	case errors.Is(err, llmdsl.ErrInvalidWorkflowInput):
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "baseline rule or workflow input failed validation",
			Code:    "INVALID_DSL",
			Details: llmdsl.WorkflowInputPhase(err),
		})
	case errors.Is(err, llmdsl.ErrReplayPayloadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "replay diagnostic payload exceeds the safety limit")
	default:
		h.logger.Error("dsl workflow operation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "dsl workflow operation failed")
	}
}
