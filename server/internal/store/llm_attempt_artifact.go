package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/crypto"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const (
	// MaxLLMProviderCallArtifactBytes is an absolute storage bound. Workflow
	// callers may impose smaller phase-specific response limits.
	MaxLLMProviderCallArtifactBytes = 1 << 20
	// MaxLLMAttemptReportArtifactBytes bounds the complete parsed-output,
	// validation, and call-lineage report for one durable attempt.
	MaxLLMAttemptReportArtifactBytes = 4 << 20
)

var (
	ErrLLMArtifactJobNotFound     = errors.New("llm artifact job not found")
	ErrLLMArtifactAttemptNotFound = errors.New("llm artifact attempt not found")
	ErrLLMProviderCallNotFound    = errors.New("llm provider call not found")
	ErrLLMAttemptReportNotFound   = errors.New("llm attempt report not found")
	ErrLLMArtifactTooLarge        = errors.New("llm artifact exceeds storage limit")
	ErrLLMArtifactInvalid         = errors.New("invalid llm artifact metadata")
)

func validLLMJobType(jobType models.LLMJobType) bool {
	return jobType == models.LLMJobTypeRequirement || jobType == models.LLMJobTypeDSL
}

func validLLMProviderCallKind(kind models.LLMProviderCallKind) bool {
	return kind == models.LLMProviderCallKindResponse ||
		kind == models.LLMProviderCallKindError ||
		kind == models.LLMProviderCallKindCacheHit
}

func validLLMDispatchLineage(
	jobType models.LLMJobType,
	attemptNumber int,
	lineage *models.LLMDispatchLineage,
) bool {
	if lineage == nil {
		return true
	}
	if strings.TrimSpace(lineage.OperationKind) == "" ||
		lineage.OperationKind != string(jobType) ||
		strings.TrimSpace(lineage.OperationID) == "" ||
		lineage.LogicalAttempt != attemptNumber ||
		strings.TrimSpace(lineage.PolicyFingerprint) == "" ||
		strings.TrimSpace(lineage.PriceRevision) == "" {
		return false
	}
	return (lineage.RouteSlot == "primary" && lineage.PhysicalOrdinal == 0) ||
		(lineage.RouteSlot == "fallback" && lineage.PhysicalOrdinal == 1)
}

func (s *Store) llmArtifactJobRecording(ctx context.Context, jobType models.LLMJobType, jobID string) (string, int, error) {
	if !validLLMJobType(jobType) || jobID == "" {
		return "", 0, ErrLLMArtifactJobNotFound
	}
	var recordingID string
	var attemptCount int
	var err error
	switch jobType {
	case models.LLMJobTypeRequirement:
		err = s.db.QueryRowContext(ctx, `
			SELECT j.recording_id, j.attempt_count
			FROM requirement_jobs j
			JOIN recordings r
			  ON r.workspace_id = j.workspace_id AND r.id = j.recording_id
			WHERE j.id = ? AND j.workspace_id = ? AND r.status != ?
		`, jobID, workspaceID(ctx), models.RecordingStatusDeleted).Scan(&recordingID, &attemptCount)
	case models.LLMJobTypeDSL:
		err = s.db.QueryRowContext(ctx, `
			SELECT w.recording_id, j.attempt_count
			FROM dsl_jobs j
			JOIN dsl_workflows w
			  ON w.workspace_id = j.workspace_id AND w.id = j.workflow_id
			JOIN recordings r
			  ON r.workspace_id = w.workspace_id AND r.id = w.recording_id
			WHERE j.id = ? AND j.workspace_id = ? AND r.status != ?
		`, jobID, workspaceID(ctx), models.RecordingStatusDeleted).Scan(&recordingID, &attemptCount)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrLLMArtifactJobNotFound
	}
	return recordingID, attemptCount, err
}

func validateLLMArtifactAttempt(attemptNumber, attemptCount int) error {
	if attemptNumber <= 0 || attemptNumber > attemptCount {
		return ErrLLMArtifactAttemptNotFound
	}
	return nil
}

func (s *Store) sealBoundedLLMArtifact(workspace, resourceType, id string, value any, maxBytes int) ([]byte, string, int, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, "", 0, fmt.Errorf("marshal llm artifact: %w", err)
	}
	if len(plain) > maxBytes {
		return nil, "", len(plain), fmt.Errorf("%w: got %d bytes, limit %d", ErrLLMArtifactTooLarge, len(plain), maxBytes)
	}
	compressed, err := gzipBytes(plain)
	if err != nil {
		return nil, "", 0, fmt.Errorf("compress llm artifact: %w", err)
	}
	encrypted, err := crypto.EncryptArtifact(
		s.encryptionKey,
		compressed,
		requirementArtifactAAD(workspace, resourceType, id, "artifact"),
	)
	if err != nil {
		return nil, "", 0, fmt.Errorf("encrypt llm artifact: %w", err)
	}
	return encrypted, artifactHash(plain), len(plain), nil
}

func (s *Store) openLLMArtifact(workspace, resourceType, id string, encrypted []byte, expectedHash string) (any, error) {
	var artifact any
	if err := s.openRequirementArtifact(workspace, resourceType, id, "artifact", encrypted, expectedHash, &artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

// CreateLLMProviderCall durably captures one provider completion before the
// caller parses or validates its content. Rows are append-only.
func (s *Store) CreateLLMProviderCall(ctx context.Context, call *models.LLMProviderCall, artifact any) error {
	if call != nil && call.CallKind == "" {
		call.CallKind = models.LLMProviderCallKindResponse
	}
	if call == nil || !validLLMJobType(call.JobType) || call.JobID == "" ||
		call.CallIndex <= 0 || call.Phase == "" || call.Provider == "" ||
		call.Model == "" || call.PromptVersion == "" || call.RequestHash == "" ||
		call.ResponseHash == "" || call.OriginalBytes < 0 || call.CapturedBytes < 0 ||
		(!call.Redacted && !call.Truncated && call.CapturedBytes > call.OriginalBytes) || call.InputTokens < 0 ||
		call.OutputTokens < 0 || call.ChunkCount < 0 ||
		!validLLMProviderCallKind(call.CallKind) || call.ProviderAttempt < 0 ||
		call.HTTPStatus < 0 || call.HTTPStatus > 599 ||
		!validLLMDispatchLineage(call.JobType, call.AttemptNumber, call.Dispatch) {
		return ErrLLMArtifactInvalid
	}
	if (call.CallKind == models.LLMProviderCallKindCacheHit &&
		(call.ProviderAttempt != 0 || !call.CacheHit)) ||
		(call.CallKind != models.LLMProviderCallKindCacheHit &&
			(call.ProviderAttempt <= 0 || call.CacheHit)) {
		return ErrLLMArtifactInvalid
	}
	if call.ChunkIndex != nil && (call.ChunkCount <= 0 || *call.ChunkIndex < 0 || *call.ChunkIndex >= call.ChunkCount) {
		return ErrLLMArtifactInvalid
	}
	if call.Replayable && (!call.OriginalBytesExact || call.Redacted || call.Truncated ||
		call.CapturedBytes != call.OriginalBytes) {
		return ErrLLMArtifactInvalid
	}

	recordingID, attemptCount, err := s.llmArtifactJobRecording(ctx, call.JobType, call.JobID)
	if err != nil {
		return err
	}
	if err := validateLLMArtifactAttempt(call.AttemptNumber, attemptCount); err != nil {
		return err
	}
	call.WorkspaceID = workspaceID(ctx)
	call.RecordingID = recordingID
	if call.ID == "" {
		call.ID = NewID()
	}
	if call.CreatedAt.IsZero() {
		call.CreatedAt = time.Now().UTC()
	}
	encrypted, hash, artifactBytes, err := s.sealBoundedLLMArtifact(
		call.WorkspaceID,
		"llm-provider-call",
		call.ID,
		artifact,
		MaxLLMProviderCallArtifactBytes,
	)
	if err != nil {
		return err
	}
	call.ArtifactHash = hash
	call.ArtifactBytes = artifactBytes
	call.Artifact = artifact

	if call.Dispatch == nil {
		// Keep legacy attempt fixtures and pre-026 rows writable without
		// inventing policy lineage. Production opens the store only after all
		// migrations, but migration qualification intentionally exercises the
		// current persistence code against historical schemas.
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO llm_provider_calls (
				id, workspace_id, recording_id, job_type, job_id, attempt_number,
				call_index, call_kind, provider_attempt, phase, chunk_index,
				chunk_count, provider, model,
				prompt_version, request_hash, response_hash, response_id,
				finish_reason, http_status, error_code, input_tokens, output_tokens, cache_hit,
				original_bytes, original_bytes_exact, captured_bytes, artifact_bytes, redacted, truncated,
				replayable, artifact, artifact_hash, created_at
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''),
				NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)
		`, call.ID, call.WorkspaceID, call.RecordingID, call.JobType, call.JobID,
			call.AttemptNumber, call.CallIndex, call.CallKind, call.ProviderAttempt,
			call.Phase, call.ChunkIndex, call.ChunkCount, call.Provider, call.Model, call.PromptVersion,
			call.RequestHash, call.ResponseHash, call.ResponseID, call.FinishReason,
			call.HTTPStatus, call.ErrorCode, call.InputTokens, call.OutputTokens, call.CacheHit, call.OriginalBytes,
			call.OriginalBytesExact, call.CapturedBytes, call.ArtifactBytes, call.Redacted, call.Truncated,
			call.Replayable, encrypted, call.ArtifactHash, call.CreatedAt)
		return err
	}

	var operationKind, operationID, logicalAttempt, physicalOrdinal any
	var routeSlot, policyFingerprint, priceRevision any
	if call.Dispatch != nil {
		operationKind = call.Dispatch.OperationKind
		operationID = call.Dispatch.OperationID
		logicalAttempt = call.Dispatch.LogicalAttempt
		physicalOrdinal = call.Dispatch.PhysicalOrdinal
		routeSlot = call.Dispatch.RouteSlot
		policyFingerprint = call.Dispatch.PolicyFingerprint
		priceRevision = call.Dispatch.PriceRevision
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO llm_provider_calls (
			id, workspace_id, recording_id, job_type, job_id, attempt_number,
			call_index, call_kind, provider_attempt, phase, chunk_index,
			chunk_count, provider, model,
			prompt_version, request_hash, operation_kind, operation_id,
			logical_attempt, physical_ordinal, route_slot, policy_fingerprint,
			price_revision, response_hash, response_id,
			finish_reason, http_status, error_code, input_tokens, output_tokens, cache_hit,
			original_bytes, original_bytes_exact, captured_bytes, artifact_bytes, redacted, truncated,
			replayable, artifact, artifact_hash, created_at
		) VALUES (
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''),
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?
		)
	`, call.ID, call.WorkspaceID, call.RecordingID, call.JobType, call.JobID,
		call.AttemptNumber, call.CallIndex, call.CallKind, call.ProviderAttempt,
		call.Phase, call.ChunkIndex, call.ChunkCount, call.Provider, call.Model, call.PromptVersion,
		call.RequestHash, operationKind, operationID, logicalAttempt, physicalOrdinal,
		routeSlot, policyFingerprint, priceRevision, call.ResponseHash, call.ResponseID, call.FinishReason,
		call.HTTPStatus, call.ErrorCode, call.InputTokens, call.OutputTokens, call.CacheHit, call.OriginalBytes,
		call.OriginalBytesExact, call.CapturedBytes, call.ArtifactBytes, call.Redacted, call.Truncated,
		call.Replayable, encrypted, call.ArtifactHash, call.CreatedAt)
	return err
}

const llmProviderCallColumns = `
	id, workspace_id, recording_id, job_type, job_id, attempt_number,
	call_index, call_kind, provider_attempt, phase, chunk_index, chunk_count, provider, model,
	prompt_version, request_hash, operation_kind, operation_id, logical_attempt,
	physical_ordinal, route_slot, policy_fingerprint, price_revision,
	response_hash, COALESCE(response_id, ''),
	COALESCE(finish_reason, ''), http_status, COALESCE(error_code, ''),
	input_tokens, output_tokens, cache_hit,
	original_bytes, original_bytes_exact, captured_bytes, artifact_bytes, redacted, truncated,
	replayable, artifact_hash, created_at`

const llmProviderCallSelect = `SELECT ` + llmProviderCallColumns + `
	FROM llm_provider_calls`

type llmProviderCallScanner interface {
	Scan(...any) error
}

type llmArtifactExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func scanLLMProviderCall(scanner llmProviderCallScanner) (*models.LLMProviderCall, error) {
	call := &models.LLMProviderCall{}
	var chunkIndex sql.NullInt64
	var operationKind, operationID, routeSlot, policyFingerprint, priceRevision sql.NullString
	var logicalAttempt, physicalOrdinal sql.NullInt64
	if err := scanner.Scan(
		&call.ID, &call.WorkspaceID, &call.RecordingID, &call.JobType, &call.JobID,
		&call.AttemptNumber, &call.CallIndex, &call.CallKind, &call.ProviderAttempt,
		&call.Phase, &chunkIndex,
		&call.ChunkCount, &call.Provider, &call.Model, &call.PromptVersion,
		&call.RequestHash, &operationKind, &operationID, &logicalAttempt, &physicalOrdinal,
		&routeSlot, &policyFingerprint, &priceRevision,
		&call.ResponseHash, &call.ResponseID, &call.FinishReason,
		&call.HTTPStatus, &call.ErrorCode, &call.InputTokens, &call.OutputTokens,
		&call.CacheHit, &call.OriginalBytes,
		&call.OriginalBytesExact, &call.CapturedBytes, &call.ArtifactBytes, &call.Redacted, &call.Truncated,
		&call.Replayable, &call.ArtifactHash, &call.CreatedAt,
	); err != nil {
		return nil, err
	}
	if chunkIndex.Valid {
		value := int(chunkIndex.Int64)
		call.ChunkIndex = &value
	}
	if err := populateLLMDispatchLineage(
		call,
		operationKind,
		operationID,
		logicalAttempt,
		physicalOrdinal,
		routeSlot,
		policyFingerprint,
		priceRevision,
	); err != nil {
		return nil, err
	}
	return call, nil
}

func populateLLMDispatchLineage(
	call *models.LLMProviderCall,
	operationKind, operationID sql.NullString,
	logicalAttempt, physicalOrdinal sql.NullInt64,
	routeSlot, policyFingerprint, priceRevision sql.NullString,
) error {
	present := operationKind.Valid || operationID.Valid || logicalAttempt.Valid ||
		physicalOrdinal.Valid || routeSlot.Valid || policyFingerprint.Valid || priceRevision.Valid
	if !present {
		return nil
	}
	if !operationKind.Valid || !operationID.Valid || !logicalAttempt.Valid ||
		!physicalOrdinal.Valid || !routeSlot.Valid || !policyFingerprint.Valid || !priceRevision.Valid {
		return ErrLLMArtifactInvalid
	}
	call.Dispatch = &models.LLMDispatchLineage{
		OperationKind:     operationKind.String,
		OperationID:       operationID.String,
		LogicalAttempt:    int(logicalAttempt.Int64),
		PhysicalOrdinal:   int(physicalOrdinal.Int64),
		RouteSlot:         routeSlot.String,
		PolicyFingerprint: policyFingerprint.String,
		PriceRevision:     priceRevision.String,
	}
	if !validLLMDispatchLineage(call.JobType, call.AttemptNumber, call.Dispatch) {
		return ErrLLMArtifactInvalid
	}
	return nil
}

// GetLLMProviderCall returns one workspace-scoped call with its artifact
// decrypted and integrity-checked.
func (s *Store) GetLLMProviderCall(ctx context.Context, id string) (*models.LLMProviderCall, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+llmProviderCallColumns+`,
	       artifact
		FROM llm_provider_calls
		WHERE id = ? AND workspace_id = ?`, id, workspaceID(ctx))
	call := &models.LLMProviderCall{}
	var chunkIndex sql.NullInt64
	var operationKind, operationID, routeSlot, policyFingerprint, priceRevision sql.NullString
	var logicalAttempt, physicalOrdinal sql.NullInt64
	var encrypted []byte
	err := row.Scan(
		&call.ID, &call.WorkspaceID, &call.RecordingID, &call.JobType, &call.JobID,
		&call.AttemptNumber, &call.CallIndex, &call.CallKind, &call.ProviderAttempt,
		&call.Phase, &chunkIndex,
		&call.ChunkCount, &call.Provider, &call.Model, &call.PromptVersion,
		&call.RequestHash, &operationKind, &operationID, &logicalAttempt, &physicalOrdinal,
		&routeSlot, &policyFingerprint, &priceRevision,
		&call.ResponseHash, &call.ResponseID, &call.FinishReason,
		&call.HTTPStatus, &call.ErrorCode, &call.InputTokens, &call.OutputTokens,
		&call.CacheHit, &call.OriginalBytes,
		&call.OriginalBytesExact, &call.CapturedBytes, &call.ArtifactBytes, &call.Redacted, &call.Truncated,
		&call.Replayable, &call.ArtifactHash, &call.CreatedAt, &encrypted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLLMProviderCallNotFound
	}
	if err != nil {
		return nil, err
	}
	if chunkIndex.Valid {
		value := int(chunkIndex.Int64)
		call.ChunkIndex = &value
	}
	if err := populateLLMDispatchLineage(
		call,
		operationKind,
		operationID,
		logicalAttempt,
		physicalOrdinal,
		routeSlot,
		policyFingerprint,
		priceRevision,
	); err != nil {
		return nil, err
	}
	call.Artifact, err = s.openLLMArtifact(call.WorkspaceID, "llm-provider-call", call.ID, encrypted, call.ArtifactHash)
	if err != nil {
		return nil, err
	}
	return call, nil
}

// ListLLMProviderCalls returns ordered metadata for one durable job attempt.
// Encrypted response content is available only through GetLLMProviderCall.
func (s *Store) ListLLMProviderCalls(ctx context.Context, jobType models.LLMJobType, jobID string, attemptNumber int) ([]*models.LLMProviderCall, error) {
	if !validLLMJobType(jobType) || attemptNumber <= 0 {
		return nil, ErrLLMArtifactInvalid
	}
	rows, err := s.db.QueryContext(ctx, llmProviderCallSelect+`
		WHERE workspace_id = ? AND job_type = ? AND job_id = ? AND attempt_number = ?
		ORDER BY call_index`, workspaceID(ctx), jobType, jobID, attemptNumber)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := []*models.LLMProviderCall{}
	for rows.Next() {
		call, err := scanLLMProviderCall(rows)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

type preparedLLMAttemptReport struct {
	encrypted   []byte
	safetyFlags string
}

func (s *Store) prepareLLMAttemptReport(ctx context.Context, report *models.LLMAttemptReport, artifact any) (*preparedLLMAttemptReport, error) {
	if report == nil || !validLLMJobType(report.JobType) || report.JobID == "" ||
		(report.Outcome != models.LLMAttemptSucceeded && report.Outcome != models.LLMAttemptFailed) ||
		report.PromptVersion == "" || report.RecordingHash == "" ||
		(report.PolicyFingerprint != "" && strings.TrimSpace(report.PolicyFingerprint) == "") ||
		report.InputTokens < 0 || report.OutputTokens < 0 {
		return nil, ErrLLMArtifactInvalid
	}
	recordingID, attemptCount, err := s.llmArtifactJobRecording(ctx, report.JobType, report.JobID)
	if err != nil {
		return nil, err
	}
	if err := validateLLMArtifactAttempt(report.AttemptNumber, attemptCount); err != nil {
		return nil, err
	}
	report.WorkspaceID = workspaceID(ctx)
	report.RecordingID = recordingID
	if report.ID == "" {
		report.ID = NewID()
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Now().UTC()
	}
	if report.SafetyFlags == nil {
		report.SafetyFlags = []string{}
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM llm_provider_calls
		WHERE workspace_id = ? AND job_type = ? AND job_id = ? AND attempt_number = ?
	`, report.WorkspaceID, report.JobType, report.JobID, report.AttemptNumber).Scan(&report.CallCount); err != nil {
		return nil, err
	}
	if report.Replayable {
		var nonReplayable int
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM llm_provider_calls
			WHERE workspace_id = ? AND job_type = ? AND job_id = ? AND attempt_number = ?
			  AND replayable = 0
		`, report.WorkspaceID, report.JobType, report.JobID, report.AttemptNumber).Scan(&nonReplayable); err != nil {
			return nil, err
		}
		if report.CallCount == 0 || nonReplayable != 0 {
			return nil, ErrLLMArtifactInvalid
		}
	}
	encrypted, hash, artifactBytes, err := s.sealBoundedLLMArtifact(
		report.WorkspaceID,
		"llm-attempt-report",
		report.ID,
		artifact,
		MaxLLMAttemptReportArtifactBytes,
	)
	if err != nil {
		return nil, err
	}
	report.ArtifactHash = hash
	report.ArtifactBytes = artifactBytes
	report.Artifact = artifact
	safetyFlags, err := json.Marshal(report.SafetyFlags)
	if err != nil {
		return nil, fmt.Errorf("marshal llm attempt safety flags: %w", err)
	}
	return &preparedLLMAttemptReport{
		encrypted:   encrypted,
		safetyFlags: string(safetyFlags),
	}, nil
}

func insertLLMAttemptReport(ctx context.Context, execer llmArtifactExecer, report *models.LLMAttemptReport, prepared *preparedLLMAttemptReport) error {
	if report == nil || prepared == nil {
		return ErrLLMArtifactInvalid
	}
	if report.PolicyFingerprint == "" {
		_, err := execer.ExecContext(ctx, `
			INSERT INTO llm_attempt_reports (
				id, workspace_id, recording_id, job_type, job_id, attempt_number,
				outcome, provider, model, prompt_version, recording_hash,
				requirement_hash, baseline_hash, selector_catalog_hash, call_count,
				input_tokens, output_tokens, validation_phase, error_code,
				error_message, safety_flags, replayable, artifact_bytes, artifact,
				artifact_hash, created_at
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?,
				NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?,
				NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?
			)
		`, report.ID, report.WorkspaceID, report.RecordingID, report.JobType,
			report.JobID, report.AttemptNumber, report.Outcome, report.Provider,
			report.Model, report.PromptVersion, report.RecordingHash,
			report.RequirementHash, report.BaselineHash, report.SelectorCatalogHash,
			report.CallCount, report.InputTokens, report.OutputTokens,
			report.ValidationPhase, report.ErrorCode, report.ErrorMessage,
			prepared.safetyFlags, report.Replayable, report.ArtifactBytes, prepared.encrypted,
			report.ArtifactHash, report.CreatedAt)
		return err
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO llm_attempt_reports (
			id, workspace_id, recording_id, job_type, job_id, attempt_number,
			outcome, provider, model, prompt_version, policy_fingerprint, recording_hash,
			requirement_hash, baseline_hash, selector_catalog_hash, call_count,
			input_tokens, output_tokens, validation_phase, error_code,
			error_message, safety_flags, replayable, artifact_bytes, artifact,
			artifact_hash, created_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?,
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?
		)
	`, report.ID, report.WorkspaceID, report.RecordingID, report.JobType,
		report.JobID, report.AttemptNumber, report.Outcome, report.Provider,
		report.Model, report.PromptVersion, report.PolicyFingerprint, report.RecordingHash,
		report.RequirementHash, report.BaselineHash, report.SelectorCatalogHash,
		report.CallCount, report.InputTokens, report.OutputTokens,
		report.ValidationPhase, report.ErrorCode, report.ErrorMessage,
		prepared.safetyFlags, report.Replayable, report.ArtifactBytes, prepared.encrypted,
		report.ArtifactHash, report.CreatedAt)
	return err
}

// CreateLLMAttemptReport records the terminal parse/validation outcome and
// exact provider-call lineage for one durable attempt. Rows are append-only.
func (s *Store) CreateLLMAttemptReport(ctx context.Context, report *models.LLMAttemptReport, artifact any) error {
	prepared, err := s.prepareLLMAttemptReport(ctx, report, artifact)
	if err != nil {
		return err
	}
	return insertLLMAttemptReport(ctx, s.db, report, prepared)
}

const llmAttemptReportColumns = `
	id, workspace_id, recording_id, job_type, job_id, attempt_number,
	outcome, COALESCE(provider, ''), COALESCE(model, ''), prompt_version,
	COALESCE(policy_fingerprint, ''), recording_hash, COALESCE(requirement_hash, ''),
	COALESCE(baseline_hash, ''), COALESCE(selector_catalog_hash, ''),
	call_count, input_tokens, output_tokens,
	COALESCE(validation_phase, ''), COALESCE(error_code, ''),
	COALESCE(error_message, ''), safety_flags, replayable,
	artifact_bytes, artifact_hash, created_at`

const llmAttemptReportSelect = `SELECT ` + llmAttemptReportColumns + `
	FROM llm_attempt_reports`

type llmAttemptReportScanner interface {
	Scan(...any) error
}

func scanLLMAttemptReport(scanner llmAttemptReportScanner) (*models.LLMAttemptReport, error) {
	report := &models.LLMAttemptReport{}
	var safetyFlags string
	if err := scanner.Scan(
		&report.ID, &report.WorkspaceID, &report.RecordingID, &report.JobType,
		&report.JobID, &report.AttemptNumber, &report.Outcome, &report.Provider,
		&report.Model, &report.PromptVersion, &report.PolicyFingerprint, &report.RecordingHash,
		&report.RequirementHash, &report.BaselineHash, &report.SelectorCatalogHash,
		&report.CallCount, &report.InputTokens, &report.OutputTokens,
		&report.ValidationPhase, &report.ErrorCode, &report.ErrorMessage,
		&safetyFlags, &report.Replayable, &report.ArtifactBytes,
		&report.ArtifactHash, &report.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(safetyFlags), &report.SafetyFlags); err != nil {
		return nil, fmt.Errorf("decode llm attempt safety flags: %w", err)
	}
	if report.SafetyFlags == nil {
		report.SafetyFlags = []string{}
	}
	return report, nil
}

// GetLLMAttemptReport returns one workspace-scoped report with its artifact
// decrypted and integrity-checked.
func (s *Store) GetLLMAttemptReport(ctx context.Context, id string) (*models.LLMAttemptReport, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+llmAttemptReportColumns+`,
	       artifact
		FROM llm_attempt_reports
		WHERE id = ? AND workspace_id = ?`, id, workspaceID(ctx))
	report := &models.LLMAttemptReport{}
	var safetyFlags string
	var encrypted []byte
	err := row.Scan(
		&report.ID, &report.WorkspaceID, &report.RecordingID, &report.JobType,
		&report.JobID, &report.AttemptNumber, &report.Outcome, &report.Provider,
		&report.Model, &report.PromptVersion, &report.PolicyFingerprint, &report.RecordingHash,
		&report.RequirementHash, &report.BaselineHash, &report.SelectorCatalogHash,
		&report.CallCount, &report.InputTokens, &report.OutputTokens,
		&report.ValidationPhase, &report.ErrorCode, &report.ErrorMessage,
		&safetyFlags, &report.Replayable, &report.ArtifactBytes,
		&report.ArtifactHash, &report.CreatedAt, &encrypted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLLMAttemptReportNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(safetyFlags), &report.SafetyFlags); err != nil {
		return nil, fmt.Errorf("decode llm attempt safety flags: %w", err)
	}
	if report.SafetyFlags == nil {
		report.SafetyFlags = []string{}
	}
	report.Artifact, err = s.openLLMArtifact(report.WorkspaceID, "llm-attempt-report", report.ID, encrypted, report.ArtifactHash)
	if err != nil {
		return nil, err
	}
	return report, nil
}

// ListLLMAttemptReports returns ordered metadata for all retained attempts of
// one durable job. Encrypted artifacts are available only through Get.
func (s *Store) ListLLMAttemptReports(ctx context.Context, jobType models.LLMJobType, jobID string) ([]*models.LLMAttemptReport, error) {
	if !validLLMJobType(jobType) {
		return nil, ErrLLMArtifactInvalid
	}
	rows, err := s.db.QueryContext(ctx, llmAttemptReportSelect+`
		WHERE workspace_id = ? AND job_type = ? AND job_id = ?
		ORDER BY attempt_number`, workspaceID(ctx), jobType, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := []*models.LLMAttemptReport{}
	for rows.Next() {
		report, err := scanLLMAttemptReport(rows)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}
