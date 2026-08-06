package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type auditLedgerRepoSpy struct {
	appended  *domain.AuditFact
	filter    domain.AuditFactFilter
	source    string
	partition string
}

func (r *auditLedgerRepoSpy) Append(_ context.Context, fact *domain.AuditFact) error {
	r.appended = fact
	return nil
}

func (r *auditLedgerRepoSpy) List(_ context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	r.filter = filter
	return nil, "", nil
}

func (r *auditLedgerRepoSpy) ChainPartitions(ctx context.Context) ([]repository.ChainPartition, error) {
	return nil, nil
}

func (r *auditLedgerRepoSpy) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	return nil, repository.ErrNotFound
}

func (r *auditLedgerRepoSpy) VerifyChain(_ context.Context, source, partition string) error {
	r.source, r.partition = source, partition
	return nil
}

func TestAuditLedgerBindsTenantFromVerifiedIdentity(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if err := ledger.Record(ctx, AuditFactInput{TenantID: "tenant-b", Action: "account.viewed"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if repo.appended.TenantID != "tenant-a" || repo.appended.PartitionKey != "tenant-a:user-1" {
		t.Fatalf("appended tenant/partition = %q/%q", repo.appended.TenantID, repo.appended.PartitionKey)
	}
}

func TestAuditLedgerOverridesRequestedTenant(t *testing.T) {
	repo := &auditLedgerRepoSpy{}
	ledger := NewAuditLedgerService(repo)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", TenantID: "tenant-a"})

	if _, _, err := ledger.List(ctx, domain.AuditFactFilter{TenantID: "tenant-b"}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repo.filter.TenantID != "tenant-a" {
		t.Fatalf("filter tenant = %q, want tenant-a", repo.filter.TenantID)
	}
	if err := ledger.VerifyChain(ctx, "aero-id", "user-1"); err != nil {
		t.Fatalf("VerifyChain() error = %v", err)
	}
	if repo.source != "aero-id" || repo.partition != "tenant-a:user-1" {
		t.Fatalf("verified source/partition = %q/%q", repo.source, repo.partition)
	}
}
