package service

import (
	"context"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func allowedOperationType(value string) bool {
	switch value {
	case "sync", "erase", "source.command", "replay":
		return true
	default:
		return false
	}
}

func allowedOperationSource(value string) bool {
	switch strings.TrimSpace(value) {
	case "snaplink", "aero-im", "aero-vault":
		return true
	default:
		return false
	}
}

func allowedOperationAction(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sync", "summary", "source.summary", "erase", "delete", "source.erase":
		return true
	default:
		return false
	}
}

func operationPermission(operationType string) string {
	switch operationType {
	case "erase":
		return domain.PermAccountErase
	case "replay":
		return domain.PermAuditReplay
	case "sync":
		return domain.PermSourceSync
	default:
		return domain.PermOperationManage
	}
}

// operationActionPermission is the permission implied by the remote effect,
// rather than by the transport-shaped operation type. In particular,
// source.command is not a wildcard escape hatch for destructive actions.
func operationActionPermission(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "sync", "summary", "source.summary":
		return domain.PermSourceSync
	case "erase", "delete", "source.erase":
		return domain.PermAccountErase
	default:
		return ""
	}
}

func operationActionMatchesType(operationType, action string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	switch operationType {
	case "sync":
		return action == "sync" || action == "summary" || action == "source.summary"
	case "erase":
		return action == "erase" || action == "delete" || action == "source.erase"
	case "source.command", "replay":
		return true
	default:
		return false
	}
}

func (s *operationService) requireOperationPermission(ctx context.Context, actor, permission string) error {
	if strings.TrimSpace(permission) == "" {
		return apperr.BadRequest("operation.invalid_action", "operation action is not allow-listed")
	}
	if s.permission == nil {
		return apperr.Forbidden("operation.permission_unavailable", "operation permission is not configured")
	}
	ok, err := s.permission.HasPermission(ctx, actor, permission)
	if err != nil {
		return apperr.Internal("operation.permission_check_failed", "failed to verify operation permission").Wrap(err)
	}
	if !ok {
		return apperr.Forbidden("operation.forbidden", "operation is not accessible to actor")
	}
	return nil
}
