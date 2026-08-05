package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httphandler "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/search"
)

type fakeSearchService struct {
	searchFn func(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error)
}

func (f *fakeSearchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error) {
	if query == "" {
		return nil, apperr.BadRequest("search.invalid", "query is required")
	}
	if f.searchFn != nil {
		return f.searchFn(ctx, query, kinds, limit)
	}
	return nil, nil
}

func setupSearchRouter(svc search.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	h := search.NewSearchHandler(svc)
	routes := httphandler.Routes{
		Auth: e.Group("/v1", func(c *gin.Context) {
			c.Request = c.Request.WithContext(domain.WithActor(c.Request.Context(), "u1"))
			c.Next()
		}),
	}
	h.RegisterRoutes(routes)
	return e
}

func TestSearchHandler_Search(t *testing.T) {
	svc := &fakeSearchService{
		searchFn: func(_ context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error) {
			if query != "hello" {
				t.Errorf("query = %q, want hello", query)
			}
			if limit != 20 {
				t.Errorf("limit = %d, want 20", limit)
			}
			return []search.SearchHit{
				{Kind: "messages", Message: &domain.Message{ID: domain.ID{}, FromUID: "a", ToUID: "b", Content: "hi"}},
			}, nil
		},
	}
	e := setupSearchRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/search?q=hello", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			Results []struct {
				Kind   string         `json:"kind"`
				Result map[string]any `json:"result"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Data.Results))
	}
	if resp.Data.Results[0].Kind != "messages" {
		t.Errorf("kind = %q", resp.Data.Results[0].Kind)
	}
}

func TestSearchHandler_EmptyQueryReturnsBadRequest(t *testing.T) {
	svc := &fakeSearchService{}
	e := setupSearchRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/search", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSearchHandler_KindsParsed(t *testing.T) {
	svc := &fakeSearchService{
		searchFn: func(_ context.Context, query string, kinds []domain.SearchKind, limit int) ([]search.SearchHit, error) {
			if len(kinds) != 2 {
				t.Fatalf("kinds len = %d, want 2", len(kinds))
			}
			if kinds[0] != "comments" || kinds[1] != "files" {
				t.Errorf("kinds = %v", kinds)
			}
			return nil, nil
		},
	}
	e := setupSearchRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/search?q=test&kinds=comments,files", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestSearchHandler_ServiceError(t *testing.T) {
	svc := &fakeSearchService{
		searchFn: func(_ context.Context, _ string, _ []domain.SearchKind, _ int) ([]search.SearchHit, error) {
			return nil, apperr.Internal("search.failed", "boom")
		},
	}
	e := setupSearchRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/search?q=test", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestSearchHandler_NoContent(t *testing.T) {
	svc := &fakeSearchService{
		searchFn: func(_ context.Context, _ string, _ []domain.SearchKind, _ int) ([]search.SearchHit, error) {
			return nil, nil
		},
	}
	e := setupSearchRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/search?q=test", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			Results []json.RawMessage `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Results == nil {
		t.Fatal("results should be non-nil empty array")
	}
	if len(resp.Data.Results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(resp.Data.Results))
	}
}
