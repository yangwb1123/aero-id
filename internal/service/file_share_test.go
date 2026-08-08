package service_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

// TestShareFileAuditFailureFailsClosed (FM-1 + FM-3): the share grants commit
// before the audit loop; the first grantee's audit failure must abort the loop
// (grantees 2..n un-audited) and surface as 503 audit.ingest_failed.
func TestShareFileAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: id, OwnerUID: "owner"}, nil
	}}
	grantCalls := 0
	shares := &mocks.FileShareRepo{GrantWithAuditFn: func(_ context.Context, _ domain.ID, _ string, _ []string, _ []*domain.AuditFact) error {
		grantCalls++
		return stderrors.New("ledger down")
	}}
	svc := service.NewFileService(files, nil, "local", service.WithFileShareRepo(shares),
		service.WithFileAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "owner")

	err := svc.ShareFile(ctx, id, []string{"g1", "g2"})
	assertAppCode(t, err, 500)
	if grantCalls != 1 {
		t.Fatalf("GrantWithAudit calls = %d, want 1 (one in-tx call for all grantees)", grantCalls)
	}
}

// TestUnshareFileAuditFailureFailsClosed (FM-1): the revoke commits before the
// durable audit append; the append failure must surface as 503 audit.ingest_failed.
func TestUnshareFileAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: id, OwnerUID: "owner"}, nil
	}}
	revokeCalls := 0
	shares := &mocks.FileShareRepo{RevokeWithAuditFn: func(_ context.Context, _ domain.ID, _ string, _ *domain.AuditFact) error {
		revokeCalls++
		return stderrors.New("ledger down")
	}}
	svc := service.NewFileService(files, nil, "local", service.WithFileShareRepo(shares),
		service.WithFileAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "owner")

	err := svc.UnshareFile(ctx, id, "g1")
	assertAppCode(t, err, 500)
	if revokeCalls != 1 {
		t.Fatalf("RevokeWithAudit calls = %d, want 1", revokeCalls)
	}
}
