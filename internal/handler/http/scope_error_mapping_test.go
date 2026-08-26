package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
)

type operationHandlerStub struct {
	service.OperationService
	calls int
}

func (s *operationHandlerStub) Create(_ context.Context, _ service.OperationInput) (*domain.Operation, error) {
	s.calls++
	return nil, nil
}

type syncJobHandlerStub struct {
	service.SyncJobService
	calls int
}

func (s *syncJobHandlerStub) Create(_ context.Context, _ service.SyncJobInput) (*domain.SyncJob, error) {
	s.calls++
	return nil, nil
}

func TestOperationHandlerMalformedJSONMapsToBadRequest(t *testing.T) {
	stub := &operationHandlerStub{}
	handler := NewOperationHandler(stub)
	context, recorder := testJSONContext(http.MethodPost, "/v1/operations", `{"steps":`)
	handler.Create(context)
	assertHTTPErrorCode(t, recorder, http.StatusBadRequest, "operation.invalid_request")
	if stub.calls != 0 {
		t.Fatalf("service calls = %d, want 0", stub.calls)
	}
}

func TestSyncJobHandlerMalformedDatasetArrayMapsToBadRequest(t *testing.T) {
	stub := &syncJobHandlerStub{}
	handler := NewSyncJobHandler(stub, nil)
	context, recorder := testJSONContext(http.MethodPost, "/v1/me/erase", `{"datasets":[7]}`)
	handler.create(context, domain.SyncJobErase, domain.ID{})
	assertHTTPErrorCode(t, recorder, http.StatusBadRequest, "sync.invalid_request")
	if stub.calls != 0 {
		t.Fatalf("service calls = %d, want 0", stub.calls)
	}
}

func testJSONContext(method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	return context, recorder
}

func assertHTTPErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("HTTP status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q", body.Error.Code, wantCode)
	}
}
