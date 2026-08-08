package service

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type commentService struct {
	comments repository.CommentRepository
	files    repository.FileRepository
	messages repository.MessageRepository
	dir      UserDirectory
	audit    AuditLogger // nil => audit inactive
}

// CommentOption configures optional commentService behaviour.
type CommentOption func(*commentService)

// WithCommentAuditLogger wires an AuditLogger into the comment service so that
// moderation actions (report resolution) emit audit events.
func WithCommentAuditLogger(a AuditLogger) CommentOption {
	return func(s *commentService) { s.audit = a }
}

func NewCommentService(
	comments repository.CommentRepository,
	files repository.FileRepository,
	messages repository.MessageRepository,
	dir UserDirectory,
	opts ...CommentOption,
) CommentService {
	s := &commentService{comments: comments, files: files, messages: messages, dir: dir}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// assertTargetReadable verifies the comment target exists and the actor may read
// it, reusing the same ACL as the file/message services.
func (s *commentService) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
	switch t {
	case domain.TargetTypeFile:
		f, err := s.files.Get(ctx, target)
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target file not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !canReadFile(actor, f) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	case domain.TargetTypeMessage:
		m, err := s.messages.Get(ctx, target)
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target message not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !canReadMessage(actor, m) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	default:
		return apperr.BadRequest("comment.invalid", "unsupported target type")
	}
}

//nolint:cyclop,funlen // legacy comment behavior remains compatibility-bound until source migration.
func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Content == "" {
		return nil, apperr.BadRequest("comment.invalid", "content is required")
	}
	if err := s.assertTargetReadable(ctx, actor, in.TargetType, in.TargetID); err != nil {
		return nil, err
	}

	var parent *domain.Comment
	if in.ParentID != nil {
		p, err := s.comments.Get(ctx, *in.ParentID)
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.BadRequest("comment.invalid", "parent comment not found")
		}
		if err != nil {
			return nil, apperr.Internal("comment.get_failed", "failed to load parent comment").Wrap(err)
		}
		if p.TargetType != in.TargetType || p.TargetID != in.TargetID {
			return nil, apperr.BadRequest("comment.invalid", "parent belongs to a different target")
		}
		// Enforce the two-level nesting rule at the domain layer.
		if err := domain.ValidateNewComment(p); err != nil {
			return nil, apperr.BadRequest("comment.nesting_too_deep", "replies may not be nested more than two levels")
		}
		parent = p
	}

	c := domain.NewComment(idgen.NewID(), actor, in.TargetType, in.TargetID, parent, in.Content)
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	// For replies, emit a "comment.replied" event in the same transaction as the
	// insert + reply_count bump so the notification substrate is reliable.
	// Determine the event to emit alongside the comment insert.
	// For replies, emit a "comment.replied" event to notify the parent author.
	// For top-level comments, emit "file.commented" or "message.commented" to
	// notify the target owner, but only when the commenter is not the owner.
	var evt *outbox.Event
	if parent != nil {
		evt = newEvent("comment", c.ID.String(), "comment.replied", map[string]any{
			"comment_id":        c.ID.String(),
			"parent_author_uid": parent.OwnerUID,
		})
	} else {
		// Top-level comment — notify the target owner.
		ownerUID, err := s.resolveTargetOwner(ctx, in.TargetType, in.TargetID)
		if err == nil && ownerUID != "" && ownerUID != actor {
			eventType := "file.commented"
			subjectType := "file"
			if in.TargetType == domain.TargetTypeMessage {
				eventType = "message.commented"
				subjectType = "message"
			}
			evt = newEvent("comment", c.ID.String(), eventType, map[string]any{
				"comment_id":    c.ID.String(),
				"target_type":   string(in.TargetType),
				"target_id":     in.TargetID.String(),
				"owner_uid":     ownerUID,
				"commenter_uid": actor,
				"subject_type":  subjectType,
			})
		}
	}
	// Persist the comment and bump the parent's reply_count atomically so the
	// denormalized counter can never drift from the rows that back it.
	if err := s.comments.CreateWithReplyBump(ctx, c, evt); err != nil {
		return nil, apperr.Internal("comment.create_failed", "failed to create comment").Wrap(err)
	}
	return c, nil
}

func (s *commentService) List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := mustActor(ctx)
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

func (s *commentService) Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	root, err := s.comments.Get(ctx, rootID)
	if stderrors.Is(err, repository.ErrNotFound) {
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

// Edit rewrites the content of a comment. Only the comment's author may edit it;
// content must be non-empty; a soft-deleted comment cannot be edited (it surfaces as
// NotFound from the repository), and a hidden comment is treated as immutable until
// unhidden (Conflict). The updated comment is returned.
func (s *commentService) Edit(ctx context.Context, id domain.ID, newContent string) (*domain.Comment, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if newContent == "" {
		return nil, apperr.BadRequest("comment.invalid", "content is required")
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
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
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("comment.not_found", "comment not found")
		}
		return nil, apperr.Internal("comment.edit_failed", "failed to edit comment").Wrap(err)
	}
	c.Content = newContent
	c.UpdatedAt = now
	c.EditedAt = &now
	return c, nil
}

func (s *commentService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
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

// Report files a report against a comment by the authenticated actor. Any user who
// can read the comment's target may report it; a repeat report by the same user is a
// Conflict.
func (s *commentService) Report(ctx context.Context, id domain.ID, reason string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if reason == "" {
		return apperr.BadRequest("comment.invalid", "reason is required")
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, c.TargetType, c.TargetID); err != nil {
		return err
	}
	report := &domain.CommentReport{
		ID:          idgen.NewID(),
		CommentID:   id,
		ReporterUID: actor,
		Reason:      reason,
		CreatedAt:   time.Now().UTC(),
	}
	err = s.comments.CreateReport(ctx, report)
	if stderrors.Is(err, repository.ErrDuplicate) {
		return apperr.Conflict("comment.already_reported", "comment already reported by this user")
	}
	if err != nil {
		return apperr.Internal("comment.report_failed", "failed to report comment").Wrap(err)
	}
	return nil
}

// Hide marks a comment hidden so it is excluded from listings. Only the comment's
// author may hide it (author-scoped self-moderation; there is no admin role).
func (s *commentService) Hide(ctx context.Context, id domain.ID) error {
	return s.setStatusAsAuthor(ctx, id, domain.CommentStatusHidden)
}

// Unhide restores a hidden comment to normal visibility. Author-only.
func (s *commentService) Unhide(ctx context.Context, id domain.ID) error {
	return s.setStatusAsAuthor(ctx, id, domain.CommentStatusNormal)
}

func (s *commentService) setStatusAsAuthor(ctx context.Context, id domain.ID, status domain.CommentStatus) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the author can change this comment's visibility")
	}
	if err := s.comments.SetStatus(ctx, id, status); err != nil {
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.not_found", "comment not found")
		}
		return apperr.Internal("comment.status_failed", "failed to update comment status").Wrap(err)
	}
	return nil
}

// ListReports returns the moderation queue for a comment. AUTHZ: aero-id has no role
// system, so the queue is scoped to the reported comment's author — the owner of a
// comment may see and triage reports filed against it (the same author-scoped
// self-moderation model as Hide/Unhide). An optional status filter narrows the page.
func (s *commentService) ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) (*ReportList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.comments.Get(ctx, commentID)
	if stderrors.Is(err, repository.ErrNotFound) {
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

// ResolveReport triages a report by transitioning it to resolved or dismissed. AUTHZ:
// only the reported comment's author may resolve. Dismissing leaves the comment as-is;
// resolving hides the offending comment (an explicit, simple moderation action).
//
//nolint:cyclop,funlen // legacy moderation transitions remain compatibility-bound until source migration.
func (s *commentService) ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if status != domain.ReportStatusResolved && status != domain.ReportStatusDismissed {
		return apperr.BadRequest("comment.invalid", "status must be resolved or dismissed")
	}
	rep, err := s.comments.GetReport(ctx, reportID)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("comment.report_not_found", "report not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load report").Wrap(err)
	}
	c, err := s.comments.Get(ctx, rep.CommentID)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the comment author may resolve its reports")
	}
	now := time.Now().UTC()
	// Only resolved/dismissed reach here (validated above); map to the API's
	// string vocabulary rather than the raw int16 enum for a readable audit trail.
	statusLabel := "dismissed"
	if status == domain.ReportStatusResolved {
		statusLabel = "resolved"
	}
	// The report update, the conditional comment hide and the anchored fact
	// (EventID = reportID, load-bearing retry-dedup) commit in ONE transaction
	// (B2-1): a failing hide now rolls the resolve back with it — the intended
	// atomicity change; report-not-found still wins when both fail.
	hideCommentID := domain.ID{}
	if status == domain.ReportStatusResolved && c.Status == domain.CommentStatusNormal {
		hideCommentID = rep.CommentID
	}
	if s.audit != nil {
		repo, ok := s.comments.(repository.CommentAuditRepository)
		if !ok {
			return apperr.Internal("comment.audit_unavailable", "comment audit persistence is unavailable")
		}
		if err := repo.ResolveReportWithAudit(ctx, reportID, status, actor, now, hideCommentID,
			buildLegacyAuditFact(ctx, actor, domain.AuditReportResolve, "comment_report", reportID.String(),
				c.OwnerUID, map[string]any{"comment_id": rep.CommentID.String(), "status": statusLabel}, reportID)); err != nil {
			if stderrors.Is(err, repository.ErrNotFound) {
				return apperr.NotFound("comment.report_not_found", "report not found")
			}
			return apperr.Internal("comment.resolve_failed", "failed to resolve report").Wrap(err)
		}
		return nil
	}
	if err := s.comments.ResolveReport(ctx, reportID, status, actor, now); err != nil {
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.report_not_found", "report not found")
		}
		return apperr.Internal("comment.resolve_failed", "failed to resolve report").Wrap(err)
	}
	// Resolving a report hides the offending comment; dismissing leaves it visible.
	if hideCommentID != (domain.ID{}) {
		if err := s.comments.SetStatus(ctx, hideCommentID, domain.CommentStatusHidden); err != nil {
			if stderrors.Is(err, repository.ErrNotFound) {
				return apperr.NotFound("comment.not_found", "comment not found")
			}
			return apperr.Internal("comment.status_failed", "failed to update comment status").Wrap(err)
		}
	}
	return nil
}

// reporterUIDs returns the distinct reporter uids preserving first-seen order.
func reporterUIDs(rs []*domain.CommentReport) []string {
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

// authorUIDs returns the distinct owner_uids preserving first-seen order.
func authorUIDs(cs []*domain.Comment) []string {
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

// resolveTargetOwner returns the uid of the owner of the target being
// commented on (file or message). It is used to determine whom to notify
// when a top-level comment is created. Returns empty string on error or
// if the target cannot be resolved (e.g. not found).
func (s *commentService) resolveTargetOwner(ctx context.Context, t domain.TargetType, targetID domain.ID) (string, error) {
	switch t {
	case domain.TargetTypeFile:
		f, err := s.files.Get(ctx, targetID)
		if err != nil {
			return "", err
		}
		return f.OwnerUID, nil
	case domain.TargetTypeMessage:
		m, err := s.messages.Get(ctx, targetID)
		if err != nil {
			return "", err
		}
		// For messages, notify the other participant (not the sender).
		// We use the targetID as the message — the message's ToUID is
		// the peer of the sender. But comments are on messages, so the
		// "owner" is the message's ToUID (the recipient).
		// Actually, both participants can be notified — but the design
		// decides to notify the ToUID (message recipient) when someone
		// comments on a message they sent.
		return m.ToUID, nil
	default:
		return "", nil
	}
}
