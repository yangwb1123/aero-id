package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadCallbackPreviousMasterKeysFromEnvironment(t *testing.T) {
	t.Setenv("AERO_ID_APP_ENV", "dev")
	t.Setenv("AERO_ID_SIGNING_KEY", strings.Repeat("s", 32))
	t.Setenv("AERO_ID_AUDIT_LEDGER_SIGNING_KEY", strings.Repeat("a", 32))
	t.Setenv("AERO_ID_CALLBACK_MASTER_KEY", strings.Repeat("n", 40))
	t.Setenv("AERO_ID_CALLBACK_SECRET_ENVELOPE_VERSION", "1")
	t.Setenv("AERO_ID_CALLBACK_DELIVERY_PAUSED", "true")
	oldKeys := []string{strings.Repeat("o", 40), strings.Repeat("p", 40)}
	t.Setenv("AERO_ID_CALLBACK_PREVIOUS_MASTER_KEYS", strings.Join(oldKeys, ","))
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "false")
	t.Setenv("AERO_ID_BILLING_ENABLED", "false")

	path := filepath.Join(repoRootConfigTest(t), "configs", "config.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load shipped config with callback keyring: %v", err)
	}
	if !reflect.DeepEqual(cfg.Callback.PreviousMasterKeys, oldKeys) {
		t.Fatal("previous callback keys did not match the environment override")
	}
	if cfg.Callback.SecretEnvelopeVersion != 1 || !cfg.Callback.DeliveryPaused {
		t.Fatal("callback envelope/pause environment overrides did not reach configuration")
	}
}

func TestLoadCallbackPauseOverrideWorksWithoutFileKeys(t *testing.T) {
	t.Setenv("AERO_ID_CALLBACK_DELIVERY_PAUSED", "true")
	t.Setenv("AERO_ID_CALLBACK_SECRET_ENVELOPE_VERSION", "1")
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatalf("Load callback flags without a callback file section: %v", err)
	}
	if !cfg.Callback.DeliveryPaused || cfg.Callback.SecretEnvelopeVersion != 1 {
		t.Fatal("env-only callback configuration was silently ignored")
	}
}
