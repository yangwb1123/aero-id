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
	likeFn    func(context.Context, domain.ID) (*service.ReactionResult, error)
	unlikeFn  func(context.Context, domain.ID) (*service.ReactionResult, error)
	reportFn  func(context.Context, domain.ID, string) error
	hideFn    func(context.Context, domain.ID) error
	unhideFn  func(context.Context, domain.ID) error
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
func (f *fakeCommentService) Delete(ctx context.Context, id domain.ID) error {
	return f.deleteFn(ctx, id)
}
func (f *fakeCommentService) Like(ctx context.Context, id domain.ID) (*service.ReactionResult, error) {
	return f.likeFn(ctx, id)
}
func (f *fakeCommentService) Unlike(ctx context.Context, id domain.ID) (*service.ReactionResult, error) {
	return f.unlikeFn(ctx, id)
}
func (f *fakeCommentService) Report(ctx context.Context, id domain.ID, reason string) error {
	if f.reportFn == nil {
		return nil
	}
	return f.reportFn(ctx, id, reason)
}
func (f *fakeCommentService) Hide(ctx context.Context, id domain.ID) error {
	if f.hideFn == nil {
		return nil
	}
	return f.hideFn(ctx, id)
}
func (f *fakeCommentService) Unhide(ctx context.Context, id domain.ID) error {
	if f.unhideFn == nil {
		return nil
	}
	return f.unhideFn(ctx, id)
}

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

func TestCommentHandler_Like(t *testing.T) {
	svc := &fakeCommentService{likeFn: func(context.Context, domain.ID) (*service.ReactionResult, error) {
		return &service.ReactionResult{Liked: true, LikeCount: 3}, nil
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/comments/"+idgen.NewID().String()+"/like", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"like_count":3`) || !strings.Contains(w.Body.String(), `"liked":true`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestCommentHandler_Report204(t *testing.T) {
	var gotReason string
	svc := &fakeCommentService{reportFn: func(_ context.Context, _ domain.ID, reason string) error {
		gotReason = reason
		return nil
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/comments/"+idgen.NewID().String()+"/report", strings.NewReader(`{"reason":"spam"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if gotReason != "spam" {
		t.Errorf("reason = %q, want spam", gotReason)
	}
}

func TestCommentHandler_ReportConflict(t *testing.T) {
	svc := &fakeCommentService{reportFn: func(context.Context, domain.ID, string) error {
		return apperr.Conflict("comment.already_reported", "dup")
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/comments/"+idgen.NewID().String()+"/report", strings.NewReader(`{"reason":"spam"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_Hide204(t *testing.T) {
	svc := &fakeCommentService{hideFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/comments/"+idgen.NewID().String()+"/hide", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_UnhideForbidden(t *testing.T) {
	svc := &fakeCommentService{unhideFn: func(context.Context, domain.ID) error {
		return apperr.Forbidden("comment.forbidden", "nope")
	}}
	e := serveWithActor(httpapi.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/comments/"+idgen.NewID().String()+"/unhide", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}
