//go:build integration

package migrate_test

import (
	"testing"

	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestCommandRequestBindingRequiresWatermarkAndBothColumns(t *testing.T) {
	db := testpg.Start(t)
	if err := migrate.VerifyCommandRequestBinding(db); err == nil {
		t.Fatal("unmigrated database accepted")
	}
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	if err := migrate.VerifyCommandRequestBinding(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE sync_jobs DROP COLUMN request_fingerprint").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrate.VerifyCommandRequestBinding(db); err == nil {
		t.Fatal("missing fingerprint column accepted under a high watermark")
	}
}
