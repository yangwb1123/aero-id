package domain

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidEntitlement = errors.New("invalid entitlement projection")

// LimitGrant makes zero-capacity distinct from unlimited capacity.
type LimitGrant struct {
	Soft      int64 `json:"soft"`
	Hard      int64 `json:"hard"`
	Unlimited bool  `json:"unlimited,omitempty"`
}

func (grant LimitGrant) Validate() error {
	if grant.Soft < 0 || grant.Hard < 0 {
		return ErrInvalidEntitlement
	}
	if grant.Unlimited && (grant.Soft != 0 || grant.Hard != 0) {
		return ErrInvalidEntitlement
	}
	if !grant.Unlimited && grant.Soft > grant.Hard {
		return ErrInvalidEntitlement
	}
	return nil
}

type PlanRef struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

// EntitlementProjection is a durable copy of Snaplink's versioned tenant
// entitlement. It is safe for read-path fallback; mutations still use the
// central reservation API so hard limits remain globally atomic.
type EntitlementProjection struct {
	TenantID       string                `json:"tenant_id"`
	SubscriptionID string                `json:"subscription_id"`
	Plan           PlanRef               `json:"plan"`
	Revision       uint64                `json:"revision"`
	Active         bool                  `json:"active"`
	Features       map[string]bool       `json:"features"`
	Limits         map[string]LimitGrant `json:"limits"`
	EffectiveAt    time.Time             `json:"effective_at"`
	ExpiresAt      time.Time             `json:"expires_at,omitempty"`
	GeneratedAt    time.Time             `json:"generated_at"`
	FetchedAt      time.Time             `json:"fetched_at,omitempty"`
}

func (projection *EntitlementProjection) Validate(expectedTenant string) error {
	if projection == nil || !projection.validHeader(expectedTenant) {
		return ErrInvalidEntitlement
	}
	for key, grant := range projection.Limits {
		if strings.TrimSpace(key) == "" || grant.Validate() != nil {
			return ErrInvalidEntitlement
		}
	}
	return nil
}

func (projection *EntitlementProjection) validHeader(expectedTenant string) bool {
	return strings.TrimSpace(projection.TenantID) != "" && projection.TenantID == expectedTenant &&
		projection.SubscriptionID != "" && projection.Plan.ID != "" && projection.Plan.Version > 0 &&
		projection.Revision > 0 && !projection.EffectiveAt.IsZero() && !projection.GeneratedAt.IsZero()
}

func (projection *EntitlementProjection) Effective(at time.Time) bool {
	if projection == nil || !projection.Active || at.Before(projection.EffectiveAt) {
		return false
	}
	return projection.ExpiresAt.IsZero() || at.Before(projection.ExpiresAt)
}

func (projection *EntitlementProjection) Limit(dimension string, at time.Time) (LimitGrant, bool) {
	if !projection.Effective(at) {
		return LimitGrant{}, false
	}
	grant, ok := projection.Limits[dimension]
	return grant, ok
}

func (projection *EntitlementProjection) FeatureEnabled(feature string, at time.Time) bool {
	return projection.Effective(at) && projection.Features[feature]
}
