package file

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httphandler "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type SignedDownloader interface {
	VerifyPresignedURL(rawURL string, now time.Time) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

type FileHandler struct {
	files      Service
	downloader SignedDownloader
	urlTTL     time.Duration
}

func NewFileHandler(files Service, downloader SignedDownloader, urlTTL time.Duration) *FileHandler {
	return &FileHandler{files: files, downloader: downloader, urlTTL: urlTTL}
}

func (h *FileHandler) RegisterRoutes(r httphandler.Routes) {
	r.Auth.POST("/files", h.Upload)
	r.Auth.GET("/files", h.List)
	r.Auth.GET("/files/shared-with-me", h.ListSharedWithMe)
	r.Auth.POST("/files/upload-url", h.PresignUpload)
	r.Auth.POST("/files/complete", h.CompleteUpload)
	r.Auth.GET("/files/:id", h.Get)
	r.Auth.PATCH("/files/:id", h.UpdateMetadata)
	r.Auth.GET("/files/:id/url", h.DownloadURL)
	r.Auth.DELETE("/files/:id", h.Delete)
	r.Auth.POST("/files/:id/shares", h.Share)
	r.Auth.DELETE("/files/:id/shares/:uid", h.Unshare)
	if h.downloader != nil {
		r.Public.GET("/files/dl", h.Download)
	}
}

func (h *FileHandler) Upload(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "missing 'file' form field"))
		return
	}
	src, err := fh.Open()
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "cannot read uploaded file"))
		return
	}
	defer func() { _ = src.Close() }()

	vis := domain.VisibilityPrivate
	if c.PostForm("visibility") == "1" {
		vis = domain.VisibilityPublic
	}
	name := c.PostForm("name")
	if name == "" {
		name = fh.Filename
	}
	created, err := h.files.Create(c.Request.Context(), CreateFileInput{
		Name:       name,
		MimeType:   fh.Header.Get("Content-Type"),
		Size:       fh.Size,
		Visibility: vis,
		Reader:     src,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, fileToResponse(created))
}

func (h *FileHandler) PresignUpload(c *gin.Context) {
	var body struct {
		Name       string         `json:"name"`
		MimeType   string         `json:"mime_type"`
		Visibility int16          `json:"visibility"`
		Meta       map[string]any `json:"meta"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "invalid request body"))
		return
	}
	res, err := h.files.PresignUpload(c.Request.Context(), PresignUploadInput{
		Name:       body.Name,
		MimeType:   body.MimeType,
		Visibility: domain.Visibility(body.Visibility),
		Meta:       body.Meta,
	}, h.urlTTL)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"file_id":     res.FileID.String(),
		"upload_url":  res.UploadURL,
		"storage_key": res.StorageKey,
	})
}

func (h *FileHandler) CompleteUpload(c *gin.Context) {
	var body struct {
		FileID     string         `json:"file_id"`
		StorageKey string         `json:"storage_key"`
		Name       string         `json:"name"`
		MimeType   string         `json:"mime_type"`
		Visibility int16          `json:"visibility"`
		Meta       map[string]any `json:"meta"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "invalid request body"))
		return
	}
	fileID, err := idgen.Parse(body.FileID)
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("request.invalid_id", "invalid file_id"))
		return
	}
	created, err := h.files.CompleteUpload(c.Request.Context(), CompleteUploadInput{
		FileID:     fileID,
		StorageKey: body.StorageKey,
		Name:       body.Name,
		MimeType:   body.MimeType,
		Visibility: domain.Visibility(body.Visibility),
		Meta:       body.Meta,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, fileToResponse(created))
}

func (h *FileHandler) List(c *gin.Context) {
	list, err := h.files.List(c.Request.Context(), c.Query("owner_uid"), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]fileResponse, 0, len(list.Files))
	for _, f := range list.Files {
		items = append(items, fileToResponse(f))
	}
	httpx.OK(c, gin.H{"files": items, "next_cursor": list.NextCursor})
}

func (h *FileHandler) Get(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	f, err := h.files.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, fileToResponse(f))
}

func (h *FileHandler) UpdateMetadata(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var body struct {
		Name       *string        `json:"name"`
		Visibility *int16         `json:"visibility"`
		Meta       map[string]any `json:"meta"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "invalid request body"))
		return
	}
	in := UpdateFileMetadataInput{Name: body.Name, Meta: body.Meta}
	if body.Visibility != nil {
		v := domain.Visibility(*body.Visibility)
		in.Visibility = &v
	}
	f, err := h.files.UpdateMetadata(c.Request.Context(), id, in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, fileToResponse(f))
}

func (h *FileHandler) DownloadURL(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	url, err := h.files.DownloadURL(c.Request.Context(), id, h.urlTTL)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"url": url})
}

func (h *FileHandler) Delete(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.files.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *FileHandler) Share(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var body struct {
		UIDs []string `json:"uids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("file.invalid", "invalid request body"))
		return
	}
	if err := h.files.ShareFile(c.Request.Context(), id, body.UIDs); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *FileHandler) Unshare(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.files.UnshareFile(c.Request.Context(), id, c.Param("uid")); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *FileHandler) ListSharedWithMe(c *gin.Context) {
	list, err := h.files.ListSharedWithMe(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]fileResponse, 0, len(list.Files))
	for _, f := range list.Files {
		items = append(items, fileToResponse(f))
	}
	httpx.OK(c, gin.H{"files": items, "next_cursor": list.NextCursor})
}

func (h *FileHandler) Download(c *gin.Context) {
	if err := h.downloader.VerifyPresignedURL(c.Request.URL.String(), time.Now()); err != nil {
		httpx.WriteError(c, apperr.Forbidden("file.bad_signature", "invalid or expired download link"))
		return
	}
	rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
	if err != nil {
		httpx.WriteError(c, apperr.NotFound("file.not_found", "file not found"))
		return
	}
	defer func() { _ = rc.Close() }()
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, rc)
}

type fileResponse struct {
	ID          string         `json:"id"`
	OwnerUID    string         `json:"owner_uid"`
	Name        string         `json:"name"`
	Size        int64          `json:"size"`
	MimeType    string         `json:"mime_type"`
	Visibility  int16          `json:"visibility"`
	StorageType string         `json:"storage_type"`
	Meta        map[string]any `json:"meta,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func fileToResponse(f *File) fileResponse {
	return fileResponse{
		ID:          f.ID.String(),
		OwnerUID:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		Visibility:  int16(f.Visibility),
		StorageType: f.StorageType,
		Meta:        f.Meta,
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
}
