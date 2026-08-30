//go:build integration

package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestVerifyOutboxRetryEligibilityTracksDeployedSchema(t *testing.T) {
	db := testpg.Start(t)
	root := filepath.Join(testpg.RepoRoot(t), "migrations")
	testpg.Migrate(t, db, root)

	if err := migrate.VerifyOutboxRetryEligibility(db); err != nil {
		t.Fatalf("fully migrated schema rejected: %v", err)
	}

	down, err := os.ReadFile(filepath.Join(root, "000069_outbox_retry_eligibility.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(down)).Error; err != nil {
		t.Fatalf("down 000069: %v", err)
	}
	if err := migrate.VerifyOutboxRetryEligibility(db); err == nil {
		t.Fatal("pre-000069 schema accepted by new relay compatibility check")
	}
}
