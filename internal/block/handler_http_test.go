package block_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/block"
	"github.com/aero/aero-id/internal/domain"
	httphandler "github.com/aero/aero-id/internal/handler/http"
)

type mockBlockService struct {
	BlockFn       func(ctx context.Context, uid string) error
	UnblockFn     func(ctx context.Context, uid string) error
	ListBlockedFn func(ctx context.Context, page domain.Page) (*block.BlockList, error)
}

func (m *mockBlockService) Block(ctx context.Context, uid string) error {
	if m.BlockFn != nil {
		return m.BlockFn(ctx, uid)
	}
	return nil
}
func (m *mockBlockService) Unblock(ctx context.Context, uid string) error {
	if m.UnblockFn != nil {
		return m.UnblockFn(ctx, uid)
	}
	return nil
}
func (m *mockBlockService) ListBlocked(ctx context.Context, page domain.Page) (*block.BlockList, error) {
	if m.ListBlockedFn != nil {
		return m.ListBlockedFn(ctx, page)
	}
	return &block.BlockList{}, nil
}

func setupBlockRouter(svc block.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	h := block.NewBlockHandler(svc)
	routes := httphandler.Routes{
		Auth: e.Group("/v1", func(c *gin.Context) {
			c.Request = c.Request.WithContext(domain.WithActor(c.Request.Context(), "u1"))
			c.Next()
		}),
	}
	h.RegisterRoutes(routes)
	return e
}

func TestBlockHTTP_BlockUser(t *testing.T) {
	called := false
	svc := &mockBlockService{
		BlockFn: func(_ context.Context, uid string) error {
			called = true
			if uid != "u2" {
				t.Fatalf("uid = %q", uid)
			}
			return nil
		},
	}
	r := setupBlockRouter(svc)
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"uid":"u2"}`)
	req := httptest.NewRequest("POST", "/v1/blocks", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("code = %d", w.Code)
	}
	if !called {
		t.Fatal("Block not called")
	}
}

func TestBlockHTTP_BlockInvalidBody(t *testing.T) {
	svc := &mockBlockService{}
	r := setupBlockRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/blocks", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", w.Code)
	}
}

func TestBlockHTTP_UnblockUser(t *testing.T) {
	called := false
	svc := &mockBlockService{
		UnblockFn: func(_ context.Context, uid string) error {
			called = true
			if uid != "u2" {
				t.Fatalf("uid = %q", uid)
			}
			return nil
		},
	}
	r := setupBlockRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/blocks/u2", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("code = %d", w.Code)
	}
	if !called {
		t.Fatal("Unblock not called")
	}
}

func TestBlockHTTP_ListBlocked(t *testing.T) {
	svc := &mockBlockService{
		ListBlockedFn: func(_ context.Context, page domain.Page) (*block.BlockList, error) {
			return &block.BlockList{UIDs: []string{"x", "y"}, NextCursor: "next"}, nil
		},
	}
	r := setupBlockRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/blocks", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var resp struct {
		Data struct {
			UIDs       []string `json:"uids"`
			NextCursor string   `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.UIDs) != 2 || resp.Data.NextCursor != "next" {
		t.Errorf("uids=%v cursor=%q", resp.Data.UIDs, resp.Data.NextCursor)
	}
}
