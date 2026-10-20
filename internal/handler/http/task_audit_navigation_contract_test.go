package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
)

func TestTaskAuditNavigationHTTPUsesPublishedIDFilters(t *testing.T) {
	id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
	for _, field := range []string{"operation_id", "request_id"} {
		t.Run(field, func(t *testing.T) {
			svc := &operationAuditReadStub{next: "opaque:next/1"}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: true}}
			engine := operationAuditRouter(svc, checker)
			value := id.String()
			if field == "request_id" {
				value = "original/request?part=1&marker=%+"
			}
			query := url.Values{field: {value}, "cursor": {"opaque:original/1"}, "limit": {"50"}}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/audit/events?"+query.Encode(), nil))
			if recorder.Code != http.StatusOK || len(svc.seen) != 1 {
				t.Fatalf("status/calls = %d/%d", recorder.Code, len(svc.seen))
			}
			filter := svc.seen[0]
			if filter.Page.Cursor != "opaque:original/1" || filter.Page.Limit != 50 {
				t.Fatalf("page filter = %+v", filter.Page)
			}
			if field == "request_id" && (filter.RequestID != value || filter.OperationID != uuid.Nil) {
				t.Fatalf("request filter was substituted/intersected: %+v", filter)
			}
			if field == "operation_id" && (filter.OperationID != id || filter.RequestID != "") {
				t.Fatalf("operation filter was substituted/intersected: %+v", filter)
			}
			assertTaskAuditEmptyPage(t, recorder)
		})
	}
}

func assertTaskAuditEmptyPage(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var envelope struct {
		Data struct {
			Events []json.RawMessage `json:"events"`
			Next   string            `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Events == nil || len(envelope.Data.Events) != 0 || envelope.Data.Next != "opaque:next/1" {
		t.Fatalf("query result = %+v", envelope.Data)
	}
}

func TestTaskAuditNavigationHTTPStillRequiresReadPermission(t *testing.T) {
	svc := &operationAuditReadStub{}
	checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: false}}
	engine := operationAuditRouter(svc, checker)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/audit/events?request_id=original", nil))
	if recorder.Code != http.StatusForbidden || len(svc.seen) != 0 {
		t.Fatalf("denied status/calls = %d/%d", recorder.Code, len(svc.seen))
	}
}
