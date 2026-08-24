package domain

import (
	"strings"
	"time"
)

// AccountStatus is the local aggregate lifecycle state. Snaplink remains the
// authority for credential and identity lifecycle; this state describes whether
// aero-id will serve the account's control-plane data.
type AccountStatus string

const (
	AccountActive    AccountStatus = "active"
	AccountSuspended AccountStatus = "suspended"
	AccountErased    AccountStatus = "erased"
)

// Account is aero-id's canonical control-plane identity. It contains no
// password, token, MFA secret, or other authentication material.
type Account struct {
	ID           ID
	TenantID     string
	CanonicalUID string
	HomeRegion   string
	Status       AccountStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AccountProfile contains fields explicitly owned by aero-id. Verified identity
// fields such as email, phone, and lifecycle status belong to Snaplink and are
// represented only in source projections.
type AccountProfile struct {
	AccountID   ID
	DisplayName string
	AvatarURL   string
	Locale      string
	Timezone    string
	Preferences map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AccountSource is a reference and freshness record for one source account or
// scoped resource. Source-native identifiers remain opaque strings.
type AccountSource struct {
	ID              ID
	AccountID       ID
	Source          string
	SourceRegion    string
	SourceAccountID string
	ScopeType       string
	ScopeID         string
	Status          string
	Version         int64
	GeneratedAt     *time.Time
	LastSyncedAt    *time.Time
	ErrorCode       string
	Data            map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AccountMembership links an account to a source-owned tenant, workspace, or
// bucket scope.
type AccountMembership struct {
	ID             ID
	AccountID      ID
	Source         string
	SourceRegion   string
	ScopeType      string
	ScopeID        string
	SourceMemberID string
	Role           string
	Status         string
	Version        int64
	LastSyncedAt   *time.Time
	ErrorCode      string
	Data           map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (a *Account) Valid() bool {
	return a != nil && a.ID != (ID{}) && strings.TrimSpace(a.TenantID) != "" && strings.TrimSpace(a.CanonicalUID) != ""
}

func (p *AccountProfile) Normalize() {
	if p == nil {
		return
	}
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.AvatarURL = strings.TrimSpace(p.AvatarURL)
	p.Locale = strings.TrimSpace(p.Locale)
	p.Timezone = strings.TrimSpace(p.Timezone)
}

func (s *AccountSource) Valid() bool {
	return s != nil && s.AccountID != (ID{}) && strings.TrimSpace(s.Source) != "" &&
		strings.TrimSpace(s.SourceAccountID) != ""
}

func (m *AccountMembership) Valid() bool {
	return m != nil && m.AccountID != (ID{}) && strings.TrimSpace(m.Source) != "" &&
		strings.TrimSpace(m.ScopeType) != "" && strings.TrimSpace(m.ScopeID) != "" &&
		strings.TrimSpace(m.SourceMemberID) != ""
}
