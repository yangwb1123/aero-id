package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// serveWithActor builds an engine that injects a fixed actor (simulating the auth
// middleware) then registers the handler. Shared by all handler tests here.
func serveWithActor(h httpapi.RouteRegistrar, actor string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Set("actor_uid", actor)
		c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), actor))
		c.Next()
	})
	h.RegisterRoutes(httpapi.Routes{Auth: e.Group("/v1"), Public: e.Group("")})
	return e
}

type fakeFileService struct {
	createFn      func(context.Context, service.CreateFileInput) (*domain.File, error)
	getFn         func(context.Context, domain.ID) (*domain.File, error)
	listFn        func(context.Context, string, domain.Page) (*service.FileList, error)
	deleteFn      func(context.Context, domain.ID) error
	downloadURLFn func(context.Context, domain.ID, time.Duration) (string, error)
}

func (f *fakeFileService) Create(ctx context.Context, in service.CreateFileInput) (*domain.File, error) {
	return f.createFn(ctx, in)
}
func (f *fakeFileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return f.getFn(ctx, id)
}
func (f *fakeFileService) List(ctx context.Context, owner string, p domain.Page) (*service.FileList, error) {
	return f.listFn(ctx, owner, p)
}
func (f *fakeFileService) Delete(ctx context.Context, id domain.ID) error { return f.deleteFn(ctx, id) }
func (f *fakeFileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	return f.downloadURLFn(ctx, id, ttl)
}

func TestFileHandler_Upload(t *testing.T) {
	svc := &fakeFileService{createFn: func(_ context.Context, in service.CreateFileInput) (*domain.File, error) {
		return &domain.File{ID: idgen.NewID(), OwnerUID: "u1", Name: in.Name, Size: 5}, nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "a.txt")
	_, _ = fw.Write([]byte("hello"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.ID == "" || resp.Data.Name != "a.txt" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestFileHandler_GetNotFoundMapsTo404(t *testing.T) {
	svc := &fakeFileService{getFn: func(context.Context, domain.ID) (*domain.File, error) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestFileHandler_InvalidID400(t *testing.T) {
	e := serveWithActor(httpapi.NewFileHandler(&fakeFileService{}, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/not-a-uuid", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestFileHandler_DownloadURL(t *testing.T) {
	svc := &fakeFileService{downloadURLFn: func(context.Context, domain.ID, time.Duration) (string, error) {
		return "https://signed/x", nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/"+idgen.NewID().String()+"/url", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestFileHandler_Delete204(t *testing.T) {
	svc := &fakeFileService{deleteFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/files/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}
