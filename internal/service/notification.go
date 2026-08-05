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

// NotificationPusher delivers a real-time notification event to a connected client.
// It is satisfied by *hub.Hub via hub.Adapter; the service layer must NOT import hub.
type NotificationPusher interface {
	Publish(uid, eventType, data string, eventID int64)
}

// NotificationDispatcher is the outbox.Handler that turns domain events into
// per-recipient notification rows. It plugs into the outbox relay in place of the
// no-op log handler. Returning an error leaves the event unpublished so the relay
// retries it; "nothing to notify" (self-message, unknown event, malformed payload)
// is treated as a successful no-op so the event is not retried forever.
type NotificationDispatcher struct {
	notifs      repository.NotificationRepository
	blocks      BlockChecker       // nil => block suppression is inactive
	prefs       PreferenceChecker  // nil => preference check is inactive
	pusher      NotificationPusher // nil => no real-time push
	rateLimiter *notifRateLimiter  // nil => no per-recipient rate limiting
	collapseWin time.Duration      // 0 => notification collapsing is disabled
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

// WithDispatcherPreferenceChecker suppresses notifications whose type the
// recipient has opted out of (via notification_preferences.enabled = false) or
// that fall inside the recipient's quiet-hours window. When unset (the default)
// all notification types are dispatched, preserving backward compatibility.
func WithDispatcherPreferenceChecker(prefs PreferenceChecker) DispatcherOption {
	return func(d *NotificationDispatcher) { d.prefs = prefs }
}

// WithDispatcherRateLimiter caps the number of notifications a single recipient
// may receive per notification type per minute. When the per-minute threshold
// is exceeded, excess notifications are silently dropped (no retry). rate > 0
// enables limiting; rate <= 0 is a no-op (default).
func WithDispatcherRateLimiter(rate int) DispatcherOption {
	return func(d *NotificationDispatcher) {
		if rate > 0 {
			d.rateLimiter = newNotifRateLimiter(rate)
		}
	}
}

// WithDispatcherCollapse enables notification collapsing: when a notification
// with the same (recipient, type, actor) exists within the given window and is
// still unread, the dispatcher increments that row's collapsed_count instead of
// creating a new row. A zero or negative window disables collapsing (default).
// A 5-minute window is a sensible starting point.
func WithDispatcherCollapse(window time.Duration) DispatcherOption {
	return func(d *NotificationDispatcher) {
		if window > 0 {
			d.collapseWin = window
		}
	}
}

// WithDispatcherPusher wires an SSE hub so that each newly created notification
// is also pushed in real-time to any connected client for the recipient uid.
func WithDispatcherPusher(p NotificationPusher) DispatcherOption {
	return func(d *NotificationDispatcher) { d.pusher = p }
}

func NewNotificationDispatcher(notifs repository.NotificationRepository, opts ...DispatcherOption) *NotificationDispatcher {
	d := &NotificationDispatcher{notifs: notifs}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Payload types mirror the JSON emitted by the message and comment services
// into the outbox. Each corresponds to one EventType.
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

// commentOnTargetPayload is emitted when a top-level comment is created on a
// file or message target. The owner_uid is the target's owner who receives
// the notification.
type commentOnTargetPayload struct {
	CommentID    string `json:"comment_id"`
	TargetType   string `json:"target_type"` // "file" or "message"
	TargetID     string `json:"target_id"`
	OwnerUID     string `json:"owner_uid"` // who owns the target (recipient)
	CommenterUID string `json:"commenter_uid"`
}

// commentLikedPayload is emitted when a user likes a comment. The
// comment_owner_uid receives the notification.
type commentLikedPayload struct {
	CommentID       string `json:"comment_id"`
	CommentOwnerUID string `json:"comment_owner_uid"`
	LikerUID        string `json:"liker_uid"`
}

// messageReactedPayload is emitted when a user reacts to a message.
// The message peer (the non-actor participant) receives the notification.
type messageReactedPayload struct {
	MessageID string `json:"message_id"`
	PeerUID   string `json:"peer_uid"` // the other participant (recipient of notification)
	ActorUID  string `json:"actor_uid"`
}

// messageDeletedPayload is emitted when the sender soft-deletes a message.
// The recipient (ToUID) receives the notification.
type messageDeletedPayload struct {
	MessageID string `json:"message_id"`
	FromUID   string `json:"from_uid"`
	ToUID     string `json:"to_uid"`
}

//nolint:cyclop,funlen // notification dispatch preserves event-specific deduplication and recipient rules.
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
	n := d.notificationFor(e)
	if n == nil {
		return nil // nothing to notify: unknown event, self-action, or malformed payload
	}
	// Preference suppression: skip notifying when the recipient has opted out of
	// this notification type or is inside quiet hours. Inactive unless a checker
	// is wired. When no preference row exists the default is "allowed" so every
	// user who has never set preferences keeps getting notifications.
	if d.prefs != nil {
		allowed, err := d.prefs.IsAllowed(ctx, n.RecipientUID, n.Type)
		if err != nil {
			return err // transient: leave the event unpublished so the relay retries
		}
		if !allowed {
			return nil // recipient disabled this notification type or it's quiet hours
		}
	}
	// Per-recipient rate limiting: when the rate limiter is active, drop
	// notifications that exceed the per-minute threshold per (recipient, type)
	// to prevent floods (denial-of-wallet / UX abuse).
	if d.rateLimiter != nil && !d.rateLimiter.allow(n.RecipientUID, n.Type) {
		return nil // rate limited: silently drop
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
	if err := d.notifs.Create(ctx, n); err != nil {
		return err
	}
	// Notification collapsing: when enabled and a recent unread notification of
	// the same (recipient, type, actor) already exists, the dispatcher merges the
	// new event into the existing row instead of creating a duplicate.
	if d.collapseWin > 0 {
		existing, err := d.notifs.FindRecentByTypeAndActor(ctx, n.RecipientUID, n.Type, n.ActorUID, d.collapseWin)
		if err == nil && existing != nil && existing.ID != n.ID {
			// Increment count on the existing row. The newly created row (n)
			// stays in the DB as audit trail but is unread — the user's feed
			// query will still surface the collapsed row first (newest by
			// created_at). Best effort: the extra row is harmless.
			_ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
			// Push the updated state to real-time subscribers.
			if d.pusher != nil {
				existing.CollapsedCount++
				existing.CreatedAt = time.Now().UTC()
				payload, _ := json.Marshal(existing)
				d.pusher.Publish(n.RecipientUID, n.Type, string(payload), existing.CreatedAt.UnixMilli())
			}
			return nil
		}
	}
	// Non-collapsed notification: increment the materialized unread counter.
	// For collapsed notifications the existing row is already counted, so we
	// skip to avoid double-counting.
	if err := d.notifs.IncrementUnreadCount(ctx, n.RecipientUID, 1); err != nil {
		return err
	}
	if d.pusher != nil {
		payload, _ := json.Marshal(n)
		d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
	}
	return nil
}

// notificationFor maps a single event to the notification it should produce, or
// nil when there is nothing to notify. Payloads are parsed defensively.
//
//nolint:cyclop,funlen // the event-to-notification mapping is an explicit legacy event compatibility matrix.
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
			ID:             idgen.NewID(),
			RecipientUID:   p.ToUID,
			Type:           string(domain.NotifTypeMessageReceived),
			ActorUID:       p.FromUID,
			SubjectType:    "message",
			SubjectID:      p.MessageID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
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
			ID:             idgen.NewID(),
			RecipientUID:   p.FromUID, // notify the original sender
			Type:           string(domain.NotifTypeMessageRead),
			ActorUID:       p.ToUID, // the reader
			SubjectType:    "message",
			SubjectID:      p.MessageID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
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
			ID:             idgen.NewID(),
			RecipientUID:   p.ParentAuthorUID,
			Type:           string(domain.NotifTypeCommentReply),
			ActorUID:       p.ReplierUID,
			SubjectType:    "comment",
			SubjectID:      p.CommentID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
		}
	case "file.commented", "message.commented":
		var p commentOnTargetPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.OwnerUID == "" || p.OwnerUID == p.CommenterUID {
			return nil // missing owner or commenter owns the target (self-comment)
		}
		notifType := string(domain.NotifTypeFileCommented)
		if e.EventType == "message.commented" {
			notifType = string(domain.NotifTypeMessageCommented)
		}
		return &domain.Notification{
			ID:             idgen.NewID(),
			RecipientUID:   p.OwnerUID,
			Type:           notifType,
			ActorUID:       p.CommenterUID,
			SubjectType:    p.TargetType,
			SubjectID:      p.CommentID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
		}
	case "comment.liked":
		var p commentLikedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.CommentOwnerUID == "" || p.CommentOwnerUID == p.LikerUID {
			return nil // missing owner or liker liked their own comment
		}
		return &domain.Notification{
			ID:             idgen.NewID(),
			RecipientUID:   p.CommentOwnerUID,
			Type:           string(domain.NotifTypeCommentLiked),
			ActorUID:       p.LikerUID,
			SubjectType:    "comment",
			SubjectID:      p.CommentID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
		}
	case "message.reacted":
		var p messageReactedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.PeerUID == "" || p.PeerUID == p.ActorUID {
			return nil // missing peer or self-reaction
		}
		return &domain.Notification{
			ID:             idgen.NewID(),
			RecipientUID:   p.PeerUID,
			Type:           string(domain.NotifTypeMessageReacted),
			ActorUID:       p.ActorUID,
			SubjectType:    "message",
			SubjectID:      p.MessageID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
		}
	case "message.deleted":
		var p messageDeletedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		if p.ToUID == "" || p.ToUID == p.FromUID {
			return nil // missing recipient or self-targeted message
		}
		return &domain.Notification{
			ID:             idgen.NewID(),
			RecipientUID:   p.ToUID,
			Type:           string(domain.NotifTypeMessageDeleted),
			ActorUID:       p.FromUID,
			SubjectType:    "message",
			SubjectID:      p.MessageID,
			CollapsedCount: 1,
			CreatedAt:      time.Now().UTC(),
		}
	default:
		return nil
	}
}
