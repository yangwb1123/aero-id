package app

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

// mustPanic asserts that fn panics; audit wiring is fail-closed (FM-8): a
// callback service or ledger repository that cannot attach the audit outbox —
// or a legacy service constructed without a non-nil audit logger — must stop
// the process at startup, never drop or pend facts silently.
func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic on unwirable audit wiring, wiring succeeded", name)
		}
	}()
	fn()
}

// TestWireAuditServicesFailClosedOnMissingSetters (FM-8): every silent
// type-assertion no-op in the audit outbox wiring is replaced by a panic.
// A nil callback service and a ledger repository that cannot attach the
// publication repository must both fail loudly.
func TestWireAuditServicesFailClosedOnMissingSetters(t *testing.T) {
	var nilRepo repository.AuditLedgerRepository = nil
	mustPanic(t, "nil callback service", func() { wireAuditServices(nil, nilRepo) })
	mustPanic(t, "ledger repo without AuditPublicationRepository", func() {
		wireAuditServices(service.NewCallbackService(&wiringTestRepo{}, service.CallbackConfig{MasterKey: "k"}), nilRepo)
	})
}

// wiringTestRepo is the minimal repository a callback service needs to exist
// without a database; it intentionally lacks nothing — it is a real
// CallbackRepository, and the panic under test comes from the nil ledger repo.
type wiringTestRepo struct{}

func (*wiringTestRepo) CreateSubscription(ctx context.Context, s *domain.CallbackSubscription) error {
	return nil
}
func (*wiringTestRepo) ListSubscriptions(ctx context.Context, tenant, owner string, page domain.Page) ([]*domain.CallbackSubscription, string, error) {
	return nil, "", nil
}
func (*wiringTestRepo) DeleteSubscription(ctx context.Context, tenant, owner string, id domain.ID) error {
	return nil
}
func (*wiringTestRepo) GetSubscription(ctx context.Context, id domain.ID) (*domain.CallbackSubscription, error) {
	return nil, repository.ErrNotFound
}
func (*wiringTestRepo) ListEnabledByAction(ctx context.Context, tenant, action string) ([]*domain.CallbackSubscription, error) {
	return nil, nil
}
func (*wiringTestRepo) EnqueueDelivery(ctx context.Context, d *domain.CallbackDelivery) error {
	return nil
}
func (*wiringTestRepo) FetchDueDeliveries(ctx context.Context, limit int, worker string, lease time.Duration) ([]*domain.CallbackDelivery, error) {
	return nil, nil
}
func (*wiringTestRepo) MarkDelivery(ctx context.Context, id domain.ID, worker string, generation int64, status string, statusCode int, response, errorCode string, next time.Time) error {
	return nil
}
func (*wiringTestRepo) ResetDelivery(ctx context.Context, id domain.ID) error { return nil }
func (*wiringTestRepo) GetDelivery(ctx context.Context, id domain.ID) (*domain.CallbackDelivery, error) {
	return nil, repository.ErrNotFound
}
func (*wiringTestRepo) RequeueDead(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, nil
}
func (*wiringTestRepo) PurgeDeadLetterDeliveries(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}
func (*wiringTestRepo) CountDeadLetterDeliveries(context.Context, []domain.ID, int, string) (int, error) {
	return 0, nil
}
func (*wiringTestRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, nil
}

// wiredProbe is the minimal AuditLoggerWired fake: the assertion must accept
// any service value that reports a non-nil audit logger and reject the rest.
type wiredProbe struct{ wired bool }

func (p wiredProbe) AuditLoggerWired() bool { return p.wired }

// TestAssertAuditWiringFailsFastOnNilAuditLogger (FM-8): a composition root
// that builds one of the six legacy services without a non-nil audit logger
// must panic at startup. The `if s.audit != nil` skip guards at the 10 Log
// sites must be provably unreachable in production, not protected by an
// unwritten convention — the assertion closes that wiring gap.
func TestAssertAuditWiringFailsFastOnNilAuditLogger(t *testing.T) {
	mustPanic(t, "unwired service", func() {
		assertAuditWiring(auditWiredService{name: "admin", svc: wiredProbe{wired: false}})
	})
	mustPanic(t, "service without AuditLoggerWired", func() {
		assertAuditWiring(auditWiredService{name: "admin", svc: struct{}{}})
	})
	// Wired services must not panic — the assertion is a guard, not a gate on
	// the six shipped concrete types.
	assertAuditWiring(
		auditWiredService{name: "file", svc: wiredProbe{wired: true}},
		auditWiredService{name: "ban", svc: wiredProbe{wired: true}},
	)
}
