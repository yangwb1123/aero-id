package http_test

import (
	"context"
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

type exportDownloadHTTPStub struct {
	service.SyncJobService
	fail    error
	calls   int
	oneTime bool
}

func (s *exportDownloadHTTPStub) DownloadExport(_ context.Context, _ domain.ID, oneTime bool) ([]byte, error) {
	s.calls++
	s.oneTime = oneTime
	return []byte(`{"schema_version":1}`), s.fail
}

func TestExportDownloadHTTPNeverCachesContentOrAdmissionErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"success", "forbidden", "invalid id"} {
		t.Run(mode, func(t *testing.T) {
			stub := &exportDownloadHTTPStub{}
			id, code := idgen.NewID().String(), http.StatusOK
			switch mode {
			case "forbidden":
				stub.fail = apperr.Forbidden("export.forbidden", "current export permission is required")
				code = http.StatusForbidden
			case "invalid id":
				id, code = "invalid-id", http.StatusBadRequest
			}
			engine := gin.New()
			httpapi.NewSyncJobHandler(stub, nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/sync/jobs/"+id+"/export/download?one_time=true", nil))
			if response.Code != code || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("download content/admission error was not explicitly non-cacheable")
			}
			if mode == "invalid id" {
				if stub.calls != 0 {
					t.Fatal("invalid ID reached service")
				}
			} else if stub.calls != 1 || !stub.oneTime {
				t.Fatal("handler changed one-time intent or repeated admission")
			}
			if mode == "success" && response.Body.String() != `{"schema_version":1}` {
				t.Fatal("download representation changed")
			}
		})
	}
}
