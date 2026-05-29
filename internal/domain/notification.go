package domain

import "time"

// Notification is a per-recipient record of a domain event (a message received,
// a comment reply, …). Notifications are derived asynchronously from the outbox
// rather than written in the request path, so the recipient feed stays decoupled
// from the action that produced it.
type Notification struct {
	ID           ID
	RecipientUID string
	Type         string // e.g. "message.received", "comment.reply"
	ActorUID     string // who triggered it; empty when not applicable
	SubjectType  string // e.g. "message", "comment"
	SubjectID    string // the subject's id as a string
	ReadAt       *time.Time
	CreatedAt    time.Time
}
