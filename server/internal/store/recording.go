package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/singhand-labs/AegisCrawler/internal/crypto"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var (
	ErrRecordingNotFound = errors.New("recording not found")
	ErrRecordingDeleted  = errors.New("recording has been deleted")
	ErrRecordingTooLarge = errors.New("compressed recording exceeds configured limit")
)

func recordingAAD(workspace, id string) []byte {
	return []byte(workspace + "\x00" + id)
}

// CreateRecording compresses and encrypts a sanitized delta archive before it
// reaches SQLite. maxCompressedBytes <= 0 disables the size check.
func (s *Store) CreateRecording(ctx context.Context, rec *models.Recording, archive []byte, maxCompressedBytes int64) error {
	rec.WorkspaceID = workspaceID(ctx)
	if rec.ID == "" {
		rec.ID = NewID()
	}
	digest := sha256.Sum256(archive)
	rec.ContentHash = fmt.Sprintf("%x", digest)
	compressed, err := gzipBytes(archive)
	if err != nil {
		return err
	}
	rec.CompressedBytes = int64(len(compressed))
	if maxCompressedBytes > 0 && rec.CompressedBytes > maxCompressedBytes {
		return fmt.Errorf("%w: got %d bytes, limit %d", ErrRecordingTooLarge, rec.CompressedBytes, maxCompressedBytes)
	}
	encrypted, err := crypto.EncryptArtifact(s.encryptionKey, compressed, recordingAAD(rec.WorkspaceID, rec.ID))
	if err != nil {
		return fmt.Errorf("encrypt recording: %w", err)
	}

	const query = `
		INSERT INTO recordings (
			id, workspace_id, owner, status, protocol_version, sanitization_version,
			redaction_count, removed_field_count, action_count, snapshot_count,
			compressed_bytes, content_hash, encrypted_payload,
			started_at, ended_at, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.ExecContext(ctx, query,
		rec.ID, rec.WorkspaceID, rec.Owner, rec.Status, rec.ProtocolVersion, rec.SanitizationVersion,
		rec.RedactionCount, rec.RemovedFieldCount, rec.ActionCount, rec.SnapshotCount,
		rec.CompressedBytes, rec.ContentHash, encrypted,
		rec.StartedAt, rec.EndedAt, rec.CreatedAt, rec.UpdatedAt, rec.ExpiresAt,
	)
	return err
}

// GetRecordingByID returns metadata and the decrypted, decompressed delta
// archive. Deleted recordings retain metadata but never return payload bytes.
func (s *Store) GetRecordingByID(ctx context.Context, id string) (*models.Recording, []byte, error) {
	rec := &models.Recording{}
	var encrypted []byte
	const query = `
		SELECT id, workspace_id, owner, status, protocol_version, sanitization_version,
		       redaction_count, removed_field_count, action_count, snapshot_count,
		       compressed_bytes, content_hash, encrypted_payload,
		       started_at, ended_at, created_at, updated_at, expires_at, deleted_at
		FROM recordings WHERE id = ? AND workspace_id = ?
	`
	err := s.db.QueryRowContext(ctx, query, id, workspaceID(ctx)).Scan(
		&rec.ID, &rec.WorkspaceID, &rec.Owner, &rec.Status, &rec.ProtocolVersion, &rec.SanitizationVersion,
		&rec.RedactionCount, &rec.RemovedFieldCount, &rec.ActionCount, &rec.SnapshotCount,
		&rec.CompressedBytes, &rec.ContentHash, &encrypted,
		&rec.StartedAt, &rec.EndedAt, &rec.CreatedAt, &rec.UpdatedAt, &rec.ExpiresAt, &rec.DeletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrRecordingNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if rec.Status == models.RecordingStatusDeleted || len(encrypted) == 0 {
		return rec, nil, ErrRecordingDeleted
	}
	compressed, err := crypto.DecryptArtifact(s.encryptionKey, encrypted, recordingAAD(rec.WorkspaceID, rec.ID))
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt recording: %w", err)
	}
	archive, err := gunzipBytes(compressed)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(archive)
	actualHash := fmt.Sprintf("%x", digest)
	if subtle.ConstantTimeCompare([]byte(actualHash), []byte(rec.ContentHash)) != 1 {
		return nil, nil, errors.New("recording content hash mismatch")
	}
	return rec, archive, nil
}

// DeleteRecording removes encrypted content while retaining non-sensitive
// hashes, counts, ownership, and timestamps for audit lineage.
func (s *Store) DeleteRecording(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE recordings
			SET status = ?, encrypted_payload = NULL, updated_at = ?, deleted_at = ?
			WHERE id = ? AND workspace_id = ? AND status != ?
		`, models.RecordingStatusDeleted, now, now, id, workspaceID(ctx), models.RecordingStatusDeleted)
		if err != nil {
			return err
		}
		count, _ := res.RowsAffected()
		if count == 0 {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recordings WHERE id = ? AND workspace_id = ?)`, id, workspaceID(ctx)).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrRecordingNotFound
			}
		}
		return deleteRecordingDerivedArtifacts(ctx, tx, workspaceID(ctx), id)
	})
}

func (s *Store) DeleteExpiredRecordings(ctx context.Context, before time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1000
	}
	now := time.Now().UTC()
	var deleted int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE recordings
			SET status = ?, encrypted_payload = NULL, updated_at = ?, deleted_at = ?
			WHERE id IN (
				SELECT id FROM recordings
				WHERE workspace_id = ? AND status != ? AND expires_at <= ?
				ORDER BY expires_at LIMIT ?
			)
		`, models.RecordingStatusDeleted, now, now, workspaceID(ctx), models.RecordingStatusDeleted, before.UTC(), batch)
		if err != nil {
			return err
		}
		deleted, err = res.RowsAffected()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM dsl_replay_attempts
			WHERE workspace_id = ? AND workflow_id IN (
				SELECT id FROM dsl_workflows WHERE workspace_id = ? AND recording_id IN (
					SELECT id FROM recordings WHERE workspace_id = ? AND status = ? AND deleted_at = ?
				)
			)
		`, workspaceID(ctx), workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM dsl_jobs
			WHERE workspace_id = ? AND workflow_id IN (
				SELECT id FROM dsl_workflows WHERE workspace_id = ? AND recording_id IN (
					SELECT id FROM recordings WHERE workspace_id = ? AND status = ? AND deleted_at = ?
				)
			)
		`, workspaceID(ctx), workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM dsl_workflows
			WHERE workspace_id = ? AND recording_id IN (
				SELECT id FROM recordings WHERE workspace_id = ? AND status = ? AND deleted_at = ?
			)
		`, workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM llm_attempt_reports
			WHERE workspace_id = ? AND recording_id IN (
				SELECT id FROM recordings
				WHERE workspace_id = ? AND status = ? AND deleted_at = ?
			)
		`, workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM llm_provider_calls
			WHERE workspace_id = ? AND recording_id IN (
				SELECT id FROM recordings
				WHERE workspace_id = ? AND status = ? AND deleted_at = ?
			)
		`, workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM collection_requirements
			WHERE workspace_id = ? AND recording_id IN (
				SELECT id FROM recordings WHERE workspace_id = ? AND status = ? AND deleted_at = ?
			)
		`, workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			DELETE FROM requirement_jobs
			WHERE workspace_id = ? AND recording_id IN (
				SELECT id FROM recordings WHERE workspace_id = ? AND status = ? AND deleted_at = ?
			)
		`, workspaceID(ctx), workspaceID(ctx), models.RecordingStatusDeleted, now)
		return err
	})
	return deleted, err
}

func deleteRecordingDerivedArtifacts(ctx context.Context, tx *sql.Tx, workspace, recordingID string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM dsl_replay_attempts
		WHERE workspace_id = ? AND workflow_id IN (
			SELECT id FROM dsl_workflows WHERE workspace_id = ? AND recording_id = ?
		)
	`, workspace, workspace, recordingID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM dsl_jobs
		WHERE workspace_id = ? AND workflow_id IN (
			SELECT id FROM dsl_workflows WHERE workspace_id = ? AND recording_id = ?
		)
	`, workspace, workspace, recordingID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM dsl_workflows WHERE workspace_id = ? AND recording_id = ?`,
		workspace, recordingID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM llm_attempt_reports WHERE workspace_id = ? AND recording_id = ?`,
		workspace, recordingID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM llm_provider_calls WHERE workspace_id = ? AND recording_id = ?`,
		workspace, recordingID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM collection_requirements WHERE workspace_id = ? AND recording_id = ?`,
		workspace, recordingID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM requirement_jobs WHERE workspace_id = ? AND recording_id = ?`,
		workspace, recordingID)
	return err
}

func gzipBytes(input []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(input); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("compress recording: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish recording compression: %w", err)
	}
	return buffer.Bytes(), nil
}

// maxDecompressedRecordingBytes caps the decompressed size of a stored
// recording. The compressed write path limits to ~25MB compressed; a crafted
// gzip bomb could decompress to hundreds of MB. This limit (256MB) is well
// above any legitimate recording while preventing OOM.
const maxDecompressedRecordingBytes = 256 * 1024 * 1024

func gunzipBytes(input []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(input))
	if err != nil {
		return nil, fmt.Errorf("open recording compression: %w", err)
	}
	defer reader.Close()
	// M-5: cap decompressed size to prevent a gzip bomb from consuming all
	// memory. A 25MB compressed payload can decompress to gigabytes without
	// this guard.
	limited := io.LimitReader(reader, maxDecompressedRecordingBytes+1)
	output, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("decompress recording: %w", err)
	}
	if len(output) > maxDecompressedRecordingBytes {
		return nil, fmt.Errorf("decompressed recording exceeds %d bytes (possible compression bomb)", maxDecompressedRecordingBytes)
	}
	return output, nil
}
