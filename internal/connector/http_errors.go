package connector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

func (c Source) String() string { return string(c) }

func requestTimedOut(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return true
	}
	// http.Client.Timeout can cancel its private request context a few
	// instructions before the caller's context observes the deadline. In that
	// race net/http may return a redacted/generic read error. The caller's
	// deadline is still the authoritative timeout budget, so treat an error
	// observed after it as a timeout as well.
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return true
		}
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func remoteHTTPError(source Source, code int) *RemoteError {
	kind := ErrorUnavailable
	temporary := code == http.StatusTooManyRequests || code >= 500
	switch code {
	case http.StatusUnauthorized:
		kind = ErrorUnauthorized
	case http.StatusForbidden:
		kind = ErrorForbidden
	case http.StatusRequestTimeout:
		kind = ErrorTimeout
	case http.StatusBadRequest, http.StatusNotFound:
		kind = ErrorInvalid
	default:
		if code >= http.StatusBadRequest && code < http.StatusInternalServerError && code != http.StatusTooManyRequests {
			kind = ErrorInvalid
		}
	}
	return &RemoteError{Source: source, Kind: kind, Code: fmt.Sprintf("source.http_%d", code), Temporary: temporary}
}

func timePtr(value time.Time) *time.Time { return &value }
