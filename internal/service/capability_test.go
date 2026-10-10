package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type capabilityAccounts struct {
	AccountService
	account *domain.Account
	calls   int
}

func (f *capabilityAccounts) GetMe(context.Context) (*AccountView, error) {
	panic("capabilities must not read profile or source content")
}
func (f *capabilityAccounts) GetMeMetadata(context.Context) (*domain.Account, error) {
	f.calls++
	return f.account, nil
}

type capabilitySnapshots struct {
	repository.SnapshotRepository
	accountID domain.ID
	values    []*domain.AccountSnapshot
	err       error
	calls     int
}

func (f *capabilitySnapshots) ListSnapshotMetadata(_ context.Context, id domain.ID, scope []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	f.accountID = id
	f.calls++
	return f.values, f.err
}
func (f *capabilitySnapshots) ListSnapshots(context.Context, domain.ID, []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	panic("capabilities must not read projection content")
}

type capabilityPermissions func(context.Context, string, string) (bool, error)

func (f capabilityPermissions) HasPermission(ctx context.Context, actor, permission string) (bool, error) {
	return f(ctx, actor, permission)
}

func capabilityContext(t *testing.T) context.Context {
	t.Helper()
	return WithIdentity(t.Context(), &domain.TokenIdentity{Verified: true, UID: "caller", Subject: "caller",
		Issuer: "https://snaplink.example", TenantID: "tenant-a", ClientID: "console"})
}
func capabilityFixture() (*capabilityAccounts, *capabilitySnapshots, CapabilityConfig) {
	accounts := &capabilityAccounts{account: &domain.Account{ID: idgen.NewID(), CanonicalUID: "caller", TenantID: "tenant-a", HomeRegion: "local", Status: domain.AccountActive}}
	return accounts, &capabilitySnapshots{}, CapabilityConfig{LocalRegion: "local", Sources: map[string]SourceCapabilityConfig{"aero-vault": {SummaryEnabled: true}}}
}
func capabilityByName(report *domain.CapabilityReport, name string) domain.Capability {
	for _, item := range report.Capabilities {
		if item.Name == name {
			return item
		}
	}
	return domain.Capability{}
}
func datasetCapabilityByName(report *domain.CapabilityReport, name domain.AccountDataset) domain.DatasetCapability {
	for _, item := range report.Datasets {
		if item.Name == name {
			return item
		}
	}
	return domain.DatasetCapability{}
}

func TestCapabilityReportSeparatesEnablementPermissionsAndFreshness(t *testing.T) {
	accounts, snapshots, cfg := capabilityFixture()
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	snapshots.values = []*domain.AccountSnapshot{
		{AccountID: accounts.account.ID, Source: "aero-vault", Dataset: "aero-vault.usage", Status: domain.SnapshotFresh, Version: 3, GeneratedAt: now, LastSyncedAt: &now, Data: map[string]any{"secret": "must-not-leak"}},
		{AccountID: accounts.account.ID, Source: "aero-vault", Dataset: "aero-vault.buckets", Status: domain.SnapshotFresh, LastSyncedAt: &old},
	}
	checker := capabilityPermissions(func(_ context.Context, _, permission string) (bool, error) {
		return permission == domain.PermAuditRead, nil
	})
	report, err := NewCapabilityService(accounts, snapshots, checker, cfg).GetMeCapabilities(capabilityContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Partial || report.AuthorizationVersion != "unsupported" || len(report.Datasets) != 11 {
		t.Fatalf("report = %+v", report)
	}
	if item := capabilityByName(report, "account.self.export"); item.Enabled || item.Available || item.Authorization != domain.AccessAllowed {
		t.Fatalf("export = %+v", item)
	}
	if item := capabilityByName(report, "audit.read"); !item.Available || item.AuthorizationScope != "global" {
		t.Fatalf("audit = %+v", item)
	}
	if item := capabilityByName(report, "system.config"); item.Available || item.Authorization != domain.AccessDenied {
		t.Fatalf("system = %+v", item)
	}
	if item := datasetCapabilityByName(report, "aero-vault.usage"); !item.Enabled || !item.Fresh || item.ResourceAuthorization != domain.AccessUnknown || item.Version != 3 {
		t.Fatalf("usage = %+v", item)
	}
	if item := datasetCapabilityByName(report, "aero-vault.buckets"); item.Fresh || item.Status != domain.SnapshotStale {
		t.Fatalf("buckets = %+v", item)
	}
	if item := datasetCapabilityByName(report, "snaplink.identity"); item.Enabled || !item.Registered {
		t.Fatalf("snaplink = %+v", item)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "must-not-leak") || snapshots.calls != 1 {
		t.Fatal("capabilities leaked content or repeated metadata queries")
	}
}

func TestCapabilityReportUnknownAuthorizationNeverGrantsOrLeaks(t *testing.T) {
	accounts, snapshots, cfg := capabilityFixture()
	snapshots.err = errors.New("private SQL/topology")
	checker := capabilityPermissions(func(context.Context, string, string) (bool, error) { return true, errors.New("private policy/token") })
	report, err := NewCapabilityService(accounts, snapshots, checker, cfg).GetMeCapabilities(capabilityContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Partial || capabilityByName(report, "audit.read").Available {
		t.Fatalf("unknown was granted: %+v", report)
	}
	for _, item := range report.Capabilities {
		if item.AuthorizationScope == "global" && item.Authorization != domain.AccessUnknown {
			t.Fatalf("decision = %+v", item)
		}
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("dependency error leaked")
	}
}

func TestCapabilityReportRejectsIncompleteOrCrossTenantContextBeforeReads(t *testing.T) {
	accounts, snapshots, cfg := capabilityFixture()
	svc := NewCapabilityService(accounts, snapshots, nil, cfg)
	if _, err := svc.GetMeCapabilities(WithActor(t.Context(), "caller")); err == nil || accounts.calls != 0 {
		t.Fatal("UID-only context reached account")
	}
	accounts.account.TenantID = "another-tenant"
	if _, err := svc.GetMeCapabilities(capabilityContext(t)); err == nil || snapshots.calls != 0 {
		t.Fatal("foreign account reached snapshots")
	}
}

func TestCapabilityReportErasedIdentityReturnsUnavailableMetadata(t *testing.T) {
	accounts, snapshots, cfg := capabilityFixture()
	accounts.account.Status = domain.AccountErased
	accounts.account.CanonicalUID = "erased:" + accounts.account.ID.String()
	report, err := NewCapabilityService(accounts, snapshots, allowAllChecker{}, cfg).GetMeCapabilities(capabilityContext(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.Capabilities {
		if item.Available {
			t.Fatalf("erased capability granted: %+v", item)
		}
	}
}

func TestCapabilityReportBoundsAuthorizationAndKeepsSelfEligibility(t *testing.T) {
	accounts, snapshots, cfg := capabilityFixture()
	cfg.AuthorizationTimeout = 20 * time.Millisecond
	var active, peak atomic.Int32
	checker := capabilityPermissions(func(ctx context.Context, _, _ string) (bool, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		<-ctx.Done()
		return false, ctx.Err()
	})
	start := time.Now()
	report, err := NewCapabilityService(accounts, snapshots, checker, cfg).GetMeCapabilities(capabilityContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 4 || time.Since(start) > time.Second {
		t.Fatal("authorization exceeded bulkhead/deadline")
	}
	if !report.Partial || !capabilityByName(report, "account.self.read").Available {
		t.Fatal("global outage corrupted self eligibility")
	}
	accounts.account.HomeRegion = "other"
	report, err = NewCapabilityService(accounts, snapshots, allowAllChecker{}, cfg).GetMeCapabilities(capabilityContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if item := capabilityByName(report, "account.self.update"); item.Available || item.ReasonCode != "account.home_region_required" {
		t.Fatalf("home region = %+v", item)
	}
	if !capabilityByName(report, "audit.read").Available {
		t.Fatal("global reads were tied to actor account home region")
	}
}
