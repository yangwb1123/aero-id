package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type replayCursorService struct {
	input service.ReplayInput
}

func (s *replayCursorService) Create(_ context.Context,
	input service.ReplayInput) (*service.ReplayResult, error) {
	s.input = input
	return &service.ReplayResult{ReplayID: idgen.NewID(),
		Mode: input.Mode, Status: "completed"}, nil
}

func (*replayCursorService) Get(context.Context, domain.ID) (*domain.Operation, error) {
	return nil, nil
}

func TestReplayHandlerPassesOpaqueCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/audit/replays", bytes.NewBufferString(
		`{"mode":"forensic","account_id":"01991c87-8dc8-7b45-b868-8b741fc98400","from":"2026-08-20T10:00:00Z","to":"2026-08-21T10:00:00Z","cursor":"opaque-window-2","limit":25}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	service := &replayCursorService{}

	NewReplayHandler(service, nil).Create(ctx)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.input.Cursor != "opaque-window-2" || service.input.Limit != 25 {
		t.Fatalf("input = %#v", service.input)
	}
	if service.input.From == nil || service.input.To == nil ||
		service.input.From.Format(time.RFC3339) != "2026-08-20T10:00:00Z" ||
		service.input.To.Format(time.RFC3339) != "2026-08-21T10:00:00Z" {
		t.Fatalf("time window = %v to %v", service.input.From, service.input.To)
	}
}
