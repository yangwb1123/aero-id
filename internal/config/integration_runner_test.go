package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type integrationRunnerProbe struct {
	args        []string
	environment []string
	timeouts    []string
	output      string
	err         error
}

func TestIntegrationRunnerCompleteCommandsAndBudgets(t *testing.T) {
	for _, target := range []string{"test-integration", "test-integration-bounded"} {
		t.Run(target, func(t *testing.T) {
			probe := probeIntegrationRunner(t, target, "0", "0")
			if probe.err != nil {
				t.Fatalf("Make recipe failed: %v: %s", probe.err, probe.output)
			}
			want := []string{"test", "-tags=integration", "-p", "1", "-timeout=15m", "./...", "-count=1"}
			if target == "test-integration-bounded" {
				want = []string{"test", "-mod=readonly", "-json", "-tags=integration", "-p", "2", "-timeout=15m", "./...", "-count=1"}
				if !reflect.DeepEqual(probe.environment, []string{"GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local"}) {
					t.Fatalf("ambient filters/workspace/toolchain survived: %v", probe.environment)
				}
			}
			if !reflect.DeepEqual(probe.args, want) {
				t.Fatalf("full-suite arguments = %v, want %v", probe.args, want)
			}
			if !strings.Contains(strings.Join(probe.timeouts, "\n"), "--foreground --kill-after=30s 20m go test") {
				t.Fatalf("original whole-process budget changed: %v", probe.timeouts)
			}
		})
	}
}

func TestIntegrationRunnerPropagatesFailureAndDoesNotRetry(t *testing.T) {
	for _, target := range []string{"test-integration", "test-integration-bounded"} {
		t.Run(target, func(t *testing.T) {
			probe := probeIntegrationRunner(t, target, "23", "0")
			var exit *exec.ExitError
			if !errors.As(probe.err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("failed go test was hidden: err=%v, output=%s", probe.err, probe.output)
			}
			if len(probe.args) == 0 || strings.Count(strings.Join(probe.timeouts, "\n"), "go test") != 1 {
				t.Fatalf("failed full command was skipped or retried: %+v", probe)
			}
		})
	}
}

func TestIntegrationRunnerRejectsUnavailableDockerBeforeTests(t *testing.T) {
	probe := probeIntegrationRunner(t, "test-integration-bounded", "0", "19")
	if probe.err == nil || len(probe.args) != 0 || !strings.Contains(probe.output, "Docker daemon is unavailable") {
		t.Fatalf("Docker denial did not stop admission: %+v", probe)
	}
	want := []string{"--foreground --kill-after=5s 30s docker info"}
	if !reflect.DeepEqual(probe.timeouts, want) {
		t.Fatalf("Docker readiness is not independently bounded: %v", probe.timeouts)
	}
}

// These commands only record argv/environment and return requested statuses.
// The real timeout binary and real project Makefile are used; no Docker daemon,
// Go tests, source endpoint or image is substituted in an acceptance invocation.
func probeIntegrationRunner(t *testing.T, target, goStatus, dockerStatus string) integrationRunnerProbe {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	timeout, err := exec.LookPath("timeout")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeIntegrationProbeCommands(t, bin)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := integrationProbeCommand(t, ctx, target)
	cmd.Dir = root
	cmd.Env = integrationProbeEnvironment(bin, timeout, goStatus, dockerStatus)
	out, runErr := cmd.CombinedOutput()
	return integrationRunnerProbe{
		args:        readIntegrationProbeLines(t, filepath.Join(bin, "go-args")),
		environment: readIntegrationProbeLines(t, filepath.Join(bin, "go-env")),
		timeouts:    readIntegrationProbeLines(t, filepath.Join(bin, "timeouts")), output: string(out), err: runErr,
	}
}

func integrationProbeCommand(t *testing.T, ctx context.Context, target string) *exec.Cmd {
	t.Helper()
	switch target {
	case "test-integration":
		return exec.CommandContext(ctx, "make", "--no-print-directory", "-f", "Makefile", "test-integration")
	case "test-integration-bounded":
		return exec.CommandContext(ctx, "make", "--no-print-directory", "-f", "Makefile", "test-integration-bounded")
	default:
		t.Fatal("unsupported integration probe target")
		return nil
	}
}

func integrationProbeEnvironment(bin, timeout, goStatus, dockerStatus string) []string {
	// Do not inherit MAKEFLAGS/MAKEFILES/MAKEOVERRIDES or parent Go filters:
	// no ambient make options may turn the probe into a dry-run or another file.
	return []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + bin, "LC_ALL=C",
		"PROBE_DIR=" + bin, "PROBE_TIMEOUT=" + timeout,
		"PROBE_GO_STATUS=" + goStatus, "PROBE_DOCKER_STATUS=" + dockerStatus,
		"GOFLAGS=-run=TestMustNotRestrictFullSuite -skip=TestMustNotSkip -count=7 -json",
		"GOWORK=/must-not-use-an-ambient-workspace", "GOTOOLCHAIN=must-not-download-a-toolchain",
	}
}

func writeIntegrationProbeCommands(t *testing.T, dir string) {
	t.Helper()
	commands := map[string]string{
		"go": `#!/bin/bash
printf '%s\n' "$@" >> "$PROBE_DIR/go-args"
printf 'GOFLAGS=%s\nGOWORK=%s\nGOTOOLCHAIN=%s\n' "$GOFLAGS" "$GOWORK" "$GOTOOLCHAIN" > "$PROBE_DIR/go-env"
exit "$PROBE_GO_STATUS"
`,
		"docker": `#!/bin/bash
[[ "$*" == "info" ]] || exit 91
exit "$PROBE_DOCKER_STATUS"
`,
		"timeout": `#!/bin/bash
printf '%s\n' "$*" >> "$PROBE_DIR/timeouts"
exec "$PROBE_TIMEOUT" "$@"
`,
	}
	for name, script := range commands {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func readIntegrationProbeLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}
