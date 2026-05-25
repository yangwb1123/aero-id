package service

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type messageService struct {
	messages repository.MessageRepository
	files    repository.FileRepository // used only to validate file attachments
	dir      UserDirectory
}

func NewMessageService(messages repository.MessageRepository, files repository.FileRepository, dir UserDirectory) MessageService {
	return &messageService{messages: messages, files: files, dir: dir}
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
	if in.Content == "" && in.FileID == nil {
		return nil, apperr.BadRequest("message.invalid", "content or file_id is required")
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
	if err := s.messages.Create(ctx, m); err != nil {
		return nil, apperr.Internal("message.send_failed", "failed to send message").Wrap(err)
	}
	return m, nil
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
	if err := s.messages.MarkRead(ctx, id, time.Now().UTC()); err != nil {
		return apperr.Internal("message.mark_read_failed", "failed to mark message read").Wrap(err)
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
