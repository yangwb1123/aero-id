package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AuditLogger records a single audit event. A failed log is a failed durable audit
// write: callers must propagate the error (fail-closed). The earlier "a failed log
// must not fail the primary operation" fail-open wording is revoked — it predates
// the live-layer fail-closed conversion and must never be reinstated.
//
// QUARANTINED DEAD TREE — do not wire as-is: this interface is used only by the
// unwired parallel tree (internal/{file,message,comment,block,notification,search,
// audit}), reachable only from its own tests. Six `_ = s.audit.Log(` swallow sites
// remain there (internal/file/service_share.go:45,75; internal/block/block.go:66,86;
// internal/comment/service.go:431; internal/message/service.go:289). They must be
// converted to propagated `if err :=` form before this tree is ever wired. The live
// layer uses service.AuditLogger and is policed by make check-audit-swallow.
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
