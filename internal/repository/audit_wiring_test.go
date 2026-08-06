package repository_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// mustPanic asserts that fn panics; the fail-closed constructor contract
// requires a panic (stdlib recover — testify is not a dependency).
func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic on nil audit, construction succeeded", name)
		}
	}()
	fn()
}

// TestConstructorsRejectNilAudit is AC1: every repository whose mutations can
// append a durable audit fact must fail closed at construction time when the
// transactional ledger is missing — never silently run audit-disabled and
// surface the error on the first production mutation.
func TestConstructorsRejectNilAudit(t *testing.T) {
	db := &gorm.DB{}
	mustPanic(t, "NewAccountRepository", func() { repository.NewAccountRepository(db, nil) })
	mustPanic(t, "NewOperationRepository", func() { repository.NewOperationRepository(db, nil) })
	mustPanic(t, "NewSyncJobRepository", func() { repository.NewSyncJobRepository(db, nil) })
	mustPanic(t, "NewEventReceiptRepository", func() { repository.NewEventReceiptRepository(db, nil) })
	// WithStore delegates; one panic path suffices.
	mustPanic(t, "NewEventReceiptRepositoryWithStore", func() {
		repository.NewEventReceiptRepositoryWithStore(db, nil, nil)
	})
}

// TestConstructorsRejectTypedNilAudit is the C5 hardening: a typed-nil
// interface value is != nil, so the plain nil check alone would let a
// fail-open repository through. The shared requireAuditLedger helper checks
// the underlying pointer too.
func TestConstructorsRejectTypedNilAudit(t *testing.T) {
	db := &gorm.DB{}
	var ledger repository.AuditLedgerTransactionalRepository = (*auditLedgerStub)(nil)
	mustPanic(t, "NewAccountRepository (typed nil)", func() { repository.NewAccountRepository(db, ledger) })
	mustPanic(t, "NewOperationRepository (typed nil)", func() { repository.NewOperationRepository(db, ledger) })
}

// auditLedgerStub is a minimal AuditLedgerTransactionalRepository used only to
// fabricate a typed-nil value; it is never constructed.
type auditLedgerStub struct{}

func (*auditLedgerStub) AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error { return nil }
