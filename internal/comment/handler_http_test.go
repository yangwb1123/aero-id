package comment_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"

	"github.com/aero/aero-id/internal/comment"
)

type fakeCommentService struct {
	createFn        func(context.Context, comment.CreateCommentInput) (*comment.Comment, error)
	listFn          func(context.Context, domain.TargetType, domain.ID, domain.Page) (*comment.CommentList, error)
	repliesFn       func(context.Context, domain.ID, domain.Page) (*comment.CommentList, error)
	editFn          func(context.Context, domain.ID, string) (*comment.Comment, error)
	deleteFn        func(context.Context, domain.ID) error
	likeFn          func(context.Context, domain.ID) (*comment.ReactionResult, error)
	unlikeFn        func(context.Context, domain.ID) (*comment.ReactionResult, error)
	reportFn        func(context.Context, domain.ID, string) error
	hideFn          func(context.Context, domain.ID) error
	unhideFn        func(context.Context, domain.ID) error
	listReportsFn   func(context.Context, domain.ID, *domain.ReportStatus, domain.Page) (*comment.ReportList, error)
	resolveReportFn func(context.Context, domain.ID, domain.ReportStatus) error
}

func (f *fakeCommentService) Create(ctx context.Context, in comment.CreateCommentInput) (*comment.Comment, error) {
	return f.createFn(ctx, in)
}
func (f *fakeCommentService) List(ctx context.Context, t domain.TargetType, id domain.ID, p domain.Page) (*comment.CommentList, error) {
	return f.listFn(ctx, t, id, p)
}
func (f *fakeCommentService) Replies(ctx context.Context, root domain.ID, p domain.Page) (*comment.CommentList, error) {
	return f.repliesFn(ctx, root, p)
}
func (f *fakeCommentService) Edit(ctx context.Context, id domain.ID, content string) (*comment.Comment, error) {
	return f.editFn(ctx, id, content)
}
func (f *fakeCommentService) Delete(ctx context.Context, id domain.ID) error {
	return f.deleteFn(ctx, id)
}
func (f *fakeCommentService) Like(ctx context.Context, id domain.ID) (*comment.ReactionResult, error) {
	return f.likeFn(ctx, id)
}
func (f *fakeCommentService) Unlike(ctx context.Context, id domain.ID) (*comment.ReactionResult, error) {
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
func (f *fakeCommentService) ListReports(ctx context.Context, id domain.ID, status *domain.ReportStatus, p domain.Page) (*comment.ReportList, error) {
	if f.listReportsFn == nil {
		return &comment.ReportList{}, nil
	}
	return f.listReportsFn(ctx, id, status, p)
}
func (f *fakeCommentService) ResolveReport(ctx context.Context, reportID domain.ID, status domain.ReportStatus) error {
	if f.resolveReportFn == nil {
		return nil
	}
	return f.resolveReportFn(ctx, reportID, status)
}

func serveWithActor(h httpapi.RouteRegistrar, actor string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(domain.WithActor(c.Request.Context(), actor))
		c.Next()
	})
	h.RegisterRoutes(httpapi.Routes{Auth: e.Group("/v1"), Public: e.Group("")})
	return e
}

func TestCommentHandler_Create(t *testing.T) {
	target := idgen.NewID()
	svc := &fakeCommentService{createFn: func(_ context.Context, in comment.CreateCommentInput) (*comment.Comment, error) {
		if in.TargetType != domain.TargetTypeFile || in.TargetID != target {
			t.Errorf("input not bound: %+v", in)
		}
		return &comment.Comment{ID: idgen.NewID(), OwnerUID: "a", TargetType: in.TargetType, TargetID: in.TargetID, Content: in.Content}, nil
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
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
	svc := &fakeCommentService{createFn: func(context.Context, comment.CreateCommentInput) (*comment.Comment, error) {
		return nil, apperr.BadRequest("comment.nesting_too_deep", "too deep")
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
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
	e := serveWithActor(comment.NewCommentHandler(&fakeCommentService{}), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments?target_type=file&target_id=bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCommentHandler_List200(t *testing.T) {
	target := idgen.NewID()
	svc := &fakeCommentService{listFn: func(context.Context, domain.TargetType, domain.ID, domain.Page) (*comment.CommentList, error) {
		return &comment.CommentList{Comments: []*comment.Comment{{ID: idgen.NewID(), OwnerUID: "a"}}, NextCursor: ""}, nil
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments?target_type=file&target_id="+target.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_Delete204(t *testing.T) {
	svc := &fakeCommentService{deleteFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/comments/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestCommentHandler_Like(t *testing.T) {
	svc := &fakeCommentService{likeFn: func(context.Context, domain.ID) (*comment.ReactionResult, error) {
		return &comment.ReactionResult{Liked: true, LikeCount: 3}, nil
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
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
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
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
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
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
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
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
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/comments/"+idgen.NewID().String()+"/unhide", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_Edit200(t *testing.T) {
	id := idgen.NewID()
	var gotContent string
	svc := &fakeCommentService{editFn: func(_ context.Context, cid domain.ID, content string) (*comment.Comment, error) {
		gotContent = content
		return &comment.Comment{ID: cid, OwnerUID: "a", Content: content}, nil
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/v1/comments/"+id.String(), strings.NewReader(`{"content":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if gotContent != "updated" {
		t.Errorf("content = %q, want updated", gotContent)
	}
	if !strings.Contains(w.Body.String(), `"content":"updated"`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestCommentHandler_EditForbidden(t *testing.T) {
	svc := &fakeCommentService{editFn: func(context.Context, domain.ID, string) (*comment.Comment, error) {
		return nil, apperr.Forbidden("comment.forbidden", "nope")
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/v1/comments/"+idgen.NewID().String(), strings.NewReader(`{"content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_ListReports200(t *testing.T) {
	commentID := idgen.NewID()
	svc := &fakeCommentService{listReportsFn: func(_ context.Context, _ domain.ID, _ *domain.ReportStatus, _ domain.Page) (*comment.ReportList, error) {
		return &comment.ReportList{Reports: []*comment.CommentReport{
			{ID: idgen.NewID(), CommentID: commentID, ReporterUID: "r1", Status: domain.ReportStatusOpen},
		}, NextCursor: ""}, nil
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments/"+commentID.String()+"/reports", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"open"`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestCommentHandler_ListReportsForbidden(t *testing.T) {
	svc := &fakeCommentService{listReportsFn: func(context.Context, domain.ID, *domain.ReportStatus, domain.Page) (*comment.ReportList, error) {
		return nil, apperr.Forbidden("comment.forbidden", "nope")
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/comments/"+idgen.NewID().String()+"/reports", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCommentHandler_ResolveReport204(t *testing.T) {
	reportID := idgen.NewID()
	var gotID domain.ID
	var gotStatus domain.ReportStatus
	svc := &fakeCommentService{resolveReportFn: func(_ context.Context, id domain.ID, s domain.ReportStatus) error {
		gotID = id
		gotStatus = s
		return nil
	}}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/comments/reports/"+reportID.String()+"/resolve", strings.NewReader(`{"status":"resolved"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if gotID != reportID || gotStatus != domain.ReportStatusResolved {
		t.Errorf("resolve bound wrong: id=%v status=%d", gotID, gotStatus)
	}
}

func TestCommentHandler_ResolveReportInvalidStatus(t *testing.T) {
	svc := &fakeCommentService{}
	e := serveWithActor(comment.NewCommentHandler(svc), "a")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/comments/reports/"+idgen.NewID().String()+"/resolve", strings.NewReader(`{"status":"bogus"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}
