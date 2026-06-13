package domain

import (
	"time"

	"github.com/google/uuid"
)

// Audit action constants for sensitive operations.
const (
	AuditFileShareGrant  = "file.share.grant"
	AuditFileShareRevoke = "file.share.revoke"
	AuditReportResolve   = "comment.report.resolve"
	AuditUserBlock       = "user.block"
	AuditUserUnblock     = "user.unblock"
	AuditMessageDelete   = "message.delete"
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
