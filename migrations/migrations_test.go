//go:build integration

package migrations_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestMigrationsUpAndDown applies all *.up.sql files in filename order against a
// throwaway Postgres container, asserts the three expected tables exist, then
// applies the *.down.sql files in reverse order and confirms clean rollback.
func TestMigrationsUpAndDown(t *testing.T) {
	db := testpg.Start(t)

	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)

	upFiles := []string{
		"000001_create_files.up.sql",
		"000002_create_messages.up.sql",
		"000003_create_comments.up.sql",
	}
	downFiles := []string{
		"000003_create_comments.down.sql",
		"000002_create_messages.down.sql",
		"000001_create_files.down.sql",
	}

	// Apply UP migrations.
	for _, name := range upFiles {
		sql, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if res := db.Exec(string(sql)); res.Error != nil {
			t.Fatalf("exec %s: %v", name, res.Error)
		}
	}

	// Assert all three tables exist.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	for _, table := range []string{"files", "messages", "comments"} {
		var ok bool
		if err := sqlDB.QueryRow(
			`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`,
			table,
		).Scan(&ok); err != nil {
			t.Fatalf("query table %q: %v", table, err)
		}
		if !ok {
			t.Fatalf("table %q missing after up migrations", table)
		}
		t.Logf("table %q: present", table)
	}

	// Apply DOWN migrations (reverse order).
	for _, name := range downFiles {
		sql, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if res := db.Exec(string(sql)); res.Error != nil {
			t.Fatalf("exec %s: %v", name, res.Error)
		}
	}

	// Assert all three tables are gone.
	for _, table := range []string{"files", "messages", "comments"} {
		var ok bool
		if err := sqlDB.QueryRow(
			`SELECT EXISTS(SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`,
			table,
		).Scan(&ok); err != nil {
			t.Fatalf("query table %q after down: %v", table, err)
		}
		if ok {
			t.Fatalf("table %q still present after down migrations", table)
		}
		t.Logf("table %q: gone (rollback confirmed)", table)
	}
}
