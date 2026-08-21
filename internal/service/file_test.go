package service_test

import (
	"context"
	stderrors "errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
	"github.com/aero/aero-id/internal/storage"
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

// assertAuditIngestFailed asserts err is an *AppError with code audit.ingest_failed
// and HTTP 503 — the fail-closed contract for a durable audit write (Log/Record)
// failure. Shared by the per-site AuditFailureFailsClosed tests.
func assertAuditIngestFailed(t *testing.T, err error) {
	t.Helper()
	var ae *apperr.AppError
	if !stderrors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.Code != "audit.ingest_failed" {
		t.Fatalf("code = %q, want audit.ingest_failed (http=%d)", ae.Code, ae.HTTP)
	}
	if ae.HTTP != http.StatusServiceUnavailable {
		t.Fatalf("HTTP = %d, want 503 (code=%s)", ae.HTTP, ae.Code)
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

func TestFileService_DownloadURLRecordsAccessAudit(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, event *domain.AuditEvent) error {
		logged = event
		return nil
	}}
	repo := &mocks.FileRepo{GetFn: func(_ context.Context, got domain.ID) (*domain.File, error) {
		if got != id {
			t.Fatalf("file id = %s, want %s", got, id)
		}
		return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "owner/file", StorageType: "s3"}, nil
	}}
	store := &mocks.Storage{PresignGetFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
		return "https://download.example/file", nil
	}}
	svc := service.NewFileService(repo, store, "s3", service.WithFileAuditLogger(audit))
	if _, err := svc.DownloadURL(service.WithActor(context.Background(), "owner"), id, time.Minute); err != nil {
		t.Fatal(err)
	}
	if logged == nil || logged.Action != domain.AuditFileDownload || logged.SubjectID != id.String() {
		t.Fatalf("download audit = %#v, want file.download for %s", logged, id)
	}
}

func TestFileService_GetGranteeAllowed(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	shares := &mocks.FileShareRepo{
		IsSharedWithFn: func(_ context.Context, _ domain.ID, uid string) (bool, error) {
			return uid == "friend", nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "friend")
	if _, err := svc.Get(ctx, idgen.NewID()); err != nil {
		t.Fatalf("grantee should read shared private file: %v", err)
	}
}

func TestFileService_GetNonGranteePrivateForbidden(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	shares := &mocks.FileShareRepo{} // default IsSharedWith => false
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_DownloadURLGranteeAllowed(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate, StorageKey: "k"}, nil
		},
	}
	shares := &mocks.FileShareRepo{
		IsSharedWithFn: func(_ context.Context, _ domain.ID, _ string) (bool, error) { return true, nil },
	}
	store := &mocks.Storage{
		PresignGetFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://signed/" + key, nil
		},
	}
	svc := service.NewFileService(repo, store, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "friend")
	url, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	if err != nil || url != "https://signed/k" {
		t.Fatalf("grantee download url=%q err=%v", url, err)
	}
}

func TestFileService_ShareFileOwnerGrants(t *testing.T) {
	var granted []string
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	shares := &mocks.FileShareRepo{
		GrantFn: func(_ context.Context, _ domain.ID, owner string, uids []string) error {
			if owner != "owner" {
				t.Errorf("owner = %q", owner)
			}
			granted = uids
			return nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "owner")
	// "owner" self-grant is dropped; "a" appears once despite the duplicate.
	if err := svc.ShareFile(ctx, idgen.NewID(), []string{"a", "a", "owner", "b"}); err != nil {
		t.Fatal(err)
	}
	if len(granted) != 2 || granted[0] != "a" || granted[1] != "b" {
		t.Fatalf("granted = %v, want [a b]", granted)
	}
}

func TestFileService_ShareFileNonOwnerForbidden(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(&mocks.FileShareRepo{}))
	ctx := service.WithActor(context.Background(), "not-owner")
	err := svc.ShareFile(ctx, idgen.NewID(), []string{"a"})
	assertAppCode(t, err, 403)
}

func TestFileService_ShareFileRejectsBlankUID(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	shares := &mocks.FileShareRepo{
		GrantFn: func(context.Context, domain.ID, string, []string) error {
			t.Fatal("Grant must not be called when a uid is blank")
			return nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "owner")
	err := svc.ShareFile(ctx, idgen.NewID(), []string{"a", "   "})
	assertAppCode(t, err, 400)
}

func TestFileService_UnshareFileOwnerRevokes(t *testing.T) {
	var revoked string
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	shares := &mocks.FileShareRepo{
		RevokeFn: func(_ context.Context, _ domain.ID, uid string) error { revoked = uid; return nil },
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "owner")
	if err := svc.UnshareFile(ctx, idgen.NewID(), "friend"); err != nil {
		t.Fatal(err)
	}
	if revoked != "friend" {
		t.Fatalf("revoked = %q, want friend", revoked)
	}
}

func TestFileService_UnshareFileNonOwnerForbidden(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileShareRepo(&mocks.FileShareRepo{}))
	ctx := service.WithActor(context.Background(), "not-owner")
	err := svc.UnshareFile(ctx, idgen.NewID(), "friend")
	assertAppCode(t, err, 403)
}

func TestFileService_ListSharedWithMe(t *testing.T) {
	shares := &mocks.FileShareRepo{
		ListFilesSharedWithFn: func(_ context.Context, uid string, _ domain.Page) ([]*domain.File, string, error) {
			if uid != "me" {
				t.Errorf("uid = %q, want me", uid)
			}
			return []*domain.File{{OwnerUID: "owner"}}, "next", nil
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local", service.WithFileShareRepo(shares))
	ctx := service.WithActor(context.Background(), "me")
	list, err := svc.ListSharedWithMe(ctx, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if list.NextCursor != "next" || len(list.Files) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestFileService_PresignUploadUnsupportedBackend(t *testing.T) {
	// Default mock Storage.PresignPut reports ErrNotSupported (mirrors the local
	// backend); the service must translate that to a BadRequest.
	svc := service.NewFileService(&mocks.FileRepo{}, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.PresignUpload(ctx, service.PresignUploadInput{Name: "big.bin"}, time.Minute)
	assertAppCode(t, err, 400)
}

func TestFileService_PresignUploadHappyPath(t *testing.T) {
	store := &mocks.Storage{
		NewKeyFn: func(owner string) string { return owner + "/2026/05/abc" },
		PresignPutFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://s3/put/" + key, nil
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "s3")
	ctx := service.WithActor(context.Background(), "owner")
	res, err := svc.PresignUpload(ctx, service.PresignUploadInput{Name: "big.bin"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if res.StorageKey != "owner/2026/05/abc" || res.UploadURL != "https://s3/put/owner/2026/05/abc" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.FileID == (domain.ID{}) {
		t.Fatal("expected an allocated file id")
	}
}

func TestFileService_PresignUploadRejectsBlankName(t *testing.T) {
	store := &mocks.Storage{
		PresignPutFn: func(context.Context, string, time.Duration) (string, error) {
			t.Fatal("PresignPut must not be called for an invalid request")
			return "", nil
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "s3")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.PresignUpload(ctx, service.PresignUploadInput{}, time.Minute)
	assertAppErrCode(t, err, "file.invalid")
}

func TestFileService_CompleteUploadPersistsRow(t *testing.T) {
	var persisted *domain.File
	repo := &mocks.FileRepo{CreateFn: func(_ context.Context, f *domain.File) error { persisted = f; return nil }}
	store := &mocks.Storage{
		StatFn: func(_ context.Context, key string) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Key: key, Size: 42}, nil
		},
	}
	svc := service.NewFileService(repo, store, "s3")
	ctx := service.WithActor(context.Background(), "owner")
	id := idgen.NewID()
	f, err := svc.CompleteUpload(ctx, service.CompleteUploadInput{
		FileID: id, StorageKey: "owner/2026/05/abc", Name: "big.bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != id || f.Size != 42 || f.OwnerUID != "owner" || f.StorageType != "s3" {
		t.Fatalf("unexpected file: %+v", f)
	}
	if persisted == nil {
		t.Fatal("repo.Create was not called")
	}
}

func TestFileService_CompleteUploadRejectsForeignKey(t *testing.T) {
	store := &mocks.Storage{
		StatFn: func(context.Context, string) (*storage.ObjectInfo, error) {
			t.Fatal("Stat must not run for a key the actor doesn't own")
			return nil, nil
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "s3")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.CompleteUpload(ctx, service.CompleteUploadInput{
		FileID: idgen.NewID(), StorageKey: "someone-else/2026/05/abc", Name: "x",
	})
	assertAppCode(t, err, 403)
}

func TestFileService_CompleteUploadRejectsOversized(t *testing.T) {
	var deleted string
	store := &mocks.Storage{
		StatFn: func(_ context.Context, key string) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Key: key, Size: 100}, nil
		},
		DeleteFn: func(_ context.Context, key string) error { deleted = key; return nil },
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "s3", service.WithMaxUploadBytes(10))
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.CompleteUpload(ctx, service.CompleteUploadInput{
		FileID: idgen.NewID(), StorageKey: "owner/2026/05/abc", Name: "big.bin",
	})
	assertAppErrCode(t, err, "file.too_large")
	if deleted != "owner/2026/05/abc" {
		t.Errorf("oversized object should be deleted, got %q", deleted)
	}
}

func TestFileService_CompleteUploadObjectMissing(t *testing.T) {
	store := &mocks.Storage{
		StatFn: func(context.Context, string) (*storage.ObjectInfo, error) {
			return nil, storage.ErrNotFound
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "s3")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.CompleteUpload(ctx, service.CompleteUploadInput{
		FileID: idgen.NewID(), StorageKey: "owner/2026/05/abc", Name: "x",
	})
	assertAppErrCode(t, err, "file.not_uploaded")
}

func strptr(s string) *string                       { return &s }
func visptr(v domain.Visibility) *domain.Visibility { return &v }

func TestFileService_UpdateMetadataOwnerRenames(t *testing.T) {
	var gotFields map[string]any
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old.txt", Visibility: domain.VisibilityPrivate}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, fields map[string]any) error {
			gotFields = fields
			return nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	f, err := svc.UpdateMetadata(ctx, idgen.NewID(), service.UpdateFileMetadataInput{Name: strptr("new.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "new.txt" {
		t.Fatalf("name = %q, want new.txt", f.Name)
	}
	if gotFields["name"] != "new.txt" {
		t.Errorf("repo name field = %v, want new.txt", gotFields["name"])
	}
	if _, ok := gotFields["updated_at"]; !ok {
		t.Error("updated_at must be bumped")
	}
	if _, ok := gotFields["visibility"]; ok {
		t.Error("visibility must not be updated when not provided")
	}
}

func TestFileService_UpdateMetadataNonOwnerForbidden(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
		UpdateMetadataFn: func(context.Context, domain.ID, map[string]any) error {
			t.Fatal("UpdateMetadata must not be called for a non-owner")
			return nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "stranger")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), service.UpdateFileMetadataInput{Name: strptr("x")})
	assertAppCode(t, err, 403)
}

func TestFileService_UpdateMetadataEmptyName(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), service.UpdateFileMetadataInput{Name: strptr("   ")})
	assertAppCode(t, err, 400)
}

func TestFileService_UpdateMetadataInvalidVisibility(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), service.UpdateFileMetadataInput{Visibility: visptr(domain.Visibility(99))})
	assertAppErrCode(t, err, "file.invalid_visibility")
}

func TestFileService_UpdateMetadataPartialChangesOnlyProvided(t *testing.T) {
	var gotFields map[string]any
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "keep.txt", Visibility: domain.VisibilityPrivate, Meta: map[string]any{"a": "1"}}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, fields map[string]any) error {
			gotFields = fields
			return nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	f, err := svc.UpdateMetadata(ctx, idgen.NewID(), service.UpdateFileMetadataInput{
		Visibility: visptr(domain.VisibilityPublic),
		Meta:       map[string]any{"b": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// name was not provided -> unchanged, not in the field set.
	if _, ok := gotFields["name"]; ok {
		t.Error("name must not be in the update set when unprovided")
	}
	if gotFields["visibility"] != int16(domain.VisibilityPublic) {
		t.Errorf("visibility field = %v, want 1", gotFields["visibility"])
	}
	if f.Name != "keep.txt" {
		t.Errorf("name = %q, want keep.txt (unchanged)", f.Name)
	}
	// meta is merged: original key preserved, new key added.
	if f.Meta["a"] != "1" || f.Meta["b"] != "2" {
		t.Errorf("meta = %+v, want merged {a,b}", f.Meta)
	}
}

func TestFileService_UpdateMetadataSoftDeletedNotFound(t *testing.T) {
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old"}, nil
		},
		// The row was soft-deleted between the Get and the targeted update.
		UpdateMetadataFn: func(context.Context, domain.ID, map[string]any) error {
			return repository.ErrNotFound
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local")
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), service.UpdateFileMetadataInput{Name: strptr("new")})
	assertAppCode(t, err, 404)
}

// assertAppErrCode asserts err is an *AppError carrying the given Code string.
func assertAppErrCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	var ae *apperr.AppError
	if !stderrors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.Code != wantCode {
		t.Fatalf("code = %s, want %s", ae.Code, wantCode)
	}
}

func TestFileService_CreateRejectsTooLargeByDeclaredSize(t *testing.T) {
	store := &mocks.Storage{
		PutFn: func(context.Context, string, string, io.Reader) (string, int64, error) {
			t.Fatal("storage.Put must not be called when the declared size exceeds the limit")
			return "", 0, nil
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "local", service.WithMaxUploadBytes(5))
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, service.CreateFileInput{Name: "big.bin", Size: 10, Reader: strings.NewReader("0123456789")})
	assertAppErrCode(t, err, "file.too_large")
}

func TestFileService_CreateCapsOversizedStream(t *testing.T) {
	// Declared size is 0 (unknown) but the stream exceeds the limit; the cappedReader
	// must abort the write and the error must surface as file.too_large.
	store := &mocks.Storage{
		PutFn: func(_ context.Context, _, _ string, r io.Reader) (string, int64, error) {
			n, err := io.Copy(io.Discard, r)
			return "k", n, err
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "local", service.WithMaxUploadBytes(5))
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, service.CreateFileInput{Name: "big.bin", Reader: strings.NewReader("0123456789")})
	assertAppErrCode(t, err, "file.too_large")
}

func TestFileService_CreateRejectsUnsupportedType(t *testing.T) {
	store := &mocks.Storage{
		PutFn: func(context.Context, string, string, io.Reader) (string, int64, error) {
			t.Fatal("storage.Put must not be called for a disallowed type")
			return "", 0, nil
		},
	}
	svc := service.NewFileService(&mocks.FileRepo{}, store, "local",
		service.WithAllowedMimeTypes([]string{"image/png", "image/jpeg"}))
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, service.CreateFileInput{Name: "x.exe", MimeType: "application/octet-stream", Reader: strings.NewReader("x")})
	assertAppErrCode(t, err, "file.unsupported_type")
}

func TestFileService_CreateAllowsWithinLimits(t *testing.T) {
	repo := &mocks.FileRepo{CreateFn: func(context.Context, *domain.File) error { return nil }}
	store := &mocks.Storage{
		PutFn: func(_ context.Context, _, _ string, r io.Reader) (string, int64, error) {
			n, _ := io.Copy(io.Discard, r)
			return "k", n, nil
		},
	}
	svc := service.NewFileService(repo, store, "local",
		service.WithMaxUploadBytes(1024),
		service.WithAllowedMimeTypes([]string{"text/plain"}))
	ctx := service.WithActor(context.Background(), "owner")
	f, err := svc.Create(ctx, service.CreateFileInput{
		Name: "ok.txt", MimeType: "text/plain; charset=utf-8", Size: 5, Reader: strings.NewReader("hello"),
	})
	if err != nil {
		t.Fatalf("within limits should succeed: %v", err)
	}
	if f.Size != 5 {
		t.Fatalf("size = %d, want 5", f.Size)
	}
}

func TestFileService_CreateDedupesIdenticalContent(t *testing.T) {
	var deletedKey string
	store := &mocks.Storage{
		PutFn: func(_ context.Context, _, _ string, r io.Reader) (string, int64, error) {
			n, _ := io.Copy(io.Discard, r)
			return "fresh-key", n, nil
		},
		DeleteFn: func(_ context.Context, key string) error { deletedKey = key; return nil },
	}
	var persisted *domain.File
	repo := &mocks.FileRepo{
		FindByChecksumFn: func(_ context.Context, owner, _ string) (*domain.File, error) {
			return &domain.File{OwnerUID: owner, StorageKey: "existing-key"}, nil
		},
		CreateFn: func(_ context.Context, f *domain.File) error { persisted = f; return nil },
	}
	svc := service.NewFileService(repo, store, "local")
	ctx := service.WithActor(context.Background(), "owner")

	f, err := svc.Create(ctx, service.CreateFileInput{Name: "dup.bin", Reader: strings.NewReader("identical")})
	if err != nil {
		t.Fatal(err)
	}
	if f.StorageKey != "existing-key" {
		t.Errorf("storage key = %q, want existing-key (deduped to the existing blob)", f.StorageKey)
	}
	if deletedKey != "fresh-key" {
		t.Errorf("redundant blob deleted = %q, want fresh-key", deletedKey)
	}
	if persisted == nil || persisted.Checksum == "" {
		t.Error("a content checksum must be persisted on the new row")
	}
}

// ── B3 Gap-4 audit sites (standalone-fact) ──────────────────────────────────

func TestFileService_CreateAuditFactCommitted(t *testing.T) {
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.FileRepo{CreateFn: func(_ context.Context, f *domain.File) error { return nil }}
	store := &mocks.Storage{
		PutFn: func(_ context.Context, _, _ string, _ io.Reader) (string, int64, error) {
			return "owner/2026/05/key", 11, nil
		},
	}
	svc := service.NewFileService(repo, store, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	f, err := svc.Create(ctx, service.CreateFileInput{Name: "a.txt", Reader: strings.NewReader("hello world")})
	if err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("file.create audit fact was not emitted")
	}
	if logged.ID != f.ID {
		t.Fatalf("event ID = %v, want %v (anchored on the file ID)", logged.ID, f.ID)
	}
	if logged.Action != domain.AuditFileCreate {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditFileCreate)
	}
	if logged.SubjectType != "file" || logged.SubjectID != f.ID.String() {
		t.Fatalf("subject = (%q,%q), want (file,%s)", logged.SubjectType, logged.SubjectID, f.ID.String())
	}
	if logged.ActorUID != "owner" {
		t.Fatalf("actor = %q, want owner", logged.ActorUID)
	}
}

func TestFileService_CreateAuditFailureFailsClosed(t *testing.T) {
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.FileRepo{CreateFn: func(_ context.Context, f *domain.File) error { return nil }}
	store := &mocks.Storage{
		PutFn: func(_ context.Context, _, _ string, _ io.Reader) (string, int64, error) {
			return "owner/2026/05/key", 11, nil
		},
	}
	svc := service.NewFileService(repo, store, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, service.CreateFileInput{Name: "a.txt", Reader: strings.NewReader("hello world")})
	assertAuditIngestFailed(t, err)
}

func TestFileService_DeleteAuditFactCommitted(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "owner/k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { return nil },
	}
	svc := service.NewFileService(repo, &mocks.Storage{
		DeleteFn: func(_ context.Context, _ string) error { return nil },
	}, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	if err := svc.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("file.delete audit fact was not emitted")
	}
	if logged.ID != id {
		t.Fatalf("event ID = %v, want %v (anchored on the file ID)", logged.ID, id)
	}
	if logged.Action != domain.AuditFileDelete {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditFileDelete)
	}
	if logged.SubjectType != "file" || logged.SubjectID != id.String() {
		t.Fatalf("subject = (%q,%q), want (file,%s)", logged.SubjectType, logged.SubjectID, id.String())
	}
	if logged.ActorUID != "owner" {
		t.Fatalf("actor = %q, want owner", logged.ActorUID)
	}
}

func TestFileService_DeleteAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "owner/k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { return nil },
	}
	svc := service.NewFileService(repo, &mocks.Storage{
		DeleteFn: func(_ context.Context, _ string) error { return nil },
	}, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	assertAuditIngestFailed(t, svc.Delete(ctx, id))
}

func TestFileService_UpdateMetadataAuditFactCommitted(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old.txt"}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, _ map[string]any) error { return nil },
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	name := "new.txt"
	if _, err := svc.UpdateMetadata(ctx, id, service.UpdateFileMetadataInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("file.update audit fact was not emitted")
	}
	if logged.Action != domain.AuditFileUpdate {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditFileUpdate)
	}
	if logged.ID != (domain.ID{}) {
		t.Fatalf("event ID = %v, want zero (fresh per-execution — multi-occurrence op)", logged.ID)
	}
	if logged.SubjectType != "file" || logged.SubjectID != id.String() || logged.ActorUID != "owner" {
		t.Fatalf("unexpected update fact: %+v", logged)
	}
}

func TestFileService_UpdateMetadataAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old.txt"}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, _ map[string]any) error { return nil },
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	name := "new.txt"
	_, err := svc.UpdateMetadata(ctx, id, service.UpdateFileMetadataInput{Name: &name})
	assertAuditIngestFailed(t, err)
}

func TestFileService_UpdateMetadataNoOpEmitsNoFact(t *testing.T) {
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, _ *domain.AuditEvent) error {
		calls++
		return nil
	}}
	id := idgen.NewID()
	repo := &mocks.FileRepo{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old.txt"}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, _ map[string]any) error {
			t.Fatal("empty-mask update must not reach the repository")
			return nil
		},
	}
	svc := service.NewFileService(repo, &mocks.Storage{}, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	if _, err := svc.UpdateMetadata(ctx, id, service.UpdateFileMetadataInput{}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("empty-mask update must emit no audit fact (nothing changed), called %d times", calls)
	}
}

func TestFilePresign_CompleteUploadAuditFactCommitted(t *testing.T) {
	fileID := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.FileRepo{CreateFn: func(_ context.Context, f *domain.File) error { return nil }}
	store := &mocks.Storage{
		StatFn: func(_ context.Context, _ string) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Key: "owner/up/k", Size: 11}, nil
		},
	}
	svc := service.NewFileService(repo, store, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	in := service.CompleteUploadInput{FileID: fileID, Name: "a.txt", StorageKey: "owner/up/k"}
	f, err := svc.CompleteUpload(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("file.create (presigned materialization) audit fact was not emitted")
	}
	if logged.ID != f.ID || f.ID != fileID {
		t.Fatalf("event ID = %v, want %v (anchored on the presigned FileID — retry dedup)", logged.ID, fileID)
	}
	if logged.Action != domain.AuditFileCreate {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditFileCreate)
	}
	if logged.SubjectType != "file" || logged.SubjectID != fileID.String() || logged.ActorUID != "owner" {
		t.Fatalf("unexpected presigned-create fact: %+v", logged)
	}
}

func TestFilePresign_CompleteUploadAuditFailureFailsClosed(t *testing.T) {
	fileID := idgen.NewID()
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.FileRepo{CreateFn: func(_ context.Context, f *domain.File) error { return nil }}
	store := &mocks.Storage{
		StatFn: func(_ context.Context, _ string) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Key: "owner/up/k", Size: 11}, nil
		},
	}
	svc := service.NewFileService(repo, store, "local", service.WithFileAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "owner")
	in := service.CompleteUploadInput{FileID: fileID, Name: "a.txt", StorageKey: "owner/up/k"}
	_, err := svc.CompleteUpload(ctx, in)
	assertAuditIngestFailed(t, err)
}
