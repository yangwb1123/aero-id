package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type fakeBlockService struct {
	blockFn       func(context.Context, string) error
	unblockFn     func(context.Context, string) error
	listBlockedFn func(context.Context, domain.Page) (*service.BlockList, error)
}

func (f *fakeBlockService) Block(ctx context.Context, uid string) error {
	return f.blockFn(ctx, uid)
}
func (f *fakeBlockService) Unblock(ctx context.Context, uid string) error {
	return f.unblockFn(ctx, uid)
}
func (f *fakeBlockService) ListBlocked(ctx context.Context, p domain.Page) (*service.BlockList, error) {
	return f.listBlockedFn(ctx, p)
}

func TestBlockHandler_Block204(t *testing.T) {
	var got string
	svc := &fakeBlockService{blockFn: func(_ context.Context, uid string) error { got = uid; return nil }}
	e := serveWithActor(httpapi.NewBlockHandler(svc), "u1")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/blocks", strings.NewReader(`{"uid":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if got != "b" {
		t.Fatalf("blocked uid = %q, want b", got)
	}
}

func TestBlockHandler_Unblock204(t *testing.T) {
	var got string
	svc := &fakeBlockService{unblockFn: func(_ context.Context, uid string) error { got = uid; return nil }}
	e := serveWithActor(httpapi.NewBlockHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/blocks/b", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if got != "b" {
		t.Fatalf("unblocked uid = %q, want b", got)
	}
}

func TestBlockHandler_List(t *testing.T) {
	svc := &fakeBlockService{listBlockedFn: func(context.Context, domain.Page) (*service.BlockList, error) {
		return &service.BlockList{UIDs: []string{"b", "c"}, NextCursor: "next"}, nil
	}}
	e := serveWithActor(httpapi.NewBlockHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/blocks", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Blocked    []string `json:"blocked"`
			NextCursor string   `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Blocked) != 2 || resp.Data.Blocked[0] != "b" || resp.Data.NextCursor != "next" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}
