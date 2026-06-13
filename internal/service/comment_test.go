package service_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
	"github.com/aero/aero-id/internal/sso"
)

// readableFileRepo is a FileRepo whose target is a public file (so target ACL passes).
func readableFileRepo(target domain.ID) *mocks.FileRepo {
	return &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPublic}, nil
	}}
}

func TestCommentService_CreateTopLevel(t *testing.T) {
	target := idgen.NewID()
	var saved *domain.Comment
	comments := &mocks.CommentRepo{CreateWithReplyBumpFn: func(_ context.Context, c *domain.Comment, _ *outbox.Event) error { saved = c; return nil }}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	c, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, Content: "nice"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ParentID != nil || c.RootID != c.ID {
		t.Fatalf("top-level wiring wrong: %+v", c)
	}
	if saved == nil {
		t.Fatal("comment was not persisted")
	}
}

func TestCommentService_CreateReplyIncrementsParent(t *testing.T) {
	target := idgen.NewID()
	root := idgen.NewID()
	var bumpedFor *domain.ID
	var evt *outbox.Event
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: root, ParentID: nil, RootID: root, TargetType: domain.TargetTypeFile, TargetID: target, OwnerUID: "rootauthor"}, nil
		},
		CreateWithReplyBumpFn: func(_ context.Context, c *domain.Comment, e *outbox.Event) error {
			bumpedFor = c.ParentID
			evt = e
			return nil
		},
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	c, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &root, Content: "re"})
	if err != nil {
		t.Fatal(err)
	}
	if c.RootID != root || c.ParentID == nil || *c.ParentID != root {
		t.Fatalf("reply wiring wrong: %+v", c)
	}
	// The atomic reply-bump must be invoked with the parent set (the increment itself
	// is exercised by the repository integration test).
	if bumpedFor == nil || *bumpedFor != root {
		t.Fatal("reply was not routed through the atomic reply-count bump with the parent set")
	}
	// A reply must also emit a "comment.replied" event transactionally.
	if evt == nil || evt.EventType != "comment.replied" || evt.AggregateType != "comment" {
		t.Fatalf("expected comment.replied event, got %+v", evt)
	}
}

func TestCommentService_RejectsThirdLevel(t *testing.T) {
	target := idgen.NewID()
	root := idgen.NewID()
	midParent := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
			// The requested parent is itself a reply (ParentID != nil) → depth 3.
			return &domain.Comment{ID: midParent, ParentID: &root, RootID: root, TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &midParent, Content: "too deep"})
	assertAppCode(t, err, 400)
	var ae *apperr.AppError
	stderrors.As(err, &ae)
	if ae.Code != "comment.nesting_too_deep" {
		t.Fatalf("code = %q, want comment.nesting_too_deep", ae.Code)
	}
}

func TestCommentService_TargetNotFound(t *testing.T) {
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewCommentService(&mocks.CommentRepo{}, files, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: idgen.NewID(), Content: "x"})
	assertAppCode(t, err, 404)
}

func TestCommentService_TargetForbidden(t *testing.T) {
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "someone", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := service.NewCommentService(&mocks.CommentRepo{}, files, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: idgen.NewID(), Content: "x"})
	assertAppCode(t, err, 403)
}

func TestCommentService_ParentBelongsToDifferentTarget(t *testing.T) {
	target := idgen.NewID()
	other := idgen.NewID()
	root := idgen.NewID()
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: root, ParentID: nil, RootID: root, TargetType: domain.TargetTypeFile, TargetID: other}, nil
	}}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, service.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &root, Content: "x"})
	assertAppCode(t, err, 400)
}

func TestCommentService_ListEnrichesAuthors(t *testing.T) {
	target := idgen.NewID()
	comments := &mocks.CommentRepo{ListByTargetFn: func(_ context.Context, _ domain.TargetType, _ domain.ID, _ domain.Page) ([]*domain.Comment, string, error) {
		return []*domain.Comment{{OwnerUID: "x"}, {OwnerUID: "y"}, {OwnerUID: "x"}}, "next", nil
	}}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		return map[string]sso.User{"x": {UID: "x"}, "y": {UID: "y"}}, nil
	}}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, dir)
	ctx := service.WithActor(context.Background(), "viewer")
	list, err := svc.List(ctx, domain.TargetTypeFile, target, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 {
		t.Fatalf("expected 2 deduped author uids, got %v", requested)
	}
	if list.NextCursor != "next" {
		t.Fatalf("cursor = %q", list.NextCursor)
	}
}

func TestCommentService_RepliesHappyPath(t *testing.T) {
	target := idgen.NewID()
	rootID := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		ListRepliesFn: func(_ context.Context, _ domain.ID, _ domain.Page) ([]*domain.Comment, string, error) {
			return []*domain.Comment{{OwnerUID: "u1"}, {OwnerUID: "u2"}}, "tok", nil
		},
	}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u}
		}
		return out, nil
	}}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, dir)
	ctx := service.WithActor(context.Background(), "viewer")
	list, err := svc.Replies(ctx, rootID, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Comments) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(list.Comments))
	}
	if list.NextCursor != "tok" {
		t.Fatalf("cursor = %q, want tok", list.NextCursor)
	}
	if len(requested) != 2 {
		t.Fatalf("expected 2 enriched uids, got %v", requested)
	}
}

func TestCommentService_RepliesForbiddenForPrivateTarget(t *testing.T) {
	target := idgen.NewID()
	rootID := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: id, OwnerUID: "owner", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
	}
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := service.NewCommentService(comments, files, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.Replies(ctx, rootID, domain.Page{})
	assertAppCode(t, err, 403)
}

func TestCommentService_DeleteRequiresOwner(t *testing.T) {
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: id, OwnerUID: "author"}, nil
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "not-author")
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestCommentService_LikeAndUnlike(t *testing.T) {
	target := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		LikeFn:   func(_ context.Context, _ domain.ID, _ string) (int, error) { return 1, nil },
		UnlikeFn: func(_ context.Context, _ domain.ID, _ string) (int, error) { return 0, nil },
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "viewer")

	liked, err := svc.Like(ctx, idgen.NewID())
	if err != nil || !liked.Liked || liked.LikeCount != 1 {
		t.Fatalf("like: %+v err=%v", liked, err)
	}
	unliked, err := svc.Unlike(ctx, idgen.NewID())
	if err != nil || unliked.Liked || unliked.LikeCount != 0 {
		t.Fatalf("unlike: %+v err=%v", unliked, err)
	}
}

func TestCommentService_ReportSucceeds(t *testing.T) {
	target := idgen.NewID()
	var saved *domain.CommentReport
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		CreateReportFn: func(_ context.Context, r *domain.CommentReport) error { saved = r; return nil },
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "reporter")
	if err := svc.Report(ctx, idgen.NewID(), "spam"); err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.ReporterUID != "reporter" || saved.Reason != "spam" {
		t.Fatalf("report not persisted correctly: %+v", saved)
	}
}

func TestCommentService_ReportDuplicateIsConflict(t *testing.T) {
	target := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		CreateReportFn: func(_ context.Context, _ *domain.CommentReport) error { return repository.ErrDuplicate },
	}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "reporter")
	err := svc.Report(ctx, idgen.NewID(), "spam")
	assertAppCode(t, err, 409)
}

func TestCommentService_ReportBlankReasonIsBadRequest(t *testing.T) {
	svc := service.NewCommentService(&mocks.CommentRepo{}, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "reporter")
	err := svc.Report(ctx, idgen.NewID(), "")
	assertAppCode(t, err, 400)
}

func TestCommentService_HideByNonAuthorIsForbidden(t *testing.T) {
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: id, OwnerUID: "author"}, nil
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "not-author")
	err := svc.Hide(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestCommentService_HideByAuthorSetsHiddenStatus(t *testing.T) {
	var gotStatus domain.CommentStatus
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: id, OwnerUID: "author"}, nil
		},
		SetStatusFn: func(_ context.Context, _ domain.ID, s domain.CommentStatus) error { gotStatus = s; return nil },
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	if err := svc.Hide(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if gotStatus != domain.CommentStatusHidden {
		t.Fatalf("status = %d, want hidden(%d)", gotStatus, domain.CommentStatusHidden)
	}
}

// TestCommentService_HiddenCommentExcludedFromList asserts the service returns only
// what the repository returns, and that the repository (which filters by status) is
// what drives visibility: a hidden comment never appears because the repo omits it.
func TestCommentService_HiddenCommentExcludedFromList(t *testing.T) {
	target := idgen.NewID()
	// The repo mock stands in for the status-filtered query: it returns only the
	// visible comment, never the hidden one.
	comments := &mocks.CommentRepo{ListByTargetFn: func(_ context.Context, _ domain.TargetType, _ domain.ID, _ domain.Page) ([]*domain.Comment, string, error) {
		return []*domain.Comment{{OwnerUID: "x", Status: domain.CommentStatusNormal}}, "", nil
	}}
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u}
		}
		return out, nil
	}}
	svc := service.NewCommentService(comments, readableFileRepo(target), &mocks.MessageRepo{}, dir)
	ctx := service.WithActor(context.Background(), "viewer")
	list, err := svc.List(ctx, domain.TargetTypeFile, target, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Comments) != 1 {
		t.Fatalf("expected 1 visible comment, got %d", len(list.Comments))
	}
	for _, cm := range list.Comments {
		if cm.Status != domain.CommentStatusNormal {
			t.Fatalf("non-visible comment leaked into listing: status=%d", cm.Status)
		}
	}
}

func TestCommentService_EditByAuthorSucceeds(t *testing.T) {
	id := idgen.NewID()
	var gotContent string
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: cid, OwnerUID: "author", Content: "old", Status: domain.CommentStatusNormal}, nil
		},
		UpdateContentFn: func(_ context.Context, _ domain.ID, content string, _ time.Time) error {
			gotContent = content
			return nil
		},
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	c, err := svc.Edit(ctx, id, "new content")
	if err != nil {
		t.Fatal(err)
	}
	if gotContent != "new content" || c.Content != "new content" {
		t.Fatalf("content not updated: repo=%q resp=%q", gotContent, c.Content)
	}
	if c.EditedAt == nil {
		t.Fatal("edited_at not stamped on the returned comment")
	}
}

func TestCommentService_EditByNonAuthorIsForbidden(t *testing.T) {
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "not-author")
	_, err := svc.Edit(ctx, idgen.NewID(), "x")
	assertAppCode(t, err, 403)
}

func TestCommentService_EditEmptyContentIsBadRequest(t *testing.T) {
	svc := service.NewCommentService(&mocks.CommentRepo{}, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Edit(ctx, idgen.NewID(), "")
	assertAppCode(t, err, 400)
}

func TestCommentService_EditRemovedCommentIsNotFound(t *testing.T) {
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Comment, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Edit(ctx, idgen.NewID(), "x")
	assertAppCode(t, err, 404)
}

func TestCommentService_EditHiddenCommentIsConflict(t *testing.T) {
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusHidden}, nil
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	_, err := svc.Edit(ctx, idgen.NewID(), "x")
	assertAppCode(t, err, 409)
}

func TestCommentService_ListReportsAuthorScoped(t *testing.T) {
	commentID := idgen.NewID()
	comments := &mocks.CommentRepo{
		GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: cid, OwnerUID: "author"}, nil
		},
		ListReportsFn: func(_ context.Context, _ domain.ID, _ *domain.ReportStatus, _ domain.Page) ([]*domain.CommentReport, string, error) {
			return []*domain.CommentReport{
				{ID: idgen.NewID(), CommentID: commentID, ReporterUID: "r1", Status: domain.ReportStatusOpen},
				{ID: idgen.NewID(), CommentID: commentID, ReporterUID: "r2", Status: domain.ReportStatusOpen},
			}, "next", nil
		},
	}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		return map[string]sso.User{}, nil
	}}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, dir)

	// The comment author may list reports.
	ctx := service.WithActor(context.Background(), "author")
	list, err := svc.ListReports(ctx, commentID, nil, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Reports) != 2 || list.NextCursor != "next" {
		t.Fatalf("unexpected report list: %+v cursor=%q", list.Reports, list.NextCursor)
	}
	if len(requested) != 2 {
		t.Fatalf("expected 2 reporter uids enriched, got %v", requested)
	}

	// A non-author is forbidden.
	other := service.WithActor(context.Background(), "stranger")
	if _, err := svc.ListReports(other, commentID, nil, domain.Page{}); err == nil {
		assertAppCode(t, err, 403)
	} else {
		assertAppCode(t, err, 403)
	}
}

func TestCommentService_ResolveReportTransitionsAndHides(t *testing.T) {
	reportID := idgen.NewID()
	commentID := idgen.NewID()
	var resolvedStatus domain.ReportStatus
	var resolver string
	var hiddenComment *domain.ID
	comments := &mocks.CommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*domain.CommentReport, error) {
			return &domain.CommentReport{ID: reportID, CommentID: commentID, Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
		ResolveReportFn: func(_ context.Context, _ domain.ID, s domain.ReportStatus, uid string, _ time.Time) error {
			resolvedStatus = s
			resolver = uid
			return nil
		},
		SetStatusFn: func(_ context.Context, cid domain.ID, _ domain.CommentStatus) error {
			hiddenComment = &cid
			return nil
		},
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	if err := svc.ResolveReport(ctx, reportID, domain.ReportStatusResolved); err != nil {
		t.Fatal(err)
	}
	if resolvedStatus != domain.ReportStatusResolved || resolver != "author" {
		t.Fatalf("resolve recorded wrong: status=%d by=%q", resolvedStatus, resolver)
	}
	if hiddenComment == nil || *hiddenComment != commentID {
		t.Fatal("resolving a report should hide the offending comment")
	}
}

func TestCommentService_ResolveReportDismissLeavesCommentVisible(t *testing.T) {
	comments := &mocks.CommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*domain.CommentReport, error) {
			return &domain.CommentReport{ID: idgen.NewID(), CommentID: idgen.NewID(), Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
		SetStatusFn: func(_ context.Context, _ domain.ID, _ domain.CommentStatus) error {
			t.Fatal("dismiss must not change the comment status")
			return nil
		},
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	if err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusDismissed); err != nil {
		t.Fatal(err)
	}
}

func TestCommentService_ResolveReportByNonAuthorIsForbidden(t *testing.T) {
	comments := &mocks.CommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*domain.CommentReport, error) {
			return &domain.CommentReport{ID: idgen.NewID(), CommentID: idgen.NewID(), Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "stranger")
	err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusResolved)
	assertAppCode(t, err, 403)
}

func TestCommentService_ResolveUnknownReportIsNotFound(t *testing.T) {
	comments := &mocks.CommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*domain.CommentReport, error) {
			return nil, repository.ErrNotFound
		},
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusResolved)
	assertAppCode(t, err, 404)
}

func TestCommentService_ResolveReportInvalidStatusIsBadRequest(t *testing.T) {
	svc := service.NewCommentService(&mocks.CommentRepo{}, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "author")
	err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusOpen)
	assertAppCode(t, err, 400)
}

func TestCommentService_ResolveReportEmitsAuditEvent(t *testing.T) {
	reportID := idgen.NewID()
	commentID := idgen.NewID()
	var logged *domain.AuditEvent
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		calls++
		return nil
	}}
	comments := &mocks.CommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*domain.CommentReport, error) {
			return &domain.CommentReport{ID: reportID, CommentID: commentID, Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*domain.Comment, error) {
			return &domain.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
		ResolveReportFn: func(_ context.Context, _ domain.ID, _ domain.ReportStatus, _ string, _ time.Time) error { return nil },
		SetStatusFn:     func(_ context.Context, _ domain.ID, _ domain.CommentStatus) error { return nil },
	}
	svc := service.NewCommentService(comments, &mocks.FileRepo{}, &mocks.MessageRepo{}, &mocks.UserDir{}, service.WithCommentAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "author")
	if err := svc.ResolveReport(ctx, reportID, domain.ReportStatusResolved); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("audit Log called %d times, want exactly 1", calls)
	}
	if logged.Action != domain.AuditReportResolve {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditReportResolve)
	}
	if logged.ActorUID != "author" {
		t.Fatalf("actor = %q, want author (resolver)", logged.ActorUID)
	}
	if logged.SubjectID != reportID.String() {
		t.Fatalf("subject id = %q, want report id %s", logged.SubjectID, reportID.String())
	}
	if logged.SubjectType != "comment_report" {
		t.Fatalf("subject type = %q, want comment_report", logged.SubjectType)
	}
	// Meta carries the comment id and the resolved status label.
	if logged.Meta["comment_id"] != commentID.String() {
		t.Fatalf("meta comment_id = %v, want %s", logged.Meta["comment_id"], commentID.String())
	}
	if logged.Meta["status"] != "resolved" {
		t.Fatalf("meta status = %v, want resolved", logged.Meta["status"])
	}
}

func TestCommentService_LikeForbiddenForPrivateTarget(t *testing.T) {
	target := idgen.NewID()
	comments := &mocks.CommentRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Comment, error) {
		return &domain.Comment{ID: id, OwnerUID: "owner", TargetType: domain.TargetTypeFile, TargetID: target}, nil
	}}
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := service.NewCommentService(comments, files, &mocks.MessageRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "stranger")
	if _, err := svc.Like(ctx, idgen.NewID()); err == nil {
		t.Fatal("expected forbidden when liking a comment on a private target")
	}
}
