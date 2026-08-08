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
	mustPanic(t, "NewCallbackRepository", func() { repository.NewCallbackRepository(db, nil) })
	mustPanic(t, "NewAuditHoldRepository", func() { repository.NewAuditHoldRepository(db, nil) })
	mustPanic(t, "NewLegacyCutoverRepository", func() { repository.NewLegacyCutoverRepository(db, nil) })
}

// TestConstructorsRejectTypedNilAudit is the C5 hardening: a typed-nil
// interface value is != nil, so the plain nil check alone would let a
// fail-open repository through. The shared requireAuditLedger helper checks
// the underlying value too. Every live ledger-taking constructor in the
// composition root (internal/app/new.go plus buildAuditStack) is enumerated
// here so a new wiring path cannot land without its fail-closed test.
func TestConstructorsRejectTypedNilAudit(t *testing.T) {
	db := &gorm.DB{}
	var ledger repository.AuditLedgerTransactionalRepository = (*auditLedgerStub)(nil)
	mustPanic(t, "NewAccountRepository (typed nil)", func() { repository.NewAccountRepository(db, ledger) })
	mustPanic(t, "NewOperationRepository (typed nil)", func() { repository.NewOperationRepository(db, ledger) })
	mustPanic(t, "NewSyncJobRepository (typed nil)", func() { repository.NewSyncJobRepository(db, ledger) })
	mustPanic(t, "NewEventReceiptRepository (typed nil)", func() { repository.NewEventReceiptRepository(db, ledger) })
	mustPanic(t, "NewEventReceiptRepositoryWithStore (typed nil)", func() {
		repository.NewEventReceiptRepositoryWithStore(db, nil, ledger)
	})
	mustPanic(t, "NewCallbackRepository (typed nil)", func() { repository.NewCallbackRepository(db, ledger) })
	mustPanic(t, "NewAuditHoldRepository (typed nil)", func() { repository.NewAuditHoldRepository(db, ledger) })
	mustPanic(t, "NewLegacyCutoverRepository (typed nil)", func() { repository.NewLegacyCutoverRepository(db, ledger) })
}

// auditLedgerMap is a non-pointer implementation of the transactional ledger
// interface. A nil value of this type is a typed-nil that a Ptr-only reflect
// check would miss; requireAuditLedger must reject every nil-able kind.
type auditLedgerMap map[string]any

func (auditLedgerMap) AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error { return nil }

func TestConstructorsRejectNilKindAudit(t *testing.T) {
	db := &gorm.DB{}
	var ledger repository.AuditLedgerTransactionalRepository = (auditLedgerMap)(nil)
	mustPanic(t, "NewAccountRepository (nil map kind)", func() { repository.NewAccountRepository(db, ledger) })
}

// auditLedgerStub is a minimal AuditLedgerTransactionalRepository used only to
// fabricate a typed-nil value; it is never constructed.
type auditLedgerStub struct{}

func (*auditLedgerStub) AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error { return nil }
