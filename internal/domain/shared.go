package domain

import "github.com/aero/aero-id/internal/pkg/idgen"

// ID is the canonical identifier type for all aero-id entities.
type ID = idgen.ID

// Visibility for files.
type Visibility int16

const (
	VisibilityPrivate Visibility = 0
	VisibilityPublic  Visibility = 1
)

// MessageKind enumerates message payload types.
type MessageKind int16

const (
	MessageKindText   MessageKind = 1
	MessageKindImage  MessageKind = 2
	MessageKindFile   MessageKind = 3
	MessageKindSystem MessageKind = 4
)

// IsValid reports whether k is a known, supported message kind.
func (k MessageKind) IsValid() bool {
	switch k {
	case MessageKindText, MessageKindImage, MessageKindFile, MessageKindSystem:
		return true
	default:
		return false
	}
}

// IsSystem reports whether k denotes a server-emitted system message. System
// messages must never be forgeable through the normal user send path.
func (k MessageKind) IsSystem() bool { return k == MessageKindSystem }

// CommentStatus tracks moderation state.
type CommentStatus int16

const (
	CommentStatusNormal  CommentStatus = 1
	CommentStatusDeleted CommentStatus = 2
	CommentStatusHidden  CommentStatus = 3
)

// ReportStatus tracks the triage lifecycle of a comment report.
type ReportStatus int16

const (
	ReportStatusOpen      ReportStatus = 1
	ReportStatusResolved  ReportStatus = 2
	ReportStatusDismissed ReportStatus = 3
)

// IsTerminal reports whether s is a closed (resolved/dismissed) report state.
func (s ReportStatus) IsTerminal() bool {
	return s == ReportStatusResolved || s == ReportStatusDismissed
}

// TargetType is the polymorphic discriminator for what a comment is attached to.
type TargetType string

const (
	TargetTypeFile    TargetType = "file"
	TargetTypeMessage TargetType = "message"
)

// Page is the standard pagination request.
type Page struct {
	Limit  int    // default 20, max 100
	Cursor string // opaque, repository-defined
}

const (
	DefaultPageLimit = 20
	MaxPageLimit     = 100
)

// Normalize clamps Limit to [1, MaxPageLimit] and applies the default if zero.
func (p *Page) Normalize() {
	if p.Limit <= 0 {
		p.Limit = DefaultPageLimit
	}
	if p.Limit > MaxPageLimit {
		p.Limit = MaxPageLimit
	}
}

// SystemSenderUID is the synthetic from_uid for system notifications.
const SystemSenderUID = "system"
