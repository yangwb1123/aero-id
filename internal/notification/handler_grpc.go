package notification

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/sso"
)

type NotificationServer struct {
	aeropb.UnimplementedNotificationServiceServer
	notifs Service
}

func NewNotificationServer(notifs Service) *NotificationServer {
	return &NotificationServer{notifs: notifs}
}

func (s *NotificationServer) Register(srv *grpc.Server) {
	aeropb.RegisterNotificationServiceServer(srv, s)
}

func (s *NotificationServer) ListNotifications(ctx context.Context, req *aeropb.ListNotificationsRequest) (*aeropb.ListNotificationsResponse, error) {
	list, err := s.notifs.List(ctx, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ListNotificationsResponse{
		Notifications: notifsToPB(list.Notifications),
		Users:         usersToPB(list.Users),
		NextCursor:    list.NextCursor,
	}, nil
}

func (s *NotificationServer) MarkRead(ctx context.Context, req *aeropb.MarkNotificationReadRequest) (*aeropb.MarkNotificationReadResponse, error) {
	id, err := parseID(req.Id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	if err := s.notifs.MarkRead(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.MarkNotificationReadResponse{}, nil
}

func (s *NotificationServer) MarkAllRead(ctx context.Context, _ *aeropb.MarkAllNotificationsReadRequest) (*aeropb.MarkAllNotificationsReadResponse, error) {
	if err := s.notifs.MarkAllRead(ctx); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.MarkAllNotificationsReadResponse{}, nil
}

func (s *NotificationServer) UnreadCount(ctx context.Context, _ *aeropb.NotificationUnreadCountRequest) (*aeropb.NotificationUnreadCountResponse, error) {
	n, err := s.notifs.UnreadCount(ctx)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.NotificationUnreadCountResponse{UnreadCount: n}, nil
}

func pageFromPB(p *aeropb.Page) domain.Page {
	if p == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(p.Limit), Cursor: p.Cursor}
}

func parseID(s string) (domain.ID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return domain.ID{}, apperr.BadRequest("notification.invalid_id", "invalid notification ID")
	}
	return domain.ID(id), nil
}

func notifsToPB(ns []*Notification) []*aeropb.Notification {
	out := make([]*aeropb.Notification, len(ns))
	for i, n := range ns {
		out[i] = &aeropb.Notification{
			Id:        n.ID.String(),
			Type:      n.Type,
			ActorUid:  n.ActorUID,
			SubjectId: n.SubjectID,
		}
	}
	return out
}

func usersToPB(users map[string]sso.User) map[string]*aeropb.User {
	out := make(map[string]*aeropb.User, len(users))
	for uid, u := range users {
		out[uid] = &aeropb.User{Uid: u.UID, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out
}
