package domain

// TokenIdentity is the verified, non-secret identity context carried from
// Snaplink authentication into services and source connectors. Claims are
// intentionally limited to policy context; credentials are never persisted.
type TokenIdentity struct {
	// Verified is set only by a claims-capable token verifier. It prevents
	// source delegation from treating caller-constructed identity data as
	// proof of a verified delegation claim.
	Verified bool
	UID      string
	Issuer   string
	Subject  string
	TenantID string
	ClientID string
	Audience []string
	// Scopes are parsed from the signed OAuth scope claim. They are carried as
	// optional authorization context; routes that require a scope enforce it
	// explicitly rather than changing legacy route behavior globally.
	Scopes []string
	// AllowedTenants is a verified delegation claim for service identities.
	// It is intentionally distinct from TenantID: a service may act for more
	// than one tenant only when Snaplink (or an equivalent trusted verifier)
	// has authenticated that delegation.
	AllowedTenants []string
}
