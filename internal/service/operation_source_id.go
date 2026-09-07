package service

import (
	"strings"

	"github.com/aero/aero-id/internal/connector"
)

// retainSourceOperationID keeps the first source operation identifier as the
// durable identity of a step. A different identifier on a later attempt is
// not a new success: it is an uncorrelated response and must remain unknown.
func retainSourceOperationID(existing, returned string) (string, bool) {
	existing = strings.TrimSpace(existing)
	returned = strings.TrimSpace(returned)
	if existing != "" && returned != "" && existing != returned {
		return existing, false
	}
	if returned != "" {
		return returned, true
	}
	return existing, true
}

func sourceOperationIDConflict(source connector.Source) error {
	return &connector.RemoteError{Source: source, Kind: connector.ErrorUnknown,
		Code: "source.source_operation_id_mismatch", Temporary: true}
}
