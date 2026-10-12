package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Load reads and validates one immutable process configuration. Secrets may
// arrive through bound environment variables, but never from the YAML file.
func Load(path string) (*Config, error) {
	loader, err := newConfigLoader(path)
	if err != nil {
		return nil, err
	}
	cfg, err := decodeConfig(loader, path)
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Replication.MaxTerminalAttempts == 0 {
		cfg.Replication.MaxTerminalAttempts = DefaultReplicationMaxTerminalAttempts
	}
	return cfg, nil
}

func newConfigLoader(path string) (*viper.Viper, error) {
	loader := viper.New()
	loader.SetConfigFile(path)
	loader.SetConfigType("yaml")
	loader.SetEnvPrefix("AERO_ID")
	loader.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	loader.AutomaticEnv()
	if err := bindConfigEnvironment(loader); err != nil {
		return nil, err
	}
	return loader, nil
}

func bindConfigEnvironment(loader *viper.Viper) error {
	for _, key := range configEnvironmentKeys() {
		if err := loader.BindEnv(key); err != nil {
			return fmt.Errorf("bind source environment: %w", err)
		}
	}
	return nil
}

func configEnvironmentKeys() []string {
	keys := []string{
		"sources.aero_im.allow_insecure_loopback", "sources.snaplink.allow_insecure_loopback",
		"sources.aero_vault.allow_insecure_loopback", "sources.aero_vault.capabilities.erase",
		"sources.aero_vault.capabilities.operation_status", "sources.snaplink.capabilities.summary",
		"sources.snaplink.capabilities.erase", "sources.snaplink.capabilities.operation_status",
		"sources.snaplink.capabilities.erase_contract_version", "replication.max_terminal_attempts",
		"callback.master_key", "callback.previous_master_keys",
		"callback.secret_envelope_version", "callback.delivery_paused",
	}
	for _, source := range []string{"snaplink", "aero_im", "aero_vault"} {
		for _, setting := range []string{"bearer_token", "oauth_token_url", "oauth_client_id", "oauth_client_secret",
			"oauth_scope", "oauth_resource", "oauth_tls_ca_file", "oauth_tls_cert_file", "oauth_tls_key_file",
			"oauth_tls_server_name", "oauth_tenant_id"} {
			keys = append(keys, "sources."+source+"."+setting)
		}
	}
	for _, source := range []string{"snaplink", "aero_im", "aero_vault"} {
		for _, setting := range []string{"query_max_concurrency", "command_max_concurrency", "health_max_concurrency"} {
			keys = append(keys, "sources."+source+"."+setting)
		}
	}
	return keys
}

func decodeConfig(loader *viper.Viper, path string) (*Config, error) {
	if err := loader.ReadInConfig(); err != nil {
		return nil, errors.New("read config: configuration file is unavailable")
	}
	if err := rejectSourceSecretConfig(path); err != nil {
		return nil, err
	}
	var cfg Config
	// Do not return mapstructure's raw type error: decoder text can contain a
	// secret, path, or URL. Unknown keys are still rejected by UnmarshalExact.
	if err := loader.UnmarshalExact(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", redactedUnmarshalError(err))
	}
	applySourceEnvironmentOverrides(&cfg)
	return &cfg, nil
}
