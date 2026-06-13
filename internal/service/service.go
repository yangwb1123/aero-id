// Package service holds aero-id's business logic, shared by the HTTP and gRPC
// transports. It depends on repository/storage interfaces plus a narrow
// UserDirectory; it is the boundary that converts raw errors into *errors.AppError.
package service

import (
	"context"
	"io"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/sso"
)

// ctxKey is an unexported context key type so callers cannot collide with us.
type ctxKey int

const actorKey ctxKey = iota

// WithActor returns a child context carrying the authenticated uid. The HTTP auth
// middleware and the gRPC auth interceptor call this after validating the token
// with SSO. The actor is never written to business tables — it is only used for
// authorization decisions and logging.
func WithActor(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, actorKey, uid)
}

// ActorFrom extracts the authenticated uid. ok is false when absent or empty.
func ActorFrom(ctx context.Context) (string, bool) {
	uid, _ := ctx.Value(actorKey).(string)
	return uid, uid != ""
}

// AuditLogger records a single audit event. Implementations must be non-blocking
// from the caller's perspective; a failed log must not fail the primary operation.
type AuditLogger interface {
	Log(ctx context.Context, e *domain.AuditEvent) error
}

// UserDirectory is the subset of the SSO client the service uses to enrich list
// responses with author/sender display info. *sso.Client satisfies it.
type UserDirectory interface {
	GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}

// ---- Input/output DTOs ----

type CreateFileInput struct {
	Name       string
	MimeType   string
	Size       int64 // declared content length; used to reject oversized uploads early
	Visibility domain.Visibility
	Reader     io.Reader
	Meta       map[string]any
}

type FileList struct {
	Files      []*domain.File
	NextCursor string
}

// UpdateFileMetadataInput carries the owner-editable file metadata. Only non-nil
// fields are applied (field-mask style); a nil field is left unchanged. Meta, when
// provided, is merged into the existing meta (set keys overwrite, others remain).
type UpdateFileMetadataInput struct {
	Name       *string
	Visibility *domain.Visibility
	Meta       map[string]any
}

// PresignUploadInput requests a presigned direct-to-backend upload. Name/MimeType/
// Visibility/Meta are carried through to the row created at finalize time.
type PresignUploadInput struct {
	Name       string
	MimeType   string
	Visibility domain.Visibility
	Meta       map[string]any
}

// PresignUploadResult is returned to a client that will PUT the object directly to
// the storage backend, then call CompleteUpload with the same FileID.
type PresignUploadResult struct {
	FileID     domain.ID
	UploadURL  string
	StorageKey string
}

// CompleteUploadInput finalizes a presigned direct upload: the client has PUT the
// object to StorageKey under the FileID handed back by PresignUpload. Name/MimeType/
// Visibility/Meta describe the row to persist (no row exists until finalize).
type CompleteUploadInput struct {
	FileID     domain.ID
	StorageKey string
	Name       string
	MimeType   string
	Visibility domain.Visibility
	Meta       map[string]any
}

type SendMessageInput struct {
	ToUID   string
	Kind    domain.MessageKind
	Content string
	FileID  *domain.ID
	Meta    map[string]any
}

type MessageList struct {
	Messages   []*domain.Message
	NextCursor string
	Users      map[string]sso.User
}

type CreateCommentInput struct {
	TargetType domain.TargetType
	TargetID   domain.ID
	ParentID   *domain.ID
	Content    string
}

type CommentList struct {
	Comments   []*domain.Comment
	NextCursor string
	Users      map[string]sso.User
}

// ReportList is a page of moderation reports against a comment, plus resolved
// display info for the reporters.
type ReportList struct {
	Reports    []*domain.CommentReport
	NextCursor string
	Users      map[string]sso.User
}

// NotificationList is the recipient feed plus resolved actor display info.
type NotificationList struct {
	Notifications []*domain.Notification
	NextCursor    string
	Users         map[string]sso.User
}

// BlockList is a page of uids the caller has blocked.
type BlockList struct {
	UIDs       []string
	NextCursor string
}

// ReactionResult is the outcome of a like/unlike: the caller's resulting like state
// and the comment's updated like count.
type ReactionResult struct {
	Liked     bool
	LikeCount int
}

// ---- Service interfaces (implemented in file.go / message.go / comment.go) ----

type FileService interface {
	Create(ctx context.Context, in CreateFileInput) (*domain.File, error)
	Get(ctx context.Context, id domain.ID) (*domain.File, error)
	List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error)
	Delete(ctx context.Context, id domain.ID) error
	DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error)
	// ShareFile grants read access on a file the caller owns to each grantee uid.
	ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error
	// UnshareFile revokes a single grantee's access to a file the caller owns.
	UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error
	// ListSharedWithMe lists files other owners have shared with the caller.
	ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error)
	// PresignUpload allocates a file id + owner-scoped storage key and returns a
	// presigned URL the client PUTs the object to directly (S3 only). Unsupported
	// backends (local) return a BadRequest.
	PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error)
	// CompleteUpload finalizes a presigned direct upload by Stat-ing the object and
	// persisting the file row. Dedup is skipped (no checksum without downloading).
	CompleteUpload(ctx context.Context, in CompleteUploadInput) (*domain.File, error)
	// UpdateMetadata lets the file's owner change mutable metadata (name, visibility,
	// and/or a merged meta map) without re-uploading. Owner-only.
	UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*domain.File, error)
}

type MessageService interface {
	Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
	// SendSystem posts a server-emitted system-kind message. Internal callers
	// only; it bypasses the user "cannot send system" rule.
	SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error)
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error)
	Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error)
	MarkRead(ctx context.Context, id domain.ID) error
	// MarkConversationRead marks every message from withUID to the caller as read.
	MarkConversationRead(ctx context.Context, withUID string) error
	UnreadCount(ctx context.Context) (int64, error)
	// Edit rewrites a message's content; only the original sender may edit, the
	// message must be a non-deleted, non-system message, and content must be non-empty.
	Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error)
	// Delete soft-deletes a message; only the original sender may delete. Idempotent.
	Delete(ctx context.Context, id domain.ID) error
	// ReactToMessage records a reaction by the authenticated actor. Idempotent.
	ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error
	// UnreactMessage removes the authenticated actor's reaction. Idempotent.
	UnreactMessage(ctx context.Context, messageID domain.ID, actorUID string) error
}

type NotificationService interface {
	List(ctx context.Context, page domain.Page) (*NotificationList, error)
	MarkRead(ctx context.Context, id domain.ID) error
	MarkAllRead(ctx context.Context) error
	UnreadCount(ctx context.Context) (int64, error)
}

// BlockService is the safety primitive: the caller (blocker) can block/unblock
// other uids and list whom they have blocked. Blocking is directional and private
// to the blocker — A blocking B stops B's messages reaching A and suppresses
// notifications about B's actions in A's feed.
type BlockService interface {
	// Block records that the caller has blocked uid. Idempotent.
	Block(ctx context.Context, uid string) error
	// Unblock removes the caller's block on uid. Idempotent.
	Unblock(ctx context.Context, uid string) error
	// ListBlocked lists uids the caller has blocked, newest first.
	ListBlocked(ctx context.Context, page domain.Page) (*BlockList, error)
}

type CommentService interface {
	Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error)
	List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error)
	Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error)
	// Edit rewrites the content of a comment the caller authored.
	Edit(ctx context.Context, id domain.ID, newContent string) (*domain.Comment, error)
	Delete(ctx context.Context, id domain.ID) error
	Like(ctx context.Context, id domain.ID) (*ReactionResult, error)
	Unlike(ctx context.Context, id domain.ID) (*ReactionResult, error)
	Report(ctx context.Context, id domain.ID, reason string) error
	Hide(ctx context.Context, id domain.ID) error
	Unhide(ctx context.Context, id domain.ID) error
	// ListReports returns the moderation queue for a comment, scoped to its author.
	ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) (*ReportList, error)
	// ResolveReport triages a report (resolve/dismiss); only the reported comment's
	// author may do so.
	ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus) error
}

// ---- shared helpers (unexported; reused across the three service impls) ----

// mustActor returns the authenticated uid or an Unauthorized AppError.
func mustActor(ctx context.Context) (string, error) {
	uid, ok := ActorFrom(ctx)
	if !ok {
		return "", apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
	}
	return uid, nil
}

// canReadFile is the file read ACL: public files are world-readable; private ones
// only by their owner.
func canReadFile(actor string, f *domain.File) bool {
	return f.Visibility == domain.VisibilityPublic || f.OwnerUID == actor
}

// canReadMessage is the message read ACL: only the two participants.
func canReadMessage(actor string, m *domain.Message) bool {
	return m.FromUID == actor || m.ToUID == actor
}

// enrichUsers resolves display info for a set of uids via the directory. An empty
// input short-circuits without an RPC. The underlying cache deduplicates.
func enrichUsers(ctx context.Context, dir UserDirectory, uids []string) (map[string]sso.User, error) {
	if len(uids) == 0 {
		return map[string]sso.User{}, nil
	}
	return dir.GetUsers(ctx, uids)
}
