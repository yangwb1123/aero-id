//go:build integration

package repository_test

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/service"
)

func seedLegacyRow(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
}

func newTestAccountService(t *testing.T, db *gorm.DB) service.AccountService {
	t.Helper()
	return service.NewAccountService(repository.NewAccountRepository(db),
		service.WithAccountIssuer("snaplink"),
		service.WithAccountRegion("local"))
}

// TestLegacyUIDBackfillScan exercises the P1-07 union scan against a real
// PostgreSQL schema: distinct legacy uids are discovered, already-mapped uids
// are excluded, and shadow identities are created without touching legacy rows.
func TestLegacyUIDBackfillScan(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	accounts := repository.NewAccountRepository(db)
	backfill, ok := accounts.(repository.LegacyBackfillRepository)
	if !ok {
		t.Fatal("AccountRepository must implement LegacyBackfillRepository")
	}

	now := time.Now().UTC()
	likeCommentID := "11111111-1111-4111-8111-111111111111"
	reportCommentID := "22222222-2222-4222-8222-222222222222"
	seedLegacyRow(t, db, "INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'a.txt', 1, 'text/plain', 'k', 'local', $2, $2)", "u-files", now)
	seedLegacyRow(t, db, "INSERT INTO messages (id, from_uid, to_uid, kind, content, created_at) VALUES (gen_random_uuid(), $1, $2, 1, 'hi', $3)", "u-from", "u-to", now)
	seedLegacyRow(t, db, "INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'file', gen_random_uuid(), 'c', $2, $2)", "u-comments", now)
	seedLegacyRow(t, db, "INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at) VALUES ($1, 'u-like-target', 'file', gen_random_uuid(), 'like me', $2, $2)", likeCommentID, now)
	seedLegacyRow(t, db, "INSERT INTO comments (id, owner_uid, target_type, target_id, content, created_at, updated_at) VALUES ($1, 'u-report-target', 'file', gen_random_uuid(), 'report me', $2, $2)", reportCommentID, now)
	seedLegacyRow(t, db, "INSERT INTO file_shares (file_id, grantee_uid, owner_uid) VALUES (gen_random_uuid(), $1, $2)", "u-grantee", "u-share-owner")
	seedLegacyRow(t, db, "INSERT INTO comment_likes (comment_id, uid, created_at) VALUES ($1, $2, $3)", likeCommentID, "u-liker", now)
	seedLegacyRow(t, db, "INSERT INTO comment_reports (id, comment_id, reporter_uid, reason, created_at) VALUES (gen_random_uuid(), $1, $2, 'spam', $3)", reportCommentID, "u-reporter", now)
	seedLegacyRow(t, db, "INSERT INTO user_blocks (blocker_uid, blocked_uid, created_at) VALUES ($1, $2, $3)", "u-blocker", "u-blocked", now)

	// uid appearing in several tables must be returned once.
	if _, err := accounts.EnsureByIdentity(t.Context(), "legacy", "snaplink", "u-files", "u-files", "local"); err != nil {
		t.Fatal(err)
	}

	uids, err := backfill.ListLegacyUIDs(t.Context(), "legacy", "snaplink", 1000)
	if err != nil {
		t.Fatalf("ListLegacyUIDs: %v", err)
	}
	// u-files is excluded (already mapped); every other distinct uid remains,
	// including the owners of the like/report target comments.
	want := map[string]bool{"u-from": true, "u-to": true, "u-comments": true, "u-grantee": true,
		"u-share-owner": true, "u-liker": true, "u-reporter": true, "u-blocker": true, "u-blocked": true,
		"u-like-target": true, "u-report-target": true}
	if len(uids) != len(want) {
		t.Fatalf("got %d uids %v, want %d", len(uids), uids, len(want))
	}
	for _, uid := range uids {
		if !want[uid] {
			t.Errorf("unexpected uid %q", uid)
		}
		delete(want, uid)
	}
	if len(want) != 0 {
		t.Errorf("missing uids: %v", want)
	}

	// Tenant/issuer scoping: different tenant or issuer sees the same rows,
	// but a different issuer must not exclude uids mapped under snaplink.
	otherIssuer, err := backfill.ListLegacyUIDs(t.Context(), "legacy", "other-issuer", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherIssuer) != len(uids)+1 { // +1 because u-files is not mapped under other-issuer
		t.Errorf("other-issuer scan = %d uids, want %d", len(otherIssuer), len(uids)+1)
	}

	total, err := backfill.CountLegacyUIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len(uids))+1 { // all distinct uids including u-files
		t.Errorf("count = %d, want %d", total, len(uids)+1)
	}

	// Shadow backfill via the service path: creates identities, no legacy writes.
	svc := newTestAccountService(t, db)
	result, err := svc.BackfillLegacyIdentities(t.Context(), 1000)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if result.Created != int64(len(uids)) {
		t.Errorf("created = %d, want %d", result.Created, len(uids))
	}
	if !result.Completed {
		t.Error("completed = false, want true")
	}
	// Second run: the union scan already excludes mapped uids, so nothing to do
	// (idempotent; "existing" only counts uids still returned by the scan).
	second, err := svc.BackfillLegacyIdentities(t.Context(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 0 || second.Created != 0 || !second.Completed {
		t.Errorf("second run scanned/created/completed = %d/%d/%v, want 0/0/true", second.Scanned, second.Created, second.Completed)
	}
}
