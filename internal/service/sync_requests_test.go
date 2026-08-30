package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

type syncSummaryAccountLoader struct {
	account *domain.Account
}

func (l syncSummaryAccountLoader) GetByID(_ context.Context, tenantID string, accountID domain.ID) (*domain.Account, error) {
	if l.account == nil || l.account.TenantID != tenantID || l.account.ID != accountID {
		return nil, errors.New("account not found")
	}
	return l.account, nil
}

func TestSyncSummaryRequestUsesAuthoritativeAccountIdentity(t *testing.T) {
	account := &domain.Account{ID: domain.ID{1}, TenantID: "tenant-1", CanonicalUID: "uid-1", HomeRegion: "us-east-1", Status: domain.AccountActive}
	job := &domain.SyncJob{ID: domain.ID{2}, AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-1", RequestID: "request-1"}
	request, err := syncSummaryRequest(t.Context(), syncSummaryAccountLoader{account}, account, job, connector.SourceAeroIM, []domain.AccountDataset{"aero-im.profile"})
	if err != nil {
		t.Fatal(err)
	}
	if request.AccountID != account.ID || request.CanonicalUID != account.CanonicalUID ||
		request.TenantID != account.TenantID || request.Region != account.HomeRegion {
		t.Fatalf("request identity = %#v, want account-authoritative fields", request)
	}
	if request.ActorUID != job.ActorUID || request.OperationID != job.ID.String() {
		t.Fatalf("request context = %#v, want job context", request)
	}
}

func TestSyncSummaryRequestRejectsTenantCanonicalAndRegionMismatches(t *testing.T) {
	baseAccount := &domain.Account{ID: domain.ID{1}, TenantID: "tenant-1", CanonicalUID: "uid-1", HomeRegion: "us-east-1", Status: domain.AccountActive}
	baseJob := &domain.SyncJob{ID: domain.ID{2}, AccountID: baseAccount.ID, TenantID: "tenant-1"}
	cases := []struct {
		name   string
		mutate func(*domain.Account, *domain.SyncJob)
	}{
		{"tenant", func(_ *domain.Account, job *domain.SyncJob) { job.TenantID = "tenant-2" }},
		{"canonical UID", func(account *domain.Account, _ *domain.SyncJob) { account.CanonicalUID = "" }},
		{"region", func(account *domain.Account, _ *domain.SyncJob) { account.HomeRegion = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := *baseAccount
			job := *baseJob
			tc.mutate(&account, &job)
			if _, err := syncSummaryRequest(t.Context(), syncSummaryAccountLoader{&account}, &account, &job, connector.SourceAeroIM, []domain.AccountDataset{"aero-im.profile"}); err == nil {
				t.Fatal("invalid authoritative target was accepted")
			}
		})
	}
}

func TestSyncSummaryRequestKeepsAeroVaultConstructionSourceAware(t *testing.T) {
	account := &domain.Account{ID: domain.ID{3}, TenantID: "tenant-1", CanonicalUID: "opaque-user", HomeRegion: "us-west-2", Status: domain.AccountActive}
	job := &domain.SyncJob{ID: domain.ID{4}, AccountID: account.ID, ActorUID: "actor-1", TenantID: account.TenantID, RequestID: "request-1"}
	request, err := syncSummaryRequest(t.Context(), syncSummaryAccountLoader{account}, nil, job, connector.SourceAeroVault,
		[]domain.AccountDataset{"aero-vault.tenants"})
	if err != nil {
		t.Fatalf("Aero Vault sync request: %v", err)
	}
	if request.AccountID != account.ID || request.CanonicalUID != account.CanonicalUID ||
		request.TenantID != job.TenantID || request.Region != account.HomeRegion ||
		len(request.Datasets) != 1 || request.Datasets[0] != "aero-vault.tenants" {
		t.Fatalf("Aero Vault request = %#v, want account-bound request fields", request)
	}
}

func TestSyncSummaryRequestKeepsAeroIMFailClosedWithoutLoader(t *testing.T) {
	account := &domain.Account{ID: domain.ID{5}, TenantID: "tenant-1", CanonicalUID: "opaque-user", HomeRegion: "us-west-2", Status: domain.AccountActive}
	job := &domain.SyncJob{ID: domain.ID{6}, AccountID: account.ID, TenantID: account.TenantID}
	if _, err := syncSummaryRequest(t.Context(), nil, account, job, connector.SourceAeroIM,
		[]domain.AccountDataset{"aero-im.profile"}); err == nil {
		t.Fatal("Aero IM sync request was constructed without an authoritative loader")
	}
}

func TestEraseRequestForJobCarriesAccountHomeRegion(t *testing.T) {
	account := &domain.Account{ID: domain.ID{1}, CanonicalUID: "uid-1", HomeRegion: "us-east-1"}
	job := &domain.SyncJob{ID: domain.ID{2}, ActorUID: "actor-1", TenantID: "tenant-1", RequestID: "request-1",
		IdempotencyKey: "erase-1"}
	request := eraseRequestForJob(account, job, connector.SourceAeroVault, nil)
	if request.Region != account.HomeRegion {
		t.Fatalf("region = %q, want account home region %q", request.Region, account.HomeRegion)
	}
	if request.IdempotencyKey != "erase-1:aero-vault" {
		t.Fatalf("idempotency key = %q, want stable source-qualified key", request.IdempotencyKey)
	}
	if retry := eraseRequestForJob(account, job, connector.SourceAeroVault, nil).IdempotencyKey; retry != request.IdempotencyKey {
		t.Fatalf("retry key = %q, want %q", retry, request.IdempotencyKey)
	}
}

func TestEraseCallOutcomeDoesNotTurnUnknownIntoFailure(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		status      string
		wantUnknown bool
		wantFailed  bool
	}{
		{name: "timeout", err: &connector.RemoteError{Kind: connector.ErrorTimeout}, status: "unknown", wantUnknown: true},
		{name: "unavailable", err: &connector.RemoteError{Kind: connector.ErrorUnavailable}, status: "unknown", wantUnknown: true},
		{name: "pending", err: &connector.RemoteError{Kind: connector.ErrorUnknown}, status: "unknown", wantUnknown: true},
		{name: "rejected", err: &connector.RemoteError{Kind: connector.ErrorInvalid}, status: "failed", wantFailed: true},
		{name: "local", err: errors.New("transport mapping missing"), status: "failed", wantFailed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, unknown, failed := eraseCallOutcome(tc.err)
			if status != tc.status || unknown != tc.wantUnknown || failed != tc.wantFailed {
				t.Fatalf("outcome = %q/%v/%v, want %q/%v/%v", status, unknown, failed,
					tc.status, tc.wantUnknown, tc.wantFailed)
			}
		})
	}
}

func TestIsUnknownRemoteTreatsCancellationAsUnknown(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if !isUnknownRemote(err) {
			t.Errorf("isUnknownRemote(%v) = false, want true", err)
		}
	}
}

func TestEraseSourceOutcomeKeepsPendingRemoteStateUnknown(t *testing.T) {
	cases := map[string]string{
		"completed": "completed",
		"success":   "completed",
		"accepted":  "unknown",
		"":          "unknown",
		"failed":    "failed",
		"cancelled": "failed",
	}
	for input, want := range cases {
		if got := eraseSourceOutcome(input); got != want {
			t.Errorf("eraseSourceOutcome(%q) = %q, want %q", input, got, want)
		}
	}
}
