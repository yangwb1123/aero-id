package domain

import "time"

// CommandPlan contains only normalized, allow-listed command parameters. It is
// not a resource ACL, authorization grant or a remotely consistent snapshot.
type CommandPlan struct {
	Kind           string                 `json:"kind"`
	Datasets       []AccountDataset       `json:"datasets"`
	OperationType  string                 `json:"operation_type,omitempty"`
	RequestPayload map[string]any         `json:"request_payload,omitempty"`
	Steps          []CommandPlanStep      `json:"steps,omitempty"`
	Callback       *CommandCallbackTarget `json:"callback,omitempty"`
}

// Callback targets are local CAS metadata, not a receiver-effect receipt.
type CommandCallbackTarget struct {
	SubscriptionID  ID    `json:"subscription_id"`
	DeliveryID      ID    `json:"delivery_id"`
	EventID         ID    `json:"event_id"`
	Revision        int64 `json:"revision"`
	LeaseGeneration int64 `json:"lease_generation"`
}

type CommandPlanStep struct {
	StepKey            string         `json:"step_key"`
	Source             string         `json:"source"`
	Action             string         `json:"action"`
	RequestPayload     map[string]any `json:"request_payload"`
	CompensationAction string         `json:"compensation_action,omitempty"`
}

type CommandPrincipal struct {
	ActorUID string `json:"actor_uid"`
	TenantID string `json:"tenant_id"`
	Issuer   string `json:"issuer"`
	Subject  string `json:"subject"`
	ClientID string `json:"client_id"`
}

// These versions describe local metadata only. WriterEpoch is a fencing
// generation, never a projection causal watermark or source operation version.
type CommandPreconditions struct {
	AccountStatus   AccountStatus            `json:"account_status"`
	HomeRegion      string                   `json:"home_region"`
	WriterEpoch     int64                    `json:"writer_epoch"`
	ResolverVersion string                   `json:"resolver_version"`
	Snapshots       []CommandSnapshotVersion `json:"snapshots"`
}

type CommandSnapshotVersion struct {
	ID           ID             `json:"id"`
	Dataset      AccountDataset `json:"dataset"`
	Source       string         `json:"source"`
	SourceRegion string         `json:"source_region"`
	Status       string         `json:"status"`
	Version      int64          `json:"version"`
	GeneratedAt  *time.Time     `json:"generated_at,omitempty"`
	LastSyncedAt *time.Time     `json:"last_synced_at,omitempty"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
}

type CommandIntent struct {
	ID                    ID                   `json:"intent_id"`
	SchemaVersion         int                  `json:"schema_version"`
	AccountID             ID                   `json:"account_id"`
	Principal             CommandPrincipal     `json:"-"`
	IdempotencyKey        string               `json:"-"`
	RequestFingerprint    string               `json:"-"`
	Version               int64                `json:"version"`
	Status                string               `json:"status"`
	Plan                  CommandPlan          `json:"plan"`
	Preconditions         CommandPreconditions `json:"preconditions"`
	ConfirmationDigest    string               `json:"confirmation_digest"`
	Destructive           bool                 `json:"destructive"`
	AuthorizationVersion  string               `json:"authorization_version"`
	ResourceAuthorization string               `json:"resource_authorization"`
	ReceiptKind           string               `json:"receipt_kind,omitempty"`
	ReceiptID             ID                   `json:"receipt_id,omitempty"`
	CreatedAt             time.Time            `json:"created_at"`
	ExpiresAt             time.Time            `json:"expires_at"`
	AbandonedAt           *time.Time           `json:"abandoned_at,omitempty"`
}
