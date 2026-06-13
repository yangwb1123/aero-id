package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type notificationService struct {
	notifs repository.NotificationRepository
	dir    UserDirectory
}

func NewNotificationService(notifs repository.NotificationRepository, dir UserDirectory) NotificationService {
	return &notificationService{notifs: notifs, dir: dir}
}

func (s *notificationService) List(ctx context.Context, page domain.Page) (*NotificationList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	ns, next, err := s.notifs.ListByRecipient(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("notification.list_failed", "failed to list notifications").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, actorUIDs(ns))
	if err != nil {
		return nil, apperr.Internal("notification.enrich_failed", "failed to resolve actors").Wrap(err)
	}
	return &NotificationList{Notifications: ns, NextCursor: next, Users: users}, nil
}

func (s *notificationService) MarkRead(ctx context.Context, id domain.ID) error {
	if _, err := mustActor(ctx); err != nil {
		return err
	}
	// The repo only flips a still-unread row, so this is idempotent; recipient
	// scoping is enforced at read time (List/CountUnread are per-actor).
	if err := s.notifs.MarkRead(ctx, id, time.Now().UTC()); err != nil {
		return apperr.Internal("notification.mark_read_failed", "failed to mark notification read").Wrap(err)
	}
	return nil
}

func (s *notificationService) MarkAllRead(ctx context.Context) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if err := s.notifs.MarkAllRead(ctx, actor, time.Now().UTC()); err != nil {
		return apperr.Internal("notification.mark_all_read_failed", "failed to mark notifications read").Wrap(err)
	}
	return nil
}

func (s *notificationService) UnreadCount(ctx context.Context) (int64, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return 0, err
	}
	n, err := s.notifs.CountUnread(ctx, actor)
	if err != nil {
		return 0, apperr.Internal("notification.unread_count_failed", "failed to count unread notifications").Wrap(err)
	}
	return n, nil
}

// actorUIDs returns the distinct, non-empty actor uids preserving first-seen order.
func actorUIDs(ns []*domain.Notification) []string {
	seen := make(map[string]struct{}, len(ns))
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		if n.ActorUID == "" {
			continue
		}
		if _, ok := seen[n.ActorUID]; ok {
			continue
		}
		seen[n.ActorUID] = struct{}{}
		out = append(out, n.ActorUID)
	}
	return out
}

// NotificationDispatcher is the outbox.Handler that turns domain events into
// per-recipient notification rows. It plugs into the outbox relay in place of the
// no-op log handler. Returning an error leaves the event unpublished so the relay
// retries it; "nothing to notify" (self-message, unknown event, malformed payload)
// is treated as a successful no-op so the event is not retried forever.
type NotificationDispatcher struct {
	notifs repository.NotificationRepository
	blocks BlockChecker // nil => block suppression is inactive
}

// DispatcherOption configures optional NotificationDispatcher behaviour.
type DispatcherOption func(*NotificationDispatcher)

// WithDispatcherBlockChecker suppresses notifications for a recipient who has
// blocked the actor: before writing a notification for recipient R about actor X,
// the row is skipped (no error, no row) when IsBlocked(blocker=R, blocked=X). When
// unset (the default), every notification is dispatched.
func WithDispatcherBlockChecker(blocks BlockChecker) DispatcherOption {
	return func(d *NotificationDispatcher) { d.blocks = blocks }
}

func NewNotificationDispatcher(notifs repository.NotificationRepository, opts ...DispatcherOption) *NotificationDispatcher {
	d := &NotificationDispatcher{notifs: notifs}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// messageSentPayload / messageReadPayload / commentRepliedPayload mirror the JSON
// emitted by the message and comment services into the outbox.
type messageSentPayload struct {
	MessageID string `json:"message_id"`
	FromUID   string `json:"from_uid"`
	ToUID     string `json:"to_uid"`
}

type messageReadPayload struct {
	MessageID string `json:"message_id"`
	FromUID   string `json:"from_uid"` // original sender — receives the read-receipt
	ToUID     string `json:"to_uid"`   // reader / actor
}

type commentRepliedPayload struct {
	CommentID       string `json:"comment_id"`
	ParentAuthorUID string `json:"parent_author_uid"`
	ReplierUID      string `json:"replier_uid"` // optional; omitted by current producers
}

func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
	n := d.notificationFor(e)
	if n == nil {
		return nil // nothing to notify: unknown event, self-action, or malformed payload
	}
	// Block suppression: skip notifying a recipient who has blocked the actor.
	// Directional — IsBlocked(blocker=recipient, blocked=actor). A blank actor (e.g.
	// system) can never be blocked. Inactive unless a checker is wired.
	if d.blocks != nil && n.ActorUID != "" {
		blocked, err := d.blocks.IsBlocked(ctx, n.RecipientUID, n.ActorUID)
		if err != nil {
			return err // transient: leave the event unpublished so the relay retries
		}
		if blocked {
			return nil // recipient blocked the actor: no notification
		}
	}
	return d.notifs.Create(ctx, n)
}

// notificationFor maps a single event to the notification it should produce, or
// nil when there is nothing to notify. Payloads are parsed defensively.
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
	switch e.EventType {
	case "message.sent":
		var p messageSentPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.ToUID == "" || p.ToUID == p.FromUID {
			return nil // missing recipient or self-message: nothing to deliver
		}
		return &domain.Notification{
			ID:           idgen.NewID(),
			RecipientUID: p.ToUID,
			Type:         "message.received",
			ActorUID:     p.FromUID,
			SubjectType:  "message",
			SubjectID:    p.MessageID,
			CreatedAt:    time.Now().UTC(),
		}
	case "message.read":
		var p messageReadPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.FromUID == "" || p.FromUID == p.ToUID {
			return nil // missing sender or self-read: nothing to notify
		}
		return &domain.Notification{
			ID:           idgen.NewID(),
			RecipientUID: p.FromUID, // notify the original sender
			Type:         "message.read_receipt",
			ActorUID:     p.ToUID, // the reader
			SubjectType:  "message",
			SubjectID:    p.MessageID,
			CreatedAt:    time.Now().UTC(),
		}
	case "comment.replied":
		var p commentRepliedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.ParentAuthorUID == "" || p.ParentAuthorUID == p.ReplierUID {
			return nil // no parent author, or the author replied to themselves
		}
		return &domain.Notification{
			ID:           idgen.NewID(),
			RecipientUID: p.ParentAuthorUID,
			Type:         "comment.reply",
			ActorUID:     p.ReplierUID,
			SubjectType:  "comment",
			SubjectID:    p.CommentID,
			CreatedAt:    time.Now().UTC(),
		}
	default:
		return nil
	}
}
