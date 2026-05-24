package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type fakeCommentService struct {
	createFn  func(context.Context, service.CreateCommentInput) (*domain.Comment, error)
	listFn    func(context.Context, domain.TargetType, domain.ID, domain.Page) (*service.CommentList, error)
	repliesFn func(context.Context, domain.ID, domain.Page) (*service.CommentList, error)
	deleteFn  func(context.Context, domain.ID) error
}

func (f *fakeCommentService) Create(ctx context.Context, in service.CreateCommentInput) (*domain.Comment, error) {
	return f.createFn(ctx, in)
}
func (f *fakeCommentService) List(ctx context.Context, t domain.TargetType, id domain.ID, p domain.Page) (*service.CommentList, error) {
	return f.listFn(ctx, t, id, p)
}
func (f *fakeCommentService) Replies(ctx context.Context, root domain.ID, p domain.Page) (*service.CommentList, error) {
	return f.repliesFn(ctx, root, p)
}
func (f *fakeCommentService) Delete(ctx context.Context, id domain.ID) error { return f.deleteFn(ctx, id) }

func TestCommentHandler_Create(t *testing.T) {
	target := idgen.NewID()
	svc := &fakeCommentService{createFn: func(_ context.Context, in service.CreateCommentInput) (*domain.Comment, error) {
		if in.TargetType != domain.TargetTypeFile || in.TargetID != target {
			t.Errorf("input not bound: %+v", in)
		}
		return &domain.Comment{ID: idgen.NewID(), OwnerUID: "a", TargetType: in.TargetType, TargetID: in.TargetID, Content: in.Content}, nil
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	bodyJSON := `{"target_type":"file","target_id":"` + target.String() + `","content":"nice"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/comments", strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_CreateNestingTooDeepMapsTo400(t *testing.T) {
	svc := &fakeCommentService{createFn: func(context.Context, service.CreateCommentInput) (*domain.Comment, error) {
		return nil, apperr.BadRequest("comment.nesting_too_deep", "too deep")
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	body := `{"target_type":"file","target_id":"` + idgen.NewID().String() + `","parent_id":"` + idgen.NewID().String() + `","content":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/comments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCommentHandler_ListRequiresTargetID(t *testing.T) {
	e := serveWithActor(httpapi.NewCommentHandler(&fakeCommentService{}), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments?target_type=file&target_id=bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCommentHandler_List200(t *testing.T) {
	target := idgen.NewID()
	svc := &fakeCommentService{listFn: func(context.Context, domain.TargetType, domain.ID, domain.Page) (*service.CommentList, error) {
		return &service.CommentList{Comments: []*domain.Comment{{ID: idgen.NewID(), OwnerUID: "a"}}, NextCursor: ""}, nil
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments?target_type=file&target_id="+target.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_Delete204(t *testing.T) {
	svc := &fakeCommentService{deleteFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/comments/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}
