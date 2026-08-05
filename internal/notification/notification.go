package notification

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/sso"
)

type Notification = domain.Notification

type Repository interface {
	Create(ctx context.Context, n *Notification) error
	ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*Notification, string, error)
	MarkRead(ctx context.Context, id domain.ID, readAt time.Time) error
	MarkAllRead(ctx context.Context, recipientUID string, readAt time.Time) error
	CountUnread(ctx context.Context, recipientUID string) (int64, error)
}

type UserDirectory interface {
	GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}

type NotificationList struct {
	Notifications []*Notification
	NextCursor    string
	Users         map[string]sso.User
}

type Service interface {
	List(ctx context.Context, page domain.Page) (*NotificationList, error)
	MarkRead(ctx context.Context, id domain.ID) error
	MarkAllRead(ctx context.Context) error
	UnreadCount(ctx context.Context) (int64, error)
}

type notificationService struct {
	notifs Repository
	dir    UserDirectory
}

func NewService(notifs Repository, dir UserDirectory) Service {
	return &notificationService{notifs: notifs, dir: dir}
}
