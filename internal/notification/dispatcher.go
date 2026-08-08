package notification

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// blockChecker is the package-local view of the block store used by the
// notification guard (the unwired internal/block dead tree was deleted; the
// live service.BlockChecker satisfies this interface structurally).
type blockChecker interface {
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
}

type Dispatcher struct {
	notifs Repository
	blocks blockChecker
	pusher hub.Pusher
}

type DispatcherOption func(*Dispatcher)

func WithDispatcherBlockChecker(blocks blockChecker) DispatcherOption {
	return func(d *Dispatcher) { d.blocks = blocks }
}

func WithDispatcherPusher(p hub.Pusher) DispatcherOption {
	return func(d *Dispatcher) { d.pusher = p }
}

func NewDispatcher(notifs Repository, opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{notifs: notifs}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

type messageSentPayload struct {
	MessageID string `json:"message_id"`
	FromUID   string `json:"from_uid"`
	ToUID     string `json:"to_uid"`
}

type messageReadPayload struct {
	MessageID string `json:"message_id"`
	FromUID   string `json:"from_uid"`
	ToUID     string `json:"to_uid"`
}

type commentRepliedPayload struct {
	CommentID       string `json:"comment_id"`
	ParentAuthorUID string `json:"parent_author_uid"`
	ReplierUID      string `json:"replier_uid"`
}

func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
	n := d.notificationFor(e)
	if n == nil {
		return nil
	}
	if d.blocks != nil && n.ActorUID != "" {
		blocked, err := d.blocks.IsBlocked(ctx, n.RecipientUID, n.ActorUID)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
	}
	if err := d.notifs.Create(ctx, n); err != nil {
		return err
	}
	if d.pusher != nil {
		payload, _ := json.Marshal(n)
		d.pusher.Publish(n.RecipientUID, n.Type, string(payload))
	}
	return nil
}

func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
	switch e.EventType {
	case "message.sent":
		return d.notificationMessageSent(e)
	case "message.read":
		return d.notificationMessageRead(e)
	case "comment.replied":
		return d.notificationCommentReplied(e)
	default:
		return nil
	}
}

func (d *Dispatcher) notificationMessageSent(e *outbox.Event) *domain.Notification {
	var p messageSentPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil
	}
	if p.ToUID == "" || p.ToUID == p.FromUID {
		return nil
	}
	return &domain.Notification{
		ID: idgen.NewID(), RecipientUID: p.ToUID,
		Type: "message.received", ActorUID: p.FromUID,
		SubjectType: "message", SubjectID: p.MessageID,
		CreatedAt: time.Now().UTC(),
	}
}

func (d *Dispatcher) notificationMessageRead(e *outbox.Event) *domain.Notification {
	var p messageReadPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil
	}
	if p.FromUID == "" || p.FromUID == p.ToUID {
		return nil
	}
	return &domain.Notification{
		ID: idgen.NewID(), RecipientUID: p.FromUID,
		Type: "message.read_receipt", ActorUID: p.ToUID,
		SubjectType: "message", SubjectID: p.MessageID,
		CreatedAt: time.Now().UTC(),
	}
}

func (d *Dispatcher) notificationCommentReplied(e *outbox.Event) *domain.Notification {
	var p commentRepliedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil
	}
	if p.ParentAuthorUID == "" || p.ParentAuthorUID == p.ReplierUID {
		return nil
	}
	return &domain.Notification{
		ID: idgen.NewID(), RecipientUID: p.ParentAuthorUID,
		Type: "comment.reply", ActorUID: p.ReplierUID,
		SubjectType: "comment", SubjectID: p.CommentID,
		CreatedAt: time.Now().UTC(),
	}
}
