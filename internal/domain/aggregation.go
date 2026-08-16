package domain

import "time"

// SnapshotStatus describes the state of an account dataset projection.
type SnapshotStatus string

const (
	SnapshotFresh       SnapshotStatus = "fresh"
	SnapshotStale       SnapshotStatus = "stale"
	SnapshotUnavailable SnapshotStatus = "unavailable"
	snapshotUnknown     SnapshotStatus = "unknown" // persisted/transmitted via SnapshotStatus casts
)

func (s SnapshotStatus) Valid() bool {
	return s == SnapshotFresh || s == SnapshotStale || s == SnapshotUnavailable || s == snapshotUnknown
}

// ConsistencyMode controls whether an aggregation reads local projections or
// asks source connectors for a bounded/strong refresh.
type ConsistencyMode string

const (
	ConsistencyEventual ConsistencyMode = "eventual"
	ConsistencyBounded  ConsistencyMode = "bounded"
	ConsistencyStrong   ConsistencyMode = "strong"
)

// AccountDataset is the allow-listed public dataset registry. New source data
// must be added here deliberately; callers cannot proxy arbitrary source paths.
type AccountDataset string

// Dataset values are the registry's public contract: they are persisted in
// rows, carried in event payloads, and converted from strings by callers. The
// symbolic names are private because callers consume the values via casts and
// the registry functions; only DatasetAeroIMActivitySummary is exported (it is
// referenced directly by handlers).
const (
	datasetSnaplinkIdentity      AccountDataset = "snaplink.identity"
	datasetSnaplinkTenants       AccountDataset = "snaplink.tenants"
	datasetSnaplinkPermissions   AccountDataset = "snaplink.permissions"
	datasetSnaplinkSecurity      AccountDataset = "snaplink.security"
	datasetAeroIMProfile         AccountDataset = "aero-im.profile"
	datasetAeroIMWorkspaces      AccountDataset = "aero-im.workspaces"
	DatasetAeroIMActivitySummary AccountDataset = "aero-im.activity_summary"
	datasetAeroVaultTenants      AccountDataset = "aero-vault.tenants"
	datasetAeroVaultBuckets      AccountDataset = "aero-vault.buckets"
	datasetAeroVaultUsage        AccountDataset = "aero-vault.usage"
)

type datasetDescriptor struct {
	Name   AccountDataset
	Source string
	PII    bool
}

var accountDatasetRegistry = []datasetDescriptor{
	{Name: datasetSnaplinkIdentity, Source: "snaplink", PII: true},
	{Name: datasetSnaplinkTenants, Source: "snaplink"},
	{Name: datasetSnaplinkPermissions, Source: "snaplink"},
	{Name: datasetSnaplinkSecurity, Source: "snaplink", PII: true},
	{Name: datasetAeroIMProfile, Source: "aero-im"},
	{Name: datasetAeroIMWorkspaces, Source: "aero-im"},
	{Name: DatasetAeroIMActivitySummary, Source: "aero-im"},
	{Name: datasetAeroVaultTenants, Source: "aero-vault"},
	{Name: datasetAeroVaultBuckets, Source: "aero-vault"},
	{Name: datasetAeroVaultUsage, Source: "aero-vault"},
}

func AccountDatasetRegistry() []datasetDescriptor {
	result := make([]datasetDescriptor, len(accountDatasetRegistry))
	copy(result, accountDatasetRegistry)
	return result
}

func DatasetDescriptorOf(dataset AccountDataset) (datasetDescriptor, bool) {
	for _, descriptor := range accountDatasetRegistry {
		if descriptor.Name == dataset {
			return descriptor, true
		}
	}
	return datasetDescriptor{}, false
}

type AccountSnapshot struct {
	ID           ID
	AccountID    ID
	Source       string
	SourceRegion string
	Dataset      AccountDataset
	Status       SnapshotStatus
	Version      int64
	GeneratedAt  time.Time
	LastSyncedAt *time.Time
	ExpiresAt    *time.Time
	ErrorCode    string
	Data         map[string]any
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s *AccountSnapshot) Normalize() {
	if s == nil {
		return
	}
	if s.Data == nil {
		s.Data = map[string]any{}
	}
	if s.Status == "" {
		s.Status = snapshotUnknown
	}
}

func (s *AccountSnapshot) Fresh(now time.Time) bool {
	if s == nil || s.Status != SnapshotFresh {
		return false
	}
	return s.ExpiresAt == nil || now.Before(*s.ExpiresAt)
}

type AggregateOverview struct {
	Account       *Account
	Profile       *AccountProfile
	Snapshots     []*AccountSnapshot
	Consistency   ConsistencyMode
	Partial       bool
	GeneratedAt   time.Time
	SourceErrors  map[string]string
	StaleDatasets []AccountDataset
}
