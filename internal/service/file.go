package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

type fileService struct {
	files       repository.FileRepository
	shares      repository.FileShareRepository // nil => explicit sharing is inactive
	store       storage.FileStorage
	storageType string // "local" | "s3", recorded on each row for later routing
	maxBytes    int64  // 0 = unlimited
	allowedMime map[string]bool
	audit       AuditLogger // nil => audit is inactive
}

// FileOption configures optional fileService policy (upload limits, type allow-list).
type FileOption func(*fileService)

// WithMaxUploadBytes caps the size of an uploaded file. Zero (the default) means no limit.
func WithMaxUploadBytes(n int64) FileOption {
	return func(s *fileService) { s.maxBytes = n }
}

// WithAllowedMimeTypes restricts uploads to the given media types (compared on the
// base type, case-insensitively). An empty list (the default) allows any type.
func WithAllowedMimeTypes(types []string) FileOption {
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

// WithFileAuditLogger wires an AuditLogger into the file service.
func WithFileAuditLogger(a AuditLogger) FileOption {
	return func(s *fileService) { s.audit = a }
}

// WithFileShareRepo enables explicit per-user file sharing. When unset (the default),
// the share methods report the feature unavailable and read access is unchanged.
func WithFileShareRepo(shares repository.FileShareRepository) FileOption {
	return func(s *fileService) { s.shares = shares }
}

func NewFileService(files repository.FileRepository, store storage.FileStorage, storageType string, opts ...FileOption) FileService {
	s := &fileService{files: files, store: store, storageType: storageType}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if in.Reader == nil {
		return nil, apperr.BadRequest("file.invalid", "file content is required")
	}
	if s.maxBytes > 0 && in.Size > s.maxBytes {
		return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
	}
	if !s.mimeAllowed(in.MimeType) {
		return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
	}

	// Cap the stream as defense-in-depth (the backend can never write past the limit
	// even if the declared size lies) and hash it in the same pass so we can record a
	// content checksum and deduplicate identical uploads.
	var reader io.Reader = in.Reader
	if s.maxBytes > 0 {
		reader = &cappedReader{r: in.Reader, left: s.maxBytes}
	}
	hasher := sha256.New()
	reader = io.TeeReader(reader, hasher)

	key, size, err := s.store.Put(ctx, actor, in.Name, reader)
	if err != nil {
		if stderrors.Is(err, errFileTooLarge) {
			return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
		}
		return nil, apperr.Internal("file.store_failed", "failed to store file").Wrap(err)
	}
	checksum := hex.EncodeToString(hasher.Sum(nil))

	// Deduplicate: if this owner already has an identical file, reuse its blob and drop
	// the copy we just wrote. `reused` guards cleanup so we never delete a shared blob.
	reused := false
	if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
		if existing.StorageKey != key {
			_ = s.store.Delete(ctx, key)
			key = existing.StorageKey
		}
		reused = true
	} else if !stderrors.Is(ferr, repository.ErrNotFound) {
		_ = s.store.Delete(ctx, key)
		return nil, apperr.Internal("file.dedup_failed", "failed to check for an existing file").Wrap(ferr)
	}

	now := time.Now().UTC()
	f := &domain.File{
		ID:          idgen.NewID(),
		OwnerUID:    actor,
		Name:        in.Name,
		Size:        size,
		MimeType:    in.MimeType,
		StorageKey:  key,
		StorageType: s.storageType,
		Checksum:    checksum,
		Visibility:  in.Visibility,
		Meta:        in.Meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.files.Create(ctx, f); err != nil {
		if !reused {
			_ = s.store.Delete(ctx, key) // only clean up a blob we exclusively own
		}
		return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
	}
	return f, nil
}

func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return nil, apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	allowed, err := s.canRead(ctx, actor, f)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, apperr.Forbidden("file.forbidden", "not allowed to access this file")
	}
	return f, nil
}

// canRead reports whether actor may read f: the owner and public files are always
// allowed; otherwise, if explicit sharing is enabled, an actor the file was shared
// with is allowed. Returns an *AppError if the share lookup fails.
func (s *fileService) canRead(ctx context.Context, actor string, f *domain.File) (bool, error) {
	if canReadFile(actor, f) {
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

func (s *fileService) List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if ownerUID == "" {
		ownerUID = actor
	}
	if ownerUID != actor {
		return nil, apperr.Forbidden("file.forbidden", "can only list your own files")
	}
	files, next, err := s.files.ListByOwner(ctx, ownerUID, page)
	if err != nil {
		return nil, apperr.Internal("file.list_failed", "failed to list files").Wrap(err)
	}
	return &FileList{Files: files, NextCursor: next}, nil
}

func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return apperr.Forbidden("file.forbidden", "only the owner can delete this file")
	}
	// Soft-delete the authoritative row first, then remove the blob.
	if err := s.files.Delete(ctx, id); err != nil {
		return apperr.Internal("file.delete_failed", "failed to delete file").Wrap(err)
	}
	// Blob removal is dedup-safe and best-effort: drop the physical object only once no
	// other live file references the same storage key, and never fail the request over
	// it (the row is already gone; any orphan is left for a future GC sweep).
	if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
		_ = s.store.Delete(ctx, f.StorageKey)
	}
	return nil
}

func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	f, err := s.Get(ctx, id) // reuses the read ACL above
	if err != nil {
		return "", err
	}
	url, err := s.store.PresignGet(ctx, f.StorageKey, ttl)
	if err != nil {
		return "", apperr.Internal("file.presign_failed", "failed to presign download URL").Wrap(err)
	}
	return url, nil
}

// PresignUpload allocates a file id + owner-scoped storage key and returns a presigned
// PUT URL the client uploads to directly, bypassing this process. No row is written
// yet — the row is created on CompleteUpload, so an abandoned presign leaves no
// dangling state (at worst an orphan object the backend lifecycle can reap). Backends
// that cannot presign (local) surface as a BadRequest.
func (s *fileService) PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if !s.mimeAllowed(in.MimeType) {
		return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
	}
	key := s.store.NewKey(actor)
	url, err := s.store.PresignPut(ctx, key, ttl)
	if stderrors.Is(err, storage.ErrNotSupported) {
		return nil, apperr.BadRequest("file.presign_unsupported", "direct upload is not supported by this storage backend")
	}
	if err != nil {
		return nil, apperr.Internal("file.presign_failed", "failed to presign upload URL").Wrap(err)
	}
	return &PresignUploadResult{FileID: idgen.NewID(), UploadURL: url, StorageKey: key}, nil
}

// CompleteUpload finalizes a presigned direct upload. The actor must own the storage
// key (its owner prefix matches the actor), the object must exist, and it must respect
// the size limit. Content-checksum dedup is intentionally SKIPPED for direct uploads:
// the bytes never pass through this process, so we cannot hash them without an extra
// download. The row is persisted with an empty checksum.
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.FileID == (domain.ID{}) {
		return nil, apperr.BadRequest("file.invalid", "file id is required")
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if !s.mimeAllowed(in.MimeType) {
		return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
	}
	// The key is owner-scoped (built by NewKey as "<owner>/..."); refuse to finalize a
	// key that isn't prefixed with the actor's uid so a client can't claim another
	// owner's object.
	if !strings.HasPrefix(in.StorageKey, actor+"/") {
		return nil, apperr.Forbidden("file.forbidden", "not allowed to finalize this upload")
	}
	info, err := s.store.Stat(ctx, in.StorageKey)
	if stderrors.Is(err, storage.ErrNotFound) {
		return nil, apperr.BadRequest("file.not_uploaded", "no object found at the upload key")
	}
	if err != nil {
		return nil, apperr.Internal("file.stat_failed", "failed to stat uploaded object").Wrap(err)
	}
	if s.maxBytes > 0 && info.Size > s.maxBytes {
		_ = s.store.Delete(ctx, in.StorageKey) // reject + clean up the oversized object
		return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
	}

	now := time.Now().UTC()
	f := &domain.File{
		ID:          in.FileID,
		OwnerUID:    actor,
		Name:        in.Name,
		Size:        info.Size,
		MimeType:    in.MimeType,
		StorageKey:  in.StorageKey,
		StorageType: s.storageType,
		Visibility:  in.Visibility,
		Meta:        in.Meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.files.Create(ctx, f); err != nil {
		_ = s.store.Delete(ctx, in.StorageKey) // direct uploads aren't deduped, so this blob is exclusively ours
		return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
	}
	return f, nil
}

// UpdateMetadata lets the file's owner change mutable metadata without re-uploading.
// Only the provided (non-nil) fields are applied; a provided meta map is merged into
// the existing meta. Validation: a provided name must be non-blank (BadRequest); a
// provided visibility must be a known value (BadRequest); a soft-deleted/missing file
// is NotFound; non-owners get Forbidden. updated_at is bumped on every change.
func (s *fileService) UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.ownedFile(ctx, id, actor)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, apperr.BadRequest("file.invalid", "name must not be blank")
		}
		fields["name"] = name
		f.Name = name
	}
	if in.Visibility != nil {
		v := *in.Visibility
		if v != domain.VisibilityPrivate && v != domain.VisibilityPublic {
			return nil, apperr.BadRequest("file.invalid_visibility", "visibility must be 0 (private) or 1 (public)")
		}
		fields["visibility"] = int16(v)
		f.Visibility = v
	}
	if in.Meta != nil {
		merged := make(map[string]any, len(f.Meta)+len(in.Meta))
		for k, v := range f.Meta {
			merged[k] = v
		}
		for k, v := range in.Meta {
			merged[k] = v
		}
		fields["meta"] = merged
		f.Meta = merged
	}
	if len(fields) == 0 {
		return f, nil // nothing requested; return the current state unchanged
	}

	now := time.Now().UTC()
	fields["updated_at"] = now
	if err := s.files.UpdateMetadata(ctx, id, fields); err != nil {
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("file.not_found", "file not found")
		}
		return nil, apperr.Internal("file.update_failed", "failed to update file").Wrap(err)
	}
	f.UpdatedAt = now
	return f, nil
}

// ShareFile grants read access on a file the caller owns to each grantee uid. It is
// idempotent and rejects blank uids and self-grants (the owner already has access).
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if s.shares == nil {
		return apperr.Internal("file.sharing_unavailable", "file sharing is not enabled")
	}
	f, err := s.ownedFile(ctx, id, actor)
	if err != nil {
		return err
	}
	// Validate and de-duplicate the grantee set: reject blank uids; silently drop a
	// self-grant since the owner already has access.
	seen := make(map[string]bool, len(granteeUIDs))
	grantees := make([]string, 0, len(granteeUIDs))
	for _, g := range granteeUIDs {
		g = strings.TrimSpace(g)
		if g == "" {
			return apperr.BadRequest("file.invalid_grantee", "grantee uid must not be blank")
		}
		if g == f.OwnerUID || seen[g] {
			continue
		}
		seen[g] = true
		grantees = append(grantees, g)
	}
	if len(grantees) == 0 {
		return nil
	}
	if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil {
		return apperr.Internal("file.share_failed", "failed to share file").Wrap(err)
	}
	if s.audit != nil {
		for _, g := range grantees {
			_ = s.audit.Log(ctx, &domain.AuditEvent{
				ActorUID:    actor,
				Action:      domain.AuditFileShareGrant,
				SubjectType: "file",
				SubjectID:   id.String(),
				TargetUID:   g,
			})
		}
	}
	return nil
}

// UnshareFile revokes a single grantee's access to a file the caller owns. Revoking a
// grant that does not exist is a no-op.
func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if s.shares == nil {
		return apperr.Internal("file.sharing_unavailable", "file sharing is not enabled")
	}
	if strings.TrimSpace(granteeUID) == "" {
		return apperr.BadRequest("file.invalid_grantee", "grantee uid must not be blank")
	}
	if _, err := s.ownedFile(ctx, id, actor); err != nil {
		return err
	}
	if err := s.shares.Revoke(ctx, id, granteeUID); err != nil {
		return apperr.Internal("file.unshare_failed", "failed to unshare file").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditFileShareRevoke,
			SubjectType: "file",
			SubjectID:   id.String(),
			TargetUID:   granteeUID,
		})
	}
	return nil
}

// ListSharedWithMe lists files other owners have shared with the caller.
func (s *fileService) ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.shares == nil {
		return &FileList{Files: []*domain.File{}}, nil
	}
	files, next, err := s.shares.ListFilesSharedWith(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("file.list_failed", "failed to list shared files").Wrap(err)
	}
	return &FileList{Files: files, NextCursor: next}, nil
}

// ownedFile loads a file and asserts the actor is its owner, translating raw errors
// to the same NotFound/Forbidden AppErrors the other owner-only operations return.
func (s *fileService) ownedFile(ctx context.Context, id domain.ID, actor string) (*domain.File, error) {
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
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

// mimeAllowed reports whether the content type passes the configured allow-list.
// With no allow-list configured, every type is accepted.
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

// errFileTooLarge is returned by cappedReader once the configured byte limit is exceeded.
var errFileTooLarge = stderrors.New("file exceeds maximum allowed size")

// cappedReader wraps an io.Reader and fails with errFileTooLarge as soon as more
// than `left` bytes have been read, so an oversized upload is never fully written.
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
