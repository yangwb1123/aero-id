package message

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/sso"
)

// ConversationSummary is the aggregated representation of a peer conversation.
type ConversationSummary struct {
	PeerUID     string          // the other participant in the conversation
	LastMessage *domain.Message // most recent message in the conversation
	UnreadCount int64           // number of unread messages from this peer
}

// ConversationList is the paginated result of ListConversations.
type ConversationList struct {
	Conversations []*ConversationSummary
	NextCursor    string
	Users         map[string]sso.User
}

// FileRepository is the minimal file store needed for message file validation.
// In production this is satisfied by file.Repository.
type FileRepository interface {
	Get(ctx context.Context, id domain.ID) (*domain.File, error)
}

type SendMessageInput struct {
	ToUID   string
	Kind    domain.MessageKind
	Content string
	FileID  *domain.ID
	Meta    map[string]any
}

type MessageList struct {
	Messages   []*domain.Message
	NextCursor string
	Users      map[string]sso.User
}

type MessageRepository interface {
	Create(ctx context.Context, m *domain.Message, evt *outbox.Event) error
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	ListConversations(ctx context.Context, uid string, page domain.Page) ([]*ConversationSummary, string, error)
	MarkRead(ctx context.Context, id domain.ID, at time.Time) error
	MarkReadWithEvent(ctx context.Context, id domain.ID, at time.Time, evt *outbox.Event) error
	CountUnread(ctx context.Context, toUID string) (int64, error)
	CountUnreadFrom(ctx context.Context, toUID, fromUID string) (int64, error)
	UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error
	SoftDelete(ctx context.Context, id domain.ID) error
	MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error
	ReactToMessage(ctx context.Context, messageID domain.ID, uid string) error
	UnreactMessage(ctx context.Context, messageID domain.ID, uid string) error
	SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error)
}

type BlockChecker interface {
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
}

type UserDirectory interface {
	GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}

type Service interface {
	Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
	SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error)
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error)
	Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error)
	ListConversations(ctx context.Context, page domain.Page) (*ConversationList, error)
	MarkRead(ctx context.Context, id domain.ID) error
	MarkConversationRead(ctx context.Context, withUID string) error
	UnreadCount(ctx context.Context) (int64, error)
	Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error)
	Delete(ctx context.Context, id domain.ID) error
	ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error
	UnreactMessage(ctx context.Context, messageID domain.ID, actorUID string) error
}

type service struct {
	messages MessageRepository
	files    FileRepository
	dir      UserDirectory
	blocks   BlockChecker
	audit    domain.AuditLogger
}

type Option func(*service)

func WithMessageAuditLogger(a domain.AuditLogger) Option {
	return func(s *service) { s.audit = a }
}

func WithBlockChecker(blocks BlockChecker) Option {
	return func(s *service) { s.blocks = blocks }
}

func NewService(messages MessageRepository, files FileRepository, dir UserDirectory, opts ...Option) Service {
	s := &service{messages: messages, files: files, dir: dir}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func canReadMessage(actor string, m *domain.Message) bool {
	return m.FromUID == actor || m.ToUID == actor
}

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

func enrichUsers(ctx context.Context, dir UserDirectory, uids []string) (map[string]sso.User, error) {
	if len(uids) == 0 {
		return map[string]sso.User{}, nil
	}
	return dir.GetUsers(ctx, uids)
}

func mustActor(ctx context.Context) (string, error) {
	uid, ok := domain.ActorFrom(ctx)
	if !ok {
		return "", apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
	}
	return uid, nil
}
