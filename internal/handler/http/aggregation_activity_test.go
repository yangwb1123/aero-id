package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
)

type activityAggregationHandlerFixture struct {
	service.AggregationService
	page domain.Page
}

func (f *activityAggregationHandlerFixture) ListMeActivity(_ context.Context, _ service.AggregationInput,
	page domain.Page) (*domain.AccountActivityPage, error) {
	f.page = page
	return &domain.AccountActivityPage{Events: []map[string]any{{"event_id": "event-2"}},
		NextCursor: "event-2", Partial: true, GeneratedAt: time.Unix(1, 0).UTC(),
		SourceErrors:  map[string]string{"aero-im": "source.unavailable"},
		StaleDatasets: []domain.AccountDataset{domain.DatasetAeroIMActivitySummary}}, nil
}

func TestAggregationActivityHandlerReturnsPagedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fixture := &activityAggregationHandlerFixture{}
	handler := NewAggregationHandler(fixture, nil)
	router := gin.New()
	router.GET("/v1/me/activity", handler.GetMeActivity)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/v1/me/activity?cursor=event-3&limit=7", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Events        []map[string]any  `json:"events"`
			NextCursor    string            `json:"next_cursor"`
			Partial       bool              `json:"partial"`
			SourceErrors  map[string]string `json:"source_errors"`
			StaleDatasets []string          `json:"stale_datasets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if fixture.page.Cursor != "event-3" || fixture.page.Limit != 7 {
		t.Fatalf("page = %+v", fixture.page)
	}
	if len(envelope.Data.Events) != 1 || envelope.Data.NextCursor != "event-2" || !envelope.Data.Partial {
		t.Fatalf("response = %+v", envelope.Data)
	}
	if envelope.Data.SourceErrors["aero-im"] != "source.unavailable" || len(envelope.Data.StaleDatasets) != 1 {
		t.Fatalf("freshness metadata = %+v", envelope.Data)
	}
}
