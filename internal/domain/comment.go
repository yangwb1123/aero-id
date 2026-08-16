package domain

import (
	"errors"
	"time"
)

type Comment struct {
	ID         ID
	OwnerUID   string
	TargetType TargetType
	TargetID   ID
	ParentID   *ID
	RootID     ID
	Content    string
	LikeCount  int
	ReplyCount int
	Status     CommentStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// EditedAt is the time the author last edited the content; nil if never edited.
	EditedAt *time.Time
}

// CommentReport is a single user's report against a comment. The (CommentID,
// ReporterUID) pair is unique so a user can report a given comment at most once.
type CommentReport struct {
	ID          ID
	CommentID   ID
	ReporterUID string
	Reason      string
	Status      ReportStatus
	CreatedAt   time.Time
	// ResolvedAt / ResolvedBy are set when a report leaves the open state.
	ResolvedAt *time.Time
	ResolvedBy string
}

// errNestingTooDeep is returned by ValidateNewComment when a reply would create
// a depth >= 3 chain (root → reply → reply-of-reply). It is unexported because
// callers only check for a non-nil error.
var errNestingTooDeep = errors.New("comment nesting must not exceed two levels")

// ValidateNewComment enforces the two-level nesting rule.
//   - nil parent (top-level): always allowed.
//   - parent whose own ParentID is nil (top-level): allowed (becomes 2nd level).
//   - parent whose ParentID is non-nil (already a reply): rejected.
func ValidateNewComment(parent *Comment) error {
	if parent == nil {
		return nil
	}
	if parent.ParentID != nil {
		return errNestingTooDeep
	}
	return nil
}

// NewComment is a factory that wires ID/RootID/ParentID per the nesting rule.
// Caller must have already validated the parent with ValidateNewComment.
func NewComment(id ID, ownerUID string, targetType TargetType, targetID ID, parent *Comment, content string) *Comment {
	c := &Comment{
		ID:         id,
		OwnerUID:   ownerUID,
		TargetType: targetType,
		TargetID:   targetID,
		Content:    content,
		Status:     CommentStatusNormal,
	}
	if parent == nil {
		c.RootID = id
		return c
	}
	pid := parent.ID
	c.ParentID = &pid
	c.RootID = parent.RootID
	return c
}
