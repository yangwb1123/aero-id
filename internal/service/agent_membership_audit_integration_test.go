//go:build integration

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func TestAgentMembershipDecisionPersistsDurableAuditFact(t *testing.T) {
	h := startStandaloneHarness(t)
	policy := AgentMembershipPolicy{Issuer: "https://snaplink.example", Subject: "agent-hub", ClientID: "agent-hub",
		TenantID: "tenant-a", Source: "snaplink", ScopeType: "tenant", ScopeID: "team-a", MaxAge: 5 * time.Minute}
	accounts := repository.NewAccountRepository(h.db, h.repo)
	account, err := accounts.EnsureByIdentity(context.Background(), policy.TenantID, policy.Issuer,
		"hub-user", "hub-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	syncedAt := time.Now().UTC().Add(-time.Minute)
	if err := accounts.UpsertMembership(context.Background(), &domain.AccountMembership{
		AccountID: account.ID, Source: policy.Source, ScopeType: policy.ScopeType, ScopeID: policy.ScopeID,
		SourceMemberID: "member-a", Status: "active", Version: 1, LastSyncedAt: &syncedAt,
	}); err != nil {
		t.Fatal(err)
	}
	reader := accounts.(repository.AgentMembershipProjectionRepository)
	checker, err := NewAgentMembershipCheckService(reader, []AgentMembershipPolicy{policy}, h.ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := &domain.TokenIdentity{Verified: true, UID: policy.Subject, Issuer: policy.Issuer,
		Subject: policy.Subject, ClientID: policy.ClientID, TenantID: policy.TenantID,
		Scopes: []string{agentMembershipCheckScope}}
	result, err := checker.Check(WithIdentity(context.Background(), identity), account.ID)
	if err != nil || !result.Eligible {
		t.Fatalf("membership Check = %+v/%v, want eligible result", result, err)
	}
	var count int64
	if err := h.db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = ? AND action = ? AND account_id = ?`,
		policy.TenantID, "agent.membership.checked", account.ID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable membership audit rows = %d, want 1", count)
	}
	var payloadJSON string
	if err := h.db.Raw(`SELECT payload::text FROM audit_ledger WHERE tenant_id = ? AND action = ? AND account_id = ?`,
		policy.TenantID, "agent.membership.checked", account.ID).Scan(&payloadJSON).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("decode durable audit payload %q: %v", payloadJSON, err)
	}
	if payload["eligible"] != true || payload["required_scope"] != agentMembershipCheckScope ||
		payload["membership_source"] != policy.Source || payload["membership_scope_id"] != policy.ScopeID {
		t.Fatalf("durable audit payload = %#v, want eligibility and fixed scope metadata", payload)
	}
}
