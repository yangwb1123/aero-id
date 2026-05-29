//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupCommentRepo(t *testing.T) repository.CommentRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewCommentRepository(db)
}

func TestCommentRepo_TopLevelAndReply(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)

	target := idgen.NewID()
	now := time.Now().UTC()

	root := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
		TargetID: target, Content: "top", Status: domain.CommentStatusNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	root.RootID = root.ID
	if err := repo.Create(ctx, root); err != nil {
		t.Fatal(err)
	}

	reply := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u2", TargetType: domain.TargetTypeFile,
		TargetID: target, ParentID: &root.ID, RootID: root.ID,
		Content: "child", Status: domain.CommentStatusNormal,
		CreatedAt: now.Add(time.Millisecond), UpdatedAt: now.Add(time.Millisecond),
	}
	if err := repo.Create(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if err := repo.IncReplyCount(ctx, root.ID); err != nil {
		t.Fatal(err)
	}

	tops, _, err := repo.ListByTarget(ctx, domain.TargetTypeFile, target, domain.Page{})
	if err != nil || len(tops) != 1 {
		t.Fatalf("tops len=%d err=%v", len(tops), err)
	}
	if tops[0].ReplyCount != 1 {
		t.Errorf("reply_count = %d, want 1", tops[0].ReplyCount)
	}

	replies, _, err := repo.ListReplies(ctx, root.ID, domain.Page{})
	if err != nil || len(replies) != 1 {
		t.Fatalf("replies len=%d err=%v", len(replies), err)
	}
}

func TestCommentRepo_ListByTargetPagination(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)

	target := idgen.NewID()

	for i := 0; i < 3; i++ {
		c := &domain.Comment{
			ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
			TargetID: target, Content: "top-level comment",
			Status:    domain.CommentStatusNormal,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		c.RootID = c.ID
		if err := repo.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	page1, cursor1, err := repo.ListByTarget(ctx, domain.TargetTypeFile, target, domain.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1 len=%d cursor=%q, want 2 comments and non-empty cursor", len(page1), cursor1)
	}

	page2, cursor2, err := repo.ListByTarget(ctx, domain.TargetTypeFile, target, domain.Page{Limit: 2, Cursor: cursor1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2 len=%d cursor=%q, want 1 comment and empty cursor", len(page2), cursor2)
	}
}

func TestCommentRepo_SoftDelete(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)

	c := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u", TargetType: domain.TargetTypeFile,
		TargetID: idgen.NewID(), Content: "x", Status: domain.CommentStatusNormal,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	c.RootID = c.ID
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Get(ctx, c.ID)
	if err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCommentRepo_CreateWithReplyBumpIsAtomic(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)

	target := idgen.NewID()
	now := time.Now().UTC()

	root := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
		TargetID: target, Content: "top", Status: domain.CommentStatusNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	root.RootID = root.ID
	if err := repo.CreateWithReplyBump(ctx, root); err != nil {
		t.Fatal(err)
	}

	reply := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u2", TargetType: domain.TargetTypeFile,
		TargetID: target, ParentID: &root.ID, RootID: root.ID,
		Content: "child", Status: domain.CommentStatusNormal,
		CreatedAt: now.Add(time.Millisecond), UpdatedAt: now.Add(time.Millisecond),
	}
	if err := repo.CreateWithReplyBump(ctx, reply); err != nil {
		t.Fatal(err)
	}

	// The reply insert and the parent's reply_count bump must commit together.
	got, err := repo.Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyCount != 1 {
		t.Errorf("reply_count = %d, want 1 (atomic bump)", got.ReplyCount)
	}
}

func TestCommentRepo_LikeUnlike(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)
	now := time.Now().UTC()

	c := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
		TargetID: idgen.NewID(), Content: "top", Status: domain.CommentStatusNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	c.RootID = c.ID
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	// Liking increments; a repeat by the same user is idempotent.
	if n, err := repo.Like(ctx, c.ID, "viewer"); err != nil || n != 1 {
		t.Fatalf("first like n=%d err=%v, want 1", n, err)
	}
	if n, err := repo.Like(ctx, c.ID, "viewer"); err != nil || n != 1 {
		t.Fatalf("duplicate like n=%d err=%v, want 1 (idempotent)", n, err)
	}
	if n, err := repo.Like(ctx, c.ID, "viewer2"); err != nil || n != 2 {
		t.Fatalf("second user like n=%d err=%v, want 2", n, err)
	}

	// Unliking decrements; a repeat is idempotent and never goes negative.
	if n, err := repo.Unlike(ctx, c.ID, "viewer"); err != nil || n != 1 {
		t.Fatalf("unlike n=%d err=%v, want 1", n, err)
	}
	if n, err := repo.Unlike(ctx, c.ID, "viewer"); err != nil || n != 1 {
		t.Fatalf("duplicate unlike n=%d err=%v, want 1 (idempotent)", n, err)
	}
}

func TestCommentRepo_CreateReportDedup(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)
	now := time.Now().UTC()

	c := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
		TargetID: idgen.NewID(), Content: "top", Status: domain.CommentStatusNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	c.RootID = c.ID
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	if err := repo.CreateReport(ctx, &domain.CommentReport{
		ID: idgen.NewID(), CommentID: c.ID, ReporterUID: "reporter", Reason: "spam", CreatedAt: now,
	}); err != nil {
		t.Fatalf("first report: %v", err)
	}
	// A repeat report by the same reporter must surface as ErrDuplicate.
	err := repo.CreateReport(ctx, &domain.CommentReport{
		ID: idgen.NewID(), CommentID: c.ID, ReporterUID: "reporter", Reason: "again", CreatedAt: now,
	})
	if err != repository.ErrDuplicate {
		t.Fatalf("duplicate report err=%v, want ErrDuplicate", err)
	}
	// A different reporter may report the same comment.
	if err := repo.CreateReport(ctx, &domain.CommentReport{
		ID: idgen.NewID(), CommentID: c.ID, ReporterUID: "reporter2", Reason: "spam", CreatedAt: now,
	}); err != nil {
		t.Fatalf("second reporter: %v", err)
	}
}

func TestCommentRepo_SetStatusExcludesFromListings(t *testing.T) {
	ctx := context.Background()
	repo := setupCommentRepo(t)
	now := time.Now().UTC()

	target := idgen.NewID()
	root := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u1", TargetType: domain.TargetTypeFile,
		TargetID: target, Content: "top", Status: domain.CommentStatusNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	root.RootID = root.ID
	if err := repo.Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	reply := &domain.Comment{
		ID: idgen.NewID(), OwnerUID: "u2", TargetType: domain.TargetTypeFile,
		TargetID: target, ParentID: &root.ID, RootID: root.ID,
		Content: "child", Status: domain.CommentStatusNormal,
		CreatedAt: now.Add(time.Millisecond), UpdatedAt: now.Add(time.Millisecond),
	}
	if err := repo.Create(ctx, reply); err != nil {
		t.Fatal(err)
	}

	// Hide the top-level comment: it must drop out of ListByTarget.
	if err := repo.SetStatus(ctx, root.ID, domain.CommentStatusHidden); err != nil {
		t.Fatal(err)
	}
	tops, _, err := repo.ListByTarget(ctx, domain.TargetTypeFile, target, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tops) != 0 {
		t.Fatalf("hidden top-level comment leaked: got %d", len(tops))
	}

	// Hide the reply: it must drop out of ListReplies.
	if err := repo.SetStatus(ctx, reply.ID, domain.CommentStatusHidden); err != nil {
		t.Fatal(err)
	}
	replies, _, err := repo.ListReplies(ctx, root.ID, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 0 {
		t.Fatalf("hidden reply leaked: got %d", len(replies))
	}

	// SetStatus on a missing comment is ErrNotFound.
	if err := repo.SetStatus(ctx, idgen.NewID(), domain.CommentStatusHidden); err != repository.ErrNotFound {
		t.Fatalf("set status on missing comment err=%v, want ErrNotFound", err)
	}
}
