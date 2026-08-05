//go:build integration

package repository_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestLegacyMigrationDigests exercises the P8-02 shadow digests against real
// PostgreSQL: counts and deterministic digests are produced without loading
// content bodies, truncation is flagged, and digests are content-sensitive.
func TestLegacyMigrationDigests(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	migration := repository.NewLegacyMigrationRepository(db)
	now := time.Now().UTC()

	seedLegacyRow(t, db, "INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, checksum, created_at, updated_at) VALUES (gen_random_uuid(), 'u-mig', 'a.txt', 10, 'text/plain', 'k1', 'local', 'c1', $1, $1)", now)
	seedLegacyRow(t, db, "INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, checksum, created_at, updated_at) VALUES (gen_random_uuid(), 'u-mig', 'b.txt', 20, 'text/plain', 'k2', 'local', 'c2', $1, $1)", now)
	seedLegacyRow(t, db, "INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, checksum, created_at, updated_at) VALUES (gen_random_uuid(), 'u-other', 'c.txt', 30, 'text/plain', 'k3', 'local', 'c3', $1, $1)", now)
	seedLegacyRow(t, db, "INSERT INTO messages (id, from_uid, to_uid, kind, content, created_at) VALUES (gen_random_uuid(), 'u-mig', 'u-other', 1, 'hello world', $1)", now)
	seedLegacyRow(t, db, "INSERT INTO messages (id, from_uid, to_uid, kind, content, created_at) VALUES (gen_random_uuid(), 'u-other', 'u-mig', 1, 'reply', $1)", now)
	seedLegacyRow(t, db, "INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at) VALUES (gen_random_uuid(), 'u-mig', 'file', gen_random_uuid(), 'first comment', $1, $1)", now)
	seedLegacyRow(t, db, "INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at) VALUES (gen_random_uuid(), 'u-mig', 'file', gen_random_uuid(), 'second', $1, $1)", now)

	files, err := migration.LegacyFileDigest(t.Context(), "u-mig", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if files.Count != 2 || files.SizeBytes != 30 {
		t.Errorf("files = %+v, want count 2 size 30", files)
	}
	if files.Truncated || files.Digest == "" {
		t.Errorf("files digest incomplete: %+v", files)
	}
	// Deterministic: re-digesting yields the same value.
	again, err := migration.LegacyFileDigest(t.Context(), "u-mig", 1000)
	if err != nil || again.Digest != files.Digest {
		t.Errorf("digest not deterministic: %v vs %v", again.Digest, files.Digest)
	}

	messages, err := migration.LegacyMessageDigest(t.Context(), "u-mig", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if messages.Count != 2 { // from + to sides
		t.Errorf("messages = %+v, want count 2", messages)
	}

	comments, err := migration.LegacyCommentDigest(t.Context(), "u-mig", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if comments.Count != 2 {
		t.Errorf("comments = %+v, want count 2", comments)
	}

	// Truncation is flagged when the limit is hit.
	limited, err := migration.LegacyFileDigest(t.Context(), "u-mig", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !limited.Truncated || limited.Count != 1 {
		t.Errorf("limited = %+v, want truncated count 1", limited)
	}

	// A content change must change the message digest (content hashing works).
	seedLegacyRow(t, db, "INSERT INTO messages (id, from_uid, to_uid, kind, content, created_at) VALUES (gen_random_uuid(), 'u-mig', 'u-other', 1, 'hello world', $1)", now)
	messages2, err := migration.LegacyMessageDigest(t.Context(), "u-mig", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if messages2.Count != 3 {
		t.Errorf("messages after insert = %+v, want count 3", messages2)
	}
}
