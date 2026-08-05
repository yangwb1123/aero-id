package domain

import "time"

// Built-in role permissions.
const (
	PermContentModerate   = "content.moderate" // moderate comments, hide/unhide globally
	PermAuditRead         = "audit.read"       // query audit log across all users
	PermAuditExport       = "audit.export"     // export audit facts (P4-08)
	PermUserBan           = "user.ban"         // ban/unban users
	PermSystemConfig      = "system.config"    // change system-wide settings
	PermCallbackManage    = "callback.manage"
	PermOperationRead     = "operation.read"
	PermOperationManage   = "operation.manage"
	PermSourceSync        = "source.sync"
	PermAccountExport     = "account.export"
	PermAccountErase      = "account.erase"
	PermAuditReplay       = "audit.replay"
	PermSourceEventIngest = "source.event.ingest"
)

// Role is a named set of permissions.
type Role struct {
	Code        string
	Name        string
	Permissions []string
	CreatedAt   time.Time
}

// UserRole associates a uid with a role.
type UserRole struct {
	UID       string
	RoleCode  string
	GrantedBy string
	CreatedAt time.Time
}
