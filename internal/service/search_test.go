package service_test

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

func TestSearchService_BlankQueryBadRequest(t *testing.T) {
	svc := service.NewSearchService(&mocks.MessageRepo{}, &mocks.CommentRepo{}, &mocks.FileRepo{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Search(ctx, "", nil, 10)
	assertAppCode(t, err, 400)
}

func TestSearchService_RequiresActor(t *testing.T) {
	svc := service.NewSearchService(&mocks.MessageRepo{}, &mocks.CommentRepo{}, &mocks.FileRepo{})
	if _, err := svc.Search(context.Background(), "hello", nil, 10); err == nil {
		t.Fatal("expected unauthorized without actor")
	}
}

// TestSearchService_EmptyKindsSearchesAll verifies that an empty kinds slice fans
// the query out to all three repos and aggregates the hits with the correct Kind
// discriminator on each.
func TestSearchService_EmptyKindsSearchesAll(t *testing.T) {
	var msgSearched, cmtSearched, fileSearched bool
	msgs := &mocks.MessageRepo{SearchMessagesWithSnippetFn: func(_ context.Context, _, _ string, _ int) ([]repository.MessageSearchResult, error) {
		msgSearched = true
		return []repository.MessageSearchResult{{Message: &domain.Message{ID: idgen.NewID()}}}, nil
	}}
	comments := &mocks.CommentRepo{SearchCommentsWithSnippetFn: func(_ context.Context, _, _ string, _ int) ([]repository.CommentSearchResult, error) {
		cmtSearched = true
		return []repository.CommentSearchResult{{Comment: &domain.Comment{ID: idgen.NewID()}}}, nil
	}}
	files := &mocks.FileRepo{SearchFilesFn: func(_ context.Context, _, _ string, _ int) ([]*domain.File, error) {
		fileSearched = true
		return []*domain.File{{ID: idgen.NewID()}}, nil
	}}
	svc := service.NewSearchService(msgs, comments, files)
	ctx := service.WithActor(context.Background(), "a")
	hits, err := svc.Search(ctx, "q", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !msgSearched || !cmtSearched || !fileSearched {
		t.Fatalf("empty kinds must search all repos: msg=%v cmt=%v file=%v", msgSearched, cmtSearched, fileSearched)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 aggregated hits, got %d", len(hits))
	}
	// Each hit carries the right discriminator and the matching union member.
	for _, h := range hits {
		switch h.Kind {
		case domain.SearchKindMessages:
			if h.Message == nil {
				t.Fatal("message hit missing Message")
			}
		case domain.SearchKindComments:
			if h.Comment == nil {
				t.Fatal("comment hit missing Comment")
			}
		case domain.SearchKindFiles:
			if h.File == nil {
				t.Fatal("file hit missing File")
			}
		default:
			t.Fatalf("unexpected kind %q", h.Kind)
		}
	}
}

// TestSearchService_ExplicitKindsRestrict verifies that naming a single kind only
// hits that repo and leaves the others untouched.
func TestSearchService_ExplicitKindsRestrict(t *testing.T) {
	cmtSearched, fileSearched := false, false
	msgs := &mocks.MessageRepo{SearchMessagesWithSnippetFn: func(_ context.Context, _, _ string, _ int) ([]repository.MessageSearchResult, error) {
		return []repository.MessageSearchResult{
			{Message: &domain.Message{ID: idgen.NewID()}},
			{Message: &domain.Message{ID: idgen.NewID()}},
		}, nil
	}}
	comments := &mocks.CommentRepo{SearchCommentsWithSnippetFn: func(_ context.Context, _, _ string, _ int) ([]repository.CommentSearchResult, error) {
		cmtSearched = true
		return nil, nil
	}}
	files := &mocks.FileRepo{SearchFilesFn: func(_ context.Context, _, _ string, _ int) ([]*domain.File, error) {
		fileSearched = true
		return nil, nil
	}}
	svc := service.NewSearchService(msgs, comments, files)
	ctx := service.WithActor(context.Background(), "a")
	hits, err := svc.Search(ctx, "q", []domain.SearchKind{domain.SearchKindMessages}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cmtSearched || fileSearched {
		t.Fatalf("explicit messages kind must not search comments/files: cmt=%v file=%v", cmtSearched, fileSearched)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 message hits, got %d", len(hits))
	}
	for _, h := range hits {
		if h.Kind != domain.SearchKindMessages {
			t.Fatalf("kind = %q, want messages", h.Kind)
		}
	}
}

// TestSearchService_LimitClamping checks the [1,50] bounds and the default applied
// for a non-positive limit. The clamped value is what reaches the repo.
func TestSearchService_LimitClamping(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero defaults to 20", 0, 20},
		{"negative defaults to 20", -5, 20},
		{"one stays one", 1, 1},
		{"over-max clamps to 50", 1000, 50},
		{"exactly max stays", 50, 50},
		{"mid-range stays", 25, 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotLimit int
			msgs := &mocks.MessageRepo{SearchMessagesWithSnippetFn: func(_ context.Context, _, _ string, limit int) ([]repository.MessageSearchResult, error) {
				gotLimit = limit
				return nil, nil
			}}
			svc := service.NewSearchService(msgs, &mocks.CommentRepo{}, &mocks.FileRepo{})
			ctx := service.WithActor(context.Background(), "a")
			if _, err := svc.Search(ctx, "q", []domain.SearchKind{domain.SearchKindMessages}, tc.in); err != nil {
				t.Fatal(err)
			}
			if gotLimit != tc.want {
				t.Fatalf("limit passed to repo = %d, want %d", gotLimit, tc.want)
			}
		})
	}
}

// TestSearchService_UIDScoping is the security-critical case: the actor uid from
// context must be the uid every repo search is scoped to, never an attacker-supplied
// value.
func TestSearchService_UIDScoping(t *testing.T) {
	var msgUID, cmtUID, fileUID string
	msgs := &mocks.MessageRepo{SearchMessagesWithSnippetFn: func(_ context.Context, uid, _ string, _ int) ([]repository.MessageSearchResult, error) {
		msgUID = uid
		return nil, nil
	}}
	comments := &mocks.CommentRepo{SearchCommentsWithSnippetFn: func(_ context.Context, uid, _ string, _ int) ([]repository.CommentSearchResult, error) {
		cmtUID = uid
		return nil, nil
	}}
	files := &mocks.FileRepo{SearchFilesFn: func(_ context.Context, uid, _ string, _ int) ([]*domain.File, error) {
		fileUID = uid
		return nil, nil
	}}
	svc := service.NewSearchService(msgs, comments, files)
	ctx := service.WithActor(context.Background(), "actor-uid")
	if _, err := svc.Search(ctx, "q", nil, 10); err != nil {
		t.Fatal(err)
	}
	if msgUID != "actor-uid" || cmtUID != "actor-uid" || fileUID != "actor-uid" {
		t.Fatalf("repos must be scoped to the actor uid: msg=%q cmt=%q file=%q", msgUID, cmtUID, fileUID)
	}
}

func TestSearchService_QueryPassedThrough(t *testing.T) {
	var gotQuery string
	msgs := &mocks.MessageRepo{SearchMessagesWithSnippetFn: func(_ context.Context, _, query string, _ int) ([]repository.MessageSearchResult, error) {
		gotQuery = query
		return nil, nil
	}}
	svc := service.NewSearchService(msgs, &mocks.CommentRepo{}, &mocks.FileRepo{})
	ctx := service.WithActor(context.Background(), "a")
	if _, err := svc.Search(ctx, "needle", []domain.SearchKind{domain.SearchKindMessages}, 10); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "needle" {
		t.Fatalf("query passed to repo = %q, want needle", gotQuery)
	}
}

func TestSearchService_QueryTooLong(t *testing.T) {
	svc := service.NewSearchService(&mocks.MessageRepo{}, &mocks.CommentRepo{}, &mocks.FileRepo{})
	ctx := service.WithActor(context.Background(), "a")
	// Build a query string that exceeds searchMaxQueryLen (200 chars).
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	_, err := svc.Search(ctx, string(long), nil, 10)
	assertAppCode(t, err, 400)
}
