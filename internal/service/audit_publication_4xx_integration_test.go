//go:build integration

package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/repository"
)

// TestAuditPublicationAllNonAuth4xxArePermanent exercises the complete
// connector-to-database path for the governance contract's all-non-auth-4xx
// rule. HTTP-level 408, 425, and 429 are deliberately distinct from a
// transport timeout: the governance response is a received, authoritative
// rejection and must not be retried by the audit drain.
func TestAuditPublicationAllNonAuth4xxArePermanent(t *testing.T) {
	var responseStatus atomic.Int32
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" || r.URL.Query().Get("wait_for") != "ledgered" {
			http.NotFound(w, r)
			return
		}
		posts.Add(1)
		w.WriteHeader(int(responseStatus.Load()))
	}))
	defer server.Close()

	db, svc, _, _, pubRepo := drainHarness(t, "tenant-classification", "test.classification")
	tokens := staticTokenSource{tenantID: "tenant-classification"}
	publisher, err := auditgovernance.NewPublisher(auditgovernance.Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAuditPublishers(publisher)

	for _, status := range []int{http.StatusBadRequest, http.StatusRequestTimeout, http.StatusTooEarly,
		http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			responseStatus.Store(int32(status))
			fact := appendDrainFact(t, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{
				SigningKey: "integration-audit-ledger-key-0123456789abcdef",
			}), "tenant-classification", "test.classification")

			before := posts.Load()
			svc.drainPublications(t.Context())
			if posts.Load() != before+1 {
				t.Fatalf("governance posts = %d, want one", posts.Load()-before)
			}
			state, owner, attempt := publicationState(t, db, fact.EventID)
			if state != "dead_letter" || owner != "" || attempt != 1 {
				t.Fatalf("settlement = %s/owner %q/attempt %d, want dead_letter//1", state, owner, attempt)
			}
			var code string
			if err := db.Raw(`SELECT error_code FROM audit_publications ap
				JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, fact.EventID).Scan(&code).Error; err != nil {
				t.Fatal(err)
			}
			if code != "audit.event_rejected" {
				t.Fatalf("error code = %q, want audit.event_rejected", code)
			}
			claimed, err := pubRepo.ClaimPublication(t.Context(), "observer", time.Minute, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(claimed) != 0 {
				t.Fatalf("terminal publication was claimable: %d rows", len(claimed))
			}
		})
	}
	if posts.Load() != 7 {
		t.Fatalf("total governance posts = %d, want 7", posts.Load())
	}
}
