package migrate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseDownArg(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", 1, false},
		{"  ", 1, false},
		{"1", 1, false},
		{"3", 3, false},
		{" 5 ", 5, false},
		{"0", 0, true},
		{"-2", 0, true},
		{"abc", 0, true},
		{"1.5", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseDownArg(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDownArg(%q): expected error, got %d", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDownArg(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDownArg(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseFilename(t *testing.T) {
	tests := []struct {
		name    string
		version uint64
		migName string
		dir     string
		ok      bool
	}{
		{"000001_create_files.up.sql", 1, "create_files", "up", true},
		{"000009_create_notifications.down.sql", 9, "create_notifications", "down", true},
		{"42_thing.up.sql", 42, "thing", "up", true},
		{"000001_create_files.sql", 0, "", "", false},
		{"README.md", 0, "", "", false},
		{"_no_version.up.sql", 0, "", "", false},
		{"abc_bad.up.sql", 0, "", "", false},
		{"000001.up.sql", 0, "", "", false}, // no underscore separator
	}
	for _, tt := range tests {
		v, name, dir, ok := parseFilename(tt.name)
		if ok != tt.ok {
			t.Errorf("parseFilename(%q): ok=%v, want %v", tt.name, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if v != tt.version || name != tt.migName || dir != tt.dir {
			t.Errorf("parseFilename(%q) = (%d, %q, %q), want (%d, %q, %q)",
				tt.name, v, name, dir, tt.version, tt.migName, tt.dir)
		}
	}
}

// writeFiles writes the given relative paths (contents are irrelevant to the
// integrity checks) into a fresh temp dir and returns the dir.
func writeFiles(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("SELECT 1;"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscover(t *testing.T) {
	dir := writeFiles(t,
		"000002_b.up.sql", "000002_b.down.sql",
		"000001_a.up.sql", "000001_a.down.sql",
		"notes.txt", // ignored
	)

	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("expected 2 migrations, got %d: %+v", len(migs), migs)
	}
	// Sorted ascending by version.
	if migs[0].Version != 1 || migs[1].Version != 2 {
		t.Fatalf("unexpected order: %d, %d", migs[0].Version, migs[1].Version)
	}
	for _, mig := range migs {
		if mig.UpFile == "" || mig.DownFile == "" {
			t.Errorf("migration %d missing files: %+v", mig.Version, mig)
		}
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestDiscoverDuplicateVersion(t *testing.T) {
	// Two up files (and two down files) for the same version: the silent
	// map-merge must error and name BOTH files.
	dir := writeFiles(t,
		"000060_x.up.sql", "000060_x.down.sql",
		"000060_y.up.sql", "000060_y.down.sql",
	)
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("expected duplicate-version error, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "duplicate migration version 000060") {
		t.Errorf("error %q missing version class", errStr)
	}
	// Both files of the duplicate pair (in whichever direction readdir hits
	// first) must be named — the map-merge pin.
	if strings.Count(errStr, ".up.sql") < 2 && strings.Count(errStr, ".down.sql") < 2 {
		t.Errorf("error %q must name both files of one duplicate direction", errStr)
	}

	// Same-direction-only duplicate: two ups, no down for 60. This is the
	// exact evidence example — the error names both .up.sql files.
	dir = writeFiles(t,
		"000001_a.up.sql", "000001_a.down.sql",
		"000060_x.up.sql", "000060_y.up.sql",
	)
	_, err = Discover(dir)
	if err == nil {
		t.Fatal("expected duplicate-version error for same-direction duplicate, got nil")
	}
	for _, want := range []string{"duplicate migration version 000060", "000060_x.up.sql", "000060_y.up.sql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestDiscoverUnpaired(t *testing.T) {
	dir := writeFiles(t, "000060_x.up.sql")
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("expected unpaired (up-only) error, got nil")
	}
	for _, want := range []string{"000060 has no .down.sql file", "000060_x.up.sql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	dir = writeFiles(t, "000060_x.down.sql")
	_, err = Discover(dir)
	if err == nil {
		t.Fatal("expected unpaired (down-only) error, got nil")
	}
	if !strings.Contains(err.Error(), "000060 has no .up.sql file") {
		t.Errorf("error %q missing pair-invariant class", err)
	}
}

func TestDiscoverNameMismatch(t *testing.T) {
	dir := writeFiles(t, "000060_x.up.sql", "000060_y.down.sql")
	_, err := Discover(dir)
	if err == nil {
		t.Fatal("expected name-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), `migration 000060 name mismatch: up name "x" vs down name "y"`) {
		t.Errorf("error %q missing name-mismatch class", err)
	}
}

func TestDiscoverMalformed(t *testing.T) {
	// Each malformed file alone must error as malformed. 42_thing.up.sql parses
	// (permissive parseFilename) but fails the canonical zero-padded form — the
	// name-shape predicate (shared with the bash gate) still rejects it.
	for _, bad := range []string{"000060_x.UP.sql", "000060_x.sql", "abc_bad.up.sql", "42_thing.up.sql"} {
		dir := writeFiles(t, bad)
		_, err := Discover(dir)
		if err == nil {
			t.Errorf("Discover with %q: expected malformed-filename error, got nil", bad)
			continue
		}
		if !strings.Contains(err.Error(), "malformed migration filename") {
			t.Errorf("Discover with %q: error %q missing malformed-filename class", bad, err)
		}
	}
}

func TestDiscoverIgnoresNonMigration(t *testing.T) {
	dir := writeFiles(t,
		"000001_a.up.sql", "000001_a.down.sql",
		"000002_b.up.sql", "000002_b.down.sql",
		"README.md", "notes.txt", "migrations_test.go", ".gitkeep",
	)
	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(migs) != 2 || migs[0].Version != 1 || migs[1].Version != 2 {
		t.Fatalf("expected exactly migrations 1 and 2, got %+v", migs)
	}
}

// TestUpUnpaired pins the fail-before-mutate contract (REQ-2): Up validates the
// migration set before any database access, so a broken set errors with a nil
// DB and never dereferences it.
func TestUpUnpaired(t *testing.T) {
	dir := writeFiles(t, "000060_x.up.sql")
	if _, err := New(nil, dir, nil).Up(); err == nil {
		t.Fatal("expected Up error on unpaired migration with nil DB, got nil")
	} else if !strings.Contains(err.Error(), "has no .down.sql file") {
		t.Fatalf("Up error %q missing pair-invariant class", err)
	}
}

func TestValidateSet(t *testing.T) {
	pairs := []Migration{
		{Version: 1, Name: "a", UpFile: "1.up.sql", DownFile: "1.down.sql"},
		{Version: 2, Name: "b", UpFile: "2.up.sql", DownFile: "2.down.sql"},
		{Version: 3, Name: "c", UpFile: "3.up.sql", DownFile: "3.down.sql"},
	}

	// All applied: no pending, no drift.
	res := ValidateSet(pairs, map[uint64]bool{1: true, 2: true, 3: true})
	if res.Pairs != 3 || len(res.Pending) != 0 || len(res.AppliedNoFiles) != 0 {
		t.Errorf("all-applied: got %+v", res)
	}

	// Some pending, one applied version with no files on disk.
	res = ValidateSet(pairs, map[uint64]bool{1: true, 5: true})
	if res.Pairs != 3 {
		t.Errorf("Pairs = %d, want 3", res.Pairs)
	}
	if len(res.Pending) != 2 || res.Pending[0] != 2 || res.Pending[1] != 3 {
		t.Errorf("Pending = %v, want [2 3]", res.Pending)
	}
	if len(res.AppliedNoFiles) != 1 || res.AppliedNoFiles[0] != 5 {
		t.Errorf("AppliedNoFiles = %v, want [5]", res.AppliedNoFiles)
	}

	// Empty applied: everything pending.
	res = ValidateSet(pairs, nil)
	if res.Pairs != 3 || len(res.Pending) != 3 || len(res.AppliedNoFiles) != 0 {
		t.Errorf("empty applied: got %+v", res)
	}

	// Empty on-disk set with an applied version: drift only.
	res = ValidateSet(nil, map[uint64]bool{1: true})
	if res.Pairs != 0 || len(res.Pending) != 0 || len(res.AppliedNoFiles) != 1 {
		t.Errorf("empty set: got %+v", res)
	}

	// Synthetic incomplete migration (missing down): Pairs counts only
	// complete up/down pairs; Pending still lists the version.
	res = ValidateSet([]Migration{{Version: 9, Name: "x", UpFile: "9.up.sql"}}, nil)
	if res.Pairs != 0 || len(res.Pending) != 1 || res.Pending[0] != 9 {
		t.Errorf("incomplete pair: got %+v", res)
	}
}

// TestDiscoverFixtureLiveness pins the bash gate's self-test fixtures from the
// Go side (REQ-7): make test must also fail if a positive fixture stops
// reddening (or a fixture tree is deleted).
func TestDiscoverFixtureLiveness(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	for _, cls := range []string{"duplicate", "unpaired"} {
		dir := filepath.Join(root, "scripts", "testdata", "migration-set", "positive", cls, "migrations")
		if _, err := Discover(dir); err == nil {
			t.Errorf("fixture %s: expected Discover error, got nil", cls)
		}
	}
	// The negative fixture must stay clean.
	dir := filepath.Join(root, "scripts", "testdata", "migration-set", "negative", "clean", "migrations")
	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("clean fixture: Discover: %v", err)
	}
	if len(migs) != 2 || migs[0].Version != 1 || migs[1].Version != 2 {
		t.Errorf("clean fixture: expected migrations 1 and 2, got %+v", migs)
	}
}
