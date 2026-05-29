package service_test

import (
	"context"
	stderrors "errors"
	"testing"

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
