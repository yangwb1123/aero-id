package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
)

// peerOf returns the other participant of a message from the acting actor's
// perspective (the recipient when the sender acts; the sender otherwise) — the
// peer pick the react path uses for its notification event and the audit
// helpers use for TargetUID.
func peerOf(actorUID string, m *domain.Message) string {
	if actorUID == m.FromUID {
		return m.ToUID
	}
	return m.FromUID
}

// auditMessageSend records the message.send fact anchored on the message ID
// (EventID = m.ID), so a retry of the same send dedups at the ledger instead
// of appending a duplicate (the entity anchor is correct here: a send creates
// exactly one message with one fact). Returns nil when no audit logger is
// wired (nil => audit inactive — unit-test construction path).
//
// audit-site: standalone-fact — message.send, anchored on the message ID.
func (s *messageService) auditMessageSend(ctx context.Context, m *domain.Message) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Log(ctx, &domain.AuditEvent{
		ID:          m.ID,
		ActorUID:    m.FromUID,
		Action:      domain.AuditMessageSend,
		SubjectType: "message",
		SubjectID:   m.ID.String(),
		TargetUID:   m.ToUID,
	})
}

// auditMessageEdit records the message.edit fact. Editing is multi-occurrence
// (the same message can be edited repeatedly), so the EventID is left zero:
// anchoring on the message ID would let uq_audit_ledger_tenant_event silently
// dedup a later legitimate edit (same message ID, different intent). Zero
// EventID = one fresh row per execution, distinguishable by occurred_at; no
// retry-dedup contract (a retry may append a duplicate row, never a silently
// dropped later fact). Returns nil when no audit logger is wired.
//
// audit-site: standalone-fact — message.edit, multi-occurrence, fresh per-execution.
func (s *messageService) auditMessageEdit(ctx context.Context, m *domain.Message) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Log(ctx, &domain.AuditEvent{
		ActorUID:    m.FromUID,
		Action:      domain.AuditMessageEdit,
		SubjectType: "message",
		SubjectID:   m.ID.String(),
		TargetUID:   m.ToUID,
	})
}

// auditMessageReact records the message.react fact for the reacting actor
// (the handler passes the authenticated ctx actor as actorUID). Multi-
// occurrence: zero EventID, one fresh row per execution (see auditMessageEdit
// rationale). Returns nil when no audit logger is wired.
//
// audit-site: standalone-fact — message.react, multi-occurrence, fresh per-execution.
func (s *messageService) auditMessageReact(ctx context.Context, actorUID string, messageID domain.ID, m *domain.Message) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Log(ctx, &domain.AuditEvent{
		ActorUID:    actorUID,
		Action:      domain.AuditMessageReact,
		SubjectType: "message",
		SubjectID:   messageID.String(),
		TargetUID:   peerOf(actorUID, m),
	})
}

// auditMessageUnreact records the message.unreact fact (symmetric mutation to
// react; kept in scope for invariant completeness). Multi-occurrence: zero
// EventID, one fresh row per execution. Returns nil when no audit logger is
// wired.
//
// audit-site: standalone-fact — message.unreact, multi-occurrence, fresh per-execution.
func (s *messageService) auditMessageUnreact(ctx context.Context, actorUID string, messageID domain.ID, m *domain.Message) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Log(ctx, &domain.AuditEvent{
		ActorUID:    actorUID,
		Action:      domain.AuditMessageUnreact,
		SubjectType: "message",
		SubjectID:   messageID.String(),
		TargetUID:   peerOf(actorUID, m),
	})
}
