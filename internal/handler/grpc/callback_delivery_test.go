package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type callbackDeliveryGRPCFake struct {
	service.CallbackService
	item     *domain.CallbackDeliveryMetadata
	sub      domain.ID
	delivery domain.ID
	filter   domain.CallbackDeliveryFilter
	fail     error
}

func (f *callbackDeliveryGRPCFake) ListDeliveries(_ context.Context, id domain.ID, filter domain.CallbackDeliveryFilter) ([]*domain.CallbackDeliveryMetadata, string, error) {
	f.sub, f.filter = id, filter
	return []*domain.CallbackDeliveryMetadata{f.item}, "", f.fail
}
func (f *callbackDeliveryGRPCFake) GetDeliveryMetadata(_ context.Context, sub, id domain.ID) (*domain.CallbackDeliveryMetadata, error) {
	f.sub, f.delivery = sub, id
	return f.item, f.fail
}
func TestCallbackDeliveryGRPCUsesSharedReadServiceAndMapper(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	fake := &callbackDeliveryGRPCFake{item: &domain.CallbackDeliveryMetadata{ID: idgen.NewID(), SubscriptionID: idgen.NewID(), EventID: idgen.NewID(),
		TenantID: "tenant-a", Action: "account.provisioned", Status: "delivering", LeaseGeneration: 4, LeaseUntil: &now, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}}
	server := NewCallbackServer(fake)
	page, err := server.ListCallbackDeliveries(t.Context(), &aeropb.ListCallbackDeliveriesRequest{SubscriptionId: fake.item.SubscriptionID.String(), Status: "delivering", Page: &aeropb.Page{Limit: 7}})
	if err != nil || len(page.GetDeliveries()) != 1 || fake.sub != fake.item.SubscriptionID || fake.filter.Status != "delivering" || fake.filter.Page.Limit != 7 {
		t.Fatal("list service contract differs", page, err)
	}
	item, err := server.GetCallbackDelivery(t.Context(), &aeropb.GetCallbackDeliveryRequest{SubscriptionId: fake.item.SubscriptionID.String(), DeliveryId: fake.item.ID.String()})
	if err != nil || fake.delivery != fake.item.ID || item.GetDelivery().GetLeaseGeneration() != 4 || !item.GetDelivery().GetLeaseUntil().AsTime().Equal(now) {
		t.Fatal("detail mapping differs", item, err)
	}
	fake.fail = apperr.Unavailable("callback.delivery_read_failed", "callback delivery read or audit persistence failed")
	item, err = server.GetCallbackDelivery(t.Context(), &aeropb.GetCallbackDeliveryRequest{SubscriptionId: fake.item.SubscriptionID.String(), DeliveryId: fake.item.ID.String()})
	if item != nil || status.Code(err) != codes.Unavailable {
		t.Fatal("shared error mapping differs", item, err)
	}
	_, err = server.ListCallbackDeliveries(t.Context(), &aeropb.ListCallbackDeliveriesRequest{SubscriptionId: "invalid"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
