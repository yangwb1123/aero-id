package service

import (
	"context"
	"io"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

type eraseOnExportRead struct {
	*memoryStore
	account *exportAccountFixture
}

func (s *eraseOnExportRead) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	reader, err := s.memoryStore.Get(ctx, key)
	s.account.account.Status = domain.AccountErased
	return reader, err
}

func exportOwnerContext(t *testing.T) context.Context {
	t.Helper()
	return WithIdentity(t.Context(), &domain.TokenIdentity{Verified: true, UID: "actor-1", Subject: "actor-1",
		Issuer: "https://snaplink.example", TenantID: "tenant-a", ClientID: "console"})
}

func TestExportDownloadDoesNotReturnContentAfterEraseDuringRead(t *testing.T) {
	svc, job, account, _, store := newScopedExportFixture(t, 1)
	if _, err := svc.executeExport(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	svc.store = &eraseOnExportRead{memoryStore: store, account: account}
	content, err := svc.DownloadExport(exportOwnerContext(t), job.ID, false)
	if len(content) != 0 || requireAppError(t, err).Code != "export.account_erased" {
		t.Fatal("read-time erase released export content")
	}
}

func TestHistoricalAuditExportJobIsMetadataOnly(t *testing.T) {
	job := &domain.SyncJob{JobType: domain.SyncJobAuditExport, Result: map[string]any{
		"events": []any{"sensitive historical fact"}, "event_count": 1, "_storage_key": "original-key"}}
	public := sanitizeAccountExportJob(job)
	if _, ok := public.Result["events"]; ok || public.Result["event_count"] != 1 || public.Result["_storage_key"] != "original-key" {
		t.Fatal("historical audit content remained in job response")
	}
	if job.Result["events"] == nil {
		t.Fatal("read sanitization mutated authoritative history")
	}
}

func TestExportDownloadReauthorizesAfterObjectRead(t *testing.T) {
	svc, job, _, _, _ := newScopedExportFixture(t, 1)
	if _, err := svc.executeExport(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.permission = capabilityPermissions(func(context.Context, string, string) (bool, error) {
		calls++
		return calls == 1, nil
	})
	content, err := svc.DownloadExport(exportOwnerContext(t), job.ID, true)
	if len(content) != 0 || requireAppError(t, err).Code != "export.forbidden" || calls != 2 {
		t.Fatal("revoked export permission released content")
	}
}
