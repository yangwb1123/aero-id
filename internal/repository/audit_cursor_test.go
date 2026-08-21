package repository

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestEncodeAuditCursorAtRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 34, 56, 123456789, time.FixedZone("local", -8*60*60))
	id := idgen.NewID().String()
	encoded := encodeAuditCursorAt(at, id)
	decoded, err := decodeAuditCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if !decoded.OccurredAt.Equal(at.UTC()) || decoded.ID != id {
		t.Fatalf("decoded = %#v, want time=%s id=%s", decoded, at.UTC(), id)
	}
}

func TestEncodeAuditCursorAtRejectsIncompleteValues(t *testing.T) {
	if got := encodeAuditCursorAt(time.Time{}, idgen.NewID().String()); got != "" {
		t.Fatalf("zero timestamp cursor = %q, want empty", got)
	}
	if got := encodeAuditCursorAt(time.Now(), ""); got != "" {
		t.Fatalf("empty id cursor = %q, want empty", got)
	}
}

func TestDecodeAuditCursorRejectsMalformed(t *testing.T) {
	for _, value := range []string{"", "not-base64", "aW52YWxpZA"} {
		if _, err := decodeAuditCursor(value); err == nil {
			t.Fatalf("decode %q unexpectedly succeeded", value)
		}
	}
}
