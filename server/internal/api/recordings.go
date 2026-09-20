package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	platformcrypto "github.com/singhand-labs/AegisCrawler/internal/crypto"
	platformrecording "github.com/singhand-labs/AegisCrawler/internal/recording"
	"github.com/singhand-labs/AegisCrawler/internal/store"
	"go.uber.org/zap"
)

func (h *Handler) recordingFeatureAvailable(w http.ResponseWriter) bool {
	if !h.cfg.RecordingV2Enabled {
		writeError(w, http.StatusNotFound, "FEATURE_DISABLED", "recording v2 is not enabled")
		return false
	}
	if h.recordings == nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "recording service is unavailable")
		return false
	}
	return true
}

func (h *Handler) CreateRecording(w http.ResponseWriter, r *http.Request) {
	if !h.recordingFeatureAvailable(w) {
		return
	}
	var request CreateRecordingRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil || request.Recording == nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "recording is required")
		return
	}
	input := platformrecording.CreateInput{Payload: request.Recording}
	if request.StartedAt != nil {
		input.StartedAt = request.StartedAt.UTC()
	}
	if request.EndedAt != nil {
		input.EndedAt = request.EndedAt.UTC()
	}
	recording, err := h.recordings.Create(r.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, platformrecording.ErrSnapshotsRequired), errors.Is(err, platformrecording.ErrInvalidTimeRange):
			writeError(w, http.StatusBadRequest, "INVALID_RECORDING", err.Error())
		case errors.Is(err, platformrecording.ErrActionLimitExceeded), errors.Is(err, platformrecording.ErrDurationLimitExceeded), errors.Is(err, store.ErrRecordingTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "RECORDING_LIMIT_EXCEEDED", err.Error())
		case errors.Is(err, platformcrypto.ErrEncryptionKeyRequired):
			writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_REQUIRED", "recording encryption is not configured")
		default:
			h.logger.Error("create recording failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to store recording")
		}
		return
	}
	h.auditLog(r.Context(), "create_recording", "recording", recording.ID, map[string]any{
		"actionCount": recording.ActionCount, "snapshotCount": recording.SnapshotCount,
	})
	writeJSON(w, http.StatusCreated, RecordingResponse{Recording: recording})
}

func (h *Handler) GetRecording(w http.ResponseWriter, r *http.Request) {
	if !h.recordingFeatureAvailable(w) {
		return
	}
	recording, err := h.recordings.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		switch {
		case errors.Is(err, store.ErrRecordingNotFound):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "recording not found")
		case errors.Is(err, store.ErrRecordingDeleted):
			writeError(w, http.StatusGone, "RECORDING_DELETED", "recording content has been deleted")
		default:
			h.logger.Error("get recording failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get recording")
		}
		return
	}
	writeJSON(w, http.StatusOK, RecordingResponse{Recording: recording})
}

func (h *Handler) DeleteRecording(w http.ResponseWriter, r *http.Request) {
	if !h.recordingFeatureAvailable(w) {
		return
	}
	id := r.PathValue("id")
	if err := h.recordings.Delete(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrRecordingNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "recording not found")
			return
		}
		h.logger.Error("delete recording failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete recording")
		return
	}
	h.auditLog(r.Context(), "delete_recording", "recording", id, map[string]any{"deletedAt": time.Now().UTC()})
	writeJSON(w, http.StatusOK, SuccessResponse{Success: true})
}
