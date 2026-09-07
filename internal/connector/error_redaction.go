package connector

import (
	"context"
	"errors"
)

// redactedRemoteCause preserves cancellation semantics without retaining URL,
// header, credential, or dialer details from net/http and crypto/tls errors.
func redactedRemoteCause(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("remote dependency operation failed")
}
