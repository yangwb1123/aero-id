package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repoRoot walks up from the test binary's CWD (the package directory under
// `go test`) to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatalf("repo root not found from %s", dir)
	return ""
}

// buildMigrateBinary builds the real cmd/migrate binary into a temp dir and
// returns its path.
func buildMigrateBinary(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aero-id-migrate")
	build := exec.Command("go", "build", "-o", bin, "./cmd/migrate")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/migrate: %v\n%s", err, out)
	}
	return bin
}

// TestMigrateMissingCommand pins that an empty CLI fails before config
// loading: with a nonexistent -config the same "a command is required" error
// must surface (a config-load failure instead would prove the ordering
// regressed — missing-command must precede config.Load).
func TestMigrateMissingCommand(t *testing.T) {
	want := "a command is required\n\n" + usage()
	if err := run(nil); err == nil || err.Error() != want {
		t.Fatalf("run(nil) err = %v, want %q", err, want)
	}
	err := run([]string{"-config", filepath.Join(t.TempDir(), "missing-migrate.yaml")})
	if err == nil || err.Error() != want {
		t.Fatalf("run(-config missing) err = %v, want %q (missing command must precede config.Load)", err, want)
	}
}

// TestMigrateUnknownCommand pins that the command whitelist is enforced before
// config loading: a nonexistent -config must not be reached for an unknown
// command (pre-refactor, this returned the config error instead).
func TestMigrateUnknownCommand(t *testing.T) {
	want := "unknown command \"frobnicate\"\n\n" + usage()
	err := run([]string{"-config", filepath.Join(t.TempDir(), "missing-migrate.yaml"), "frobnicate"})
	if err == nil || err.Error() != want {
		t.Fatalf("run(... frobnicate) err = %v, want %q (whitelist must precede config.Load)", err, want)
	}
}

// TestMigrateDownArgValidationAtCLI pins that the down count is validated at
// the CLI boundary before config loading, and that a valid count proceeds past
// pre-flight into config.Load (proving the pre-flight ordering and that
// ParseDownArg unit coverage is not duplicated here).
func TestMigrateDownArgValidationAtCLI(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-migrate.yaml")

	for _, tt := range []struct {
		args []string
		want string
	}{
		{
			args: []string{"-config", missing, "down", "0"},
			want: "invalid down count 0: must be >= 1",
		},
		{
			args: []string{"-config", missing, "down", "abc"},
			want: `invalid down count "abc": must be a positive integer`,
		},
	} {
		err := run(tt.args)
		if err == nil || err.Error() != tt.want {
			t.Errorf("run(%q) err = %v, want %q", tt.args, err, tt.want)
		}
	}

	// A valid down count proceeds past pre-flight into config.Load; the
	// error must be the config one, not a CLI validation error.
	err := run([]string{"-config", missing, "down", "3"})
	if err == nil || !strings.HasPrefix(err.Error(), "load config: read config: ") {
		t.Fatalf("run(... down 3) err = %v, want prefix %q", err, "load config: read config: ")
	}
}

// TestMigrateConfigLoadFailure builds the real cmd/migrate binary and pins the
// byte-exact stderr + exit 1 for a missing config file ("any extra line is a
// boot side effect", mirroring cmd/server's activation-gate boot test). All
// Docker-free: the failure happens before db.Open.
func TestMigrateConfigLoadFailure(t *testing.T) {
	root := repoRoot(t)
	bin := buildMigrateBinary(t, root)

	missing := filepath.Join(t.TempDir(), "missing-migrate.yaml")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-config", missing, "up")
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("binary err = %v, want *exec.ExitError (exit 1 at Load); output:\n%s", err, out)
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Fatalf("binary exit code = %d, want 1; output:\n%s", code, out)
	}
	want := "migrate: load config: read config: open " + missing + ": no such file or directory\n"
	if got := string(out); got != want {
		t.Fatalf("stderr = %q, want byte-identical %q (any extra line is a boot side effect)", got, want)
	}
}

func TestPreflightAcceptsVerify(t *testing.T) {
	cmd, n, err := preflight([]string{"verify"})
	if err != nil {
		t.Fatalf("preflight verify: %v", err)
	}
	if cmd != "verify" || n != 1 {
		t.Fatalf("preflight = (%q, %d), want (verify, 1)", cmd, n)
	}
}
