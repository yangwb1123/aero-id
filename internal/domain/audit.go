package domain

import (
	"time"
)

// Audit action constants for sensitive operations. A failed audit write is a
// failed durable audit write: callers must propagate the error (fail-closed).
// The earlier "a failed log must not fail the primary operation" fail-open
// wording is revoked — it predates the live-layer fail-closed conversion and
// must never be reinstated. The dead-tree AuditLogger interface (which
// carried the last excluded `_ = s.audit.Log(` swallow sites in the unwired
// internal/{file,message,notification,audit} tree) was deleted in the
// 2026-08-10 quarantine; the live layer uses service.AuditLogger, policed by
// make check-audit-swallow.
const (
	AuditFileShareGrant               = "file.share.grant"
	AuditFileShareRevoke              = "file.share.revoke"
	AuditReportResolve                = "comment.report.resolve"
	AuditUserBlock                    = "user.block"
	AuditUserUnblock                  = "user.unblock"
	AuditMessageDelete                = "message.delete"
	AuditAdminGrantRole               = "admin.role.grant"
	AuditAdminRevokeRole              = "admin.role.revoke"
	AuditUserBan                      = "user.ban"
	AuditUserUnban                    = "user.unban"
	AuditFileCreate                   = "file.create"
	AuditFileDelete                   = "file.delete"
	AuditFileUpdate                   = "file.update"
	AuditMessageSend                  = "message.send"
	AuditMessageEdit                  = "message.edit"
	AuditMessageReact                 = "message.react"
	AuditMessageUnreact               = "message.unreact"
	AuditPreferenceSet                = "preference.set"
	AuditRegionPromote                = "region.promote"
	AuditProjectionReplicationPublish = "projection.replication.publish"
	AuditProjectionReplay             = "projection.replay.run"
)

// AuditEvent records who did what to which resource and when. Live: consumed
// by the repository/ledger and admin read paths — do not prune.
type AuditEvent struct {
	ID          ID
	ActorUID    string
	Action      string
	SubjectType string
	SubjectID   string
	TargetUID   string
	Meta        map[string]any
	CreatedAt   time.Time
}
