package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type CallbackDeliveryService interface {
	ListDeliveries(context.Context, domain.ID, domain.CallbackDeliveryFilter) ([]*domain.CallbackDeliveryMetadata, string, error)
	GetDeliveryMetadata(context.Context, domain.ID, domain.ID) (*domain.CallbackDeliveryMetadata, error)
}

// CallbackDeliveryPermissionSetter is a composition-time seam. Call before
// publishing the service or starting workers, never mutate a live checker.
type CallbackDeliveryPermissionSetter interface {
	SetCallbackDeliveryPermission(PermissionChecker)
}

func (s *callbackService) SetCallbackDeliveryPermission(checker PermissionChecker) {
	attached := false
	s.deliveryPermissionOnce.Do(func() {
		s.deliveryPermission = checker
		attached = true
	})
	if !attached {
		panic("callback: delivery permission is immutable after composition")
	}
}

// Start/read freezes missing configuration too; a late setter may not repair
// or replace a dependency on an already published service.
func (s *callbackService) callbackDeliveryPermission() PermissionChecker {
	s.deliveryPermissionOnce.Do(func() {})
	return s.deliveryPermission
}

const callbackDeliveryReadBudget = 3 * time.Second

func (s *callbackService) ListDeliveries(ctx context.Context, subscriptionID domain.ID,
	filter domain.CallbackDeliveryFilter) ([]*domain.CallbackDeliveryMetadata, string, error) {
	ctx, cancel := context.WithTimeout(ctx, callbackDeliveryReadBudget)
	defer cancel()
	identity, repo, err := s.callbackDeliveryReadAccess(ctx, subscriptionID)
	if err != nil {
		return nil, "", err
	}
	filter, err = normalizeCallbackDeliveryFilter(filter)
	if err != nil {
		return nil, "", err
	}
	fact := callbackDeliveryReadFact(ctx, "callback.deliveries.read", "callback_subscription", subscriptionID,
		map[string]any{"status": filter.Status, "page_limit": filter.Page.Limit, "cursor_present": filter.Page.Cursor != ""})
	items, next, err := repo.ListDeliveryMetadataWithAudit(ctx, identity.UID, identity.TenantID, subscriptionID, filter, fact)
	if err != nil {
		return nil, "", callbackDeliveryReadError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", callbackDeliveryReadError(err)
	}
	for i, item := range items {
		items[i], err = safeCallbackDeliveryMetadata(item, subscriptionID, identity.TenantID)
		if err != nil {
			return nil, "", err
		}
	}
	return items, next, nil
}

func (s *callbackService) GetDeliveryMetadata(ctx context.Context, subscriptionID, deliveryID domain.ID) (*domain.CallbackDeliveryMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, callbackDeliveryReadBudget)
	defer cancel()
	identity, repo, err := s.callbackDeliveryReadAccess(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	if deliveryID == (domain.ID{}) {
		return nil, apperr.BadRequest("callback.delivery_id_invalid", "callback delivery ID is invalid")
	}
	fact := callbackDeliveryReadFact(ctx, "callback.delivery.read", "callback_delivery", deliveryID, nil)
	item, err := repo.GetDeliveryMetadataWithAudit(ctx, identity.UID, identity.TenantID, subscriptionID, deliveryID, fact)
	if err != nil {
		return nil, callbackDeliveryReadError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, callbackDeliveryReadError(err)
	}
	if item == nil || item.ID != deliveryID {
		return nil, callbackDeliveryReadError(errors.New("callback delivery scope differs"))
	}
	return safeCallbackDeliveryMetadata(item, subscriptionID, identity.TenantID)
}

func (s *callbackService) callbackDeliveryReadAccess(ctx context.Context,
	subscriptionID domain.ID) (*domain.TokenIdentity, repository.CallbackDeliveryReader, error) {
	identity, err := verifiedControlPlaneIdentity(ctx)
	if err != nil {
		return nil, nil, err
	}
	if subscriptionID == (domain.ID{}) {
		return nil, nil, apperr.BadRequest("callback.subscription_id_invalid", "callback subscription ID is invalid")
	}
	checker := s.callbackDeliveryPermission()
	if isNilExecutionDependency(checker) {
		return nil, nil, apperr.Unavailable("callback.authorization_unavailable", "current callback authorization is unavailable")
	}
	allowed, err := checker.HasPermission(ctx, identity.UID, domain.PermCallbackManage)
	if err != nil || ctx.Err() != nil {
		return nil, nil, apperr.Unavailable("callback.authorization_unavailable", "current callback authorization is unavailable")
	}
	if !allowed {
		return nil, nil, apperr.Forbidden("callback.authorization_denied", "current callback action is not authorized")
	}
	repo, ok := s.repo.(repository.CallbackDeliveryReader)
	if !ok || isNilExecutionDependency(repo) {
		return nil, nil, apperr.Unavailable("callback.delivery_read_unavailable", "callback delivery reads are unavailable")
	}
	return identity, repo, nil
}

func normalizeCallbackDeliveryFilter(filter domain.CallbackDeliveryFilter) (domain.CallbackDeliveryFilter, error) {
	filter.Status = strings.TrimSpace(filter.Status)
	switch filter.Status {
	case "", "pending", "delivering", "success", "dead_letter":
	default:
		return filter, apperr.BadRequest("callback.delivery_status_invalid", "callback delivery status filter is invalid")
	}
	if filter.Page.Cursor != "" {
		id, err := idgen.Parse(filter.Page.Cursor)
		if err != nil || id == (domain.ID{}) {
			return filter, apperr.BadRequest("callback.delivery_cursor_invalid", "callback delivery cursor is invalid")
		}
		filter.Page.Cursor = id.String()
	}
	filter.Page.Normalize()
	return filter, nil
}

func callbackDeliveryReadFact(ctx context.Context, action, resourceType string, id domain.ID, payload map[string]any) *domain.AuditFact {
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", Action: action,
		ResourceType: resourceType, ResourceID: id.String(), Payload: payload})
	// The persisted fact ID is this read execution's identity, never a
	// resource-wide dedup anchor or a browser/transport idempotency key.
	fact.EventID = fact.ID
	return fact
}

func callbackDeliveryReadError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("callback.delivery_not_found", "callback subscription or delivery not found")
	}
	return apperr.Unavailable("callback.delivery_read_failed", "callback delivery read or audit persistence failed").Wrap(err)
}
