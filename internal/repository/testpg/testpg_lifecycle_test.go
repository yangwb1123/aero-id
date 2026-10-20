//go:build integration

package testpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

type recordingTerminator struct {
	events       *[]string
	ctx          context.Context
	err          error
	terminateErr error
}

func (r *recordingTerminator) Terminate(ctx context.Context) error {
	*r.events = append(*r.events, "terminate")
	r.ctx = ctx
	r.err = ctx.Err()
	return r.terminateErr
}

func TestCleanupFixtureClosesPoolBeforeContainer(t *testing.T) {
	var events []string
	container := &recordingTerminator{events: &events}
	cleanupFixture(t, container.Terminate, func() error {
		events = append(events, "close")
		return nil
	})

	if want := []string{"close", "terminate"}; !equalStrings(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
	if container.ctx == nil {
		t.Fatal("terminate received no context")
	}
	if _, ok := container.ctx.Deadline(); !ok {
		t.Fatal("terminate context has no deadline")
	}
	if err := container.err; err != nil {
		t.Fatalf("terminate context unexpectedly canceled: %v", err)
	}
}

type cleanupRecorder struct {
	cleanups []func()
	errors   []string
	logs     []string
}

func (r *cleanupRecorder) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }
func (r *cleanupRecorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *cleanupRecorder) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

type ownedFixtureContainer struct {
	testcontainers.Container
	*recordingTerminator
}

func (*ownedFixtureContainer) GetContainerID() string { return "exact-owned-container" }
func (c *ownedFixtureContainer) Terminate(ctx context.Context) error {
	return c.recordingTerminator.Terminate(ctx)
}

func TestPostgresOwnershipRegistersBeforeReadiness(t *testing.T) {
	owner := &cleanupRecorder{}
	var events []string
	container := &ownedFixtureContainer{recordingTerminator: &recordingTerminator{events: &events}}
	poolAvailable := false
	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:16-alpine", LifecycleHooks: []testcontainers.ContainerLifecycleHooks{{}},
		},
	}
	err := postgresOwnership(owner, func() error {
		if !poolAvailable {
			t.Error("cleanup captured the pool before setup completed")
		}
		events = append(events, "close")
		return nil
	}).Customize(&req)
	if err != nil || len(req.LifecycleHooks) != 2 || req.Image != "postgres:16-alpine" {
		t.Fatalf("ownership altered the fixture request: %+v, %v", req, err)
	}
	setupCtx, cancelSetup := context.WithCancel(context.Background())
	cancelSetup()
	if err := req.LifecycleHooks[1].PostCreates[0](setupCtx, container); err != nil {
		t.Fatal(err)
	}
	if len(owner.cleanups) != 1 {
		t.Fatal("owned container was not registered before startup/readiness")
	}
	poolAvailable = true
	owner.cleanups[0]()
	assertCleanupDeadline(t, container.recordingTerminator)
	if len(owner.errors) != 0 || !equalStrings(events, []string{"close", "terminate"}) {
		t.Fatalf("cleanup errors=%v, events=%v", owner.errors, events)
	}
}

func TestCleanupFixtureReportsBothErrorsAndStillTerminates(t *testing.T) {
	owner := &cleanupRecorder{}
	var events []string
	container := &recordingTerminator{events: &events, terminateErr: errors.New("delete failed")}
	cleanupFixture(owner, container.Terminate, func() error {
		events = append(events, "close")
		return errors.New("pool failed")
	})
	assertCleanupDeadline(t, container)
	if !equalStrings(events, []string{"close", "terminate"}) || len(owner.errors) != 2 {
		t.Fatalf("errors=%v, events=%v", owner.errors, events)
	}
	for _, phase := range []string{"close_pool", "terminate"} {
		if !strings.Contains(strings.Join(owner.logs, "\n"), "phase="+phase) {
			t.Fatalf("missing diagnostic phase %s: %v", phase, owner.logs)
		}
	}
	if strings.Count(strings.Join(owner.logs, "\n"), "outcome=failed") != 2 {
		t.Fatal("cleanup failures were logged as success")
	}
}

func assertCleanupDeadline(t *testing.T, container *recordingTerminator) {
	t.Helper()
	deadline, ok := container.ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 0 || remaining > postgresFixtureTimeout || container.err != nil {
		t.Fatalf("cleanup deadline=%v, context error=%v", deadline, container.err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
