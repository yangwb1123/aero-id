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
}

type MessageService interface {
	Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error)
	Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error)
	MarkRead(ctx context.Context, id domain.ID) error
	UnreadCount(ctx context.Context) (int64, error)
}

type CommentService interface {
	Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error)
	List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error)
	Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error)
	Delete(ctx context.Context, id domain.ID) error
	Like(ctx context.Context, id domain.ID) (*ReactionResult, error)
	Unlike(ctx context.Context, id domain.ID) (*ReactionResult, error)
	Report(ctx context.Context, id domain.ID, reason string) error
	Hide(ctx context.Context, id domain.ID) error
	Unhide(ctx context.Context, id domain.ID) error
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
