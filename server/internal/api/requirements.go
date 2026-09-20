package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	llmrequirement "github.com/singhand-labs/AegisCrawler/internal/llm/requirement"
	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

type requirementManager interface {
	SubmitCandidates(context.Context, string) (*models.RequirementJob, error)
	SubmitNormalization(context.Context, string, llmrequirement.NormalizationInput) (*models.RequirementJob, error)
	GetJob(context.Context, string) (*models.RequirementJob, error)
	ListProviderAttempts(context.Context, string) ([]*models.LLMAttemptReport, error)
	GetProviderAttempt(context.Context, string, int) (*llmrequirement.RequirementAttemptDetail, error)
	GetProviderCall(context.Context, string, int, string) (*models.LLMProviderCall, error)
	RetryJob(context.Context, string) error
	GetRequirement(context.Context, string) (*models.CollectionRequirement, error)
	ConfirmRequirement(context.Context, string) (*models.CollectionRequirement, error)
}

func (h *Handler) requirementWorkflowAvailable(w http.ResponseWriter) bool {
	if !h.cfg.WorkflowV2Enabled {
		writeError(w, http.StatusNotFound, "FEATURE_DISABLED", "requirement workflow v2 is not enabled")
		return false
	}
	if h.requirements == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "requirement workflow is unavailable")
		return false
	}
	return true
}

// CreateCandidateRequirementJob godoc
// @Summary Generate collection requirement candidates
// @Description Starts a durable asynchronous job that generates exactly three structured requirement candidates from a persisted recording.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Recording ID"
// @Success 202 {object} RequirementJobResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/recordings/{id}/requirement-jobs [post]
func (h *Handler) CreateCandidateRequirementJob(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	recordingID := r.PathValue("id")
	job, err := h.requirements.SubmitCandidates(r.Context(), recordingID)
	if err != nil {
		h.writeRequirementSubmissionError(r.Context(), w, err, recordingID, nil)
		return
	}
	h.auditLog(r.Context(), "requirement_candidates_submitted", "requirement_job", job.ID, map[string]any{
		"recordingId": recordingID, "kind": job.Kind,
	})
	writeJSON(w, http.StatusAccepted, RequirementJobResponse{Job: job, StatusURL: "/api/v1/requirement-jobs/" + job.ID})
}

// CreateNormalizationRequirementJob godoc
// @Summary Normalize a collection requirement
// @Description Starts a durable asynchronous job that normalizes a selected, edited, custom-text, or manually structured requirement.
// @Tags requirements
// @Accept json
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Recording ID"
// @Param request body NormalizeRequirementRequest true "Requirement to normalize"
// @Success 202 {object} RequirementJobResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/recordings/{id}/requirement-jobs/normalize [post]
func (h *Handler) CreateNormalizationRequirementJob(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	var request NormalizeRequirementRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid normalization request")
		return
	}
	recordingID := r.PathValue("id")
	job, err := h.requirements.SubmitNormalization(r.Context(), recordingID, llmrequirement.NormalizationInput{
		Requirement: request.Requirement, CustomText: request.CustomText,
		CandidateJobID: request.CandidateJobID, CandidateID: request.CandidateID,
	})
	if err != nil {
		h.writeRequirementSubmissionError(r.Context(), w, err, recordingID, redactedPayload(&request))
		return
	}
	h.auditLog(r.Context(), "requirement_normalization_submitted", "requirement_job", job.ID, map[string]any{
		"recordingId": recordingID, "source": job.Source,
	})
	writeJSON(w, http.StatusAccepted, RequirementJobResponse{Job: job, StatusURL: "/api/v1/requirement-jobs/" + job.ID})
}

// GetRequirementJob godoc
// @Summary Get a collection requirement job
// @Description Returns durable status, provenance, safety findings, and the completed structured result when available.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Requirement job ID"
// @Success 200 {object} RequirementJobResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/requirement-jobs/{id} [get]
func (h *Handler) GetRequirementJob(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	job, err := h.requirements.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrRequirementJobNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "requirement job not found")
			return
		}
		h.logger.Error("get requirement job failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get requirement job")
		return
	}
	writeJSON(w, http.StatusOK, RequirementJobResponse{Job: job, StatusURL: "/api/v1/requirement-jobs/" + job.ID})
}

// ListRequirementProviderAttempts godoc
// @Summary List retained provider attempts for a requirement job
// @Description Returns workspace-scoped immutable attempt metadata without provider response content.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Requirement job ID"
// @Success 200 {object} RequirementProviderAttemptListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/requirement-jobs/{id}/provider-attempts [get]
func (h *Handler) ListRequirementProviderAttempts(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	jobID := r.PathValue("id")
	attempts, err := h.requirements.ListProviderAttempts(r.Context(), jobID)
	if err != nil {
		h.writeRequirementProviderArtifactError(w, err)
		return
	}
	h.auditLog(r.Context(), "requirement_provider_attempts_read", "requirement_job", jobID, map[string]any{
		"attemptCount": len(attempts),
	})
	writeJSON(w, http.StatusOK, RequirementProviderAttemptListResponse{Attempts: attempts})
}

// GetRequirementProviderAttempt godoc
// @Summary Get a retained provider attempt for a requirement job
// @Description Returns the bounded decrypted validation artifact and ordered provider-call metadata. Raw provider call content uses a separate endpoint.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Requirement job ID"
// @Param attempt path int true "Attempt number"
// @Success 200 {object} RequirementProviderAttemptResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/requirement-jobs/{id}/provider-attempts/{attempt} [get]
func (h *Handler) GetRequirementProviderAttempt(w http.ResponseWriter, r *http.Request) {
	setProviderArtifactNoStoreHeaders(w)
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	attemptNumber, err := strconv.Atoi(r.PathValue("attempt"))
	if err != nil || attemptNumber <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "attempt must be a positive integer")
		return
	}
	jobID := r.PathValue("id")
	detail, err := h.requirements.GetProviderAttempt(r.Context(), jobID, attemptNumber)
	if err != nil {
		h.writeRequirementProviderArtifactError(w, err)
		return
	}
	if err := h.auditLogRequired(r.Context(), "requirement_provider_attempt_read", "requirement_job", jobID, map[string]any{
		"attempt": attemptNumber, "attemptId": detail.Report.ID,
		"artifactHash": detail.Report.ArtifactHash,
	}); err != nil {
		h.logger.Error("audit requirement provider attempt read failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "provider attempt content could not be returned because auditing failed")
		return
	}
	writeJSON(w, http.StatusOK, RequirementProviderAttemptResponse{
		Attempt: detail.Report, Calls: detail.Calls, Artifact: detail.Artifact,
	})
}

// GetRequirementProviderCallContent godoc
// @Summary Get one retained provider response for a requirement job attempt
// @Description Returns one bounded, defensively redacted provider response artifact.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Requirement job ID"
// @Param attempt path int true "Attempt number"
// @Param callId path string true "Provider call ID"
// @Success 200 {object} RequirementProviderCallContentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/requirement-jobs/{id}/provider-attempts/{attempt}/calls/{callId}/content [get]
func (h *Handler) GetRequirementProviderCallContent(w http.ResponseWriter, r *http.Request) {
	setProviderArtifactNoStoreHeaders(w)
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	attemptNumber, err := strconv.Atoi(r.PathValue("attempt"))
	if err != nil || attemptNumber <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "attempt must be a positive integer")
		return
	}
	jobID := r.PathValue("id")
	call, err := h.requirements.GetProviderCall(r.Context(), jobID, attemptNumber, r.PathValue("callId"))
	if err != nil {
		h.writeRequirementProviderArtifactError(w, err)
		return
	}
	if err := h.auditLogRequired(r.Context(), "requirement_provider_call_content_read", "requirement_job", jobID, map[string]any{
		"attempt": attemptNumber, "callId": call.ID,
		"artifactHash": call.ArtifactHash, "responseHash": call.ResponseHash,
	}); err != nil {
		h.logger.Error("audit requirement provider call read failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "provider response content could not be returned because auditing failed")
		return
	}
	writeJSON(w, http.StatusOK, RequirementProviderCallContentResponse{Call: call, Content: call.Artifact})
}

func setProviderArtifactNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (h *Handler) writeRequirementProviderArtifactError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRequirementJobNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "requirement job not found")
	case errors.Is(err, store.ErrLLMAttemptReportNotFound), errors.Is(err, store.ErrLLMProviderCallNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "provider attempt artifact not found")
	default:
		h.logger.Error("get requirement provider artifact failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get provider attempt artifact")
	}
}

// RetryRequirementJob godoc
// @Summary Retry a failed collection requirement job
// @Description Requeues a failed durable job when its bounded attempt budget has not been exhausted.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Requirement job ID"
// @Success 202 {object} RequirementJobResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/requirement-jobs/{id}/retry [post]
func (h *Handler) RetryRequirementJob(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.requirements.RetryJob(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrRequirementJobNotFound):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "requirement job not found")
		case errors.Is(err, store.ErrRequirementJobState):
			writeError(w, http.StatusConflict, "INVALID_STATE", "only failed requirement jobs can be retried")
		default:
			h.logger.Error("retry requirement job failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retry requirement job")
		}
		return
	}
	h.auditLog(r.Context(), "requirement_job_retried", "requirement_job", id, map[string]any{})
	job, err := h.requirements.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get retried requirement job")
		return
	}
	writeJSON(w, http.StatusAccepted, RequirementJobResponse{Job: job, StatusURL: "/api/v1/requirement-jobs/" + job.ID})
}

// GetCollectionRequirement godoc
// @Summary Get a normalized collection requirement
// @Description Returns the workspace-scoped structured requirement and confirmation status used by the DSL generation workflow.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Collection requirement ID"
// @Success 200 {object} CollectionRequirementResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/requirements/{id} [get]
func (h *Handler) GetCollectionRequirement(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	requirement, err := h.requirements.GetRequirement(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeRequirementLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, CollectionRequirementResponse{Requirement: requirement})
}

// ConfirmCollectionRequirement godoc
// @Summary Confirm a normalized collection requirement
// @Description Immutably confirms the reviewed structured requirement before any provisional DSL can be generated.
// @Tags requirements
// @Produce json
// @Security AdminApiKey
// @Param id path string true "Collection requirement ID"
// @Success 200 {object} CollectionRequirementResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/requirements/{id}/confirm [post]
func (h *Handler) ConfirmCollectionRequirement(w http.ResponseWriter, r *http.Request) {
	if !h.requirementWorkflowAvailable(w) {
		return
	}
	id := r.PathValue("id")
	requirement, err := h.requirements.ConfirmRequirement(r.Context(), id)
	if err != nil {
		h.writeRequirementLookupError(w, err)
		return
	}
	h.auditLog(r.Context(), "requirement_confirmed", "collection_requirement", id, map[string]any{
		"recordingId": requirement.RecordingID, "contentHash": requirement.ContentHash,
	})
	writeJSON(w, http.StatusOK, CollectionRequirementResponse{Requirement: requirement})
}

func (h *Handler) writeRequirementSubmissionError(ctx context.Context, w http.ResponseWriter, err error, recordingID string, payload map[string]any) {
	switch {
	case errors.Is(err, store.ErrRecordingNotFound), errors.Is(err, store.ErrRecordingDeleted):
		writeError(w, http.StatusNotFound, "RECORDING_NOT_FOUND", "recording not found")
	case errors.Is(err, llmrequirement.ErrInvalidRequirement):
		writeError(w, http.StatusBadRequest, "INVALID_REQUIREMENT", err.Error())
	case errors.Is(err, llmrequirement.ErrUnsafeRequirement):
		// WI-15: audit-log the safety rejection with the redacted request payload.
		h.auditLog(ctx, "requirement_safety_rejected", "recording", recordingID, payload)
		writeError(w, http.StatusBadRequest, "UNSAFE_REQUIREMENT", "the requirement requests disallowed sensitive data")
	default:
		h.logger.Error("submit requirement job failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to submit requirement job")
	}
}

// redactedPayload converts a NormalizeRequirementRequest into an audit-safe
// map by running each field through the redact package. The requirement spec
// (an admin-authored structure) is passed through redact.Any to strip any
// credential-like strings; the free-form customText is redacted as a string.
func redactedPayload(req *NormalizeRequirementRequest) map[string]any {
	if req == nil {
		return nil
	}
	return map[string]any{
		"requirement":    redact.Any(req.Requirement),
		"customText":     redact.String(req.CustomText),
		"candidateJobId": req.CandidateJobID,
		"candidateId":    req.CandidateID,
	}
}

func (h *Handler) writeRequirementLookupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRequirementNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "collection requirement not found")
	case errors.Is(err, store.ErrRequirementState):
		writeError(w, http.StatusConflict, "INVALID_STATE", "collection requirement is already confirmed")
	default:
		h.logger.Error("collection requirement operation failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "collection requirement operation failed")
	}
}
