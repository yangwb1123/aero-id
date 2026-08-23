package http_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/gin-gonic/gin"
)

type regionHandlerFencing struct {
	target string
	epoch  int64
}

func (f *regionHandlerFencing) Directory(context.Context, string, domain.ID) (*repository.RegionDirectory, error) {
	return nil, repository.ErrNotFound
}

func (f *regionHandlerFencing) Promote(_ context.Context, _ string, _ domain.ID,
	target string, expected int64) (int64, error) {
	f.target, f.epoch = target, expected
	return 8, nil
}

func (f *regionHandlerFencing) RegionEpoch(context.Context, string) (int64, error) {
	return 0, nil
}

func TestPromoteRegionPinsResponseToNewWriter(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	fencing := &regionHandlerFencing{}
	handler := httpapi.NewAccountHandlerWithExtensions(nil, fencing, nil, nil, nil)
	router := gin.New()
	router.POST("/v1/admin/regions/promote", func(c *gin.Context) {
		identity := &domain.TokenIdentity{UID: "admin", TenantID: "tenant-a"}
		c.Request = c.Request.WithContext(service.WithIdentity(c.Request.Context(), identity))
		handler.PromoteRegion(c)
	})
	body := []byte(`{"account_id":"018f47d2-a5d1-7b33-b444-000000000001","target_region":"eu-west","expected_epoch":7}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/regions/promote", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Aero-Write-Epoch"); got != "eu-west:8" {
		t.Fatalf("write epoch = %q, want eu-west:8", got)
	}
	if fencing.target != "eu-west" || fencing.epoch != 7 {
		t.Fatalf("promote input = %q/%d, want eu-west/7", fencing.target, fencing.epoch)
	}
}
