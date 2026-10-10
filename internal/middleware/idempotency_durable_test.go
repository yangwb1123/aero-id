package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/middleware"
)

func TestDurableCreateCommandsAlwaysReachService(t *testing.T) {
	paths := []struct{ route, concrete string }{
		{"/v1/operations", "/v1/operations"},
		{"/v1/me/sync", "/v1/me/sync"},
		{"/v1/me/export", "/v1/me/export"},
		{"/v1/me/erase", "/v1/me/erase"},
		{"/v1/audit/export", "/v1/audit/export"},
		{"/v1/audit/replays", "/v1/audit/replays"},
		{"/v1/admin/accounts/:id/sync", "/v1/admin/accounts/account-1/sync"},
		{"/v1/admin/accounts/:id/export", "/v1/admin/accounts/account-1/export"},
		{"/v1/admin/accounts/:id/erase", "/v1/admin/accounts/account-1/erase"},
	}
	for _, tc := range paths {
		t.Run(tc.route, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			store := newMemStore()
			calls := 0
			router := gin.New()
			router.Use(middleware.Idempotency(store, time.Hour, nil))
			router.POST(tc.route, func(c *gin.Context) {
				calls++
				if calls == 1 {
					c.JSON(http.StatusCreated, gin.H{"data": gin.H{"id": "durable-1"}})
				} else {
					c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "sync.idempotency_conflict"}})
				}
			})
			for i := 0; i < 2; i++ {
				request := httptest.NewRequest(http.MethodPost, tc.concrete, strings.NewReader(`{"scope":"changed"}`))
				request.Header.Set("Idempotency-Key", "key-1")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if i == 1 && recorder.Code != http.StatusConflict {
					t.Fatalf("second request = %d, cache hid service conflict", recorder.Code)
				}
			}
			if calls != 2 || store.completeCalls != 0 {
				t.Fatalf("handler/response-cache writes = %d/%d, want 2/0", calls, store.completeCalls)
			}
		})
	}
}
