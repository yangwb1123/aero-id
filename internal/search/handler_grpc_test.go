package search_test

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/search"
)

type mockSearchService struct {
	searchFn func(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error)
}

func (m *mockSearchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, query, kinds, limit)
	}
	return nil, nil
}

func TestSearchServer_Search(t *testing.T) {
	msgID := idgen.NewID()
	srv := search.NewSearchServer(&mockSearchService{
		searchFn: func(_ context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error) {
			if query != "hello" {
				t.Errorf("query = %q, want hello", query)
			}
			if len(kinds) != 1 || kinds[0] != domain.SearchKindMessages {
				t.Errorf("kinds = %v", kinds)
			}
			if limit != 20 {
				t.Errorf("limit = %d, want 20", limit)
			}
			return []search.SearchHit{
				{Kind: domain.SearchKindMessages, Message: &domain.Message{ID: msgID, FromUID: "a", ToUID: "b", Content: "hi there"}},
			}, nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.Search(ctx, &aeropb.SearchRequest{
		Query: "hello",
		Kinds: []string{"messages"},
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(resp.Hits))
	}
	if resp.Hits[0].Kind != "messages" {
		t.Errorf("kind = %q", resp.Hits[0].Kind)
	}
	if resp.Hits[0].Message == nil {
		t.Fatal("hit should carry message")
	}
}

func TestSearchServer_SearchMultipleKinds(t *testing.T) {
	cmtID := idgen.NewID()
	fileID := idgen.NewID()
	srv := search.NewSearchServer(&mockSearchService{
		searchFn: func(_ context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error) {
			return []search.SearchHit{
				{Kind: domain.SearchKindComments, Comment: &domain.Comment{ID: cmtID, OwnerUID: "a", TargetID: idgen.NewID(), RootID: idgen.NewID(), Content: "nice"}},
				{Kind: domain.SearchKindFiles, File: &domain.File{ID: fileID, OwnerUID: "a", Name: "report.pdf"}},
			}, nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.Search(ctx, &aeropb.SearchRequest{
		Query: "test",
		Kinds: []string{"comments", "files"},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(resp.Hits))
	}
	if resp.Hits[0].Kind != "comments" || resp.Hits[0].Comment == nil {
		t.Errorf("hit[0] should be comment, got kind=%s", resp.Hits[0].Kind)
	}
	if resp.Hits[1].Kind != "files" || resp.Hits[1].File == nil {
		t.Errorf("hit[1] should be file, got kind=%s", resp.Hits[1].Kind)
	}
}

func TestSearchServer_SearchEmptyHits(t *testing.T) {
	srv := search.NewSearchServer(&mockSearchService{
		searchFn: func(_ context.Context, _ string, _ []domain.SearchKind, _ int) ([]search.SearchHit, error) {
			return nil, nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.Search(ctx, &aeropb.SearchRequest{Query: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(resp.Hits))
	}
}

func TestSearchServer_SearchWithAllKinds(t *testing.T) {
	msgID := idgen.NewID()
	cmtID := idgen.NewID()
	fileID := idgen.NewID()
	srv := search.NewSearchServer(&mockSearchService{
		searchFn: func(_ context.Context, _ string, kinds []domain.SearchKind, _ int) ([]search.SearchHit, error) {
			if len(kinds) != 3 {
				t.Fatalf("kinds len = %d, want 3", len(kinds))
			}
			return []search.SearchHit{
				{Kind: domain.SearchKindMessages, Message: &domain.Message{ID: msgID, FromUID: "a", ToUID: "b", Content: "hi"}},
				{Kind: domain.SearchKindComments, Comment: &domain.Comment{ID: cmtID, OwnerUID: "a", TargetID: idgen.NewID(), RootID: idgen.NewID(), Content: "nice"}},
				{Kind: domain.SearchKindFiles, File: &domain.File{ID: fileID, OwnerUID: "a", Name: "report.pdf"}},
			}, nil
		},
	})
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.Search(ctx, &aeropb.SearchRequest{
		Query: "test",
		Kinds: []string{"messages", "comments", "files"},
		Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(resp.Hits))
	}
	// Verify each hit has the correct type populated.
	for _, hit := range resp.Hits {
		switch hit.Kind {
		case "messages":
			if hit.Message == nil {
				t.Error("hit should carry message")
			}
		case "comments":
			if hit.Comment == nil {
				t.Error("hit should carry comment")
			}
		case "files":
			if hit.File == nil {
				t.Error("hit should carry file")
			}
		default:
			t.Errorf("unexpected kind: %s", hit.Kind)
		}
	}
}
