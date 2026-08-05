package migrate

import (
	"os"
	"path/filepath"
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

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"000002_b.up.sql", "000002_b.down.sql",
		"000001_a.up.sql", "000001_a.down.sql",
		"000003_c.up.sql", // intentionally missing its down
		"notes.txt",       // ignored
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("SELECT 1;"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(migs) != 3 {
		t.Fatalf("expected 3 migrations, got %d: %+v", len(migs), migs)
	}
	// Sorted ascending by version.
	if migs[0].Version != 1 || migs[1].Version != 2 || migs[2].Version != 3 {
		t.Fatalf("unexpected order: %d, %d, %d", migs[0].Version, migs[1].Version, migs[2].Version)
	}
	if migs[0].UpFile == "" || migs[0].DownFile == "" {
		t.Errorf("migration 1 missing files: %+v", migs[0])
	}
	if migs[2].UpFile == "" {
		t.Errorf("migration 3 should have an up file: %+v", migs[2])
	}
	if migs[2].DownFile != "" {
		t.Errorf("migration 3 should have no down file: %+v", migs[2])
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing dir")
	}
}
