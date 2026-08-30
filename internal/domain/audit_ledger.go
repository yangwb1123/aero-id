package domain

import "time"

type AuditFact struct {
	ID               ID
	HashVersion      int
	ChainSequence    int64
	Source           string
	SourceRegion     string
	PartitionKey     string
	EventID          ID
	OperationID      ID
	CorrelationID    ID
	CausationID      ID
	TraceID          string
	RequestID        string
	ActorUID         string
	AccountID        ID
	TenantID         string
	Action           string
	ResourceType     string
	ResourceID       string
	OccurredAt       time.Time
	Payload          map[string]any
	PayloadHash      string
	PreviousHash     string
	RecordHash       string
	SignatureVersion int
	Signature        string
	CreatedAt        time.Time
}

type AuditFactFilter struct {
	Source       string
	ActorUID     string
	RequestID    string
	AccountID    ID
	TenantID     string
	Action       string
	ResourceType string
	OperationID  ID
	From         *time.Time
	To           *time.Time
	Page         Page
}

type AuditPublication struct {
	ID      ID
	Fact    *AuditFact
	Status  string
	Attempt int
	// LeaseGeneration changes on every claim and fences stale finishers,
	// including a reclaim by the same worker ID.
	LeaseGeneration int64
	LeaseOwner      string
	LeaseUntil      *time.Time
	NextAttemptAt   time.Time
	ErrorCode       string
}
