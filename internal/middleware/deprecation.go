package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// LegacyDeprecation adds RFC 8594 deprecation headers to legacy compatibility
// routes (files/messages/comments and their satellites). The surface is kept
// as a compatibility source until the ADR-011 migration lands; the headers are
// non-breaking and purely informational.
//
// Usage:
//
//	legacy := r.Auth.Group("", middleware.LegacyDeprecation(sunsetDate))
//	legacy.GET("/files", ...)
func LegacyDeprecation(sunset string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isLegacyPath(c.FullPath()) {
			c.Header("Deprecation", "true")
			c.Header("Link", `</docs/plans/2026-08-04-distributed-account-aggregation.md>; rel="deprecation"`)
			if sunset != "" {
				c.Header("Sunset", sunset)
			}
		}
		c.Next()
	}
}

// legacyPrefixes are the compatibility-surface route prefixes under /v1.
var legacyPrefixes = []string{
	"/v1/files",
	"/v1/messages",
	"/v1/comments",
	"/v1/conversations",
	"/v1/notifications",
	"/v1/search",
	"/v1/blocks",
	"/v1/events",
}

func isLegacyPath(path string) bool {
	for _, prefix := range legacyPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
