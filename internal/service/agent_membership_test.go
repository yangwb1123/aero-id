package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type agentMembershipReaderFake struct {
	eligible bool
	err      error
	calls    int
	args     []any
}

func (f *agentMembershipReaderFake) HasFreshActiveMembership(_ context.Context, tenant string,
	account domain.ID, source, scopeType, scopeID string, after, now time.Time) (bool, error) {
	f.calls++
	f.args = []any{tenant, account, source, scopeType, scopeID, after, now}
	return f.eligible, f.err
}

func TestAgentMembershipCheckUsesExactPrincipalAndFixedScope(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	reader := &agentMembershipReaderFake{eligible: true}
	checker, err := NewAgentMembershipCheckService(reader, []AgentMembershipPolicy{{
		Issuer: "https://issuer.example", Subject: "machine-sub", ClientID: "agent-hub",
		TenantID: "tenant-a", Source: "snaplink", ScopeType: "tenant", ScopeID: "team-42", MaxAge: 5 * time.Minute,
	}}, NewAuditLedgerService(&auditLedgerRepoSpy{}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	accountID := idgen.NewID()
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{Verified: true, UID: "machine-sub",
		Issuer: "https://issuer.example", Subject: "machine-sub", ClientID: "agent-hub", TenantID: "tenant-a",
		Scopes: []string{agentMembershipCheckScope}})
	result, err := checker.Check(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || result.TenantID != "tenant-a" || result.AccountID != accountID.String() {
		t.Fatalf("membership response = %+v", result)
	}
	want := []any{"tenant-a", accountID, "snaplink", "tenant", "team-42", now.Add(-5 * time.Minute), now}
	if reader.calls != 1 || len(reader.args) != len(want) {
		t.Fatalf("reader calls=%d args=%v", reader.calls, reader.args)
	}
	for i := range want {
		if reader.args[i] != want[i] {
			t.Errorf("reader arg[%d]=%v, want %v", i, reader.args[i], want[i])
		}
	}
}

func TestAgentMembershipCheckRejectsUnboundUnverifiedAndTenantMismatch(t *testing.T) {
	reader := &agentMembershipReaderFake{eligible: true}
	checker, err := NewAgentMembershipCheckService(reader, []AgentMembershipPolicy{{
		Issuer: "issuer", Subject: "subject", ClientID: "client", TenantID: "tenant-a",
		Source: "snaplink", ScopeType: "tenant", ScopeID: "scope", MaxAge: time.Minute,
	}}, NewAuditLedgerService(&auditLedgerRepoSpy{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	base := &domain.TokenIdentity{Verified: true, UID: "subject", Issuer: "issuer", Subject: "subject",
		ClientID: "client", TenantID: "tenant-a", Scopes: []string{agentMembershipCheckScope}}
	for name, identity := range map[string]*domain.TokenIdentity{
		"issuer":  {Verified: true, UID: "subject", Issuer: "other", Subject: "subject", ClientID: "client", TenantID: "tenant-a"},
		"subject": {Verified: true, UID: "subject", Issuer: "issuer", Subject: "other", ClientID: "client", TenantID: "tenant-a"},
		"client":  {Verified: true, UID: "subject", Issuer: "issuer", Subject: "subject", ClientID: "other", TenantID: "tenant-a"},
		"tenant":  {Verified: true, UID: "subject", Issuer: "issuer", Subject: "subject", ClientID: "client", TenantID: "tenant-b"},
		"scope missing": {Verified: true, UID: "subject", Issuer: "issuer", Subject: "subject", ClientID: "client",
			TenantID: "tenant-a"},
		"scope unrelated": {Verified: true, UID: "subject", Issuer: "issuer", Subject: "subject", ClientID: "client",
			TenantID: "tenant-a", Scopes: []string{"agent.sessions:read"}},
		"unverified":  {UID: "subject", Issuer: "issuer", Subject: "subject", ClientID: "client", TenantID: "tenant-a"},
		"no identity": nil,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if identity != nil {
				ctx = WithIdentity(ctx, identity)
			}
			if _, err := checker.Check(ctx, idgen.NewID()); err == nil {
				t.Fatal("unbound identity accepted")
			}
		})
	}
	if reader.calls != 0 {
		t.Fatalf("unbound checks reached repository %d times", reader.calls)
	}
	if _, err := checker.Check(WithIdentity(t.Context(), base), idgen.NewID()); err != nil {
		t.Fatalf("exact identity rejected: %v", err)
	}
}

func TestAgentMembershipCheckReturnsProjectionDenialAndUnavailable(t *testing.T) {
	checkerFor := func(reader *agentMembershipReaderFake) AgentMembershipCheckService {
		checker, err := NewAgentMembershipCheckService(reader, []AgentMembershipPolicy{{
			Issuer: "issuer", Subject: "subject", ClientID: "client", TenantID: "tenant-a",
			Source: "snaplink", ScopeType: "tenant", ScopeID: "scope", MaxAge: time.Minute,
		}}, NewAuditLedgerService(&auditLedgerRepoSpy{}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return checker
	}
	identity := &domain.TokenIdentity{Verified: true, UID: "subject", Issuer: "issuer", Subject: "subject",
		ClientID: "client", TenantID: "tenant-a", Scopes: []string{agentMembershipCheckScope}}
	if result, err := checkerFor(&agentMembershipReaderFake{}).Check(WithIdentity(t.Context(), identity), idgen.NewID()); err != nil || result.Eligible {
		t.Fatalf("missing projection should deny: result=%+v err=%v", result, err)
	}
	if _, err := checkerFor(&agentMembershipReaderFake{err: errors.New("database details")}).Check(
		WithIdentity(t.Context(), identity), idgen.NewID()); err == nil {
		t.Fatal("repository failure must fail closed")
	} else if app, ok := err.(interface{ HTTPStatus() int }); !ok || app.HTTPStatus() != 503 {
		t.Fatalf("repository error should map to 503, got %v", err)
	}
}

func TestAgentMembershipCheckAuditsBothEligibilityResults(t *testing.T) {
	for _, eligible := range []bool{true, false} {
		t.Run(map[bool]string{true: "eligible", false: "ineligible"}[eligible], func(t *testing.T) {
			reader := &agentMembershipReaderFake{eligible: eligible}
			auditRepo := &auditLedgerRepoSpy{}
			policy := AgentMembershipPolicy{Issuer: "issuer", Subject: "machine", ClientID: "hub", TenantID: "tenant-a",
				Source: "snaplink", ScopeType: "tenant", ScopeID: "scope-a", MaxAge: time.Minute}
			checker, err := NewAgentMembershipCheckService(reader, []AgentMembershipPolicy{policy},
				NewAuditLedgerService(auditRepo), nil)
			if err != nil {
				t.Fatal(err)
			}
			accountID := idgen.NewID()
			identity := &domain.TokenIdentity{Verified: true, UID: "machine", Issuer: policy.Issuer,
				Subject: policy.Subject, ClientID: policy.ClientID, TenantID: policy.TenantID,
				Scopes: []string{agentMembershipCheckScope}}
			result, err := checker.Check(WithIdentity(t.Context(), identity), accountID)
			if err != nil || result.Eligible != eligible {
				t.Fatalf("Check = %+v/%v, want eligible=%v", result, err, eligible)
			}
			fact := auditRepo.appended
			if fact == nil || fact.Action != "agent.membership.checked" || fact.ActorUID != policy.Subject ||
				fact.TenantID != policy.TenantID || fact.AccountID != accountID || fact.ResourceID != accountID.String() {
				t.Fatalf("audit fact = %+v, want actor/tenant/account-bound decision", fact)
			}
			wantPayload := map[string]any{"eligible": eligible, "issuer": policy.Issuer, "client_id": policy.ClientID,
				"required_scope": agentMembershipCheckScope, "membership_source": policy.Source,
				"membership_scope_type": policy.ScopeType, "membership_scope_id": policy.ScopeID}
			if len(fact.Payload) != len(wantPayload) {
				t.Fatalf("audit payload = %#v, want only minimal decision metadata", fact.Payload)
			}
			for key, value := range wantPayload {
				if fact.Payload[key] != value {
					t.Errorf("audit payload[%q] = %v, want %v", key, fact.Payload[key], value)
				}
			}
		})
	}
}

func TestAgentMembershipCheckFailsClosedWhenAuditAppendFails(t *testing.T) {
	reader := &agentMembershipReaderFake{eligible: true}
	auditRepo := &auditLedgerRepoSpy{appendErr: errors.New("ledger unavailable")}
	checker, err := NewAgentMembershipCheckService(reader, []AgentMembershipPolicy{{
		Issuer: "issuer", Subject: "machine", ClientID: "hub", TenantID: "tenant-a",
		Source: "snaplink", ScopeType: "tenant", ScopeID: "scope-a", MaxAge: time.Minute,
	}}, NewAuditLedgerService(auditRepo), nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := &domain.TokenIdentity{Verified: true, UID: "machine", Issuer: "issuer", Subject: "machine",
		ClientID: "hub", TenantID: "tenant-a", Scopes: []string{agentMembershipCheckScope}}
	if _, err := checker.Check(WithIdentity(t.Context(), identity), idgen.NewID()); !isAuditIngestFailed(err) {
		t.Fatalf("audit failure error = %v, want audit.ingest_failed (503)", err)
	}
	if reader.calls != 1 || auditRepo.appended == nil {
		t.Fatalf("reader calls/audit fact = %d/%+v, want query then attempted durable append", reader.calls, auditRepo.appended)
	}
}

func TestAgentMembershipCheckRequiresAuditConfiguration(t *testing.T) {
	_, err := NewAgentMembershipCheckService(&agentMembershipReaderFake{}, []AgentMembershipPolicy{{
		Issuer: "issuer", Subject: "machine", ClientID: "hub", TenantID: "tenant-a",
		Source: "snaplink", ScopeType: "tenant", ScopeID: "scope-a", MaxAge: time.Minute,
	}}, nil, nil)
	if err == nil {
		t.Fatal("membership check service accepted missing audit ledger")
	}
}
