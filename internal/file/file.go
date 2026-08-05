package file

import (
	"context"
	stderrors "errors"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/storage"
)

type File = domain.File

type Repository interface {
	Get(ctx context.Context, id domain.ID) (*File, error)
	ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*File, string, error)
	Create(ctx context.Context, f *File) error
	Delete(ctx context.Context, id domain.ID) error
	FindByChecksum(ctx context.Context, ownerUID, checksum string) (*File, error)
	CountByStorageKey(ctx context.Context, storageKey string) (int64, error)
	UpdateMetadata(ctx context.Context, id domain.ID, fields map[string]any) error
	ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*File, error)
	HardDelete(ctx context.Context, ids []domain.ID) error
	CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error)
	SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*File, error)
}

type ShareRepository interface {
	Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error
	Revoke(ctx context.Context, fileID domain.ID, granteeUID string) error
	IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error)
	ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*File, string, error)
}

type CreateFileInput struct {
	Name       string
	MimeType   string
	Size       int64
	Visibility domain.Visibility
	Reader     io.Reader
	Meta       map[string]any
}

type FileList struct {
	Files      []*File
	NextCursor string
}

type UpdateFileMetadataInput struct {
	Name       *string
	Visibility *domain.Visibility
	Meta       map[string]any
}

type PresignUploadInput struct {
	Name       string
	MimeType   string
	Visibility domain.Visibility
	Meta       map[string]any
}

type PresignUploadResult struct {
	FileID     domain.ID
	UploadURL  string
	StorageKey string
}

type CompleteUploadInput struct {
	FileID     domain.ID
	StorageKey string
	Name       string
	MimeType   string
	Visibility domain.Visibility
	Meta       map[string]any
}

type Service interface {
	Create(ctx context.Context, in CreateFileInput) (*File, error)
	Get(ctx context.Context, id domain.ID) (*File, error)
	List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error)
	Delete(ctx context.Context, id domain.ID) error
	DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error)
	ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error
	UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error
	ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error)
	PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error)
	CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error)
	UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*File, error)
}

type fileService struct {
	files       Repository
	shares      ShareRepository
	store       storage.FileStorage
	storageType string
	maxBytes    int64
	allowedMime map[string]bool
	audit       domain.AuditLogger
}

type Option func(*fileService)

func WithMaxUploadBytes(n int64) Option {
	return func(s *fileService) { s.maxBytes = n }
}

func WithAllowedMimeTypes(types []string) Option {
	return func(s *fileService) {
		if len(types) == 0 {
			return
		}
		m := make(map[string]bool, len(types))
		for _, t := range types {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				m[t] = true
			}
		}
		s.allowedMime = m
	}
}

func WithAuditLogger(a domain.AuditLogger) Option {
	return func(s *fileService) { s.audit = a }
}

func WithFileShareRepo(shares ShareRepository) Option {
	return func(s *fileService) { s.shares = shares }
}

func NewService(files Repository, store storage.FileStorage, storageType string, opts ...Option) Service {
	s := &fileService{files: files, store: store, storageType: storageType}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *fileService) mimeAllowed(contentType string) bool {
	if len(s.allowedMime) == 0 {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return s.allowedMime[strings.ToLower(mt)]
}

var errFileTooLarge = stderrors.New("file exceeds maximum allowed size")

type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, errFileTooLarge
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errFileTooLarge
	}
	return n, err
}

func (s *fileService) ownedFile(ctx context.Context, id domain.ID, actor string) (*File, error) {
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return nil, apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return nil, apperr.Forbidden("file.forbidden", "only the owner can share this file")
	}
	return f, nil
}

func (s *fileService) canRead(ctx context.Context, actor string, f *File) (bool, error) {
	if domain.CanReadFile(actor, f) {
		return true, nil
	}
	if s.shares == nil {
		return false, nil
	}
	shared, err := s.shares.IsSharedWith(ctx, f.ID, actor)
	if err != nil {
		return false, apperr.Internal("file.share_lookup_failed", "failed to check file sharing").Wrap(err)
	}
	return shared, nil
}
