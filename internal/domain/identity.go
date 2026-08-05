package domain

// TokenIdentity is the verified, non-secret identity context carried from
// Snaplink authentication into services and source connectors. Claims are
// intentionally limited to policy context; credentials are never persisted.
type TokenIdentity struct {
	UID      string
	Issuer   string
	Subject  string
	TenantID string
	ClientID string
	Audience []string
}
