package service_test

import (
	"context"
	stderrors "errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

// assertAppCode asserts err is an *AppError with the given HTTP status. Defined
// once here and reused by message_test.go and comment_test.go (same package).
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

func TestFileService_CreateStoresAndPersists(t *testing.T) {
	var created *domain.File
	repo := &mocks.FileRepo{
		CreateFn: func(_ context.Context, f *domain.File) error { created = f; return nil },
	}
	store := &mocks.Storage{
		PutFn: func(_ context.Context, owner, name string, _ io.Reader) (string, int64, error) {
			if owner != "owner" {
				t.Errorf("owner passed to storage = %q", owner)
			}
			return "owner/2026/05/key", 11, nil
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	f, err := svc.Create(ctx, service.CreateFileInput{Name: "a.txt", Reader: strings.NewReader("hello world")})
	if err != nil {
		t.Fatal(err)
	}
	if f.OwnerUID != "owner" || f.Size != 11 || f.StorageType != "local" || f.StorageKey != "owner/2026/05/key" {
		t.Fatalf("unexpected file: %+v", f)
	}
	if created == nil {
		t.Fatal("repo.Create was not called")
	}
}

func TestFileService_CreateRequiresActor(t *testing.T) {
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	_, err := svc.Create(context.Background(), service.CreateFileInput{Name: "a", Reader: strings.NewReader("x")})
	assertAppCode(t, err, 401)
}

func TestFileService_CreateRejectsEmptyName(t *testing.T) {
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, service.CreateFileInput{Reader: strings.NewReader("x")})
	assertAppCode(t, err, 400)
}

func TestFileService_GetPrivateForbidden(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "someone", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_GetPublicAllowed(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "someone", Visibility: domain.VisibilityPublic}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	if _, err := svc.Get(ctx, idgen.NewID()); err != nil {
		t.Fatalf("public file should be readable: %v", err)
	}
}

func TestFileService_GetNotFound(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) { return nil, repository.ErrNotFound },
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "x")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 404)
}

func TestFileService_ListOnlyOwnFiles(t *testing.T) {
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "me")
	_, err := svc.List(ctx, "someone-else", domain.Page{})
	assertAppCode(t, err, 403)
}

func TestFileService_ListDefaultsToActor(t *testing.T) {
	repo := &mocks.FileRepo{
		ListByOwnerFn: func(_ context.Context, owner string, _ domain.Page) ([]*domain.File, string, error) {
			if owner != "me" {
				t.Errorf("owner = %q, want me", owner)
			}
			return []*domain.File{{OwnerUID: "me"}}, "next", nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "me")
	list, err := svc.List(ctx, "", domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if list.NextCursor != "next" || len(list.Files) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestFileService_DeleteRequiresOwner(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "not-owner")
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_DeleteRemovesRowThenBlob(t *testing.T) {
	deletedRow, deletedBlob := false, false
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { deletedRow = true; return nil },
	}
	store := &mocks.Storage{
		DeleteFn: func(_ context.Context, key string) error {
			if key != "k" {
				t.Errorf("blob key = %q", key)
			}
			deletedBlob = true
			return nil
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if !deletedRow || !deletedBlob {
		t.Fatalf("deletedRow=%v deletedBlob=%v", deletedRow, deletedBlob)
	}
}

func TestFileService_DeleteSucceedsWhenBlobDeleteFails(t *testing.T) {
	rowDeleted := false
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { rowDeleted = true; return nil },
	}
	store := &mocks.Storage{
		DeleteFn: func(_ context.Context, _ string) error {
			return stderrors.New("unexpected storage error")
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	// Blob removal is best-effort: a storage error must not fail the request.
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !rowDeleted {
		t.Fatal("repo.Delete was not called")
	}
}

func TestFileService_DownloadURLAppliesACL(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate, StorageKey: "k"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	assertAppCode(t, err, 403)
}

func TestFileService_DownloadURLPresigns(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
	}
	store := &mocks.Storage{
		PresignGetFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://signed/" + key, nil
		},
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")
	url, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	if err != nil || url != "https://signed/k" {
		t.Fatalf("url=%q err=%v", url, err)
	}
}
