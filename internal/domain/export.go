package domain

import (
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// ExportJobStatus enumerates the lifecycle states of a data export job.
type ExportJobStatus string

const (
	ExportJobPending    ExportJobStatus = "pending"
	ExportJobProcessing ExportJobStatus = "processing"
	ExportJobCompleted  ExportJobStatus = "completed"
	ExportJobFailed     ExportJobStatus = "failed"
)

// ExportJob tracks an asynchronous user-data export request. The worker
// transitions from pending → processing → completed (or → failed). Once
// completed the archive is downloadable via the storage layer (the
// ArchiveStorageKey points to the generated ZIP).
type ExportJob struct {
	ID                idgen.ID
	OwnerUID          string
	Status            ExportJobStatus
	ArchiveStorageKey string
	FileSize          int64
	ErrorMessage      string
	ExpiresAt         time.Time
	CreatedAt         time.Time
	CompletedAt       *time.Time
}

// ValidStatusTransition reports whether moving from current to next is legal
// per the export-job state machine.
func ValidStatusTransition(current, next ExportJobStatus) bool {
	switch current {
	case ExportJobPending:
		return next == ExportJobProcessing || next == ExportJobFailed
	case ExportJobProcessing:
		return next == ExportJobCompleted || next == ExportJobFailed
	case ExportJobCompleted, ExportJobFailed:
		return false
	default:
		return false
	}
}
