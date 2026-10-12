package app

import (
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/service"
)

func TestNewRejectsUnsafeCallbackConfigBeforeOpeningDatabase(t *testing.T) {
	for _, settings := range []config.CallbackConfig{
		{}, {MasterKey: "short"}, {MasterKey: strings.Repeat("k", 32), SecretEnvelopeVersion: 2},
		{MasterKey: strings.Repeat("k", 32), AllowPrivateTarget: true},
	} {
		cfg := &config.Config{App: config.AppConfig{Env: "production"}, Callback: settings}
		// Database configuration is intentionally absent. The error must come
		// from the callback gate before a DB handle or any workers are created.
		application, err := New(t.Context(), cfg, zap.NewNop())
		if err == nil || !strings.Contains(err.Error(), "callback.") || application != nil {
			t.Fatalf("New callback gate returned application=%v, error=%v", application, err)
		}
	}
}

func TestBuildAuditStackRechecksCallbackKeysForInCodeConfig(t *testing.T) {
	cfg := &config.Config{AuditLedger: config.AuditLedgerConfig{SigningKey: strings.Repeat("a", 32)},
		App: config.AppConfig{Env: "prod"}, Callback: config.CallbackConfig{MasterKey: "short"}}
	callbacks, ledger, repo, err := buildAuditStack(cfg, nil, service.NewRegistryControlPlaneMetrics(nil))
	if err == nil || !strings.Contains(err.Error(), "callback.master_key") || callbacks != nil || ledger != nil || repo != nil {
		t.Fatalf("buildAuditStack did not reject unsafe in-code callback keys: %v", err)
	}
	cfg.Callback = config.CallbackConfig{DeliveryPaused: true}
	callbacks, ledger, repo, err = buildAuditStack(cfg, nil, service.NewRegistryControlPlaneMetrics(nil))
	if err != nil || callbacks == nil || ledger == nil || repo == nil {
		t.Fatalf("explicit callback pause disabled the durable audit stack: %v", err)
	}
}
