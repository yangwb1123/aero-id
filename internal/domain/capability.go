package domain

import "time"

// AccessState describes a control-plane eligibility decision, never a source
// resource ACL or a reusable authorization credential.
type AccessState string

const (
	AccessAllowed AccessState = "allowed"
	AccessDenied  AccessState = "denied"
	AccessUnknown AccessState = "unknown"
)

type Capability struct {
	Name               string      `json:"name"`
	Registered         bool        `json:"registered"`
	Enabled            bool        `json:"enabled"`
	Authorization      AccessState `json:"authorization"`
	AuthorizationScope string      `json:"authorization_scope"`
	Available          bool        `json:"available"`
	ReasonCode         string      `json:"reason_code,omitempty"`
}

type DatasetCapability struct {
	Name          AccountDataset `json:"name"`
	Source        string         `json:"source"`
	PII           bool           `json:"pii"`
	Registered    bool           `json:"registered"`
	Enabled       bool           `json:"enabled"`
	Authorization AccessState    `json:"authorization"`
	// Resource authorization remains at the source, not in this local report.
	ResourceAuthorization AccessState    `json:"resource_authorization"`
	Status                SnapshotStatus `json:"status"`
	Fresh                 bool           `json:"fresh"`
	Version               int64          `json:"version"`
	GeneratedAt           *time.Time     `json:"generated_at,omitempty"`
	LastSyncedAt          *time.Time     `json:"last_synced_at,omitempty"`
	ReasonCode            string         `json:"reason_code,omitempty"`
}

type CapabilityReport struct {
	SchemaVersion int       `json:"schema_version"`
	AccountID     ID        `json:"account_id"`
	TenantID      string    `json:"tenant_id"`
	ClientID      string    `json:"client_id"`
	Issuer        string    `json:"issuer"`
	HomeRegion    string    `json:"home_region"`
	LocalRegion   string    `json:"local_region"`
	GeneratedAt   time.Time `json:"generated_at"`
	// The checked Snaplink Check contract does not supply a policy version.
	// Reauthorize commands; do not manufacture a version from a local hash.
	AuthorizationVersion string              `json:"authorization_version"`
	Partial              bool                `json:"partial"`
	Capabilities         []Capability        `json:"capabilities"`
	Datasets             []DatasetCapability `json:"datasets"`
}
