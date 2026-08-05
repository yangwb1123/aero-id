package config

import "time"

// BillingConfig points at Snaplink's tenantless M2M metering API. Bindings are
// indexed by the verified tenant claim; callers cannot choose credentials by
// body, query, or forwarding header.
type BillingConfig struct {
	Enabled                bool                            `mapstructure:"enabled"`
	Endpoint               string                          `mapstructure:"endpoint"`
	Timeout                time.Duration                   `mapstructure:"timeout"`
	MaxBodyBytes           int64                           `mapstructure:"max_body_bytes"`
	MaxProjectionStaleness time.Duration                   `mapstructure:"max_projection_staleness"`
	AccountFeature         string                          `mapstructure:"account_feature"`
	UserDimension          string                          `mapstructure:"user_dimension"`
	ReservationTTL         time.Duration                   `mapstructure:"reservation_ttl"`
	AllowInsecureLoopback  bool                            `mapstructure:"allow_insecure_loopback"`
	Bindings               map[string]BillingBindingConfig `mapstructure:"bindings"`
}

type BillingBindingConfig struct {
	TokenURL        string   `mapstructure:"token_url"`
	ClientID        string   `mapstructure:"client_id"`
	ClientSecretEnv string   `mapstructure:"client_secret_env"`
	Scope           string   `mapstructure:"scope"`
	Resources       []string `mapstructure:"resources"`
}
