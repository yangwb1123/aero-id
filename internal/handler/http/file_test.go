package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
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
	createFn           func(context.Context, service.CreateFileInput) (*domain.File, error)
	getFn              func(context.Context, domain.ID) (*domain.File, error)
	listFn             func(context.Context, string, domain.Page) (*service.FileList, error)
	deleteFn           func(context.Context, domain.ID) error
	downloadURLFn      func(context.Context, domain.ID, time.Duration) (string, error)
	shareFn            func(context.Context, domain.ID, []string) error
	unshareFn          func(context.Context, domain.ID, string) error
	listSharedWithMeFn func(context.Context, domain.Page) (*service.FileList, error)
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
func (f *fakeFileService) ShareFile(ctx context.Context, id domain.ID, uids []string) error {
	return f.shareFn(ctx, id, uids)
}
func (f *fakeFileService) UnshareFile(ctx context.Context, id domain.ID, uid string) error {
	return f.unshareFn(ctx, id, uid)
}
func (f *fakeFileService) ListSharedWithMe(ctx context.Context, p domain.Page) (*service.FileList, error) {
	return f.listSharedWithMeFn(ctx, p)
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

func TestFileHandler_Share204(t *testing.T) {
	var gotUIDs []string
	svc := &fakeFileService{shareFn: func(_ context.Context, _ domain.ID, uids []string) error {
		gotUIDs = uids
		return nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/files/"+idgen.NewID().String()+"/shares",
		strings.NewReader(`{"uids":["a","b"]}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if len(gotUIDs) != 2 {
		t.Fatalf("uids = %v, want 2", gotUIDs)
	}
}

func TestFileHandler_Unshare204(t *testing.T) {
	var gotUID string
	svc := &fakeFileService{unshareFn: func(_ context.Context, _ domain.ID, uid string) error {
		gotUID = uid
		return nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/files/"+idgen.NewID().String()+"/shares/friend", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if gotUID != "friend" {
		t.Fatalf("uid = %q, want friend", gotUID)
	}
}

func TestFileHandler_ListSharedWithMe(t *testing.T) {
	svc := &fakeFileService{listSharedWithMeFn: func(context.Context, domain.Page) (*service.FileList, error) {
		return &service.FileList{Files: []*domain.File{{OwnerUID: "owner"}}, NextCursor: "next"}, nil
	}}
	e := serveWithActor(httpapi.NewFileHandler(svc, nil, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/files/shared-with-me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Files      []map[string]any `json:"files"`
			NextCursor string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Files) != 1 || body.Data.NextCursor != "next" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

// fakeSignedDownloader implements httpapi.SignedDownloader for tests.
type fakeSignedDownloader struct {
	verifyFn  func(rawURL string, now time.Time) error
	getFn     func(ctx context.Context, key string) (io.ReadCloser, error)
	getCalled bool
}

func (f *fakeSignedDownloader) VerifyPresignedURL(rawURL string, now time.Time) error {
	return f.verifyFn(rawURL, now)
}

func (f *fakeSignedDownloader) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f.getCalled = true
	return f.getFn(ctx, key)
}

func TestFileHandler_DownloadValidSignature(t *testing.T) {
	dl := &fakeSignedDownloader{
		verifyFn: func(string, time.Time) error { return nil },
		getFn: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("hello")), nil
		},
	}
	e := serveWithActor(httpapi.NewFileHandler(&fakeFileService{}, dl, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/files/dl?key=k&exp=123&sig=abc", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "hello" {
		t.Fatalf("body = %q, want %q", got, "hello")
	}
}

func TestFileHandler_DownloadBadSignature(t *testing.T) {
	dl := &fakeSignedDownloader{
		verifyFn: func(string, time.Time) error { return stderrors.New("bad signature") },
		getFn: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("should not be called")), nil
		},
	}
	e := serveWithActor(httpapi.NewFileHandler(&fakeFileService{}, dl, time.Minute), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/files/dl?key=k&exp=123&sig=bad", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if dl.getCalled {
		t.Fatal("Get should not have been called when signature verification fails")
	}
}
