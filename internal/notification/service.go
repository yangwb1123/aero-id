package notification

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func (s *notificationService) List(ctx context.Context, page domain.Page) (*NotificationList, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	ns, next, err := s.notifs.ListByRecipient(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("notification.list_failed", "failed to list notifications").Wrap(err)
	}
	users, err := s.dir.GetUsers(ctx, actorUIDs(ns))
	if err != nil {
		return nil, apperr.Internal("notification.enrich_failed", "failed to resolve actors").Wrap(err)
	}
	return &NotificationList{Notifications: ns, NextCursor: next, Users: users}, nil
}

func (s *notificationService) MarkRead(ctx context.Context, id domain.ID) error {
	if _, err := domain.MustActor(ctx); err != nil {
		return err
	}
	if err := s.notifs.MarkRead(ctx, id, time.Now().UTC()); err != nil {
		return apperr.Internal("notification.mark_read_failed", "failed to mark notification read").Wrap(err)
	}
	return nil
}

func (s *notificationService) MarkAllRead(ctx context.Context) error {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return err
	}
	if err := s.notifs.MarkAllRead(ctx, actor, time.Now().UTC()); err != nil {
		return apperr.Internal("notification.mark_all_read_failed", "failed to mark notifications read").Wrap(err)
	}
	return nil
}

func (s *notificationService) UnreadCount(ctx context.Context) (int64, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return 0, err
	}
	n, err := s.notifs.CountUnread(ctx, actor)
	if err != nil {
		return 0, apperr.Internal("notification.unread_count_failed", "failed to count unread notifications").Wrap(err)
	}
	return n, nil
}

func actorUIDs(ns []*Notification) []string {
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
