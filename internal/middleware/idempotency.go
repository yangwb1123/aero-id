package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/idempotency"
)

const defaultIdempotencyTTL = 24 * time.Hour

// idempotencySettlementTimeout keeps response persistence alive briefly after
// a client disconnects. The mutation may already have committed when the
// request context is canceled; using that canceled context here would leave a
// reclaimable reservation and let the retry execute the mutation again.
const idempotencySettlementTimeout = 5 * time.Second

// maxIdempotencyCaptureBytes bounds the buffered response per keyed POST.
// Larger responses are delivered intact but never stored: the reservation is
// released so a retry re-executes the mutation (see IdempotencyObserver.
// Overflowed). In-bounds responses keep the full exactly-once guarantee.
const maxIdempotencyCaptureBytes = 1 << 20 // 1 MiB

// IdempotencyObserver observes idempotency middleware failures and degrades.
// A nil observer is a no-op; implementations must not block the request path
// (the middleware guards nil and recovers panics before invoking).
type IdempotencyObserver interface {
	CompleteFailed(err error) // store.Complete failed on the success path
	ReleaseFailed(err error)  // store.Release failed on the 5xx or overflow path
	Overflowed()              // response capture exceeded maxIdempotencyCaptureBytes
}

// Idempotency makes POST requests that carry an Idempotency-Key safe to retry: the
// first request for a (actor, concrete request path, key) runs the handler and
// its response is stored; identical retries replay that stored response without
// re-executing. A concurrent duplicate still in flight gets 409. Store errors
// fail open so the idempotency layer can never take writes down; failures and
// degrades are reported to obs (a nil observer is a no-op).
func Idempotency(store idempotency.Store, ttl time.Duration, obs IdempotencyObserver) gin.HandlerFunc {
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
		// Scope a key to the concrete resource path, not Gin's route template.
		// A callback replay key for /deliveries/A must never replay the response
		// for /deliveries/B. Query parameters are part of the command identity as
		// well; keyed POST handlers must not silently alias distinct requests.
		endpoint := c.Request.Method + " " + c.Request.URL.EscapedPath()
		if c.Request.URL.RawQuery != "" {
			endpoint += "?" + c.Request.URL.RawQuery
		}
		ctx := c.Request.Context()

		var rec *idempotency.Record
		var reserved bool
		var leaseToken string
		var err error
		if leased, ok := store.(idempotency.LeaseStore); ok {
			rec, reserved, leaseToken, err = leased.ReserveLease(ctx, uid, endpoint, key)
		} else {
			rec, reserved, err = store.Reserve(ctx, uid, endpoint, key)
		}
		handleIdempotencyReservation(c, store, obs, uid, endpoint, key, ttl, leaseToken, rec, reserved, err)

	}
}

// finalizeIdempotency settles a reservation after the handler ran: store the
// in-bounds response, or release on 5xx/overflow so a retry re-executes. Store
// errors are reported to obs, never silently discarded.
func handleIdempotencyReservation(c *gin.Context, store idempotency.Store, obs IdempotencyObserver,
	uid, endpoint, key string, ttl time.Duration, leaseToken string, rec *idempotency.Record, reserved bool, reserveErr error) {
	if reserveErr != nil || rec == nil && !reserved {
		c.Next()
		return
	}
	if reserved {
		cw := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = cw
		c.Next()
		finalizeIdempotency(c, cw, obs, store, uid, endpoint, key, ttl, leaseToken)
		return
	}
	if rec.Completed {
		if isCallbackRegistrationEndpoint(endpoint) {
			if err := validateReplayBody(rec.Body); err != nil {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
					"code": "idempotency.unsafe_legacy_record", "message": "idempotent callback response is unavailable",
				}})
				return
			}
		}
		replayIdempotent(c, rec)
		return
	}
	c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
		"code": "idempotency.in_progress", "message": "a request with this Idempotency-Key is still being processed",
	}})
}

func finalizeIdempotency(c *gin.Context, cw *captureWriter, obs IdempotencyObserver,
	store idempotency.Store, uid, endpoint, key string, ttl time.Duration, leaseToken string) {
	status := c.Writer.Status()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), idempotencySettlementTimeout)
	defer cancel()
	required, replayBody := idempotencyReplayPolicy(c)
	if cw != nil && cw.overflowed {
		notify(obs, func() { obs.Overflowed() })
		releaseReservation(ctx, store, obs, uid, endpoint, key, leaseToken)
		return
	}
	if status >= http.StatusInternalServerError {
		releaseReservation(ctx, store, obs, uid, endpoint, key, leaseToken)
		return
	}
	if required {
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			err := completeExplicitReplay(ctx, store, uid, endpoint, key, leaseToken, status,
				c.Writer.Header().Get("Content-Type"), replayBody, ttl)
			notifyCompleteFailure(obs, err)
			return
		}
		// A marked response without a successful safe representation must never
		// persist the captured body. Retain the reservation conservatively: a
		// callback registration may already have succeeded before the response
		// failed, so releasing it could duplicate the source-side mutation.
		notifyCompleteFailure(obs, errors.New("idempotency safe replay body unavailable"))
		return
	}
	completeCapturedReplay(ctx, store, obs, uid, endpoint, key, leaseToken, status,
		c.Writer.Header().Get("Content-Type"), capturedBody(cw), ttl)
}

func releaseReservation(ctx context.Context, store idempotency.Store, obs IdempotencyObserver,
	uid, endpoint, key, leaseToken string) {
	var err error
	if leased, ok := store.(idempotency.LeaseStore); ok && leaseToken != "" {
		err = leased.ReleaseLease(ctx, uid, endpoint, key, leaseToken)
	} else {
		err = store.Release(ctx, uid, endpoint, key)
	}
	if err != nil {
		notify(obs, func() { obs.ReleaseFailed(err) })
	}
}

func completeCapturedReplay(ctx context.Context, store idempotency.Store, obs IdempotencyObserver,
	uid, endpoint, key, leaseToken string, status int, contentType string, body []byte, ttl time.Duration) {
	record := idempotency.Record{Status: status, ContentType: contentType, Body: body}
	var err error
	if leased, ok := store.(idempotency.LeaseStore); ok && leaseToken != "" {
		err = leased.CompleteLease(ctx, uid, endpoint, key, leaseToken, record, ttl)
	} else {
		err = store.Complete(ctx, uid, endpoint, key, record, ttl)
	}
	notifyCompleteFailure(obs, err)
}

func capturedBody(cw *captureWriter) []byte {
	if cw == nil || cw.body == nil {
		return nil
	}
	return cw.body.Bytes()
}

func notifyCompleteFailure(obs IdempotencyObserver, err error) {
	if err != nil {
		notify(obs, func() { obs.CompleteFailed(err) })
	}
}

// notify delivers one observation to obs. A nil observer is a no-op; a panic
// inside the callback is recovered so an observer bug can never alter the
// request path.
func completeExplicitReplay(ctx context.Context, store idempotency.Store, uid, endpoint, key, leaseToken string,
	status int, contentType string, body []byte, ttl time.Duration) error {
	if err := validateReplayBody(body); err != nil {
		return errors.Join(errors.New("explicit idempotency replay body rejected"), err)
	}
	record := idempotency.Record{Status: status, ContentType: contentType, Body: append([]byte(nil), body...)}
	if leased, ok := store.(idempotency.LeaseStore); ok && leaseToken != "" {
		return leased.CompleteLease(ctx, uid, endpoint, key, leaseToken, record, ttl)
	}
	return store.Complete(ctx, uid, endpoint, key, record, ttl)
}

func notify(obs IdempotencyObserver, fn func()) {
	if obs == nil {
		return
	}
	defer func() { _ = recover() }()
	fn()
}

func isCallbackRegistrationEndpoint(endpoint string) bool {
	return endpoint == "POST /v1/admin/callback-subscriptions"
}

func replayIdempotent(c *gin.Context, rec *idempotency.Record) {
	ct := rec.ContentType
	if ct == "" {
		ct = "application/json; charset=utf-8"
	}
	c.Header("Idempotent-Replayed", "true")
	c.Header("Cache-Control", "no-store")
	c.Data(rec.Status, ct, rec.Body)
	c.Abort()
}

// captureWriter tees the response body into a bounded buffer so a successful
// in-bounds response can be stored for later idempotent replay. Bytes beyond
// maxIdempotencyCaptureBytes are still forwarded to the client unchanged but
// never buffered (overflowed), bounding memory per request regardless of
// response size or concurrency.
type captureWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	overflowed bool
}

func (w *captureWriter) capture(b []byte) {
	if w.overflowed || len(b) == 0 {
		return
	}
	remaining := maxIdempotencyCaptureBytes - w.body.Len()
	if remaining <= 0 {
		w.overflowed = true
		return
	}
	if len(b) > remaining {
		w.overflowed = true
		w.body.Write(b[:remaining])
		return
	}
	w.body.Write(b)
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.capture(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}
