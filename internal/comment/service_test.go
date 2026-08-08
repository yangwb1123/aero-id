package comment_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"

	"github.com/aero/aero-id/internal/comment"
)

type mockCommentRepo struct {
	CreateFn              func(ctx context.Context, c *comment.Comment) error
	CreateWithReplyBumpFn func(ctx context.Context, c *comment.Comment, evt *outbox.Event) error
	GetFn                 func(ctx context.Context, id domain.ID) (*comment.Comment, error)
	ListByTargetFn        func(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*comment.Comment, string, error)
	ListRepliesFn         func(ctx context.Context, rootID domain.ID, page domain.Page) ([]*comment.Comment, string, error)
	IncReplyCountFn       func(ctx context.Context, parentID domain.ID) error
	SoftDeleteFn          func(ctx context.Context, id domain.ID) error
	UpdateContentFn       func(ctx context.Context, id domain.ID, content string, at time.Time) error
	LikeFn                func(ctx context.Context, commentID domain.ID, uid string) (int, error)
	UnlikeFn              func(ctx context.Context, commentID domain.ID, uid string) (int, error)
	CreateReportFn        func(ctx context.Context, r *comment.CommentReport) error
	SetStatusFn           func(ctx context.Context, commentID domain.ID, status domain.CommentStatus) error
	ListReportsFn         func(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) ([]*comment.CommentReport, string, error)
	GetReportFn           func(ctx context.Context, reportID domain.ID) (*comment.CommentReport, error)
	ResolveReportFn       func(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time) error
	SearchCommentsFn      func(ctx context.Context, ownerUID, query string, limit int) ([]*comment.Comment, error)
}

func (m *mockCommentRepo) Create(ctx context.Context, c *comment.Comment) error {
	return m.CreateFn(ctx, c)
}
func (m *mockCommentRepo) CreateWithReplyBump(ctx context.Context, c *comment.Comment, evt *outbox.Event) error {
	if m.CreateWithReplyBumpFn == nil {
		return nil
	}
	return m.CreateWithReplyBumpFn(ctx, c, evt)
}
func (m *mockCommentRepo) Get(ctx context.Context, id domain.ID) (*comment.Comment, error) {
	return m.GetFn(ctx, id)
}
func (m *mockCommentRepo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*comment.Comment, string, error) {
	return m.ListByTargetFn(ctx, t, target, page)
}
func (m *mockCommentRepo) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*comment.Comment, string, error) {
	return m.ListRepliesFn(ctx, rootID, page)
}
func (m *mockCommentRepo) IncReplyCount(ctx context.Context, parentID domain.ID) error {
	return m.IncReplyCountFn(ctx, parentID)
}
func (m *mockCommentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	return m.SoftDeleteFn(ctx, id)
}
func (m *mockCommentRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
	if m.UpdateContentFn == nil {
		return nil
	}
	return m.UpdateContentFn(ctx, id, content, at)
}
func (m *mockCommentRepo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
	return m.LikeFn(ctx, commentID, uid)
}
func (m *mockCommentRepo) Unlike(ctx context.Context, commentID domain.ID, uid string) (int, error) {
	return m.UnlikeFn(ctx, commentID, uid)
}
func (m *mockCommentRepo) CreateReport(ctx context.Context, r *comment.CommentReport) error {
	if m.CreateReportFn == nil {
		return nil
	}
	return m.CreateReportFn(ctx, r)
}
func (m *mockCommentRepo) SetStatus(ctx context.Context, commentID domain.ID, status domain.CommentStatus) error {
	if m.SetStatusFn == nil {
		return nil
	}
	return m.SetStatusFn(ctx, commentID, status)
}
func (m *mockCommentRepo) ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) ([]*comment.CommentReport, string, error) {
	if m.ListReportsFn == nil {
		return nil, "", nil
	}
	return m.ListReportsFn(ctx, commentID, status, page)
}
func (m *mockCommentRepo) GetReport(ctx context.Context, reportID domain.ID) (*comment.CommentReport, error) {
	if m.GetReportFn == nil {
		return nil, domain.ErrNotFound
	}
	return m.GetReportFn(ctx, reportID)
}
func (m *mockCommentRepo) ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time) error {
	if m.ResolveReportFn == nil {
		return nil
	}
	return m.ResolveReportFn(ctx, reportID, status, resolverUID, at)
}
func (m *mockCommentRepo) SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*comment.Comment, error) {
	if m.SearchCommentsFn == nil {
		return nil, nil
	}
	return m.SearchCommentsFn(ctx, ownerUID, query, limit)
}

type mockFileRepo struct {
	GetFn func(ctx context.Context, id domain.ID) (*domain.File, error)
}

func (m *mockFileRepo) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return m.GetFn(ctx, id)
}

type mockMessageRepo struct {
	GetFn func(ctx context.Context, id domain.ID) (*domain.Message, error)
}

func (m *mockMessageRepo) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return m.GetFn(ctx, id)
}

type mockUserDir struct {
	GetUsersFn func(ctx context.Context, uids []string) (map[string]sso.User, error)
}

func (m *mockUserDir) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
	return m.GetUsersFn(ctx, uids)
}

func readableFileRepo(target domain.ID) *mockFileRepo {
	return &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPublic}, nil
	}}
}

func newService(
	repo comment.Repository,
	files comment.FileGetter,
	messages comment.MessageGetter,
	dir comment.UserDirectory,
	opts ...comment.Option,
) comment.Service {
	return comment.NewService(repo, files, messages, dir, opts...)
}

func TestCommentService_CreateTopLevel(t *testing.T) {
	target := idgen.NewID()
	var saved *comment.Comment
	comments := &mockCommentRepo{CreateWithReplyBumpFn: func(_ context.Context, c *comment.Comment, _ *outbox.Event) error { saved = c; return nil }}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	c, err := svc.Create(ctx, comment.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, Content: "nice"})
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
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: root, ParentID: nil, RootID: root, TargetType: domain.TargetTypeFile, TargetID: target, OwnerUID: "rootauthor"}, nil
		},
		CreateWithReplyBumpFn: func(_ context.Context, c *comment.Comment, e *outbox.Event) error {
			bumpedFor = c.ParentID
			evt = e
			return nil
		},
	}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	c, err := svc.Create(ctx, comment.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &root, Content: "re"})
	if err != nil {
		t.Fatal(err)
	}
	if c.RootID != root || c.ParentID == nil || *c.ParentID != root {
		t.Fatalf("reply wiring wrong: %+v", c)
	}
	if bumpedFor == nil || *bumpedFor != root {
		t.Fatal("reply was not routed through the atomic reply-count bump with the parent set")
	}
	if evt == nil || evt.EventType != "comment.replied" || evt.AggregateType != "comment" {
		t.Fatalf("expected comment.replied event, got %+v", evt)
	}
}

func TestCommentService_RejectsThirdLevel(t *testing.T) {
	target := idgen.NewID()
	root := idgen.NewID()
	midParent := idgen.NewID()
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: midParent, ParentID: &root, RootID: root, TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
	}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, comment.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &midParent, Content: "too deep"})
	assertAppCode(t, err, 400)
	var ae *apperr.AppError
	stderrors.As(err, &ae)
	if ae.Code != "comment.nesting_too_deep" {
		t.Fatalf("code = %q, want comment.nesting_too_deep", ae.Code)
	}
}

func TestCommentService_TargetNotFound(t *testing.T) {
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return nil, domain.ErrNotFound
	}}
	svc := newService(&mockCommentRepo{}, files, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, comment.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: idgen.NewID(), Content: "x"})
	assertAppCode(t, err, 404)
}

func TestCommentService_TargetForbidden(t *testing.T) {
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "someone", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := newService(&mockCommentRepo{}, files, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "stranger")
	_, err := svc.Create(ctx, comment.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: idgen.NewID(), Content: "x"})
	assertAppCode(t, err, 403)
}

func TestCommentService_ParentBelongsToDifferentTarget(t *testing.T) {
	target := idgen.NewID()
	other := idgen.NewID()
	root := idgen.NewID()
	comments := &mockCommentRepo{GetFn: func(_ context.Context, _ domain.ID) (*comment.Comment, error) {
		return &comment.Comment{ID: root, ParentID: nil, RootID: root, TargetType: domain.TargetTypeFile, TargetID: other}, nil
	}}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	_, err := svc.Create(ctx, comment.CreateCommentInput{TargetType: domain.TargetTypeFile, TargetID: target, ParentID: &root, Content: "x"})
	assertAppCode(t, err, 400)
}

func TestCommentService_ListEnrichesAuthors(t *testing.T) {
	target := idgen.NewID()
	comments := &mockCommentRepo{ListByTargetFn: func(_ context.Context, _ domain.TargetType, _ domain.ID, _ domain.Page) ([]*comment.Comment, string, error) {
		return []*comment.Comment{{OwnerUID: "x"}, {OwnerUID: "y"}, {OwnerUID: "x"}}, "next", nil
	}}
	var requested []string
	dir := &mockUserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		return map[string]sso.User{"x": {UID: "x"}, "y": {UID: "y"}}, nil
	}}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, dir)
	ctx := domain.WithActor(context.Background(), "viewer")
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
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		ListRepliesFn: func(_ context.Context, _ domain.ID, _ domain.Page) ([]*comment.Comment, string, error) {
			return []*comment.Comment{{OwnerUID: "u1"}, {OwnerUID: "u2"}}, "tok", nil
		},
	}
	var requested []string
	dir := &mockUserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u}
		}
		return out, nil
	}}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, dir)
	ctx := domain.WithActor(context.Background(), "viewer")
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
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: id, OwnerUID: "owner", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
	}
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := newService(comments, files, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "stranger")
	_, err := svc.Replies(ctx, rootID, domain.Page{})
	assertAppCode(t, err, 403)
}

func TestCommentService_DeleteRequiresOwner(t *testing.T) {
	comments := &mockCommentRepo{GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
		return &comment.Comment{ID: id, OwnerUID: "author"}, nil
	}}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "not-author")
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestCommentService_LikeAndUnlike(t *testing.T) {
	target := idgen.NewID()
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		LikeFn:   func(_ context.Context, _ domain.ID, _ string) (int, error) { return 1, nil },
		UnlikeFn: func(_ context.Context, _ domain.ID, _ string) (int, error) { return 0, nil },
	}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "viewer")

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
	var saved *comment.CommentReport
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		CreateReportFn: func(_ context.Context, r *comment.CommentReport) error { saved = r; return nil },
	}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "reporter")
	if err := svc.Report(ctx, idgen.NewID(), "spam"); err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.ReporterUID != "reporter" || saved.Reason != "spam" {
		t.Fatalf("report not persisted correctly: %+v", saved)
	}
}

func TestCommentService_ReportDuplicateIsConflict(t *testing.T) {
	target := idgen.NewID()
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: id, OwnerUID: "author", TargetType: domain.TargetTypeFile, TargetID: target}, nil
		},
		CreateReportFn: func(_ context.Context, _ *comment.CommentReport) error { return domain.ErrDuplicate },
	}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "reporter")
	err := svc.Report(ctx, idgen.NewID(), "spam")
	assertAppCode(t, err, 409)
}

func TestCommentService_ReportBlankReasonIsBadRequest(t *testing.T) {
	svc := newService(&mockCommentRepo{}, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "reporter")
	err := svc.Report(ctx, idgen.NewID(), "")
	assertAppCode(t, err, 400)
}

func TestCommentService_HideByNonAuthorIsForbidden(t *testing.T) {
	comments := &mockCommentRepo{GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
		return &comment.Comment{ID: id, OwnerUID: "author"}, nil
	}}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "not-author")
	err := svc.Hide(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestCommentService_HideByAuthorSetsHiddenStatus(t *testing.T) {
	var gotStatus domain.CommentStatus
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: id, OwnerUID: "author"}, nil
		},
		SetStatusFn: func(_ context.Context, _ domain.ID, s domain.CommentStatus) error { gotStatus = s; return nil },
	}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	if err := svc.Hide(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if gotStatus != domain.CommentStatusHidden {
		t.Fatalf("status = %d, want hidden(%d)", gotStatus, domain.CommentStatusHidden)
	}
}

func TestCommentService_HiddenCommentExcludedFromList(t *testing.T) {
	target := idgen.NewID()
	comments := &mockCommentRepo{ListByTargetFn: func(_ context.Context, _ domain.TargetType, _ domain.ID, _ domain.Page) ([]*comment.Comment, string, error) {
		return []*comment.Comment{{OwnerUID: "x", Status: domain.CommentStatusNormal}}, "", nil
	}}
	dir := &mockUserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u}
		}
		return out, nil
	}}
	svc := newService(comments, readableFileRepo(target), &mockMessageRepo{}, dir)
	ctx := domain.WithActor(context.Background(), "viewer")
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
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: cid, OwnerUID: "author", Content: "old", Status: domain.CommentStatusNormal}, nil
		},
		UpdateContentFn: func(_ context.Context, _ domain.ID, content string, _ time.Time) error {
			gotContent = content
			return nil
		},
	}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
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
	comments := &mockCommentRepo{GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
		return &comment.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
	}}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "not-author")
	_, err := svc.Edit(ctx, idgen.NewID(), "x")
	assertAppCode(t, err, 403)
}

func TestCommentService_EditEmptyContentIsBadRequest(t *testing.T) {
	svc := newService(&mockCommentRepo{}, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	_, err := svc.Edit(ctx, idgen.NewID(), "")
	assertAppCode(t, err, 400)
}

func TestCommentService_EditRemovedCommentIsNotFound(t *testing.T) {
	comments := &mockCommentRepo{GetFn: func(_ context.Context, _ domain.ID) (*comment.Comment, error) {
		return nil, domain.ErrNotFound
	}}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	_, err := svc.Edit(ctx, idgen.NewID(), "x")
	assertAppCode(t, err, 404)
}

func TestCommentService_EditHiddenCommentIsConflict(t *testing.T) {
	comments := &mockCommentRepo{GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
		return &comment.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusHidden}, nil
	}}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	_, err := svc.Edit(ctx, idgen.NewID(), "x")
	assertAppCode(t, err, 409)
}

func TestCommentService_ListReportsAuthorScoped(t *testing.T) {
	commentID := idgen.NewID()
	comments := &mockCommentRepo{
		GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: cid, OwnerUID: "author"}, nil
		},
		ListReportsFn: func(_ context.Context, _ domain.ID, _ *domain.ReportStatus, _ domain.Page) ([]*comment.CommentReport, string, error) {
			return []*comment.CommentReport{
				{ID: idgen.NewID(), CommentID: commentID, ReporterUID: "r1", Status: domain.ReportStatusOpen},
				{ID: idgen.NewID(), CommentID: commentID, ReporterUID: "r2", Status: domain.ReportStatusOpen},
			}, "next", nil
		},
	}
	var requested []string
	dir := &mockUserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		return map[string]sso.User{}, nil
	}}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, dir)

	ctx := domain.WithActor(context.Background(), "author")
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

	other := domain.WithActor(context.Background(), "stranger")
	if _, err := svc.ListReports(other, commentID, nil, domain.Page{}); err == nil {
		t.Fatal("expected error for non-author")
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
	comments := &mockCommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*comment.CommentReport, error) {
			return &comment.CommentReport{ID: reportID, CommentID: commentID, Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
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
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
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
	comments := &mockCommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*comment.CommentReport, error) {
			return &comment.CommentReport{ID: idgen.NewID(), CommentID: idgen.NewID(), Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
		SetStatusFn: func(_ context.Context, _ domain.ID, _ domain.CommentStatus) error {
			t.Fatal("dismiss must not change the comment status")
			return nil
		},
	}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	if err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusDismissed); err != nil {
		t.Fatal(err)
	}
}

func TestCommentService_ResolveReportByNonAuthorIsForbidden(t *testing.T) {
	comments := &mockCommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*comment.CommentReport, error) {
			return &comment.CommentReport{ID: idgen.NewID(), CommentID: idgen.NewID(), Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
	}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "stranger")
	err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusResolved)
	assertAppCode(t, err, 403)
}

func TestCommentService_ResolveUnknownReportIsNotFound(t *testing.T) {
	comments := &mockCommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*comment.CommentReport, error) {
			return nil, domain.ErrNotFound
		},
	}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusResolved)
	assertAppCode(t, err, 404)
}

func TestCommentService_ResolveReportInvalidStatusIsBadRequest(t *testing.T) {
	svc := newService(&mockCommentRepo{}, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "author")
	err := svc.ResolveReport(ctx, idgen.NewID(), domain.ReportStatusOpen)
	assertAppCode(t, err, 400)
}

func TestCommentService_ResolveReportEmitsAuditEvent(t *testing.T) {
	reportID := idgen.NewID()
	commentID := idgen.NewID()
	var logged *domain.AuditEvent
	calls := 0
	audit := &mockAuditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		calls++
		return nil
	}}
	comments := &mockCommentRepo{
		GetReportFn: func(_ context.Context, _ domain.ID) (*comment.CommentReport, error) {
			return &comment.CommentReport{ID: reportID, CommentID: commentID, Status: domain.ReportStatusOpen}, nil
		},
		GetFn: func(_ context.Context, cid domain.ID) (*comment.Comment, error) {
			return &comment.Comment{ID: cid, OwnerUID: "author", Status: domain.CommentStatusNormal}, nil
		},
		ResolveReportFn: func(_ context.Context, _ domain.ID, _ domain.ReportStatus, _ string, _ time.Time) error { return nil },
		SetStatusFn:     func(_ context.Context, _ domain.ID, _ domain.CommentStatus) error { return nil },
	}
	svc := newService(comments, &mockFileRepo{}, &mockMessageRepo{}, &mockUserDir{}, comment.WithAuditLogger(audit))
	ctx := domain.WithActor(context.Background(), "author")
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
	if logged.ID != reportID {
		t.Fatalf("event id = %s, want report id %s (deterministic dedup anchor)", logged.ID, reportID)
	}
	if logged.SubjectType != "comment_report" {
		t.Fatalf("subject type = %q, want comment_report", logged.SubjectType)
	}
	if logged.Meta["comment_id"] != commentID.String() {
		t.Fatalf("meta comment_id = %v, want %s", logged.Meta["comment_id"], commentID.String())
	}
	if logged.Meta["status"] != "resolved" {
		t.Fatalf("meta status = %v, want resolved", logged.Meta["status"])
	}
}

func TestCommentService_LikeForbiddenForPrivateTarget(t *testing.T) {
	target := idgen.NewID()
	comments := &mockCommentRepo{GetFn: func(_ context.Context, id domain.ID) (*comment.Comment, error) {
		return &comment.Comment{ID: id, OwnerUID: "owner", TargetType: domain.TargetTypeFile, TargetID: target}, nil
	}}
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{ID: target, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
	}}
	svc := newService(comments, files, &mockMessageRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "stranger")
	if _, err := svc.Like(ctx, idgen.NewID()); err == nil {
		t.Fatal("expected forbidden when liking a comment on a private target")
	}
}

type mockAuditor struct {
	LogFn func(ctx context.Context, e *domain.AuditEvent) error
}

func (m *mockAuditor) Log(ctx context.Context, e *domain.AuditEvent) error {
	if m.LogFn == nil {
		return nil
	}
	return m.LogFn(ctx, e)
}

func assertAppCode(t *testing.T, err error, wantHTTP int) {
	t.Helper()
	var ae *apperr.AppError
	if !stderrors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.HTTP != wantHTTP {
		t.Fatalf("HTTP = %d, want %d (code=%s)", ae.HTTP, wantHTTP, ae.Code)
	}
}
