package middleware

import (
	"bytes"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/idempotency"
)

const defaultIdempotencyTTL = 24 * time.Hour

// Idempotency makes POST requests that carry an Idempotency-Key safe to retry: the
// first request for a (actor, endpoint, key) runs the handler and its response is
// stored; identical retries replay that stored response without re-executing. A
// concurrent duplicate still in flight gets 409. Store errors fail open so the
// idempotency layer can never take writes down.
func Idempotency(store idempotency.Store, ttl time.Duration) gin.HandlerFunc {
	if ttl <= 0 {
		ttl = defaultIdempotencyTTL
	}
	return func(c *gin.Context) {
		key := c.GetHeader("Idempotency-Key")
		if c.Request.Method != http.MethodPost || key == "" {
			c.Next()
			return
		}
		uid := c.GetString("actor_uid")
		endpoint := c.Request.Method + " " + c.FullPath()
		ctx := c.Request.Context()

		rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
		switch {
		case err != nil:
			c.Next() // fail open
		case reserved:
			cw := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
			c.Writer = cw
			c.Next()
			if status := c.Writer.Status(); status >= http.StatusInternalServerError {
				_ = store.Release(ctx, uid, endpoint, key) // let the client retry a server error
			} else {
				_ = store.Complete(ctx, uid, endpoint, key, idempotency.Record{
					Status:      status,
					ContentType: c.Writer.Header().Get("Content-Type"),
					Body:        cw.body.Bytes(),
				}, ttl)
			}
		case rec.Completed:
			replayIdempotent(c, rec)
		default:
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
				"code":    "idempotency.in_progress",
				"message": "a request with this Idempotency-Key is still being processed",
			}})
		}
	}
}

func replayIdempotent(c *gin.Context, rec *idempotency.Record) {
	ct := rec.ContentType
	if ct == "" {
		ct = "application/json; charset=utf-8"
	}
	c.Header("Idempotent-Replayed", "true")
	c.Data(rec.Status, ct, rec.Body)
	c.Abort()
}

// captureWriter tees the response body into a buffer so a successful response can be
// stored for later idempotent replay.
type captureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}
