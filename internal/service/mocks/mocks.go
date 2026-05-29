// Package mocks provides function-field test doubles for the service layer's
// repository, storage, and SSO dependencies. Each method delegates to a field;
// a test sets only the fields the case under test actually exercises.
package mocks

import (
	"context"
	"io"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

type FileRepo struct {
	CreateFn            func(ctx context.Context, f *domain.File) error
	GetFn               func(ctx context.Context, id domain.ID) (*domain.File, error)
	ListByOwnerFn       func(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error)
	DeleteFn            func(ctx context.Context, id domain.ID) error
	FindByChecksumFn    func(ctx context.Context, ownerUID, checksum string) (*domain.File, error)
	CountByStorageKeyFn func(ctx context.Context, storageKey string) (int64, error)
}

func (m *FileRepo) Create(ctx context.Context, f *domain.File) error { return m.CreateFn(ctx, f) }
func (m *FileRepo) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return m.GetFn(ctx, id)
}
func (m *FileRepo) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error) {
	return m.ListByOwnerFn(ctx, ownerUID, page)
}
func (m *FileRepo) Delete(ctx context.Context, id domain.ID) error { return m.DeleteFn(ctx, id) }

// FindByChecksum and CountByStorageKey default to "feature inactive" (no dedup match /
// no other references) when their Fn is unset, so tests that don't exercise dedup need
// not wire them.
func (m *FileRepo) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*domain.File, error) {
	if m.FindByChecksumFn == nil {
		return nil, repository.ErrNotFound
	}
	return m.FindByChecksumFn(ctx, ownerUID, checksum)
}

func (m *FileRepo) CountByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	if m.CountByStorageKeyFn == nil {
		return 0, nil
	}
	return m.CountByStorageKeyFn(ctx, storageKey)
}

type FileShareRepo struct {
	GrantFn               func(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error
	RevokeFn              func(ctx context.Context, fileID domain.ID, granteeUID string) error
	ListGranteesFn        func(ctx context.Context, fileID domain.ID) ([]string, error)
	ListFilesSharedWithFn func(ctx context.Context, uid string, page domain.Page) ([]*domain.File, string, error)
	IsSharedWithFn        func(ctx context.Context, fileID domain.ID, uid string) (bool, error)
}

// The defaults model "sharing inactive": no grants exist, lists are empty, and
// IsSharedWith is false, so a fileService wired with this mock behaves exactly as
// one with no share repo until a test sets the relevant Fn.
func (m *FileShareRepo) Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error {
	if m.GrantFn == nil {
		return nil
	}
	return m.GrantFn(ctx, fileID, ownerUID, granteeUIDs)
}

func (m *FileShareRepo) Revoke(ctx context.Context, fileID domain.ID, granteeUID string) error {
	if m.RevokeFn == nil {
		return nil
	}
	return m.RevokeFn(ctx, fileID, granteeUID)
}

func (m *FileShareRepo) ListGrantees(ctx context.Context, fileID domain.ID) ([]string, error) {
	if m.ListGranteesFn == nil {
		return nil, nil
	}
	return m.ListGranteesFn(ctx, fileID)
}

func (m *FileShareRepo) ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*domain.File, string, error) {
	if m.ListFilesSharedWithFn == nil {
		return nil, "", nil
	}
	return m.ListFilesSharedWithFn(ctx, uid, page)
}

func (m *FileShareRepo) IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error) {
	if m.IsSharedWithFn == nil {
		return false, nil
	}
	return m.IsSharedWithFn(ctx, fileID, uid)
}

type MessageRepo struct {
	CreateFn           func(ctx context.Context, m *domain.Message) error
	GetFn              func(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInboxFn        func(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversationFn func(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	MarkReadFn         func(ctx context.Context, id domain.ID, at time.Time) error
	CountUnreadFn      func(ctx context.Context, toUID string) (int64, error)
}

func (m *MessageRepo) Create(ctx context.Context, msg *domain.Message) error {
	return m.CreateFn(ctx, msg)
}
func (m *MessageRepo) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return m.GetFn(ctx, id)
}
func (m *MessageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
	return m.ListInboxFn(ctx, toUID, page, unreadOnly)
}
func (m *MessageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
	return m.ListConversationFn(ctx, uidA, uidB, page)
}
func (m *MessageRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	return m.MarkReadFn(ctx, id, at)
}
func (m *MessageRepo) CountUnread(ctx context.Context, toUID string) (int64, error) {
	return m.CountUnreadFn(ctx, toUID)
}

type CommentRepo struct {
	CreateFn              func(ctx context.Context, c *domain.Comment) error
	CreateWithReplyBumpFn func(ctx context.Context, c *domain.Comment) error
	GetFn                 func(ctx context.Context, id domain.ID) (*domain.Comment, error)
	ListByTargetFn        func(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	ListRepliesFn         func(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error)
	IncReplyCountFn       func(ctx context.Context, parentID domain.ID) error
	SoftDeleteFn          func(ctx context.Context, id domain.ID) error
	LikeFn                func(ctx context.Context, commentID domain.ID, uid string) (int, error)
	UnlikeFn              func(ctx context.Context, commentID domain.ID, uid string) (int, error)
}

func (m *CommentRepo) Create(ctx context.Context, c *domain.Comment) error { return m.CreateFn(ctx, c) }
func (m *CommentRepo) CreateWithReplyBump(ctx context.Context, c *domain.Comment) error {
	return m.CreateWithReplyBumpFn(ctx, c)
}
func (m *CommentRepo) Get(ctx context.Context, id domain.ID) (*domain.Comment, error) {
	return m.GetFn(ctx, id)
}
func (m *CommentRepo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	return m.ListByTargetFn(ctx, t, target, page)
}
func (m *CommentRepo) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
	return m.ListRepliesFn(ctx, rootID, page)
}
func (m *CommentRepo) IncReplyCount(ctx context.Context, parentID domain.ID) error {
	return m.IncReplyCountFn(ctx, parentID)
}
func (m *CommentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	return m.SoftDeleteFn(ctx, id)
}
func (m *CommentRepo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
	return m.LikeFn(ctx, commentID, uid)
}
func (m *CommentRepo) Unlike(ctx context.Context, commentID domain.ID, uid string) (int, error) {
	return m.UnlikeFn(ctx, commentID, uid)
}

type Storage struct {
	PutFn        func(ctx context.Context, ownerUID, name string, r io.Reader) (string, int64, error)
	GetFn        func(ctx context.Context, key string) (io.ReadCloser, error)
	DeleteFn     func(ctx context.Context, key string) error
	PresignGetFn func(ctx context.Context, key string, ttl time.Duration) (string, error)
	StatFn       func(ctx context.Context, key string) (*storage.ObjectInfo, error)
}

func (m *Storage) Put(ctx context.Context, ownerUID, name string, r io.Reader) (string, int64, error) {
	return m.PutFn(ctx, ownerUID, name, r)
}
func (m *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return m.GetFn(ctx, key)
}
func (m *Storage) Delete(ctx context.Context, key string) error { return m.DeleteFn(ctx, key) }
func (m *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return m.PresignGetFn(ctx, key, ttl)
}
func (m *Storage) Stat(ctx context.Context, key string) (*storage.ObjectInfo, error) {
	return m.StatFn(ctx, key)
}

type UserDir struct {
	GetUsersFn func(ctx context.Context, uids []string) (map[string]sso.User, error)
}

func (m *UserDir) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
	return m.GetUsersFn(ctx, uids)
}

// Compile-time guarantees the doubles satisfy the real interfaces.
var (
	_ repository.FileRepository      = (*FileRepo)(nil)
	_ repository.FileShareRepository = (*FileShareRepo)(nil)
	_ repository.MessageRepository   = (*MessageRepo)(nil)
	_ repository.CommentRepository   = (*CommentRepo)(nil)
	_ storage.FileStorage            = (*Storage)(nil)
)
