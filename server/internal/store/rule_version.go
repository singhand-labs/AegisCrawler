package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/authz"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var (
	ErrRuleVersionNotFound = errors.New("rule version not found")
	ErrRuleVersionState    = errors.New("rule version cannot transition from its current state")
)

type ruleVersionExecer interface {
	execer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) CreateRuleVersion(ctx context.Context, rule *models.Rule, recordingID string) (*models.RuleVersion, error) {
	var created *models.RuleVersion
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		created, err = createRuleVersionWithExec(ctx, tx, rule, recordingID)
		return err
	})
	return created, err
}

func (s *Store) CreateRuleVersionTx(ctx context.Context, tx *sql.Tx, rule *models.Rule, recordingID string) (*models.RuleVersion, error) {
	return createRuleVersionWithExec(ctx, tx, rule, recordingID)
}

func createRuleVersionWithExec(ctx context.Context, ex ruleVersionExecer, input *models.Rule, recordingID string) (*models.RuleVersion, error) {
	inputSchema := models.JSON(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)
	outputSchema := models.JSON(`{"type":"object","properties":{},"additionalProperties":true}`)
	if input != nil {
		inputSchema = inferLegacyInputSchema(input.Variables)
		if len(input.Output) > 0 {
			outputSchema = input.Output
		}
	}
	return createRuleVersionWithContractExec(ctx, ex, input, recordingID, inputSchema, outputSchema, "")
}

func (s *Store) CreateRuleVersionWithContractTx(ctx context.Context, tx *sql.Tx, input *models.Rule, recordingID string, inputSchema, outputSchema models.JSON, browserProfileID string) (*models.RuleVersion, error) {
	return createRuleVersionWithContractExec(ctx, tx, input, recordingID, inputSchema, outputSchema, browserProfileID)
}

func createRuleVersionWithContractExec(ctx context.Context, ex ruleVersionExecer, input *models.Rule, recordingID string, inputSchema, outputSchema models.JSON, browserProfileID string) (*models.RuleVersion, error) {
	return createRuleVersionWithContractAndLineageExec(
		ctx, ex, input, recordingID, inputSchema, outputSchema, browserProfileID,
		models.RuleVersionContract{}, nil, "", "",
	)
}

func createRuleVersionWithContractAndLineageExec(ctx context.Context, ex ruleVersionExecer, input *models.Rule, recordingID string, inputSchema, outputSchema models.JSON, browserProfileID string, lineage models.RuleVersionContract, safetyFlags []string, dslWorkflowID, dslJobID string) (*models.RuleVersion, error) {
	if input == nil || input.ID == "" {
		return nil, ErrRuleNotFound
	}
	workspace := workspaceID(ctx)
	var exists bool
	if err := ex.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rules WHERE id = ? AND workspace_id = ?)`, input.ID, workspace).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		catalogData, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("marshal new rule catalog: %w", err)
		}
		catalog := &models.Rule{}
		if err := json.Unmarshal(catalogData, catalog); err != nil {
			return nil, fmt.Errorf("copy new rule catalog: %w", err)
		}
		now := time.Now().UTC()
		catalog.WorkspaceID = workspace
		catalog.Owner = authz.Subject(ctx, "system")
		catalog.ApprovalStatus = string(models.RuleApprovalPending)
		catalog.Enabled = false
		if lineage.SourceKind != "" {
			catalog.Source = lineage.SourceKind
		} else if catalog.Source == "" {
			catalog.Source = "pageagent"
		}
		if catalog.CreatedAt.IsZero() {
			catalog.CreatedAt = now
		}
		catalog.UpdatedAt = now
		if err := createRuleWithExec(ctx, ex, catalog); err != nil {
			if errors.Is(err, ErrWorkspaceConflict) {
				return nil, ErrRuleNotFound
			}
			return nil, err
		}
	}
	if recordingID != "" {
		if err := ex.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recordings WHERE id = ? AND workspace_id = ? AND status != ?)`,
			recordingID, workspace, models.RecordingStatusDeleted).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrRecordingNotFound
		}
	}

	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal rule version input: %w", err)
	}
	rule := &models.Rule{}
	if err := json.Unmarshal(data, rule); err != nil {
		return nil, fmt.Errorf("copy rule version input: %w", err)
	}
	rule.WorkspaceID = workspace
	rule.Owner = authz.Subject(ctx, "system")
	rule.ApprovalStatus = string(models.RuleApprovalApproved)
	rule.Enabled = true
	if rule.Source == "" && lineage.SourceKind == "" {
		rule.Source = "pageagent"
	}
	versionSource := rule.Source
	if lineage.SourceKind != "" {
		versionSource = lineage.SourceKind
	}
	now := time.Now().UTC()
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now
	if rule.Version == "" {
		rule.Version = "1"
	}
	contentRule := *rule
	contentRule.CreatedAt = time.Time{}
	contentRule.UpdatedAt = time.Time{}
	contentJSON, err := json.Marshal(&contentRule)
	if err != nil {
		return nil, fmt.Errorf("marshal rule version content: %w", err)
	}
	// The input/output schemas and browser profile are part of the immutable
	// execution contract. Identical DSL with a different contract must create a
	// new version rather than aliasing the earlier version by rule JSON alone.
	contractContent, err := json.Marshal(struct {
		Rule               json.RawMessage `json:"rule"`
		InputSchema        json.RawMessage `json:"inputSchema"`
		OutputSchema       json.RawMessage `json:"outputSchema"`
		BrowserProfileID   string          `json:"browserProfileId"`
		SourceKind         string          `json:"sourceKind,omitempty"`
		SourceAuthority    string          `json:"sourceAuthority,omitempty"`
		SourceArtifactHash string          `json:"sourceArtifactHash,omitempty"`
		SourceExportHash   string          `json:"sourceExportHash,omitempty"`
		SourceWorkflowID   string          `json:"sourceWorkflowId,omitempty"`
	}{
		Rule: contentJSON, InputSchema: json.RawMessage(inputSchema), OutputSchema: json.RawMessage(outputSchema),
		BrowserProfileID: browserProfileID, SourceKind: lineage.SourceKind,
		SourceAuthority: lineage.SourceAuthority, SourceArtifactHash: lineage.SourceArtifactHash,
		SourceExportHash: lineage.SourceExportHash, SourceWorkflowID: lineage.SourceWorkflowID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal immutable rule contract: %w", err)
	}
	digest := sha256.Sum256(contractContent)
	hash := fmt.Sprintf("%x", digest)

	ruleJSON, err := json.Marshal(rule)
	if err != nil {
		return nil, fmt.Errorf("marshal immutable rule version: %w", err)
	}

	existing, err := getRuleVersionByHash(ctx, ex, workspace, rule.ID, hash)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrRuleVersionNotFound) {
		return nil, err
	}

	var version int
	if err := ex.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_number), 0) + 1 FROM rule_versions WHERE workspace_id = ? AND rule_id = ?`, workspace, rule.ID).Scan(&version); err != nil {
		return nil, err
	}
	label := rule.Version
	if label == "" {
		label = strconv.Itoa(version)
	}
	safetyFlagsJSON, err := json.Marshal(safetyFlags)
	if err != nil {
		return nil, fmt.Errorf("marshal safety flags: %w", err)
	}
	_, err = ex.ExecContext(ctx, `
		INSERT INTO rule_versions (
			workspace_id, rule_id, version_number, version_label, rule_json, content_hash,
			approval_status, owner, source, recording_id, safety_flags,
			dsl_workflow_id, dsl_job_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)
	`, workspace, rule.ID, version, label, ruleJSON, hash, models.RuleApprovalPending,
		rule.Owner, versionSource, recordingID, string(safetyFlagsJSON),
		dslWorkflowID, dslJobID, now)
	if err != nil {
		return nil, err
	}
	if err := insertRuleVersionContractWithExec(ctx, ex, &models.RuleVersionContract{
		WorkspaceID: workspace, RuleID: rule.ID, Version: version,
		InputSchema: inputSchema, OutputSchema: outputSchema,
		BrowserProfileID: browserProfileID,
		SourceKind:       lineage.SourceKind, SourceAuthority: lineage.SourceAuthority,
		SourceArtifactHash: lineage.SourceArtifactHash, SourceExportHash: lineage.SourceExportHash,
		SourceWorkflowID: lineage.SourceWorkflowID, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return &models.RuleVersion{
		WorkspaceID: workspace, RuleID: rule.ID, Version: version, VersionLabel: label,
		Rule: rule, ContentHash: hash, Status: models.RuleApprovalPending, Owner: rule.Owner,
		Source: versionSource, RecordingID: recordingID, SafetyFlags: safetyFlags,
		DSLWorkflowID: dslWorkflowID, DSLJobID: dslJobID, CreatedAt: now,
	}, nil
}

func (s *Store) GetRuleVersion(ctx context.Context, ruleID string, version int) (*models.RuleVersion, error) {
	return getRuleVersion(ctx, s.db, workspaceID(ctx), ruleID, version)
}

func (s *Store) ListRuleVersions(ctx context.Context, ruleID string) ([]*models.RuleVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workspace_id, rule_id, version_number, version_label, rule_json, content_hash,
		       approval_status, owner, source, COALESCE(recording_id, ''), created_at,
		       approved_at, COALESCE(approved_by, ''), rejected_at, COALESCE(rejected_by, ''),
		       COALESCE(safety_flags, '[]'),
		       COALESCE(dsl_workflow_id, ''), COALESCE(dsl_job_id, '')
		FROM rule_versions
		WHERE workspace_id = ? AND rule_id = ?
		ORDER BY version_number DESC
	`, workspaceID(ctx), ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*models.RuleVersion
	for rows.Next() {
		version, err := scanRuleVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (s *Store) ApproveRuleVersion(ctx context.Context, ruleID string, version int) (*models.RuleVersion, error) {
	var approved *models.RuleVersion
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		current, err := getRuleVersion(ctx, tx, workspaceID(ctx), ruleID, version)
		if err != nil {
			return err
		}
		approved, err = approveRuleVersionWithExec(ctx, tx, current)
		return err
	})
	return approved, err
}

func approveRuleVersionWithExec(ctx context.Context, ex ruleVersionExecer, current *models.RuleVersion) (*models.RuleVersion, error) {
	if current == nil || current.Status != models.RuleApprovalPending {
		return nil, ErrRuleVersionState
	}
	now := time.Now().UTC()
	actor := authz.Subject(ctx, "system")
	result, err := ex.ExecContext(ctx, `
		UPDATE rule_versions
		SET approval_status = ?, approved_at = ?, approved_by = ?
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ? AND approval_status = ?
	`, models.RuleApprovalApproved, now, actor, current.WorkspaceID, current.RuleID,
		current.Version, models.RuleApprovalPending)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrRuleVersionState
	}
	current.Rule.Version = current.VersionLabel
	current.Rule.ApprovalStatus = string(models.RuleApprovalApproved)
	current.Rule.Enabled = true
	current.Rule.UpdatedAt = now
	if err := createRuleWithExec(ctx, ex, current.Rule); err != nil {
		return nil, err
	}
	current.Status = models.RuleApprovalApproved
	current.ApprovedAt = &now
	current.ApprovedBy = actor
	return current, nil
}

func (s *Store) RejectRuleVersion(ctx context.Context, ruleID string, version int) (*models.RuleVersion, error) {
	current, err := s.GetRuleVersion(ctx, ruleID, version)
	if err != nil {
		return nil, err
	}
	if current.Status != models.RuleApprovalPending {
		return nil, ErrRuleVersionState
	}
	now := time.Now().UTC()
	actor := authz.Subject(ctx, "system")
	result, err := s.db.ExecContext(ctx, `
		UPDATE rule_versions
		SET approval_status = ?, rejected_at = ?, rejected_by = ?
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ? AND approval_status = ?
	`, models.RuleApprovalRejected, now, actor, workspaceID(ctx), ruleID, version, models.RuleApprovalPending)
	if err != nil {
		return nil, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return nil, ErrRuleVersionState
	}
	current.Status = models.RuleApprovalRejected
	current.RejectedAt = &now
	current.RejectedBy = actor
	return current, nil
}

type ruleVersionScanner interface {
	Scan(...any) error
}

func getRuleVersion(ctx context.Context, ex ruleVersionExecer, workspace, ruleID string, version int) (*models.RuleVersion, error) {
	row := ex.QueryRowContext(ctx, `
		SELECT workspace_id, rule_id, version_number, version_label, rule_json, content_hash,
		       approval_status, owner, source, COALESCE(recording_id, ''), created_at,
		       approved_at, COALESCE(approved_by, ''), rejected_at, COALESCE(rejected_by, ''),
		       COALESCE(safety_flags, '[]'),
		       COALESCE(dsl_workflow_id, ''), COALESCE(dsl_job_id, '')
		FROM rule_versions
		WHERE workspace_id = ? AND rule_id = ? AND version_number = ?
	`, workspace, ruleID, version)
	return scanRuleVersion(row)
}

func getRuleVersionByHash(ctx context.Context, ex ruleVersionExecer, workspace, ruleID, hash string) (*models.RuleVersion, error) {
	row := ex.QueryRowContext(ctx, `
		SELECT workspace_id, rule_id, version_number, version_label, rule_json, content_hash,
		       approval_status, owner, source, COALESCE(recording_id, ''), created_at,
		       approved_at, COALESCE(approved_by, ''), rejected_at, COALESCE(rejected_by, ''),
		       COALESCE(safety_flags, '[]'),
		       COALESCE(dsl_workflow_id, ''), COALESCE(dsl_job_id, '')
		FROM rule_versions
		WHERE workspace_id = ? AND rule_id = ? AND content_hash = ?
	`, workspace, ruleID, hash)
	return scanRuleVersion(row)
}

func scanRuleVersion(scanner ruleVersionScanner) (*models.RuleVersion, error) {
	version := &models.RuleVersion{}
	var ruleJSON []byte
	var safetyFlagsJSON string
	err := scanner.Scan(
		&version.WorkspaceID, &version.RuleID, &version.Version, &version.VersionLabel,
		&ruleJSON, &version.ContentHash, &version.Status, &version.Owner, &version.Source,
		&version.RecordingID, &version.CreatedAt, &version.ApprovedAt, &version.ApprovedBy,
		&version.RejectedAt, &version.RejectedBy, &safetyFlagsJSON,
		&version.DSLWorkflowID, &version.DSLJobID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRuleVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	version.Rule = &models.Rule{}
	if err := json.Unmarshal(ruleJSON, version.Rule); err != nil {
		return nil, fmt.Errorf("decode immutable rule version: %w", err)
	}
	if err := json.Unmarshal([]byte(safetyFlagsJSON), &version.SafetyFlags); err != nil {
		return nil, fmt.Errorf("decode safety_flags: %w", err)
	}
	return version, nil
}
