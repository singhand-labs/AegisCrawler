package api

import (
	"net/http"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
)

// Capabilities reports additive protocols that this server can accept. It is
// deliberately unauthenticated and contains no deployment secrets.
func (h *Handler) Capabilities(w http.ResponseWriter, _ *http.Request) {
	workerProtocols := []string{"v1"}
	if h.cfg.WorkerProtocolV2Enabled {
		workerProtocols = append(workerProtocols, "v2")
	}

	maxActions := h.cfg.RecordingMaxActions
	if maxActions <= 0 {
		maxActions = 500
	}
	maxDuration := h.cfg.RecordingMaxDuration
	if maxDuration <= 0 {
		maxDuration = 2 * time.Hour
	}
	maxCompressedBytes := h.cfg.RecordingMaxCompressedBytes
	if maxCompressedBytes <= 0 {
		maxCompressedBytes = 25 * 1024 * 1024
	}

	writeJSON(w, http.StatusOK, CapabilitiesResponse{
		APIVersion:             "v1",
		WorkspaceMode:          "single",
		DefaultWorkspaceID:     authz.DefaultWorkspaceID,
		WorkerProtocolVersions: workerProtocols,
		Features: CapabilityFeatureSet{
			RecordingV2: h.cfg.RecordingV2Enabled,
			WorkflowV2:  h.cfg.WorkflowV2Enabled,
			PageMarks:   h.cfg.WorkflowV2Enabled,
			MCP:         h.cfg.MCPEnabled,
		},
		RecordingLimits: RecordingLimits{
			MaxActions:         maxActions,
			MaxDurationMs:      maxDuration.Milliseconds(),
			MaxCompressedBytes: maxCompressedBytes,
		},
	})
}
