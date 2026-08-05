package domain

import "time"

type SyncJobType string

const (
	SyncJobSync      SyncJobType = "sync"
	SyncJobReconcile SyncJobType = "reconcile"
	SyncJobExport    SyncJobType = "export"
	SyncJobErase     SyncJobType = "erase"
)

type SyncJobStatus string

const (
	SyncJobPending           SyncJobStatus = "pending"
	SyncJobRunning           SyncJobStatus = "running"
	SyncJobCompleted         SyncJobStatus = "completed"
	SyncJobPartiallyComplete SyncJobStatus = "partially_completed"
	SyncJobFailed            SyncJobStatus = "failed"
	SyncJobUnknown           SyncJobStatus = "unknown"
)

type SyncJob struct {
	ID             ID
	AccountID      ID
	ActorUID       string
	TenantID       string
	RequestID      string
	JobType        SyncJobType
	Status         SyncJobStatus
	IdempotencyKey string
	Request        map[string]any
	Result         map[string]any
	ErrorCode      string
	ErrorMessage   string
	Attempt        int
	LeaseOwner     string
	LeaseUntil     *time.Time
	LeaseKind      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}
