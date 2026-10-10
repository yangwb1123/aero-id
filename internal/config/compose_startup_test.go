package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeUsesShippedMigrationRunnerAndSharedClaimsEnvironment(t *testing.T) {
	data, err := os.ReadFile("../../deployments/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Entrypoint  []string          `yaml:"entrypoint"`
			Command     yaml.Node         `yaml:"command"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatal(err)
	}
	migration := compose.Services["migrate"]
	var command []string
	if err := migration.Command.Decode(&command); err != nil {
		t.Fatal(err)
	}
	if migration.Image != "" || len(migration.Entrypoint) != 1 || migration.Entrypoint[0] != "/app/migrate" ||
		len(command) == 0 || command[len(command)-1] != "up" {
		t.Fatal("Compose must use the shipped per-version migration runner, not golang-migrate bookkeeping")
	}
	for _, service := range []string{"migrate", "aero-id"} {
		environment := compose.Services[service].Environment
		for _, key := range []string{"AERO_ID_SSO_ENDPOINT", "AERO_ID_SSO_JWKS_URL", "AERO_ID_SSO_ISSUER",
			"AERO_ID_SSO_AUDIENCE", "AERO_ID_SSO_AUTHZ_CLIENT_ID", "AERO_ID_DATABASE_PASSWORD",
			"AERO_ID_SIGNING_KEY", "AERO_ID_AUDIT_LEDGER_SIGNING_KEY"} {
			if !strings.Contains(environment[key], ":?") {
				t.Fatalf("%s does not require %s before startup", service, key)
			}
		}
		if environment["AERO_ID_ACCOUNT_ISSUER"] != environment["AERO_ID_SSO_ISSUER"] ||
			environment["AERO_ID_SSO_AUTHZ_REQUIRED"] != "true" || environment["AERO_ID_SSO_AUTHZ_LEGACY_FALLBACK"] != "false" {
			t.Fatalf("%s has inconsistent issuer or fail-open global authorization", service)
		}
	}
}

func TestLocalControlPlaneEnvironmentLoadsClaimsAndExactBrowserOrigin(t *testing.T) {
	for name, value := range map[string]string{
		"AERO_ID_DATABASE_PASSWORD": "test-only-db-password",
		"AERO_ID_SIGNING_KEY":       strings.Repeat("a", 64), "AERO_ID_AUDIT_LEDGER_SIGNING_KEY": strings.Repeat("b", 64),
		"AERO_ID_SSO_ENDPOINT": "snaplink.example.test:443", "AERO_ID_SSO_INSECURE": "false",
		"AERO_ID_SSO_JWKS_URL": "https://snaplink.example.test/.well-known/jwks.json",
		"AERO_ID_SSO_ISSUER":   "https://snaplink.example.test", "AERO_ID_ACCOUNT_ISSUER": "https://snaplink.example.test",
		"AERO_ID_SSO_AUDIENCE": "aero-id", "AERO_ID_SSO_AUTHZ_CLIENT_ID": "aero-id",
		"AERO_ID_SSO_AUTHZ_REQUIRED": "true", "AERO_ID_SSO_AUTHZ_LEGACY_FALLBACK": "false",
		"AERO_ID_SERVER_HTTP_CORS_ORIGINS": "http://localhost:5180",
	} {
		t.Setenv(name, value)
	}
	cfg, err := Load("../../configs/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSO.JWKSURL == "" || cfg.SSO.Audience != "aero-id" || cfg.Account.Issuer != cfg.SSO.Issuer ||
		!cfg.SSO.AuthZRequired || cfg.SSO.AuthZLegacyFallback || len(cfg.Server.HTTP.CORSOrigins) != 1 {
		t.Fatal("local environment did not preserve the verified control-plane configuration")
	}
}
