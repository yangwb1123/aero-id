package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestCapabilityCallbackReadsRemainAvailableWithoutDeliveryConfiguration(t *testing.T) {
	accounts, snapshots, cfg := capabilityFixture()
	accounts.account.HomeRegion = "another-region"
	cfg.CallbacksEnabled = false
	var calls atomic.Int32
	checker := capabilityPermissions(func(_ context.Context, _, permission string) (bool, error) {
		if permission == domain.PermCallbackManage {
			calls.Add(1)
		}
		return true, nil
	})
	report, err := NewCapabilityService(accounts, snapshots, checker, cfg).GetMeCapabilities(capabilityContext(t))
	if err != nil {
		t.Fatal(err)
	}
	read, write := capabilityByName(report, "callback.read"), capabilityByName(report, "callback.manage")
	if !read.Available || read.AuthorizationScope != "global" || !read.Enabled || write.Available || write.Enabled {
		t.Fatalf("callback guidance read=%+v write=%+v", read, write)
	}
	if calls.Load() != 1 {
		t.Fatalf("same permission was checked %d times", calls.Load())
	}
}
