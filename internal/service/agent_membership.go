package service

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

const agentMembershipCheckScope = "aero-id.membership:check"

type agentMembershipPrincipal struct {
	issuer   string
	subject  string
	clientID string
	tenantID string
}

// AgentMembershipPolicy binds a service principal to one fixed membership
// projection scope for one tenant.
type AgentMembershipPolicy struct {
	Issuer, Subject, ClientID, TenantID string
	Source, ScopeType, ScopeID          string
	MaxAge                              time.Duration
}

type AgentMembershipResult struct {
	Eligible  bool   `json:"eligible"`
	TenantID  string `json:"tenant_id"`
	AccountID string `json:"account_id"`
}

type AgentMembershipCheckService interface {
	Check(context.Context, domain.ID) (AgentMembershipResult, error)
}

type agentMembershipService struct {
	reader   repository.AgentMembershipProjectionRepository
	audit    AuditLedgerService
	policies map[agentMembershipPrincipal]AgentMembershipPolicy
	now      func() time.Time
}

func NewAgentMembershipCheckService(reader repository.AgentMembershipProjectionRepository,
	policies []AgentMembershipPolicy, audit AuditLedgerService, now func() time.Time) (AgentMembershipCheckService, error) {
	if reader == nil || audit == nil || len(policies) == 0 {
		return nil, apperr.Internal("agent_membership.invalid_config", "membership check is not configured")
	}
	if now == nil {
		now = time.Now
	}
	service := &agentMembershipService{reader: reader, audit: audit,
		policies: make(map[agentMembershipPrincipal]AgentMembershipPolicy, len(policies)), now: now}
	for _, policy := range policies {
		if !validAgentMembershipPolicy(policy) {
			return nil, apperr.Internal("agent_membership.invalid_config", "membership check is not configured")
		}
		key := agentMembershipPrincipal{policy.Issuer, policy.Subject, policy.ClientID, policy.TenantID}
		if _, exists := service.policies[key]; exists {
			return nil, apperr.Internal("agent_membership.invalid_config", "membership check is not configured")
		}
		service.policies[key] = policy
	}
	return service, nil
}

func validAgentMembershipPolicy(policy AgentMembershipPolicy) bool {
	for _, value := range []string{policy.Issuer, policy.Subject, policy.ClientID, policy.TenantID,
		policy.Source, policy.ScopeType, policy.ScopeID} {
		if value == "" {
			return false
		}
	}
	return policy.MaxAge > 0
}

func (s *agentMembershipService) Check(ctx context.Context, accountID domain.ID) (AgentMembershipResult, error) {
	identity, ok := IdentityFrom(ctx)
	if !ok || !identity.Verified {
		return AgentMembershipResult{}, apperr.Forbidden("agent_membership.not_bound", "membership check is not authorized")
	}
	key := agentMembershipPrincipal{identity.Issuer, identity.Subject, identity.ClientID, identity.TenantID}
	policy, ok := s.policies[key]
	if !ok {
		return AgentMembershipResult{}, apperr.Forbidden("agent_membership.not_bound", "membership check is not authorized")
	}
	if !hasScope(identity.Scopes, agentMembershipCheckScope) {
		return AgentMembershipResult{}, apperr.Forbidden("agent_membership.scope_required", "membership check scope is required")
	}
	checkedAt := s.now().UTC()
	eligible, err := s.reader.HasFreshActiveMembership(ctx, identity.TenantID, accountID,
		policy.Source, policy.ScopeType, policy.ScopeID, checkedAt.Add(-policy.MaxAge), checkedAt)
	if err != nil {
		return AgentMembershipResult{}, apperr.Unavailable("agent_membership.unavailable", "membership check is unavailable")
	}
	if err := s.recordMembershipDecision(ctx, identity, policy, accountID, eligible); err != nil {
		return AgentMembershipResult{}, apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
	}
	return AgentMembershipResult{Eligible: eligible, TenantID: identity.TenantID,
		AccountID: accountID.String()}, nil
}

func hasScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func (s *agentMembershipService) recordMembershipDecision(ctx context.Context, identity *domain.TokenIdentity,
	policy AgentMembershipPolicy, accountID domain.ID, eligible bool) error {
	// audit-site: standalone-fact — sensitive membership eligibility read; one durable row per authorized decision.
	return s.audit.Record(ctx, AuditFactInput{Source: "aero-id", ActorUID: identity.Subject, AccountID: accountID,
		TenantID: identity.TenantID, Action: "agent.membership.checked", ResourceType: "account_membership",
		ResourceID: accountID.String(), Payload: map[string]any{"eligible": eligible, "issuer": identity.Issuer,
			"client_id": identity.ClientID, "required_scope": agentMembershipCheckScope, "membership_source": policy.Source,
			"membership_scope_type": policy.ScopeType, "membership_scope_id": policy.ScopeID}})
}
