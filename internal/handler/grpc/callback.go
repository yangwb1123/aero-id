package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type CallbackServer struct {
	aeropb.UnimplementedCallbackServiceServer
	callbacks service.CallbackService
}

func NewCallbackServer(callbacks service.CallbackService) *CallbackServer {
	return &CallbackServer{callbacks: callbacks}
}

func (s *CallbackServer) Register(server *grpc.Server) {
	aeropb.RegisterCallbackServiceServer(server, s)
}

var _ aeropb.CallbackServiceServer = (*CallbackServer)(nil)

func (s *CallbackServer) RegisterCallback(ctx context.Context, req *aeropb.RegisterCallbackRequest) (*aeropb.RegisterCallbackResponse, error) {
	subscription, secret, err := s.callbacks.Register(ctx, service.CallbackInput{
		Endpoint: req.GetEndpoint(), TenantID: req.GetTenantId(), EventTypes: req.GetEventTypes()})
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.RegisterCallbackResponse{Subscription: callbackSubscriptionToProto(subscription), Secret: secret}, nil
}

func (s *CallbackServer) ListCallbacks(ctx context.Context, req *aeropb.ListCallbacksRequest) (*aeropb.ListCallbacksResponse, error) {
	items, next, err := s.callbacks.List(ctx, pageFromProto(req.GetPage()))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result := make([]*aeropb.CallbackSubscription, len(items))
	for i, item := range items {
		result[i] = callbackSubscriptionToProto(item)
	}
	return &aeropb.ListCallbacksResponse{Subscriptions: result, NextCursor: next}, nil
}

func (s *CallbackServer) DeleteCallback(ctx context.Context, req *aeropb.DeleteCallbackRequest) (*aeropb.DeleteCallbackResponse, error) {
	id, err := parseID(req.GetId(), "callback_id")
	if err != nil {
		return nil, err
	}
	if err := s.callbacks.Delete(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.DeleteCallbackResponse{}, nil
}

func (s *CallbackServer) ReplayCallbackDelivery(ctx context.Context,
	req *aeropb.ReplayCallbackDeliveryRequest) (*aeropb.ReplayCallbackDeliveryResponse, error) {
	id, err := parseID(req.GetId(), "delivery_id")
	if err != nil {
		return nil, err
	}
	if err := s.callbacks.ReplayDelivery(ctx, id); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ReplayCallbackDeliveryResponse{}, nil
}

func callbackSubscriptionToProto(item *domain.CallbackSubscription) *aeropb.CallbackSubscription {
	if item == nil {
		return nil
	}
	return &aeropb.CallbackSubscription{Id: item.ID.String(), OwnerUid: item.OwnerUID, TenantId: item.TenantID,
		Endpoint: item.Endpoint, EventTypes: item.EventTypes, Enabled: item.Enabled, Verified: item.Verified}
}
