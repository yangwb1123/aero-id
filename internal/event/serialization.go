package event

import (
	"encoding/json"
	"errors"
	"fmt"
)

// marshalCanonicalEnvelope returns the only wire representation used by event
// publishers. Validation is performed against the caller's envelope, while
// encoding uses a bounded copy so transport serialization cannot leak source
// credentials, content, or object data.
func marshalCanonicalEnvelope(envelope *Envelope) ([]byte, error) {
	if err := envelope.Validate(); err != nil {
		return nil, fmt.Errorf("invalid canonical envelope: %w", err)
	}

	body, err := json.Marshal(BoundedEnvelope(envelope))
	if err != nil {
		// Do not expose json.Marshal's error: custom marshalers can include
		// source payload values in their error text.
		return nil, errors.New("marshal canonical envelope")
	}
	return body, nil
}
