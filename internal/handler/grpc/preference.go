package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

// PreferenceServer implements the aeropb.PreferenceServiceServer.
type PreferenceServer struct {
	aeropb.UnimplementedPreferenceServiceServer
	svc service.PreferenceService
}

func NewPreferenceServer(svc service.PreferenceService) *PreferenceServer {
	return &PreferenceServer{svc: svc}
}

func (s *PreferenceServer) Register(srv *grpc.Server) { aeropb.RegisterPreferenceServiceServer(srv, s) }

// compile-time check
var _ aeropb.PreferenceServiceServer = (*PreferenceServer)(nil)

func (s *PreferenceServer) GetPreferences(ctx context.Context, _ *aeropb.GetPreferencesRequest) (*aeropb.GetPreferencesResponse, error) {
	prefs, err := s.svc.List(ctx)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	pb := make([]*aeropb.NotificationPreference, len(prefs))
	for i, p := range prefs {
		pb[i] = prefToProto(p)
	}
	return &aeropb.GetPreferencesResponse{Preferences: pb}, nil
}

func (s *PreferenceServer) SetPreferences(ctx context.Context, req *aeropb.SetPreferencesRequest) (*aeropb.SetPreferencesResponse, error) {
	prefs := make([]*domain.NotificationPreference, len(req.Preferences))
	for i, p := range req.Preferences {
		if p == nil {
			continue
		}
		prefs[i] = &domain.NotificationPreference{
			NotificationType: p.NotificationType,
			Enabled:          p.Enabled,
			QuietHoursStart:  p.QuietHoursStart,
			QuietHoursEnd:    p.QuietHoursEnd,
			Timezone:         p.Timezone,
		}
	}
	if err := s.svc.Set(ctx, prefs); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.SetPreferencesResponse{}, nil
}

func prefToProto(p *domain.NotificationPreference) *aeropb.NotificationPreference {
	return &aeropb.NotificationPreference{
		NotificationType: p.NotificationType,
		Enabled:          p.Enabled,
		QuietHoursStart:  p.QuietHoursStart,
		QuietHoursEnd:    p.QuietHoursEnd,
		Timezone:         p.Timezone,
	}
}
