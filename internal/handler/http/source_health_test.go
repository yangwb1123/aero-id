package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/connector"
)

// fakeHealthConnector reports source health on demand.
type fakeHealthConnector struct {
	source connector.Source
	err    error
}

func (f *fakeHealthConnector) Source() connector.Source { return f.source }
func (f *fakeHealthConnector) Health(ctx context.Context) error {
	return f.err
}
func (f *fakeHealthConnector) Summary(ctx context.Context, req connector.SummaryRequest) (*connector.Summary, error) {
	return &connector.Summary{Source: f.source}, nil
}

// TestSourceHealth_ReportsDegradedWithoutFailingReadiness: a source outage is
// reported on /health/sources but never affects /readyz (layered health).
func TestSourceHealth_ReportsDegradedWithoutFailingReadiness(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	connectors := []connector.Connector{
		&fakeHealthConnector{source: connector.SourceAeroIM},
		&fakeHealthConnector{source: connector.SourceAeroVault, err: context.DeadlineExceeded},
	}
	router := gin.New()
	router.GET("/readyz", func(c *gin.Context) { c.Status(http.StatusOK) })
	routes := httpapi.Routes{Public: router.Group("")}
	httpapi.NewSourceHealthHandler(connectors).RegisterRoutes(routes)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/sources", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health/sources = %d", rec.Code)
	}
	var body struct {
		Data struct {
			Sources []struct {
				Source string `json:"source"`
				Status string `json:"status"`
			} `json:"sources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	bySource := map[string]string{}
	for _, s := range body.Data.Sources {
		bySource[s.Source] = s.Status
	}
	if bySource["aero-im"] != "ok" {
		t.Errorf("aero-im status = %q, want ok", bySource["aero-im"])
	}
	if bySource["aero-vault"] != "degraded" {
		t.Errorf("aero-vault status = %q, want degraded", bySource["aero-vault"])
	}
}
