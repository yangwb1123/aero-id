package message

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	normalized, err := validateSendInput(in)
	if err != nil {
		return nil, err
	}
	in.Kind = normalized
	if err := s.checkSendBlock(ctx, in.ToUID, actor); err != nil {
		return nil, err
	}
	if err := s.checkSendFile(ctx, in.FileID, actor); err != nil {
		return nil, err
	}
	return s.createMessage(ctx, actor, in)
}

func validateSendInput(in SendMessageInput) (domain.MessageKind, error) {
	if in.ToUID == "" {
		return 0, apperr.BadRequest("message.invalid", "to_uid is required")
	}
	kind := in.Kind
	if kind == 0 {
		kind = domain.MessageKindText
	}
	if !kind.IsValid() {
		return 0, apperr.BadRequest("message.invalid", "unsupported message kind")
	}
	if kind.IsSystem() {
		return 0, apperr.Forbidden("message.forbidden", "system messages cannot be sent by users")
	}
	if in.Content == "" && in.FileID == nil {
		return 0, apperr.BadRequest("message.invalid", "content or file_id is required")
	}
	return kind, nil
}

func (s *service) checkSendBlock(ctx context.Context, toUID, actor string) error {
	if s.blocks == nil {
		return nil
	}
	blocked, err := s.blocks.IsBlocked(ctx, toUID, actor)
	if err != nil {
		return apperr.Internal("message.send_failed", "failed to check block status").Wrap(err)
	}
	if blocked {
		return apperr.Forbidden("message.forbidden", "recipient is not accepting messages from you")
	}
	return nil
}

func (s *service) checkSendFile(ctx context.Context, fileID *domain.ID, actor string) error {
	if fileID == nil {
		return nil
	}
	f, err := s.files.Get(ctx, *fileID)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.BadRequest("message.invalid", "referenced file does not exist")
	}
	if err != nil {
		return apperr.Internal("message.send_failed", "failed to verify file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return apperr.Forbidden("message.forbidden", "cannot attach a file you do not own")
	}
	return nil
}

func (s *service) createMessage(ctx context.Context, actor string, in SendMessageInput) (*domain.Message, error) {
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
	evt := outbox.NewEvent("message", m.ID.String(), "message.sent", map[string]any{
		"message_id": m.ID.String(),
		"from_uid":   m.FromUID,
		"to_uid":     m.ToUID,
	})
	if err := s.messages.Create(ctx, m, evt); err != nil {
		return nil, apperr.Internal("message.send_failed", "failed to send message").Wrap(err)
	}
	return m, nil
}

func (s *service) SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error) {
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
	evt := outbox.NewEvent("message", m.ID.String(), "message.sent", map[string]any{
		"message_id": m.ID.String(),
		"from_uid":   m.FromUID,
		"to_uid":     m.ToUID,
	})
	if err := s.messages.Create(ctx, m, evt); err != nil {
		return nil, apperr.Internal("message.send_failed", "failed to send system message").Wrap(err)
	}
	return m, nil
}

func (s *service) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
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

func (s *service) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
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

func (s *service) Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error) {
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

// ListConversations returns aggregated conversation summaries for the authenticated
// actor, enriched with user profiles of each peer.
func (s *service) ListConversations(ctx context.Context, page domain.Page) (*ConversationList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	summaries, next, err := s.messages.ListConversations(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("message.list_failed", "failed to list conversations").Wrap(err)
	}
	// Collect all peer UIDs for batch resolution.
	peerUIDs := make([]string, len(summaries))
	for i, s := range summaries {
		peerUIDs[i] = s.PeerUID
	}
	users, err := enrichUsers(ctx, s.dir, peerUIDs)
	if err != nil {
		return nil, apperr.Internal("message.enrich_failed", "failed to resolve peers").Wrap(err)
	}
	return &ConversationList{Conversations: summaries, NextCursor: next, Users: users}, nil
}

func (s *service) MarkRead(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if m.ToUID != actor {
		return apperr.Forbidden("message.forbidden", "only the recipient can mark a message read")
	}
	if m.ReadAt != nil {
		return nil
	}
	readAt := time.Now().UTC()
	evt := outbox.NewEvent("message", m.ID.String(), "message.read", map[string]any{
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

func (s *service) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if content == "" {
		return nil, apperr.BadRequest("message.invalid", "content is required")
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
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

func (s *service) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	m, err := s.messages.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil
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

func (s *service) MarkConversationRead(ctx context.Context, withUID string) error {
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

func (s *service) UnreadCount(ctx context.Context) (int64, error) {
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

func (s *service) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
	m, err := s.messages.Get(ctx, messageID)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("message.not_found", "message not found")
	}
	if err != nil {
		return apperr.Internal("message.get_failed", "failed to load message").Wrap(err)
	}
	if !canReadMessage(actorUID, m) {
		return apperr.Forbidden("message.forbidden", "not a participant in this message")
	}
	if err := s.messages.ReactToMessage(ctx, messageID, actorUID); err != nil {
		if stderrors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("message.not_found", "message not found")
		}
		return apperr.Internal("message.react_failed", "failed to react to message").Wrap(err)
	}
	return nil
}

func (s *service) UnreactMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
	m, err := s.messages.Get(ctx, messageID)
	if stderrors.Is(err, domain.ErrNotFound) {
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
