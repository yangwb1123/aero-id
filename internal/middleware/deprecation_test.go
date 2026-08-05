package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLegacyDeprecation_HeadersOnLegacyRoutes(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	group := router.Group("/v1", LegacyDeprecation("2027-06-30"))
	group.GET("/files/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	group.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/v1/files/abc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("Deprecation") != "true" {
		t.Errorf("Deprecation header = %q, want true", rec.Header().Get("Deprecation"))
	}
	if rec.Header().Get("Sunset") != "2027-06-30" {
		t.Errorf("Sunset header = %q, want 2027-06-30", rec.Header().Get("Sunset"))
	}
	if rec.Header().Get("Link") == "" {
		t.Error("Link deprecation header missing")
	}
}

func TestLegacyDeprecation_NoHeadersOnControlPlane(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	group := router.Group("/v1", LegacyDeprecation("2027-06-30"))
	group.GET("/me", func(c *gin.Context) { c.Status(http.StatusOK) })
	group.GET("/operations/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/v1/me", "/v1/operations/abc"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Header().Get("Deprecation") != "" {
			t.Errorf("Deprecation header set on %s: %q", path, rec.Header().Get("Deprecation"))
		}
		if rec.Header().Get("Sunset") != "" {
			t.Errorf("Sunset header set on %s: %q", path, rec.Header().Get("Sunset"))
		}
	}
}

func TestIsLegacyPath(t *testing.T) {
	cases := map[string]bool{
		"/v1/files":                 true,
		"/v1/files/abc/url":         true,
		"/v1/messages":              true,
		"/v1/comments/abc/replies":  true,
		"/v1/conversations/u1":      true,
		"/v1/notifications":         true,
		"/v1/search":                true,
		"/v1/blocks":                true,
		"/v1/events":                true,
		"/v1/me":                    false,
		"/v1/me/overview":           false,
		"/v1/operations":            false,
		"/v1/audit/events":          false,
		"/v1/admin/accounts":        false,
		"/v1/account-datasets":      false,
		"/v1/files-shared":          false, // prefix must be path-segment aware
		"/v1/file-metadata":         false,
		"/v1/notifications-prefs":   false,
	}
	for path, want := range cases {
		if got := isLegacyPath(path); got != want {
			t.Errorf("isLegacyPath(%q) = %v, want %v", path, got, want)
		}
	}
}
