package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsUnauthenticatedAeroIMWithoutSecrets(t *testing.T) {
	content := sample + `
sources:
  aero_im:
    endpoint: "https://im.example"
    region: local
    bearer_token: "do-not-echo-this-token"
`
	content = strings.Replace(content, `bearer_token: "do-not-echo-this-token"`, `bearer_token: ""`, 1)
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "sources.aero-im service authentication is required") {
		t.Fatalf("Load error = %v, want fail-closed Aero IM authentication error", err)
	}
	if strings.Contains(err.Error(), "do-not-echo-this-token") {
		t.Fatalf("Load error disclosed an authentication secret: %v", err)
	}
}

func TestLoadSourceWorkloadConcurrencyEnvironmentOverrides(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_SNAPLINK_QUERY_MAX_CONCURRENCY", "7")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_COMMAND_MAX_CONCURRENCY", "5")
	content := sample + `
sources:
  snaplink:
    query_max_concurrency: 1
  aero_im:
    command_max_concurrency: 1
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.Sources.Snaplink.QueryMaxConcurrency != 7 {
		t.Fatalf("Snaplink.QueryMaxConcurrency = %d, want 7", cfg.Sources.Snaplink.QueryMaxConcurrency)
	}
	if cfg.Sources.AeroIM.CommandMaxConcurrency != 5 {
		t.Fatalf("AeroIM.CommandMaxConcurrency = %d, want 5", cfg.Sources.AeroIM.CommandMaxConcurrency)
	}
}

func TestLoadRejectsUnauthenticatedSnaplinkWithoutSecrets(t *testing.T) {
	content := sample + `
sources:
  snaplink:
    endpoint: "https://snaplink.example"
    region: local
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "sources.snaplink service authentication is required") {
		t.Fatalf("Load error = %v, want fail-closed Snaplink authentication error", err)
	}
}

func TestLoadSnaplinkLoopbackCompatibilityUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_SNAPLINK_ALLOW_INSECURE_LOOPBACK", "true")
	content := sample + `
sources:
  snaplink:
    endpoint: "http://127.0.0.1:8080"
    region: local
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v, want loopback compatibility to pass", err)
	}
	if !cfg.Sources.Snaplink.AllowInsecureLoopback {
		t.Fatal("Snaplink.AllowInsecureLoopback = false, want environment override true")
	}
}

func TestLoadAeroIMLoopbackCompatibilityUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_IM_ALLOW_INSECURE_LOOPBACK", "true")
	content := sample + `
sources:
  aero_im:
    endpoint: "http://127.0.0.1:8080"
    region: local
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v, want loopback compatibility override to pass", err)
	}
	if !cfg.Sources.AeroIM.AllowInsecureLoopback {
		t.Fatal("AeroIM.AllowInsecureLoopback = false, want environment override true")
	}
}
