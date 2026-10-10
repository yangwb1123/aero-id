package domain

import "time"

type OperationStatus string

const (
	OperationRequested          OperationStatus = "requested"
	OperationRunning            OperationStatus = "running"
	OperationWaiting            OperationStatus = "waiting"
	OperationUnknown            OperationStatus = "unknown"
	OperationPartiallyCompleted OperationStatus = "partially_completed"
	OperationCompleted          OperationStatus = "completed"
	OperationFailed             OperationStatus = "failed"
	OperationCompensating       OperationStatus = "compensating"
)

type OperationStepStatus string

const (
	StepPending   OperationStepStatus = "pending"
	StepRunning   OperationStepStatus = "running"
	StepUnknown   OperationStepStatus = "unknown"
	StepSucceeded OperationStepStatus = "succeeded"
	StepFailed    OperationStepStatus = "failed"
	StepSkipped   OperationStepStatus = "skipped"
)

type Operation struct {
	ID             ID
	AccountID      ID
	ActorUID       string
	TenantID       string
	RequestID      string
	HomeRegion     string
	OperationType  string
	Status         OperationStatus
	IdempotencyKey string
	// RequestFingerprint binds this durable receipt to the originating intent.
	// It is internal metadata, not part of the public transport contract.
	RequestFingerprint string `json:"-"`
	RequestPayload     map[string]any
	Result             map[string]any
	ErrorCode          string
	ErrorMessage       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
	Steps              []*OperationStep
}

type OperationStep struct {
	ID                            ID
	OperationID                   ID
	StepKey                       string
	Source                        string
	Action                        string
	Status                        OperationStepStatus
	Attempt                       int
	LeaseOwner                    string
	LeaseUntil                    *time.Time
	LeaseKind                     string
	SourceOperationID             string
	RequestPayload                map[string]any
	Result                        map[string]any
	ErrorCode                     string
	ErrorMessage                  string
	CompensationAction            string
	CompensationStatus            OperationStepStatus
	CompensationAttempt           int
	CompensationLeaseOwner        string
	CompensationLeaseUntil        *time.Time
	CompensationSourceOperationID string
	CompensationResult            map[string]any
	CompensationErrorCode         string
	CompensationErrorMessage      string
	CompensationUpdatedAt         *time.Time
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}
