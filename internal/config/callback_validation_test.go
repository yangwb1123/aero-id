package config

import (
	"strings"
	"testing"
)

const testCallbackMasterKey = "callback-test-master-key-0123456789abcdef"

func TestValidateCallbackPrivateTargetPolicy(t *testing.T) {
	for _, environment := range []string{"dev", "test"} {
		if err := validateCallbackPolicy(CallbackConfig{AllowPrivateTarget: true}, environment); err != nil {
			t.Errorf("private callback target rejected in %q: %v", environment, err)
		}
	}
	for _, environment := range []string{"", "qa", "prod", "production", "staging", "future-env"} {
		err := validateCallbackPolicy(CallbackConfig{MasterKey: testCallbackMasterKey, AllowPrivateTarget: true}, environment)
		if err == nil || !strings.Contains(err.Error(), "callback.allow_private_target") {
			t.Errorf("private callback target accepted in %q: %v", environment, err)
		}
	}
	if err := validateCallbackPolicy(CallbackConfig{MasterKey: testCallbackMasterKey}, "production"); err != nil {
		t.Fatalf("default public-only callback policy rejected: %v", err)
	}
}

func TestValidateCallbackMasterKeys(t *testing.T) {
	cases := []struct {
		name        string
		config      CallbackConfig
		environment string
		wantErr     string
	}{
		{"empty nonproduction", CallbackConfig{}, "dev", ""},
		{"empty production", CallbackConfig{}, "prod", "callback.master_key is required"},
		{"explicit production pause", CallbackConfig{DeliveryPaused: true}, "production", ""},
		{"short paused key", CallbackConfig{DeliveryPaused: true, MasterKey: "short"}, "production", "at least 32 bytes"},
		{"negative envelope version", CallbackConfig{SecretEnvelopeVersion: -1}, "dev", "callback.secret_envelope_version"},
		{"unsupported envelope version", CallbackConfig{SecretEnvelopeVersion: 2}, "dev", "callback.secret_envelope_version"},
		{"versioned rotation", CallbackConfig{MasterKey: testCallbackMasterKey, SecretEnvelopeVersion: 1}, "prod", ""},
		{"short active key", CallbackConfig{MasterKey: "too-short"}, "dev", "at least 32 bytes"},
		{"padded key", CallbackConfig{MasterKey: " " + testCallbackMasterKey}, "dev", "canonical"},
		{"comma in key", CallbackConfig{MasterKey: testCallbackMasterKey + ","}, "dev", "without commas"},
		{"control in key", CallbackConfig{MasterKey: testCallbackMasterKey + "\x01"}, "dev", "printable ASCII"},
		{"oversized key", CallbackConfig{MasterKey: strings.Repeat("k", maxCallbackMasterKeyBytes+1)}, "dev", "maximum"},
		{"too many prior keys", CallbackConfig{MasterKey: testCallbackMasterKey, PreviousMasterKeys: make([]string, maxCallbackPreviousKeys+1)}, "dev", "more than 4"},
		{"short prior key", CallbackConfig{MasterKey: testCallbackMasterKey, PreviousMasterKeys: []string{"short"}}, "dev", "previous_master_keys entries"},
		{"prior without active", CallbackConfig{PreviousMasterKeys: []string{testCallbackMasterKey}}, "dev", "requires an active"},
		{"paused production prior without active", CallbackConfig{DeliveryPaused: true, PreviousMasterKeys: []string{testCallbackMasterKey}}, "production", "requires an active"},
		{"duplicate active", CallbackConfig{MasterKey: testCallbackMasterKey, PreviousMasterKeys: []string{testCallbackMasterKey}}, "dev", "repeat the active"},
		{"duplicate previous", CallbackConfig{MasterKey: testCallbackMasterKey, PreviousMasterKeys: []string{"old-callback-key-0123456789abcdef", "old-callback-key-0123456789abcdef"}}, "dev", "duplicate keys"},
		{"placeholder production", CallbackConfig{MasterKey: shippedSigningKeyPlaceholder}, "production", "must not be a placeholder"},
		{"rotation production", CallbackConfig{MasterKey: testCallbackMasterKey, PreviousMasterKeys: []string{"old-callback-key-0123456789abcdef"}}, "production", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCallbackPolicy(tc.config, tc.environment)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("validation error = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("validation error = %v, want substring %q", err, tc.wantErr)
			}
			for _, secret := range append([]string{tc.config.MasterKey}, tc.config.PreviousMasterKeys...) {
				if secret != "" && err != nil && strings.Contains(err.Error(), secret) {
					t.Fatal("validation error disclosed callback master key")
				}
			}
		})
	}
}
