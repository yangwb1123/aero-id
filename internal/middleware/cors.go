package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS applies permissive defaults when origins is empty, otherwise reflects only
// allow-listed origins. Handles the OPTIONS preflight with 204 for allowed origins
// and 403 for disallowed origins.
func CORS(origins []string) gin.HandlerFunc {
	allowAll := len(origins) == 0
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			_, originAllowed := allowed[origin]
			switch {
			case allowAll:
				c.Header("Access-Control-Allow-Origin", "*")
			case originAllowed:
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			default:
				// disallowed origin: reject preflight outright, let real requests through without CORS headers
				if c.Request.Method == http.MethodOptions {
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
			}
			if allowAll || originAllowed {
				c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,"+RequestIDHeader)
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
