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

func setupShareRepos(t *testing.T) (repository.FileRepository, repository.FileShareRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewFileRepository(db), repository.NewFileShareRepository(db)
}

func TestFileShareRepo_GrantRevokeListIsShared(t *testing.T) {
	ctx := context.Background()
	files, shares := setupShareRepos(t)

	now := time.Now().UTC()
	f := &domain.File{
		ID: idgen.NewID(), OwnerUID: "owner", Name: "doc", Size: 1,
		MimeType: "text/plain", StorageKey: "owner/doc", StorageType: "local",
		Visibility: domain.VisibilityPrivate, CreatedAt: now, UpdatedAt: now,
	}
	if err := files.Create(ctx, f); err != nil {
		t.Fatal(err)
	}

	// Grant is idempotent: granting "a" twice across two calls yields one row.
	if err := shares.Grant(ctx, f.ID, "owner", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := shares.Grant(ctx, f.ID, "owner", []string{"a"}); err != nil {
		t.Fatal(err)
	}

	grantees, err := shares.ListGrantees(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grantees) != 2 {
		t.Fatalf("grantees = %v, want 2", grantees)
	}

	if ok, err := shares.IsSharedWith(ctx, f.ID, "a"); err != nil || !ok {
		t.Fatalf("IsSharedWith(a) = %v err=%v, want true", ok, err)
	}
	if ok, err := shares.IsSharedWith(ctx, f.ID, "stranger"); err != nil || ok {
		t.Fatalf("IsSharedWith(stranger) = %v err=%v, want false", ok, err)
	}

	// Revoke removes one grant; revoking a non-existent grant is a no-op.
	if err := shares.Revoke(ctx, f.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := shares.Revoke(ctx, f.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := shares.IsSharedWith(ctx, f.ID, "a"); err != nil || ok {
		t.Fatalf("IsSharedWith(a) after revoke = %v err=%v, want false", ok, err)
	}
}

func TestFileShareRepo_ListFilesSharedWith(t *testing.T) {
	ctx := context.Background()
	files, shares := setupShareRepos(t)

	mk := func(name string) *domain.File {
		now := time.Now().UTC()
		f := &domain.File{
			ID: idgen.NewID(), OwnerUID: "owner", Name: name, Size: 1,
			MimeType: "text/plain", StorageKey: "owner/" + name, StorageType: "local",
			Visibility: domain.VisibilityPrivate, CreatedAt: now, UpdatedAt: now,
		}
		if err := files.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	f1, f2 := mk("a"), mk("b")
	// f1 shared with "me", f2 shared with someone else.
	if err := shares.Grant(ctx, f1.ID, "owner", []string{"me"}); err != nil {
		t.Fatal(err)
	}
	if err := shares.Grant(ctx, f2.ID, "owner", []string{"other"}); err != nil {
		t.Fatal(err)
	}

	got, next, err := shares.ListFilesSharedWith(ctx, "me", domain.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Errorf("next cursor = %q, want empty", next)
	}
	if len(got) != 1 || got[0].ID != f1.ID {
		t.Fatalf("shared-with-me = %+v, want only f1", got)
	}
}
