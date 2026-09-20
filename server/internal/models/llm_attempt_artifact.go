package models

import "time"

// LLMJobType identifies the durable workflow job that owns a provider attempt.
// Provider artifacts use this discriminator because requirement and DSL jobs
// intentionally remain separate durable queues.
type LLMJobType string

const (
	LLMJobTypeRequirement LLMJobType = "requirement"
	LLMJobTypeDSL         LLMJobType = "dsl"
)

// LLMAttemptOutcome is the terminal outcome of one claimed durable job attempt.
type LLMAttemptOutcome string

const (
	LLMAttemptSucceeded LLMAttemptOutcome = "succeeded"
	LLMAttemptFailed    LLMAttemptOutcome = "failed"
)

type LLMProviderCallKind string

const (
	LLMProviderCallKindResponse LLMProviderCallKind = "provider_response"
	LLMProviderCallKindError    LLMProviderCallKind = "provider_error"
	LLMProviderCallKindCacheHit LLMProviderCallKind = "cache_hit"
)

// LLMDispatchLineage is the secret-free tuple that joins an enforced provider
// call or cache-hit trace to its budget ledger identity and immutable policy
// snapshot. Legacy calls have no dispatch lineage.
type LLMDispatchLineage struct {
	OperationKind     string `json:"operationKind"`
	OperationID       string `json:"operationId"`
	LogicalAttempt    int    `json:"logicalAttempt"`
	PhysicalOrdinal   int    `json:"physicalOrdinal"`
	RouteSlot         string `json:"routeSlot"`
	PolicyFingerprint string `json:"policyFingerprint"`
	PriceRevision     string `json:"priceRevision"`
}

// LLMProviderCall is immutable metadata and encrypted content for one provider
// completion. Artifact is decrypted only by an authenticated store lookup;
// list operations intentionally return metadata without the content.
type LLMProviderCall struct {
	ID                 string              `json:"id"`
	WorkspaceID        string              `json:"workspaceId"`
	RecordingID        string              `json:"recordingId"`
	JobType            LLMJobType          `json:"jobType"`
	JobID              string              `json:"jobId"`
	AttemptNumber      int                 `json:"attemptNumber"`
	CallIndex          int                 `json:"callIndex"`
	CallKind           LLMProviderCallKind `json:"callKind"`
	ProviderAttempt    int                 `json:"providerAttempt"`
	Phase              string              `json:"phase"`
	ChunkIndex         *int                `json:"chunkIndex,omitempty"`
	ChunkCount         int                 `json:"chunkCount"`
	Provider           string              `json:"provider"`
	Model              string              `json:"model"`
	PromptVersion      string              `json:"promptVersion"`
	RequestHash        string              `json:"requestHash"`
	Dispatch           *LLMDispatchLineage `json:"dispatch,omitempty"`
	ResponseHash       string              `json:"responseHash"`
	ResponseID         string              `json:"responseId,omitempty"`
	FinishReason       string              `json:"finishReason,omitempty"`
	HTTPStatus         int                 `json:"httpStatus,omitempty"`
	ErrorCode          string              `json:"errorCode,omitempty"`
	InputTokens        int                 `json:"inputTokens"`
	OutputTokens       int                 `json:"outputTokens"`
	CacheHit           bool                `json:"cacheHit"`
	OriginalBytes      int                 `json:"originalBytes"`
	OriginalBytesExact bool                `json:"originalBytesExact"`
	CapturedBytes      int                 `json:"capturedBytes"`
	ArtifactBytes      int                 `json:"artifactBytes"`
	Redacted           bool                `json:"redacted"`
	Truncated          bool                `json:"truncated"`
	Replayable         bool                `json:"replayable"`
	ArtifactHash       string              `json:"artifactHash"`
	Artifact           any                 `json:"-"`
	CreatedAt          time.Time           `json:"createdAt"`
}

// LLMAttemptReport is the immutable validation and lineage record for one
// claimed durable job attempt. Its encrypted artifact may contain the ordered
// provider-call IDs, parsed provider output, and bounded validation details.
type LLMAttemptReport struct {
	ID                  string            `json:"id"`
	WorkspaceID         string            `json:"workspaceId"`
	RecordingID         string            `json:"recordingId"`
	JobType             LLMJobType        `json:"jobType"`
	JobID               string            `json:"jobId"`
	AttemptNumber       int               `json:"attemptNumber"`
	Outcome             LLMAttemptOutcome `json:"outcome"`
	Provider            string            `json:"provider,omitempty"`
	Model               string            `json:"model,omitempty"`
	PromptVersion       string            `json:"promptVersion"`
	PolicyFingerprint   string            `json:"policyFingerprint,omitempty"`
	RecordingHash       string            `json:"recordingHash"`
	RequirementHash     string            `json:"requirementHash,omitempty"`
	BaselineHash        string            `json:"baselineHash,omitempty"`
	SelectorCatalogHash string            `json:"selectorCatalogHash,omitempty"`
	CallCount           int               `json:"callCount"`
	InputTokens         int               `json:"inputTokens"`
	OutputTokens        int               `json:"outputTokens"`
	ValidationPhase     string            `json:"validationPhase,omitempty"`
	ErrorCode           string            `json:"errorCode,omitempty"`
	ErrorMessage        string            `json:"errorMessage,omitempty"`
	SafetyFlags         []string          `json:"safetyFlags"`
	Replayable          bool              `json:"replayable"`
	ArtifactBytes       int               `json:"artifactBytes"`
	ArtifactHash        string            `json:"artifactHash"`
	Artifact            any               `json:"-"`
	CreatedAt           time.Time         `json:"createdAt"`
}
