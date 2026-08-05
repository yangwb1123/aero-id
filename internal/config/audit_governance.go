package config

import "time"

// AuditGovernanceConfig controls the durable, tenant-bound audit relay. The
// OAuth clients are isolated per tenant and registered only for that tenant's
// derived source; no tenant or source supplied by an API caller is trusted.
type AuditGovernanceConfig struct {
	Enabled               bool                                    `mapstructure:"enabled"`
	Endpoint              string                                  `mapstructure:"endpoint"`
	SourcePrefix          string                                  `mapstructure:"source_prefix"`
	ActorID               string                                  `mapstructure:"actor_id"`
	SchemaID              string                                  `mapstructure:"schema_id"`
	SchemaVersion         int                                     `mapstructure:"schema_version"`
	DataClassification    string                                  `mapstructure:"data_classification"`
	RetentionClass        string                                  `mapstructure:"retention_class"`
	Timeout               time.Duration                           `mapstructure:"timeout"`
	RefreshSkew           time.Duration                           `mapstructure:"refresh_skew"`
	AllowInsecureLoopback bool                                    `mapstructure:"allow_insecure_loopback"`
	Bindings              map[string]AuditGovernanceBindingConfig `mapstructure:"bindings"`
}

type AuditGovernanceBindingConfig struct {
	TokenURL        string   `mapstructure:"token_url"`
	ClientID        string   `mapstructure:"client_id"`
	ClientSecretEnv string   `mapstructure:"client_secret_env"`
	Scope           string   `mapstructure:"scope"`
	Resources       []string `mapstructure:"resources"`
}
