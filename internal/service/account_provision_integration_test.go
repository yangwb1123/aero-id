//go:build integration

package service_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	billingconnector "github.com/aero/aero-id/internal/connector/billing"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/service"
)

// TestProvisionAccountCompensatesAfterAuditTransactionFailure exercises the
// service, real GORM account/quota repository, real audit repository, and real
// billing connector together. An unconfigured signing key injects a failure in
// the in-transaction audit append after the account and quota writes have
// started, so PostgreSQL must roll all local rows back before Release runs.
func TestProvisionAccountCompensatesAfterAuditTransactionFailure(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	billingState := &integrationBillingState{}
	server := newIntegrationBillingServer(t, billingState)
	defer server.Close()

	client, err := billingconnector.NewClient(billingconnector.Config{
		Endpoint: server.URL, Timeout: time.Second, MaxBodyBytes: 64 << 10, AllowInsecureLoopback: true,
		Bindings: map[string]billingconnector.BindingConfig{"tenant-a": {
			TokenURL: server.URL + "/token", ClientID: "tenant-client", ClientSecret: "tenant-secret",
			Scope:     billingconnector.ScopeEntitlementRead + " " + billingconnector.ScopeMeteringWrite,
			Resources: []string{"billing-api"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The same real repository is wired into the account service and its
	// in-transaction audit surface. The empty signing key is the injected
	// persistence/audit failure; it is not a mock of the transaction boundary.
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	quota := service.NewBillingService(repository.NewEntitlementRepository(db), client, service.BillingConfig{})
	accountService := service.NewAccountService(accounts,
		service.WithAccountAudit(service.NewAuditLedgerService(ledger)),
		service.WithAccountQuota(quota), service.WithAccountIssuer("snaplink"), service.WithAccountRegion("local"))
	ctx := service.WithIdentity(t.Context(), &domain.TokenIdentity{
		UID: "user-1", Issuer: "snaplink", Subject: "user-1", TenantID: "tenant-a",
	})

	_, err = accountService.GetMe(ctx)
	if err == nil {
		t.Fatal("provision must fail when the in-transaction audit append is unavailable")
	}
	if !strings.Contains(err.Error(), "audit ledger signing key is not configured") {
		t.Fatalf("provision error = %v, want audit persistence failure", err)
	}

	billingState.mu.Lock()
	defer billingState.mu.Unlock()
	if billingState.reserveID == "" || billingState.releaseID != billingState.reserveID {
		t.Fatalf("reserve/release IDs = %q/%q, want identical IDs", billingState.reserveID, billingState.releaseID)
	}
	if billingState.releaseCount != 1 {
		t.Fatalf("release count = %d, want exactly one", billingState.releaseCount)
	}
	if billingState.releaseKey != "usr-release-"+stableReservationSuffix(billingState.reserveID) {
		t.Fatalf("release key = %q, want deterministic key", billingState.releaseKey)
	}
	if billingState.pending[billingState.reserveID] {
		t.Fatal("billing reservation remained pending after compensation")
	}

	for _, table := range []string{"accounts", "account_identities", "account_quota_reservations", "audit_ledger"} {
		var rows int64
		if err := db.Raw("SELECT count(*) FROM "+table+" WHERE tenant_id = ?", "tenant-a").Scan(&rows).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if rows != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", table, rows)
		}
	}
	var publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.tenant_id = ?`, "tenant-a").Scan(&publicationRows).Error; err != nil {
		t.Fatalf("count audit publications: %v", err)
	}
	if publicationRows != 0 {
		t.Fatalf("audit publication rows after rollback = %d, want 0", publicationRows)
	}
}

func stableReservationSuffix(reservationID string) string {
	const prefix = "usr-res-"
	if len(reservationID) >= len(prefix) && reservationID[:len(prefix)] == prefix {
		return reservationID[len(prefix):]
	}
	return reservationID
}

type integrationBillingState struct {
	mu           sync.Mutex
	pending      map[string]bool
	reserveID    string
	releaseID    string
	releaseKey   string
	releaseCount int
}

func newIntegrationBillingServer(t *testing.T, state *integrationBillingState) *httptest.Server {
	t.Helper()
	state.pending = make(map[string]bool)
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			writeIntegrationJSON(t, writer, http.StatusOK, map[string]any{
				"access_token": "billing-token", "token_type": "Bearer", "expires_in": 300,
			})
		case "/api/v1/metering/entitlement":
			at := time.Now().UTC()
			writeIntegrationJSON(t, writer, http.StatusOK, map[string]any{"entitlement": map[string]any{
				"tenant_id": "tenant-a", "subscription_id": "sub-a", "plan": map[string]any{"id": "full", "version": 1},
				"revision": 1, "active": true, "features": map[string]bool{"account": true},
				"limits":       map[string]any{"users": map[string]any{"hard": 10}},
				"effective_at": at.Add(-time.Minute), "expires_at": at.Add(time.Hour), "generated_at": at,
			}})
		case "/api/v1/metering/reservations":
			var command struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Errorf("decode reserve command: %v", err)
				writeIntegrationJSON(t, writer, http.StatusBadRequest, map[string]string{"error": "invalid"})
				return
			}
			state.mu.Lock()
			state.reserveID = command.ID
			state.pending[command.ID] = true
			state.mu.Unlock()
			writeIntegrationJSON(t, writer, http.StatusCreated, map[string]any{"reservation": map[string]any{
				"id": command.ID, "tenant_id": "tenant-a", "dimension": "users", "quantity": 1, "status": "pending",
			}})
		default:
			if request.Method != http.MethodDelete {
				http.NotFound(writer, request)
				return
			}
			reservationID, err := url.PathUnescape(request.URL.EscapedPath()[len("/api/v1/metering/reservations/"):])
			if err != nil {
				t.Errorf("unescape release path: %v", err)
			}
			state.mu.Lock()
			state.releaseID = reservationID
			state.releaseKey = request.Header.Get("Idempotency-Key")
			state.releaseCount++
			state.pending[reservationID] = false
			state.mu.Unlock()
			writeIntegrationJSON(t, writer, http.StatusOK, map[string]any{"reservation": map[string]any{
				"id": reservationID, "tenant_id": "tenant-a", "status": "released",
			}})
		}
	}))
}

func writeIntegrationJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
