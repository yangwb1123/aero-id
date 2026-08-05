package service

import (
	"errors"
	"testing"
	"time"
)

func TestPublicationOutcomeKeepsUnboundTenantRecoverable(t *testing.T) {
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	delay := 15 * time.Minute

	status, code, next := publicationOutcome(errAuditTenantUnbound, 99, 8, now, delay)
	if status != "pending" || code != "audit.binding_unavailable" || !next.Equal(now.Add(delay)) {
		t.Fatalf("unexpected unbound outcome: status=%q code=%q next=%s", status, code, next)
	}
}

func TestPublicationOutcomeDeadLettersBoundPublisherFailure(t *testing.T) {
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	status, code, _ := publicationOutcome(errors.New("transport"), 8, 8, now, time.Minute)
	if status != "dead_letter" || code != "audit.publication_failed" {
		t.Fatalf("unexpected publisher failure outcome: status=%q code=%q", status, code)
	}
}
