package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type projectionReplayCapture struct {
	input service.ReplayProjectionInput
	calls int
}

func (s *projectionReplayCapture) ReplayProjection(_ context.Context,
	input service.ReplayProjectionInput) (*service.ProjectionReplayResult, error) {
	s.calls++
	s.input = input
	return &service.ProjectionReplayResult{AccountID: input.AccountID.String(), Source: input.Source,
		Completed: true}, nil
}

func TestProjectionReplayHandlerPassesOpaqueCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accountID, cursor := idgen.NewID(), idgen.NewID()
	capture := &projectionReplayCapture{}
	handler := NewReplayHandlerWithProjection(nil, capture, nil)
	router := gin.New()
	router.POST("/projection", handler.ReplayProjection)
	body := []byte(`{"account_id":"` + accountID.String() + `","source":"aero-id","cursor":"` +
		cursor.String() + `","limit":250}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/projection", bytes.NewReader(body)))

	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s, want 200", response.Code, response.Body.String())
	}
	if capture.calls != 1 || capture.input.AccountID != accountID || capture.input.Cursor != cursor ||
		capture.input.Source != "aero-id" || capture.input.Limit != 250 {
		t.Fatalf("captured input = %+v, calls=%d", capture.input, capture.calls)
	}
}

func TestProjectionReplayHandlerRejectsInvalidCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capture := &projectionReplayCapture{}
	handler := NewReplayHandlerWithProjection(nil, capture, nil)
	router := gin.New()
	router.POST("/projection", handler.ReplayProjection)
	body := []byte(`{"account_id":"` + idgen.NewID().String() +
		`","source":"aero-vault","cursor":"not-a-uuid"}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/projection", bytes.NewReader(body)))

	if response.Code != http.StatusBadRequest || capture.calls != 0 {
		t.Fatalf("status/calls/body = %d/%d/%s, want 400/0", response.Code, capture.calls, response.Body.String())
	}
}
