package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validArtifactDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validArtifactStorageKey(value string, jobID domain.ID) bool {
	return len(value) > 0 && len(value) <= 1024 && !strings.ContainsAny(value, "\\\x00") &&
		!strings.Contains(value, "..") && strings.Contains(value, "/"+jobID.String()+"/")
}

func (r *syncJobRepo) GetArtifact(ctx context.Context, id domain.ID, kind string) (*ArtifactMetadata, error) {
	var row artifactRow
	if err := r.db.WithContext(ctx).Where("job_id = ? AND kind = ?", id, kind).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var continuation map[string]any
	if err := json.Unmarshal(row.Continuation, &continuation); err != nil {
		return nil, ErrSyncReplayStateInvalid
	}
	return &ArtifactMetadata{JobID: row.JobID, Kind: row.Kind, StorageKey: row.StorageKey,
		ManifestSHA256: row.ManifestSHA256, SizeBytes: row.SizeBytes, Continuation: continuation, UpdatedAt: row.UpdatedAt}, nil
}

func (r *syncJobRepo) CheckpointArtifact(ctx context.Context, checkpoint ArtifactCheckpoint) error {
	data, err := json.Marshal(resultOrEmpty(checkpoint.Continuation))
	if err != nil {
		return err
	}
	if !validArtifactDigest(checkpoint.ManifestSHA256) || checkpoint.SizeBytes < 0 ||
		!validArtifactStorageKey(checkpoint.StorageKey, checkpoint.JobID) {
		return ErrSyncReplayStateInvalid
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := r.lockRecoveryJob(tx, checkpoint.JobID, checkpoint.Worker)
		if err != nil {
			return err
		}
		if !validArtifactJobKind(job.JobType, checkpoint.Kind) {
			return ErrSyncReplayStateInvalid
		}
		row := &artifactRow{JobID: checkpoint.JobID, Kind: checkpoint.Kind, StorageKey: checkpoint.StorageKey,
			ManifestSHA256: checkpoint.ManifestSHA256, SizeBytes: checkpoint.SizeBytes, Continuation: datatypes.JSON(data), UpdatedAt: now}
		original, err := persistOriginalArtifactTx(tx, row)
		if err != nil {
			return err
		}
		checkpoint.Continuation = nil
		if err := json.Unmarshal(original.Continuation, &checkpoint.Continuation); err != nil {
			return err
		}
		return checkpointArtifactResultTx(tx, job, checkpoint)
	})
}

func validArtifactJobKind(jobType, kind string) bool {
	return jobType == kind && (kind == string(domain.SyncJobExport) || kind == string(domain.SyncJobAuditExport))
}

func persistOriginalArtifactTx(tx *gorm.DB, row *artifactRow) (*artifactRow, error) {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return nil, err
	}
	var existing artifactRow
	if err := tx.Where("job_id = ? AND kind = ?", row.JobID, row.Kind).Take(&existing).Error; err != nil {
		return nil, err
	}
	if existing.StorageKey != row.StorageKey || existing.ManifestSHA256 != row.ManifestSHA256 || existing.SizeBytes != row.SizeBytes {
		return nil, ErrSyncOutcomeConflict
	}
	return &existing, nil
}

func checkpointArtifactResultTx(tx *gorm.DB, job *syncJobRow, checkpoint ArtifactCheckpoint) error {
	result, ok := checkpoint.Continuation["_artifact_result"].(map[string]any)
	if !ok {
		return nil // Existing recovery ports may only checkpoint internal cursors.
	}
	if job.JobType != checkpoint.Kind || result["_storage_key"] != checkpoint.StorageKey {
		return ErrSyncReplayStateInvalid
	}
	if _, valid := syncJobExpiry(result["expires_at"]); !valid {
		return ErrSyncReplayStateInvalid
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	// This metadata-only pointer/TTL is committed before Put. It also makes a
	// failed/unknown/running attempt discoverable by bounded retention cleanup.
	return tx.Model(job).Update("result", datatypes.JSON(data)).Error
}
