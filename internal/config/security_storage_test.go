package config

import (
	"strings"
	"testing"
)

func secureProductionConfig() Config {
	return Config{
		App:      AppConfig{Env: "production"},
		Database: DatabaseConfig{SSLMode: "require"},
		SSO: SSOConfig{
			Insecure:            false,
			AuthZRequired:       true,
			AuthZLegacyFallback: false,
		},
	}
}

func TestValidateEnvironmentSecurity(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"secure production", nil, ""},
		{"secure staging", func(c *Config) { c.App.Env = "staging" }, ""},
		{"development keeps compatibility defaults", func(c *Config) {
			c.App.Env = "dev"
			c.SSO.Insecure = true
			c.SSO.AuthZLegacyFallback = true
			c.Database.SSLMode = "disable"
		}, ""},
		{"insecure sso", func(c *Config) { c.SSO.Insecure = true }, "sso.insecure"},
		{"authz not required", func(c *Config) { c.SSO.AuthZRequired = false }, "sso.authz_required"},
		{"legacy authz fallback", func(c *Config) { c.SSO.AuthZLegacyFallback = true }, "sso.authz_legacy_fallback"},
		{"database tls disabled", func(c *Config) { c.Database.SSLMode = "disable" }, "database.sslmode"},
		{"signed events require timestamp", func(c *Config) {
			c.EventIngest.SigningKey = strings.Repeat("s", 32)
		}, "event_ingest.require_timestamp"},
		{"signed events with timestamp", func(c *Config) {
			c.EventIngest.SigningKey = strings.Repeat("s", 32)
			c.EventIngest.RequireTimestamp = true
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := secureProductionConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := cfg.validateEnvironmentSecurity()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateStorageContract(t *testing.T) {
	cases := []struct {
		name    string
		value   StorageConfig
		wantErr string
	}{
		{"local", StorageConfig{Type: "local", Local: LocalStorageConfig{Root: "/var/lib/aero-id/files"}}, ""},
		{"s3 with workload identity", StorageConfig{Type: "s3", S3: S3StorageConfig{Region: "us-east-1", Bucket: "aero-id"}}, ""},
		{"local root required", StorageConfig{Type: "local"}, "storage.local.root"},
		{"s3 region required", StorageConfig{Type: "s3", S3: S3StorageConfig{Bucket: "aero-id"}}, "storage.s3.region"},
		{"s3 bucket required", StorageConfig{Type: "s3", S3: S3StorageConfig{Region: "us-east-1"}}, "storage.s3.bucket"},
		{"s3 credentials are paired", StorageConfig{Type: "s3", S3: S3StorageConfig{Region: "us-east-1", Bucket: "aero-id", AccessKey: "access"}}, "access_key"},
		{"remote http endpoint rejected", StorageConfig{Type: "s3", S3: S3StorageConfig{Region: "us-east-1", Bucket: "aero-id", Endpoint: "http://object.example"}}, "must use https"},
		{"local http endpoint accepted", StorageConfig{Type: "s3", S3: S3StorageConfig{Region: "us-east-1", Bucket: "aero-id", Endpoint: "http://localhost:9000"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStorage(tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
