package service

import (
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
)

func safeCallbackDeliveryMetadata(item *domain.CallbackDeliveryMetadata, subscriptionID domain.ID, tenant string) (*domain.CallbackDeliveryMetadata, error) {
	if item == nil || item.ID == (domain.ID{}) || item.SubscriptionID != subscriptionID || item.TenantID != tenant {
		return nil, callbackDeliveryReadError(errors.New("callback delivery scope differs"))
	}
	copy := *item
	copy.Status = callbackMetadataStatus(copy.Status)
	if len(copy.Action) == 0 || len(copy.Action) > 128 || strings.IndexFunc(copy.Action, invalidCallbackActionRune) >= 0 {
		copy.Action = "unknown"
	}
	copy.ErrorCode = callbackMetadataErrorCode(copy.ErrorCode)
	return &copy, nil
}

func callbackMetadataStatus(value string) string {
	switch value {
	case "pending", "delivering", "success", "dead_letter":
		return value
	default:
		return "unknown"
	}
}

// Historical/source-specific diagnostics are not an unrestricted error channel.
func callbackMetadataErrorCode(value string) string {
	switch value {
	case "", "callback.delivery_failed", "callback.secret_unavailable", "manual_replay":
		return value
	default:
		return "callback.error_unknown"
	}
}

func invalidCallbackActionRune(value rune) bool {
	return !(value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("._:-", value))
}
