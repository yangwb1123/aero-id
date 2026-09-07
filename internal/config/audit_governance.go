package config

import "time"

// AuditGovernanceConfig controls the durable, tenant-bound audit relay. The
// OAuth clients are isolated per tenant and registered only for that tenant's
// derived source; no tenant or source supplied by an API caller is trusted.
type AuditGovernanceConfig struct {
	Enabled            bool   `mapstructure:"enabled"`
	Endpoint           string `mapstructure:"endpoint"`
	SourcePrefix       string `mapstructure:"source_prefix"`
	ActorID            string `mapstructure:"actor_id"`
	SchemaID           string `mapstructure:"schema_id"`
	SchemaVersion      int    `mapstructure:"schema_version"`
	DataClassification string `mapstructure:"data_classification"`
	RetentionClass     string `mapstructure:"retention_class"`
	// AnchorScheme identifies the deterministic event_id derivation contract
	// for audit facts (idgen.Anchor: RFC-4122 v5 UUID over NUL-separated
	// identity parts in the fixed AnchorNamespace
	// 38248e3a-32f0-4929-9b56-b2dbd75aa849; derivation contract
	// derive-stable-deterministic-event-id-from-an-anc §2.1/§2.2 R1-R7). The
	// scheme is load-bearing: every retried mutation relies on the ledger dedup
	// key (tenant_id, source, event_id) being re-derivable, so the declared
	// scheme must match the compiled-in derivation. The namespace and encoding
	// are constant forever: never rotate the namespace and never change the
	// derivation without a scheme-version bump that fails at Load().
	//
	// Three identifiers must never be conflated: the anchored EventID is the
	// ledger dedup key, fact.ID is the ledger row PK (and the governance
	// event_id/idempotency_key), and record_hash is the WORM tamper-evidence
	// chain.
	AnchorScheme          string        `mapstructure:"anchor_scheme"`
	AnchorSchemeVersion   int           `mapstructure:"anchor_scheme_version"`
	Timeout               time.Duration `mapstructure:"timeout"`
	RefreshSkew           time.Duration `mapstructure:"refresh_skew"`
	AllowInsecureLoopback bool          `mapstructure:"allow_insecure_loopback"`
	// PurgePause suspends the dead-letter purge scan in the drain loop
	// (audit_publications and callback_deliveries) so maintenance flows such
	// as cmd/audit-replay cannot race a purge. Static per boot (no hot reload).
	PurgePause bool `mapstructure:"purge_pause"`
	// DeadLetterRetention bounds how long dead-lettered publications and
	// deliveries survive before the drain loop purges them (0 = never purge).
	DeadLetterRetention time.Duration                           `mapstructure:"dead_letter_retention"`
	Bindings            map[string]AuditGovernanceBindingConfig `mapstructure:"bindings"`
}

type AuditGovernanceBindingConfig struct {
	TokenURL        string   `mapstructure:"token_url"`
	ClientID        string   `mapstructure:"client_id"`
	ClientSecretEnv string   `mapstructure:"client_secret_env"`
	Scope           string   `mapstructure:"scope"`
	Resources       []string `mapstructure:"resources"`
}
