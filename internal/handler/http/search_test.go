package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// fakeSearchService captures the parsed arguments forwarded by the handler and
// returns a canned set of hits (or an error). It mirrors the real service by
// rejecting calls that carry no authenticated actor on the context.
type fakeSearchService struct {
	gotQuery string
	gotKinds []domain.SearchKind
	gotLimit int
	called   bool

	searchFn func(context.Context, string, []domain.SearchKind, int) ([]domain.SearchHit, error)
}

func (f *fakeSearchService) Search(ctx context.Context, q string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error) {
	f.called = true
	f.gotQuery = q
	f.gotKinds = kinds
	f.gotLimit = limit
	if f.searchFn != nil {
		return f.searchFn(ctx, q, kinds, limit)
	}
	return nil, nil
}

// serveSearchNoActor builds an engine WITHOUT the auth middleware, so requests
// reach the handler with a context that has no authenticated actor.
func serveSearchNoActor(h httpapi.RouteRegistrar) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	h.RegisterRoutes(httpapi.Routes{Auth: e.Group("/v1"), Public: e.Group("")})
	return e
}

func TestSearchHandler_MissingQ_BadRequest(t *testing.T) {
	svc := &fakeSearchService{}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if resp.Error.Code != "search.invalid" {
		t.Fatalf("error code = %q, want search.invalid (body=%s)", resp.Error.Code, w.Body.String())
	}
	if svc.called {
		t.Fatal("service should not be called when q is missing")
	}
}

func TestSearchHandler_ParsesKindsAndLimit(t *testing.T) {
	svc := &fakeSearchService{}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=hello&kind=messages,files&limit=7", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !svc.called {
		t.Fatal("service was not called")
	}
	if svc.gotQuery != "hello" {
		t.Errorf("query = %q, want hello", svc.gotQuery)
	}
	if svc.gotLimit != 7 {
		t.Errorf("limit = %d, want 7", svc.gotLimit)
	}
	want := []domain.SearchKind{domain.SearchKindMessages, domain.SearchKindFiles}
	if len(svc.gotKinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", svc.gotKinds, want)
	}
	for i, k := range want {
		if svc.gotKinds[i] != k {
			t.Fatalf("kinds = %v, want %v", svc.gotKinds, want)
		}
	}
}

func TestSearchHandler_KindWhitespaceTrimmedAndUnknownDropped(t *testing.T) {
	svc := &fakeSearchService{}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=x&kind=+comments+,bogus", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if len(svc.gotKinds) != 1 || svc.gotKinds[0] != domain.SearchKindComments {
		t.Fatalf("kinds = %v, want [comments] (whitespace trimmed, unknown dropped)", svc.gotKinds)
	}
}

func TestSearchHandler_DefaultLimitWhenAbsent(t *testing.T) {
	svc := &fakeSearchService{}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=x", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	// The handler defaults limit to 20 when ?limit is absent, and leaves kinds
	// empty (the service expands empty kinds to all three).
	if svc.gotLimit != 20 {
		t.Errorf("limit = %d, want default 20", svc.gotLimit)
	}
	if len(svc.gotKinds) != 0 {
		t.Errorf("kinds = %v, want empty when ?kind absent", svc.gotKinds)
	}
}

func TestSearchHandler_NonNumericLimitKeepsDefault(t *testing.T) {
	svc := &fakeSearchService{}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=x&limit=abc", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if svc.gotLimit != 20 {
		t.Errorf("limit = %d, want default 20 when ?limit is non-numeric", svc.gotLimit)
	}
}

func TestSearchHandler_RendersHitShape(t *testing.T) {
	msgID := idgen.NewID()
	cmtID := idgen.NewID()
	fileID := idgen.NewID()
	svc := &fakeSearchService{
		searchFn: func(context.Context, string, []domain.SearchKind, int) ([]domain.SearchHit, error) {
			return []domain.SearchHit{
				{Kind: domain.SearchKindMessages, Message: &domain.Message{ID: msgID, FromUID: "a", ToUID: "b", Content: "hi there"}},
				{Kind: domain.SearchKindComments, Comment: &domain.Comment{ID: cmtID, OwnerUID: "a", TargetID: idgen.NewID(), RootID: idgen.NewID(), Content: "nice"}},
				{Kind: domain.SearchKindFiles, File: &domain.File{ID: fileID, OwnerUID: "a", Name: "report.pdf"}},
			}, nil
		},
	}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=foo", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Hits []struct {
				Kind    string         `json:"kind"`
				Message map[string]any `json:"message"`
				Comment map[string]any `json:"comment"`
				File    map[string]any `json:"file"`
			} `json:"hits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Data.Hits) != 3 {
		t.Fatalf("expected 3 hits, got %d (body=%s)", len(resp.Data.Hits), w.Body.String())
	}

	// Hit 0: messages -> message populated, comment/file omitted.
	h0 := resp.Data.Hits[0]
	if h0.Kind != "messages" {
		t.Errorf("hit[0].kind = %q, want messages", h0.Kind)
	}
	if h0.Message == nil || h0.Message["id"] != msgID.String() || h0.Message["content"] != "hi there" {
		t.Errorf("hit[0].message rendered wrong: %v", h0.Message)
	}
	if h0.Comment != nil || h0.File != nil {
		t.Errorf("hit[0] should only carry message, got comment=%v file=%v", h0.Comment, h0.File)
	}

	// Hit 1: comments -> comment populated, message/file omitted.
	h1 := resp.Data.Hits[1]
	if h1.Kind != "comments" {
		t.Errorf("hit[1].kind = %q, want comments", h1.Kind)
	}
	if h1.Comment == nil || h1.Comment["id"] != cmtID.String() || h1.Comment["content"] != "nice" {
		t.Errorf("hit[1].comment rendered wrong: %v", h1.Comment)
	}
	if h1.Message != nil || h1.File != nil {
		t.Errorf("hit[1] should only carry comment, got message=%v file=%v", h1.Message, h1.File)
	}

	// Hit 2: files -> file populated, message/comment omitted.
	h2 := resp.Data.Hits[2]
	if h2.Kind != "files" {
		t.Errorf("hit[2].kind = %q, want files", h2.Kind)
	}
	if h2.File == nil || h2.File["id"] != fileID.String() || h2.File["name"] != "report.pdf" {
		t.Errorf("hit[2].file rendered wrong: %v", h2.File)
	}
	if h2.Message != nil || h2.Comment != nil {
		t.Errorf("hit[2] should only carry file, got message=%v comment=%v", h2.Message, h2.Comment)
	}
}

func TestSearchHandler_EmptyHitsRendersEmptyArray(t *testing.T) {
	svc := &fakeSearchService{
		searchFn: func(context.Context, string, []domain.SearchKind, int) ([]domain.SearchHit, error) {
			return nil, nil
		},
	}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=foo", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	// The handler builds the slice with make(...,0,...) so it renders [] not null.
	var resp struct {
		Data struct {
			Hits []json.RawMessage `json:"hits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if resp.Data.Hits == nil {
		t.Fatalf("hits should be a non-nil empty array, body=%s", w.Body.String())
	}
	if len(resp.Data.Hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(resp.Data.Hits))
	}
}

func TestSearchHandler_ServiceErrorMapsToStatus(t *testing.T) {
	svc := &fakeSearchService{
		searchFn: func(context.Context, string, []domain.SearchKind, int) ([]domain.SearchHit, error) {
			return nil, apperr.Internal("search.failed", "boom")
		},
	}
	e := serveWithActor(httpapi.NewSearchHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=foo", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", w.Code, w.Body.String())
	}
}

func TestSearchHandler_UnauthenticatedRejected(t *testing.T) {
	// The handler forwards the request context to the service; with no auth
	// middleware the context carries no actor, and the (real) service rejects
	// via mustActor with auth.unauthorized -> 401. The fake mirrors that.
	svc := &fakeSearchService{
		searchFn: func(ctx context.Context, _ string, _ []domain.SearchKind, _ int) ([]domain.SearchHit, error) {
			if _, ok := service.ActorFrom(ctx); !ok {
				return nil, apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
			}
			return nil, nil
		},
	}
	e := serveSearchNoActor(httpapi.NewSearchHandler(svc))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/search?q=foo", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if resp.Error.Code != "auth.unauthorized" {
		t.Fatalf("error code = %q, want auth.unauthorized", resp.Error.Code)
	}
}
