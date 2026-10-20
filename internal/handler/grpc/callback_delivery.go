package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func (s *CallbackServer) ListCallbackDeliveries(ctx context.Context,
	req *aeropb.ListCallbackDeliveriesRequest) (*aeropb.ListCallbackDeliveriesResponse, error) {
	id, err := parseID(req.GetSubscriptionId(), "subscription_id")
	if err != nil {
		return nil, err
	}
	items, next, err := s.callbacks.ListDeliveries(ctx, id,
		domain.CallbackDeliveryFilter{Status: req.GetStatus(), Page: pageFromProto(req.GetPage())})
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result := make([]*aeropb.CallbackDeliveryMetadata, len(items))
	for i, item := range items {
		result[i] = callbackDeliveryToProto(item)
	}
	return &aeropb.ListCallbackDeliveriesResponse{Deliveries: result, NextCursor: next}, nil
}

func (s *CallbackServer) GetCallbackDelivery(ctx context.Context, req *aeropb.GetCallbackDeliveryRequest) (*aeropb.GetCallbackDeliveryResponse, error) {
	subscriptionID, err := parseID(req.GetSubscriptionId(), "subscription_id")
	if err != nil {
		return nil, err
	}
	deliveryID, err := parseID(req.GetDeliveryId(), "delivery_id")
	if err != nil {
		return nil, err
	}
	item, err := s.callbacks.GetDeliveryMetadata(ctx, subscriptionID, deliveryID)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.GetCallbackDeliveryResponse{Delivery: callbackDeliveryToProto(item)}, nil
}

func callbackDeliveryToProto(item *domain.CallbackDeliveryMetadata) *aeropb.CallbackDeliveryMetadata {
	if item == nil {
		return nil
	}
	result := &aeropb.CallbackDeliveryMetadata{Id: item.ID.String(), SubscriptionId: item.SubscriptionID.String(),
		EventId: item.EventID.String(), TenantId: item.TenantID, Action: item.Action, Status: item.Status, Attempt: item.Attempt,
		Revision: item.Revision, LeaseGeneration: item.LeaseGeneration, NextAttemptAt: timestamppb.New(item.NextAttemptAt), StatusCode: item.StatusCode,
		ErrorCode: item.ErrorCode, CreatedAt: timestamppb.New(item.CreatedAt), UpdatedAt: timestamppb.New(item.UpdatedAt)}
	if item.LeaseUntil != nil {
		result.LeaseUntil = timestamppb.New(*item.LeaseUntil)
	}
	return result
}
