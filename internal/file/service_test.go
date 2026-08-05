package file_test

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
	"github.com/aero/aero-id/internal/storage"

	"github.com/aero/aero-id/internal/file"
)

// ---- test doubles ----

type fileRepoMock struct {
	CreateFn                func(ctx context.Context, f *domain.File) error
	GetFn                   func(ctx context.Context, id domain.ID) (*domain.File, error)
	ListByOwnerFn           func(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error)
	DeleteFn                func(ctx context.Context, id domain.ID) error
	FindByChecksumFn        func(ctx context.Context, ownerUID, checksum string) (*domain.File, error)
	CountByStorageKeyFn     func(ctx context.Context, storageKey string) (int64, error)
	ListSoftDeletedBeforeFn func(ctx context.Context, before time.Time, limit int) ([]*domain.File, error)
	HardDeleteFn            func(ctx context.Context, ids []domain.ID) error
	CountAnyByStorageKeyFn  func(ctx context.Context, storageKey string) (int64, error)
	UpdateMetadataFn        func(ctx context.Context, id domain.ID, fields map[string]any) error
	SearchFilesFn           func(ctx context.Context, ownerUID, query string, limit int) ([]*domain.File, error)
}

func (m *fileRepoMock) Create(ctx context.Context, f *domain.File) error { return m.CreateFn(ctx, f) }
func (m *fileRepoMock) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return m.GetFn(ctx, id)
}
func (m *fileRepoMock) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error) {
	return m.ListByOwnerFn(ctx, ownerUID, page)
}
func (m *fileRepoMock) Delete(ctx context.Context, id domain.ID) error { return m.DeleteFn(ctx, id) }
func (m *fileRepoMock) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*domain.File, error) {
	if m.FindByChecksumFn == nil {
		return nil, domain.ErrNotFound
	}
	return m.FindByChecksumFn(ctx, ownerUID, checksum)
}
func (m *fileRepoMock) CountByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	if m.CountByStorageKeyFn == nil {
		return 0, nil
	}
	return m.CountByStorageKeyFn(ctx, storageKey)
}
func (m *fileRepoMock) ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*domain.File, error) {
	if m.ListSoftDeletedBeforeFn == nil {
		return nil, nil
	}
	return m.ListSoftDeletedBeforeFn(ctx, before, limit)
}
func (m *fileRepoMock) HardDelete(ctx context.Context, ids []domain.ID) error {
	if m.HardDeleteFn == nil {
		return nil
	}
	return m.HardDeleteFn(ctx, ids)
}
func (m *fileRepoMock) CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	if m.CountAnyByStorageKeyFn == nil {
		return 0, nil
	}
	return m.CountAnyByStorageKeyFn(ctx, storageKey)
}
func (m *fileRepoMock) UpdateMetadata(ctx context.Context, id domain.ID, fields map[string]any) error {
	if m.UpdateMetadataFn == nil {
		return nil
	}
	return m.UpdateMetadataFn(ctx, id, fields)
}
func (m *fileRepoMock) SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*domain.File, error) {
	if m.SearchFilesFn == nil {
		return nil, nil
	}
	return m.SearchFilesFn(ctx, ownerUID, query, limit)
}

type fileShareRepoMock struct {
	GrantFn               func(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error
	RevokeFn              func(ctx context.Context, fileID domain.ID, granteeUID string) error
	ListGranteesFn        func(ctx context.Context, fileID domain.ID) ([]string, error)
	ListFilesSharedWithFn func(ctx context.Context, uid string, page domain.Page) ([]*domain.File, string, error)
	IsSharedWithFn        func(ctx context.Context, fileID domain.ID, uid string) (bool, error)
}

func (m *fileShareRepoMock) Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error {
	if m.GrantFn == nil {
		return nil
	}
	return m.GrantFn(ctx, fileID, ownerUID, granteeUIDs)
}
func (m *fileShareRepoMock) Revoke(ctx context.Context, fileID domain.ID, granteeUID string) error {
	if m.RevokeFn == nil {
		return nil
	}
	return m.RevokeFn(ctx, fileID, granteeUID)
}
func (m *fileShareRepoMock) ListGrantees(ctx context.Context, fileID domain.ID) ([]string, error) {
	if m.ListGranteesFn == nil {
		return nil, nil
	}
	return m.ListGranteesFn(ctx, fileID)
}
func (m *fileShareRepoMock) ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*domain.File, string, error) {
	if m.ListFilesSharedWithFn == nil {
		return nil, "", nil
	}
	return m.ListFilesSharedWithFn(ctx, uid, page)
}
func (m *fileShareRepoMock) IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error) {
	if m.IsSharedWithFn == nil {
		return false, nil
	}
	return m.IsSharedWithFn(ctx, fileID, uid)
}

type storageMock struct {
	PutFn        func(ctx context.Context, ownerUID, name string, r io.Reader) (string, int64, error)
	GetFn        func(ctx context.Context, key string) (io.ReadCloser, error)
	DeleteFn     func(ctx context.Context, key string) error
	PresignGetFn func(ctx context.Context, key string, ttl time.Duration) (string, error)
	StatFn       func(ctx context.Context, key string) (*storage.ObjectInfo, error)
	NewKeyFn     func(ownerUID string) string
	PresignPutFn func(ctx context.Context, key string, ttl time.Duration) (string, error)
}

func (m *storageMock) Put(ctx context.Context, ownerUID, name string, r io.Reader) (string, int64, error) {
	return m.PutFn(ctx, ownerUID, name, r)
}
func (m *storageMock) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return m.GetFn(ctx, key)
}
func (m *storageMock) Delete(ctx context.Context, key string) error { return m.DeleteFn(ctx, key) }
func (m *storageMock) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return m.PresignGetFn(ctx, key, ttl)
}
func (m *storageMock) Stat(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	return m.StatFn(ctx, key)
}
func (m *storageMock) NewKey(ownerUID string) string {
	if m.NewKeyFn == nil {
		return ownerUID + "/key"
	}
	return m.NewKeyFn(ownerUID)
}
func (m *storageMock) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if m.PresignPutFn == nil {
		return "", storage.ErrNotSupported
	}
	return m.PresignPutFn(ctx, key, ttl)
}

// ---- helpers ----

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

func strptr(s string) *string                       { return &s }
func visptr(v domain.Visibility) *domain.Visibility { return &v }

// ---- tests ----

func TestFileService_CreateStoresAndPersists(t *testing.T) {
	var created *domain.File
	repo := &fileRepoMock{
		CreateFn: func(_ context.Context, f *domain.File) error { created = f; return nil },
	}
	store := &storageMock{
		PutFn: func(_ context.Context, owner, name string, _ io.Reader) (string, int64, error) {
			if owner != "owner" {
				t.Errorf("owner passed to storage = %q", owner)
			}
			return "owner/2026/05/key", 11, nil
		},
	}
	svc := file.NewService(repo, store, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	f, err := svc.Create(ctx, file.CreateFileInput{Name: "a.txt", Reader: strings.NewReader("hello world")})
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
	svc := file.NewService(&fileRepoMock{}, &storageMock{}, "local")
	_, err := svc.Create(context.Background(), file.CreateFileInput{Name: "a", Reader: strings.NewReader("x")})
	assertAppCode(t, err, 401)
}

func TestFileService_CreateRejectsEmptyName(t *testing.T) {
	svc := file.NewService(&fileRepoMock{}, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, file.CreateFileInput{Reader: strings.NewReader("x")})
	assertAppCode(t, err, 400)
}

func TestFileService_GetPrivateForbidden(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "someone", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "stranger")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_GetPublicAllowed(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "someone", Visibility: domain.VisibilityPublic}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "stranger")
	if _, err := svc.Get(ctx, idgen.NewID()); err != nil {
		t.Fatalf("public file should be readable: %v", err)
	}
}

func TestFileService_GetNotFound(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) { return nil, domain.ErrNotFound },
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "x")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 404)
}

func TestFileService_ListOnlyOwnFiles(t *testing.T) {
	svc := file.NewService(&fileRepoMock{}, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "me")
	_, err := svc.List(ctx, "someone-else", domain.Page{})
	assertAppCode(t, err, 403)
}

func TestFileService_ListDefaultsToActor(t *testing.T) {
	repo := &fileRepoMock{
		ListByOwnerFn: func(_ context.Context, owner string, _ domain.Page) ([]*domain.File, string, error) {
			if owner != "me" {
				t.Errorf("owner = %q, want me", owner)
			}
			return []*domain.File{{OwnerUID: "me"}}, "next", nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "me")
	list, err := svc.List(ctx, "", domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if list.NextCursor != "next" || len(list.Files) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestFileService_DeleteRequiresOwner(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "not-owner")
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_DeleteRemovesRowThenBlob(t *testing.T) {
	deletedRow, deletedBlob := false, false
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { deletedRow = true; return nil },
	}
	store := &storageMock{
		DeleteFn: func(_ context.Context, key string) error {
			if key != "k" {
				t.Errorf("blob key = %q", key)
			}
			deletedBlob = true
			return nil
		},
	}
	svc := file.NewService(repo, store, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if !deletedRow || !deletedBlob {
		t.Fatalf("deletedRow=%v deletedBlob=%v", deletedRow, deletedBlob)
	}
}

func TestFileService_DeleteSucceedsWhenBlobDeleteFails(t *testing.T) {
	rowDeleted := false
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
		DeleteFn: func(_ context.Context, _ domain.ID) error { rowDeleted = true; return nil },
	}
	store := &storageMock{
		DeleteFn: func(_ context.Context, _ string) error {
			return stderrors.New("unexpected storage error")
		},
	}
	svc := file.NewService(repo, store, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !rowDeleted {
		t.Fatal("repo.Delete was not called")
	}
}

func TestFileService_DownloadURLAppliesACL(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate, StorageKey: "k"}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "stranger")
	_, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	assertAppCode(t, err, 403)
}

func TestFileService_DownloadURLPresigns(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", StorageKey: "k"}, nil
		},
	}
	store := &storageMock{
		PresignGetFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://signed/" + key, nil
		},
	}
	svc := file.NewService(repo, store, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	url, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	if err != nil || url != "https://signed/k" {
		t.Fatalf("url=%q err=%v", url, err)
	}
}

func TestFileService_GetGranteeAllowed(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	shares := &fileShareRepoMock{
		IsSharedWithFn: func(_ context.Context, _ domain.ID, uid string) (bool, error) {
			return uid == "friend", nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "friend")
	if _, err := svc.Get(ctx, idgen.NewID()); err != nil {
		t.Fatalf("grantee should read shared private file: %v", err)
	}
}

func TestFileService_GetNonGranteePrivateForbidden(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate}, nil
		},
	}
	shares := &fileShareRepoMock{}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "stranger")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestFileService_DownloadURLGranteeAllowed(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Visibility: domain.VisibilityPrivate, StorageKey: "k"}, nil
		},
	}
	shares := &fileShareRepoMock{
		IsSharedWithFn: func(_ context.Context, _ domain.ID, _ string) (bool, error) { return true, nil },
	}
	store := &storageMock{
		PresignGetFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://signed/" + key, nil
		},
	}
	svc := file.NewService(repo, store, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "friend")
	url, err := svc.DownloadURL(ctx, idgen.NewID(), time.Minute)
	if err != nil || url != "https://signed/k" {
		t.Fatalf("grantee download url=%q err=%v", url, err)
	}
}

func TestFileService_ShareFileOwnerGrants(t *testing.T) {
	var granted []string
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	shares := &fileShareRepoMock{
		GrantFn: func(_ context.Context, _ domain.ID, owner string, uids []string) error {
			if owner != "owner" {
				t.Errorf("owner = %q", owner)
			}
			granted = uids
			return nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "owner")
	if err := svc.ShareFile(ctx, idgen.NewID(), []string{"a", "a", "owner", "b"}); err != nil {
		t.Fatal(err)
	}
	if len(granted) != 2 || granted[0] != "a" || granted[1] != "b" {
		t.Fatalf("granted = %v, want [a b]", granted)
	}
}

func TestFileService_ShareFileNonOwnerForbidden(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(&fileShareRepoMock{}))
	ctx := domain.WithActor(context.Background(), "not-owner")
	err := svc.ShareFile(ctx, idgen.NewID(), []string{"a"})
	assertAppCode(t, err, 403)
}

func TestFileService_ShareFileRejectsBlankUID(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	shares := &fileShareRepoMock{
		GrantFn: func(context.Context, domain.ID, string, []string) error {
			t.Fatal("Grant must not be called when a uid is blank")
			return nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "owner")
	err := svc.ShareFile(ctx, idgen.NewID(), []string{"a", "   "})
	assertAppCode(t, err, 400)
}

func TestFileService_UnshareFileOwnerRevokes(t *testing.T) {
	var revoked string
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	shares := &fileShareRepoMock{
		RevokeFn: func(_ context.Context, _ domain.ID, uid string) error { revoked = uid; return nil },
	}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "owner")
	if err := svc.UnshareFile(ctx, idgen.NewID(), "friend"); err != nil {
		t.Fatal(err)
	}
	if revoked != "friend" {
		t.Fatalf("revoked = %q, want friend", revoked)
	}
}

func TestFileService_UnshareFileNonOwnerForbidden(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local", file.WithFileShareRepo(&fileShareRepoMock{}))
	ctx := domain.WithActor(context.Background(), "not-owner")
	err := svc.UnshareFile(ctx, idgen.NewID(), "friend")
	assertAppCode(t, err, 403)
}

func TestFileService_ListSharedWithMe(t *testing.T) {
	shares := &fileShareRepoMock{
		ListFilesSharedWithFn: func(_ context.Context, uid string, _ domain.Page) ([]*domain.File, string, error) {
			if uid != "me" {
				t.Errorf("uid = %q, want me", uid)
			}
			return []*domain.File{{OwnerUID: "owner"}}, "next", nil
		},
	}
	svc := file.NewService(&fileRepoMock{}, &storageMock{}, "local", file.WithFileShareRepo(shares))
	ctx := domain.WithActor(context.Background(), "me")
	list, err := svc.ListSharedWithMe(ctx, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if list.NextCursor != "next" || len(list.Files) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestFileService_PresignUploadUnsupportedBackend(t *testing.T) {
	svc := file.NewService(&fileRepoMock{}, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.PresignUpload(ctx, file.PresignUploadInput{Name: "big.bin"}, time.Minute)
	assertAppCode(t, err, 400)
}

func TestFileService_PresignUploadHappyPath(t *testing.T) {
	store := &storageMock{
		NewKeyFn: func(owner string) string { return owner + "/2026/05/abc" },
		PresignPutFn: func(_ context.Context, key string, _ time.Duration) (string, error) {
			return "https://s3/put/" + key, nil
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "s3")
	ctx := domain.WithActor(context.Background(), "owner")
	res, err := svc.PresignUpload(ctx, file.PresignUploadInput{Name: "big.bin"}, time.Minute)
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
	store := &storageMock{
		PresignPutFn: func(context.Context, string, time.Duration) (string, error) {
			t.Fatal("PresignPut must not be called for an invalid request")
			return "", nil
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "s3")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.PresignUpload(ctx, file.PresignUploadInput{}, time.Minute)
	assertAppErrCode(t, err, "file.invalid")
}

func TestFileService_CompleteUploadPersistsRow(t *testing.T) {
	var persisted *domain.File
	repo := &fileRepoMock{CreateFn: func(_ context.Context, f *domain.File) error { persisted = f; return nil }}
	store := &storageMock{
		StatFn: func(_ context.Context, key string) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Key: key, Size: 42}, nil
		},
	}
	svc := file.NewService(repo, store, "s3")
	ctx := domain.WithActor(context.Background(), "owner")
	id := idgen.NewID()
	f, err := svc.CompleteUpload(ctx, file.CompleteUploadInput{
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
	store := &storageMock{
		StatFn: func(context.Context, string) (*storage.ObjectInfo, error) {
			t.Fatal("Stat must not run for a key the actor doesn't own")
			return nil, nil
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "s3")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.CompleteUpload(ctx, file.CompleteUploadInput{
		FileID: idgen.NewID(), StorageKey: "someone-else/2026/05/abc", Name: "x",
	})
	assertAppCode(t, err, 403)
}

func TestFileService_CompleteUploadRejectsOversized(t *testing.T) {
	var deleted string
	store := &storageMock{
		StatFn: func(_ context.Context, key string) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Key: key, Size: 100}, nil
		},
		DeleteFn: func(_ context.Context, key string) error { deleted = key; return nil },
	}
	svc := file.NewService(&fileRepoMock{}, store, "s3", file.WithMaxUploadBytes(10))
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.CompleteUpload(ctx, file.CompleteUploadInput{
		FileID: idgen.NewID(), StorageKey: "owner/2026/05/abc", Name: "big.bin",
	})
	assertAppErrCode(t, err, "file.too_large")
	if deleted != "owner/2026/05/abc" {
		t.Errorf("oversized object should be deleted, got %q", deleted)
	}
}

func TestFileService_CompleteUploadObjectMissing(t *testing.T) {
	store := &storageMock{
		StatFn: func(context.Context, string) (*storage.ObjectInfo, error) {
			return nil, storage.ErrNotFound
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "s3")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.CompleteUpload(ctx, file.CompleteUploadInput{
		FileID: idgen.NewID(), StorageKey: "owner/2026/05/abc", Name: "x",
	})
	assertAppErrCode(t, err, "file.not_uploaded")
}

func TestFileService_UpdateMetadataOwnerRenames(t *testing.T) {
	var gotFields map[string]any
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old.txt", Visibility: domain.VisibilityPrivate}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, fields map[string]any) error {
			gotFields = fields
			return nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	f, err := svc.UpdateMetadata(ctx, idgen.NewID(), file.UpdateFileMetadataInput{Name: strptr("new.txt")})
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
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
		UpdateMetadataFn: func(context.Context, domain.ID, map[string]any) error {
			t.Fatal("UpdateMetadata must not be called for a non-owner")
			return nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "stranger")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), file.UpdateFileMetadataInput{Name: strptr("x")})
	assertAppCode(t, err, 403)
}

func TestFileService_UpdateMetadataEmptyName(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), file.UpdateFileMetadataInput{Name: strptr("   ")})
	assertAppCode(t, err, 400)
}

func TestFileService_UpdateMetadataInvalidVisibility(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner"}, nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), file.UpdateFileMetadataInput{Visibility: visptr(domain.Visibility(99))})
	assertAppErrCode(t, err, "file.invalid_visibility")
}

func TestFileService_UpdateMetadataPartialChangesOnlyProvided(t *testing.T) {
	var gotFields map[string]any
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "keep.txt", Visibility: domain.VisibilityPrivate, Meta: map[string]any{"a": "1"}}, nil
		},
		UpdateMetadataFn: func(_ context.Context, _ domain.ID, fields map[string]any) error {
			gotFields = fields
			return nil
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	f, err := svc.UpdateMetadata(ctx, idgen.NewID(), file.UpdateFileMetadataInput{
		Visibility: visptr(domain.VisibilityPublic),
		Meta:       map[string]any{"b": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gotFields["name"]; ok {
		t.Error("name must not be in the update set when unprovided")
	}
	if gotFields["visibility"] != int16(domain.VisibilityPublic) {
		t.Errorf("visibility field = %v, want 1", gotFields["visibility"])
	}
	if f.Name != "keep.txt" {
		t.Errorf("name = %q, want keep.txt (unchanged)", f.Name)
	}
	if f.Meta["a"] != "1" || f.Meta["b"] != "2" {
		t.Errorf("meta = %+v, want merged {a,b}", f.Meta)
	}
}

func TestFileService_UpdateMetadataSoftDeletedNotFound(t *testing.T) {
	repo := &fileRepoMock{
		GetFn: func(_ context.Context, id domain.ID) (*domain.File, error) {
			return &domain.File{ID: id, OwnerUID: "owner", Name: "old"}, nil
		},
		UpdateMetadataFn: func(context.Context, domain.ID, map[string]any) error {
			return domain.ErrNotFound
		},
	}
	svc := file.NewService(repo, &storageMock{}, "local")
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.UpdateMetadata(ctx, idgen.NewID(), file.UpdateFileMetadataInput{Name: strptr("new")})
	assertAppCode(t, err, 404)
}

func TestFileService_CreateRejectsTooLargeByDeclaredSize(t *testing.T) {
	store := &storageMock{
		PutFn: func(context.Context, string, string, io.Reader) (string, int64, error) {
			t.Fatal("storage.Put must not be called when the declared size exceeds the limit")
			return "", 0, nil
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "local", file.WithMaxUploadBytes(5))
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, file.CreateFileInput{Name: "big.bin", Size: 10, Reader: strings.NewReader("0123456789")})
	assertAppErrCode(t, err, "file.too_large")
}

func TestFileService_CreateCapsOversizedStream(t *testing.T) {
	store := &storageMock{
		PutFn: func(_ context.Context, _, _ string, r io.Reader) (string, int64, error) {
			n, err := io.Copy(io.Discard, r)
			return "k", n, err
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "local", file.WithMaxUploadBytes(5))
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, file.CreateFileInput{Name: "big.bin", Reader: strings.NewReader("0123456789")})
	assertAppErrCode(t, err, "file.too_large")
}

func TestFileService_CreateRejectsUnsupportedType(t *testing.T) {
	store := &storageMock{
		PutFn: func(context.Context, string, string, io.Reader) (string, int64, error) {
			t.Fatal("storage.Put must not be called for a disallowed type")
			return "", 0, nil
		},
	}
	svc := file.NewService(&fileRepoMock{}, store, "local",
		file.WithAllowedMimeTypes([]string{"image/png", "image/jpeg"}))
	ctx := domain.WithActor(context.Background(), "owner")
	_, err := svc.Create(ctx, file.CreateFileInput{Name: "x.exe", MimeType: "application/octet-stream", Reader: strings.NewReader("x")})
	assertAppErrCode(t, err, "file.unsupported_type")
}

func TestFileService_CreateAllowsWithinLimits(t *testing.T) {
	repo := &fileRepoMock{CreateFn: func(context.Context, *domain.File) error { return nil }}
	store := &storageMock{
		PutFn: func(_ context.Context, _, _ string, r io.Reader) (string, int64, error) {
			n, _ := io.Copy(io.Discard, r)
			return "k", n, nil
		},
	}
	svc := file.NewService(repo, store, "local",
		file.WithMaxUploadBytes(1024),
		file.WithAllowedMimeTypes([]string{"text/plain"}))
	ctx := domain.WithActor(context.Background(), "owner")
	f, err := svc.Create(ctx, file.CreateFileInput{
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
	store := &storageMock{
		PutFn: func(_ context.Context, _, _ string, r io.Reader) (string, int64, error) {
			n, _ := io.Copy(io.Discard, r)
			return "fresh-key", n, nil
		},
		DeleteFn: func(_ context.Context, key string) error { deletedKey = key; return nil },
	}
	var persisted *domain.File
	repo := &fileRepoMock{
		FindByChecksumFn: func(_ context.Context, owner, _ string) (*domain.File, error) {
			return &domain.File{OwnerUID: owner, StorageKey: "existing-key"}, nil
		},
		CreateFn: func(_ context.Context, f *domain.File) error { persisted = f; return nil },
	}
	svc := file.NewService(repo, store, "local")
	ctx := domain.WithActor(context.Background(), "owner")

	f, err := svc.Create(ctx, file.CreateFileInput{Name: "dup.bin", Reader: strings.NewReader("identical")})
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
