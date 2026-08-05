package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AuditLogger records a single audit event. Implementations must be non-blocking
// from the caller's perspective; a failed log must not fail the primary operation.
type AuditLogger interface {
	Log(ctx context.Context, e *AuditEvent) error
}

// Audit action constants for sensitive operations.
const (
	AuditFileShareGrant  = "file.share.grant"
	AuditFileShareRevoke = "file.share.revoke"
	AuditReportResolve   = "comment.report.resolve"
	AuditUserBlock       = "user.block"
	AuditUserUnblock     = "user.unblock"
	AuditMessageDelete   = "message.delete"
	AuditAdminGrantRole  = "admin.role.grant"
	AuditAdminRevokeRole = "admin.role.revoke"
	AuditUserBan         = "user.ban"
	AuditUserUnban       = "user.unban"
)

// AuditEvent records who did what to which resource and when.
type AuditEvent struct {
	ID          uuid.UUID
	ActorUID    string
	Action      string
	SubjectType string
	SubjectID   string
	TargetUID   string
	Meta        map[string]any
	CreatedAt   time.Time
}
