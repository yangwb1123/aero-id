package config

import (
	"strings"
	"testing"
)

func TestValidateSnaplinkCapabilitiesMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*SourceEndpointConfig)
		wantErr string
	}{
		{name: "disabled default"},
		{name: "erase path without capability", mutate: func(value *SourceEndpointConfig) {
			value.ErasePath = "/erase"
		}, wantErr: "sources.snaplink.erase_path"},
		{name: "erase without path", mutate: func(value *SourceEndpointConfig) {
			value.Capabilities.Erase = true
		}, wantErr: "sources.snaplink.capabilities.erase"},
		{name: "status path without capability", mutate: func(value *SourceEndpointConfig) {
			value.StatusPath = "/status"
		}, wantErr: "sources.snaplink.status_path"},
		{name: "status without path", mutate: func(value *SourceEndpointConfig) {
			value.Capabilities.OperationStatus = true
		}, wantErr: "sources.snaplink.capabilities.operation_status"},
		{name: "version without erase", mutate: func(value *SourceEndpointConfig) {
			value.Capabilities.EraseContractVersion = "test-v1"
		}, wantErr: "erase_contract_version"},
		{name: "erase without version", mutate: func(value *SourceEndpointConfig) {
			value.Capabilities.Summary = true
			value.Capabilities.Erase = true
			value.ErasePath = "/erase"
			value.Endpoint = "https://snaplink.example"
		}, wantErr: "recognized erase_contract_version"},
		{name: "unknown version", mutate: func(value *SourceEndpointConfig) {
			value.Capabilities.Summary = true
			value.Capabilities.Erase = true
			value.Capabilities.EraseContractVersion = "owner-contract-not-verified"
			value.ErasePath = "/erase"
			value.Endpoint = "https://snaplink.example"
		}, wantErr: "erase_contract_version"},
		{name: "endpoint without summary", mutate: func(value *SourceEndpointConfig) {
			value.Endpoint = "https://snaplink.example"
		}, wantErr: "capabilities.summary"},
		{name: "summary without endpoint", mutate: func(value *SourceEndpointConfig) {
			value.Capabilities.Summary = true
		}, wantErr: "capabilities require a non-empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := SourceEndpointConfig{}
			if tc.mutate != nil {
				tc.mutate(&value)
			}
			err := validateSourceEndpoint("snaplink", value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validation error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validation error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadSnaplinkCapabilitiesEnvironmentOverrides(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_SNAPLINK_ALLOW_INSECURE_LOOPBACK", "true")
	t.Setenv("AERO_ID_SOURCES_SNAPLINK_CAPABILITIES_SUMMARY", "true")
	content := sample + `
sources:
  snaplink:
    endpoint: "http://127.0.0.1:8080"
    region: local
    capabilities: {summary: false, erase: false, operation_status: false, erase_contract_version: ""}
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v, want environment override to pass", err)
	}
	if !cfg.Sources.Snaplink.Capabilities.Summary {
		t.Fatal("Snaplink summary capability = false, want environment override true")
	}
}

func TestLoadRejectsDormantSnaplinkCommandPaths(t *testing.T) {
	for _, path := range []string{"erase_path: /erase", "status_path: /status"} {
		t.Run(path, func(t *testing.T) {
			content := sample + "\nsources:\n  snaplink:\n    endpoint: \"\"\n    " + path + "\n"
			_, err := Load(writeTemp(t, content))
			if err == nil || !strings.Contains(err.Error(), "sources.snaplink") {
				t.Fatalf("Load error = %v, want dormant path rejection", err)
			}
		})
	}
}

func TestLoadRejectsCapabilityFieldsOnOtherSources(t *testing.T) {
	content := sample + `
sources:
  aero_im:
    capabilities: {summary: true}
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "capabilities.summary") {
		t.Fatalf("Load error = %v, want non-Snaplink capability rejection", err)
	}
}
