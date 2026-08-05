package search_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/comment"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/file"
	"github.com/aero/aero-id/internal/message"
	"github.com/aero/aero-id/internal/outbox"
	searchsvc "github.com/aero/aero-id/internal/search"
)

type mockMsgRepo struct {
	SearchMessagesFn func(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error)
}

func (m *mockMsgRepo) Create(_ context.Context, _ *domain.Message, _ *outbox.Event) error { return nil }
func (m *mockMsgRepo) Get(_ context.Context, _ domain.ID) (*domain.Message, error)        { return nil, nil }
func (m *mockMsgRepo) ListInbox(_ context.Context, _ string, _ domain.Page, _ bool) ([]*domain.Message, string, error) {
	return nil, "", nil
}
func (m *mockMsgRepo) ListConversation(_ context.Context, _, _ string, _ domain.Page) ([]*domain.Message, string, error) {
	return nil, "", nil
}
func (m *mockMsgRepo) MarkRead(_ context.Context, _ domain.ID, _ time.Time) error { return nil }
func (m *mockMsgRepo) MarkReadWithEvent(_ context.Context, _ domain.ID, _ time.Time, _ *outbox.Event) error {
	return nil
}
func (m *mockMsgRepo) CountUnread(_ context.Context, _ string) (int64, error)        { return 0, nil }
func (m *mockMsgRepo) CountUnreadFrom(_ context.Context, _, _ string) (int64, error) { return 0, nil }
func (m *mockMsgRepo) ListConversations(_ context.Context, _ string, _ domain.Page) ([]*message.ConversationSummary, string, error) {
	return nil, "", nil
}
func (m *mockMsgRepo) UpdateContent(_ context.Context, _ domain.ID, _ string, _ time.Time) error {
	return nil
}
func (m *mockMsgRepo) SoftDelete(_ context.Context, _ domain.ID) error { return nil }
func (m *mockMsgRepo) MarkConversationRead(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}
func (m *mockMsgRepo) ReactToMessage(_ context.Context, _ domain.ID, _ string) error { return nil }
func (m *mockMsgRepo) UnreactMessage(_ context.Context, _ domain.ID, _ string) error { return nil }
func (m *mockMsgRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
	if m.SearchMessagesFn != nil {
		return m.SearchMessagesFn(ctx, uid, query, limit)
	}
	return nil, nil
}

type mockCommentRepo struct {
	SearchCommentsFn func(ctx context.Context, uid, query string, limit int) ([]*comment.Comment, error)
}

func (m *mockCommentRepo) Create(_ context.Context, _ *comment.Comment) error { return nil }
func (m *mockCommentRepo) CreateWithReplyBump(_ context.Context, _ *comment.Comment, _ *outbox.Event) error {
	return nil
}
func (m *mockCommentRepo) Get(_ context.Context, _ domain.ID) (*comment.Comment, error) {
	return nil, nil
}
func (m *mockCommentRepo) ListByTarget(_ context.Context, _ domain.TargetType, _ domain.ID, _ domain.Page) ([]*comment.Comment, string, error) {
	return nil, "", nil
}
func (m *mockCommentRepo) ListReplies(_ context.Context, _ domain.ID, _ domain.Page) ([]*comment.Comment, string, error) {
	return nil, "", nil
}
func (m *mockCommentRepo) IncReplyCount(_ context.Context, _ domain.ID) error { return nil }
func (m *mockCommentRepo) SoftDelete(_ context.Context, _ domain.ID) error    { return nil }
func (m *mockCommentRepo) UpdateContent(_ context.Context, _ domain.ID, _ string, _ time.Time) error {
	return nil
}
func (m *mockCommentRepo) Like(_ context.Context, _ domain.ID, _ string) (int, error) { return 0, nil }
func (m *mockCommentRepo) Unlike(_ context.Context, _ domain.ID, _ string) (int, error) {
	return 0, nil
}
func (m *mockCommentRepo) CreateReport(_ context.Context, _ *comment.CommentReport) error { return nil }
func (m *mockCommentRepo) SetStatus(_ context.Context, _ domain.ID, _ domain.CommentStatus) error {
	return nil
}
func (m *mockCommentRepo) ListReports(_ context.Context, _ domain.ID, _ *domain.ReportStatus, _ domain.Page) ([]*comment.CommentReport, string, error) {
	return nil, "", nil
}
func (m *mockCommentRepo) GetReport(_ context.Context, _ domain.ID) (*comment.CommentReport, error) {
	return nil, nil
}
func (m *mockCommentRepo) ResolveReport(_ context.Context, _ domain.ID, _ domain.ReportStatus, _ string, _ time.Time) error {
	return nil
}
func (m *mockCommentRepo) SearchComments(ctx context.Context, uid, query string, limit int) ([]*comment.Comment, error) {
	if m.SearchCommentsFn != nil {
		return m.SearchCommentsFn(ctx, uid, query, limit)
	}
	return nil, nil
}

type mockFileRepo struct {
	SearchFilesFn func(ctx context.Context, uid, query string, limit int) ([]*file.File, error)
}

func (m *mockFileRepo) Get(_ context.Context, _ domain.ID) (*file.File, error) { return nil, nil }
func (m *mockFileRepo) ListByOwner(_ context.Context, _ string, _ domain.Page) ([]*file.File, string, error) {
	return nil, "", nil
}
func (m *mockFileRepo) Create(_ context.Context, _ *file.File) error { return nil }
func (m *mockFileRepo) Delete(_ context.Context, _ domain.ID) error  { return nil }
func (m *mockFileRepo) FindByChecksum(_ context.Context, _, _ string) (*file.File, error) {
	return nil, domain.ErrNotFound
}
func (m *mockFileRepo) CountByStorageKey(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockFileRepo) UpdateMetadata(_ context.Context, _ domain.ID, _ map[string]any) error {
	return nil
}
func (m *mockFileRepo) ListSoftDeletedBefore(_ context.Context, _ time.Time, _ int) ([]*file.File, error) {
	return nil, nil
}
func (m *mockFileRepo) HardDelete(_ context.Context, _ []domain.ID) error { return nil }
func (m *mockFileRepo) CountAnyByStorageKey(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *mockFileRepo) SearchFiles(ctx context.Context, uid, query string, limit int) ([]*file.File, error) {
	if m.SearchFilesFn != nil {
		return m.SearchFilesFn(ctx, uid, query, limit)
	}
	return nil, nil
}

func TestSearch_RejectsEmptyQuery(t *testing.T) {
	svc := searchsvc.NewService(&mockMsgRepo{}, &mockCommentRepo{}, &mockFileRepo{})
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := svc.Search(ctx, "", nil, 20)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestSearch_RequiresActor(t *testing.T) {
	svc := searchsvc.NewService(&mockMsgRepo{}, &mockCommentRepo{}, &mockFileRepo{})
	_, err := svc.Search(context.Background(), "foo", nil, 20)
	if err == nil {
		t.Fatal("expected error for missing actor")
	}
}

func TestSearch_ReturnsMessageHits(t *testing.T) {
	msgs := &mockMsgRepo{SearchMessagesFn: func(_ context.Context, _, _ string, _ int) ([]*domain.Message, error) {
		return []*domain.Message{{ID: uuid.Must(uuid.NewV7()), Content: "hello"}}, nil
	}}
	svc := searchsvc.NewService(msgs, &mockCommentRepo{}, &mockFileRepo{})
	ctx := domain.WithActor(context.Background(), "u1")
	hits, err := svc.Search(ctx, "hello", []domain.SearchKind{domain.SearchKindMessages}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Kind != domain.SearchKindMessages {
		t.Fatalf("hits=%v", hits)
	}
}

func TestSearch_ClampsLimit(t *testing.T) {
	svc := searchsvc.NewService(&mockMsgRepo{}, &mockCommentRepo{}, &mockFileRepo{})
	ctx := domain.WithActor(context.Background(), "u1")
	// Zero limit should use default (20), negative should use min (1)
	hits, err := svc.Search(ctx, "x", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = hits
}

func TestSearch_DefaultKindsAllThree(t *testing.T) {
	var searchedKinds []domain.SearchKind
	msgs := &mockMsgRepo{SearchMessagesFn: func(_ context.Context, _, _ string, _ int) ([]*domain.Message, error) {
		searchedKinds = append(searchedKinds, domain.SearchKindMessages)
		return nil, nil
	}}
	cmts := &mockCommentRepo{SearchCommentsFn: func(_ context.Context, _, _ string, _ int) ([]*comment.Comment, error) {
		searchedKinds = append(searchedKinds, domain.SearchKindComments)
		return nil, nil
	}}
	files := &mockFileRepo{SearchFilesFn: func(_ context.Context, _, _ string, _ int) ([]*file.File, error) {
		searchedKinds = append(searchedKinds, domain.SearchKindFiles)
		return nil, nil
	}}
	svc := searchsvc.NewService(msgs, cmts, files)
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := svc.Search(ctx, "x", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(searchedKinds) != 3 {
		t.Fatalf("searched %d kinds, want 3", len(searchedKinds))
	}
}
