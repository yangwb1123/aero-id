package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type NotificationServer struct {
	aeropb.UnimplementedNotificationServiceServer
	notifs service.NotificationService
}

func NewNotificationServer(notifs service.NotificationService) *NotificationServer {
	return &NotificationServer{notifs: notifs}
}

func (s *NotificationServer) Register(srv *grpc.Server) {
	aeropb.RegisterNotificationServiceServer(srv, s)
}

func (s *NotificationServer) ListNotifications(ctx context.Context, req *aeropb.ListNotificationsRequest) (*aeropb.ListNotificationsResponse, error) {
	list, err := s.notifs.List(ctx, pageFromPB(req.Page))
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListNotificationsResponse{
		Notifications: notificationsToPB(list.Notifications),
		Users:         usersToPB(list.Users),
		NextCursor:    list.NextCursor,
	}, nil
}

func (s *NotificationServer) UnreadCount(ctx context.Context, _ *aeropb.NotificationUnreadCountRequest) (*aeropb.NotificationUnreadCountResponse, error) {
	n, err := s.notifs.UnreadCount(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.NotificationUnreadCountResponse{UnreadCount: n}, nil
}

func (s *NotificationServer) MarkRead(ctx context.Context, req *aeropb.MarkNotificationReadRequest) (*aeropb.MarkNotificationReadResponse, error) {
	id, err := idgen.Parse(req.Id)
	if err != nil {
		return nil, grpcError(apperr.BadRequest("request.invalid_id", "invalid id"))
	}
	if err := s.notifs.MarkRead(ctx, id); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.MarkNotificationReadResponse{}, nil
}

func (s *NotificationServer) MarkAllRead(ctx context.Context, _ *aeropb.MarkAllNotificationsReadRequest) (*aeropb.MarkAllNotificationsReadResponse, error) {
	if err := s.notifs.MarkAllRead(ctx); err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.MarkAllNotificationsReadResponse{}, nil
}

func notificationToPB(n *domain.Notification) *aeropb.Notification {
	pb := &aeropb.Notification{
		Id:           n.ID.String(),
		RecipientUid: n.RecipientUID,
		Type:         n.Type,
		ActorUid:     n.ActorUID,
		SubjectType:  n.SubjectType,
		SubjectId:    n.SubjectID,
		CreatedAt:    timestamppb.New(n.CreatedAt),
	}
	if n.ReadAt != nil {
		pb.ReadAt = timestamppb.New(*n.ReadAt)
	}
	return pb
}

func notificationsToPB(ns []*domain.Notification) []*aeropb.Notification {
	out := make([]*aeropb.Notification, 0, len(ns))
	for _, n := range ns {
		out = append(out, notificationToPB(n))
	}
	return out
}
