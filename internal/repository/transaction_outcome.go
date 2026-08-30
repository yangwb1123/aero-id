package repository

import (
	"errors"
	"fmt"
)

// ErrTransactionOutcomeUnknown means the database connection failed while the
// transaction was being committed. The server cannot safely infer whether the
// transaction committed or rolled back. Callers must reconcile durable state;
// they must not compensate a remote operation based on this error alone.
var ErrTransactionOutcomeUnknown = errors.New("database transaction outcome is unknown")

func transactionOutcome(commitErr, callbackErr error) error {
	if commitErr == nil {
		return nil
	}
	if callbackErr == nil {
		return fmt.Errorf("%w: %w", ErrTransactionOutcomeUnknown, commitErr)
	}
	return commitErr
}

func IsTransactionOutcomeUnknown(err error) bool {
	return errors.Is(err, ErrTransactionOutcomeUnknown)
}
