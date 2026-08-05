package repository

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestAuditRecordHashBindsRequestIDInVersionTwo(t *testing.T) {
	base := &domain.AuditFact{ID: idgen.NewID(), HashVersion: 2, ChainSequence: 1, Source: "aero-id",
		SourceRegion: "local", PartitionKey: "account-1", EventID: idgen.NewID(), RequestID: "request-1",
		Action: "account.viewed", OccurredAt: time.Unix(1, 0).UTC(), CreatedAt: time.Unix(2, 0).UTC(), PayloadHash: "payload"}
	first := auditRecordHash(base)
	base.RequestID = "request-2"
	if first == auditRecordHash(base) {
		t.Fatal("version-two audit hash does not bind request id")
	}
}

func TestAuditLedgerFactToRowPreservesHashVersion(t *testing.T) {
	fact := &domain.AuditFact{ID: idgen.NewID(), HashVersion: 2, Source: "aero-id", SourceRegion: "local",
		PartitionKey: "p", Action: "test", OccurredAt: time.Now().UTC()}
	row := auditLedgerFactToRow(fact, []byte(`{}`))
	if row.HashVersion != 2 {
		t.Fatalf("row hash version = %d, want 2", row.HashVersion)
	}
}
