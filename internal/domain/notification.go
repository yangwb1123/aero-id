package domain

import "time"

// NotificationType enumerates the known notification types in the system.
// These correspond to outbox event types the NotificationDispatcher maps.
type NotificationType string

const (
	NotifTypeMessageReceived  NotificationType = "message.received"
	NotifTypeMessageRead      NotificationType = "message.read_receipt"
	NotifTypeCommentReply     NotificationType = "comment.reply"
	NotifTypeFileCommented    NotificationType = "file.commented"
	NotifTypeMessageCommented NotificationType = "message.commented"
	NotifTypeMessageReacted   NotificationType = "message.reacted"
	NotifTypeCommentLiked     NotificationType = "comment.liked"
	NotifTypeMessageDeleted   NotificationType = "message.deleted"
)

// Notification is a per-recipient record of a domain event (a message received,
// a comment reply, …). Notifications are derived asynchronously from the outbox
// rather than written in the request path, so the recipient feed stays decoupled
// from the action that produced it.
type Notification struct {
	ID             ID
	RecipientUID   string
	Type           string // e.g. "message.received", "comment.reply"
	ActorUID       string // who triggered it; empty when not applicable
	SubjectType    string // e.g. "message", "comment"
	SubjectID      string // the subject's id as a string
	ReadAt         *time.Time
	CollapsedCount int // 1 = not merged; >1 = collapsed N events
	CreatedAt      time.Time
}

// NotificationPreference controls whether a recipient receives a given
// notification type. Each (uid, notification_type, scope_type, scope_id) row
// is independent. When the table has no row for a uid+type pair the default is
// "enabled" (backward compatible with all users who never set preferences).
// An empty scope_type signals a global preference (no resource scoping).
type NotificationPreference struct {
	UID              string
	NotificationType string
	ScopeType        string // "" (global), "file", "conversation"
	ScopeID          string // the scoped resource id; empty when ScopeType is ""
	Enabled          bool
	QuietHoursStart  string // "HH:MM" 24h; empty = no quiet hours
	QuietHoursEnd    string
	Timezone         string // IANA timezone name, default "UTC"
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
