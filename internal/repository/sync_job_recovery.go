package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSyncReplayConflict     = errors.New("sync summary replay conflicts with an existing request")
	ErrSyncReplayStateInvalid = errors.New("sync summary replay state is invalid")
	ErrSyncOutcomeConflict    = errors.New("sync source outcome conflicts with an existing state")
)

// SummaryReplay is internal recovery state and is deliberately not part of a
// public SyncJob response.
type SummaryReplay struct {
	JobID            domain.ID
	Source           string
	ExecutionAttempt int
	Request          domain.SummaryApplicationRequest
	RequestSHA256    string
	State            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ResolvedAt       *time.Time
}

type SummaryReplayInput struct {
	JobID            domain.ID
	Source           string
	ExecutionAttempt int
	Request          domain.SummaryApplicationRequest
}

type EraseOutcomeInput struct {
	Source            string
	RequestedDatasets []domain.AccountDataset
}

type EraseOutcome struct {
	JobID             domain.ID
	Source            string
	RequestedDatasets []domain.AccountDataset
	Status            string
	SourceOperationID string
	ErrorCode         string
	Result            map[string]any
	UpdatedAt         time.Time
}

type EraseOutcomeCheckpoint struct {
	JobID             domain.ID
	Source            string
	Worker            string
	Status            string
	SourceOperationID string
	ErrorCode         string
	Result            map[string]any
}

type ArtifactCheckpoint struct {
	JobID          domain.ID
	Kind           string
	Worker         string
	StorageKey     string
	ManifestSHA256 string
	SizeBytes      int64
	Continuation   map[string]any
}

type ArtifactMetadata struct {
	JobID          domain.ID
	Kind           string
	StorageKey     string
	ManifestSHA256 string
	SizeBytes      int64
	Continuation   map[string]any
	UpdatedAt      time.Time
}

// SyncJobArtifactRepository is a read-only internal capability. It is kept
// separate from the recovery writer interface so legacy test and migration
// repositories cannot accidentally expose recovery metadata publicly.
type SyncJobArtifactRepository interface {
	GetArtifact(context.Context, domain.ID, string) (*ArtifactMetadata, error)
}

// SyncJobArtifactBurner atomically removes the durable pointer before the
// backing object is deleted, preventing two one-time downloads from both
// passing the read-before-delete race.
type SyncJobArtifactBurner interface {
	BurnArtifact(context.Context, domain.ID, string, string) error
}

// SyncJobRecoveryRepository contains only internal recovery capabilities so
// existing repository fakes and the public job contract remain compatible.
type SyncJobRecoveryRepository interface {
	SaveSummaryReplay(context.Context, SummaryReplayInput) error
	ListSummaryReplays(context.Context, domain.ID) ([]SummaryReplay, error)
	ResolveSummaryReplay(context.Context, domain.ID, string, int, string) error
	EnsureEraseOutcomes(context.Context, domain.ID, string, []EraseOutcomeInput) error
	ListEraseOutcomes(context.Context, domain.ID) ([]EraseOutcome, error)
	CheckpointEraseOutcome(context.Context, EraseOutcomeCheckpoint) error
	CheckpointArtifact(context.Context, ArtifactCheckpoint) error
}

type summaryReplayRow struct {
	JobID            domain.ID      `gorm:"column:job_id;primaryKey"`
	Source           string         `gorm:"column:source;primaryKey"`
	ExecutionAttempt int            `gorm:"column:execution_attempt;primaryKey"`
	Request          datatypes.JSON `gorm:"column:request"`
	RequestSHA256    string         `gorm:"column:request_sha256"`
	State            string         `gorm:"column:state"`
	CreatedAt        time.Time      `gorm:"column:created_at"`
	UpdatedAt        time.Time      `gorm:"column:updated_at"`
	ResolvedAt       *time.Time     `gorm:"column:resolved_at"`
}

func (summaryReplayRow) TableName() string { return "sync_job_summary_replays" }

type eraseOutcomeRow struct {
	JobID             domain.ID      `gorm:"column:job_id;primaryKey"`
	Source            string         `gorm:"column:source;primaryKey"`
	RequestedDatasets datatypes.JSON `gorm:"column:requested_datasets"`
	Status            string         `gorm:"column:status"`
	SourceOperationID string         `gorm:"column:source_operation_id"`
	ErrorCode         string         `gorm:"column:error_code"`
	Result            datatypes.JSON `gorm:"column:result"`
	UpdatedAt         time.Time      `gorm:"column:updated_at"`
}

func (eraseOutcomeRow) TableName() string { return "sync_job_source_outcomes" }

type artifactRow struct {
	JobID          domain.ID      `gorm:"column:job_id;primaryKey"`
	Kind           string         `gorm:"column:kind;primaryKey"`
	StorageKey     string         `gorm:"column:storage_key"`
	ManifestSHA256 string         `gorm:"column:manifest_sha256"`
	SizeBytes      int64          `gorm:"column:size_bytes"`
	Continuation   datatypes.JSON `gorm:"column:continuation"`
	UpdatedAt      time.Time      `gorm:"column:updated_at"`
}

func (artifactRow) TableName() string { return "sync_job_artifacts" }

func requestDigest(request domain.SummaryApplicationRequest) ([]byte, string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, "", fmt.Errorf("marshal summary replay: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func (r *syncJobRepo) lockRecoveryJob(tx *gorm.DB, id domain.ID, worker string) (*syncJobRow, error) {
	var row syncJobRow
	// Lock the parent before touching recovery state. The lease check must use
	// clock_timestamp(), not now(): PostgreSQL's now() is fixed at transaction
	// start, so a lock wait could otherwise resurrect an expired claim.
	result := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND status = ? AND lease_owner = ? AND lease_until > clock_timestamp()", id,
			string(domain.SyncJobRunning), worker).First(&row)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ErrSyncJobLeaseLost
		}
		return nil, result.Error
	}
	return &row, nil
}

func (r *syncJobRepo) SaveSummaryReplay(ctx context.Context, input SummaryReplayInput) error {
	data, digest, err := requestDigest(input.Request)
	if err != nil {
		return err
	}
	worker := recoveryWorker(ctx)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := r.lockRecoveryJob(tx, input.JobID, worker)
		if err != nil {
			return err
		}
		if err := validateSummaryReplayInput(input, job); err != nil {
			return err
		}
		row := &summaryReplayRow{JobID: input.JobID, Source: input.Source, ExecutionAttempt: input.ExecutionAttempt,
			Request: datatypes.JSON(data), RequestSHA256: digest, State: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			return err
		}
		var existing summaryReplayRow
		if err := tx.Where("job_id = ? AND source = ? AND execution_attempt = ?",
			input.JobID, input.Source, input.ExecutionAttempt).First(&existing).Error; err != nil {
			return err
		}
		if existing.RequestSHA256 != digest {
			return ErrSyncReplayConflict
		}
		return nil
	})
}

func validateSummaryReplayInput(input SummaryReplayInput, job *syncJobRow) error {
	if input.ExecutionAttempt <= 0 || input.Source == "" || requestSourceMismatch(input.Request, input.Source) ||
		input.Request.AccountID != job.AccountID || input.ExecutionAttempt != job.Attempt {
		return ErrSyncReplayStateInvalid
	}
	return nil
}

func requestSourceMismatch(request domain.SummaryApplicationRequest, source string) bool {
	return request.Source != source || request.AccountID == (domain.ID{})
}

type SummaryReplayStateRepository interface {
	MarkSummaryReplayUnknown(context.Context, domain.ID, string, int) error
}

func (r *syncJobRepo) MarkSummaryReplayUnknown(ctx context.Context, id domain.ID, source string, attempt int) error {
	worker := recoveryWorker(ctx)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := r.lockRecoveryJob(tx, id, worker); err != nil {
			return err
		}
		updated := tx.Model(&summaryReplayRow{}).
			Where("job_id = ? AND source = ? AND execution_attempt = ? AND state <> ?", id, source, attempt, "resolved").
			Updates(map[string]any{"state": "unknown", "updated_at": time.Now().UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSyncReplayStateInvalid
		}
		return nil
	})
}

func (r *syncJobRepo) ListSummaryReplays(ctx context.Context, id domain.ID) ([]SummaryReplay, error) {
	var rows []summaryReplayRow
	if err := r.db.WithContext(ctx).Where("job_id = ?", id).Order("execution_attempt ASC, source ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]SummaryReplay, 0, len(rows))
	for i := range rows {
		replay, err := summaryReplayFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		result = append(result, replay)
	}
	return result, nil
}

func summaryReplayFromRow(row *summaryReplayRow) (SummaryReplay, error) {
	if !validSummaryReplayRow(row) {
		return SummaryReplay{}, ErrSyncReplayStateInvalid
	}
	var request domain.SummaryApplicationRequest
	if err := json.Unmarshal(row.Request, &request); err != nil {
		return SummaryReplay{}, fmt.Errorf("%w: malformed request", ErrSyncReplayStateInvalid)
	}
	_, digest, err := requestDigest(request)
	if err != nil || digest != row.RequestSHA256 || request.Source != row.Source || request.AccountID == (domain.ID{}) {
		return SummaryReplay{}, ErrSyncReplayStateInvalid
	}
	return SummaryReplay{JobID: row.JobID, Source: row.Source, ExecutionAttempt: row.ExecutionAttempt,
		Request: request, RequestSHA256: row.RequestSHA256, State: row.State, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, ResolvedAt: row.ResolvedAt}, nil
}

func validSummaryReplayRow(row *summaryReplayRow) bool {
	return row != nil && row.ExecutionAttempt > 0 && row.Source != "" &&
		(row.State == "pending" || row.State == "unknown" || row.State == "resolved") &&
		((row.State == "resolved") == (row.ResolvedAt != nil))
}

func (r *syncJobRepo) ResolveSummaryReplay(ctx context.Context, id domain.ID, source string, attempt int, digest string) error {
	worker := recoveryWorker(ctx)
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := r.lockRecoveryJob(tx, id, worker); err != nil {
			return err
		}
		updated := tx.Model(&summaryReplayRow{}).Where("job_id = ? AND source = ? AND execution_attempt = ? AND request_sha256 = ?",
			id, source, attempt, digest).Updates(map[string]any{"state": "resolved", "resolved_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSyncReplayStateInvalid
		}
		return nil
	})
}

func (r *syncJobRepo) EnsureEraseOutcomes(ctx context.Context, id domain.ID, worker string, inputs []EraseOutcomeInput) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := r.lockRecoveryJob(tx, id, worker); err != nil {
			return err
		}
		for _, input := range inputs {
			if input.Source == "" || len(input.RequestedDatasets) == 0 {
				return ErrSyncOutcomeConflict
			}
			datasets, err := json.Marshal(input.RequestedDatasets)
			if err != nil {
				return err
			}
			row := &eraseOutcomeRow{JobID: id, Source: input.Source,
				RequestedDatasets: datatypes.JSON(datasets), Status: "pending",
				Result: datatypes.JSON([]byte(`{}`)), UpdatedAt: time.Now().UTC()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
				return err
			}
			var existing eraseOutcomeRow
			if findErr := tx.Where("job_id = ? AND source = ?", id, input.Source).First(&existing).Error; findErr != nil {
				return findErr
			}
			if string(existing.RequestedDatasets) != string(datasets) {
				return ErrSyncOutcomeConflict
			}
		}
		return nil
	})
}

func (r *syncJobRepo) ListEraseOutcomes(ctx context.Context, id domain.ID) ([]EraseOutcome, error) {
	var rows []eraseOutcomeRow
	if err := r.db.WithContext(ctx).Where("job_id = ?", id).Order("source ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]EraseOutcome, 0, len(rows))
	for i := range rows {
		outcome, err := eraseOutcomeFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		result = append(result, outcome)
	}
	return result, nil
}

func eraseOutcomeFromRow(row *eraseOutcomeRow) (EraseOutcome, error) {
	var datasets []domain.AccountDataset
	var data map[string]any
	if !validEraseOutcomeStatus(row.Status) || row.Source == "" || len(row.SourceOperationID) > 512 ||
		len(row.ErrorCode) > 256 || json.Unmarshal(row.RequestedDatasets, &datasets) != nil || len(datasets) == 0 ||
		json.Unmarshal(row.Result, &data) != nil || data == nil {
		return EraseOutcome{}, ErrSyncReplayStateInvalid
	}
	return EraseOutcome{JobID: row.JobID, Source: row.Source, RequestedDatasets: datasets,
		Status: row.Status, SourceOperationID: row.SourceOperationID, ErrorCode: row.ErrorCode, Result: data, UpdatedAt: row.UpdatedAt}, nil
}

func (r *syncJobRepo) CheckpointEraseOutcome(ctx context.Context, checkpoint EraseOutcomeCheckpoint) error {
	data, err := json.Marshal(resultOrEmpty(checkpoint.Result))
	if err != nil {
		return err
	}
	if !validEraseOutcomeStatus(checkpoint.Status) || checkpoint.Source == "" {
		return ErrSyncOutcomeConflict
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := r.lockRecoveryJob(tx, checkpoint.JobID, checkpoint.Worker); err != nil {
			return err
		}
		var existing eraseOutcomeRow
		loaded := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND source = ?", checkpoint.JobID, checkpoint.Source).First(&existing)
		if loaded.Error != nil {
			if errors.Is(loaded.Error, gorm.ErrRecordNotFound) {
				return gorm.ErrRecordNotFound
			}
			return loaded.Error
		}
		operationID, code, err := checkpointValues(&existing, checkpoint)
		if err != nil {
			return err
		}
		return tx.Model(&existing).Updates(map[string]any{
			"status": checkpoint.Status, "source_operation_id": operationID, "error_code": code,
			"result": datatypes.JSON(data), "updated_at": now}).Error
	})
}

func checkpointValues(existing *eraseOutcomeRow, checkpoint EraseOutcomeCheckpoint) (string, string, error) {
	if existing.SourceOperationID != "" && checkpoint.SourceOperationID != "" &&
		existing.SourceOperationID != checkpoint.SourceOperationID {
		return "", "", ErrSyncOutcomeConflict
	}
	if !validEraseOutcomeTransition(existing.Status, checkpoint.Status) {
		return "", "", ErrSyncOutcomeConflict
	}
	operationID := checkpoint.SourceOperationID
	if operationID == "" {
		operationID = existing.SourceOperationID
	}
	code := checkpoint.ErrorCode
	if code == "" {
		code = existing.ErrorCode
	}
	return operationID, code, nil
}

func validEraseOutcomeStatus(status string) bool {
	switch status {
	case "pending", "completed", "failed", "unknown":
		return true
	default:
		return false
	}
}

func terminalEraseOutcome(status string) bool { return status == "completed" || status == "failed" }

func validEraseOutcomeTransition(current, next string) bool {
	if current == next {
		return true
	}
	if terminalEraseOutcome(current) {
		return false
	}
	return next == "completed" || next == "failed" || next == "unknown"
}

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
		if _, err := r.lockRecoveryJob(tx, checkpoint.JobID, checkpoint.Worker); err != nil {
			return err
		}
		if checkpoint.Kind != string(domain.SyncJobExport) && checkpoint.Kind != string(domain.SyncJobAuditExport) {
			return ErrSyncReplayStateInvalid
		}
		row := &artifactRow{JobID: checkpoint.JobID, Kind: checkpoint.Kind, StorageKey: checkpoint.StorageKey,
			ManifestSHA256: checkpoint.ManifestSHA256, SizeBytes: checkpoint.SizeBytes, Continuation: datatypes.JSON(data), UpdatedAt: now}
		return tx.Save(row).Error
	})
}

func recoveryWorker(ctx context.Context) string {
	if value, ok := ctx.Value(syncWorkerContextKey{}).(string); ok {
		return value
	}
	return ""
}
