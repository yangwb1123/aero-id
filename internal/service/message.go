package service

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type messageService struct {
	messages repository.MessageRepository
	files    repository.FileRepository // used only to validate file attachments
	dir      UserDirectory
	blocks   BlockChecker // nil => block guard is inactive
	audit    AuditLogger  // nil => audit inactive
}

// MessageOption configures optional messageService policy.
type MessageOption func(*messageService)

// WithMessageAuditLogger wires an AuditLogger into the message service.
func WithMessageAuditLogger(a AuditLogger) MessageOption {
	return func(s *messageService) { s.audit = a }
}

// WithBlockChecker enables the block guard on Send: a message is rejected when the
// recipient has blocked the sender. When unset (the default), Send is unaffected.
func WithBlockChecker(blocks BlockChecker) MessageOption {
	return func(s *messageService) { s.blocks = blocks }
}

func NewMessageService(messages repository.MessageRepository, files repository.FileRepository, dir UserDirectory, opts ...MessageOption) MessageService {
	s := &messageService{messages: messages, files: files, dir: dir}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *messageService) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.ToUID == "" {
		return nil, apperr.BadRequest("message.invalid", "to_uid is required")
	}
	if in.Kind == 0 {
		in.Kind = domain.MessageKindText
	}
	if !in.Kind.IsValid() {
		return nil, apperr.BadRequest("message.invalid", "unsupported message kind")
	}
	// System messages are server-emitted only (see SendSystem); they must not be
	// forgeable through the user-facing send path.
	if in.Kind.IsSystem() {
		return nil, apperr.Forbidden("message.forbidden", "system messages cannot be sent by users")
	}
	if in.Content == "" && in.FileID == nil {
		return nil, apperr.BadRequest("message.invalid", "content or file_id is required")
	}
	// Block guard: if the recipient has blocked the sender, the message is refused.
	// Directional — IsBlocked(blocker=recipient, blocked=sender). Inactive unless a
	// checker is wired (WithBlockChecker).
	if s.blocks != nil {
		blocked, err := s.blocks.IsBlocked(ctx, in.ToUID, actor)
		if err != nil {
			return nil, apperr.Internal("message.send_failed", "failed to check block status").Wrap(err)
		}
		if blocked {
			return nil, apperr.Forbidden("message.forbidden", "recipient is not accepting messages from you")
		}
	}
	if in.FileID != nil {
		f, err := s.files.Get(ctx, *in.FileID)
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.BadRequest("message.invalid", "referenced file does not exist")
		}
		if err != nil {
			return nil, apperr.Internal("message.send_failed", "failed to verify file").Wrap(err)
		}
		if f.OwnerUID != actor {
			return nil, apperr.Forbidden("message.forbidden", "cannot attach a file you do not own")
		}
	}

	m := &domain.Message{
		ID:        idgen.NewID(),
		FromUID:   actor,
		ToUID:     in.ToUID,
		Kind:      in.Kind,
		Content:   in.Content,
		FileID:    in.FileID,
		Meta:      in.Meta,
		CreatedAt: time.Now().UTC(),
	}
	// Emit a "message.sent" domain event in the same transaction as the insert so
	// downstream effects (notifications) become reliable rather than best-effort.
	evt := newEvent("message", m.ID.String(), "message.sent", map[string]any{
		"message_id": m.ID.String(),
		"from_uid":   m.FromUID,
		"to_uid":     m.ToUID,
	})
	if err := s.messages.Create(ctx, m, evt); err != nil {
		return nil, apperr.Internal("message.send_failed", "failed to send message").Wrap(err)
	}
	return m, nil
}

// SendSystem posts a system-kind message to toUID. It is an internal-only emit
// path: it does NOT require an authenticated actor and is exempt from the rule
// that forbids users from sending system messages, so server code (e.g. the
// notifications/outbox machinery) can post system notices. The message is
// stamped with domain.SystemSenderUID as the sender.
func (s *messageService) SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error) {
	if toUID == "" {
		return nil, apperr.BadRequest("message.invalid", "to_uid is required")
	}
	if content == "" {
		return nil, apperr.BadRequest("message.invalid", "content is required")
	}
	m := &domain.Message{
		ID:        idgen.NewID(),
		FromUID:   domain.SystemSenderUID,
		ToUID:     toUID,
		Kind:      domain.MessageKindSystem,
		Content:   content,
		Meta:      meta,
		CreatedAt: time.Now().UTC(),
	}
	evt := newEvent("message", m.ID.String(), "message.sent", map[string]any{
		"message_id": m.ID.String(),
		"from_uid":   m.FromUID,
		"to_uid":     m.ToUID,
	})
	if err := s.messages.Create(ctx, m, evt); err != nil {
		return nil, apperr.Internal("message.send_failed", "failed to send system message").Wrap(err)
	}
	return m, nil
}

// newEvent builds an outbox.Event with a fresh UUIDv7 id and JSON-encoded payload.
// A payload that fails to marshal yields a nil-payload event rather than failing
// the write; the maps used here are always marshalable.
func newEvent(aggregateType, aggregateID, eventType string, payload map[string]any) *outbox.Event {
	b, _ := json.Marshal(payload)
	return &outbox.Event{
		ID:            idgen.NewID(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       b,
		CreatedAt:     time.Now().UTC(),
	}
}

func (s *messageService) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return nil, apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if !canReadMessage(actor, m) {
		return nil, apperr.Forbidden("message.forbidden", "not a participant in this message")
	}
	return m, nil
}

func (s *messageService) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	msgs, next, err := s.messages.ListInbox(ctx, actor, page, unreadOnly)
	if err != nil {
		return nil, apperr.Internal("message.list_failed", "failed to list inbox").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, senderUIDs(msgs))
	if err != nil {
		return nil, apperr.Internal("message.enrich_failed", "failed to resolve senders").Wrap(err)
	}
	return &MessageList{Messages: msgs, NextCursor: next, Users: users}, nil
}

func (s *messageService) Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if withUID == "" {
		return nil, apperr.BadRequest("message.invalid", "peer uid is required")
	}
	msgs, next, err := s.messages.ListConversation(ctx, actor, withUID, page)
	if err != nil {
		return nil, apperr.Internal("message.list_failed", "failed to list conversation").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, []string{actor, withUID})
	if err != nil {
		return nil, apperr.Internal("message.enrich_failed", "failed to resolve participants").Wrap(err)
	}
	return &MessageList{Messages: msgs, NextCursor: next, Users: users}, nil
}

func (s *messageService) MarkRead(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if m.ToUID != actor {
		return apperr.Forbidden("message.forbidden", "only the recipient can mark a message read")
	}
	if m.ReadAt != nil {
		return nil // already read — idempotent no-op
	}
	readAt := time.Now().UTC()
	// Emit a "message.read" domain event in the same transaction as the read_at
	// stamp so the sender receives a reliable read-receipt notification.
	evt := newEvent("message", m.ID.String(), "message.read", map[string]any{
		"message_id": m.ID.String(),
		"from_uid":   m.FromUID,
		"to_uid":     actor,
		"read_at":    readAt.Format(time.RFC3339),
	})
	if err := s.messages.MarkReadWithEvent(ctx, id, readAt, evt); err != nil {
		return apperr.Internal("message.mark_read_failed", "failed to mark message read").Wrap(err)
	}
	return nil
}

// Edit rewrites a message's content. Only the sender may edit; the content must be
// non-empty; system-kind messages are server-owned and not user-editable. A
// soft-deleted message reads back as NotFound (the repo's default scope hides it),
// so editing a deleted message is rejected as not found.
func (s *messageService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if content == "" {
		return nil, apperr.BadRequest("message.invalid", "content is required")
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return nil, apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if m.FromUID != actor {
		return nil, apperr.Forbidden("message.forbidden", "only the sender can edit a message")
	}
	if m.Kind.IsSystem() {
		return nil, apperr.Forbidden("message.forbidden", "system messages cannot be edited")
	}
	now := time.Now().UTC()
	if err := s.messages.UpdateContent(ctx, id, content, now); err != nil {
		return nil, apperr.Internal("message.edit_failed", "failed to edit message").Wrap(err)
	}
	m.Content = content
	m.EditedAt = &now
	return m, nil
}

// Delete soft-deletes a message. Only the sender may delete; the call is idempotent
// (deleting an already-deleted message, which reads back as NotFound, is a no-op success).
func (s *messageService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil // already gone — idempotent no-op
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if m.FromUID != actor {
		return apperr.Forbidden("message.forbidden", "only the sender can delete a message")
	}
	if err := s.messages.SoftDelete(ctx, id); err != nil {
		return apperr.Internal("message.delete_failed", "failed to delete message").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditMessageDelete,
			SubjectType: "message",
			SubjectID:   id.String(),
			TargetUID:   m.ToUID,
		})
	}
	return nil
}

// MarkConversationRead marks every message from withUID to the caller as read.
func (s *messageService) MarkConversationRead(ctx context.Context, withUID string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if withUID == "" {
		return apperr.BadRequest("message.invalid", "peer uid is required")
	}
	if err := s.messages.MarkConversationRead(ctx, actor, withUID, time.Now().UTC()); err != nil {
		return apperr.Internal("message.mark_read_failed", "failed to mark conversation read").Wrap(err)
	}
	return nil
}

func (s *messageService) UnreadCount(ctx context.Context) (int64, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return 0, err
	}
	n, err := s.messages.CountUnread(ctx, actor)
	if err != nil {
		return 0, apperr.Internal("message.unread_count_failed", "failed to count unread messages").Wrap(err)
	}
	return n, nil
}

// senderUIDs returns the distinct from_uids preserving first-seen order.
func senderUIDs(msgs []*domain.Message) []string {
	seen := make(map[string]struct{}, len(msgs))
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if _, ok := seen[m.FromUID]; ok {
			continue
		}
		seen[m.FromUID] = struct{}{}
		out = append(out, m.FromUID)
	}
	return out
}

// ReactToMessage records a reaction by actorUID on a message. Only participants
// (sender or recipient) may react. Idempotent.
func (s *messageService) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
	m, err := s.messages.Get(ctx, messageID)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if !canReadMessage(actorUID, m) {
		return apperr.Forbidden("message.forbidden", "not a participant in this message")
	}
	if err := s.messages.ReactToMessage(ctx, messageID, actorUID); err != nil {
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("message.not_found", "message not found")
		}
		return apperr.Internal("message.react_failed", "failed to react to message").Wrap(err)
	}
	return nil
}

// UnreactMessage removes actorUID's reaction from a message. Idempotent.
func (s *messageService) UnreactMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
	m, err := s.messages.Get(ctx, messageID)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if !canReadMessage(actorUID, m) {
		return apperr.Forbidden("message.forbidden", "not a participant in this message")
	}
	if err := s.messages.UnreactMessage(ctx, messageID, actorUID); err != nil {
		return apperr.Internal("message.unreact_failed", "failed to remove reaction").Wrap(err)
	}
	return nil
}
