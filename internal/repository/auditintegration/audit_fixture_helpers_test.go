//go:build integration

package auditintegration

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
)

// These four private fixtures retain the original repository test bodies.
// The Docker-free relocation guard checks their ASTs against the root copies;
// no production helper or fixture sharing is introduced across test processes.
func auditWiringFact(eventID domain.ID, tenantID, action string) *domain.AuditFact {
	return &domain.AuditFact{
		EventID:      eventID,
		Source:       "aero-im",
		SourceRegion: "us-west",
		PartitionKey: "p-1",
		TenantID:     tenantID,
		ActorUID:     "actor-1",
		Action:       action,
		ResourceType: "profile",
		ResourceID:   "r-1",
		OccurredAt:   time.Now().UTC(),
		Payload:      map[string]any{"k": "v"},
	}
}

func countAuditLedgerRows(t *testing.T, db *gorm.DB, eventID domain.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&n).Error; err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func countAuditPublicationRows(t *testing.T, db *gorm.DB, eventID domain.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&n).Error; err != nil {
		t.Fatalf("count audit publication rows: %v", err)
	}
	return n
}

func installFailPublicationInsertTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE FUNCTION fail_publication_insert() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'publication insert blocked (test)';
		END;
		$$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_publication_insert_trigger
		BEFORE INSERT ON audit_publications FOR EACH ROW EXECUTE FUNCTION fail_publication_insert()`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DROP TRIGGER IF EXISTS fail_publication_insert_trigger ON audit_publications`).Error
		_ = db.Exec(`DROP FUNCTION IF EXISTS fail_publication_insert()`).Error
	})
}
