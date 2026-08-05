package comment

import (
	"context"
	"errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/sso"
)

type Comment = domain.Comment

type CommentReport = domain.CommentReport

var ErrNestingTooDeep = errors.New("comment nesting must not exceed two levels")

func ValidateNewComment(parent *Comment) error {
	if parent == nil {
		return nil
	}
	if parent.ParentID != nil {
		return ErrNestingTooDeep
	}
	return nil
}

func NewComment(id domain.ID, ownerUID string, targetType domain.TargetType, targetID domain.ID, parent *Comment, content string) *Comment {
	c := &Comment{
		ID:         id,
		OwnerUID:   ownerUID,
		TargetType: targetType,
		TargetID:   targetID,
		Content:    content,
		Status:     domain.CommentStatusNormal,
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

type CreateCommentInput struct {
	TargetType domain.TargetType
	TargetID   domain.ID
	ParentID   *domain.ID
	Content    string
}

type CommentList struct {
	Comments   []*Comment
	NextCursor string
	Users      map[string]sso.User
}

type ReportList struct {
	Reports    []*CommentReport
	NextCursor string
	Users      map[string]sso.User
}

type ReactionResult struct {
	Liked     bool
	LikeCount int
}

type Repository interface {
	Create(ctx context.Context, c *Comment) error
	CreateWithReplyBump(ctx context.Context, c *Comment, evt *outbox.Event) error
	Get(ctx context.Context, id domain.ID) (*Comment, error)
	ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*Comment, string, error)
	ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*Comment, string, error)
	IncReplyCount(ctx context.Context, parentID domain.ID) error
	SoftDelete(ctx context.Context, id domain.ID) error
	UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error
	Like(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error)
	Unlike(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error)
	CreateReport(ctx context.Context, r *CommentReport) error
	SetStatus(ctx context.Context, commentID domain.ID, status domain.CommentStatus) error
	ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) ([]*CommentReport, string, error)
	GetReport(ctx context.Context, reportID domain.ID) (*CommentReport, error)
	ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time) error
	SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*Comment, error)
}

type Service interface {
	Create(ctx context.Context, in CreateCommentInput) (*Comment, error)
	List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error)
	Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error)
	Edit(ctx context.Context, id domain.ID, newContent string) (*Comment, error)
	Delete(ctx context.Context, id domain.ID) error
	Like(ctx context.Context, id domain.ID) (*ReactionResult, error)
	Unlike(ctx context.Context, id domain.ID) (*ReactionResult, error)
	Report(ctx context.Context, id domain.ID, reason string) error
	Hide(ctx context.Context, id domain.ID) error
	Unhide(ctx context.Context, id domain.ID) error
	ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) (*ReportList, error)
	ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus) error
}

var reportStatusByName = map[string]domain.ReportStatus{
	"open":      domain.ReportStatusOpen,
	"resolved":  domain.ReportStatusResolved,
	"dismissed": domain.ReportStatusDismissed,
}

func reportStatusName(s domain.ReportStatus) string {
	switch s {
	case domain.ReportStatusResolved:
		return "resolved"
	case domain.ReportStatusDismissed:
		return "dismissed"
	default:
		return "open"
	}
}
