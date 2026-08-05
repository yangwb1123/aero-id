package domain

import "time"

// SnapshotStatus describes the state of an account dataset projection.
type SnapshotStatus string

const (
	SnapshotFresh       SnapshotStatus = "fresh"
	SnapshotStale       SnapshotStatus = "stale"
	SnapshotUnavailable SnapshotStatus = "unavailable"
	SnapshotUnknown     SnapshotStatus = "unknown"
)

func (s SnapshotStatus) Valid() bool {
	return s == SnapshotFresh || s == SnapshotStale || s == SnapshotUnavailable || s == SnapshotUnknown
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

const (
	DatasetSnaplinkIdentity      AccountDataset = "snaplink.identity"
	DatasetSnaplinkTenants       AccountDataset = "snaplink.tenants"
	DatasetSnaplinkPermissions   AccountDataset = "snaplink.permissions"
	DatasetSnaplinkSecurity      AccountDataset = "snaplink.security"
	DatasetAeroIMProfile         AccountDataset = "aero-im.profile"
	DatasetAeroIMWorkspaces      AccountDataset = "aero-im.workspaces"
	DatasetAeroIMActivitySummary AccountDataset = "aero-im.activity_summary"
	DatasetAeroVaultTenants      AccountDataset = "aero-vault.tenants"
	DatasetAeroVaultBuckets      AccountDataset = "aero-vault.buckets"
	DatasetAeroVaultUsage        AccountDataset = "aero-vault.usage"
)

type DatasetDescriptor struct {
	Name   AccountDataset
	Source string
	PII    bool
}

var accountDatasetRegistry = []DatasetDescriptor{
	{Name: DatasetSnaplinkIdentity, Source: "snaplink", PII: true},
	{Name: DatasetSnaplinkTenants, Source: "snaplink"},
	{Name: DatasetSnaplinkPermissions, Source: "snaplink"},
	{Name: DatasetSnaplinkSecurity, Source: "snaplink", PII: true},
	{Name: DatasetAeroIMProfile, Source: "aero-im"},
	{Name: DatasetAeroIMWorkspaces, Source: "aero-im"},
	{Name: DatasetAeroIMActivitySummary, Source: "aero-im"},
	{Name: DatasetAeroVaultTenants, Source: "aero-vault"},
	{Name: DatasetAeroVaultBuckets, Source: "aero-vault"},
	{Name: DatasetAeroVaultUsage, Source: "aero-vault"},
}

func AccountDatasetRegistry() []DatasetDescriptor {
	result := make([]DatasetDescriptor, len(accountDatasetRegistry))
	copy(result, accountDatasetRegistry)
	return result
}

func DatasetDescriptorOf(dataset AccountDataset) (DatasetDescriptor, bool) {
	for _, descriptor := range accountDatasetRegistry {
		if descriptor.Name == dataset {
			return descriptor, true
		}
	}
	return DatasetDescriptor{}, false
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
		s.Status = SnapshotUnknown
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
