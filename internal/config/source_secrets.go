package config

import (
	"errors"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// rejectSourceSecretConfig keeps credentials out of versioned YAML. Viper
// merges environment values before decoding, so origin must be checked against
// the file itself rather than the merged Config value.
func rejectSourceSecretConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("read config: configuration file is unavailable")
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return errors.New("unmarshal config: configuration contains an invalid or unsupported field")
	}
	sources, ok := document["sources"].(map[string]any)
	if !ok {
		return nil
	}
	for _, name := range []string{"snaplink", "aero_im", "aero_vault"} {
		value, ok := sources[name].(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"bearer_token", "oauth_client_secret"} {
			if sourceSecretPresent(value[field]) {
				return errors.New("sources." + name + "." + field + " must be supplied by an environment or approved secret provider")
			}
		}
	}
	return nil
}

func sourceSecretPresent(value any) bool {
	if value == nil {
		return false
	}
	text, ok := value.(string)
	if !ok {
		return true
	}
	return strings.TrimSpace(text) != ""
}

// applySourceEnvironmentOverrides handles list-valued environment settings.
// Viper's weak decoder cannot safely turn a comma-separated environment value
// into []string under UnmarshalExact, so the conversion is explicit here.
func applySourceEnvironmentOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	for _, item := range []struct {
		name   string
		values *[]string
	}{
		{"SNAPLINK", &cfg.Sources.Snaplink.OAuthAllowedHosts},
		{"AERO_IM", &cfg.Sources.AeroIM.OAuthAllowedHosts},
		{"AERO_VAULT", &cfg.Sources.AeroVault.OAuthAllowedHosts},
	} {
		if raw, ok := os.LookupEnv("AERO_ID_SOURCES_" + item.name + "_OAUTH_ALLOWED_HOSTS"); ok {
			*item.values = splitSourceList(raw)
		}
	}
}

func splitSourceList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
