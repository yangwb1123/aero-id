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

// auditGovernanceActivationGateError is the byte-identical documented gate
// error (internal/config/validate.go validateAuditGovernance; parity const
// auditGovernanceActivationGateError in internal/app/audit.go) that
// cmd/server/main.go prints after "load config: ".
const auditGovernanceActivationGateError = "audit_governance.enabled requires at least one tenant binding (activation gate)"

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

// scrubAuditGovernanceEnv strips ambient AERO_ID_AUDIT_GOVERNANCE_* vars from
// a child environment so the subprocess leg is deterministic (CI may carry
// governance env from another job).
func scrubAuditGovernanceEnv(env []string) []string {
	kept := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "AERO_ID_AUDIT_GOVERNANCE_") {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// buildServerBinary builds the real cmd/server binary into a temp dir and
// returns its path.
func buildServerBinary(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aero-id-server")
	build := exec.Command("go", "build", "-o", bin, "./cmd/server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/server: %v\n%s", err, out)
	}
	return bin
}

// TestServerBinaryActivationGateBoot (REQ-3, T-5): builds the real cmd/server
// binary and boots it against the shipped configs/config.yaml with the relay
// env-enabled (zero bindings). The activation gate must fail the boot at
// config.Load: exit 1 with stderr exactly the documented "load config: <gate
// error>" line and nothing else — no logger output, no DB/app wiring, no boot
// side effects (the gate precedes logger.New and app.New in main).
func TestServerBinaryActivationGateBoot(t *testing.T) {
	root := repoRoot(t)
	bin := buildServerBinary(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-config", filepath.Join(root, "configs", "config.yaml"))
	cmd.Env = append(scrubAuditGovernanceEnv(os.Environ()), "AERO_ID_AUDIT_GOVERNANCE_ENABLED=true")
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("binary err = %v, want *exec.ExitError (exit 1 at Load); output:\n%s", err, out)
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Fatalf("binary exit code = %d, want 1; output:\n%s", code, out)
	}
	want := "load config: " + auditGovernanceActivationGateError + "\n"
	if got := string(out); got != want {
		t.Fatalf("stderr = %q, want byte-identical %q (any extra line is a boot side effect)", got, want)
	}
}
