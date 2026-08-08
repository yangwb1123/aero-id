package comment

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"
)

type serviceImpl struct {
	comments Repository
	files    FileGetter
	messages MessageGetter
	dir      UserDirectory
	audit    domain.AuditLogger
}

type FileGetter interface {
	Get(ctx context.Context, id domain.ID) (*domain.File, error)
}

type MessageGetter interface {
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
}

type UserDirectory interface {
	GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}

type Option func(*serviceImpl)

func WithAuditLogger(a domain.AuditLogger) Option {
	return func(s *serviceImpl) { s.audit = a }
}

func NewService(
	comments Repository,
	files FileGetter,
	messages MessageGetter,
	dir UserDirectory,
	opts ...Option,
) Service {
	s := &serviceImpl{comments: comments, files: files, messages: messages, dir: dir}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *serviceImpl) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
	switch t {
	case domain.TargetTypeFile:
		f, err := s.files.Get(ctx, target)
		if stderrors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target file not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !domain.CanReadFile(actor, f) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	case domain.TargetTypeMessage:
		m, err := s.messages.Get(ctx, target)
		if stderrors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target message not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !domain.CanReadMessage(actor, m) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	default:
		return apperr.BadRequest("comment.invalid", "unsupported target type")
	}
}

//nolint:cyclop // legacy comment compatibility behavior is intentionally preserved during migration.
func (s *serviceImpl) Create(ctx context.Context, in CreateCommentInput) (*Comment, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Content == "" {
		return nil, apperr.BadRequest("comment.invalid", "content is required")
	}
	if err := s.assertTargetReadable(ctx, actor, in.TargetType, in.TargetID); err != nil {
		return nil, err
	}

	var parent *Comment
	if in.ParentID != nil {
		p, err := s.comments.Get(ctx, *in.ParentID)
		if stderrors.Is(err, domain.ErrNotFound) {
			return nil, apperr.BadRequest("comment.invalid", "parent comment not found")
		}
		if err != nil {
			return nil, apperr.Internal("comment.get_failed", "failed to load parent comment").Wrap(err)
		}
		if p.TargetType != in.TargetType || p.TargetID != in.TargetID {
			return nil, apperr.BadRequest("comment.invalid", "parent belongs to a different target")
		}
		if err := ValidateNewComment(p); err != nil {
			return nil, apperr.BadRequest("comment.nesting_too_deep", "replies may not be nested more than two levels")
		}
		parent = p
	}

	c := NewComment(idgen.NewID(), actor, in.TargetType, in.TargetID, parent, in.Content)
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	var evt *outbox.Event
	if parent != nil {
		evt = outbox.NewEvent("comment", c.ID.String(), "comment.replied", map[string]any{
			"comment_id":        c.ID.String(),
			"parent_author_uid": parent.OwnerUID,
		})
	}
	if err := s.comments.CreateWithReplyBump(ctx, c, evt); err != nil {
		return nil, apperr.Internal("comment.create_failed", "failed to create comment").Wrap(err)
	}
	return c, nil
}

func (s *serviceImpl) List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertTargetReadable(ctx, actor, t, target); err != nil {
		return nil, err
	}
	cs, next, err := s.comments.ListByTarget(ctx, t, target, page)
	if err != nil {
		return nil, apperr.Internal("comment.list_failed", "failed to list comments").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve authors").Wrap(err)
	}
	return &CommentList{Comments: cs, NextCursor: next, Users: users}, nil
}

func (s *serviceImpl) Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	root, err := s.comments.Get(ctx, rootID)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("comment.not_found", "root comment not found")
	}
	if err != nil {
		return nil, apperr.Internal("comment.get_failed", "failed to load root comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, root.TargetType, root.TargetID); err != nil {
		return nil, err
	}
	cs, next, err := s.comments.ListReplies(ctx, rootID, page)
	if err != nil {
		return nil, apperr.Internal("comment.list_failed", "failed to list replies").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve authors").Wrap(err)
	}
	return &CommentList{Comments: cs, NextCursor: next, Users: users}, nil
}

func (s *serviceImpl) Edit(ctx context.Context, id domain.ID, newContent string) (*Comment, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if newContent == "" {
		return nil, apperr.BadRequest("comment.invalid", "content is required")
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return nil, apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return nil, apperr.Forbidden("comment.forbidden", "only the author can edit this comment")
	}
	if c.Status != domain.CommentStatusNormal {
		return nil, apperr.Conflict("comment.not_editable", "comment cannot be edited in its current state")
	}
	now := time.Now().UTC()
	if err := s.comments.UpdateContent(ctx, id, newContent, now); err != nil {
		if stderrors.Is(err, domain.ErrNotFound) {
			return nil, apperr.NotFound("comment.not_found", "comment not found")
		}
		return nil, apperr.Internal("comment.edit_failed", "failed to edit comment").Wrap(err)
	}
	c.Content = newContent
	c.UpdatedAt = now
	c.EditedAt = &now
	return c, nil
}

func (s *serviceImpl) Delete(ctx context.Context, id domain.ID) error {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the author can delete this comment")
	}
	if err := s.comments.SoftDelete(ctx, id); err != nil {
		return apperr.Internal("comment.delete_failed", "failed to delete comment").Wrap(err)
	}
	return nil
}

func (s *serviceImpl) Like(ctx context.Context, id domain.ID) (*ReactionResult, error) {
	count, err := s.react(ctx, id, true)
	if err != nil {
		return nil, err
	}
	return &ReactionResult{Liked: true, LikeCount: count}, nil
}

func (s *serviceImpl) Unlike(ctx context.Context, id domain.ID) (*ReactionResult, error) {
	count, err := s.react(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return &ReactionResult{Liked: false, LikeCount: count}, nil
}

func (s *serviceImpl) react(ctx context.Context, id domain.ID, like bool) (int, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return 0, err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return 0, apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return 0, apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, c.TargetType, c.TargetID); err != nil {
		return 0, err
	}
	var count int
	if like {
		count, err = s.comments.Like(ctx, id, actor)
	} else {
		count, err = s.comments.Unlike(ctx, id, actor)
	}
	if err != nil {
		return 0, apperr.Internal("comment.react_failed", "failed to update reaction").Wrap(err)
	}
	return count, nil
}

func (s *serviceImpl) Report(ctx context.Context, id domain.ID, reason string) error {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return err
	}
	if reason == "" {
		return apperr.BadRequest("comment.invalid", "reason is required")
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, c.TargetType, c.TargetID); err != nil {
		return err
	}
	report := &CommentReport{
		ID:          idgen.NewID(),
		CommentID:   id,
		ReporterUID: actor,
		Reason:      reason,
		CreatedAt:   time.Now().UTC(),
	}
	err = s.comments.CreateReport(ctx, report)
	if stderrors.Is(err, domain.ErrDuplicate) {
		return apperr.Conflict("comment.already_reported", "comment already reported by this user")
	}
	if err != nil {
		return apperr.Internal("comment.report_failed", "failed to report comment").Wrap(err)
	}
	return nil
}

func (s *serviceImpl) Hide(ctx context.Context, id domain.ID) error {
	return s.setStatusAsAuthor(ctx, id, domain.CommentStatusHidden)
}

func (s *serviceImpl) Unhide(ctx context.Context, id domain.ID) error {
	return s.setStatusAsAuthor(ctx, id, domain.CommentStatusNormal)
}

func (s *serviceImpl) setStatusAsAuthor(ctx context.Context, id domain.ID, status domain.CommentStatus) error {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the author can change this comment's visibility")
	}
	if err := s.comments.SetStatus(ctx, id, status); err != nil {
		if stderrors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("comment.not_found", "comment not found")
		}
		return apperr.Internal("comment.status_failed", "failed to update comment status").Wrap(err)
	}
	return nil
}

func (s *serviceImpl) ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) (*ReportList, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.comments.Get(ctx, commentID)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return nil, apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return nil, apperr.Forbidden("comment.forbidden", "only the comment author may view its reports")
	}
	reports, next, err := s.comments.ListReports(ctx, commentID, status, page)
	if err != nil {
		return nil, apperr.Internal("comment.report_list_failed", "failed to list reports").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, reporterUIDs(reports))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve reporters").Wrap(err)
	}
	return &ReportList{Reports: reports, NextCursor: next, Users: users}, nil
}

//nolint:cyclop // legacy moderation transitions retain their existing authorization semantics.
func (s *serviceImpl) ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus) error {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return err
	}
	if status != domain.ReportStatusResolved && status != domain.ReportStatusDismissed {
		return apperr.BadRequest("comment.invalid", "status must be resolved or dismissed")
	}
	rep, c, err := s.loadReportAndComment(ctx, reportID)
	if err != nil {
		return err
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the comment author may resolve its reports")
	}
	now := time.Now().UTC()
	if err := s.comments.ResolveReport(ctx, reportID, status, actor, now); err != nil {
		if stderrors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("comment.report_not_found", "report not found")
		}
		return apperr.Internal("comment.resolve_failed", "failed to resolve report").Wrap(err)
	}
	if status == domain.ReportStatusResolved && c.Status == domain.CommentStatusNormal {
		if err := s.comments.SetStatus(ctx, rep.CommentID, domain.CommentStatusHidden); err != nil {
			if stderrors.Is(err, domain.ErrNotFound) {
				return apperr.NotFound("comment.not_found", "comment not found")
			}
			return apperr.Internal("comment.status_failed", "failed to update comment status").Wrap(err)
		}
	}
	s.logResolveAudit(ctx, actor, reportID, rep, c, status)
	return nil
}

func (s *serviceImpl) loadReportAndComment(ctx context.Context, reportID domain.ID) (*CommentReport, *Comment, error) {
	rep, err := s.comments.GetReport(ctx, reportID)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, nil, apperr.NotFound("comment.report_not_found", "report not found")
	}
	if err != nil {
		return nil, nil, apperr.Internal("comment.get_failed", "failed to load report").Wrap(err)
	}
	c, err := s.comments.Get(ctx, rep.CommentID)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, nil, apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return nil, nil, apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	return rep, c, nil
}

func (s *serviceImpl) logResolveAudit(ctx context.Context, actor string, reportID domain.ID, rep *CommentReport, c *Comment, status domain.ReportStatus) {
	if s.audit == nil {
		return
	}
	statusLabel := "dismissed"
	if status == domain.ReportStatusResolved {
		statusLabel = "resolved"
	}
	_ = s.audit.Log(ctx, &domain.AuditEvent{
		ID:          reportID,
		ActorUID:    actor,
		Action:      domain.AuditReportResolve,
		SubjectType: "comment_report",
		SubjectID:   reportID.String(),
		TargetUID:   c.OwnerUID,
		Meta:        map[string]any{"comment_id": rep.CommentID.String(), "status": statusLabel},
	})
}

func reporterUIDs(rs []*CommentReport) []string {
	seen := make(map[string]struct{}, len(rs))
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if _, ok := seen[r.ReporterUID]; ok {
			continue
		}
		seen[r.ReporterUID] = struct{}{}
		out = append(out, r.ReporterUID)
	}
	return out
}

func authorUIDs(cs []*Comment) []string {
	seen := make(map[string]struct{}, len(cs))
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if _, ok := seen[c.OwnerUID]; ok {
			continue
		}
		seen[c.OwnerUID] = struct{}{}
		out = append(out, c.OwnerUID)
	}
	return out
}

func enrichUsers(ctx context.Context, dir UserDirectory, uids []string) (map[string]sso.User, error) {
	if len(uids) == 0 {
		return map[string]sso.User{}, nil
	}
	return dir.GetUsers(ctx, uids)
}
